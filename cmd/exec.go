package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bincooo/MiaoX"
	"github.com/bincooo/MiaoX/types"
	"github.com/bincooo/MiaoX/vars"
	clTypes "github.com/bincooo/claude-api/types"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	manager = MiaoX.NewBotManager()
	proxy   string
	port    int
)

const (
	H = "H:"
	A = "A:"
	S = "System:"
)

type rj struct {
	Prompt        string   `json:"prompt"`
	Model         string   `json:"model"`
	MaxTokens     int      `json:"max_tokens_to_sample"`
	StopSequences []string `json:"stop_sequences"`
	Temperature   float32  `json:"temperature"`
	TopP          float32  `json:"top_p"`
	TopK          float32  `json:"top_k"`
	Stream        bool     `json:"stream"`
}

func main() {
	Exec()
}

func Exec() {
	types.CacheWaitTimeout = 1500 * time.Millisecond
	types.CacheMessageL = 20

	var rootCmd = &cobra.Command{
		Use:   "MiaoX",
		Short: "MiaoX控制台工具",
		Long:  "MiaoX是集成了多款AI接口的控制台工具",
		Run:   Run,
	}

	rootCmd.Flags().StringVarP(&proxy, "proxy", "P", "", "本地代理")
	rootCmd.Flags().IntVarP(&port, "port", "p", 8080, "服务端口")
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func Run(cmd *cobra.Command, args []string) {
	gin.SetMode(gin.ReleaseMode)
	route := gin.Default()

	route.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		//c.Writer.Header().Set("Transfer-Encoding", "chunked")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Next()
	})

	route.POST("/v1/complete", complete)
	addr := ":" + strconv.Itoa(port)
	fmt.Println("Start by http://127.0.0.1" + addr + "/v1")
	if err := route.Run(addr); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func complete(ctx *gin.Context) {
	var r rj

	token := ctx.Request.Header.Get("X-Api-Key")
	if err := ctx.BindJSON(&r); err != nil {
		responseError(ctx, err, r.Stream)
		return
	}

	fmt.Println("-----------------------请求报文-----------------\n", r, "\n--------------------END-------------------")

	IsClose := false
	context, err := createConversationContext(token, &r, func() bool { return IsClose })
	if err != nil {
		responseError(ctx, err, r.Stream)
		return
	}
	partialResponse := manager.Reply(*context, func(response types.PartialResponse) {
		if r.Stream {
			if response.Status == vars.Begin {
				ctx.Status(200)
				ctx.Header("Content-Type", "text/event-stream; charset=utf-8")
				ctx.Writer.Flush()
				return
			}

			if response.Error != nil {
				responseError(ctx, response.Error, r.Stream)
				return
			}

			if len(response.Message) > 0 {
				select {
				case <-ctx.Request.Context().Done():
					IsClose = true
				default:
					if !writeString(ctx, response.Message) {
						IsClose = true
					}
				}
			}

			if response.Status == vars.Closed {
				writeDone(ctx)
			}
		} else {
			select {
			case <-ctx.Request.Context().Done():
				IsClose = true
			default:
			}
		}
	})

	if !r.Stream && !IsClose {
		if partialResponse.Error != nil {
			responseError(ctx, partialResponse.Error, r.Stream)
			return
		}
		ctx.JSON(200, gin.H{
			"completion": partialResponse.Message,
		})
	}
}

func Handle(IsC func() bool) func(rChan any) func(*types.CacheBuffer) error {
	return func(rChan any) func(*types.CacheBuffer) error {
		pos := 0
		begin := false
		beginIndex := -1
		partialResponse := rChan.(chan clTypes.PartialResponse)
		return func(self *types.CacheBuffer) error {
			response, ok := <-partialResponse
			if !ok {
				// 清理一下残留
				self.Cache = strings.TrimSuffix(self.Cache, A)
				self.Cache = strings.TrimSuffix(self.Cache, S)
				self.Closed = true
				return nil
			}

			if IsC() {
				self.Closed = true
				return nil
			}

			if response.Error != nil {
				self.Closed = true
				return response.Error
			}

			text := response.Text
			str := []rune(text)
			self.Cache += string(str[pos:])
			pos = len(str)

			mergeMessage := self.Complete + self.Cache
			// 遇到“A:” 或者积累200字就假定是正常输出
			if index := strings.Index(mergeMessage, A); index > -1 {
				if !begin {
					begin = true
					beginIndex = index
				}

			} else if !begin && len(mergeMessage) > 200 {
				begin = true
				beginIndex = pos
			}

			if begin {
				// 遇到“H:”就结束接收
				if index := strings.Index(mergeMessage, H); index > -1 && index > beginIndex {
					self.Cache = strings.TrimSuffix(self.Cache, H)
					self.Closed = true
					return nil
				} else if index = strings.Index(mergeMessage, S); index > -1 && index > beginIndex {
					// 遇到“System:”就结束接收
					self.Cache = strings.TrimSuffix(self.Cache, S)
					self.Closed = true
					return nil
				}
			}
			return nil
		}
	}
}

func createConversationContext(token string, r *rj, IsC func() bool) (*types.ConversationContext, error) {
	var (
		bot   string
		model string
	)
	switch r.Model {
	case "claude-2.0":
		bot = vars.Claude
		model = vars.Model4WebClaude2S
	case "claude-1.0", "claude-1.2", "claude-1.3":
		bot = vars.Claude
	default:
		return nil, errors.New("未知/不支持的模型`" + r.Model + "`")
	}

	return &types.ConversationContext{
		Id:     "claude2",
		Token:  token,
		Prompt: r.Prompt,
		Bot:    bot,
		Model:  model,
		Proxy:  proxy,
		H:      Handle(IsC),
	}, nil
}

func responseError(ctx *gin.Context, err error, isStream bool) {
	if isStream {
		marshal, e := json.Marshal(gin.H{
			"completion": "Error: " + err.Error(),
		})
		fmt.Println("Error: ", err)
		if e != nil {
			fmt.Println("Error: ", e)
			return
		}
		ctx.String(200, "data: %s\n\ndata: [DONE]", string(marshal))
	} else {
		ctx.JSON(200, gin.H{
			"completion": "Error: " + err.Error(),
		})
	}
}

func writeString(ctx *gin.Context, content string) bool {
	c := strings.ReplaceAll(strings.ReplaceAll(content, "\n", "\\n"), "\"", "\\\"")
	if _, err := ctx.Writer.Write([]byte("\n\ndata: {\"completion\": \"" + c + "\"}")); err != nil {
		fmt.Println("Error: ", err)
		return false
	} else {
		ctx.Writer.Flush()
		return true
	}
}

func writeDone(ctx *gin.Context) {
	if _, err := ctx.Writer.Write([]byte("\n\ndata: [DONE]")); err != nil {
		fmt.Println("Error: ", err)
	}
}

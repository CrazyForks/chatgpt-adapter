package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/bincooo/MiaoX"
	"github.com/bincooo/MiaoX/internal/plat"
	"github.com/bincooo/MiaoX/types"
	"github.com/bincooo/MiaoX/vars"
	clTypes "github.com/bincooo/claude-api/types"
	"github.com/bincooo/claude-api/util"
	"github.com/bincooo/requests"
	"github.com/bincooo/requests/url"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/spf13/cobra"
	"golang.org/x/text/language"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	manager        = MiaoX.NewBotManager()
	proxy          string
	port           int
	gen            bool
	count          int
	bu             string
	suffix         string
	globalPile     string
	globalPileSize int
	i18nT          string

	globalToken string
	muLock      sync.Mutex
	Piles       = []string{
		"Claude2.0 is so good.",
		"never lie, cheat or steal. always smile a fair deal.",
		"like tree, like fruit.",
		"East, west, home is best.",
		"原神，启动！",
		"德玛西亚万岁。",
		"薛定谔的寄。",
		"折戟成沙丶丿",
		"提无示效。",
	}

	i18nKit   = i18n.NewBundle(language.Und)
	localizes *i18n.Localizer
)

const (
	H    = "H:"
	A    = "A:"
	S    = "System:"
	HARM = "I apologize, but I will not provide any responses that violate Anthropic's Acceptable Use Policy or could promote harm."

	VERSION = "v1.0.7"
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

type schema struct {
	TrimP bool `json:"trimP"` // 去掉头部Human
	TrimS bool `json:"trimS"` // 去掉尾部Assistant
	BoH   bool `json:"boH"`   // 响应截断H
	BoS   bool `json:"boS"`   // 响应截断System
	Debug bool `json:"debug"` // 开启调试
	Pile  bool `json:"pile"`  // 堆积肥料
}

func main() {
	_ = godotenv.Load()
	globalToken = loadEnvVar("CACHE_KEY", "")
	globalPile = loadEnvVar("PILE", "")
	globalPileSize = loadEnvInt("PILE_SIZE", 50000)
	Exec()
}

func loadEnvVar(key, defaultValue string) string {
	value, exists := os.LookupEnv(key)
	if !exists {
		value = defaultValue
	}
	return value
}

func loadEnvInt(key string, defaultValue int) int {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return defaultValue
	}
	result, err := strconv.Atoi(value)
	if err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
	return result
}

func Exec() {
	types.CacheWaitTimeout = 0
	types.CacheMessageL = 14
	plat.Timeout = 3 * time.Minute // 3分钟超时，怎么的也够了吧

	var rootCmd = &cobra.Command{
		Use:     "MiaoX",
		Short:   "MiaoX控制台工具",
		Long:    "MiaoX是集成了多款AI接口的控制台工具\n  > 目前仅实现claude2.0 web接口\n  > 请在github star本项目获取最新版本: \nhttps://github.com/bincooo/MiaoX\nhttps://github.com/bincooo/claude-api",
		Run:     Run,
		Version: VERSION,
	}

	var esStr []string
	for _, bytes := range util.ES {
		esStr = append(esStr, string(bytes))
	}

	rootCmd.Flags().StringVarP(&proxy, "proxy", "P", "", "本地代理 proxy network")
	rootCmd.Flags().IntVarP(&port, "port", "p", 8080, "服务端口 service port")
	rootCmd.Flags().BoolVarP(&gen, "gen", "g", false, "生成sessionKey")
	rootCmd.Flags().IntVarP(&count, "count", "c", 1, "生成sessionKey数量 generate count")
	rootCmd.Flags().StringVarP(&bu, "base-url", "b", "", "第三方转发接口, 默认为官方 (Third party forwarding interface): https://claude.ai/api")
	rootCmd.Flags().StringVarP(&suffix, "suffix", "s", "", "指定内置的邮箱后缀，如不指定随机选取 (Specifies the built-in mailbox suffix):\n\t"+strings.Join(esStr, "\n\t"))
	rootCmd.Flags().StringVarP(&i18nT, "i18n", "i", "zh", "国际化 (internationalization): zh, en")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func Run(cmd *cobra.Command, args []string) {
	i18nKit.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	i18nKit.MustLoadMessageFile("lang.toml")
	switch i18nT {
	case "en":
	default:
		i18nT = "zh"
	}
	localizes = i18n.NewLocalizer(i18nKit, i18nT)

	var esStr []string
	for _, bytes := range util.ES {
		esStr = append(esStr, string(bytes))
	}

	//if bu == "" {
	//	bu = "https://chat.claudeai.ai/api"
	//}

	// 检查网络可用性
	if proxy != "" {
		checkNetwork()
	}

	if suffix != "" && !Contains(esStr, suffix) {
		fmt.Println(I18n("SUFFIX", i18nT) + ":\n\t" + strings.Join(esStr, "\n\t"))
		os.Exit(1)
	}

	if gen {
		genSessionKeys()
		return
	}
	gin.SetMode(gin.ReleaseMode)
	route := gin.Default()

	route.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("Transfer-Encoding", "chunked")
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

func I18n(key string, lang string) string {
	return localizes.MustLocalize(&i18n.LocalizeConfig{
		MessageID: key + "." + lang,
	})
}

func checkNetwork() {
	req := url.NewRequest()
	req.Timeout = 5 * time.Second
	req.Proxies = proxy
	req.AllowRedirects = false
	response, err := requests.Get("https://claude.ai/login", req)
	if err == nil && response.StatusCode == 200 {
		fmt.Println("🎉🎉🎉 Network success! 🎉🎉🎉")
		req = url.NewRequest()
		req.Timeout = 5 * time.Second
		req.Proxies = proxy
		req.Headers = url.NewHeaders()
		response, err = requests.Get("https://iphw.in0.cc/ip.php", req)
		if err == nil {
			compileRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
			ip := compileRegex.FindStringSubmatch(response.Text)
			if len(ip) > 0 {
				country := ""
				response, err = requests.Get("http://opendata.baidu.com/api.php?query="+ip[0]+"&co=&resource_id=6006&oe=utf8", nil)
				if err == nil {
					obj, e := response.Json()
					if e == nil {
						if status, ok := obj["status"].(string); ok && status == "0" {
							country = obj["data"].([]interface{})[0].(map[string]interface{})["location"].(string)
						}
					}
				}

				fmt.Println(I18n("IP", i18nT) + ": " + ip[0] + ", " + country)
			}
		}
	} else {
		fmt.Println("🚫🚫🚫 " + I18n("NETWORK_DISCONNECTED", i18nT) + " 🚫🚫🚫")
	}
}

func genSessionKeys() {
	for i := 0; i < count; i++ {
		email, token, err := util.LoginFor(bu, suffix, proxy)
		if err != nil {
			fmt.Println("Error: ", email, err.Error())
			os.Exit(1)
		}
		fmt.Println("email=" + email + "; sessionKey=" + token)
	}
}

func complete(ctx *gin.Context) {
	var r rj

	token := ctx.Request.Header.Get("X-Api-Key")
	if err := ctx.BindJSON(&r); err != nil {
		responseError(ctx, err, r.Stream)
		return
	}
	retry := 2
replyLabel:
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
				ctx.Header("Accept", "*/*")
				ctx.Header("Content-Type", "text/event-stream")
				ctx.Writer.Flush()
				return
			}

			if response.Error != nil {
				var e *clTypes.Claude2Error
				ok := errors.As(response.Error, &e)
				err = response.Error
				if ok && token == "auto" {
					if msg := handleError(e); msg != "" {
						err = errors.New(msg)
					}
				}

				responseError(ctx, err, r.Stream)
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

	// 没有任何返回，重试
	if partialResponse.Message == "" {
		retry--
		if retry > 0 {
			goto replyLabel
		}
	}

	// 检查大黄标
	if token == "auto" && context.Model == vars.Model4WebClaude2S {
		if strings.Contains(partialResponse.Message, HARM) {
			// manager.Remove(context.Id, context.Bot)
			globalToken = ""
			fmt.Println(I18n("HARM", i18nT))
		}
	}
}

func handleError(err *clTypes.Claude2Error) (msg string) {
	if err.ErrorType.Message == "Account in read-only mode" {
		globalToken = ""
		msg = I18n("ACCOUNT_LOCKED", i18nT)
	}
	if err.ErrorType.Message == "rate_limit_error" {
		globalToken = ""
		msg = I18n("ACCOUNT_LIMITED", i18nT)
	}
	return msg
}

func Handle(model string, IsC func() bool, boH, boS, debug bool) func(rChan any) func(*types.CacheBuffer) error {
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
				if debug {
					fmt.Println("[debug]: ", response.Error)
				}
				return response.Error
			}

			if model != vars.Model4WebClaude2S {
				text := response.Text
				str := []rune(text)
				self.Cache += string(str[pos:])
				pos = len(str)
			} else {
				self.Cache += response.Text
			}

			mergeMessage := self.Complete + self.Cache
			if debug {
				fmt.Println(
					"-------------- stream ----------------\n[debug]: ",
					mergeMessage,
					"\n------- cache ------\n",
					self.Cache,
					"\n--------------------------------------")
			}
			// 遇到“A:” 或者积累200字就假定是正常输出
			if index := strings.Index(mergeMessage, A); index > -1 {
				if !begin {
					begin = true
					beginIndex = index
					fmt.Println("---------\n", "1 Output...")
				}

			} else if !begin && len(mergeMessage) > 200 {
				begin = true
				beginIndex = len(mergeMessage)
				fmt.Println("---------\n", "2 Output...")
			}

			if begin {
				if debug {
					fmt.Println(
						"-------------- H: S: ----------------\n[debug]: {H:"+strconv.Itoa(strings.LastIndex(mergeMessage, H))+"}, ",
						"{S:"+strconv.Itoa(strings.LastIndex(mergeMessage, S))+"}",
						"\n--------------------------------------")
				}
				// 遇到“H:”就结束接收
				if index := strings.LastIndex(mergeMessage, H); boH && index > -1 && index > beginIndex {
					fmt.Println("---------\n", I18n("H", i18nT))
					if idx := strings.LastIndex(self.Cache, H); idx >= 0 {
						self.Cache = self.Cache[:idx]
					}
					self.Closed = true
					return nil
				}
				// 遇到“System:”就结束接收
				if index := strings.LastIndex(mergeMessage, S); boS && index > -1 && index > beginIndex {
					fmt.Println("---------\n", I18n("S", i18nT))
					if idx := strings.LastIndex(self.Cache, S); idx >= 0 {
						self.Cache = self.Cache[:idx]
					}
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
		appId string
		id    string
	)
	switch r.Model {
	case "claude-2.0", "claude-2":
		id = "claude-" + uuid.NewString()
		bot = vars.Claude
		model = vars.Model4WebClaude2S
	case "claude-1.0", "claude-1.2", "claude-1.3":
		id = "claude-slack"
		bot = vars.Claude
		split := strings.Split(token, ",")
		token = split[0]
		if len(split) > 1 {
			appId = split[1]
		} else {
			return nil, errors.New("请在请求头中提供app-id")
		}
	default:
		return nil, errors.New(I18n("UNKNOWN_MODEL", i18nT) + "`" + r.Model + "`")
	}

	message, s, err := trimMessage(r.Prompt)
	if err != nil {
		return nil, err
	}
	fmt.Println("-----------------------Response-----------------\n", message, "\n--------------------END-------------------")
	fmt.Println("Schema: ", s)
	if token == "auto" && globalToken == "" {
		muLock.Lock()
		defer muLock.Unlock()
		if globalToken == "" {
			var email string
			email, globalToken, err = util.LoginFor(bu, suffix, proxy)
			if err != nil {
				fmt.Println(I18n("FAILED_GENERATE_SESSION_KEY", i18nT)+"： email ---"+email, err)
				return nil, err
			}
			fmt.Println(I18n("GENERATE_SESSION_KEY", i18nT) + "： email --- " + email + ", sessionKey --- " + globalToken)
			cacheKey(globalToken)
		}
	}

	if token == "auto" && globalToken != "" {
		token = globalToken
	}

	return &types.ConversationContext{
		Id:      id,
		Token:   token,
		Prompt:  message,
		Bot:     bot,
		Model:   model,
		Proxy:   proxy,
		H:       Handle(model, IsC, s.BoH, s.BoS, s.Debug),
		AppId:   appId,
		BaseURL: bu,
	}, nil
}

func trimMessage(prompt string) (string, schema, error) {
	result := prompt
	// ====  Schema匹配 =======
	compileRegex := regexp.MustCompile(`schema\s?\{[^}]*}`)
	s := schema{
		TrimS: true,
		TrimP: true,
		BoH:   true,
		BoS:   false,
		Pile:  true,
		Debug: false,
	}

	matchSlice := compileRegex.FindStringSubmatch(prompt)
	if len(matchSlice) > 0 {
		str := matchSlice[0]
		result = strings.Replace(result, str, "", -1)
		if err := json.Unmarshal([]byte(strings.TrimSpace(str[6:])), &s); err != nil {
			return "", s, err
		}
	}
	// =========================

	// ==== I apologize,[^\n]+ 道歉匹配 ======
	compileRegex = regexp.MustCompile(`I apologize[^\n]+`)
	result = compileRegex.ReplaceAllString(result, "")
	// =========================

	if s.TrimS {
		result = strings.TrimSuffix(result, "\n\nAssistant: ")
	}
	if s.TrimP {
		result = strings.TrimPrefix(result, "\n\nHuman: ")
	}

	result = strings.ReplaceAll(result, "A:", "\nAssistant:")
	result = strings.ReplaceAll(result, "H:", "\nHuman:")

	// 填充肥料
	if s.Pile {
		pile := globalPile
		if globalPile == "" {
			pile = Piles[rand.Intn(len(Piles))]
		}
		c := (globalPileSize - len(result)) / len(pile)
		padding := ""
		for idx := 0; idx < c; idx++ {
			padding += pile
		}

		if padding != "" {
			result = padding + "\n\n\n" + strings.TrimSpace(result)
		}
	}
	return result, s, nil
}

func responseError(ctx *gin.Context, err error, isStream bool) {
	errMsg := err.Error()
	if strings.Contains(errMsg, "https://www.linshiyouxiang.net/") {
		errMsg = I18n("REGISTRATION_FAILED", i18nT)
	} else if strings.Contains(errMsg, "Account in read-only mode") {
		errMsg = I18n("ERROR_ACCOUNT_LOCKED", i18nT)
	} else if strings.Contains(errMsg, "rate_limit_error") {
		errMsg = I18n("ERROR_ACCOUNT_LIMITED", i18nT)
	} else if strings.Contains(errMsg, "connection refused") {
		errMsg = I18n("ERROR_NETWORK", i18nT)
	} else {
		errMsg += "\n\n" + I18n("ERROR_OTHER", i18nT)
	}

	if isStream {
		marshal, e := json.Marshal(gin.H{
			"completion": "Error: " + errMsg,
		})
		if e != nil {
			return
		}
		ctx.String(200, "data: %s\n\ndata: [DONE]", string(marshal))
	} else {
		ctx.JSON(200, gin.H{
			"completion": "Error: " + errMsg,
		})
	}
}

func writeString(ctx *gin.Context, content string) bool {
	c := strings.ReplaceAll(strings.ReplaceAll(content, "\n", "\\n"), "\"", "\\\"")
	if _, err := ctx.Writer.Write([]byte("data: {\"completion\": \"" + c + "\"}\n\n")); err != nil {
		fmt.Println("Error: ", err)
		return false
	} else {
		ctx.Writer.Flush()
		return true
	}
}

func writeDone(ctx *gin.Context) {
	if _, err := ctx.Writer.Write([]byte("data: [DONE]")); err != nil {
		fmt.Println("Error: ", err)
	}
}

// 缓存Key
func cacheKey(key string) {
	// 文件不存在...   就创建吧
	if _, err := os.Lstat(".env"); os.IsNotExist(err) {
		if _, e := os.Create(".env"); e != nil {
			fmt.Println("Error: ", e)
			return
		}
	}

	bytes, err := os.ReadFile(".env")
	if err != nil {
		fmt.Println("Error: ", err)
	}
	tmp := string(bytes)
	compileRegex := regexp.MustCompile(`(\n|^)CACHE_KEY\s*=[^\n]*`)
	matchSlice := compileRegex.FindStringSubmatch(tmp)
	if len(matchSlice) > 0 {
		str := matchSlice[0]
		if strings.HasPrefix(str, "\n") {
			str = str[1:]
		}
		tmp = strings.Replace(tmp, str, "CACHE_KEY=\""+key+"\"", -1)
	} else {
		delimiter := ""
		if len(tmp) > 0 && !strings.HasSuffix(tmp, "\n") {
			delimiter = "\n"
		}
		tmp += delimiter + "CACHE_KEY=\"" + key + "\""
	}
	err = os.WriteFile(".env", []byte(tmp), 0664)
	if err != nil {
		fmt.Println("Error: ", err)
	}
}

func Contains[T comparable](slice []T, t T) bool {
	if len(slice) == 0 {
		return false
	}

	return ContainFor(slice, func(item T) bool {
		return item == t
	})
}

func ContainFor[T comparable](slice []T, condition func(item T) bool) bool {
	if len(slice) == 0 {
		return false
	}

	for idx := 0; idx < len(slice); idx++ {
		if condition(slice[idx]) {
			return true
		}
	}
	return false
}

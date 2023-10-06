package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bincooo/AutoAI/cmd/util/pool"
	"github.com/bincooo/AutoAI/types"
	"github.com/bincooo/AutoAI/utils"
	"github.com/bincooo/AutoAI/vars"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"sync"

	cmdtypes "github.com/bincooo/AutoAI/cmd/types"
	cmdvars "github.com/bincooo/AutoAI/cmd/vars"
	cltypes "github.com/bincooo/claude-api/types"
)

var (
	muLock sync.Mutex

	HARM = "I apologize, but I will not provide any responses that violate Anthropic's Acceptable Use Policy or could promote harm."

	H = "H:"
	A = "A:"
	S = "System:"

	piles = []string{
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
)

type schema struct {
	Debug     bool `json:"debug"`     // 开启调试
	TrimP     bool `json:"trimP"`     // 去掉头部Human
	TrimS     bool `json:"trimS"`     // 去掉尾部Assistant
	BoH       bool `json:"boH"`       // 响应截断H
	BoS       bool `json:"boS"`       // 响应截断System
	Pile      bool `json:"pile"`      // 堆积肥料
	FullColon bool `json:"fullColon"` // 全角冒号
	TrimPlot  bool `json:"trimPlot"`  // slot xml删除处理
}

func DoClaudeComplete(ctx *gin.Context, token string, r *cmdtypes.RequestDTO, wd bool) {
	IsClose := false
	IsDone := false
	fmt.Println("TOKEN_KEY: " + token)
	prepare(ctx, r)
	// 重试次数
	retry := 2

	var err error
	var context *types.ConversationContext
label:
	if IsDone {
		if err != nil {
			_ = catchClaudeHandleError(err, token)
		}
		return
	}

	context, err = createClaudeConversation(token, r, func() bool { return IsClose })
	if err != nil {
		errorMessage := catchClaudeHandleError(err, token)
		if retry > 0 {
			retry--
			goto label
		}
		ResponseError(ctx, errorMessage, r.Stream, r.IsCompletions, wd)
		return
	}
	partialResponse := cmdvars.Manager.Reply(*context, func(response types.PartialResponse) {
		if r.Stream {
			if response.Status == vars.Begin {
				ctx.Status(200)
				ctx.Header("Accept", "*/*")
				ctx.Header("Content-Type", "text/event-stream")
				ctx.Writer.Flush()
				return
			}

			if response.Error != nil {
				IsClose = true
				var e *cltypes.Claude2Error
				ok := errors.As(response.Error, &e)
				err = response.Error
				if ok && token == "auto" {
					if msg := handleClaudeError(e); msg != "" {
						err = errors.New(msg)
					}
				}

				errorMessage := catchClaudeHandleError(err, token)
				if retry > 0 {
					retry--
				} else {
					ResponseError(ctx, errorMessage, r.Stream, r.IsCompletions, wd)
				}
				return
			}

			if len(response.Message) > 0 {
				select {
				case <-ctx.Request.Context().Done():
					IsClose = true
					IsDone = true
				default:
					if !WriteString(ctx, response.Message, r.IsCompletions) {
						IsClose = true
						IsDone = true
					}
				}
			}

			if response.Status == vars.Closed && wd {
				WriteDone(ctx, r.IsCompletions)
			}
		} else {
			select {
			case <-ctx.Request.Context().Done():
				IsClose = true
				IsDone = true
			default:
			}
		}
	})

	if !r.Stream && !IsClose {
		if partialResponse.Error != nil {
			errorMessage := catchClaudeHandleError(partialResponse.Error, token)
			if !IsDone && retry > 0 {
				goto label
			}
			ResponseError(ctx, errorMessage, r.Stream, r.IsCompletions, wd)
			return
		}

		ctx.JSON(200, BuildCompletion(r.IsCompletions, partialResponse.Message))
	}

	if !IsDone && partialResponse.Error != nil && retry > 0 {
		goto label
	}

	// 检查大黄标
	if token == "auto" && context.Model == vars.Model4WebClaude2S {
		if strings.Contains(partialResponse.Message, HARM) {
			cmdvars.GlobalToken = ""
			logrus.Warn(cmdvars.I18n("HARM"))
		}
	}
}

func createClaudeConversation(token string, r *cmdtypes.RequestDTO, IsC func() bool) (*types.ConversationContext, error) {
	var (
		bot   string
		model string
		appId string
		id    string
		chain string
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
			return nil, errors.New("请在请求头中提供appId")
		}
	default:
		return nil, errors.New(cmdvars.I18n("UNKNOWN_MODEL") + "`" + r.Model + "`")
	}

	message, s, err := trimClaudeMessage(r)
	if err != nil {
		return nil, err
	}
	fmt.Println("-----------------------Response-----------------\n", message, "\n--------------------END-------------------")
	marshal, _ := json.Marshal(s)
	fmt.Println("Schema: " + string(marshal))
	if token == "auto" && cmdvars.GlobalToken == "" {
		if cmdvars.EnablePool { // 使用池的方式
			cmdvars.GlobalToken, err = pool.GetKey()
			if err != nil {
				return nil, err
			}

		} else {
			muLock.Lock()
			defer muLock.Unlock()
			if cmdvars.GlobalToken == "" {
				var email string
				email, cmdvars.GlobalToken, err = pool.GenerateSessionKey()
				logrus.Info(cmdvars.I18n("GENERATE_SESSION_KEY") + "：available -- " + strconv.FormatBool(err == nil) + " email --- " + email + ", sessionKey --- " + cmdvars.GlobalToken)
				pool.CacheKey("CACHE_KEY", cmdvars.GlobalToken)
			}
		}
	}

	if token == "auto" && cmdvars.GlobalToken != "" {
		token = cmdvars.GlobalToken
	}
	fmt.Println("TOKEN_KEY: " + token)
	return &types.ConversationContext{
		Id:      id,
		Token:   token,
		Prompt:  message,
		Bot:     bot,
		Model:   model,
		Proxy:   cmdvars.Proxy,
		H:       claudeHandle(model, IsC),
		AppId:   appId,
		BaseURL: cmdvars.Bu,
		Chain:   chain,
	}, nil
}

func trimClaudeMessage(r *cmdtypes.RequestDTO) (string, schema, error) {
	result := r.Prompt
	if (r.Model == "claude-1.0" || r.Model == "claude-2.0") && len(r.Messages) > 0 {
		// 将repository的内容往上挪
		repositoryXmlHandle(r)

		// 合并消息
		for _, message := range r.Messages {
			switch message["role"] {
			case "assistant":
				result += "Assistant: " + strings.TrimSpace(message["content"]) + "\n\n"
			case "user":
				content := strings.TrimSpace(message["content"])
				if content == "" {
					continue
				}
				if strings.HasPrefix(content, "System:") {
					result += strings.TrimSpace(message["content"][7:]) + "\n\n"
				} else {
					result += "Human: " + message["content"] + "\n\n"
				}
			default:
				result += strings.TrimSpace(message["content"]) + "\n\n"
			}
		}
	}
	// ====  Schema匹配 =======
	compileRegex := regexp.MustCompile(`schema\s?\{[^}]*}`)
	s := schema{
		TrimS:     true,
		TrimP:     true,
		BoH:       true,
		BoS:       false,
		Pile:      true,
		FullColon: true,
		Debug:     false,
		TrimPlot:  false,
	}

	matchSlice := compileRegex.FindStringSubmatch(result)
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
	if s.FullColon {
		// result = strings.ReplaceAll(result, "System:", "System：")
		result = strings.ReplaceAll(result, "Assistant:", "Assistant：")
		result = strings.ReplaceAll(result, "Human:", "Human：")
	}

	// 填充肥料
	if s.Pile && (r.Model == "claude-2.0" || r.Model == "claude-2") {
		pile := cmdvars.GlobalPile
		if cmdvars.GlobalPile == "" {
			pile = piles[rand.Intn(len(piles))]
		}
		c := (cmdvars.GlobalPileSize - len(result)) / len(pile)
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

func claudeHandle(model string, IsC func() bool) types.CustomCacheHandler {
	return func(rChan any) func(*types.CacheBuffer) error {
		needClose := false
		matchers := utils.GlobalMatchers()
		// 遇到`A:`符号剔除
		matchers = append(matchers, &types.StringMatcher{
			Find: A,
			H: func(i int, content string) (state int, result string) {
				return types.MAT_MATCHED, strings.Replace(content, A, "", -1)
			},
		})
		// 遇到`H:`符号结束输出
		matchers = append(matchers, &types.StringMatcher{
			Find: H,
			H: func(i int, content string) (state int, result string) {
				needClose = true
				logrus.Info("---------\n", cmdvars.I18n("H"))
				return types.MAT_MATCHED, strings.Replace(content, H, "", -1)
			},
		})
		// 遇到`System:`符号结束输出
		matchers = append(matchers, &types.StringMatcher{
			Find: S,
			H: func(i int, content string) (state int, result string) {
				needClose = true
				logrus.Info("---------\n", cmdvars.I18n("S"))
				return types.MAT_MATCHED, strings.Replace(content, S, "", -1)
			},
		})

		pos := 0
		partialResponse := rChan.(chan cltypes.PartialResponse)
		return func(self *types.CacheBuffer) error {
			response, ok := <-partialResponse
			if !ok {
				self.Closed = true
				return nil
			}

			if IsC() {
				self.Closed = true
				return nil
			}

			if needClose {
				self.Closed = true
				return nil
			}

			if response.Error != nil {
				self.Closed = true
				return response.Error
			}

			var rawText string
			if model != vars.Model4WebClaude2S {
				text := response.Text
				str := []rune(text)
				rawText = string(str[pos:])
				pos = len(str)
			} else {
				rawText = response.Text
			}

			if rawText == "" {
				return nil
			}

			logrus.Info("rawText ----", rawText)
			self.Cache += utils.ExecMatchers(matchers, rawText)
			return nil
		}
	}
}

func handleClaudeError(err *cltypes.Claude2Error) (msg string) {
	if err.ErrorType.Message == "Account in read-only mode" {
		cmdvars.GlobalToken = ""
		msg = cmdvars.I18n("ACCOUNT_LOCKED")
	}
	if err.ErrorType.Message == "rate_limit_error" {
		cmdvars.GlobalToken = ""
		msg = cmdvars.I18n("ACCOUNT_LIMITED")
	}
	return msg
}

// claude异常处理（清理Token）
func catchClaudeHandleError(err error, token string) string {
	errMsg := err.Error()
	if strings.Contains(errMsg, "failed to fetch the `organizationId`") ||
		strings.Contains(errMsg, "failed to fetch the `conversationId`") {
		CleanToken(token)
	}

	if strings.Contains(errMsg, "Account in read-only mode") {
		CleanToken(token)
		errMsg = cmdvars.I18n("ERROR_ACCOUNT_LOCKED")
	} else if strings.Contains(errMsg, "rate_limit_error") {
		CleanToken(token)
		errMsg = cmdvars.I18n("ERROR_ACCOUNT_LIMITED")
	} else if strings.Contains(errMsg, "connection refused") {
		errMsg = cmdvars.I18n("ERROR_NETWORK")
	} else if strings.Contains(errMsg, "Account has not completed verification") {
		CleanToken(token)
		errMsg = cmdvars.I18n("ACCOUNT_SMS_VERIFICATION")
	} else {
		errMsg += "\n\n" + cmdvars.I18n("ERROR_OTHER")
	}
	return errMsg
	// ResponseError(ctx, errMsg, isStream, isCompletions, wd)
}

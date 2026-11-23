package traechat

// ide_chat.go — Trae IDE agent 协议（/api/agent/v3/llm_utils_chat）。
//
// 为什么需要它：SOLO 协议（/api/remote/v1）每次请求都要带完整的 agent 系统
// 提示词（实测 ~22000 token），首字要 12–19 秒。IDE agent 协议只发用户内容，
// 首字 ~1.3 秒。
//
// 逻辑与 New API 侧的自研 Trae 渠道（adaptor.go）一致：
//   · content 统一转成 [{type:"text",text:"..."}]
//   · 顶层固定 {model, config_name, messages, stream:true, function}
//   · model 与 config_name 都是裸模型名（不带 trae: 前缀）
//   · maxMode 字段把上下文放开到 1M / 输出 64k
//   · function 归属可能不同：报 code=4001 时按 ideFunctionChain 依次重试
//   · 配额类错误（1005/4008/4011）返回 ErrModelRateLimit，交给号池冷却换号

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ideChatPath = "/api/agent/v3/llm_utils_chat"

	ideMaxContextTokens = 1000000
	ideMaxPromptTokens  = 936000
	ideMaxOutputTokens  = 64000
	ideMaxModeType      = 1
)

// ideHTTPClient 是全进程共享的 HTTP 客户端。
//
// 为什么必须共享：默认 http.DefaultTransport 的 MaxIdleConnsPerHost 只有 2，
// 24 个账号并发时会频繁新建连接，每次 TLS 握手约 0.9 秒——这正是首字慢的主要来源。
// 这里把空闲连接数与超时调大，让上游连接长期复用。
var ideHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	},
	// 不设整体超时：流式响应由 ctx 控制
}

// ideFunctionChain 是 function 回退顺序：不同模型归属不同通道，
// 报 4001（参数无效 / 通道不认这个模型）就换下一个。
var ideFunctionChain = []string{
	"solo_agent",
	"solo_work_lite",
	"solo_agent_remote",
	"solo_work_remote",
	"solo_agent_lite",
	"chat_v3",
	"builder_v3",
	"solo_coder",
	"solo_design_lite",
	"solo_design_remote",
}

// ErrModelRateLimit 表示模型级限流（应冷却「账号+模型」而不是禁用账号）。
type ErrModelRateLimit struct{ Msg string }

func (e *ErrModelRateLimit) Error() string { return "trae model rate limit: " + e.Msg }

// ErrIDEUnauthorized 表示凭据失效（应让号池冷却该账号）。
type ErrIDEUnauthorized struct{ Msg string }

func (e *ErrIDEUnauthorized) Error() string { return "trae unauthorized: " + e.Msg }

// ideTextOf 把 Message.Content（string 或 parts 数组）转成 Trae 的 parts。
func idePartsOf(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return []map[string]any{}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return []map[string]any{}
		}
		return []map[string]any{{"type": "text", "text": s}}
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		out := make([]map[string]any, 0, len(parts))
		for _, p := range parts {
			typ, _ := p["type"].(string)
			switch typ {
			case "text", "input_text":
				if t, ok := p["text"].(string); ok {
					out = append(out, map[string]any{"type": "text", "text": t})
				}
			case "image_url", "input_image":
				// IDE 协议只认 image_url；保持键名不变
				var url string
				switch v := p["image_url"].(type) {
				case string:
					url = v
				case map[string]any:
					url, _ = v["url"].(string)
				}
				if url != "" {
					out = append(out, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": url},
					})
				}
			}
		}
		return out
	}
	return []map[string]any{{"type": "text", "text": string(raw)}}
}

// ideBody 组装上游请求体。bareModel 是不带 trae: 前缀的模型名。
func ideBody(req ChatRequest, bareModel, function string) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := map[string]any{
			"role":    m.Role,
			"content": idePartsOf(m.Content),
		}
		if len(m.ToolCalls) > 0 {
			msg["tool_calls"] = m.ToolCalls
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		msgs = append(msgs, msg)
	}
	body := map[string]any{
		"model":       bareModel,
		"config_name": bareModel,
		"messages":    msgs,
		"stream":      true, // 上游只支持流式
		"function":    function,
		// maxMode：放开到 1M 上下文 / 64k 输出
		"model_auto_selection": map[string]any{
			"strategy":                  "max",
			"fallback_to_advance_model": nil,
			"entitlement_id":            nil,
		},
		"model_selection_strategy": "max",
		"mode_type":                ideMaxModeType,
		"context_window_size":      ideMaxContextTokens,
		"prompt_max_tokens":        ideMaxPromptTokens,
		"max_tokens":               ideMaxOutputTokens,
	}
	if req.MaxTokens > 0 && req.MaxTokens < ideMaxOutputTokens {
		body["max_tokens"] = req.MaxTokens
	}
	return body
}

// ideSSEEvent 是 IDE 协议里 data: 行的联合结构。
type ideSSEEvent struct {
	Response         string `json:"response"`
	ReasoningContent string `json:"reasoning_content"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	Status           string `json:"status"`
	ErrorMessage     string `json:"error_message"`
	Code             any    `json:"code"`
	Message          string `json:"message"`
	TurnID           string `json:"turn_id"`
}

// ideQuotaCode 判断是否为「额度/限额」类错误码 → 交给号池冷却换号。
func ideQuotaCode(v any) bool {
	switch c := v.(type) {
	case float64:
		return c == 1005 || c == 4008 || c == 4011
	case string:
		return c == "1005" || c == "4008" || c == "4011"
	}
	return false
}

func ideParamInvalid(body map[string]any) bool {
	switch c := body["code"].(type) {
	case float64:
		return c == 4001
	case string:
		return c == "4001"
	}
	return false
}

// ideChatOnce 发一次请求并消费 SSE。返回 (是否因 4001 需要换 function, usage, turnID, err)
func ideChatOnce(ctx context.Context, acct *Account, body map[string]any,
	em Emitter) (needRetry bool, usage Usage, hasUsage bool, turnID string, err error) {

	raw, _ := json.Marshal(body)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, ideHost+ideChatPath, bytes.NewReader(raw))
	if rerr != nil {
		return false, usage, false, "", rerr
	}
	req.Header = ideHeaders(acct)
	req.Header.Set("Accept", "text/event-stream")

	client := ideHTTPClient // 共享连接池：复用 TCP/TLS，避免每次握手
	resp, derr := client.Do(req)
	if derr != nil {
		return false, usage, false, "", fmt.Errorf("IDE 对话请求失败: %w", derr)
	}
	defer func() {
		// 必须读干净才能把连接还回池子：提前 return 时残留的 body 会让
		// 该连接被直接丢弃，下次请求只能重新握手（实测多付约 0.9 秒）。
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, usage, false, "", &ErrIDEUnauthorized{Msg: truncateStr(string(head), 120)}
	}
	if resp.StatusCode != http.StatusOK {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, usage, false, "", fmt.Errorf("IDE 对话 HTTP %d: %s",
			resp.StatusCode, truncateStr(string(head), 160))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var event string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev ideSSEEvent
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch event {
		case "output":
			if ev.ReasoningContent != "" {
				if e := em.OnReasoning(ev.ReasoningContent); e != nil {
					return false, usage, hasUsage, turnID, e
				}
			}
			if ev.Response != "" {
				if e := em.OnContent(ev.Response); e != nil {
					return false, usage, hasUsage, turnID, e
				}
			}
		case "token_usage":
			usage = Usage{
				PromptTokens:     ev.PromptTokens,
				CompletionTokens: ev.CompletionTokens,
				TotalTokens:      ev.TotalTokens,
			}
			hasUsage = true
			em.OnUsage(usage)
		case "metadata":
			if ev.TurnID != "" {
				turnID = ev.TurnID
			}
		case "done":
			if ev.ErrorMessage != "" {
				return false, usage, hasUsage, turnID,
					fmt.Errorf("上游结束但报错: %s", truncateStr(ev.ErrorMessage, 160))
			}
			return false, usage, hasUsage, turnID, nil
		case "error":
			if ideQuotaCode(ev.Code) {
				return false, usage, hasUsage, turnID,
					&ErrModelRateLimit{Msg: truncateStr(ev.Message, 120)}
			}
			if ideParamInvalid(map[string]any{"code": ev.Code}) {
				return true, usage, hasUsage, turnID, nil
			}
			return false, usage, hasUsage, turnID,
				fmt.Errorf("上游错误 code=%v %s", ev.Code, truncateStr(ev.Message, 120))
		}
	}
	if serr := scanner.Err(); serr != nil && serr != io.EOF {
		return false, usage, hasUsage, turnID, fmt.Errorf("读取上游流失败: %w", serr)
	}
	return false, usage, hasUsage, turnID, nil
}

// runIDE 用 IDE 协议跑一次请求，走 function 回退链。
func (r *Relay) runIDE(ctx context.Context, req ChatRequest, em Emitter,
	acct *Account, bareModel string) (*converter, error) {

	conv := newConverter(em)
	var lastErr error
	for _, fn := range ideFunctionChain {
		body := ideBody(req, bareModel, fn)
		retry, usage, hasUsage, turnID, err := ideChatOnce(ctx, acct, body, em)
		if retry {
			lastErr = fmt.Errorf("function %s 不认该模型，换下一个", fn)
			continue
		}
		if err != nil {
			return nil, err
		}
		conv.usage = usage
		conv.hasUsage = hasUsage
		conv.turnID = turnID
		conv.done = true
		return conv, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有 function 都失败")
	}
	return nil, lastErr
}

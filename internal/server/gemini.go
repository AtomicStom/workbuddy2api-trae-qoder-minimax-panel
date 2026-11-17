package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/gemini"
)

// Gemini 官方 API Key 通道：模型名带 "<组ID>:" 前缀，例如 gemini: / gemx:。
//
// 上游 generativelanguage.googleapis.com/v1beta/openai 本身就是 OpenAI 兼容格式，
// 所以这里只做「改写 model + 带 key 转发 + 流式透传」，不做协议翻译。
// 分组用于把不同用途/不同额度的 key 彻底隔开，互不抢额度、互不影响。

// geminiModels 返回所有分组的模型名单（纯动态；无 key/不可用 → 该组不列出）。
func (h *Handler) geminiModels() []map[string]any {
	if h.cfg.Gemini == nil {
		return nil
	}
	var out []map[string]any
	for _, g := range h.cfg.Gemini.Groups {
		ids, err := g.Models(h.baseCtx())
		if err != nil || len(ids) == 0 {
			continue
		}
		for _, id := range ids {
			out = append(out, map[string]any{
				"id":                g.ID + ":" + id,
				"object":            "model",
				"created":           1753600000,
				"owned_by":          g.ID,
				"name":              id,
				"vendor":            g.Name,
				"context_length":    1048576,
				"max_output_tokens": 65536,
				"supports_tools":    true,
			})
		}
	}
	return out
}

// geminiChat 处理某个分组的对话请求（流式/非流式均由上游原样返回）。
func (h *Handler) geminiChat(w http.ResponseWriter, r *http.Request, body []byte, bareModel string, g *gemini.Client, st *chatStat) {
	if g == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "gemini_api_disabled",
			"Gemini API Key 通道未启用")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "解析请求体失败: "+err.Error())
		return
	}
	// 上游只认裸模型名；"<组ID>:" 是网关侧路由前缀。
	payload["model"] = bareModel
	rewritten, err := json.Marshal(payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "重写请求体失败")
		return
	}

	resp, err := g.Forward(r.Context(), "/chat/completions", rewritten)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "gemini_api_error", err.Error())
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

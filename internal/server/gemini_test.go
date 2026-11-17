package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/gemini"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// 假上游：断言收到的 model 是裸名、Authorization 是 Bearer key，并回一个 OpenAI 形状的响应。
func newFakeGeminiUpstream(t *testing.T, gotModel *string, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		if m, ok := payload["model"].(string); ok {
			*gotModel = m
		}
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
}

// 通过真实配置文件路径走一遍完整链路（Load → Match → Forward → 路由改写）。
func TestGeminiChatRoutesByPrefixUsingRegistry(t *testing.T) {
	var gotModel, gotAuth string
	srv := newFakeGeminiUpstream(t, &gotModel, &gotAuth)
	defer srv.Close()

	// 写一个密钥文件，base_url 指向假上游。
	dir := t.TempDir()
	cfgPath := dir + "/gemini_api_keys.json"
	body := `{"base_url":"` + srv.URL + `","enabled":true,"groups":[{"id":"gemini","name":"G","keys":["testkey"]}]}`
	if err := writeTempFile(cfgPath, body); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	reg, err := gemini.Load(cfgPath)
	if err != nil || reg == nil {
		t.Fatalf("load registry: %v", err)
	}

	h := NewHandler(Config{Gemini: reg})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"gemini:gemini-3-flash-preview","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotModel != "gemini-3-flash-preview" {
		t.Fatalf("upstream model = %q, want bare id", gotModel)
	}
	if gotAuth != "Bearer testkey" {
		t.Fatalf("auth = %q", gotAuth)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not json: %v", err)
	}
	if _, ok := out["choices"]; !ok {
		t.Fatalf("response missing choices: %s", rec.Body.String())
	}
}

// 非 gemini: 前缀的请求不应走 Gemini 通道（没有 WorkBuddy 账号时返回未启用/无可用账号类错误，
// 而不是打到假上游）。
func TestGeminiPrefixNotMatched(t *testing.T) {
	var gotModel, gotAuth string
	srv := newFakeGeminiUpstream(t, &gotModel, &gotAuth)
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := dir + "/gemini_api_keys.json"
	if err := writeTempFile(cfgPath, `{"base_url":"`+srv.URL+`","enabled":true,"groups":[{"id":"gemini","keys":["k"]}]}`); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	reg, _ := gemini.Load(cfgPath)
	// 空池（无 WorkBuddy 账号）：非 gemini: 前缀的请求会走到 WorkBuddy 选号并因无账号报错，
	// 但绝不应该打到 Gemini 假上游。
	h := NewHandler(Config{Gemini: reg, Pool: pool.New("")})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"geminiX:foo","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotModel != "" {
		t.Fatalf("non-gemini prefix should not hit Gemini upstream, got model %q", gotModel)
	}
}

// /v1/models 应出现 gemini: 前缀的模型条目。
func TestGeminiModelsListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"models/gemini-3-flash-preview"},{"id":"models/gemini-2.5-pro"}]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := dir + "/gemini_api_keys.json"
	if err := writeTempFile(cfgPath, `{"base_url":"`+srv.URL+`","enabled":true,"groups":[{"id":"gemini","name":"Gemini 官方","keys":["k"]}]}`); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	reg, _ := gemini.Load(cfgPath)
	h := NewHandler(Config{Gemini: reg, Pool: pool.New("")})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("models not json: %v", err)
	}
	found := map[string]bool{}
	for _, m := range out.Data {
		id, _ := m["id"].(string)
		found[id] = true
	}
	if !found["gemini:gemini-3-flash-preview"] {
		t.Fatalf("missing gemini:gemini-3-flash-preview in %v", found)
	}
	if !found["gemini:gemini-2.5-pro"] {
		t.Fatalf("missing gemini:gemini-2.5-pro in %v", found)
	}
}

func writeTempFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

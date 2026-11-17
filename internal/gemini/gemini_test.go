package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gemini_api_keys.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	return p
}

func TestLoadMissingFile(t *testing.T) {
	reg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if reg != nil {
		t.Fatalf("missing file should give nil registry")
	}
}

func TestLoadDisabled(t *testing.T) {
	p := writeCfg(t, `{"enabled":false,"groups":[{"id":"gemini","keys":["k1"]}]}`)
	reg, err := Load(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if reg != nil {
		t.Fatalf("disabled should give nil registry")
	}
}

func TestLoadGroups(t *testing.T) {
	p := writeCfg(t, `{"base_url":"https://x/v1beta/openai","enabled":true,
		"groups":[{"id":"gemini","name":"G","keys":["k1"," k2 "]},{"id":"gemx","keys":["k3"]}]}`)
	reg, err := Load(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if reg == nil || len(reg.Groups) != 2 {
		t.Fatalf("want 2 groups, got %v", reg)
	}
	if reg.Groups[0].Base != "https://x/v1beta/openai" {
		t.Fatalf("base mismatch: %q", reg.Groups[0].Base)
	}
	if len(reg.Groups[0].Keys) != 2 || reg.Groups[0].Keys[1] != "k2" {
		t.Fatalf("keys not trimmed: %#v", reg.Groups[0].Keys)
	}
	if reg.Groups[1].Name != "gemx" {
		t.Fatalf("name should default to id, got %q", reg.Groups[1].Name)
	}
}

func TestLoadLegacyKeys(t *testing.T) {
	p := writeCfg(t, `{"enabled":true,"keys":["k1"]}`)
	reg, err := Load(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if reg == nil || len(reg.Groups) != 1 {
		t.Fatalf("legacy format should give 1 group")
	}
	if reg.Groups[0].ID != "gemini" {
		t.Fatalf("legacy group id should be gemini, got %q", reg.Groups[0].ID)
	}
	if reg.Groups[0].Base != DefaultBase {
		t.Fatalf("legacy group should use DefaultBase, got %q", reg.Groups[0].Base)
	}
}

func TestMatch(t *testing.T) {
	reg := &Registry{byID: map[string]*Client{"gemini": {ID: "gemini"}}}
	g, bare, ok := reg.Match("gemini:gemini-2.5-pro")
	if !ok || g == nil || bare != "gemini-2.5-pro" {
		t.Fatalf("match failed: %v %q %v", g, bare, ok)
	}
	if _, _, ok := reg.Match("other:x"); ok {
		t.Fatalf("unknown prefix should not match")
	}
	if _, _, ok := reg.Match("nocolon"); ok {
		t.Fatalf("no-colon should not match")
	}
	var nilReg *Registry
	if _, _, ok := nilReg.Match("gemini:x"); ok {
		t.Fatalf("nil registry must not match")
	}
}

// 上游 /models 返回 "models/xxx" 形式，对外要剥掉前缀。
func TestModelsStripsModelsPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k1" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "models/gemini-2.5-pro"}, {"id": "models/gemini-3-flash-preview"}},
		})
	}))
	defer srv.Close()

	c := &Client{ID: "gemini", Base: srv.URL, Keys: []string{"k1"}, HTTP: srv.Client()}
	ids, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("models err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "gemini-2.5-pro" || ids[1] != "gemini-3-flash-preview" {
		t.Fatalf("ids = %#v", ids)
	}
}

// 坏 key（Gemini 用 400，不是 401）应触发换下一把。
func TestForwardRotatesOnBadKey400(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		if key == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key."}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{ID: "gemini", Base: srv.URL, Keys: []string{"bad", "good"}, HTTP: srv.Client()}
	resp, err := c.Forward(context.Background(), "/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("forward err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(seen) != 2 || seen[0] != "bad" || seen[1] != "good" {
		t.Fatalf("key rotation order = %#v", seen)
	}
}

// 与 key 无关的 400（例如模型不存在）必须原样返回，不能被吞掉换 key。
func TestForwardKeepsNonKey400(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"models/gemini-2.5-pro is not found"}}`)
	}))
	defer srv.Close()

	c := &Client{ID: "gemini", Base: srv.URL, Keys: []string{"k1", "k2"}, HTTP: srv.Client()}
	resp, err := c.Forward(context.Background(), "/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("forward err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if len(seen) != 1 {
		t.Fatalf("should not rotate keys on non-key 400, seen=%#v", seen)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "is not found") {
		t.Fatalf("upstream body lost: %s", body)
	}
}

func TestForwardAllKeysBad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &Client{ID: "gemini", Base: srv.URL, Keys: []string{"a", "b"}, HTTP: srv.Client()}
	if _, err := c.Forward(context.Background(), "/chat/completions", []byte(`{}`)); err == nil {
		t.Fatalf("want error when all keys unusable")
	}
}

// 显式传入代理时，Transport 必须挂上该代理。
//
// ⚠️ 2026-11-17 更正：本测试原先断言「默认客户端必须直连、不吃环境变量」，
// 理由写的是「本机注册表系统代理开着 127.0.0.1:10808 且经常拒连」。
// 该结论已被实测推翻：
//   - 直连 generativelanguage.googleapis.com：25s 超时（Google 在本机不可直连）；
//   - 走 http://127.0.0.1:10808：683ms 返回 HTTP 200。
// 所以 Gemini 通道必须走代理，"直连"才是会挂掉的那条路。
// 现在改为断言「显式代理被正确挂载」。
func TestNewHTTPClientUsesExplicitProxy(t *testing.T) {
	c := newHTTPClient(10*time.Second, "http://127.0.0.1:10808")
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", c.Transport)
	}
	if tr.Proxy == nil {
		t.Fatalf("Proxy must be set when an explicit proxy is configured")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	u, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("proxy func error: %v", err)
	}
	if u == nil || u.Host != "127.0.0.1:10808" {
		t.Fatalf("proxy target = %v, want 127.0.0.1:10808", u)
	}
}

// 代理地址为空时必须是直连（不挂 Proxy）。
func TestNewHTTPClientNoProxyIsDirect(t *testing.T) {
	c := newHTTPClient(10*time.Second, "")
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatalf("Proxy must be nil when no proxy is configured")
	}
}

// 请求体必须原样转发（OpenAI 兼容协议不做改写）。
func TestForwardPassesBodyVerbatim(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := []byte(`{"model":"gemini-3-flash-preview","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c := &Client{ID: "gemini", Base: srv.URL, Keys: []string{"k"}, HTTP: srv.Client()}
	resp, err := c.Forward(context.Background(), "/chat/completions", body)
	if err != nil {
		t.Fatalf("forward err: %v", err)
	}
	defer resp.Body.Close()
	if string(got) != string(body) {
		t.Fatalf("body changed:\n got %s\nwant %s", got, body)
	}
}

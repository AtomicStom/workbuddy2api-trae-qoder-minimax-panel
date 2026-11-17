// Package gemini 把 Google Gemini 官方 API Key 接进网关。
//
// 与 internal/mmapi 的结构完全一致（静态 API Key + OpenAI 兼容上游 + 多 key 轮换），
// 差别只在 Gemini 侧的几个细节：
//   - 上游 base 是 https://generativelanguage.googleapis.com/v1beta/openai，
//     它自己就提供 OpenAI 兼容的 /chat/completions 与 /models，不需要协议翻译；
//   - /models 返回的模型 id 带 "models/" 前缀（例如 "models/gemini-2.5-pro"），
//     对外暴露时剥掉前缀，调用时再由网关补回去。
//
// 支持按用途分组（例如「订阅」与「按量」分开），每组是一个独立模型前缀：
//
//	{"base_url":"https://generativelanguage.googleapis.com/v1beta/openai","enabled":true,
//	 "groups":[{"id":"gemini","name":"Gemini 官方","keys":["AIza…"]},
//	           {"id":"gemx","name":"Gemini 备用","keys":["AIza…"]}]}
//
// 兼容老格式 {"keys":[...]}：会当成一个 id=gemini 的组。
package gemini

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBase Google Gemini 的 OpenAI 兼容入口。
const DefaultBase = "https://generativelanguage.googleapis.com/v1beta/openai"

// Client 一个分组的转发器（组内多 key 轮询）。
type Client struct {
	ID      string // 模型前缀：<ID>:<model>
	Name    string // 展示名
	Base    string
	Proxy   string // 该组使用的代理地址（空 = 直连）
	Keys    []string
	HTTP    *http.Client

	cur atomic.Uint64

	mu     sync.Mutex
	models []string
	at     time.Time
}

// Registry 全部通道分组（按配置顺序）。
type Registry struct {
	Groups []*Client
	byID   map[string]*Client
}

// Match 按模型名前缀找分组：形如 "gemini:gemini-2.5-pro" → (组, "gemini-2.5-pro", true)。
func (r *Registry) Match(model string) (*Client, string, bool) {
	if r == nil {
		return nil, "", false
	}
	i := strings.IndexByte(model, ':')
	if i <= 0 {
		return nil, "", false
	}
	if g, ok := r.byID[model[:i]]; ok {
		return g, model[i+1:], true
	}
	return nil, "", false
}

// Load 读取配置；文件不存在/未启用返回 nil（通道关闭）。
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f struct {
		BaseURL string   `json:"base_url"`
		Enabled bool     `json:"enabled"`
		Proxy   string   `json:"proxy_url"`
		Keys    []string `json:"keys"`
		Groups  []struct {
			ID   string   `json:"id"`
			Name string   `json:"name"`
			Keys []string `json:"keys"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("密钥文件解析失败: %w", err)
	}
	if !f.Enabled {
		return nil, nil
	}
	base := strings.TrimRight(strings.TrimSpace(f.BaseURL), "/")
	if base == "" {
		base = DefaultBase
	}
	proxyURL := resolveProxy(f.Proxy)

	reg := &Registry{byID: map[string]*Client{}}
	add := func(id, name string, keys []string) {
		id = strings.TrimSpace(id)
		var clean []string
		for _, k := range keys {
			if k = strings.TrimSpace(k); k != "" {
				clean = append(clean, k)
			}
		}
		if id == "" || len(clean) == 0 || reg.byID[id] != nil {
			return
		}
		c := &Client{ID: id, Name: name, Base: base, Proxy: proxyURL, Keys: clean,
			HTTP: newHTTPClient(300*time.Second, proxyURL)}
		if c.Name == "" {
			c.Name = id
		}
		reg.Groups = append(reg.Groups, c)
		reg.byID[id] = c
	}

	for _, g := range f.Groups {
		add(g.ID, g.Name, g.Keys)
	}
	if len(f.Groups) == 0 {
		add("gemini", "Gemini", f.Keys) // 兼容老格式
	}
	if len(reg.Groups) == 0 {
		return nil, nil
	}
	return reg, nil
}

// ProxyURL 返回该通道实际使用的代理地址（空 = 直连），供调用方打日志。
func (r *Registry) ProxyURL() string {
	if r == nil || len(r.Groups) == 0 {
		return ""
	}
	// 各组共用同一个 proxyURL（Load 时统一解析），取第一组即可。
	return resolveProxy(r.Groups[0].Proxy)
}

// newHTTPClient 构造出站客户端。
//
// 代理策略见 proxy.go：本机 Google 不可直连（实测直连 25s 超时），
// 必须走本机代理（http://127.0.0.1:10808 → 683ms HTTP 200）。
// 代理地址由 resolveProxy 按「配置项 > 环境变量 > Windows 系统代理注册表」解析，
// 解析不到才直连。不用 http.ProxyFromEnvironment 的原因见 proxy.go 注释：
// 它不读注册表，而服务由计划任务拉起时环境变量被清空，结果就是静默直连卡死。
//
// 连接层加固（禁 h2 / 建连超时 / TLS 握手超时 / 空闲连接短回收）与
// internal/upstream 的 newTransport 同一套口径。
func newHTTPClient(timeout time.Duration, proxyURL string) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 15 * time.Second,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 proxyFunc(proxyURL),
			DialContext:           dialer.DialContext,
			TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
			TLSHandshakeTimeout:   10 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
}

// nextKey 轮询挑一把 key。
func (c *Client) nextKey() string {
	n := c.cur.Add(1)
	return c.Keys[int(n-1)%len(c.Keys)]
}

// Models 拉该组的模型列表（缓存 30 分钟）。
//
// 上游返回的 id 形如 "models/gemini-2.5-pro"，这里剥掉 "models/" 前缀返回裸名，
// 与调用侧（Forward）用同一套命名。
func (c *Client) Models(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	if len(c.models) > 0 && time.Since(c.at) < 30*time.Minute {
		out := append([]string(nil), c.models...)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.nextKey())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini /models HTTP %d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		ids = append(ids, strings.TrimPrefix(id, "models/"))
	}
	c.mu.Lock()
	c.models, c.at = ids, time.Now()
	c.mu.Unlock()
	return ids, nil
}

// Forward 把请求体原样转发到上游（OpenAI 兼容）。
//
// 组内多 key 时，失效（400/401）、欠费（402）、限流（429）会自动换下一把重试，
// 避免用户看到"key 有问题"其实是轮到了坏 key。
//
// 注意：Gemini 对无效 API key 返回 400（不是 401），所以 400 也要参与轮换。
func (c *Client) Forward(ctx context.Context, path string, body []byte) (*http.Response, error) {
	var lastErr error
	n := len(c.Keys)
	start := int(c.cur.Add(1) - 1)
	for i := 0; i < n; i++ {
		key := c.Keys[(start+i)%n]
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream, application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		// 只在「换一把 key 也许有用」的错误上轮换：认证失败 / 欠费 / 限流。
		//
		// 400 不参与轮换：Gemini 对无效 API key 返回 400（"API key not valid"），
		// 但 400 同样覆盖「请求参数非法」「模型不存在」等与 key 无关的错误——
		// 把这些一起吞掉换 key，只会让用户拿到一句无意义的
		// "所有 API Key 均不可用"，真正的原因被丢掉。所以 400 要看 body：
		// 只有明确提到 key 失效才换，否则原样把上游响应还给调用方。
		if resp.StatusCode == http.StatusBadRequest {
			sniff, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			if !bodyIndicatesBadKey(sniff) {
				return &http.Response{
					StatusCode: resp.StatusCode,
					Status:     resp.Status,
					Header:     resp.Header,
					Body:       io.NopCloser(bytes.NewReader(sniff)),
				}, nil
			}
			continue
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusPaymentRequired,
			http.StatusTooManyRequests:
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("该分组的所有 API Key 均不可用（欠费/限流/失效）")
}

// bodyIndicatesBadKey 判断 400 响应体是否在说「API Key 无效」。
// Gemini 的相关措辞：API key not valid / API_KEY_INVALID / invalid API key。
func bodyIndicatesBadKey(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, kw := range []string{
		"api key not valid",
		"api_key_invalid",
		"invalid api key",
		"apikey not valid",
		"api key expired",
	} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// proxy.go 出站代理解析：决定 Gemini 上游请求走不走代理、走哪个代理。
//
// 为什么需要这一层（本机实测事实）：
//   - 直连 generativelanguage.googleapis.com：25s 超时，Google 在本机不可直连；
//   - 走 http://127.0.0.1:10808（本机 Shadowrocket）：683ms 返回 HTTP 200。
// 所以 Gemini 通道必须走代理，否则永远拿不到模型列表与对话结果。
//
// 为什么不能只靠 http.ProxyFromEnvironment：
//   Go 的 ProxyFromEnvironment 只读 HTTP_PROXY/HTTPS_PROXY 环境变量，
//   **不读 Windows 注册表系统代理**。服务由计划任务拉起时环境变量被启动脚本清空，
//   于是默认 Transport 会变成"直连"并卡死——这正是旧版本 502 的真正成因
//   （旧版以为是自己设了 Proxy:nil，实测该修正方向反了）。
//
// 解析优先级（先到先用）：
//  1. 配置项 proxy_url（密钥文件里显式写死，最可控）；
//  2. 环境变量 HTTPS_PROXY / HTTP_PROXY（尊重运维显式设置）；
//  3. Windows 注册表系统代理 ProxyEnable + ProxyServer（IE/Edge 那套，
//     Electron 认的就是它；本机 ProxyEnable=1 → 127.0.0.1:10808）。
//
// 三条都拿不到时返回 nil（直连），不臆造代理。
package gemini

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
)

// systemProxyRegistryPath 是 Windows 系统代理所在的注册表路径。
const systemProxyRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// resolveProxy 按优先级解析出代理 URL；返回 "" 表示直连。
func resolveProxy(configured string) string {
	if s := strings.TrimSpace(configured); s != "" {
		return normalizeProxy(s)
	}
	for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if s := strings.TrimSpace(os.Getenv(env)); s != "" {
			return normalizeProxy(s)
		}
	}
	if s := systemProxyFromRegistry(); s != "" {
		return s
	}
	return ""
}

// normalizeProxy 补齐 scheme，并处理 "host:port" 这种省略 scheme 的写法。
// Go 的 http.ProxyURL 要求带 scheme，否则解析出来是空 host。
func normalizeProxy(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.String()
}

// systemProxyFromRegistry 读 Windows 系统代理设置。
//
// 只在 Windows 上生效；非 Windows 返回 ""。
// ProxyEnable != "1" 视为未开启（IE 那套的语义）。
// ProxyServer 常见三种写法，本函数按顺序尝试：
//   - "host:port"                      单代理
//   - "http=host:port;https=host:port" 分协议
//   - "socks=host:port"               socks（Go 的 http.Transport 支持 socks5 scheme）
func systemProxyFromRegistry() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	v, err := readRegistryValue(systemProxyRegistryPath, "ProxyEnable")
	if err != nil || strings.TrimSpace(v) != "1" {
		return ""
	}
	ps, err := readRegistryValue(systemProxyRegistryPath, "ProxyServer")
	if err != nil {
		return ""
	}
	return parseProxyServer(strings.TrimSpace(ps))
}

// parseProxyServer 从 ProxyServer 字符串里挑一个可用的代理地址。
func parseProxyServer(s string) string {
	if s == "" {
		return ""
	}
	// 分协议写法：优先 https，其次 http，最后 socks
	if strings.Contains(s, "=") {
		m := map[string]string{}
		for _, part := range strings.Split(s, ";") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				m[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
			}
		}
		for _, k := range []string{"https", "http", "socks", "socks5"} {
			if v, ok := m[k]; ok && v != "" {
				if n := normalizeProxyScheme(k, v); n != "" {
					return n
				}
			}
		}
		return ""
	}
	// 单代理写法
	return normalizeProxy(s)
}

// normalizeProxyScheme 给 socks 类协议补上正确的 scheme。
// "socks=127.0.0.1:10809" → "socks5://127.0.0.1:10809"（Go 的 Transport 认 socks5）。
func normalizeProxyScheme(scheme, addr string) string {
	s := strings.TrimSpace(addr)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		return normalizeProxy(s)
	}
	switch strings.ToLower(scheme) {
	case "socks", "socks5":
		return "socks5://" + s
	default:
		return normalizeProxy(s)
	}
}

// proxyFunc 返回可挂到 http.Transport.Proxy 上的函数。
// proxyURL 为空 → 直连（返回 nil）。
func proxyFunc(proxyURL string) func(*http.Request) (*url.URL, error) {
	if proxyURL == "" {
		return nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil
	}
	return http.ProxyURL(u)
}

// DescribeProxy 给日志用的一行描述。
func DescribeProxy(proxyURL string) string {
	if proxyURL == "" {
		return "direct (no proxy resolved)"
	}
	return fmt.Sprintf("proxy %s", proxyURL)
}

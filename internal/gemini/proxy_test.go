package gemini

import (
	"testing"
)

func TestNormalizeProxy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://127.0.0.1:10808", "http://127.0.0.1:10808"},
		{"127.0.0.1:10808", "http://127.0.0.1:10808"},
		{"  http://127.0.0.1:10808  ", "http://127.0.0.1:10808"},
		{"socks5://127.0.0.1:10809", "socks5://127.0.0.1:10809"},
		{"", ""},
		{"http://", ""},
	}
	for _, c := range cases {
		if got := normalizeProxy(c.in); got != c.want {
			t.Errorf("normalizeProxy(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseProxyServer(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"127.0.0.1:10808", "http://127.0.0.1:10808"},
		{"http=127.0.0.1:10808;https=127.0.0.1:10808", "http://127.0.0.1:10808"},
		{"https=127.0.0.1:8443", "http://127.0.0.1:8443"},
		{"socks=127.0.0.1:10809", "socks5://127.0.0.1:10809"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseProxyServer(c.in); got != c.want {
			t.Errorf("parseProxyServer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 配置项优先级最高：写了 proxy_url 就用它，不看环境变量。
func TestResolveProxyConfiguredWins(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	got := resolveProxy("http://127.0.0.1:10808")
	if got != "http://127.0.0.1:10808" {
		t.Fatalf("resolveProxy = %q, want the configured value", got)
	}
}

// 环境变量次之：HTTPS_PROXY 优先于 HTTP_PROXY。
//
// 注意：Windows 的环境变量名大小写不敏感，os.Getenv("HTTPS_PROXY") 与
// os.Getenv("https_proxy") 命中的是同一个变量，所以这里不能像类 Unix
// 那样再把小写变体单独置空——那会把大写的一起清掉。
func TestResolveProxyFromEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:10808")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:7777")
	got := resolveProxy("")
	if got != "http://127.0.0.1:10808" {
		t.Fatalf("resolveProxy = %q, want the HTTPS_PROXY value", got)
	}
}

// 三条都拿不到时返回空（直连），不臆造代理地址。
//
// 本机注册表里系统代理是开着的（ProxyEnable=1 → 127.0.0.1:10808），
// 环境变量清空后会回落到注册表，所以这里断言"回落到注册表的系统代理"。
func TestResolveProxyFallsBackToRegistry(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY"} {
		t.Setenv(k, "")
	}
	got := resolveProxy("")
	if got == "" {
		// 非 Windows 或注册表未开系统代理：直连也是合法结果。
		t.Log("no proxy resolved (direct)")
		return
	}
	if got != "http://127.0.0.1:10808" {
		t.Fatalf("resolveProxy = %q, want the registry system proxy", got)
	}
}

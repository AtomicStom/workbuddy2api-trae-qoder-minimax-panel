package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestDesktopReportCarriesReportDelayAndTimestamp 锁住 2026-11-17 实测出的国际版硬约束：
//
//	POST https://www.workbuddy.ai/v2/report
//	→ {"code":10001,"msg":"10001:event missing both reportDelay and timestamp fields"}
//
// 缺 reportDelay / timestamp 的事件会被上游**直接 400 丢弃**（国服宽容、国际版严格），
// 于是「上报 HTTP 200」其实是「从未被接收」——这正是国际版成长路径点了没反应的原因。
// 本测试确保后续改动不会把这两个字段弄丢。
func TestDesktopReportCarriesReportDelayAndTimestamp(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := New()
	c.ChatBaseGlobal = srv.URL
	c.BillingBaseGlobal = srv.URL
	a := &auth.Auth{UID: "u-1", Nickname: "n1", Domain: "www.workbuddy.ai"}

	ev := DesktopEvent{"eventCode": "chat_request_send", "conversationId": "conv-1"}
	if err := c.ReportDesktopEvent(a, ev); err != nil {
		t.Fatalf("report err=%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("上报事件数=%d want 1", len(got))
	}
	for _, k := range []string{"reportDelay", "timestamp"} {
		if _, ok := got[0][k]; !ok {
			t.Errorf("事件缺少 %q 字段（国际版会以 10001 丢弃）: %v", k, got[0])
		}
	}
}

// TestDesktopReportBusinessFieldsOverrideFingerprint 业务字段优先于公共指纹
// （国际版 checklist 的 skillId / template_id 等必须用业务值，不能被指纹覆盖）。
func TestDesktopReportBusinessFieldsOverrideFingerprint(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := New()
	c.ChatBaseGlobal = srv.URL
	c.BillingBaseGlobal = srv.URL
	a := &auth.Auth{UID: "u-1", Nickname: "n1", Domain: "www.workbuddy.ai"}

	ev := IntlTemplateUsedEvent("tpl-123", "coding")
	if err := c.ReportDesktopEvent(a, ev); err != nil {
		t.Fatalf("report err=%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("上报事件数=%d want 1", len(got))
	}
	if got[0]["template_id"] != "tpl-123" {
		t.Errorf("template_id=%v want tpl-123", got[0]["template_id"])
	}
	if got[0]["task_mode"] != "coding" {
		t.Errorf("task_mode=%v want coding", got[0]["task_mode"])
	}
	if got[0]["eventCode"] != "template_used" {
		t.Errorf("eventCode=%v want template_used", got[0]["eventCode"])
	}
}

// TestIntlChecklistEventCodes 国际版 checklist 的任务码就是客户端 eventCode
// （2026-11-17 从国际版客户端 app.asar 实测得到）。
func TestIntlChecklistEventCodes(t *testing.T) {
	cases := map[string]DesktopEvent{
		"skill_installed":   IntlSkillInstalledEvent("n", "s1", "1.0.0"),
		"expert_summoned":   IntlExpertSummonedEvent("ex_1", "名字", "标题", "agent", "1.4.0"),
		"template_used":     IntlTemplateUsedEvent("tpl-1", ""),
		"chat_response_complete": IntlChatResponseCompleteEvent("conv", "req"),
	}
	for code, ev := range cases {
		if ev["eventCode"] != code {
			t.Errorf("eventCode=%v want %s", ev["eventCode"], code)
		}
	}
	// template_used 的业务字段名是下划线（客户端原样），task_mode 缺省为 coding。
	ev := IntlTemplateUsedEvent("tpl-1", "")
	if ev["task_mode"] != "coding" {
		t.Errorf("task_mode 缺省=%v want coding", ev["task_mode"])
	}
	// 对话完成事件缺 conversationID / requestID 时客户端会 skip，面板也不该放行。
	if err := (&Client{}).IntlFirstChatReport(&auth.Auth{}, "", "req"); err == nil {
		t.Error("缺 conversationID 时应报错")
	}
	if err := (&Client{}).IntlFirstChatReport(&auth.Auth{}, "conv", ""); err == nil {
		t.Error("缺 requestID 时应报错")
	}
}

// TestIntlChecklistClientHint 覆盖全部国际版 checklist 任务码（面板指引不能漏项）。
func TestIntlChecklistClientHint(t *testing.T) {
	for _, code := range []string{IntlTaskFirstChat, IntlTaskSkillInstalled,
		IntlTaskWechatLinked, IntlTaskExpertSummoned, IntlTaskTemplateUsed} {
		h, ok := IntlChecklistClientHint[code]
		if !ok || strings.TrimSpace(h) == "" {
			t.Errorf("任务码 %s 缺客户端操作指引", code)
		}
		if !IntlChecklistIsClientOnly(code) {
			t.Errorf("任务码 %s 应标记为 client-only", code)
		}
	}
}

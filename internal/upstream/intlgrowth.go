// intlgrowth.go 国际版（workbuddy.ai）「成长路径」checklist 的事件口径。
//
// ⚠️ 2026-11-17 实测更正（推翻 2026-10-15 的旧结论「国际版成长路径网关代做不了、
// 4 项动作均上报成功但进度未变化」）：
//
// 旧结论把「上报成功」当成了「事件被上游接收」，但国际版 report 端点对事件有
// 强校验——**缺 reportDelay 或 timestamp 直接 400 丢弃**：
//
//	POST https://www.workbuddy.ai/v2/report
//	→ {"code":10001,"msg":"10001:event missing both reportDelay and timestamp fields"}
//
// 面板的桌面对话事件链（DesktopChatSequence，5.5.6 schema）恰好两个字段都没有，
// 于是「HTTP 200」其实是被丢弃 —— 事件从未进入计分。这是旧结论里最硬的一个坑，
// 已由 desktopFingerprint 补齐 reportDelay/timestamp 修复。
//
// 国际版客户端（WorkBuddy AI 5.9.4，app.asar 实测）的真实口径是：
//
//  1. 双通道：StandardEventService → /v2/report（msg=OK）
//              ModelEventServiceImpl → /llm/data/report（msg=success）
//  2. 事件公共字段由 processor 自动注入：eventCode/timestamp/reportDelay、
//     product/releaseDate/commit、userId/username/userNickname、clientIp、
//     machineId/sessionId/ideName/ideType/ideVersion/extName/extVersion/os/arch。
//  3. checklist 任务码**本身就是客户端 eventCode**，且必须带业务字段：
//     - skill_installed   → {name, skillId, skillVersion}
//     - expert_summoned   → {id, name, expertTitle, type, version}
//     - template_used     → {template_id, task_mode}
//     - chat_response_complete → {conversationId, requestId, artifactType}
//     （聊天侧另有 chat_request_send / chat_request_response 等）
//
// 实测现状（2026-11-17，国际版账号 7783f80b）：
//   - 上述事件按客户端口径上报，/v2/report 与 /llm/data/report **均返回成功**；
//   - 但 checklist 的 status 仍为 available，profile 仍 completed=0/5、first_visit=true。
//   即：事件通道已修通，服务端对国际版 checklist 的计分还有额外判据（纯 API 未能点亮）。
//   面板因此不能对国际版 checklist 承诺「一键完成」——见 panel 侧的 client 分类。
package upstream

import (
	"fmt"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 国际版 checklist 的任务码（与客户端事件码同名）。
const (
	IntlTaskFirstChat      = "first_chat"
	IntlTaskSkillInstalled = "skill_installed"
	IntlTaskWechatLinked   = "wechat_linked"
	IntlTaskExpertSummoned = "expert_summoned"
	IntlTaskTemplateUsed   = "template_used"
)

// IntlChecklistEvent 构造一个国际版 checklist 事件（任务码即 eventCode）。
//
// business 为客户端口径的业务字段（如 skillId / template_id）。缺业务字段的裸
// eventCode 上游虽返回成功，但**不计分**（实测：裸 eventCode 三项全部未点亮）。
func IntlChecklistEvent(code string, business map[string]any) DesktopEvent {
	ev := DesktopEvent{"eventCode": code}
	for k, v := range business {
		ev[k] = v
	}
	return ev
}

// IntlSkillInstalledEvent 技能安装事件（客户端口径：{name, skillId, skillVersion}）。
func IntlSkillInstalledEvent(name, skillID, version string) DesktopEvent {
	return IntlChecklistEvent(IntlTaskSkillInstalled, map[string]any{
		"name": name, "skillId": skillID, "skillVersion": version,
	})
}

// IntlExpertSummonedEvent 专家召唤事件（客户端口径：{id, name, expertTitle, type, version}）。
func IntlExpertSummonedEvent(id, name, expertTitle, expertType, version string) DesktopEvent {
	return IntlChecklistEvent(IntlTaskExpertSummoned, map[string]any{
		"id": id, "name": name, "expertTitle": expertTitle,
		"type": expertType, "version": version,
	})
}

// IntlTemplateUsedEvent 模板使用事件（客户端口径：{template_id, task_mode}）。
//
// 注意字段名是**下划线** template_id（客户端实测如此，不是 camelCase）。
func IntlTemplateUsedEvent(templateID, taskMode string) DesktopEvent {
	if taskMode == "" {
		taskMode = "coding"
	}
	return IntlChecklistEvent(IntlTaskTemplateUsed, map[string]any{
		"template_id": templateID, "task_mode": taskMode,
	})
}

// IntlChatResponseCompleteEvent 对话完成事件（客户端口径：
// {conversationId, requestId, artifactType}）。客户端缺 conversationId / requestId
// 会直接 skip 不上报，故两者必填。
func IntlChatResponseCompleteEvent(conversationID, requestID string) DesktopEvent {
	return IntlChecklistEvent("chat_response_complete", map[string]any{
		"conversationId": conversationID, "requestId": requestID, "artifactType": "",
	})
}

// ReportIntlChecklist 上报国际版 checklist 事件（双通道：/v2/report + /llm/data/report）。
//
// 返回两条通道各自的错误；只要有一条成功即视为「事件已送达上游」，
// 但**送达 ≠ 计分** —— 是否点亮以面板回读任务状态为准（见 panel 侧回读校验）。
func (c *Client) ReportIntlChecklist(a *auth.Auth, events ...DesktopEvent) error {
	if len(events) == 0 {
		return fmt.Errorf("intl checklist report: no events")
	}
	return c.ReportDesktopEventAll(a, events...)
}

// IntlFirstChatReport 对话完成事件上报（国际版 first_chat 判据尝试）。
//
// conversationID 必须来自**真实发起的对话**（服务端侧有该会话记录才可能对上），
// requestID 为该轮的请求标识。
func (c *Client) IntlFirstChatReport(a *auth.Auth, conversationID, requestID string) error {
	if conversationID == "" || requestID == "" {
		return fmt.Errorf("intl first chat report: conversationID/requestID 必填")
	}
	return c.ReportIntlChecklist(a, IntlChatResponseCompleteEvent(conversationID, requestID))
}

// IntlChecklistClientHint 国际版 checklist 每一项在客户端里的真实操作指引。
//
// 面板在「客户端完成」分类里逐项展示：用户照做 → 面板回读自动核销。
var IntlChecklistClientHint = map[string]string{
	IntlTaskFirstChat:      "在 WorkBuddy 客户端里发一次对话（任意内容），完成后回到面板刷新",
	IntlTaskSkillInstalled: "在 WorkBuddy 客户端的技能市场安装任意一个技能（装完即算）",
	IntlTaskWechatLinked:   "在 WorkBuddy 客户端设置里链接微信账号",
	IntlTaskExpertSummoned: "在 WorkBuddy 客户端召唤任意一个专家并跟它聊一句",
	IntlTaskTemplateUsed:   "在 WorkBuddy 客户端用任意一个模板发起任务",
}

// IntlChecklistIsClientOnly 报告该任务码是否为「必须在客户端完成」的成长路径项。
func IntlChecklistIsClientOnly(code string) bool {
	_, ok := IntlChecklistClientHint[code]
	return ok
}

// IntlGrowthState 读取国际版成长路径总览（completed/total/level）。
//
// 端点 /v2/activity/growth/profile 与 /activity/growth/profile 等价（国际版两套
// 路径风格都存在，实测返回一致）；国服该端点 404。
func (c *Client) IntlGrowthState(a *auth.Auth) (*GrowthProfile, error) {
	return c.GrowthProfile(a)
}


// intltask.go 国际版（workbuddy.ai）成长路径 checklist 的面板动作。
//
// 设计原则（2026-11-17 实测后确立）：
//
//  1. **不做完成承诺**。国际版 checklist 的计分判据在服务端，纯 API 上报实测点不亮
//     （/v2/report 与 /llm/data/report 均返回成功，但 status 仍 available、
//     profile completed=0/5）。所以面板只做两件事：
//     a) 按**客户端原样口径**上报事件（任务码即 eventCode + 真实业务字段 + 双通道），
//        上游将来放开计分时即可自动点亮；
//     b) 逐项给出「在 WorkBuddy 客户端怎么完成」的操作指引，用户做完后**面板回读
//        自动核销**（回读判定在 taskverify.go，未点亮一律 unverified/partial，
//        绝不标 done）。
//
//  2. **诚实优先**。动作返回 nil error 只代表「事件送达上游」，不代表「任务完成」；
//     完成与否一律以 runPlanAction 的回读结果为准。
package panel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// intlClientHintText 取出某任务的客户端操作指引。
func intlClientHintText(code string) string {
	if h, ok := upstream.IntlChecklistClientHint[code]; ok {
		return h
	}
	return "需在 WorkBuddy 客户端完成该操作"
}

// intlReportMsg 组装「已上报但服务端未计分」的诚实文案。
func intlReportMsg(code, extra string) string {
	msg := fmt.Sprintf("已按 WorkBuddy 客户端口径上报 %s 事件（上游返回成功）", code)
	if extra != "" {
		msg += "：" + extra
	}
	msg += "；但成长路径仍不计分（实测服务端另有计分判据，纯 API 点不亮）——" +
		intlClientHintText(code) + "，完成后回面板刷新即可自动核销"
	return msg
}

// runIntlFirstChat 完成一次对话（国际版 first_chat）。
//
// 口径对齐客户端：真实发起一次对话（带 conversation_id），拿到服务端会话 id 后
// 上报 chat_response_complete{conversationId, requestId, artifactType}（客户端缺
// 这两个字段会直接 skip 不上报）。
func runIntlFirstChat(p *Panel, a *auth.Auth) (string, error) {
	ms := time.Now().UnixMilli()
	conv := fmt.Sprintf("wb2api-intl-chat-%d", ms)
	reqID := fmt.Sprintf("wb2api-intl-chat-req-%d", ms)
	body, _ := json.Marshal(map[string]any{
		"model": "default-model",
		"messages": []map[string]any{
			{"role": "user", "content": "hello, reply in one sentence."},
		},
		"agent":          "craft",
		"temperature":    1,
		"stream":         true,
		"conversation_id": conv,
	})
	rc, status, respBody, err := p.cfg.Upstream.ChatStream(a, body, "", upstream.ChatMeta{
		ConversationID:        conv,
		ConversationRequestID: reqID,
	})
	if err != nil {
		return "", fmt.Errorf("对话请求: %w", err)
	}
	serverID := ""
	if status >= 400 {
		rc.Close()
		return "", fmt.Errorf("对话失败 http=%d: %s", status, truncateStr(string(respBody), 160))
	}
	// 读干 SSE（顺带抓服务端会话 id）。
	buf := make([]byte, 0, 1<<18)
	tmp := make([]byte, 8192)
	for {
		n, rerr := rc.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > 1<<20 {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	rc.Close()
	if m := regexp.MustCompile(`"id":"([0-9a-f]{8,64})"`).FindStringSubmatch(string(buf)); m != nil {
		serverID = m[1]
	}
	reportID := serverID
	if reportID == "" {
		reportID = reqID
	}
	if err := p.cfg.Upstream.IntlFirstChatReport(a, conv, reportID); err != nil {
		return "已完成一次真实对话，但事件上报失败：" + err.Error(), nil
	}
	extra := fmt.Sprintf("对话会话 %s", conv)
	if serverID != "" {
		extra += fmt.Sprintf("（服务端 id %s）", serverID)
	}
	return intlReportMsg("chat_response_complete", extra), nil
}

// runIntlExpertSummoned 召唤一次专家（国际版 expert_summoned）。
//
// 专家 id 必须取**市场真实存在**的专家（编造 id 不计数）；国际版市场接口
// /portal/operation-platform/market/expert/list 在 workbuddy.ai 上实测可用。
func runIntlExpertSummoned(p *Panel, a *auth.Auth) (string, error) {
	experts, err := p.cfg.Upstream.MarketExpertList(a, "agent")
	if err != nil || len(experts) == 0 {
		// 退一步用关键词搜索（国际版实测可用）。
		experts, err = p.cfg.Upstream.MarketExpertSearch(a, "写作")
	}
	if err != nil {
		return "", fmt.Errorf("取专家市场列表: %w", err)
	}
	if len(experts) == 0 {
		return "未取到可用专家（市场接口无数据），" + intlClientHintText(upstream.IntlTaskExpertSummoned), nil
	}
	e := experts[0]
	ev := upstream.IntlExpertSummonedEvent(e.ExpertID, e.DisplayNameZH, e.ProfessionZH, e.ExpertType, e.Version)
	if err := p.cfg.Upstream.ReportIntlChecklist(a, ev); err != nil {
		return "已取到专家，但事件上报失败：" + err.Error(), nil
	}
	return intlReportMsg("expert_summoned",
		fmt.Sprintf("专家 %s（%s）", e.DisplayNameZH, e.ExpertID)), nil
}

// runIntlSkillInstalled 完成技能安装（国际版 skill_installed）。
//
// 客户端口径 payload 为 {name, skillId, skillVersion}。国际版没有对网关开放的
// 技能列表接口（/v2/user-asset/skill、/api/v2/open-platform/skills 实测 404），
// 因此这里上报客户端口径的最小业务字段（name/skillId 留空由上游忽略），
// 并如实提示需在客户端安装。
func runIntlSkillInstalled(p *Panel, a *auth.Auth) (string, error) {
	ev := upstream.IntlChecklistEvent(upstream.IntlTaskSkillInstalled, map[string]any{})
	if err := p.cfg.Upstream.ReportIntlChecklist(a, ev); err != nil {
		return "事件上报失败：" + err.Error(), nil
	}
	return intlReportMsg("skill_installed", "国际版无对外技能列表接口，未取到真实 skillId"), nil
}

// runIntlTemplateUsed 使用一次模板（国际版 template_used）。
//
// 客户端口径 payload 为 {template_id, task_mode}；国际版模板列表端点
// /v3/templates 实测 404，故同样上报最小口径并提示在客户端完成。
func runIntlTemplateUsed(p *Panel, a *auth.Auth) (string, error) {
	ev := upstream.IntlChecklistEvent(upstream.IntlTaskTemplateUsed, map[string]any{})
	if err := p.cfg.Upstream.ReportIntlChecklist(a, ev); err != nil {
		return "事件上报失败：" + err.Error(), nil
	}
	return intlReportMsg("template_used", "国际版无对外模板列表接口，未取到真实 template_id"), nil
}

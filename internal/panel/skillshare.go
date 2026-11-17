// skillshare.go —— 成长任务 skill_share（「分享一个技能给好友」）的自动动作。
//
// 2026-11-17 实测（此前该任务在面板里被归 manual 桶，实际可纯 API 完成）：
//
//	accept（报名）→ 取真实市场技能 → 服务端创建分享链接 → 上报 skill_share_channel_action
//
// 实测要点：
//   - 不 accept 时上游 target=0，事件不计分（accept 后 target 变 1）；
//   - skillId 必须是**真实市场技能**（/portal/operation-platform/market/skill/list），
//     编造的 id 不计数；
//   - 分享链接必须走服务端接口 /api/v2/open-platform/asset-shares/c2c/channels
//     （asset_ref 是 {"asset_id":…} 的 JSON **字符串**，用 assetId/对象形式会被拒）；
//   - 判据事件只认 action=wechat + panelAction=success（panelAction=click 不计分）。
package panel

import (
	"fmt"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// runSkillShare 完成 skill_share（分享一个技能给好友，+50c +5e）。
func runSkillShare(p *Panel, a *auth.Auth) (string, error) {
	const code = "skill_share"

	// 1) 报名（幂等）：不 accept 时上游 target=0，行为事件不计分。
	if err := p.cfg.Upstream.AcceptTasks(a, []string{code}); err != nil {
		return "", fmt.Errorf("接受任务: %w", err)
	}
	time.Sleep(reportGap)

	// 2) 取一个真实市场技能（编造的 skillId 不计数）。
	skills, err := p.cfg.Upstream.MarketSkillList(a)
	if err != nil {
		return "", fmt.Errorf("取市场技能: %w", err)
	}
	skill := skills[0]

	// 3) 服务端创建分享链接（真实留痕，返回 channel_id）。
	ch, err := p.cfg.Upstream.CreateSkillShareChannel(a, skill)
	if err != nil {
		return "", fmt.Errorf("创建分享链接: %w", err)
	}

	// 4) 上报判据事件（桌面指纹）。
	ev := upstream.DesktopSkillShareChannelAction(skill, ch.ChannelID)
	events := []upstream.DesktopEvent{ev}

	// 5) 真实设备指纹兜底：本机装了 WorkBuddy 客户端时，用它的 machineId / qimei36
	//    再报一份（与真实客户端口径一致）。任务 target=1，重复上报不会重复计分；
	//    读不到设备标识就只报标准指纹一份。
	if mid, qimei := upstream.RealDeviceIDs(); mid != "" {
		aligned := upstream.DesktopSkillShareChannelAction(skill, ch.ChannelID)
		aligned["machineId"] = mid
		aligned["actionAt"] = time.Now().UnixMilli()
		if qimei != "" {
			aligned["qimei36"] = qimei
		}
		events = append(events, aligned)
	}

	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", fmt.Errorf("上报分享事件: %w", err)
	}
	return fmt.Sprintf("已创建分享链接（技能 %s，channel_id=%s）并上报 skill_share_channel_action（微信分享成功口径）",
		skill.DisplayName(), ch.ChannelID), nil
}

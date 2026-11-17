// market_skill.go —— 国服「技能分享」能力（成长任务 skill_share 的判据链路）。
//
// 背景（2026-11-17 实测）：
// 成长任务 `skill_share`（「分享一个技能给好友」，+50c +5e）此前在面板里被归到
// manual 桶（无自动动作），实测**可以纯 API 完成**，判据链是：
//
//  1. accept（报名）——不 accept 时上游 target=0，事件不计分；accept 后 target 变 1。
//  2. 取一个**真实市场技能**（/portal/operation-platform/market/skill/list）——
//     编造的 skillId 不计数（与 expert_5 的真实专家 id 同理）。
//  3. 服务端创建分享链接（/api/v2/open-platform/asset-shares/c2c/channels）——
//     服务端留痕，返回 channel_id / share_code。
//  4. 上报判据事件 `skill_share_channel_action`（桌面指纹，/v2/report）。
//
// ⚠️ asset_ref 必须是**字符串**且键名是 `asset_id`（snake_case）：
//
//	{"asset_kind":"skill","asset_source":"market",
//	 "asset_ref":"{\"asset_id\":\"skill_xxx\"}","ways":["link","wxacode","wxapath"]}
//
// 实测：用对象 {"assetId": ...} 或键名 assetId 一律被上游拒为「请求参数不合法」
// （code 100001）；改成 {"asset_id": ...} 的 JSON 字符串即 code 0。
//
// ⚠️ 判据事件的通道口径（客户端 asar 逆向 + 2026-11-17 实测）：必须是
// action=wechat 且 panelAction=success（isSuccessful=true）。实测对照：
// panelAction=click 不计分，panelAction=success 才把 current 顶到 1/1 completed。
package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

const (
	// marketSkillListPath 国服技能市场列表（POST）。
	marketSkillListPath = "/portal/operation-platform/market/skill/list"
	// assetShareChannelsPath C2C 分享通道（POST），创建技能的分享链接。
	assetShareChannelsPath = "/api/v2/open-platform/asset-shares/c2c/channels"
)

// MarketSkill 市场技能（/portal/operation-platform/market/skill/list 响应子集）。
type MarketSkill struct {
	SkillID       string `json:"skill_id"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	DisplayNameZH string `json:"display_name_zh"`
	Icon          string `json:"icon"`
	Source        string `json:"source"`
}

// DisplayName 展示名（中文优先，回落英文标识）。
func (s MarketSkill) DisplayName() string {
	if s.DisplayNameZH != "" {
		return s.DisplayNameZH
	}
	return s.Name
}

// MarketSkillList 拉取国服市场真实技能列表（成长任务 skill_share 的素材来源）。
//
// 2026-11-17 实测：GET 该路径返回 404 page not found，**必须 POST**；
// 体 {page, page_size, sort_by, sort_order} 有效，返回 data.skills[]（total_count 62002+）。
func (c *Client) MarketSkillList(a *auth.Auth) ([]MarketSkill, error) {
	body := map[string]any{"page": 1, "page_size": 5, "sort_by": "reco_rank", "sort_order": "desc"}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.chatBase(a)+marketSkillListPath, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("X-Domain", c.chatBase(a))
	req.Header.Set("X-Product", "SaaS")
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	// doJSON 已解信封并保证 code==0，返回的是 data 段本身。
	var resp struct {
		Skills []MarketSkill `json:"skills"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("技能市场列表解析失败: %w", err)
	}
	if len(resp.Skills) == 0 {
		return nil, fmt.Errorf("技能市场未返回任何技能（code=0 但 data.skills 为空）")
	}
	return resp.Skills, nil
}

// SkillShareChannel 服务端分享链接（/api/v2/open-platform/asset-shares/c2c/channels 响应子集）。
type SkillShareChannel struct {
	ChannelID string `json:"channel_id"`
	ShareCode string `json:"share_code"`
	Sig       string `json:"sig"`
}

// CreateSkillShareChannel 为一个市场技能创建服务端分享链接（真实留痕）。
//
// 2026-11-17 实测：asset_ref 必须是 JSON **字符串**且键名为 asset_id；
// 用 {"assetId": ...} 或对象形式一律被拒为「请求参数不合法」。
func (c *Client) CreateSkillShareChannel(a *auth.Auth, skill MarketSkill) (*SkillShareChannel, error) {
	raw, err := json.Marshal(buildSkillShareBody(skill))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.chatBase(a)+assetShareChannelsPath, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("X-Domain", c.chatBase(a))
	req.Header.Set("X-Product", "SaaS")
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	// doJSON 已解信封并保证 code==0（上游拒绝时这里直接返回 *Error）。
	var resp SkillShareChannel
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("分享链接响应解析失败: %w", err)
	}
	if resp.ChannelID == "" {
		return nil, fmt.Errorf("创建分享链接失败: 上游未返回 channel_id")
	}
	return &resp, nil
}

// buildSkillShareBody 构造创建分享链接的请求体。
//
// ⚠️ asset_ref 必须是 JSON **字符串**且键名是 `asset_id`（snake_case）。
// 实测（2026-11-17）：对象形式或 assetId 键名一律被上游拒为「请求参数不合法」
// （code 100001）；改成 {"asset_id":"skill_xxx"} 的字符串后返回 code 0。
func buildSkillShareBody(skill MarketSkill) map[string]any {
	ref, _ := json.Marshal(map[string]string{"asset_id": skill.SkillID})
	return map[string]any{
		"asset_kind":   "skill",
		"asset_source": "market",
		"asset_ref":    string(ref), // 字符串，不是对象
		"ways":         []string{"link", "wxacode", "wxapath"},
	}
}

// DesktopSkillShareChannelAction 构造「技能分享」判据事件。
//
// 客户端口径（asar 逆向）：技能详情页点「分享」→ 选渠道 → 上报
// skill_share_channel_action {skillId, skillName, skillType, skillSource, action,
// panelAction, panelType, isSuccessful, cost, id, actionAt}。
//
// ⚠️ 实测计分只认 **action=wechat + panelAction=success**：
// panelAction=click 不计分（2026-11-17 两账号对照实测）。
func DesktopSkillShareChannelAction(skill MarketSkill, channelID string) DesktopEvent {
	return DesktopEvent{
		"eventCode":      "skill_share_channel_action",
		"skillId":        skill.SkillID,
		"skillName":      skill.DisplayName(),
		"skillType":      "market",
		"skillSource":    "market",
		"action":         "wechat",
		"panelAction":    "success",
		"panelType":      "mini_program_card",
		"isSuccessful":   true,
		"cost":           0,
		"id":             channelID,
		"actionAt":       time.Now().UnixMilli(),
		"entry":          "skill_detail",
		"status":         "safe",
		"actionSuccessful": true,
	}
}

// market_skill_test.go —— 锁死「分享技能」判据链的实测口径。
//
// 依据：2026-11-17 国服两账号实测（skill_share 从 0/1 → 1/1 completed）。
// 这些断言防的是「凭印象改口径」——asset_ref 的键名/形态、事件通道与
// panelAction 的取值一旦被改回旧写法，任务就会静默不计分。
package upstream

import (
	"encoding/json"
	"testing"
)

func TestSkillShareBodyAssetRefIsStringWithAssetID(t *testing.T) {
	sk := MarketSkill{SkillID: "skill_2095070772061040640"}
	body := buildSkillShareBody(sk)

	ref, ok := body["asset_ref"].(string)
	if !ok {
		t.Fatalf("asset_ref 必须是 JSON 字符串（实测对象形式会被拒），实际类型 %T", body["asset_ref"])
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(ref), &m); err != nil {
		t.Fatalf("asset_ref 不是合法 JSON 字符串: %v", err)
	}
	if m["asset_id"] != sk.SkillID {
		t.Fatalf("asset_ref 键名必须是 asset_id（实测 assetId 会被拒），实际 %v", m)
	}
	if _, bad := m["assetId"]; bad {
		t.Fatal("asset_ref 不得使用 assetId 键名")
	}
	if body["asset_kind"] != "skill" || body["asset_source"] != "market" {
		t.Fatalf("asset_kind/asset_source 不对: %v", body)
	}
	ways, _ := body["ways"].([]string)
	if len(ways) == 0 {
		t.Fatal("ways 不能为空")
	}
}

func TestDesktopSkillShareChannelActionWechatSuccess(t *testing.T) {
	sk := MarketSkill{SkillID: "skill_1", Name: "n", DisplayNameZH: "展示名", Version: "1.0.0"}
	ev := DesktopSkillShareChannelAction(sk, "585490")

	if ev["eventCode"] != "skill_share_channel_action" {
		t.Fatalf("eventCode 错: %v", ev["eventCode"])
	}
	// 实测只认 action=wechat + panelAction=success；click 不计分。
	if ev["action"] != "wechat" {
		t.Fatalf("action 必须是 wechat: %v", ev["action"])
	}
	if ev["panelAction"] != "success" {
		t.Fatalf("panelAction 必须是 success（click 实测不计分）: %v", ev["panelAction"])
	}
	if ev["panelType"] != "mini_program_card" {
		t.Fatalf("panelType 错: %v", ev["panelType"])
	}
	if ev["isSuccessful"] != true {
		t.Fatalf("isSuccessful 必须为 true: %v", ev["isSuccessful"])
	}
	if ev["skillId"] != sk.SkillID {
		t.Fatalf("skillId 必须用真实 skill_id: %v", ev["skillId"])
	}
	if ev["skillName"] != "展示名" {
		t.Fatalf("skillName 应取展示名: %v", ev["skillName"])
	}
	if ev["id"] != "585490" {
		t.Fatalf("id 应为服务端 channel_id: %v", ev["id"])
	}
}

func TestMarketSkillDisplayNameFallback(t *testing.T) {
	if got := (MarketSkill{Name: "abc"}).DisplayName(); got != "abc" {
		t.Fatalf("无中文名时回落英文标识，实际 %q", got)
	}
	if got := (MarketSkill{Name: "abc", DisplayNameZH: "中文"}).DisplayName(); got != "中文" {
		t.Fatalf("有中文名时应优先，实际 %q", got)
	}
}

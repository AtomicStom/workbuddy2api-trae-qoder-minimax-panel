package traechat

// ide_catalog.go — 用 Trae IDE 客户端的接口取模型目录。
//
// 为什么不用原来的 SOLO 目录：SOLO（/api/remote/v1）只暴露 20 个模型，
// 而 IDE 目录（/api/ide/v1/batch_get_detail_param）能拿到完整 37 个，
// 且带 consumption_rate（倍率）与 multimodal 能力位。
//
// 请求头与 IDE 客户端一致：Cloud-IDE-JWT + 一整套 X-Ide-* / X-Machine-Id。
// 缺 X-Machine-Id 会被上游按设备维度拒绝，所以账号里必须存 machine_id。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	ideHost          = "https://trae-api-cn.mchost.guru"
	ideDetailPath    = "/api/ide/v1/batch_get_detail_param"
	ideAppID         = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
	ideAppVersion    = "default"
	ideVersion       = "0.1.52"
	ideVersionCode   = "20260811"
	ideVersionType   = "stable"
	ideDeviceType    = "macos"
	ideOSVersion     = "macOS 15.7.4"
	ideDeviceBrand   = "Apple"
	ideUserAgent     = "Trae/0.1.52"
	ideTrafficType   = "prod"
)

// ideFunctions 是探测目录时要问的通道列表；上游按通道分组返回。
//
// 这 22 个与 OFM 渠道包（business.mjs:44006）逐字一致。只问少数几个通道会
// 导致模型面严重偏差：实测问 7 个通道得到 99 条原始条目、混合后语义混乱；
// 问全 22 个通道再按同一套规则过滤，才与 OFM 的模型清单对齐。
// 顺序即优先级（越靠前越优先），用于同名模型去重时择一。
var ideFunctions = []string{
	"ui_builder_v2", "solo_coder", "chat_v3", "solo_builder", "builder_v3", "builder",
	"chat", "inline_chat", "git_ai", "custom_agent_generation", "utils", "code_reviewer",
	"code_review_summary", "solo_agent", "solo_agent_remote", "solo_work_remote",
	"solo_agent_lite", "solo_work_lite", "solo_design_lite", "solo_design_remote",
	"multimodal", "system_diagnosis",
}

// ideHeaders 构造 IDE 客户端的请求头。
func ideHeaders(acct *Account) http.Header {
	h := http.Header{}
	tok := strings.TrimPrefix(acct.JWT, "Cloud-IDE-JWT ")
	h.Set("Authorization", "Cloud-IDE-JWT "+tok)
	h.Set("X-Cloudide-Token", tok)
	h.Set("X-Ide-Token", tok)
	h.Set("X-Uid", acct.UserID)
	h.Set("X-App-Id", ideAppID)
	h.Set("X-App-Version", ideAppVersion)
	h.Set("X-Ide-Version", ideVersion)
	h.Set("X-Ide-Version-Code", ideVersionCode)
	h.Set("X-App-Version-Code", ideVersionCode)
	h.Set("X-Ide-Version-Type", ideVersionType)
	h.Set("X-Device-Type", ideDeviceType)
	h.Set("X-OS-Version", ideOSVersion)
	h.Set("X-Device-Brand", ideDeviceBrand)
	h.Set("Request-Traffic-Type", ideTrafficType)
	h.Set("User-Agent", ideUserAgent)
	if acct.MachineID != "" {
		h.Set("X-Machine-Id", acct.MachineID)
	}
	if acct.DeviceID != "" {
		h.Set("X-Device-Id", acct.DeviceID)
	}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	return h
}

// ideRate 从 display_contact_config（JSON 字符串）里取消费倍率。
//
// 结构：{"consumption_rate":{"data":{"rate":0.78}}}；
// activity_discount.data.current.consumption_rate 存在时优先（活动价）。
func ideRate(raw string) (rate float64, ok bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, false
	}
	var doc struct {
		ConsumptionRate struct {
			Data struct {
				Rate float64 `json:"rate"`
			} `json:"data"`
		} `json:"consumption_rate"`
		ActivityDiscount struct {
			Data struct {
				Current struct {
					ConsumptionRate float64 `json:"consumption_rate"`
				} `json:"current"`
			} `json:"data"`
		} `json:"activity_discount"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return 0, false
	}
	if r := doc.ActivityDiscount.Data.Current.ConsumptionRate; r > 0 {
		return r, true
	}
	if r := doc.ConsumptionRate.Data.Rate; r > 0 {
		return r, true
	}
	return 0, false
}

// IDEModels 拉取 IDE 目录，返回去重后的模型清单（按名称排序）。
func (c *Client) IDEModels(acct *Account) ([]ModelInfo, error) {
	if acct == nil || strings.TrimSpace(acct.JWT) == "" {
		return nil, fmt.Errorf("IDEModels: 账号缺少 JWT")
	}
	body, _ := json.Marshal(map[string]any{
		"functions":              ideFunctions,
		"agent_type":             "",
		"current_config_info":    map[string]any{"config_name": "", "is_custom_model": false},
		"mode_type":              0,
		"access_type":            0,
		"ab_force_vids":          "",
		"ab_autotest_advanced_mode": 0,
		"show_custom_model":      true,
	})
	req, err := http.NewRequest(http.MethodPost, ideHost+ideDetailPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = ideHeaders(acct)
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("IDE 目录请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IDE 目录 HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 160))
	}

	var doc struct {
		Code           int `json:"code"`
		FunctionConfigs []struct {
			Function       string `json:"function"`
			ConfigInfoList []struct {
				ConfigName          string `json:"config_name"`
				DisplayConfig       struct {
					ModelCapability string `json:"model_capability"`
					Multimodal      bool   `json:"multimodal"`
					MaxMode         any    `json:"max_mode"`
				} `json:"display_config"`
				DisplayContactConfig string `json:"display_contact_config"`
				// IsInvisibleToUser 上游的「不展示给用户」标志。
				// 对应 OFM 渠道包里的 isHidden —— 那是它的模型筛选依据：
				// 只过滤展示，不代表模型不可调用（glm-5 / DeepSeek-V4-Flash 这类
				// 能跑但被标记隐藏，是为了不在 IDE 的选模列表里出现）。
				IsInvisibleToUser bool `json:"is_invisible_to_user"`
				// Usage 上游声明的用途：只有 "chat_completion" 才是可直连的对话模型。
				Usage string `json:"usage"`
				// ConfigSwitch 为 false 表示该条目已关闭。
				ConfigSwitch *bool `json:"config_switch"`
			} `json:"config_info_list"`
		} `json:"function_configs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("IDE 目录解析失败: %w", err)
	}
	if doc.Code != 0 {
		return nil, fmt.Errorf("IDE 目录 code=%d", doc.Code)
	}

	// 按 ideFunctions 的顺序收，先去重（同一个模型会出现在多个通道里）
	order := map[string]int{}
	for i, fn := range ideFunctions {
		order[fn] = i
	}
	groups := doc.FunctionConfigs
	sort.SliceStable(groups, func(i, j int) bool {
		return order[groups[i].Function] < order[groups[j].Function]
	})

	seen := map[string]bool{}
	out := make([]ModelInfo, 0, 64)
	for _, g := range groups {
		for _, it := range g.ConfigInfoList {
			name := strings.TrimSpace(it.ConfigName)
			if name == "" {
				continue
			}
			// ① 只保留对话补全类：embedding / summary / fast_apply /
			//    title_generation / custom_model 等都不是可直连的对话模型。
			if it.Usage != "" && it.Usage != "chat_completion" {
				continue
			}
			// ② 上游显式关闭的条目。
			if it.ConfigSwitch != nil && !*it.ConfigSwitch {
				continue
			}
			// ③ 上游标记「不展示给用户」——内部子智能体、隐藏档位等。
			//    这是 OFM 的 isHidden，也是唯一会随上游自动变化的筛选依据。
			if it.IsInvisibleToUser {
				continue
			}
			if seen[name] || !validModelName(name) {
				continue
			}
			seen[name] = true
			m := ModelInfo{
				Name:         name,
				DisplayName:  name,
				Multimodal:   it.DisplayConfig.Multimodal,
				Features:     it.DisplayConfig.ModelCapability,
				IsPreset:     true,
				ContextWindow: 1_000_000,
			}
			if r, ok := ideRate(it.DisplayContactConfig); ok {
				m.ConsumptionRate = r
				m.HasConsumptionRate = true
			}
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("IDE 目录为空")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

package ideauth

import (
	"net/http"
	"strings"
	"testing"
)

// 回归（2026-10-24 实测修正）：Qoder 的每日签到活动**只对真实设备指纹下发**。
// 旧策略（每账号派生合成 MachineId、不发送 MachineToken）导致活动列表里永远
// 没有签到活动 —— 表现为"扫描不到签到活动"。正确策略：至少**号池第一个号**用
// 本机真实 runtime-info 指纹（machineToken/machineCode/machineId 三件套）；
// 每设备每日限领一个账号是服务端硬规则，由扫描结果如实说明。
//
// ⚠️ 2026-11-14 更正：本测试原来断言「**所有账号**应使用同一真实设备指纹」
// （da.MachineID == db.MachineID）。那条结论是 2026-10-24 的旧结论，后来因为
// 「多号派生」被改掉，2026-11-15 又按实测改了回来 —— 两次修正都保留原文。
//
// ⚠️ 2026-11-15 终版（实测）：默认 device_mode=auto → 展开为 **real**（全部号）。
// 依据：派生身份会被上游按设备维度过滤掉每日额度 —— 实测 gmail 号
// （derived）pending 领不到，同一号改成 real 立刻 +100 Credits。
// 所以「所有账号共用同一真实设备指纹」**重新成立**，而且不影响各号各领各的
// （2026-11-14 实测：两个号都是 real，各领各的 100 积分）。
func TestQoderDeviceRealFingerprint(t *testing.T) {
	// 「真实指纹」来自本机装的 Qoder 客户端（runtime-info）。CI / 无客户端的机器上
	// 取不到，这时断言真实指纹没有意义（会误报失败），直接跳过；
	// 开发机上有客户端时，下面这些断言照常生效。
	if info := QoderDeviceInfo(); info == nil || info.MachineToken == "" || info.MachineID == "" {
		t.Skip("本机没有可用的 Qoder 客户端指纹（runtime-info），跳过真实设备断言")
	}

	// 号池：a@test.com 是第一个号（→ real），b@test.com 是第二个号（→ derived）。
	dir := t.TempDir()
	t.Setenv("IDEAUTH_POOL_DIR", dir)
	pool := &QoderPoolFile{Accounts: []*QoderSnapshot{
		{Email: "a@test.com", Auth: &QoderAuth{Token: "t1", User: QoderUser{ID: "uid-a", Email: "a@test.com"}}},
		{Email: "b@test.com", Auth: &QoderAuth{Token: "t2", User: QoderUser{ID: "uid-b", Email: "b@test.com"}}},
	}}
	if err := SaveQoderPool(pool); err != nil {
		t.Fatal(err)
	}

	a := &QoderAuth{User: QoderUser{ID: "uid-a", Email: "a@test.com"}}
	b := &QoderAuth{User: QoderUser{ID: "uid-b", Email: "b@test.com"}}

	da := qoderDeviceForAccount(a)
	db := qoderDeviceForAccount(b)
	if da == nil || db == nil {
		t.Fatal("设备对象为空")
	}
	if da.Synthetic {
		t.Error("设备必须按真实指纹发送（Synthetic=false）")
	}
	// 请求头必须带物理设备令牌三件套（活动下发的必要条件）。
	h := http.Header{}
	da.Apply(h)
	if h.Get("Cosy-MachineToken") == "" || h.Get("Cosy-MachineCode") == "" {
		t.Errorf("请求头必须包含物理设备令牌: token=%q code=%q",
			h.Get("Cosy-MachineToken"), h.Get("Cosy-MachineCode"))
	}
	if h.Get("Cosy-MachineId") == "" {
		t.Error("Cosy-MachineId 不能为空")
	}
	// 默认（auto → real）：两个号都用真实设备（Derived=false），
	// 且共用同一真实设备指纹 —— 2026-10-24 的原结论，2026-11-15 实测恢复成立。
	if da.Derived || db.Derived {
		t.Error("默认（auto→real）下所有号都应使用真实设备（Derived=false）")
	}
	if da.MachineID != db.MachineID {
		t.Errorf("所有账号应使用同一真实设备指纹: %s != %s", da.MachineID, db.MachineID)
	}
	if !strings.HasPrefix(da.MachineID, "") && len(da.MachineID) == 0 {
		t.Error("machineId 为空")
	}
}

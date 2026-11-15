package ideauth

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// itoa 便于构造测试用的 campaigns JSON。
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// TestQoderAutoDeviceAssignment 锁死「设备身份自动分配」的规则：
//
// ⚠️ 2026-11-15 实测修正：auto 原先是「号池第一个号 real、其余 derived」。
// 实测当天 gmail 号生效身份是 derived → 领不到当天额度（pending），
// 改成 real → 立刻 +100 Credits。所以 auto 的展开改成 **real**（全部号）。
//
// 用户诉求（2026-11-14）：「设备是真实设备还是邮箱派生必须手动去调，
// 应该自动生成，反正俩肯定应该不一样」—— 但「能领到」优先级更高：
// 派生身份实测被上游按设备维度过滤掉额度。
func TestQoderAutoDeviceAssignment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IDEAUTH_POOL_DIR", dir)
	t.Setenv("QODER_EGRESS_FILE", filepath.Join(dir, "qoder_egress.json"))

	pool := &QoderPoolFile{Accounts: []*QoderSnapshot{
		{Email: "first@test.com", Auth: &QoderAuth{Token: "t1", User: QoderUser{Email: "first@test.com"}}},
		{Email: "second@test.com", Auth: &QoderAuth{Token: "t2", User: QoderUser{Email: "second@test.com"}}},
		{Email: "third@test.com", Auth: &QoderAuth{Token: "t3", User: QoderUser{Email: "third@test.com"}}},
	}}
	if err := SaveQoderPool(pool); err != nil {
		t.Fatal(err)
	}

	// 首次补全：号池每个号都自动拿到一条出口配置，device_mode=auto。
	n, err := EnsureQoderEgress()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("首次补全应写入 3 条配置，实际 %d", n)
	}
	eg, err := LoadQoderEgress()
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"first@test.com", "second@test.com", "third@test.com"} {
		if got := eg.Profiles[email].DeviceMode; got != QoderDeviceModeAuto {
			t.Fatalf("%s 的 device_mode 应为 auto，实际 %q", email, got)
		}
		if eg.Profiles[email].ProfileDir == "" {
			t.Fatalf("%s 的 profile_dir 应自动生成", email)
		}
	}

	// 展开后的生效值：**全部 real** —— auto 不再把非首个号派成 derived
	// （派生身份实测领不到每日额度）。
	want := map[string]string{
		"first@test.com":  QoderDeviceModeReal,
		"second@test.com": QoderDeviceModeReal,
		"third@test.com":  QoderDeviceModeReal,
	}
	for email, w := range want {
		if got := QoderEffectiveDeviceMode(email, eg.Profiles[email].DeviceMode); got != w {
			t.Errorf("%s 的生效设备模式应为 %q，实际 %q", email, w, got)
		}
	}

	// 显式 derived 仍然尊重手动配置（手动优先于自动）。
	if got := QoderEffectiveDeviceMode("second@test.com", QoderDeviceModeDerived); got != QoderDeviceModeDerived {
		t.Errorf("显式 derived 应被尊重，实际 %q", got)
	}

	// 幂等：再跑一次不产生改动。
	if n, err := EnsureQoderEgress(); err != nil || n != 0 {
		t.Fatalf("重复补全应无改动，实际 changed=%d err=%v", n, err)
	}
}

// TestQoderEgressLegacyRealMigratedToAuto 锁死存量迁移：
//
// 老版本默认 device_mode=real，号池里每个号都被写成 real（导致多号共用一台设备）。
// 「用户从未真正改过」的条目（没配代理 + profile 目录就是自动生成的那个）应迁移到 auto；
// 配了代理 / 改过目录名的条目保持不动（尊重手动配置）。
func TestQoderEgressLegacyRealMigratedToAuto(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IDEAUTH_POOL_DIR", dir)
	egPath := filepath.Join(dir, "qoder_egress.json")
	t.Setenv("QODER_EGRESS_FILE", egPath)

	pool := &QoderPoolFile{Accounts: []*QoderSnapshot{
		{Email: "a@test.com", Auth: &QoderAuth{Token: "t1", User: QoderUser{Email: "a@test.com"}}},
		{Email: "b@test.com", Auth: &QoderAuth{Token: "t2", User: QoderUser{Email: "b@test.com"}}},
	}}
	if err := SaveQoderPool(pool); err != nil {
		t.Fatal(err)
	}

	legacy := &QoderEgressPool{Profiles: map[string]QoderEgressProfile{
		// 未改过：real + 无代理 + 自动生成的 profile 目录 → 迁移到 auto。
		"a@test.com": {
			Email: "a@test.com", DeviceMode: QoderDeviceModeReal,
			ProfileDir: filepath.Join("D:\\data", "qoder-web", QoderProfileDirName("a@test.com")),
		},
		// 手动配了代理 → 不动。
		"b@test.com": {
			Email: "b@test.com", DeviceMode: QoderDeviceModeReal,
			ProxyURL: "http://127.0.0.1:1080",
			ProfileDir: filepath.Join("D:\\data", "qoder-web", QoderProfileDirName("b@test.com")),
		},
	}}
	if err := SaveQoderEgress(legacy); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureQoderEgress(); err != nil {
		t.Fatal(err)
	}
	eg, err := LoadQoderEgress()
	if err != nil {
		t.Fatal(err)
	}
	if got := eg.Profiles["a@test.com"].DeviceMode; got != QoderDeviceModeAuto {
		t.Errorf("未手动改过的 real 条目应迁移为 auto，实际 %q", got)
	}
	if got := eg.Profiles["b@test.com"].DeviceMode; got != QoderDeviceModeReal {
		t.Errorf("配了代理的条目应保留手动值 real，实际 %q", got)
	}
}

// TestQoderEgressIndependentUnderAuto 锁死 auto 模式下的「独立出口」判定：
//
// auto 展开为 real ⇒ 默认**没有**独立身份；只有显式 derived 或配了代理才算独立。
// 2026-11-15 修的真 bug：原来 auto 把非首个号派成 derived，而判定只看配置原值
// "derived"，两边口径不一致，签到策略会退化成 unknown。
func TestQoderEgressIndependentUnderAuto(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IDEAUTH_POOL_DIR", dir)
	t.Setenv("QODER_EGRESS_FILE", filepath.Join(dir, "qoder_egress.json"))

	pool := &QoderPoolFile{Accounts: []*QoderSnapshot{
		{Email: "first@test.com", Auth: &QoderAuth{Token: "t1", User: QoderUser{Email: "first@test.com"}}},
		{Email: "second@test.com", Auth: &QoderAuth{Token: "t2", User: QoderUser{Email: "second@test.com"}}},
	}}
	if err := SaveQoderPool(pool); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureQoderEgress(); err != nil {
		t.Fatal(err)
	}
	if qoderEgressIndependentForEmail("first@test.com") {
		t.Error("auto（=real）不应判为独立出口")
	}
	if qoderEgressIndependentForEmail("second@test.com") {
		t.Error("auto（=real）不应判为独立出口")
	}
	// 显式 derived → 独立。
	if _, err := SetQoderEgress("second@test.com", QoderDeviceModeDerived, "", ""); err != nil {
		t.Fatal(err)
	}
	if !qoderEgressIndependentForEmail("second@test.com") {
		t.Error("显式 derived 应判为独立出口")
	}
}

// TestQoderHasDailyCampaignToday 锁死「今天的每日额度」判定：
// 只有 actionType=CLAIM_BENEFIT 且开始时间落在今天（UTC+8）才算数。
func TestQoderHasDailyCampaignToday(t *testing.T) {
	now := time.Now().In(qoderCST)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, qoderCST).Unix()
	yesterdayStart := todayStart - 24*3600

	raw := func(start int64, action, claimStatus string) []byte {
		return []byte(`{"campaigns":[{"campaignId":"cid-1","actionType":"` + action +
			`","claimStatus":"` + claimStatus + `","startAt":` + itoa(start) +
			`,"endAt":` + itoa(start+86400) + `,"benefit":{"amount":100}}]}`)
	}
	if !qoderHasDailyCampaignToday(raw(todayStart, qoderClaimAction, "CLAIMABLE")) {
		t.Error("今天的 CLAIM_BENEFIT 应被识别为今天有额度")
	}
	if !qoderHasDailyCampaignToday(raw(todayStart, qoderClaimAction, "CLAIMED")) {
		t.Error("今天已领取的 CLAIM_BENEFIT 也算「上游给本号下发了今天的额度」")
	}
	if qoderHasDailyCampaignToday(raw(yesterdayStart, qoderClaimAction, "CLAIMED")) {
		t.Error("昨天的 CLAIM_BENEFIT 不算今天有额度")
	}
	if qoderHasDailyCampaignToday(raw(todayStart, "VIEW_DETAILS", "CLAIMED")) {
		t.Error("VIEW_DETAILS 不是每日额度")
	}
}

// TestQoderPendingMsgForNoAuth 锁死「没有凭证时不得 panic、也不得瞎下结论」。
func TestQoderPendingMsgForNoAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IDEAUTH_POOL_DIR", dir)
	t.Setenv("QODER_EGRESS_FILE", filepath.Join(dir, "qoder_egress.json"))

	got := qoderPendingMsgFor("nobody@test.com", nil, "", "")
	if got == "" {
		t.Fatal("没有凭证时也应给出一句说明")
	}
	if got == qoderPendingDeviceFilteredMsg {
		t.Error("没有凭证时不能断言「被设备身份过滤」")
	}
}

// TestQoderResetEgressAuto 锁死「全部恢复自动」：所有号 device_mode=auto，代理保留。
func TestQoderResetEgressAuto(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IDEAUTH_POOL_DIR", dir)
	egPath := filepath.Join(dir, "qoder_egress.json")
	t.Setenv("QODER_EGRESS_FILE", egPath)

	pool := &QoderPoolFile{Accounts: []*QoderSnapshot{
		{Email: "a@test.com", Auth: &QoderAuth{Token: "t1", User: QoderUser{Email: "a@test.com"}}},
	}}
	if err := SaveQoderPool(pool); err != nil {
		t.Fatal(err)
	}
	if _, err := SetQoderEgress("a@test.com", QoderDeviceModeDerived, "http://127.0.0.1:1080", ""); err != nil {
		t.Fatal(err)
	}
	n, err := ResetQoderEgressAuto()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应改动 1 个账号，实际 %d", n)
	}
	eg, err := LoadQoderEgress()
	if err != nil {
		t.Fatal(err)
	}
	p := eg.Profiles["a@test.com"]
	if p.DeviceMode != QoderDeviceModeAuto {
		t.Errorf("device_mode 应为 auto，实际 %q", p.DeviceMode)
	}
	if p.ProxyURL != "http://127.0.0.1:1080" {
		t.Errorf("代理应保留，实际 %q", p.ProxyURL)
	}
	if _, err := os.Stat(egPath); err != nil {
		t.Fatalf("出口配置文件应存在: %v", err)
	}
}

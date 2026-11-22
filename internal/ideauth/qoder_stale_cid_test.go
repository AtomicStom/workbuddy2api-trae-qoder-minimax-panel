package ideauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestQoderStaleSnapshotCidNotAlready 契约回归（2026-11-22 真实事故）：
//
// 号池快照里存着**上次领取**的活动实例 id（ClaimCID），可能是昨天/更早的。
// 11-19 起连续四天，pending 二次判定拿这个陈旧 cid 去只读探测，服务端如实回
// status=CLAIMED + **当时的** claimedAt，qoderPersonVerdict 见 CLAIMED 即判
// Owner —— 待签号被标成「今日已领」：
//   - MarkCheckedIn 每轮刷新 Entries（面板整天假「已签到」）；
//   - MarkClaimedAt 把 Claims 回拨到旧日；
//   - 账本记录用陈旧 claimedAt 算日期，合并进历史条目 → 当天审计里这个号凭空消失。
//
// 修复后必须满足：claimedAt **确凿陈旧**（能解析且不是今天）⇒ 结论降为 Unknown，
// 号保持 pending、不写任何签到记录、账本条目落在**今天**。
func TestQoderStaleSnapshotCidNotAlready(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本项目面向 Windows（runtime-info.exe 指纹）")
	}
	t.Setenv("IDEAUTH_POOL_DIR", t.TempDir())
	if sys := os.Getenv("SystemRoot"); sys != "" {
		t.Setenv("QODER_UMID_EXE", filepath.Join(sys, "System32", "cmd.exe"))
	}

	// 陈旧领取时刻：距现在 26 小时（跨过 UTC+8 自然日，任何时刻跑都不会撞「今天」）。
	staleClaimedAt := time.Now().In(qoderCST).Add(-26 * time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == qoderPathCampaigns:
			// 今天的每日额度：上游没给这两个号下发（真实事故里连续四天的形态）。
			_, _ = w.Write([]byte(`{"campaigns":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/sash/api/v1/me/campaigns/cid-old/reward":
			// 两个号当时都真领过这条**旧**活动：服务端如实回 CLAIMED + 旧领取时刻。
			// 这正是旧代码误判 Owner 的毒源。
			fmt.Fprintf(w, `{"grantId":"grant-old","status":"CLAIMED","campaignId":"cid-old","claimedAt":%q,"benefit":{"amount":100}}`, staleClaimedAt)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("QODER_OPENAPI_BASE", srv.URL)

	// A 的快照里存着那条旧活动的 cid（11-18 真实数据形态：有 ClaimCID）；
	// B 没有（QQ 号的真实形态：无 ClaimCID）。
	pool := &QoderPoolFile{Accounts: []*QoderSnapshot{
		{Email: "a@test.com", UserID: "ua", ClaimCID: "cid-old", Auth: &QoderAuth{Token: "tok-a", User: QoderUser{Email: "a@test.com", ID: "ua"}}},
		{Email: "b@test.com", UserID: "ub", Auth: &QoderAuth{Token: "tok-b", User: QoderUser{Email: "b@test.com", ID: "ub"}}},
	}}
	if err := SaveQoderPool(pool); err != nil {
		t.Fatal(err)
	}

	out, err := QoderCheckinPool(nil)
	if err != nil {
		t.Fatalf("QoderCheckinPool: %v", err)
	}
	if out.Already != 0 {
		t.Fatalf("陈旧 cid 的 CLAIMED 不得产生 already（今日没领就是没领），实际 Already=%d：%+v", out.Already, out.Results)
	}
	for _, r := range out.Results {
		if r.Phase != QoderPhasePending {
			t.Errorf("%s 应保持 pending，实际 %+v", r.Email, r)
		}
		if r.CheckedInToday {
			t.Errorf("%s 不得显示「今日已签到」：%+v", r.Email, r)
		}
	}

	// 不写任何「已签」记录：Entries 与 Claims 都不得出现这两个号。
	for _, email := range []string{"a@test.com", "b@test.com"} {
		if _, ok := CheckedInAt("qoder", email); ok {
			t.Errorf("%s 被误写签到记录（假签到）", email)
		}
		if c, ok := LastClaimedAt("qoder", email); ok {
			t.Errorf("%s 被误写领取记录 %v（Claims 回拨）", email, c)
		}
	}

	// 账本：两个号的这次尝试必须落在**今天**（Date=今天、phase=pending），
	// 绝不能因陈旧 claimedAt 被合并进历史日期而凭空消失。
	today := time.Now().In(qoderCST).Format("2006-01-02")
	records := QoderClaimHistory("", 0)
	seen := map[string]bool{}
	for _, rec := range records {
		seen[rec.Email] = seen[rec.Email] || (rec.Date == today && rec.Phase == QoderPhasePending)
		if rec.Date != today {
			t.Errorf("%s 账本条目应落在今天 %s，实际 %s（陈旧 claimedAt 不得改写账本日期）", rec.Email, today, rec.Date)
		}
	}
	for _, email := range []string{"a@test.com", "b@test.com"} {
		if !seen[email] {
			t.Errorf("%s 在今天的账本里应有一条 pending 记录（审计可见），实际 records=%+v", email, records)
		}
	}
}

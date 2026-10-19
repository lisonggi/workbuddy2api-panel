package pool

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// 第八轮补齐：cooldown/pick/state/realm/modelview 的剩余分支。

func fullEntryOf(t *testing.T, p *Pool, uid string) *entry {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byUID[uid]
	if e == nil {
		t.Fatalf("账号不存在: %s", uid)
	}
	return e
}

func TestFull8CooldownClampBranches(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	now := time.Now()
	// 负数分支：credits/expiring/earliestRemaining 全部钳 0；过期 earliestAt 清零。
	p.SetCreditsDetailed("u1", -5, 100, -3, now.Add(-time.Hour), -7)
	e := fullEntryOf(t, p, "u1")
	if e.credits != 0 || e.creditsExpiring != 0 || e.creditsEarliestRemaining != 0 || !e.creditsEarliestExpiry.IsZero() {
		t.Fatalf("负数钳制后 = %+v", e)
	}
	// 超过 credits 的分支：expiring / earliestRemaining 都钳到 credits。
	p.SetCreditsDetailed("u1", 10, 100, 20, now.Add(time.Hour), 50)
	e = fullEntryOf(t, p, "u1")
	if e.creditsExpiring != 10 || e.creditsEarliestRemaining != 10 {
		t.Fatalf("上限钳制后 = %+v", e)
	}
	if e.creditsEarliestExpiry.IsZero() {
		t.Fatal("未来 earliestAt 不应被清空")
	}
}

func TestFull8ClearExpiringSnapshots(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCreditsDetailed("u1", 10, 100, 5, time.Now().Add(time.Hour), 5)
	p.ClearExpiringSnapshots()
	e := fullEntryOf(t, p, "u1")
	if e.creditsExpiring != 0 || e.creditsEarliestRemaining != 0 || !e.creditsEarliestExpiry.IsZero() {
		t.Fatalf("清空后 = %+v", e)
	}
	if e.credits != 10 {
		t.Fatalf("余额不应被清空: %d", e.credits)
	}
}

func TestFull8SetNicknameSaveError(t *testing.T) {
	a := &auth.Auth{UID: "u1", AccessToken: "at", Nickname: "旧名", FilePath: filepath.Join(t.TempDir(), "missing", "auth.json")}
	p := New("")
	p.Add(a)
	if p.SetNickname("u1", "新名") {
		t.Fatal("落盘失败应返回 false")
	}
	if a.Nickname != "新名" {
		t.Fatalf("内存昵称应已更新: %q", a.Nickname)
	}
}

func TestFull8RateLimitAuditEdges(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.RecordModelRateLimitAudit("", "m", "r")
	p.RecordModelRateLimitAudit("ghost", "m", "r")

	e := fullEntryOf(t, p, "u1")
	e.modelCooldowns = map[string]modelCooldown{
		"m": {Until: time.Now().Add(time.Hour), Reason: "真实冷却"},
	}
	p.RecordModelRateLimitAudit("u1", "m", "审计")
	e = fullEntryOf(t, p, "u1")
	if e.modelCooldowns["m"].Reason != "真实冷却" {
		t.Fatalf("真实冷却被覆盖: %+v", e.modelCooldowns["m"])
	}

	e.until = time.Time{} // 无账号级截止 → 用 softRateMax 兜底
	p.RecordModelRateLimitAudit("u1", "m2", "审计")
	e = fullEntryOf(t, p, "u1")
	mc := e.modelCooldowns["m2"]
	if !mc.AuditOnly || mc.Reason != "审计" {
		t.Fatalf("审计条目 = %+v", mc)
	}
	if d := time.Until(mc.Until); d < time.Hour || d > 3*time.Hour {
		t.Fatalf("默认软冷却上限兜底异常: %v", d)
	}
}

func TestFull8BlockModelBackoffEdges(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.BlockModelBackoff("", "m", "r")
	p.BlockModelBackoff("ghost", "m", "r")
	for i := 0; i < 3; i++ {
		p.BlockModelBackoff("u1", "m", "11102 model not found")
	}
	e := fullEntryOf(t, p, "u1")
	mc := e.modelCooldowns["m"]
	if mc.Hits != 3 {
		t.Fatalf("Hits = %d", mc.Hits)
	}
	if d := time.Until(mc.Until); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("第三次应封顶 24h，实际 %v", d)
	}
}

func TestFull8BlockModelClearEdges(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.BlockModelClear("", "m")
	p.BlockModelClear("ghost", "m")
	p.BlockModelClear("u2", "m") // 无台账

	e := fullEntryOf(t, p, "u1")
	e.modelCooldowns = map[string]modelCooldown{
		"m6004": {Until: time.Now().Add(time.Hour), Reason: "6004 rate limited"},
		"m1110": {Until: time.Now().Add(time.Hour), Reason: "11102 model not found"},
	}
	p.BlockModelClear("u1", "m6004")
	e = fullEntryOf(t, p, "u1")
	if _, ok := e.modelCooldowns["m6004"]; !ok {
		t.Fatal("6004 条目不应被清除")
	}
	p.BlockModelClear("u1", "m1110")
	e = fullEntryOf(t, p, "u1")
	if _, ok := e.modelCooldowns["m1110"]; ok {
		t.Fatal("11102 条目应被清除")
	}
	if _, ok := e.modelCooldowns["m6004"]; !ok {
		t.Fatal("6004 条目应保留")
	}
}

func TestFull8CooldownSoftRateWithReset(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	reset := time.Now().Add(30 * time.Minute)
	p.CooldownSoftRate("u1", time.Minute, reset, "reset")
	e := fullEntryOf(t, p, "u1")
	if e.coolKind != CoolSoft || e.reason != "reset" {
		t.Fatalf("冷却状态 = %+v", e)
	}
	if d := time.Until(e.until); d < 25*time.Minute || d > 35*time.Minute {
		t.Fatalf("until 应对齐 resetAt，实际 %v", d)
	}
}

func TestFull8SoftDurationShiftCap(t *testing.T) {
	p := New("")
	d := p.softDurationLocked(time.Minute, softStreakShiftMax+3)
	if d != defaultSoftRateMax {
		t.Fatalf("移位封顶后 = %v，期望 %v", d, defaultSoftRateMax)
	}
}

func TestFull8PickExplore(t *testing.T) {
	p := New("")
	p.SetRandomSource(func(int64) int64 { return 0 })
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SetCredits("u1", 1000, 0)
	p.SetCredits("u2", 1000, 0)
	p.NoteModelCost("u1", "m", 0, 1000) // tier 0：已实测免费

	acct := p.PickExcludingForModel(nil, "m")
	if acct == nil || acct.UID != "u2" {
		t.Fatalf("探索应改选 tier 1 的 u2，实际 %+v", acct)
	}
	p.mu.RLock()
	events, last := p.costExploreEvents, p.exploreLast["\x1fm"]
	p.mu.RUnlock()
	if events != 1 || last.IsZero() {
		t.Fatalf("探索记录 events=%d last=%v", events, last)
	}
}

func TestFull8PickCostOrder(t *testing.T) {
	p := New("")
	p.SetRandomSource(func(int64) int64 { return 0 })
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SetCredits("u1", 1000, 0)
	p.SetCredits("u2", 1000, 0)
	p.NoteModelCost("u1", "m", 10, 1000) // 单价高
	p.NoteModelCost("u2", "m", 4, 1000)  // 单价低

	acct := p.PickExcludingForModel(nil, "m")
	if acct == nil || acct.UID != "u2" {
		t.Fatalf("同层应按单价升序选 u2，实际 %+v", acct)
	}
}

func TestFull8PickLRUFallback(t *testing.T) {
	p := New("")
	p.SetRandomSource(func(int64) int64 { return 0 })
	uids := []string{"a1", "a2", "a3", "a4", "a5"}
	for _, uid := range uids {
		p.Add(&auth.Auth{UID: uid})
		p.SetCredits(uid, 1000, 0)
	}
	now := time.Now()
	p.mu.Lock()
	for i, uid := range uids {
		e := p.byUID[uid]
		e.lastUsed = now
		e.usedSeq = uint64(len(uids) - i) // 越靠后 usedSeq 越小
	}
	p.mu.Unlock()

	acct := p.Pick()
	if acct == nil || acct.UID != "a5" {
		t.Fatalf("LRU 应选 usedSeq 最小的 a5，实际 %+v", acct)
	}
}

func TestFull8PickFallbackRealmAndInflight(t *testing.T) {
	p := New("")
	p.maxInFlight = 1
	p.Add(&auth.Auth{UID: "u-cn", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "u-gl", Domain: "www.workbuddy.ai"})
	now := time.Now()
	p.mu.Lock()
	p.byUID["u-cn"].until = now.Add(time.Hour)
	p.byUID["u-cn"].coolKind = CoolSoft
	p.byUID["u-gl"].until = now.Add(time.Hour)
	p.byUID["u-gl"].coolKind = CoolSoft
	p.byUID["u-gl"].inFlight.Store(1)
	p.mu.Unlock()

	if acct := p.PickExcludingForRealm(nil, "", "global"); acct != nil {
		t.Fatalf("兜底无可用号应返回 nil，实际 %+v", acct)
	}
}

func TestFull8PickByUIDForModelInFlight(t *testing.T) {
	p := New("")
	p.maxInFlight = 1
	p.Add(&auth.Auth{UID: "u1"})
	fullEntryOf(t, p, "u1").inFlight.Store(1)
	if acct := p.PickByUIDForModel("u1", "m"); acct != nil {
		t.Fatalf("在途占满应返回 nil，实际 %+v", acct)
	}
}

func TestFull8AvailableUIDsInflight(t *testing.T) {
	p := New("")
	p.maxInFlight = 1
	p.Add(&auth.Auth{UID: "u1"})
	fullEntryOf(t, p, "u1").inFlight.Store(1)
	if got := p.AvailableUIDsForRealm("cn"); len(got) != 0 {
		t.Fatalf("AvailableUIDsForRealm = %v", got)
	}
	if got := p.AvailableUIDsForModel("m"); len(got) != 0 {
		t.Fatalf("AvailableUIDsForModel = %v", got)
	}
	if got := p.AvailableUIDsForModelRealm("m", "cn"); len(got) != 0 {
		t.Fatalf("AvailableUIDsForModelRealm = %v", got)
	}
}

func TestFull8FloorRateParseError(t *testing.T) {
	p := New("")
	p.creditFloor = 100
	p.SetModelRateOf(func(string, string) string { return "not-a-number" })
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 1, 0)
	e := fullEntryOf(t, p, "u1")
	if p.floorBlockedForRealmModel(e, "m", "cn", time.Now()) {
		t.Fatal("倍率解析失败不应拦截")
	}
}

func TestFull8PickWeightedLastCandidate(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SetCredits("u1", 1000, 0)
	p.SetCredits("u2", 1000, 0)
	p.SetRandomSource(func(n int64) int64 { return n }) // r == total → 走末尾兜底
	e1 := fullEntryOf(t, p, "u1")
	e2 := fullEntryOf(t, p, "u2")
	if got := p.pickWeighted([]*entry{e1, e2}); got != e2 {
		t.Fatalf("末尾兜底应返回最后一个候选")
	}
}

func TestFull8WeightNegativeIdle(t *testing.T) {
	p := New("")
	e := &entry{a: &auth.Auth{UID: "u"}, credits: 0, lastUsed: time.Now().Add(time.Hour)}
	if got := p.weightOf(e, 0, time.Now()); got != 1.0 {
		t.Fatalf("时钟回拨时 idle 权重应钳 0，w = %v", got)
	}
}

func TestFull8NoteSessionDeadUnknown(t *testing.T) {
	p := New("")
	if p.NoteSessionDead("ghost") {
		t.Fatal("不存在的账号应返回 false")
	}
}

func TestFull8ModelCostStatusExpired(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	e := fullEntryOf(t, p, "u1")
	now := time.Now()
	e.modelCost = map[string]modelCostEntry{
		"zero": {LastSeen: time.Time{}},
		"old":  {LastSeen: now.Add(-2 * modelCostTTL)},
	}
	p.mu.Lock()
	rows := p.modelCostsStatusLocked(e, now)
	p.mu.Unlock()
	if rows != nil {
		t.Fatalf("过期观测不应进台账: %+v", rows)
	}
}

func TestFull8RateLimitedRowsEmpty(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	e := fullEntryOf(t, p, "u1")
	now := time.Now()
	e.modelCooldowns = map[string]modelCooldown{
		"m1": {Until: now.Add(-time.Minute)},
		"m2": {Until: time.Time{}},
	}
	p.mu.Lock()
	rows := p.rateLimitedModelsLocked(e, now)
	p.mu.Unlock()
	if rows != nil {
		t.Fatalf("全部过期应返回 nil: %+v", rows)
	}
}

func TestFull8ModelLockViewStates(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := New("")
	cn := func(uid string) *auth.Auth {
		return &auth.Auth{UID: uid, Domain: "www.codebuddy.cn"}
	}
	gl := func(uid string) *auth.Auth {
		return &auth.Auth{UID: uid, Domain: "www.workbuddy.ai"}
	}
	for _, uid := range []string{"a", "e", "f"} {
		p.Add(cn(uid))
	}
	for _, uid := range []string{"b", "c", "d"} {
		p.Add(gl(uid))
	}
	now := time.Now()
	until := now.Add(time.Hour)
	p.mu.Lock()
	// cn：a/e/f 都对 m-locked 冷却 → locked。
	for _, uid := range []string{"a", "e", "f"} {
		p.byUID[uid].modelCooldowns = map[string]modelCooldown{"m-locked": {Until: until, Reason: "11102 x"}}
	}
	// global：b/c 账号级冷却、d 对 m-starved 冷却 → starved。
	for _, uid := range []string{"b", "c"} {
		p.byUID[uid].until = until
		p.byUID[uid].coolKind = CoolSoft
	}
	p.byUID["d"].modelCooldowns = map[string]modelCooldown{"m-starved": {Until: until}}
	// cn：f 对两个模型冷却，a/e 可服务 → partial（两个不同模型触发行间排序）。
	p.byUID["f"].modelCooldowns["m-partial"] = modelCooldown{Until: until}
	p.byUID["f"].modelCooldowns["m-partial2"] = modelCooldown{Until: until}
	p.mu.Unlock()

	rows := p.ModelLockView()
	states := map[string]string{}
	for _, r := range rows {
		states[r.Model] = r.State
	}
	if states["m-locked"] != "locked" || states["m-starved"] != "starved" ||
		states["m-partial"] != "partial" || states["m-partial2"] != "partial" {
		t.Fatalf("states = %v", states)
	}
	if len(rows) < 2 || rows[0].Model != "m-locked" {
		t.Fatalf("locked 行应排最前: %+v", rows)
	}
	if rows[len(rows)-2].Model != "m-partial" || rows[len(rows)-1].Model != "m-partial2" {
		t.Fatalf("partial 行应按模型名排序: %+v", rows)
	}
}

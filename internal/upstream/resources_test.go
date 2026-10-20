package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// resourceList 逐包接口的响应：data.Accounts + TotalCount。
func resourceList(accounts ...string) string {
	return fmt.Sprintf(`{"code":0,"data":{"Accounts":[%s],"TotalCount":%d}}`, strings.Join(accounts, ","), len(accounts))
}

// onResources 登记官方查积分的三个接口：免费包列表回 accounts，付费包为空；summary 为空串时汇总回 404
// （网关改用逐包合计），否则原样作为汇总的 data。
func (f *upFake) onResources(summary string, accounts ...string) {
	f.on(http.MethodPost, resourceFreePath, 200, resourceList(accounts...))
	f.on(http.MethodPost, resourcePaidPath, 200, resourceList())
	if summary == "" {
		f.on(http.MethodPost, resourceSummaryPath, 404, `{"code":404,"msg":"not found"}`)
	} else {
		f.on(http.MethodPost, resourceSummaryPath, 200, `{"code":0,"data":`+summary+`}`)
	}
}

// resourceRT 假传输：汇总回 404（网关改用逐包合计），付费包为空，免费包回 accounts。
func resourceRT(accounts ...string) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case resourceSummaryPath:
			return jsonResp(404, `{"code":404,"msg":"not found"}`), nil
		case resourcePaidPath:
			return jsonResp(200, resourceList()), nil
		case resourceFreePath:
			return jsonResp(200, resourceList(accounts...)), nil
		}
		return nil, errors.New("wrong path: " + r.URL.Path)
	}
}

// cyclePkg 一个按周期发放的包：额度与剩余用官方的 *Precise 字符串，extra 是追加的字段（可空）。
func cyclePkg(name string, size, remain int, extra string) string {
	s := fmt.Sprintf(`{"PackageName":%q,"CycleCapacitySizePrecise":"%d","CycleCapacityRemainPrecise":"%d"`, name, size, remain)
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

// TestUserResourcesOfficialRequests 三个接口的路径、请求体、Accept-Language 与官方桌面端一致，两域都不带 /v2。
func TestUserResourcesOfficialRequests(t *testing.T) {
	for _, tc := range []struct {
		name, lang string
		global     bool
	}{{"国内版", "zh", false}, {"国际版", "en", true}} {
		t.Run(tc.name, func(t *testing.T) {
			old := billingRetryDelay
			billingRetryDelay = 0
			t.Cleanup(func() { billingRetryDelay = old })
			f := newUpFake(t)
			f.onResources(`{"Packages":[]}`)
			c := upClient(f)
			a := cnAuth()
			if tc.global {
				c.ChatBaseGlobal, c.BillingBaseGlobal = upBase, upBase
				a = globalAuth()
			}
			if _, _, _, err := c.CreditPackages(a); err != nil {
				t.Fatalf("err = %v", err)
			}
			day := time.Now().In(softRateResetLoc).Format("2006-01-02")
			bodies := map[string]map[string]any{}
			for _, r := range f.seen() {
				if r.method != http.MethodPost || r.header.Get("Accept-Language") != tc.lang || r.header.Get("X-Product") != "SaaS" {
					t.Errorf("%s 请求头不对：%v", r.path, r.header)
				}
				var b map[string]any
				_ = json.Unmarshal(r.body, &b)
				bodies[r.path] = b
			}
			if len(bodies) != 3 || len(bodies[resourceSummaryPath]) != 0 {
				t.Fatalf("应只发三个接口、汇总请求体为空对象：%v", bodies)
			}
			paid, free := bodies[resourcePaidPath], bodies[resourceFreePath]
			for _, b := range []map[string]any{paid, free} {
				if b["PageNumber"] != float64(1) || b["PageSize"] != float64(200) || fmt.Sprint(b["Status"]) != "[0 3]" {
					t.Errorf("分页或状态不对：%v", b)
				}
			}
			if paid["NeedRenewInfo"] != true || len(paid["PackageCodes"].([]any)) != len(paidPackageCodes) {
				t.Errorf("付费包请求体不对：%v", paid)
			}
			if free["SlicePeriodStartTime"] != day+" 00:00:00" || free["SlicePeriodEndTime"] != day+" 23:59:59" ||
				len(free["PackageCodes"].([]any)) != len(freePackageCodes) {
				t.Errorf("免费包请求体不对：%v", free)
			}
		})
	}
}

// TestUserResourcesPagingAndDedupe 一页满 200 条且没到总数就翻页，最多 10 页；同一 ResourceId 只算一次。
func TestUserResourcesPagingAndDedupe(t *testing.T) {
	pkg := func(id string) string {
		return `{"ResourceId":"` + id + `","CycleCapacitySizePrecise":"1","CycleCapacityRemainPrecise":"1"}`
	}
	full := make([]string, resourcePageSize)
	for i := range full {
		full[i] = pkg(fmt.Sprintf("r%d", i))
	}
	f := newUpFake(t)
	f.onResources("")
	pages := 0
	f.onFunc(http.MethodPost, resourceFreePath, func(r *upReq) (*http.Response, error) {
		pages++
		var b struct{ PageNumber int }
		_ = json.Unmarshal(r.body, &b)
		if b.PageNumber == 1 {
			return jsonResp(200, `{"code":0,"data":{"Accounts":[`+strings.Join(full, ",")+`],"TotalCount":203}}`), nil
		}
		// 第二页带一个与第一页重复的包，以及没有 ResourceId 的包（不去重）。
		return jsonResp(200, `{"code":0,"data":{"Accounts":[`+pkg("r0")+`,`+pkg("x1")+`,{"CycleCapacitySizePrecise":"2"}],"TotalCount":203}}`), nil
	})
	f.on(http.MethodPost, resourcePaidPath, 200, resourceList(pkg("x1")))
	packs, remain, size, err := upClient(f).CreditPackages(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if pages != 2 || len(packs) != 202 || remain != 201 || size != 203 {
		t.Fatalf("pages=%d packs=%d remain=%d size=%d", pages, len(packs), remain, size)
	}

	// 一直满页时最多 10 页。
	f2 := newUpFake(t)
	f2.onResources("")
	n := 0
	f2.onFunc(http.MethodPost, resourceFreePath, func(*upReq) (*http.Response, error) {
		n++
		return jsonResp(200, `{"code":0,"data":{"Accounts":[`+strings.Join(full, ",")+`],"TotalCount":99999}}`), nil
	})
	if _, _, _, err := upClient(f2).CreditPackages(upAuth()); err != nil || n != resourceMaxPages {
		t.Fatalf("n=%d err=%v，应最多拉 %d 页", n, err, resourceMaxPages)
	}
}

// TestResourceCapacitySlice 按天切片的包有当天切片明细时取切片额度，没有明细时取周期额度；精确到小数，
// 整数字段不参与计算。
func TestResourceCapacitySlice(t *testing.T) {
	f := newUpFake(t)
	f.onResources("",
		`{"PackageName":"切片","CapacityType":4,"CycleCapacitySizePrecise":"500","CycleCapacityRemainPrecise":"300",
		  "SlicePeriodUsageDetails":[{"SlicePeriodCapacitySizePrecise":"20","SlicePeriodCapacityRemainPrecise":"7.6"}]}`,
		`{"PackageName":"无明细","CapacityType":4,"CycleCapacitySizePrecise":"30","CycleCapacityRemainPrecise":"10.4","CycleCapacityRemain":999}`,
		`{"PackageName":"数字","CycleCapacitySizePrecise":100,"CycleCapacityRemainPrecise":"abc"}`)
	packs, remain, size, err := upClient(f).CreditPackages(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	got := map[string]CreditPackage{}
	for _, p := range packs {
		got[p.Name] = p
	}
	if p := got["切片"]; p.Size != 20 || p.Remain != 8 || p.Used != 12 || p.Cycle {
		t.Errorf("切片包 = %+v", p)
	}
	if p := got["无明细"]; p.Size != 30 || p.Remain != 10 || !p.Cycle {
		t.Errorf("无明细的切片包 = %+v", p)
	}
	if p := got["数字"]; p.Size != 100 || p.Remain != 0 {
		t.Errorf("数字额度、无法解析的剩余 = %+v", p)
	}
	// 合计先按小数相加再取整：7.6 + 10.4 = 18。
	if remain != 18 || size != 150 {
		t.Fatalf("remain=%d size=%d，期望 18/150", remain, size)
	}
}

// TestUserResourceSummaryPreferred 余额与总额取汇总（各档相加），汇总查不到或解析不了时用逐包合计；
// 快到期分桶仍按逐包，且不超过余额。
func TestUserResourceSummaryPreferred(t *testing.T) {
	old := billingRetryDelay
	billingRetryDelay = 0
	t.Cleanup(func() { billingRetryDelay = old })
	soonEnd := time.Now().In(softRateResetLoc).Add(48 * time.Hour).Format(packageEndLayout)
	pkgs := []string{`{"CycleCapacitySizePrecise":"100","CycleCapacityRemainPrecise":"60","CycleEndTime":"` + soonEnd + `"}`}
	for _, tc := range []struct {
		name, summary           string
		remain, total, expiring int64
	}{
		{"汇总", `{"Packages":[{"CycleTotalCapacity":"80.4","CycleRemainCapacity":"40.5"},{"CycleTotalCapacity":"20","CycleRemainCapacity":"x"}]}`, 41, 100, 41},
		{"汇总失败", "", 60, 100, 60},
		{"汇总解析失败", `"oops"`, 60, 100, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.onResources(tc.summary, pkgs...)
			remain, total, expiring, earliestAt, earliestRemaining, err := upClient(f).UserResourceDetailedWithExpiry(upAuth(), 7*24*time.Hour)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if remain != tc.remain || total != tc.total || expiring != tc.expiring || earliestRemaining != tc.expiring || earliestAt.IsZero() {
				t.Fatalf("remain=%d total=%d expiring=%d earliest=%v/%d", remain, total, expiring, earliestAt, earliestRemaining)
			}
		})
	}
}

// TestUserResourcesListFailures 付费包或免费包任一失败都报错（余额不完整时不更新）。
func TestUserResourcesListFailures(t *testing.T) {
	for _, path := range []string{resourcePaidPath, resourceFreePath} {
		f := newUpFake(t)
		f.onResources(`{"Packages":[]}`)
		f.on(http.MethodPost, path, 400, `{"code":10001,"msg":"no"}`)
		if _, _, err := upClient(f).UserResource(upAuth()); err == nil {
			t.Fatalf("%s 失败应报错", path)
		}
	}
}

// TestDailyCheckinOfficialFlow 照官方先查签到状态再签到，两条都带设备令牌、都走 /v2；活动没开或今天已签过就不再签到；
// 状态查不到时照常签到。
func TestDailyCheckinOfficialFlow(t *testing.T) {
	old := billingRetryDelay
	billingRetryDelay = 0
	t.Cleanup(func() { billingRetryDelay = old })
	for _, tc := range []struct {
		name, status   string
		statusCode     int
		wantCheckin    bool
		wantErr        string
		wantAlreadyErr bool
	}{
		{"未签到", `{"code":0,"data":{"active":true,"today_checked_in":false}}`, 200, true, "", false},
		{"已签到", `{"code":0,"data":{"active":true,"today_checked_in":true}}`, 200, false, "今天已签到", true},
		{"活动未开", `{"code":0,"data":{"active":false}}`, 200, false, "签到活动未开启", false},
		{"状态查不到", `{"code":10001,"msg":"no"}`, 400, true, "", false},
		{"状态解析不了", `{"code":0,"data":"oops"}`, 200, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, checkinStatusPath, tc.statusCode, tc.status)
			f.on(http.MethodPost, dailyCheckinPath, 200, `{"code":0,"data":{}}`)
			a := upAuth()
			a.DeviceToken = "dev-tok"
			err := upClient(f).DailyCheckin(a)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if IsAlreadyCheckin(err) != tc.wantAlreadyErr {
				t.Fatalf("IsAlreadyCheckin = %v", IsAlreadyCheckin(err))
			}
			if got := f.count(http.MethodPost, dailyCheckinPath); (got == 1) != tc.wantCheckin {
				t.Fatalf("签到次数 = %d", got)
			}
			for _, r := range f.seen() {
				if r.header.Get("X-Device-Token") != "dev-tok" || string(r.body) != "{}" {
					t.Errorf("%s 应带设备令牌、请求体为 {}：%v %s", r.path, r.header, r.body)
				}
			}
		})
	}
}

// TestUpstreamTestPathsMatch 其他包测试用的 upstreamtest 路径与这里的常量一致。
func TestUpstreamTestPathsMatch(t *testing.T) {
	for got, want := range map[string]string{
		upstreamtest.SummaryPath:       resourceSummaryPath,
		upstreamtest.PaidPath:          resourcePaidPath,
		upstreamtest.FreePath:          resourceFreePath,
		upstreamtest.CheckinStatusPath: checkinStatusPath,
		upstreamtest.CheckinPath:       dailyCheckinPath,
	} {
		if got != want {
			t.Errorf("upstreamtest 路径 %q，应为 %q", got, want)
		}
	}
	// 默认响应能被网关解析：免费包里的包计入余额，汇总 404 时用逐包合计，签到状态允许签到。
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if st, body, ok := upstreamtest.Respond(r.URL.Path, upstreamtest.Package(100, 40, `"PackageName":"p"`)); ok {
			return jsonResp(st, body), nil
		}
		if r.URL.Path == dailyCheckinPath {
			return jsonResp(200, `{"code":0,"data":{}}`), nil
		}
		return nil, errors.New("wrong path: " + r.URL.Path)
	})
	if remain, total, err := c.UserResource(upAuth()); err != nil || remain != 40 || total != 100 {
		t.Fatalf("remain=%d total=%d err=%v", remain, total, err)
	}
	if err := c.DailyCheckin(upAuth()); err != nil {
		t.Fatalf("签到 err=%v", err)
	}
	if _, _, ok := upstreamtest.Respond("/other"); ok {
		t.Fatal("别的路径应交给调用方")
	}
}

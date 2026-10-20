package upstream

// department_test.go 企业号的部门全名：有值时桌面端请求带 X-Department-Info。

import (
	"net/http"
	"testing"
)

// TestDepartmentHeader 有部门全名时对话、后台服务、刷新令牌的请求都带 X-Department-Info，值原样；
// 没有时一个都不带。
func TestDepartmentHeader(t *testing.T) {
	c := &Client{}
	for _, tc := range []struct{ name, dept string }{{"企业号", "研发部/平台组"}, {"个人号", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			a := cnAuth()
			a.EnterpriseID = "corp-cn"
			a.DepartmentFullName = tc.dept
			reqs := map[string]*http.Request{}
			for _, kind := range []string{"对话", "后台服务", "刷新令牌"} {
				req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
				switch kind {
				case "对话":
					c.ChatHeaders(req, a, "", ChatMeta{})
				case "后台服务":
					c.BillingHeaders(req, a)
				default:
					c.RefreshHeaders(req, a)
				}
				reqs[kind] = req
			}
			for kind, req := range reqs {
				got, sent := req.Header.Get("X-Department-Info"), len(req.Header.Values("X-Department-Info")) > 0
				if got != tc.dept || sent != (tc.dept != "") {
					t.Errorf("%s: X-Department-Info = %q（发了=%v），应为 %q", kind, got, sent, tc.dept)
				}
			}
		})
	}
}

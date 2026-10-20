package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseDepartment 部门全名两种文件形状都读：嵌套形 account.departmentFullName、扁平形
// departmentFullName；没有这个键时为空。
func TestParseDepartment(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"嵌套形", `{"auth":{"accessToken":"at"},"account":{"uid":"u1","enterpriseId":"e1","departmentFullName":"研发部/平台组"}}`, "研发部/平台组"},
		{"扁平形", `{"accessToken":"at","uid":"u1","enterpriseId":"e1","departmentFullName":"Sales"}`, "Sales"},
		{"个人号没有", `{"auth":{"accessToken":"at"},"account":{"uid":"u1"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if a.DepartmentValue() != tc.want {
				t.Errorf("部门全名 = %q，应为 %q", a.DepartmentValue(), tc.want)
			}
		})
	}
	var nilAuth *Auth
	if nilAuth.DepartmentValue() != "" {
		t.Error("nil 接收者应返回空串")
	}
}

// TestSaveDepartment 部门全名有值才写进 account.departmentFullName，读回一致；个人号的文件里
// 没有这个键。
func TestSaveDepartment(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, dept string }{{"企业号", "研发部"}, {"个人号", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			fp := filepath.Join(dir, tc.name+".json")
			a := &Auth{AccessToken: "at", UID: "u1", DepartmentFullName: tc.dept, FilePath: fp}
			if err := a.SaveAtomic(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(fp)
			if err != nil {
				t.Fatal(err)
			}
			if has := strings.Contains(string(raw), "departmentFullName"); has != (tc.dept != "") {
				t.Errorf("文件里有没有 departmentFullName = %v，部门全名 %q:\n%s", has, tc.dept, raw)
			}
			b, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if b.DepartmentFullName != tc.dept {
				t.Errorf("读回 = %q，应为 %q", b.DepartmentFullName, tc.dept)
			}
		})
	}
}

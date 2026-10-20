package main

// run_test.go 登录工具 run 与 doJSON：假 client 走 url/poll 全流程、realm 选择、
// fatal 输出捕获（替换 exitFunc）与上游错误分支。stateFile 指向 t.TempDir()。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type loginRT func(*http.Request) (*http.Response, error)

func (f loginRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type loginRec struct{ method, host, path, origin string }

type loginFake struct {
	mu      sync.Mutex
	recs    []loginRec
	handler func(*http.Request) (int, string)
}

func (f *loginFake) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.recs = append(f.recs, loginRec{r.Method, r.URL.Host, r.URL.Path, r.Header.Get("Origin")})
	f.mu.Unlock()
	status, body := f.handler(r)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (f *loginFake) requests() []loginRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]loginRec(nil), f.recs...)
}

// exitPanic 捕获 fatal（exitFunc 被替换成 panic）。
type exitPanic struct{ code int }

func stubExit(t *testing.T) {
	t.Helper()
	old := exitFunc
	exitFunc = func(code int) { panic(exitPanic{code}) }
	t.Cleanup(func() { exitFunc = old })
}

// runLogin 执行 run 并返回 (stdout, stderr, 是否触发 fatal 及退出码)。
func runLogin(t *testing.T, args []string, stdin io.Reader, client *http.Client) (string, string, bool, int) {
	t.Helper()
	stubExit(t)
	var out, errb bytes.Buffer
	fatal := false
	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(exitPanic)
				if !ok {
					panic(r)
				}
				fatal, code = true, p.code
			}
		}()
		run(args, stdin, &out, &errb, client)
	}()
	return out.String(), errb.String(), fatal, code
}

// useStateFile 把包级 stateFile 指到临时目录并还原。
func useStateFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	old := stateFile
	stateFile = path
	t.Cleanup(func() { stateFile = old })
	return path
}

// 假上游：state / token / account 三个端点。
func loginFakeClient(t *testing.T) *loginFake {
	t.Helper()
	return &loginFake{handler: func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			return 200, `{"code":0,"data":{"state":"st-1","authUrl":"https://auth.example/authorize?x=1"}}`
		case "/v2/plugin/auth/token":
			return 200, `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600,"domain":"www.codebuddy.cn"}}`
		case "/v2/plugin/login/account":
			return 200, `{"code":0,"data":{"uid":"u1","enterpriseId":"ent-1","nickname":"一号"}}`
		default:
			t.Errorf("未登记的请求: %s %s", r.Method, r.URL.Path)
			return 400, `{"code":400}`
		}
	}}
}

// url 子命令：state 落盘（含 realm），stdout 打印授权地址；global 走国际版端点与 Origin。
func TestRunURLCNAndGlobal(t *testing.T) {
	t.Run("cn", func(t *testing.T) {
		statePath := useStateFile(t)
		fake := loginFakeClient(t)
		out, errb, fatal, _ := runLogin(t, []string{"url"}, strings.NewReader(""), &http.Client{Transport: fake})
		if fatal {
			t.Fatalf("不应 fatal: %s", errb)
		}
		if strings.TrimSpace(out) != "https://auth.example/authorize?x=1" {
			t.Fatalf("stdout=%q", out)
		}
		raw, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		var st loginState
		if err := json.Unmarshal(raw, &st); err != nil || st.State != "st-1" || st.Realm != "cn" {
			t.Fatalf("state 文件 = %s (%v)", raw, err)
		}
		reqs := fake.requests()
		if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].host != "www.workbuddy.cn" ||
			reqs[0].path != "/v2/plugin/auth/state" || reqs[0].origin != originRefererCN {
			t.Fatalf("请求不对: %+v", reqs)
		}
	})

	t.Run("global", func(t *testing.T) {
		useStateFile(t)
		fake := loginFakeClient(t)
		out, errb, fatal, _ := runLogin(t, []string{"--realm=global", "url"}, strings.NewReader(""), &http.Client{Transport: fake})
		if fatal {
			t.Fatalf("不应 fatal: %s", errb)
		}
		if !strings.Contains(out, "auth.example") {
			t.Fatalf("stdout=%q", out)
		}
		reqs := fake.requests()
		if len(reqs) != 1 || reqs[0].host != "www.workbuddy.ai" || reqs[0].origin != originRefererGlobal {
			t.Fatalf("应走国际版端点与 Origin: %+v", reqs)
		}
	})
}

// poll 子命令：读 state、拿 token/account、输出完整 JSON 并删掉 state 文件。
func TestRunPollSuccess(t *testing.T) {
	statePath := useStateFile(t)
	if err := os.WriteFile(statePath, []byte(`{"state":"st-1","realm":"cn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := loginFakeClient(t)
	out, errb, fatal, _ := runLogin(t, []string{"poll"}, strings.NewReader(""), &http.Client{Transport: fake})
	if fatal {
		t.Fatalf("不应 fatal: %s", errb)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v\n%s", err, out)
	}
	want := map[string]any{
		"access_token": "at-1", "refresh_token": "rt-1", "expires_in": float64(3600),
		"domain": "www.codebuddy.cn", "realm": "cn", "uid": "u1",
		"enterprise_id": "ent-1", "nickname": "一号",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v want %v (输出 %v)", k, got[k], v, got)
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("poll 成功后应删除 state 文件: %v", err)
	}
}

// poll 的错误分支：state 缺失/损坏、realm 不一致、token 未完成/500/空 token。
func TestRunPollErrors(t *testing.T) {
	t.Run("state 缺失", func(t *testing.T) {
		useStateFile(t)
		_, errb, fatal, code := runLogin(t, []string{"poll"}, strings.NewReader(""),
			&http.Client{Transport: loginFakeClient(t)})
		if !fatal || code != 1 || !strings.Contains(errb, "read state") {
			t.Fatalf("fatal=%v code=%d stderr=%q", fatal, code, errb)
		}
	})

	t.Run("state 损坏", func(t *testing.T) {
		statePath := useStateFile(t)
		if err := os.WriteFile(statePath, []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, errb, fatal, _ := runLogin(t, []string{"poll"}, strings.NewReader(""),
			&http.Client{Transport: loginFakeClient(t)})
		if !fatal || !strings.Contains(errb, "parse state") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})

	t.Run("realm 不一致", func(t *testing.T) {
		statePath := useStateFile(t)
		if err := os.WriteFile(statePath, []byte(`{"state":"st-1","realm":"global"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, errb, fatal, _ := runLogin(t, []string{"poll"}, strings.NewReader(""),
			&http.Client{Transport: loginFakeClient(t)})
		if !fatal || !strings.Contains(errb, "realm mismatch") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})

	pollWithToken := func(t *testing.T, status int, body string) (string, bool) {
		t.Helper()
		statePath := useStateFile(t)
		if err := os.WriteFile(statePath, []byte(`{"state":"st-1","realm":"cn"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := &loginFake{handler: func(r *http.Request) (int, string) { return status, body }}
		_, errb, fatal, _ := runLogin(t, []string{"poll"}, strings.NewReader(""), &http.Client{Transport: fake})
		return errb, fatal
	}

	t.Run("登录未完成（4xx）", func(t *testing.T) {
		errb, fatal := pollWithToken(t, 400, `{"code":1,"msg":"login ing"}`)
		if !fatal || !strings.Contains(errb, "登录未完成") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})
	t.Run("token 端点 500", func(t *testing.T) {
		errb, fatal := pollWithToken(t, 500, `{"code":500,"msg":"boom"}`)
		if !fatal || !strings.Contains(errb, "token endpoint error") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})
	t.Run("空 accessToken", func(t *testing.T) {
		errb, fatal := pollWithToken(t, 200, `{"code":0,"data":{"accessToken":""}}`)
		if !fatal || !strings.Contains(errb, "登录未完成") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})
}

// account 端点失败不阻塞输出（uid 为空），仍写完整 JSON。
func TestRunPollAccountFailure(t *testing.T) {
	statePath := useStateFile(t)
	if err := os.WriteFile(statePath, []byte(`{"state":"st-1","realm":"cn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &loginFake{handler: func(r *http.Request) (int, string) {
		if r.URL.Path == "/v2/plugin/auth/token" {
			return 200, `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600,"domain":"www.codebuddy.cn"}}`
		}
		return 500, `{"code":500,"msg":"account boom"}`
	}}
	out, errb, fatal, _ := runLogin(t, []string{"poll"}, strings.NewReader(""), &http.Client{Transport: fake})
	if fatal {
		t.Fatalf("不应 fatal: %s", errb)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["uid"] != "" || got["access_token"] != "at-1" {
		t.Fatalf("输出不对: %v err=%v", got, err)
	}
}

// realm 子命令：stdin 选择 → stdout 只输出归一化 realm；参数/子命令错误走 fatal。
func TestRunRealmAndArgs(t *testing.T) {
	t.Run("realm 选择", func(t *testing.T) {
		out, errb, fatal, _ := runLogin(t, []string{"realm"}, strings.NewReader("2\n"),
			&http.Client{Transport: loginFakeClient(t)})
		if fatal {
			t.Fatalf("不应 fatal: %s", errb)
		}
		if strings.TrimSpace(out) != "global" {
			t.Fatalf("stdout=%q", out)
		}
		if !strings.Contains(errb, "选择登录版本") {
			t.Fatalf("提示应打到 stderr: %q", errb)
		}
	})

	t.Run("realm 参数非法", func(t *testing.T) {
		_, errb, fatal, code := runLogin(t, []string{"--realm=foo", "url"}, strings.NewReader(""),
			&http.Client{Transport: loginFakeClient(t)})
		if !fatal || code != 1 || !strings.Contains(errb, "invalid --realm") {
			t.Fatalf("fatal=%v code=%d stderr=%q", fatal, code, errb)
		}
	})

	t.Run("缺少子命令", func(t *testing.T) {
		_, errb, fatal, _ := runLogin(t, nil, strings.NewReader(""), &http.Client{Transport: loginFakeClient(t)})
		if !fatal || !strings.Contains(errb, "usage: login") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})

	t.Run("未知子命令", func(t *testing.T) {
		_, errb, fatal, _ := runLogin(t, []string{"wat"}, strings.NewReader(""),
			&http.Client{Transport: loginFakeClient(t)})
		if !fatal || !strings.Contains(errb, "unknown subcommand") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})
}

// 上游 state 响应缺字段、写 state 失败都走 fatal。
func TestRunURLFailures(t *testing.T) {
	t.Run("缺 authUrl", func(t *testing.T) {
		useStateFile(t)
		fake := &loginFake{handler: func(*http.Request) (int, string) {
			return 200, `{"code":0,"data":{"state":"st-1"}}`
		}}
		_, errb, fatal, _ := runLogin(t, []string{"url"}, strings.NewReader(""), &http.Client{Transport: fake})
		if !fatal || !strings.Contains(errb, "missing state or authUrl") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})

	t.Run("写 state 失败", func(t *testing.T) {
		old := stateFile
		stateFile = filepath.Join(t.TempDir(), "missing", "dir", "state.json")
		t.Cleanup(func() { stateFile = old })
		_, errb, fatal, _ := runLogin(t, []string{"url"}, strings.NewReader(""),
			&http.Client{Transport: loginFakeClient(t)})
		if !fatal || !strings.Contains(errb, "write state") {
			t.Fatalf("fatal=%v stderr=%q", fatal, errb)
		}
	})
}

// doJSON：非 2xx / 3xx / 非 JSON / code≠0 的错误与成功解包。
func TestDoJSON(t *testing.T) {
	mk := func(status int, body string) *http.Client {
		return &http.Client{Transport: loginRT(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status,
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:   io.NopCloser(strings.NewReader(body))}, nil
		})}
	}
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"500", 500, `{}`, "http_error: upstream 500"},
		{"302", 302, `{}`, "upstream redirect 302"},
		{"非 JSON", 200, `not-json`, "parse failed"},
		{"code 非 0", 200, `{"code":7,"msg":"nope"}`, "code=7 msg=nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := doJSON(mk(c.status, c.body), http.MethodGet, "https://x.test/p", nil, nil)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err=%v want %q", err, c.wantErr)
			}
		})
	}

	data, status, err := doJSON(mk(200, `{"code":0,"data":{"v":1}}`), http.MethodGet, "https://x.test/p", nil, nil)
	if err != nil || status != 200 || string(data) != `{"v":1}` {
		t.Fatalf("成功解包 = %s, %d, %v", data, status, err)
	}

	// 自定义 headers 覆盖默认头（Origin 随 realm 切换）。
	hdr := make(chan string, 1)
	client := &http.Client{Transport: loginRT(func(r *http.Request) (*http.Response, error) {
		hdr <- r.Header.Get("Origin")
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})}
	headers := commonHeaders(originRefererGlobal)
	if _, _, err := doJSON(client, http.MethodGet, "https://x.test/p", headers, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-hdr; got != originRefererGlobal {
		t.Fatalf("Origin=%q", got)
	}
}

// 请求构造失败（非法 URL）也返回错误。
func TestDoJSONBadURL(t *testing.T) {
	if _, _, err := doJSON(mkEmptyClient(), http.MethodGet, "http://exa\x00mple/", nil, nil); err == nil {
		t.Fatal("非法 URL 应报错")
	}
}

func mkEmptyClient() *http.Client {
	return &http.Client{Transport: loginRT(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})}
}

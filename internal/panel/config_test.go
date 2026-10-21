// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// config_test.go 面板配置接口（config.go）：读取回显与保存编排。
// 校验/落盘/热应用都在 main 注入的闭包里，这里只测 HTTP 编排与错误映射。

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/pool"
)

// failingReader 模拟读取请求体失败（连接中断等）。
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// 配置读取：未注入 501、回调失败 500、成功把回调返回的对象和路径原样回显
// （面板不脱敏，敏感字段由 main 的 LoadConfig 决定如何返回）。
func TestGetConfig(t *testing.T) {
	t.Run("未注入加载器返回 501", func(t *testing.T) {
		p := New(Config{Version: "test", Pool: pool.New("")})
		rec := get(p, http.MethodGet, "/panel/api/config")
		autoWantCode(t, rec, http.StatusNotImplemented)
	})

	t.Run("加载失败返回 500", func(t *testing.T) {
		p := New(Config{Version: "test", Pool: pool.New(""), LoadConfig: func() (any, error) {
			return nil, fmt.Errorf("bad file")
		}})
		rec := get(p, http.MethodGet, "/panel/api/config")
		autoWantCode(t, rec, http.StatusInternalServerError)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "load config: bad file") {
			t.Fatalf("错误信息不对: %q", txt)
		}
	})

	t.Run("成功原样回显", func(t *testing.T) {
		cfgObj := map[string]any{"api_key": "secret-value", "upstream": map[string]any{"base": "x"}}
		p := New(Config{
			Version:    "test",
			Pool:       pool.New(""),
			ConfigPath: "/tmp/config.json",
			LoadConfig: func() (any, error) { return cfgObj, nil },
		})
		rec := get(p, http.MethodGet, "/panel/api/config")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["ok"] != true || resp["path"] != "/tmp/config.json" {
			t.Fatalf("响应字段不对: %v", resp)
		}
		cfg, _ := resp["config"].(map[string]any)
		if cfg == nil {
			t.Fatalf("缺少 config 对象: %v", resp)
		}
		if cfg["api_key"] != "secret-value" {
			t.Fatalf("敏感字段应按回调返回值原样透出（不脱敏）: %v", cfg)
		}
	})
}

// 配置保存：未注入 501、回调报错 400、成功透传原始 body 与重启字段列表。
func TestSaveConfig(t *testing.T) {
	t.Run("未注入保存器返回 501", func(t *testing.T) {
		p := New(Config{Version: "test", Pool: pool.New("")})
		rec := postJSON(t, p, "/panel/api/config", `{"a":1}`)
		autoWantCode(t, rec, http.StatusNotImplemented)
	})

	t.Run("请求体读取失败返回 400", func(t *testing.T) {
		p := New(Config{Version: "test", Pool: pool.New(""), SaveConfig: func([]byte) ([]string, error) {
			return nil, nil
		}})
		req := httptest.NewRequest(http.MethodPost, "/panel/api/config", failingReader{})
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		autoWantCode(t, rec, http.StatusBadRequest)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "read body") {
			t.Fatalf("错误信息不对: %q", txt)
		}
	})

	t.Run("回调报错返回 400", func(t *testing.T) {
		var got string
		p := New(Config{Version: "test", Pool: pool.New(""), SaveConfig: func(raw []byte) ([]string, error) {
			got = string(raw)
			return nil, fmt.Errorf("invalid config: port out of range")
		}})
		body := `{"port":99999}`
		rec := postJSON(t, p, "/panel/api/config", body)
		autoWantCode(t, rec, http.StatusBadRequest)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "invalid config") {
			t.Fatalf("错误信息不对: %q", txt)
		}
		if got != body {
			t.Fatalf("回调应收到原始 body: %q", got)
		}
	})

	t.Run("成功返回重启字段列表", func(t *testing.T) {
		var got string
		p := New(Config{Version: "test", Pool: pool.New(""), SaveConfig: func(raw []byte) ([]string, error) {
			got = string(raw)
			return []string{"upstream.base_cn"}, nil
		}})
		body := `{"upstream":{"base_cn":"https://x"}}`
		rec := postJSON(t, p, "/panel/api/config", body)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["ok"] != true {
			t.Fatalf("成功响应应 ok=true: %v", resp)
		}
		raw, _ := resp["restart_required"].([]any)
		if len(raw) != 1 || raw[0] != "upstream.base_cn" {
			t.Fatalf("重启字段列表不对: %v", resp)
		}
		if got != body {
			t.Fatalf("回调应收到与请求一致的 body: %q", got)
		}
	})

	t.Run("nil 重启列表返回空数组", func(t *testing.T) {
		p := New(Config{Version: "test", Pool: pool.New(""), SaveConfig: func([]byte) ([]string, error) {
			return nil, nil
		}})
		rec := postJSON(t, p, "/panel/api/config", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		raw, ok := autoDecode(t, rec)["restart_required"].([]any)
		if !ok || len(raw) != 0 {
			t.Fatalf("nil 列表应序列化为空数组: %s", rec.Body)
		}
	})
}

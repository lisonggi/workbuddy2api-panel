// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/changelog"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
)

// panelRoundTripFunc 假 RoundTripper（替换 http.DefaultClient 用）。
type panelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f panelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestFetchChangelogBranches 更新日志拉取的各失败形态与默认地址。
func TestFetchChangelogBranches(t *testing.T) {
	t.Run("URL 非法", func(t *testing.T) {
		p := New(Config{UpdateURL: "://bad"})
		if _, err := p.fetchChangelog(context.Background()); err == nil {
			t.Fatal("非法 URL 应返回错误")
		}
	})

	t.Run("HTTP 非 200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		p := New(Config{UpdateURL: srv.URL})
		_, err := p.fetchChangelog(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("err = %v，应含 HTTP 500", err)
		}
	})

	t.Run("内容为空", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("# 更新日志\n\n"))
		}))
		defer srv.Close()
		p := New(Config{UpdateURL: srv.URL})
		_, err := p.fetchChangelog(context.Background())
		if err == nil || !strings.Contains(err.Error(), "没有可识别") {
			t.Fatalf("err = %v，应为空内容错误", err)
		}
	})

	t.Run("超过 1MiB", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", updateMaxBytes+1)))
		}))
		defer srv.Close()
		p := New(Config{UpdateURL: srv.URL})
		_, err := p.fetchChangelog(context.Background())
		if err == nil || !strings.Contains(err.Error(), "超过") {
			t.Fatalf("err = %v，应为超限错误", err)
		}
	})

	t.Run("默认地址", func(t *testing.T) {
		var gotURL string
		oldClient := http.DefaultClient
		http.DefaultClient = &http.Client{Transport: panelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/markdown"}},
				Body:       io.NopCloser(strings.NewReader(remoteChangelog)),
			}, nil
		})}
		defer func() { http.DefaultClient = oldClient }()

		p := New(Config{UpdateURL: ""})
		rels, err := p.fetchChangelog(context.Background())
		if err != nil || len(rels) == 0 {
			t.Fatalf("rels=%d err=%v", len(rels), err)
		}
		if gotURL != defaultUpdateURL {
			t.Errorf("默认地址 = %q，应为 %q", gotURL, defaultUpdateURL)
		}
	})

	t.Run("传输层错误", func(t *testing.T) {
		oldClient := http.DefaultClient
		http.DefaultClient = &http.Client{Transport: panelRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial blocked")
		})}
		defer func() { http.DefaultClient = oldClient }()

		p := New(Config{UpdateURL: "https://example.invalid/changelog"})
		if _, err := p.fetchChangelog(context.Background()); err == nil {
			t.Fatal("传输层错误应返回错误")
		}
	})
}

// TestStartUpdateCheckExits 后台检查启动后 ctx 取消即退出（ticker 1 分钟，只测退出）。
func TestStartUpdateCheckExits(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()

	p := New(Config{
		Version:   "1.1.0",
		UpdateURL: srv.URL,
		Live:      livecfg.New(livecfg.Snapshot{UpdateCheck: true}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.StartUpdateCheck(ctx)
	time.Sleep(20 * time.Millisecond) // 只给 goroutine 启动/退出的时间片
	if hits != 0 {
		t.Errorf("ticker 1 分钟未到，不应请求远端，got %d", hits)
	}
}

// TestSelfUpdateBusy 下载进行中再次更新 → ErrBusy 映射 409；updaterErrStatus 的映射。
func TestSelfUpdateBusy(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	upd := selfupdate.Launch(t.TempDir(), "1.1.0")
	upd.DownloadBase = srv.URL + "/"
	upd.Restart = func() { t.Error("下载未完成不应重启") }
	live := livecfg.New(livecfg.Snapshot{UpdateCheck: true})
	p := New(Config{Version: "1.1.0", Live: live, Updater: upd})
	p.upd.remote = changelog.Parse(remoteChangelog)

	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.2.0"}`); rr.Code != http.StatusAccepted {
		t.Fatalf("首次更新 status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr := postJSON(t, p, "/panel/api/update", `{"version":"1.2.0"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("下载中再次更新应 409，got %d body=%s", rr.Code, rr.Body.String())
	}

	if got := updaterErrStatus(selfupdate.ErrBusy); got != http.StatusConflict {
		t.Errorf("ErrBusy 映射 = %d，应为 409", got)
	}
	if got := updaterErrStatus(errors.New("other")); got != http.StatusBadRequest {
		t.Errorf("其他错误映射 = %d，应为 400", got)
	}
}

// TestRollbackWithoutUpdater 没接入一键更新时回滚返回 501。
func TestRollbackWithoutUpdater(t *testing.T) {
	p := New(Config{})
	if rr := postJSON(t, p, "/panel/api/update/rollback", ""); rr.Code != http.StatusNotImplemented {
		t.Fatalf("rollback status=%d，应为 501", rr.Code)
	}
}

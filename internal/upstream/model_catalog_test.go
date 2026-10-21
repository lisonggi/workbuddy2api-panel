// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeCatalogFile 把 content 写成测试用的 model.json。
func writeCatalogFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写 model.json: %v", err)
	}
}

func readCatalogFile(t *testing.T, path string) map[string]ModelCapEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 model.json: %v", err)
	}
	var out map[string]ModelCapEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 model.json: %v", err)
	}
	return out
}

// noRequest 假客户端：一旦被调用就记测试失败（断言“不发请求”）。
func noRequest(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("不应发请求，实际请求 %s %s", r.Method, r.URL)
		return jsonResp(400, `{}`), nil
	})}
}

// TestListingChainLevels 查找链前三级：上游值权威、空模型直接兜底、
// 静态种子表命中、model.json 缓存命中——都不发请求。
func TestListingChainLevels(t *testing.T) {
	t.Run("remote>0 原样返回", func(t *testing.T) {
		mdReset(t)
		cl := noRequest(t)
		if got := ContextWindowListingV4("glm-5.2", 424242, cl); got != 424242 {
			t.Errorf("context = %d，应为 remote 424242", got)
		}
		if got, ok := MaxOutputTokensListingV4("glm-5.2", 7777, cl); got != 7777 || !ok {
			t.Errorf("max = %d/%v，应为 7777/true", got, ok)
		}
	})

	t.Run("model 为空", func(t *testing.T) {
		mdReset(t)
		cl := noRequest(t)
		if got := ContextWindowListingV4("", 0, cl); got != DefaultContextWindow {
			t.Errorf("context = %d，应为 %d", got, DefaultContextWindow)
		}
		if got, ok := MaxOutputTokensListingV4("", 0, cl); got != 0 || ok {
			t.Errorf("max = %d/%v，应为 0/false", got, ok)
		}
	})

	t.Run("静态种子表命中", func(t *testing.T) {
		mdReset(t)
		cl := noRequest(t)
		if got := ContextWindowListingV4("glm-5.2", 0, cl); got != 1000000 {
			t.Errorf("context = %d，应为种子表 1000000", got)
		}
		if got, ok := MaxOutputTokensListingV4("glm-5.2", 0, cl); got != 131072 || !ok {
			t.Errorf("max = %d/%v，应为 131072/true", got, ok)
		}
		// 种子表命中但输出上限未知：context 有值，max 省略（继续落第 4 级，nil client 不发请求）。
		if got := ContextWindowListingV4("kimi-k2.8-preview", 0, nil); got != 1048576 {
			t.Errorf("context = %d，应为 1048576", got)
		}
		if got, ok := MaxOutputTokensListingV4("kimi-k2.8-preview", 0, nil); got != 0 || ok {
			t.Errorf("max = %d/%v，应为 0/false", got, ok)
		}
	})

	t.Run("model.json 缓存命中", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "model.json")
		writeCatalogFile(t, path, `{"cached-model":{"context_length":54321,"max_output_tokens":2100,"source":"manual"}}`)
		loadModelCatalogAt(path)
		cl := noRequest(t)
		if got := ContextWindowListingV4("cached-model", 0, cl); got != 54321 {
			t.Errorf("context = %d，应为缓存 54321", got)
		}
		if got, ok := MaxOutputTokensListingV4("cached-model", 0, cl); got != 2100 || !ok {
			t.Errorf("max = %d/%v，应为 2100/true", got, ok)
		}
	})
}

// TestListingAllMissAsyncFetch 全链 miss：本次先回兜底，异步拉 models.dev 一次；
// 拉到后两条链返回 models.dev 值，并写进 model.json（重新加载文件可读到），
// 同时清除该模型的负缓存。
func TestListingAllMissAsyncFetch(t *testing.T) {
	mdReset(t)
	path := filepath.Join(t.TempDir(), "model.json")
	loadModelCatalogAt(path) // 文件不存在 → 种子初值

	release := make(chan struct{})
	f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
		<-release
		return jsonResp(200, `{"zai":{"models":{"md-new-model":{"limit":{"context":777000,"output":42000}}}}}`), nil
	}}
	cl := f.client()

	const model = "md-new-model"
	if got := ContextWindowListingV4(model, 0, cl); got != DefaultContextWindow {
		t.Errorf("本次 context = %d，应为兜底 %d", got, DefaultContextWindow)
	}
	if got, ok := MaxOutputTokensListingV4(model, 0, cl); got != 0 || ok {
		t.Errorf("本次 max = %d/%v，应为 0/false", got, ok)
	}

	close(release) // 放行异步拉取
	// 回填先写 model.json 再清负缓存，两步都完成才算拉取收尾。
	mdWait(t, "模型值写入 model.json 并清掉负缓存", func() bool {
		_, ok := modelCatalogGet(model)
		return ok && mdNegativeCount() == 0
	})

	if got := ContextWindowListingV4(model, 0, cl); got != 777000 {
		t.Errorf("拉到后 context = %d，应为 777000", got)
	}
	if got, ok := MaxOutputTokensListingV4(model, 0, cl); got != 42000 || !ok {
		t.Errorf("拉到后 max = %d/%v，应为 42000/true", got, ok)
	}
	if mdNegativeCount() != 0 || modelsDev.negativeFresh(model) {
		t.Errorf("回填后应清除负缓存，剩余 %d 条", mdNegativeCount())
	}
	if n := f.count(); n != 1 {
		t.Errorf("应只拉 1 次，实际 %d", n)
	}

	// 重新从磁盘加载：值确实已落盘到 model.json。
	file := readCatalogFile(t, path)
	e, ok := file[model]
	if !ok || e.ContextLength != 777000 || e.MaxOutputTokens != 42000 || e.Source != "modelsdev" {
		t.Fatalf("落盘条目 = %+v/%v", e, ok)
	}
	if _, err := time.Parse(time.RFC3339, e.FetchedAt); err != nil {
		t.Errorf("fetched_at 不是 RFC3339: %q (%v)", e.FetchedAt, err)
	}
	resetModelCatalog()
	loadModelCatalogAt(path)
	if got, ok := modelCatalogGet(model); !ok || got.ContextLength != 777000 {
		t.Errorf("重新加载后应读到缓存，got %+v/%v", got, ok)
	}
}

// TestListingDocReadyImmediate 进程内文档索引已就绪时，查找链第 4 级直接命中
// 并写入 model.json（不需要再等异步拉取）。
func TestListingDocReadyImmediate(t *testing.T) {
	mdReset(t)
	path := filepath.Join(t.TempDir(), "model.json")
	loadModelCatalogAt(path)
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{
		"doc-hit":      {Context: 999, Output: 88},
		"doc-zero-out": {Context: 555, Output: 0},
	}
	modelsDev.mu.Unlock()

	if got := ContextWindowListingV4("doc-hit", 0, nil); got != 999 {
		t.Errorf("context = %d，应为 999", got)
	}
	if got, ok := MaxOutputTokensListingV4("doc-hit", 0, nil); got != 88 || !ok {
		t.Errorf("max = %d/%v，应为 88/true", got, ok)
	}
	if e, ok := modelCatalogGet("doc-hit"); !ok || e.ContextLength != 999 || e.MaxOutputTokens != 88 {
		t.Errorf("应写入 model.json，got %+v/%v", e, ok)
	}

	// 文档给了 context 但 output=0：max 仍省略，但值照样入缓存。
	if got := ContextWindowListingV4("doc-zero-out", 0, nil); got != 555 {
		t.Errorf("context = %d，应为 555", got)
	}
	if got, ok := MaxOutputTokensListingV4("doc-zero-out", 0, nil); got != 0 || ok {
		t.Errorf("max = %d/%v，应为 0/false", got, ok)
	}
	if e, ok := modelCatalogGet("doc-zero-out"); !ok || e.MaxOutputTokens != 0 {
		t.Errorf("应写入 output=0 的缓存条目，got %+v/%v", e, ok)
	}
}

// TestListingNegativeCacheShortCircuit 负缓存有效期内再次 miss 不再触发拉取，
// 也不重新记录时刻（请求计数不变）。
func TestListingNegativeCacheShortCircuit(t *testing.T) {
	mdReset(t)
	f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
		return jsonResp(200, `{}`), nil
	}}
	cl := f.client()

	// 制造拉取冷却：已拉过且就在刚刚（文档未就绪）。
	modelsDev.fetched = true
	modelsDev.lastFetch = time.Now()

	for i := 0; i < 2; i++ {
		if got := ContextWindowListingV4("negative-model", 0, cl); got != DefaultContextWindow {
			t.Errorf("第 %d 次 context = %d，应为兜底", i, got)
		}
		if got, ok := MaxOutputTokensListingV4("negative-model", 0, cl); got != 0 || ok {
			t.Errorf("第 %d 次 max = %d/%v，应为 0/false", i, got, ok)
		}
	}
	if n := f.count(); n != 0 {
		t.Errorf("负缓存/冷却期内不应发请求，实际 %d", n)
	}
	if !modelsDev.negativeFresh("negative-model") {
		t.Error("miss 应记入负缓存")
	}
}

// signalCloseBody 响应体关闭时关闭信号通道：用来确定异步拉取 goroutine 已收尾，
// 避免测试在 goroutine 仍在写单例时结束。
type signalCloseBody struct {
	io.ReadCloser
	once sync.Once
	ch   chan struct{}
}

func (b *signalCloseBody) Close() error {
	b.once.Do(func() { close(b.ch) })
	return b.ReadCloser.Close()
}

// TestListingModelsDevFailuresSilent models.dev 回非 200 / 超大 / 非 JSON 时
// 静默失败，继续返回兜底值，不 panic。
func TestListingModelsDevFailuresSilent(t *testing.T) {
	cases := []struct {
		name string
		body func() io.ReadCloser
	}{
		{name: "非 200", body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{}`)) }},
		{name: "超大响应", body: func() io.ReadCloser { return io.NopCloser(&repeatReader{n: modelsDevMaxBody + 1024}) }},
		{name: "非 JSON", body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(`<html>`)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mdReset(t)
			done := make(chan struct{})
			f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
				status := 200
				if tc.name == "非 200" {
					status = 500
				}
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       &signalCloseBody{ReadCloser: tc.body(), ch: done},
				}, nil
			}}
			cl := f.client()
			if got := ContextWindowListingV4("fail-model", 0, cl); got != DefaultContextWindow {
				t.Errorf("context = %d，应为兜底 %d", got, DefaultContextWindow)
			}
			if got, ok := MaxOutputTokensListingV4("fail-model", 0, cl); got != 0 || ok {
				t.Errorf("max = %d/%v，应为 0/false", got, ok)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("异步拉取未结束")
			}
			if n := f.count(); n != 1 {
				t.Errorf("应只拉 1 次，实际 %d", n)
			}
			if mdDocReady() {
				t.Error("失败响应不应建立索引")
			}
		})
	}
}

// TestListingNilClient nil client 不发请求，直接返回兜底值。
func TestListingNilClient(t *testing.T) {
	mdReset(t)
	if got := ContextWindowListingV4("nil-client-model", 0, nil); got != DefaultContextWindow {
		t.Errorf("context = %d，应为兜底 %d", got, DefaultContextWindow)
	}
	if got, ok := MaxOutputTokensListingV4("nil-client-model", 0, nil); got != 0 || ok {
		t.Errorf("max = %d/%v，应为 0/false", got, ok)
	}
}

// TestModelCatalogLoadBranches 加载分支：path 为空用种子；文件条目校验；
// 文件整体损坏降级种子不崩溃。
func TestModelCatalogLoadBranches(t *testing.T) {
	t.Run("path 为空用内置种子", func(t *testing.T) {
		mdReset(t)
		e, ok := modelCatalogGet("glm-5.2")
		if !ok || e.ContextLength != 1000000 || e.Source != "seed" {
			t.Errorf("种子条目 = %+v/%v", e, ok)
		}
	})

	t.Run("合法条目加载、非法条目剔除", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "model.json")
		writeCatalogFile(t, path, `{
			"ok-model":{"context_length":1000,"max_output_tokens":10,"source":"manual"},
			"zero-context":{"context_length":0,"max_output_tokens":10,"source":"manual"},
			"negative-output":{"context_length":1000,"max_output_tokens":-1,"source":"manual"}
		}`)
		loadModelCatalogAt(path)
		if e, ok := modelCatalogGet("ok-model"); !ok || e.ContextLength != 1000 || e.MaxOutputTokens != 10 {
			t.Errorf("合法条目 = %+v/%v", e, ok)
		}
		if _, ok := modelCatalogGet("zero-context"); ok {
			t.Error("context<=0 的条目应被剔除")
		}
		if _, ok := modelCatalogGet("negative-output"); ok {
			t.Error("output<0 的条目应被剔除")
		}
		// 文件合法时以文件为准，不再并入种子。
		if _, ok := modelCatalogGet("glm-5.2"); ok {
			t.Error("文件存在时不应再加载内置种子")
		}
	})

	t.Run("文件损坏降级种子", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "model.json")
		writeCatalogFile(t, path, `{"glm-5.2": not-json`)
		loadModelCatalogAt(path) // 不应 panic
		e, ok := modelCatalogGet("glm-5.2")
		if !ok || e.ContextLength != 1000000 || e.Source != "seed" {
			t.Errorf("损坏后应降级种子，got %+v/%v", e, ok)
		}
	})

	t.Run("读取失败降级种子", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "model.json")
		if err := os.Mkdir(path, 0o755); err != nil { // path 是目录 → ReadFile 失败
			t.Fatalf("建目录: %v", err)
		}
		loadModelCatalogAt(path)
		if e, ok := modelCatalogGet("glm-5.2"); !ok || e.ContextLength != 1000000 {
			t.Errorf("读取失败应降级种子，got %+v/%v", e, ok)
		}
	})
}

// TestModelCatalogPutSave 写入与落盘：格式（source/fetched_at）、原子落盘、
// 重新加载可读；目录不可写时内存缓存仍生效；改名失败静默。
func TestModelCatalogPutSave(t *testing.T) {
	t.Run("落盘格式与重新加载", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "nested", "model.json") // 目录不存在，覆盖 MkdirAll
		loadModelCatalogAt(path)
		modelCatalogPut("saved-model", 1234, 567)

		file := readCatalogFile(t, path)
		e, ok := file["saved-model"]
		if !ok || e.ContextLength != 1234 || e.MaxOutputTokens != 567 || e.Source != "modelsdev" {
			t.Fatalf("落盘条目 = %+v/%v", e, ok)
		}
		if _, err := time.Parse(time.RFC3339, e.FetchedAt); err != nil {
			t.Errorf("fetched_at = %q 不是 RFC3339: %v", e.FetchedAt, err)
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Errorf("不应残留 .tmp 文件: %v", err)
		}

		resetModelCatalog()
		loadModelCatalogAt(path)
		if got, ok := modelCatalogGet("saved-model"); !ok || got.ContextLength != 1234 {
			t.Errorf("重新加载 = %+v/%v", got, ok)
		}
	})

	t.Run("目录不可写时内存缓存仍生效", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 不受目录权限限制")
		}
		mdReset(t)
		dir := t.TempDir()
		ro := filepath.Join(dir, "ro")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatalf("建只读目录: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
		path := filepath.Join(ro, "model.json")
		loadModelCatalogAt(path)
		modelCatalogPut("mem-only", 4321, 99)
		if e, ok := modelCatalogGet("mem-only"); !ok || e.ContextLength != 4321 || e.MaxOutputTokens != 99 {
			t.Errorf("内存缓存 = %+v/%v", e, ok)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("不可写目录不应落盘，stat err=%v", err)
		}
	})

	t.Run("改名失败静默且内存缓存生效", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "model.json")
		if err := os.Mkdir(path, 0o755); err != nil { // 目标是目录 → tmp+rename 的 rename 失败
			t.Fatalf("建目录: %v", err)
		}
		loadModelCatalogAt(path)
		modelCatalogPut("rename-fail", 777, 7)
		if e, ok := modelCatalogGet("rename-fail"); !ok || e.ContextLength != 777 {
			t.Errorf("内存缓存 = %+v/%v", e, ok)
		}
	})

	t.Run("非法输入不写", func(t *testing.T) {
		mdReset(t)
		path := filepath.Join(t.TempDir(), "model.json")
		loadModelCatalogAt(path)
		modelCatalogPut("", 100, 10)
		modelCatalogPut("zero-context", 0, 10)
		modelCatalogPut("negative-output", 100, -1)
		for _, m := range []string{"", "zero-context", "negative-output"} {
			if _, ok := modelCatalogGet(m); ok {
				t.Errorf("%q 不应写入缓存", m)
			}
		}
	})
}

// TestModelCatalogGetRejectsInvalidInMemory 内存里被塞入非法条目时 get 也不返回。
func TestModelCatalogGetRejectsInvalidInMemory(t *testing.T) {
	mdReset(t)
	catalogState.mu.Lock()
	catalogState.initLocked()
	catalogState.entries["broken"] = ModelCapEntry{ContextLength: 0, MaxOutputTokens: 10, Source: "manual"}
	catalogState.mu.Unlock()
	if _, ok := modelCatalogGet("broken"); ok {
		t.Error("非法条目不应命中")
	}
}

// TestSetModelCatalogPathAfterLoad 已加载后改路径只更新落盘位置，不清空已加载条目。
func TestSetModelCatalogPathAfterLoad(t *testing.T) {
	mdReset(t)
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.json")
	loadModelCatalogAt(pathA) // 不存在 → 种子 + 绑定 a.json
	pathB := filepath.Join(dir, "b.json")
	SetModelCatalogPath(pathB)
	modelCatalogPut("moved-model", 321, 54)
	if got, ok := modelCatalogGet("moved-model"); !ok || got.ContextLength != 321 {
		t.Errorf("内存缓存 = %+v/%v", got, ok)
	}
	file := readCatalogFile(t, pathB)
	if e, ok := file["moved-model"]; !ok || e.ContextLength != 321 {
		t.Errorf("应写到新路径，got %+v/%v", e, ok)
	}
	if _, ok := file["glm-5.2"]; !ok {
		t.Error("已加载的种子条目应随内存缓存一起落盘到新路径")
	}
}

// TestResets 两个测试隔离入口都清空各自负责的单例状态。
func TestResets(t *testing.T) {
	mdReset(t)
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{"m": {Context: 1}}
	modelsDev.fetched = true
	modelsDev.negatives = map[string]time.Time{"m": time.Now()}
	modelsDev.mu.Unlock()
	catalogState.mu.Lock()
	catalogState.initLocked()
	catalogState.entries["m"] = ModelCapEntry{ContextLength: 1, Source: "manual"}
	catalogState.path = filepath.Join(t.TempDir(), "unused.json")
	catalogState.mu.Unlock()

	ResetLookupChainForTest()

	catalogState.mu.Lock()
	if catalogState.entries != nil || catalogState.loaded || catalogState.path != "" {
		t.Errorf("model catalog 未清空: entries=%v loaded=%v path=%q",
			catalogState.entries, catalogState.loaded, catalogState.path)
	}
	catalogState.mu.Unlock()
	modelsDev.mu.Lock()
	if modelsDev.doc != nil || modelsDev.fetched || modelsDev.negatives != nil {
		t.Errorf("models.dev 未清空: doc=%v fetched=%v negatives=%v",
			modelsDev.doc, modelsDev.fetched, modelsDev.negatives)
	}
	modelsDev.mu.Unlock()
}

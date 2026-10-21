// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package selfupdate

// full_more_test.go 补齐 override 环境分支、清单写盘各步骤失败与 Launch/fail/MarkStable
// 的日志分支，以及 stage 的默认下载基址、不可写目录、归档下载失败、回卷失败、
// 解包写文件失败。所有 exec 都经 stub，绝不真执行。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// captureSULogs 捕获 log 输出。
func captureSULogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := log.Writer()
	buf := &bytes.Buffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// 从数据目录里的程序启动且 WB2A_BASE_EXE 有值：识别为 override，并记住自带程序信息。
func TestNewUpdaterOverrideEnv(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", binName)
	old := osExecutable
	osExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { osExecutable = old })
	t.Setenv(envBaseExe, "/base/wb2api")
	t.Setenv(envBaseVersion, "1.0.0")

	u := newUpdater(dir, "1.2.0")
	if !u.override {
		t.Fatal("应识别为跑着数据目录里的版本")
	}
	if u.baseExe != "/base/wb2api" || u.baseVersion != "1.0.0" {
		t.Fatalf("base = %q v%q", u.baseExe, u.baseVersion)
	}
}

// 启动切换前写清单失败：记日志并留在自带版本。
func TestLaunchManifestWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	u := newTestUpdater(t, "1.1.0")
	installed(t, u, Manifest{Version: "1.2.0"}, fakeBinary("1.2.0"))
	if err := os.Chmod(u.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.dir, 0o700) })
	logs := captureSULogs(t)
	calls := stubExec(t, nil)
	runLaunch(u)
	if len(*calls) != 0 {
		t.Fatal("写清单失败时不应 exec")
	}
	if text := logs.String(); !strings.Contains(text, "写入") || !strings.Contains(text, "使用程序自带的 v1.1.0") {
		t.Fatalf("日志 = %q", text)
	}
}

// fail：写清单失败也要打日志并记录退回原因。
func TestFailManifestWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	u := newTestUpdater(t, "1.1.0")
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(u.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.dir, 0o700) })
	logs := captureSULogs(t)
	u.fail(Manifest{Version: "1.2.0"}, "退回原因")
	text := logs.String()
	if !strings.Contains(text, "写入") || !strings.Contains(text, "退回原因") {
		t.Fatalf("日志 = %q", text)
	}
}

// MarkStable 写清单失败：打日志并提前返回，不再打“已稳定运行”。
func TestMarkStableWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	u := newTestUpdater(t, "1.2.0")
	installed(t, u, Manifest{Version: "1.2.0", Attempts: 1}, fakeBinary("1.2.0"))
	u.override = true
	if err := os.Chmod(u.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.dir, 0o700) })
	logs := captureSULogs(t)
	u.MarkStable()
	text := logs.String()
	if !strings.Contains(text, "写入") || strings.Contains(text, "已稳定运行") {
		t.Fatalf("日志 = %q", text)
	}
}

// writeManifest 的三处中途失败（写 / 刷盘 / 关闭）。
func TestWriteManifestStepErrors(t *testing.T) {
	steps := []struct {
		name string
		stub func(t *testing.T)
		want string
	}{
		{"写失败", func(t *testing.T) {
			old := manifestWrite
			manifestWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write boom") }
			t.Cleanup(func() { manifestWrite = old })
		}, "write boom"},
		{"刷盘失败", func(t *testing.T) {
			old := manifestSync
			manifestSync = func(*os.File) error { return errors.New("sync boom") }
			t.Cleanup(func() { manifestSync = old })
		}, "sync boom"},
		{"关闭失败", func(t *testing.T) {
			old := manifestClose
			manifestClose = func(*os.File) error { return errors.New("close boom") }
			t.Cleanup(func() { manifestClose = old })
		}, "close boom"},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.stub(t)
			dir := t.TempDir()
			err := writeManifest(dir, Manifest{Version: "1.2.0"})
			if err == nil || !strings.Contains(err.Error(), step.want) {
				t.Fatalf("err=%v want %q", err, step.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, manifestName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("失败时不应留下清单: %v", statErr)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("临时文件应被清掉: %v", entries)
			}
		})
	}
}

// stage：DownloadBase 为空时用默认 Release 地址。
func TestStageDefaultDownloadBase(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	var gotURL string
	u.Client = &http.Client{Transport: suRoundTrip(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return nil, errors.New("stop")
	})}
	err := u.stage(context.Background(), "1.2.0")
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("err=%v", err)
	}
	if want := DefaultDownloadBase + "1.2.0/checksums.txt"; gotURL != want {
		t.Fatalf("URL = %q want %q", gotURL, want)
	}
}

// stage：数据目录不可写（CreateTemp 失败）。
func TestStageCreateTempError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	u := newTestUpdater(t, "1.1.0")
	asset := AssetName("1.2.0", runtime.GOOS, runtime.GOARCH)
	sums := strings.Repeat("a", 64) + "  " + asset + "\n"
	u.Client = okClient(sums)
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(u.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.dir, 0o700) })
	if err := u.stage(context.Background(), "1.2.0"); err == nil || !strings.Contains(err.Error(), "数据目录不可写") {
		t.Fatalf("err=%v", err)
	}
}

// okClient 对 checksums.txt 返回给定内容，其余返回 500。
func okClient(sums string) *http.Client {
	return &http.Client{Transport: suRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			return &http.Response{StatusCode: 200, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(sums))}, nil
		}
		return &http.Response{StatusCode: 500, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader("boom"))}, nil
	})}
}

// stage：安装包下载失败。
func TestStageArchiveFetchError(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	asset := AssetName("1.2.0", runtime.GOOS, runtime.GOARCH)
	u.Client = okClient(strings.Repeat("a", 64) + "  " + asset + "\n")
	err := u.stage(context.Background(), "1.2.0")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err=%v", err)
	}
}

// stage：校验通过后回卷失败。
func TestStageSeekError(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	archive := []byte("not really an archive")
	asset := AssetName("1.2.0", runtime.GOOS, runtime.GOARCH)
	u.Client = &http.Client{Transport: suRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := archive
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			body = []byte(sha(archive) + "  " + asset + "\n")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	old := archiveSeek
	archiveSeek = func(*os.File, int64, int) (int64, error) { return 0, errors.New("seek boom") }
	t.Cleanup(func() { archiveSeek = old })

	if err := u.stage(context.Background(), "1.2.0"); err == nil || !strings.Contains(err.Error(), "seek boom") {
		t.Fatalf("err=%v", err)
	}
}

// stage：解包时 tar 内容被截断 → 写 wb2api 失败。
func TestStageExtractWriteError(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: binName, Mode: 0o755, Size: 100, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	// 不关 tar.Writer：留下“声明 100 字节只写了 5 字节”的截断条目。
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	asset := AssetName("1.2.0", runtime.GOOS, runtime.GOARCH)
	u := newTestUpdater(t, "1.1.0")
	u.Client = &http.Client{Transport: suRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := archive
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			body = []byte(sha(archive) + "  " + asset + "\n")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	err := u.stage(context.Background(), "1.2.0")
	if err == nil || !strings.Contains(err.Error(), "写出 wb2api 失败") {
		t.Fatalf("err=%v", err)
	}
}

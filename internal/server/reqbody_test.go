package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// bodyRecorder 假上游：记下每次对话请求的请求体，一律回成功。
type bodyRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (b *bodyRecorder) client() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			b.mu.Lock()
			b.bodies = append(b.bodies, string(raw))
			b.mu.Unlock()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

func (b *bodyRecorder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bodies)
}

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestChatCompressedRequestBody 客户端发 gzip 压缩的请求体：标了 Content-Encoding 的按头解，
// 没标但开头是 gzip 魔数的也解开（生产见过，以前原样转给上游换来 11101）。
// 坏的压缩体、不是 JSON 的请求体就地 400，不打上游。
func TestChatCompressedRequestBody(t *testing.T) {
	rec := &bodyRecorder{}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: rec.client()})
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`

	send := func(raw []byte, enc string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw))
		if enc != "" {
			req.Header.Set("Content-Encoding", enc)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, enc := range []string{"gzip", ""} {
		before := rec.count()
		w := send(gzipBytes(t, body), enc)
		if w.Code != 200 || rec.count() != before+1 {
			t.Fatalf("enc=%q：code=%d body=%s", enc, w.Code, w.Body)
		}
		if got := rec.bodies[len(rec.bodies)-1]; !strings.Contains(got, `"glm-5.2"`) || !strings.Contains(got, `"hi"`) {
			t.Errorf("enc=%q：上游收到的不是解开的 JSON：%.80q", enc, got)
		}
	}

	before := rec.count()
	for name, c := range map[string]struct {
		raw []byte
		enc string
	}{
		"坏的 gzip（标了头）":  {[]byte("not gzip"), "gzip"},
		"坏的 gzip（只有魔数）": {[]byte{0x1f, 0x8b, 0x00}, ""},
		"不是 JSON":       {[]byte("model=glm-5.2"), ""},
		"空请求体":          {nil, ""},
	} {
		w := send(c.raw, c.enc)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Errorf("%s：code=%d body=%s", name, w.Code, w.Body)
		}
	}
	if rec.count() != before {
		t.Error("坏请求体不该打上游")
	}
}

// TestCountTokensCompressedBody Claude Code 入口同样认压缩的请求体。
func TestCountTokensCompressedBody(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith()})
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", bytes.NewReader(gzipBytes(t, `{"model":"x","messages":[{"role":"user","content":"hello world"}]}`)))
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"input_tokens"`) {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
}

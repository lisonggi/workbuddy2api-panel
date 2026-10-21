// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// encoding.go 出站请求的 Accept-Encoding 与响应解压。
//
// 依据（10-20 读官方桌面端 5.8.3 安装包）：
//   - 内置 CLI 与后台服务的请求框架都是 axios，http 适配器在请求没设 Accept-Encoding 时
//     补上 "gzip, compress, deflate" 加运行时支持 brotli 时的 ", br"（Node 都支持）；CLI 的
//     axios 另有 advertiseZstdAcceptEncoding 开关，默认关，不带 zstd。官方代码没有在哪个
//     请求上改写这个头。
//   - 网页登录态的登录码请求是 Node 内置 fetch（undici）：https 请求补 "br, gzip, deflate"
//     （安装包里 undici 6.25.0 的 lib/web/fetch/index.js）。
//   - 官网网页在用户的浏览器里跑：桌面 Chrome 123 起发 "gzip, deflate, br, zstd"；小程序的
//     web-view 是 iOS WebKit，发 "gzip, deflate, br"。网页请求头配了自定义 UA 时仍按 Chrome 发。
//   - HTTPDNS 用 Node 的 https.request，不发这个头（见 httpdns.go）。
//
// Go 的 Transport 只在自己补 "Accept-Encoding: gzip" 时才自动解压；请求显式设了这个头，
// 响应要自己解。doDecoded 在每个出站请求的 Do 之后按 Content-Encoding 解开，解法照 axios：
// gzip / compress 按头部自动分辨 gzip 与 zlib，deflate 按首字节分辨 zlib 与裸 deflate，
// br、zstd 用现成的库。不认识的编码原样交回。
package upstream

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

const (
	// acceptEncodingAxios 官方请求框架（axios http 适配器）的默认值：对话、刷新令牌、登录、
	// 模型目录、积分、签到、上报、外观、专家列表。
	acceptEncodingAxios = "gzip, compress, deflate, br"
	// acceptEncodingNodeFetch Node 内置 fetch 对 https 请求的默认值（网页登录码）。
	acceptEncodingNodeFetch = "br, gzip, deflate"
	// acceptEncodingChrome 桌面 Chrome 的默认值（官网网页、client-login 页面、国际版注册页）。
	acceptEncodingChrome = "gzip, deflate, br, zstd"
	// acceptEncodingBrowser iOS WebKit 的默认值（小程序 web-view 里的网页）。
	acceptEncodingBrowser = "gzip, deflate, br"
)

// doDecoded 发请求，并在请求显式设了 Accept-Encoding 时按响应的 Content-Encoding 解压。
// 解压是惰性的：第一次读响应体时才建解压器，流式响应（SSE）不会在这里阻塞。
func doDecoded(hc *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err != nil || req.Header.Get("Accept-Encoding") == "" {
		return resp, err
	}
	return decodeResponse(resp), nil
}

// decodeResponse 按 Content-Encoding 换上解压后的响应体，删掉 Content-Encoding 与
// Content-Length（同 Go 自动解压 gzip 时的做法）。没有编码、Go 已经解过、或编码不认识
// 时原样返回。
func decodeResponse(resp *http.Response) *http.Response {
	if resp == nil || resp.Body == nil || resp.Uncompressed {
		return resp
	}
	body, ok := DecodeBody(resp.Header.Get("Content-Encoding"), resp.Body)
	if !ok {
		return resp
	}
	resp.Body = body
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true
	return resp
}

// DecodeBody 按 Content-Encoding 的值给 raw 套上惰性解压（gzip / compress / deflate / br /
// zstd，解法照 axios）。没有编码、identity 或不认识的编码返回 ok=false，调用方照用 raw。
// 网关的入站请求体也用它解（server.readRequestBody）。
func DecodeBody(contentEncoding string, raw io.ReadCloser) (io.ReadCloser, bool) {
	var open func(*bufio.Reader) (io.ReadCloser, error)
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "gzip", "x-gzip", "compress", "x-compress":
		open = openGzipOrZlib
	case "deflate":
		open = openDeflate
	case "br":
		open = func(r *bufio.Reader) (io.ReadCloser, error) { return io.NopCloser(brotli.NewReader(r)), nil }
	case "zstd":
		open = func(r *bufio.Reader) (io.ReadCloser, error) {
			zr, err := zstd.NewReader(r)
			if err != nil {
				return nil, err
			}
			return zr.IOReadCloser(), nil
		}
	default:
		return raw, false
	}
	return &decodedBody{raw: raw, open: open}, true
}

// openGzipOrZlib axios 对 gzip / compress 用 zlib.createUnzip：按头部自动分辨 gzip（1f 8b）
// 与 zlib。
func openGzipOrZlib(r *bufio.Reader) (io.ReadCloser, error) {
	head, err := r.Peek(2)
	if err == nil && head[0] == 0x1f && head[1] == 0x8b {
		return gzip.NewReader(r)
	}
	return zlib.NewReader(r)
}

// openDeflate axios 对 deflate 先看首字节：低 4 位是 8 当 zlib 包装的 deflate，否则当裸
// deflate。
func openDeflate(r *bufio.Reader) (io.ReadCloser, error) {
	head, err := r.Peek(1)
	if err == nil && head[0]&0x0f == 0x08 {
		return zlib.NewReader(r)
	}
	return flate.NewReader(r), nil
}

// decodedBody 惰性解压的响应体：第一次 Read 时建解压器；响应体一个字节都没有时直接 EOF
// （空响应带了 Content-Encoding 也不报错）。Close 关掉解压器和原响应体。
type decodedBody struct {
	raw  io.ReadCloser
	open func(*bufio.Reader) (io.ReadCloser, error)
	dec  io.ReadCloser
	err  error
}

func (b *decodedBody) Read(p []byte) (int, error) {
	if b.dec == nil && b.err == nil {
		br := bufio.NewReader(b.raw)
		if _, err := br.Peek(1); err != nil {
			if errors.Is(err, io.EOF) {
				b.err = io.EOF
			} else {
				b.err = err
			}
			return 0, b.err
		}
		b.dec, b.err = b.open(br)
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.dec.Read(p)
}

func (b *decodedBody) Close() error {
	if b.dec != nil {
		_ = b.dec.Close()
	}
	return b.raw.Close()
}

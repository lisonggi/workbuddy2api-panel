package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// readRequestBody 读入站请求体，按 Content-Encoding 解压（gzip / deflate / br / zstd，解法同
// 出站响应的 upstream.DecodeBody）。生产 10-19～10-20 有客户端发来 gzip 压缩的请求体，网关
// 以前原样转给上游，上游报 11101「invalid character '\x1f'」，一天四十多次。
// 没标 Content-Encoding 但开头是 gzip 魔数（1f 8b）时也按 gzip 解：JSON 不会以这两个字节开头。
func readRequestBody(r *http.Request) ([]byte, error) {
	if dec, ok := upstream.DecodeBody(r.Header.Get("Content-Encoding"), r.Body); ok {
		defer func() { _ = dec.Close() }()
		body, err := io.ReadAll(dec)
		if err != nil {
			return nil, fmt.Errorf("decompress body: %w", err)
		}
		return body, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decompress body: %w", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("decompress body: %w", err)
	}
	return body, nil
}

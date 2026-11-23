package exploreboundary

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/dlp"
)

const responseWithheld = "response_withheld"

var responseEscapes = regexp.MustCompile(`%[0-9a-fA-F]{2}|\\u[0-9a-fA-F]{4}`)

func responseHasCredential(text string) bool {
	if dlp.CredentialText(text) != text {
		return true
	}
	// 정규화된 문자열은 검사에만 쓰고 실제 응답에는 사용하지 않는다.
	decoded := responseEscapes.ReplaceAllStringFunc(html.UnescapeString(text), func(s string) string {
		if s[0] == '%' {
			out, _ := url.PathUnescape(s)
			return out
		}
		value, _ := strconv.ParseUint(s[2:], 16, 16)
		return string(rune(value))
	})
	return decoded != text && dlp.CredentialText(decoded) != decoded
}

func responseMayContainCredential(resp *http.Response, body []byte, maxBytes int64) bool {
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Location", "Refresh"} {
		for _, value := range resp.Header.Values(key) {
			if key == "Authorization" || key == "Proxy-Authorization" || responseHasCredential(value) {
				return true
			}
		}
	}
	if len(body) == 0 {
		return false
	}
	var decoded io.ReadCloser
	var err error
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		decoded, err = gzip.NewReader(bytes.NewReader(body))
	case "deflate":
		decoded, err = zlib.NewReader(bytes.NewReader(body))
	default:
		return true
	}
	if err != nil {
		return true
	}
	if decoded != nil {
		defer decoded.Close()
		body, err = io.ReadAll(io.LimitReader(decoded, maxBytes+1))
		defer clear(body)
		if err != nil || int64(len(body)) > maxBytes {
			return true
		}
	}
	media := mediaType(resp.Header.Get("Content-Type"))
	if (strings.HasPrefix(media, "text/") || media == "application/json" || strings.HasSuffix(media, "+json")) && !utf8.Valid(body) {
		return true
	}
	return responseHasCredential(string(body))
}

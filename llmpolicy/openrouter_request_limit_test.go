package llmpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func openRouterTextRequest(t *testing.T, text string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"model": OpenRouterModel, "messages": []map[string]string{{"role": "user", "content": text}}, "max_tokens": 100, "stream": false})
	if err != nil {
		t.Fatal(err)
	}
	req := openRouterRequest()
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))
	return req
}
func TestOpenRouterLongContextDoesNotUseTokensAsBytes(t *testing.T) {
	for _, kind := range []string{"ascii-307289-bytes", "utf8"} {
		t.Run(kind, func(t *testing.T) {
			p := openRouterFixture(t)
			if p.ValidateCallLimits(100, 262) != nil {
				t.Fatal("documented context profile rejected")
			}
			hits := 0
			client := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
				hits++
				raw, err := io.ReadAll(r.Body)
				if err != nil || len(raw) <= 262144 || len(raw) > openRouterRequestLimit {
					t.Fatal("long request not bounded in bytes", len(raw), err)
				}
				return openRouterResponse(openRouterReply), nil
			}))
			text := strings.Repeat("가", 110000)
			if kind == "ascii-307289-bytes" {
				empty := openRouterTextRequest(t, "")
				overhead := empty.ContentLength
				empty.Body.Close()
				text = strings.Repeat("x", 307289-int(overhead))
			}
			req := openRouterTextRequest(t, text)
			if kind == "ascii-307289-bytes" && req.ContentLength != 307289 {
				t.Fatal("reproduction byte size changed")
			}
			resp, err := client.Do(req)
			if err != nil || hits != 1 {
				t.Fatal("valid long-context request rejected before wire", err, hits)
			}
			resp.Body.Close()
			if p.Snapshot().ReservedInputTokens != 262144 || p.Snapshot().ReservedOutputTokens != 100 {
				t.Fatal("byte limit changed token reservation")
			}
		})
	}
}

func TestOpenRouterSerializedRequestByteBoundary(t *testing.T) {
	p := openRouterFixture(t)
	var sizes []int
	client := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(raw))
		return openRouterResponse(openRouterReply), nil
	}))
	resp, err := client.Do(openRouterTextRequest(t, ""))
	if err != nil {
		t.Fatal("size probe failed", err)
	}
	resp.Body.Close()
	// 실측한 직렬화 오버헤드로 wire 바이트 경계를 맞춰 라우팅 주입 검사를 별도로 고정한다.
	contentBytes := openRouterRequestLimit - sizes[0]
	resp, err = client.Do(openRouterTextRequest(t, strings.Repeat(" ", contentBytes)))
	if err != nil || len(sizes) != 2 || sizes[1] != openRouterRequestLimit {
		t.Fatal("exact byte boundary rejected", err, sizes)
	}
	resp.Body.Close()
	req := openRouterTextRequest(t, strings.Repeat(" ", contentBytes+1))
	if req.ContentLength > openRouterRequestLimit {
		t.Fatal("raw read cap masks serialization cap")
	}
	resp, err = client.Do(req)
	if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || len(sizes) != 2 || p.Snapshot().Requests != 2 {
		t.Fatal("oversized serialized request reached reservation/wire", err, len(sizes))
	}
}

type openRouterCountingBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *openRouterCountingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *openRouterCountingBody) Close() error { b.closed = true; return nil }
func TestOpenRouterRawRequestReadIsBounded(t *testing.T) {
	p := openRouterFixture(t)
	hits := 0
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { hits++; return openRouterResponse(openRouterReply), nil }))
	req := openRouterTextRequest(t, strings.Repeat(" ", openRouterRequestLimit+64))
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	req.Body.Close()
	body := &openRouterCountingBody{Reader: bytes.NewReader(raw)}
	req.Body = body
	resp, err := client.Do(req)
	if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || hits != 0 || p.Snapshot().Requests != 0 || body.read != openRouterRequestLimit+1 || !body.closed {
		t.Fatal("oversized body was not bounded before parsing", err, body.read, hits)
	}
}
func TestOpenRouterSerializationExpansionIsBounded(t *testing.T) {
	p := openRouterFixture(t)
	hits := 0
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { hits++; return openRouterResponse(openRouterReply), nil }))
	raw := `{"model":"` + OpenRouterModel + `","messages":[{"role":"user","content":"` + strings.Repeat("&", openRouterRequestLimit/4) + `"}],"max_tokens":100}`
	if len(raw) > openRouterRequestLimit || !json.Valid([]byte(raw)) {
		t.Fatal("expansion fixture is not valid bounded JSON")
	}
	req := openRouterRequest()
	req.Body.Close()
	req.Body = io.NopCloser(strings.NewReader(raw))
	req.ContentLength = int64(len(raw))
	resp, err := client.Do(req)
	if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || hits != 0 || p.Snapshot().Requests != 0 {
		t.Fatal("expanded request escaped byte cap", err, hits)
	}
}
func TestOpenRouterRequestByteCapDoesNotRelaxReportedTokens(t *testing.T) {
	p := openRouterFixture(t)
	hits := 0
	reply := strings.Replace(openRouterReply, `"prompt_tokens":10`, `"prompt_tokens":262145`, 1)
	client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { hits++; return openRouterResponse(reply), nil }))
	resp, err := client.Do(openRouterTextRequest(t, strings.Repeat("x", 307289)))
	if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || hits != 1 || !p.Snapshot().Blocked || p.Snapshot().ReservedInputTokens != 262144 || p.Snapshot().InputTokens != 0 {
		t.Fatal("reported token overflow became valid usage", err, hits)
	}
}

func TestOpenRouterRawByteCapChecksCompleteJSONWithPadding(t *testing.T) {
	for _, extra := range []int{0, 1} {
		p := openRouterFixture(t)
		hits := 0
		client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { hits++; return openRouterResponse(openRouterReply), nil }))
		req := openRouterRequest()
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		req.Body.Close()
		padded := append(raw, bytes.Repeat([]byte(" "), openRouterRequestLimit-len(raw)+extra)...)
		if !json.Valid(padded) {
			t.Fatal("padded JSON control is invalid")
		}
		req.Body = io.NopCloser(bytes.NewReader(padded))
		req.ContentLength = int64(len(padded))
		resp, err := client.Do(req)
		if extra == 0 {
			if err != nil || hits != 1 {
				t.Fatal("raw byte boundary rejected", err, hits)
			}
			resp.Body.Close()
		} else if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || hits != 0 || p.Snapshot().Requests != 0 {
			t.Fatal("complete JSON bypassed raw byte cap", err, hits)
		}
	}
}

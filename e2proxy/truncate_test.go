package e2proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// C16 결정안 2: 본문 상한을 넘는 응답은 앞부분만 정상 HTTP 응답으로 준다. 대상이 답했으므로 withheld + response_truncated이고
// 결과 불명이 아니다. Content-Length 응답과 chunked 응답 둘 다 같다.
func TestOversizedResponseIsTruncatedNotUnknown(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run("chunked="+strconv.FormatBool(chunked), func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := strings.Repeat("a", 20000)
				if chunked {
					w.Header().Set("Trailer", "X-Fixture-Trailer")
					for i := 0; i < 20; i++ {
						io.WriteString(w, body[i*1000:(i+1)*1000])
						w.(http.Flusher).Flush()
					}
					w.Header().Set("X-Fixture-Trailer", "end")
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				io.WriteString(w, body)
			}))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			cfg.Limits.MaxBodyBytes = 4096
			cfg.Limits.MaxUnknown = 1
			e, call := coreOpen(t, cfg)
			resp, r, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
			if err != nil || resp == nil {
				t.Fatalf("truncated response not delivered: %v %+v", err, r)
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil || len(body) != 4096 || resp.ContentLength != 4096 || resp.Header.Get("Content-Length") != "4096" ||
				len(resp.TransferEncoding) != 0 || resp.Header.Get("Trailer") != "" || len(resp.Trailer) != 0 {
				t.Fatalf("framing: len=%d cl=%d hdr=%q te=%v trailer=%v err=%v", len(body), resp.ContentLength, resp.Header.Get("Content-Length"), resp.TransferEncoding, resp.Trailer, readErr)
			}
			counts := e.Snapshot().Counts
			if r.Reason != Withheld || !r.ResponseTruncated || r.ResponseComplete || !r.ResponseReceived || counts.Withheld != 1 || counts.Unknown != 0 {
				t.Fatalf("truncated receipt: %+v counts=%+v", r, counts)
			}
			obs := call.Result().Observations
			if len(obs) != 1 || !obs[0].ResponseTruncated || obs[0].Reason != Withheld {
				t.Fatalf("trusted observation lost truncation: %+v", obs)
			}
			// 결과 불명 상한(1)에 닿지 않았으므로 같은 call이 계속 보낸다.
			if resp, r, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); err != nil || r.Reason != Withheld {
				t.Fatalf("second truncated request: %v %+v", err, r)
			}
			resp.Body.Close()
		})
	}
}

// 전달 구간 끝에 걸친 보호 값(앞 절반은 전달 구간 안, 나머지는 밖)은 검사 상한 안에 통째로 들어와 응답 전체를 보류한다.
func TestSecretAcrossTruncationBoundaryIsWithheld(t *testing.T) {
	const secret = "protected-session-value-straddles-cut"
	const limit = 4096
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var b bytes.Buffer
		b.WriteString(strings.Repeat("a", limit-len(secret)/2))
		b.WriteString(secret)
		b.WriteString(strings.Repeat("b", 9000))
		w.Write(b.Bytes())
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.Limits.MaxBodyBytes = limit
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {secret}}
	_, call := coreOpen(t, cfg)
	resp, r, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrWithheld) || resp != nil || r.Reason != Withheld || r.ResponseTruncated || hits.Load() != 1 {
		t.Fatalf("secret across the cut was delivered: %v %+v", err, r)
	}
}

// 응답이 검사 상한보다 길어도 검사 상한까지 읽고 닫는다. 끊김이 아니므로 결과 불명이 아니다.
func TestResponseLongerThanScanLimitStillTruncates(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 64; i++ {
			if _, err := io.WriteString(w, strings.Repeat("z", 4096)); err != nil {
				return
			}
		}
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.Limits.MaxBodyBytes = 4096
	_, call := coreOpen(t, cfg)
	resp, r, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || resp == nil || r.Reason != Withheld || !r.ResponseTruncated {
		t.Fatalf("long response: %v %+v", err, r)
	}
	resp.Body.Close()
}

// 실제 프록시 경로: 도구는 정상 HTTP 응답으로 앞부분을 받고 결과 헤더에 truncated 표시가 붙는다.
func TestProxyDeliversTruncatedResponse(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(OutcomeHeader, "complete")
		io.WriteString(w, strings.Repeat("x", 9000))
	}))
	defer target.Close()
	_, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, roots := proxyTestListener(t, call)
	resp, err := proxyTestClient(t, addr, roots).Get(target.URL + "/allowed")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	limit := int(call.e.cfg.Limits.MaxBodyBytes)
	if readErr != nil || resp.StatusCode != 200 || len(body) != limit || resp.Header.Get(OutcomeHeader) != OutcomeTruncated {
		t.Fatalf("proxy truncation: status=%d len=%d outcome=%q err=%v", resp.StatusCode, len(body), resp.Header.Get(OutcomeHeader), readErr)
	}
}

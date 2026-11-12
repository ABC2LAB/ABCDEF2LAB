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

// C16 결정안 3: 응답 전달 상한을 요청 상한과 따로 준다. 요청 본문은 요청 상한으로만, 응답은 응답 상한으로만 자른다.
func TestSeparateResponseLimitTruncatesAtResponseLimit(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("r", 20000))
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
	cfg.Limits.MaxBodyBytes = 1024
	cfg.Limits.MaxResponseBodyBytes = 10000
	_, call := coreOpen(t, cfg)
	resp, r, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || resp == nil || !r.ResponseTruncated {
		t.Fatalf("response limit: %v %+v", err, r)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) != 10000 {
		t.Fatalf("response cut at %d, want the response limit 10000", len(body))
	}
	// 규칙이 GET만 허용하므로 본문을 실은 GET으로 요청 본문 상한만 따로 본다.
	big := coreRequest(t, "GET", target.URL+"/allowed")
	big.Body = io.NopCloser(strings.NewReader(strings.Repeat("q", 2048)))
	big.ContentLength = 2048
	if _, r, err := call.Do(context.Background(), big); err == nil || r.Reason != Unsupported || r.ForwardAttempted {
		t.Fatalf("request body over the request limit was sent: %v %+v", err, r)
	}
}

// 검사 상한은 응답 상한 + 가장 긴 보호 값 길이 − 1 이상이어야 한다. 어기거나 범위 밖이면 정책 적재를 거부한다.
func TestResponseScanLimitConditionIsChecked(t *testing.T) {
	const secret = "protected-value-of-32-bytes-long"
	base := func() Config {
		cfg := coreConfig(t, "http://127.0.0.1:1", &coreRecorder{}, 2)
		cfg.HeaderMode = HeaderProtectedSessionProposal
		cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {secret}}
		cfg.Limits.MaxResponseBodyBytes = 4096
		return cfg
	}
	ok := base()
	ok.Limits.MaxResponseScanBytes = 4096 + int64(len(secret)) - 1
	if err := ValidatePolicyTemplate(ok); err != nil {
		t.Fatalf("scan limit meeting the condition rejected: %v", err)
	}
	// 보호 값이 없어도 검사 상한은 전달 상한 이상이어야 한다(그래야 넘는 응답을 알아챈다).
	plain := base()
	plain.HeaderMode = HeaderPreserve
	plain.Rules[0].CredentialHeaders = nil
	plain.Limits.MaxResponseScanBytes = 4096
	if err := ValidatePolicyTemplate(plain); err != nil {
		t.Fatalf("scan equal to the response limit rejected without secrets: %v", err)
	}
	plain.Limits.MaxResponseScanBytes = 4095
	if ValidatePolicyTemplate(plain) == nil {
		t.Error("scan below the response limit accepted without secrets")
	}
	for name, change := range map[string]func(*Limits){
		"scan below condition":   func(l *Limits) { l.MaxResponseScanBytes = 4096 + int64(len(secret)) - 2 },
		"negative response":      func(l *Limits) { l.MaxResponseBodyBytes = -1 },
		"negative scan":          func(l *Limits) { l.MaxResponseScanBytes = -1 },
		"response over cap":      func(l *Limits) { l.MaxResponseBodyBytes = maxResponseBodyBytes + 1 },
		"scan over cap":          func(l *Limits) { l.MaxResponseScanBytes = maxResponseScanBytes + 1 },
		"scan without body room": func(l *Limits) { l.MaxResponseBodyBytes = 0; l.MaxBodyBytes = 8192; l.MaxResponseScanBytes = 4096 },
	} {
		cfg := base()
		change(&cfg.Limits)
		if ValidatePolicyTemplate(cfg) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// 보호 값이 없는 실행에서 검사 상한 = 전달 상한이면 넘는 응답을 정상으로 자른다(완전 응답으로 기록하지 않는다).
// 적재 검사를 거치지 않은 한도가 와도 읽기는 전달 상한 아래로 내려가지 않는다.
func TestScanLimitAtResponseLimitStillDetectsOversize(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "20000")
		io.WriteString(w, strings.Repeat("s", 20000))
	}))
	defer target.Close()
	for _, scan := range []int64{4096, 4095} {
		cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
		cfg.Limits.MaxResponseBodyBytes = 4096
		cfg.Limits.MaxResponseScanBytes = scan
		e := &Execution{cfg: cfg}
		if limit, read := responseLimits(cfg.Limits, e.longestSecret()); limit != 4096 || read < limit {
			t.Fatalf("scan %d: read limit %d below the response limit %d", scan, read, limit)
		}
		if scan < 4096 {
			continue // 적재 검사가 막는 값이라 실행은 만들지 않는다(위 TestResponseScanLimitConditionIsChecked).
		}
		_, call := coreOpen(t, cfg)
		resp, r, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
		if err != nil || resp == nil || !r.ResponseTruncated || r.ResponseComplete {
			t.Fatalf("scan %d: oversize response recorded as %+v (%v)", scan, r, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(body) != 4096 || resp.ContentLength != 4096 {
			t.Fatalf("scan %d: delivered %d bytes with Content-Length %d", scan, len(body), resp.ContentLength)
		}
	}
}

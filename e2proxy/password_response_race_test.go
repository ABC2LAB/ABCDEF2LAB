package e2proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPasswordResponseRegistrationDuringRealReadWithholds(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "public prefix ")
		w.(http.Flusher).Flush()
		close(entered)
		<-release
		io.WriteString(w, "newly-protected-password")
	}))
	defer target.Close()
	defer once.Do(func() { close(release) })
	e, call := coreOpen(t, passwordConfig(t, target.URL, &coreRecorder{}))
	if e.AddProtectedPasswords([]byte("initial-password")) != nil {
		t.Fatal("초기 보호 값 등록 실패")
	}
	type outcome struct {
		response *http.Response
		receipt  Receipt
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, receipt, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed/slow"))
		done <- outcome{response, receipt, err}
	}()
	<-entered
	if e.AddProtectedPasswords([]byte("newly-protected-password")) != nil {
		t.Fatal("읽기 중 등록 실패")
	}
	once.Do(func() { close(release) })
	got := <-done
	if got.response != nil || !errors.Is(got.err, ErrWithheld) || got.receipt.Reason != Withheld || e.Snapshot().Counts.Unknown != 0 {
		t.Fatal("새 검사 세대가 이전 읽기 길이를 안전으로 처리")
	}
}

func TestPasswordResponseChunkedTrailersAndBodyCeiling(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "chunked", true: "oversized"}[oversized], func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Trailer", "X-Public, Digest")
				w.(http.Flusher).Flush()
				io.WriteString(w, "before seeded-password after")
				if oversized {
					io.WriteString(w, strings.Repeat("x", 512))
				}
				w.Header().Set("X-Public", "trailer")
				w.Header().Set("Digest", "synthetic-original")
			}))
			defer target.Close()
			cfg := passwordConfig(t, target.URL, &coreRecorder{})
			cfg.Limits.MaxBodyBytes = 64
			e, call := coreOpen(t, cfg)
			if e.AddProtectedPasswords([]byte("seeded-password")) != nil {
				t.Fatal("등록 실패")
			}
			response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/page"))
			if oversized {
				if response != nil || !errors.Is(err, ErrWithheld) || receipt.Reason != Withheld || e.Snapshot().Counts.Unknown != 0 {
					t.Fatal("불완전 본문을 앞부분 가림으로 전달")
				}
				return
			}
			if err != nil || response == nil {
				t.Fatal("정상 chunked 응답 가림 실패", err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != "before [REDACTED] after" || len(response.Trailer) != 0 || len(response.TransferEncoding) != 0 || response.Header.Get("Trailer") != "" || response.ContentLength != int64(len(body)) || receipt.ResponseComplete || receipt.ResponseTruncated {
				t.Fatal("가림 후 chunked/trailer/길이·영수증 불일치")
			}
		})
	}
}

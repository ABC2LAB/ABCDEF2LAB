package exploreboundary

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

func fixtureFetch(f *proxyFixture) DirectFetch {
	return DirectFetch{Proxy: f.proxyURL, Roots: f.mitm.pool, Timeout: proxyTimeout, MaxBytes: proxyMaxResponse,
		Status: func(context.Context) (exploration.State, error) { return f.session.Status(), nil }}
}

// 정적 fetch는 경계가 붙인 순번으로 받은 본문을 같은 순번의 private 관찰과 맞춘다. 경계는 응답을 쓰기 전에 관찰을
// 남기므로 받자마자 그 순번을 읽을 수 있다. 대상이 보낸 같은 이름의 헤더는 지운다.
func TestDirectFetchBindsBodyToBoundaryObservation(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(SeqHeader, "999")
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<a href="/app/item">item</a>`)
	}, nil)
	result, err := fixtureFetch(f).Fetch(t.Context(), f.target+"/app")
	if err != nil || result.Reason != "" || !result.Complete || result.Seq == 0 || result.Seq == 999 || string(result.Body) != `<a href="/app/item">item</a>` {
		t.Fatalf("fetch: %+v %v", result, err)
	}
	o, err := f.session.Reader().ReadObservation(t.Context(), f.session.flow, result.Seq)
	if err != nil || o.Path != "/app" || o.Method != "GET" || o.ResponseSHA256 != sha256.Sum256(result.Body) || o.ResponseContentType != "text/html" {
		t.Fatalf("observation for fetched seq: %+v %v", o, err)
	}
}

// 범위 밖 요청은 그 요청만 실패한다(세션은 그대로). 상한을 넘는 응답은 순번은 있고 완전 응답이 아니다.
func TestDirectFetchRefusalAndOversizedResponse(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/big" {
			io.WriteString(w, strings.Repeat("x", proxyMaxResponse+1))
			return
		}
		io.WriteString(w, "ok")
	}, nil)
	fetch := fixtureFetch(f)
	if result, err := fetch.Fetch(t.Context(), f.target+"/other"); !errors.Is(err, ErrFetchRefused) || result.Reason != "" || f.hits.Load() != 0 {
		t.Fatalf("out-of-scope fetch: %+v %v hits=%d", result, err, f.hits.Load())
	}
	result, err := fetch.Fetch(t.Context(), f.target+"/app/big")
	if err != nil || result.Seq == 0 || result.Complete || result.Reason != "" || int64(len(result.Body)) > fetch.MaxBytes {
		t.Fatalf("oversized fetch: seq=%d complete=%v reason=%q body=%d err=%v", result.Seq, result.Complete, result.Reason, len(result.Body), err)
	}
	if !f.session.Status().Active {
		t.Fatal("single refusal closed the session")
	}
}

// 동시 연결 상한(cap_exhausted)처럼 그 요청만 막힌 경우는 세션이 열려 있으므로 탐색을 마감하지 않는다.
func TestDirectFetchPerRequestCapIsNotSessionClose(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/hold" {
			entered <- struct{}{}
			<-release
		}
		io.WriteString(w, "ok")
	}, nil)
	f.session.binding.Limits.MaxConcurrency = 1
	fetch := fixtureFetch(f)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = fetch.Fetch(context.Background(), f.target+"/app/hold")
	}()
	<-entered
	result, err := fetch.Fetch(t.Context(), f.target+"/app")
	close(release)
	<-done
	if !errors.Is(err, ErrFetchRefused) || result.Reason != "" || !f.session.Status().Active {
		t.Fatalf("per-request cap treated as session close: %+v %v", result, err)
	}
}

// 세션이 닫혀 막히면 닫힌 사유로 탐색을 마감한다(요청 실패로 다음 경로를 계속 두드리지 않음).
func TestDirectFetchReportsSessionCloseReason(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := f.session.Close(ctx, exploration.TargetEnded); err != nil {
		t.Fatal(err)
	}
	result, err := fixtureFetch(f).Fetch(t.Context(), f.target+"/app")
	if err != nil || result.Reason != string(exploration.TargetEnded) || f.hits.Load() != 0 {
		t.Fatalf("closed session fetch: %+v %v", result, err)
	}
	broken := fixtureFetch(f)
	broken.Status = func(context.Context) (exploration.State, error) { return exploration.State{}, ErrUnavailable }
	if result, err := broken.Fetch(t.Context(), f.target+"/app"); err != nil || result.Reason != string(exploration.BoundaryUnavailable) {
		t.Fatalf("unreadable status: %+v %v", result, err)
	}
}

func TestDirectFetchRejectsIncompleteConfigAndNonHTTPS(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	for name, change := range map[string]func(*DirectFetch){
		"no proxy":  func(d *DirectFetch) { d.Proxy = nil },
		"no roots":  func(d *DirectFetch) { d.Roots = nil },
		"no status": func(d *DirectFetch) { d.Status = nil },
		"no limit":  func(d *DirectFetch) { d.MaxBytes = 0 },
	} {
		d := fixtureFetch(f)
		change(&d)
		if _, err := d.Fetch(t.Context(), f.target+"/app"); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := fixtureFetch(f).Fetch(t.Context(), "http://"+fixtureHost+"/app"); !errors.Is(err, ErrFetchRefused) || f.hits.Load() != 0 {
		t.Fatalf("plain http fetch: %v", err)
	}
}

// 실행기가 순번 헤더를 붙여 보내도 대상에 가지 않는다(경계 표시 헤더는 경계만 쓴다).
func TestProxyDoesNotForwardSeqHeaderToTarget(t *testing.T) {
	var got []string
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values(SeqHeader)
		io.WriteString(w, "ok")
	}, nil)
	req, _ := http.NewRequest(http.MethodGet, f.target+"/app", nil)
	req.Header.Set(SeqHeader, "7")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if f.hits.Load() != 1 || len(got) != 0 || resp.Header.Get(SeqHeader) == "7" || resp.Header.Get(SeqHeader) == "" {
		t.Fatalf("seq header leaked to target %q or response %q", got, resp.Header.Get(SeqHeader))
	}
}

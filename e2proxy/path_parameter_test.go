package e2proxy

// #487 리뷰(C17): 경로 매개변수·인코딩 경로 반례.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// C17: 경로 매개변수(;)는 Tomcat·Spring 계열 대상에서 조각 이름에서 잘려 ..가 된다. 매개변수를 뗀 이름이
// 점 조각이면 보내지 않는다(인코딩한 ;도 같다).
func TestDotSegmentWithPathParameterRefused(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.RequestURI)
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 10))
	for _, p := range []string{"/allowed/..;/secret", "/allowed/..%3b/secret", "/allowed/..%3B/secret", "/allowed/%2e%2e;/secret"} {
		resp, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+p))
		if resp != nil {
			resp.Body.Close()
		}
		if r.ForwardAttempted || r.Reason != PolicyDenied || err == nil {
			t.Errorf("%s: reason=%s forward=%v err=%v", p, r.Reason, r.ForwardAttempted, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Fatalf("target saw dot-segment paths: %q", seen)
	}
}

// C17: 인코딩·제어 문자 경로 묶음. 승인 범위를 바꿀 수 있는 표기만 막고, 대상이 달리 해석할 뿐인 정상 인코딩은 보낸다.
func TestEncodedPathBatch(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.RequestURI)
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 100))
	for _, p := range []string{
		"/allowed/%2E%2E/secret", "/allowed/%252e%252e/secret", "/allowed/%5c../secret", "/allowed/%c0%ae%c0%ae/secret",
		"/allowed/%ef%bc%8e%ef%bc%8e/secret", "/allowed/%EF%BC%8E%EF%BC%8E/secret", "/allowed/..%20/secret", "/allowed/.%00./secret",
		"/allowed/x%3f/../../secret", "/allowed%3b/../secret", "/allowed;/secret", "/ALLOWED/x", "/al%6cowed/x", "/allowed%2fx",
		"/allowed/a+b", "/allowed/%41", "/allowed/%e2%80%ae", "/allowed/%c2%85", "/allowed/%7f", "/allowed/x/..;", "/allowed/%2e",
	} {
		resp, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+p))
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil && r.ForwardAttempted {
			t.Errorf("%s: forwarded with error %v", p, err)
		}
		// 점 조각·인코딩한 경계·제어 문자는 보내지 않는다. 정상 인코딩·대상 해석 차이만 있는 경로는 보낸다.
		blocked := map[string]bool{"/allowed/%2E%2E/secret": true, "/allowed/%252e%252e/secret": true, "/allowed/%5c../secret": true, "/allowed/%c0%ae%c0%ae/secret": true,
			"/allowed/.%00./secret": true, "/allowed/x%3f/../../secret": true, "/allowed%3b/../secret": true, "/allowed;/secret": true, "/ALLOWED/x": true, "/allowed%2fx": true,
			"/allowed/%c2%85": true, "/allowed/%7f": true, "/allowed/x/..;": true, "/allowed/%2e": true}
		if r.ForwardAttempted == blocked[p] {
			t.Errorf("%s forwarded=%v", p, r.ForwardAttempted)
		}
	}
}

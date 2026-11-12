package e2proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// 인코딩 경로(C17). 정상 percent 인코딩은 실제로 보내고, 범위 검사와 대상 해석이 갈릴 수 있는 인코딩은 막는다.
func TestEncodedPathsFollowDecodedScopeAndBlockHazards(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.EscapedPath())
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 16))
	for _, path := range []string{"/allowed/hello%20world", "/allowed/%ED%95%9C%EA%B8%80", "/allowed/a+b"} {
		resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+path))
		if err != nil || r.Reason != Complete {
			t.Fatalf("normal encoded path %s refused: %v %v", path, r.Reason, err)
		}
		resp.Body.Close()
	}
	for _, path := range []string{
		"/allowed/%2e%2e/secret", "/allowed/%2E%2E/secret", "/allowed/..%2fsecret", "/allowed/a%2Fb", "/allowed/a%5Cb",
		"/allowed/%252e%252e/secret", "/allowed/a%00b", "/allowed/./x", "/allowed/x/../../secret", "/allowed//x", "/allowed%2fx",
	} {
		if _, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+path)); err == nil || r.Reason != PolicyDenied || r.ForwardAttempted {
			t.Fatalf("hazardous path %s: %v %v", path, r.Reason, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// 대상은 보낸 그대로의 인코딩을 받는다(범위 검사는 풀린 경로, 송신은 같은 요청의 원래 표기).
	want := []string{"/allowed/hello%20world", "/allowed/%ED%95%9C%EA%B8%80", "/allowed/a+b"}
	if len(seen) != len(want) {
		t.Fatalf("target saw %d requests: %v", len(seen), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("wire path changed: %q want %q", seen[i], want[i])
		}
	}
}

func TestEncodedPathCannotEscapePrefix(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("escaped prefix reached target", r.URL.EscapedPath())
	}))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 4))
	// 풀면 /allowed-other로 승인 접두 밖이다. 인코딩한 문자로 접두 비교를 속이지 못한다.
	if _, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed%2Dother")); err == nil || r.Reason != PolicyDenied {
		t.Fatal("encoded character bypassed the prefix boundary", r.Reason, err)
	}
}

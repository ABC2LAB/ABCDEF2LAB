package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestCrawlWritePageCarriesAttempts(t *testing.T) {
	rec, _ := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	page := get(t, h, "/crawl/S13")
	if page.Code != http.StatusOK {
		t.Fatal(page.Code)
	}
	body := page.Body.String()
	for _, part := range []string{
		`<iframe name="s13-sink" hidden>`, `f.action="/write/S13-form";f.target="s13-sink"`, `f.submit();f.remove()`,
		`action="/write/S13-click"`, `<button id="save">`,
		`send("POST","/write/S13-post")`, `send("PUT","/write/S13-put")`, `send("PATCH","/write/S13-patch")`,
		`send("DELETE","/write/S13-delete")`, `send("POST","/write/remove-S13")`, `send("POST","/write/S13-excluded")`,
		`getElementById("s13-attempted").textContent=String(n)`, `try{fetch(p,`,
	} {
		if !strings.Contains(body, part) {
			t.Fatal(part, body)
		}
	}
	// 자동 폼은 DOM에 남지 않고(크롤러가 다시 제출하지 못함), 다른 시도의 완료를 기다리지 않는다(#377 리뷰).
	script := body[strings.Index(body, "<script>"):]
	if regexp.MustCompile(`<form[^>]*/write/S13-form`).MatchString(body) || strings.Contains(script, ".then(") ||
		strings.Contains(script, "await") || strings.Contains(script, "Promise") ||
		strings.Index(script, "f.submit()") > strings.Index(script, "fetch(") {
		t.Fatal(script)
	}
	// 우회 페이지와 크롤링 페이지는 서로의 경로로 열리지 않는다.
	for _, path := range []string{"/crawl/S99", "/crawl/B14", "/bypass/S13"} {
		if got := get(t, h, path); got.Code != http.StatusNotFound {
			t.Fatal(path, got.Code)
		}
	}
}

// 페이지가 시도하는 쓰기는 전부 이름과 메서드가 등록되어 있고, 등록된 이름은 전부 어떤 페이지가 시도한다.
func TestEveryWriteAttemptIsAKnownHit(t *testing.T) {
	sent := regexp.MustCompile(`send\("([A-Z]+)","/write/([^"]+)"\)`)
	attempted := map[string]string{}
	for _, pages := range []map[string]string{scenarios, crawlScenarios} {
		for id, page := range pages {
			methods := map[string]string{}
			for _, match := range sent.FindAllStringSubmatch(page, -1) {
				methods[match[2]] = match[1]
			}
			for _, match := range regexp.MustCompile(`/write/([^"']+)`).FindAllStringSubmatch(page, -1) {
				method, ok := methods[match[1]]
				if !ok {
					method = http.MethodPost // 폼·fetch POST·sendBeacon
				}
				if writeHits[match[1]] != method {
					t.Fatal(id, match[1], method)
				}
				attempted[match[1]] = method
			}
		}
	}
	if !reflect.DeepEqual(attempted, writeHits) {
		t.Fatal(attempted)
	}
}

func TestWritesAreRecordedOnlyWithTheirMethod(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	cases := []struct {
		method, name string
		status       int
		want         string
	}{
		{http.MethodPost, "S13-post", http.StatusNoContent, "S13-post"},
		{http.MethodPut, "S13-put", http.StatusNoContent, "S13-put"},
		{http.MethodPatch, "S13-patch", http.StatusNoContent, "S13-patch"},
		{http.MethodDelete, "S13-delete", http.StatusNoContent, "S13-delete"},
		{http.MethodPost, "remove-S13", http.StatusNoContent, "remove-S13"},
		// 메서드가 다르면 그 이름으로 세지 않는다. 쓰기가 아닌 탐침도 마찬가지다.
		{http.MethodOptions, "S13-put", http.StatusMethodNotAllowed, unknownScenario},
		{http.MethodPut, "B14-fetch", http.StatusMethodNotAllowed, unknownScenario},
		{http.MethodPost, "S13-delete", http.StatusNoContent, unknownScenario},
		{http.MethodPut, sentinel, http.StatusMethodNotAllowed, unknownScenario},
	}
	var want []row
	for i, tc := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "/write/"+tc.name+"?q="+sentinel, strings.NewReader(sentinel)))
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
		want = append(want, row{uint64(i + 1), surfaceTargetWrite, tc.want, tc.method, false})
	}
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/write/S13-post", nil))
	if head.Code != http.StatusNotFound {
		t.Fatal(head.Code)
	}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), sentinel) {
		t.Fatal(string(data))
	}
}

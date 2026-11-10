package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	for _, part := range []string{
		`action="/write/S13-form"`, `action="/write/S13-click"`, `<button id="save">`,
		`send("POST","/write/S13-post")`, `send("PUT","/write/S13-put")`, `send("PATCH","/write/S13-patch")`,
		`send("DELETE","/write/S13-delete")`, `send("POST","/write/remove-S13")`, `send("POST","/write/S13-excluded")`,
		`getElementById("auto").submit()`,
	} {
		if !strings.Contains(page.Body.String(), part) {
			t.Fatal(part, page.Body.String())
		}
	}
	// 우회 페이지와 크롤링 페이지는 서로의 경로로 열리지 않는다.
	for _, path := range []string{"/crawl/S99", "/crawl/B14", "/bypass/S13"} {
		if got := get(t, h, path); got.Code != http.StatusNotFound {
			t.Fatal(path, got.Code)
		}
	}
}

// 페이지가 시도하는 쓰기는 전부 이름이 등록되어 있고, 등록된 이름은 전부 어떤 페이지가 시도한다.
func TestEveryWriteAttemptIsAKnownHit(t *testing.T) {
	attempted := map[string]bool{}
	for _, pages := range []map[string]string{scenarios, crawlScenarios} {
		for id, page := range pages {
			for _, match := range regexp.MustCompile(`/write/([^"']+)`).FindAllStringSubmatch(page, -1) {
				if !writeHits[match[1]] {
					t.Fatal(id, match[1])
				}
				attempted[match[1]] = true
			}
		}
	}
	if !reflect.DeepEqual(attempted, writeHits) {
		t.Fatal(attempted)
	}
}

func TestWritesAreRecordedForEveryMethod(t *testing.T) {
	rec, path := newTestRecorder(t)
	h := targetHandler(rec, testReceiver)
	cases := []struct{ method, name string }{
		{http.MethodPost, "S13-post"}, {http.MethodPut, "S13-put"}, {http.MethodPatch, "S13-patch"},
		{http.MethodDelete, "S13-delete"}, {http.MethodPost, "remove-S13"}, {http.MethodPut, sentinel},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "/write/"+tc.name+"?q="+sentinel, strings.NewReader(sentinel)))
		if w.Code != http.StatusNoContent {
			t.Fatal(tc, w.Code)
		}
	}
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/write/S13-post", nil))
	if head.Code != http.StatusNotFound {
		t.Fatal(head.Code)
	}
	want := []row{{1, surfaceTargetWrite, "S13-post", "POST", false}, {2, surfaceTargetWrite, "S13-put", "PUT", false},
		{3, surfaceTargetWrite, "S13-patch", "PATCH", false}, {4, surfaceTargetWrite, "S13-delete", "DELETE", false},
		{5, surfaceTargetWrite, "remove-S13", "POST", false}, {6, surfaceTargetWrite, unknownScenario, "PUT", false}}
	if got := readRows(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), sentinel) {
		t.Fatal(string(data))
	}
}

// 위험 경로 시도는 엔진의 내장 삭제 엔드포인트 규칙에 걸리는 이름이어야 하고, 나머지 S13 이름은 걸리지 않아야 한다.
// 규칙이 바뀌면 이 시험이 먼저 알려 준다.
func TestRiskyWritePathMatchesTheBuiltinRule(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "db", "db.go"))
	if err != nil {
		t.Fatal(err)
	}
	found := regexp.MustCompile("const deleteEndpointPathPattern = `([^`]+)`").FindSubmatch(src)
	if found == nil {
		t.Fatal("deleteEndpointPathPattern not found")
	}
	rule := regexp.MustCompile(string(found[1]))
	for name := range writeHits {
		if !strings.HasPrefix(name, "S13") && name != "remove-S13" {
			continue
		}
		if got := rule.MatchString("/write/" + name); got != (name == "remove-S13") {
			t.Fatal(name, got)
		}
	}
}

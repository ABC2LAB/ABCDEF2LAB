package diagnosisflow

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2runner"
)

// 크롤·발견 분류도 풀린 경로로 위험 동작 단어·제외 경로를 본다(#627). 정규 표기만 보면 단어 앞 이스케이프(%3C·%5B)가
// 단어에 붙어 delete를 놓치고, 비ASCII 제외 경로는 인코딩 표기(%C3%BCber)와 원문(über)이 달라 맞지 않아 크롤이 실제로
// 방문(전송)했다. 평범한 비ASCII 경로와 평문 링크는 지금처럼 방문한다.
func TestCrawlChecksDecodedPathForRiskAndExclusion(t *testing.T) {
	for _, writes := range []bool{false, true} {
		c, _ := flowFixture(t, Auto)
		c.binding.ReversibleWrites = writes
		c.binding.Limits.MaxPages = 12
		c.binding.Excluded = append(c.binding.Excluded, "/read/über")
		links := []string{"/read/%3Cdelete", "/read/a%5Bdelete", "/read/%7Bremove%7D", "/read/%C3%BCber", "/read/%C3%BCber/deeper", "/read/delete", "/read/plain", "/read/%C3%A9t%C3%A9"}
		html := ""
		for _, l := range links {
			html += `<a href="` + l + `">x</a>`
		}
		var visited []string
		if _, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
			visited = append(visited, strings.TrimPrefix(target, "https://owned.test"))
			return crawlResult(html), nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, refused := range []string{"/read/%3Cdelete", "/read/a%5Bdelete", "/read/%7Bremove%7D", "/read/%C3%BCber", "/read/%C3%BCber/deeper", "/read/delete"} {
			if has(visited, refused) {
				t.Fatalf("writes=%v crawl sent %s", writes, refused)
			}
			c.mu.Lock()
			e := c.endpointLocked("GET", refused)
			c.mu.Unlock()
			if e != nil && (e.InScope || e.Change != "blocked" || e.Selected) {
				t.Fatalf("writes=%v %s classified as sendable: %+v", writes, refused, *e)
			}
		}
		for _, allowed := range []string{"/read/plain", "/read/%C3%A9t%C3%A9"} {
			if !has(visited, allowed) {
				t.Fatalf("writes=%v crawl skipped ordinary link %s (visited %q)", writes, allowed, visited)
			}
		}
	}
}

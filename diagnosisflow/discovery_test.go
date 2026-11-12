package diagnosisflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func discoveredEndpoint(t *testing.T, c *Controller, method, path string) *Endpoint {
	t.Helper()
	s, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for i := range s.Endpoints {
		if s.Endpoints[i].Method == method && s.Endpoints[i].Path == path {
			return &s.Endpoints[i]
		}
	}
	return nil
}

// 보고된 엔드포인트는 추정 발견이다. 승인 범위 안이면 저장하지만, 읽기 접두 안의 GET이어도 자동 방문·선택 대상
// (attested_read)으로 올리지 않는다. 키 이름은 정렬·중복 제거되고 content-type은 매개변수 없이 소문자로 남는다.
func TestReportStaticStoresInferredDiscovery(t *testing.T) {
	c, store := flowFixture(t, Auto)
	before, _ := c.Snapshot()
	n, err := c.ReportStatic(context.Background(), []Discovery{
		{Method: "get", Path: "/read/orders", QueryKeys: []string{"page", "id", "page"}},
		{Method: "POST", Path: "/read/orders/search", BodyKeys: []string{"filter[status]", "q"}, ContentType: "Application/JSON"},
	})
	if err != nil || n != 2 {
		t.Fatalf("accepted=%d err=%v", n, err)
	}
	get := discoveredEndpoint(t, c, "GET", "/read/orders")
	if get == nil || get.Kind != "api" || get.Source != SourceAPIReconStatic || !get.InScope || get.Change != "possible" || get.Visited ||
		strings.Join(get.QueryKeys, ",") != "id,page" || get.BodyKeys != nil {
		t.Fatalf("inferred GET: %+v", get)
	}
	post := discoveredEndpoint(t, c, "POST", "/read/orders/search")
	if post == nil || strings.Join(post.BodyKeys, ",") != "filter[status],q" || post.ContentType != "application/json" {
		t.Fatalf("inferred POST: %+v", post)
	}
	s, _ := c.Snapshot()
	if s.Counts.Queued != before.Counts.Queued || s.Counts.DiscoveriesDropped != 0 {
		t.Fatalf("inferred discovery queued or dropped: %+v", s.Counts)
	}
	// 저장소에도 같은 내용이 남는다.
	if saved := store.records["prep-owned"].Snapshot; len(saved.Endpoints) != len(s.Endpoints) || saved.Revision != s.Revision {
		t.Fatal("discovery not persisted")
	}
}

// 형식 오류·범위 밖 보고는 저장하지 않고 수만 센다(탐색 실행기 결정안 §2-3, 절대규칙 4).
func TestReportStaticDropsInvalidAndOutOfScope(t *testing.T) {
	long := strings.Repeat("k", maxDiscoveryKeyBytes+1)
	many := make([]string, maxDiscoveryKeys+1)
	for i := range many {
		many[i] = "k" + strings.Repeat("x", i)
	}
	bad := []Discovery{
		{Method: "GET", Path: "/other"},                                                   // 승인 경로 밖
		{Method: "DELETE", Path: "/read/x"},                                               // 승인 메서드 밖
		{Method: "GET", Path: "/read/excluded/x"},                                         // 제외 경로
		{Method: "GET", Path: "/read/payment"},                                            // 위험 경로 단어
		{Method: "BREW", Path: "/read/x"},                                                 // 메서드 형식
		{Method: "GET", Path: "read/x"},                                                   // 상대 경로
		{Method: "GET", Path: "//evil.test/read/x"},                                       // 다른 host
		{Method: "GET", Path: "https://evil.test/read/x"},                                 // 절대 URL
		{Method: "GET", Path: "/read/x?id=1"},                                             // query 값
		{Method: "GET", Path: "/read/x#frag"},                                             // fragment
		{Method: "GET", Path: "/read/a/../b"},                                             // 정규화하면 범위 안이지만 쓴 것과 다른 주소
		{Method: "GET", Path: "/read/../admin"},                                           // 정규화로 바뀌는 경로
		{Method: "GET", Path: "/read/%2e%2e/admin"},                                       // 인코딩
		{Method: "GET", Path: "/read/x", QueryKeys: []string{"id=1"}},                     // 키에 값
		{Method: "GET", Path: "/read/x", QueryKeys: []string{"a b"}},                      // 공백
		{Method: "GET", Path: "/read/x", BodyKeys: []string{`"q"`}},                       // 따옴표
		{Method: "GET", Path: "/read/x", BodyKeys: []string{""}},                          // 빈 이름
		{Method: "GET", Path: "/read/x", BodyKeys: []string{long}},                        // 긴 이름
		{Method: "GET", Path: "/read/x", QueryKeys: many},                                 // 이름 수 상한
		{Method: "POST", Path: "/read/x", ContentType: "application/json; charset=utf-8"}, // 매개변수
		{Method: "POST", Path: "/read/x", ContentType: "json"},                            // type/subtype 아님
	}
	c, _ := flowFixture(t, Auto)
	before, _ := c.Snapshot()
	n, err := c.ReportStatic(context.Background(), bad)
	if err != nil || n != 0 {
		t.Fatalf("accepted=%d err=%v", n, err)
	}
	after, _ := c.Snapshot()
	if len(after.Endpoints) != len(before.Endpoints) || after.Counts.DiscoveriesDropped != uint64(len(bad)) {
		t.Fatalf("dropped=%d endpoints %d→%d", after.Counts.DiscoveriesDropped, len(before.Endpoints), len(after.Endpoints))
	}
	for _, e := range after.Endpoints {
		if e.Source == SourceAPIReconStatic {
			t.Fatalf("invalid discovery stored: %+v", e)
		}
	}
}

// 같은 엔드포인트를 다시 보고하면 이름을 합치고, 문자 그대로 본 발견(링크 등)의 출처를 추정으로 내리지 않는다.
func TestReportStaticMergesAndNeverDowngradesSource(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	ctx := context.Background()
	start := discoveredEndpoint(t, c, "GET", "/read")
	if start == nil || start.Source == SourceAPIReconStatic {
		t.Fatal("fixture start page")
	}
	if n, err := c.ReportStatic(ctx, []Discovery{{Method: "GET", Path: "/read", QueryKeys: []string{"b"}}}); err != nil || n != 1 {
		t.Fatal(err)
	}
	if n, err := c.ReportStatic(ctx, []Discovery{{Method: "GET", Path: "/read", QueryKeys: []string{"a", "b"}}}); err != nil || n != 1 {
		t.Fatal(err)
	}
	e := discoveredEndpoint(t, c, "GET", "/read")
	if e.Source != start.Source || e.Kind != start.Kind || e.Change != start.Change || strings.Join(e.QueryKeys, ",") != "a,b" {
		t.Fatalf("merged endpoint: %+v (start %+v)", e, start)
	}
}

// 보고는 초기 탐색 단계에서만 받는다. 빈 보고와 한 번에 너무 많은 보고는 거부한다.
func TestReportStaticOnlyDuringCrawlAndBounded(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	ctx := context.Background()
	if _, err := c.ReportStatic(ctx, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("empty report accepted")
	}
	tooMany := make([]Discovery, maxDiscoveriesPerReport+1)
	for i := range tooMany {
		tooMany[i] = Discovery{Method: "GET", Path: "/read/x"}
	}
	if _, err := c.ReportStatic(ctx, tooMany); !errors.Is(err, ErrDenied) {
		t.Fatal("oversized report accepted")
	}
	c.mu.Lock()
	c.record.Snapshot.Stage = "diagnosing"
	c.mu.Unlock()
	if _, err := c.ReportStatic(ctx, []Discovery{{Method: "GET", Path: "/read/x"}}); !errors.Is(err, ErrDenied) {
		t.Fatal("report accepted after the crawl stage")
	}
}

// 투영 크기를 넘으면 요청·응답 요약 다음에 보고된 이름 목록을 지운다. 범위·선택은 그대로다.
func TestCompactProjectionDropsDiscoveryShapeLast(t *testing.T) {
	keys := make([]string, maxDiscoveryKeys)
	for i := range keys {
		keys[i] = strings.Repeat(string(rune('a'+i%26)), maxDiscoveryKeyBytes-2) + string(rune('a'+i/26))
	}
	s := Snapshot{Endpoints: make([]Endpoint, 32)}
	for i := range s.Endpoints {
		s.Endpoints[i] = Endpoint{ID: "e", Method: "GET", Path: "/read/x", InScope: true, Selected: i == 0, QueryKeys: keys, BodyKeys: keys, ContentType: "application/json", Request: "GET /read/x HTTP/1.1"}
	}
	out := compactProjection(s)
	if !out.DetailTruncated {
		t.Fatal("oversized projection not compacted")
	}
	for _, e := range out.Endpoints {
		if e.QueryKeys != nil || e.BodyKeys != nil || e.ContentType != "" || e.Request != "" || !e.InScope || e.Path != "/read/x" {
			t.Fatalf("compacted endpoint: %+v", e)
		}
	}
	if !out.Endpoints[0].Selected {
		t.Fatal("selection changed by compaction")
	}
}

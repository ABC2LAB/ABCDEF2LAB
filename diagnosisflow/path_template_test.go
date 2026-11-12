package diagnosisflow

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// crawlPage는 모든 페이지가 같은 본문을 돌려주는 크롤을 끝까지 돌린다.
func crawlPage(t *testing.T, c *Controller, body string) {
	t.Helper()
	calls := 0
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		calls++
		view := e2proxy.RequestView{Method: "GET", URL: target}
		if err := c.Before(ctx, view); err != nil {
			t.Fatalf("crawler request refused %s: %v", target, err)
		}
		receipt := e2proxy.Receipt{AttemptID: statusText(calls), CallRef: "owned-call", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200}
		if err := c.After(ctx, view, receipt); err != nil {
			t.Fatal(err)
		}
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n" + body, Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
	})
	if !handled || err != nil {
		t.Fatal("crawl", handled, err)
	}
}

func selectPaths(t *testing.T, c *Controller, want ...string) {
	t.Helper()
	s, _ := c.Snapshot()
	var ids []string
	for _, e := range s.Endpoints {
		for _, w := range want {
			if e.Method+" "+e.Path == w {
				ids = append(ids, e.ID)
			}
		}
	}
	if len(ids) != len(want) {
		t.Fatalf("endpoints %q not all discovered: %+v", want, s.Endpoints)
	}
	if _, err := c.Command(t.Context(), "ARTEX", "selection", "select-template", s.Revision, ids); err != nil {
		t.Fatal(err)
	}
}

const templatePage = `<a href="/read/users/1">me</a><a href="/read/users/3">other</a><a href="/read/files/0123456789abcdef0123">file</a><form method="post" action="/read/users/1"></form>`

// C18: 선택한 끝점은 값 조각만 다른 같은 메서드 요청도 받는다. 다른 이름 조각·다른 메서드·제외 경로·위험 단어·읽기
// 전용 분류가 다른 경로는 받지 않는다.
func TestSelectedEndpointCoversOtherObjectIDs(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.binding.Excluded = append(c.binding.Excluded, "/read/users/7")
	crawlPage(t, c, templatePage)
	before := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/users/2"}
	if c.Before(t.Context(), before) == nil {
		t.Fatal("variant admitted before selection")
	}
	selectPaths(t, c, "GET /read/users/1", "POST /read/users/1", "GET /read/files/0123456789abcdef0123")
	for _, tc := range []struct {
		method, path string
		body         bool
		allowed      bool
		preapproved  bool
	}{
		{"GET", "/read/users/1", false, true, true},
		{"GET", "/read/users/2", false, true, true},
		{"HEAD", "/read/users/2", false, false, false},
		{"GET", "/read/users/3", false, true, true},
		{"GET", "/read/users/123456789", false, true, true},
		{"GET", "/read/files/fedcba9876543210fedc", false, true, true},
		{"POST", "/read/users/2", true, true, false},
		{"GET", "/read/users/abc", false, false, false},
		{"GET", "/read/users/2/orders", false, false, false},
		{"GET", "/read/accounts/2", false, false, false},
		{"GET", "/read/users/7", false, false, false},
		{"GET", "/read/users/delete-0123456789abcdefgh", false, false, false},
		// 이름·동사 조각은 길거나 특수문자·숫자가 섞여도 객체 ID가 아니다(#501 리뷰).
		{"GET", "/read/users/cancel-all-pending-orders-for-this-user", false, false, false},
		{"GET", "/read/users/123:cancel", false, false, false},
		{"GET", "/read/users/exportAllCustomersToCsvFileNow2026", false, false, false},
		{"GET", "/read/users/x,y", false, false, false},
		{"GET", "/read/users/123e4567-e89b-12d3-a456-426614174000", false, false, false},
	} {
		view := e2proxy.RequestView{Method: tc.method, URL: "https://owned.test" + tc.path, BodyPresent: tc.body}
		if got := c.Before(t.Context(), view) == nil; got != tc.allowed {
			t.Errorf("Before %s %s allowed=%v, want %v", tc.method, tc.path, got, tc.allowed)
		}
		if got := c.PreapprovedRead(view); got != tc.preapproved {
			t.Errorf("PreapprovedRead %s %s = %v, want %v", tc.method, tc.path, got, tc.preapproved)
		}
	}
	if !strings.Contains(c.Instructions(), "differs only in object ID segments of the same kind") {
		t.Fatal("model not told that selected endpoints cover other object IDs")
	}
}

// C18: 값 조각을 바꾼 경로가 읽기 전용 접두 밖이면 선택한 읽기 끝점의 사전 승인을 물려받지 않는다.
func TestTemplateVariantKeepsReadOnlyClassification(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.binding.ReadonlyPrefixes = []string{"/read/users/1", "/read/users/3", "/read/files"}
	crawlPage(t, c, templatePage)
	selectPaths(t, c, "GET /read/users/1")
	for _, path := range []string{"/read/users/2", "/read/users/4"} {
		view := e2proxy.RequestView{Method: "GET", URL: "https://owned.test" + path}
		if c.Before(t.Context(), view) == nil || c.PreapprovedRead(view) {
			t.Fatalf("%s inherited the read exemption of /read/users/1", path)
		}
	}
	// 선택하지 않은 끝점의 값만 바꾼 경로는 사전 승인도 받지 않는다.
	if c.PreapprovedRead(e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/files/fedcba9876543210fedc"}) {
		t.Fatal("unselected endpoint template granted a read exemption")
	}
	if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/users/3"}); err != nil {
		t.Fatal("read-only sibling under its own read-only prefix denied", err)
	}
}

// C18: 크롤 단계는 값 조각을 바꾼 경로를 받지 않는다(발견한 정확한 경로만 자동 방문).
func TestCrawlStageKeepsExactPaths(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	c.discoverBodyLocked([]byte(templatePage), "text/html", "/read")
	stage := c.record.Snapshot.Stage
	c.mu.Unlock()
	if stage != "crawling" {
		t.Fatalf("stage %s", stage)
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/users/2"}) == nil {
		t.Fatal("crawl admitted an unseen object id")
	}
}

// C18: 허용 판정의 객체 ID는 같은 자리·같은 종류(숫자·UUID·긴 16진·이메일)일 때만 같은 템플릿이다. 경로 가림 규칙처럼
// 애매한 조각을 값으로 보지 않는다(#501 리뷰).
func TestPathTemplateMatchesOnlySameIDKind(t *testing.T) {
	same := map[string][]string{
		"/api/orders/123": {"/api/orders/9", "/api/orders/00000000000000000001"},
		"/api/files/123e4567-e89b-12d3-a456-426614174000": {"/api/files/00000000-0000-0000-0000-000000000000"},
		"/api/objects/507f1f77bcf86cd799439011":           {"/api/objects/507f191e810c19729de860ea"},
		"/api/users/alice@example.test/profile":           {"/api/users/bob.b+1@corp.example.test/profile"},
		"/api/orders/123/items/4":                         {"/api/orders/5/items/6"},
	}
	for selected, variants := range same {
		base, n := pathTemplate(selected)
		if n == 0 {
			t.Fatalf("%s has no id segment", selected)
		}
		for _, v := range variants {
			if got, _ := pathTemplate(v); got != base {
				t.Errorf("%s -> %s, want %s", v, got, base)
			}
		}
	}
	base, _ := pathTemplate("/api/orders/123")
	for _, p := range []string{"/api/orders/cancel-all-pending-orders-for-this-user", "/api/orders/123:cancel",
		"/api/orders/exportAllCustomersToCsvFileNow2026", "/api/orders/x,y", "/api/orders/cancel", "/api/orders/507f1f77bcf86cd799439011",
		"/api/orders/123e4567-e89b-12d3-a456-426614174000", "/api/orders/1'or'1'='1", "/api/orders/-1", "/api/orders/123456789012345678901", "/api/orders/123:456"} {
		if tp, _ := pathTemplate(p); tp == base {
			t.Errorf("%s matched selected template %s", p, base)
		}
	}
	hexBase, _ := pathTemplate("/api/objects/507f1f77bcf86cd799439011")
	for _, p := range []string{"/api/objects/exportAllCustomersToCsvFile", "/api/objects/0123456789abcdefXYZ"} {
		if tp, _ := pathTemplate(p); tp == hexBase {
			t.Errorf("%s matched hex template %s", p, hexBase)
		}
	}
}

// C18: 같은 경로가 따로 발견돼 있으면 그 근거가 상한이다. /users/1을 GET 링크로 선택하고 /users/2를 GET 폼으로 봤으면,
// /users/2는 선택한 링크의 사전 승인 읽기를 물려받지 않는다(#501 리뷰 P1). 같은 근거의 형제는 그대로 받는다.
func TestTemplateKeepsStricterExactSibling(t *testing.T) {
	for _, mode := range []string{Manual, Auto} {
		c, _ := flowFixture(t, mode)
		crawlPage(t, c, `<a href="/read/users/1">me</a><a href="/read/users/3">other</a><form method="get" action="/read/users/2"></form>`)
		if mode == Manual {
			selectPaths(t, c, "GET /read/users/1")
		}
		form := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/users/2"}
		if c.PreapprovedRead(form) {
			t.Fatalf("%s: GET form sibling inherited the selected link's read exemption", mode)
		}
		if mode == Auto && c.Before(t.Context(), form) == nil {
			t.Fatalf("%s: automatic mode admitted a possible GET form", mode)
		}
		link := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/users/3"}
		if c.Before(t.Context(), link) != nil || !c.PreapprovedRead(link) {
			t.Fatalf("%s: same-evidence sibling link denied", mode)
		}
		unseen := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/users/4"}
		if c.Before(t.Context(), unseen) != nil || !c.PreapprovedRead(unseen) {
			t.Fatalf("%s: unseen object id denied", mode)
		}
	}
}

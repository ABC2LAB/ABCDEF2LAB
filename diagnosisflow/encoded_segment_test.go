package diagnosisflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

// 진단 단계의 인코딩한 경로 조각(2026-11-16 사용자 결정). 경로 조각은 흔한 주입 지점이라 VAmPI 실제 회차에서 E②가 승인
// 규칙으로 맞춘 GET /users/v1/<인코딩한 값> 요청이 워크플로 경계에서만 거부됐다. 진단 단계만 한 번 푼 경로로 검사해
// 받고, 크롤 단계는 지금처럼 막는다. 벡터는 flowFixture(승인·읽기 접두 /read, 제외 /read/excluded, 자동 모드) 기준이다.
type encodedPathVector struct {
	name, method, path string
	// allow는 진단 단계(자동 모드·쓰기 승인)에서 허용해야 하는지다.
	allow bool
}

var encodedPathVectors = []encodedPathVector{
	// 허용: 한 조각 안의 인코딩한 주입 값. 크롤 단계에서는 막는다(TestEncodedPathSegmentsStayRefusedInCrawlStage).
	{"따옴표·공백·등호 주입 값", "GET", "/read/abc%27%20OR%20%271%27%3D%271", true},
	{"세미콜론 주입 값", "GET", "/read/1%3B%20SELECT%201", true},
	{"인코딩한 공백", "GET", "/read/a%20b", true},
	{"인코딩한 한글", "GET", "/read/%ED%95%9C%EA%B8%80", true},
	{"인코딩한 하이픈", "GET", "/read/new%2Ditem", true},
	{"인코딩된 표기 128바이트", "GET", "/read/" + strings.Repeat("%41", 40) + "ab", true},
	{"인코딩한 쓰기 경로", "POST", "/read/cart%2Fadd", false},
	{"인코딩한 쓰기 값", "POST", "/read/cart/add%20x", true},

	// 거부: 조각 경계·점 조각·두 번 인코딩·제어 문자.
	{"인코딩한 slash %2f", "GET", "/read/a%2fb", false},
	{"인코딩한 slash %2F", "GET", "/read/a%2Fb", false},
	{"인코딩한 역슬래시 %5c", "GET", "/read/a%5cb", false},
	{"인코딩한 점 조각 %2e%2e", "GET", "/read/%2e%2e", false},
	{"인코딩한 점 조각으로 접두 밖", "GET", "/read/%2e%2e/createdb", false},
	{"인코딩한 점 조각 대문자", "GET", "/read/%2E%2E/createdb", false},
	{"인코딩한 점 하나", "GET", "/read/%2e/item", false},
	{"이름 안 인코딩한 점", "GET", "/read/a%2eb", false},
	{"두 번 인코딩 %252f", "GET", "/read/a%252fb", false},
	{"두 번 인코딩 %252e%252e", "GET", "/read/%252e%252e/createdb", false},
	{"인코딩한 % 하나", "GET", "/read/100%25", false},
	{"NUL %00", "GET", "/read/a%00b", false},
	{"CRLF %0d%0a", "GET", "/read/a%0d%0aSet-Cookie:x", false},
	{"DEL %7f", "GET", "/read/a%7fb", false},
	{"C1 제어 %C2%85", "GET", "/read/a%C2%85b", false},
	{"UTF-8 아님", "GET", "/read/%ff", false},
	{"잘못된 인코딩", "GET", "/read/a%zzb", false},
	{"풀린 점 조각", "GET", "/read/../createdb", false},
	{"매개변수 붙은 점 조각", "GET", "/read/..;/createdb", false},
	{"인코딩한 세미콜론 점 조각", "GET", "/read/..%3B/createdb", false},
	{"빈 조각", "GET", "/read//a%20b", false},
	{"인코딩된 표기 129바이트", "GET", "/read/" + strings.Repeat("%41", 40) + "abc", false},
	{"정규 표기 129바이트 초과", "GET", "/read/" + strings.Repeat("'", 41), false},

	// 거부: 풀린 경로가 범위 밖·제외 경로·위험 동작.
	{"인코딩 문자로 접두 밖", "GET", "/read%2Dother/x", false},
	{"인코딩한 파괴 단어", "GET", "/read/%64elete", false},
	{"인코딩 뒤에 붙은 파괴 단어", "GET", "/read/%3Cdelete", false},
	{"인코딩한 거래 단어 쓰기", "POST", "/read/%63heckout", false},
	{"제외 경로를 인코딩", "GET", "/read/%65xcluded/a%20b", false},
	{"제외 경로 아래 인코딩한 값", "GET", "/read/excluded/a%27b", false},
	{"인코딩 값과 위험 query", "GET", "/read/a%20b?action=delete", false},
}

func diagnosingFlow(t *testing.T) *Controller {
	t.Helper()
	c, _ := flowFixture(t, Auto)
	c.mu.Lock()
	c.record.Snapshot.Stage = "diagnosing"
	c.mu.Unlock()
	return c
}

func encodedView(origin string, v encodedPathVector) e2proxy.RequestView {
	view := e2proxy.RequestView{Method: v.method, URL: origin + v.path}
	if v.method == "POST" {
		view.BodyPresent, view.BodyBytes = true, 2
	}
	return view
}

func TestDiagnosisStageEncodedPathSegments(t *testing.T) {
	c := diagnosingFlow(t)
	for _, v := range encodedPathVectors {
		if err := c.Before(t.Context(), encodedView("https://owned.test", v)); (err == nil) != v.allow {
			t.Errorf("%s: %s %s allow=%v err=%v", v.name, v.method, v.path, v.allow, err)
		}
	}
}

// 크롤 단계는 지금처럼 Go 기본 인코딩과 다른 표기를 막는다. 같은 경로를 발견해 확인한 읽기여도 다른 표기로는 보내지 않고,
// 진단 단계로 넘어가면 같은 요청이 그 끝점으로 맞춰진다.
func TestEncodedPathSegmentsStayRefusedInCrawlStage(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	payload := (&url.URL{Path: "/read/abc' OR '1'='1"}).EscapedPath()
	c.mu.Lock()
	for _, p := range []string{"/read/item", payload} {
		if e := c.addLocked("GET", p, "page", "html_link", "/read"); e == nil || e.Change != "attested_read" {
			c.mu.Unlock()
			t.Fatalf("fixture endpoint %s: %+v", p, e)
		}
	}
	c.mu.Unlock()
	for _, tc := range []struct {
		path              string
		crawl, diagnosing bool
	}{
		{"/read/item", true, true},
		{payload, true, true},
		{"/read/%69tem", false, true},
		{"/read/abc%27%20OR%20%271%27%3D%271", false, true},
		{"/read/a%2fb", false, false},
	} {
		view := e2proxy.RequestView{Method: "GET", URL: "https://owned.test" + tc.path}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		err := c.Before(ctx, view)
		cancel()
		if (err == nil) != tc.crawl {
			t.Errorf("crawl %s: allow=%v err=%v", tc.path, tc.crawl, err)
		}
		if c.PreapprovedRead(view) != tc.crawl {
			t.Errorf("crawl preapproved %s: want %v", tc.path, tc.crawl)
		}
	}
	c.mu.Lock()
	c.record.Snapshot.Stage = "diagnosing"
	c.mu.Unlock()
	for _, tc := range []struct {
		path       string
		allow, pre bool
	}{
		{"/read/%69tem", true, true},
		{"/read/abc%27%20OR%20%271%27%3D%271", true, true},
		{"/read/a%2fb", false, false},
	} {
		view := e2proxy.RequestView{Method: "GET", URL: "https://owned.test" + tc.path}
		if err := c.Before(t.Context(), view); (err == nil) != tc.allow {
			t.Errorf("diagnosing %s: allow=%v err=%v", tc.path, tc.allow, err)
		}
		// 인코딩만 다른 같은 경로는 발견한 끝점의 확인한 읽기로 맞춰진다.
		if c.PreapprovedRead(view) != tc.pre {
			t.Errorf("diagnosing preapproved %s: want %v", tc.path, tc.pre)
		}
	}
}

// 결과 불명 쓰기의 기억 키는 풀린 경로의 정규 표기다. 평문으로 보낸 쓰기를 인코딩만 바꿔, 인코딩해 보낸 쓰기를 평문으로
// 다시 보내지 못한다(CLAUDE.md "결과 불명 쓰기는 자동으로 다시 보내지 않는다").
func TestEncodedVariantOfUnknownWriteIsNotResent(t *testing.T) {
	for _, tc := range []struct{ first, again, other string }{
		{"/read/new-write", "/read/new%2Dwrite", "/read/third-write"},
		{"/read/other%2Dwrite", "/read/other-write", "/read/third%2Dwrite"},
		{"/read/x%20y", "/read/x%20%79", "/read/x%20z"},
	} {
		c := diagnosingFlow(t)
		view := func(path string) e2proxy.RequestView {
			return e2proxy.RequestView{Method: "POST", URL: "https://owned.test" + path, BodyPresent: true, BodyBytes: 2}
		}
		if err := c.Before(t.Context(), view(tc.first)); err != nil {
			t.Fatalf("%s: first write denied %v", tc.first, err)
		}
		if err := c.After(t.Context(), view(tc.first), e2proxy.Receipt{AttemptID: "unknown-" + tc.first, CallRef: "call-0", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, Reason: e2proxy.Unknown}); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{tc.first, tc.again} {
			if err := c.Before(t.Context(), view(path)); !errors.Is(err, ErrDenied) {
				t.Errorf("%s after unknown %s: resent (%v)", path, tc.first, err)
			}
		}
		if err := c.Before(t.Context(), view(tc.other)); err != nil {
			t.Errorf("%s: unrelated write denied %v", tc.other, err)
		}
	}
}

// 차등 시험: 워크플로 경계가 허용한 요청은 실제 E②(e2proxy 실행의 경로 정규화·pathscope 규칙 대조)도 허용해야 한다
// (diagnosisflow 허용 ⊆ E② 허용). 인코딩 벡터와 기존 허용·거부 표의 경로를 같은 승인 범위로 둘 다에 보낸다. E②는
// 루프백 시험 대상에만 보낸다.
func TestDiagnosisEncodedPathAllowsAreWithinE2(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 4, MaxRequestsPerApproval: 1000})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: "task", BudgetRef: "approval"}, Binding: budget.Binding{PolicyRef: "policy", ResetRef: "reset"}, MaxRequests: 1000, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	methods := []string{"GET", "HEAD", "POST"}
	execution, err := e2proxy.NewExecution(context.Background(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: transmit.Scope{TaskRef: "task", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
		Rules:  []e2proxy.Rule{{Ref: "rule", Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/read", PathScopeRef: "read-prefix", Methods: methods}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: discardRecorder{},
		Limits: e2proxy.Limits{MaxAttempts: 1000, MaxCalls: 10, MaxConcurrent: 4, MaxBodyBytes: 1 << 16, RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 1000, MaxTransient: 1000, MaxUnknown: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	call, err := execution.BeginCall(context.Background(), "command-ref")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = call.Close(ctx)
	}()
	flow, err := New(t.Context(), Binding{JobID: "prep-owned", TaskID: "1", RunRef: "run-owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: Auto, TargetURL: target.URL + "/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: methods, Excluded: []string{"/read/excluded"}, ReversibleWrites: true, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096, AllowReversibleWrites: true}, Current: func() error { return nil }}, &memoryStore{records: map[string]Record{}})
	if err != nil {
		t.Fatal(err)
	}
	flow.mu.Lock()
	flow.record.Snapshot.Stage = "diagnosing"
	flow.mu.Unlock()

	vectors := append([]encodedPathVector(nil), encodedPathVectors...)
	// 기존 허용·거부 표(TestAutomaticModeSendsUndiscoveredPathsWithinApprovedScope·TestExactPathPlanningAndSendAdmission)의 경로.
	for _, v := range []encodedPathVector{
		{"표: 처음 보는 경로", "GET", "/read/newly-found", true},
		{"표: HEAD", "HEAD", "/read/item", true},
		{"표: 객체 ID와 query", "GET", "/read/api/Users/7?q=1", true},
		{"표: 쓰기", "POST", "/read/newly-found", true},
		{"표: 접두 밖", "GET", "/admin", false},
		{"표: 승인 안 한 메서드", "PUT", "/read/newly-found", false},
		{"표: 제외 경로", "GET", "/read/excluded/deeper", false},
		{"표: 위험 query", "GET", "/read/newly-found?action=delete", false},
		{"표: 위험 경로", "GET", "/read/account/delete", false},
		{"표: 루트 인코딩 slash", "GET", "/%2fcreatedb", false},
		{"표: 빈 조각", "GET", "//", false},
	} {
		vectors = append(vectors, v)
	}
	bothAllowed := 0
	for _, v := range vectors {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		flowErr := flow.Before(ctx, encodedView(target.URL, v))
		var body io.Reader
		if v.method == "POST" {
			body = strings.NewReader("{}")
		}
		req, err := http.NewRequestWithContext(ctx, v.method, target.URL+v.path, body)
		if err != nil {
			// URL로 읽을 수 없는 요청(잘못된 인코딩)은 E②에 닿지도 못한다. 워크플로도 막아야 한다.
			cancel()
			if flowErr == nil || v.allow {
				t.Errorf("%s: unparsable URL allowed by workflow", v.name)
			}
			continue
		}
		resp, receipt, e2Err := call.Do(ctx, req)
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		cancel()
		e2Allowed := e2Err == nil && receipt.Reason == e2proxy.Complete
		if (flowErr == nil) != v.allow {
			t.Errorf("%s: workflow allow=%v err=%v", v.name, v.allow, flowErr)
		}
		if flowErr == nil && !e2Allowed {
			t.Errorf("%s: %s %s workflow allowed but E2 refused (%s %v) — diagnosisflow must stay within E2", v.name, v.method, v.path, receipt.Reason, e2Err)
		}
		if flowErr == nil && e2Allowed {
			bothAllowed++
		}
	}
	// 둘 다 허용한 요청이 없으면 포함 관계가 공허하게 참이 된다. 허용 벡터(인코딩 값 7개·표 4개) 전부가 둘 다 통과해야 한다.
	if want := 11; bothAllowed != want {
		t.Fatalf("allowed by both: %d want %d", bothAllowed, want)
	}
}

type discardRecorder struct{}

func (discardRecorder) Record(context.Context, e2proxy.Event) error { return nil }

// 제외 경로는 대상 입력에서 따옴표·비ASCII 문자를 받는다(diagnosisprep CanonicalInput). 그런 제외 경로는 Go 기본 인코딩
// 표기(%27·%C3%BC)로는 맞지 않으므로 풀린 경로로 본다. 인코딩 표기 하나로 제외 경로를 우회하지 못한다.
func TestDecodedPathHonoursExclusionWithEscapableCharacters(t *testing.T) {
	c := diagnosingFlow(t)
	c.binding.Excluded = append(c.binding.Excluded, "/read/it's", "/read/über")
	for _, tc := range []struct {
		path  string
		allow bool
	}{
		{"/read/it%27s", false},
		{"/read/it%27s/deeper", false},
		{"/read/%C3%BCber", false},
		{"/read/%C3%BCber/x%20y", false},
		{"/read/its", true},
		{"/read/uber", true},
	} {
		if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test" + tc.path}); (err == nil) != tc.allow {
			t.Errorf("%s: allow=%v err=%v", tc.path, tc.allow, err)
		}
	}
}

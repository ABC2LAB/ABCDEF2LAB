package diagnosisflow

import (
	"context"
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

var diffExcluded = []string{"/read/über", "/read/admin", "/read/*/secret", "/read/café/", "/read/tmp"}

type excludedVector struct {
	method, path string
	// excluded는 같은 승인 범위에서 제외 경로 때문에만 막혀야 하는지다(제외가 없으면 둘 다 허용하는 입력).
	excluded bool
}

var excludedVectors = []excludedVector{
	// 제외: 원문·인코딩·소문자 16진·조각 경계·와일드카드·";" 매개변수(원문과 인코딩)·query·쓰기
	{"GET", "/read/über", true}, {"GET", "/read/%C3%BCber", true}, {"GET", "/read/%c3%bcber", true}, {"GET", "/read/%C3%BCber/x", true},
	{"GET", "/read/admin", true}, {"GET", "/read/%61dmin", true}, {"GET", "/read/%61%64%6d%69%6e", true}, {"GET", "/read/admin/", true}, {"GET", "/read/admin/7", true},
	{"GET", "/read/admin;x", true}, {"GET", "/read/admin%3Bx", true}, {"GET", "/read/admin%3bx/7", true},
	{"GET", "/read/a/secret", true}, {"GET", "/read/%2A/secret", true}, {"GET", "/read/b/secret/z", true},
	{"GET", "/read/caf%C3%A9", true}, {"GET", "/read/caf%C3%A9/x", true}, {"GET", "/read/tmp", true}, {"GET", "/read/tmp?x=1", true}, {"HEAD", "/read/tmp", true}, {"POST", "/read/admin", true},
	// 대소문자 변형(제외는 대소문자 무시, 유니코드 단순 fold): ASCII·인코딩한 비ASCII 대문자·와일드카드·";"·쓰기
	{"GET", "/read/ADMIN", true}, {"GET", "/read/Admin/7", true}, {"GET", "/read/%41DMIN", true}, {"GET", "/read/ADMIN;x", true},
	{"GET", "/read/%C3%9Cber", true}, {"GET", "/read/%C3%9CBER/x", true}, {"GET", "/read/CAF%C3%89", true}, {"GET", "/read/A/SECRET", true},
	{"GET", "/read/TMP?x=1", true}, {"POST", "/read/Admin", true},
	// 제외 밖: 이름 앞부분만 같음·더 깊은 와일드카드·";" 앞이 다른 이름
	{"GET", "/read/item", false}, {"GET", "/read/administrator", false}, {"GET", "/read/%C3%BCberx", false}, {"GET", "/read/secret", false},
	{"GET", "/read/a/b/secret", false}, {"GET", "/read/ADMINISTRATOR", false}, {"GET", "/read/tmpx", false}, {"GET", "/read/caf%C3%A9x", false}, {"GET", "/read/x;admin", false},
	{"POST", "/read/item", false},
	// 제외와 상관없이 둘 다 막는 입력(두 번 인코딩·인코딩 slash·점 조각)과, E②는 받지만 진단 흐름이 길이로 막는 입력
	{"GET", "/read/%2561dmin", false}, {"GET", "/read/admin%2Fx", false}, {"GET", "/read/%2e%2e/admin", false},
	{"GET", "/read/admin/" + strings.Repeat("a", 130), false},
}

func diffExecution(t *testing.T, origin string, excluded []string) *e2proxy.Call {
	t.Helper()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 4, MaxRequestsPerApproval: 1000})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: "task", BudgetRef: "approval"}, Binding: budget.Binding{PolicyRef: "policy", ResetRef: "reset"}, MaxRequests: 1000, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	execution, err := e2proxy.NewExecution(context.Background(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: transmit.Scope{TaskRef: "task", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
		Rules:         []e2proxy.Rule{{Ref: "rule", Origin: origin, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/read", PathScopeRef: "read-prefix", Methods: []string{"GET", "HEAD", "POST"}}},
		ExcludedPaths: excluded,
		Budget:        transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: discardRecorder{},
		Limits: e2proxy.Limits{MaxAttempts: 1000, MaxCalls: 10, MaxConcurrent: 4, MaxBodyBytes: 1 << 16, RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 1000, MaxTransient: 1000, MaxUnknown: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	call, err := execution.BeginCall(context.Background(), "command-ref")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = call.Close(ctx)
	})
	return call
}

func diffFlow(t *testing.T, origin string, excluded []string) *Controller {
	t.Helper()
	flow, err := New(t.Context(), Binding{JobID: "prep-owned", TaskID: "1", RunRef: "run-owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: Auto, TargetURL: origin + "/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD", "POST"}, Excluded: excluded, ReversibleWrites: true, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096, AllowReversibleWrites: true}, Current: func() error { return nil }}, &memoryStore{records: map[string]Record{}})
	if err != nil {
		t.Fatal(err)
	}
	flow.mu.Lock()
	flow.record.Snapshot.Stage = "diagnosing"
	flow.mu.Unlock()
	return flow
}

func e2Sends(t *testing.T, call *e2proxy.Call, origin string, v excludedVector) bool {
	t.Helper()
	var body io.Reader
	if v.method == "POST" {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(t.Context(), v.method, origin+v.path, body)
	if err != nil {
		return false
	}
	resp, r, err := call.Do(t.Context(), req)
	if resp != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return err == nil && r.Reason == e2proxy.Complete
}

func flowAllows(t *testing.T, flow *Controller, origin string, v excludedVector) bool {
	t.Helper()
	view := e2proxy.RequestView{Method: v.method, URL: origin + v.path}
	if v.method == "POST" {
		view.BodyPresent, view.BodyBytes = true, 2
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return flow.Before(ctx, view) == nil
}

// E② 제외 경로 집행과 진단 흐름 제외 판정의 차등 시험(#627 동찬 조건 2). 같은 승인 범위·입력에서 제외 경로만 넣고
// 뺀 두 설정을 비교해 "제외 때문에 막힌 것"만 뽑는다.
//   - E² 제외 거부 ⊆ 진단 흐름 제외 거부: E²가 제외로 막은 입력은 진단 흐름도 제외로 막는다. 진단 흐름이 제외와
//     상관없이 이미 막는 입력(길이 상한 등)은 E² 제외 거부가 새로 막는 송신이 아니다.
//   - 둘 다 받는 입력에서는 두 제외 판정이 같다(진단 흐름이 막는 제외는 E²도 막음).
//   - 제외 경로는 E²를 거쳐 대상에 실제로 닿지 않고, 제외 밖 경로는 지금처럼 닿는다.
func TestExcludedPathsE2DenialsWithinWorkflowDenials(t *testing.T) {
	var received []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.URL.EscapedPath())
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	e2With, e2Without := diffExecution(t, target.URL, diffExcluded), diffExecution(t, target.URL, nil)
	flowWith, flowWithout := diffFlow(t, target.URL, diffExcluded), diffFlow(t, target.URL, nil)
	e2Excl, flowExcl, common := 0, 0, 0
	for _, v := range excludedVectors {
		name := v.method + " " + v.path
		if len(name) > 60 {
			name = name[:60]
		}
		received = nil
		withSent := e2Sends(t, e2With, target.URL, v)
		if withSent != (len(received) == 1) {
			t.Fatalf("%s: E② 결과와 대상 수신이 다름", name)
		}
		withoutSent := e2Sends(t, e2Without, target.URL, v)
		dfWith, dfWithout := flowAllows(t, flowWith, target.URL, v), flowAllows(t, flowWithout, target.URL, v)
		isE2Excl := withoutSent && !withSent
		isFlowExcl := dfWithout && !dfWith
		if isE2Excl {
			e2Excl++
			if dfWith {
				t.Errorf("%s: E②가 제외로 막은 요청을 진단 흐름은 허용", name)
			}
		}
		if isFlowExcl {
			flowExcl++
		}
		if withoutSent && dfWithout {
			common++
			if isE2Excl != isFlowExcl || isE2Excl != v.excluded {
				t.Errorf("%s: 둘 다 받는 입력에서 제외 판정이 다름: E²=%v 진단흐름=%v 기대=%v", name, isE2Excl, isFlowExcl, v.excluded)
			}
		}
		if v.excluded && !isE2Excl {
			t.Errorf("%s: 제외 경로를 E②가 막지 않음(제외 없이 송신=%v, 제외 넣고 송신=%v)", name, withoutSent, withSent)
		}
		if v.excluded && withSent {
			t.Errorf("%s: 제외 경로가 대상에 닿음", name)
		}
	}
	if e2Excl == 0 || flowExcl == 0 || common == 0 {
		t.Fatal("비교가 비었음", e2Excl, flowExcl, common)
	}
	t.Logf("E2 exclusion denials=%d workflow exclusion denials=%d both-accepted inputs=%d vectors=%d", e2Excl, flowExcl, common, len(excludedVectors))
}

// 크롤 단계의 링크 판정(#632)도 같은 제외 판정을 쓴다. 크롤이 받는 표기(인코딩 없는 경로)에서 E² 제외 판정과 같다.
func TestCrawlLinkExclusionMatchesE2(t *testing.T) {
	flow := diffFlow(t, "https://owned.test", diffExcluded)
	for _, raw := range []string{"/read/über", "/read/über/x", "/read/ÜBER", "/read/admin", "/read/admin;x", "/read/ADMIN", "/read/a/secret", "/read/café", "/read/CAFÉ", "/read/tmp", "/read/item", "/read/administrator", "/read/a/b/secret"} {
		u := &url.URL{Path: raw}
		canonical, ok := canonicalPath(u)
		if !ok {
			t.Fatalf("크롤이 받는 표기가 아님: %s", raw)
		}
		decoded, _ := url.PathUnescape(canonical)
		e2Excluded := false
		for _, p := range diffExcluded {
			e2Excluded = e2Excluded || excludedWithin(decoded, p)
		}
		flow.mu.Lock()
		allowed := flow.allowedPath("GET", canonical)
		flow.mu.Unlock()
		if allowed == e2Excluded {
			t.Errorf("%s: 크롤 판정 allowed=%v, E² 제외=%v", raw, allowed, e2Excluded)
		}
	}
}

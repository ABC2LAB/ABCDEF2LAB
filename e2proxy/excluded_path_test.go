package e2proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// countingBoundary는 Before에 닿은 경로를 센다. 모든 요청을 허용한다.
type countingBoundary struct {
	mu   sync.Mutex
	seen []string
}

func (b *countingBoundary) Before(_ context.Context, v RequestView) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, v.URL)
	return nil
}
func (b *countingBoundary) PreapprovedRead(RequestView) bool { return false }
func (b *countingBoundary) After(context.Context, RequestView, Receipt) error {
	return nil
}

// 운영자 제외 경로는 E②가 승인 규칙보다 먼저 거부한다(#627). 풀린 경로로 맞춰 인코딩·비ASCII·와일드카드·";"
// 매개변수·대소문자 변형 우회가 막히고, 거부는 진단 흐름 경계 거부와 같은 모양(policy_denied, 맞은 규칙 참조)의 결과 기록 하나·거부
// 계수 하나로 남으며 진단 흐름 경계(Before)는 불리지 않는다(요청당 한 번, 사유 하나). 대상은 제외 경로를 한 번도 받지
// 않고, 제외 밖 경로는 지금처럼 보낸다.
func TestExcludedPathsDeniedOnceBeforeBoundary(t *testing.T) {
	var mu sync.Mutex
	received := map[string]int{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.URL.Path]++
		mu.Unlock()
		io.WriteString(w, "owned ordinary body")
	}))
	defer target.Close()
	recorder := &coreRecorder{}
	cfg := coreConfig(t, target.URL, recorder, 1000)
	cfg.Rules[0].PathPrefix = "/read"
	cfg.ExcludedPaths = []string{"/read/über", "/read/admin", "/read/*/secret"}
	cfg.Limits.MaxDenials = 1000
	boundary := &countingBoundary{}
	cfg.RequestBoundary = boundary
	e, c := coreOpen(t, cfg)
	excluded := []string{"/read/über", "/read/%C3%BCber", "/read/%C3%BCber/x", "/read/admin", "/read/%61dmin", "/read/admin/", "/read/admin/x", "/read/admin;x", "/read/admin%3Bx/y", "/read/a/secret", "/read/b/secret/z", "/read/admin?q=1", "/read/ADMIN", "/read/Admin/7", "/read/%C3%9CBER", "/read/A/SECRET"}
	allowed := []string{"/read/item", "/read/administrator", "/read/ADMINISTRATOR", "/read/%C3%BCberx", "/read/secret", "/read/a/b/secret"}
	for _, p := range excluded {
		resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+p))
		if err == nil || resp != nil || !r.NotSent || r.ForwardAttempted || r.Decision != "denied" || r.Reason != PolicyDenied || r.RuleRef != "rule" {
			t.Fatalf("%s: 제외 경로 송신 또는 사유 불일치: err=%v decision=%s reason=%s rule=%q", p, err, r.Decision, r.Reason, r.RuleRef)
		}
	}
	for _, p := range allowed {
		resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+p))
		if err != nil || resp == nil || r.Reason != Complete {
			t.Fatalf("%s: 제외 밖 경로가 막힘: %v %s", p, err, r.Reason)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	mu.Lock()
	for path, n := range received {
		if path == "/read/über" || strings.HasPrefix(path, "/read/über/") || strings.HasPrefix(path, "/read/admin") && path != "/read/administrator" || strings.HasSuffix(path, "/secret") && path != "/read/secret" && path != "/read/a/b/secret" || strings.Contains(path, "/secret/") {
			t.Errorf("대상이 제외 경로를 받음: %q x%d", path, n)
		}
	}
	if len(received) != len(allowed) {
		t.Errorf("대상 수신 경로 수 %d, 기대 %d (%v)", len(received), len(allowed), received)
	}
	mu.Unlock()
	boundary.mu.Lock()
	for _, seen := range boundary.seen {
		for _, p := range excluded {
			if seen == target.URL+p {
				t.Errorf("제외 경로가 진단 흐름 경계까지 감: %s", p)
			}
		}
	}
	boundary.mu.Unlock()
	recorder.mu.Lock()
	denied, intents := 0, 0
	for _, ev := range recorder.events {
		if ev.Stage == "result" && ev.Reason == PolicyDenied {
			denied++
		}
		if ev.Stage == "intent" {
			intents++
		}
	}
	recorder.mu.Unlock()
	if denied != len(excluded) || intents != len(allowed) {
		t.Fatalf("감사 기록: 거부 결과 %d(기대 %d), 송신 의도 %d(기대 %d)", denied, len(excluded), intents, len(allowed))
	}
	result, err := c.Close(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Denied != uint64(len(excluded)) || result.Counts.Allowed != uint64(len(allowed)) {
		t.Fatalf("호출 계수 거부 %d·허용 %d, 기대 %d·%d", result.Counts.Denied, result.Counts.Allowed, len(excluded), len(allowed))
	}
	_ = e
}

// 제외 경로 거부는 다른 거부와 같은 호출당 거부 상한을 쓴다. 상한에 닿으면 그 호출만 막히고, 새 호출과 실행은 계속된다
// (실행 전체 중단이 새로 생기지 않음, C01).
func TestExcludedPathDenialsKeepPerCallLimit(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 1000)
	cfg.Rules[0].PathPrefix = "/read"
	cfg.ExcludedPaths = []string{"/read/admin"}
	cfg.Limits.MaxDenials = 2
	e, c := coreOpen(t, cfg)
	for i := 0; i < 2; i++ {
		if _, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/read/admin")); err == nil || r.Reason != PolicyDenied {
			t.Fatalf("제외 거부 %d: %v %s", i, err, r.Reason)
		}
	}
	if _, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/read/item")); err == nil || r.Reason != DenialLimit {
		t.Fatalf("거부 상한 뒤 같은 호출: %v %s", err, r.Reason)
	}
	next, err := e.BeginCall(context.Background(), "command-ref-2")
	if err != nil {
		t.Fatal("거부 상한이 실행 전체를 멈춤:", err)
	}
	defer func() { _, _ = next.Close(context.Background()) }()
	resp, r, err := next.Do(t.Context(), coreRequest(t, "GET", target.URL+"/read/item"))
	if err != nil || r.Reason != Complete {
		t.Fatalf("새 호출의 정상 요청이 막힘: %v %s", err, r.Reason)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// 제외 경로 문법·개수는 진단 입력 검사와 같은 규칙으로 정책 검사에서 거부한다(잘못된 패턴이 조용히 무시되지 않음).
func TestExcludedPathsPolicyValidation(t *testing.T) {
	base := coreConfig(t, "https://owned.test", &coreRecorder{}, 10)
	if err := ValidatePolicyTemplate(base); err != nil {
		t.Fatal(err)
	}
	ok := base
	ok.ExcludedPaths = []string{"/", "/read/über", "/read/*/x"}
	if err := ValidatePolicyTemplate(ok); err != nil {
		t.Fatal("valid exclusions rejected:", err)
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = "/read/x"
	}
	for _, bad := range [][]string{{"read"}, {"/read/a*"}, {"/read/%2f"}, {"/read/../x"}, {"//read"}, {""}, many} {
		cfg := base
		cfg.ExcludedPaths = bad
		if ValidatePolicyTemplate(cfg) == nil {
			t.Errorf("invalid exclusions accepted: %d %q", len(bad), bad[0])
		}
		if _, err := NewExecution(context.Background(), cfg); err == nil {
			t.Errorf("execution accepted invalid exclusions: %q", bad[0])
		}
	}
}

// denyingBoundary는 이 변경 전처럼 진단 흐름 경계가 제외 경로를 막는 상황이다.
type denyingBoundary struct{ countingBoundary }

func (b *denyingBoundary) Before(ctx context.Context, v RequestView) error {
	_ = b.countingBoundary.Before(ctx, v)
	return ErrDenied
}

// E²의 제외 거부 기록은 이 변경 전 진단 흐름 경계가 막을 때와 같은 모양이다(결정·사유·규칙 참조·송신 표시·감사
// 단계 수). 그래서 평가 내보내기(e2_export)·시도 대조·반복 차단 집계·작업대 e2-flow 어휘가 새 사유 없이 그대로 읽는다.
func TestExcludedPathDenialMatchesPreviousBoundaryDenial(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	shape := func(excluded []string, boundary RequestBoundary) (Receipt, []string) {
		recorder := &coreRecorder{}
		cfg := coreConfig(t, target.URL, recorder, 100)
		cfg.Rules[0].PathPrefix = "/read"
		cfg.ExcludedPaths = excluded
		cfg.RequestBoundary = boundary
		_, c := coreOpen(t, cfg)
		_, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/read/admin"))
		if err == nil {
			t.Fatal("excluded request sent")
		}
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		var stages []string
		for _, ev := range recorder.events {
			stages = append(stages, ev.Stage+":"+string(ev.Reason)+":"+ev.RuleRef+":"+ev.Decision)
		}
		return r, stages
	}
	now, nowStages := shape([]string{"/read/admin"}, &countingBoundary{})
	before, beforeStages := shape(nil, &denyingBoundary{})
	if now.Decision != before.Decision || now.Reason != before.Reason || now.RuleRef != before.RuleRef || now.NotSent != before.NotSent || now.ForwardAttempted != before.ForwardAttempted || now.AuditComplete != before.AuditComplete {
		t.Fatalf("거부 영수증 모양이 다름: now=%+v before=%+v", now, before)
	}
	if strings.Join(nowStages, ",") != strings.Join(beforeStages, ",") {
		t.Fatalf("감사 기록 모양이 다름: now=%v before=%v", nowStages, beforeStages)
	}
}

package e2proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

func e3CreateFixture(t *testing.T, target string, spec e3contract.CreateSpec, objectID string) (*Execution, E3CreateInput, *coreRecorder) {
	t.Helper()
	recorder := &coreRecorder{}
	cfg := coreConfig(t, target, recorder, 3)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = 3
	cfg.E3WriteConsent = true
	cfg.E3Resettable = true
	cfg.Rules = append(cfg.Rules,
		Rule{Ref: "create-worker", Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed", PathScopeRef: "create-worker-path", Methods: []string{"POST"}},
		Rule{Ref: "create-program", Purpose: PurposeE3Create, Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: spec.Path, PathScopeRef: "create-program-path", Methods: []string{spec.Method}},
		Rule{Ref: "read-worker", Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed", PathScopeRef: "read-worker-path", Methods: []string{"GET"}},
		Rule{Ref: "read-program", Purpose: PurposeE3Read, Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed/books/{id}", PathScopeRef: "read-program-path", Methods: []string{"GET"}})
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := e3contract.NewMarker(bytes.NewReader(bytes.Repeat([]byte{2}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	value := marker.Reveal()
	if err := e.RegisterProgramMarker(value); err != nil {
		t.Fatal(err)
	}
	clear(value)
	body, err := e3contract.AssembleBody(spec, marker, objectID)
	if err != nil {
		t.Fatal(err)
	}
	in := E3CreateInput{Binding: e3contract.RunBinding{JobRef: "job-1", RunRef: "run", TaskRef: "task",
		E2PolicyRef: "policy", GenerationRef: "generation-1", InstanceRef: "instance-1",
		Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-1",
			DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "approval-1"}},
		ResourceType: "book", AccountLabel: "owner-a", Spec: spec, Marker: marker, Body: body, ObjectID: objectID,
		Accounts:  &e3IdentityAccounts{ref: "acct-1", header: http.Header{"Authorization": {"Bearer fixture-session"}}, current: true},
		DigestKey: bytes.Repeat([]byte{7}, 32), Timeout: time.Second}
	return e, in, recorder
}

func TestE3CreateSendsOneMarkedRequestAndKeepsIDPrivate(t *testing.T) {
	for _, tc := range []struct {
		name, idPath, idField, objectID, response string
	}{
		{name: "response-id", idPath: "id", response: `{"id":"book-a"}`},
		{name: "request-id", idField: "title", objectID: "e3id-0123456789abcdef0123456789abcdef", response: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || r.Method != "POST" || r.URL.Path != "/allowed/books" ||
					r.Header.Get("Authorization") != "Bearer fixture-session" ||
					!bytes.Contains(body, []byte("marker")) || r.Header.Get(AttemptHeader) == "" {
					t.Error("생성 요청의 방법·경로·계정·본문이 다름")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
				BodyTemplate: `{}`, MarkerField: "marker", IDPath: tc.idPath, IDField: tc.idField}
			e, in, recorder := e3CreateFixture(t, server.URL, spec, tc.objectID)
			event, err := e.SendE3Create(t.Context(), in)
			if err != nil || requests.Load() != 1 || !event.AuditComplete || !event.ResponseComplete ||
				event.E3Audit == nil || event.E3Audit.Normal != oracle.StateYes || event.E3Audit.ObjectIDDigest == "" {
				t.Fatalf("생성 결과 누락: err=%v requests=%d event=%+v", err, requests.Load(), event.Receipt)
			}
			ref := e3contract.CallRef{CallRef: event.CallRef, AttemptID: event.AttemptID, Sequence: event.Sequence}
			if e.e3Created[ref] == "" {
				t.Fatal("조회용 객체 ID가 실행 메모리에 남지 않음")
			}
			for _, row := range recorder.events {
				if !validJournalRecordEvent(row, JournalVersionV3) {
					t.Fatalf("잘못된 v3 생성 사건: %s", row.Stage)
				}
				raw, err := json.Marshal(row)
				if err != nil || bytes.Contains(raw, []byte("fixture-session")) ||
					bytes.Contains(raw, in.Marker.Reveal()) || bytes.Contains(raw, []byte(e.e3Created[ref])) {
					t.Fatal("생성 사건에 세션·표식·객체 ID 원문이 남음")
				}
			}
		})
	}
}

func TestE3CreateMissingOrInvalidResponseIDIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, response string
	}{
		{name: "missing", contentType: "application/json", response: `{}`},
		{name: "invalid_path", contentType: "application/json", response: `{"id":"../outside"}`},
		{name: "non_json", contentType: "text/plain", response: `book-a`},
		{name: "malformed", contentType: "application/json", response: `{"id":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
				BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
			e, in, _ := e3CreateFixture(t, server.URL, spec, "")
			event, err := e.SendE3Create(t.Context(), in)
			if err != nil || requests.Load() != 1 || !event.ResponseComplete || event.E3Audit == nil ||
				event.E3Audit.Normal != oracle.StateUnknown || event.E3Audit.ObjectIDDigest != "" || len(e.e3Created) != 0 {
				t.Fatalf("객체 ID 추출 실패를 정상 생성으로 기록: err=%v requests=%d receipt=%+v", err, requests.Load(), event.Receipt)
			}
		})
	}
}

func TestE3CreateRequiresWriteConsentAndReset(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	for _, change := range []func(*Execution){
		func(e *Execution) { e.cfg.E3WriteConsent = false },
		func(e *Execution) { e.cfg.E3Resettable = false },
		func(e *Execution) { e.cfg.E3ConfirmationRequests = 0 },
	} {
		e, in, rec := e3CreateFixture(t, server.URL, spec, "")
		change(e)
		if _, err := e.SendE3Create(t.Context(), in); err == nil || len(rec.events) != 0 {
			t.Fatal("동의·리셋·확정 몫 없이 생성 허용")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("거부한 생성이 대상에 도착")
	}
}

func TestE3CreateUnknownResultDoesNotRetryOrIssueID(t *testing.T) {
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: http.MethodPost, Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	e, in, _ := e3CreateFixture(t, server.URL, spec, "")
	event, _ := e.SendE3Create(t.Context(), in)
	if writes.Load() != 1 || event.Stage != "result" || event.ResponseComplete || event.E3Audit != nil && event.E3Audit.ObjectIDDigest != "" ||
		len(e.e3Created) != 0 {
		t.Fatalf("결과 불명 생성을 재시도하거나 ID를 발급: writes=%d", writes.Load())
	}
}

// e3OutcomeGate는 거절·만료를 나눠 돌려주는 승인 경계다(RequestApprovalOutcomeGate).
type e3OutcomeGate struct {
	outcome RequestApprovalOutcome
	calls   atomic.Int64
}

func (g *e3OutcomeGate) AwaitRequestApproval(context.Context, RequestApprovalProposal) (bool, error) {
	return g.outcome == RequestApprovalAllowed, nil
}

func (g *e3OutcomeGate) AwaitRequestApprovalOutcome(context.Context, RequestApprovalProposal) (RequestApprovalOutcome, error) {
	g.calls.Add(1)
	return g.outcome, nil
}

// 사용자 승인 모드에서 확정 단계 생성 요청이 거절·만료되면 보내지 않고 계약대로 approval_denied·approval_timeout을 남긴다
// (e3contract.Transport, S10). 감사 사유는 policy_denied 그대로다. 결과를 나누지 못하는 승인 경계는 지금처럼 policy_denied다.
func TestE3CreateApprovalOutcomeSetsNotSentReason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gate     RequestApprovalGate
		want     e3contract.NotSentReason
		requests int64
	}{
		{name: "denied", gate: &e3OutcomeGate{outcome: RequestApprovalDenied}, want: e3contract.NotSentApprovalDenied},
		{name: "expired", gate: &e3OutcomeGate{outcome: RequestApprovalExpired}, want: e3contract.NotSentApprovalTimeout},
		{name: "undecided", gate: &e3OutcomeGate{outcome: RequestApprovalUndecided}, want: e3contract.NotSentPolicyDenied},
		{name: "bool-gate", gate: &e3ApprovalGate{}, want: e3contract.NotSentPolicyDenied},
		{name: "allowed", gate: &e3OutcomeGate{outcome: RequestApprovalAllowed}, requests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"book-a"}`)
			}))
			defer server.Close()
			spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
				BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
			e, in, _ := e3CreateFixture(t, server.URL, spec, "")
			e.cfg.ApprovalMode = ApprovalConfirmEveryRequest
			e.cfg.RequestApproval = tc.gate
			event, err := e.SendE3Create(t.Context(), in)
			if requests.Load() != tc.requests {
				t.Fatalf("대상 도착 %d건, 기대 %d건", requests.Load(), tc.requests)
			}
			if gate, ok := tc.gate.(*e3OutcomeGate); ok && gate.calls.Load() != 1 {
				t.Fatal("결과를 나누는 승인 경계를 부르지 않음")
			}
			if tc.requests == 1 {
				if err != nil || event.E3Audit == nil || event.E3Audit.NotSentReason != "" {
					t.Fatalf("허용된 생성이 미송신으로 남음: err=%v", err)
				}
				return
			}
			if err != nil || event.E3Audit == nil || event.E3Audit.NotSentReason != tc.want ||
				event.Reason != PolicyDenied || event.Decision != "denied" || event.ForwardAttempted {
				t.Fatalf("미송신 사유가 다름: err=%v receipt=%+v", err, event.Receipt)
			}
		})
	}
}

// e3WaitingGate는 제품 승인 대기처럼 결정 없이 기다린다. 기한(ExpiresAt)이 먼저 오면 expired, 문맥이 먼저 끝나면
// undecided를 돌려준다(server.RequestApprovalService와 같은 끝 모양). honorExpiry가 false면 기한을 보지 않는다.
type e3WaitingGate struct {
	honorExpiry bool
	denyOnDone  bool // 문맥이 끝나는 순간 거절이 온 경우(거절과 요청 상한이 경합)
	onWait      func()
	expires     time.Time
}

func (g *e3WaitingGate) AwaitRequestApproval(ctx context.Context, p RequestApprovalProposal) (bool, error) {
	outcome, err := g.AwaitRequestApprovalOutcome(ctx, p)
	return outcome == RequestApprovalAllowed, err
}

func (g *e3WaitingGate) AwaitRequestApprovalOutcome(ctx context.Context, p RequestApprovalProposal) (RequestApprovalOutcome, error) {
	g.expires = p.ExpiresAt
	if g.onWait != nil {
		g.onWait()
	}
	var timer <-chan time.Time
	if g.honorExpiry {
		t := time.NewTimer(time.Until(p.ExpiresAt))
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
	case <-timer:
	}
	if g.denyOnDone {
		return RequestApprovalDenied, nil
	}
	if g.honorExpiry && !time.Now().Before(p.ExpiresAt) {
		return RequestApprovalExpired, nil
	}
	return RequestApprovalUndecided, nil
}

// 사용자 승인 모드에서 확정 생성 요청의 승인 대기는 요청 시간 상한(제품 10초) 밖이고, 승인 기한과 호출자 문맥(확정 단계
// 시간 몫) 안에서만 기다린다(사용자 결정 2026-11-21). 결정 없이 시간 몫 기한에 닿으면 대상 송신 0건, 감사 결정
// denied·사유 policy_denied, 미송신 사유 approval_timeout이다(#836 이동찬 리뷰). 승인 기한은 시간 몫 기한에 맞춘다. 실행
// 중지처럼 기한이 아닌 취소는 approval_timeout이 아니다.
func TestE3CreateApprovalWaitBoundedByStageShare(t *testing.T) {
	for _, tc := range []struct {
		name        string
		honorExpiry bool
		denyOnDone  bool
		stop        bool
		want        e3contract.NotSentReason
		reason      Reason
	}{
		{name: "expiry-aligned", honorExpiry: true, want: e3contract.NotSentApprovalTimeout, reason: PolicyDenied},
		{name: "context-deadline", honorExpiry: false, want: e3contract.NotSentApprovalTimeout, reason: PolicyDenied},
		// 거절이 시간 몫 기한과 같이 오면 거절이다(만료로 덮지 않는다). 문맥이 끝났으므로 감사 사유는 closed다.
		{name: "denied-at-deadline", denyOnDone: true, want: e3contract.NotSentApprovalDenied, reason: Closed},
		{name: "run-stopped", honorExpiry: false, stop: true, want: e3contract.NotSentPolicyDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
			defer server.Close()
			spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
				BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
			e, in, _ := e3CreateFixture(t, server.URL, spec, "")
			// 요청 상한(50ms)보다 시간 몫 기한(250ms)이 길다. 승인 대기는 요청 상한에 끊기지 않고 시간 몫까지 간다.
			in.Timeout = 50 * time.Millisecond
			share := 250 * time.Millisecond
			ctx, cancel := context.WithTimeout(t.Context(), share)
			defer cancel()
			gate := &e3WaitingGate{honorExpiry: tc.honorExpiry, denyOnDone: tc.denyOnDone}
			if tc.stop {
				gate.onWait = func() { time.AfterFunc(30*time.Millisecond, cancel) }
			}
			e.cfg.ApprovalMode = ApprovalConfirmEveryRequest
			e.cfg.RequestApproval = gate
			started := time.Now()
			event, err := e.SendE3Create(ctx, in)
			if requests.Load() != 0 {
				t.Fatalf("승인 없이 대상에 %d건 도착", requests.Load())
			}
			if gate.expires.After(started.Add(share + 50*time.Millisecond)) {
				t.Fatal("승인 기한이 확정 단계 시간 몫보다 늦음")
			}
			if tc.stop {
				// 실행 중지는 그 요청의 결과를 돌려주지 않을 수도 있다. 결과가 있으면 승인 만료로 남지 않아야 한다.
				if err == nil && (event.E3Audit == nil || event.E3Audit.NotSentReason != tc.want) {
					t.Fatalf("중지가 승인 만료로 남음: %+v", event.Receipt)
				}
				return
			}
			if waited := time.Since(started); waited < share-30*time.Millisecond {
				t.Fatalf("승인 대기가 요청 상한(%s)에 끊김: %s", in.Timeout, waited)
			}
			if err != nil || event.E3Audit == nil || event.E3Audit.NotSentReason != tc.want ||
				event.Decision != "denied" || event.Reason != tc.reason || event.ForwardAttempted {
				t.Fatalf("시간 몫 승인 만료 기록이 다름: err=%v receipt=%+v", err, event.Receipt)
			}
		})
	}
}

// 미송신 사유 매핑만 본다: 운영자 거절과 동시에 문맥이 끝나 감사 사유가 closed여도 거절(approval_denied)이다. 결정 없는
// 취소·중지는 policy_denied, 예산 소진은 confirmation_budget_exhausted다.
func TestE3ProgramCallFinishMapsApprovalOutcome(t *testing.T) {
	for _, tc := range []struct {
		approval RequestApprovalOutcome
		reason   Reason
		want     e3contract.NotSentReason
	}{
		{RequestApprovalDenied, PolicyDenied, e3contract.NotSentApprovalDenied},
		{RequestApprovalDenied, Closed, e3contract.NotSentApprovalDenied},
		{RequestApprovalExpired, PolicyDenied, e3contract.NotSentApprovalTimeout},
		{RequestApprovalUndecided, Closed, e3contract.NotSentPolicyDenied},
		{"", PolicyDenied, e3contract.NotSentPolicyDenied},
		{RequestApprovalDenied, BudgetExhausted, e3contract.NotSentBudgetExhausted},
	} {
		c := &e3ProgramCall{approval: tc.approval}
		r := Receipt{Decision: "denied", Reason: tc.reason, E3Audit: &E3Audit{}}
		c.finish(&r, t.Context())
		if r.E3Audit.NotSentReason != tc.want {
			t.Errorf("%s/%s → %s, 기대 %s", tc.approval, tc.reason, r.E3Audit.NotSentReason, tc.want)
		}
	}
}

// e3ApprovingGate는 정해진 시간 뒤 허용한다(문맥이 먼저 끝나면 결정 없음).
type e3ApprovingGate struct{ after time.Duration }

func (g *e3ApprovingGate) AwaitRequestApproval(ctx context.Context, p RequestApprovalProposal) (bool, error) {
	outcome, err := g.AwaitRequestApprovalOutcome(ctx, p)
	return outcome == RequestApprovalAllowed, err
}

func (g *e3ApprovingGate) AwaitRequestApprovalOutcome(ctx context.Context, _ RequestApprovalProposal) (RequestApprovalOutcome, error) {
	select {
	case <-time.After(g.after):
		return RequestApprovalAllowed, nil
	case <-ctx.Done():
		return RequestApprovalUndecided, nil
	}
}

// 승인 기한(E② 호출 기한)에 닿아 대기가 끝나도 승인 만료다. 결과를 나누지 않고 결정 없음만 돌려주는 경계여도 같다.
func TestE3CreateApprovalWaitBoundedByCallDeadline(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	e, in, _ := e3CreateFixture(t, server.URL, spec, "")
	in.Timeout = 50 * time.Millisecond
	e.cfg.Limits.CallTimeout = 200 * time.Millisecond
	e.cfg.ApprovalMode = ApprovalConfirmEveryRequest
	e.cfg.RequestApproval = &e3WaitingGate{honorExpiry: false}
	started := time.Now()
	event, err := e.SendE3Create(t.Context(), in)
	if requests.Load() != 0 || time.Since(started) < 150*time.Millisecond {
		t.Fatalf("호출 기한 전에 끝나거나 보냄: requests=%d waited=%s", requests.Load(), time.Since(started))
	}
	if err != nil || event.E3Audit == nil || event.E3Audit.NotSentReason != e3contract.NotSentApprovalTimeout || event.Reason != PolicyDenied {
		t.Fatalf("호출 기한 승인 만료 기록이 다름: err=%v receipt=%+v", err, event.Receipt)
	}
}

// 사용자 승인 모드: 요청 상한보다 늦게 와도 승인 기한 안의 허용이면 보낸다. 송신은 승인 뒤부터 요청 상한으로 끊는다.
// 자동 모드(승인 경계 없음)는 지금처럼 요청 전체가 상한 안이다(사용자 결정 2026-11-21, 자동 모드 영향 없음).
func TestE3CreateApprovalWaitOutsideRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gate      RequestApprovalGate
		slow      time.Duration
		timeout   time.Duration
		requests  int64
		timedOut  bool
		maxWaited time.Duration
	}{
		{name: "approved-after-request-timeout", gate: &e3ApprovingGate{after: 200 * time.Millisecond}, timeout: 80 * time.Millisecond, requests: 1},
		{name: "send-bounded-after-approval", gate: &e3ApprovingGate{after: 10 * time.Millisecond}, slow: 600 * time.Millisecond, timeout: 100 * time.Millisecond, requests: 1, timedOut: true, maxWaited: 450 * time.Millisecond},
		{name: "auto-mode-unchanged", slow: 600 * time.Millisecond, timeout: 100 * time.Millisecond, requests: 1, timedOut: true, maxWaited: 450 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tc.slow > 0 {
					select {
					case <-time.After(tc.slow):
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"book-a"}`)
			}))
			defer server.Close()
			spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
				BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
			e, in, _ := e3CreateFixture(t, server.URL, spec, "")
			in.Timeout = tc.timeout
			if tc.gate != nil {
				e.cfg.ApprovalMode = ApprovalConfirmEveryRequest
				e.cfg.RequestApproval = tc.gate
			}
			started := time.Now()
			event, err := e.SendE3Create(t.Context(), in)
			waited := time.Since(started)
			if requests.Load() != tc.requests {
				t.Fatalf("대상 도착 %d건, 기대 %d건", requests.Load(), tc.requests)
			}
			if tc.maxWaited > 0 && waited > tc.maxWaited {
				t.Fatalf("송신이 요청 상한으로 끊기지 않음: %s", waited)
			}
			if tc.timedOut {
				if err != nil || event.E3Audit == nil || !event.E3Audit.TimedOut || !event.ForwardAttempted || event.ResponseComplete {
					t.Fatalf("요청 상한 초과가 시간 초과로 남지 않음: err=%v receipt=%+v", err, event.Receipt)
				}
				return
			}
			if err != nil || event.E3Audit == nil || !event.ResponseComplete || event.E3Audit.NotSentReason != "" || event.E3Audit.TimedOut {
				t.Fatalf("승인된 생성이 보내지지 않음: err=%v receipt=%+v", err, event.Receipt)
			}
		})
	}
}

// 사용자 승인 모드에서 승인 뒤, intent 기록과 송신 사이에 결속이 철회되면 진위 실패(ErrAuthorityRevoked)다. 송신 문맥이
// 끝난 것(취소)으로 바뀌지 않는다(#839 윤재상 리뷰). intent 기록을 막아 그 사이에 철회한다.
func TestE3CreateRevokedAfterApprovalIsAuthorityRevoked(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	spec := e3contract.CreateSpec{Path: "/allowed/books", Method: "POST", Kind: e3contract.BodyJSON,
		BodyTemplate: `{}`, MarkerField: "marker", IDPath: "id"}
	e, in, recorder := e3CreateFixture(t, server.URL, spec, "")
	accounts, ok := in.Accounts.(*e3IdentityAccounts)
	if !ok {
		t.Fatal("fixture accounts type changed")
	}
	recorder.blockStage, recorder.entered, recorder.release = "intent", make(chan struct{}, 1), make(chan struct{})
	go func() {
		<-recorder.entered
		accounts.mu.Lock()
		accounts.current = false
		accounts.mu.Unlock()
		close(recorder.release)
	}()
	e.cfg.ApprovalMode = ApprovalConfirmEveryRequest
	e.cfg.RequestApproval = &e3ApprovingGate{}
	_, err := e.SendE3Create(t.Context(), in)
	if requests.Load() != 0 || !errors.Is(err, e3contract.ErrAuthorityRevoked) {
		t.Fatalf("승인 뒤 철회가 진위 실패로 남지 않음: requests=%d err=%v", requests.Load(), err)
	}
}

// deadlineOnlyCtx는 기한은 지났지만 타이머 콜백이 아직 돌지 않아 Err()가 빈 문맥이다(부하 중 경합 재현).
type deadlineOnlyCtx struct {
	context.Context
	deadline time.Time
}

func (c deadlineOnlyCtx) Deadline() (time.Time, bool) { return c.deadline, true }
func (c deadlineOnlyCtx) Err() error                  { return nil }

// 보낸 뒤 응답을 다 받지 못했고 요청 문맥의 기한이 지났으면, 타이머 콜백이 늦어 Err()가 비어 있어도 시간 초과다.
// 기한 전이거나 기한이 없으면 시간 초과가 아니다(결과 불명).
func TestE3ProgramCallFinishTimedOutByDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{name: "deadline-passed-err-pending", ctx: deadlineOnlyCtx{Context: t.Context(), deadline: time.Now().Add(-time.Millisecond)}, want: true},
		{name: "deadline-ahead", ctx: deadlineOnlyCtx{Context: t.Context(), deadline: time.Now().Add(time.Hour)}},
		{name: "no-deadline", ctx: t.Context()},
	} {
		c := &e3ProgramCall{}
		r := Receipt{Decision: "allowed", ForwardAttempted: true, RequestWritten: true, E3Audit: &E3Audit{}}
		c.finish(&r, tc.ctx)
		if r.E3Audit.TimedOut != tc.want {
			t.Errorf("%s: TimedOut=%v, 기대 %v", tc.name, r.E3Audit.TimedOut, tc.want)
		}
	}
}

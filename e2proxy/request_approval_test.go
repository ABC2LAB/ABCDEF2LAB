package e2proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/transmit/budget"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type requestApprovalFunc func(context.Context, RequestApprovalProposal) (bool, error)

func (f requestApprovalFunc) AwaitRequestApproval(ctx context.Context, p RequestApprovalProposal) (bool, error) {
	return f(ctx, p)
}

func TestRequestApprovalHoldAndSingleDispatch(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.URL.RequestURI() != "/allowed?value=original" || string(body) != "original" || r.Header.Get("X-Input") != "original" {
			t.Error("request no longer matches the retained proposal")
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	proposed := make(chan RequestApprovalProposal, 1)
	release := make(chan bool, 1)
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.Rules[0].Methods = []string{"POST"}
	cfg.Limits.RequestTimeout = 100 * time.Millisecond
	cfg.ApprovalMode = ApprovalConfirmEveryRequest
	cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, p RequestApprovalProposal) (bool, error) {
		proposed <- p
		select {
		case allowed := <-release:
			return allowed, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	_, c := coreOpen(t, cfg)
	req := coreRequest(t, "POST", target.URL+"/allowed?value=original")
	req.Header.Set("X-Input", "original")
	req.Body = io.NopCloser(strings.NewReader("original"))
	done := make(chan Receipt, 1)
	go func() { _, r, _ := c.Do(context.Background(), req); done <- r }()
	var p RequestApprovalProposal
	select {
	case p = <-proposed:
	case <-time.After(time.Second):
		t.Fatalf("approval not reached; actual target receipts=%d", hits.Load())
	}
	if p.Receipt.AttemptID == "" || p.OwnerRef != cfg.OwnerRef || p.AccountRef != cfg.AccountRef || p.SessionRef != cfg.SessionRef ||
		p.Receipt.PolicyRevision != cfg.PolicyRevision || p.Receipt.Scope != cfg.Scope ||
		p.Method != "POST" || string(p.Body) != "original" {
		t.Fatal("proposal binding missing")
	}
	// 승인 대기는 대상 응답 제한보다 길어도 미송신이며, 제안 복사본 수정은 송신 변경이 아니다.
	time.Sleep(150 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatal("request sent before approval")
	}
	p.Headers.Set("X-Input", "changed")
	p.Body[0] = 'X'
	req.URL.RawQuery = "value=changed"
	req.Header.Set("X-Input", "changed")
	release <- true
	r := <-done
	if !r.ResponseComplete || !r.AuditComplete || hits.Load() != 1 {
		t.Fatalf("result %+v, hits %d", r, hits.Load())
	}
	if string(p.Body) != "Xriginal" {
		t.Fatal("approval evidence was erased after dispatch")
	}
	release <- true
	if hits.Load() != 1 {
		t.Fatal("duplicate decision dispatched again")
	}
}

func TestRequestApprovalDenialsNeverReachTarget(t *testing.T) {
	for _, scenario := range []string{"deny", "store-failure", "cancel-store-failure", "cancel", "stop", "revoke", "close", "expire", "policy", "budget", "audit"} {
		t.Run(scenario, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
			defer target.Close()
			rec := &coreRecorder{}
			cfg := coreConfig(t, target.URL, rec, 1)
			if scenario == "expire" {
				cfg.Limits.CallTimeout = 50 * time.Millisecond
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			cfg.ApprovalMode = ApprovalConfirmEveryRequest
			cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, p RequestApprovalProposal) (bool, error) {
				close(entered)
				<-release
				if scenario == "store-failure" || scenario == "cancel-store-failure" {
					return false, errors.New("private failure")
				}
				return scenario != "deny", nil
			})
			e, c := coreOpen(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan Receipt, 1)
			go func() { _, r, _ := c.Do(ctx, coreRequest(t, "GET", target.URL+"/allowed")); done <- r }()
			<-entered
			if hits.Load() != 0 {
				t.Fatal("sent while awaiting decision")
			}
			switch scenario {
			case "cancel", "cancel-store-failure":
				cancel()
			case "stop":
				_ = e.Stop(context.Background())
			case "revoke":
				_ = e.Revoke(context.Background())
			case "close":
				c.Block()
			case "expire":
				time.Sleep(80 * time.Millisecond)
			case "policy":
				if err := e.Restrict(context.Background(), Restriction{PolicyRevision: "revision2", ExpiresAt: cfg.ExpiresAt, RuleRefs: []string{"rule"}, Methods: []string{"GET"}}); err != nil {
					t.Fatal(err)
				}
			case "budget":
				if err := cfg.Budget.Ledger.Reserve(cfg.Budget.Key, cfg.Budget.Binding, budget.Attempt{RunRef: "other-run", Sequence: 1}, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "audit":
				rec.failStage = "intent"
			}
			close(release)
			r := <-done
			if hits.Load() != 0 || !r.NotSent || r.ForwardAttempted || r.ResponseReceived {
				t.Fatalf("unexpected transmission %+v hits=%d", r, hits.Load())
			}
			if scenario == "cancel-store-failure" && (r.Reason != AuditFailed || e.Snapshot().Reason != AuditFailed) {
				t.Fatal("real storage error hidden by caller cancellation")
			}
			if scenario == "cancel" || scenario == "policy" {
				remaining, err := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
				if err != nil || remaining != 1 {
					t.Fatalf("denial reserved budget: %d %v", remaining, err)
				}
				for _, event := range rec.events {
					if event.Stage == "intent" {
						t.Fatal("denial recorded a transmission intent")
					}
				}
				if scenario == "cancel" && r.Reason != Closed {
					t.Fatalf("late allow reason=%s", r.Reason)
				}
			}

		})
	}
}

func TestRequestApprovalProposalDoesNotSerializePrivateInput(t *testing.T) {
	p := RequestApprovalProposal{URL: "secret-url", Body: []byte("secret-body"), Headers: http.Header{"X-Private": {"secret-header"}}}
	b, err := json.Marshal(p)
	if err != nil || !bytes.Equal(b, []byte("{}")) {
		t.Fatal("private proposal JSON exposure")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", p, p), "secret") {
		t.Fatal("private proposal log exposure")
	}
}

func TestRequestApprovalWaitEndsWithCallContext(t *testing.T) {
	for _, scenario := range []string{"cancel", "stop", "expire"} {
		t.Run(scenario, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 1)
			if scenario == "expire" {
				cfg.Limits.CallTimeout = 50 * time.Millisecond
			}
			entered := make(chan struct{})
			cfg.ApprovalMode = ApprovalConfirmEveryRequest
			cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, p RequestApprovalProposal) (bool, error) {
				close(entered)
				<-ctx.Done()
				return true, nil // 늦은 허용도 취소를 이기지 못한다.
			})
			e, c := coreOpen(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan Receipt, 1)
			go func() { _, r, _ := c.Do(ctx, coreRequest(t, "GET", target.URL+"/allowed")); done <- r }()
			<-entered
			if scenario == "cancel" {
				cancel()
			}
			if scenario == "stop" {
				_ = e.Stop(context.Background())
			}
			select {
			case r := <-done:
				if !r.NotSent || hits.Load() != 0 {
					t.Fatal("canceled wait dispatched")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("approval wait did not end")
			}
		})
	}
}

func TestRequestApprovalExcludesProtectedCredentialValues(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-Session") != "fixture-private-session" {
			t.Error("protected session missing")
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 1)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"fixture-private-session"}}
	cfg.ApprovalMode = ApprovalConfirmEveryRequest
	cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, p RequestApprovalProposal) (bool, error) {
		if headerPresent(p.Headers, "X-Session") || strings.Contains(p.URL+string(p.Body)+fmt.Sprint(p.Headers), "fixture-private-session") {
			t.Error("protected credential exposed to approval")
			return false, nil
		}
		return p.SessionRef == cfg.SessionRef, nil
	})
	_, c := coreOpen(t, cfg)
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || !r.ResponseComplete || hits.Load() != 1 {
		t.Fatalf("approved credential request %+v %v", r, err)
	}
}

func TestRequestApprovalModeRejectsMissingAndConflictingConfiguration(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	var typedNil requestApprovalFunc
	allow := requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { return true, nil })
	for _, tc := range []struct {
		name string
		mode ApprovalMode
		gate RequestApprovalGate
	}{
		{"missing", "", nil},
		{"missing-with-gate", "", allow},
		{"unknown", "unknown", allow},
		{"confirmation-without-gate", ApprovalConfirmEveryRequest, nil},
		{"confirmation-with-typed-nil", ApprovalConfirmEveryRequest, typedNil},
		{"automatic-with-gate", ApprovalPreauthorizedScope, allow},
		{"automatic-with-typed-nil", ApprovalPreauthorizedScope, typedNil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
			cfg.ApprovalMode, cfg.RequestApproval = tc.mode, tc.gate
			e, err := NewExecution(context.Background(), cfg)
			if !errors.Is(err, ErrConfig) || e != nil {
				t.Fatal("invalid approval configuration accepted")
			}
			if hits.Load() != 0 {
				t.Fatal("target received request on configuration failure")
			}
		})
	}
}

func TestRequestApprovalModesPreserveBoundedUsefulDispatch(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalConfirmEveryRequest, ApprovalPreauthorizedScope} {
		t.Run(string(mode), func(t *testing.T) {
			var hits, approvals atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
			cfg.ApprovalMode = mode
			if mode == ApprovalConfirmEveryRequest {
				cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { approvals.Add(1); return true, nil })
			}
			e, c := coreOpen(t, cfg)
			if e.Policy().ApprovalMode != mode || e.Snapshot().ApprovalMode != mode {
				t.Fatal("selected mode missing from host views")
			}
			_, receipt, _ := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if !receipt.ResponseComplete || hits.Load() != 1 {
				t.Fatalf("normal allowed request failed: %+v, receipts=%d", receipt, hits.Load())
			}
			_, denied, _ := c.Do(context.Background(), coreRequest(t, "POST", target.URL+"/allowed"))
			if !denied.NotSent || hits.Load() != 1 {
				t.Fatalf("scope was widened: %+v, receipts=%d", denied, hits.Load())
			}
			if mode == ApprovalPreauthorizedScope && approvals.Load() != 0 {
				t.Fatal("automatic mode invoked confirmation")
			}
			if mode == ApprovalConfirmEveryRequest && approvals.Load() == 0 {
				t.Fatal("confirmation bypassed")
			}
		})
	}
}

func TestRequestApprovalOperationalFailureLatchesExecution(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			var hits, approvals atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
			cfg.ApprovalMode = ApprovalConfirmEveryRequest
			cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) {
				if approvals.Add(1) == 1 {
					return allowed, errors.New("approval storage unavailable")
				}
				return true, nil
			})
			e, c := coreOpen(t, cfg)
			_, receipt, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if !errors.Is(err, ErrAudit) || receipt.Reason != AuditFailed || receipt.AuditComplete || !receipt.NotSent || hits.Load() != 0 {
				t.Fatalf("operational error became ordinary denial: %+v, err=%v, receipts=%d", receipt, err, hits.Load())
			}
			if view := e.Snapshot(); view.AuditComplete || view.Reason != AuditFailed {
				t.Fatalf("failure not latched in execution: %+v", view)
			}
			if result := c.Result(); result.AuditComplete || result.Reason != AuditFailed {
				t.Fatalf("failure not latched in call: %+v", result)
			}
			_, later, laterErr := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if laterErr == nil || !later.NotSent || hits.Load() != 0 || approvals.Load() != 1 {
				t.Fatalf("later approval bypassed failure: %+v, receipts=%d approvals=%d", later, hits.Load(), approvals.Load())
			}
			if _, err := e.BeginCall(context.Background(), "later-command"); err == nil {
				t.Fatal("new call reopened failed execution")
			}
			if e.Snapshot().AuditComplete || c.Result().AuditComplete {
				t.Fatal("later request erased failure")
			}
		})
	}
}

func TestRequestApprovalNormalDenialStillAllowsAlternative(t *testing.T) {
	var hits, approvals atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.ApprovalMode = ApprovalConfirmEveryRequest
	cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { return approvals.Add(1) > 1, nil })
	e, c := coreOpen(t, cfg)
	_, denied, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrDenied) || denied.Reason != PolicyDenied || !denied.AuditComplete || hits.Load() != 0 {
		t.Fatalf("normal denial misclassified: %+v", denied)
	}
	_, allowed, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || !allowed.ResponseComplete || hits.Load() != 1 || !e.Snapshot().AuditComplete {
		t.Fatalf("allowed alternative blocked: %+v, receipts=%d", allowed, hits.Load())
	}
}

func TestRequestApprovalCallerCancellationIsNotOperationalFailure(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.ApprovalMode = ApprovalConfirmEveryRequest
	cfg.RequestApproval = requestApprovalFunc(func(wait context.Context, _ RequestApprovalProposal) (bool, error) {
		cancel()
		<-wait.Done()
		return false, wait.Err()
	})
	e, c := coreOpen(t, cfg)
	_, receipt, err := c.Do(ctx, coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrDenied) || receipt.Reason != Closed || !receipt.NotSent || hits.Load() != 0 || !e.Snapshot().AuditComplete {
		t.Fatalf("caller cancellation misclassified: %+v, err=%v", receipt, err)
	}
}

func TestRequestApprovalRecordedSuccessCannotEraseConcurrentFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Execution{ctx: ctx, cancel: cancel}
	c := &Call{e: e, auditComplete: true}
	e.mu.Lock()
	// 정상 요청은 이미 기록을 마쳤다(err=nil). 잠금 획득 전 다른 요청이 실패했다.
	c.preserveOutcomeLocked(Receipt{Reason: AuditFailed}, nil)
	c.preserveOutcomeLocked(Receipt{Reason: Complete}, nil)
	if !e.auditFailed || c.auditComplete || c.reason != AuditFailed || e.stopReason != AuditFailed || ctx.Err() == nil {
		e.mu.Unlock()
		t.Fatal("recorded success erased concurrent operational failure")
	}
	e.mu.Unlock()
}

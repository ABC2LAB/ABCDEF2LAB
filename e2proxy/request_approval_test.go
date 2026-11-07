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
	for _, scenario := range []string{"deny", "store-failure", "cancel", "stop", "revoke", "close", "expire", "policy", "budget", "audit"} {
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
			cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, p RequestApprovalProposal) (bool, error) {
				close(entered)
				<-release
				if scenario == "store-failure" {
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
			case "cancel":
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

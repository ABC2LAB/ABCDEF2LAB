package e2proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type selectiveBoundary struct {
	deny           bool
	afterFail      bool
	after          atomic.Int64
	before         atomic.Int64
	denyAfterFirst bool
	misclassify    bool
}

func (b *selectiveBoundary) Before(context.Context, RequestView) error {
	count := b.before.Add(1)
	if b.deny || b.denyAfterFirst && count > 1 {
		return ErrDenied
	}
	return nil
}
func (b *selectiveBoundary) PreapprovedRead(v RequestView) bool {
	return b.misclassify || v.Method == "GET" && !v.BodyPresent
}

func TestWorkflowPolicyRejectsTypedNilAndNeverExemptsPOST(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1:1", &coreRecorder{}, 4)
	cfg.ApprovalMode = ApprovalWorkbenchV1
	cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { return true, nil })
	var missing *selectiveBoundary
	cfg.RequestBoundary = missing
	if validApprovalConfig(cfg) {
		t.Fatal("typed nil boundary accepted")
	}
	var hits, decisions atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer target.Close()
	cfg = coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.Rules[0].Methods = []string{"POST"}
	cfg.ApprovalMode = ApprovalWorkbenchV1
	cfg.RequestBoundary = &selectiveBoundary{misclassify: true}
	cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { decisions.Add(1); return false, nil })
	_, call := coreOpen(t, cfg)
	if _, _, err := call.Do(t.Context(), coreRequest(t, "POST", target.URL+"/allowed")); err == nil || hits.Load() != 0 || decisions.Load() != 1 {
		t.Fatal("POST bypassed decision through faulty read classifier")
	}
}

func TestWorkflowRechecksBoundaryAfterApprovalBeforeWrite(t *testing.T) {
	var hits, decisions atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.Rules[0].Methods = []string{"POST"}
	cfg.ApprovalMode = ApprovalWorkbenchV1
	cfg.RequestBoundary = &selectiveBoundary{denyAfterFirst: true}
	cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { decisions.Add(1); return true, nil })
	_, call := coreOpen(t, cfg)
	_, receipt, err := call.Do(t.Context(), coreRequest(t, "POST", target.URL+"/allowed"))
	if err == nil || hits.Load() != 0 || decisions.Load() != 1 || receipt.ForwardAttempted {
		t.Fatal("approval bypassed changed selection/pause boundary")
	}
}
func (b *selectiveBoundary) After(context.Context, RequestView, Receipt) error {
	b.after.Add(1)
	if b.afterFail {
		return errors.New("owned storage failure")
	}
	return nil
}
func TestWorkbenchPolicyReadBypassAndStateChangeDecision(t *testing.T) {
	for _, allow := range []bool{false, true} {
		var hits, approvals atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, "owned") }))
		cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
		cfg.Rules[0].Methods = []string{"GET", "POST"}
		boundary := &selectiveBoundary{}
		cfg.RequestBoundary = boundary
		cfg.ApprovalMode = ApprovalWorkbenchV1
		cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) {
			approvals.Add(1)
			if hits.Load() != 1 {
				t.Error("stateful request reached target before approval")
			}
			return allow, nil
		})
		_, call := coreOpen(t, cfg)
		if _, _, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); err != nil || approvals.Load() != 0 || hits.Load() != 1 {
			t.Fatal("attested read did not bypass per-request confirmation", err)
		}
		_, r, err := call.Do(t.Context(), coreRequest(t, "POST", target.URL+"/allowed"))
		want := int64(1)
		if allow {
			want = 2
		}
		if (err == nil) != allow || hits.Load() != want || approvals.Load() != 1 || r.ForwardAttempted != allow {
			t.Fatal("stateful decision not enforced", allow, err, r)
		}
		target.Close()
	}
}
func TestWorkflowBoundaryAndAuditFailureCannotBeBypassed(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, "owned") }))
	defer target.Close()
	boundary := &selectiveBoundary{deny: true}
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.RequestBoundary = boundary
	_, call := coreOpen(t, cfg)
	_, r, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || hits.Load() != 0 || r.ForwardAttempted {
		t.Fatal("unselected request sent")
	}
	boundary.deny = false
	boundary.afterFail = true
	if _, _, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); !errors.Is(err, ErrAudit) {
		t.Fatal("workflow audit failure ignored", err)
	}
	if _, _, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); err == nil || hits.Load() != 1 {
		t.Fatal("audit failure allowed another request")
	}
}

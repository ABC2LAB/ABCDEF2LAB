//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/e2proxy"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestApprovalProductionFactoryReceiverOwnedPostgres(t *testing.T) {
	d := approvalTestDB(t)
	task, err := d.CreateTask("synthetic factory approval", "fixture", nil, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.OwnerRef = "ARTEX"
	policy.ApprovalMode = e2proxy.ApprovalConfirmEveryRequest
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/allowed"
	policy.Rules[0].ApprovalDisplay.PublicPath = true
	write := func() {
		raw, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(cfg.PolicyFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	service := newRequestApprovalService(d)
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}}
	if _, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err == nil {
		t.Fatal("confirmation without handler accepted")
	}
	input.RequestApprovals = service
	policy.ApprovalMode = ""
	write()
	if _, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err == nil {
		t.Fatal("missing mode accepted")
	}
	policy.ApprovalMode = e2proxy.ApprovalConfirmEveryRequest
	write()
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewE2HTTPRuntime(t.Context(), options)
	if err != nil || runtime.options.RequestApprovals != service {
		t.Fatal("runtime lost approval handler", err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	acquired, err := options.Acquire(run, E2HTTPRequest{TaskID: strconv.FormatInt(task.ID, 10), TaskIDNumber: task.ID, ExplorationID: task.ExplorationID, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Release(context.Background())
	call, err := acquired.Execution.BeginCall(run, "synthetic-command")
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close(context.Background())
	done := make(chan e2proxy.Receipt, 1)
	go func() {
		request, _ := http.NewRequest("GET", target.URL+"/allowed", nil)
		_, receipt, _ := call.Do(run, request)
		done <- receipt
	}()
	var id, digest string
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		rows, err := service.list(t.Context(), task.ID, "ARTEX")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			id, digest = rows[0].ID, rows[0].RequestSHA256
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" || hits.Load() != 0 {
		t.Fatal("factory did not hold before send")
	}
	s := &Server{m: &Manager{pg: d}, jwtKey: []byte(strings.Repeat("a", 32)), requestApprovals: runtime.options.RequestApprovals}
	hash, err := s.reloadPassHash()
	if err != nil {
		t.Fatal(err)
	}
	token, err := signJWT(s.signingKey(hash))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks/{id}/request-approvals/{approvalID}/decision", s.requestApprovalDecide)
	raw, _ := json.Marshal(map[string]string{"decision": "allow", "request_sha256": digest})
	req := httptest.NewRequest("POST", "/api/tasks/"+strconv.FormatInt(task.ID, 10)+"/request-approvals/"+id+"/decision", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.requireAuth(s.firstDiagnosisRoutes(mux)).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("decision API failed", rec.Code, rec.Body.String())
	}
	select {
	case receipt := <-done:
		if !receipt.ResponseComplete || hits.Load() != 1 {
			t.Fatal("factory allowed path did not send exactly one observed request")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("factory request did not complete")
	}
	record, err := service.get(t.Context(), task.ID, "ARTEX", id)
	if err != nil || !record.Consumed || record.ExecutionState != "completed" {
		t.Fatal("factory journal observer did not record completion", err)
	}
}

//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func TestRequestApprovalFactoryRunStopRouter(t *testing.T) {
	for _, mode := range []e2proxy.ApprovalMode{e2proxy.ApprovalPreauthorizedScope, e2proxy.ApprovalConfirmEveryRequest} {
		for _, acquired := range []bool{false, true} {
			t.Run(string(mode)+"/acquired="+strconv.FormatBool(acquired), func(t *testing.T) {
				d := approvalTestDB(t)
				row, err := d.CreateTask("synthetic stop", "fixture", nil, 0, 300)
				if err != nil {
					t.Fatal(err)
				}
				defer d.DeleteTask(row.ID)
				other, err := d.CreateTask("synthetic other", "fixture", nil, 0, 300)
				if err != nil {
					t.Fatal(err)
				}
				defer d.DeleteTask(other.ID)
				var hits atomic.Int64
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
				defer target.Close()
				cfg, _, policy := e2HostPolicyFixture(t)
				policy.OwnerRef = "ARTEX"
				policy.ApprovalMode = mode
				policy.Rules[0].Origin = target.URL
				policy.Rules[0].PathPrefix = "/allowed"
				raw, _ := json.Marshal(policy)
				if err = os.WriteFile(cfg.PolicyFile, raw, 0600); err != nil {
					t.Fatal(err)
				}
				service := newRequestApprovalService(d)
				options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}, RequestApprovals: service})
				if err != nil {
					t.Fatal(err)
				}
				runtime, err := NewE2HTTPRuntime(t.Context(), options)
				if err != nil {
					t.Fatal(err)
				}
				task := taskFromPG(row, d.Exploration(row.ExplorationID), nil)
				if err = runtime.AdmitTask(t.Context(), task); err != nil {
					t.Fatal(err)
				}
				request := E2HTTPRequest{TaskID: task.ID, TaskIDNumber: row.ID, ExplorationID: row.ExplorationID, IntentID: 1}
				var lease E2HTTPRunOptions
				var call *e2proxy.Call
				if acquired {
					lease, err = options.Acquire(runtime.task.ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					defer lease.Release(context.Background())
					call, err = lease.Execution.BeginCall(runtime.task.ctx, "synthetic")
					if err != nil {
						t.Fatal(err)
					}
					defer call.Close(context.Background())
				}
				s := &Server{m: &Manager{pg: d}, jwtKey: []byte(strings.Repeat("a", 32)), requestApprovals: service, firstDiagnosis: true}
				hash, err := s.reloadPassHash()
				if err != nil {
					t.Fatal(err)
				}
				token, err := signJWT(s.signingKey(hash))
				if err != nil {
					t.Fatal(err)
				}
				handler := s.Handler()
				invoke := func(method, path, auth string) *httptest.ResponseRecorder {
					req := httptest.NewRequest(method, path, nil)
					if auth != "" {
						req.Header.Set("Authorization", "Bearer "+auth)
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					return rec
				}
				base := "/api/tasks/" + task.ID + "/request-approvals"
				if rec := invoke("POST", base+"/stop", ""); rec.Code != 401 {
					t.Fatalf("unauthorized: %d", rec.Code)
				}
				if rec := invoke("POST", "/api/tasks/"+strconv.FormatInt(other.ID, 10)+"/request-approvals/stop", token); rec.Code != 403 {
					t.Fatalf("other task: %d", rec.Code)
				}
				if service.userStopped(row.ID) {
					t.Fatal("unauthorized stop changed state")
				}
				for i := 0; i < 2; i++ {
					rec := invoke("POST", base+"/stop", token)
					if rec.Code != 200 {
						t.Fatalf("stop %d: %d %s", i, rec.Code, rec.Body.String())
					}
				}
				rec := invoke("GET", base, token)
				if rec.Code != 200 {
					t.Fatalf("status: %d", rec.Code)
				}
				var status struct {
					Run requestApprovalRunState `json:"run"`
				}
				if err = json.Unmarshal(rec.Body.Bytes(), &status); err != nil || !status.Run.StopRequested || !status.Run.ContextCancelled || status.Run.RunRef != policy.RunRef {
					t.Fatalf("incorrect run status: %s", rec.Body.String())
				}
				if _, err = runtime.PreparePlanning(t.Context(), task); err == nil {
					t.Fatal("planner survived stop")
				}
				if acquired {
					req, _ := http.NewRequest("GET", target.URL+"/allowed", nil)
					_, receipt, _ := call.Do(t.Context(), req)
					if receipt.ForwardAttempted {
						t.Fatal("transmitted after stop")
					}
				} else if _, err = options.Acquire(runtime.task.ctx, request); err == nil {
					t.Fatal("acquired after stop")
				}
				if hits.Load() != 0 {
					t.Fatalf("target hits after stop: %d", hits.Load())
				}
				if err = service.bind(t.Context(), row.ID, "ARTEX", policy.RunRef, policy.Rules); err == nil {
					t.Fatal("stopped run rebound")
				}
			})
		}
	}
}

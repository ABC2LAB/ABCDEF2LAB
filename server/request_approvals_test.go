package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/golang-jwt/jwt/v5"
)

type approvalTestRecorder struct {
	service        *RequestApprovalService
	fail           atomic.Bool
	resultFail     atomic.Bool
	controlEntered chan struct{}
	controlRelease chan struct{}
}

func (r *approvalTestRecorder) Record(ctx context.Context, e e2proxy.Event) error {
	if e.Stage == "control" && r.controlEntered != nil {
		close(r.controlEntered)
		select {
		case <-r.controlRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.resultFail.Load() && e.Stage == "result" {
		return errors.New("synthetic upstream result failure")
	}
	if r.fail.Load() && e.Stage == "intent" {
		return errors.New("synthetic persistence failure")
	}
	return r.service.observe(ctx, e)
}

type approvalResultFailStore struct{ requestApprovalStore }

func (s approvalResultFailStore) SwapRequestApproval(ctx context.Context, a db.RequestApproval, fresh bool) error {
	if a.ExecutionState == "completed" || a.ExecutionState == "unknown" {
		return errors.New("synthetic result persistence failure")
	}
	return s.requestApprovalStore.SwapRequestApproval(ctx, a, fresh)
}

type approvalFailStore struct{ requestApprovalStore }

func (approvalFailStore) InsertRequestApproval(context.Context, db.RequestApproval) error {
	return errors.New("synthetic unavailable")
}

func approvalTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_REQUEST_APPROVAL_TEST_DSN")
	owned := os.Getenv("ARTEX_REQUEST_APPROVAL_TEST_OWNED") == "1"
	// 저장소 CI가 매 패키지마다 새로 만드는 승인 시험 DB를 재사용한다.
	if dsn == "" && os.Getenv("CI_PG_SOCKET") != "" && os.Getenv("CI_PG_DATABASES") != "" {
		dsn = os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
		owned = dsn != ""
	}
	if dsn == "" {
		t.Skip("요청별 승인 시험 전용 DB 없음")
	}
	if !owned {
		t.Fatal("소유 시험 DB 확인 필요")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
func TestRequestApprovalAPIControlledReceiverOwnedPostgres(t *testing.T) {
	d := approvalTestDB(t)
	for _, scenario := range []string{"allow", "deny", "expire", "cancel", "stop", "revoke", "policy", "budget", "audit", "store-failure", "unknown", "result-store-failure", "upstream-result-failure"} {
		t.Run(scenario, func(t *testing.T) {
			task, err := d.CreateTask("synthetic approval", "synthetic", nil, 0, 300)
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteTask(task.ID)
			taskRef := strconv.FormatInt(task.ID, 10)
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if scenario == "unknown" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
					return
				}
				io.WriteString(w, "ok")
			}))
			defer target.Close()
			run, cancelRun := context.WithCancel(t.Context())
			defer cancelRun()
			service := newRequestApprovalService(d)
			if err := service.bind(run, task.ID, "ARTEX", "run", []E2HTTPFileRule{{Ref: "rule", PathPrefix: "/allowed", ApprovalDisplay: RequestApprovalDisplay{PublicPath: true, QueryKeys: []string{"public"}, JSONFields: []string{"public", "nested", "password"}}}}); err != nil {
				t.Fatal(err)
			}
			if scenario == "store-failure" {
				service.store = approvalFailStore{d}
			}
			recorder := &approvalTestRecorder{service: service}
			if scenario == "policy" {
				recorder.controlEntered = make(chan struct{})
				recorder.controlRelease = make(chan struct{})
			}
			ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 2})
			if err != nil {
				t.Fatal(err)
			}
			key := budget.Key{TaskRef: taskRef, BudgetRef: "budget"}
			binding := budget.Binding{PolicyRef: "policy", ResetRef: "reset"}
			now := time.Now()
			if err := ledger.Register(budget.Grant{Key: key, Binding: binding, MaxRequests: 2, ValidFrom: now.Add(-time.Second), ValidUntil: now.Add(time.Minute)}, now); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(target.URL)
			cfg := e2proxy.Config{ApprovalMode: e2proxy.ApprovalConfirmEveryRequest, RequestApproval: service, Scope: transmit.Scope{TaskRef: taskRef, RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision", AccountRef: "account", SessionRef: "protected-session", ExpiresAt: now.Add(time.Minute), Budget: transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding}, Recorder: recorder, Rules: []e2proxy.Rule{{Ref: "rule", Origin: u.Scheme + "://" + u.Host, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "path", Methods: []string{"POST"}}}, Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 2, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeout: time.Second, CallTimeout: 3 * time.Second, MaxDenials: 8, MaxTransient: 8, MaxUnknown: 8}}
			if scenario == "expire" {
				cfg.Limits.CallTimeout = 250 * time.Millisecond
			}
			execution, err := e2proxy.NewExecution(run, cfg)
			if err != nil {
				t.Fatal(err)
			}
			service.attachExecution(task.ID, execution)
			call, err := execution.BeginCall(run, "command")
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest("POST", target.URL+"/allowed?public=value&secret=do-not-store", strings.NewReader(`{"public":"review-me","nested":{"password":"do-not-store"},"password":"do-not-store"}`))
			done := make(chan e2proxy.Receipt, 1)
			go func() { _, receipt, _ := call.Do(run, request); done <- receipt }()
			if scenario == "store-failure" {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("store failure did not return")
				}
				if hits.Load() != 0 {
					t.Fatal("store failure sent")
				}
				return
			}
			var pending db.RequestApproval
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				rows, e := d.ListRequestApprovals(t.Context(), task.ID, "ARTEX")
				if e != nil {
					t.Fatal(e)
				}
				if len(rows) > 0 {
					pending = rows[0]
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pending.ID == "" {
				t.Fatal("pending not stored")
			}
			if hits.Load() != 0 {
				t.Fatal("sent while pending")
			}
			encoded, _ := json.Marshal(pending)
			if strings.Contains(string(encoded), "do-not-store") || pending.InputPreview["body.public"] != `"review-me"` {
				t.Fatal("private payload leaked or public input missing")
			}
			server := &Server{m: &Manager{pg: d}, jwtKey: []byte(strings.Repeat("s", 32)), requestApprovals: service}
			hash, err := server.reloadPassHash()
			if err != nil {
				t.Fatal(err)
			}
			token, err := signJWT(server.signingKey(hash))
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/tasks/{id}/request-approvals/stop", server.requestApprovalStop)
			mux.HandleFunc("GET /api/tasks/{id}/request-approvals", server.requestApprovalList)
			mux.HandleFunc("GET /api/tasks/{id}/request-approvals/{approvalID}", server.requestApprovalGet)
			mux.HandleFunc("POST /api/tasks/{id}/request-approvals/{approvalID}/decision", server.requestApprovalDecide)
			handler := server.requireAuth(server.firstDiagnosisRoutes(mux))
			path := "/api/tasks/" + taskRef + "/request-approvals/" + pending.ID
			send := func(method, path, credential string, body any) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(body)
				req := httptest.NewRequest(method, path, bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				if credential != "" {
					req.Header.Set("Authorization", "Bearer "+credential)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}
			decision := map[string]string{"decision": "allow", "request_sha256": pending.RequestSHA256}
			if send("POST", path+"/decision", "", decision).Code != 401 {
				t.Fatal("unauthenticated decision accepted")
			}
			wrong, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "other", ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}).SignedString(server.signingKey(hash))
			if send("POST", path+"/decision", wrong, decision).Code != 401 {
				t.Fatal("wrong subject accepted")
			}
			if send("POST", "/api/tasks/"+strconv.FormatInt(task.ID+99999, 10)+"/request-approvals/"+pending.ID+"/decision", token, decision).Code != 403 {
				t.Fatal("wrong task accepted")
			}
			decision["request_sha256"] = strings.Repeat("0", 64)
			if send("POST", path+"/decision", token, decision).Code != 409 {
				t.Fatal("digest mismatch accepted")
			}
			decision["request_sha256"] = pending.RequestSHA256
			if hits.Load() != 0 {
				t.Fatal("invalid authorization sent request")
			}
			switch scenario {
			case "deny":
				decision["decision"] = "deny"
			case "expire":
				time.Sleep(300 * time.Millisecond)
			case "cancel":
				cancelRun()
			case "stop":
				if response := send("POST", "/api/tasks/"+taskRef+"/request-approvals/stop", token, map[string]any{}); response.Code != 200 {
					t.Fatal("API stop failed", response.Code)
				}
			case "revoke":
				execution.Revoke(t.Context())
			case "policy":
				restricted := make(chan error, 1)
				go func() {
					restricted <- execution.Restrict(t.Context(), e2proxy.Restriction{PolicyRevision: "revision2", ExpiresAt: cfg.ExpiresAt, RuleRefs: []string{"rule"}, Methods: []string{"POST"}})
				}()
				<-recorder.controlEntered
				view, readErr := service.get(t.Context(), task.ID, "ARTEX", pending.ID)
				if readErr != nil || view.State != "pending" || !view.Live {
					t.Fatal("temporary policy update was shown as cancelled", readErr, view.State)
				}
				close(recorder.controlRelease)
				if err := <-restricted; err != nil {
					t.Fatal(err)
				}
			case "budget":
				for i := uint64(1); i <= 2; i++ {
					if err := ledger.Reserve(key, binding, budget.Attempt{RunRef: "other", Sequence: i}, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
			case "audit":
				recorder.fail.Store(true)
			case "result-store-failure":
				service.store = approvalResultFailStore{d}
			case "upstream-result-failure":
				recorder.resultFail.Store(true)
			}
			response := send("POST", path+"/decision", token, decision)
			if scenario == "allow" || scenario == "deny" || scenario == "policy" || scenario == "budget" || scenario == "audit" || scenario == "unknown" || scenario == "result-store-failure" || scenario == "upstream-result-failure" {
				if response.Code != 200 {
					t.Fatalf("decision failed: %d %s", response.Code, response.Body.String())
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("request waiter did not finish")
			}
			expected := int64(0)
			if scenario == "allow" || scenario == "unknown" || scenario == "result-store-failure" || scenario == "upstream-result-failure" {
				expected = 1
			}
			if hits.Load() != expected {
				t.Fatalf("target received=%d want=%d", hits.Load(), expected)
			}
			if send("POST", path+"/decision", token, decision).Code == 200 {
				t.Fatal("duplicate decision accepted")
			}
			if hits.Load() != expected {
				t.Fatal("duplicate dispatch")
			}
			record, err := d.GetRequestApproval(t.Context(), task.ID, "ARTEX", pending.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "allow" && (!record.Consumed || record.ExecutionState != "completed") {
				t.Fatalf("allowed result not recorded: %+v", record)
			}
			if scenario == "unknown" && record.ExecutionState != "unknown" {
				t.Fatal("unknown result not preserved")
			}
			if scenario == "result-store-failure" || scenario == "upstream-result-failure" {
				view, err := service.get(t.Context(), task.ID, "ARTEX", pending.ID)
				if err != nil || view.ExecutionState != "unknown" {
					t.Fatal("lost result remained dispatch pending", err, view.ExecutionState)
				}
			}
			restarted := newRequestApprovalService(d)
			if err := restarted.bind(t.Context(), task.ID, "ARTEX", "new-run", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.decide(t.Context(), task.ID, "ARTEX", pending.ID, pending.RequestSHA256, "allow"); err == nil {
				t.Fatal("restart replay allowed")
			}
		})
	}
}

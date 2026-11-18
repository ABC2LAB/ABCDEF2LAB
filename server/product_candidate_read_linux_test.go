//go:build linux

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

// 보호 준비 서비스에는 실행 확인까지 합성으로 게시하고, 실제 결과 표는 PostgreSQL에 저장한다.
type candidateTestBackend struct {
	*reportFixtureBackend
	active chan struct{}
}

type candidateTestEndpoint struct {
	*reportFixtureEndpoint
	revoked  atomic.Bool
	revision atomic.Uint64
}

func (e *candidateTestEndpoint) Call(ctx context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	if req.Command != diagnosisprep.ProductControl {
		return e.reportFixtureEndpoint.Call(ctx, req)
	}
	if req.Product == nil || req.Product.Operation != "target" || req.Product.TargetID != "owned" {
		return diagnosisprep.Response{}, diagnosisprep.ErrProtocol
	}
	state := "approved"
	if e.revoked.Load() {
		state = "revoked"
	}
	return diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Product: &diagnosisprep.ProductResponse{
		Target: &diagnosisprep.ProductTarget{ID: "owned", Origin: "https://owned.test", State: state, Revision: e.revision.Load(), ExpiresAt: time.Now().Add(time.Hour)},
	}}, nil
}

func (b candidateTestBackend) Execute(ctx context.Context, exec diagnosisprep.Execution, publish diagnosisprep.Publish) error {
	return b.reportFixtureBackend.Execute(ctx, exec, func(u diagnosisprep.Update) error {
		if u.State != diagnosisprep.Completed {
			return publish(u)
		}
		close(b.active)
		<-ctx.Done()
		return ctx.Err()
	})
}

func activeCandidateJob(t *testing.T, task string) (*diagnosisprep.Job, *reportFixtureEndpoint, candidateTestBackend, string) {
	t.Helper()
	root := firstDiagnosisPrivateTemp(t)
	backend := candidateTestBackend{&reportFixtureBackend{task: task, expires: time.Now().UTC().Add(time.Hour)}, make(chan struct{})}
	prep, err := diagnosisprep.New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := prep.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	call := func(r diagnosisprep.Request) *diagnosisprep.Job {
		t.Helper()
		r.Version, r.RequestID, r.Principal = diagnosisprep.Version, "candidate-fixture", "ARTEX"
		out, err := prep.Call(t.Context(), r)
		if err != nil || out.Error != "" || diagnosisprep.ValidateResponse(r, out) != nil || out.Job == nil {
			t.Fatal("Job fixture 실패", err, out.Error)
		}
		return out.Job
	}
	job := call(diagnosisprep.Request{Command: diagnosisprep.Create, IdempotencyKey: "create", Input: &diagnosisprep.Input{TargetURL: "https://owned.test/", ProfileID: "local"}})
	call(diagnosisprep.Request{Command: diagnosisprep.Start, JobID: job.ID, ExpectedRevision: job.Revision, IdempotencyKey: "start"})
	select {
	case <-backend.active:
	case <-time.After(5 * time.Second):
		t.Fatal("활성 Job을 게시하지 못했습니다")
	}
	return call(diagnosisprep.Request{Command: diagnosisprep.AuthorizeTask, TaskID: task}), &reportFixtureEndpoint{inner: prep}, backend, root
}

func TestProductCandidateRequestScope(t *testing.T) {
	for _, test := range []struct {
		method, path, body string
		valid              bool
	}{
		{"GET", "/api/tasks/1/diagnostic-candidates", "", true},
		{"GET", "/api/tasks/9223372036854775807/diagnostic-candidates", "", true},
		{"GET", "/api/tasks/9223372036854775808/diagnostic-candidates", "", false},
		{"GET", "/api/tasks/01/diagnostic-candidates", "", false},
		{"GET", "/api/tasks/0/diagnostic-candidates", "", false},
		{"GET", "/api/tasks/%31/diagnostic-candidates", "", false},
		{"GET", "/api/tasks/1/diagnostic%2Dcandidates", "", false},
		{"GET", "/api/tasks/1/diagnostic-candidates?task=2", "", false},
		{"GET", "/api/tasks/1/diagnostic-candidates?", "", false},
		{"GET", "/api/tasks/1/diagnostic-candidates/", "", false},
		{"GET", "/api/tasks/1/diagnostic-candidates/extra/..", "", false},
		{"GET", "/api/tasks/2/../1/diagnostic-candidates", "", false},
		{"GET", "/api/tasks//1/diagnostic-candidates", "", false},
		{"GET", "/api/tasks/1/diagnostic-candidates", "{}", false},
		{"HEAD", "/api/tasks/1/diagnostic-candidates", "", false},
		{"POST", "/api/tasks/1/diagnostic-candidates", "", false},
	} {
		t.Run(test.method+" "+test.path+" "+test.body, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if _, valid := ProductCandidateRequestTask(r); valid != test.valid || !ProductCandidatePath(r) {
				t.Fatal("조회 경로 판정 불일치", valid)
			}
		})
	}
}

func TestProductLiveCandidatesOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	pg := &db.DB{DB: f.Installer}
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	source, err := pg.CreateTask("소유 원천 작업", "", nil, 3600, 30)
	if err != nil {
		t.Fatal(err)
	}
	task, err := pg.CreateTask("소유 진단", "", nil, 3600, 30)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO task_relations(task_id, source_task_id) VALUES($1,$2)`, task.ID, source.ID)
	taskRef := strconv.FormatInt(task.ID, 10)
	taskPath := "/api/tasks/" + taskRef + "/diagnostic-candidates"
	job, prepEndpoint, backend, journal := activeCandidateJob(t, taskRef)
	executions, inspections := backend.executions.Load(), backend.inspections.Load()
	endpoint := &candidateTestEndpoint{reportFixtureEndpoint: prepEndpoint}
	endpoint.revision.Store(3)
	run := "run-" + job.ID
	store := pg.Exploration(task.ExplorationID)
	sourceStore := pg.Exploration(source.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "합성 후보 의도"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	sourceIntent, err := sourceStore.AddIntent(map[string]any{"summary": "원천 후보 의도"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	const private = "fixture-private-N3Z6"
	addWithOutput := func(store *db.ExplorationStore, task, intent int64, run, call, output string) int64 {
		t.Helper()
		evidence := fmt.Sprintf(`{"trusted":{"classification":"result_unknown","execution":{"scope":{"TaskRef":%q,"RunRef":%q},"intent_id":%d},"call":{"call_ref":%q,"intent_id":%d}},"token":%q,"output":%q}`,
			strconv.FormatInt(task, 10), run, intent, call, intent, private, output)
		id, err := store.RecordDiagnosticCandidate(t.Context(), db.DiagnosticCandidate{
			TaskID: task, IntentID: intent, RunRef: run, CallRef: call, VulnClass: "Authorization: Bearer " + private,
			Name: "https://owned.test/?session=" + private, Summary: "password=" + private, Remediation: "Cookie: sid=" + private,
			Classification: "result_unknown", Verification: "manual_review_required", EvidenceVersion: 1,
			Evidence: evidence, EvidenceDigest: db.DiagnosticEvidenceDigest(evidence), ObservedAt: time.Now().UTC(),
			SuggestedSeverity: private, SeveritySource: db.WorkerEstimate, SeverityRationale: private,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add := func(store *db.ExplorationStore, task, intent int64, run, call string) int64 {
		t.Helper()
		return addWithOutput(store, task, intent, run, call, "")
	}
	add(sourceStore, source.ID, sourceIntent, run, "source-only")
	add(store, task.ID, intent, "other-run", "other-run")
	exec(`INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES($1,'finding','confirmed','test',$2)`,
		task.ExplorationID, `{"name":"ordinary-finding"}`)
	hash, err := bcrypt.GenerateFromPassword([]byte("owned-candidate-fixture"), bcrypt.MinCost)
	if err != nil || pg.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("인증 fixture 실패")
	}
	keys, data := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keys, jwtKeyFilename), []byte("synthetic-candidate-read-key-material"), 0600) != nil {
		t.Fatal("키 fixture 실패")
	}
	f.Runtime.SetMaxOpenConns(1)
	if _, err := f.Runtime.Exec(`SET default_transaction_read_only=on`); err != nil {
		t.Fatal(err)
	}
	runtimeServer, _, err := newDiagnosisManagementAuthentication(t.Context(), f.Runtime, keys, data)
	if err != nil {
		t.Fatal(err)
	}
	var scopeStatus atomic.Int64
	var attempts atomic.Int64
	var hookMu sync.Mutex
	var hook func(http.ResponseWriter, *http.Request, *httptest.ResponseRecorder)
	setHook := func(fn func(http.ResponseWriter, *http.Request, *httptest.ResponseRecorder)) {
		hookMu.Lock()
		defer hookMu.Unlock()
		hook = fn
	}
	reader := runtimeServer.productCandidateHandler(taskRef, task.ExplorationID, run, job, 3, func(context.Context) int { return 0 }, func(context.Context) int {
		return int(scopeStatus.Load())
	})
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get(productCandidateProof) != candidateProof(job, task.ExplorationID, 3) {
			t.Error("중계 요청에 사용자 헤더가 섞였습니다")
		}
		result := httptest.NewRecorder()
		reader.ServeHTTP(result, r)
		hookMu.Lock()
		fn := hook
		hookMu.Unlock()
		if fn != nil {
			fn(w, r, result)
			return
		}
		copyCandidateResult(w, result)
	}))
	defer runtime.Close()
	product, err := NewProductDiagnosisHandler(t.Context(), f.Runtime, keys, data, endpoint, runtime.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	product.ServeHTTP(login, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"ABC2LAB","password":"owned-candidate-fixture"}`)))
	var session struct{ Token string }
	if json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("제품 로그인 실패")
	}
	request := func(ctx context.Context, method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Cookie", "sid="+private)
		r.Header.Set(productCandidateProof, private)
		w := httptest.NewRecorder()
		product.ServeHTTP(w, r)
		return w
	}
	check := func(method, path, token string, status int) productCandidatePage {
		t.Helper()
		w := request(t.Context(), method, path, token)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get(productCandidateProof) != "" ||
			strings.Contains(w.Body.String(), private) {
			t.Fatalf("제품 후보 조회 %s %s 응답 %d: %s", method, path, w.Code, w.Body)
		}
		var page productCandidatePage
		if status == 200 && (strictJSON(w.Body.Bytes(), &page) != nil || !page.valid(taskRef)) {
			t.Fatalf("잘못된 제품 후보 DTO: %s", w.Body)
		}
		if status != 200 && strings.Contains(w.Body.String(), "items") {
			t.Fatal("오류 응답에 후보가 포함됐습니다")
		}
		return page
	}
	token := session.Token
	t.Run("empty-with-source-other-run-and-artEx-finding", func(t *testing.T) {
		page := check("GET", taskPath, token, 200)
		if *page.Total != 0 {
			t.Fatal("다른 출처의 데이터 혼입")
		}
	})
	first := add(store, task.ID, intent, run, "current")
	t.Run("stored-candidate-projection", func(t *testing.T) {
		page := check("GET", taskPath, token, 200)
		if *page.Total != 1 || page.Items[0].ID != strconv.FormatInt(first, 10) || page.Items[0].SuggestedSeverity != "" {
			t.Fatal("실제 저장 ID 또는 등급 가림 불일치")
		}
	})
	t.Run("runtime-bearer-authentication", func(t *testing.T) {
		trace := &candidateReadTrace{}
		pool := tracedCandidateRuntimePool(t, f.Runtime, trace)
		auth, _, err := newDiagnosisManagementAuthentication(t.Context(), pool, keys, data)
		if err != nil {
			t.Fatal(err)
		}
		live := func(context.Context) int { return 0 }
		handler := auth.productCandidateHandler(taskRef, task.ExplorationID, run, job, 3, live, live)
		proof := candidateProof(job, task.ExplorationID, 3)
		read := func(t *testing.T, bearer string) *httptest.ResponseRecorder {
			t.Helper()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, taskPath, nil)
			req.Header.Set(productCandidateProof, proof)
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			return out
		}
		assertUnauthorized := func(t *testing.T, out *httptest.ResponseRecorder) {
			t.Helper()
			if out.Code != http.StatusUnauthorized || out.Header().Get("Cache-Control") != "no-store" ||
				out.Header().Get(productCandidateProof) != "" || strings.Contains(out.Body.String(), `"items"`) {
				t.Fatalf("런타임 인증 거부 또는 후보 비공개 실패: HTTP %d", out.Code)
			}
		}
		sign := func(t *testing.T, key []byte, expires time.Time) string {
			t.Helper()
			value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
				Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(expires),
			}).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
		out := read(t, token)
		var page productCandidatePage
		if out.Code != http.StatusOK || out.Header().Get(productCandidateProof) != proof ||
			strictJSON(out.Body.Bytes(), &page) != nil || len(page.Items) != 1 || trace.transactions != 1 {
			t.Fatalf("유효 Bearer 제어 요청 실패: HTTP %d, 후보 트랜잭션 %d", out.Code, trace.transactions)
		}
		now := time.Now()
		for _, tc := range []struct{ name, token string }{
			{"missing-bearer", ""},
			{"wrong-signature", sign(t, []byte("unrelated-synthetic-signing-key"), now.Add(time.Hour))},
			{"expired-bearer", sign(t, auth.signingKey(string(hash)), now.Add(-time.Hour))},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := trace.transactions
				assertUnauthorized(t, read(t, tc.token))
				if trace.transactions != before {
					t.Fatal("인증 거부된 요청이 후보 조회 트랜잭션을 시작했습니다")
				}
			})
		}
		t.Run("password-revoked-after-candidate-read", func(t *testing.T) {
			replacement, err := bcrypt.GenerateFromPassword([]byte("replaced-candidate-fixture"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := pg.SetSetting(authPassKey, string(hash)); err != nil {
					t.Error(err)
				}
			})
			revoked := false
			var revokeErr error
			trace.afterCommit = func() {
				revoked = true
				revokeErr = pg.SetSetting(authPassKey, string(replacement))
			}
			defer func() { trace.afterCommit = nil }()
			out := read(t, token)
			if !revoked || revokeErr != nil {
				t.Fatalf("후보 조회 완료 직후 비밀번호 교체 실패: %v", revokeErr)
			}
			assertUnauthorized(t, out)
		})
	})
	t.Run("authorization-and-shape-before-forwarding", func(t *testing.T) {
		count := attempts.Load()
		check("GET", taskPath, "", 401)
		check("GET", "/api/tasks/"+strconv.FormatInt(source.ID, 10)+"/diagnostic-candidates", token, 403)
		check("POST", taskPath, token, 400)
		check("GET", taskPath+"?task=2", token, 400)
		check("GET", "/api/tasks/%30"+taskRef+"/diagnostic-candidates", token, 400)
		check("GET", taskPath+"/", token, 400)
		legacy, err := NewURLDiagnosisManagementHandler(t.Context(), f.Runtime, keys, data, endpoint, runtime.URL)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", taskPath, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		legacy.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatal("제품 외 URL 진단 입구에 후보 GET 공개", out.Code)
		}
		if attempts.Load() != count {
			t.Fatal("거부 요청을 실행 조회로 중계했습니다")
		}
	})
	t.Run("ownership-recheck-at-public-response", func(t *testing.T) {
		setHook(func(w http.ResponseWriter, _ *http.Request, result *httptest.ResponseRecorder) {
			endpoint.deny.Store(true)
			copyCandidateResult(w, result)
		})
		defer setHook(nil)
		defer endpoint.deny.Store(false)
		check("GET", taskPath, token, 403)
	})
	t.Run("revoked-live-scope-before-response", func(t *testing.T) {
		scopeStatus.Store(403)
		defer scopeStatus.Store(0)
		check("GET", taskPath, token, 403)
	})
	t.Run("target-revoked-after-runtime-read", func(t *testing.T) {
		setHook(func(w http.ResponseWriter, _ *http.Request, result *httptest.ResponseRecorder) {
			endpoint.revoked.Store(true)
			copyCandidateResult(w, result)
		})
		defer setHook(nil)
		defer endpoint.revoked.Store(false)
		check("GET", taskPath, token, 403)
	})
	t.Run("target-revision-changed-after-runtime-read", func(t *testing.T) {
		setHook(func(w http.ResponseWriter, _ *http.Request, result *httptest.ResponseRecorder) {
			endpoint.revision.Store(4)
			copyCandidateResult(w, result)
		})
		defer setHook(nil)
		defer endpoint.revision.Store(3)
		check("GET", taskPath, token, 403)
	})
	t.Run("closed-live-scope-before-response", func(t *testing.T) {
		scopeStatus.Store(409)
		defer scopeStatus.Store(0)
		check("GET", taskPath, token, 409)
	})
	t.Run("deleted-task-during-read", func(t *testing.T) {
		setHook(func(w http.ResponseWriter, _ *http.Request, result *httptest.ResponseRecorder) {
			if _, err := pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task.ID); err != nil {
				t.Error(err)
			}
			copyCandidateResult(w, result)
		})
		defer setHook(nil)
		defer exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, task.ID)
		check("GET", taskPath, token, 404)
	})
	t.Run("upstream-projection-and-size", func(t *testing.T) {
		for _, override := range []func(*httptest.ResponseRecorder){
			func(r *httptest.ResponseRecorder) { r.Header().Set(productCandidateProof, private) },
			func(r *httptest.ResponseRecorder) { r.Body.WriteString(private) },
			func(r *httptest.ResponseRecorder) { r.Body.WriteString(strings.Repeat("x", productCandidateBytes)) },
		} {
			setHook(func(w http.ResponseWriter, _ *http.Request, result *httptest.ResponseRecorder) {
				override(result)
				copyCandidateResult(w, result)
			})
			check("GET", taskPath, token, 502)
		}
		setHook(nil)
	})
	t.Run("corrupt-stored-row", func(t *testing.T) {
		exec(`UPDATE exploration_nodes SET payload=payload #- '{diagnostic_candidate_v1,evidence_digest}' WHERE id=$1`, first)
		check("GET", taskPath, token, 503)
		exec(`DELETE FROM exploration_nodes WHERE id=$1`, first)
		add(store, task.ID, intent, run, "current-restored")
	})
	t.Run("missing-intent-edge", func(t *testing.T) {
		id := add(store, task.ID, intent, run, "orphan")
		defer exec(`DELETE FROM exploration_nodes WHERE id=$1`, id)
		exec(`DELETE FROM exploration_edges WHERE dst_id=$1 AND rel='yields'`, id)
		check("GET", taskPath, token, 503)
	})
	t.Run("db-permission-failure", func(t *testing.T) {
		var role string
		if f.Runtime.QueryRow(`SELECT current_user`).Scan(&role) != nil {
			t.Fatal("DB 시험 역할 확인 실패")
		}
		exec(`REVOKE SELECT ON exploration_nodes FROM "` + role + `"`)
		defer exec(`GRANT SELECT ON exploration_nodes TO "` + role + `"`)
		check("GET", taskPath, token, 503)
	})
	t.Run("unavailable-preparation", func(t *testing.T) {
		endpoint.unavailable.Store(true)
		defer endpoint.unavailable.Store(false)
		check("GET", taskPath, token, 503)
	})
	t.Run("bounded-page", func(t *testing.T) {
		last := int64(0)
		for i := range db.LiveDiagnosticCandidateLimit {
			last = add(store, task.ID, intent, run, fmt.Sprintf("over-limit-%d", i))
		}
		page := check("GET", taskPath, token, 200)
		if !*page.Truncated || page.Total != nil || *page.Returned != 200 || page.Items[0].ID != strconv.FormatInt(last, 10) {
			t.Fatal("페이지 건수 또는 순서 불일치")
		}
	})
	t.Run("byte-budget-truncates-public-page", func(t *testing.T) {
		const count = 72
		output := strings.Repeat("x", 255<<10)
		ids := make([]int64, count)
		for i := range ids {
			id := addWithOutput(store, task.ID, intent, run, fmt.Sprintf("large-output-%d", i), output)
			ids[i] = id
			t.Cleanup(func() {
				if _, err := pg.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, id); err != nil {
					t.Error("합성 대형 후보 정리 실패")
				}
			})
		}
		page := check("GET", taskPath, token, 200)
		if !*page.Truncated || page.Total != nil || *page.Returned == 0 || *page.Returned >= count {
			t.Fatal("바이트 상한의 짧은 페이지 계약 위반")
		}
		for i, item := range page.Items {
			if item.ID != strconv.FormatInt(ids[count-1-i], 10) {
				t.Fatal("바이트 상한이 최신 연속 후보의 순서를 바꿈")
			}
		}
	})
	t.Run("concurrent-polling-and-cancellation-without-side-effects", func(t *testing.T) {
		before, receipt := reportDatabaseDigest(t, f.Installer), reportJournalDigest(t, journal)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if result := request(t.Context(), "GET", taskPath, token); result.Code != 200 {
					t.Error("동시 조회 실패", result.Code)
				}
			})
		}
		wg.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if result := request(ctx, "GET", taskPath, token); result.Code != 503 {
			t.Fatal("취소를 성공으로 반환", result.Code)
		}
		if !reflect.DeepEqual(before, reportDatabaseDigest(t, f.Installer)) {
			t.Fatal("조회가 PostgreSQL 저장 상태를 변경했습니다")
		}
		if receipt != reportJournalDigest(t, journal) {
			t.Fatal("조회가 준비 서비스의 영속 기록을 변경했습니다")
		}
		if backend.executions.Load() != executions || backend.inspections.Load() != inspections {
			t.Fatal("조회가 준비·실행을 추가로 호출했습니다")
		}
		endpoint.mu.Lock()
		defer endpoint.mu.Unlock()
		for _, command := range endpoint.commands {
			if command != diagnosisprep.AuthorizeTask {
				t.Error("조회에서 다른 준비 명령을 호출했습니다", command)
			}
		}
	})
}

// Tracing the completed read transaction gives the fixture a deterministic
// password change after the rows are read and before the response auth check.
type candidateReadTrace struct {
	transactions int
	afterCommit  func()
}

func (*candidateReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (t *candidateReadTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil {
		return
	}
	switch data.CommandTag.String() {
	case "BEGIN":
		t.transactions++
	case "COMMIT":
		if t.afterCommit != nil {
			t.afterCommit()
		}
	}
}

func tracedCandidateRuntimePool(t *testing.T, runtime *sql.DB, trace *candidateReadTrace) *sql.DB {
	t.Helper()
	conn, err := runtime.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var config *pgx.ConnConfig
	if err := conn.Raw(func(driverConn any) error {
		config = driverConn.(*stdlib.Conn).Conn().Config()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["default_transaction_read_only"] = "on"
	config.Tracer = trace
	pool := stdlib.OpenDB(*config)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func copyCandidateResult(w http.ResponseWriter, result *httptest.ResponseRecorder) {
	for key, values := range result.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(result.Code)
	_, _ = w.Write(result.Body.Bytes())
}

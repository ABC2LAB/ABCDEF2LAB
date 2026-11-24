//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// 설치·완료 receipt만 준비하는 합성 backend다. 보고서 GET 전에 종료·재개방하므로 실행 복구도 검출한다.
type reportFixtureBackend struct {
	expires     time.Time
	task        string
	executions  atomic.Int64
	inspections atomic.Int64
}

func (b *reportFixtureBackend) Profiles(context.Context, string) ([]diagnosisprep.Profile, error) {
	return []diagnosisprep.Profile{{ID: "local", Label: "소유 합성 모델", ModelProfileRef: "local", ModelLabel: "합성"}}, nil
}
func (b *reportFixtureBackend) Inspect(context.Context, string, diagnosisprep.Input) (diagnosisprep.Inspection, error) {
	b.inspections.Add(1)
	budget := diagnosisprep.Budget{Requests: 4, ModelRequests: 3, InputTokens: 30, OutputTokens: 20, DurationSeconds: 60}
	return diagnosisprep.Inspection{Scope: &diagnosisprep.Scope{TargetRef: "owned", ScopeRef: "owned-read", Origin: "https://owned.test", PathPrefixes: []string{"/"}, Methods: []string{"GET"}, QueryPolicy: "any", BodyPolicy: "bounded", MaxBodyBytes: 1024, ApprovalMode: "installed_delegation", AttestationRef: "owned", Budget: budget, ExpiresAt: b.expires}, InstallationDigest: strings.Repeat("a", 64), DelegationRef: "owned", MaxRuns: 3, AggregateBudget: diagnosisprep.Budget{Requests: 12, ModelRequests: 9, InputTokens: 90, OutputTokens: 60, DurationSeconds: 180}}, nil
}
func (b *reportFixtureBackend) Execute(_ context.Context, _ diagnosisprep.Execution, p diagnosisprep.Publish) error {
	b.executions.Add(1)
	u := diagnosisprep.Update{State: diagnosisprep.Preparing, Phase: diagnosisprep.TaskCreation, PreparationStarted: true, AuthorityState: diagnosisprep.AuthorityNotIssued}
	if err := p(u); err != nil {
		return err
	}
	u.TaskID = b.task
	if err := p(u); err != nil {
		return err
	}
	u.Phase = diagnosisprep.Planning
	if err := p(u); err != nil {
		return err
	}
	u.Phase = diagnosisprep.Authority
	if err := p(u); err != nil {
		return err
	}
	u.AuthorityState = diagnosisprep.AuthorityIssued
	u.Phase = diagnosisprep.Gateway
	if err := p(u); err != nil {
		return err
	}
	u.AuthorityState = diagnosisprep.AuthorityConsumed
	if err := p(u); err != nil {
		return err
	}
	u.Started = true
	u.State = diagnosisprep.Running
	u.Phase = diagnosisprep.ExecutionPhase
	if err := p(u); err != nil {
		return err
	}
	u.State = diagnosisprep.Completed
	u.Phase = diagnosisprep.Report
	u.DrainComplete = true
	u.ResultReady = true
	u.Result = &diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1}}
	return p(u)
}

type reportFixtureEndpoint struct {
	inner       diagnosisprep.Endpoint
	deny        atomic.Bool
	unavailable atomic.Bool
	mu          sync.Mutex
	commands    []diagnosisprep.Command
}

func (e *reportFixtureEndpoint) Call(ctx context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.mu.Lock()
	e.commands = append(e.commands, r.Command)
	inner := e.inner
	e.mu.Unlock()
	if e.unavailable.Load() {
		return diagnosisprep.Response{}, fmt.Errorf("synthetic preparation unavailable")
	}
	if e.deny.Load() && r.Command == diagnosisprep.AuthorizeTask {
		no := false
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Authorized: &no}, nil
	}
	return inner.Call(ctx, r)
}

type reportJoinedAuthorities []diagnosisprep.Endpoint

func (all reportJoinedAuthorities) Call(ctx context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	for _, next := range all {
		response, err := next.Call(ctx, r)
		if err != nil {
			return response, err
		}
		if response.Authorized != nil && *response.Authorized {
			return response, nil
		}
	}
	no := false
	return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Authorized: &no}, nil
}

func reportFixtureService(t *testing.T, task int64) (*reportFixtureEndpoint, *reportFixtureBackend, string) {
	t.Helper()
	root := firstDiagnosisPrivateTemp(t)
	backend := &reportFixtureBackend{task: strconv.FormatInt(task, 10), expires: time.Now().UTC().Add(time.Hour)}
	s, err := diagnosisprep.New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	call := func(r diagnosisprep.Request) diagnosisprep.Response {
		t.Helper()
		r.Version = diagnosisprep.Version
		r.Principal = "ARTEX"
		r.RequestID = "owned"
		out, e := s.Call(t.Context(), r)
		if e != nil || out.Error != "" || diagnosisprep.ValidateResponse(r, out) != nil {
			t.Fatal("보호 Job fixture 실패", e, out.Error)
		}
		return out
	}
	j := call(diagnosisprep.Request{Command: diagnosisprep.Create, IdempotencyKey: "create", Input: &diagnosisprep.Input{TargetURL: "https://owned.test/", ProfileID: "local"}}).Job
	call(diagnosisprep.Request{Command: diagnosisprep.Start, JobID: j.ID, ExpectedRevision: j.Revision, IdempotencyKey: "start"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		j = call(diagnosisprep.Request{Command: diagnosisprep.Status, JobID: j.ID}).Job
		if j.State == diagnosisprep.Completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("합성 완료 receipt 대기 실패", j.State)
		}
		time.Sleep(time.Millisecond)
	}
	if s.Shutdown(t.Context()) != nil {
		t.Fatal("보호 Job fixture 종료 실패")
	}
	s, err = diagnosisprep.New(t.Context(), root, backend)
	if err != nil {
		t.Fatal("영속 Job 재개방 실패", err)
	}
	t.Cleanup(func() {
		if s.Shutdown(context.Background()) != nil {
			t.Error("영속 Job 마감 실패")
		}
	})
	return &reportFixtureEndpoint{inner: s}, backend, root
}

func reportDatabaseDigest(t *testing.T, pool *sql.DB) map[string]string {
	t.Helper()
	rows, err := pool.Query(`SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) != nil {
			t.Fatal("표 목록 실패")
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string]string{}
	for _, n := range names {
		var d string
		if pool.QueryRow(`SELECT md5(COALESCE(string_agg(row_to_json(t)::text,'' ORDER BY row_to_json(t)::text),'')) FROM `+pgx.Identifier{n}.Sanitize()+` t`).Scan(&d) != nil {
			t.Fatal("표 지문 실패", n)
		}
		out[n] = d
	}
	return out
}

func reportJournalDigest(t *testing.T, root string) [32]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		h.Write(raw)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func TestReportGraphReadJWTClaimsOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-report-claims-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		m:      &Manager{pg: &db.DB{DB: f.Runtime}},
		jwtKey: []byte("synthetic-report-claims-signing-key"),
	}
	expires := jwt.NewNumericDate(time.Now().Add(time.Hour))
	cases := []struct {
		name       string
		claims     jwt.RegisteredClaims
		wantStatus int
		wantCalls  int
	}{
		{"valid", jwt.RegisteredClaims{Subject: "ARTEX", ExpiresAt: expires}, http.StatusNoContent, 1},
		{"wrong-subject", jwt.RegisteredClaims{Subject: "synthetic-other-subject", ExpiresAt: expires}, http.StatusUnauthorized, 0},
		{"missing-expiration", jwt.RegisteredClaims{Subject: "ARTEX"}, http.StatusUnauthorized, 0},
	}
	// Enter the report branch directly so management authentication cannot mask a regression.
	for _, leaf := range []string{"coverage-graph", "access-map"} {
		for _, tc := range cases {
			t.Run(leaf+"/"+tc.name, func(t *testing.T) {
				token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, tc.claims).SignedString(s.signingKey(string(hash)))
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/tasks/42/"+leaf, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				calls := 0
				handler := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					w.WriteHeader(http.StatusNoContent)
				}))
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, req)
				if out.Code != tc.wantStatus || calls != tc.wantCalls {
					t.Fatalf("status=%d, handler calls=%d; want status=%d, handler calls=%d", out.Code, calls, tc.wantStatus, tc.wantCalls)
				}
				if out.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("보고서 인증 응답에 no-store 헤더가 없음")
				}
				if tc.wantStatus == http.StatusUnauthorized {
					var body struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body.Error != authErrTokenInvalid {
						t.Fatalf("error=%q, want %q", body.Error, authErrTokenInvalid)
					}
				}
			})
		}
	}
}

func TestProductReportGraphsOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	pg := &db.DB{DB: f.Installer}
	m := newAccessMapTestFixture(t, pg)
	hash, err := bcrypt.GenerateFromPassword([]byte("owned-report-fixture"), bcrypt.MinCost)
	if err != nil || pg.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("인증 설치 실패")
	}
	keys, data := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keys, jwtKeyFilename), []byte("synthetic-report-read-jwt-key-material"), 0600) != nil {
		t.Fatal("JWT fixture 실패")
	}
	endpoint, backend, journal := reportFixtureService(t, m.b.TaskID)
	readBackends := []*reportFixtureBackend{backend}
	readJournals := []string{journal}
	// 같은 역할의 조회가 실수로 쓰기를 시도하면 PostgreSQL 자체에서 거부한다.
	f.Runtime.SetMaxOpenConns(1)
	if _, err := f.Runtime.Exec(`SET default_transaction_read_only=on`); err != nil {
		t.Fatal(err)
	}
	reportHandler, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keys, data, m.b.TaskID)
	if err != nil {
		t.Fatal("실제 저장 보고서 생성자 실패", err)
	}
	var targetCalls, modelCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(500) }))
	defer target.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelCalls.Add(1); w.WriteHeader(500) }))
	defer model.Close()
	profileID, err := pg.SaveProfile(&db.LLMProfile{Name: "report-only-owned-model", Format: "openai", Model: "local:report-fixture", BaseURL: model.URL + "/v1", Streaming: false, ContextWindowK: 12})
	if err != nil || pg.SetActiveProfile(profileID) != nil {
		t.Fatal("모델 무호출 계측 fixture 설정 실패")
	}
	var callbackMu sync.Mutex
	var before, after func()
	setBefore := func(fn func()) { callbackMu.Lock(); before = fn; callbackMu.Unlock() }
	setAfter := func(fn func()) { callbackMu.Lock(); after = fn; callbackMu.Unlock() }
	var runtimeCalls atomic.Int64
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeCalls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Secret") != "" || len(r.Header.Values(reportReadScopeHeader)) != 1 {
			t.Error("요청 헤더 결속 실패")
		}
		callbackMu.Lock()
		first := before
		last := after
		callbackMu.Unlock()
		if first != nil {
			first()
		}
		recorded := httptest.NewRecorder()
		reportHandler.ServeHTTP(recorded, r)
		if last != nil {
			last()
		}
		for k, v := range recorded.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorded.Code)
		w.Write(recorded.Body.Bytes())
	}))
	defer runtime.Close()
	product, err := NewProductDiagnosisHandler(t.Context(), f.Runtime, keys, data, endpoint, runtime.URL, nil, nil)
	if err != nil {
		t.Fatal("실제 제품 생성자 실패", err)
	}
	api := httptest.NewServer(product)
	defer api.Close()
	resp, err := http.Post(api.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"ABC2LAB","password":"owned-report-fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	var session struct{ Token string }
	if json.NewDecoder(resp.Body).Decode(&session) != nil || session.Token == "" {
		t.Fatal("실제 제품 로그인 실패")
	}
	resp.Body.Close()
	taskPath := "/api/tasks/" + m.b.Scope.TaskRef
	requestAt := func(apiURL, method, path, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, apiURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Cookie", "forbidden=secret")
		req.Header.Set("X-Secret", "secret")
		req.Header.Set(reportReadScopeHeader, strings.Repeat("f", 64))
		client := *api.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Error("응답 캐시 금지 누락")
		}
		if res.Header.Get(reportReadScopeHeader) != "" {
			t.Error("내부 결속 헤더 노출")
		}
		return res.StatusCode, raw
	}
	request := func(method, path, token string) (int, []byte) { return requestAt(api.URL, method, path, token) }
	assertReadOnly := func(t *testing.T, run func()) {
		t.Helper()
		before := reportDatabaseDigest(t, f.Installer)
		executionCounts, inspectionCounts := []int64{}, []int64{}
		journalDigests := [][32]byte{}
		for _, b := range readBackends {
			executionCounts = append(executionCounts, b.executions.Load())
			inspectionCounts = append(inspectionCounts, b.inspections.Load())
		}
		for _, root := range readJournals {
			journalDigests = append(journalDigests, reportJournalDigest(t, root))
		}
		run()
		if !reflect.DeepEqual(before, reportDatabaseDigest(t, f.Installer)) {
			t.Fatal("조회가 fixture DB를 변경함")
		}
		for i, root := range readJournals {
			if journalDigests[i] != reportJournalDigest(t, root) {
				t.Fatal("조회가 영속 Job을 변경함")
			}
		}
		for i, b := range readBackends {
			if executionCounts[i] != b.executions.Load() || inspectionCounts[i] != b.inspections.Load() {
				t.Fatal("조회가 backend 실행·Inspect를 호출함")
			}
		}
		if targetCalls.Load() != 0 || modelCalls.Load() != 0 {
			t.Fatal("보고서 조회가 모델·대상에 접근함")
		}
		endpoint.mu.Lock()
		defer endpoint.mu.Unlock()
		for _, c := range endpoint.commands {
			if c != diagnosisprep.AuthorizeTask {
				t.Fatal("조회 밖 준비 명령", c)
			}
		}
	}
	t.Run("empty-graph", func(t *testing.T) {
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/coverage-graph", session.Token)
			if code != 200 || string(raw) != "{\"nodes\":[],\"edges\":[]}\n" {
				t.Fatalf("정상 빈 그래프 실패 %d %s", code, raw)
			}
		})
	})
	t.Run("registered-no-verdict", func(t *testing.T) {
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/access-map", session.Token)
			var out accessMapResponse
			if code != 200 || json.Unmarshal(raw, &out) != nil || accessMapFind(t, out, "reader").ProjectionError == nil || *accessMapFind(t, out, "reader").ProjectionError != "observation_unavailable" {
				t.Fatal("관측 없음과 빈 격자를 혼동", code)
			}
		})
	})
	m.save(t)
	t.Run("registered-verdict-and-repeat", func(t *testing.T) {
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/access-map", session.Token)
			code2, again := request("GET", taskPath+"/access-map", session.Token)
			var out accessMapResponse
			if code != 200 || code2 != 200 || string(raw) != string(again) || json.Unmarshal(raw, &out) != nil || accessMapFind(t, out, "reader").State != "confirmed" {
				t.Fatal("저장 E③ 지도 대조 실패", code)
			}
		})
	})
	// setup에는 합성 자산만 넣는다. URL 경로·query·제목의 표식이 공개 DTO에 나가면 실패한다.
	var serviceID, endpointID, foreignID int64
	insert := func(kind, url, domain, root string, task int64) int64 {
		t.Helper()
		var id int64
		if pg.QueryRow(`INSERT INTO assets(type,url,domain,root_domain,page_title,task_ids) VALUES($1,$2,$3,$4,'private-token',$5::bigint[]) RETURNING id`, kind, url, domain, root, fmt.Sprintf("{%d}", task)).Scan(&id) != nil {
			t.Fatal("자산 fixture 실패")
		}
		return id
	}
	serviceID = insert("service", target.URL+"/private-token?token=private-token", "", "", m.b.TaskID)
	endpointID = insert("endpoint", target.URL+"/private-token?token=private-token", "", "", m.b.TaskID)
	if _, err := pg.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2),($1,$3)`, m.b.IntentID, serviceID, endpointID); err != nil {
		t.Fatal(err)
	}
	foreign, err := pg.CreateTask("foreign fixture", "owned fixture only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	foreignID = insert("endpoint", "https://foreign.test/private-token", "", "", foreign.ID)
	t.Run("assets-and-edges", func(t *testing.T) {
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/coverage-graph", session.Token)
			var g coverageGraphPublic
			if code != 200 || json.Unmarshal(raw, &g) != nil || len(g.Nodes) != 2 || len(g.Edges) != 1 || strings.Contains(string(raw), "private-token") {
				t.Fatal("실제 그래프 조회 실패", code, string(raw))
			}
			for _, n := range g.Nodes {
				if (n.AssetID == endpointID && n.DisplayPath != "/{id}") || (n.AssetID != endpointID && n.DisplayPath != "") || n.URL != target.URL || n.Label != target.URL {
					t.Fatal("엔드포인트의 표시 경로 또는 기존 origin 계약 불일치")
				}
				if n.AssetID == foreignID {
					t.Fatal("다른 Job 자산 노출")
				}
			}
		})
	})
	// KG 조회 v1(kg-query-v1 §9 Q15): 실제 제품 HTTP → 관리 중계 → 저장 보고서 서버 → 읽기 전용 runtime 역할.
	kgNodes := func(t *testing.T, raw []byte) (map[string]bool, []string) {
		t.Helper()
		var out struct {
			Version string `json:"version"`
			TaskID  string `json:"task_id"`
			Scope   struct {
				TaskIDs []string `json:"task_ids"`
			} `json:"scope"`
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		}
		if json.Unmarshal(raw, &out) != nil || out.Version != db.KGQueryVersion || out.TaskID != m.b.Scope.TaskRef {
			t.Fatal("KG v1 응답 형식", string(raw))
		}
		ids := map[string]bool{}
		for _, n := range out.Nodes {
			ids[n.ID] = true
		}
		return ids, out.Scope.TaskIDs
	}
	t.Run("kg-v1-assets", func(t *testing.T) {
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/coverage-graph?view=kg-v1", session.Token)
			if code != 200 {
				t.Fatal("제품 경로 KG v1 조회 실패", code, string(raw))
			}
			ids, tasks := kgNodes(t, raw)
			if !ids[fmt.Sprintf("a:%d", serviceID)] || !ids[fmt.Sprintf("a:%d", endpointID)] || ids[fmt.Sprintf("a:%d", foreignID)] || len(tasks) != 1 {
				t.Fatal("KG v1 자산 집합", string(raw))
			}
			if strings.Contains(string(raw), "private-token") || strings.Contains(string(raw), strings.TrimPrefix(target.URL, "http://")) || strings.Contains(string(raw), "display_path") {
				t.Fatal("KG v1 응답에 URL 원문")
			}
		})
	})
	// Q19: 상한 초과는 중계를 지나도 413 kg_graph_too_large(재조회 불가) 본문 그대로다 — 503·빈 그래프로 바뀌지 않는다.
	t.Run("kg-v1-too-large-through-bridge", func(t *testing.T) {
		if _, err := pg.Exec(`INSERT INTO assets(type,url,task_ids) SELECT 'endpoint','https://kg-limit.owned.test/p'||g,ARRAY[$1::bigint] FROM generate_series(1,$2) g`, m.b.TaskID, db.KGLimits.Nodes); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := pg.Exec(`DELETE FROM assets WHERE url LIKE 'https://kg-limit.owned.test/%'`); err != nil {
				t.Error(err)
			}
		}()
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/coverage-graph?view=kg-v1", session.Token)
			if code != 413 || string(raw) != `{"error":{"code":"kg_graph_too_large","retryable":false},"version":"abc2lab.kg-query/v1"}`+"\n" {
				t.Fatal("상한 초과 전달", code, string(raw))
			}
		})
	})
	foreignRoot := insert("root_domain", "", "shared.owned.test", "", foreign.ID)
	subdomain := insert("subdomain", "", "child.shared.owned.test", "shared.owned.test", m.b.TaskID)
	if _, err := pg.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2)`, m.b.IntentID, subdomain); err != nil {
		t.Fatal(err)
	}
	t.Run("connector-does-not-read-foreign-root-asset", func(t *testing.T) {
		assertReadOnly(t, func() {
			code, raw := request("GET", taskPath+"/coverage-graph", session.Token)
			var graph db.CoverageGraphData
			if code != 200 || json.Unmarshal(raw, &graph) != nil {
				t.Fatal("연결 노드 조회 실패", code)
			}
			root := false
			for _, n := range graph.Nodes {
				if n.AssetID == foreignRoot {
					t.Fatal("범위 밖 전역 루트 자산 PK 노출")
				}
				if n.Key == "r:shared.owned.test" {
					root = true
					if n.InScope || n.AssetID != 0 {
						t.Fatal("가상 연결 노드를 실제 자산으로 위장")
					}
				}
			}
			if !root {
				t.Fatal("기존 포함 관계 연결 누락")
			}
		})
	})
	t.Run("request-guards", func(t *testing.T) {
		assertReadOnly(t, func() {
			before := runtimeCalls.Load()
			for _, path := range []string{taskPath + "/coverage-graph?view=kg-v2", taskPath + "/coverage-graph?view=kg-v1&view=kg-v1", taskPath + "/access-map?view=kg-v1", taskPath + "/access-map?run_ref=old", taskPath + "/access-map?x=1&x=2", strings.Replace(taskPath, "/tasks/", "/tasks/0", 1) + "/coverage-graph", taskPath + "//coverage-graph", taskPath + "/%63overage-graph"} {
				if code, _ := request("GET", path, session.Token); code != 403 {
					t.Fatal("잘못된 경로 허용", path, code)
				}
			}
			for _, method := range []string{"HEAD", "POST", "PUT"} {
				for _, query := range []string{"", "?view=kg-v1"} {
					if code, _ := request(method, taskPath+"/coverage-graph"+query, session.Token); code != 403 {
						t.Fatal("GET 외 허용", query, code)
					}
				}
			}
			if code, _ := request("GET", taskPath+"/coverage-graph", ""); code != 401 {
				t.Fatal("무인증 허용", code)
			}
			if code, _ := request("GET", "/api/tasks/999999/coverage-graph", session.Token); code != 403 {
				t.Fatal("다른 Job 허용", code)
			}
			other, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "other-owner", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}).SignedString((&Server{jwtKey: []byte("synthetic-report-read-jwt-key-material")}).signingKey(string(hash)))
			if err != nil {
				t.Fatal(err)
			}
			if code, _ := request("GET", taskPath+"/coverage-graph", other); code != 401 {
				t.Fatal("다른 서명 주체 허용", code)
			}
			for _, duplicate := range []bool{false, true} {
				req, _ := http.NewRequest("GET", api.URL+taskPath+"/coverage-graph", nil)
				req.AddCookie(&http.Cookie{Name: "artex_token", Value: session.Token})
				if duplicate {
					req.Header.Add("Authorization", "Bearer "+session.Token)
					req.Header.Add("Authorization", "Bearer "+session.Token)
				}
				res, err := api.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != 401 {
					t.Fatal("쿠키 또는 중복 Authorization 허용", res.StatusCode)
				}
			}
			if runtimeCalls.Load() != before {
				t.Fatal("거절 요청이 내부 실행 서버에 도달")
			}
		})
	})
	t.Run("map-registration-changed-during-read", func(t *testing.T) {
		setAfter(func() {
			if _, err := m.store.invalidate(t.Context(), m.b.Scope.TaskRef); err != nil {
				t.Error(err)
			}
		})
		defer func() {
			setAfter(nil)
			if err := m.store.update(t.Context(), m.b.TaskID, func(b *accessMapBinding) error { *b = m.b; return nil }); err != nil {
				t.Error(err)
			}
		}()
		if code, raw := request("GET", taskPath+"/access-map", session.Token); code != 409 || strings.Contains(string(raw), "cells") {
			t.Fatal("과거 등록 지도 응답 노출", code)
		}
	})
	t.Run("current-registration-missing", func(t *testing.T) {
		if _, err := m.store.invalidate(t.Context(), m.b.Scope.TaskRef); err != nil {
			t.Fatal(err)
		}
		assertReadOnly(t, func() {
			if code, _ := request("GET", taskPath+"/access-map", session.Token); code != 409 {
				t.Fatal("미등록 scope를 빈 지도 합성", code)
			}
		})
	})
	var source, recursive int64
	for _, dst := range []*int64{&source, &recursive} {
		task, err := pg.CreateTask("owned source fixture", "synthetic only", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		*dst = task.ID
	}
	if _, err := pg.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2),($2,$3)`, m.b.TaskID, source, recursive); err != nil {
		t.Fatal(err)
	}
	t.Run("unauthorized-direct-source", func(t *testing.T) {
		assertReadOnly(t, func() {
			before := runtimeCalls.Load()
			for _, query := range []string{"", "?view=kg-v1"} {
				if code, _ := request("GET", taskPath+"/coverage-graph"+query, session.Token); code != 403 || runtimeCalls.Load() != before {
					t.Fatal("읽기 권한 없는 원천 노출", query, code)
				}
			}
		})
	})
	sourceAuthority, sourceBackend, sourceJournal := reportFixtureService(t, source)
	readBackends = append(readBackends, sourceBackend)
	readJournals = append(readJournals, sourceJournal)
	endpoint.mu.Lock()
	endpoint.inner = reportJoinedAuthorities{endpoint.inner, sourceAuthority}
	endpoint.mu.Unlock()
	addSourceAsset := func(task int64, host string) int64 {
		t.Helper()
		var exp, node int64
		if pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task).Scan(&exp) != nil {
			t.Fatal("원천 task 조회 실패")
		}
		if pg.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,state,payload) VALUES($1,'fact','confirmed','{}') RETURNING id`, exp).Scan(&node) != nil {
			t.Fatal("원천 fact 준비 실패")
		}
		id := insert("endpoint", "https://"+host+"/private-token", host, "", task)
		if _, err := pg.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2)`, node, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sourceAsset := addSourceAsset(source, "source.owned.test")
	recursiveAsset := addSourceAsset(recursive, "recursive.owned.test")
	t.Run("authorized-direct-source-not-recursive", func(t *testing.T) {
		assertReadOnly(t, func() {
			prior := sourceBackend.executions.Load()
			code, raw := request("GET", taskPath+"/coverage-graph", session.Token)
			var graph db.CoverageGraphData
			if code != 200 || json.Unmarshal(raw, &graph) != nil {
				t.Fatal("승인 원천 조회 실패", code, string(raw))
			}
			found := false
			for _, n := range graph.Nodes {
				if n.AssetID == sourceAsset {
					found = true
					if !n.Tested {
						t.Fatal("원천 fact 표시 유실")
					}
				}
				if n.AssetID == recursiveAsset {
					t.Fatal("재귀 원천 노출")
				}
			}
			if !found || sourceBackend.executions.Load() != prior {
				t.Fatal("직접 원천 조회 누락 또는 실행")
			}
			code, raw = request("GET", taskPath+"/coverage-graph?view=kg-v1", session.Token)
			if code != 200 {
				t.Fatal("KG v1 승인 원천 조회 실패", code, string(raw))
			}
			ids, tasks := kgNodes(t, raw)
			if !ids[fmt.Sprintf("a:%d", sourceAsset)] || ids[fmt.Sprintf("a:%d", recursiveAsset)] || len(tasks) != 2 || tasks[1] != strconv.FormatInt(source, 10) {
				t.Fatal("KG v1 직접 원천 한정", string(raw))
			}
		})
	})
	t.Run("scope-ABA-during-upstream-read", func(t *testing.T) {
		if _, err := pg.Exec(`DELETE FROM task_relations WHERE task_id=$1`, m.b.TaskID); err != nil {
			t.Fatal(err)
		}
		setBefore(func() {
			if _, err := pg.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, m.b.TaskID, recursive); err != nil {
				t.Error(err)
			}
		})
		setAfter(func() {
			if _, err := pg.Exec(`DELETE FROM task_relations WHERE task_id=$1`, m.b.TaskID); err != nil {
				t.Error(err)
			}
		})
		defer func() { setBefore(nil); setAfter(nil) }()
		if code, raw := request("GET", taskPath+"/coverage-graph", session.Token); code != 409 || strings.Contains(string(raw), "nodes") {
			t.Fatal("조회 도중 임시 원천을 섞음", code)
		}
	})
	if _, err := pg.Exec(`DELETE FROM task_relations WHERE task_id=$1`, m.b.TaskID); err != nil {
		t.Fatal(err)
	}
	t.Run("password-revoked-during-read", func(t *testing.T) {
		replacement, err := bcrypt.GenerateFromPassword([]byte("synthetic-replaced-report-password"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		setAfter(func() {
			if err := pg.SetSetting(authPassKey, string(replacement)); err != nil {
				t.Error(err)
			}
		})
		defer func() {
			setAfter(nil)
			if err := pg.SetSetting(authPassKey, string(hash)); err != nil {
				t.Error(err)
			}
		}()
		if code, raw := request("GET", taskPath+"/coverage-graph", session.Token); code != 401 || strings.Contains(string(raw), "nodes") {
			t.Fatal("회수된 JWT의 응답 노출", code)
		}
	})
	t.Run("cancelled-auth-read-preserves-cache", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		req := httptest.NewRequestWithContext(ctx, "GET", taskPath+"/coverage-graph", nil)
		req.Header.Set("Authorization", "Bearer "+session.Token)
		auth := &Server{m: &Manager{pg: &db.DB{DB: f.Runtime}}, jwtKey: []byte("synthetic-report-read-jwt-key-material")}
		auth.setPassHash("existing-synthetic-cache")
		out := httptest.NewRecorder()
		if auth.authenticateReportRead(out, req) || out.Code != 503 {
			t.Fatal("취소된 인증 읽기가 허용됨", out.Code)
		}
		auth.authMu.Lock()
		cached := auth.passHash
		auth.authMu.Unlock()
		if cached != "existing-synthetic-cache" {
			t.Fatal("실패한 조회가 비밀번호 캐시를 바꿈")
		}
	})
	t.Run("owner-revoked-during-read", func(t *testing.T) {
		setAfter(func() { endpoint.deny.Store(true) })
		defer func() { setAfter(nil); endpoint.deny.Store(false) }()
		if code, raw := request("GET", taskPath+"/coverage-graph", session.Token); code != 403 || strings.Contains(string(raw), "nodes") {
			t.Fatal("회수 뒤 응답 노출", code)
		}
	})
	t.Run("source-added-during-read", func(t *testing.T) {
		setAfter(func() {
			_, err := pg.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, m.b.TaskID, source)
			if err != nil {
				t.Error(err)
			}
		})
		defer func() { setAfter(nil); pg.Exec(`DELETE FROM task_relations WHERE task_id=$1`, m.b.TaskID) }()
		if code, _ := request("GET", taskPath+"/coverage-graph", session.Token); code != 409 {
			t.Fatal("원천 변경을 응답 전 검출하지 못함", code)
		}
	})
	sourceHandler, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keys, data, source)
	if err != nil {
		t.Fatal("두 번째 소유 Job 보고서 생성 실패", err)
	}
	sourceRuntime := httptest.NewServer(sourceHandler)
	defer sourceRuntime.Close()
	sourceProduct, err := NewProductDiagnosisHandler(t.Context(), f.Runtime, keys, data, endpoint, sourceRuntime.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceAPI := httptest.NewServer(sourceProduct)
	defer sourceAPI.Close()
	sourcePath := "/api/tasks/" + strconv.FormatInt(source, 10)
	t.Run("URL-job-without-KG-registration", func(t *testing.T) {
		assertReadOnly(t, func() {
			if code, _ := requestAt(sourceAPI.URL, "GET", sourcePath+"/access-map", session.Token); code != 409 {
				t.Fatal("미등록 URL Job 지도 합성", code)
			}
		})
	})
	t.Run("deleted-task", func(t *testing.T) {
		if _, err := pg.Exec(`UPDATE tasks SET deleted_at=NOW() WHERE id=$1`, source); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := pg.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, source); err != nil {
				t.Error(err)
			}
		}()
		if code, _ := requestAt(sourceAPI.URL, "GET", sourcePath+"/coverage-graph", session.Token); code != 404 {
			t.Fatal("삭제 task 조회", code)
		}
	})
	t.Run("preparation-service-unavailable", func(t *testing.T) {
		endpoint.unavailable.Store(true)
		defer endpoint.unavailable.Store(false)
		if code, _ := request("GET", taskPath+"/coverage-graph", session.Token); code != 503 {
			t.Fatal("권한 서비스 장애를 거부와 혼동", code)
		}
	})
	t.Run("database-failure", func(t *testing.T) {
		if _, err := pg.Exec(`ALTER TABLE task_relations RENAME TO unavailable_relations`); err != nil {
			t.Fatal(err)
		}
		defer pg.Exec(`ALTER TABLE unavailable_relations RENAME TO task_relations`)
		if code, _ := request("GET", taskPath+"/coverage-graph", session.Token); code != 503 {
			t.Fatal("DB 실패를 빈 그래프로 합성", code)
		}
	})
	t.Run("unverified-runtime-response", func(t *testing.T) {
		assertReadOnly(t, func() {
			for _, proof := range []string{"", strings.Repeat("f", 64)} {
				oldRuntime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if proof != "" {
						w.Header().Set(reportReadScopeHeader, proof)
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"nodes":[{"key":"a:1","kind":"endpoint","label":"synthetic-old-secret","asset_id":1,"tested":false,"in_scope":true}],"edges":[]}`)
				}))
				oldProduct, err := NewProductDiagnosisHandler(t.Context(), f.Runtime, keys, data, endpoint, oldRuntime.URL, nil, nil)
				if err != nil {
					oldRuntime.Close()
					t.Fatal(err)
				}
				oldAPI := httptest.NewServer(oldProduct)
				code, raw := requestAt(oldAPI.URL, "GET", taskPath+"/coverage-graph", session.Token)
				oldAPI.Close()
				oldRuntime.Close()
				if code != 503 || strings.Contains(string(raw), "synthetic-old-secret") || strings.Contains(string(raw), "nodes") {
					t.Fatal("미검증 실행 서버 응답 노출", code)
				}
			}
		})
	})
	t.Run("database-connection-unavailable", func(t *testing.T) {
		if err := f.Runtime.Close(); err != nil {
			t.Fatal(err)
		}
		for _, leaf := range []string{"coverage-graph", "access-map"} {
			if code, _ := request("GET", taskPath+"/"+leaf, session.Token); code != 503 {
				t.Fatal("DB 연결 실패를 인증 실패로 오분류", code)
			}
		}
	})
	t.Logf("대상 HTTP=%d 모델=%d; 조회 중 backend 실행·Inspect·E③ 발급·DB 및 Job journal 쓰기=0", targetCalls.Load(), modelCalls.Load())
}

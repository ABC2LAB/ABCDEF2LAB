package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
)

// KG 조회 v1 처리기·중계 시험(kg-query-v1 §9). 모든 ID·문구는 합성 fixture다.

type kgEnvelope struct {
	Version string `json:"version"`
	Error   struct {
		Code      string `json:"code"`
		Retryable *bool  `json:"retryable"`
	} `json:"error"`
}

func kgEnvelopeOf(t *testing.T, w *httptest.ResponseRecorder) kgEnvelope {
	t.Helper()
	var e kgEnvelope
	dec := json.NewDecoder(w.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil || e.Version != db.KGQueryVersion || e.Error.Retryable == nil {
		t.Fatalf("kg 오류 봉투가 아님: %v %q", err, w.Body.String())
	}
	return e
}

// Q07·Q12·Q17(일부): DB 없이 닫히는 v1 오류는 kg 봉투이고, 무인자 조회의 오류 본문은 그대로다.
func TestKGViewErrorsWithoutDatabase(t *testing.T) {
	s := &Server{jwtKey: []byte("synthetic-kg-auth-key-32-bytes!!")}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		method, target, auth string
		status               int
		code                 string
		retryable            bool
	}{
		"bearer 없음":           {"GET", "/api/tasks/42/coverage-graph?view=kg-v1", "", 401, "kg_auth_required", false},
		"query token 불인정":     {"GET", "/api/tasks/42/coverage-graph?view=kg-v1&token=" + token, "", 401, "kg_auth_required", false},
		"HEAD":                {"HEAD", "/api/tasks/42/coverage-graph?view=kg-v1", "", 405, "kg_method_not_allowed", false},
		"인증 DB 없음":            {"GET", "/api/tasks/42/coverage-graph?view=kg-v1", "Bearer " + token, 503, "kg_store_unavailable", true},
		"미지원 view도 kg 봉투로 인증": {"GET", "/api/tasks/42/coverage-graph?view=kg-v2", "", 401, "kg_auth_required", false},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			s.serveCoverageGraph(w, r, 0)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d %s", w.Code, w.Body)
			}
			if tc.method == "HEAD" {
				return
			}
			e := kgEnvelopeOf(t, w)
			if e.Error.Code != tc.code || *e.Error.Retryable != tc.retryable {
				t.Fatalf("error=%+v", e.Error)
			}
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("토큰이 오류 본문에 남음")
			}
		})
	}
	// Q18: 무인자 오류 본문은 기존 writeErr 그대로다.
	w := httptest.NewRecorder()
	s.serveCoverageGraph(w, httptest.NewRequest("GET", "/api/tasks/42/coverage-graph", nil), 0)
	var legacy map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil || w.Code != 401 || len(legacy) != 1 || legacy["error"] != authErrUnauthorized {
		t.Fatalf("무인자 오류 본문 변경: %d %s", w.Code, w.Body)
	}
	// 범용 서버의 requireAuth도 v1을 보고서 Bearer 인증으로 받는다(cookie·query token 불인정).
	r := httptest.NewRequest("GET", "/api/tasks/42/coverage-graph?view=kg-v1", nil)
	r.AddCookie(&http.Cookie{Name: "token", Value: token})
	w = httptest.NewRecorder()
	called := false
	s.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, r)
	if called || w.Code != 401 || kgEnvelopeOf(t, w).Error.Code != "kg_auth_required" {
		t.Fatalf("requireAuth: called=%v status=%d", called, w.Code)
	}
}

type kgPGFixture struct {
	s          *Server
	pg         *db.DB
	token      string
	cur, src   *db.Task
	srcsrc     *db.Task
	asset      int64
	srcAsset   int64
	srcsrcItem int64
}

func kgPGServer(t *testing.T) kgPGFixture {
	t.Helper()
	dsn, _, err := config.PostgresDSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("synthetic-kg-auth-key-32-bytes!!")}
	hash, err := s.reloadReportPassHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token, err := signJWT(s.signingKey(hash))
	if err != nil {
		t.Fatal(err)
	}
	f := kgPGFixture{s: s, pg: pg, token: token}
	newTask := func() *db.Task {
		task, err := pg.CreateTask("synthetic kg-v1 handler", "fixture", nil, 0, 300)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
		return task
	}
	f.cur, f.src, f.srcsrc = newTask(), newTask(), newTask()
	if _, err := pg.Exec(`INSERT INTO task_relations(task_id, source_task_id) VALUES ($1,$2),($2,$3)`, f.cur.ID, f.src.ID, f.srcsrc.ID); err != nil {
		t.Fatal(err)
	}
	asset := func(task int64) int64 {
		var id int64
		if err := pg.QueryRow(`INSERT INTO assets(type, url, task_ids) VALUES ('endpoint', $1, ARRAY[$2::bigint]) RETURNING id`,
			fmt.Sprintf("https://kg-%d.test/canary-path?token=canary", task), task).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM assets WHERE id=$1`, id) })
		return id
	}
	f.asset, f.srcAsset, f.srcsrcItem = asset(f.cur.ID), asset(f.src.ID), asset(f.srcsrc.ID)
	return f
}

func (f kgPGFixture) get(target string, bound int64, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	f.s.serveCoverageGraph(w, r, bound)
	return w
}

// Q05·Q12·Q15(처리기)·Q18: 실 PostgreSQL에서 v1 응답·오류 상태와 무인자 응답 유지.
func TestKGViewHandlerPostgres(t *testing.T) {
	f := kgPGServer(t)
	cur := strconv.FormatInt(f.cur.ID, 10)
	path := "/api/tasks/" + cur + "/coverage-graph"

	w := f.get(path+"?view=kg-v1", 0)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("v1: %d %s", w.Code, w.Body)
	}
	if proof := w.Header().Values(reportReadScopeHeader); len(proof) != 1 || len(proof[0]) != 64 {
		t.Fatal("v1 응답에 조회 결속 증명이 없음 — 제품 중계가 503으로 닫음")
	}
	body := w.Body.String()
	var g struct {
		Version string `json:"version"`
		TaskID  string `json:"task_id"`
		Scope   struct {
			TaskIDs []string `json:"task_ids"`
		} `json:"scope"`
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil || g.Version != db.KGQueryVersion || g.TaskID != cur {
		t.Fatalf("v1 본문: %v %s", err, body)
	}
	// 41→40→39에서 직접 원천 40까지만(Q05)
	if !slices.Equal(g.Scope.TaskIDs, []string{cur, strconv.FormatInt(f.src.ID, 10)}) {
		t.Fatalf("task_ids=%v", g.Scope.TaskIDs)
	}
	var ids []string
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	want := []string{"t:" + cur, fmt.Sprintf("t:%d", f.src.ID), fmt.Sprintf("a:%d", f.asset), fmt.Sprintf("a:%d", f.srcAsset)}
	if !slices.Equal(ids, want) {
		t.Fatalf("nodes=%v want %v", ids, want)
	}
	if strings.Contains(body, "canary") || strings.Contains(body, ".test") {
		t.Fatal("URL 원문이 응답에 남음")
	}

	for name, tc := range map[string]struct {
		target string
		bound  int64
		header []string
		status int
		code   string
	}{
		"미지원 view":    {path + "?view=kg-v2", 0, nil, 400, "kg_request_invalid"},
		"중복 view":     {path + "?view=kg-v1&view=kg-v1", 0, nil, 400, "kg_request_invalid"},
		"다른 query 추가": {path + "?view=kg-v1&task=" + cur, 0, nil, 400, "kg_request_invalid"},
		"결속 밖 작업":     {path + "?view=kg-v1", f.cur.ID + 1, nil, 403, "kg_task_forbidden"},
		"없는 작업":       {"/api/tasks/" + strconv.FormatInt(f.cur.ID+1_000_000, 10) + "/coverage-graph?view=kg-v1", 0, nil, 404, "kg_task_not_found"},
		"중계 결속 형식 오류": {path + "?view=kg-v1", 0, []string{reportReadScopeHeader, "not-hex"}, 400, "kg_request_invalid"},
		"중계 결속 문맥 변경": {path + "?view=kg-v1", 0, []string{reportReadScopeHeader, strings.Repeat("0", 64)}, 409, "kg_scope_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			w := f.get(tc.target, tc.bound, tc.header...)
			if w.Code != tc.status {
				t.Fatalf("status=%d %s", w.Code, w.Body)
			}
			if e := kgEnvelopeOf(t, w); e.Error.Code != tc.code || *e.Error.Retryable {
				t.Fatalf("error=%+v", e.Error)
			}
		})
	}

	// Q18: 무인자 응답은 기존 그래프 DTO 그대로(version 칸 없음).
	w = f.get(path, 0)
	var legacy map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil || w.Code != 200 || legacy["version"] != nil || legacy["nodes"] == nil {
		t.Fatalf("무인자 응답 변경: %d %s", w.Code, w.Body)
	}

	// Q13: 건수 상한 초과는 자르지 않고 413 kg_graph_too_large(재조회 불가).
	if _, err := f.pg.Exec(`INSERT INTO assets(type, url, task_ids) SELECT 'endpoint', 'https://kg-limit.test/p' || g, ARRAY[$1::bigint] FROM generate_series(1, $2) g`, f.cur.ID, db.KGLimits.Nodes); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pg.Exec(`DELETE FROM assets WHERE url LIKE 'https://kg-limit.test/%'`) })
	w = f.get(path+"?view=kg-v1", 0)
	if w.Code != 413 {
		t.Fatalf("상한 초과: %d", w.Code)
	}
	if e := kgEnvelopeOf(t, w); e.Error.Code != "kg_graph_too_large" || *e.Error.Retryable {
		t.Fatalf("error=%+v", e.Error)
	}
}

// Q19·Q20: 제품 중계는 v1의 413만 본문 그대로 넘기고, 무인자 coverage-graph·access-map의 413은 지금처럼 503이다.
func TestKGBridgeStatusPassThrough(t *testing.T) {
	f := kgPGServer(t)
	cur := strconv.FormatInt(f.cur.ID, 10)
	var originStatus int
	var originBody string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if originStatus == 200 {
			w.Header().Set(reportReadScopeHeader, r.Header.Get(reportReadScopeHeader))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(originStatus)
		_, _ = io.WriteString(w, originBody)
	}))
	defer origin.Close()
	target, _ := url.Parse(origin.URL)
	bridge := &diagnosisManagementBridge{
		origin:          target,
		client:          origin.Client(),
		authenticate:    func(*http.Request) (string, error) { return "synthetic-owner", nil },
		authorized:      func(context.Context, string, string) bool { return true },
		readPool:        f.pg.DB,
		reportAuthorize: func(context.Context, string, string) (bool, error) { return true, nil },
	}
	tooLarge := `{"version":"abc2lab.kg-query/v1","error":{"code":"kg_graph_too_large","retryable":false}}`
	for name, tc := range map[string]struct {
		leaf, query string
		status      int
		want        int
		passBody    bool
	}{
		"v1 413 보존":            {"coverage-graph", "?view=kg-v1", 413, 413, true},
		"v1 200":               {"coverage-graph", "?view=kg-v1", 200, 200, true},
		"v1 503":               {"coverage-graph", "?view=kg-v1", 503, 503, true},
		"무인자 coverage 413→503": {"coverage-graph", "", 413, 503, false},
		"access-map 413→503":   {"access-map", "", 413, 503, false},
		"v1 그 밖(500)→503":      {"coverage-graph", "?view=kg-v1", 500, 503, false},
	} {
		t.Run(name, func(t *testing.T) {
			originStatus, originBody = tc.status, tooLarge
			if tc.status == 200 {
				originBody = `{"version":"abc2lab.kg-query/v1"}`
			}
			r := httptest.NewRequest("GET", "/api/tasks/"+cur+"/"+tc.leaf+tc.query, nil)
			r.Header.Set("Authorization", "Bearer synthetic")
			w := httptest.NewRecorder()
			bridge.ServeHTTP(w, r)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d %s", w.Code, w.Body)
			}
			if (w.Body.String() == originBody) != tc.passBody {
				t.Fatalf("본문 전달=%v: %s", !tc.passBody, w.Body)
			}
		})
	}
	// Q20: 무인자 허용 목록 200·400·401·403·404·409·503은 그대로 통과한다.
	for _, status := range []int{400, 401, 403, 404, 409, 503} {
		originStatus, originBody = status, `{"error":"synthetic"}`
		r := httptest.NewRequest("GET", "/api/tasks/"+cur+"/coverage-graph", nil)
		w := httptest.NewRecorder()
		bridge.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("무인자 %d → %d", status, w.Code)
		}
	}
}

//go:build linux

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/reportcapture"
	"github.com/Autumn-27/artex/reproduction"
	"github.com/Autumn-27/artex/server"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type productReportFrame struct{ png []byte }

type productReportTestAuthority struct{ job diagnosisprep.Job }

func (a productReportTestAuthority) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	if r.Command == diagnosisprep.ProductControl && r.Product.Operation == "target" && a.job.Scope != nil && r.Product.TargetID == a.job.Scope.TargetRef && r.Principal == "ARTEX" {
		s := a.job.Scope
		target := &diagnosisprep.ProductTarget{ID: s.TargetRef, Origin: s.Origin, State: "approved", PathPrefixes: s.PathPrefixes, Methods: s.Methods, Requests: s.Budget.Requests, ExpiresAt: s.ExpiresAt}
		return diagnosisprep.Response{Version: r.Version, RequestID: r.RequestID, Product: &diagnosisprep.ProductResponse{Target: target}}, nil
	}
	if r.Command != diagnosisprep.AuthorizeTask {
		return diagnosisprep.Response{}, diagnosisprep.ErrProtocol
	}
	allowed := r.Principal == "ARTEX" && r.TaskID == a.job.TaskID
	out := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Authorized: &allowed}
	if allowed {
		out.Job = &a.job
	}
	return out, nil
}

func (f productReportFrame) CurrentFrame(context.Context) (reportcapture.Frame, error) {
	return reportcapture.Frame{PNG: f.png, CapturedAt: time.Now()}, nil
}

// 실제 설치 GRANT와 product_api 연결로 저장 이미지를 읽는다. 합성 관찰·픽셀만 사용한다.
func TestProductReportCaptureReadsOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_TEST_DSN")
	if dsn == "" {
		t.Skip("전용 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("전용 DB 소유 확인 필요")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "product_report_" + productRandom(8)
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	var installer *db.DB
	var apiPool, runtimePool *sql.DB
	created := []string{}
	t.Cleanup(func() {
		if apiPool != nil {
			apiPool.Close()
		}
		if runtimePool != nil {
			runtimePool.Close()
		}
		if installer != nil {
			installer.Close()
		}
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + ident(name) + " WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		for _, role := range created {
			if _, err := admin.Exec("DROP ROLE " + ident(role)); err != nil {
				t.Error(err)
			}
		}
		admin.Close()
	})
	if _, err := admin.Exec("CREATE DATABASE " + ident(name) + " TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	u.RawPath = ""
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	installer, err = db.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate, e2checkpoint.Migrate} {
		if err := m(t.Context(), installer.DB); err != nil {
			t.Fatal(err)
		}
	}
	if installer.EnsureLLMUsageTable() != nil || installer.EnsureLLMRecordsTable() != nil {
		t.Fatal("사용량 표 준비 실패")
	}
	password := "synthetic-product-report-password"
	for _, role := range []string{"product_runtime", "product_api", "product_model_writer"} {
		if _, err := admin.Exec("CREATE ROLE " + ident(role) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + password + "'"); err != nil {
			t.Fatal("새 전용 역할 준비 실패", err)
		}
		created = append(created, role)
	}
	if _, err := admin.Exec("CREATE ROLE product_e2_head_writer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
		t.Fatal("새 head 역할 준비 실패", err)
	}
	created = append(created, "product_e2_head_writer")
	if productGrantRoles(t.Context(), installer.DB, name) != nil {
		t.Fatal("실제 제품 설치 권한 적용 실패")
	}
	// CI 소켓 DSN의 query user/password가 새 역할의 userinfo를 덮어쓰지 않게 한다.
	roleQuery := u.Query()
	for _, key := range []string{"user", "password", "dbname", "database"} {
		roleQuery.Del(key)
	}
	u.RawQuery = roleQuery.Encode()
	u.User = url.UserPassword("product_api", password)
	apiPool, err = sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("product_runtime", password)
	runtimePool, err = sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	// 실제 설치 권한이 서버의 일반 HTTP 실행 역할 검사(진단 Worker insert_assets의 자산 쓰기 포함, C24)를 통과한다.
	if err := server.CheckFirstDiagnosisHTTPRuntimeDatabase(t.Context(), runtimePool); err != nil {
		t.Fatal("설치된 실행 역할이 일반 HTTP 실행 검사를 통과하지 않음", err)
	}
	var selectAllowed, writeAllowed bool
	if apiPool.QueryRow(`SELECT has_table_privilege(current_user,'diagnostic_captures_v1','SELECT'),has_table_privilege(current_user,'diagnostic_captures_v1','INSERT,UPDATE,DELETE,TRUNCATE')`).Scan(&selectAllowed, &writeAllowed) != nil || !selectAllowed || writeAllowed {
		t.Fatal("보고서 역할의 캡처 읽기·쓰기 권한 분리 실패")
	}
	row, err := installer.CreateTask("synthetic product report", "stored observations only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := installer.Exploration(row.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"description": "synthetic capture"}, 5, nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"tool_output": "synthetic response", "trusted": map[string]any{"classification": "completed", "execution": map[string]any{"scope": map[string]any{"TaskRef": strconv.FormatInt(row.ID, 10), "RunRef": "product-report-run"}, "intent_id": intent}, "call": map[string]any{"call_ref": "product-report-call", "intent_id": intent, "observations": []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "synthetic-attempt", Method: "GET", Endpoint: "/safe", ObservedAt: time.Now()}}}}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := db.DiagnosticCandidate{TaskID: row.ID, IntentID: intent, RunRef: "product-report-run", CallRef: "product-report-call", VulnClass: "XSS", Name: "합성 관찰", Summary: "synthetic summary", Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1, EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now(), Evidence: string(raw)}
	id, err := store.RecordDiagnosticCandidate(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	entry := fmt.Sprintf("candidate-%d", id)
	source, err := db.LoadDiagnosticRemediationSource(t.Context(), installer.DB, row.ID, entry)
	if err != nil {
		t.Fatal(err)
	}
	steps := reproduction.FromStored(row.ID, entry, 1, source.EvidenceDigest, source.Context)
	if len(steps) != 1 {
		t.Fatal("합성 단계 부재")
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	binding := reportcapture.Binding{TaskID: row.ID, EntryID: entry, Step: steps[0]}
	runtimeDB := &db.DB{DB: runtimePool}
	if err := reportcapture.Collect(t.Context(), binding, reportcapture.Policy{}, nil, runtimeDB); err != nil {
		t.Fatal(err)
	}
	if err := reportcapture.Collect(t.Context(), binding, reportcapture.Policy{ApprovedNonsensitiveRegions: []image.Rectangle{image.Rect(0, 0, 1, 1)}}, productReportFrame{imageBytes.Bytes()}, runtimeDB); err != nil {
		t.Fatal("실제 runtime 열 권한으로 실패→성공 저장 실패", err)
	}
	capture, err := db.ReadDiagnosticCapture(t.Context(), apiPool, row.ID, entry, steps[0])
	if err != nil {
		t.Fatal("product_api 캡처 읽기 실패", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting("auth.password_hash", string(hash)) != nil {
		t.Fatal("인증 설정 실패")
	}
	keyDir, dataDir := t.TempDir(), t.TempDir()
	if os.Chmod(keyDir, 0700) != nil || os.Chmod(dataDir, 0700) != nil {
		t.Fatal("사적 시험 디렉터리 준비 실패")
	}
	if os.WriteFile(filepath.Join(keyDir, "jwt.key"), []byte(strings.Repeat("s", 32)), 0600) != nil {
		t.Fatal("시험 키 준비 실패")
	}
	handler, err := server.NewFirstDiagnosisReportHandler(t.Context(), apiPool, keyDir, dataDir, row.ID)
	if err != nil {
		t.Fatal("product_api 보고서 서버 기동 실패", err)
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"ABC2LAB","password":"`+password+`"}`)))
	var session struct {
		Token string `json:"token"`
	}
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("보고서 인증 실패", login.Code)
	}
	imagePath := fmt.Sprintf("/api/report/diagnostic/image?task=%d&audience=internal&entry=%s&step=%s&digest=%s", row.ID, entry, steps[0].ID, capture.Digest)
	request := httptest.NewRequest("GET", imagePath, nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), capture.PNG) {
		t.Fatal("실제 설치 역할의 이미지 GET 실패", response.Code)
	}
	// 실제 설치자가 부여한 product_api 역할로 그래프의 범위 SQL까지 도달한다.
	var scopeRead, scopeWrite bool
	if apiPool.QueryRow(`SELECT has_table_privilege(current_user,'task_scope','SELECT'),has_table_privilege(current_user,'task_scope','INSERT,UPDATE,DELETE,TRUNCATE')`).Scan(&scopeRead, &scopeWrite) != nil || !scopeRead || scopeWrite {
		t.Fatal("보고서 scope의 읽기 전용 설치 권한 불일치")
	}
	var scopedAsset int64
	if err := installer.QueryRow(`INSERT INTO assets(type,domain,task_ids) VALUES('subdomain','report.owned.test',$1::bigint[]) RETURNING id`, fmt.Sprintf("{%d}", row.ID)).Scan(&scopedAsset); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Exec(`INSERT INTO task_scope(task_id,kind,domain,source) VALUES($1,'subdomain','report.owned.test','manual')`, row.ID); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(handler)
	defer upstream.Close()
	job := diagnosisprep.Job{
		ID: "fixture-report", Revision: 1, SourceDigest: strings.Repeat("a", 64),
		Input: diagnosisprep.Input{TargetURL: "https://report.owned.test/", ProfileID: "local"},
		State: diagnosisprep.Unknown, Phase: diagnosisprep.Closure, PreparationStarted: true,
		AuthorityState: diagnosisprep.AuthorityNotIssued, TaskID: strconv.FormatInt(row.ID, 10),
	}
	product, err := server.NewProductDiagnosisHandler(t.Context(), apiPool, keyDir, dataDir,
		productReportTestAuthority{job: job}, upstream.URL, nil, nil)
	if err != nil {
		t.Fatal("제품 관리 서버 기동 실패", err)
	}
	t.Run("remediation-followup-role", func(t *testing.T) {
		_, err := installer.SaveDiagnosticRemediation(t.Context(), row.ID, entry, source.SourceDigest, nil)
		if err != nil {
			t.Fatal(err)
		}
		completed := job
		completed.Started, completed.DrainComplete, completed.ResultReady = true, true, true
		completed.State, completed.Phase, completed.AuthorityState = diagnosisprep.Completed, diagnosisprep.Report, diagnosisprep.AuthorityConsumed
		completed.Scope = &diagnosisprep.Scope{TargetRef: "owned", ScopeRef: "scope-owned", Origin: "https://report.owned.test", PathPrefixes: []string{"/"}, Methods: []string{"GET"}, QueryPolicy: "any", BodyPolicy: "bounded", ApprovalMode: "installed_delegation", AttestationRef: "owned", Budget: diagnosisprep.Budget{Requests: 4, ModelRequests: 3, InputTokens: 30, OutputTokens: 20, DurationSeconds: 60}, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		h, err := server.NewProductDiagnosisHandler(t.Context(), apiPool, keyDir, dataDir, productReportTestAuthority{job: completed}, upstream.URL, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		q := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d/remediation-followup", row.ID), nil)
		q.Header.Set("Authorization", "Bearer "+session.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		var draft struct {
			Items []struct {
				Entry string `json:"entry_id"`
			}
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &draft) != nil || len(draft.Items) != 1 || draft.Items[0].Entry != entry {
			t.Fatal("제품 역할에서 이행점검 초안 조회 실패", w.Code, w.Body.String())
		}
		var ok bool
		err = apiPool.QueryRow(`SELECT has_table_privilege(current_user,'remediation_followup_reviews','SELECT')
			AND has_table_privilege(current_user,'remediation_followup_reviews','INSERT')
			AND has_column_privilege(current_user,'remediation_followup_reviews','revision','UPDATE')
			AND NOT has_column_privilege(current_user,'remediation_followup_reviews','record_id','UPDATE')
			AND NOT has_table_privilege(current_user,'remediation_followup_reviews','DELETE,TRUNCATE')
			AND NOT has_table_privilege('product_runtime','remediation_followup_reviews','SELECT,INSERT,UPDATE,DELETE')`).Scan(&ok)
		if err != nil || !ok {
			t.Fatal("이행점검 역할 권한", err)
		}
	})
	read := func(leaf string) *httptest.ResponseRecorder {
		q := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d/%s", row.ID, leaf), nil)
		q.Header.Set("Authorization", "Bearer "+session.Token)
		out := httptest.NewRecorder()
		product.ServeHTTP(out, q)
		return out
	}
	assertGraph := func(t *testing.T) {
		t.Helper()
		out := read("coverage-graph")
		var graph db.CoverageGraphData
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &graph) != nil || len(graph.Nodes) != 1 || !graph.Nodes[0].InScope {
			t.Fatal("실제 보고서 역할의 제품 그래프 GET 실패", out.Code)
		}
		if out := read("access-map"); out.Code != 409 {
			t.Fatal("KG 미등록 URL Job의 지도 상태가 달라짐", out.Code)
		}
	}
	// KG 조회 v1(kg-query-v1)은 무인자 본문 읽기에 더해 task_scope의 id·source를 읽는다(scope 출처 ID·출처 값).
	// 권한이 모자라면 빈 그래프·내부 오류가 아니라 503 kg_store_unavailable(재조회 가능)이다.
	assertKG := func(t *testing.T, want int) {
		t.Helper()
		out := read("coverage-graph?view=kg-v1")
		body := out.Body.String()
		// 성공 본문에는 출처 종류 "task_scope"가 정상으로 들어가므로 원문 도메인만 본다. 실패 본문은 고정 봉투와 통째로 비교한다.
		if out.Code != want || strings.Contains(body, "report.owned.test") {
			t.Fatal("실제 보고서 역할의 KG v1 GET", want, out.Code)
		}
		if want != 200 {
			if body != `{"error":{"code":"kg_store_unavailable","retryable":true},"version":"abc2lab.kg-query/v1"}`+"\n" {
				t.Fatal("KG v1 권한 부족 본문", body)
			}
			return
		}
		var graph struct {
			Version    string `json:"version"`
			Provenance []struct {
				ID     string `json:"id"`
				Source string `json:"source"`
			} `json:"provenance"`
		}
		if json.Unmarshal(out.Body.Bytes(), &graph) != nil || graph.Version != db.KGQueryVersion || !strings.Contains(body, fmt.Sprintf(`"a:%d"`, scopedAsset)) {
			t.Fatal("KG v1 본문", body)
		}
		scoped := false
		for _, p := range graph.Provenance {
			scoped = scoped || strings.HasPrefix(p.ID, "scope:") && strings.HasSuffix(p.ID, fmt.Sprintf(":%d", scopedAsset)) && p.Source == "manual"
		}
		if !scoped {
			t.Fatal("KG v1 범위 출처(id·source) 누락", body)
		}
	}
	t.Run("fresh-install", func(t *testing.T) {
		assertGraph(t)
		assertKG(t, 200)
	})
	t.Run("before-683", func(t *testing.T) {
		if _, err := installer.Exec(`REVOKE SELECT ON public.task_scope FROM product_api`); err != nil {
			t.Fatal(err)
		}
		out := read("coverage-graph")
		if out.Code != 503 || strings.Contains(out.Body.String(), `"nodes"`) || strings.Contains(out.Body.String(), "task_scope") {
			t.Fatal("조회 권한 누락이 그래프 또는 내부 오류로 공개됨", out.Code)
		}
		if out := read("access-map"); out.Code != 409 {
			t.Fatal("그래프 전용 권한이 지도 읽기에 영향을 줌", out.Code)
		}
		assertKG(t, 503)
	})
	t.Run("minimal-upgrade", func(t *testing.T) {
		if _, err := installer.Exec(`GRANT SELECT (task_id, kind, company_id, domain, net, value) ON public.task_scope TO product_api`); err != nil {
			t.Fatal(err)
		}
		var tableRead, unrelatedRead, writable, helper bool
		if err := apiPool.QueryRow(`SELECT has_table_privilege(current_user,'public.task_scope','SELECT'),
			has_column_privilege(current_user,'public.task_scope','reason','SELECT'),
			has_table_privilege(current_user,'public.task_scope','INSERT,UPDATE,DELETE,TRUNCATE')
			  OR has_any_column_privilege(current_user,'public.task_scope','INSERT,UPDATE'),
			has_function_privilege(current_user,'public.try_inet(text)','EXECUTE')`).Scan(&tableRead, &unrelatedRead, &writable, &helper); err != nil || tableRead || unrelatedRead || writable || !helper {
			t.Fatal("최소 이관 권한의 경계가 달라짐", err)
		}
		assertGraph(t)
		// 6개 열만 이관한 기존 설치: 무인자 그래프는 그대로 되고 v1은 권한 부족으로 닫힌다.
		assertKG(t, 503)
	})
	t.Run("minimal-upgrade-kg-v1", func(t *testing.T) {
		if _, err := installer.Exec(`GRANT SELECT (id, source) ON public.task_scope TO product_api`); err != nil {
			t.Fatal(err)
		}
		var tableRead, unrelatedRead, writable bool
		if err := apiPool.QueryRow(`SELECT has_table_privilege(current_user,'public.task_scope','SELECT'),
			has_column_privilege(current_user,'public.task_scope','reason','SELECT'),
			has_table_privilege(current_user,'public.task_scope','INSERT,UPDATE,DELETE,TRUNCATE')
			  OR has_any_column_privilege(current_user,'public.task_scope','INSERT,UPDATE')`).Scan(&tableRead, &unrelatedRead, &writable); err != nil || tableRead || unrelatedRead || writable {
			t.Fatal("v1 이관 권한의 경계가 달라짐", err)
		}
		assertGraph(t)
		assertKG(t, 200)
	})
}

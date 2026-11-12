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
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/reportcapture"
	"github.com/Autumn-27/artex/reproduction"
	"github.com/Autumn-27/artex/server"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type productReportFrame struct{ png []byte }

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
	for _, m := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate} {
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
}

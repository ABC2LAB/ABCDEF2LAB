//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// 실제 PG 로그인과 HTTP를 사용하되 실행 Gateway는 가짜다. 엔진·대상 요청은 발생하지 않는다.
func TestDiagnosisManagementExistingAuthOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	password := "synthetic-management-fixture"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("auth fixture")
	}
	keyDir, dataDir := t.TempDir(), t.TempDir()
	for _, dir := range []string{keyDir, dataDir} {
		if os.Chmod(dir, 0700) != nil {
			t.Fatal("private fixture")
		}
	}
	key := []byte("synthetic-management-existing-jwt-key-only")
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), key, 0600) != nil {
		t.Fatal("key fixture")
	}
	var role, schema string
	if f.Runtime.QueryRow("SELECT current_user,current_schema()").Scan(&role, &schema) != nil {
		t.Fatal("runtime identity")
	}
	ident := func(value string) string { return pgx.Identifier{value}.Sanitize() }
	for _, q := range []string{
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA " + ident(schema) + " FROM " + ident(role),
		"REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA " + ident(schema) + " FROM " + ident(role),
		"GRANT SELECT ON settings TO " + ident(role),
	} {
		if _, err := installer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	gateway := &managementTestGateway{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler, err := NewDiagnosisManagementHandler(ctx, f.Runtime, keyDir, dataDir, managementTestRegistry{}, gateway)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	var token string
	call := func(method, path, key string, body any) (int, map[string]json.RawMessage) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]json.RawMessage
		_ = json.Unmarshal(data, &out)
		return res.StatusCode, out
	}
	if code, _ := call("GET", diagnosisapi.BasePath+"/catalog", "", nil); code != 401 {
		t.Fatal(code)
	}
	code, data := call("POST", "/api/auth/login", "", map[string]string{"username": "ARTEX", "password": password})
	if code != 200 || json.Unmarshal(data["token"], &token) != nil || token == "" {
		t.Fatal("existing login rejected", code)
	}
	validToken := token
	code, data = call("GET", diagnosisapi.BasePath+"/catalog", "", nil)
	if code != 200 {
		t.Fatal("catalog", code)
	}
	var profiles []diagnosisapi.Profile
	if json.Unmarshal(data["profiles"], &profiles) != nil || len(profiles) != 1 {
		t.Fatal("catalog shape")
	}
	code, data = call("POST", diagnosisapi.BasePath+"/drafts", "create-owned", map[string]any{"version": diagnosisapi.Version, "configuration": profiles[0].Configuration})
	var draft diagnosisapi.Draft
	if code != 201 || json.Unmarshal(data["draft"], &draft) != nil || draft.ID == "" {
		t.Fatal("draft", code)
	}
	base := diagnosisapi.BasePath + "/drafts/" + draft.ID
	code, data = call("POST", base+"/validate", "", map[string]any{"version": diagnosisapi.Version, "expected_revision": draft.Revision})
	if code != 200 || json.Unmarshal(data["draft"], &draft) != nil || draft.State != "awaiting_approval" {
		t.Fatal("unapproved validation", code)
	}
	if code, _ = call("POST", base+"/start", "start-owned", map[string]any{"version": diagnosisapi.Version, "expected_revision": draft.Revision}); code != 409 {
		t.Fatal("unapproved start", code)
	}
	if gateway.calls != 0 {
		t.Fatal("draft reached engine")
	}
	if code, _ = call("POST", "/api/tasks", "", map[string]string{"url": "http://unapproved.invalid"}); code != 404 {
		t.Fatal("legacy start route", code)
	}
	for _, claims := range []jwt.RegisteredClaims{
		{Subject: "wrong", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
		{Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))},
		{Subject: "ARTEX"},
	} {
		s := &Server{jwtKey: key}
		token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.signingKey(string(hash)))
		if err != nil {
			t.Fatal(err)
		}
		if code, _ = call("GET", diagnosisapi.BasePath+"/catalog", "", nil); code != 401 {
			t.Fatal("invalid claims", code)
		}
	}
	token = validToken
	if code, _ = call("GET", diagnosisapi.BasePath+"/catalog?token=ignored", "", nil); code != 400 {
		t.Fatal("query accepted", code)
	}
	newHash, _ := bcrypt.GenerateFromPassword([]byte("changed-fixture-password"), bcrypt.MinCost)
	if installer.SetSetting(authPassKey, string(newHash)) != nil {
		t.Fatal("fixture password rotation")
	}
	if code, _ = call("GET", diagnosisapi.BasePath+"/catalog", "", nil); code != 401 {
		t.Fatal("stale session", code)
	}
	cancel()
	if code, _ = call("GET", "/api/auth/status", "", nil); code != 503 {
		t.Fatal("closed service", code)
	}
}

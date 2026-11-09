package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

func TestProductCredentialsOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	var schema string
	if f.Installer.QueryRow(`SELECT current_schema()`).Scan(&schema) != nil {
		t.Fatal("fixture schema")
	}
	role := "writer_" + strings.TrimPrefix(schema, "local_host_")
	ident := pgx.Identifier{role}.Sanitize()
	exec := func(query string) {
		t.Helper()
		if _, e := f.Installer.Exec(query); e != nil {
			t.Fatal("fixture privilege setup", e)
		}
	}
	exec("CREATE ROLE " + ident + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'owned-product-writer'")
	t.Cleanup(func() { f.Installer.Exec("DROP OWNED BY " + ident); f.Installer.Exec("DROP ROLE " + ident) })
	exec("GRANT USAGE ON SCHEMA " + pgx.Identifier{schema}.Sanitize() + " TO " + ident)
	exec("GRANT SELECT(id,model,base_url,format),UPDATE(api_key) ON llm_profiles TO " + ident)
	cfg, e := pgx.ParseConfig(os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_DSN"))
	if e != nil {
		t.Fatal("fixture DSN")
	}
	cfg.User, cfg.Password = role, "owned-product-writer"
	cfg.RuntimeParams["search_path"] = schema
	writer := stdlib.OpenDB(*cfg)
	defer writer.Close()
	installer := &pgdb.DB{DB: f.Installer}
	password := "owned-product-login-password"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("fixture auth")
	}
	profile := &pgdb.LLMProfile{Name: "owned-external", Format: "openai", Model: llmpolicy.OpenRouterModel, BaseURL: llmpolicy.OpenRouterBaseURL}
	id, e := installer.SaveProfile(profile)
	if e != nil {
		t.Fatal("fixture profile")
	}
	keys, data := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keys, jwtKeyFilename), []byte("owned-synthetic-product-jwt-key-at-least-32-bytes"), 0600) != nil {
		t.Fatal("fixture key")
	}
	endpoint := &urlManagementEndpoint{}
	revoked := false
	binding := ProductModelBinding{ProfileRef: "installed-external", ProfileID: id, Model: llmpolicy.OpenRouterModel, Check: func() error {
		if revoked {
			return errors.New("revoked")
		}
		return nil
	}}
	handler, e := NewProductDiagnosisHandler(t.Context(), f.Runtime, keys, data, endpoint, "", writer, []ProductModelBinding{binding})
	if e != nil {
		t.Fatal("product assembly", e)
	}
	request := func(path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	login, _ := json.Marshal(map[string]string{"username": "ARTEX", "password": password})
	w := request("/api/auth/login", string(login), "")
	var session struct{ Token string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("product login")
	}
	secret := "owned-synthetic-provider-key"
	body, _ := json.Marshal(map[string]string{"profile_id": binding.ProfileRef, "model": binding.Model, "api_key": secret})
	for _, token := range []string{"", "invalid"} {
		if request("/api/product/model-credentials", string(body), token).Code != 401 {
			t.Fatal("unauthenticated key update")
		}
	}
	w = request("/api/product/model-credentials", string(body), session.Token)
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential registration response")
	}
	var stored string
	if f.Installer.QueryRow(`SELECT api_key FROM llm_profiles WHERE id=$1`, id).Scan(&stored) != nil || stored != secret {
		t.Fatal("key not stored")
	}
	if writer.QueryRow(`SELECT api_key FROM llm_profiles WHERE id=$1`, id).Scan(&stored) == nil {
		t.Fatal("writer can read model key")
	}
	for _, changed := range []string{strings.Replace(string(body), binding.Model, "unapproved-model", 1), strings.Replace(string(body), binding.ProfileRef, "other-profile", 1), strings.TrimSuffix(string(body), "}") + `,"base_url":"https://other.invalid"}`} {
		if request("/api/product/model-credentials", changed, session.Token).Code != 400 {
			t.Fatal("unapproved model settings accepted")
		}
	}
	revoked = true
	if request("/api/product/model-credentials", string(body), session.Token).Code != 409 {
		t.Fatal("revoked binding accepted")
	}
	if endpoint.calls.Load() != 0 {
		t.Fatal("key registration invoked preparation or execution")
	}
	if _, e = NewProductDiagnosisHandler(context.Background(), f.Runtime, keys, data, endpoint, "", f.Installer, []ProductModelBinding{binding}); e == nil {
		t.Fatal("privileged writer accepted")
	}
}

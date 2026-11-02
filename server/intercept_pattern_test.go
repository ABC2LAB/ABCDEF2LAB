package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestInterceptPatternErrorsArePrivateAndTranslated(t *testing.T) {
	const privatePattern = "(?P<synthetic-private-pattern"
	err := validateInterceptRuleReq(interceptRuleReq{Name: "fixture", MatchTarget: "tool_name", MatchType: "regex", Pattern: privatePattern, Action: "deny"})
	if err != db.ErrInvalidInterceptPattern || strings.Contains(err.Error(), privatePattern) {
		t.Fatal("HTTP validation exposed the pattern or used a different error contract")
	}
	for _, message := range []string{err.Error(), intercept.ErrConfigurationUnavailable.Error()} {
		for _, language := range []string{"en", "zh", "es", "ja"} {
			if errCatalog[message][language] == "" {
				t.Fatalf("missing configuration error translation: %s", language)
			}
		}
	}
}

func TestInterceptPatternHTTPRejectsInvalidWritesAndEnable(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	t.Cleanup(func() { _ = d.Close() })
	s := &Server{m: &Manager{pg: d, interceptor: intercept.New(d)}, jwtKey: []byte("synthetic-pattern-api-test-key")}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	const invalid = `{"name":"fixture","match_target":"tool_name","match_type":"regex","pattern":"(?P<synthetic-private-pattern","action":"deny","enabled":true}`
	if rec := request("POST", "/api/intercept/rules", invalid); rec.Code != 400 || strings.Contains(rec.Body.String(), "synthetic-private-pattern") {
		t.Fatal("invalid regex create was accepted or exposed its pattern")
	}
	rule, err := d.CreateInterceptRule(t.Name(), "tool_name", "regex", "^Bash$", "deny", "", 10, false, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
	path := fmt.Sprintf("/api/intercept/rules/%d", rule.ID)
	if rec := request("PUT", path, invalid); rec.Code != 400 || strings.Contains(rec.Body.String(), "synthetic-private-pattern") {
		t.Fatal("invalid regex update was accepted or exposed its pattern")
	}
	if _, err := d.Exec(`UPDATE intercept_rules SET pattern='(' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	rec := request("POST", path+"/toggle", `{"enabled":true}`)
	var body map[string]any
	if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["error"] != db.ErrInvalidInterceptPattern.Error() {
		t.Fatalf("invalid stored regex toggle did not return the fixed 400 error: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("POST", path+"/toggle", `{"enabled":false}`); rec.Code != 200 {
		t.Fatal("invalid stored regex could not be disabled through the API")
	}
	previous, existed, err := d.GetSetting("intercept_enabled_tools")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = d.SetSetting("intercept_enabled_tools", previous)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key='intercept_enabled_tools'`)
		}
	})
	for _, invalid := range []string{"{", "null"} {
		if err := d.SetSetting("intercept_enabled_tools", invalid); err != nil {
			t.Fatal(err)
		}
		rec := request("GET", "/api/intercept/tool-config", "")
		if rec.Code != 500 || !strings.Contains(rec.Body.String(), intercept.ErrConfigurationUnavailable.Error()) || strings.Contains(rec.Body.String(), "enabled_tools") {
			t.Fatal("malformed settings were presented as an empty successful configuration")
		}
	}
}

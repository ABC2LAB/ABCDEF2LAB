package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/config"
)

// DB 상태와 무관하게 지원하지 않는 언어의 오류 본문 계약을 확인한다.
func TestOutputLanguageUnsupportedResponseExplainsKoreanOnly(t *testing.T) {
	for _, language := range []string{"en", "zh", "es", "ja", "fr"} {
		t.Run(language, func(t *testing.T) {
			s := &Server{}
			req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"output_language":"`+language+`"}`))
			rec := httptest.NewRecorder()
			s.putSettings(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != "지원하지 않는 언어입니다(ko만 지원합니다)" {
				t.Fatalf("error=%q, want ko-only explanation", body.Error)
			}
		})
	}
}

// TestOutputLanguageCacheFollowsSettings pins that the cached output language
// (read by every writeErr and label) is refreshed by PUT /api/settings, so a
// switch applies to the very next response without re-reading the settings
// table per message.
func TestOutputLanguageCacheFollowsSettings(t *testing.T) {
	t.Setenv("ARTEX_LANG", "")
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	prev, hadPrev, _ := m.pg.GetSetting(settingOutputLanguage)
	t.Cleanup(func() { config.SetCurrentLanguage(nil) })
	// defer (not t.Cleanup) so the restore runs before the fixture closes its DB.
	defer func() {
		if hadPrev {
			_ = m.pg.SetSetting(settingOutputLanguage, prev)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingOutputLanguage)
		}
	}()
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingOutputLanguage)

	s := newTestServer(t, context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	errOf := func(rec *httptest.ResponseRecorder) string {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v.Error
	}

	if got := s.outputLanguage(); got != "ko" {
		t.Fatalf("default output language = %q, want ko", got)
	}
	// 기존 DB에 저장된 다른 언어 값은 따르지 않는다.
	if err := s.m.pg.SetSetting(settingOutputLanguage, "en"); err != nil {
		t.Fatal(err)
	}
	if got := s.loadOutputLanguage(); got != "ko" {
		t.Fatalf("stored en resolved to %q, want ko", got)
	}
	for _, language := range []string{"en", "fr"} {
		rec := do(http.MethodPut, "/api/settings", map[string]any{"output_language": language})
		if rec.Code != http.StatusBadRequest || errOf(rec) != errOutputLanguageInvalid {
			t.Fatalf("set %s status=%d body=%s", language, rec.Code, rec.Body.String())
		}
	}
	if rec := do(http.MethodPut, "/api/settings", map[string]any{"output_language": "ko"}); rec.Code != http.StatusOK {
		t.Fatalf("set ko status=%d body=%s", rec.Code, rec.Body.String())
	}
	var settings struct {
		OutputLanguage string `json:"output_language"`
	}
	_ = json.Unmarshal(do(http.MethodGet, "/api/settings", nil).Body.Bytes(), &settings)
	if settings.OutputLanguage != "ko" {
		t.Fatalf("GET /api/settings output_language = %q, want ko", settings.OutputLanguage)
	}
}

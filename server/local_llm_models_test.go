package server

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalOnlyModelListDoesNotSendFormOrStoredKey(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal("dedicated PostgreSQL unavailable")
	}
	t.Cleanup(func() { _ = m.Close() })
	var hits, keys atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.Contains(r.Header.Get("Authorization"), "synthetic-model-key") || r.Header.Get("x-api-key") == "synthetic-model-key" {
			keys.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"synthetic-model"}]}`)
	}))
	defer upstream.Close()
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("model-key-%d", time.Now().UnixNano()), Format: "openai", Model: "local:test", BaseURL: upstream.URL, APIKey: "synthetic-model-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id) })
	local := &Server{m: m, localResolver: localTestResolver(t, "http://127.0.0.1:1/v1")}
	external := &Server{m: m}
	for _, provider := range []string{"openai", "anthropic"} {
		for _, source := range []string{"form", "stored"} {
			t.Run(provider+"/"+source, func(t *testing.T) {
				payload := map[string]any{"provider": provider, "base_url": upstream.URL}
				if source == "form" {
					payload["api_key"] = "synthetic-model-key"
				} else {
					payload["profile_id"] = id
				}
				data, _ := json.Marshal(payload)
				invoke := func(s *Server) *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					s.pgListModels(w, httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader(string(data))))
					return w
				}
				// Prove this fixture reaches the legacy route and carries the chosen key.
				beforeHits, beforeKeys := hits.Load(), keys.Load()
				positive := invoke(external)
				if positive.Code != 200 || !strings.Contains(positive.Body.String(), `"ok":true`) || hits.Load() != beforeHits+1 || keys.Load() != beforeKeys+1 {
					t.Fatal("legacy fixture did not transmit the expected synthetic key")
				}
				beforeHits, beforeKeys = hits.Load(), keys.Load()
				blocked := invoke(local)
				if blocked.Code != 200 || !strings.Contains(blocked.Body.String(), `"ok":false`) || hits.Load() != beforeHits || keys.Load() != beforeKeys {
					t.Fatalf("local model list sent a key: status=%d hits=%d", blocked.Code, hits.Load()-beforeHits)
				}
				if strings.Contains(blocked.Body.String(), "synthetic-model-key") || strings.Contains(blocked.Body.String(), upstream.URL) {
					t.Fatal("refusal exposed request details")
				}
			})
		}
	}
}

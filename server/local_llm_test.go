package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

func localTestResolver(t *testing.T, baseURL string) *llmpolicy.ProfileResolver {
	return localTestResolverID(t, 7, baseURL)
}

func localTestResolverID(t *testing.T, id int64, baseURL string) *llmpolicy.ProfileResolver {
	t.Helper()
	data := fmt.Sprintf(`{"version":1,"profiles":[{"profile_id":%d,"format":"openai","model":"local:test","base_url":%q,"addresses":["127.0.0.1"]}]}`, id, baseURL)
	r, err := llmpolicy.ParseProfileManifest([]byte(data))
	if err != nil {
		t.Fatalf("parse synthetic manifest: %v", err)
	}
	return r
}

func TestLocalOnlyProfileProviderAndNoFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected model request") }))
	defer upstream.Close()
	baseURL := upstream.URL + "/v1"
	s := &Server{localResolver: localTestResolver(t, baseURL)}
	cfg := agent.Config{Format: llm.FormatOpenAI, Model: "local:test", BaseURL: baseURL, APIKey: "synthetic"}
	provider, err := s.newProfileProvider(7, cfg)
	if err != nil || provider == nil {
		t.Fatalf("bound local provider: %v", err)
	}
	if s.poolForActive(7, provider, cfg) != provider || s.poolForBinding(7, provider, cfg) != provider {
		t.Fatal("local-only mode attached a fallback pool")
	}
	for name, changed := range map[string]agent.Config{
		"model": func() agent.Config { c := cfg; c.Model = "other"; return c }(),
		"url":   func() agent.Config { c := cfg; c.BaseURL += "/other"; return c }(),
		"proxy": func() agent.Config { c := cfg; c.Proxy = "http://127.0.0.1:1"; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			p, err := s.newProfileProvider(7, changed)
			if p != nil || err != ErrLocalLLMProfile {
				t.Fatalf("mismatched profile built provider: %v %v", p, err)
			}
		})
	}
}

func TestBlockedLocalProviderReturnsFixedError(t *testing.T) {
	p := blockedLocalProvider{}
	_, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{})
	if err != ErrLocalLLMProfile {
		t.Fatalf("complete error = %v", err)
	}
	count := 0
	for _, err := range p.Stream(context.Background(), llm.CompletionRequest{}) {
		count++
		if err != ErrLocalLLMProfile {
			t.Fatalf("stream error = %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("stream error count = %d", count)
	}
}

func TestLocalOnlyRejectsLegacyProbeAndWrite(t *testing.T) {
	var received atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer upstream.Close()
	s := &Server{localResolver: localTestResolver(t, upstream.URL+"/v1")}
	body := fmt.Sprintf(`{"provider":"openai","model":"local:test","base_url":%q,"api_key":"synthetic"}`, upstream.URL)
	probe := httptest.NewRecorder()
	s.testLLM(probe, httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(body)))
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"ok":false`) {
		t.Fatalf("probe status=%d body=%s", probe.Code, probe.Body.String())
	}
	write := httptest.NewRecorder()
	s.setLLM(write, httptest.NewRequest(http.MethodPost, "/api/llm", strings.NewReader(body)))
	if write.Code != http.StatusForbidden || received.Load() != 0 {
		t.Fatalf("legacy route status=%d received=%d", write.Code, received.Load())
	}
}

func TestLocalOnlyStartupRejectsMissingManifest(t *testing.T) {
	s, err := NewWithLocalLLM(context.Background(), nil, "", "", "", "/nonexistent/operator-pins.json")
	if s != nil || !errors.Is(err, ErrLocalLLMPolicy) || strings.Contains(fmt.Sprint(err), "/nonexistent") {
		t.Fatalf("startup did not fail closed: %v %v", s, err)
	}
}

func TestLocalOnlyStartupUsesPinnedActiveProfile(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("dedicated PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected model request") }))
	defer upstream.Close()
	baseURL := upstream.URL + "/v1"
	originalActive, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	profile := &db.LLMProfile{Name: fmt.Sprintf("local-startup-%d", time.Now().UnixNano()), Format: "openai", Model: "local:test", BaseURL: baseURL, APIKey: "synthetic"}
	id, err := m.pg.SaveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if originalActive != nil {
			_ = m.pg.SetActiveProfile(originalActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		}
		_ = m.pg.DeleteProfile(id)
	})
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	manifestDir := t.TempDir()
	if err := os.Chmod(manifestDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(manifestDir, "pins.json")
	manifest := fmt.Sprintf(`{"version":1,"profiles":[{"profile_id":%d,"format":"openai","model":"local:test","base_url":%q,"addresses":["127.0.0.1"]}]}`, id, baseURL)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pinned", "unpinned active", "env only"} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "unpinned active":
				profile.ID, profile.Model = id, "not-pinned"
				if _, err := m.pg.SaveProfile(profile); err != nil {
					t.Fatal(err)
				}
			case "env only":
				if _, err := m.pg.Exec(`UPDATE llm_profiles SET is_default=false`); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ARTEX_LLM_PROVIDER", "openai")
				t.Setenv("OPENAI_API_KEY", "synthetic-env-only")
				t.Setenv("ARTEX_LLM_MODEL", "local:test")
				t.Setenv("ARTEX_LLM_BASE_URL", baseURL)
				if _, ok := agent.FromEnv(); !ok {
					t.Fatal("env fixture does not build a legacy config")
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			s, err := NewWithLocalLLM(ctx, m, t.TempDir(), t.TempDir(), t.TempDir(), manifestPath)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			t.Cleanup(func() { cancel(); drainTestServer(t, s) })
			if s.localResolver == nil {
				t.Fatal("startup discarded the local policy resolver")
			}
			write := httptest.NewRecorder()
			s.setLLM(write, httptest.NewRequest(http.MethodPost, "/api/llm", strings.NewReader(`{}`)))
			if write.Code != http.StatusForbidden {
				t.Fatalf("startup accepted legacy settings: %d", write.Code)
			}
			_, _, global := s.globalProvider()
			chat := s.resolveChatAgent(&db.Conversation{})
			if name == "pinned" {
				if !global || chat == nil {
					t.Fatal("pinned startup did not install local providers")
				}
			} else if global || chat != nil || s.llmOn || s.llmProv != nil || s.chatAgent != nil {
				t.Fatal("untrusted active/env config made local engine ready")
			}
		})
	}

}

func TestLocalOnlyExplicitBindingAndStaleCache(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("dedicated PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected model request") }))
	defer upstream.Close()
	baseURL := upstream.URL + "/v1"
	profile := &db.LLMProfile{Name: fmt.Sprintf("local-policy-%d", time.Now().UnixNano()), Format: "openai", Model: "wrong-model", BaseURL: baseURL, APIKey: "synthetic"}
	id, err := m.pg.SaveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = m.pg.SetAgentLLMProfile("worker", nil)
		_ = m.pg.DeleteProfile(id)
	})
	if err := m.pg.SetAgentLLMProfile("worker", &id); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTaskWithOptions("local policy", "do not send", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	s := &Server{m: m, localResolver: localTestResolverID(t, id, baseURL), provByProfile: map[int64]*provEntry{}, profChatAgents: map[int64]*agent.ChatAgent{}}
	s.chatAgent = &agent.ChatAgent{} // a global fallback must not mask an invalid explicit choice
	provider, _, ok := s.agentBindingProvider("worker")
	if _, blocked := provider.(blockedLocalProvider); !ok || !blocked {
		t.Fatalf("invalid binding selected another provider: %T", provider)
	}
	if s.taskRuntimeAvailable(task, "worker") {
		t.Fatal("blocked binding reported task runnable")
	}
	resolution, err := s.resolveTaskRoleLLM(task, "worker")
	if err != nil || resolution.Available || resolution.Source != "agent_binding" {
		t.Fatalf("invalid binding resolution = %+v, %v", resolution, err)
	}
	if s.resolveChatAgent(&db.Conversation{AgentKey: "mainagent", LLMProfileID: &id}) != nil {
		t.Fatal("invalid explicit chat profile fell back to global")
	}
	profile.ID, profile.Model = id, "local:test"
	if _, err := m.pg.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	validProvider, cfg, ok := s.providerForProfile(id)
	if !ok {
		t.Fatal("valid local profile did not build")
	}
	if s.chatAgentForProfile(id) == nil {
		t.Fatal("valid local chat profile did not build")
	}
	originalActive, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if originalActive != nil {
			_ = m.pg.SetActiveProfile(originalActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		}
	})
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	s.llmProv, s.llmCfg, s.llmOn = validProvider, cfg, true
	if _, _, ok := s.globalProvider(); !ok {
		t.Fatal("valid active local profile not available globally")
	}
	profile.Model = "changed-after-cache"
	if _, err := m.pg.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.providerForProfile(id); ok {
		t.Fatal("cached provider survived DB tuple change")
	}
	if s.chatAgentForProfile(id) != nil {
		t.Fatal("cached chat agent survived DB tuple change")
	}
	if _, _, ok := s.globalProvider(); ok {
		t.Fatal("global provider survived DB tuple change")
	}
	if s.resolveChatAgent(&db.Conversation{}) != nil {
		t.Fatal("global chat agent survived DB tuple change")
	}
}

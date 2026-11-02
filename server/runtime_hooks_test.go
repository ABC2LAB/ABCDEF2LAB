package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

func TestNewServersKeepTheirRuntimeHooks(t *testing.T) {
	if _, _, err := db.DSN(); err != nil {
		t.Skip("set ARTEX_PG_DSN to a dedicated PostgreSQL test database")
	}
	previousLanguage := config.SetCurrentLanguage(nil)
	previousNotify := notify.SetLanguageResolver(nil)
	t.Cleanup(func() {
		config.SetCurrentLanguage(previousLanguage)
		notify.SetLanguageResolver(previousNotify)
	})
	first, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal("dedicated test manager unavailable")
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal("second test manager unavailable")
	}
	t.Cleanup(func() { _ = second.Close() })
	planner, err := first.pg.GetAgentByKey("planner")
	if err != nil || planner == nil {
		t.Fatal("planner fixture unavailable")
	}
	previous, err := first.pg.AgentSkillNames(planner.ID)
	if err != nil {
		t.Fatal(err)
	}
	const directoryName = "owned-runtime-fixture"
	if err := first.pg.SetAgentSkillVisibility(planner.ID, []string{directoryName}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.pg.SetAgentSkillVisibility(planner.ID, previous) })
	makeServer := func(manager *Manager, owner string) *Server {
		t.Helper()
		skillRoot := t.TempDir()
		directory := filepath.Join(skillRoot, directoryName)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: owner-" + owner + "\ndescription: synthetic owner fixture\n---\nINSTRUCTIONS-" + owner
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		return New(ctx, manager, skillRoot, t.TempDir(), t.TempDir())
	}
	a := makeServer(first, "A")
	b := makeServer(second, "B")
	a.outputLang.Store("en")
	b.outputLang.Store("ja")
	for _, tc := range []struct {
		server *Server
		owner  string
		lang   string
	}{{a, "A", "en"}, {b, "B", "ja"}, {a, "A", "en"}} {
		hooks := agent.RuntimeHooksFrom(tc.server.ctx)
		if hooks.OutputLanguage == nil || hooks.OutputLanguage() != tc.lang || hooks.PromptOverride == nil || hooks.ToolResolve == nil {
			t.Fatal("server startup lost its owned runtime callbacks")
		}
		tools, _, cleanup := agent.AugmentTools(tc.server.ctx, "planner", nil)
		found := false
		for _, tool := range tools {
			if tool.Name() != "Skill" {
				continue
			}
			args, err := json.Marshal(map[string]string{"name": "owner-" + tc.owner})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tool.Call(tc.server.ctx, args, nil)
			guidance, encodeErr := json.Marshal(result.Extra)
			if err != nil || encodeErr != nil || result.IsError || !strings.Contains(string(guidance), "INSTRUCTIONS-"+tc.owner) {
				t.Fatal("server used another owner's skill directory")
			}
			found = true
		}
		cleanup()
		if !found {
			t.Fatal("owned skill was not assembled")
		}
	}
}

func TestEngineRuntimeContextKeepsOwnerAndParent(t *testing.T) {
	hooks := agent.RuntimeHooks{OutputLanguage: func() string { return "en" }}
	engine := &Engine{runtimeHooks: &hooks}
	parent, cancel := context.WithCancel(t.Context())
	parent = agent.WithRunInfo(parent, agent.RunInfo{TaskID: 17, ExplorationID: 29})
	parent = agent.WithRuntimeHooks(parent, agent.RuntimeHooks{OutputLanguage: func() string { return "ja" }})
	owned := engine.withRuntimeHooks(parent)
	if agent.RuntimeHooksFrom(owned).OutputLanguage() != "en" || agent.RunInfoFrom(owned).TaskID != 17 {
		t.Fatal("engine did not bind its owner or lost parent attribution")
	}
	cancel()
	if owned.Err() != context.Canceled {
		t.Fatal("runtime binding lost parent cancellation")
	}
	if agent.RuntimeHooksFrom((&Engine{}).withRuntimeHooks(parent)).OutputLanguage() != "ja" {
		t.Fatal("standalone engine overwrote its caller's runtime")
	}
}

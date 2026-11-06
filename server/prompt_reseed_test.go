package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func newPromptReseedServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { pg.Close() })
	return &Server{ctx: context.Background(), m: &Manager{pg: pg}}, pg
}

// clearSetting 은 시험 전에 1회성 플래그를 지워, 앞선 시험이나 기동 시드가 세운 값과 무관하게 돌게 한다.
func clearSetting(t *testing.T, pg *db.DB, key string) {
	t.Helper()
	if err := pg.SetSetting(key, "false"); err != nil {
		t.Fatal(err)
	}
}

// 기존 DB의 옛 본문은 새 버전으로 바뀌고 이전 본문은 버전 기록에 남아야 한다. 두 번째 호출은
// 플래그로 막혀 버전을 더 만들지 않아야 한다.
func TestReseedBuiltinPromptsReplacesOldBodyOnce(t *testing.T) {
	s, pg := newPromptReseedServer(t)
	seedPrompts(pg)
	const oldBody = legacyPlannerPromptFixture
	a, err := pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("planner agent: %v, %v", a, err)
	}
	if _, err := pg.SavePrompt(a.ID, oldBody, "test", "test"); err != nil {
		t.Fatal(err)
	}
	clearSetting(t, pg, "builtin_prompts_english_v1")

	s.reseedBuiltinPrompts()

	cur, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur != agent.BuiltinPromptSeeds()["planner"] {
		t.Fatalf("planner prompt not refreshed: %q", cur[:40])
	}
	versions, err := pg.ListPromptVersions(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, v := range versions {
		kept = kept || v.Template == oldBody
	}
	if !kept {
		t.Fatal("old prompt body must stay in version history")
	}

	if _, err := pg.SavePrompt(a.ID, oldBody, "test", "test"); err != nil {
		t.Fatal(err)
	}
	s.reseedBuiltinPrompts()
	cur, err = pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur != oldBody {
		t.Fatal("reseed must run once: the flag should keep a later edit")
	}
	if _, err := pg.ResetPromptToDefault(a.ID, agent.BuiltinPromptSeeds()["planner"]); err != nil {
		t.Fatal(err)
	}
}

// 이미 기본값이면 버전을 늘리지 않아야 한다(새 DB에서 시드 직후 호출되는 경로).
func TestReseedBuiltinPromptsSkipsCurrentDefault(t *testing.T) {
	s, pg := newPromptReseedServer(t)
	seedPrompts(pg)
	a, err := pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		t.Fatalf("worker agent: %v, %v", a, err)
	}
	if _, err := pg.ResetPromptToDefault(a.ID, agent.BuiltinPromptSeeds()["worker"]); err != nil {
		t.Fatal(err)
	}
	before, err := pg.ListPromptVersions(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	clearSetting(t, pg, "builtin_prompts_english_v1")

	s.reseedBuiltinPrompts()

	after, err := pg.ListPromptVersions(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("versions %d -> %d, want unchanged", len(before), len(after))
	}
}

// 정확한 옛 기본값만 바뀌고, 사용자 문구와 다른 도구의 트리거는 그대로여야 한다.
func TestUpgradeReporterTriggerMessageReplacesKnownDefaultsOnly(t *testing.T) {
	s, pg := newPromptReseedServer(t)
	const edited = "운영자가 고친 문구"
	legacy, err := pg.CreateTrigger(&db.AgentTrigger{AgentKey: "reporter", Enabled: false, OnToolCall: true,
		ToolNames: []string{"report_finding"}, ToolCallMessage: legacyReporterTriggerFixtures[0]})
	if err != nil {
		t.Fatal(err)
	}
	type preservedTrigger struct {
		id      int64
		message string
	}
	var preserved []preservedTrigger
	for _, input := range []struct {
		message string
		tools   []string
	}{
		{"漏洞已登记", []string{"report_finding"}},
		{legacyReporterTriggerFixtures[1], []string{"other_tool"}},
	} {
		row, err := pg.CreateTrigger(&db.AgentTrigger{AgentKey: "reporter", Enabled: false, OnToolCall: true, ToolNames: input.tools, ToolCallMessage: input.message})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.DeleteTrigger(row.ID) })
		preserved = append(preserved, preservedTrigger{row.ID, input.message})
	}
	custom, err := pg.CreateTrigger(&db.AgentTrigger{AgentKey: "reporter", Enabled: false, OnToolCall: true,
		ToolNames: []string{"report_finding"}, ToolCallMessage: edited})
	if err != nil {
		t.Fatal(err)
	}
	clearSetting(t, pg, "reporter_trigger_english_v1")

	s.upgradeReporterTriggerMessage()

	got := map[int64]string{}
	triggers, err := pg.ListTriggersFor("reporter")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range triggers {
		got[tr.ID] = tr.ToolCallMessage
	}
	if got[legacy.ID] != reporterToolCallMessage {
		t.Fatalf("legacy message not upgraded: %q", got[legacy.ID])
	}
	if got[custom.ID] != edited {
		t.Fatalf("edited message changed: %q", got[custom.ID])
	}
	for _, row := range preserved {
		if got[row.id] != row.message {
			t.Fatalf("custom or unrelated trigger %d changed", row.id)
		}
	}
}

func TestReseedBuiltinPromptsPreservesCustomCurrentBody(t *testing.T) {
	s, pg := newPromptReseedServer(t)
	seedPrompts(pg)
	a, err := pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("planner: %v", err)
	}
	previous, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pg.SavePrompt(a.ID, previous, "restore test fixture", "test"); err != nil {
			t.Error(err)
		}
	})
	for _, body := range []string{"OLD PROMPT BODY", "운영자가 저장한 프롬프트", "用户自定义提示"} {
		if _, err := pg.SavePrompt(a.ID, body, "test", "test"); err != nil {
			t.Fatal(err)
		}
		before, err := pg.ListPromptVersions(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		clearSetting(t, pg, "builtin_prompts_english_v1")
		s.reseedBuiltinPrompts()
		cur, err := pg.CurrentPrompt(a.ID)
		if err != nil || cur != body {
			t.Fatalf("custom current changed: %q err=%v", cur, err)
		}
		after, err := pg.ListPromptVersions(a.ID)
		if err != nil || len(after) != len(before) {
			t.Fatal("custom prompt created a replacement version")
		}
	}
}

func TestLegacyPromptAndTriggerIdentity(t *testing.T) {
	if !isLegacyPromptDefault("planner", legacyPlannerPromptFixture) || isLegacyPromptDefault("worker", legacyPlannerPromptFixture) || isLegacyPromptDefault("planner", "OLD PROMPT BODY") {
		t.Fatal("legacy prompt identity mismatch")
	}
	for _, value := range legacyReporterTriggerFixtures {
		if !isLegacyReporterTrigger(value) || isLegacyReporterTrigger(value+" ") {
			t.Fatal("legacy trigger match is not exact")
		}
	}
	if isLegacyReporterTrigger("漏洞已登记") {
		t.Fatal("custom Han trigger was considered a default")
	}
}

func TestReporterToolCallMessageIsEnglish(t *testing.T) {
	if containsHan(reporterToolCallMessage) {
		t.Fatalf("trigger message must not contain Han: %q", reporterToolCallMessage)
	}
}

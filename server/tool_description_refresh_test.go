package server

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

// 한자가 든 시스템 도구 행만 코드 기본값으로 바뀌고, 운영자가 다른 언어로 고친 행과 바인딩·활성 여부는 유지돼야 한다.
func TestRefreshHanToolDescriptions(t *testing.T) {
	s, pg := newPromptReseedServer(t)
	seeds := map[string]agent.ToolSeed{}
	for _, sd := range agent.BuiltinToolSeeds() {
		schema, _ := json.Marshal(sd.Schema)
		agents, _ := json.Marshal(sd.Agents)
		if err := pg.SeedTool(sd.Key, sd.Desc, schema, agents); err != nil {
			t.Fatal(err)
		}
		seeds[sd.Key] = sd
	}
	const hanDesc, hanSchema, edited = "list_goals", "prove_goal", "list_facts"
	for _, key := range []string{hanDesc, hanSchema, edited} {
		old, err := pg.GetTool(key)
		if err != nil || old == nil {
			t.Fatalf("missing tool %s: %v", key, err)
		}
		t.Cleanup(func() {
			bindings, _ := json.Marshal(old.Agents)
			if err := pg.UpdateTool(key, old.Description, old.Schema, bindings, old.Enabled); err != nil {
				t.Error(err)
			}
		})
	}
	bindings := json.RawMessage(`["custom-agent"]`)
	plainSchema := json.RawMessage(`{"type":"object"}`)
	if err := pg.UpdateTool(hanDesc, "列出目标", plainSchema, bindings, false); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpdateTool(hanSchema, "plain description", json.RawMessage(`{"type":"object","description":"目标节点"}`), bindings, true); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpdateTool(edited, "OPERATOR EDIT", plainSchema, bindings, true); err != nil {
		t.Fatal(err)
	}
	clearSetting(t, pg, "tool_descriptions_english_v1")

	s.refreshHanToolDescriptions()

	row, err := pg.GetTool(hanDesc)
	if err != nil || row.Description != seeds[hanDesc].Desc {
		t.Fatalf("Han description not refreshed: %+v err=%v", row, err)
	}
	if row.Enabled || len(row.Agents) != 1 || row.Agents[0] != "custom-agent" {
		t.Fatalf("binding or enabled flag changed: %+v", row)
	}
	row, err = pg.GetTool(hanSchema)
	if err != nil || row.Description != seeds[hanSchema].Desc || containsHan(string(row.Schema)) {
		t.Fatalf("Han schema not refreshed: %+v err=%v", row, err)
	}
	row, err = pg.GetTool(edited)
	if err != nil || row.Description != "OPERATOR EDIT" {
		t.Fatalf("operator edit overwritten: %+v err=%v", row, err)
	}

	if err := pg.UpdateTool(hanDesc, "列出目标", plainSchema, bindings, true); err != nil {
		t.Fatal(err)
	}
	s.refreshHanToolDescriptions()
	row, err = pg.GetTool(hanDesc)
	if err != nil || row.Description != "列出目标" {
		t.Fatalf("refresh must run once: %+v err=%v", row, err)
	}
}

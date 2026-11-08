package server

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/traffic"
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
	if err := pg.UpdateTool(hanDesc, legacyToolFixtures[hanDesc].Description, plainSchema, bindings, false); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpdateTool(hanSchema, "plain description", legacyToolFixtures[hanSchema].Schema, bindings, true); err != nil {
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
	var plain map[string]any
	if err := json.Unmarshal(row.Schema, &plain); err != nil || len(plain) != 1 || plain["type"] != "object" {
		t.Fatal("custom schema was overwritten")
	}
	row, err = pg.GetTool(hanSchema)
	if err != nil || row.Description != "plain description" || containsHan(string(row.Schema)) {
		t.Fatalf("Han schema not refreshed: %+v err=%v", row, err)
	}
	row, err = pg.GetTool(edited)
	if err != nil || row.Description != "OPERATOR EDIT" {
		t.Fatalf("operator edit overwritten: %+v err=%v", row, err)
	}

	if err := pg.UpdateTool(hanDesc, legacyToolFixtures[hanDesc].Description, plainSchema, bindings, true); err != nil {
		t.Fatal(err)
	}
	s.refreshHanToolDescriptions()
	row, err = pg.GetTool(hanDesc)
	if err != nil || row.Description != legacyToolFixtures[hanDesc].Description {
		t.Fatalf("refresh must run once: %+v err=%v", row, err)
	}
}

func TestRefreshOlderTrafficSearchDescriptionPreservesCustomSchema(t *testing.T) {
	s, pg := newPromptReseedServer(t)
	const key = "traffic_search"
	previous, err := pg.GetTool(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if previous == nil {
			_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
			return
		}
		bindings, _ := json.Marshal(previous.Agents)
		if err := pg.UpdateTool(key, previous.Description, previous.Schema, bindings, previous.Enabled); err != nil {
			t.Error(err)
		}
	})
	custom := json.RawMessage(`{"type":"object","description":"운영자 스키마"}`)
	if err := pg.SeedTool(key, legacyTrafficSearchDescriptionFixture, custom, json.RawMessage(`["custom-agent"]`)); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpdateTool(key, legacyTrafficSearchDescriptionFixture, custom, json.RawMessage(`["custom-agent"]`), false); err != nil {
		t.Fatal(err)
	}
	clearSetting(t, pg, "tool_descriptions_english_v1")
	s.refreshHanToolDescriptions()
	row, err := pg.GetTool(key)
	if err != nil {
		t.Fatal(err)
	}
	if row.Description != traffic.TrafficSearchDescription || row.Enabled || len(row.Agents) != 1 || row.Agents[0] != "custom-agent" {
		t.Fatal("older traffic_search description or user options were lost")
	}
	var schema map[string]any
	if err := json.Unmarshal(row.Schema, &schema); err != nil || schema["description"] != "운영자 스키마" {
		t.Fatal("custom traffic_search schema changed")
	}
}

func TestLegacyToolHashesMatchExactFields(t *testing.T) {
	for key, fixture := range legacyToolFixtures {
		if !isLegacyToolDescription(key, fixture.Description) || isLegacyToolDescription(key, fixture.Description+" ") || isLegacyToolDescription(key, "用户自定义说明") {
			t.Fatalf("description identity mismatch for %s", key)
		}
		var value any
		if err := json.Unmarshal(fixture.Schema, &value); err != nil {
			t.Fatal(err)
		}
		indented, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got, err := legacySchemaHash(indented)
		if err != nil || got != legacyToolHashes[key].Schema {
			t.Fatalf("JSONB schema identity mismatch for %s: %s err=%v", key, got, err)
		}
	}
	if !isLegacyToolDescription("traffic_search", legacyTrafficSearchDescriptionFixture) {
		t.Fatal("older traffic_search default was lost")
	}
	a, err := legacySchemaHash(json.RawMessage(`{"b":2,"a":{"z":"x","c":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := legacySchemaHash(json.RawMessage(`{ "a": { "c":1, "z":"x" }, "b":2 }`))
	if err != nil || a != b {
		t.Fatal("schema normalization depends on JSONB key order")
	}
	if _, err := legacySchemaHash(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid stored schema was accepted")
	}
}

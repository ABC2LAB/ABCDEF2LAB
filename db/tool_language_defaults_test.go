package db

import (
	"encoding/json"
	"testing"
)

func TestRefreshToolLanguageDefaultsPreservesConcurrentFieldEdits(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("전용 PostgreSQL 필요: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	const key = "language_cas_fixture"
	oldSchema := json.RawMessage(`{"description":"old schema"}`)
	newSchema := json.RawMessage(`{"description":"new schema"}`)
	bindings := json.RawMessage(`["custom-agent"]`)
	if err := d.SeedTool(key, "old description", oldSchema, bindings); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM tools WHERE key=$1`, key) })
	if err := d.UpdateTool(key, "operator description", oldSchema, bindings, false); err != nil {
		t.Fatal(err)
	}
	if err := d.RefreshToolLanguageDefaults(key, "old description", oldSchema, "new description", newSchema, true, true); err != nil {
		t.Fatal(err)
	}
	row, err := d.GetTool(key)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(row.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if row.Description != "operator description" || schema["description"] != "new schema" || row.Enabled || len(row.Agents) != 1 || row.Agents[0] != "custom-agent" {
		t.Fatalf("independent field update lost user edits: %+v", row)
	}
	customSchema := json.RawMessage(`{"description":"operator schema"}`)
	if err := d.UpdateTool(key, "operator description", customSchema, bindings, false); err != nil {
		t.Fatal(err)
	}
	if err := d.RefreshToolLanguageDefaults(key, "operator description", oldSchema, "new description", newSchema, false, true); err != nil {
		t.Fatal(err)
	}
	row, err = d.GetTool(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(row.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if row.Description != "operator description" || schema["description"] != "operator schema" {
		t.Fatal("stale migration overwrote a concurrent schema edit")
	}
}

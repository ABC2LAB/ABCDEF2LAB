package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestE2FactAssetReferenceChecksTaskScope(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("PostgreSQL test configuration is unavailable")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal("PostgreSQL test connection failed")
	}
	defer pg.Close()
	taskOf := func() *db.Task {
		task, err := pg.CreateTask("synthetic fact asset scope", "test", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	a, b := taskOf(), taskOf()
	assetOf := func(taskID int64) int64 {
		var id int64
		if err := pg.QueryRow(`INSERT INTO assets(type,task_ids) VALUES ('endpoint',ARRAY[$1::bigint]) RETURNING id`, taskID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	valid, foreign := assetOf(a.ID), assetOf(b.ID)
	store := pg.Exploration(a.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "synthetic observation"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	worker := newE2HTTPWorkerToolSet(store, nil, "fixture-worker", a.ID, intent)
	write := func(worker *ToolSet, ids string) (int64, error) {
		var item factItem
		if err := json.Unmarshal([]byte(`{"summary":"fixture observation","asset_ids":`+ids+"}"), &item); err != nil {
			t.Fatal(err)
		}
		return worker.recordOneFact(item, intent)
	}
	for _, tc := range []struct{ name, ids string }{
		{"missing", "[9223372036854775807]"},
		{"other task", fmt.Sprintf("[%d,%d]", valid, foreign)},
		{"invalid syntax", "[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := write(worker, tc.ids)
			if id != 0 || err == nil || err.Error() != "asset_reference_invalid" {
				t.Fatal("unbound reference was stored or returned a storage-level error")
			}
		})
	}
	wrong := newE2HTTPWorkerToolSet(store, nil, "fixture-worker", b.ID, intent)
	if id, err := write(wrong, fmt.Sprintf("[%d]", foreign)); id != 0 || err == nil || err.Error() != "asset_reference_invalid" {
		t.Fatal("mismatched task and exploration were accepted")
	}
	id, err := write(worker, fmt.Sprintf("[%d]", valid))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pg.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, id, valid).Scan(&count); err != nil || count != 1 {
		t.Fatal("valid association was not stored")
	}
}

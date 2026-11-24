package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	t.Cleanup(func() { pg.Close() })
	taskOf := func() *db.Task {
		task, err := pg.CreateTask("synthetic fact asset scope", "test", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := pg.DeleteTask(task.ID); err != nil {
				t.Errorf("시험 작업 정리 실패: %v", err)
			}
		})
		return task
	}
	a, b, source := taskOf(), taskOf(), taskOf()
	if _, err := pg.Exec(`INSERT INTO task_relations(task_id, source_task_id) VALUES ($1,$2)`, a.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	assetOf := func(taskID int64, domain string) int64 {
		var id int64
		if err := pg.QueryRow(`INSERT INTO assets(type,task_ids,root_domain) VALUES ('endpoint',ARRAY[$1::bigint],$2) RETURNING id`, taskID, domain).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := pg.Exec(`DELETE FROM assets WHERE id=$1`, id); err != nil {
				t.Errorf("시험 자산 정리 실패: %v", err)
			}
		})
		return id
	}
	ownDomain := fmt.Sprintf("current-%d.test", a.ID)
	sourceDomain := fmt.Sprintf("source-%d.test", source.ID)
	for task, domain := range map[int64]string{a.ID: ownDomain, source.ID: sourceDomain} {
		if _, err := pg.Assets().AddAgentScope(task, "root_domain", domain, "합성 시험 범위", "agent"); err != nil {
			t.Fatal(err)
		}
	}
	valid, foreign := assetOf(a.ID, ""), assetOf(b.ID, "")
	previous, inherited := assetOf(b.ID, ownDomain), assetOf(source.ID, sourceDomain)
	visible, err := pg.Assets().GetByIDsInScope(a.ID, []int64{previous, inherited, foreign})
	if err != nil || len(visible) != 2 {
		t.Fatalf("도구가 읽는 선언 범위 시험 자료: %v, %v", len(visible), err)
	}
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
			if id != 0 || err == nil || err.Error() != db.ErrFactAssetReference.Error() || !strings.Contains(err.Error(), "list_assets") {
				t.Fatal("unbound reference was stored or returned a storage-level error")
			}
		})
	}
	wrong := newE2HTTPWorkerToolSet(store, nil, "fixture-worker", b.ID, intent)
	if id, err := write(wrong, fmt.Sprintf("[%d]", foreign)); id != 0 || err == nil || err.Error() != db.ErrFactAssetReference.Error() || !strings.Contains(err.Error(), "list_assets") {
		t.Fatal("mismatched task and exploration were accepted")
	}
	tooMany := "[" + strings.Repeat(fmt.Sprintf("%d,", valid), db.MaxTaskAssetMutationCount) + fmt.Sprint(valid) + "]"
	if id, err := write(worker, tooMany); id != 0 || !errors.Is(err, db.ErrFactAssetLimit) {
		t.Fatalf("자산 수 상한 오류: id=%d err=%v", id, err)
	}
	id, err := write(worker, fmt.Sprintf("[%d,%d,%d]", valid, previous, inherited))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pg.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id IN ($2,$3,$4)`, id, valid, previous, inherited).Scan(&count); err != nil || count != 3 {
		t.Fatal("valid association was not stored")
	}
}

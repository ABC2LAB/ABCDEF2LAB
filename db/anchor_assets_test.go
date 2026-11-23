package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// 모델이 준 자산 번호가 자산 그래프에 없으면 노드를 만들기 전에 거부하고, 외래 키 오류 대신 영어 도구 오류를 돌려준다
// (11/23 VAmPI 회차 exploration_anchors 외래 키 위반 4건). 있는 번호만이면 그대로 저장한다.
func TestAnchorAssetsRejectUnknownBeforeSave(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask("anchor asset check", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("anchor-%d.example.test", time.Now().UnixNano()),
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
	var missing int64
	if err := d.QueryRow(`SELECT COALESCE(max(id),0)+1000 FROM assets`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	nodes := func() int {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	anchors := func(node int64) []int64 {
		rows, err := d.Query(`SELECT asset_id FROM exploration_anchors WHERE node_id=$1 ORDER BY asset_id`, node)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	unknown := func(t *testing.T, err error, want []int64) {
		t.Helper()
		var u *UnknownAssetsError
		if !errors.As(err, &u) || !slices.Equal(u.IDs, want) {
			t.Fatalf("없는 자산 번호 오류여야 함: %v", err)
		}
		// 모델에 가는 문구는 외래 키·표 이름이 아니라 고칠 방법이다.
		if msg := err.Error(); strings.Contains(msg, "foreign key") || strings.Contains(msg, "exploration_anchors") || !strings.Contains(msg, "list_assets") {
			t.Fatalf("도구 오류 문구: %q", msg)
		}
	}

	before := nodes()
	_, err = store.AddNode(KindFact, map[string]any{"summary": "x"}, 5, "confirmed", "worker", []int64{assetID, missing, missing})
	unknown(t, err, []int64{missing})
	_, err = store.AddIntent(map[string]any{"summary": "x"}, 5, []int64{missing}, "planner")
	unknown(t, err, []int64{missing})
	_, err = store.RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, Worker: "worker",
		VulnClass: "IDOR", Severity: "high", Summary: "x", AssetIDs: []int64{missing}})
	unknown(t, err, []int64{missing})
	if got := nodes(); got != before {
		t.Fatalf("거부한 호출이 노드를 남김: %d → %d", before, got)
	}

	// 있는 번호만이면 저장하고 앵커도 남는다.
	fact, err := store.AddNode(KindFact, map[string]any{"summary": "x"}, 5, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	if got := anchors(fact); !slices.Equal(got, []int64{assetID}) {
		t.Fatalf("앵커: %v", got)
	}
	recorded, err := store.RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, Worker: "worker",
		VulnClass: "IDOR", Severity: "high", Summary: "x", AssetIDs: []int64{assetID}})
	if err != nil {
		t.Fatal(err)
	}
	if got := anchors(recorded.NodeID); !slices.Equal(got, []int64{assetID}) {
		t.Fatalf("취약점 앵커: %v", got)
	}

	// 단건 Anchor도 없는 번호를 거부하고, 있는 번호는 넣는다.
	unknown(t, store.Anchor(fact, missing), []int64{missing})
	if err := store.Anchor(recorded.NodeID, assetID); err != nil {
		t.Fatal(err)
	}
}

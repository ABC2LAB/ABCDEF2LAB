package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// 확인한 자산 행은 앵커 INSERT까지 잠겨, 그 사이 자산 삭제가 커밋되지 못하고 기다린다(#881 이동찬 리뷰: 삭제 경쟁).
func TestAnchorAssetsCheckLocksAgainstConcurrentDelete(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("anchor lock", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("anchor-lock-%d.example.test", time.Now().UnixNano()), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id=$1`, assetID)

	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkAnchorAssets(tx, []int64{assetID}); err != nil {
		t.Fatal(err)
	}
	other, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback()
	if _, err := other.Exec(`SET LOCAL lock_timeout = '300ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("확인 뒤 자산 삭제가 잠금을 기다리지 않음: %v", err)
	}
}

// 잠금을 거치지 않은 경로에서 외래 키 위반이 나도 원시 DB 오류 대신 제약별 고정 오류로 돌려준다. 자산 키만 없는 자산 번호
// 오류이고, 노드 키는 노드 없음 오류다(#881 리뷰: 노드 쪽을 자산 없음으로 분류하지 않음).
func TestAnchorInsertErrorMapsForeignKeyViolation(t *testing.T) {
	var u *UnknownAssetsError
	if err := anchorInsertError(&pgconn.PgError{Code: "23503", ConstraintName: anchorAssetFKey}, 9); !errors.As(err, &u) || !slices.Equal(u.IDs, []int64{9}) {
		t.Fatalf("자산 외래 키 위반 변환: %v", err)
	}
	if err := anchorInsertError(&pgconn.PgError{Code: "23503", ConstraintName: anchorNodeFKey}, 9); !errors.Is(err, ErrAnchorNodeMissing) || errors.As(err, &u) {
		t.Fatalf("노드 외래 키 위반 변환: %v", err)
	}
	for _, other := range []*pgconn.PgError{{Code: "23505"}, {Code: "23503", ConstraintName: "other_fkey"}} {
		if err := anchorInsertError(other, 9); err != other {
			t.Fatalf("다른 오류를 바꿈: %v", err)
		}
	}
}

// 실제 DB에서도 두 제약을 구별한다: 없는 노드에 있는 자산을 붙이면 노드 없음, 있는 노드에 없는 자산을 붙이면 자산 없음이다.
func TestAnchorDistinguishesNodeAndAssetForeignKeys(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("anchor fkeys", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("anchor-fk-%d.example.test", time.Now().UnixNano()), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
	store := d.Exploration(task.ExplorationID)
	var missingNode, missingAsset int64
	if err := d.QueryRow(`SELECT COALESCE(max(id),0)+1000 FROM exploration_nodes`).Scan(&missingNode); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COALESCE(max(id),0)+1000 FROM assets`).Scan(&missingAsset); err != nil {
		t.Fatal(err)
	}
	var u *UnknownAssetsError
	if err := store.Anchor(missingNode, assetID); !errors.Is(err, ErrAnchorNodeMissing) || errors.As(err, &u) {
		t.Fatalf("없는 노드: %v", err)
	}
	node, err := store.AddNode(KindFact, map[string]any{"summary": "x"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Anchor(node, missingAsset); !errors.As(err, &u) || !slices.Equal(u.IDs, []int64{missingAsset}) {
		t.Fatalf("없는 자산: %v", err)
	}
}

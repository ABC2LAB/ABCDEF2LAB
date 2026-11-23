package db

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
)

// UnknownAssetsError는 모델이 준 자산 번호 가운데 자산 그래프에 없는 번호다. 탐색 노드(사실·의도·취약점)를 저장하기 전에
// 걸러, DB 외래 키 위반으로 그 기록만 빠지는 대신 모델에 도구 오류로 돌려준다(절대 규칙 4: LLM 출력은 데이터이고 없는 것을
// 가리키면 버린다). 메시지는 모델이 읽는 도구 결과라 영어다.
type UnknownAssetsError struct {
	IDs []int64
}

func (e *UnknownAssetsError) Error() string {
	return fmt.Sprintf("asset_ids %v do not exist, so nothing was saved. Pass only asset ids returned by list_assets (not exploration node ids), or omit asset_ids", e.IDs)
}

// checkAnchorAssets는 자산 번호가 모두 있는지 본다. 하나라도 없으면 *UnknownAssetsError이고, 호출자는 아무것도 저장하지
// 않고 멈춘다. 앵커 INSERT와 같은 트랜잭션에서 불러야 한다. 확인한 자산 행을 FOR KEY SHARE로 잡아, 확인 뒤·INSERT 전에
// 커밋되는 자산 삭제(작업·회사 삭제)가 트랜잭션이 끝날 때까지 기다리게 한다(#881 이동찬 리뷰: 삭제 경쟁으로 외래 키 오류가
// 모델에 다시 가는 경로).
func checkAnchorAssets(q rowsQuerier, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	want := slices.Clone(ids)
	slices.Sort(want)
	want = slices.Compact(want)
	rows, err := q.Query(`SELECT id FROM assets WHERE id = ANY($1::bigint[]) FOR KEY SHARE`, want)
	if err != nil {
		return err
	}
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var missing []int64
	for _, id := range want {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return &UnknownAssetsError{IDs: missing}
	}
	return nil
}

// 앵커 표의 두 외래 키 이름이다(PostgreSQL 기본 이름, db/schema.sql exploration_anchors).
const (
	anchorAssetFKey = "exploration_anchors_asset_id_fkey"
	anchorNodeFKey  = "exploration_anchors_node_id_fkey"
)

// ErrAnchorNodeMissing은 앵커를 붙일 탐색 노드가 없을 때다(노드 외래 키 위반). 자산이 없다는 안내와 구별한다(#881 이동찬
// 리뷰: 노드 쪽 위반을 자산 없음으로 바꾸면 있는 자산을 다시 찾으라는 잘못된 안내가 간다). 모델이 읽을 수 있어 영어다.
var ErrAnchorNodeMissing = errors.New("the exploration node for this anchor no longer exists, so nothing was saved")

// anchorInsertError는 앵커 INSERT 오류다. 확인과 잠금 뒤에도 외래 키 위반(23503)이 나면(잠금을 거치지 않은 경로 등) 원시 DB
// 오류 대신 제약별 고정 오류로 바꿔 모델에 표 이름·제약 이름이 가지 않게 한다. 자산 키 위반만 *UnknownAssetsError다.
func anchorInsertError(err error, assetID int64) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	switch pgErr.ConstraintName {
	case anchorAssetFKey:
		return &UnknownAssetsError{IDs: []int64{assetID}}
	case anchorNodeFKey:
		return ErrAnchorNodeMissing
	}
	return err
}

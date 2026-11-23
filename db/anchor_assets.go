package db

import (
	"fmt"
	"slices"
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
// 않고 멈춘다. 저장과 같은 트랜잭션에서 확인하도록 *sql.Tx도 받는다(rowsQuerier).
func checkAnchorAssets(q rowsQuerier, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	want := slices.Clone(ids)
	slices.Sort(want)
	want = slices.Compact(want)
	rows, err := q.Query(`SELECT id FROM assets WHERE id = ANY($1::bigint[])`, want)
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

package server

import (
	"encoding/json"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/pathmask"
)

// E2AttemptPatternsFile은 제품 실행의 시도별 화면용 경로 패턴이다(작업대 e2-flow의 path_pattern, 계약
// docs/contracts/workbench-live-data.md §4). 실행 상태 디렉터리에 감사 저널과 따로 쓴다. 감사 기록이 아니라 표시용이며,
// 행마다 시도 ID와 pathmask.Readable로 값 조각을 가린 경로만 남긴다(원문 경로·query·헤더·본문 없음). 쓰지 못한 행은 빠지고
// 화면은 그 시도의 path_pattern을 비운다. 평가 회차(Evaluation)에는 만들지 않는다(평가 경로 투영은 E2AttemptPathsFile).
const E2AttemptPatternsFile = "attempt-patterns.jsonl"

// E2AttemptPatternRow는 E2AttemptPatternsFile 한 줄이다.
type E2AttemptPatternRow struct {
	AttemptID   string `json:"attempt_id"`
	PathPattern string `json:"path_pattern"`
}

func e2AttemptPatternLine(r e2proxy.Receipt, escaped string) ([]byte, bool) {
	if escaped == "" || r.AttemptID == "" {
		return nil, false
	}
	row, err := json.Marshal(E2AttemptPatternRow{AttemptID: r.AttemptID, PathPattern: pathmask.Readable(escaped)})
	if err != nil {
		return nil, false
	}
	return append(row, '\n'), true
}

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

// e2AttemptPatternLine은 한 줄을 만든다. sensitive는 실행이 아는 민감 조각 판단이다(보호·가림 값, 시험 계정 사용자명).
// 판단할 수 없으면(nil) 줄을 쓰지 않는다 — 이름 조각을 원문으로 남기는 표시라 가림을 거치지 않은 줄은 남기지 않는다.
func e2AttemptPatternLine(r e2proxy.Receipt, escaped string, sensitive func(string) bool) ([]byte, bool) {
	if escaped == "" || r.AttemptID == "" || sensitive == nil {
		return nil, false
	}
	row, err := json.Marshal(E2AttemptPatternRow{AttemptID: r.AttemptID, PathPattern: pathmask.ReadableWith(escaped, sensitive)})
	if err != nil {
		return nil, false
	}
	return append(row, '\n'), true
}

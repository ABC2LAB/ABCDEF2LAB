package server

import (
	"encoding/json"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/pathmask"
)

// E2AttemptPatternsFile은 제품 실행의 시도별 화면용 경로 패턴이다(작업대 e2-flow의 path_pattern, 계약
// docs/contracts/workbench-live-data.md §4). 실행 상태 디렉터리에 감사 저널과 따로 쓴다. 감사 기록이 아니라 표시용이며,
// 행마다 시도 ID와 승인 틀에 있는 이름 조각만 남긴다(규칙 밖 원문 조각·query·헤더·본문 없음). 쓰지 못한 행은 빠지고
// 화면은 그 시도의 path_pattern을 비운다. 평가 회차(Evaluation)에는 만들지 않는다(평가 경로 투영은 E2AttemptPathsFile).
const E2AttemptPatternsFile = "attempt-patterns.jsonl"

// E2AcquireRecordFile은 실행이 E②를 잡았다는 기록이다(계약 docs/contracts/workbench-live-data.md §4, 2026-11-27 결정). E②를
// 잡을 때 화면용 저널(events.jsonl)보다 먼저 상태 디렉터리에 배타 생성·fsync하고, 남기지 못하면 E②를 잡지 않는다. 작업대
// e2-flow는 이 기록이 없을 때만 '저널 없음(none)'으로 답하고, 기록은 있는데 저널이 없으면(지워졌거나 만들지 못함) 무효로
// 본다. 감사 기록이 아니고 허용 판단·송신·계수에 영향이 없다.
const E2AcquireRecordFile = "e2-acquire.json"

// e2AcquireRecordVersion은 E2AcquireRecordFile의 형식이다.
const e2AcquireRecordVersion = "e2-acquire/v1"

// E2AttemptPatternRow는 E2AttemptPatternsFile 한 줄이다.
type E2AttemptPatternRow struct {
	AttemptID      string `json:"attempt_id"`
	PathPattern    string `json:"path_pattern"`
	DisplayVersion int    `json:"display_version,omitempty"`
}

func e2AttemptPatternLine(r e2proxy.Receipt, escaped, template string, sensitive func(string) bool) ([]byte, bool) {
	if escaped == "" || r.AttemptID == "" {
		return nil, false
	}
	row, err := json.Marshal(E2AttemptPatternRow{AttemptID: r.AttemptID, PathPattern: pathmask.DisplayWithTemplate(escaped, template, sensitive), DisplayVersion: 2})
	if err != nil {
		return nil, false
	}
	return append(row, '\n'), true
}

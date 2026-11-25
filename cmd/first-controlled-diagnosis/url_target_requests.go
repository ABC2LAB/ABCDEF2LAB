package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
)

// 작업대 '보낸 요청' 총계(2026-11-25 사용자 지시). 화면의 보낸 요청(diagnosisflow counts.sent)은 모델이 진단 단계에서
// 보낸 것만 센다. 대상에 나간 요청 전부를 보이도록 e2-flow 응답 한 번에 같은 시점의 내역을 싣는다:
//   - 모델·프로그램 로그인(인증 준비·세션 다시 받기)·확정 단계: 검증한 E② 저널에서 요청을 쓴 시도(request_written) 수
//   - 탐색: 탐색 경계가 세션 마감 때 확정한 대상 요청 수(exploration.State.TargetRequests, 진단 E② 예산에서 빼는 그 몫),
//     탐색 중이면 경계가 지금 보고하는 수
// 합(total)은 탐색 수를 알 때만 채운다. 모르는 수를 0으로 바꾸지 않는다.

const (
	// explorationRequestsFile은 회차 상태 디렉터리의 탐색 요청 수 기록(0600)이다. 감사 기록이 아니라 표시용이다.
	explorationRequestsFile   = "exploration-requests.json"
	explorationRequestsFormat = "abc2lab.exploration-requests/v1"
	maxExplorationRequests    = 1 << 10
	// 탐색 중 경계에 지금 요청 수를 묻는 시간 상한. 화면 조회라 짧게 둔다.
	explorationLiveTimeout = 2 * time.Second
)

// 탐색 요청 수 상태.
const (
	explorationNone    = "none"    // 이 회차에 탐색 단계가 없다고 기록했다(0건). 기록이 없으면 이 값이 아니라 모름이다.
	explorationRunning = "running" // 탐색 중이다. 경계가 지금 수를 보고하면 그 값, 못 하면 모름.
	explorationClosed  = "closed"  // 경계가 세션 마감을 확정한 수다.
	explorationNotRun  = "not_run" // 탐색을 열지 않고 끝났다(0건).
	explorationUnknown = "unknown" // 경계가 마감을 확정하지 못했다(모름).
)

type explorationRequestsRecord struct {
	Format         string  `json:"format"`
	State          string  `json:"state"`
	TargetRequests *uint64 `json:"target_requests,omitempty"`
}

func (r explorationRequestsRecord) valid() bool {
	if r.Format != explorationRequestsFormat {
		return false
	}
	switch r.State {
	case explorationRunning, explorationUnknown:
		return r.TargetRequests == nil
	case explorationNone, explorationNotRun:
		return r.TargetRequests != nil && *r.TargetRequests == 0
	case explorationClosed:
		return r.TargetRequests != nil
	}
	return false
}

// writeExplorationRequests는 회차 상태 디렉터리에 탐색 요청 수 기록을 원자적으로 쓴다.
func writeExplorationRequests(stateDir string, r explorationRequestsRecord) error {
	if stateDir == "" || !r.valid() {
		return errPreparation
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, explorationRequestsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func explorationKnown(state string, n uint64) explorationRequestsRecord {
	return explorationRequestsRecord{Format: explorationRequestsFormat, State: state, TargetRequests: &n}
}

// explorationResultRecord는 탐색 단계 결과를 기록으로 바꾼다. 경계가 마감을 확정한 몫만 수로 남긴다.
func explorationResultRecord(notRun, known bool, requests uint64) explorationRequestsRecord {
	switch {
	case notRun && known && requests == 0:
		return explorationKnown(explorationNotRun, 0)
	case known:
		return explorationKnown(explorationClosed, requests)
	}
	return explorationRequestsRecord{Format: explorationRequestsFormat, State: explorationUnknown}
}

// e2FlowTargetRequests는 대상에 나간 요청 내역이다(작업대 총계·마우스 올림 내역).
type e2FlowTargetRequests struct {
	Model        int `json:"model"`
	Login        int `json:"login"`
	Confirmation int `json:"confirmation"`
	// Unclassified는 이 판이 모르는 프로그램 호출 종류의 보낸 요청 수다. 0이 아니면 total은 null이다.
	Unclassified int `json:"unclassified"`
	// Exploration·Total은 모르면 null이다(ExplorationState가 사유).
	Exploration      *uint64 `json:"exploration"`
	ExplorationState string  `json:"exploration_state"`
	Total            *uint64 `json:"total"`
}

// liveExploration은 탐색 중인 이 회차의 경계 세션이 지금 보고하는 대상 요청 수다. 없으면 ok가 거짓이다.
type liveExploration func(ctx context.Context, jobID string) (requests uint64, ok bool)

// readExplorationRequests는 회차 상태 디렉터리의 기록을 읽는다. 기록이 없으면 모름이다: 탐색 단계가 없는 회차도 none을
// 기록하므로, 없는 것은 기록 쓰기 실패이거나 이 칸 이전 회차다(#929 이동찬 P2, 0으로 보이지 않게).
func readExplorationRequests(b *runBinding) (explorationRequestsRecord, error) {
	raw, err := readRunFile(b.root, "state/"+explorationRequestsFile, maxExplorationRequests)
	if errors.Is(err, fs.ErrNotExist) {
		return explorationRequestsRecord{Format: explorationRequestsFormat, State: explorationUnknown}, nil
	}
	var r explorationRequestsRecord
	if err != nil || e2config.StrictJSON(raw, &r) != nil || !r.valid() {
		return explorationRequestsRecord{}, errPreparation
	}
	return r, nil
}

// targetRequestsOf는 검증한 저널 사건과 탐색 기록으로 내역을 만든다. 시도마다 결과 행 하나만 센다(e2FlowOf와 같음).
func targetRequestsOf(events []e2proxy.Event, exploration explorationRequestsRecord) e2FlowTargetRequests {
	out := e2FlowTargetRequests{ExplorationState: exploration.State}
	seen := map[string]bool{}
	for _, e := range events {
		if e.Stage != "result" || e.AttemptID == "" || seen[e.AttemptID] {
			continue
		}
		seen[e.AttemptID] = true
		if !e.RequestWritten {
			continue
		}
		switch e.Program {
		case "":
			if e.E3Audit != nil {
				out.Confirmation++
			} else {
				out.Model++
			}
		case e2proxy.ProgramAuthPrep, e2proxy.ProgramRefreshSession:
			out.Login++
		case e2proxy.ProgramE3Identity, e2proxy.ProgramE3Create, e2proxy.ProgramE3Read:
			out.Confirmation++
		default:
			// 모르는 프로그램 호출 종류는 어느 내역에도 넣지 않고 따로 센다. 합은 모름으로 둔다(#929 재상 봇 P3).
			out.Unclassified++
		}
	}
	if exploration.TargetRequests != nil {
		n := *exploration.TargetRequests
		out.Exploration = &n
		if out.Unclassified == 0 {
			total := uint64(out.Model+out.Login+out.Confirmation) + n
			out.Total = &total
		}
	}
	return out
}

// explorationNow는 기록이 탐색 중이면 경계에 지금 수를 묻는다. 경계가 이 회차 세션을 보고하지 않으면 모름이다.
func explorationNow(ctx context.Context, record explorationRequestsRecord, jobID string, live liveExploration) explorationRequestsRecord {
	if record.State != explorationRunning {
		return record
	}
	if live == nil {
		return record
	}
	bounded, cancel := context.WithTimeout(ctx, explorationLiveTimeout)
	defer cancel()
	n, ok := live(bounded, jobID)
	if !ok {
		return record
	}
	return explorationRequestsRecord{Format: explorationRequestsFormat, State: explorationRunning, TargetRequests: &n}
}

// recordExplorationStage는 탐색 단계 시작·끝을 기록한다. 기록 실패는 화면 총계만 모르게 하므로 진단을 멈추지 않는다.
// 바뀔 수 있는 기록(running)에는 수를 담지 않는다: 시작 전에는 기록이 없고(모름), 시작 때 running, 끝날 때 확정 상태를 쓴다.
// 끝 기록을 쓰지도 지우지도 못하면 running이 남아 경계 조회나 모름이 되므로, 지난 단계가 확정 수로 읽히는 길이 없다(#929 이동찬
// P2 두 번). none은 탐색 단계가 없는 회차가 한 번만 쓰고 바뀌지 않는다. 쓰지 못하면 앞 기록을 지워 본다(지우기 실패는 로그).
func recordExplorationStage(stateDir string, r explorationRequestsRecord) {
	err := writeExplorationRequests(stateDir, r)
	if err == nil {
		return
	}
	log.Print("[first-diagnosis] 탐색 요청 수 기록 실패(화면 총계는 모름으로 보임)")
	if stateDir == "" {
		return
	}
	if rmErr := os.Remove(filepath.Join(stateDir, explorationRequestsFile)); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		log.Print("[first-diagnosis] 지난 탐색 요청 수 기록을 지우지 못함(화면 총계가 지난 단계로 보일 수 있음)")
	}
}

// liveExplorationRequests는 이 회차의 탐색 경계 세션이 열려 있으면 경계가 지금 보고하는 대상 요청 수다.
func (b *urlPreparationRuntime) liveExplorationRequests(ctx context.Context, jobID string) (uint64, bool) {
	if b == nil || b.product == nil || b.product.exploration == nil {
		return 0, false
	}
	st, err := b.product.exploration.control.CurrentSession(ctx)
	if err != nil || st == nil || !st.Active || st.Binding.JobID != jobID {
		return 0, false
	}
	return st.TargetRequests, true
}

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/exploration"
)

// 멈춘 작업 해제(C04, 재기동형)의 제품 쪽 연결. 준비 서비스는 근거를 확인해 감사 기록을 남긴 뒤 스스로 정상 종료하고,
// 컨테이너 재시작 정책이 다시 띄운다. 이 프로세스는 Docker를 다루지 않는다.

// releaseQuietEnv는 활동 없음 기준(초)을 받는 환경 변수다. 비우면 diagnosisprep.DefaultReleaseQuiet다.
const releaseQuietEnv = "PRODUCT_RELEASE_QUIET_SECONDS"

// exitRestartRequested는 운영자 해제로 정상 종료했다는 종료 코드다(EX_TEMPFAIL). on-failure 재시작 정책에서도 다시 뜬다.
const exitRestartRequested = 75

var errExplorationOpen = errors.New("탐색 경계에 열린 세션이 남아 있습니다")

func releaseQuiet(raw string) (time.Duration, error) {
	if raw == "" {
		return diagnosisprep.DefaultReleaseQuiet, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < int64(diagnosisprep.MinReleaseQuiet/time.Second) || seconds > int64((24*time.Hour)/time.Second) {
		return 0, fmt.Errorf("%s는 %d초 이상 하루 이하의 정수여야 합니다", releaseQuietEnv, int64(diagnosisprep.MinReleaseQuiet/time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}

// LastActivity는 그 실행 감독자 엔진의 마지막 활동 시각이다. 이 프로세스에 그 실행이 없으면 false다.
func (b *urlPreparationRuntime) LastActivity(e diagnosisprep.Execution) (time.Time, bool) {
	b.mu.RLock()
	runner := b.recovery[e.Job.ID]
	b.mu.RUnlock()
	if runner == nil || runner.launch == nil || runner.launch.supervisor == nil {
		return time.Time{}, false
	}
	return runner.launch.supervisor.LastActivity(runner.taskID)
}

// CloseExploration은 그 실행의 탐색 경계 세션을 닫고 닫힘을 확인한다. 탐색이 없는 설치는 닫을 것이 없다.
func (b *urlPreparationRuntime) CloseExploration(ctx context.Context, e diagnosisprep.Execution) (bool, error) {
	if b.product == nil || b.product.exploration == nil {
		return false, nil
	}
	return b.product.exploration.closeForRelease(ctx, e.Job.ID)
}

// ExplorationClear는 탐색 경계에 열린 세션이 없음을 확인한다.
func (b *urlPreparationRuntime) ExplorationClear(ctx context.Context) error {
	if b.product == nil || b.product.exploration == nil {
		return nil
	}
	return b.product.exploration.clearForRelease(ctx)
}

func (h *productExplorationHost) closeForRelease(ctx context.Context, jobID string) (bool, error) {
	st, err := h.control.CurrentSession(ctx)
	if err != nil {
		return false, err
	}
	if st == nil || !st.Active {
		return false, nil
	}
	if st.Binding.JobID != jobID {
		return false, diagnosisprep.ErrExplorationBusy
	}
	if _, err := h.control.CloseSession(ctx, st.Binding, exploration.Cancelled); err != nil {
		return false, err
	}
	return true, h.clearForRelease(ctx)
}

func (h *productExplorationHost) clearForRelease(ctx context.Context) error {
	st, err := h.control.CurrentSession(ctx)
	if err != nil {
		return err
	}
	if st != nil && (st.Active || st.ActiveConnections != 0) {
		return errExplorationOpen
	}
	return nil
}

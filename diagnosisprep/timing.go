package diagnosisprep

import "time"

// 시각은 known fact에만 결속한다. 구 기록은 사실이 true여도 시각이 없을 수 있다.
func validRunTiming(started, drained bool, startedAt, finishedAt *time.Time) bool {
	if !validRunTime(startedAt) || !validRunTime(finishedAt) || startedAt != nil && !started || finishedAt != nil && !drained {
		return false
	}
	return startedAt == nil || finishedAt == nil || !finishedAt.Before(*startedAt)
}

func validRunTime(at *time.Time) bool {
	return at == nil || !at.IsZero() && at.Location() == time.UTC && at.Year() >= 1 && at.Year() <= 9999
}

// maxTimeLimitPart는 시간 상한의 각 몫(유예·확정 몫·여유)의 상한이다. 회차 시간 예산 상한(하루)보다 길 수 없다.
const maxTimeLimitPart = 24 * time.Hour

// validTimeLimit은 시간 상한 값의 모양과 식을 검사한다. 준비를 시작한 회차에만 있을 수 있다.
func validTimeLimit(t *TimeLimit, preparationStarted bool) bool {
	if t == nil {
		return true
	}
	if !preparationStarted || !validRunTime(&t.RunDeadlineAt) || !validRunTime(&t.ModelCloseAt) {
		return false
	}
	total := time.Duration(0)
	for _, ms := range []int64{t.ModelDrainMS, t.ConfirmationReserveMS, t.ShutdownMarginMS} {
		// 곱하기 전에 범위를 검사한다. 큰 int64를 Duration으로 바꾸면 넘쳐 짧은 몫처럼 보일 수 있다.
		if ms < 0 || ms > int64(maxTimeLimitPart/time.Millisecond) {
			return false
		}
		total += time.Duration(ms) * time.Millisecond
	}
	return t.ModelCloseAt.Add(total).Equal(t.RunDeadlineAt)
}

func sameTimeLimit(a, b *TimeLimit) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.RunDeadlineAt.Equal(b.RunDeadlineAt) && a.ModelCloseAt.Equal(b.ModelCloseAt) && a.ModelDrainMS == b.ModelDrainMS &&
		a.ConfirmationReserveMS == b.ConfirmationReserveMS && a.ShutdownMarginMS == b.ShutdownMarginMS
}

func copyTimeLimit(t *TimeLimit) *TimeLimit {
	if t == nil {
		return nil
	}
	copy := *t
	copy.RunDeadlineAt, copy.ModelCloseAt = t.RunDeadlineAt.UTC(), t.ModelCloseAt.UTC()
	return &copy
}

// 저장된 시각을 바꾸거나 구 기록의 누락을 뒤늦게 현재 시각으로 채우지 않는다. 시간 상한도 처음 저장한 값을 지우거나 바꾸지 않는다.
func monotonicRunTiming(old, next Job) bool {
	unchanged := func(a, b *time.Time) bool { return a == nil || b != nil && a.Equal(*b) }
	return unchanged(old.StartedAt, next.StartedAt) && unchanged(old.FinishedAt, next.FinishedAt) &&
		(old.TimeLimit == nil || sameTimeLimit(old.TimeLimit, next.TimeLimit)) &&
		!(old.Started && old.TimeLimit == nil && next.TimeLimit != nil) &&
		!(old.Started && old.StartedAt == nil && next.StartedAt != nil) &&
		!(old.DrainComplete && old.FinishedAt == nil && next.FinishedAt != nil)
}

func copyRunTime(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	copy := *at
	return &copy
}

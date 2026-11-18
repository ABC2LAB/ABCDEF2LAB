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

// 저장된 시각을 바꾸거나 구 기록의 누락을 뒤늦게 현재 시각으로 채우지 않는다.
func monotonicRunTiming(old, next Job) bool {
	unchanged := func(a, b *time.Time) bool { return a == nil || b != nil && a.Equal(*b) }
	return unchanged(old.StartedAt, next.StartedAt) && unchanged(old.FinishedAt, next.FinishedAt) &&
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

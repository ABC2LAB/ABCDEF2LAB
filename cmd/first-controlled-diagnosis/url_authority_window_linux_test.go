package main

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/server"
)

// 회차 권한은 실행 시간 + 정리 시간 동안 유효해야 한다. 설치가 고를 수 있는 가장 긴 회차(실행 시간 상한 + 정리 시간 상한)도
// 권한을 받고, 그보다 긴 구간·빈 구간은 받지 않는다. 설치 위임 기한이 더 짧으면 그 기한까지다.
func TestURLAuthorityUntilCoversTheLongestRun(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	far := now.Add(24 * time.Hour)
	for name, tc := range map[string]struct {
		run, shutdown int64
		delegation    time.Time
		want          time.Duration
		ok            bool
	}{
		// 2026-11-14 실제 회차: 워크플로 1800초 + 정리 20초가 15분 상한에 걸려 권한 발급이 실패했다.
		"workflow-1800":   {1800, 20, far, 1820 * time.Second, true},
		"longest":         {server.MaxFirstDiagnosisTaskSeconds, server.MaxFirstDiagnosisShutdownSeconds, far, server.MaxFirstDiagnosisHTTPAuthority, true},
		"over":            {server.MaxFirstDiagnosisTaskSeconds + 1, server.MaxFirstDiagnosisShutdownSeconds, far, 0, false},
		"delegation-cuts": {600, 20, now.Add(5 * time.Minute), 5 * time.Minute, true},
		"delegation-past": {600, 20, now.Add(-time.Second), 0, false},
	} {
		until, ok := urlAuthorityUntil(now, tc.run, tc.shutdown, tc.delegation)
		if ok != tc.ok || ok && until.Sub(now) != tc.want {
			t.Fatalf("%s: until=%s ok=%v", name, until.Sub(now), ok)
		}
	}
	if server.MaxFirstDiagnosisHTTPAuthority != time.Duration(server.MaxFirstDiagnosisTaskSeconds+server.MaxFirstDiagnosisShutdownSeconds)*time.Second {
		t.Fatal("authority window no longer covers the run and shutdown caps")
	}
}

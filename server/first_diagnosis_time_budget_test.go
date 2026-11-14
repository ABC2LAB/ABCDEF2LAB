package server

import (
	"errors"
	"testing"
	"time"
)

// 시간 상한 마감은 Run 전에 한 번만, 정한 범위 안에서만 받는다(계약 §3.4).
func TestFirstDiagnosisSupervisorSetTimeBudget(t *testing.T) {
	at := time.Now().Add(time.Minute)
	if err := (*FirstDiagnosisSupervisor)(nil).SetTimeBudget(at, time.Second); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("nil supervisor accepted")
	}
	for name, tc := range map[string]struct {
		at    time.Time
		drain time.Duration
	}{
		"zero-time":      {time.Time{}, time.Second},
		"negative-drain": {at, -time.Second},
		"drain-over-cap": {at, FirstDiagnosisTimeBudgetDrain + time.Nanosecond},
	} {
		if err := (&FirstDiagnosisSupervisor{}).SetTimeBudget(tc.at, tc.drain); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
			t.Fatalf("%s accepted", name)
		}
	}
	s := &FirstDiagnosisSupervisor{}
	if err := s.SetTimeBudget(at, FirstDiagnosisTimeBudgetDrain); err != nil || !s.timeCloseAt.Equal(at) || s.timeDrain != FirstDiagnosisTimeBudgetDrain {
		t.Fatal("valid time budget rejected", err)
	}
	if err := s.SetTimeBudget(at.Add(time.Minute), time.Second); !errors.Is(err, ErrFirstDiagnosisSupervisor) || !s.timeCloseAt.Equal(at) {
		t.Fatal("time budget replaced")
	}
	for name, state := range map[string]func(*FirstDiagnosisSupervisor){
		"running": func(s *FirstDiagnosisSupervisor) { s.running = true },
		"started": func(s *FirstDiagnosisSupervisor) { s.runStarted = true },
		"closing": func(s *FirstDiagnosisSupervisor) { s.closing = true },
		"closed":  func(s *FirstDiagnosisSupervisor) { s.closed = true },
	} {
		s := &FirstDiagnosisSupervisor{}
		state(s)
		if err := s.SetTimeBudget(at, time.Second); !errors.Is(err, ErrFirstDiagnosisSupervisor) || !s.timeCloseAt.IsZero() {
			t.Fatalf("time budget set after %s", name)
		}
	}
}

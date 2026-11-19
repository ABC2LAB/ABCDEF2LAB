package server

import (
	"context"
	"errors"
	"testing"
)

// E③ 확정 단계 훅은 Run 전에 한 번만, 확정 단계·중지 기록 둘 다 있을 때만 받는다(결정 §5).
func TestFirstDiagnosisSupervisorSetE3Confirmation(t *testing.T) {
	hook := FirstDiagnosisE3Hooks{Run: func(context.Context, string, func()) error { return nil }, Stopped: func(context.Context, string) error { return nil }}
	if err := (*FirstDiagnosisSupervisor)(nil).SetE3Confirmation(hook); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("nil supervisor accepted")
	}
	for name, half := range map[string]FirstDiagnosisE3Hooks{"empty": {}, "no_run": {Stopped: hook.Stopped}, "no_stopped": {Run: hook.Run}} {
		if err := (&FirstDiagnosisSupervisor{}).SetE3Confirmation(half); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
			t.Fatal("incomplete hooks accepted:", name)
		}
	}
	s := &FirstDiagnosisSupervisor{}
	if err := s.SetE3Confirmation(hook); err != nil || s.e3Confirmation.Run == nil || s.e3Confirmation.Stopped == nil {
		t.Fatal("valid hook rejected", err)
	}
	replaced := false
	if err := s.SetE3Confirmation(FirstDiagnosisE3Hooks{Run: func(context.Context, string, func()) error { replaced = true; return nil }, Stopped: hook.Stopped}); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("hook replaced")
	}
	if err := s.e3Confirmation.Run(t.Context(), "1", func() {}); err != nil || replaced {
		t.Fatal("hook replaced")
	}
	for name, state := range map[string]func(*FirstDiagnosisSupervisor){
		"running": func(s *FirstDiagnosisSupervisor) { s.running = true },
		"started": func(s *FirstDiagnosisSupervisor) { s.runStarted = true },
		"closing": func(s *FirstDiagnosisSupervisor) { s.closing = true },
		"closed":  func(s *FirstDiagnosisSupervisor) { s.closed = true },
	} {
		s := &FirstDiagnosisSupervisor{}
		state(s)
		if err := s.SetE3Confirmation(hook); !errors.Is(err, ErrFirstDiagnosisSupervisor) || s.e3Confirmation.Run != nil {
			t.Fatalf("hook set after %s", name)
		}
	}
}

// 확정 단계는 진단 로그에 자기 단계로 남는다(실행 단계와 섞지 않음).
func TestDiagnosisLogStageE3Confirmation(t *testing.T) {
	if got := diagnosisLogStage("e3-confirmation"); got != "e3_confirmation" {
		t.Fatalf("e3 confirmation stage: %s", got)
	}
}

package server

import (
	"context"
	"errors"
	"testing"
)

// E③ 확정 단계 훅은 Run 전에 한 번만, nil이 아닐 때만 받는다(결정 §5).
func TestFirstDiagnosisSupervisorSetE3Confirmation(t *testing.T) {
	hook := func(context.Context, string, func()) error { return nil }
	if err := (*FirstDiagnosisSupervisor)(nil).SetE3Confirmation(hook); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("nil supervisor accepted")
	}
	if err := (&FirstDiagnosisSupervisor{}).SetE3Confirmation(nil); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("nil hook accepted")
	}
	s := &FirstDiagnosisSupervisor{}
	if err := s.SetE3Confirmation(hook); err != nil || s.e3Confirmation == nil {
		t.Fatal("valid hook rejected", err)
	}
	replaced := false
	if err := s.SetE3Confirmation(func(context.Context, string, func()) error { replaced = true; return nil }); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("hook replaced")
	}
	if err := s.e3Confirmation(t.Context(), "1", func() {}); err != nil || replaced {
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
		if err := s.SetE3Confirmation(hook); !errors.Is(err, ErrFirstDiagnosisSupervisor) || s.e3Confirmation != nil {
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

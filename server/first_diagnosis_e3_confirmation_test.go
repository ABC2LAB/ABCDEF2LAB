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

// 실행에 들어가기 전에 중지되면(실행 문맥·감독자 문맥 취소, 감독자 종료 중) 확정 단계 없이 중지 기록이 한 번 남는다(#738 이동찬
// 리뷰). 등록 GET 경로, 다른 Run이 이미 시작한 감독자, 준비 서버는 기록하지 않는다. 한 감독자에서 두 번 기록하지 않는다.
func TestFirstDiagnosisSupervisorStoppedBeforeEntry(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	cases := map[string]struct {
		ctx      context.Context
		edit     func(*FirstDiagnosisSupervisor)
		intent   int64
		snapshot string
		want     int
	}{
		"run_ctx_cancelled":        {cancelled, nil, 0, "", 1},
		"supervisor_ctx_cancelled": {live, func(s *FirstDiagnosisSupervisor) { s.ctx = cancelled }, 0, "", 1},
		"closing":                  {live, func(s *FirstDiagnosisSupervisor) { s.closing = true }, 0, "", 1},
		"closed":                   {live, func(s *FirstDiagnosisSupervisor) { s.closed = true }, 0, "", 1},
		"registered_path":          {cancelled, nil, 1, "snap-1", 0},
		"other_run_started":        {cancelled, func(s *FirstDiagnosisSupervisor) { s.runStarted = true }, 0, "", 0},
		"other_run_running":        {live, func(s *FirstDiagnosisSupervisor) { s.running = true }, 0, "", 0},
		"preparation_server": {live, func(s *FirstDiagnosisSupervisor) {
			s.server = &Server{firstDiagnosisPreparation: true}
			s.closing = true
		}, 0, "", 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			runs, stops := 0, 0
			s := &FirstDiagnosisSupervisor{ctx: live, server: &Server{}}
			s.e3Confirmation = FirstDiagnosisE3Hooks{
				Run: func(context.Context, string, func()) error { runs++; return nil },
				Stopped: func(ctx context.Context, id string) error {
					if ctx.Err() != nil || id != "7" {
						t.Errorf("중지 기록 문맥·작업: %v %s", ctx.Err(), id)
					}
					stops++
					return nil
				},
			}
			if c.edit != nil {
				c.edit(s)
			}
			if err := s.Run(c.ctx, "7", c.intent, c.snapshot); err == nil {
				t.Fatal("진입 전 중지가 성공함")
			}
			if runs != 0 || stops != c.want {
				t.Fatalf("확정 단계 %d·중지 기록 %d(기대 %d)", runs, stops, c.want)
			}
			// 같은 감독자에서 다시 진입 전 중지가 와도 기록은 하나다(결과 파일은 회차마다 하나).
			if err := s.Run(cancelled, "7", c.intent, c.snapshot); err == nil || runs != 0 || stops != c.want {
				t.Fatalf("두 번째 진입 전 중지: 확정 %d·중지 기록 %d", runs, stops)
			}
		})
	}
}

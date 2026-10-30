package server

import (
	"log"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

const controlledReadIdleSummary = "통제 조회 작업이 다음 승인·설정 확인을 기다립니다"

// maybeParkControlledTask is called only after the caller releases its task
// operation. It deliberately does not mark the task done: a controlled task
// remains resumable for the next operator-approved registration.
func (e *Engine) maybeParkControlledTask(task *Task) {
	if e == nil || task == nil || e.controlledIdle == nil || e.controlledReads == nil {
		return
	}
	if !e.controlledTask(task.ID) || e.inflightCount(task.ID) != 0 || e.IsDeleting(task.ID) || e.IsPaused(task.ID) {
		return
	}
	if isTerminalStatus(task.lifecycleSnapshot().Status) {
		return
	}
	r := e.controlledReads
	r.mu.Lock()
	pending := false
	for key := range r.pending {
		if key.task == task.ID {
			pending = true
			break
		}
	}
	active := false
	for key := range r.active {
		if key.task == task.ID {
			active = true
			break
		}
	}
	r.mu.Unlock()
	if pending || active {
		return
	}
	e.controlledIdle(task)
}

// parkControlledReadTask serializes the final idle transition with task
// control/admission. The second idle check closes the gap between the engine's
// lock-free observation and this callback's pause commit.
func (s *Server) parkControlledReadTask(task *Task) {
	if s == nil || s.engine == nil || s.m == nil || task == nil {
		return
	}
	s.concMu.Lock()
	defer s.concMu.Unlock()
	current, ok := s.m.Task(task.ID)
	if !ok || current != task || s.engine.IsDeleting(task.ID) || s.engine.IsPaused(task.ID) ||
		isTerminalStatus(task.lifecycleSnapshot().Status) || s.engine.inflightCount(task.ID) != 0 {
		return
	}
	r := s.engine.controlledReads
	if r == nil || !s.engine.controlledTask(task.ID) {
		return
	}
	r.mu.Lock()
	busy := false
	for key := range r.pending {
		if key.task == task.ID {
			busy = true
			break
		}
	}
	if !busy {
		for key := range r.active {
			if key.task == task.ID {
				busy = true
				break
			}
		}
	}
	r.mu.Unlock()
	if busy || !s.engine.beginTaskOperation(task.ID) {
		return
	}
	defer s.engine.decInflight(task.ID)
	s.engine.Pause(task.ID, agent.AbortPausedByOrchestrator)
	if err := s.m.ApplyTaskPause(task.ID); err != nil {
		log.Printf("[E2] controlled task parking could not be persisted")
		s.engine.Resume(task)
		return
	}
	s.engine.emitActivity(task, db.Activity{Worker: "system", Kind: "text", Summary: controlledReadIdleSummary})
	go s.reconcileConcurrency()
}

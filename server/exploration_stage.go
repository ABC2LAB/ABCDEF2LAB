package server

import (
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
)

// ExplorationWorkerFor는 제품 단계 callback에 기존 task router의 Worker만 빌려준다.
// 같은 provider가 모델 정책·비용 예약·사용량 정산을 계속 집행하며, 새 자격값이나 대체 모델을 만들지 않는다.
func (s *Server) ExplorationWorkerFor(task *Task) (*agent.Worker, error) {
	if s == nil || s.engine == nil || task == nil {
		return nil, exploration.ErrUnavailable
	}
	if _, err := parseTaskID(task.ID); err != nil {
		return nil, exploration.ErrBinding
	}
	_, worker := s.engine.snapshotFor(task)
	if !worker.ExplorationReady() {
		return nil, exploration.ErrUnavailable
	}
	return worker, nil
}

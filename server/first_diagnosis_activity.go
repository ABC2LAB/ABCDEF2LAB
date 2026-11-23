package server

import "time"

// LastActivity는 이 감독자 엔진에서 그 작업의 마지막 Planner·Worker 활동 시각이다. 멈춘 작업 해제(C04)의 활동 없음
// 근거로만 쓴다. 엔진이 그 작업을 아직 안 돌렸거나 마감해 기록을 지웠으면 false다.
func (s *FirstDiagnosisSupervisor) LastActivity(taskID string) (time.Time, bool) {
	if s == nil || s.server == nil || s.server.engine == nil || taskID == "" {
		return time.Time{}, false
	}
	at := s.server.engine.LastActivity(taskID)
	if at <= 0 {
		return time.Time{}, false
	}
	return time.Unix(at, 0).UTC(), true
}

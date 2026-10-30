package server

import (
	"io"
	"net/http"
	"strconv"
)

// This route inherits the server's /api JWT middleware. Only opaque approval
// references arrive over HTTP; credentials, URLs, keys and budgets stay private.
func (s *Server) registerControlledReadHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SnapshotRef string `json:"snapshot_ref"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, controlledReadInputLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil || strictJSON(body, &request) != nil || !safeSnapshotRef.MatchString(request.SnapshotRef) {
		writeErr(w, http.StatusBadRequest, "승인된 조회 설정 참조가 필요합니다")
		return
	}
	intentID, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || intentID <= 0 {
		writeErr(w, http.StatusBadRequest, "올바른 의도 참조가 필요합니다")
		return
	}
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok || task.Store == nil {
		writeErr(w, http.StatusNotFound, "작업을 찾을 수 없습니다")
		return
	}
	if !s.engine.beginTaskOperation(task.ID) {
		writeErr(w, http.StatusConflict, "종료 중인 작업에는 조회를 등록할 수 없습니다")
		return
	}
	defer s.engine.decInflight(task.ID)
	intent, err := task.Store.GetNode(intentID)
	if err != nil || intent == nil || intent.Kind != "intent" {
		writeErr(w, http.StatusNotFound, "작업의 의도를 찾을 수 없습니다")
		return
	}
	// Main-agent chat ignores ordinary task pause; serialize registration with
	// its admission as well, so it cannot race the durable controlled marker.
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if s.chatBusy[task.ID] || s.engine.inflightCount(task.ID) > 1 {
		writeErr(w, http.StatusConflict, "실행 중인 작업이 끝난 뒤 조회를 등록하세요")
		return
	}
	if intent.State == "running" {
		writeErr(w, http.StatusConflict, "실행 중인 의도는 종료된 뒤 다시 등록하세요")
		return
	}
	ready := func() error {
		// Also CAS open->open: cancellation may have deleted the node after the
		// initial read. A stale open observation must not publish an orphan lease.
		return transitionIntentState(task.Store, intentID, intent.State, "open")
	}
	if err := s.engine.registerControlledRead(r.Context(), task, intentID, request.SnapshotRef, ready); err != nil {
		writeErr(w, http.StatusConflict, "작업을 일시정지하고 유효한 승인 조회 설정을 등록하세요")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task_id": task.ID, "intent_id": intentID, "state": "registered"})
}

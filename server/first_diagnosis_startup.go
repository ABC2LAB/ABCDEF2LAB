package server

import (
	"context"
	"errors"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/llmpolicy"
	"golang.org/x/crypto/bcrypt"
)

// 첫 기동은 설치자가 준비한 인증만 사용하며 새 설정 토큰이나 비밀번호를 만들지 않는다.
func (s *Server) bootstrapFirstDiagnosisAuth() error {
	hash, err := s.reloadPassHash()
	if err != nil || hash == "" {
		return ErrControlledReadHostConfiguration
	}
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}

// NewWithE2HTTPRuntime은 일반 HTTP 실행을 admission 전에 설치한다.
func NewWithE2HTTPRuntime(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, runtime *E2HTTPRuntime, manifestPath ...string) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || runtime == nil {
		return nil, ErrE2HTTPHost
	}
	if len(manifestPath) > 1 {
		return nil, ErrLocalLLMPolicy
	}
	var resolver *llmpolicy.ProfileResolver
	if len(manifestPath) == 1 && manifestPath[0] != "" {
		var err error
		resolver, err = llmpolicy.LoadProfileManifestFile(manifestPath[0])
		if err != nil {
			return nil, ErrLocalLLMPolicy
		}
	}
	return newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, resolver, serverStartupOptions{existingAuthOnly: true, noAutomaticAdmission: true, e2HTTP: runtime}, nil)
}

// 준비 서버는 모델의 실제 Planner 의도 생성만 허용하며 대상 도구와 실행 admission이 없다.
func NewFirstDiagnosisPreparationServer(ctx context.Context, m *Manager, skillDir, dataDir, keyDir, manifestPath string) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || manifestPath == "" {
		return nil, ErrControlledReadHostConfiguration
	}
	if CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, m.pg.DB) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	resolver, err := llmpolicy.LoadProfileManifestFile(manifestPath)
	if err != nil {
		return nil, ErrLocalLLMPolicy
	}
	return newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, resolver, serverStartupOptions{existingAuthOnly: true, noAutomaticAdmission: true, preparationOnly: true}, nil)
}

// 첫 회차의 실행은 신뢰한 로컬 감독자만 시작한다. 채팅·MCP·수정 API로 우회할 수 없다.
func (s *Server) firstDiagnosisRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := s.firstDiagnosisReportRoute(r)
		if r.Method == http.MethodPost && r.URL.Path == "/api/auth/login" {
			allowed = true
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			allowed = allowed || firstDiagnosisReadRoute(r.URL.Path)
		}
		if requestApprovalRoute(r) {
			allowed = true
		}
		if firstDiagnosisControlRoute(r) {
			allowed = true
		}
		if s.firstDiagnosisPreparation || !allowed {
			writeErr(w, http.StatusForbidden, "첫 진단 전용 서버에서는 이 작업을 사용할 수 없습니다")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// 분해 실패·목표 0개면 원래 목표로 계획을 계속한다(P3 결정안 C11 결정 1). 문맥 취소만 실패로 끝낸다.
// 분해 실패는 진단 로그에 WARN으로 남긴다.
func (s *Server) createFirstDiagnosisGoals(ctx context.Context, t *Task, emit func(db.Activity)) (resultErr error) {
	startedAt := time.Now()
	fallback := false
	defer func() {
		if t == nil {
			return
		}
		if fallback {
			// 분해 단계 실패를 운영 기록에 남기되 실행은 계속한다(진단 로그는 감사가 아니다).
			// incomplete는 로거가 관찰 불완전으로 세어 완료 실행을 unknown으로 바꾸므로 쓰지 않는다. 계획은 원래 목표로 온전히 돈다.
			_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "supervisor", Event: "stage_finished", Level: "WARN", TaskID: mustFirstDiagnosisTaskNumber(t.ID), Stage: "goal_creation", Status: "failed", ReasonCode: "goal_creation_failed", DurationMS: time.Since(startedAt).Milliseconds()})
			return
		}
		diagnosisStage(ctx, mustFirstDiagnosisTaskNumber(t.ID), "goal_creation", resultErr, "goal_creation_failed", startedAt)
	}()
	if s == nil || !s.firstDiagnosisPreparation || ctx == nil || ctx.Err() != nil || t == nil || t.Store == nil {
		return ErrControlledReadHostConfiguration
	}
	if yes, err := t.Store.HasOpenGoal(); err != nil {
		return ErrControlledReadHostConfiguration
	} else if yes {
		return nil
	}
	runtime := s.agentsForTask(t).runtime
	if runtime == nil {
		return ErrLocalLLMPolicy
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return ErrControlledReadHostConfiguration
	}
	var failed, terminal atomic.Bool
	capture := func(a db.Activity) {
		if a.Kind == "result" {
			terminal.Store(true)
			if a.IsError {
				failed.Store(true)
			}
		}
		if emit != nil {
			emit(a)
		}
	}
	s.engine.BeginLLMCall(t.ID)
	goals := agent.DecomposeGoalsWithProvider(agent.WithE2HTTPPlanning(ctx), runtime, s.m.dir, t.Goal, t.Description, s.m.assets, t.Store, taskID, runtime.nonStreaming(), runtime.maxTokens(), capture)
	s.engine.EndLLMCall(t.ID)
	if ctx.Err() != nil {
		return ErrLocalLLMPolicy
	}
	if failed.Load() || !terminal.Load() || len(goals) == 0 {
		// 목표 분해는 계획을 돕는 단계이지 필수 단계가 아니다(P3 결정안 C11 결정 1). 원본(startTaskEngine)처럼
		// 분해한 목표 없이 원래 작업 목표로 계획을 계속한다. 모델 예산이 막혔다면 다음 계획 호출이 그 사유로 멈춘다.
		fallback = true
		log.Printf("[first-diagnosis] task %s 목표 분해 결과 없음, 원래 목표로 계획 계속", t.ID)
		return nil
	}
	return nil
}

// 조회라는 HTTP 메서드만으로 외부 동기화·자가 갱신·MCP 발견을 허용하지 않는다.
func firstDiagnosisReadRoute(path string) bool {
	switch path {
	case "/api/health", "/api/auth/status", "/api/tasks", "/api/exploration/findings", "/api/exploration/findings/groups", "/api/exploration/findings/asset-tree", "/api/exploration/findings/stats",
		// 활동 조회(실시간·이력·스트림)는 읽기 전용이라 새 전송·admission을 열지 않는다(C33). 운영자가 돌아가는 진단을 지켜본다.
		"/api/exploration/activity", "/api/exploration/activity/history", "/api/exploration/activity/stream":
		return true
	}
	if tail, ok := strings.CutPrefix(path, "/api/tasks/"); ok {
		parts := strings.Split(tail, "/")
		if !numericID.MatchString(parts[0]) {
			return false
		}
		if len(parts) == 1 {
			return true
		}
		if len(parts) == 2 {
			switch parts[1] {
			case "coverage", "coverage-graph", "asset-refs", "intent-assets", "scope", "goals", "constraints":
				return true
			}
		}
	}
	if tail, ok := strings.CutPrefix(path, "/api/exploration/findings/"); ok {
		parts := strings.Split(tail, "/")
		return numericID.MatchString(parts[0]) && (len(parts) == 1 || len(parts) == 2 && parts[1] == "lineage")
	}
	if tail, ok := strings.CutPrefix(path, "/api/exploration/activity/"); ok {
		// 활동 상세(activity/{seq}). history·stream은 위 switch가 이미 받았다.
		return numericID.MatchString(tail)
	}
	return false
}

// firstDiagnosisControlRoute는 제품 경로에서 운영자가 회차를 일시정지·재개하는 조작 경로만 연다(C33). 실제 동작은
// firstDiagnosisControl이다: 일시정지는 회차를 바로 끝내는 운영자 중지이고(확정 단계 interrupted), 재개는 지원하지 않는다
// (resume_unsupported, 사용자 결정 2026-11-20 분기점 1). 진짜 재개 지원은 측정 뒤 개선 항목이다(개발 계획). 실행 "시작"·
// 재admission이나 새 전송을 열지 않는다. 채팅·intent 조종·rerun(종료 task 재admission)·설정 쓰기는 admission/전송 인접 허용
// 판단이라 여기서 열지 않는다(E² 담당 정렬).
// firstDiagnosisControlResumeUnsupported는 첫 진단 회차의 재개 요청을 거절하는 고정 사유 코드다.
const firstDiagnosisControlResumeUnsupported = "resume_unsupported"

// firstDiagnosisControl은 첫 진단 회차의 일시정지·재개 요청이다(사용자 결정 2026-11-20 분기점 1). 겉과 속을 맞춘다:
// 일시정지는 엔진을 멈춰 두는 것이 아니라 회차를 바로 끝내는 운영자 중지다(요청 승인 중지와 같은 경로, 확정 단계
// interrupted). 일시정지 상태로 시간 상한까지 걸려 있지 않게 하려는 것이다. 재개는 지원하지 않는다(resume_unsupported).
func (s *Server) firstDiagnosisControl(w http.ResponseWriter, r *http.Request, t *Task, action string) {
	if action == "resume" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": trMsg("첫 진단 회차는 재개를 지원하지 않습니다. 일시정지는 회차를 중지합니다"),
			"reason_code": firstDiagnosisControlResumeUnsupported})
		return
	}
	number, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || number <= 0 {
		writeErr(w, http.StatusBadRequest, "작업 ID가 올바르지 않습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.requestApprovals.stop(ctx, number, "ARTEX"); err != nil {
		if errors.Is(err, db.ErrRequestApprovalConflict) {
			writeErr(w, http.StatusConflict, "이미 종료된 실행은 중지할 수 없습니다")
			return
		}
		writeErr(w, http.StatusServiceUnavailable, "실행 중지 기록을 확인할 수 없습니다")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": t.ID, "paused": false, "queued": false, "status": "stopping",
		"reason_code": "pause_stops_run"})
}

func firstDiagnosisControlRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return false
	}
	parts := strings.Split(tail, "/")
	return len(parts) == 2 && numericID.MatchString(parts[0]) && parts[1] == "control"
}

// 보고서만 기존 생성기와 저장소로 연결한다. 모델 또는 대상 실행 admission은 열지 않는다.
// task는 신뢰한 기동 과정에서 고정한 동일 결속만 사용하며 중복 query도 거부한다.
func (s *Server) firstDiagnosisReportRoute(r *http.Request) bool {
	number, ok := firstDiagnosisReportRequestTask(r)
	if !ok {
		return false
	}
	s.firstModelMu.RLock()
	defer s.firstModelMu.RUnlock()
	return s.firstModel != nil && s.firstModel.TaskNumber == number && s.firstModel.TaskID == strconv.FormatInt(number, 10) && s.firstModel.ExplorationID > 0
}

func firstDiagnosisReportRequestTask(r *http.Request) (int64, bool) {
	if id, ok := reportGraphRequestTask(r); ok {
		return id, true
	}
	if id, ok := promptProjectionRequestTask(r); ok {
		return id, true
	}
	if id, ok := accessMapRequestTask(r); ok {
		return id, true
	}
	if id, ok := diagnosisRunRequestTask(r); ok {
		return id, true
	}
	switch r.URL.Path {
	case "/api/report/diagnostic", "/api/report/diagnostic/image", "/api/report/diagnostic/remediation/prompt":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return 0, false
		}
	case "/api/report/diagnostic/remediation":
		if r.Method == http.MethodPost {
			// 쿠키·query token만으로 생기는 교차 출처 쓰기를 새로 열지 않는다.
			auth := r.Header.Get("Authorization")
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == "" || err != nil || mediaType != "application/json" {
				return 0, false
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			return 0, false
		}
	default:
		return 0, false
	}
	// URL.Query는 잘못된 escape를 조용히 버리므로 폐쇄 파서를 사용한다.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, false
	}
	// task도 다른 키와 같은 곳에서 중복을 거부한다. 누락·정규형은 아래 정수 검사가 맡는다.
	for _, values := range query {
		if len(values) != 1 {
			return 0, false
		}
	}
	task := query.Get("task")
	number, err := strconv.ParseInt(task, 10, 64)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != task {
		return 0, false
	}
	return number, true
}

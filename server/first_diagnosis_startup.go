package server

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
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
		if s.firstDiagnosisPreparation || !allowed {
			writeErr(w, http.StatusForbidden, "첫 진단 전용 서버에서는 이 작업을 사용할 수 없습니다")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// 준비 단계에서는 분해 실패를 원문 목표 fallback으로 숨기지 않는다.
func (s *Server) createFirstDiagnosisGoals(ctx context.Context, t *Task, emit func(db.Activity)) error {
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
	if ctx.Err() != nil || failed.Load() || !terminal.Load() || len(goals) == 0 {
		return ErrLocalLLMPolicy
	}
	return nil
}

// 조회라는 HTTP 메서드만으로 외부 동기화·자가 갱신·MCP 발견을 허용하지 않는다.
func firstDiagnosisReadRoute(path string) bool {
	switch path {
	case "/api/health", "/api/auth/status", "/api/tasks", "/api/exploration/findings", "/api/exploration/findings/groups", "/api/exploration/findings/asset-tree", "/api/exploration/findings/stats":
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
	return false
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
	if id, ok := diagnosisRunRequestTask(r); ok {
		return id, true
	}
	switch r.URL.Path {
	case "/api/report/diagnostic", "/api/report/diagnostic/remediation/prompt":
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
	if err != nil || len(query["task"]) != 1 {
		return 0, false
	}
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

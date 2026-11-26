package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
	"github.com/Autumn-27/artex/loginattempt"
	"github.com/Autumn-27/artex/server"
)

// 시험·보호된 Go 조립이 넣는 탐색 capability다. 제품은 설치된 대상 정책 파일(SHA-256 고정)의 exploration 블록으로
// 실제 경계·브라우저 샌드박스를 묶는다(product_exploration_host.go). URL·모델·환경 변수로는 켜지지 않는다.
type urlRuntimeHostCapabilities struct {
	Exploration       server.ExplorationPipelineFactory
	ExplorationLimits *exploration.Limits
}
type urlExplorationStageKey struct{}

func withURLExplorationStage(ctx context.Context, stage server.ExplorationStageFactory) context.Context {
	return context.WithValue(ctx, urlExplorationStageKey{}, stage)
}
func urlExplorationStageFrom(ctx context.Context) server.ExplorationStageFactory {
	stage, _ := ctx.Value(urlExplorationStageKey{}).(server.ExplorationStageFactory)
	return stage
}

func (b *urlPreparationRuntime) productExplorationOptions(l *urlLoadedProfile, e diagnosisprep.Execution, taskID string, flow *diagnosisflow.Controller) (server.ExplorationPipelineOptions, bool) {
	if b == nil || b.product == nil || l == nil || l.productTarget == nil || flow == nil {
		return server.ExplorationPipelineOptions{}, false
	}
	requests := min(l.profile.Budget.Requests, l.productTarget.Target.Requests)
	if requests < 0 {
		requests = 0
	}
	timeout := time.Duration(l.policy.Runtime.WorkerTimeoutMS) * time.Millisecond
	seconds := min(l.profile.Budget.DurationSeconds, int64((timeout+time.Second-1)/time.Second))
	snapshot, _ := flow.Snapshot()
	limits := exploration.Limits{TimeBudgetSeconds: seconds, MaxRequests: uint64(requests), MaxConcurrency: l.policy.Limits.MaxConcurrent, MaxResponseBytes: int64(snapshot.Limits.MaxResponseBytes)}
	limits.MaxConcurrency = min(limits.MaxConcurrency, int(requests))
	if host := b.product.exploration; host != nil && b.hostCapabilities.ExplorationLimits == nil && l.launch.ModelExecutionMode == "openrouter" {
		// 외부 통신 승인 모드에서만 렌더링용 외부 자원 상한을 준다. 완전 로컬은 0이다(규칙 8).
		limits.MaxExternalRequests = host.maxExternalRequests
	}
	if b.hostCapabilities.ExplorationLimits != nil {
		approved := *b.hostCapabilities.ExplorationLimits
		limits.TimeBudgetSeconds = min(limits.TimeBudgetSeconds, approved.TimeBudgetSeconds)
		limits.MaxRequests = min(limits.MaxRequests, approved.MaxRequests)
		limits.MaxConcurrency = min(limits.MaxConcurrency, approved.MaxConcurrency)
		limits.MaxResponseBytes = min(limits.MaxResponseBytes, approved.MaxResponseBytes)
		limits.MaxExternalRequests = approved.MaxExternalRequests
	}
	mode := exploration.FullLocal
	if l.launch.ModelExecutionMode == "openrouter" {
		mode = exploration.ExternalAllowed
	}
	binding := exploration.Binding{SessionRef: "explore-" + rand.Text(), RunRef: "run-" + e.Job.ID, JobID: e.Job.ID, TaskID: taskID, TargetRef: l.productTarget.Target.ID, TargetRevision: l.productTarget.Target.Revision, WriteMode: exploration.ReadOnly, CommMode: mode, Limits: limits}
	maxDuration := min(timeout, time.Duration(limits.TimeBudgetSeconds)*time.Second)
	options := server.ExplorationPipelineOptions{Workflow: flow, Binding: binding, SourceDigest: e.Job.SourceDigest, TargetURL: e.Job.Input.TargetURL, ApprovedPaths: productExplorationPaths(e.Job), MaxTurns: l.policy.Runtime.MaxWorkerTurns, MaxDuration: maxDuration}
	// 탐색 에이전트도 등록 계정으로 브라우저에서 직접 로그인한다(사용자 결정 2026-11-26). 자격을 못 읽으면 탐색을 열지 않는다.
	credentials, err := l.explorationCredentials()
	if err != nil {
		return server.ExplorationPipelineOptions{}, false
	}
	options.Credentials = credentials
	if factory := b.hostCapabilities.Exploration; factory != nil {
		options.OpenHost = func(ctx context.Context, got exploration.Binding, w *agent.Worker) (server.ExplorationPipelineHost, error) {
			return factory(ctx, got, w)
		}
	} else if host := b.product.exploration; host != nil && b.product.targets != nil {
		// 설치 정책에 탐색 블록이 있으면 실제 경계·브라우저 샌드박스를 쓴다. 이 실행의 대상 기록·원본 digest·제외 경로로 묶는다.
		excluded := []string{}
		if e.Job.Input.ExcludedPaths != "" {
			excluded = strings.Split(e.Job.Input.ExcludedPaths, "\n")
		}
		// 탐색 기록 가림용 이 회차 시험 계정 비밀번호(메모리에만). 만들지 못하면 factory가 탐색을 열지 않는다.
		passwords, _ := l.explorationPasswords()
		options.OpenHost = host.factory(*l.productTarget, b.product.targets.current, e.Job.SourceDigest, excluded, limits.MaxResponseBytes, l.productConnection, passwords, productExplorationLogin(credentials))
	}

	return options, true
}

func (b *urlPreparationRuntime) productExplorationStage(l *urlLoadedProfile, e diagnosisprep.Execution, taskID string, flow *diagnosisflow.Controller) server.ExplorationStageFactory {
	// 작업대 '보낸 요청' 총계의 탐색 몫(url_target_requests.go). 탐색 단계가 없는 회차도 없음을 기록한다(기록이 없으면 모름).
	stateDir := filepath.Join(b.config.RunsDir, e.Job.ID, "state")
	options, ok := b.productExplorationOptions(l, e, taskID, flow)
	if !ok {
		recordExplorationStage(stateDir, explorationKnown(explorationNone, 0))
		return nil
	}
	return func(host *server.Server) server.E2HTTPExplorationStage {
		return func(ctx context.Context, task *server.Task) (server.ExplorationCharge, error) {
			if task == nil || task.ID != taskID {
				return server.ExplorationCharge{}, exploration.ErrBinding
			}
			recordExplorationStage(stateDir, explorationRequestsRecord{Format: explorationRequestsFormat, State: explorationRunning})
			result, err := host.RunExplorationPipeline(ctx, task, options)
			recordExplorationStage(stateDir, explorationResultRecord(result.Status == "NOT_RUN", result.Charge.Known, result.Charge.Requests))
			// 탐색 단계 로그인 시도 행을 회차 기록에 옮긴다. 진단 단계(E②)가 이 행으로 아이디별 연속 실패를 이어 센다(계약 §5).
			b.recordExplorationLoginAttempts(ctx, stateDir, options.Binding, options.Credentials)
			return result.Charge, err
		}
	}
}

// productExplorationPaths는 실행에 결속된 대상 승인 경로다(승인 기록 그대로, 순서 유지). 없으면 nil이다.
func productExplorationPaths(job diagnosisprep.Job) []string {
	if job.Scope == nil {
		return nil
	}
	return append([]string(nil), job.Scope.PathPrefixes...)
}

// productExplorationLogin은 탐색 경계의 로그인 시도 판정 설정이다. 계정이 없어도 판정한다(등록 밖 아이디도 연속 실패
// 상한을 받는다, 계약 §1). 자격은 진단 단계와 같은 실행 자료에서 읽은 것이다.
func productExplorationLogin(credentials []agent.E2HTTPAccountCredential) *exploreboundary.WireLogin {
	login := &exploreboundary.WireLogin{Accounts: []exploreboundary.WireLoginAccount{}, MaxConsecutiveFailures: productLoginConsecutiveFailures,
		MaxContrastProbes: productLoginContrastProbes, TokenMinBytes: productLoginTokenMinBytes}
	for _, c := range credentials {
		login.Accounts = append(login.Accounts, exploreboundary.WireLoginAccount{Label: c.Label, Username: c.Username, Password: c.Password})
	}
	return login
}

// recordExplorationLoginAttempts는 탐색 경계의 로그인 시도 행을 회차 상태 디렉터리 기록에 덧붙인다. 읽거나 쓰지 못하면
// 로그만 남긴다(진단 단계는 탐색 실패 수를 모르는 채 0부터 센다).
func (b *urlPreparationRuntime) recordExplorationLoginAttempts(ctx context.Context, stateDir string, binding exploration.Binding, credentials []agent.E2HTTPAccountCredential) {
	host := b.product.exploration
	if host == nil || b.hostCapabilities.Exploration != nil {
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), explorationCloseTimeout)
	defer cancel()
	// 넘겨받지 못하면(읽기·쓰기 실패, 상한으로 빠진 행) 표지를 남긴다. 진단 단계는 표지를 보고 로그인 시도를 보내지 않는다
	// (회차 공통 연속 실패 상한을 지킬 수 없으므로, #969 동찬 리뷰 P2). 표지를 못 쓰면 로그만 남긴다.
	unavailable := func(why string) {
		log.Print("[first-diagnosis] 탐색 로그인 시도 기록을 넘겨받지 못함: " + why + "(진단 단계는 로그인 시도를 보내지 않음)")
		if server.MarkLoginHistoryUnavailable(stateDir) != nil {
			log.Print("[first-diagnosis] 탐색 로그인 기록 인계 실패 표지를 쓰지 못함")
		}
	}
	rows, omitted, err := host.loginAttempts(readCtx, binding)
	if err != nil {
		unavailable("경계 조회 실패")
		return
	}
	if omitted > 0 {
		log.Printf("[first-diagnosis] 탐색 로그인 시도 행 %d개가 경계 상한으로 빠짐", omitted)
		unavailable("행 빠짐")
	}
	if err := server.AppendLoginAttemptRows(stateDir, rows); err != nil {
		unavailable("기록 쓰기 실패")
		return
	}
	labels := make([]string, 0, len(credentials))
	for _, c := range credentials {
		labels = append(labels, c.Label)
	}
	if err := writeLoginSummary(stateDir, loginattempt.AccountAuthOfRows(rows, labels)); err != nil {
		log.Print("[first-diagnosis] 탐색 로그인 요약을 쓰지 못함")
	}
}

// loginSummaryFile은 회차 상태 디렉터리의 탐색 로그인 요약(0600)이다(계약 model-login-attempts §6). 탐색 단계 행만으로 정한
// 계정별 결과를 남겨, 탐색이 대조를 못 세운 비율을 회차마다 본다(사용자 결정 2026-11-26). 화면 API에는 싣지 않는다.
const (
	loginSummaryFile   = "login-summary.json"
	loginSummaryFormat = "abc2lab.login-summary/v1"
)

func writeLoginSummary(stateDir string, exploration map[string]string) error {
	if stateDir == "" {
		return errPreparation
	}
	raw, err := json.Marshal(struct {
		Format      string            `json:"format"`
		Exploration map[string]string `json:"exploration"`
	}{loginSummaryFormat, exploration})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, loginSummaryFile), append(raw, '\n'), 0o600)
}

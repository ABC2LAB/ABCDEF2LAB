package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploresandbox"
	"github.com/Autumn-27/norma/harness"
	actool "github.com/Autumn-27/norma/tool"
)

var ErrExplorationClosure = errors.New("exploration_cleanup_incomplete")
var ErrExplorationObservation = errors.New("exploration_observation_unavailable")

type explorationMCPContextKey struct{}

func isExplorationMCPContext(ctx context.Context) bool {
	v, _ := ctx.Value(explorationMCPContextKey{}).(bool)
	return v
}

// BrowserInfo는 서버가 직접 만든 브라우저 컨테이너 lease의 attestation이다. 모델/공개 JSON이 발급하지 않는다.
// host는 실제 이미지·패키지 판·no-webmcp·caps 없음·file URL 차단을 따로 증명해야 한다(호스트 의무 문서).
type ExplorationBrowserInfo struct {
	Binding        exploration.Binding
	PackageVersion string
	MCP            db.MCPServer
}

// Close는 해당 세션의 브라우저 프로필·컨테이너·자식 프로세스를 실제 정리해야 한다.
// 현재 제품에는 이 인터페이스를 자동 활성화하는 Docker/경계 구현을 설치하지 않는다.
type ExplorationBrowserLease interface {
	Info() ExplorationBrowserInfo
	Close(context.Context) error
}
type ExplorationSessionOptions struct {
	Worker       *agent.Worker
	Binding      exploration.Binding
	Boundary     exploration.Boundary
	StartBrowser func(context.Context, exploration.Binding) (ExplorationBrowserLease, error)
	// DirectBash는 회차에 결속된 탐색 직접 Bash다(직접 입구 하나만 잇는 격리 실행, exploresandbox). 없으면 셸 없이 돈다.
	DirectBash *exploresandbox.DirectBash
	// APIRecon은 원본 스크립트를 일반 Skill로 여는 경로라 아직 지원하지 않는다. non-nil이면 I/O 전에 거부한다.
	APIRecon actool.CoreTool
	Redact   func([]byte) []byte
	// Finalize는 모델/도구와 브라우저 폐쇄 뒤 보호 저장소의 기존 관찰만 배출한다. 새 송신·모델 호출·원문 로그를 만들지 않으며 문맥 상한을 지킨다.
	Finalize func(context.Context, exploration.Binding) error
	// Observe는 각 경계 도구 결과 뒤 private 관찰만 저장하고 다음 모델 호출 전에 돌아온다.
	Observe        func(context.Context, exploration.Binding) error
	CloseTimeout   time.Duration
	StatusInterval time.Duration
}

type ExplorationSession struct {
	options           ExplorationSessionOptions
	ctx               context.Context
	cancel            context.CancelFunc
	boundary          exploration.Lease
	browser           ExplorationBrowserLease
	client            mcpClient
	tools             []actool.CoreTool
	mu                sync.Mutex
	closed            bool
	ran               bool
	reason            exploration.Reason
	once              sync.Once
	closeErr          error
	done              chan struct{}
	finalizeReady     bool
	workerResult      agent.ExplorationRunResult
	workerErr         error
	observationFailed bool
}

func OpenExplorationSession(parent context.Context, o ExplorationSessionOptions) (*ExplorationSession, error) {
	if parent == nil || parent.Err() != nil || !o.Worker.ExplorationReady() || o.Binding.Validate() != nil || o.Boundary == nil || o.StartBrowser == nil || o.Redact == nil || o.CloseTimeout < 4*time.Millisecond || o.CloseTimeout > time.Minute {
		return nil, exploration.ErrUnavailable
	}
	if o.StatusInterval <= 0 {
		o.StatusInterval = 250 * time.Millisecond
	}
	if o.StatusInterval > time.Second {
		return nil, exploration.ErrBinding
	}
	// 범용 Skill은 현재 지원하지 않는다. 직접 Bash는 typed adapter(exploresandbox)만 받는다.
	if o.APIRecon != nil {
		return nil, exploration.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(o.Binding.Limits.TimeBudgetSeconds)*time.Second)
	s := &ExplorationSession{options: o, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	fail := func() (*ExplorationSession, error) {
		closeErr := s.Close(context.Background(), exploration.BoundaryUnavailable)
		if closeErr != nil {
			exploration.ReportFailure("open_cleanup", closeErr)
			return nil, errors.Join(exploration.ErrUnavailable, ErrExplorationClosure, closeErr)
		}
		return nil, exploration.ErrUnavailable
	}
	lease, err := o.Boundary.Open(ctx, o.Binding)
	if err != nil || lease == nil {
		exploration.ReportFailure("boundary_open", err)
		cancel()
		if lease != nil {
			s.boundary = lease
			return fail()
		}
		return nil, exploration.ErrUnavailable
	}
	s.boundary = lease
	if s.Check(ctx) != nil {
		return fail()
	}
	browser, err := o.StartBrowser(ctx, o.Binding)
	s.browser = browser
	if err != nil || browser == nil {
		exploration.ReportFailure("browser_start", err)
		return fail()
	}
	info := browser.Info()
	if !validExplorationBrowser(info, o.Binding) {
		exploration.ReportFailure("browser_start", exploration.ErrBinding)
		return fail()
	}
	client, err := connectMCP(context.WithValue(ctx, explorationMCPContextKey{}, true), &info.MCP)
	s.client = client
	if err != nil || client == nil {
		exploration.ReportFailure("mcp_connect", err)
		return fail()
	}
	tools, err := agentMCPTools(ctx, &info.MCP, client)
	if err != nil || !completeExplorationCatalog(tools) {
		exploration.ReportFailure("catalog", exploration.ErrBinding)
		return fail()
	}
	s.tools = tools
	if s.Check(ctx) != nil {
		return fail()
	}
	s.finalizeReady = true
	go s.watch()
	return s, nil
}
func validExplorationBrowser(info ExplorationBrowserInfo, b exploration.Binding) bool {
	if info.Binding != b || info.PackageVersion != exploration.MCPPackageVersion || info.MCP.Name != exploration.MCPServerName || info.MCP.Transport != "http" || info.MCP.Insecure || info.MCP.Command != "" || !emptyExplorationArgs(info.MCP.Args) || !emptyExplorationEnv(info.MCP.Env) {
		return false
	}
	u, e := url.Parse(info.MCP.URL)
	return e == nil && u.Scheme == "http" && u.Hostname() != "" && u.Port() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/" || u.Path == "/mcp")
}
func emptyExplorationArgs(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var args []string
	return json.Unmarshal(raw, &args) == nil && len(args) == 0
}
func emptyExplorationEnv(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var env map[string]string
	return json.Unmarshal(raw, &env) == nil && len(env) == 0
}
func nilExplorationTool(t actool.CoreTool) bool {
	v := reflect.ValueOf(t)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
func completeExplorationCatalog(tools []actool.CoreTool) bool {
	seen := map[string]bool{}
	prefix := "mcp__" + exploration.MCPServerName + "__"
	for _, t := range tools {
		if t == nil || nilExplorationTool(t) {
			return false
		}
		base, ok := strings.CutPrefix(t.Name(), prefix)
		if !ok || !exploration.AllowedBrowserTool(base) || seen[base] {
			return false
		}
		seen[base] = true
	}
	return len(seen) == len(exploration.BrowserToolNames())
}
func (s *ExplorationSession) Binding() exploration.Binding { return s.options.Binding }
func (s *ExplorationSession) Context() context.Context     { return s.ctx }
func (s *ExplorationSession) Check(ctx context.Context) error {
	if s == nil || ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil {
		return exploration.ErrClosed
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return exploration.ErrClosed
	}
	state, err := s.boundary.Status(ctx)
	if err != nil || state.Validate(s.options.Binding) != nil {
		if err == nil {
			err = exploration.ErrBinding
		}
		exploration.ReportFailure("status", err)
		return exploration.ErrUnavailable
	}
	if !state.Active {
		return exploration.ErrClosed
	}
	s.mu.Lock()
	closed = s.closed
	s.mu.Unlock()
	if closed || s.ctx.Err() != nil {
		return exploration.ErrClosed
	}
	return nil
}
func (s *ExplorationSession) watch() {
	ticker := time.NewTicker(s.options.StatusInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			reason := exploration.Cancelled
			if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
				reason = exploration.CapExhausted
			}
			s.mu.Lock()
			if s.observationFailed {
				reason = exploration.BoundaryUnavailable
			}
			s.mu.Unlock()
			s.closeAndReport(reason, "watch_close")
			return
		case <-ticker.C:
			state, err := s.boundary.Status(s.ctx)
			if err != nil || state.Validate(s.options.Binding) != nil {
				if s.ctx.Err() != nil {
					continue
				}
				if err == nil {
					err = exploration.ErrBinding
				}
				exploration.ReportFailure("status", err)
				s.closeAndReport(exploration.BoundaryUnavailable, "watch_close")
				return
			}
			if !state.Active {
				s.closeAndReport(state.ClosedReason, "watch_close")
				return
			}
		}
	}
}

// Run은 제품 단계 callback이 호출한다. 같은 세션을 다시 실행해 모델/도구 상한을 갱신하지 않는다.
func (s *ExplorationSession) Run(ctx context.Context, w *agent.Worker, o agent.ExplorationRunOptions, emit func(db.Activity)) (result agent.ExplorationRunResult, err error) {
	if s == nil || ctx == nil || ctx.Err() != nil || w != s.options.Worker {
		return result, exploration.ErrClosed
	}
	s.mu.Lock()
	if s.closed || s.ran {
		s.mu.Unlock()
		return result, exploration.ErrClosed
	}
	s.ran = true
	s.mu.Unlock()
	runCtx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	o.Binding = s.options.Binding
	o.BrowserTools = append([]actool.CoreTool{}, s.tools...)
	if s.options.Observe != nil {
		wrap := func(tool actool.CoreTool) actool.CoreTool {
			if tool == nil {
				return nil
			}
			return &explorationObservedTool{CoreTool: tool, observe: func(ctx context.Context) error {
				if s.options.Observe(ctx, s.options.Binding) != nil {
					if ctx.Err() == nil {
						s.mu.Lock()
						s.observationFailed = true
						s.mu.Unlock()
					}
					s.cancel()
					return exploration.ErrUnavailable
				}
				return nil
			}}
		}
		for i, tool := range o.BrowserTools {
			o.BrowserTools[i] = wrap(tool)
		}
		o.ObserveBoundaryTool = wrap
		o.APIRecon = wrap(s.options.APIRecon)
	} else {
		o.APIRecon = s.options.APIRecon
	}
	o.DirectBash = s.options.DirectBash
	o.Redact = s.options.Redact
	o.Check = s.Check
	defer func() {
		reason := exploration.Completed

		if runCtx.Err() != nil {
			reason = exploration.Cancelled
			if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
				reason = exploration.CapExhausted
			}
		}
		if runCtx.Err() == nil {
			switch {
			case result.Reason == harness.ReasonModelError:
				reason = exploration.ModelError
			case err != nil:
				reason = exploration.RuntimeUnavailable
			case result.Reason == harness.ReasonMaxTurns || result.Reason == harness.ReasonTimeout:
				reason = exploration.CapExhausted
			case result.Reason != harness.ReasonCompleted:
				reason = exploration.RuntimeUnavailable
			}
		}
		s.mu.Lock()
		if s.observationFailed {
			reason = exploration.BoundaryUnavailable
		}
		s.mu.Unlock()
		if err != nil {
			exploration.ReportFailure("worker_run", err)
		}
		if closeErr := s.Close(context.Background(), reason); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	result, err = w.ExecuteExploration(runCtx, o, emit)
	s.mu.Lock()
	if s.observationFailed {
		err = errors.Join(err, ErrExplorationObservation)
	}
	s.workerResult = result
	s.workerErr = err
	s.mu.Unlock()
	return result, err
}

// Close는 모델 도구를 먼저 폐쇄하고, 실행 취소·MCP·브라우저 소유 자원·경계 세션을 순서대로 정리한다.
func (s *ExplorationSession) Close(parent context.Context, reason exploration.Reason) error {
	if s == nil {
		return nil
	}
	if parent == nil || !reason.Valid() {
		return exploration.ErrBinding
	}
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.reason = reason
		s.mu.Unlock()
		s.cancel()
		base := context.WithoutCancel(parent)
		steps := 4
		if s.finalizeReady && s.options.Finalize != nil {
			steps++
		}
		budget := s.options.CloseTimeout / time.Duration(steps)
		var errs []error
		clean := func(stage string, fn func(context.Context) error) {
			ctx, cancel := context.WithTimeout(base, budget)
			defer cancel()
			err := fn(ctx)
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if err != nil {
				errs = append(errs, exploration.ReportFailure(stage, err))
			}
		}
		if s.client != nil {
			clean("browser_close", func(ctx context.Context) error {
				for _, tool := range s.tools {
					if tool.Name() == "mcp__"+exploration.MCPServerName+"__browser_close" {
						result, err := tool.Call(ctx, json.RawMessage(`{}`), nil)
						if err != nil {
							return err
						}
						if result.IsError {
							return exploration.ErrToolResult
						}
						break
					}
				}
				return nil
			})
		}
		// 브라우저의 정상 종료가 끝난 뒤 기존 관찰을 읽는다. 이 동안 경계의 private reader는 닫지 않는다.
		if s.finalizeReady && s.options.Finalize != nil {
			clean("finalize", func(ctx context.Context) error { return s.options.Finalize(ctx, s.options.Binding) })
		}
		if s.client != nil {
			clean("mcp_delete", func(ctx context.Context) error {
				if cl, ok := s.client.(interface{ CloseContext(context.Context) error }); ok {
					return cl.CloseContext(ctx)
				}
				return s.client.Close()
			})
		}
		if s.browser != nil {
			clean("browser_lease", s.browser.Close)
		}
		if s.boundary != nil {
			clean("boundary_close", func(ctx context.Context) error {
				state, err := s.boundary.Close(ctx, reason)
				if err != nil {
					return err
				}
				if state.Validate(s.options.Binding) != nil || state.Active || (reason != exploration.Completed && state.ClosedReason == exploration.Completed) {
					return exploration.ErrBinding
				}
				// Close와 대상 철회가 경합하면 경계가 실제로 확정한 마감 사유를 남긴다.
				s.mu.Lock()
				s.reason = state.ClosedReason
				s.mu.Unlock()
				return nil
			})
		}
		if len(errs) > 0 {
			s.closeErr = errors.Join(ErrExplorationClosure, errors.Join(errs...))
		}
		close(s.done)
	})
	return s.closeErr
}
func (s *ExplorationSession) ClosedReason() exploration.Reason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// WorkerResult는 마감 callback이 모델 오류를 탐색 완료와 구별할 때만 쓰는 호스트 자료다.
func (s *ExplorationSession) WorkerResult() (agent.ExplorationRunResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.workerResult
	result.Tools = append([]string{}, result.Tools...)
	return result, s.workerErr
}

type explorationObservedTool struct {
	actool.CoreTool
	observe func(context.Context) error
}

func (t *explorationObservedTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	result, err := t.CoreTool.Call(ctx, in, tc)
	if ctx.Err() == nil && t.observe(ctx) != nil {
		return actool.Errorf("exploration_observation_unavailable"), nil
	}
	return result, err
}
func (s *ExplorationSession) closeAndReport(reason exploration.Reason, stage string) {
	if err := s.Close(context.Background(), reason); err != nil {
		exploration.ReportFailure(stage, err)
	}
}

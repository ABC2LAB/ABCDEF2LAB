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
	actool "github.com/Autumn-27/norma/tool"
)

type explorationMCPContextKey struct{}

func isExplorationMCPContext(ctx context.Context) bool {
	v, _ := ctx.Value(explorationMCPContextKey{}).(bool)
	return v
}

// BrowserInfo는 서버가 직접 만든 브라우저 컨테이너 lease의 결속이다. 공개 JSON 설치 입력이나 모델이 발급하지 않는다.
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
	// DirectBash와 APIRecon은 이 binding의 직접 샌드박스 도구만 받는다. 일반 ToolAugment 결과는 받지 않는다.
	DirectBash actool.CoreTool
	APIRecon   actool.CoreTool
	Redact     func([]byte) []byte
	// Finalize는 모델/도구와 브라우저 폐쇄 뒤 보호 저장소의 기존 관찰만 배출한다. 새 송신·모델 호출·원문 로그를 만들지 않으며 문맥 상한을 지킨다.
	Finalize       func(context.Context, exploration.Binding) error
	CloseTimeout   time.Duration
	StatusInterval time.Duration
}

type ExplorationSession struct {
	options       ExplorationSessionOptions
	ctx           context.Context
	cancel        context.CancelFunc
	boundary      exploration.Lease
	browser       ExplorationBrowserLease
	client        mcpClient
	tools         []actool.CoreTool
	mu            sync.Mutex
	closed        bool
	ran           bool
	reason        exploration.Reason
	once          sync.Once
	closeErr      error
	done          chan struct{}
	finalizeReady bool
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
	for _, named := range []struct {
		name string
		t    actool.CoreTool
	}{{"Bash", o.DirectBash}, {"Skill", o.APIRecon}} {
		if named.t != nil && (nilExplorationTool(named.t) || named.t.Name() != named.name) {
			return nil, exploration.ErrBinding
		}
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(o.Binding.Limits.TimeBudgetSeconds)*time.Second)
	s := &ExplorationSession{options: o, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	fail := func() (*ExplorationSession, error) {
		_ = s.Close(context.Background(), exploration.BoundaryUnavailable)
		return nil, exploration.ErrUnavailable
	}
	lease, err := o.Boundary.Open(ctx, o.Binding)
	if err != nil || lease == nil {
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
		return fail()
	}
	info := browser.Info()
	if !validExplorationBrowser(info, o.Binding) {
		return fail()
	}
	client, err := connectMCP(context.WithValue(ctx, explorationMCPContextKey{}, true), &info.MCP)
	s.client = client
	if err != nil || client == nil {
		return fail()
	}
	tools, err := agentMCPTools(ctx, &info.MCP, client)
	if err != nil || !completeExplorationCatalog(tools) {
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
			_ = s.Close(context.Background(), reason)
			return
		case <-ticker.C:
			state, err := s.boundary.Status(s.ctx)
			if err != nil || state.Validate(s.options.Binding) != nil {
				_ = s.Close(context.Background(), exploration.BoundaryUnavailable)
				return
			}
			if !state.Active {
				_ = s.Close(context.Background(), state.ClosedReason)
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
	o.DirectBash = s.options.DirectBash
	o.APIRecon = s.options.APIRecon
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
		if closeErr := s.Close(context.Background(), reason); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return w.ExecuteExploration(runCtx, o, emit)
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
		clean := func(fn func(context.Context) error) {
			ctx, cancel := context.WithTimeout(base, budget)
			defer cancel()
			if err := fn(ctx); err != nil || ctx.Err() != nil {
				errs = append(errs, exploration.ErrUnavailable)
			}
		}
		if s.client != nil {
			clean(func(ctx context.Context) error {
				for _, tool := range s.tools {
					if tool.Name() == "mcp__"+exploration.MCPServerName+"__browser_close" {
						result, err := tool.Call(ctx, json.RawMessage(`{}`), nil)
						if err != nil || result.IsError {
							return exploration.ErrUnavailable
						}
						break
					}
				}
				return nil
			})
		}
		// 브라우저의 정상 종료가 끝난 뒤 기존 관찰을 읽는다. 이 동안 경계의 private reader는 닫지 않는다.
		if s.finalizeReady && s.options.Finalize != nil {
			clean(func(ctx context.Context) error { return s.options.Finalize(ctx, s.options.Binding) })
		}
		if s.client != nil {
			clean(func(ctx context.Context) error {
				if cl, ok := s.client.(interface{ CloseContext(context.Context) error }); ok {
					return cl.CloseContext(ctx)
				}
				return s.client.Close()
			})
		}
		if s.browser != nil {
			clean(s.browser.Close)
		}
		if s.boundary != nil {
			clean(func(ctx context.Context) error {
				state, err := s.boundary.Close(ctx, reason)
				if err != nil || state.Validate(s.options.Binding) != nil || state.Active {
					return exploration.ErrUnavailable
				}
				// Close와 대상 철회가 경합하면 경계가 실제로 확정한 마감 사유를 남긴다.
				s.mu.Lock()
				s.reason = state.ClosedReason
				s.mu.Unlock()
				return nil
			})
		}
		s.closeErr = errors.Join(errs...)
		close(s.done)
	})
	return s.closeErr
}
func (s *ExplorationSession) ClosedReason() exploration.Reason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

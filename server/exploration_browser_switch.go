package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/exploration"
)

// 브라우저 문맥 바꾸기(사용자 결정 2026-11-26 (나): 한 번에 한 계정 문맥). MCP는 연결마다 새 브라우저 문맥을 만든다
// (--isolated). 계정을 바꾸면 지금 연결을 닫아 문맥(쿠키 저장소)을 버리고 새 연결을 연다. 저장해 둔 세션은 새 문맥에 다시
// 싣지 않으므로, 경계에 문맥을 새로 열었다고 알려 앞 계정을 '로그인 안 됨'으로 돌린다(다시 로그인 가능).

var errExplorationBrowserSwitch = errors.New("exploration_browser_switch_failed")

// explorationBrowserTool은 모델에 넘기는 브라우저 도구다. 이름·설명·입력 형식은 처음 연결의 도구 것이고, 부를 때마다
// 지금 연결의 같은 이름 도구로 보낸다. 문맥을 바꾸는 동안에는 도구 호출을 기다리게 한다.
type explorationBrowserTool struct {
	actool.CoreTool
	s *ExplorationSession
}

func (t *explorationBrowserTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	t.s.switchMu.RLock()
	defer t.s.switchMu.RUnlock()
	current := t.s.currentBrowserTool(t.Name())
	if current == nil {
		return actool.Errorf("browser_unavailable"), nil
	}
	return current.Call(ctx, in, tc)
}

func (s *ExplorationSession) currentBrowserTool(name string) actool.CoreTool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// browserTools는 모델에 넘길 브라우저 도구(지금 연결을 따라가는 감싸개)다.
func (s *ExplorationSession) browserTools() []actool.CoreTool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]actool.CoreTool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, &explorationBrowserTool{CoreTool: t, s: s})
	}
	return out
}

// closeBrowserContext는 지금 문맥의 브라우저를 닫고 MCP 연결을 지운다(문맥이 닫힌다). 실패는 오류로 돌려준다.
func closeBrowserContext(ctx context.Context, client mcpClient, tools []actool.CoreTool) error {
	var errs []error
	for _, tool := range tools {
		if tool.Name() == "mcp__"+exploration.MCPServerName+"__browser_close" {
			result, err := tool.Call(ctx, json.RawMessage(`{}`), nil)
			if err == nil && result.IsError {
				err = exploration.ErrToolResult
			}
			errs = append(errs, err)
			break
		}
	}
	if client != nil {
		if cl, ok := client.(interface{ CloseContext(context.Context) error }); ok {
			errs = append(errs, cl.CloseContext(ctx))
		} else {
			errs = append(errs, client.Close())
		}
	}
	return errors.Join(errs...)
}

// switchBrowser는 지금 브라우저 문맥을 닫고 새 문맥을 연 뒤 경계에 알린다. 새 연결을 열지 못하면 브라우저 도구는 쓸 수
// 없게 되고(오류), 경계 알림이 실패하면 앞 계정이 '로그인됨'으로 남아 다른 계정 로그인이 막힌다(안전한 쪽).
func (s *ExplorationSession) switchBrowser(ctx context.Context, account string) error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	if ctx == nil || s.Check(ctx) != nil {
		return exploration.ErrClosed
	}
	s.mu.Lock()
	oldClient, oldTools, info := s.client, s.tools, s.mcp
	s.client, s.tools = nil, nil
	s.mu.Unlock()
	closeCtx, cancel := context.WithTimeout(ctx, s.options.CloseTimeout)
	if err := closeBrowserContext(closeCtx, oldClient, oldTools); err != nil {
		exploration.ReportFailure("browser_switch_close", err)
	}
	cancel()
	client, err := connectMCP(context.WithValue(s.ctx, explorationMCPContextKey{}, true), &info)
	if err != nil || client == nil {
		exploration.ReportFailure("browser_switch_connect", err)
		return errExplorationBrowserSwitch
	}
	tools, err := agentMCPTools(s.ctx, &info, client)
	if err != nil || !completeExplorationCatalog(tools) {
		exploration.ReportFailure("browser_switch_catalog", exploration.ErrBinding)
		_ = closeBrowserContext(ctx, client, nil)
		return errExplorationBrowserSwitch
	}
	s.mu.Lock()
	s.client, s.tools = client, tools
	s.mu.Unlock()
	if resetter, ok := s.boundary.(interface{ ResetBrowserContext(context.Context) error }); ok {
		if err := resetter.ResetBrowserContext(ctx); err != nil {
			exploration.ReportFailure("browser_context_reset", err)
			return errExplorationBrowserSwitch
		}
	}
	return nil
}

// switchLockPoll은 마감이 문맥 바꾸기 잠금을 기다릴 때 다시 보는 간격이다.
const switchLockPoll = 5 * time.Millisecond

// tryLockSwitch는 문맥 바꾸기 잠금을 d까지만 기다려 잡는다. 잡으면 참이고 호출자가 푼다.
func (s *ExplorationSession) tryLockSwitch(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if s.switchMu.TryLock() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(switchLockPoll)
	}
}

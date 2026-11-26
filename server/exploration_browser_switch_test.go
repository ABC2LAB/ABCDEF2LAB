package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
)

// explorationFixtureResets는 경계에 보낸 브라우저 문맥 초기화 알림 수다.
var explorationFixtureResets atomic.Int32

func (l *explorationFixtureLease) ResetBrowserContext(context.Context) error {
	explorationFixtureResets.Add(1)
	l.events.add("boundary:context_reset")
	return nil
}

// 한 번에 한 계정 문맥(사용자 결정 2026-11-26 (나)): 등록 계정이 있는 탐색에는 switch_browser_account 도구가 있고, 부르면
// 지금 문맥의 브라우저를 닫고 MCP 연결을 지운 뒤(문맥 닫힘) 새 연결을 열고 경계에 문맥 초기화를 알린다. 마감은 지금(새)
// 연결을 닫는다. 등록 계정이 없으면 도구가 없다.
func TestExplorationSessionSwitchesBrowserContext(t *testing.T) {
	explorationFixtureResets.Store(0)
	o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
	p.firstTool, p.firstInput = "switch_browser_account", `{"account":"tester-a"}`
	s, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	creds := []agent.E2HTTPAccountCredential{{Label: "tester-a", Username: "user1", Password: "alpha-pass-1"}}
	result, err := s.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second, Credentials: creds}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hasSwitch := false
	for _, name := range result.Tools {
		hasSwitch = hasSwitch || name == "switch_browser_account"
	}
	if !hasSwitch || explorationFixtureResets.Load() != 1 {
		t.Fatalf("문맥 바꾸기 도구·경계 알림: tool=%v resets=%d", hasSwitch, explorationFixtureResets.Load())
	}
	events := strings.Join(b.lease.events.read(), ",")
	if !strings.Contains(events, "mcp:browser_close,mcp:delete,boundary:context_reset") {
		t.Fatalf("문맥 바꾸기 순서: %s", events)
	}
	if err := s.Close(t.Context(), exploration.Completed); err != nil {
		t.Fatal(err)
	}
	if f.deleted.Load() != 2 || browser.closes.Load() != 1 {
		t.Fatalf("바꾼 뒤 새 연결을 마감에서 닫지 않음: deleted=%d", f.deleted.Load())
	}
	// 등록 계정이 없으면 도구가 없다.
	o2, _, _, _, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
	s2, err := OpenExplorationSession(t.Context(), o2)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s2.Run(t.Context(), o2.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range result.Tools {
		if name == "switch_browser_account" {
			t.Fatal("등록 계정 없는 탐색에 문맥 바꾸기 도구가 있음")
		}
	}
	_ = s2.Close(t.Context(), exploration.Completed)
}

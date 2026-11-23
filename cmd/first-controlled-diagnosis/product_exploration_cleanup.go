package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/mcphttp"
)

// 재시작 정리(2026-11-24 사용자 결정, docs/contracts/exploration-boundary.md 세션 수명).
//
// 준비 서비스가 갑자기 죽으면(#559 자동 재시작 포함) 탐색 경계 세션과 브라우저 문맥(MCP 세션)이 남는다. 탐색을 모는
// 모델 루프는 같이 죽지만, 브라우저에 열린 페이지가 스스로 보내는 요청(타이머·주기 조회·자동 새로고침)은 경계 세션
// 기한까지 대상에 닿을 수 있다. 새로 시작한 준비 서비스는 새 탐색을 열기 전에 남은 경계 세션을 닫고(감사 원자료
// closed_by restart_cleanup) 닫힘을 확인하며, 기록해 둔 MCP 세션을 지워 그 페이지들을 닫는다. 경계는 따로 실행기의
// 상태 조회가 끊기면 세션을 닫는다(exploreboundary Manager.Heartbeat).

const (
	// productMCPSessionsFile은 열린 탐색 MCP 세션 ID 목록(보호 상태 디렉터리, 0600)이다.
	productMCPSessionsFile = "mcp-sessions.json"
	// productExplorationStateDir는 탐색 재시작 정리 상태를 두는 하위 디렉터리(0700)다.
	productExplorationStateDir = "exploration"
	// 남길 수 있는 MCP 세션 수 상한. 탐색은 한 번에 하나라 보통 0~1개이고, 지우지 못한 것이 쌓여도 이만큼에서 멈춘다.
	maxProductMCPSessions = 16
	// 시작 정리의 한 번 시도 시간과 다시 시도 간격. 경계가 준비 서비스보다 늦게 뜰 수 있어 몇 번 다시 해 본다.
	explorationCleanupAttempt = 10 * time.Second
	explorationCleanupRetry   = 2 * time.Second
	explorationCleanupTries   = 15
)

type productMCPSession struct {
	Address   string `json:"address"`
	SessionID string `json:"session_id"`
}

// productMCPSessionStore는 mcphttp.ScopedSessionRecorder다. 열린 세션을 파일에 남기고 지운 세션을 뺀다.
type productMCPSessionStore struct {
	mu   sync.Mutex
	path string
}

func newProductMCPSessionStore(stateDir string) (*productMCPSessionStore, error) {
	dir := filepath.Join(stateDir, productExplorationStateDir)
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if checkDirectory(dir, true) != nil {
		return nil, errPreparation
	}
	return &productMCPSessionStore{path: filepath.Join(dir, productMCPSessionsFile)}, nil
}

func (s *productMCPSessionStore) readLocked() ([]productMCPSession, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []productMCPSession
	if err := json.Unmarshal(raw, &out); err != nil || len(out) > maxProductMCPSessions {
		return nil, errPreparation
	}
	return out, nil
}

func (s *productMCPSessionStore) writeLocked(list []productMCPSession) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *productMCPSessionStore) Opened(address, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		return err
	}
	if len(list) >= maxProductMCPSessions {
		return errPreparation
	}
	return s.writeLocked(append(list, productMCPSession{Address: address, SessionID: sessionID}))
}

func (s *productMCPSessionStore) Closed(address, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readLocked()
	if err != nil {
		log.Print("[exploration] 탐색 MCP 세션 기록을 읽지 못함")
		return
	}
	kept := list[:0]
	for _, e := range list {
		if e.Address != address || e.SessionID != sessionID {
			kept = append(kept, e)
		}
	}
	if err := s.writeLocked(kept); err != nil {
		log.Print("[exploration] 탐색 MCP 세션 기록을 고치지 못함")
	}
}

func (s *productMCPSessionStore) pending() ([]productMCPSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

// cleanupStale은 이전 실행이 남긴 경계 세션을 닫고 닫힘을 확인한 뒤, 기록된 MCP 세션을 지운다. 경계 세션을 닫지
// 못하면 오류다(새 탐색을 열면 안 된다). MCP 세션은 지울 수 있는 만큼 지우고, 남은 것은 다음에 다시 지운다(경계가
// 닫혀 있어 그 페이지는 대상에 닿지 못한다).
func (h *productExplorationHost) cleanupStale(ctx context.Context) (closed bool, err error) {
	st, err := h.control.CurrentSession(ctx)
	if err != nil {
		return false, err
	}
	if st != nil && st.Active {
		if _, err := h.control.CloseStale(ctx, st.Binding); err != nil {
			return false, err
		}
		if err := h.clearForRelease(ctx); err != nil {
			return false, err
		}
		closed = true
		log.Printf("[exploration] 재시작 정리: 이전 실행이 남긴 탐색 경계 세션을 닫음(작업 %s)", st.Binding.JobID)
	}
	if h.mcpSessions == nil {
		return closed, nil
	}
	list, err := h.mcpSessions.pending()
	if err != nil {
		log.Print("[exploration] 재시작 정리: 탐색 MCP 세션 기록을 읽지 못함")
		return closed, nil
	}
	for _, e := range list {
		if e.Address != h.mcpURL {
			// 다른 주소의 기록은 이 설치의 MCP가 아니다. 보내지 않고 기록에서 뺀다.
			h.mcpSessions.Closed(e.Address, e.SessionID)
			continue
		}
		if err := mcphttp.DeleteScopedSession(ctx, e.Address, e.SessionID); err != nil {
			log.Print("[exploration] 재시작 정리: 남은 탐색 브라우저 문맥을 닫지 못함(다음 시작 때 다시 함)")
			continue
		}
		h.mcpSessions.Closed(e.Address, e.SessionID)
		log.Print("[exploration] 재시작 정리: 남은 탐색 브라우저 문맥을 닫음")
	}
	return closed, nil
}

// ensureClean은 시작 정리가 끝났는지다. 끝나지 않았으면 한 번 더 해 본다. 새 탐색 세션을 열기 전에 부른다.
func (h *productExplorationHost) ensureClean(ctx context.Context) error {
	h.cleanMu.Lock()
	defer h.cleanMu.Unlock()
	if h.cleaned {
		return nil
	}
	if _, err := h.cleanupStale(ctx); err != nil {
		return errors.Join(exploration.ErrUnavailable, err)
	}
	h.cleaned = true
	return nil
}

// startCleanup은 준비 서비스가 시작할 때 재시작 정리를 시도한다. 경계가 늦게 뜰 수 있어 몇 번 다시 해 본다. 끝내 못 하면
// 새 탐색을 열 때(ensureClean) 다시 한다.
func (h *productExplorationHost) startCleanup(ctx context.Context) {
	go func() {
		for try := 0; try < explorationCleanupTries && ctx.Err() == nil; try++ {
			attempt, cancel := context.WithTimeout(ctx, explorationCleanupAttempt)
			err := h.ensureClean(attempt)
			cancel()
			if err == nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(explorationCleanupRetry):
			}
		}
		log.Print("[exploration] 재시작 정리를 시작 때 끝내지 못함(새 탐색을 열 때 다시 함)")
	}()
}

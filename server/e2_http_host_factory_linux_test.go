//go:build linux

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

// 실제 factory의 Host 접속·인증 준비·Release 경로를 지난다. 소켓 상대는
// 프로토콜 fixture이며, DB head/MAC 검증은 e2host PostgreSQL 시험이 맡는다.
func TestE2HTTPFactoryAuthFailureClosesHostBeforeNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	var busy bool
	var opens, seals, records int
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				reader := bufio.NewScanner(conn)
				for reader.Scan() {
					var request struct {
						Operation string `json:"operation"`
					}
					if json.Unmarshal(reader.Bytes(), &request) != nil {
						return
					}
					mu.Lock()
					ok := true
					switch request.Operation {
					case "open":
						ok = !busy
						if ok {
							busy = true
							opens++
						}
					case "record":
						ok = busy
						if ok {
							records++
						}
					case "seal":
						ok = busy
						if ok {
							busy = false
							seals++
						}
					default:
						ok = false
					}
					mu.Unlock()
					if json.NewEncoder(conn).Encode(struct {
						OK bool `json:"ok"`
					}{ok}) != nil || !ok || request.Operation == "seal" {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-acceptDone; workers.Wait() })

	var posts atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin, policy.Rules[0].PathPrefix = target.URL, "/read"
	authRule := policy.Rules[0]
	authRule.Ref, authRule.PathScopeRef, authRule.PathPrefix = "auth-login", "auth-login-path", "/login"
	authRule.Methods, authRule.Purpose = []string{"POST"}, e2proxy.PurposeAuthPrep
	policy.Rules = append(policy.Rules, authRule)
	pin := authCredentialPin(t, filepath.Dir(cfg.PolicyFile), "factory-host-account.json", "tester@owned.test", "seeded-pass-88ff")
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	policy.Auth = &auth
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, HostSocket: "/run/e2-host/audit.sock",
		Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}}
	connect := func(ctx context.Context, socket string, scope transmit.Scope, revision string) (*e2host.Client, error) {
		if socket != "/run/e2-host/audit.sock" {
			t.Error("제품 Host 소켓 정책 우회")
		}
		return e2host.OpenClient(ctx, path, scope, revision)
	}
	for i := 0; i < 2; i++ {
		input.OnAcquire = nil
		if i == 0 {
			input.OnAcquire = func(context.Context, E2HTTPAcquired) error { return ErrE2HTTPHost }
		}
		policy.RunRef = "fixture-host-run-" + string(rune('a'+i))
		state, err := os.MkdirTemp("/var/tmp", "e2hf-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(state)
		policy.StateDir = state
		raw, err := json.Marshal(policy)
		if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
			t.Fatal("정책 fixture 쓰기 실패", err)
		}
		options, err := loadE2HTTPRuntimeOptions(t.Context(), input, nil, nil, connect)
		if err != nil {
			t.Fatal(err)
		}
		acquired, err := options.Acquire(t.Context(), E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
		if i == 0 && err == nil || i == 1 && err != nil || acquired.Release == nil ||
			acquired.AccountAuth["tester-a"] != E2HTTPAuthLoginFailed {
			t.Fatal("실제 인증 실패 준비 경로가 실행되지 않음", err)
		}
		if err := acquired.Runner.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := acquired.Runner.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := acquired.Release(t.Context()); err != nil {
			t.Fatal("실패 회차의 Host 마감 실패", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if busy || opens != 2 || seals != 2 || records == 0 || posts.Load() != 2 {
		t.Fatalf("인증 실패 뒤 다음 회차 재개 실패: open=%d seal=%d record=%d post=%d busy=%v", opens, seals, records, posts.Load(), busy)
	}
}

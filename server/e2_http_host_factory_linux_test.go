//go:build linux

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3contract"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

// 실제 factory가 로컬 저널 생성과 인증 준비에 실패한 뒤에도 Host를 마감하는지 본다.
// 소켓 상대는 프로토콜 fixture이며, DB head/MAC 검증은 e2host PostgreSQL 시험이 맡는다.
func TestE2HTTPFactoryPreparationFailuresCloseHostBeforeNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	var busy bool
	var opens, seals, records int
	// sealReply는 다음 봉인 응답에 실을 봉인 확인 결과다(없으면 결과 없는 ACK).
	var sealReply *e2host.SealResult
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
					var seal *e2host.SealResult
					if request.Operation == "seal" {
						seal = sealReply
					}
					mu.Unlock()
					if json.NewEncoder(conn).Encode(struct {
						OK   bool               `json:"ok"`
						Seal *e2host.SealResult `json:"seal,omitempty"`
					}{ok, seal}) != nil || !ok || request.Operation == "seal" {
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
	var sealChecks []E2HTTPSealCheck
	recordHook := func(_ context.Context, s E2HTTPSealCheck) error {
		sealChecks = append(sealChecks, s)
		return errors.New("기록 실패도 봉인 실패가 아니다")
	}
	// 기록 훅이 멈춰도(DB 대기 등, 문맥도 무시) 봉인 뒤 회차 마감은 기록 기한 안에 끝나야 한다(#816 리뷰).
	recordLimit := e2HTTPSealCheckRecordTimeout
	e2HTTPSealCheckRecordTimeout = 200 * time.Millisecond
	t.Cleanup(func() { e2HTTPSealCheckRecordTimeout = recordLimit })
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	stuckHook := func(context.Context, E2HTTPSealCheck) error { <-stuck; return nil }
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, HostSocket: e2host.ProductSocketPath,
		Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}}
	connect := func(ctx context.Context, socket string, scope transmit.Scope, revision string) (*e2host.Client, error) {
		if socket != e2host.ProductSocketPath {
			t.Error("제품 Host 소켓 정책 우회")
		}
		return e2host.OpenClient(ctx, path, scope, revision)
	}
	var screenCopies []bool
	for _, tc := range []struct {
		name                                        string
		existingJournal, rejectAcquire, stuckRecord bool
	}{
		{name: "journal-exists", existingJournal: true},
		{name: "reject-after-auth", rejectAcquire: true},
		{name: "next-run"},
		{name: "stuck-record", stuckRecord: true},
	} {
		// Host 모드 로컬 저널은 화면 사본이라 확정 단계 투영을 받는다(받지 못하면 실제 회차에서 확정 사건 기록이 감사 실패).
		input.OnAcquire = func(_ context.Context, a E2HTTPAcquired) error {
			screenCopies = append(screenCopies, a.Journal.ScreenCopy())
			return nil
		}
		input.SealChecked = recordHook
		if tc.stuckRecord {
			input.SealChecked = stuckHook
		}
		if tc.rejectAcquire {
			input.OnAcquire = func(context.Context, E2HTTPAcquired) error { return ErrE2HTTPHost }
		}
		policy.RunRef = "fixture-host-" + tc.name
		mu.Lock()
		sealReply = nil
		if tc.name == "next-run" || tc.stuckRecord {
			// 봉인 응답에 Host 봉인 확인 결과가 실리면 그 회차·작업으로 기록 훅에 넘긴다(E1 계약 §6).
			sealReply = &e2host.SealResult{Phase: "sealed", Head: e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: "journal-fixture",
				Sequence: 7, MAC: strings.Repeat("e", 64), Scope: transmit.Scope{RunRef: policy.RunRef, TaskRef: "17"}},
				Checks: []e2host.SealCheck{{Sequence: 5, MAC: strings.Repeat("c", 64), Included: true}, {Sequence: 6, MAC: strings.Repeat("d", 64), Included: false}}}
		}
		mu.Unlock()
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
		if tc.existingJournal {
			if err := os.WriteFile(filepath.Join(state, "events.jsonl"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		acquired, err := options.Acquire(t.Context(), E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
		if tc.existingJournal {
			if !errors.Is(err, ErrE2HTTPHost) || acquired.Release != nil || acquired.Execution != nil || posts.Load() != 0 {
				t.Fatal("로컬 저널 생성 실패 전에 실행이 시작됨", err)
			}
			continue
		}
		if (err != nil) != tc.rejectAcquire || acquired.Release == nil ||
			acquired.AccountAuth["tester-a"] != E2HTTPAuthLoginFailed {
			t.Fatal("실제 인증 실패 준비 경로가 실행되지 않음", err)
		}
		if err := acquired.Runner.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := acquired.Runner.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if err := acquired.Release(t.Context()); err != nil {
			t.Fatal("실패 회차의 Host 마감 실패", err)
		}
		if tc.stuckRecord && time.Since(started) > 5*time.Second {
			t.Fatalf("멈춘 봉인 확인 기록이 마감을 붙잡음: %s", time.Since(started))
		}
	}
	if len(screenCopies) != 2 || !screenCopies[0] || !screenCopies[1] {
		t.Fatalf("Host 모드 로컬 저널이 화면 사본이 아님: %v", screenCopies)
	}
	mu.Lock()
	defer mu.Unlock()
	if busy || opens != 4 || seals != 4 || records == 0 || posts.Load() != 3 {
		t.Fatalf("준비 실패 뒤 다음 회차 재개 실패: open=%d seal=%d record=%d post=%d busy=%v", opens, seals, records, posts.Load(), busy)
	}
	// 결과 없는 봉인 ACK(앞 두 회차)는 기록하지 않고, 결과가 있는 회차만 한 번 넘긴다. 기록 실패도 마감 실패가 아니다.
	if len(sealChecks) != 1 || sealChecks[0].TaskID != 17 || sealChecks[0].RunRef != "fixture-host-next-run" ||
		sealChecks[0].Result.Head.Sequence != 7 || len(sealChecks[0].Result.Checks) != 2 || !sealChecks[0].Result.Checks[0].Included {
		t.Fatalf("봉인 확인 기록 훅: %+v", sealChecks)
	}
	rec := e2HTTPSealCheckRecord(sealChecks[0])
	if rec.TaskID != 17 || rec.RunRef != "fixture-host-next-run" || rec.Head != (db.ProductCheckpoint{JournalID: "journal-fixture", Sequence: 7, MAC: strings.Repeat("e", 64)}) ||
		len(rec.Checks) != 2 || rec.Checks[0] != (db.SealCheck{Sequence: 5, MAC: strings.Repeat("c", 64), Included: true}) ||
		rec.Checks[1] != (db.SealCheck{Sequence: 6, MAC: strings.Repeat("d", 64), Included: false}) {
		t.Fatalf("봉인 확인 저장 기록: %+v", rec)
	}
}

// Host 클라이언트를 감싼 E③ 검증자는 Host가 검증에 쓴 head 체크포인트를 그대로 넘긴다(E1 계약 §2).
func TestE2HostE3VerifierPassesCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	scope := transmit.Scope{TaskRef: "17", RunRef: "run-verifier", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}
	checkpoint := e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: "journal-verifier", Sequence: 9, MAC: strings.Repeat("c", 64), Scope: scope}
	event := e2proxy.Event{Index: 9, Stage: "result", Receipt: e2proxy.Receipt{Version: e2proxy.JournalVersionV3, Scope: scope, CallRef: "call-1", AttemptID: strings.Repeat("a", 32), Sequence: 1}}
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewScanner(conn)
		for reader.Scan() {
			var request struct {
				Operation string `json:"operation"`
			}
			if json.Unmarshal(reader.Bytes(), &request) != nil {
				return
			}
			reply := map[string]any{"ok": true}
			if request.Operation == "verified" {
				reply["event"], reply["checkpoint"] = event, checkpoint
			}
			if json.NewEncoder(conn).Encode(reply) != nil {
				return
			}
		}
	}()
	client, err := e2host.OpenClient(t.Context(), path, scope, "revision")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Abort()
	var verifier e2proxy.E3Verifier = e2HostE3Verifier{host: client}
	withCheckpoint, ok := verifier.(e2proxy.E3CheckpointVerifier)
	if !ok {
		t.Fatal("Host 검증자가 체크포인트를 주지 않음")
	}
	ev, got, err := withCheckpoint.VerifiedCheckpoint(t.Context(), e3contract.RunBinding{RunRef: scope.RunRef}, e3contract.CallRef{CallRef: "call-1", AttemptID: strings.Repeat("a", 32), Sequence: 1})
	if err != nil || got != checkpoint || ev.CallRef != "call-1" {
		t.Fatalf("체크포인트: %+v %v", got, err)
	}
	if ev, err := verifier.Verified(t.Context(), e3contract.RunBinding{RunRef: scope.RunRef}, e3contract.CallRef{CallRef: "call-1", AttemptID: strings.Repeat("a", 32), Sequence: 1}); err != nil || ev.Index != 9 {
		t.Fatalf("검증: %v", err)
	}
}

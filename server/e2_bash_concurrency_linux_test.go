//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
)

// 외부 DTO가 아닌 실제 Manager Worker 설정은 admission에 한 번 결속한다.
func TestE2HTTPRuntimeBindsWorkersWithoutExternalInput(t *testing.T) {
	for _, workers := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			r, task := e2HTTPRuntimeFixture(t, nil)
			if err := r.admitTaskWithWorkers(t.Context(), task, workers); err != nil {
				t.Fatal(err)
			}
			if r.task.workerCount != workers || r.admitTaskWithWorkers(t.Context(), task, workers) != nil {
				t.Fatal("동일 회차 Worker 결속 실패")
			}
			if r.admitTaskWithWorkers(t.Context(), task, workers+1) == nil {
				t.Fatal("결속 후 Worker 수가 암묵적으로 변경됨")
			}
		})
	}
	r, task := e2HTTPRuntimeFixture(t, nil)
	if r.admitTaskWithWorkers(t.Context(), task, 0) == nil || r.admitTaskWithWorkers(t.Context(), task, -1) == nil {
		t.Fatal("잘못된 Worker 수가 확대 근거로 사용됨")
	}
	if r.AdmitTask(t.Context(), task) != nil || r.task.workerCount != 1 {
		t.Fatal("미지정 구형 호출이 안전한 1개를 유지하지 않음")
	}
	var request E2HTTPRequest
	if json.Unmarshal([]byte(`{"WorkerCount":99,"worker_count":99}`), &request) != nil || request.WorkerCount != 0 {
		t.Fatal("JSON으로 내부 Worker 결속 변경")
	}
	raw, err := json.Marshal(E2HTTPRequest{WorkerCount: 99})
	if err != nil || strings.Contains(string(raw), "99") {
		t.Fatal("Worker 설정이 외부 요청 칸으로 노출됨")
	}
}

func TestE2HTTPBashWorkersFollowServerAdmissionOwnedPostgres(t *testing.T) {
	manager, task, _ := firstDiagnosisActualTask(t)
	before := manager.Workers()
	defer manager.SetWorkers(before)
	defer restoreConcurrencySetting(t, manager)()
	if manager.SetWorkers(3) != nil || manager.SetConcurrency(true, 1) != nil {
		t.Fatal("실제 admission 설정 실패")
	}
	runtime, _ := e2HTTPRuntimeFixture(t, nil)
	s := newAdmissionTestServer(manager, func(*Task) bool { return false })
	s.ctx = t.Context()
	s.engine.e2HTTP = runtime
	defer s.engine.StopTask(task.ID)
	if queued, err := s.admitTask(task, "resume"); err != nil || !queued {
		t.Fatal("실제 서버 admission 경로 미도달", err)
	}
	if runtime.task == nil || runtime.task.workerCount != 3 {
		t.Fatal("서버 admission이 실제 Manager Worker 수를 결속하지 않음")
	}
}

func TestE2HTTPBashWorkersFollowProductConstructorOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	manager, err := NewFirstDiagnosisManager(f.Context, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	installer := &db.DB{DB: f.Installer}
	before := manager.Workers()
	defer installer.SetSetting("workers", fmt.Sprint(before))
	if installer.SetSetting("workers", "3") != nil || manager.Workers() != 3 {
		t.Fatal("실제 제품 Worker 설정 실패")
	}
	task, err := manager.CreateTask("Owned three-worker admission", "Bind the actual product Manager worker count", nil, 60, 60)
	if err != nil {
		t.Fatal(err)
	}
	firstDiagnosisSupervisorConstructorClose(t, manager, installer, task, func(runtime *FirstDiagnosisHTTPRuntime) {
		if runtime.host.runtime.task == nil || runtime.host.runtime.task.workerCount != 3 {
			t.Fatal("제품 조기 admission이 Manager Worker 수를 결속하지 않음")
		}
	})
}

// 설치 정책을 적재하는 실제 factory부터 helper·대상 수신·감사 마감까지 지난다.
func TestE2HTTPFactoryDerivesBashSlotsFromWorkersAndPolicy(t *testing.T) {
	helper := firstDiagnosisNativeHelper(t)
	for _, tc := range []struct{ max, workers, want int }{{16, 3, 3}, {2, 3, 2}, {1, 3, 1}, {4, 2, 2}, {16, 0, 1}} {
		t.Run(fmt.Sprintf("%d/%d", tc.max, tc.workers), func(t *testing.T) {
			entered := make(chan struct{}, 4)
			release := make(chan struct{})
			var once sync.Once
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-release:
					io.WriteString(w, "owned")
				case <-r.Context().Done():
				}
			}))
			defer target.Close()
			defer once.Do(func() { close(release) })
			cfg, _, policy := e2HostPolicyFixture(t)
			hash, err := e2HTTPExecutableDigest(helper)
			if err != nil {
				t.Fatal(err)
			}
			policy.Helper, policy.HelperSHA256 = helper, hex.EncodeToString(hash[:])
			policy.Rules[0].Origin = target.URL
			policy.MaxRequests = 8
			policy.Limits.MaxAttempts = 8
			policy.Limits.MaxCalls = 8
			policy.Limits.MaxConcurrent = tc.max
			policy.Proxy.MaxConnections = tc.max
			policy.Limits.RequestTimeoutMS = 8000
			policy.Limits.CallTimeoutMS = 10000
			policy.Proxy.RequestTimeoutMS = 8000
			policy.Runtime.WorkerTimeoutMS = 12000
			raw, err := json.Marshal(policy)
			if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
				t.Fatal("보호 정책 준비 실패")
			}
			options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := options.Acquire(t.Context(), E2HTTPRequest{WorkerCount: tc.workers, TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if lease.Runner.Close(ctx) != nil || lease.Execution.Stop(ctx) != nil || lease.Release(ctx) != nil {
					t.Error("실제 factory 자원 마감 실패")
				}
			}()
			// HTTP 정책 fingerprint와 연결 상한은 Bash 슬롯으로 덮어쓰지 않는다.
			if lease.Execution.MaxConcurrent() != tc.max {
				t.Fatal("Bash 슬롯이 HTTP 상한을 변경함")
			}
			actual := lease.Runner.(localE2HTTPRun).runner
			limits := actual.ProcessLimits()
			wantTasks := min(uint64(128), uint64(192/tc.want))
			if limits.BashSlots != tc.want || limits.CallTasks != wantTasks || limits.EngineReserveTasks != 64 || limits.ContainerTasks != 256 {
				t.Fatal("회차 task 배분이 계산된 슬롯에 결속되지 않음")
			}
			conditions, err := os.ReadFile(filepath.Join(policy.StateDir, "runtime-conditions.json"))
			var recorded struct {
				ProcessLimits e2runner.ProcessLimits `json:"process_limits"`
				Queue         bool                   `json:"queue_consumes_command_timeout"`
			}
			if err != nil || json.Unmarshal(conditions, &recorded) != nil || recorded.ProcessLimits != limits || !recorded.Queue {
				t.Fatal("실제 회차 조건 기록 누락")
			}
			finished := make(chan e2runner.Result, tc.want)
			for i := range tc.want {
				if i > 0 && lease.Execution.RegisterIntent(t.Context(), int64(i+1)) != nil {
					t.Fatal("intent 등록 실패")
				}
				go func(id int64) {
					out, _ := lease.Runner.RunForIntent(t.Context(), id, "printf 'tasks=%s/%s;' \"$(ulimit -Su)\" \"$(ulimit -Hu)\"; curl -fsS --max-time 7 '"+target.URL+"/'")
					finished <- out
				}(int64(i + 1))
			}
			for range tc.want {
				select {
				case <-entered:
				case <-time.After(7 * time.Second):
					t.Fatal("계산된 슬롯만큼 실제 Bash가 중첩되지 않음")
				}
			}
			if lease.Execution.RegisterIntent(t.Context(), 9) != nil {
				t.Fatal("추가 intent 등록 실패")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			_, err = actual.RunForIntent(ctx, 9, "printf overflow")
			cancel()
			if err == nil || len(lease.Execution.Calls()) != tc.want {
				t.Fatal("factory 슬롯 초과 실행")
			}
			once.Do(func() { close(release) })
			for range tc.want {
				select {
				case out := <-finished:
					if out.Trusted.ProcessLimits != limits || out.ToolOutput != fmt.Sprintf("tasks=%d/%d;owned", wantTasks, wantTasks) {
						t.Fatal("호출별 soft/hard RLIMIT_NPROC 또는 신뢰 조건이 실제 helper와 불일치", out.ToolOutput)
					}
					if !out.Trusted.Final || !out.Trusted.Call.AuditComplete || out.Trusted.Call.Counts.Responses != 1 {
						t.Fatal("실제 송신·감사 마감 실패")
					}
				case <-time.After(7 * time.Second):
					t.Fatal("실제 Bash 마감 지연")
				}
			}
			// 승인에 사용한 정책 바이트도 그대로다.
			after, err := os.ReadFile(cfg.PolicyFile)
			if err != nil || sha256.Sum256(after) != sha256.Sum256(raw) {
				t.Fatal("슬롯 계산이 승인 정책 파일을 변경함")
			}
		})
	}
}

func TestE2HTTPBashWorkersFollowActualEngineManagerOwnedPostgres(t *testing.T) {
	manager, task, intent := firstDiagnosisActualTask(t)
	before := manager.Workers()
	defer func() {
		if manager.SetWorkers(before) != nil {
			t.Error("Worker 설정 복원 실패")
		}
	}()
	if manager.SetWorkers(3) != nil {
		t.Fatal("실제 Worker 설정 실패")
	}
	captured := make(chan int, 1)
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.Acquire = func(_ context.Context, r E2HTTPRequest) (E2HTTPRunOptions, error) {
			captured <- r.WorkerCount
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
	})
	engine := NewEngine(manager)
	engine.e2HTTP = runtime
	engine.Run(t.Context(), task)
	defer engine.StopTask(task.ID)
	if runtime.task == nil || runtime.task.workerCount != 3 {
		t.Fatal("Engine의 실제 Worker 수 미결속")
	}
	// 시작 후 설정 변경은 이미 실행 중인 회차의 Bash 수를 새로 늘리거나 줄이지 않는다.
	if manager.SetWorkers(2) != nil {
		t.Fatal("후속 Worker 설정 변경 실패")
	}
	if _, _, err := runtime.PrepareWork(t.Context(), task, intent); err == nil {
		t.Fatal("의도한 획득 실패가 성공으로 바뀜")
	}
	select {
	case n := <-captured:
		if n != 3 {
			t.Fatal("실제 Engine 회차 Worker 수 대신 변경된 설정 사용")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("실제 intent의 factory 요청 미도달")
	}
}

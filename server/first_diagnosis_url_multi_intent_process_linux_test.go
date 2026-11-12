//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

// 모델 HTTP 응답만 지연한다. 실제 Planner·Engine Worker·공유 Budget은 교체하지 않는다.
type urlMultiIntentModel struct {
	entered chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (m *urlMultiIntentModel) release() { m.once.Do(func() { close(m.unblock) }) }

func (m *urlMultiIntentModel) respond(f *urlProcessFixture, ctx context.Context, call int64, messages []urlSyntheticMessage, tool func(string, string, any)) bool {
	if call == 1 {
		close(m.entered)
		select {
		case <-m.unblock:
		case <-ctx.Done():
			return false
		}
	}
	ref, submitted := "", false
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		if message.ToolCallID == "multi-candidate" {
			submitted = true
		}
		var content string
		if json.Unmarshal(message.Content, &content) != nil {
			continue
		}
		var result e2runner.Result
		if json.Unmarshal([]byte(content), &result) == nil && result.Trusted.Version == e2proxy.Version && result.Trusted.Call.CallRef != "" {
			ref = result.Trusted.Call.CallRef
		}
	}
	if submitted {
		return true
	}
	if ref != "" {
		tool("submit_diagnostic_candidate", "multi-candidate", map[string]any{"call_ref": ref, "vulnclass": "access_control", "name": "동시 의도 소유 관찰 후보", "summary": "각 실제 의도의 허용 HTTPS 관찰을 별도 수동 검토 후보로 저장합니다.", "remediation": "실제 접근 권한을 검토하세요."})
		return true
	}
	if call > 1 {
		// 모델 예산 중첩과 E② 실행 동시성 한도를 섞지 않는다. 두 번째 Bash는 첫 실제 Bash 마감 뒤 제안한다.
		for {
			log, err := readURLMultiLog(f.dataDir)
			if err != nil {
				f.t.Error("동시 모델 fixture의 Bash 마감 집계 실패")
				return false
			}
			if log.bashFinished >= 1 {
				break
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	tool("Bash", "multi-allowed", map[string]any{"command": "curl --silent --show-error --max-time 2 " + f.target + "/allowed", "timeout_ms": 2500})
	return true
}

type urlMultiLog struct {
	workersStarted  map[int64]bool
	workersFinished int
	workerFailures  int
	failures        map[string]int
	bashFinished    int
	modelsStarted   int
	modelsFinished  int
	modelFailures   int
	workerModels    int
	candidates      int
	summaries       int
	summary         diagnosticlog.Event
}

// 진행 중 파일은 완전히 기록된 JSONL 행만 집계하며 원문을 출력하지 않는다.
func readURLMultiLog(dataDir string) (urlMultiLog, error) {
	out := urlMultiLog{workersStarted: map[int64]bool{}, failures: map[string]int{}}
	files, err := filepath.Glob(filepath.Join(dataDir, "diagnostic-logs", "*", "*.jsonl"))
	if err != nil {
		return out, err
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > diagnosticlog.MaxBytes {
			return out, errors.New("invalid bounded diagnostic file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return out, err
		}
		lines := bytes.Split(raw, []byte("\n"))
		for _, line := range lines[:len(lines)-1] {
			var event diagnosticlog.Event
			if json.Unmarshal(line, &event) != nil || event.Version != 1 {
				return out, errors.New("invalid bounded diagnostic event")
			}
			if event.Status == "failed" || event.Status == "incomplete" || event.Status == "timeout" {
				out.failures[event.Component+"/"+event.Event+"/"+event.ReasonCode]++
			}
			if event.Component == "openrouter" {
				if event.Event == "model_request_started" {
					event.Event = "model_call_started"
				}
				if event.Event == "model_request_finished" {
					event.Event = "model_call_finished"
				}
			}
			switch event.Event {
			case "worker_started":
				out.workersStarted[event.IntentID] = true
			case "worker_finished":
				out.workersFinished++
				if event.Status != "succeeded" {
					out.workerFailures++
				}
			case "tool_finished":
				if event.Tool == "Bash" {
					out.bashFinished++
				}
			case "model_call_started":
				out.modelsStarted++
				if event.Role == "worker" {
					out.workerModels++
				}
			case "model_call_finished":
				out.modelsFinished++
				if event.Status != "succeeded" {
					out.modelFailures++
				}
			case "candidate_saved":
				out.candidates++
			case "run_summary":
				if event.Component == "supervisor" {
					out.summaries++
					out.summary = event
				}
			}
		}
	}
	return out, nil
}

func TestFirstDiagnosisURLConcurrentWorkersOwnedPostgres(t *testing.T) {
	runURLConcurrentWorkers(t, false)
}
func TestFirstDiagnosisURLOpenRouterConcurrentWorkersOwnedPostgres(t *testing.T) {
	runURLConcurrentWorkers(t, true)
}

// urlReservedInputWithin은 입력 예약이 요청 크기 상한(직렬화 바이트 + framing 여유)이고 호출당 context를
// 넘지 않는지 본다(P3 결정안 C28 결정 1). 예약은 호출마다 framing 여유보다 크다.
func urlReservedInputWithin(reserved, calls, perCall int64) bool {
	return reserved > calls*firstDiagnosisFramingTokens && reserved <= calls*perCall
}

func runURLConcurrentWorkers(t *testing.T, external bool) {
	if os.Getenv("ARTEX_URL_DIAGNOSIS_TEST") != "1" {
		t.Skip("NOT_RUN: 소유 로컬 URL 프로세스 동시 Worker 수용 시험 opt-in 필요")
	}
	multi := &urlMultiIntentModel{entered: make(chan struct{}), unblock: make(chan struct{})}
	f := newURLProcessFixtureMode(t, multi, external)
	inputPerCall := int64(131072)
	expectedCost := int64(0)
	if external {
		inputPerCall = 262144
	}
	service := f.startPreparation()
	api := f.startAPI()
	client := f.api()
	job := client.job("POST", diagnosisapi.URLBasePath+"/drafts", "multi-create", map[string]any{"version": diagnosisapi.URLVersion, "input": diagnosisprep.Input{TargetURL: f.target + "/allowed", ProfileID: "owned-url", DisplayName: "실제 동시 Worker 예산 수용"}})
	job = client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/validate", "", map[string]any{"version": diagnosisapi.URLVersion, "expected_revision": job.Revision})
	if job.State != diagnosisprep.Ready || f.modelCalls.Load() != 0 || f.allowed.Load() != 0 || f.denied.Load() != 0 {
		t.Fatal("동시 Worker 초안 검증 부작용 또는 미준비")
	}
	job = client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/start", "multi-start", urlStartBody(job.Revision, external))
	f.releaseOnce.Do(func() { close(f.releasePlanner) })
	select {
	case <-multi.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("실제 첫 Worker 모델 HTTP 미도달")
	}
	var queued diagnosisprep.Job
	for deadline := time.Now().Add(5 * time.Second); ; {
		log, err := readURLMultiLog(f.dataDir)
		if err != nil {
			t.Fatal("동시 Worker 관측 집계 실패")
		}
		var running int
		if f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND origin='planner' AND state='running'").Scan(&running) != nil {
			t.Fatal("실제 실행 중 의도 집계 실패")
		}
		if running == 2 && len(log.workersStarted) == 2 {
			break
		}
		if log.workerFailures > 0 || time.Now().After(deadline) {
			t.Fatalf("실제 Worker 중첩 미확인 running=%d started=%d finished=%d failed=%d", running, len(log.workersStarted), log.workersFinished, log.workerFailures)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 두 번째 실제 Worker가 시작된 뒤에도 첫 모델 응답 전 신규 예약/송신이 없어야 한다.
	for i := 0; i < 3; i++ {
		queued = client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/status", "", nil)
		log, err := readURLMultiLog(f.dataDir)
		if err != nil {
			t.Fatal("대기 중 모델 관측 집계 실패")
		}
		var running int
		if f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND origin='planner' AND state='running'").Scan(&running) != nil {
			t.Fatal("대기 중 의도 집계 실패")
		}
		u := queued.ModelUsage
		if running != 2 || len(log.workersStarted) != 2 || log.workersFinished != 0 || log.workerModels != 1 || f.workerCalls.Load() != 1 || f.modelCalls.Load() != 5 || u == nil || u.Requests != 5 || u.UsageKnown || !urlReservedInputWithin(u.ReservedInputTokens, 5, inputPerCall) || u.ReservedOutputTokens != 5*2048 || f.allowed.Load() != 0 || f.denied.Load() != 0 {
			t.Fatalf("공유 예산 대기 불일치 running=%d started=%d finished=%d worker_http=%d total_http=%d", running, len(log.workersStarted), log.workersFinished, f.workerCalls.Load(), f.modelCalls.Load())
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("URL_CONCURRENT_WAIT planner_intents_running=2 workers_started=2 workers_finished=0 worker_http=1 model_http=5 reserved_requests=5 waiting_additional_reservations=0 target_sends=0")
	multi.release()
	var final diagnosisprep.Job
	for deadline := time.Now().Add(45 * time.Second); ; {
		final = client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/status", "", nil)
		if final.State == diagnosisprep.Completed || final.State == diagnosisprep.Failed || final.State == diagnosisprep.Unknown || final.State == diagnosisprep.Cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("동시 Worker 실행 마감 시간 초과")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final.State != diagnosisprep.Completed || !final.Started || !final.DrainComplete || !final.ResultReady || final.AuthorityState != diagnosisprep.AuthorityConsumed {
		api.stop()
		service.stop()
		log, logErr := readURLMultiLog(f.dataDir)
		var done, blocked, candidates int
		_ = f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND state='done'").Scan(&done)
		_ = f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND state='blocked'").Scan(&blocked)
		_ = f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE origin='e2_diagnostic_candidate_v1'").Scan(&candidates)
		t.Fatalf("동시 Worker 마감 불일치 state=%s reason=%s worker_http=%d model_http=%d allowed=%d denied=%d done=%d blocked=%d candidates=%d worker_failed=%d model_failed=%d summary_status=%s summary_reason=%s summary_target=%d summary_denied=%d log_error=%v failures=%v", final.State, final.ReasonCode, f.workerCalls.Load(), f.modelCalls.Load(), f.allowed.Load(), f.denied.Load(), done, blocked, candidates, log.workerFailures, log.modelFailures, log.summary.Status, log.summary.ReasonCode, log.summary.TargetRequests, log.summary.Denied, logErr != nil, log.failures)
	}
	var tasks, intents, done, candidates, findings, runs int
	for _, q := range []struct {
		query string
		out   *int
	}{
		{"SELECT count(*) FROM tasks", &tasks},
		{"SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND origin='planner'", &intents},
		{"SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND origin='planner' AND state='done'", &done},
		{"SELECT count(*) FROM exploration_nodes WHERE origin='e2_diagnostic_candidate_v1'", &candidates},
		{"SELECT count(*) FROM findings", &findings},
		{"SELECT count(*) FROM exploration_nodes WHERE origin='" + diagnosisRunOrigin + "'", &runs},
	} {
		if f.installer.QueryRow(q.query).Scan(q.out) != nil {
			t.Fatal("다중 의도 저장 집계 실패")
		}
	}
	if tasks != 1 || intents != 2 || done != 2 || candidates != 2 || findings != 0 || runs != 1 || f.allowed.Load() != 2 || f.denied.Load() != 0 || f.workerCalls.Load() != 6 || f.goalCalls.Load() != 2 {
		t.Fatalf("다중 의도 집계 tasks=%d intents=%d done=%d candidates=%d findings=%d runs=%d allowed=%d denied=%d worker_http=%d", tasks, intents, done, candidates, findings, runs, f.allowed.Load(), f.denied.Load(), f.workerCalls.Load())
	}
	u := final.ModelUsage
	calls := f.modelCalls.Load()
	if external {
		expectedCost = calls
	}
	if calls != f.goalCalls.Load()+f.plannerCalls.Load()+f.workerCalls.Load() || calls < 11 || calls > 12 || final.Scope == nil || u == nil || !u.UsageKnown || u.Requests != calls || u.InputTokens != calls*10 || u.OutputTokens != calls*10 || !urlReservedInputWithin(u.ReservedInputTokens, calls, inputPerCall) || u.ReservedOutputTokens != calls*2048 || u.CostUSDMicros != expectedCost || u.ReservedInputTokens > final.Scope.Budget.InputTokens || u.ReservedOutputTokens > final.Scope.Budget.OutputTokens || calls > final.Scope.Budget.ModelRequests {
		t.Fatal("실제 다중 Worker 모델 요청·예약·보고 토큰 불일치")
	}
	var report []byte
	client.call("GET", "/api/report/diagnostic?task="+final.TaskID, "", nil, &report)
	api.stop()
	service.stop()
	log, err := readURLMultiLog(f.dataDir)
	if err != nil || len(log.workersStarted) != 2 || log.workersFinished != 2 || log.workerFailures != 0 || log.modelsStarted != int(calls) || log.modelsFinished != int(calls) || log.modelFailures != 0 || log.workerModels != 6 || log.candidates != 2 || log.summaries != 1 || log.summary.Status != "succeeded" || log.summary.TargetRequests != 2 || log.summary.Approved != 2 || log.summary.Denied != 0 || log.summary.Candidates != 2 {
		t.Fatal("실제 두 Worker 성공·모델 예약·E②·저장 관측 불일치")
	}
	t.Logf("URL_CONCURRENT_ACCEPTANCE tasks=1 planner_intents=2 done_intents=2 workers_succeeded=2 candidates=2 confirmed=0 allowed_sends=2 denied_sends=0 runs=1 model_calls=%d goal_calls=%d planner_calls=%d worker_calls=6 reported_input=%d reported_output=%d reserved_input=%d reserved_output=%d cost_usd_micros=%d drain=true result=true", calls, f.goalCalls.Load(), f.plannerCalls.Load(), u.InputTokens, u.OutputTokens, u.ReservedInputTokens, u.ReservedOutputTokens, u.CostUSDMicros)
}

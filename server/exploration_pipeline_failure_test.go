package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
)

const pipelineFailurePrivateMarker = "https://private.invalid/owned?api_key=credential-marker-P1"

// 표준 logger를 바꾸므로 이 시험과 하위 시험은 병렬 실행하지 않는다.
func capturePipelineFailureLog(run func()) string {
	var output bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags); log.SetPrefix(previousPrefix) }()
	run()
	return output.String()
}
func assertPipelineFixedFailure(t *testing.T, text string, want []string, result ExplorationPipelineResult, err error) {
	t.Helper()
	stages := []string{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[exploration] 탐색 단계 실패: ") {
			stages = append(stages, strings.TrimPrefix(line, "[exploration] 탐색 단계 실패: "))
		}
	}
	if !reflect.DeepEqual(stages, want) {
		t.Fatal("pipeline failure did not preserve exact fixed stage/classification")
	}
	encoded, _ := json.Marshal(result)
	visible := text + string(encoded)
	if err != nil {
		visible += err.Error()
	}
	for _, marker := range []string{pipelineFailurePrivateMarker, "private.invalid", "credential-marker-P1", "api_key"} {
		if strings.Contains(visible, marker) {
			t.Fatal("pipeline failure exposed private error data")
		}
	}
}

type pipelineFailingOpenBoundary struct{ calls atomic.Int32 }

func (b *pipelineFailingOpenBoundary) Open(context.Context, exploration.Binding) (exploration.Lease, error) {
	b.calls.Add(1)
	return nil, errors.New(pipelineFailurePrivateMarker)
}

func TestExplorationPipelineFailureLogsFixedStageAndNotRunWithoutPrivateData(t *testing.T) {
	for _, kind := range []string{"factory", "factory_deadline", "factory_binding", "missing_capability", "resolver", "boundary_open", "browser_start"} {
		t.Run(kind, func(t *testing.T) {
			server, task, o, b, browser, mcp, provider := pipelineFixture(t)
			originalFactory := o.OpenHost
			var factoryCalls, browserStarts atomic.Int32
			failedBoundary := &pipelineFailingOpenBoundary{}
			wantReason := "runtime_unavailable"
			wantStages := []string{"단계=host_factory 분류=operation_failed"}
			switch kind {
			case "factory", "factory_deadline", "factory_binding":
				failure := error(errors.New(pipelineFailurePrivateMarker))
				if kind == "factory_deadline" {
					failure = fmt.Errorf("%s: %w", pipelineFailurePrivateMarker, context.DeadlineExceeded)
					wantStages = []string{"단계=host_factory 분류=deadline"}
				}
				if kind == "factory_binding" {
					failure = fmt.Errorf("%s: %w", pipelineFailurePrivateMarker, exploration.ErrBinding)
					wantStages = []string{"단계=host_factory 분류=binding_invalid"}
				}
				o.OpenHost = func(context.Context, exploration.Binding, *agent.Worker) (ExplorationPipelineHost, error) {
					factoryCalls.Add(1)
					return ExplorationPipelineHost{}, failure
				}
			case "missing_capability":
				wantStages = []string{"단계=host_capabilities 분류=operation_failed"}
				o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
					factoryCalls.Add(1)
					host, err := originalFactory(ctx, binding, w)
					host.Reader = nil
					host.Session.StartBrowser = func(context.Context, exploration.Binding) (ExplorationBrowserLease, error) {
						browserStarts.Add(1)
						return nil, errors.New(pipelineFailurePrivateMarker)
					}
					return host, err
				}
			case "resolver":
				wantReason = "model_unavailable"
				wantStages = []string{"단계=worker_resolve 분류=operation_failed"}
				unready := agent.NewWorker(nil, pipelineFailurePrivateMarker, t.TempDir(), nil, 0, 10)
				server.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return &agent.Planner{}, unready })
				o.OpenHost = func(context.Context, exploration.Binding, *agent.Worker) (ExplorationPipelineHost, error) {
					factoryCalls.Add(1)
					return ExplorationPipelineHost{}, errors.New(pipelineFailurePrivateMarker)
				}
			case "boundary_open":
				wantReason = "browser_unavailable"
				wantStages = []string{"단계=boundary_open 분류=operation_failed", "단계=session_open 분류=operation_failed"}
				o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
					factoryCalls.Add(1)
					host, err := originalFactory(ctx, binding, w)
					host.Session.Boundary = failedBoundary
					return host, err
				}
			case "browser_start":
				wantReason = "browser_unavailable"
				wantStages = []string{"단계=browser_start 분류=operation_failed", "단계=session_open 분류=operation_failed"}
				o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
					factoryCalls.Add(1)
					host, err := originalFactory(ctx, binding, w)
					host.Session.StartBrowser = func(context.Context, exploration.Binding) (ExplorationBrowserLease, error) {
						browserStarts.Add(1)
						return nil, errors.New(pipelineFailurePrivateMarker)
					}
					return host, err
				}
			}
			var result ExplorationPipelineResult
			var runErr error
			text := capturePipelineFailureLog(func() { result, runErr = server.RunExplorationPipeline(t.Context(), task, o) })
			assertPipelineFixedFailure(t, text, wantStages, result, runErr)
			if runErr != nil || result.Status != "NOT_RUN" || result.Reason != wantReason {
				t.Fatal("pipeline failure became success or wrong NOT_RUN reason")
			}
			snapshot, err := o.Workflow.Snapshot()
			if err != nil || snapshot.ExplorationStatus != "NOT_RUN" || snapshot.ExplorationReason != wantReason || snapshot.InitialCompleted || snapshot.Stage != "crawling" || snapshot.DeepRuns != 0 || snapshot.Counts.ExplorationRequests != 0 || snapshot.Counts.Sent != 0 || snapshot.Counts.Attempted != 0 {
				t.Fatal("preflight failure fabricated execution or lost persisted NOT_RUN")
			}
			if provider.calls.Load() != 0 || mcp.requests.Load() != 0 || browser.closes.Load() != 0 {
				t.Fatal("preflight failure invoked model/MCP/browser resources")
			}
			if kind == "resolver" {
				if factoryCalls.Load() != 0 {
					t.Fatal("failed resolver reached host factory")
				}
			} else if factoryCalls.Load() != 1 {
				t.Fatal("host factory call count incorrect")
			}
			if kind == "browser_start" {
				if b.opens.Load() != 1 || b.lease.closed() != 1 || browserStarts.Load() != 1 {
					t.Fatal("browser startup failure did not close owned boundary exactly once")
				}
			} else if b.opens.Load() != 0 || b.lease.closed() != 0 || browserStarts.Load() != 0 {
				t.Fatal("earlier preflight failure acquired boundary/browser")
			}
			if kind == "boundary_open" && failedBoundary.calls.Load() != 1 {
				t.Fatal("boundary failure fixture did not execute actual Open")
			}
		})
	}
}

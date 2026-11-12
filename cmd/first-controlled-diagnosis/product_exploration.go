package main

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/server"
)

// 실제 강제 경계·private reader·격리 컨테이너가 준비된 호스트만 Go 조립에서 넣는다.
// 설치 JSON·URL·모델·환경 변수는 이 capability를 만들지 않는다. 현재 production 기본값은 nil이다.
type urlRuntimeHostCapabilities struct {
	Exploration       server.ExplorationPipelineFactory
	ExplorationLimits *exploration.Limits
}
type urlExplorationStageKey struct{}

func withURLExplorationStage(ctx context.Context, stage server.ExplorationStageFactory) context.Context {
	return context.WithValue(ctx, urlExplorationStageKey{}, stage)
}
func urlExplorationStageFrom(ctx context.Context) server.ExplorationStageFactory {
	stage, _ := ctx.Value(urlExplorationStageKey{}).(server.ExplorationStageFactory)
	return stage
}

func (b *urlPreparationRuntime) productExplorationOptions(l *urlLoadedProfile, e diagnosisprep.Execution, taskID string, flow *diagnosisflow.Controller) (server.ExplorationPipelineOptions, bool) {
	if b == nil || b.product == nil || l == nil || l.productTarget == nil || flow == nil {
		return server.ExplorationPipelineOptions{}, false
	}
	requests := min(l.profile.Budget.Requests, l.productTarget.Target.Requests)
	if requests < 0 {
		requests = 0
	}
	timeout := time.Duration(l.policy.Runtime.WorkerTimeoutMS) * time.Millisecond
	seconds := min(l.profile.Budget.DurationSeconds, int64((timeout+time.Second-1)/time.Second))
	snapshot, _ := flow.Snapshot()
	limits := exploration.Limits{TimeBudgetSeconds: seconds, MaxRequests: uint64(requests), MaxConcurrency: l.policy.Limits.MaxConcurrent, MaxResponseBytes: int64(snapshot.Limits.MaxResponseBytes)}
	limits.MaxConcurrency = min(limits.MaxConcurrency, int(requests))
	if b.hostCapabilities.ExplorationLimits != nil {
		approved := *b.hostCapabilities.ExplorationLimits
		limits.TimeBudgetSeconds = min(limits.TimeBudgetSeconds, approved.TimeBudgetSeconds)
		limits.MaxRequests = min(limits.MaxRequests, approved.MaxRequests)
		limits.MaxConcurrency = min(limits.MaxConcurrency, approved.MaxConcurrency)
		limits.MaxResponseBytes = min(limits.MaxResponseBytes, approved.MaxResponseBytes)
		limits.MaxExternalRequests = approved.MaxExternalRequests
	}
	mode := exploration.FullLocal
	if l.launch.ModelExecutionMode == "openrouter" {
		mode = exploration.ExternalAllowed
	}
	binding := exploration.Binding{SessionRef: "explore-" + rand.Text(), RunRef: "run-" + e.Job.ID, JobID: e.Job.ID, TaskID: taskID, TargetRef: l.productTarget.Target.ID, TargetRevision: l.productTarget.Target.Revision, WriteMode: exploration.ReadOnly, CommMode: mode, Limits: limits}
	maxDuration := min(timeout, time.Duration(limits.TimeBudgetSeconds)*time.Second)
	options := server.ExplorationPipelineOptions{Workflow: flow, Binding: binding, SourceDigest: e.Job.SourceDigest, TargetURL: e.Job.Input.TargetURL, MaxTurns: l.policy.Runtime.MaxWorkerTurns, MaxDuration: maxDuration}
	if factory := b.hostCapabilities.Exploration; factory != nil {
		options.OpenHost = func(ctx context.Context, got exploration.Binding, w *agent.Worker) (server.ExplorationPipelineHost, error) {
			return factory(ctx, got, w)
		}
	}

	return options, true
}

func (b *urlPreparationRuntime) productExplorationStage(l *urlLoadedProfile, e diagnosisprep.Execution, taskID string, flow *diagnosisflow.Controller) server.ExplorationStageFactory {
	options, ok := b.productExplorationOptions(l, e, taskID, flow)
	if !ok {
		return nil
	}
	return func(host *server.Server) server.E2HTTPExplorationStage {
		return func(ctx context.Context, task *server.Task) error {
			if task == nil || task.ID != taskID {
				return exploration.ErrBinding
			}
			_, err := host.RunExplorationPipeline(ctx, task, options)
			return err
		}
	}
}

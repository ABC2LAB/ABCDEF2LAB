//go:build linux

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/server"
)

func productExplorationFixture(t *testing.T, target, taskID string, revision uint64) (*urlPreparationRuntime, *urlLoadedProfile, diagnosisprep.Execution, *diagnosisflow.Controller) {
	t.Helper()
	runtime := &urlPreparationRuntime{product: &productRuntime{}}
	loaded := &urlLoadedProfile{productTarget: &productTargetRecord{Target: diagnosisprep.ProductTarget{ID: target, Revision: revision, Requests: 20}}}
	loaded.profile.Budget = diagnosisprep.Budget{Requests: 10, DurationSeconds: 60}
	loaded.policy.Runtime.MaxWorkerTurns = 7
	loaded.policy.Runtime.WorkerTimeoutMS = 30000
	loaded.policy.Limits.MaxConcurrent = 2
	loaded.launch.ModelExecutionMode = "openrouter"
	execution := diagnosisprep.Execution{Job: diagnosisprep.Job{ID: "job-" + taskID, SourceDigest: strings.Repeat("a", 64), Input: diagnosisprep.Input{TargetURL: "https://" + target + ".owned.test/read", WorkflowMode: diagnosisflow.Auto}}}
	flow, err := diagnosisflow.New(t.Context(), diagnosisflow.Binding{TargetRef: target, TargetRevision: revision, JobID: execution.Job.ID, TaskID: taskID, RunRef: "run-" + execution.Job.ID, SourceDigest: execution.Job.SourceDigest, Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: execution.Job.Input.TargetURL, PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, &workflowReviewStore{})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, loaded, execution, flow
}
func TestProductExplorationBindsDistinctTargetsRevisionsAndSafeHostPolicy(t *testing.T) {
	var sessions []string
	var urls []string
	for _, item := range []struct {
		target, task string
		revision     uint64
	}{{"target-one", "7", 2}, {"target-two", "8", 5}} {
		runtime, loaded, execution, flow := productExplorationFixture(t, item.target, item.task, item.revision)
		options, ok := runtime.productExplorationOptions(loaded, execution, item.task, flow)
		if !ok || options.Binding.Validate() != nil || options.Binding.TargetRef != item.target || options.Binding.TargetRevision != item.revision || options.Binding.TaskID != item.task || options.Binding.JobID != execution.Job.ID || options.SourceDigest != execution.Job.SourceDigest || options.TargetURL != execution.Job.Input.TargetURL || options.OpenHost != nil || options.Binding.WriteMode != exploration.ReadOnly || options.Binding.CommMode != exploration.ExternalAllowed {
			t.Fatal("URL run target or host-only capability binding changed")
		}
		if options.MaxTurns != 7 || options.MaxDuration != 30*time.Second || options.Binding.Limits.MaxRequests != 10 || options.Binding.Limits.MaxConcurrency != 2 || options.Binding.Limits.MaxExternalRequests != 0 {
			t.Fatal("unapproved exploration limits were invented")
		}
		sessions = append(sessions, options.Binding.SessionRef)
		urls = append(urls, options.TargetURL)
	}
	if sessions[0] == sessions[1] || urls[0] == urls[1] {
		t.Fatal("different runs reused exploration authority")
	}
}
func TestProductExplorationDefaultAdapterRecordsNotRunWithoutDockerOrModel(t *testing.T) {
	runtime, loaded, execution, flow := productExplorationFixture(t, "target-owned", "7", 1)
	stage := runtime.productExplorationStage(loaded, execution, "7", flow)
	if stage == nil {
		t.Fatal("product stage not connected")
	}
	if err := stage(&server.Server{})(t.Context(), &server.Task{ID: "7"}); err != nil {
		t.Fatal("missing production adapter did not retain legacy fallback", err)
	}
	snapshot, _ := flow.Snapshot()
	if snapshot.ExplorationStatus != "NOT_RUN" || snapshot.ExplorationReason != "boundary_unavailable" || snapshot.Stage != "crawling" || snapshot.InitialCompleted {
		t.Fatal("default stage misreported execution or consumed static crawl")
	}
	ctx := withURLExplorationStage(t.Context(), stage)
	if urlExplorationStageFrom(ctx) == nil || urlExplorationStageFrom(context.Background()) != nil {
		t.Fatal("host capability escaped its own call context")
	}
}
func TestProductExplorationHostCanNarrowBudgetsButCannotEnableLocalExternalTraffic(t *testing.T) {
	runtime, loaded, execution, flow := productExplorationFixture(t, "target-owned", "7", 1)
	runtime.hostCapabilities.Exploration = func(context.Context, exploration.Binding, *agent.Worker) (server.ExplorationPipelineHost, error) {
		t.Fatal("options creation started external capabilities")
		return server.ExplorationPipelineHost{}, nil
	}
	runtime.hostCapabilities.ExplorationLimits = &exploration.Limits{TimeBudgetSeconds: 10, MaxRequests: 4, MaxExternalRequests: 3, MaxConcurrency: 1, MaxResponseBytes: 4096}
	options, ok := runtime.productExplorationOptions(loaded, execution, "7", flow)
	if !ok || options.OpenHost == nil || options.Binding.Limits.MaxRequests != 4 || options.Binding.Limits.TimeBudgetSeconds != 10 || options.MaxDuration != 10*time.Second || options.Binding.Limits.MaxExternalRequests != 3 {
		t.Fatal("host policy was widened or dropped")
	}
	loaded.launch.ModelExecutionMode = "local"
	options, _ = runtime.productExplorationOptions(loaded, execution, "7", flow)
	if options.Binding.Validate() == nil {
		t.Fatal("full local mode accepted external request budget")
	}
}

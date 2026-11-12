package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/norma/harness"
)

type discoveryCommandStore struct{}

func (discoveryCommandStore) Create(context.Context, diagnosisflow.Record) error       { return nil }
func (discoveryCommandStore) Save(context.Context, uint64, diagnosisflow.Record) error { return nil }
func (discoveryCommandStore) Read(context.Context, string, string) (diagnosisflow.Record, error) {
	return diagnosisflow.Record{}, diagnosisflow.ErrUnavailable
}

type discoveryCommandRunner struct {
	E2HTTPRunner
	run func(context.Context, string) (e2runner.Result, error)
}

func (r discoveryCommandRunner) RunForIntent(ctx context.Context, _ int64, command string) (e2runner.Result, error) {
	return r.run(ctx, command)
}

// Run the actual Worker -> Crawl -> Bash adapter path. The executable records
// argv only; it makes no target, model or deployment acceptance claim.
func TestWorkflowDiscoveryPassesGloboffAndOneQuotedURL(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell required for command argv fixture")
	}
	_, _, binding := e2WorkerFixtureTarget(t, "https://owned.test", "/read")
	const target = "https://owned.test/read/My%20File%7Bc,d%7D"
	flow, err := diagnosisflow.New(t.Context(), diagnosisflow.Binding{JobID: "owned-discovery", TaskID: "7", RunRef: binding.RunRef, SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: diagnosisflow.Manual, TargetURL: target, PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 1, MaxResponseBytes: 4096}, Current: func() error { return nil }}, discoveryCommandStore{})
	if err != nil {
		t.Fatal(err)
	}
	binding.Workflow = flow
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	binding.Runner = discoveryCommandRunner{E2HTTPRunner: binding.Runner, run: func(ctx context.Context, command string) (e2runner.Result, error) {
		calls++
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Env = []string{"PATH=" + dir}
		output, err := cmd.Output()
		if err != nil {
			t.Fatal("program-owned discovery command could not be parsed", err)
		}
		args := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
		globoff, urls := false, 0
		for i, arg := range args {
			globoff = globoff || arg == "--globoff"
			if arg == "--url" {
				urls++
				if i+1 >= len(args) || args[i+1] != target {
					t.Fatal("URL shell quoting changed target")
				}
			}
		}
		if !globoff || urls != 1 {
			t.Fatal("discovery command lost globoff or exact URL cardinality")
		}
		result := completedE2WorkerResult()
		result.ToolOutput = "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\nowned"
		result.Trusted.Call.Counts = e2proxy.Counts{ForwardAttempted: 1, Responses: 1}
		return result, nil
	}}
	intent := &db.Node{ID: binding.IntentID, Kind: db.KindIntent, Payload: json.RawMessage(`{"summary":"Owned initial discovery"}`)}
	reason, _, err := (&Worker{}).executeE2HTTP(t.Context(), "owned-worker", binding.TaskID, nil, nil, intent, nil, nil, binding, "", "")
	if err != nil || reason != harness.ReasonCompleted || calls != 1 {
		t.Fatal("actual discovery path did not close", reason, err, calls)
	}
}

//go:build linux

package main

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"strings"
	"testing"
)

type workflowReviewStore struct {
	row  diagnosisflow.Record
	fail bool
}

func (s *workflowReviewStore) Create(_ context.Context, r diagnosisflow.Record) error {
	s.row = r
	return nil
}
func (s *workflowReviewStore) Save(_ context.Context, _ uint64, r diagnosisflow.Record) error {
	if s.fail {
		return errors.New("owned projection failure")
	}
	s.row = r
	return nil
}
func (s *workflowReviewStore) Read(context.Context, string, string) (diagnosisflow.Record, error) {
	return s.row, nil
}
func reviewWorkflow(t *testing.T) (*diagnosisflow.Controller, *workflowReviewStore) {
	t.Helper()
	store := &workflowReviewStore{}
	c, err := diagnosisflow.New(t.Context(), diagnosisflow.Binding{JobID: "owned-job", TaskID: "42", RunRef: "owned-run", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: diagnosisflow.Manual, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, store)
	if err != nil {
		t.Fatal(err)
	}
	return c, store
}
func TestWorkflowSettlementPreservesUnknownAndKeepsProjectionFailureLocal(t *testing.T) {
	for _, kind := range []string{"completed", "projection_failure", "diagnostics_unknown", "usage_unknown", "wire_unknown"} {
		t.Run(kind, func(t *testing.T) {
			c, store := reviewWorkflow(t)
			store.fail = kind == "projection_failure"
			u := diagnosisprep.Update{State: diagnosisprep.Completed, DrainComplete: true, ResultReady: true}
			if kind == "wire_unknown" {
				u.State = diagnosisprep.Unknown
			}
			if kind == "usage_unknown" {
				u.ModelUsage = &diagnosisprep.ModelUsage{UsageKnown: false}
			}
			got := settleProductWorkflow(t.Context(), c, u, func(out *diagnosisprep.Update) {
				if kind == "diagnostics_unknown" {
					out.State = diagnosisprep.Unknown
					out.ReasonCode = "diagnostic_logging_incomplete"
				}
			})
			want := diagnosisprep.Completed
			if kind == "projection_failure" {
				want = diagnosisprep.Failed
			} else if strings.HasSuffix(kind, "unknown") {
				want = diagnosisprep.Unknown
			}
			if got.State != want || !got.DrainComplete || !got.ResultReady {
				t.Fatal("closure certainty changed", kind, got)
			}
			if kind != "projection_failure" && store.row.Snapshot.Stage != workflowTerminal(want) {
				t.Fatal("saved workflow precedes final diagnostics", kind, store.row.Snapshot.Stage)
			}
		})
	}
}

type workflowJobEndpoint struct{ job diagnosisprep.Job }

func (e workflowJobEndpoint) Call(context.Context, diagnosisprep.Request) (diagnosisprep.Response, error) {
	return diagnosisprep.Response{Job: &e.job}, nil
}
func TestWorkflowCommandsRequireActiveURLJob(t *testing.T) {
	for _, state := range []diagnosisprep.State{diagnosisprep.Completed, diagnosisprep.Failed, diagnosisprep.Cancelled, diagnosisprep.Unknown, diagnosisprep.Running} {
		t.Run(string(state), func(t *testing.T) {
			c, _ := reviewWorkflow(t)
			snapshot, _ := c.Snapshot()
			endpoint := productPreparationEndpoint{base: workflowJobEndpoint{job: diagnosisprep.Job{ID: snapshot.JobID, TaskID: snapshot.TaskID, SourceDigest: snapshot.SourceDigest, State: state, Input: diagnosisprep.Input{WorkflowMode: diagnosisflow.Manual}}}, product: &productRuntime{flows: map[string]*diagnosisflow.Controller{snapshot.JobID: c}}}
			_, err := endpoint.workflowControl(t.Context(), diagnosisprep.Request{Principal: "ARTEX", Product: &diagnosisprep.ProductRequest{JobID: snapshot.JobID, Operation: "workflow_pause", CommandKey: "owned-pause", ExpectedRevision: snapshot.Revision}})
			if (err == nil) != (state == diagnosisprep.Running) {
				t.Fatal("URL job gate lost", state, err)
			}
			after, _ := c.Snapshot()
			if state != diagnosisprep.Running && after.Revision != snapshot.Revision {
				t.Fatal("terminal job mutated workflow")
			}
		})
	}
}

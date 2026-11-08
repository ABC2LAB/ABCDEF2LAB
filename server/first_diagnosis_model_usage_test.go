package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/llmpolicy"
	"testing"
)

func TestFirstDiagnosisPreparationModelUsageOwnedPostgres(t *testing.T) {
	pg := approvalTestDB(t)
	task, err := pg.CreateTask("synthetic model accounting", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.DeleteTask(task.ID); err != nil {
			t.Error(err)
		}
	})
	usage := llmpolicy.OpenRouterUsage{Requests: 2, InputTokens: 17, OutputTokens: 9, CostMicros: 19, ReservedCostMicros: 20000, Model: llmpolicy.OpenRouterModel, Provider: "parasail/fp8", ReturnedProvider: "Parasail", ApprovalRef: "synthetic-preparation-one"}
	for i := 0; i < 2; i++ {
		if err := saveDiagnosisPreparationModelUsage(t.Context(), pg.DB, task.ID, usage, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readDiagnosisPreparationUsage(t.Context(), pg.DB, task.ID)
	if err != nil || len(got) != 1 || got[0].Succeeded || got[0].Usage != usage {
		t.Fatalf("failed-stage accounting lost or duplicated: count=%d err=%v", len(got), err)
	}
	usage.ApprovalRef = "synthetic-preparation-two"
	if err := saveDiagnosisPreparationModelUsage(t.Context(), pg.DB, task.ID, usage, true); err != nil {
		t.Fatal(err)
	}
	got, err = readDiagnosisPreparationUsage(t.Context(), pg.DB, task.ID)
	if err != nil || len(got) != 2 || !got[1].Succeeded {
		t.Fatal("independent approved stage not persisted")
	}
	other, err := readDiagnosisPreparationUsage(t.Context(), pg.DB, task.ID+999999)
	if err != nil || len(other) != 0 {
		t.Fatal("cross-task usage exposed")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := saveDiagnosisPreparationModelUsage(cancelled, pg.DB, task.ID, usage, false); !errors.Is(err, ErrFirstDiagnosisOutcome) {
		t.Fatal("cancelled write reported successful")
	}
}
func TestDiagnosisRunPreservesModelUsageOwnedPostgres(t *testing.T) {
	pg := approvalTestDB(t)
	task, err := pg.CreateTask("synthetic model outcome", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.DeleteTask(task.ID); err != nil {
			t.Error(err)
		}
	})
	state := requestApprovalRunState{RunRef: "synthetic-run", Phase: "closed", Outcome: "completed", DrainComplete: true}
	usage := &llmpolicy.OpenRouterUsage{Requests: 1, InputTokens: 7, OutputTokens: 3, CostMicros: 10, Model: llmpolicy.OpenRouterModel, Provider: "parasail/fp8", ApprovalRef: "synthetic-run-one"}
	for i := 0; i < 2; i++ {
		if err := saveDiagnosisRunWithModel(t.Context(), pg.DB, task.ID, state, usage); err != nil {
			t.Fatal(err)
		}
	}
	var raw []byte
	if err := pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2`, task.ExplorationID, diagnosisRunOrigin).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved diagnosisRunRecord
	if json.Unmarshal(raw, &saved) != nil || saved.Model == nil || *saved.Model != *usage {
		t.Fatal("stored model accounting changed")
	}
	changed := *usage
	changed.Requests++
	if err := saveDiagnosisRunWithModel(t.Context(), pg.DB, task.ID, state, &changed); !errors.Is(err, ErrFirstDiagnosisOutcome) {
		t.Fatal("different usage overwrote an existing run")
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/report"
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

func TestDiagnosticReportShowsStoredPhaseModelUsage(t *testing.T) {
	pg := approvalTestDB(t)
	task, err := pg.CreateTask("synthetic model report", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(task.ID) })
	prep := llmpolicy.OpenRouterUsage{Requests: 2, CostMicros: 19000, InputTokens: 17, OutputTokens: 9}
	run := &llmpolicy.OpenRouterUsage{Requests: 1, CostMicros: 10000, InputTokens: 7, OutputTokens: 3}
	if err := saveDiagnosisPreparationModelUsage(t.Context(), pg.DB, task.ID, prep, true); err != nil {
		t.Fatal(err)
	}
	if err := saveDiagnosisPreparationModelUsage(t.Context(), pg.DB, task.ID, llmpolicy.OpenRouterUsage{Requests: 1, Errors: 1}, false); err != nil {
		t.Fatal(err)
	}
	preparation, err := readDiagnosisPreparationUsage(t.Context(), pg.DB, task.ID)
	if err != nil || len(preparation) != 2 {
		t.Fatal("준비 기록 시험 자료를 읽지 못함", err)
	}
	wantSuccess := "준비 (" + preparation[0].SavedAt.UTC().Format(time.RFC3339Nano) + " · 준비 작업 성공) · 호출 2건"
	wantFailure := "준비 (" + preparation[1].SavedAt.UTC().Format(time.RFC3339Nano) + " · 준비 작업 실패) · 호출 1건"
	state := requestApprovalRunState{RunRef: "synthetic-run", Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunWithModel(t.Context(), pg.DB, task.ID, state, run); err != nil {
		t.Fatal(err)
	}
	data, err := loadDiagnosticReport(t.Context(), pg, task.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(data.ModelAccounting) != 3 || data.ModelAccounting[0].Phase != "preparation" || data.ModelAccounting[1].Phase != "preparation" ||
		data.ModelAccounting[2].Phase != "execution" || data.ModelAccounting[0].CostMicros != 19000 || data.ModelAccounting[2].CostMicros != 10000 {
		t.Fatalf("stored phase usage absent from report: %+v", data.ModelAccounting)
	}
	html, err := report.DiagnosticHTML(data)
	if err != nil || !strings.Contains(string(html), "$0.019000") || !strings.Contains(string(html), "$0.010000") {
		t.Fatal("HTML report omitted stored phase costs", err)
	}
	docx, err := report.DiagnosticDOCX(data)
	if err != nil || !strings.Contains(diagnosticWordText(t, wordParts(t, docx)["word/document.xml"]), "$0.019000") ||
		!strings.Contains(diagnosticWordText(t, wordParts(t, docx)["word/document.xml"]), "$0.010000") {
		t.Fatal("DOCX report omitted stored phase costs", err)
	}
	for _, content := range []string{string(html), diagnosticWordText(t, wordParts(t, docx)["word/document.xml"])} {
		if !strings.Contains(content, wantSuccess) || !strings.Contains(content, wantFailure) {
			t.Fatal("준비 기록의 저장 시각·결과가 표시되지 않음")
		}
	}
	data.Shared = true
	shared, err := report.PrepareDiagnostic(data)
	if err != nil || len(shared.ModelAccounting) != 0 {
		t.Fatal("shared report exposed internal model accounting", err)
	}
	html, err = report.DiagnosticHTML(data)
	if err != nil || strings.Contains(string(html), "$0.019000") || strings.Contains(string(html), "$0.010000") || strings.Contains(string(html), "준비 작업 성공") || strings.Contains(string(html), "준비 작업 실패") {
		t.Fatal("shared HTML exposed internal model accounting", err)
	}
	docx, err = report.DiagnosticDOCX(data)
	if err != nil || strings.Contains(diagnosticWordText(t, wordParts(t, docx)["word/document.xml"]), "준비 작업 ") {
		t.Fatal("공유 DOCX에 준비 기록이 표시됨", err)
	}
	zeroTask, err := pg.CreateTask("synthetic uncertain model cost", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(zeroTask.ID) })
	if err := saveDiagnosisPreparationModelUsage(t.Context(), pg.DB, zeroTask.ID, llmpolicy.OpenRouterUsage{Requests: 1}, false); err != nil {
		t.Fatal(err)
	}
	zero, err := loadDiagnosticReport(t.Context(), pg, zeroTask.ID, time.Now())
	if err != nil || len(zero.ModelAccounting) != 1 || zero.ModelAccounting[0].CostText != "0 또는 미확인" {
		t.Fatal("zero record presented as known cost", err)
	}
}

// 실행 종료 기록은 실행 결과 칸을 상태에 맞을 때만 싣고, 같은 결과로만 다시 저장된다(C08 2-6). 보고서는 같은 기록에서
// 실행 결과 한 줄을 만든다.
func TestDiagnosisRunPreservesRunResultOwnedPostgres(t *testing.T) {
	pg := approvalTestDB(t)
	completed := requestApprovalRunState{RunRef: "synthetic-run", Phase: "closed", Outcome: "completed", DrainComplete: true}
	cancelled := requestApprovalRunState{RunRef: "synthetic-run", Phase: "closed", Outcome: "cancelled", DrainComplete: true}
	failed := requestApprovalRunState{RunRef: "synthetic-run", Phase: "failed", Outcome: "failed", DrainComplete: true}
	partial := &diagnosis.RunResult{Outcome: diagnosis.OutcomePartial, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, Incomplete: 1}}
	stopped := &diagnosis.RunResult{IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, Unknown: 1}}
	for name, tc := range map[string]struct {
		state  requestApprovalRunState
		result *diagnosis.RunResult
	}{
		"completed-with-counts-only": {completed, stopped},
		"cancelled-with-outcome":     {cancelled, partial},
		"failed-with-result":         {failed, stopped},
	} {
		if validDiagnosisRunResult(tc.state, tc.result) {
			t.Fatalf("%s accepted", name)
		}
	}
	for _, tc := range []struct {
		state  requestApprovalRunState
		result *diagnosis.RunResult
		want   string
	}{
		{completed, partial, "일부 완료 · 남은 작업 1"},
		{cancelled, stopped, "중지됨 · 끝난 1 / 끊긴 1"},
	} {
		task, err := pg.CreateTask("synthetic run result", "fixture", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := pg.DeleteTask(task.ID); err != nil {
				t.Error(err)
			}
		})
		if err := saveDiagnosisRunRecord(t.Context(), pg.DB, task.ID, tc.state, nil, &diagnosis.RunResult{Outcome: "bogus"}); !errors.Is(err, ErrFirstDiagnosisOutcome) {
			t.Fatal("result that does not match the state stored")
		}
		for i := 0; i < 2; i++ {
			if err := saveDiagnosisRunRecord(t.Context(), pg.DB, task.ID, tc.state, nil, tc.result); err != nil {
				t.Fatal(err)
			}
		}
		other := *tc.result
		other.IntentOutcomes.Done++
		if tc.state.Outcome == "completed" {
			other.Outcome = other.IntentOutcomes.Outcome()
		}
		if err := saveDiagnosisRunRecord(t.Context(), pg.DB, task.ID, tc.state, nil, &other); !errors.Is(err, ErrFirstDiagnosisOutcome) {
			t.Fatal("different result overwrote an existing run")
		}
		tx, err := pg.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		run, err := readDiagnosticRun(t.Context(), tx, task.ID)
		_ = tx.Rollback()
		if err != nil || run == nil || run.Result == nil || *run.Result != *tc.result || report.DiagnosticRunText(run) != tc.want {
			t.Fatalf("report run: %+v err=%v text=%q", run, err, report.DiagnosticRunText(run))
		}
	}
	// 마감 확인 실패는 실행 결과 없이 저장되고 보고서에 "마감 확인 실패"로 보인다(완료로 보이지 않게).
	task, err := pg.CreateTask("synthetic closure failure", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.DeleteTask(task.ID); err != nil {
			t.Error(err)
		}
	})
	closureFailed := requestApprovalRunState{RunRef: "synthetic-run", Phase: "failed", Outcome: "completed", ClosureFailed: true}
	if validDiagnosisRunResult(closureFailed, partial) || saveDiagnosisRunRecord(t.Context(), pg.DB, task.ID, closureFailed, nil, nil) != nil {
		t.Fatal("closure failure record")
	}
	tx, err := pg.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := readDiagnosticRun(t.Context(), tx, task.ID)
	_ = tx.Rollback()
	if err != nil || run == nil || run.State != report.DiagnosticRunClosureFailed || run.Result != nil {
		t.Fatalf("closure failure report run: %+v err=%v", run, err)
	}
}

// 실행 기록은 목표 문장의 공통 부분·제품 전용 부분 SHA를 남기고, 같은 task의 다시 저장은 목표 칸까지 같아야 한다.
// 감독자는 SHA 형식이 맞을 때 실행 시작 전에 한 번만 정할 수 있다(평가 러너가 원본 본문의 공통 부분과 대조).
func TestDiagnosisRunRecordsGoalDigestsOwnedPostgres(t *testing.T) {
	pg := approvalTestDB(t)
	task, err := pg.CreateTask("synthetic goal digest", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.DeleteTask(task.ID); err != nil {
			t.Error(err)
		}
	})
	common, product := strings.Repeat("a", 64), strings.Repeat("b", 64)
	goal := &diagnosisGoalDigests{Version: diagnosisGoalDigestVersion, CommonSHA256: common, ProductSHA256: product}
	state := requestApprovalRunState{RunRef: "synthetic-run", Phase: "closed", Outcome: "completed", DrainComplete: true}
	for i := 0; i < 2; i++ {
		if err := saveDiagnosisRunRecordGoal(t.Context(), pg.DB, task.ID, state, nil, nil, goal); err != nil {
			t.Fatal("same goal record not idempotent", err)
		}
	}
	var raw []byte
	if err := pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2`, task.ExplorationID, diagnosisRunOrigin).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored diagnosisRunRecord
	if json.Unmarshal(raw, &stored) != nil || stored.Goal == nil || *stored.Goal != *goal {
		t.Fatal("goal digests not stored", string(raw))
	}
	changed := *goal
	changed.CommonSHA256 = strings.Repeat("c", 64)
	if err := saveDiagnosisRunRecordGoal(t.Context(), pg.DB, task.ID, state, nil, nil, &changed); !errors.Is(err, ErrFirstDiagnosisOutcome) {
		t.Fatal("different goal digest accepted as the same record", err)
	}
	if err := saveDiagnosisRunRecordGoal(t.Context(), pg.DB, task.ID, state, nil, nil, nil); !errors.Is(err, ErrFirstDiagnosisOutcome) {
		t.Fatal("dropping the goal digest accepted as the same record", err)
	}

	s := &FirstDiagnosisSupervisor{}
	for _, bad := range [][2]string{{"short", product}, {common, strings.Repeat("G", 64)}, {strings.ToUpper(common), product}} {
		if s.SetGoalDigests(bad[0], bad[1]) == nil {
			t.Fatal("malformed digest accepted", bad)
		}
	}
	if s.SetGoalDigests(common, product) != nil || s.SetGoalDigests(common, product) == nil {
		t.Fatal("goal digests must be set exactly once")
	}
	if got := s.goalDigests(); got == nil || *got != *goal {
		t.Fatal("supervisor goal digests differ", got)
	}
	started := &FirstDiagnosisSupervisor{runStarted: true}
	if started.SetGoalDigests(common, product) == nil {
		t.Fatal("goal digests changed after the run started")
	}
}

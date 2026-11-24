package server

import (
	"encoding/json"
	"testing"
)

// 실행 기록에 한국어 칸 언어 섞임 건수를 남긴다(2026-11-24): 이 실행(run_ref)의 저장된 후보 가운데 다시 쓰게 한 수와
// '다른 언어 섞임' 표시 수. 다른 실행의 후보는 세지 않고, 후보가 없어도 0으로 남긴다.
func TestDiagnosisRunRecordsLanguageCountsOwnedPostgres(t *testing.T) {
	pg := approvalTestDB(t)
	state := requestApprovalRunState{RunRef: "synthetic-language-run", Phase: "closed", Outcome: "completed", DrainComplete: true}
	stored := func(t *testing.T, candidates []string) *diagnosisLanguageCounts {
		t.Helper()
		task, err := pg.CreateTask("synthetic language counts", "fixture", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := pg.DeleteTask(task.ID); err != nil {
				t.Error(err)
			}
		})
		for _, c := range candidates {
			if _, err := pg.ExecContext(t.Context(), `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,5,'confirmed','e2_diagnostic_candidate_v1')`, task.ExplorationID, `{"diagnostic_candidate_v1":`+c+`}`); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			if err := saveDiagnosisRunRecordGoal(t.Context(), pg.DB, task.ID, state, nil, nil, nil); err != nil {
				t.Fatal("같은 기록 다시 저장", err)
			}
		}
		var raw []byte
		if err := pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2`, task.ExplorationID, diagnosisRunOrigin).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var record diagnosisRunRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		return record.Language
	}
	got := stored(t, []string{
		`{"run_ref":"synthetic-language-run","language_mixed":true,"language_rewrites":1}`,
		`{"run_ref":"synthetic-language-run","language_rewrites":1}`,
		`{"run_ref":"synthetic-language-run","language_rewrites":2}`,
		`{"run_ref":"synthetic-language-run","language_mixed":true,"language_rewrites":2}`,
		`{"run_ref":"synthetic-language-run"}`,
		`{"run_ref":"other-run","language_mixed":true,"language_rewrites":1}`,
	})
	// 다시 쓰기는 후보별 횟수의 합(1+1+2+2), 섞임은 표시가 붙은 후보 수다.
	if got == nil || got.Rewrites != 6 || got.Mixed != 2 {
		t.Fatalf("언어 섞임 건수: %+v", got)
	}
	if got := stored(t, nil); got == nil || got.Rewrites != 0 || got.Mixed != 0 {
		t.Fatalf("후보 없는 실행: %+v", got)
	}
}

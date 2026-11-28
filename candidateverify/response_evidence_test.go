package candidateverify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/responseevidence"
)

func selectedEvidenceFixture(t *testing.T) (int64, string, []e2runner.TrustedResult) {
	t.Helper()
	candidate, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	for i := range calls {
		calls[i].Execution.PolicyRevision = "policy-1"
		calls[i].Execution.HeaderMode = e2proxy.HeaderPreserve
		obs := calls[i].Call.Observations[0]
		ev := responseevidence.Extract(responseevidence.Response{Received: true, Complete: true, StatusCode: obs.StatusCode, Method: "GET",
			ContentType: "application/json", Body: []byte(`{"user":{"role":"member"},"items":[1,2]}`)}, responseevidence.Limits{}, nil)
		calls[i].Call.ResponseEvidence = []e2proxy.ResponseEvidence{{AttemptRef: obs.AttemptRef, Sequence: obs.Sequence, Evidence: ev}}
	}
	return candidate.TaskID, candidate.RunRef, calls
}

func TestProjectTrustedSelectedBindsResponseEvidence(t *testing.T) {
	task, run, calls := selectedEvidenceFixture(t)
	rows, err := ProjectTrustedSelected(task, run, calls)
	if err != nil || len(rows) != 2 {
		t.Fatalf("projection: %d %v", len(rows), err)
	}
	for _, row := range rows {
		e := row.ResponseEvidence
		if e == nil || e.Evidence.State != responseevidence.StateComplete || e.ObservationDigest != row.Source.Digest ||
			e.AttemptRef != row.Source.Observation.AttemptRef || !ValidSelectedStoredObservation(task, run, row) {
			t.Fatalf("evidence not bound: %+v", e)
		}
	}
	mutations := []func(*StoredObservationV2){
		func(r *StoredObservationV2) { r.ResponseEvidence.Evidence.JSON.Fields[0].Path = "other" },
		func(r *StoredObservationV2) { r.ResponseEvidence.AttemptRef = "another-attempt" },
		func(r *StoredObservationV2) { r.ResponseEvidence.ObservationDigest = strings.Repeat("0", 64) },
		func(r *StoredObservationV2) { r.ResponseEvidence.Digest = strings.Repeat("0", 64) },
		func(r *StoredObservationV2) { r.ResponseEvidence.Evidence.StatusCode = 418 },
	}
	for i, mutate := range mutations {
		raw, _ := json.Marshal(rows[0])
		var copy StoredObservationV2
		_ = json.Unmarshal(raw, &copy)
		mutate(&copy)
		if ValidSelectedStoredObservation(task, run, copy) {
			t.Fatalf("mutation %d accepted", i)
		}
	}
	// 옛 행(봉투 없음)은 계속 유효하고 근거 없음으로 읽힌다.
	legacy := rows[0]
	legacy.ResponseEvidence = nil
	if !ValidSelectedStoredObservation(task, run, legacy) || RowResponseEvidence(legacy).State != responseevidence.StateNotRecorded {
		t.Fatal("legacy row must stay readable as not recorded")
	}
	if legacy.Source.Digest != rows[0].Source.Digest {
		t.Fatal("observation digest meaning changed")
	}
}

func TestProjectTrustedSelectedNeverInventsEvidence(t *testing.T) {
	task, run, calls := selectedEvidenceFixture(t)
	calls[0].Call.ResponseEvidence[0].Sequence++
	calls[1].Call.ResponseEvidence = append(calls[1].Call.ResponseEvidence, calls[1].Call.ResponseEvidence[0])
	rows, err := ProjectTrustedSelected(task, run, calls)
	if err != nil || len(rows) != 2 {
		t.Fatalf("projection: %d %v", len(rows), err)
	}
	for i, row := range rows {
		if row.ResponseEvidence == nil || row.ResponseEvidence.Evidence.State != responseevidence.StateNotRecorded || !ValidSelectedStoredObservation(task, run, row) {
			t.Fatalf("row %d: mismatched or duplicate evidence must be recorded as not_recorded: %+v", i, row.ResponseEvidence)
		}
	}
	wrong := rows[0]
	ev := wrong.ResponseEvidence.Evidence
	ev.StatusCode++
	wrong.ResponseEvidence = BindResponseEvidence(wrong.Source, ev)
	if ValidResponseEvidence(wrong) {
		t.Fatal("not_recorded envelope cannot claim another response status")
	}
}

func TestProjectTrustedSelectedRejectsResponseMediaMismatch(t *testing.T) {
	task, run, calls := selectedEvidenceFixture(t)
	calls[0].Call.Observations[0].ResponseMediaClass = "html"
	calls[1].Call.Observations[0].ResponseMediaClass = "other" // 기존 관찰은 +json을 other로 분류한다.
	rows, err := ProjectTrustedSelected(task, run, calls)
	if err != nil || len(rows) != 2 {
		t.Fatalf("projection: %d %v", len(rows), err)
	}
	if rows[0].ResponseEvidence.Evidence.State != responseevidence.StateNotRecorded ||
		rows[1].ResponseEvidence.Evidence.State != responseevidence.StateComplete {
		t.Fatalf("media mismatch must not be bound; +json compatibility must remain: %s / %s",
			rows[0].ResponseEvidence.Evidence.State, rows[1].ResponseEvidence.Evidence.State)
	}
	rows[1].Source.Observation.ResponseMediaClass = "html"
	if ValidResponseEvidence(rows[1]) {
		t.Fatal("stored evidence cannot contradict the observed media category")
	}
}

func TestProjectTrustedSelectedKeepsProofOverEvidence(t *testing.T) {
	task, run, calls := selectedEvidenceFixture(t)
	var fields []string
	for i := 0; i < 48; i++ {
		fields = append(fields, `"field_with_long_name_`+strings.Repeat("q", i%9)+string(rune('a'+i%26))+string(rune('a'+i/26))+`":{"x":1}`)
	}
	obs := calls[0].Call.Observations[0]
	big := responseevidence.Extract(responseevidence.Response{Received: true, Complete: true, StatusCode: obs.StatusCode, Method: "GET",
		ContentType: "application/json", Body: []byte("{" + strings.Join(fields, ",") + "}")}, responseevidence.Limits{}, nil)
	fitted, ok := responseevidence.Fit(big, e2proxy.MaxResponseEvidenceBytes)
	if !ok {
		t.Fatal("fixture evidence does not fit")
	}
	calls[0].Call.ResponseEvidence[0].Evidence = fitted
	calls[0].SelectedProof = &e2runner.SelectedAuditProof{}
	calls[0].SelectedProof.Event.Scope.PolicyRef = strings.Repeat("p", 1800)
	rows, err := ProjectTrustedSelected(task, run, calls)
	if err != nil || len(rows) != 2 || rows[0].SelectedProof == nil {
		t.Fatalf("Host proof must not be dropped for response evidence: %d %v", len(rows), err)
	}
	raw, _ := json.Marshal(rows[0])
	if len(raw) > 4096 || rows[0].ResponseEvidence == nil || !ValidSelectedStoredObservation(task, run, rows[0]) {
		t.Fatalf("row size %d", len(raw))
	}
	if st := rows[0].ResponseEvidence.Evidence.State; st != responseevidence.StateLimited && st != responseevidence.StateNotRecorded {
		t.Fatalf("shrunk evidence must be explicit: %s", st)
	}
}

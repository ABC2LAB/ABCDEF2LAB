package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

func e3TestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_E3_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_E3_TEST_DSN to a dedicated synthetic PostgreSQL database")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal("dedicated E3 database unavailable")
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(e3FindingSchemaSQL); err != nil {
		t.Fatal("E3 schema failed", err)
	}
	return d
}

type e3Fixture struct {
	prepared                        e3record.Prepared
	input                           e3record.Input
	source                          e3record.Source
	markers                         TaskModeMarkerReader
	taskID, intentID, explorationID int64
}

func e3FixtureFor(t *testing.T, d *DB, value oracle.VerdictValue, mode bool) e3Fixture {
	t.Helper()
	task, err := d.CreateTask("synthetic E3 initial storage", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "synthetic owner/cross"}, 5, nil, "e3test")
	if err != nil {
		t.Fatal(err)
	}
	markers := func(_ context.Context, id int64) (taskmode.Marker, error) {
		return taskmode.Marker{TaskID: id, State: taskmode.MarkerPresent}, nil
	}
	version := uint64(1)
	if mode {
		record, err := d.RegisterTaskMode(t.Context(), task.ID, taskmode.ModeControlled, markers)
		if err != nil {
			t.Fatal(err)
		}
		version = record.Version
	}
	at := time.Date(2026, 11, 3, 12, 0, 20, 0, time.UTC)
	c := evidencebind.Context{Scope: transmit.Scope{RunRef: fmt.Sprintf("synthetic-e3-%d", task.ID), TaskRef: strconv.FormatInt(task.ID, 10),
		CaseRef: "synthetic-case", ResetRef: "synthetic-reset", PolicyRef: "synthetic-policy"},
		IntentID: intent, ResourceRef: "synthetic:document:one", ReadApprovalRef: "synthetic-read", KGPolicyVersion: "synthetic-kg-v1",
		Owner:     idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "reader", Handle: "session-b", Status: oracle.SubjectVerified}}
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
	facts := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
		NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
	switch value {
	case oracle.Allowed:
		policy.ExpectedAccess = oracle.AccessAllow
	case oracle.Suspected:
		policy.ExpectedAccess = oracle.AccessUnknown
	case oracle.Undecidable:
		facts.CrossNormalData = oracle.StateUnknown
	}
	receipt := func(a idor.AccountRef, n uint64, admitted, done time.Time) transmit.Receipt {
		return transmit.Receipt{
			Scope: c.Scope, Attempt: n, AccountRef: a.Label, SessionRef: a.Handle, ResourceRef: c.ResourceRef, ApprovalRef: c.ReadApprovalRef,
			Method: "GET", Reason: transmit.Completed, Admitted: true, AdmittedAt: admitted, RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: done}
	}
	owner := evidencebind.Observation{Context: c, Receipt: receipt(c.Owner, 1, at.Add(-10*time.Second), at.Add(-8*time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "owner-capture", IntentID: intent, Scope: c.Scope, Attempt: 1, Role: evidencebind.OwnerRead}, ObservedAt: at.Add(-9 * time.Second)}
	cross := evidencebind.Observation{Context: c, Receipt: receipt(c.Evaluated, 2, at.Add(-5*time.Second), at.Add(time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "cross-capture", IntentID: intent, Scope: c.Scope, Attempt: 2, Role: evidencebind.CrossRead}, ObservedAt: at}
	result := idor.Result{Facts: facts, Verdict: oracle.Evaluate(oracle.CaseInput{Policy: policy, Facts: facts,
		Subjects: oracle.SubjectContext{OwnerStatus: c.Owner.Status, EvaluatedStatus: c.Evaluated.Status, OwnerRef: c.Owner.Label, EvaluatedRef: c.Evaluated.Label}}, at)}
	input := e3record.Input{Expected: c, CanaryFields: []string{"note"}, Evidence: evidencebind.Input{
		Policy: evidencebind.PolicySnapshot{Context: c, Assertion: policy}, Owner: owner, Cross: cross,
		Result: evidencebind.ResultSnapshot{Context: c, OwnerEvidence: owner.Evidence, CrossEvidence: cross.Evidence, ObservedAt: at, Result: result}}}
	source := e3record.Source{Expected: c, ExplorationID: task.ExplorationID, ModeVersion: version, EvidenceRevision: 1,
		KGSnapshotRef: "synthetic-kg-snapshot", KGSnapshotDigest: sha256.Sum256([]byte("synthetic KG")),
		GenerationRef: "synthetic-fixture-generation", InstanceRef: "synthetic-target", ModelTag: "synthetic-model",
		ResetGeneration: "synthetic-reset-generation", Policy: policy, CanaryFields: []string{"note"}, Severity: "high",
		FinalCheckpointRef: "synthetic-checkpoint", FinalCheckpointDigest: sha256.Sum256([]byte("synthetic final checkpoint")),
		FinalCheckpointScope: c.Scope, FinalCheckpointSequence: 4, RecordCreatedAt: at.Add(10 * time.Second), Completed: true, Finalized: true}
	source.EvidenceDigest, err = e3record.EvidenceDigest(input.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e3record.Prepare(t.Context(), input, func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil })
	if err != nil {
		t.Fatal("synthetic prepared input failed", err)
	}
	return e3Fixture{prepared, input, source, markers, task.ID, intent, task.ExplorationID}
}

func e3Counts(t *testing.T, d *DB, f e3Fixture) [5]int {
	t.Helper()
	var counts [5]int
	queries := []string{
		`SELECT count(*) FROM e3_evidence_manifests WHERE task_id=$1`,
		`SELECT count(*) FROM e3_verdict_issuances WHERE task_id=$1`,
		`SELECT count(*) FROM e3_verdict_links WHERE task_id=$1`,
		`SELECT count(*) FROM findings WHERE task_id=$1`,
		`SELECT count(*) FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND kind='finding'`,
	}
	for i, q := range queries {
		if err := d.QueryRow(q, f.taskID).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestControlledFindingAtomicIdempotencyAndRead(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	var wg sync.WaitGroup
	results := make(chan ControlledFindingResult, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
			results <- out
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first ControlledFindingResult
	created := 0
	for out := range results {
		if first.FindingID == 0 {
			first = out
		}
		if out.FindingID == 0 || out.FindingID != first.FindingID || out.NodeID != first.NodeID || out.IssuanceID != first.IssuanceID {
			t.Fatal("concurrent duplicate finding")
		}
		if out.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	if got := e3Counts(t, d, f); got != ([5]int{1, 1, 1, 1, 1}) {
		t.Fatalf("counts=%v", got)
	}
	payload, err := d.ControlledFindingRecord(t.Context(), first.FindingID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := f.prepared.Snapshot()
	if string(payload) != string(snapshot.Canonical) {
		t.Fatal("canonical verdict read changed")
	}
	finding, err := d.GetFinding(first.FindingID)
	if err != nil || finding.Status != FindingPending || finding.EvidenceVersion != 1 || finding.ReportEvidenceVersion != 1 {
		t.Fatal("triage/evidence semantics", err)
	}
	expectedReport, reportErr := controlledFindingReport(snapshot, first)
	if reportErr != nil || finding.Report != expectedReport {
		t.Fatal("persisted report differs after duplicate saves", reportErr)
	}
	for _, text := range []string{"## 사전 조건", "GET 시도 1", "GET 시도 2", "응답 상태 200", "Evidence.Owner", "Evidence.Cross", "## 수정 권고", "## 수정 후 재검증", "스크린샷과 요청·응답 증거 화면은 생성하지 않았습니다", fmt.Sprintf("manifest #%d", first.ManifestID)} {
		if !strings.Contains(finding.Report, text) {
			t.Fatalf("missing report section: %s", text)
		}
	}
	if strings.Contains(finding.Report, "session-a") || strings.Contains(finding.Report, "session-b") || strings.Contains(finding.Report, "synthetic:document:one") {
		t.Fatal("private reference copied into report")
	}
	changed := f.source
	changed.Severity = "medium"
	conflicting, err := e3record.Prepare(t.Context(), f.input, func(context.Context, evidencebind.Context) (e3record.Source, error) { return changed, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordControlledFinding(t.Context(), conflicting, f.markers); !errors.Is(err, ErrControlledFindingConflict) {
		t.Fatal("changed input accepted", err)
	}
	if _, err := d.Exec(`UPDATE e3_oracle_verdicts SET oracle_value='allowed' WHERE id=$1`, first.VerdictID); err == nil {
		t.Fatal("immutable verdict updated")
	}
}

func TestControlledFindingNonConfirmedAndUnknownMode(t *testing.T) {
	d := e3TestDB(t)
	for _, value := range []oracle.VerdictValue{oracle.Suspected, oracle.Undecidable, oracle.Allowed} {
		t.Run(string(value), func(t *testing.T) {
			f := e3FixtureFor(t, d, value, true)
			out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
			if err != nil || out.Value != value || out.FindingID != 0 || out.IssuanceID == 0 {
				t.Fatal(out, err)
			}
			if got := e3Counts(t, d, f); got != ([5]int{1, 1, 0, 0, 0}) {
				t.Fatal(got)
			}
		})
	}
	f := e3FixtureFor(t, d, oracle.Confirmed, false)
	if _, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers); !errors.Is(err, ErrControlledFindingMode) {
		t.Fatal("unknown mode accepted", err)
	}
	if got := e3Counts(t, d, f); got != ([5]int{}) {
		t.Fatal(got)
	}
	f = e3FixtureFor(t, d, oracle.Confirmed, true)
	badMarker := func(_ context.Context, id int64) (taskmode.Marker, error) {
		return taskmode.Marker{TaskID: id, State: taskmode.MarkerUnknown}, nil
	}
	if _, err := d.RecordControlledFinding(t.Context(), f.prepared, badMarker); !errors.Is(err, ErrControlledFindingMode) {
		t.Fatal("unknown marker accepted", err)
	}
	if _, err := d.RecordControlledFinding(t.Context(), e3record.Prepared{}, f.markers); !errors.Is(err, ErrControlledFindingInput) {
		t.Fatal("zero prepared accepted", err)
	}
	if got := e3Counts(t, d, f); got != ([5]int{}) {
		t.Fatal(got)
	}
}

func TestControlledFindingRollbackAndGenericGuards(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	name := fmt.Sprintf("e3_test_link_fail_%d", f.taskID)
	if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger AS $$ BEGIN IF NEW.task_id=%d THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql;
CREATE TRIGGER %s BEFORE INSERT ON e3_verdict_links FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.taskID, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON e3_verdict_links; DROP FUNCTION IF EXISTS %s()`, name, name))
	})
	if out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers); !errors.Is(err, ErrControlledFindingStore) || out != (ControlledFindingResult{}) {
		t.Fatal("partial failure", out, err)
	}
	if got := e3Counts(t, d, f); got != ([5]int{}) {
		t.Fatal("partial rows survived", got)
	}
	if _, err := d.Exec(fmt.Sprintf(`DROP TRIGGER %s ON e3_verdict_links`, name)); err != nil {
		t.Fatal(err)
	}
	out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if err != nil {
		t.Fatal(err)
	}
	es := d.Exploration(f.explorationID)
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"AddFinding", func() error {
			_, err := d.AddFinding(f.taskID, 0, "IDOR", "fake", "high", "", "", "model", nil)
			return err
		}},
		{"blank task laundering", func() error {
			_, err := d.AddFinding(0, out.NodeID, "IDOR", "fake", "high", "", "", "model", nil)
			return err
		}},
		{"AddNode", func() error {
			_, err := es.AddNode(KindFinding, map[string]any{}, 5, "confirmed", "model", nil)
			return err
		}},
		{"SetNodeState", func() error { return es.SetNodeState(out.NodeID, "dismissed") }},
		{"status", func() error { _, err := d.SetFindingStatus(out.FindingID, FindingConfirmed); return err }},
		{"notify status", func() error {
			_, _, _, err := d.SetFindingStatusWithNotify(t.Context(), out.FindingID, FindingFixed)
			return err
		}},
		{"vulnclass", func() error { _, err := d.SetFindingVulnClass(out.FindingID, "other"); return err }},
		{"delete", func() error { _, err := d.DeleteFinding(out.FindingID); return err }},
		{"delete by task", func() error { _, err := d.DeleteFindingsByTask(f.taskID); return err }},
		{"raw RecordFindingTx", func() error {
			return d.WithEvidenceTx(t.Context(), func(tx *sql.Tx) error {
				_, err := RecordFindingTx(t.Context(), tx, RecordFindingInput{ExplorationID: f.explorationID, IntentID: f.intentID}, nil)
				return err
			})
		}},
		{"raw evidence bind", func() error {
			return d.WithEvidenceTx(t.Context(), func(tx *sql.Tx) error { return AddFindingTrafficTx(tx, out.FindingID, nil) })
		}},
		{"edit evidence", func() error { return d.EditFindingTraffic(t.Context(), out.FindingID, 0, 1, nil, nil, false, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, ErrControlledFindingGeneric) {
				t.Fatal("generic controlled mutation accepted", err)
			}
		})
	}
	if _, err := es.AddNode(KindFact, map[string]any{"observation": "synthetic"}, 0, "confirmed", "worker", nil); err != nil {
		t.Fatal("fact confused with finding", err)
	}
	if got := e3Counts(t, d, f); got != ([5]int{1, 1, 1, 1, 1}) {
		t.Fatal("guards changed records", got)
	}
}

func TestControlledFindingRejectsRepeatableReadGenericTx(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, false)
	tx, err := d.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM task_control_modes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterTaskMode(t.Context(), f.taskID, taskmode.ModeControlled, f.markers); err != nil {
		t.Fatal(err)
	}
	_, err = RecordFindingTx(t.Context(), tx, RecordFindingInput{TaskID: f.taskID, ExplorationID: f.explorationID, IntentID: f.intentID}, nil)
	if !errors.Is(err, ErrTaskModeIsolation) {
		t.Fatal("repeatable-read stale fence accepted", err)
	}
}
func TestControlledFindingGuardsKeepLegacySentinelAndNoop(t *testing.T) {
	d := e3TestDB(t)
	id, err := d.AddFinding(-1, -1, "synthetic-legacy", "legacy", "low", "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal("legacy negative sentinel changed", err)
	}
	t.Cleanup(func() { d.DeleteFinding(id) })
	var taskID, nodeID sql.NullInt64
	if err := d.QueryRow("SELECT task_id,node_id FROM findings WHERE id=$1", id).Scan(&taskID, &nodeID); err != nil {
		t.Fatal(err)
	}
	if taskID.Valid || nodeID.Valid {
		t.Fatal("negative sentinel became a foreign key")
	}
	if n, err := d.DeleteFindingsByTask(9223372036854775807); err != nil || n != 0 {
		t.Fatal("legacy no-row delete changed", n, err)
	}
}

func TestControlledFindingRejectsUnsupportedRetestAdmission(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	out, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.CreateFindingRetest(t.Context(), out.FindingID, "synthetic"); !errors.Is(err, ErrControlledFindingGeneric) {
		t.Fatal("controlled retest admitted", err)
	}
	task, err := d.CreateTask("synthetic retest transition", "synthetic", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	es := d.Exploration(task.ExplorationID)
	node, err := es.AddNode(KindFinding, map[string]any{}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(task.ID, node, "synthetic-legacy", "legacy", "low", "", "", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	retest, _, _, err := d.CreateFindingRetest(t.Context(), fid, "before transition")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RegisterTaskMode(t.Context(), task.ID, taskmode.ModeControlled, f.markers); err != nil {
		t.Fatal(err)
	}
	if started, err := d.StartFindingRetest(t.Context(), retest.ID); !errors.Is(err, ErrControlledFindingGeneric) || started {
		t.Fatal("retest started after controlled transition", started, err)
	}
	if err := d.FinishFindingRetest(retest.ID, "stopped", "synthetic controlled transition"); err != nil {
		t.Fatal("failed/stopped cleanup blocked", err)
	}
}

func TestControlledFindingFactStateDoesNotAcquireEvidenceLock(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	es := d.Exploration(f.explorationID)
	fact, err := es.AddNode(KindFact, map[string]any{"synthetic": true}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := d.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	if _, err := hold.Exec(`SELECT pg_advisory_xact_lock($1)`, findingEvidenceLockKey); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- es.SetNodeState(fact, "dismissed") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		hold.Rollback()
		<-done
		t.Fatal("ordinary fact state mutation acquired finding evidence lock")
	}
}

func TestControlledFindingReportRejectsInvalidEvidence(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	snapshot, _ := f.prepared.Snapshot()
	result := ControlledFindingResult{ManifestID: 1, VerdictID: 2, IssuanceID: 3, Value: oracle.Confirmed}
	good, err := controlledFindingReport(snapshot, result)
	if err != nil || good == "" {
		t.Fatal("valid confirmed report rejected", err)
	}
	t.Run("manifest digest", func(t *testing.T) {
		bad := snapshot
		bad.Manifest = append([]byte(nil), snapshot.Manifest...)
		bad.Manifest[0] ^= 1
		if text, err := controlledFindingReport(bad, result); err == nil || text != "" {
			t.Fatal("corrupt evidence accepted")
		}
	})
	t.Run("context", func(t *testing.T) {
		bad := snapshot
		bad.Source.Expected.IntentID++
		if text, err := controlledFindingReport(bad, result); err == nil || text != "" {
			t.Fatal("wrong intent accepted")
		}
	})
	for _, verdict := range []oracle.VerdictValue{oracle.Allowed, oracle.Suspected, oracle.Undecidable} {
		t.Run(string(verdict), func(t *testing.T) {
			bad := snapshot
			bad.Value = verdict
			if text, err := controlledFindingReport(bad, result); err == nil || text != "" {
				t.Fatal("unconfirmed report accepted")
			}
		})
	}
	t.Run("no persisted references", func(t *testing.T) {
		if text, err := controlledFindingReport(snapshot, ControlledFindingResult{Value: oracle.Confirmed}); err == nil || text != "" {
			t.Fatal("missing references accepted")
		}
	})
}

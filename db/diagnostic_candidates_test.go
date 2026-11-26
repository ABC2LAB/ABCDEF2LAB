package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func syntheticDiagnosticCandidate(taskID, intentID int64, callRef string) DiagnosticCandidate {
	evidence := fmt.Sprintf(`{"tool_output":"synthetic observed response","trusted":{"classification":"completed","execution":{"scope":{"TaskRef":"%d","RunRef":"synthetic-run"},"intent_id":%d},"call":{"call_ref":%q,"intent_id":%d},"output_truncated":false}}`, taskID, intentID, callRef, intentID)
	return DiagnosticCandidate{TaskID: taskID, IntentID: intentID, RunRef: "synthetic-run", CallRef: callRef, VulnClass: "XSS", Name: "합성 후보", Summary: "실제 탐지 성능 아님", Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1, Evidence: evidence, EvidenceDigest: DiagnosticEvidenceDigest(evidence), ObservedAt: time.Now().UTC()}
}
func TestDiagnosticCandidateIntegrity(t *testing.T) {
	good := syntheticDiagnosticCandidate(1, 2, "call-1")
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*DiagnosticCandidate){
		func(c *DiagnosticCandidate) { c.TaskID++ }, func(c *DiagnosticCandidate) { c.IntentID++ },
		func(c *DiagnosticCandidate) { c.RunRef = "other" }, func(c *DiagnosticCandidate) { c.CallRef = "other" },
		func(c *DiagnosticCandidate) { c.Evidence += " " }, func(c *DiagnosticCandidate) { c.EvidenceVersion++ },
		func(c *DiagnosticCandidate) { c.Verification = "confirmed" }, func(c *DiagnosticCandidate) { c.Classification = "policy_not_executed" },
		func(c *DiagnosticCandidate) { c.OutputTruncated = true },
	} {
		c := good
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid provenance accepted")
		}
	}
}
func TestDiagnosticCandidatePostgres(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("synthetic diagnostic report", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
	defer d.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	s := d.Exploration(task.ExplorationID)
	intent, err := s.AddIntent(map[string]any{"summary": "synthetic"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	c := syntheticDiagnosticCandidate(task.ID, intent, "call-1")
	id, err := s.RecordDiagnosticCandidate(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RecordDiagnosticCandidate(context.Background(), c)
	if err != nil || id != again {
		t.Fatalf("idempotency %d %v", again, err)
	}
	for i := 2; i <= 205; i++ {
		next := syntheticDiagnosticCandidate(task.ID, intent, fmt.Sprintf("call-%d", i))
		if i%2 == 0 {
			next.VulnClass = "SQLi"
		}
		if _, err := s.RecordDiagnosticCandidate(context.Background(), next); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListDiagnosticCandidates(task.ID, "", MaxDiagnosticCandidates+1)
	if err != nil || len(list) != 205 {
		t.Fatalf("pagination missing %d %v", len(list), err)
	}
	hidden := list[len(list)-LiveDiagnosticCandidateLimit-1]
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload=payload #- '{diagnostic_candidate_v1,evidence_digest}' WHERE id=$1`, hidden.ID); err != nil {
		t.Fatal(err)
	}
	live, truncated, err := ListLiveDiagnosticCandidates(t.Context(), d, task.ExplorationID, task.ID, c.RunRef)
	if err != nil || !truncated || len(live) != LiveDiagnosticCandidateLimit {
		t.Fatalf("hidden candidate affected live page: returned=%d truncated=%v err=%v", len(live), truncated, err)
	}
	for i, row := range live {
		if want := list[len(list)-i-1].ID; row.ID != want {
			t.Fatalf("live row %d id=%d, want %d", i, row.ID, want)
		}
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1,evidence_digest}',to_jsonb($2::text)) WHERE id=$1`, hidden.ID, hidden.EvidenceDigest); err != nil {
		t.Fatal(err)
	}
	other, err := s.ListDiagnosticCandidates(task.ID+999, "", MaxDiagnosticCandidates+1)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross task leak %v", err)
	}
	linked := c
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(c.Evidence), &envelope); err != nil {
		t.Fatal(err)
	}
	related := syntheticDiagnosticCandidate(task.ID, intent, "call-related")
	envelope["related_calls"], err = json.Marshal([]json.RawMessage{json.RawMessage(related.Evidence)})
	if err != nil {
		t.Fatal(err)
	}
	rawLinked, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	linked.Evidence = string(rawLinked)
	linked.EvidenceDigest = DiagnosticEvidenceDigest(linked.Evidence)
	linkedID, err := s.RecordDiagnosticCandidate(context.Background(), linked)
	if err != nil || linkedID == id {
		t.Fatalf("changed linked evidence silently reused %v", err)
	}
	retryID, err := s.RecordDiagnosticCandidate(context.Background(), linked)
	if err != nil || retryID != linkedID {
		t.Fatalf("linked evidence not idempotent %v", err)
	}
	foreign := syntheticDiagnosticCandidate(task.ID+1, intent, "foreign-related")
	envelope["related_calls"], err = json.Marshal([]json.RawMessage{json.RawMessage(foreign.Evidence)})
	if err != nil {
		t.Fatal(err)
	}
	rawLinked, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	linked.Evidence = string(rawLinked)
	linked.EvidenceDigest = DiagnosticEvidenceDigest(linked.Evidence)
	if linked.Validate() == nil {
		t.Fatal("foreign related call accepted")
	}
	c.TaskID++
	if _, err := s.RecordDiagnosticCandidate(context.Background(), c); err == nil {
		t.Fatal("cross task accepted")
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload::jsonb,'{diagnostic_candidate_v1,evidence}',to_jsonb('tampered'::text)) WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListDiagnosticCandidates(task.ID, "", MaxDiagnosticCandidates+1); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	var findings int
	if err := d.QueryRow("SELECT count(*) FROM findings WHERE task_id=$1", task.ID).Scan(&findings); err != nil || findings != 0 {
		t.Fatal("candidate became finding")
	}
	raw, _ := json.Marshal(list[0])
	if len(raw) == 0 {
		t.Fatal("missing detail")
	}
}

func TestDiagnosticCandidateFirstSaveFencePostgres(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("synthetic save fence", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
	defer d.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	s := d.Exploration(task.ExplorationID)
	intent, err := s.AddIntent(map[string]any{"summary": "synthetic"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	c := syntheticDiagnosticCandidate(task.ID, intent, "fence-call")
	var mu sync.Mutex
	captures := 0
	ids := make(chan int64, 2)
	writeErrors := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.RecordDiagnosticCandidateWithFence(t.Context(), c, func() (DiagnosticCandidateSaveFence, error) {
				mu.Lock()
				defer mu.Unlock()
				captures++
				return DiagnosticCandidateSaveFence{AttemptHighWater: 1, TaskRef: fmt.Sprint(task.ID), RunRef: c.RunRef, ScopeDigest: DiagnosticEvidenceDigest("{}")}, nil
			})
			ids <- id
			writeErrors <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(writeErrors)
	for err := range writeErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first int64
	for id := range ids {
		if first == 0 {
			first = id
		} else if id != first {
			t.Fatal("duplicate candidate received another ID")
		}
	}
	if captures != 1 {
		t.Fatalf("first-save fence captured %d times", captures)
	}
	rows, err := s.ListDiagnosticCandidates(task.ID, c.RunRef, 2)
	if err != nil || len(rows) != 1 || rows[0].SaveFence == nil || rows[0].SaveFence.AttemptHighWater != 1 || rows[0].SaveFence.CapturedAt.IsZero() {
		t.Fatalf("immutable save fence missing: n=%d err=%v", len(rows), err)
	}
	var candidatePayload []byte
	if err := d.QueryRow(`SELECT payload->'diagnostic_candidate_v1' FROM exploration_nodes WHERE id=$1`, first).Scan(&candidatePayload); err != nil {
		t.Fatal(err)
	}
	var markers int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2 AND payload->>'candidate_id'=$3`, task.ExplorationID, diagnosticCandidateSaveFenceOrigin, fmt.Sprint(first)).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("save marker count=%d err=%v", markers, err)
	}
	var original DiagnosticCandidate
	if err := json.Unmarshal(candidatePayload, &original); err != nil || original.EvidenceDigest != c.EvidenceDigest || original.SaveFence != nil {
		t.Fatal("candidate evidence changed by save fence")
	}
	// Hold the intent lock while another worker admits a request. The fence
	// callback must observe the post-commit high-water, not a pre-insert value.
	lockTx, err := d.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var locked int64
	if err := lockTx.QueryRowContext(t.Context(), `SELECT id FROM exploration_nodes WHERE id=$1 FOR UPDATE`, intent).Scan(&locked); err != nil {
		lockTx.Rollback()
		t.Fatal(err)
	}
	var admitted atomic.Uint64
	admitted.Store(1)
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		_, err := s.RecordDiagnosticCandidateWithFence(t.Context(), syntheticDiagnosticCandidate(task.ID, intent, "concurrent-call"), func() (DiagnosticCandidateSaveFence, error) {
			return DiagnosticCandidateSaveFence{AttemptHighWater: admitted.Load(), TaskRef: fmt.Sprint(task.ID), RunRef: c.RunRef, ScopeDigest: DiagnosticEvidenceDigest("{}")}, nil
		})
		finished <- err
	}()
	<-started
	admitted.Store(2)
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	concurrent, err := s.ListDiagnosticCandidates(task.ID, c.RunRef, 3)
	if err != nil || len(concurrent) != 2 || concurrent[1].SaveFence == nil || concurrent[1].SaveFence.AttemptHighWater != 2 {
		t.Fatalf("concurrent save fence ignored admitted request: n=%d err=%v", len(concurrent), err)
	}
	failed := syntheticDiagnosticCandidate(task.ID, intent, "fence-failure-call")
	failedID, err := s.RecordDiagnosticCandidateWithFence(t.Context(), failed, func() (DiagnosticCandidateSaveFence, error) {
		return DiagnosticCandidateSaveFence{}, errors.New("synthetic fence capture failed")
	})
	if failedID == 0 || err == nil {
		t.Fatal("failed fence was reported as a verified save")
	}
	if _, err := s.RecordDiagnosticCandidateWithFence(t.Context(), failed, func() (DiagnosticCandidateSaveFence, error) {
		t.Fatal("duplicate unexpectedly refreshed first-save fence")
		return DiagnosticCandidateSaveFence{}, nil
	}); err == nil {
		t.Fatal("duplicate without original fence reported success")
	}
	all, err := s.ListDiagnosticCandidates(task.ID, c.RunRef, 4)
	if err != nil || len(all) != 3 || all[2].SaveFence != nil {
		t.Fatalf("failed original fence was backfilled: n=%d err=%v", len(all), err)
	}
}

func TestLiveDiagnosticCandidatesPostgresByteBudget(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := t.Context()
	task, err := d.CreateTask("synthetic live candidate byte budget", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
	defer d.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "synthetic"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	const count = 34
	var ids [count]int64
	var sizes [count]int
	escapedOutput := strings.Repeat(`\"`, 130000)
	for i := range ids {
		c := syntheticDiagnosticCandidate(task.ID, intent, fmt.Sprintf("live-%d", i))
		c.Evidence = strings.Replace(c.Evidence, "synthetic observed response", escapedOutput, 1)
		c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
		id, err := store.RecordDiagnosticCandidate(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		if err := d.QueryRowContext(ctx, `SELECT octet_length(payload::text) FROM exploration_nodes WHERE id=$1`, id).Scan(&sizes[i]); err != nil {
			t.Fatal(err)
		}
	}
	var want, total int
	for i := count - 1; i >= 0; i-- {
		if total+sizes[i] > diagnosticCandidateReadBytes {
			break
		}
		total += sizes[i]
		want++
	}
	if want == 0 || want == count {
		t.Fatalf("fixture must cross the byte budget after at least one row: returned=%d, stored=%d", want, count)
	}
	read := func() ([]DiagnosticCandidate, bool, error) {
		return ListLiveDiagnosticCandidates(ctx, d, task.ExplorationID, task.ID, "synthetic-run")
	}
	rows, truncated, err := read()
	if err != nil || !truncated || len(rows) != want {
		t.Fatalf("byte budget: returned=%d (want %d), truncated=%v, err=%v", len(rows), want, truncated, err)
	}
	for i, row := range rows {
		if wantID := ids[count-i-1]; row.ID != wantID {
			t.Fatalf("live row %d id=%d, want %d", i, row.ID, wantID)
		}
	}
	if _, err := d.ExecContext(ctx, `UPDATE exploration_nodes SET payload=payload #- '{diagnostic_candidate_v1,evidence_digest}' WHERE id=$1`, ids[count-1]); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := read(); len(rows) != 0 || !errors.Is(err, ErrDiagnosticCandidate) {
		t.Fatalf("visible digest corruption: returned=%d err=%v", len(rows), err)
	}
}

func TestDiagnosticCandidateDatabaseScopeAndConcurrency(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var tasks []*Task
	for i := 0; i < 2; i++ {
		task, err := d.CreateTask("synthetic scope", "synthetic only", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		defer d.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
		defer d.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	}
	a, b := d.Exploration(tasks[0].ExplorationID), d.Exploration(tasks[1].ExplorationID)
	ia, err := a.AddIntent(map[string]any{"summary": "a"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	ib, err := b.AddIntent(map[string]any{"summary": "b"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	foreign := syntheticDiagnosticCandidate(tasks[1].ID, ib, "shared-call")
	if foreign.Validate() != nil {
		t.Fatal("fixture is not internally valid")
	}
	if _, err := a.RecordDiagnosticCandidate(context.Background(), foreign); err == nil {
		t.Fatal("foreign task/intent accepted")
	}
	c := syntheticDiagnosticCandidate(tasks[0].ID, ia, "shared-call")
	ids := make(chan int64, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := a.RecordDiagnosticCandidate(context.Background(), c)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	var first int64
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range ids {
		if first == 0 {
			first = id
		}
		if first != id {
			t.Fatal("duplicate concurrent candidate")
		}
	}
	next := c
	next.RunRef = "second-run"
	next.Evidence = strings.ReplaceAll(next.Evidence, "synthetic-run", "second-run")
	next.EvidenceDigest = DiagnosticEvidenceDigest(next.Evidence)
	second, err := a.RecordDiagnosticCandidate(context.Background(), next)
	if err != nil || second == first {
		t.Fatalf("cross-run dedup %v", err)
	}
	for _, run := range []string{"synthetic-run", "second-run"} {
		rows, err := a.ListDiagnosticCandidates(tasks[0].ID, run, 100)
		if err != nil || len(rows) != 1 || rows[0].RunRef != run {
			t.Fatalf("run scope %s %v", run, err)
		}
	}
}

func TestDiagnosticCandidateCallIntentMustMatchIndependently(t *testing.T) {
	c := syntheticDiagnosticCandidate(1, 2, "actual")
	c.Evidence = strings.Replace(c.Evidence, `"call_ref":"actual","intent_id":2`, `"call_ref":"actual","intent_id":3`, 1)
	c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
	if c.Validate() == nil {
		t.Fatal("foreign call intent accepted with correct execution intent")
	}
}
func TestDiagnosticCandidateNodeKindAndSharedExplorationTask(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, err := d.CreateTask("synthetic kind", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(a.ID)
	b, err := d.CreateTask("synthetic same graph", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(b.ID)
	store := d.Exploration(a.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "synthetic"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	c := syntheticDiagnosticCandidate(a.ID, intent, "actual")

	id, err := store.RecordDiagnosticCandidate(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	foreign := syntheticDiagnosticCandidate(b.ID, intent, "actual")
	raw, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1}',$1::jsonb) WHERE id=$2`, string(raw), id); err != nil {
		t.Fatal(err)
	}
	// 동일 그래프 행에 내부적으로 유효한 타 task payload가 들어간 경우다.
	if _, err = store.ListDiagnosticCandidates(a.ID, "", 100); err == nil {
		t.Fatal("same-exploration foreign task payload leaked")
	}

	if _, err = d.Exec(`UPDATE exploration_nodes SET kind='fact',state='confirmed' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	c.CallRef = "other"
	c.Evidence = strings.ReplaceAll(c.Evidence, "actual", "other")
	c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
	if _, err = store.RecordDiagnosticCandidate(t.Context(), c); err == nil {
		t.Fatal("fact accepted as intent")
	}
}

package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

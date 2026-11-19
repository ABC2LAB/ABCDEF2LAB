package server

import (
	"crypto/sha256"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/oracle"
)

// candidate selector 시험(docs/contracts/finding-verdict-detail-decision.md OD05·OD09 2조각).

// storeVerdictDetailCandidate는 작업 하나에 진단 후보 하나를 저장하고 번호를 돌려준다.
func storeVerdictDetailCandidate(t *testing.T, pg *db.DB, taskID, explorationID int64, run, call string) int64 {
	t.Helper()
	store := pg.Exploration(explorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "synthetic candidate intent"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidence := `{"trusted":{"classification":"result_unknown","execution":{"scope":{"TaskRef":"` + strconv.FormatInt(taskID, 10) + `","RunRef":"` + run +
		`"},"intent_id":` + strconv.FormatInt(intent, 10) + `},"call":{"call_ref":"` + call + `","intent_id":` + strconv.FormatInt(intent, 10) + `}}}`
	id, err := store.RecordDiagnosticCandidate(t.Context(), db.DiagnosticCandidate{
		TaskID: taskID, IntentID: intent, RunRef: run, CallRef: call, VulnClass: "idor", Name: "synthetic candidate", Summary: "synthetic",
		Remediation: "synthetic", Classification: "result_unknown", Verification: "manual_review_required", EvidenceVersion: 1,
		Evidence: evidence, EvidenceDigest: db.DiagnosticEvidenceDigest(evidence), ObservedAt: time.Now().UTC(),
	})
	if err != nil || id <= 0 {
		t.Fatal("후보 저장", err)
	}
	return id
}

// storeCandidateProductVerdict는 후보 참조를 단 제품 판정을 저장한다(확정 단계가 저장 때 검증한 연결의 모양).
func storeCandidateProductVerdict(t *testing.T, pg *db.DB, taskID int64, run, resource string, candidates []string) db.ProductVerdictResult {
	t.Helper()
	v := db.ProductVerdict{TaskID: taskID,
		Binding: db.ProductBinding{JobRef: "job-synthetic", RunRef: run, E2PolicyRef: "e2-policy-synthetic", GenerationRef: "generation-synthetic",
			InstanceRef: "instance-synthetic", Policy: db.ProductPolicySource{Ref: "declaration-job-synthetic",
				DigestSHA256: sha256.Sum256([]byte("synthetic declaration")), TargetApprovalRevision: "target-revision-1"}},
		Journal:      db.ProductJournal{Ref: "journal-synthetic", DigestSHA256: sha256.Sum256([]byte("synthetic journal"))},
		ResourceType: resource, ResourceRef: resource + ":" + strings.Repeat("b", 64), ModelTag: "synthetic-model",
		Value: oracle.Confirmed, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateYes, DataCheck: oracle.StateYes,
		OracleReasons:  []oracle.JudgeReason{oracle.ReasonPolicyDenyResolved},
		ProductReasons: []string{}, CandidateRefs: candidates,
		Evidence:   []db.ProductEvidence{{CallRef: "call-1", AttemptID: "attempt-1", Sequence: 1}},
		ObservedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	out, err := pg.RecordProductVerdict(t.Context(), v)
	if err != nil || !out.Created {
		t.Fatal("제품 판정 저장", err)
	}
	return out
}

func TestFindingVerdictDetailCandidateSelector(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task, err := pg.CreateTask("synthetic candidate verdict detail", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID) })
	const run = "product-run-candidate"
	matched := &verdictDetailFakePolicy{result: verdictPolicySourceMatched}
	read := func(t *testing.T, taskID, candidate int64) (verdictDetailEnvelope, int, string) {
		t.Helper()
		return readFindingVerdictDetail(t.Context(), pg.DB, taskID, verdictDetailSelector{verdictDetailByCandidate, candidate}, matched)
	}
	wantCandidate := func(t *testing.T, env verdictDetailEnvelope, id int64) {
		t.Helper()
		ref := strconv.FormatInt(id, 10)
		if env.Selection != (verdictDetailSelection{Kind: "candidate", ID: ref}) || env.IDs.CandidateID == nil || *env.IDs.CandidateID != ref ||
			env.Candidate == nil || *env.Candidate != (verdictDetailCandidate{ID: ref, Verification: "manual_review_required"}) || env.CandidateLink != nil {
			t.Fatalf("후보 칸: %+v %+v", env.IDs, env.Candidate)
		}
	}

	linked := storeVerdictDetailCandidate(t, pg, task.ID, task.ExplorationID, run, "call-linked")
	verdict := storeCandidateProductVerdict(t, pg, task.ID, run, "document", []string{strconv.FormatInt(linked, 10)})
	env, status, code := read(t, task.ID, linked)
	wire, raw := verdictDetailWire(t, env)
	record, ok := env.Record.(verdictDetailProductRecord)
	if status != http.StatusOK || code != "" || !ok || env.Availability != "recorded" || *env.SourceKind != "product" || wire["source_kind"] != "product" ||
		*env.IDs.VerdictID != strconv.FormatInt(verdict.VerdictID, 10) || env.RecordChecks.Status != "verified" ||
		!slices.Equal(record.CandidateRefs, []string{strconv.FormatInt(linked, 10)}) {
		t.Fatalf("후보에 연결된 판정: %d %s %s", status, code, raw)
	}
	wantCandidate(t, env, linked)

	// 후보는 있고 그 후보를 참조하는 판정이 없다. 판정 미실행 증명이 아니므로 unknown이다.
	alone := storeVerdictDetailCandidate(t, pg, task.ID, task.ExplorationID, run, "call-alone")
	env, status, code = read(t, task.ID, alone)
	if status != http.StatusOK || code != "" || env.Availability != "no_link" || env.EvaluationStatus != "unknown" || env.Record != nil ||
		env.IDs.VerdictID != nil || env.SourceKind != nil || env.RecordChecks != nil {
		_, raw = verdictDetailWire(t, env)
		t.Fatalf("연결 없는 후보: %d %s %s", status, code, raw)
	}
	wantCandidate(t, env, alone)

	// 같은 후보를 참조하는 판정이 둘이면 고르지 않는다(OD05 422).
	twice := storeVerdictDetailCandidate(t, pg, task.ID, task.ExplorationID, run, "call-twice")
	for _, resource := range []string{"note", "secret"} {
		storeCandidateProductVerdict(t, pg, task.ID, run, resource, []string{strconv.FormatInt(twice, 10)})
	}
	if env, status, code = read(t, task.ID, twice); status != http.StatusUnprocessableEntity || code != verdictDetailIntegrityFailed || env.Format != "" {
		t.Fatalf("후보를 참조하는 판정 둘: %d %s", status, code)
	}

	// 후보와 다른 실행의 판정이 그 후보를 참조하면 저장 불변식 위반이다.
	crossRun := storeVerdictDetailCandidate(t, pg, task.ID, task.ExplorationID, run, "call-cross-run")
	storeCandidateProductVerdict(t, pg, task.ID, "product-run-other", "card", []string{strconv.FormatInt(crossRun, 10)})
	if _, status, code = read(t, task.ID, crossRun); status != http.StatusUnprocessableEntity || code != verdictDetailIntegrityFailed {
		t.Fatalf("다른 실행의 판정: %d %s", status, code)
	}

	// 다른 작업의 후보와 없는 후보는 같은 404다(존재 여부를 드러내지 않음).
	other, err := pg.CreateTask("synthetic other candidate task", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, other.ID) })
	// 다른 작업의 제품 판정이 같은 번호 문자열을 참조해도 이 작업의 후보 조회에 섞이지 않는다.
	storeCandidateProductVerdict(t, pg, other.ID, run, "document", []string{strconv.FormatInt(linked, 10)})
	if env, status, _ = read(t, task.ID, linked); status != http.StatusOK || env.IDs.VerdictID == nil || *env.IDs.VerdictID != strconv.FormatInt(verdict.VerdictID, 10) {
		t.Fatalf("다른 작업의 판정이 섞임: %d", status)
	}
	// 후보가 아닌 같은 작업의 탐색 노드(intent)는 후보가 아니다(404).
	intent, err := pg.Exploration(task.ExplorationID).AddIntent(map[string]any{"summary": "not a candidate"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, status, code = read(t, task.ID, intent); status != http.StatusNotFound || code != verdictDetailNotFound {
		t.Fatalf("후보 아닌 노드: %d %s", status, code)
	}
	// 같은 작업의 다른 출처 사실 노드도 후보가 아니다(출처로 거름).
	var fact int64
	if err := pg.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES($1,'fact','confirmed','test','{"name":"ordinary fact"}') RETURNING id`,
		task.ExplorationID).Scan(&fact); err != nil {
		t.Fatal(err)
	}
	if _, status, code = read(t, task.ID, fact); status != http.StatusNotFound || code != verdictDetailNotFound {
		t.Fatalf("후보 아닌 사실 노드: %d %s", status, code)
	}
	foreign := storeVerdictDetailCandidate(t, pg, other.ID, other.ExplorationID, run, "call-foreign")
	for _, c := range []int64{foreign, linked + 1_000_000} {
		if _, status, code = read(t, task.ID, c); status != http.StatusNotFound || code != verdictDetailNotFound {
			t.Fatalf("다른 작업·없는 후보 %d: %d %s", c, status, code)
		}
	}

	// 후보 노드 저장 형식이 어긋나면(다른 작업 결속) 422다.
	broken := storeVerdictDetailCandidate(t, pg, task.ID, task.ExplorationID, run, "call-broken")
	refs := verdictDetailRefs{task: task.ID}
	_, status, code = verdictDetailTamper(t, pg, refs, verdictDetailSelector{verdictDetailByCandidate, broken}, matched,
		[]string{`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1,task_id}',to_jsonb(` + strconv.FormatInt(other.ID, 10) + `)) WHERE id=` + strconv.FormatInt(broken, 10)}, nil)
	if status != http.StatusUnprocessableEntity || code != verdictDetailIntegrityFailed {
		t.Fatalf("깨진 후보: %d %s", status, code)
	}
	// 연결된 판정의 정본이 변조되면 다른 selector와 같이 진위 실패다.
	env, status, code = verdictDetailTamper(t, pg, verdictDetailRefs{task: task.ID, verdict: verdict.VerdictID}, verdictDetailSelector{verdictDetailByCandidate, linked}, matched,
		[]string{`UPDATE e3_oracle_verdicts SET payload_digest=decode(repeat('00',32),'hex') WHERE id={v}`}, nil)
	verdictDetailWantIntegrityFailure(t, env, status, code, "product")
	wantCandidate(t, env, linked)
}

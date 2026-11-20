package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
)

// 판정 상세 조회 1조각 시험(docs/contracts/finding-verdict-detail-decision.md §2). 변조는 소유 시험 연결의 transaction 안에서만
// 재현하고 반드시 롤백한다(session_replication_role=replica는 그 transaction의 불변 트리거·FK만 끈다).

type verdictDetailFakePolicy struct {
	result verdictPolicySourceResult
	got    []verdictPolicySourceRequest
}

func (f *verdictDetailFakePolicy) CheckPolicySource(_ context.Context, r verdictPolicySourceRequest) verdictPolicySourceResult {
	f.got = append(f.got, r)
	return f.result
}

type verdictDetailRefs struct{ task, issuance, verdict, manifest, finding int64 }

func (r verdictDetailRefs) sql(q string) string {
	return strings.NewReplacer("{t}", strconv.FormatInt(r.task, 10), "{i}", strconv.FormatInt(r.issuance, 10), "{v}", strconv.FormatInt(r.verdict, 10),
		"{m}", strconv.FormatInt(r.manifest, 10), "{f}", strconv.FormatInt(r.finding, 10)).Replace(q)
}

func verdictDetailWire(t *testing.T, env verdictDetailEnvelope) (map[string]any, string) {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

func storeVerdictDetailEval(t *testing.T, pg *db.DB, value oracle.VerdictValue) (diagnosticE3Fixture, verdictDetailRefs) {
	t.Helper()
	f := diagnosticE3FixtureFor(t, pg, value, true)
	result, err := pg.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if err != nil {
		t.Fatal(err)
	}
	return f, verdictDetailRefs{f.taskID, result.IssuanceID, result.VerdictID, result.ManifestID, result.FindingID}
}

func storeVerdictDetailProduct(t *testing.T, pg *db.DB, value oracle.VerdictValue) verdictDetailRefs {
	t.Helper()
	task, err := pg.CreateTask("synthetic product verdict detail", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID) })
	out := storeProductVerdict(t, pg, task.ID, "product-run-detail", value)
	return verdictDetailRefs{task.ID, out.IssuanceID, out.VerdictID, out.ManifestID, 0}
}

// verdictDetailTamper는 한 transaction에서 변조 SQL과 다시 서명한 위조를 적용하고 같은 snapshot으로 읽은 뒤 롤백한다.
func verdictDetailTamper(t *testing.T, pg *db.DB, refs verdictDetailRefs, sel verdictDetailSelector, policy policySourceReader, statements []string, forge func(*sql.Tx)) (verdictDetailEnvelope, int, string) {
	t.Helper()
	tx, err := pg.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(t.Context(), refs.sql(statement)); err != nil {
			t.Fatal(statement, err)
		}
	}
	if forge != nil {
		forge(tx)
	}
	loaded, status, code := loadVerdictDetail(t.Context(), tx, refs.task, sel)
	if status != http.StatusOK {
		return verdictDetailEnvelope{}, status, code
	}
	return assembleVerdictDetail(t.Context(), refs.task, sel, loaded, policy)
}

func verdictDetailDecode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out map[string]any
	if err := d.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func verdictDetailEncode(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type verdictDetailEvalForge struct {
	payload func(map[string]any)
	source  func(*e3record.Source, *e3record.Manifest)
	binding func(map[string]any)
	// context는 실행 문맥 칸(정본 JSON의 "키":"값")을 Source·manifest의 모든 자리에서 같이 바꾼다. 근거 digest도 다시 계산한다.
	context [2]string
}

// forgeEvalVerdict는 정본 세 개를 고친 뒤 정본 해시와 발급 입력 digest까지 다시 맞춘다. 남는 방어선은 의미 대조뿐이다.
func forgeEvalVerdict(t *testing.T, tx *sql.Tx, issuance int64, f verdictDetailEvalForge) {
	t.Helper()
	var verdict, manifestID int64
	var payloadRaw, manifestRaw, bindingRaw []byte
	if err := tx.QueryRowContext(t.Context(), `SELECT i.verdict_id,i.manifest_id,v.payload_canonical,m.canonical,i.binding_canonical FROM e3_verdict_issuances i
JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.id=$1`, issuance).
		Scan(&verdict, &manifestID, &payloadRaw, &manifestRaw, &bindingRaw); err != nil {
		t.Fatal(err)
	}
	var source e3record.Source
	var manifest e3record.Manifest
	if f.context[0] != "" {
		old, forged := []byte(f.context[0]), []byte(f.context[1])
		if !bytes.Contains(bindingRaw, old) || !bytes.Contains(manifestRaw, old) {
			t.Fatalf("위조할 문맥 칸이 정본에 없음: %s", f.context[0])
		}
		manifestRaw, bindingRaw = bytes.ReplaceAll(manifestRaw, old, forged), bytes.ReplaceAll(bindingRaw, old, forged)
	}
	if err := json.Unmarshal(bindingRaw, &source); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	payload := verdictDetailDecode(t, payloadRaw)
	if f.payload != nil {
		f.payload(payload)
	}
	if f.source != nil {
		f.source(&source, &manifest)
	}
	if f.context[0] != "" {
		digest, err := e3record.EvidenceDigest(manifest.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		manifest.EvidenceDigest, source.EvidenceDigest = digest, digest
	}
	payloadRaw, manifestRaw, bindingRaw = verdictDetailEncode(t, payload), verdictDetailEncode(t, manifest), verdictDetailEncode(t, source)
	if f.binding != nil {
		b := verdictDetailDecode(t, bindingRaw)
		f.binding(b)
		bindingRaw = verdictDetailEncode(t, b)
	}
	pd, md, bd := sha256.Sum256(payloadRaw), sha256.Sum256(manifestRaw), sha256.Sum256(bindingRaw)
	input := evalVerdictInputDigest(pd, md, bd)
	verdictDetailForgeWrite(t, tx, issuance, verdict, manifestID, payloadRaw, manifestRaw, bindingRaw, pd, md, input)
}

func verdictDetailForgeWrite(t *testing.T, tx *sql.Tx, issuance, verdict, manifest int64, payload, manifestRaw, binding []byte, pd, md, input [32]byte) {
	t.Helper()
	for _, step := range []struct {
		query string
		args  []any
	}{
		{`UPDATE e3_oracle_verdicts SET payload_canonical=$2,payload_digest=$3 WHERE id=$1`, []any{verdict, payload, pd[:]}},
		{`UPDATE e3_evidence_manifests SET canonical=$2,digest=$3 WHERE id=$1`, []any{manifest, manifestRaw, md[:]}},
		{`UPDATE e3_verdict_issuances SET binding_canonical=$2,input_digest=$3 WHERE id=$1`, []any{issuance, binding, input[:]}},
	} {
		if _, err := tx.ExecContext(t.Context(), step.query, step.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func forgeProductVerdict(t *testing.T, tx *sql.Tx, issuance int64, payload, binding, manifest func(map[string]any)) {
	t.Helper()
	var verdict, manifestID int64
	var payloadRaw, manifestRaw, bindingRaw []byte
	if err := tx.QueryRowContext(t.Context(), `SELECT i.verdict_id,i.manifest_id,v.payload_canonical,m.canonical,i.binding_canonical FROM e3_verdict_issuances i
JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.id=$1`, issuance).
		Scan(&verdict, &manifestID, &payloadRaw, &manifestRaw, &bindingRaw); err != nil {
		t.Fatal(err)
	}
	p, b, m := verdictDetailDecode(t, payloadRaw), verdictDetailDecode(t, bindingRaw), verdictDetailDecode(t, manifestRaw)
	for _, step := range []struct {
		f func(map[string]any)
		v map[string]any
	}{{payload, p}, {binding, b}, {manifest, m}} {
		if step.f != nil {
			step.f(step.v)
		}
	}
	payloadRaw, bindingRaw, manifestRaw = verdictDetailEncode(t, p), verdictDetailEncode(t, b), verdictDetailEncode(t, m)
	pd, md, bd := sha256.Sum256(payloadRaw), sha256.Sum256(manifestRaw), sha256.Sum256(bindingRaw)
	verdictDetailForgeWrite(t, tx, issuance, verdict, manifestID, payloadRaw, manifestRaw, bindingRaw, pd, md, productVerdictInputDigest(pd, md, bd))
}

func verdictDetailWantIntegrityFailure(t *testing.T, env verdictDetailEnvelope, status int, code, source string) {
	t.Helper()
	if status != http.StatusOK || code != "" {
		t.Fatalf("진위 실패가 200이 아님: %d %s", status, code)
	}
	if env.Record != nil || env.RecordChecks == nil || env.RecordChecks.Status != "unavailable" ||
		!slices.Equal(env.RecordChecks.ReasonCodes, []string{verdictDetailRecordIntegrityFailed}) ||
		env.View.DisplayVerdict != nil || env.View.ConfirmationEvidenceComplete != nil || len(env.View.MissingRequirements) != 0 ||
		env.SourceKind == nil || *env.SourceKind != source || env.Availability != "recorded" || env.EvaluationStatus != "recorded" ||
		env.PolicySource != nil || env.Presentation.Severity != nil || env.Transmission.State != "unknown" || len(env.Transmission.Steps) != 0 ||
		env.Transmission.Coverage != "unavailable" {
		t.Fatalf("진위 실패 판정이 표시됨: %+v", env)
	}
}

func TestParseVerdictDetailSelector(t *testing.T) {
	for raw, want := range map[string]verdictDetailSelector{
		"verdict_id=1":                   {verdictDetailByVerdict, 1},
		"finding_id=42":                  {verdictDetailByFinding, 42},
		"verdict_id=9223372036854775807": {verdictDetailByVerdict, math.MaxInt64},
		"candidate_id=7":                 {verdictDetailByCandidate, 7},
	} {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := parseVerdictDetailSelector(q)
		if !ok || got != want {
			t.Fatalf("%s: %+v %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"", "verdict_id=1&finding_id=1", "verdict_id=1&verdict_id=1", "verdict_id=1&verdict_id=2", "verdict_id=1&x=1", "x=1",
		"candidate_id=1&verdict_id=1", "candidate_id=01", "candidate_id=0", "verdict_id=01", "verdict_id=+1", "verdict_id=0", "verdict_id=-1", "verdict_id=9223372036854775808", "verdict_id=",
		"verdict_id=1.0", "verdict_id=%201", "finding_id=1e3", "VERDICT_ID=1", "verdict_id"} {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(raw, err)
		}
		if got, ok := parseVerdictDetailSelector(q); ok {
			t.Fatalf("%q 받아들임: %+v", raw, got)
		}
	}
}

func TestVerdictDetailResponseSizeLimit(t *testing.T) {
	base := verdictDetailEnvelope{Format: verdictDetailFormat, View: verdictDetailView{MissingRequirements: []string{}}}
	base.Record = verdictDetailEvalRecord{CaseRef: strings.Repeat("a", maxVerdictDetailBytes/2)}
	if _, status, _ := verdictDetailFinish(base); status != http.StatusOK {
		t.Fatal("상한 안 응답 거부", status)
	}
	base.Record = verdictDetailEvalRecord{CaseRef: strings.Repeat("a", maxVerdictDetailBytes)}
	env, status, code := verdictDetailFinish(base)
	if status != http.StatusUnprocessableEntity || code != verdictDetailIntegrityFailed || env.Format != "" {
		t.Fatal("상한 초과를 잘라 보냄", status, code)
	}
}

func TestFindingVerdictDetailEvalValues(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	missing := map[oracle.VerdictValue][]string{oracle.Confirmed: {}, oracle.Allowed: {}, oracle.Suspected: {"policy_check"},
		oracle.Undecidable: {"data_check", "cross_normal_data"}}
	access := map[oracle.VerdictValue]string{oracle.Confirmed: e3record.AccessPolicyViolationCandidate, oracle.Suspected: e3record.AccessPolicyViolationCandidate,
		oracle.Undecidable: e3record.AccessPolicyUndecidable, oracle.Allowed: e3record.AccessPolicyNoViolationObserved}
	for _, value := range []oracle.VerdictValue{oracle.Confirmed, oracle.Suspected, oracle.Undecidable, oracle.Allowed} {
		t.Run(string(value), func(t *testing.T) {
			f, refs := storeVerdictDetailEval(t, pg, value)
			snapshot, ok := f.prepared.Snapshot()
			if !ok || evalVerdictInputDigest(snapshot.PayloadDigest, snapshot.ManifestDigest, sha256.Sum256(snapshot.Binding)) != snapshot.InputDigest {
				t.Fatal("발급 입력 digest 재계산이 Prepare와 다름")
			}
			// 평가 판정은 정책 동결본 reader를 부르지 않는다(KG 본문은 이 조각에서 읽지 않음).
			policy := &verdictDetailFakePolicy{result: verdictPolicySourceError}
			env, status, code := readFindingVerdictDetail(t.Context(), pg.DB, f.taskID, verdictDetailSelector{verdictDetailByVerdict, refs.verdict}, policy)
			if status != http.StatusOK || code != "" || len(policy.got) != 0 {
				t.Fatalf("평가 판정 조회 %d %s 정책 호출 %d", status, code, len(policy.got))
			}
			wire, raw := verdictDetailWire(t, env)
			record, ok := env.Record.(verdictDetailEvalRecord)
			if !ok {
				t.Fatalf("평가 레코드 아님: %T", env.Record)
			}
			if env.Format != verdictDetailFormat || env.TaskID != strconv.FormatInt(f.taskID, 10) || env.Selection != (verdictDetailSelection{"verdict", strconv.FormatInt(refs.verdict, 10)}) ||
				*env.IDs.VerdictID != strconv.FormatInt(refs.verdict, 10) || *env.IDs.IssuanceID != strconv.FormatInt(refs.issuance, 10) ||
				*env.IDs.ManifestID != strconv.FormatInt(refs.manifest, 10) || env.IDs.CandidateID != nil ||
				env.SourceKind == nil || *env.SourceKind != "eval" || env.Availability != "recorded" || env.EvaluationStatus != "recorded" ||
				wire["source_kind"] != "eval" || wire["candidate_link"] != nil || wire["candidate"] != nil || wire["policy_source"] != nil || env.PolicySource != nil {
				t.Fatalf("envelope: %s", raw)
			}
			if (value == oracle.Confirmed) != (env.IDs.FindingID != nil && env.Finding != nil) ||
				value == oracle.Confirmed && (*env.IDs.FindingID != strconv.FormatInt(refs.finding, 10) || env.Finding.TriageStatus == nil || *env.Finding.TriageStatus != "pending") {
				t.Fatalf("finding 연결: %s", raw)
			}
			vuln := wire["record"].(map[string]any)["vuln_verdict"]
			if record.OracleValue != string(value) || record.Axis != "horizontal" || record.VulnClass != "IDOR" || record.CaseRef != "synthetic-case" ||
				record.ResourceRef != "synthetic:document:one" || record.AccessPolicyResult != access[value] ||
				(value == oracle.Allowed) != (vuln == nil) || value != oracle.Allowed && vuln != string(value) ||
				!slices.Equal(record.CanaryFields, []string{"note"}) || record.OwnerRef != "owner" || record.EvaluatedRef != "reader" ||
				record.OwnerStatus != "verified" || record.EvaluatedStatus != "verified" || record.PolicyVersion != "synthetic-kg-v1" ||
				record.ResetGeneration != "synthetic-reset-generation" || record.ModelTag != "synthetic-model" ||
				record.ObservedAt != "2026-11-03T12:00:20.000000Z" || record.CreatedAt != "2026-11-03T12:00:30.000000Z" ||
				record.PolicyValidFrom != "2026-11-03T11:00:20.000000Z" || record.PolicyValidUntil != "2026-11-03T13:00:20.000000Z" ||
				!record.HintMatchesCanonical || record.Sharing != "resolved_none" || record.Delegation != "resolved_none" ||
				record.TrafficEvidenceRefs == nil || len(record.TrafficEvidenceRefs) != 0 || len(record.ReasonCodes) == 0 {
				t.Fatalf("레코드 투영: %s", raw)
			}
			if env.RecordChecks == nil || env.RecordChecks.Status != "verified" || env.RecordChecks.BindingStatus != "current" ||
				!slices.Equal(env.RecordChecks.ReasonCodes, []string{verdictDetailKGSnapshotUnavailable}) {
				t.Fatalf("검증 상태: %s", raw)
			}
			if env.Presentation.Title == nil || *env.Presentation.Title != verdictDetailEvalTitle || env.Presentation.Severity == nil ||
				*env.Presentation.Severity != "high" || env.Presentation.Mock {
				t.Fatalf("표시: %s", raw)
			}
			if env.View.DisplayVerdict == nil || *env.View.DisplayVerdict != string(value) || env.View.ConfirmationEvidenceComplete == nil ||
				*env.View.ConfirmationEvidenceComplete != (value == oracle.Confirmed) || !slices.Equal(env.View.MissingRequirements, missing[value]) {
				t.Fatalf("view: %s", raw)
			}
			tr := env.Transmission
			if tr.State != "sent" || tr.Source != "registered_get" || tr.ScopeRole == nil || *tr.ScopeRole != "cross_read" || tr.Coverage != "complete" ||
				tr.AuditStatus != "unavailable" || tr.ReasonCodes == nil || len(tr.ReasonCodes) != 0 || len(tr.Steps) != 2 ||
				tr.Steps[0].Role != "owner_read" || tr.Steps[0].AttemptRef != "1" || tr.Steps[1].Role != "cross_read" || tr.Steps[1].AttemptRef != "2" {
				t.Fatalf("송신: %s", raw)
			}
			for _, step := range tr.Steps {
				if step.ForwardAttempted != nil || step.ResponseComplete != nil || step.RequestWritten == nil || !*step.RequestWritten ||
					step.ResponseReceived == nil || !*step.ResponseReceived {
					t.Fatalf("송신 단계: %s", raw)
				}
			}
			if value == oracle.Confirmed {
				byFinding, status, code := readFindingVerdictDetail(t.Context(), pg.DB, f.taskID, verdictDetailSelector{verdictDetailByFinding, refs.finding}, nil)
				if status != http.StatusOK || code != "" || byFinding.Selection.Kind != "finding" || byFinding.Record == nil ||
					*byFinding.IDs.VerdictID != *env.IDs.VerdictID || *byFinding.View.DisplayVerdict != "confirmed" {
					t.Fatalf("finding selector: %d %s %+v", status, code, byFinding)
				}
			}
		})
	}
}

func TestFindingVerdictDetailEvalIntegrity(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	f, refs := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	task := strconv.FormatInt(f.taskID, 10)
	zero := `decode(repeat('00',32),'hex')`
	cases := []struct {
		name      string
		byFinding bool
		sql       []string
		forge     *verdictDetailEvalForge
		ok        bool
	}{
		{name: "forge-noop", forge: &verdictDetailEvalForge{}, ok: true},
		{name: "payload-digest", sql: []string{`UPDATE e3_oracle_verdicts SET payload_digest=` + zero + ` WHERE id={v}`}},
		{name: "payload-rehashed", sql: []string{`UPDATE e3_oracle_verdicts SET payload_canonical=payload_canonical||'\x20'::bytea,payload_digest=sha256(payload_canonical||'\x20'::bytea) WHERE id={v}`}},
		{name: "manifest-digest", sql: []string{`UPDATE e3_evidence_manifests SET digest=` + zero + ` WHERE id={m}`}},
		{name: "manifest-rehashed", sql: []string{`UPDATE e3_evidence_manifests SET canonical=canonical||'\x20'::bytea,digest=sha256(canonical||'\x20'::bytea) WHERE id={m}`}},
		{name: "binding-bytes", sql: []string{`UPDATE e3_verdict_issuances SET binding_canonical=binding_canonical||'\x20'::bytea WHERE id={i}`}},
		{name: "input-digest", sql: []string{`UPDATE e3_verdict_issuances SET input_digest=` + zero + ` WHERE id={i}`}},
		{name: "record-version", sql: []string{`UPDATE e3_oracle_verdicts SET record_version=2 WHERE id={v}`}},
		{name: "verdict-row-missing", byFinding: true, sql: []string{`UPDATE e3_verdict_issuances SET verdict_id=verdict_id+1000000000 WHERE id={i}`}},
		{name: "manifest-row-missing", sql: []string{`UPDATE e3_verdict_issuances SET manifest_id=manifest_id+1000000000 WHERE id={i}`}},
		{name: "manifest-task", sql: []string{`UPDATE e3_evidence_manifests SET task_id=task_id+1000000000 WHERE id={m}`}},
		{name: "manifest-execution", sql: []string{`UPDATE e3_evidence_manifests SET execution_ref='other-run' WHERE id={m}`}},
		{name: "manifest-revision", sql: []string{`UPDATE e3_evidence_manifests SET evidence_revision=evidence_revision+1 WHERE id={m}`}},
		{name: "execution-both", sql: []string{`UPDATE e3_verdict_issuances SET execution_ref='other-run' WHERE id={i}`, `UPDATE e3_evidence_manifests SET execution_ref='other-run' WHERE id={m}`}},
		{name: "exploration", sql: []string{`UPDATE e3_verdict_issuances SET exploration_id=exploration_id+1 WHERE id={i}`}},
		{name: "intent", sql: []string{`UPDATE e3_verdict_issuances SET intent_id=intent_id+1 WHERE id={i}`}},
		{name: "mode", sql: []string{`UPDATE e3_verdict_issuances SET mode_version=mode_version+1 WHERE id={i}`}},
		{name: "case", sql: []string{`UPDATE e3_verdict_issuances SET case_ref='other-case' WHERE id={i}`}},
		{name: "resource", sql: []string{`UPDATE e3_verdict_issuances SET resource_ref='other:document:two' WHERE id={i}`}},
		{name: "eval-run", sql: []string{`UPDATE e3_verdict_issuances SET eval_run_id='run-other' WHERE id={i}`}},
		{name: "eval-run-empty", sql: []string{`UPDATE e3_verdict_issuances SET eval_run_id='' WHERE id={i}`}},
		{name: "model", sql: []string{`UPDATE e3_verdict_issuances SET model_tag='other-model' WHERE id={i}`}},
		{name: "reset", sql: []string{`UPDATE e3_verdict_issuances SET reset_generation='other-reset' WHERE id={i}`}},
		{name: "kg-ref", sql: []string{`UPDATE e3_verdict_issuances SET kg_snapshot_ref='other-kg' WHERE id={i}`}},
		{name: "kg-digest", sql: []string{`UPDATE e3_verdict_issuances SET kg_snapshot_digest=` + zero + ` WHERE id={i}`}},
		{name: "kg-policy", sql: []string{`UPDATE e3_verdict_issuances SET kg_policy_version='other-kg-v1' WHERE id={i}`}},
		{name: "generation", sql: []string{`UPDATE e3_verdict_issuances SET harness_generation_ref='other-generation' WHERE id={i}`}},
		{name: "instance", sql: []string{`UPDATE e3_verdict_issuances SET target_instance_ref='other-target' WHERE id={i}`}},
		{name: "oracle-column", sql: []string{`UPDATE e3_oracle_verdicts SET oracle_value='suspected' WHERE id={v}`}},
		{name: "forge-context-noop", forge: &verdictDetailEvalForge{context: [2]string{`"CaseRef":"synthetic-case"`, `"CaseRef":"synthetic-case"`}}, ok: true},
		{name: "forge-task-ref", forge: &verdictDetailEvalForge{context: [2]string{`"TaskRef":"` + task + `"`, `"TaskRef":"` + task + `0"`}}},
		{name: "forge-context-case", forge: &verdictDetailEvalForge{context: [2]string{`"CaseRef":"synthetic-case"`, `"CaseRef":"other-case"`}}},
		{name: "forge-context-run", forge: &verdictDetailEvalForge{context: [2]string{`"RunRef":"synthetic-e3-` + task + `"`, `"RunRef":"other-run"`}}},
		{name: "forge-context-resource", forge: &verdictDetailEvalForge{context: [2]string{`"ResourceRef":"synthetic:document:one"`, `"ResourceRef":"other:document:two"`}}},
		{name: "forge-context-kg-policy", forge: &verdictDetailEvalForge{context: [2]string{`"KGPolicyVersion":"synthetic-kg-v1"`, `"KGPolicyVersion":"other-kg-v1"`}}},
		{name: "forge-exploration", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, _ *e3record.Manifest) { s.ExplorationID++ }}},
		{name: "forge-revision", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, _ *e3record.Manifest) { s.EvidenceRevision++ }}},
		{name: "forge-record-case", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["case_ref"] = "other-case" }}},
		{name: "forge-record-resource", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["resource_ref"] = "other:document:two" }}},
		{name: "forge-source-model", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, _ *e3record.Manifest) { s.ModelTag = "other-model" }}},
		{name: "forge-record-model", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["model_tag"] = "other-model" }}},
		{name: "forge-source-reset", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, _ *e3record.Manifest) { s.ResetGeneration = "other-reset" }}},
		{name: "forge-record-reset", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["reset_generation"] = "other-reset" }}},
		{name: "forge-reset-empty", sql: []string{`UPDATE e3_verdict_issuances SET reset_generation='' WHERE id={i}`}, forge: &verdictDetailEvalForge{
			payload: func(p map[string]any) { p["reset_generation"] = "" }, source: func(s *e3record.Source, _ *e3record.Manifest) { s.ResetGeneration = "" }}},
		{name: "forge-record-policy", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["policy_version"] = "other-kg-v1" }}},
		{name: "forge-checkpoint-ref-empty", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, m *e3record.Manifest) { s.FinalCheckpointRef, m.CheckpointRef = "", "" }}},
		{name: "forge-checkpoint-digest-zero", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, m *e3record.Manifest) {
			s.FinalCheckpointDigest, m.CheckpointDigest = [32]byte{}, [32]byte{}
		}}},
		{name: "forge-record-owner", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["owner_ref"] = "someone" }}},
		{name: "forge-record-evaluated", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["evaluated_ref"] = "someone" }}},
		{name: "forge-severity", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, _ *e3record.Manifest) { s.Severity = "urgent" }}},
		{name: "forge-source-eval-run", forge: &verdictDetailEvalForge{source: func(s *e3record.Source, _ *e3record.Manifest) { s.EvalRunID = "run-other" }}},
		{name: "forge-record-axis", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["axis"] = "vertical" }}},
		{name: "forge-record-observed", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["observed_at"] = "2026-11-03T12:00:21.000000Z" }}},
		{name: "forge-payload-key-missing", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { delete(p, "traffic_evidence_refs") }}},
		{name: "forge-binding-key-missing", forge: &verdictDetailEvalForge{binding: func(b map[string]any) { delete(b, "EvalRunID") }}},
		{name: "forge-payload-unknown-key", forge: &verdictDetailEvalForge{payload: func(p map[string]any) { p["extra"] = "x" }}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel := verdictDetailSelector{verdictDetailByVerdict, refs.verdict}
			if c.byFinding {
				sel = verdictDetailSelector{verdictDetailByFinding, refs.finding}
			}
			var forge func(*sql.Tx)
			if c.forge != nil {
				forge = func(tx *sql.Tx) { forgeEvalVerdict(t, tx, refs.issuance, *c.forge) }
			}
			env, status, code := verdictDetailTamper(t, pg, refs, sel, nil, c.sql, forge)
			if c.ok {
				if status != http.StatusOK || env.Record == nil || env.RecordChecks.Status != "verified" {
					t.Fatalf("위조 도구가 정상 판정을 깨뜨림: %d %s %+v", status, code, env.RecordChecks)
				}
				return
			}
			verdictDetailWantIntegrityFailure(t, env, status, code, "eval")
			if env.Transmission.Source != "registered_get" || !slices.Equal(env.Transmission.ReasonCodes, []string{verdictDetailRecordIntegrityFailed}) {
				t.Fatalf("진위 실패 송신 표현: %+v", env.Transmission)
			}
		})
	}
	// 변조는 모두 롤백됐다. 같은 판정은 다시 검증된다.
	env, status, _ := readFindingVerdictDetail(t.Context(), pg.DB, refs.task, verdictDetailSelector{verdictDetailByVerdict, refs.verdict}, nil)
	if status != http.StatusOK || env.Record == nil {
		t.Fatal("롤백 뒤 판정 읽기 실패", status)
	}
}

func TestFindingVerdictDetailCanaryFieldLimit(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	for _, n := range []int{maxVerdictDetailCanaryFields, maxVerdictDetailCanaryFields + 1} {
		f := diagnosticE3FixtureFor(t, pg, oracle.Confirmed, true)
		fields := make([]string, n)
		for i := range fields {
			fields[i] = "field" + strconv.Itoa(i)
		}
		f.input.CanaryFields, f.source.CanaryFields = fields, fields
		source := f.source
		prepared, err := e3record.Prepare(t.Context(), f.input, func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil })
		if err != nil {
			t.Fatal(err)
		}
		result, err := pg.RecordControlledFinding(t.Context(), prepared, f.markers)
		if err != nil {
			t.Fatal(err)
		}
		_, status, code := readFindingVerdictDetail(t.Context(), pg.DB, f.taskID, verdictDetailSelector{verdictDetailByVerdict, result.VerdictID}, nil)
		want := http.StatusOK
		if n > maxVerdictDetailCanaryFields {
			want = http.StatusUnprocessableEntity
		}
		if status != want || status != http.StatusOK && code != verdictDetailIntegrityFailed {
			t.Fatalf("표식 칸 %d개: %d %s", n, status, code)
		}
	}
}

func TestFindingVerdictDetailProduct(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	refs := storeVerdictDetailProduct(t, pg, oracle.Confirmed)
	sel := verdictDetailSelector{verdictDetailByVerdict, refs.verdict}
	declaration := sha256.Sum256([]byte("synthetic declaration"))

	matched := &verdictDetailFakePolicy{result: verdictPolicySourceMatched}
	env, status, code := readFindingVerdictDetail(t.Context(), pg.DB, refs.task, sel, matched)
	wire, raw := verdictDetailWire(t, env)
	record, ok := env.Record.(verdictDetailProductRecord)
	if status != http.StatusOK || code != "" || !ok || env.SourceKind == nil || *env.SourceKind != "product" || wire["source_kind"] != "product" ||
		env.RecordChecks.Status != "verified" || len(env.RecordChecks.ReasonCodes) != 0 || env.RecordChecks.ReasonCodes == nil || env.RecordChecks.BindingStatus != "current" ||
		env.IDs.FindingID != nil || env.Finding != nil || *env.IDs.VerdictID != strconv.FormatInt(refs.verdict, 10) {
		t.Fatalf("제품 판정: %d %s %s", status, code, raw)
	}
	if record.OracleValue != "confirmed" || record.ResourceType != "document" || record.ResourceRef != "document:"+strings.Repeat("b", 64) ||
		record.CaseRef != "e3-product:document" || record.PolicyCheck != "yes" || record.IdentityCheck != "yes" || record.DataCheck != "yes" ||
		!slices.Equal(record.ReasonCodes, []string{string(oracle.ReasonPolicyDenyResolved)}) || record.ProductReasonCodes == nil || len(record.ProductReasonCodes) != 0 ||
		!slices.Equal(record.CandidateRefs, []string{"candidate-1"}) || record.EvidenceCount != 1 || record.ObservedAt != "2026-11-18T09:00:00.000000Z" ||
		record.ModelTag != "synthetic-model" {
		t.Fatalf("제품 레코드: %s", raw)
	}
	if env.PolicySource == nil || *env.PolicySource != (verdictDetailPolicySource{"product_declaration", "declaration-job-synthetic", hex.EncodeToString(declaration[:]), "target-revision-1"}) {
		t.Fatalf("정책 출처: %s", raw)
	}
	if len(matched.got) != 1 || matched.got[0] != (verdictPolicySourceRequest{SourceKind: "product", JobRef: "job-synthetic", RunRef: "product-run-detail",
		Ref: "declaration-job-synthetic", DigestSHA256: declaration, TargetApprovalRevision: "target-revision-1"}) {
		t.Fatalf("정책 동결본 대조 요청: %+v", matched.got)
	}
	if env.Presentation.Title == nil || *env.Presentation.Title != verdictDetailProductTitle || env.Presentation.Severity != nil || env.Presentation.Mock ||
		*env.View.DisplayVerdict != "confirmed" || env.View.ConfirmationEvidenceComplete == nil || !*env.View.ConfirmationEvidenceComplete ||
		len(env.View.MissingRequirements) != 0 {
		t.Fatalf("제품 view: %s", raw)
	}
	tr := env.Transmission
	if tr.State != "unknown" || tr.Source != "e2proxy" || tr.ScopeRole != nil || tr.Coverage != "unavailable" || tr.AuditStatus != "unavailable" ||
		!slices.Equal(tr.ReasonCodes, []string{verdictDetailJournalV3Unavailable}) || tr.Steps == nil || len(tr.Steps) != 0 {
		t.Fatalf("제품 송신: %s", raw)
	}
	for _, policy := range []policySourceReader{&verdictDetailFakePolicy{result: verdictPolicySourceUnavailable}, nil} {
		env, status, code = readFindingVerdictDetail(t.Context(), pg.DB, refs.task, sel, policy)
		if status != http.StatusOK || code != "" || env.Record == nil || env.RecordChecks.Status != "unavailable" ||
			!slices.Equal(env.RecordChecks.ReasonCodes, []string{verdictDetailPolicySourceMissing}) || *env.View.DisplayVerdict != "confirmed" ||
			env.View.ConfirmationEvidenceComplete != nil || env.PolicySource == nil || *env.SourceKind != "product" {
			t.Fatalf("동결본 미가용(%T): %d %s %+v", policy, status, code, env)
		}
	}
	env, status, code = readFindingVerdictDetail(t.Context(), pg.DB, refs.task, sel, &verdictDetailFakePolicy{result: verdictPolicySourceMismatch})
	verdictDetailWantIntegrityFailure(t, env, status, code, "product")
	if !slices.Equal(env.Transmission.ReasonCodes, []string{verdictDetailJournalV3Unavailable}) || env.Transmission.Source != "e2proxy" {
		t.Fatalf("정책 불일치 송신 표현: %+v", env.Transmission)
	}
	for _, result := range []verdictPolicySourceResult{verdictPolicySourceError, 0} {
		env, status, code = readFindingVerdictDetail(t.Context(), pg.DB, refs.task, sel, &verdictDetailFakePolicy{result: result})
		if status != http.StatusServiceUnavailable || code != verdictDetailUnavailable || env.Format != "" {
			t.Fatalf("정책 읽기 장애 %d: %d %s", result, status, code)
		}
	}
	// 확정이 아닌 제품 판정은 세 검사가 모두 yes여도 증거 충분성이 false이고, yes가 아닌 검사만 빠진 요건이다.
	undecided := storeVerdictDetailProduct(t, pg, oracle.Undecidable)
	env, status, _ = readFindingVerdictDetail(t.Context(), pg.DB, undecided.task, verdictDetailSelector{verdictDetailByVerdict, undecided.verdict}, matched)
	if status != http.StatusOK || env.Record == nil || *env.View.DisplayVerdict != "undecidable" || *env.View.ConfirmationEvidenceComplete ||
		len(env.View.MissingRequirements) != 0 {
		t.Fatalf("제품 undecidable(검사 모두 yes) view: %d %+v", status, env.View)
	}
	env, status, _ = verdictDetailTamper(t, pg, undecided, verdictDetailSelector{verdictDetailByVerdict, undecided.verdict}, matched, nil, func(tx *sql.Tx) {
		forgeProductVerdict(t, tx, undecided.issuance, func(p map[string]any) { p["data_check"] = "unknown" }, nil, nil)
	})
	if status != http.StatusOK || env.Record == nil || *env.View.DisplayVerdict != "undecidable" || *env.View.ConfirmationEvidenceComplete ||
		!slices.Equal(env.View.MissingRequirements, []string{"data_check"}) {
		t.Fatalf("제품 undecidable view: %d %+v", status, env.View)
	}
}

func TestFindingVerdictDetailProductIntegrity(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	refs := storeVerdictDetailProduct(t, pg, oracle.Confirmed)
	zero := `decode(repeat('00',32),'hex')`
	set := func(key string, value any) func(map[string]any) { return func(m map[string]any) { m[key] = value } }
	policySet := func(key string, value any) func(map[string]any) {
		return func(m map[string]any) {
			for _, field := range []string{"policy_source", "policy"} {
				if p, ok := m[field].(map[string]any); ok {
					p[key] = value
				}
			}
		}
	}
	many := func(n int, prefix string) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = prefix + strconv.Itoa(i)
		}
		return out
	}
	manyJSON := func(n int, prefix string) string {
		return string(verdictDetailEncode(t, many(n, prefix)))
	}
	cases := []struct {
		name                       string
		sql                        []string
		payload, binding, manifest func(map[string]any)
		forge                      bool
		status                     int
	}{
		{name: "forge-noop", forge: true, status: http.StatusOK},
		{name: "payload-digest", sql: []string{`UPDATE e3_oracle_verdicts SET payload_digest=` + zero + ` WHERE id={v}`}},
		{name: "payload-rehashed", sql: []string{`UPDATE e3_oracle_verdicts SET payload_canonical=payload_canonical||'\x20'::bytea,payload_digest=sha256(payload_canonical||'\x20'::bytea) WHERE id={v}`}},
		{name: "manifest-digest", sql: []string{`UPDATE e3_evidence_manifests SET digest=` + zero + ` WHERE id={m}`}},
		{name: "binding-bytes", sql: []string{`UPDATE e3_verdict_issuances SET binding_canonical=binding_canonical||'\x20'::bytea WHERE id={i}`}},
		{name: "input-digest", sql: []string{`UPDATE e3_verdict_issuances SET input_digest=` + zero + ` WHERE id={i}`}},
		{name: "record-version", sql: []string{`UPDATE e3_oracle_verdicts SET record_version=1 WHERE id={v}`}},
		{name: "manifest-revision", sql: []string{`UPDATE e3_evidence_manifests SET evidence_revision=2 WHERE id={m}`}},
		{name: "manifest-task", sql: []string{`UPDATE e3_evidence_manifests SET task_id=task_id+1000000000 WHERE id={m}`}},
		{name: "manifest-execution", sql: []string{`UPDATE e3_evidence_manifests SET execution_ref='other-run' WHERE id={m}`}},
		{name: "execution-both", sql: []string{`UPDATE e3_verdict_issuances SET execution_ref='other-run' WHERE id={i}`, `UPDATE e3_evidence_manifests SET execution_ref='other-run' WHERE id={m}`}},
		{name: "exploration", sql: []string{`UPDATE e3_verdict_issuances SET exploration_id=exploration_id+1 WHERE id={i}`}},
		{name: "case", sql: []string{`UPDATE e3_verdict_issuances SET case_ref='e3-product:other' WHERE id={i}`}},
		{name: "resource", sql: []string{`UPDATE e3_verdict_issuances SET resource_ref='document:other' WHERE id={i}`}},
		{name: "model", sql: []string{`UPDATE e3_verdict_issuances SET model_tag='other-model' WHERE id={i}`}},
		{name: "reset", sql: []string{`UPDATE e3_verdict_issuances SET reset_generation='other-generation' WHERE id={i}`}},
		{name: "generation", sql: []string{`UPDATE e3_verdict_issuances SET harness_generation_ref='other-generation' WHERE id={i}`}},
		{name: "instance", sql: []string{`UPDATE e3_verdict_issuances SET target_instance_ref='other-instance' WHERE id={i}`}},
		{name: "policy-ref", sql: []string{`UPDATE e3_verdict_issuances SET policy_source_ref='other-declaration' WHERE id={i}`}},
		{name: "policy-digest", sql: []string{`UPDATE e3_verdict_issuances SET policy_source_digest=` + zero + ` WHERE id={i}`}},
		{name: "policy-revision", sql: []string{`UPDATE e3_verdict_issuances SET policy_target_approval_revision='target-revision-2' WHERE id={i}`}},
		{name: "product-reasons-column", sql: []string{`UPDATE e3_verdict_issuances SET product_reason_codes='["other_reason"]'::jsonb WHERE id={i}`}},
		{name: "candidates-column", sql: []string{`UPDATE e3_verdict_issuances SET candidate_refs='[]'::jsonb WHERE id={i}`}},
		{name: "oracle-column", sql: []string{`UPDATE e3_oracle_verdicts SET oracle_value='suspected' WHERE id={v}`}},
		{name: "forge-format", forge: true, payload: set("format", "e3-product-verdict/v0")},
		{name: "forge-source-kind", forge: true, payload: set("source_kind", "eval")},
		{name: "forge-manifest-format", forge: true, manifest: set("format", "e3-product-manifest/v0")},
		{name: "forge-binding-format", forge: true, binding: set("format", "e3-product-binding/v0")},
		{name: "forge-binding-task", forge: true, binding: set("task_id", refs.task+1)},
		{name: "forge-binding-run", forge: true, binding: set("run_ref", "other-run")},
		{name: "forge-manifest-run", forge: true, manifest: set("run_ref", "other-run")},
		{name: "forge-binding-job", forge: true, binding: set("job_ref", "bad ref")},
		{name: "forge-binding-e2-policy", forge: true, binding: set("e2_policy_ref", "bad ref")},
		// 옛 manifest 칸(봉인 뒤 저장 전제)은 v2에 없다. 남아 있으면 진위 실패다.
		{name: "forge-old-journal-ref", forge: true, manifest: set("journal_ref", "journal-synthetic")},
		{name: "forge-checkpoint-journal", forge: true, manifest: func(m map[string]any) { m["evidence_checkpoint"].(map[string]any)["journal_id"] = "bad ref" }},
		{name: "forge-checkpoint-sequence", forge: true, manifest: func(m map[string]any) { m["evidence_checkpoint"].(map[string]any)["sequence"] = 0 }},
		{name: "forge-checkpoint-mac", forge: true, manifest: func(m map[string]any) { m["evidence_checkpoint"].(map[string]any)["mac"] = "not-hex" }},
		{name: "forge-checkpoint-extra", forge: true, manifest: func(m map[string]any) { m["evidence_checkpoint"].(map[string]any)["scope"] = "x" }},
		{name: "forge-checkpoint-key-missing", forge: true, manifest: func(m map[string]any) { delete(m["evidence_checkpoint"].(map[string]any), "mac") }},
		{name: "forge-binding-generation", forge: true, binding: set("generation_ref", "other-generation")},
		{name: "forge-generation-empty", forge: true, binding: set("generation_ref", ""),
			sql: []string{`UPDATE e3_verdict_issuances SET reset_generation='',harness_generation_ref='' WHERE id={i}`}},
		{name: "forge-instance-empty", forge: true, binding: set("instance_ref", ""), sql: []string{`UPDATE e3_verdict_issuances SET target_instance_ref='' WHERE id={i}`}},
		{name: "forge-resource-type", forge: true, payload: func(p map[string]any) {
			p["resource_type"], p["case_ref"], p["resource_ref"] = "Doc", "e3-product:Doc", "Doc:x"
		}, sql: []string{`UPDATE e3_verdict_issuances SET case_ref='e3-product:Doc',resource_ref='Doc:x' WHERE id={i}`}},
		{name: "forge-case-prefix", forge: true, payload: set("case_ref", "e3-product:other"), sql: []string{`UPDATE e3_verdict_issuances SET case_ref='e3-product:other' WHERE id={i}`}},
		{name: "forge-resource-prefix", forge: true, payload: set("resource_ref", "other:x"), sql: []string{`UPDATE e3_verdict_issuances SET resource_ref='other:x' WHERE id={i}`}},
		{name: "forge-resource-ref", forge: true, payload: set("resource_ref", "document:bad ref"), sql: []string{`UPDATE e3_verdict_issuances SET resource_ref='document:bad ref' WHERE id={i}`}},
		{name: "forge-model", forge: true, payload: set("model_tag", "other-model")},
		{name: "forge-oracle", forge: true, payload: set("oracle_value", "suspected")},
		{name: "forge-binding-policy", forge: true, binding: func(b map[string]any) { b["policy"].(map[string]any)["ref"] = "other-declaration" }},
		{name: "forge-policy-kind", forge: true, payload: policySet("kind", "kg_snapshot"), binding: policySet("kind", "kg_snapshot")},
		{name: "forge-policy-ref", forge: true, payload: policySet("ref", "bad ref"), binding: policySet("ref", "bad ref"),
			sql: []string{`UPDATE e3_verdict_issuances SET policy_source_ref='bad ref' WHERE id={i}`}},
		{name: "forge-policy-revision", forge: true, payload: policySet("target_approval_revision", "bad rev"), binding: policySet("target_approval_revision", "bad rev"),
			sql: []string{`UPDATE e3_verdict_issuances SET policy_target_approval_revision='bad rev' WHERE id={i}`}},
		{name: "forge-null-product-reasons", forge: true, payload: set("product_reason_codes", nil)},
		{name: "forge-null-candidates", forge: true, payload: set("candidate_refs", nil), sql: []string{`UPDATE e3_verdict_issuances SET candidate_refs='[]'::jsonb WHERE id={i}`}},
		{name: "forge-null-oracle-reasons", forge: true, payload: set("oracle_reason_codes", nil)},
		{name: "forge-null-evidence", forge: true, payload: func(p map[string]any) { p["oracle_value"], p["evidence"] = "undecidable", nil },
			sql: []string{`UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`}},
		{name: "forge-observed", forge: true, payload: set("observed_at", "2026-11-18T09:00:00Z")},
		{name: "forge-check", forge: true, payload: func(p map[string]any) { p["oracle_value"], p["data_check"] = "undecidable", "maybe" },
			sql: []string{`UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`}},
		{name: "forge-observed-precision", forge: true, payload: set("observed_at", "2026-11-18T09:00:00.000Z")},
		{name: "forge-oracle-reason", forge: true, payload: set("oracle_reason_codes", []any{"made_up_reason"})},
		{name: "forge-product-reason", forge: true, payload: func(p map[string]any) {
			p["oracle_value"] = "undecidable"
			p["product_reason_codes"] = []any{"Bad-Code"}
		},
			sql: []string{`UPDATE e3_verdict_issuances SET product_reason_codes='["Bad-Code"]'::jsonb WHERE id={i}`, `UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`}},
		{name: "forge-candidate-ref", forge: true, payload: set("candidate_refs", []any{"bad ref"}), sql: []string{`UPDATE e3_verdict_issuances SET candidate_refs='["bad ref"]'::jsonb WHERE id={i}`}},
		{name: "forge-confirmed-data-unknown", forge: true, payload: set("data_check", "unknown")},
		{name: "forge-confirmed-no-evidence", forge: true, payload: set("evidence", []any{})},
		{name: "forge-confirmed-product-reason", forge: true, payload: set("product_reason_codes", []any{"product_identity_unverified"}),
			sql: []string{`UPDATE e3_verdict_issuances SET product_reason_codes='["product_identity_unverified"]'::jsonb WHERE id={i}`}},
		{name: "forge-candidates-at-limit", forge: true, status: http.StatusOK, payload: set("candidate_refs", many(maxVerdictDetailCandidateRefs, "candidate-")),
			sql: []string{`UPDATE e3_verdict_issuances SET candidate_refs='` + manyJSON(maxVerdictDetailCandidateRefs, "candidate-") + `'::jsonb WHERE id={i}`}},
		{name: "forge-candidates-over-limit", forge: true, status: http.StatusUnprocessableEntity, payload: set("candidate_refs", many(maxVerdictDetailCandidateRefs+1, "candidate-")),
			sql: []string{`UPDATE e3_verdict_issuances SET candidate_refs='` + manyJSON(maxVerdictDetailCandidateRefs+1, "candidate-") + `'::jsonb WHERE id={i}`}},
		{name: "forge-reasons-at-limit", forge: true, status: http.StatusOK, payload: func(p map[string]any) {
			p["oracle_value"], p["product_reason_codes"] = "undecidable", many(maxVerdictDetailReasons, "reason_")
		}, sql: []string{`UPDATE e3_verdict_issuances SET product_reason_codes='` + manyJSON(maxVerdictDetailReasons, "reason_") + `'::jsonb WHERE id={i}`,
			`UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`}},
		{name: "forge-reasons-over-limit", forge: true, status: http.StatusUnprocessableEntity, payload: func(p map[string]any) {
			p["oracle_value"], p["product_reason_codes"] = "undecidable", many(maxVerdictDetailReasons+1, "reason_")
		}, sql: []string{`UPDATE e3_verdict_issuances SET product_reason_codes='` + manyJSON(maxVerdictDetailReasons+1, "reason_") + `'::jsonb WHERE id={i}`,
			`UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`}},
		{name: "forge-payload-key-missing", forge: true, payload: func(p map[string]any) { delete(p, "evidence") }},
		{name: "forge-binding-key-missing", forge: true, binding: func(b map[string]any) { delete(b, "e2_policy_ref") }},
		{name: "forge-manifest-key-missing", forge: true, manifest: func(m map[string]any) { delete(m, "evidence_checkpoint") }},
		// 저장 검사처럼 읽기도 겹친 사유·후보를 받지 않는다(그 행만 진위 실패).
		{name: "forge-duplicate-oracle-reason", forge: true, payload: func(p map[string]any) {
			p["oracle_reason_codes"] = []any{"policy_deny_resolved", "policy_deny_resolved"}
		}},
		{name: "forge-duplicate-candidate", forge: true, payload: set("candidate_refs", []any{"candidate-1", "candidate-1"}),
			sql: []string{`UPDATE e3_verdict_issuances SET candidate_refs='["candidate-1","candidate-1"]'::jsonb WHERE id={i}`}},
		// 확정이 아닌 판정으로 바꿔 제품 사유 불변식이 아니라 중복 검사만 걸리게 한다.
		{name: "forge-duplicate-product-reason", forge: true, payload: func(p map[string]any) {
			p["oracle_value"], p["product_reason_codes"] = "undecidable", []any{"product_x", "product_x"}
		}, sql: []string{`UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`,
			`UPDATE e3_verdict_issuances SET product_reason_codes='["product_x","product_x"]'::jsonb WHERE id={i}`}},
		// 같은 위조에서 중복만 빼면 진위 실패가 아니다(위 사례가 중복 때문에 실패함을 고정).
		{name: "forge-single-product-reason", forge: true, status: http.StatusOK, payload: func(p map[string]any) {
			p["oracle_value"], p["product_reason_codes"] = "undecidable", []any{"product_x"}
		}, sql: []string{`UPDATE e3_oracle_verdicts SET oracle_value='undecidable' WHERE id={v}`,
			`UPDATE e3_verdict_issuances SET product_reason_codes='["product_x"]'::jsonb WHERE id={i}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var forge func(*sql.Tx)
			if c.forge {
				forge = func(tx *sql.Tx) { forgeProductVerdict(t, tx, refs.issuance, c.payload, c.binding, c.manifest) }
			}
			policy := &verdictDetailFakePolicy{result: verdictPolicySourceMatched}
			env, status, code := verdictDetailTamper(t, pg, refs, verdictDetailSelector{verdictDetailByVerdict, refs.verdict}, policy, c.sql, forge)
			switch c.status {
			case http.StatusOK:
				if status != http.StatusOK || env.Record == nil || env.RecordChecks.Status != "verified" || len(policy.got) != 1 {
					t.Fatalf("정상 위조 대조군 실패: %d %s %+v", status, code, env.RecordChecks)
				}
			case http.StatusUnprocessableEntity:
				if status != c.status || code != verdictDetailIntegrityFailed || env.Format != "" {
					t.Fatalf("배열 상한 초과: %d %s", status, code)
				}
			default:
				verdictDetailWantIntegrityFailure(t, env, status, code, "product")
				if len(policy.got) != 0 {
					t.Fatal("진위 실패 판정으로 정책 동결본을 읽음")
				}
			}
		})
	}
}

func TestFindingVerdictDetailLinks(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg

	// 일반 finding만 있고 E③ link 없음: no_link·unknown, 가짜 판정 없음.
	task, err := pg.CreateTask("synthetic plain finding", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var plain int64
	if err := pg.QueryRow(`INSERT INTO findings(task_id,name,severity,status) VALUES($1,'plain synthetic','low','in_progress') RETURNING id`, task.ID).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	env, status, code := readFindingVerdictDetail(t.Context(), pg.DB, task.ID, verdictDetailSelector{verdictDetailByFinding, plain}, nil)
	wire, raw := verdictDetailWire(t, env)
	if status != http.StatusOK || code != "" || env.Availability != "no_link" || env.EvaluationStatus != "unknown" || env.Record != nil ||
		env.RecordChecks != nil || env.SourceKind != nil || wire["source_kind"] != nil || wire["record"] != nil || wire["record_checks"] != nil ||
		env.IDs.VerdictID != nil || env.IDs.IssuanceID != nil || env.IDs.ManifestID != nil || *env.IDs.FindingID != strconv.FormatInt(plain, 10) ||
		env.Finding == nil || env.Finding.TriageStatus == nil || *env.Finding.TriageStatus != "in_progress" || env.View.DisplayVerdict != nil ||
		env.View.ConfirmationEvidenceComplete != nil || env.View.MissingRequirements == nil || env.Presentation.Title != nil ||
		env.Transmission.Source != "unbound" || env.Transmission.State != "unknown" || env.Transmission.ScopeRole != nil {
		t.Fatalf("no_link: %d %s %s", status, code, raw)
	}
	if _, err := pg.Exec(`UPDATE findings SET status='invented-status' WHERE id=$1`, plain); err != nil {
		t.Fatal(err)
	}
	if env, _, _ = readFindingVerdictDetail(t.Context(), pg.DB, task.ID, verdictDetailSelector{verdictDetailByFinding, plain}, nil); env.Finding.TriageStatus != nil {
		t.Fatal("모르는 triage 상태 원문을 내보냄")
	}

	// 증거 버전이 바뀐 finding은 historical·undecidable 표시, 원값은 보존.
	_, refs := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	if _, err := pg.Exec(`UPDATE findings SET evidence_version=evidence_version+1 WHERE id=$1`, refs.finding); err != nil {
		t.Fatal(err)
	}
	for _, sel := range []verdictDetailSelector{{verdictDetailByVerdict, refs.verdict}, {verdictDetailByFinding, refs.finding}} {
		env, status, _ = readFindingVerdictDetail(t.Context(), pg.DB, refs.task, sel, nil)
		record, _ := env.Record.(verdictDetailEvalRecord)
		if status != http.StatusOK || env.RecordChecks.BindingStatus != "historical" || env.RecordChecks.Status != "verified" ||
			*env.View.DisplayVerdict != "undecidable" || env.View.ConfirmationEvidenceComplete != nil || record.OracleValue != "confirmed" {
			t.Fatalf("historical(%s): %d %+v", sel.kind, status, env.RecordChecks)
		}
	}

	_, current := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	other := storeVerdictDetailProduct(t, pg, oracle.Undecidable)
	_, otherEval := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	cases := []struct {
		name string
		sel  verdictDetailSelector
		sql  []string
	}{
		{"confirmed-link-lost", verdictDetailSelector{verdictDetailByVerdict, current.verdict}, []string{`DELETE FROM e3_verdict_links WHERE issuance_id={i}`}},
		{"link-task", verdictDetailSelector{verdictDetailByVerdict, current.verdict}, []string{`UPDATE e3_verdict_links SET task_id=task_id+1000000000 WHERE issuance_id={i}`}},
		{"link-finding-other-task", verdictDetailSelector{verdictDetailByVerdict, current.verdict}, []string{`UPDATE findings SET task_id=` + strconv.FormatInt(otherEval.task, 10) + ` WHERE id={f}`}},
		{"link-finding-missing", verdictDetailSelector{verdictDetailByVerdict, current.verdict}, []string{`UPDATE e3_verdict_links SET finding_id=finding_id+1000000000 WHERE issuance_id={i}`}},
		{"link-issuance-other-task", verdictDetailSelector{verdictDetailByFinding, current.finding}, []string{`DELETE FROM e3_verdict_links WHERE finding_id=` + strconv.FormatInt(otherEval.finding, 10),
			`UPDATE e3_verdict_links SET issuance_id=` + strconv.FormatInt(otherEval.issuance, 10) + ` WHERE finding_id={f}`}},
		{"link-issuance-missing", verdictDetailSelector{verdictDetailByFinding, current.finding}, []string{`UPDATE e3_verdict_links SET issuance_id=issuance_id+1000000000 WHERE finding_id={f}`}},
		{"product-with-link", verdictDetailSelector{verdictDetailByVerdict, other.verdict}, []string{`DELETE FROM e3_verdict_links WHERE finding_id={f}`,
			`INSERT INTO e3_verdict_links(issuance_id,task_id,finding_id,purpose,evidence_version) VALUES(` + strconv.FormatInt(other.issuance, 10) + `,` + strconv.FormatInt(other.task, 10) + `,{f},'initial',1)`,
			`UPDATE findings SET task_id=` + strconv.FormatInt(other.task, 10) + ` WHERE id={f}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refs := current
			if c.sel.id == other.verdict {
				refs.task = other.task
			}
			env, status, code := verdictDetailTamper(t, pg, refs, c.sel, &verdictDetailFakePolicy{result: verdictPolicySourceMatched}, c.sql, nil)
			if status != http.StatusUnprocessableEntity || code != verdictDetailIntegrityFailed || env.Format != "" {
				t.Fatalf("결속 불일치가 422 아님: %d %s", status, code)
			}
		})
	}
	// 대조군: 같은 변조 도구로 아무것도 바꾸지 않으면 정상이다.
	if env, status, _ := verdictDetailTamper(t, pg, current, verdictDetailSelector{verdictDetailByFinding, current.finding}, nil, nil, nil); status != http.StatusOK || env.Record == nil {
		t.Fatal("결속 대조군", status)
	}
	if env, status, _ := verdictDetailTamper(t, pg, other, verdictDetailSelector{verdictDetailByVerdict, other.verdict}, &verdictDetailFakePolicy{result: verdictPolicySourceMatched}, nil, nil); status != http.StatusOK || env.Record == nil {
		t.Fatal("제품 대조군", status)
	}
}

func TestFindingVerdictDetailNotFoundIsUniform(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	_, mine := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	_, theirs := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	_, unlinked := storeVerdictDetailEval(t, pg, oracle.Suspected)
	type result struct {
		status int
		code   string
	}
	want := result{http.StatusNotFound, verdictDetailNotFound}
	for name, sel := range map[string]verdictDetailSelector{
		"other-task-verdict": {verdictDetailByVerdict, theirs.verdict}, "missing-verdict": {verdictDetailByVerdict, math.MaxInt64},
		"other-task-finding": {verdictDetailByFinding, theirs.finding}, "missing-finding": {verdictDetailByFinding, math.MaxInt64},
		"other-task-unlinked-verdict": {verdictDetailByVerdict, unlinked.verdict},
		"missing-candidate":           {verdictDetailByCandidate, math.MaxInt64},
	} {
		env, status, code := readFindingVerdictDetail(t.Context(), pg.DB, mine.task, sel, nil)
		if (result{status, code}) != want || env.Format != "" {
			t.Fatalf("%s: %d %s", name, status, code)
		}
	}
	// 삭제 표시된 작업은 없는 작업과 같다(통제 모드 작업은 삭제가 막혀 일반 작업으로 확인).
	removed, err := pg.CreateTask("synthetic removed task", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var finding int64
	if err := pg.QueryRow(`INSERT INTO findings(task_id,name,severity,status) VALUES($1,'removed synthetic','low','pending') RETURNING id`, removed.ID).Scan(&finding); err != nil {
		t.Fatal(err)
	}
	sel := verdictDetailSelector{verdictDetailByFinding, finding}
	if _, status, _ := readFindingVerdictDetail(t.Context(), pg.DB, removed.ID, sel, nil); status != http.StatusOK {
		t.Fatal("삭제 전 대조군", status)
	}
	if _, err := pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, removed.ID); err != nil {
		t.Fatal(err)
	}
	if _, status, code := readFindingVerdictDetail(t.Context(), pg.DB, removed.ID, sel, nil); (result{status, code}) != want {
		t.Fatalf("삭제된 작업: %d %s", status, code)
	}
	for _, sel := range []verdictDetailSelector{{verdictDetailByVerdict, 0}, {verdictDetailByCandidate, 0}, {"issuance", 1}} {
		if _, status, code := readFindingVerdictDetail(t.Context(), pg.DB, mine.task, sel, nil); status != http.StatusBadRequest || code != verdictDetailInvalidRequest {
			t.Fatalf("잘못된 selector %+v: %d %s", sel, status, code)
		}
	}
	if _, status, code := readFindingVerdictDetail(t.Context(), pg.DB, 0, verdictDetailSelector{verdictDetailByVerdict, 1}, nil); status != http.StatusBadRequest || code != verdictDetailInvalidRequest {
		t.Fatalf("작업 0: %d %s", status, code)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, status, code := readFindingVerdictDetail(ctx, pg.DB, mine.task, verdictDetailSelector{verdictDetailByVerdict, mine.verdict}, nil); status != http.StatusServiceUnavailable || code != verdictDetailUnavailable {
		t.Fatalf("읽기 장애가 503 아님: %d %s", status, code)
	}
}

func TestFindingVerdictDetailReadOnlyAndNoSecrets(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	_, eval := storeVerdictDetailEval(t, pg, oracle.Confirmed)
	_, unlinked := storeVerdictDetailEval(t, pg, oracle.Undecidable)
	product := storeVerdictDetailProduct(t, pg, oracle.Confirmed)
	// 이 시험이 만든 작업으로 범위를 좁혀 다른 시험의 동시 쓰기와 섞이지 않게 센다.
	tasks := []int64{eval.task, unlinked.task, product.task}
	counts := func() [7]int64 {
		var out [7]int64
		if err := pg.QueryRow(`SELECT (SELECT count(*) FROM e3_evidence_manifests WHERE task_id=ANY($1)),
 (SELECT count(*) FROM e3_oracle_verdicts v JOIN e3_verdict_issuances i ON i.verdict_id=v.id WHERE i.task_id=ANY($1)),
 (SELECT count(*) FROM e3_verdict_issuances WHERE task_id=ANY($1)),(SELECT count(*) FROM e3_verdict_links WHERE task_id=ANY($1)),
 (SELECT count(*) FROM findings WHERE task_id=ANY($1)),
 (SELECT count(*) FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=ANY($1)),
 (SELECT count(*) FROM tasks WHERE id=ANY($1) AND deleted_at IS NULL)`, tasks).
			Scan(&out[0], &out[1], &out[2], &out[3], &out[4], &out[5], &out[6]); err != nil {
			t.Fatal(err)
		}
		if out[1] == 0 || out[2] == 0 || out[3] == 0 || out[4] == 0 {
			t.Fatalf("행 수 시험이 공허함: %v", out)
		}
		return out
	}
	before := counts()
	// 내부 참조 값은 JSON 문자열 그대로(따옴표 포함) 찾는다. 공개 정책 참조 "declaration-job-synthetic" 안의 부분 문자열은 노출이 아니다.
	forbidden := []string{`"session-a"`, `"session-b"`, "SessionRef", "Handle", "payload_canonical", "binding_canonical", `"synthetic-checkpoint"`, `"synthetic-read"`,
		`"owner-capture"`, `"cross-capture"`, `"synthetic-fixture-generation"`, `"synthetic-target"`, `"synthetic-kg-snapshot"`, `"job-synthetic"`, `"e2-policy-synthetic"`,
		`"journal-synthetic"`, `"call-1"`, `"attempt-1"`, "e3-declaration", "runs/", "/home", "/tmp", "password", "cookie", "token", "Authorization"}
	reads := []struct {
		task int64
		sel  verdictDetailSelector
	}{{eval.task, verdictDetailSelector{verdictDetailByVerdict, eval.verdict}}, {eval.task, verdictDetailSelector{verdictDetailByFinding, eval.finding}},
		{unlinked.task, verdictDetailSelector{verdictDetailByVerdict, unlinked.verdict}}, {product.task, verdictDetailSelector{verdictDetailByVerdict, product.verdict}}}
	for _, read := range reads {
		env, status, code := readFindingVerdictDetail(t.Context(), pg.DB, read.task, read.sel, &verdictDetailFakePolicy{result: verdictPolicySourceMatched})
		if status != http.StatusOK || env.Record == nil {
			t.Fatalf("읽기 실패 %+v: %d %s", read.sel, status, code)
		}
		_, raw := verdictDetailWire(t, env)
		for _, word := range forbidden {
			if strings.Contains(strings.ToLower(raw), strings.ToLower(word)) {
				t.Fatalf("응답에 %q 노출: %s", word, raw)
			}
		}
	}
	if after := counts(); after != before {
		t.Fatalf("조회가 행을 바꿈: %v → %v", before, after)
	}
}

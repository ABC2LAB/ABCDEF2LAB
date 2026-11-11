package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/remediation"
	"sort"
	"strconv"
	"time"
)

// 해시를 확인한 manifest의 독립 결속을 재검사한다. 본문·계정 값은 내보내지 않는다.
func diagnosticE3Context(ctx context.Context, q DiagnosticRemediationQueryer, in *remediation.Input, task int64, entry string, raw []byte, value string) error {
	kind, id, err := diagnosticEntryID(entry)
	if err != nil {
		return err
	}
	var binding, kgDigest []byte
	var run, caseRef, resource, policy, reset, snapshot, generation, instance, manifestRun string
	var intent, exploration, mode, revision int64
	err = q.QueryRowContext(ctx, `SELECT i.binding_canonical,i.execution_ref,i.case_ref,i.intent_id,i.exploration_id,i.mode_version,i.resource_ref,i.kg_policy_version,i.reset_generation,i.kg_snapshot_ref,i.kg_snapshot_digest,i.harness_generation_ref,i.target_instance_ref,m.execution_ref,m.evidence_revision
 FROM e3_verdict_issuances i JOIN tasks t ON t.id=i.task_id AND t.exploration_id=i.exploration_id AND t.deleted_at IS NULL JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=i.task_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id AND l.task_id=i.task_id
 WHERE i.task_id=$1 AND (($2='finding' AND l.finding_id=$3) OR ($2='verdict' AND i.id=$3 AND l.id IS NULL)) AND octet_length(i.binding_canonical)<=$4`, task, kind, id, e3record.MaxCanonicalBytes).Scan(&binding, &run, &caseRef, &intent, &exploration, &mode, &resource, &policy, &reset, &snapshot, &kgDigest, &generation, &instance, &manifestRun, &revision)
	if err != nil {
		return err
	}
	var source e3record.Source
	if json.Unmarshal(binding, &source) != nil {
		return ErrDiagnosticRemediation
	}
	expected := source.Expected
	if expected.Scope.TaskRef != strconv.FormatInt(task, 10) || expected.Scope.RunRef != run || run != manifestRun || expected.Scope.CaseRef != caseRef ||
		expected.IntentID != intent || source.ExplorationID != exploration || mode <= 0 || source.ModeVersion != uint64(mode) || expected.ResourceRef != resource || expected.KGPolicyVersion != policy ||
		source.ResetGeneration != reset || source.KGSnapshotRef != snapshot || !bytes.Equal(source.KGSnapshotDigest[:], kgDigest) || source.GenerationRef != generation || source.InstanceRef != instance || revision <= 0 || source.EvidenceRevision != uint64(revision) {
		return ErrDiagnosticRemediation
	}
	m, err := e3record.DecodeBoundManifest(raw, source, oracle.VerdictValue(value))
	if err != nil {
		return ErrDiagnosticRemediation
	}
	observations := []remediation.ContextObservation{}
	for _, record := range []struct {
		title, id string
		o         evidencebind.Observation
	}{{"소유 계정의 실제 조회", "owner-read", m.Evidence.Owner}, {"평가 계정의 실제 교차 조회", "cross-read", m.Evidence.Cross}} {
		r := record.o.Receipt
		observations = append(observations, remediation.ContextObservation{Source: "observed", Method: r.Method, Observation: fmt.Sprintf("%s: 요청 기록 %t, 응답 기록 %t, HTTP %d, 송신 결과 %s", record.title, r.RequestWritten, r.ResponseReceived, r.StatusCode, r.Reason), EvidenceRef: record.o.Evidence.ID, ObservedAt: record.o.ObservedAt.UTC().Format(time.RFC3339Nano), RunID: expected.Scope.RunRef, StepID: record.id})
	}
	// 실제 관측 시각 순서만 사용한다. 시간의 근접성으로 회차나 의존 관계를 추론하지 않는다.
	sort.SliceStable(observations, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, observations[i].ObservedAt)
		b, _ := time.Parse(time.RFC3339Nano, observations[j].ObservedAt)
		return a.Before(b)
	})
	observations = append(observations, remediation.ContextObservation{Source: "observed", Observation: "저장된 E③ 구조화 판정: " + value, EvidenceRef: in.FindingID, ObservedAt: m.Evidence.Result.ObservedAt.UTC().Format(time.RFC3339Nano), RunID: expected.Scope.RunRef, StepID: "e3-comparison", DependsOn: []string{"owner-read", "cross-read"}})
	in.Context.Observations = append(in.Context.Observations, observations...)
	return nil
}

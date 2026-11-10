package e3record

import (
	"context"
	"crypto/sha256"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
)

func Prepare(ctx context.Context, in Input, reader SourceReader) (Prepared, error) {
	in.CanaryFields = append([]string(nil), in.CanaryFields...)
	in.Evidence.Result.Result.Verdict.ReasonCodes = append([]oracle.JudgeReason(nil), in.Evidence.Result.Result.Verdict.ReasonCodes...)

	if ctx == nil || ctx.Err() != nil || reader == nil {
		return Prepared{}, ErrSource
	}
	actualEvidenceDigest, digestErr := EvidenceDigest(in.Evidence)
	if digestErr != nil {
		return Prepared{}, ErrInvalid
	}
	source, err := reader(ctx, in.Expected)
	if err != nil || ctx.Err() != nil {
		return Prepared{}, ErrSource
	}
	source = cloneSource(source)
	if !source.Completed || !source.Finalized {
		return Prepared{}, ErrIncomplete
	}
	if source.RecordCreatedAt.IsZero() || source.RecordCreatedAt.UTC().Year() < 1 || source.RecordCreatedAt.UTC().Year() > 9999 || source.RecordCreatedAt.Before(in.Evidence.Cross.Receipt.At) || source.Expected != in.Expected || source.EvidenceDigest == ([32]byte{}) || source.EvidenceDigest != actualEvidenceDigest || source.ExplorationID <= 0 ||
		source.ModeVersion == 0 || source.ModeVersion > math.MaxInt64 ||
		source.EvidenceRevision == 0 || source.EvidenceRevision > math.MaxInt64 ||
		source.FinalCheckpointScope != in.Expected.Scope || source.FinalCheckpointSequence == 0 ||
		source.FinalCheckpointSequence > math.MaxInt64 ||
		source.KGSnapshotDigest == ([32]byte{}) || source.FinalCheckpointDigest == ([32]byte{}) {
		return Prepared{}, ErrInvalid
	}
	separator := strings.LastIndex(in.Expected.ResourceRef, ":")
	if separator <= 0 || separator == len(in.Expected.ResourceRef)-1 {
		return Prepared{}, ErrInvalid
	}
	taskID, err := strconv.ParseInt(in.Expected.Scope.TaskRef, 10, 64)
	if err != nil || taskID <= 0 || strconv.FormatInt(taskID, 10) != in.Expected.Scope.TaskRef {
		return Prepared{}, ErrInvalid
	}
	for _, ref := range []string{source.KGSnapshotRef, source.GenerationRef, source.InstanceRef,
		source.ResetGeneration, source.FinalCheckpointRef} {
		if !validRef(ref) {
			return Prepared{}, ErrInvalid
		}
	}
	if source.EvalRunID != "" && !validRef(source.EvalRunID) {
		return Prepared{}, ErrInvalid
	}
	if !safeText(source.ModelTag, 128) || !validSeverity(source.Severity) ||
		!samePolicy(source.Policy, in.Evidence.Policy.Assertion) ||
		!sameFields(source.CanaryFields, in.CanaryFields) {
		return Prepared{}, ErrInvalid
	}
	for _, id := range source.TrafficEvidenceIDs {
		if id <= 0 {
			return Prepared{}, ErrInvalid
		}
	}
	checked := evidencebind.Check(in.Expected, in.Evidence)
	if !checked.BindingValid {
		return Prepared{}, ErrInvalid
	}
	verdict := checked.Recomputed
	if verdict.Value == oracle.Confirmed && len(in.CanaryFields) == 0 {
		return Prepared{}, ErrInvalid
	}

	policy := source.Policy
	// A microsecond representation must not silently change the historical
	// oracle result at a nanosecond validity-window boundary.
	policy.ValidFrom = microTime(policy.ValidFrom)
	policy.ValidUntil = microTime(policy.ValidUntil)
	normalized := oracle.Evaluate(oracle.CaseInput{Policy: policy,
		Subjects: oracle.SubjectContext{OwnerStatus: in.Expected.Owner.Status,
			EvaluatedStatus: in.Expected.Evaluated.Status, OwnerRef: in.Expected.Owner.Label,
			EvaluatedRef: in.Expected.Evaluated.Label}, Facts: in.Evidence.Result.Result.Facts},
		microTime(in.Evidence.Result.ObservedAt))
	if !sameVerdict(normalized, verdict) {
		return Prepared{}, ErrPrecision
	}
	var vuln any = string(verdict.Value)
	access := AccessPolicyViolationCandidate
	if verdict.Value == oracle.Allowed {
		vuln, access = nil, AccessPolicyNoViolationObserved
	}
	if verdict.Value == oracle.Undecidable {
		access = AccessPolicyUndecidable
	}
	facts := in.Evidence.Result.Result.Facts
	payload := map[string]any{
		"case_ref": in.Expected.Scope.CaseRef, "axis": "horizontal",
		"oracle_value": string(verdict.Value), "vuln_verdict": vuln, "access_policy_result": access,
		"policy_check": string(verdict.PolicyCheck), "identity_check": string(verdict.IdentityCheck),
		"data_check": string(verdict.DataCheck), "reason_codes": verdict.ReasonCodes,
		"same_resource": string(facts.SameResource), "owner_normal_data": string(facts.OwnerNormalData),
		"cross_normal_data": string(facts.CrossNormalData), "nontrivial_data_match": string(facts.NontrivialDataMatch),
		"reflection_excluded": string(facts.ReflectionExcluded), "template_excluded": string(facts.TemplateExcluded),
		"canary_fields":   append([]string{}, in.CanaryFields...),
		"expected_access": string(policy.ExpectedAccess), "sharing": string(policy.Sharing),
		"delegation": string(policy.Delegation), "policy_version": in.Expected.KGPolicyVersion,
		"policy_valid_from": canonicalTime(policy.ValidFrom), "policy_valid_until": canonicalTime(policy.ValidUntil),
		"hint_matches_canonical": policy.HintMatchesCanonical,
		"observed_at":            canonicalTime(in.Evidence.Result.ObservedAt), "reset_generation": source.ResetGeneration,
		"owner_ref": in.Expected.Owner.Label, "evaluated_ref": in.Expected.Evaluated.Label,
		"owner_status": string(in.Expected.Owner.Status), "evaluated_status": string(in.Expected.Evaluated.Status),
		"traffic_evidence_refs": append([]int64{}, source.TrafficEvidenceIDs...),
		"vuln_class":            "IDOR", "resource_ref": in.Expected.ResourceRef, "model_tag": source.ModelTag,
		"created_at": canonicalTime(source.RecordCreatedAt),
	}
	canonicalPayload, err := canonical(payload)
	if err != nil {
		return Prepared{}, err
	}
	manifest, err := canonical(struct {
		Version                                   int64
		EvidenceDigest                            [32]byte
		Evidence                                  evidencebind.Input
		KGSnapshotRef                             string
		KGSnapshotDigest                          [32]byte
		GenerationRef, InstanceRef, CheckpointRef string
		CheckpointDigest                          [32]byte
	}{1, actualEvidenceDigest, in.Evidence, source.KGSnapshotRef, source.KGSnapshotDigest, source.GenerationRef,
		source.InstanceRef, source.FinalCheckpointRef, source.FinalCheckpointDigest})
	if err != nil {
		return Prepared{}, err
	}
	binding, err := canonical(source)
	if err != nil {
		return Prepared{}, err
	}
	key, err := canonical([]any{int64(1), taskID, in.Expected.Scope.RunRef,
		in.Expected.Scope.CaseRef, int64(source.EvidenceRevision), "initial", int64(0)})
	if err != nil {
		return Prepared{}, err
	}
	s := Snapshot{TaskID: taskID, Source: source, Value: verdict.Value, Canonical: canonicalPayload,
		Manifest: manifest, Binding: binding, PayloadDigest: sha256.Sum256(canonicalPayload),
		ManifestDigest: sha256.Sum256(manifest), OperationKey: sha256.Sum256(key)}
	digestInput, err := canonical(struct{ Payload, Manifest, Binding [32]byte }{
		s.PayloadDigest, s.ManifestDigest, sha256.Sum256(binding)})
	if err != nil {
		return Prepared{}, err
	}
	s.InputDigest = sha256.Sum256(digestInput)
	return Prepared{true, s}, nil
}

func microTime(t time.Time) time.Time  { return t.Round(0).UTC().Truncate(time.Microsecond) }
func canonicalTime(t time.Time) string { return microTime(t).Format("2006-01-02T15:04:05.000000Z") }

func sameVerdict(a, b oracle.OracleVerdict) bool {
	return a.Value == b.Value && a.PolicyCheck == b.PolicyCheck && a.IdentityCheck == b.IdentityCheck &&
		a.DataCheck == b.DataCheck && slices.Equal(a.ReasonCodes, b.ReasonCodes)
}
func samePolicy(a, b oracle.PolicyAssertion) bool {
	return a.ExpectedAccess == b.ExpectedAccess && a.Sharing == b.Sharing && a.Delegation == b.Delegation &&
		a.HasRegisteredSource == b.HasRegisteredSource && a.HintMatchesCanonical == b.HintMatchesCanonical &&
		a.ValidFrom.Equal(b.ValidFrom) && a.ValidUntil.Equal(b.ValidUntil)
}
func sameFields(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, field := range a {
		if !safeText(field, 256) || seen[field] {
			return false
		}
		seen[field] = true
	}
	for _, field := range b {
		if !seen[field] {
			return false
		}
		delete(seen, field)
	}
	return len(seen) == 0
}
func safeText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
func validRef(s string) bool {
	if len(s) < 1 || len(s) > 128 || strings.Contains(s, "://") {
		return false
	}
	for i, c := range []byte(s) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("_.:/-", rune(c))) {
			return false
		}
	}
	return true
}
func validSeverity(s string) bool {
	return s == "critical" || s == "high" || s == "medium" || s == "low"
}

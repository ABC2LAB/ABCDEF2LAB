package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
	"github.com/golang-jwt/jwt/v5"
)

type accessMapSignals struct {
	PolicyCheck         oracle.CheckState `json:"policy_check"`
	IdentityCheck       oracle.CheckState `json:"identity_check"`
	DataCheck           oracle.CheckState `json:"data_check"`
	SameResource        oracle.CheckState `json:"same_resource"`
	OwnerNormalData     oracle.CheckState `json:"owner_normal_data"`
	CrossNormalData     oracle.CheckState `json:"cross_normal_data"`
	NontrivialDataMatch oracle.CheckState `json:"nontrivial_data_match"`
	ReflectionExcluded  oracle.CheckState `json:"reflection_excluded"`
	TemplateExcluded    oracle.CheckState `json:"template_excluded"`
}

type accessMapCell struct {
	Principal     string               `json:"principal"`
	OwnerRef      string               `json:"owner_ref"`
	ResourceRef   string               `json:"resource_ref"`
	ResourceLabel string               `json:"resource_label"`
	State         string               `json:"state"`
	OracleValue   *oracle.VerdictValue `json:"oracle_value"`
	Applicable    *bool                `json:"applicable"`
	RecordID      *int64               `json:"record_id"`
	CaseRef       *string              `json:"case_ref"`
	ObservedAt    *time.Time           `json:"observed_at"`
	accessMapSignals
	ReasonCodes     []oracle.JudgeReason `json:"reason_codes"`
	CanaryFields    []string             `json:"canary_fields"`
	ProjectionError *string              `json:"projection_error"`
}

type accessMapResponse struct {
	TaskID          int64           `json:"task_id"`
	RunRef          string          `json:"run_ref"`
	ResetGeneration string          `json:"reset_generation"`
	Axis            string          `json:"axis"`
	PolicyVersion   string          `json:"policy_version"`
	Cells           []accessMapCell `json:"cells"`
}

// 저장 payload 전체를 응답하지 않고 합의한 필드만 투영한다.
type accessMapRecord struct {
	CaseRef            string               `json:"case_ref"`
	Axis               string               `json:"axis"`
	OracleValue        oracle.VerdictValue  `json:"oracle_value"`
	VulnVerdict        *oracle.VerdictValue `json:"vuln_verdict"`
	AccessPolicyResult string               `json:"access_policy_result"`
	accessMapSignals
	ReasonCodes          []oracle.JudgeReason   `json:"reason_codes"`
	CanaryFields         []string               `json:"canary_fields"`
	ExpectedAccess       oracle.ExpectedAccess  `json:"expected_access"`
	Sharing              oracle.ResolutionState `json:"sharing"`
	Delegation           oracle.ResolutionState `json:"delegation"`
	PolicyVersion        string                 `json:"policy_version"`
	PolicyValidFrom      time.Time              `json:"policy_valid_from"`
	PolicyValidUntil     time.Time              `json:"policy_valid_until"`
	HintMatchesCanonical bool                   `json:"hint_matches_canonical"`
	ObservedAt           time.Time              `json:"observed_at"`
	ResetGeneration      string                 `json:"reset_generation"`
	OwnerRef             string                 `json:"owner_ref"`
	EvaluatedRef         string                 `json:"evaluated_ref"`
	OwnerStatus          oracle.SubjectStatus   `json:"owner_status"`
	EvaluatedStatus      oracle.SubjectStatus   `json:"evaluated_status"`
	TrafficEvidenceRefs  []int64                `json:"traffic_evidence_refs"`
	VulnClass            string                 `json:"vuln_class"`
	ResourceRef          string                 `json:"resource_ref"`
	ModelTag             string                 `json:"model_tag"`
	CreatedAt            time.Time              `json:"created_at"`
}

type accessMapStoredRecord struct {
	id     int64
	record accessMapRecord
	valid  bool
}

type accessMapManifest struct {
	Version                                   int64
	EvidenceDigest                            [32]byte
	Evidence                                  evidencebind.Input
	KGSnapshotRef                             string
	KGSnapshotDigest                          [32]byte
	GenerationRef, InstanceRef, CheckpointRef string
	CheckpointDigest                          [32]byte
}

func accessMapRequestTask(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return 0, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return 0, false
	}
	raw, ok := strings.CutSuffix(tail, "/access-map")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}

func (s *Server) accessMap(w http.ResponseWriter, r *http.Request) {
	var boundTask int64
	if s.firstDiagnosis {
		s.firstModelMu.RLock()
		if s.firstModel != nil {
			boundTask = s.firstModel.TaskNumber
		}
		s.firstModelMu.RUnlock()
		if boundTask <= 0 {
			writeErr(w, http.StatusForbidden, "선택한 작업의 지도만 사용할 수 있습니다")
			return
		}
	}
	s.serveAccessMap(w, r, boundTask)
}

func (s *Server) serveAccessMap(w http.ResponseWriter, r *http.Request, boundTask int64) {
	w.Header().Set("Cache-Control", "no-store")
	// 보고서 전용 서버와 직접 핸들러 호출에도 동일한 Bearer 인증을 적용한다.
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) <= len("Bearer ") {
		writeErr(w, http.StatusUnauthorized, authErrUnauthorized)
		return
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, authErrDataSourceUnavailable)
		return
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(*jwt.Token) (any, error) { return s.signingKey(hash), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.Subject != "ARTEX" {
		writeErr(w, http.StatusUnauthorized, authErrTokenInvalid)
		return
	}
	task, ok := accessMapRequestTask(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "지도 조회에는 올바른 작업 ID만 사용할 수 있습니다")
		return
	}
	if boundTask > 0 && boundTask != task {
		writeErr(w, http.StatusForbidden, "선택한 작업의 지도만 사용할 수 있습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accessMapTimeout)
	defer cancel()
	result, err := readAccessMap(ctx, s.m.pg.DB, task)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, "작업을 찾을 수 없습니다")
	case errors.Is(err, errAccessMapScope):
		writeErr(w, http.StatusConflict, "현재 세대의 지도 조회 범위를 확인할 수 없습니다")
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, "저장된 접근통제 지도를 확인할 수 없습니다")
	default:
		writeJSON(w, http.StatusOK, result)
	}
}

func readAccessMap(ctx context.Context, pool *sql.DB, task int64) (accessMapResponse, error) {
	var out accessMapResponse
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, errAccessMapUnavailable
	}
	defer tx.Rollback()
	var exploration int64
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, task).Scan(&exploration); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='confirmed' AND origin=$2`, exploration, accessMapOrigin)
	if err != nil {
		return out, errAccessMapUnavailable
	}
	var b accessMapBinding
	count := 0
	for rows.Next() {
		var raw []byte
		count++
		if count > 1 || rows.Scan(&raw) != nil || len(raw) > maxAccessMapBytes || strictJSON(raw, &b) != nil {
			rows.Close()
			return out, errAccessMapScope
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, errAccessMapUnavailable
	}
	if count != 1 || !b.valid(task, exploration) {
		return out, errAccessMapScope
	}
	var mode uint64
	if tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM task_control_modes WHERE task_id=$1),0)`, task).Scan(&mode) != nil {
		return out, errAccessMapUnavailable
	}
	if mode != b.ModeVersion {
		return out, errAccessMapScope
	}
	records, err := readAccessMapRecords(ctx, tx, b)
	if err != nil {
		return out, err
	}
	out = projectAccessMap(b, records)
	if err := tx.Commit(); err != nil {
		return accessMapResponse{}, errAccessMapUnavailable
	}
	return out, nil
}

func (b accessMapBinding) valid(task, exploration int64) bool {
	if b.Version != 1 || !b.Ready || b.TaskID != task || b.ExplorationID != exploration || b.IntentID <= 0 || b.Registration == "" ||
		b.Scope.TaskRef != strconv.FormatInt(task, 10) || !safeRef.MatchString(b.Scope.RunRef) || !safeRef.MatchString(b.Scope.CaseRef) ||
		!safeRef.MatchString(b.Scope.ResetRef) || !safeRef.MatchString(b.Scope.PolicyRef) || !safeRef.MatchString(b.GenerationRef) || !safeRef.MatchString(b.InstanceRef) ||
		b.SnapshotDigest == ([32]byte{}) || b.PolicyVersion != "kg-fixture/v1:"+b.SnapshotRef+":"+hex.EncodeToString(b.SnapshotDigest[:]) ||
		len(b.PolicyVersion) > 128 || len(b.Principals) == 0 || len(b.Principals) > maxControlledSessions || len(b.Resources) == 0 || len(b.Resources) > maxControlledResources {
		return false
	}
	for _, ref := range []string{b.SnapshotRef, b.Scope.RunRef, b.Scope.CaseRef, b.Scope.ResetRef, b.Scope.PolicyRef, b.GenerationRef, b.InstanceRef} {
		if !safeRef.MatchString(ref) || strings.Contains(ref, "://") {
			return false
		}
	}
	if len(b.Attempts) > len(b.Principals)*len(b.Resources) {
		return false
	}
	seen := map[string]bool{}
	for _, p := range b.Principals {
		if !safeRef.MatchString(p) || strings.Contains(p, "://") || seen[p] {
			return false
		}
		seen[p] = true
	}
	seen = map[string]bool{}
	for _, r := range b.Resources {
		if !safeRef.MatchString(r.ResourceRef) || strings.Contains(r.ResourceRef, "://") || !accessMapHas(b.Principals, r.OwnerRef) || seen[r.ResourceRef] {
			return false
		}
		seen[r.ResourceRef] = true
		policies := map[string]bool{}
		for _, p := range r.Principals {
			if !accessMapHas(b.Principals, p) || policies[p] {
				return false
			}
			policies[p] = true
		}
	}
	for _, a := range b.Attempts {
		if !accessMapHas(b.Principals, a.Principal) || !b.hasResource(a.Resource) {
			return false
		}
	}
	return true
}

// 행 필터는 출처 증명이 아니다. 별도 발급 결속과 정본 payload를 모두 대조한다.
func readAccessMapRecords(ctx context.Context, tx *sql.Tx, b accessMapBinding) ([]accessMapStoredRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT v.id,v.oracle_value,v.payload_canonical,v.payload_digest,i.binding_canonical,
 i.exploration_id,i.intent_id,i.mode_version,i.case_ref,i.resource_ref,i.model_tag,i.reset_generation,
 i.kg_snapshot_ref,i.kg_policy_version,i.kg_snapshot_digest,i.harness_generation_ref,i.target_instance_ref,
 m.evidence_revision,m.canonical,m.digest
 FROM e3_verdict_issuances i LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=i.task_id
 WHERE i.task_id=$1 AND i.execution_ref=$2 LIMIT $3`, b.TaskID, b.Scope.RunRef, maxAccessMapRecords+1)
	if err != nil {
		return nil, errAccessMapUnavailable
	}
	defer rows.Close()
	out := []accessMapStoredRecord{}
	total := 0
	var envelopeModel string
	for rows.Next() {
		var stored accessMapStoredRecord
		var payload, digest, binding, kgDigest, manifest, manifestDigest []byte
		var value, caseRef, resource, model, reset, snapshot, policy, generation, instance string
		var exploration, intent, mode, revision int64
		if rows.Scan(&stored.id, &value, &payload, &digest, &binding, &exploration, &intent, &mode, &caseRef, &resource, &model, &reset, &snapshot, &policy, &kgDigest, &generation, &instance, &revision, &manifest, &manifestDigest) != nil {
			return nil, errAccessMapUnavailable
		}
		total += len(payload) + len(binding) + len(manifest)
		if len(out) >= maxAccessMapRecords || total > maxAccessMapBytes {
			return nil, errAccessMapUnavailable
		}
		var source e3record.Source
		var m accessMapManifest
		if len(payload) > e3record.MaxCanonicalBytes || len(binding) > e3record.MaxCanonicalBytes || len(manifest) > e3record.MaxCanonicalBytes ||
			strictJSON(payload, &stored.record) != nil || strictJSON(binding, &source) != nil || strictJSON(manifest, &m) != nil {
			return nil, errAccessMapUnavailable
		}
		r := stored.record
		if !b.hasResource(r.ResourceRef) || !accessMapHas(b.Principals, r.EvaluatedRef) {
			return nil, errAccessMapUnavailable
		}
		ph, mh := sha256.Sum256(payload), sha256.Sum256(manifest)
		if envelopeModel != "" && envelopeModel != model {
			return nil, errAccessMapUnavailable
		}
		envelopeModel = model
		stored.valid = stored.id > 0 && bytes.Equal(ph[:], digest) && bytes.Equal(mh[:], manifestDigest) &&
			exploration == b.ExplorationID && intent == b.IntentID && mode == int64(b.ModeVersion) &&
			caseRef == b.Scope.CaseRef && r.CaseRef == caseRef && resource == r.ResourceRef &&
			reset == b.Scope.ResetRef && r.ResetGeneration == reset && snapshot == b.SnapshotRef && policy == b.PolicyVersion && r.PolicyVersion == policy &&
			bytes.Equal(kgDigest, b.SnapshotDigest[:]) && generation == b.GenerationRef && instance == b.InstanceRef &&
			model != "" && model == source.ModelTag && r.ModelTag == model && value == string(r.OracleValue) &&
			source.Expected.Scope == b.Scope && source.Expected.IntentID == intent && source.ExplorationID == exploration && source.ModeVersion == uint64(mode) &&
			source.Expected.ResourceRef == resource && source.Expected.KGPolicyVersion == policy && source.Expected.Owner.Label == r.OwnerRef && source.Expected.Evaluated.Label == r.EvaluatedRef &&
			source.Policy.HasRegisteredSource == b.hasPolicy(resource, r.OwnerRef, r.EvaluatedRef) &&
			source.KGSnapshotRef == snapshot && source.KGSnapshotDigest == b.SnapshotDigest && source.GenerationRef == generation && source.InstanceRef == instance && source.ResetGeneration == reset &&
			source.Completed && source.Finalized && source.EvidenceRevision == uint64(revision) && revision > 0 && r.valid(source) && m.matches(source, r)
		out = append(out, stored)
	}
	if rows.Err() != nil {
		return nil, errAccessMapUnavailable
	}
	return out, nil
}

func (m accessMapManifest) matches(s e3record.Source, r accessMapRecord) bool {
	e := m.Evidence
	digest, err := e3record.EvidenceDigest(e)
	if err != nil || m.Version != 1 || digest != m.EvidenceDigest || digest != s.EvidenceDigest ||
		m.KGSnapshotRef != s.KGSnapshotRef || m.KGSnapshotDigest != s.KGSnapshotDigest || m.GenerationRef != s.GenerationRef || m.InstanceRef != s.InstanceRef ||
		m.CheckpointRef != s.FinalCheckpointRef || m.CheckpointDigest != s.FinalCheckpointDigest || s.FinalCheckpointScope != s.Expected.Scope || s.FinalCheckpointSequence == 0 ||
		e.Policy.Context != s.Expected || e.Owner.Context != s.Expected || e.Cross.Context != s.Expected || e.Result.Context != s.Expected || e.Policy.Assertion != s.Policy ||
		!r.ObservedAt.Equal(e.Result.ObservedAt) || !r.CreatedAt.Equal(s.RecordCreatedAt) || !slices.Equal(r.TrafficEvidenceRefs, s.TrafficEvidenceIDs) {
		return false
	}
	v, f := e.Result.Result.Verdict, e.Result.Result.Facts
	return r.OracleValue == v.Value && r.PolicyCheck == v.PolicyCheck && r.IdentityCheck == v.IdentityCheck && r.DataCheck == v.DataCheck && slices.Equal(r.ReasonCodes, v.ReasonCodes) &&
		r.SameResource == f.SameResource && r.OwnerNormalData == f.OwnerNormalData && r.CrossNormalData == f.CrossNormalData &&
		r.NontrivialDataMatch == f.NontrivialDataMatch && r.ReflectionExcluded == f.ReflectionExcluded && r.TemplateExcluded == f.TemplateExcluded
}

func (r accessMapRecord) valid(s e3record.Source) bool {
	if r.Axis != "horizontal" || r.VulnClass != "IDOR" || r.ObservedAt.IsZero() || r.CreatedAt.IsZero() ||
		r.OwnerStatus != s.Expected.Owner.Status || r.EvaluatedStatus != s.Expected.Evaluated.Status || r.ExpectedAccess != s.Policy.ExpectedAccess || r.Sharing != s.Policy.Sharing || r.Delegation != s.Policy.Delegation ||
		!r.PolicyValidFrom.Equal(s.Policy.ValidFrom) || !r.PolicyValidUntil.Equal(s.Policy.ValidUntil) || r.HintMatchesCanonical != s.Policy.HintMatchesCanonical {
		return false
	}
	switch r.OracleValue {
	case oracle.Confirmed, oracle.Suspected, oracle.Undecidable:
		if r.VulnVerdict == nil || *r.VulnVerdict != r.OracleValue {
			return false
		}
		if r.OracleValue == oracle.Undecidable && r.AccessPolicyResult != "판정_불가" || r.OracleValue != oracle.Undecidable && r.AccessPolicyResult != "위반_후보" {
			return false
		}
	case oracle.Allowed:
		if r.VulnVerdict != nil || r.AccessPolicyResult != "위반_미관찰" || !s.Policy.HasRegisteredSource {
			return false
		}
	default:
		return false
	}
	for _, state := range []oracle.CheckState{r.PolicyCheck, r.IdentityCheck, r.DataCheck, r.SameResource, r.OwnerNormalData, r.CrossNormalData, r.NontrivialDataMatch, r.ReflectionExcluded, r.TemplateExcluded} {
		if state != oracle.StateYes && state != oracle.StateNo && state != oracle.StateUnknown {
			return false
		}
	}
	if len(r.ReasonCodes) == 0 || len(r.ReasonCodes) > 3 || !sameControlledPreparationFields(r.CanaryFields, s.CanaryFields) {
		return false
	}
	for _, reason := range r.ReasonCodes {
		if !accessMapReason(reason) {
			return false
		}
	}
	for _, name := range r.CanaryFields {
		if name == "" {
			return false
		}
		for _, c := range name {
			if c < 0x20 || c > 0x7e {
				return false
			}
		}
	}
	return true
}

func accessMapReason(reason oracle.JudgeReason) bool {
	switch reason {
	case oracle.ReasonPolicyDenyResolved, oracle.ReasonPolicyAllows, oracle.ReasonSharingOrDelegationAllows, oracle.ReasonExpectedAccessPolicyMissing, oracle.ReasonExpectedAccessPolicyInvalid, oracle.ReasonExpectedAccessUnknown, oracle.ReasonSharingOrDelegationUnknown, oracle.ReasonSubjectsDistinctVerified, oracle.ReasonSubjectNotVerified, oracle.ReasonSubjectsNotDistinct, oracle.ReasonDataNontrivialMatch, oracle.ReasonDataNoMatch, oracle.ReasonDataReflectionPresent, oracle.ReasonDataTemplatePresent, oracle.ReasonDataUnverifiable:
		return true
	}
	return false
}

func projectAccessMap(b accessMapBinding, records []accessMapStoredRecord) accessMapResponse {
	out := accessMapResponse{TaskID: b.TaskID, RunRef: b.Scope.RunRef, ResetGeneration: b.Scope.ResetRef, Axis: "horizontal", PolicyVersion: b.PolicyVersion, Cells: []accessMapCell{}}
	unknown := oracle.StateUnknown
	type cellKey struct{ principal, resource string }
	byCell := make(map[cellKey][]accessMapStoredRecord)
	for _, r := range records {
		key := cellKey{r.record.EvaluatedRef, r.record.ResourceRef}
		byCell[key] = append(byCell[key], r)
	}
	attempts := make(map[cellKey]bool)
	for _, a := range b.Attempts {
		attempts[cellKey{a.Principal, a.Resource}] = true
	}
	for _, resource := range b.Resources {
		for _, principal := range b.Principals {
			cell := accessMapCell{Principal: principal, OwnerRef: resource.OwnerRef, ResourceRef: resource.ResourceRef, ResourceLabel: resource.ResourceRef, State: "blank",
				accessMapSignals: accessMapSignals{unknown, unknown, unknown, unknown, unknown, unknown, unknown, unknown, unknown}, ReasonCodes: []oracle.JudgeReason{}, CanaryFields: []string{}}
			if accessMapHas(resource.Principals, principal) {
				applicable := true
				cell.Applicable = &applicable
			}
			selected := byCell[cellKey{principal, resource.ResourceRef}]
			attempted := attempts[cellKey{principal, resource.ResourceRef}]
			switch {
			case len(selected) > 1:
				cell.fail("selection_ambiguous")
			case len(selected) == 1:
				s := selected[0]
				r := s.record
				if !s.valid || r.OwnerRef != resource.OwnerRef {
					cell.fail("record_invalid")
					break
				}
				cell.RecordID, cell.CaseRef, cell.ObservedAt, cell.OracleValue = &s.id, &r.CaseRef, &r.ObservedAt, &r.OracleValue
				cell.accessMapSignals, cell.ReasonCodes, cell.CanaryFields = r.accessMapSignals, append([]oracle.JudgeReason{}, r.ReasonCodes...), append([]string{}, r.CanaryFields...)
				cell.State = "unknown"
				switch r.OracleValue {
				case oracle.Confirmed:
					if r.PolicyCheck == oracle.StateYes && r.IdentityCheck == oracle.StateYes && r.DataCheck == oracle.StateYes && r.AccessPolicyResult == "위반_후보" {
						cell.State = "confirmed"
					} else {
						cell.fail("record_invalid")
					}
				case oracle.Suspected:
					cell.State = "suspected"
				case oracle.Allowed:
					if r.SameResource == oracle.StateYes && r.CrossNormalData == oracle.StateYes && r.IdentityCheck == oracle.StateYes && r.EvaluatedStatus == oracle.SubjectVerified &&
						r.HintMatchesCanonical && !r.ObservedAt.Before(r.PolicyValidFrom) && r.ObservedAt.Before(r.PolicyValidUntil) &&
						(r.ExpectedAccess == oracle.AccessAllow || r.Sharing == oracle.ResolvedAllow || r.Delegation == oracle.ResolvedAllow) {
						cell.State = "allow-ok"
					}
				}
			case attempted:
				cell.fail("record_missing_after_attempt")
			case cell.Applicable != nil:
				cell.fail("observation_unavailable")
			}
			out.Cells = append(out.Cells, cell)
		}
	}
	return out
}

func (c *accessMapCell) fail(reason string) { c.State = "unknown"; c.ProjectionError = &reason }

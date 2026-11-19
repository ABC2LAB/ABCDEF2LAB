package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// 판정 출처 종류(e3_verdict_issuances.source_kind). 평가 등록 GET 경로와 제품 확정 경로의 판정은 같은 표에 있지만
// 섞어 읽지 않는다(2026-11-18 사용자 결정 ①(가), docs/contracts/e3-product-confirmation-interface.md).
const (
	E3SourceEval    = "eval"
	E3SourceProduct = "product"
)

// 제품 판정 정본 형식. 평가 판정(e3record, record_version 1)과 형식이 달라 판을 나눈다.
const (
	productVerdictRecordVersion = 2
	productVerdictFormat        = "e3-product-verdict/v1"
	productManifestFormat       = "e3-product-manifest/v2"
	productBindingFormat        = "e3-product-binding/v1"
	productPolicyKind           = "product_declaration"
	// 제품 회차 하나는 근거 묶음(봉인한 E² 저널) 하나를 갖고, 종류별 판정이 그것을 함께 가리킨다.
	productEvidenceRevision = 1
	productOperationVersion = 2
)

// 칸 상한. 판정 정본은 평가 경로와 같은 64KiB 안에 들어가야 한다(e3_oracle_verdicts 검사).
const (
	maxProductCanonicalBytes = 64 << 10
	maxProductRefBytes       = 128
	maxProductEvidence       = 64
	maxProductReasons        = 16
	maxProductCandidates     = 64
	maxProductOracleReasons  = 8
)

var (
	ErrProductVerdictInput    = errors.New("product_verdict_input_invalid")
	ErrProductVerdictConflict = errors.New("product_verdict_save_conflict")

	productRef  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	productCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// ProductPolicySource는 실행 시작 때 동결한 정책 선언 출처다(e3contract.PolicySource의 제품 칸).
type ProductPolicySource struct {
	Ref                    string
	DigestSHA256           [32]byte
	TargetApprovalRevision string
}

// ProductBinding은 판정이 나온 실행 결속이다(e3contract.RunBinding).
type ProductBinding struct {
	JobRef, RunRef, E2PolicyRef string
	GenerationRef, InstanceRef  string
	Policy                      ProductPolicySource
}

// ProductEvidence는 봉인한 E² 저널 안의 호출 하나다. 요청·응답 원문은 없다.
type ProductEvidence struct {
	CallRef   string
	AttemptID string
	Sequence  uint64
}

// ProductCheckpoint는 확정 단계에서 Host가 근거를 검증한 현재 head 체크포인트다(E1, docs/contracts/e3-product-verdict-store.md
// §2). 그 head는 회차의 모든 근거 사건을 앞부분으로 포함한다. 봉인 포함 여부는 따로 기록한다(e3_seal_checks).
type ProductCheckpoint struct {
	JournalID string
	Sequence  uint64
	MAC       string
}

// ProductVerdict는 제품 확정 단계에서 나온 자원 종류 하나의 판정이다. 표식 값·본문·계정 자격은 들어 있지 않다.
// 판정 값과 세 검사는 오라클(oracle.Evaluate)이 낸 그대로이고, 저장은 그 값을 다시 만들지 않는다.
type ProductVerdict struct {
	TaskID  int64
	Binding ProductBinding
	// Checkpoint는 회차의 근거 체크포인트다. 근거 호출이 없거나 Host에서 받지 못했으면 nil이다.
	Checkpoint     *ProductCheckpoint
	ResourceType   string
	ResourceRef    string
	ModelTag       string
	Value          oracle.VerdictValue
	PolicyCheck    oracle.CheckState
	IdentityCheck  oracle.CheckState
	DataCheck      oracle.CheckState
	OracleReasons  []oracle.JudgeReason
	ProductReasons []string
	CandidateRefs  []string
	Evidence       []ProductEvidence
	ObservedAt     time.Time
}

type ProductVerdictResult struct {
	ManifestID int64 `json:"manifest_id,string"`
	VerdictID  int64 `json:"verdict_id,string"`
	IssuanceID int64 `json:"issuance_id,string"`
	Created    bool  `json:"created"`
}

// 정본 바이트는 구조체 칸 순서로 고정된다(map이 아니어서 키 순서가 바뀌지 않는다).
type productEvidenceJSON struct {
	CallRef   string `json:"call_ref"`
	AttemptID string `json:"attempt_id"`
	Sequence  uint64 `json:"sequence"`
}

type productPolicyJSON struct {
	Kind                   string `json:"kind"`
	Ref                    string `json:"ref"`
	DigestSHA256           string `json:"digest_sha256"`
	TargetApprovalRevision string `json:"target_approval_revision"`
}

type productPayloadJSON struct {
	Format         string                `json:"format"`
	SourceKind     string                `json:"source_kind"`
	CaseRef        string                `json:"case_ref"`
	ResourceType   string                `json:"resource_type"`
	ResourceRef    string                `json:"resource_ref"`
	OracleValue    string                `json:"oracle_value"`
	PolicyCheck    string                `json:"policy_check"`
	IdentityCheck  string                `json:"identity_check"`
	DataCheck      string                `json:"data_check"`
	OracleReasons  []string              `json:"oracle_reason_codes"`
	ProductReasons []string              `json:"product_reason_codes"`
	CandidateRefs  []string              `json:"candidate_refs"`
	Evidence       []productEvidenceJSON `json:"evidence"`
	ObservedAt     string                `json:"observed_at"`
	ModelTag       string                `json:"model_tag"`
	Policy         productPolicyJSON     `json:"policy_source"`
}

type productManifestJSON struct {
	Format     string                 `json:"format"`
	RunRef     string                 `json:"run_ref"`
	Checkpoint *productCheckpointJSON `json:"evidence_checkpoint"`
}

type productCheckpointJSON struct {
	JournalID string `json:"journal_id"`
	Sequence  uint64 `json:"sequence"`
	MAC       string `json:"mac"`
}

type productBindingJSON struct {
	Format        string            `json:"format"`
	TaskID        int64             `json:"task_id"`
	JobRef        string            `json:"job_ref"`
	RunRef        string            `json:"run_ref"`
	E2PolicyRef   string            `json:"e2_policy_ref"`
	GenerationRef string            `json:"generation_ref"`
	InstanceRef   string            `json:"instance_ref"`
	Policy        productPolicyJSON `json:"policy"`
}

func productCaseRef(resourceType string) string { return "e3-product:" + resourceType }

func productTime(t time.Time) string {
	return t.Round(0).UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}

func hexDigest(d [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2], out[i*2+1] = digits[b>>4], digits[b&0x0f]
	}
	return string(out)
}

// ValidProductCheckpoint는 근거 체크포인트 모양이다(저널 ID는 참조 모양, 순번은 1 이상, MAC은 소문자 16진 64자).
func ValidProductCheckpoint(c ProductCheckpoint) bool {
	return validProductRefs(c.JournalID) && c.Sequence > 0 && c.Sequence <= 1<<62 && validLowerHex64(c.MAC)
}

func validLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func validProductRefs(refs ...string) bool {
	for _, r := range refs {
		if len(r) > maxProductRefBytes || !productRef.MatchString(r) {
			return false
		}
	}
	return true
}

func validCheck(s oracle.CheckState) bool {
	return s == oracle.StateYes || s == oracle.StateNo || s == oracle.StateUnknown
}

func uniqueMatching(list []string, limit int, valid func(string) bool) bool {
	if len(list) > limit {
		return false
	}
	seen := map[string]bool{}
	for _, s := range list {
		if !valid(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// validProductVerdict는 저장 전에 칸 모양과 판정 일관성을 본다. 확정(CONFIRMED)은 세 검사가 모두 yes이고 근거 호출이
// 하나 이상이며 제품 사유(증거 부족 표시)가 없을 때만 받는다(증거 없는 확정 금지).
func validProductVerdict(in ProductVerdict) bool { return ProductVerdictProblem(in) == "" }

// ProductVerdictProblem은 저장 입력이 계약 모양이 아닐 때 처음 걸린 칸 이름(고정 코드)이다. 맞으면 빈 문자열이다. 값은
// 돌려주지 않는다(로그에 칸 이름만 남기려는 것).
func ProductVerdictProblem(in ProductVerdict) string {
	b, p := in.Binding, in.Binding.Policy
	checks := []struct {
		field string
		ok    bool
	}{
		{"task_id", in.TaskID > 0},
		{"observed_at", !in.ObservedAt.IsZero() && in.ObservedAt.UTC().Year() >= 2000 && in.ObservedAt.UTC().Year() <= 9999},
		{"job_ref", validProductRefs(b.JobRef)}, {"run_ref", validProductRefs(b.RunRef)}, {"e2_policy_ref", validProductRefs(b.E2PolicyRef)},
		{"generation_ref", validProductRefs(b.GenerationRef)}, {"instance_ref", validProductRefs(b.InstanceRef)},
		{"policy_ref", validProductRefs(p.Ref)}, {"target_approval_revision", validProductRefs(p.TargetApprovalRevision)},
		{"policy_digest", p.DigestSHA256 != ([32]byte{})},
		{"evidence_checkpoint", in.Checkpoint == nil || ValidProductCheckpoint(*in.Checkpoint)},
		{"resource_type", productCode.MatchString(in.ResourceType)},
		{"resource_ref", strings.HasPrefix(in.ResourceRef, in.ResourceType+":") && validProductRefs(in.ResourceRef)},
		{"model_tag", in.ModelTag != "" && len(in.ModelTag) <= 128 && productRef.MatchString(in.ModelTag)},
		{"checks", validCheck(in.PolicyCheck) && validCheck(in.IdentityCheck) && validCheck(in.DataCheck)},
		{"evidence_count", len(in.Evidence) <= maxProductEvidence},
		{"oracle_value", in.Value == oracle.Confirmed || in.Value == oracle.Suspected || in.Value == oracle.Undecidable || in.Value == oracle.Allowed},
	}
	for _, c := range checks {
		if !c.ok {
			return c.field
		}
	}
	reasons := make([]string, len(in.OracleReasons))
	for i, r := range in.OracleReasons {
		reasons[i] = string(r)
	}
	switch {
	case !uniqueMatching(reasons, maxProductOracleReasons, productCode.MatchString):
		return "oracle_reason_codes"
	case !uniqueMatching(in.ProductReasons, maxProductReasons, productCode.MatchString):
		return "product_reason_codes"
	case !uniqueMatching(in.CandidateRefs, maxProductCandidates, func(s string) bool { return validProductRefs(s) }):
		return "candidate_refs"
	}
	seen := map[string]bool{}
	for _, e := range in.Evidence {
		if !validProductRefs(e.CallRef, e.AttemptID) || e.Sequence == 0 || e.Sequence > 1<<62 || seen[e.CallRef] {
			return "evidence"
		}
		seen[e.CallRef] = true
	}
	if in.Value == oracle.Confirmed && (in.PolicyCheck != oracle.StateYes || in.IdentityCheck != oracle.StateYes ||
		in.DataCheck != oracle.StateYes || len(in.Evidence) == 0 || len(in.ProductReasons) != 0) {
		return "confirmed_invariant"
	}
	return ""
}

type productSnapshot struct {
	payload, manifest, binding    []byte
	payloadDigest, manifestDigest [32]byte
	inputDigest, operationKey     [32]byte
	reasons, candidates           []byte
	caseRef                       string
}

func prepareProductVerdict(in ProductVerdict) (productSnapshot, error) {
	if !validProductVerdict(in) {
		return productSnapshot{}, ErrProductVerdictInput
	}
	policy := productPolicyJSON{Kind: productPolicyKind, Ref: in.Binding.Policy.Ref,
		DigestSHA256: hexDigest(in.Binding.Policy.DigestSHA256), TargetApprovalRevision: in.Binding.Policy.TargetApprovalRevision}
	oracleReasons := make([]string, len(in.OracleReasons))
	for i, r := range in.OracleReasons {
		oracleReasons[i] = string(r)
	}
	evidence := make([]productEvidenceJSON, len(in.Evidence))
	for i, e := range in.Evidence {
		evidence[i] = productEvidenceJSON{e.CallRef, e.AttemptID, e.Sequence}
	}
	product := append([]string{}, in.ProductReasons...)
	candidates := append([]string{}, in.CandidateRefs...)
	s := productSnapshot{caseRef: productCaseRef(in.ResourceType)}
	var err error
	if s.payload, err = json.Marshal(productPayloadJSON{Format: productVerdictFormat, SourceKind: E3SourceProduct, CaseRef: s.caseRef,
		ResourceType: in.ResourceType, ResourceRef: in.ResourceRef, OracleValue: string(in.Value), PolicyCheck: string(in.PolicyCheck),
		IdentityCheck: string(in.IdentityCheck), DataCheck: string(in.DataCheck), OracleReasons: oracleReasons, ProductReasons: product,
		CandidateRefs: candidates, Evidence: evidence, ObservedAt: productTime(in.ObservedAt), ModelTag: in.ModelTag, Policy: policy}); err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	manifest := productManifestJSON{Format: productManifestFormat, RunRef: in.Binding.RunRef}
	if c := in.Checkpoint; c != nil {
		manifest.Checkpoint = &productCheckpointJSON{JournalID: c.JournalID, Sequence: c.Sequence, MAC: c.MAC}
	}
	if s.manifest, err = json.Marshal(manifest); err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	b := in.Binding
	if s.binding, err = json.Marshal(productBindingJSON{Format: productBindingFormat, TaskID: in.TaskID, JobRef: b.JobRef, RunRef: b.RunRef,
		E2PolicyRef: b.E2PolicyRef, GenerationRef: b.GenerationRef, InstanceRef: b.InstanceRef, Policy: policy}); err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	if s.reasons, err = json.Marshal(product); err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	if s.candidates, err = json.Marshal(candidates); err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	if len(s.payload) > maxProductCanonicalBytes || len(s.manifest) > maxProductCanonicalBytes || len(s.binding) > maxProductCanonicalBytes {
		return productSnapshot{}, ErrProductVerdictInput
	}
	s.payloadDigest, s.manifestDigest = sha256.Sum256(s.payload), sha256.Sum256(s.manifest)
	bindingDigest := sha256.Sum256(s.binding)
	digestInput, err := json.Marshal([3]string{hexDigest(s.payloadDigest), hexDigest(s.manifestDigest), hexDigest(bindingDigest)})
	if err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	s.inputDigest = sha256.Sum256(digestInput)
	// 평가 경로 키(판 1)와 판을 달리 해서 두 경로의 operation_key가 겹치지 않는다.
	key, err := json.Marshal([]any{productOperationVersion, in.TaskID, b.RunRef, s.caseRef, "initial"})
	if err != nil {
		return productSnapshot{}, ErrProductVerdictInput
	}
	s.operationKey = sha256.Sum256(key)
	return s, nil
}

// RecordProductVerdict는 제품 확정 단계 판정 하나를 저장한다. 판정 행·근거 묶음·발급 행만 만들고 finding은 만들지 않는다:
// 제품 판정은 모델 단계 후보를 확정으로 바꾸지 않고 candidate_refs로 잇기만 한다(결정 §9). 같은 회차·종류를 다시 저장하면
// 같은 입력일 때만 앞 결과를 돌려주고, 다르면 충돌이다. 대상 요청을 보내거나 판정을 다시 계산하지 않는다.
func (d *DB) RecordProductVerdict(ctx context.Context, in ProductVerdict) (out ProductVerdictResult, err error) {
	if ctx == nil || ctx.Err() != nil || d == nil || d.DB == nil {
		return out, ErrProductVerdictInput
	}
	s, err := prepareProductVerdict(in)
	if err != nil {
		return out, err
	}
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if err := LockTaskEvidenceTx(tx, in.TaskID); err != nil {
			return err
		}
		var exploration int64
		var deleted sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT exploration_id,deleted_at FROM tasks WHERE id=$1`, in.TaskID).Scan(&exploration, &deleted); err != nil {
			return err
		}
		if deleted.Valid || exploration <= 0 {
			return ErrProductVerdictInput
		}
		var existingDigest []byte
		var source string
		err := tx.QueryRowContext(ctx, `SELECT id,manifest_id,verdict_id,input_digest,source_kind FROM e3_verdict_issuances WHERE operation_key=$1 FOR UPDATE`,
			s.operationKey[:]).Scan(&out.IssuanceID, &out.ManifestID, &out.VerdictID, &existingDigest, &source)
		if err == nil {
			if source != E3SourceProduct || !bytes.Equal(existingDigest, s.inputDigest[:]) {
				return ErrProductVerdictConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO e3_evidence_manifests(task_id,execution_ref,evidence_revision,canonical,digest)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(task_id,execution_ref,evidence_revision) DO NOTHING`,
			in.TaskID, in.Binding.RunRef, productEvidenceRevision, s.manifest, s.manifestDigest[:]); err != nil {
			return err
		}
		var manifest, digest []byte
		if err := tx.QueryRowContext(ctx, `SELECT id,canonical,digest FROM e3_evidence_manifests
WHERE task_id=$1 AND execution_ref=$2 AND evidence_revision=$3 FOR UPDATE`, in.TaskID, in.Binding.RunRef, productEvidenceRevision).
			Scan(&out.ManifestID, &manifest, &digest); err != nil {
			return err
		}
		// 같은 회차의 종류별 판정은 같은 봉인 저널을 가리켜야 한다.
		if !bytes.Equal(manifest, s.manifest) || !bytes.Equal(digest, s.manifestDigest[:]) {
			return ErrProductVerdictConflict
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO e3_oracle_verdicts(record_version,payload_canonical,payload,payload_digest,oracle_value)
VALUES($1,$2,$3::jsonb,$4,$5) RETURNING id`, productVerdictRecordVersion, s.payload, string(s.payload), s.payloadDigest[:],
			string(in.Value)).Scan(&out.VerdictID); err != nil {
			return err
		}
		b := in.Binding
		if err := tx.QueryRowContext(ctx, `INSERT INTO e3_verdict_issuances(
verdict_id,manifest_id,task_id,exploration_id,intent_id,mode_version,purpose,execution_ref,case_ref,resource_ref,
eval_run_id,model_tag,reset_generation,kg_snapshot_ref,kg_policy_version,kg_snapshot_digest,harness_generation_ref,
target_instance_ref,operation_key,input_digest,binding_canonical,source_kind,policy_source_ref,policy_source_digest,
policy_target_approval_revision,product_reason_codes,candidate_refs)
VALUES($1,$2,$3,$4,NULL,NULL,'initial',$5,$6,$7,NULL,$8,$9,NULL,NULL,NULL,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::jsonb,$20::jsonb) RETURNING id`,
			out.VerdictID, out.ManifestID, in.TaskID, exploration, b.RunRef, s.caseRef, in.ResourceRef, in.ModelTag, b.GenerationRef,
			b.GenerationRef, b.InstanceRef, s.operationKey[:], s.inputDigest[:], s.binding, E3SourceProduct, b.Policy.Ref,
			b.Policy.DigestSHA256[:], b.Policy.TargetApprovalRevision, string(s.reasons), string(s.candidates)).Scan(&out.IssuanceID); err != nil {
			return err
		}
		out.Created = true
		return nil
	})
	if err != nil {
		out = ProductVerdictResult{}
		if errors.Is(err, ErrProductVerdictInput) || errors.Is(err, ErrProductVerdictConflict) {
			return out, err
		}
		return out, controlledFindingStoreError{err}
	}
	return out, nil
}

// StoredProductVerdict는 제품 판정 행 하나를 읽은 것이다. 출처 종류를 늘 싣는다.
type StoredProductVerdict struct {
	IssuanceID   int64
	SourceKind   string
	RunRef       string
	ResourceRef  string
	Value        oracle.VerdictValue
	PolicyRef    string
	PolicyDigest [32]byte
	Payload      []byte
	IssuedAt     time.Time
}

// ProductVerdicts는 작업의 제품 판정만 읽는다(평가 판정은 섞지 않는다). 정본 바이트는 저장 digest와 대조하고, 발급 행의
// 출처 칸이 정본의 정책 출처와 같은지 확인한다. 다르면 읽기 전체를 거부한다.
func (d *DB) ProductVerdicts(ctx context.Context, taskID int64, limit int) ([]StoredProductVerdict, error) {
	if ctx == nil || d == nil || d.DB == nil || taskID <= 0 || limit <= 0 {
		return nil, ErrProductVerdictInput
	}
	rows, err := d.QueryContext(ctx, `SELECT i.id,i.source_kind,i.execution_ref,i.resource_ref,v.oracle_value,i.policy_source_ref,
 i.policy_source_digest,v.record_version,v.payload_canonical,v.payload_digest,i.issued_at
 FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 WHERE i.task_id=$1 AND i.source_kind=$2 ORDER BY i.id LIMIT $3`, taskID, E3SourceProduct, limit+1)
	if err != nil {
		return nil, controlledFindingStoreError{err}
	}
	defer rows.Close()
	out := []StoredProductVerdict{}
	for rows.Next() {
		var v StoredProductVerdict
		var policyDigest, payloadDigest []byte
		var version int
		var value string
		if err := rows.Scan(&v.IssuanceID, &v.SourceKind, &v.RunRef, &v.ResourceRef, &value, &v.PolicyRef, &policyDigest, &version,
			&v.Payload, &payloadDigest, &v.IssuedAt); err != nil {
			return nil, controlledFindingStoreError{err}
		}
		v.Value = oracle.VerdictValue(value)
		actual := sha256.Sum256(v.Payload)
		var p productPayloadJSON
		if version != productVerdictRecordVersion || len(policyDigest) != 32 || !bytes.Equal(actual[:], payloadDigest) ||
			json.Unmarshal(v.Payload, &p) != nil || p.Format != productVerdictFormat || p.SourceKind != E3SourceProduct ||
			p.OracleValue != value || p.ResourceRef != v.ResourceRef || p.Policy.Ref != v.PolicyRef {
			return nil, ErrProductVerdictConflict
		}
		copy(v.PolicyDigest[:], policyDigest)
		if p.Policy.DigestSHA256 != hexDigest(v.PolicyDigest) {
			return nil, ErrProductVerdictConflict
		}
		out = append(out, v)
		if len(out) > limit {
			return nil, ErrProductVerdictInput
		}
	}
	if err := rows.Err(); err != nil {
		return nil, controlledFindingStoreError{err}
	}
	return out, nil
}

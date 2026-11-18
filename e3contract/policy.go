package e3contract

import (
	"errors"
	"fmt"
	"regexp"
)

// PolicyKind는 E③가 읽는 정책 정본의 종류다. 종류는 이 칸으로만 정하고 policy_version 문자열 모양으로 추정하지 않는다.
type PolicyKind string

const (
	// PolicyProductDeclaration은 운영자 정책 선언의 동결본이다(제품 경로, #596 §4).
	PolicyProductDeclaration PolicyKind = "product_declaration"
	// PolicyKGSnapshot은 평가 fixture 레지스트리 snapshot이다(평가 경로). 제품 확정 단계는 받지 않는다.
	PolicyKGSnapshot PolicyKind = "kg_snapshot"
)

var (
	// ErrPolicySource는 정책 출처 칸이 모자라거나 모양이 틀린 경우다.
	ErrPolicySource = errors.New("e3contract: 정책 출처가 계약 모양이 아니다")
	// ErrProductPolicyKind는 제품 확정 경로에 product_declaration이 아닌 정책 출처가 들어온 경우다.
	ErrProductPolicyKind = errors.New("e3contract: 제품 확정 단계는 product_declaration 정책 출처만 받는다")
)

var (
	refPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// PolicySource는 판정이 기대는 정책 정본의 참조다. 원문 선언·레지스트리는 담지 않는다.
type PolicySource struct {
	Kind PolicyKind `json:"kind"`
	// Ref는 동결본(또는 snapshot)의 비민감 참조다.
	Ref string `json:"ref"`
	// DigestSHA256은 동결본 정본 바이트의 SHA-256(소문자 16진 64자)이다. policy_version은 이 값에 결속된다.
	DigestSHA256 string `json:"digest_sha256"`
	// TargetApprovalRevision은 선언이 붙은 대상 승인 판이다. 대상을 다시 승인하면 선언은 무효다(#596 §4.2).
	TargetApprovalRevision string `json:"target_approval_revision"`
}

// Validate는 칸 모양만 본다. 종류를 가리지 않는다.
func (p PolicySource) Validate() error {
	if p.Kind != PolicyProductDeclaration && p.Kind != PolicyKGSnapshot {
		return fmt.Errorf("%w: kind", ErrPolicySource)
	}
	if !refPattern.MatchString(p.Ref) {
		return fmt.Errorf("%w: ref", ErrPolicySource)
	}
	if !digestPattern.MatchString(p.DigestSHA256) {
		return fmt.Errorf("%w: digest_sha256", ErrPolicySource)
	}
	if !refPattern.MatchString(p.TargetApprovalRevision) {
		return fmt.Errorf("%w: target_approval_revision", ErrPolicySource)
	}
	return nil
}

// ValidateProduct는 제품 확정 단계용 검사다. kg_snapshot은 모양이 맞아도 거부한다.
func (p PolicySource) ValidateProduct() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Kind != PolicyProductDeclaration {
		return ErrProductPolicyKind
	}
	return nil
}

// ErrBinding은 실행 결속 칸이 모자라거나 모양이 틀린 경우다.
var ErrBinding = errors.New("e3contract: 실행 결속이 계약 모양이 아니다")

// RunBinding은 서버가 실행마다 발급하는 결속이다. 확정 단계의 모든 관찰과 판정에 같은 값이 들어간다.
// 하나라도 다르면 결속 실패(confirmation_integrity_failed)다.
type RunBinding struct {
	// JobRef는 제품 진단 Job이다.
	JobRef string `json:"job_ref"`
	// RunRef·TaskRef·E2PolicyRef는 E² 실행 범위(transmit.Scope의 RunRef·TaskRef·PolicyRef)와 같은 값이다.
	// E2PolicyRef는 실행 승인 정책이고 E③ 정책 정본(Policy)과 다르다.
	RunRef      string `json:"run_ref"`
	TaskRef     string `json:"task_ref"`
	E2PolicyRef string `json:"e2_policy_ref"`
	// GenerationRef·InstanceRef는 대상 세대·인스턴스 참조다. 서버가 실행마다 발급한다(#596 §5 0단계).
	GenerationRef string `json:"generation_ref"`
	InstanceRef   string `json:"instance_ref"`
	// Policy는 실행 시작 때 동결한 정책 출처다.
	Policy PolicySource `json:"policy"`
}

// Validate는 칸 모양과 정책 출처 모양을 본다. 제품 경로 여부는 ValidateProduct가 본다.
func (b RunBinding) Validate() error {
	for name, v := range map[string]string{"job_ref": b.JobRef, "run_ref": b.RunRef, "task_ref": b.TaskRef,
		"e2_policy_ref": b.E2PolicyRef, "generation_ref": b.GenerationRef, "instance_ref": b.InstanceRef} {
		if !refPattern.MatchString(v) {
			return fmt.Errorf("%w: %s", ErrBinding, name)
		}
	}
	if err := b.Policy.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrBinding, err)
	}
	return nil
}

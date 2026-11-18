package e3contract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DeclarationVersion은 정책 선언 레코드 형식 판이다. 정본 바이트 규칙이 바뀌면 판을 올린다.
const DeclarationVersion = "e3-product-declaration/v1"

// IDPlaceholder는 조회 경로 틀에서 객체 ID가 들어갈 조각이다. 한 경로에 정확히 한 번만 쓴다.
const IDPlaceholder = "{id}"

// declarationTimeLayout은 정본 바이트의 시각 모양이다(UTC 마이크로초).
const declarationTimeLayout = "2006-01-02T15:04:05.000000Z"

// ErrDeclaration은 선언이 계약 모양이 아닌 경우다.
var ErrDeclaration = errors.New("e3contract: 정책 선언이 계약 모양이 아니다")

// Sharing은 자원 종류의 공유 선언이다.
type Sharing string

const (
	SharingShared    Sharing = "shared"
	SharingOwnerOnly Sharing = "owner_only"
)

// BodyKind는 생성 요청 본문 틀의 모양이다.
type BodyKind string

const (
	BodyJSON BodyKind = "json"
	BodyForm BodyKind = "form"
)

var (
	typePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	fieldPathPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	actorPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$`)
)

// CreateSpec은 표식을 심을 시험 객체를 만드는 요청이다. 경로는 승인 범위 안의 정규 경로와 정확히 같아야 한다.
type CreateSpec struct {
	Path   string   `json:"path"`
	Method string   `json:"method"`
	Kind   BodyKind `json:"body_kind"`
	// BodyTemplate은 표식 칸이 없는 본문 틀이다(json이면 JSON 객체, form이면 urlencoded). 프로그램이 MarkerField에 표식을 넣는다.
	BodyTemplate string `json:"body_template"`
	// MarkerField는 표식을 넣을 칸이다(json이면 점으로 이은 경로, form이면 칸 이름). OwnerOnlyFields 중 하나여야 한다.
	MarkerField string `json:"marker_field"`
	// IDPath는 생성 응답 JSON에서 객체 ID가 있는 경로다. ID 원문은 E² 밖으로 나오지 않는다.
	IDPath string `json:"id_path"`
}

// ReadSpec은 객체 조회 경로 틀과 응답 모양이다.
type ReadSpec struct {
	// PathTemplate은 {id} 조각 하나만 있는 GET 경로 틀이다(예: /api/things/{id}).
	PathTemplate string `json:"path_template"`
	// DataPath는 응답 JSON에서 객체 데이터 위치다. "."은 본문 전체다.
	DataPath string `json:"data_path"`
	// StatusPath·NormalValue는 선택이다. 둘 다 주거나 둘 다 비운다. 비우면 정상 응답은 E²의 HTTP 상태 판정을 따른다.
	StatusPath  string `json:"status_path,omitempty"`
	NormalValue string `json:"normal_value,omitempty"`
}

// IdentitySpec은 신원 확인 요청이다(GET, #596 §5.1). 대상마다 하나다.
type IdentitySpec struct {
	Path          string `json:"path"`
	SubjectIDPath string `json:"subject_id_path"`
}

// TypeDeclaration은 자원 종류 하나의 선언이다(#596 §4.1).
type TypeDeclaration struct {
	Type            string     `json:"type"`
	Sharing         Sharing    `json:"sharing"`
	OwnerOnlyFields []string   `json:"owner_only_fields"`
	Create          CreateSpec `json:"create"`
	Read            ReadSpec   `json:"read"`
}

// Confirmable은 확정 단계가 이 종류를 시험할 수 있는지다. owner_only이고 표식 칸이 소유자 전용 필드일 때만이다.
func (t TypeDeclaration) Confirmable() bool {
	if t.Sharing != SharingOwnerOnly || t.Create.MarkerField == "" {
		return false
	}
	for _, f := range t.OwnerOnlyFields {
		if f == t.Create.MarkerField {
			return true
		}
	}
	return false
}

// Declaration은 대상 하나의 정책 선언 레코드다. 대상 등록 보호 저널에 두고 실행 시작 때 동결한다(#596 §4.2).
type Declaration struct {
	Version                string            `json:"version"`
	TargetRef              string            `json:"target_ref"`
	TargetApprovalRevision string            `json:"target_approval_revision"`
	DeclaredBy             string            `json:"declared_by"`
	DeclaredAt             time.Time         `json:"declared_at"`
	Identity               IdentitySpec      `json:"identity"`
	Types                  []TypeDeclaration `json:"types"`
}

// Validate는 선언의 모양을 본다. 승인 범위 안 경로인지는 E²가 요청마다 다시 본다.
func (d Declaration) Validate() error {
	bad := func(field string) error { return fmt.Errorf("%w: %s", ErrDeclaration, field) }
	if d.Version != DeclarationVersion {
		return bad("version")
	}
	if !refPattern.MatchString(d.TargetRef) {
		return bad("target_ref")
	}
	if !refPattern.MatchString(d.TargetApprovalRevision) {
		return bad("target_approval_revision")
	}
	if !actorPattern.MatchString(d.DeclaredBy) {
		return bad("declared_by")
	}
	if d.DeclaredAt.IsZero() {
		return bad("declared_at")
	}
	if !plainPath(d.Identity.Path) || !fieldPathPattern.MatchString(d.Identity.SubjectIDPath) {
		return bad("identity")
	}
	if len(d.Types) == 0 {
		return bad("types")
	}
	for i, t := range d.Types {
		if !typePattern.MatchString(t.Type) {
			return bad("types.type")
		}
		// 종류는 오름차순·중복 없음이다. 같은 내용이 같은 정본 바이트가 되게 한다.
		if i > 0 && d.Types[i-1].Type >= t.Type {
			return bad("types.order")
		}
		if err := t.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (t TypeDeclaration) validate() error {
	bad := func(field string) error { return fmt.Errorf("%w: %s.%s", ErrDeclaration, t.Type, field) }
	if t.Sharing != SharingShared && t.Sharing != SharingOwnerOnly {
		return bad("sharing")
	}
	for i, f := range t.OwnerOnlyFields {
		if !fieldPathPattern.MatchString(f) {
			return bad("owner_only_fields")
		}
		if i > 0 && t.OwnerOnlyFields[i-1] >= f {
			return bad("owner_only_fields.order")
		}
	}
	if t.Sharing == SharingShared {
		// 공유 종류는 시험하지 않으므로 요청 칸을 두지 않는다. 남기면 확정 단계가 쓰는 것처럼 읽힌다.
		if t.Create != (CreateSpec{}) || t.Read != (ReadSpec{}) || len(t.OwnerOnlyFields) != 0 {
			return bad("shared_has_requests")
		}
		return nil
	}
	if len(t.OwnerOnlyFields) == 0 || !t.Confirmable() {
		return bad("marker_field")
	}
	c := t.Create
	if !plainPath(c.Path) || (c.Method != "POST" && c.Method != "PUT") || !fieldPathPattern.MatchString(c.IDPath) {
		return bad("create")
	}
	switch c.Kind {
	case BodyJSON:
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(c.BodyTemplate), &obj) != nil || obj == nil {
			return bad("create.body_template")
		}
		// 표식 칸이 틀에 이미 있으면 프로그램이 덮어쓰는지 남기는지 모호하다. 맨 위 칸만 확인한다(중첩 칸은 프로그램이 만든다).
		if !strings.Contains(c.MarkerField, ".") {
			if _, exists := obj[c.MarkerField]; exists {
				return bad("create.body_template.marker_field_present")
			}
		}
	case BodyForm:
		values, err := url.ParseQuery(c.BodyTemplate)
		if err != nil || strings.Contains(c.MarkerField, ".") {
			return bad("create.body_template")
		}
		if _, exists := values[c.MarkerField]; exists {
			return bad("create.body_template.marker_field_present")
		}
	default:
		return bad("create.body_kind")
	}
	r := t.Read
	if !readTemplate(r.PathTemplate) || (r.DataPath != "." && !fieldPathPattern.MatchString(r.DataPath)) {
		return bad("read")
	}
	if (r.StatusPath == "") != (r.NormalValue == "") || (r.StatusPath != "" && !fieldPathPattern.MatchString(r.StatusPath)) {
		return bad("read.status")
	}
	return nil
}

// plainPath는 {id} 없는 절대 경로다. query·조각·점 조각·공백·제어 문자를 받지 않는다.
func plainPath(p string) bool {
	return validPathShape(p) && !strings.ContainsAny(p, "{}")
}

// readTemplate은 {id} 조각이 정확히 하나인 절대 경로다.
func readTemplate(p string) bool {
	if !validPathShape(p) {
		return false
	}
	ids := 0
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == IDPlaceholder {
			ids++
		} else if strings.ContainsAny(seg, "{}") {
			return false
		}
	}
	return ids == 1
}

func validPathShape(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 512 || strings.ContainsAny(p, "?# \t\r\n\\") {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range p {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

// CanonicalBytes는 선언의 정본 바이트다: 칸 순서 고정 JSON, HTML 이스케이프 없음, 시각은 UTC 마이크로초.
// 동결본 digest(policy_version)는 이 바이트의 SHA-256이다.
func (d Declaration) CanonicalBytes() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	type canonicalDeclaration struct {
		Version                string            `json:"version"`
		TargetRef              string            `json:"target_ref"`
		TargetApprovalRevision string            `json:"target_approval_revision"`
		DeclaredBy             string            `json:"declared_by"`
		DeclaredAt             string            `json:"declared_at"`
		Identity               IdentitySpec      `json:"identity"`
		Types                  []TypeDeclaration `json:"types"`
	}
	// 빈 목록과 nil이 다른 바이트가 되지 않게 맞춘다.
	types := make([]TypeDeclaration, len(d.Types))
	for i, t := range d.Types {
		if len(t.OwnerOnlyFields) == 0 {
			t.OwnerOnlyFields = nil
		}
		types[i] = t
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(canonicalDeclaration{Version: d.Version, TargetRef: d.TargetRef,
		TargetApprovalRevision: d.TargetApprovalRevision, DeclaredBy: d.DeclaredBy,
		DeclaredAt: d.DeclaredAt.UTC().Truncate(time.Microsecond).Format(declarationTimeLayout),
		Identity:   d.Identity, Types: types})
	if err != nil {
		return nil, fmt.Errorf("%w: 정본 바이트를 만들지 못함", ErrDeclaration)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Digest는 정본 바이트의 SHA-256(소문자 16진)이다.
func (d Declaration) Digest() (string, error) {
	b, err := d.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Freeze는 실행 시작 때 선언을 동결한 정책 출처를 만든다. ref는 동결본을 보관한 비민감 참조다.
func Freeze(d Declaration, ref string) (PolicySource, error) {
	digest, err := d.Digest()
	if err != nil {
		return PolicySource{}, err
	}
	p := PolicySource{Kind: PolicyProductDeclaration, Ref: ref, DigestSHA256: digest,
		TargetApprovalRevision: d.TargetApprovalRevision}
	if err := p.ValidateProduct(); err != nil {
		return PolicySource{}, err
	}
	return p, nil
}

var (
	// ErrPolicyUnavailable은 동결본을 읽을 수 없는 경우다. 확정 단계를 시작하지 않는다(진위 실패와 다름).
	ErrPolicyUnavailable = errors.New("e3contract: 정책 동결본을 읽을 수 없다")
	// ErrPolicyDigestMismatch는 읽은 동결본이 결속된 digest·대상 판과 다른 경우다. 진위 실패다.
	ErrPolicyDigestMismatch = errors.New("e3contract: 정책 동결본이 결속된 digest와 다르다")
)

// DeclarationReader는 E③가 정책 동결본을 읽는 SourceReader다(최민준, 정책 선언 저장·동결). 구현은 저장된 동결본을
// 그대로 돌려주고, 대조는 VerifyFrozen이 한다. 읽을 수 없으면 ErrPolicyUnavailable을 감싸 돌려준다.
type DeclarationReader interface {
	ReadFrozen(ctx context.Context, source PolicySource) (Declaration, error)
}

// VerifyFrozen은 읽은 동결본의 정본 digest와 대상 판이 결속된 정책 출처와 같은지 본다.
func VerifyFrozen(source PolicySource, d Declaration) error {
	if err := source.ValidateProduct(); err != nil {
		return err
	}
	digest, err := d.Digest()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPolicyDigestMismatch, err)
	}
	if digest != source.DigestSHA256 || d.TargetApprovalRevision != source.TargetApprovalRevision {
		return ErrPolicyDigestMismatch
	}
	return nil
}

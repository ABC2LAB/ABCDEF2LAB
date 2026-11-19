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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/dlp"
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
	// 확정 단계의 계정 역할(사용자 결정 2026-11-19, 계약 §4). OwnerAccountLabel은 객체를 만들고 소유하는 시험 계정(A),
	// CrossAccountLabel은 그 객체를 교차 조회하는 시험 계정(B)이다. AccountsSamePrivilege는 두 계정이 같은 권한 등급이라는
	// 운영자 확인이다. 서버가 계정을 이름순 등으로 고르면 관리자 계정이 B가 되어 정상 권한으로 읽은 것을 거짓 CONFIRMED로 낼
	// 수 있어서, 운영자가 정하고 동결본에 묶는다. 셋 다 선택 칸이다: 비어 있는 선언은 정본 바이트가 칸을 넣기 전과 같고,
	// 확정 단계는 꺼진다(account_roles_undeclared). AccountsSamePrivilege가 참이 아니면 역시 꺼진다.
	OwnerAccountLabel     string `json:"owner_account_label,omitempty"`
	CrossAccountLabel     string `json:"cross_account_label,omitempty"`
	AccountsSamePrivilege bool   `json:"accounts_same_privilege,omitempty"`
}

// 선언 크기 상한이다. 선언은 준비 프로세스 IPC 한 프레임(diagnosisprep.MaxFrameBytes 64KiB)에 요청·응답으로 실리므로,
// 인코딩한 크기를 그 절반 아래로 묶는다. 종류·필드 수 상한은 화면이 다룰 수 있는 범위다.
const (
	MaxDeclarationTypes        = 16
	MaxOwnerOnlyFields         = 16
	MaxBodyTemplateBytes       = 2048
	MaxNormalValueBytes        = 128
	MaxDeclarationEncodedBytes = 24 << 10
)

// DeclarationFieldError는 선언 검사가 실패한 칸이다. Field는 칸 이름과 종류 순번만 담고 입력 값을 담지 않는다
// (예: types[1].create.path). 저장 API는 이 이름만 응답한다.
type DeclarationFieldError struct {
	Field string
}

func (e *DeclarationFieldError) Error() string { return ErrDeclaration.Error() + ": " + e.Field }
func (e *DeclarationFieldError) Unwrap() error { return ErrDeclaration }

// Validate는 선언의 모양을 본다. 승인 범위 안 경로인지는 저장 API가 대상 승인과 대조하고, E²가 요청마다 다시 본다.
func (d Declaration) Validate() error {
	bad := func(field string) error { return &DeclarationFieldError{Field: field} }
	switch {
	case d.Version != DeclarationVersion:
		return bad("version")
	case !refPattern.MatchString(d.TargetRef):
		return bad("target_ref")
	case !refPattern.MatchString(d.TargetApprovalRevision):
		return bad("target_approval_revision")
	case !actorPattern.MatchString(d.DeclaredBy):
		return bad("declared_by")
	case d.DeclaredAt.IsZero():
		return bad("declared_at")
	case !plainPath(d.Identity.Path):
		return bad("identity.path")
	case !fieldPathPattern.MatchString(d.Identity.SubjectIDPath):
		return bad("identity.subject_id_path")
	case len(d.Types) == 0 || len(d.Types) > MaxDeclarationTypes:
		return bad("types")
	case d.OwnerAccountLabel != "" && !refPattern.MatchString(d.OwnerAccountLabel):
		return bad("owner_account_label")
	case d.CrossAccountLabel != "" && !refPattern.MatchString(d.CrossAccountLabel):
		return bad("cross_account_label")
	// 역할은 둘 다 정하거나 둘 다 비운다. 같은 계정을 두 역할에 둘 수 없다. 역할이 없으면 등급 확인도 없다.
	case (d.OwnerAccountLabel == "") != (d.CrossAccountLabel == ""):
		return bad("cross_account_label")
	case d.OwnerAccountLabel != "" && d.OwnerAccountLabel == d.CrossAccountLabel:
		return bad("cross_account_label")
	case d.OwnerAccountLabel == "" && d.AccountsSamePrivilege:
		return bad("accounts_same_privilege")
	}
	for i, t := range d.Types {
		if !typePattern.MatchString(t.Type) {
			return bad(fmt.Sprintf("types[%d].type", i))
		}
		// 종류는 오름차순·중복 없음이다. 같은 내용이 같은 정본 바이트가 되게 한다(저장 API는 Normalize로 맞춘 뒤 검사한다).
		if i > 0 && d.Types[i-1].Type >= t.Type {
			return bad(fmt.Sprintf("types[%d].type", i))
		}
		if err := t.validate(i); err != nil {
			return err
		}
	}
	if raw, err := json.Marshal(d); err != nil || len(raw) > MaxDeclarationEncodedBytes {
		return bad("size")
	}
	return nil
}

func (t TypeDeclaration) validate(i int) error {
	bad := func(field string) error { return &DeclarationFieldError{Field: fmt.Sprintf("types[%d].%s", i, field)} }
	if t.Sharing != SharingShared && t.Sharing != SharingOwnerOnly {
		return bad("sharing")
	}
	if len(t.OwnerOnlyFields) > MaxOwnerOnlyFields {
		return bad("owner_only_fields")
	}
	for j, f := range t.OwnerOnlyFields {
		if !fieldPathPattern.MatchString(f) || (j > 0 && t.OwnerOnlyFields[j-1] >= f) {
			return bad(fmt.Sprintf("owner_only_fields[%d]", j))
		}
	}
	if t.Sharing == SharingShared {
		// 공유 종류는 시험하지 않으므로 요청 칸을 두지 않는다. 남기면 확정 단계가 쓰는 것처럼 읽힌다.
		if t.Create != (CreateSpec{}) || t.Read != (ReadSpec{}) || len(t.OwnerOnlyFields) != 0 {
			return bad("sharing")
		}
		return nil
	}
	if len(t.OwnerOnlyFields) == 0 {
		return bad("owner_only_fields")
	}
	c := t.Create
	switch {
	case !t.Confirmable():
		return bad("create.marker_field")
	case !plainPath(c.Path):
		return bad("create.path")
	case c.Method != "POST" && c.Method != "PUT":
		return bad("create.method")
	case !fieldPathPattern.MatchString(c.IDPath):
		return bad("create.id_path")
	case len(c.BodyTemplate) > MaxBodyTemplateBytes:
		return bad("create.body_template")
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
				return bad("create.body_template")
			}
		}
	case BodyForm:
		values, err := url.ParseQuery(c.BodyTemplate)
		if err != nil {
			return bad("create.body_template")
		}
		if strings.Contains(c.MarkerField, ".") {
			return bad("create.marker_field")
		}
		if _, exists := values[c.MarkerField]; exists {
			return bad("create.body_template")
		}
	default:
		return bad("create.body_kind")
	}
	r := t.Read
	switch {
	case !readTemplate(r.PathTemplate):
		return bad("read.path_template")
	case r.DataPath != "." && !fieldPathPattern.MatchString(r.DataPath):
		return bad("read.data_path")
	case (r.StatusPath == "") != (r.NormalValue == ""):
		return bad("read.status_path")
	case r.StatusPath != "" && !fieldPathPattern.MatchString(r.StatusPath):
		return bad("read.status_path")
	case r.NormalValue != "" && !normalValue(r.NormalValue):
		return bad("read.normal_value")
	}
	return nil
}

// normalValue는 상태 칸의 정상 값이다. 짧고 제어 문자가 없는 UTF-8이다.
func normalValue(v string) bool {
	if len(v) > MaxNormalValueBytes || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Normalize는 종류를 이름순으로, 소유자 전용 필드를 이름순으로 정렬한 사본이다. 저장 API가 화면 입력 순서와 관계없이
// 같은 내용을 같은 정본 바이트로 만들려고 검사 전에 부른다. 중복은 남겨 두고 Validate가 거부한다. 빈 필드 목록은 빈
// 목록([])으로 둔다(null을 받지 않는 보호 저널·IPC 때문이다. 정본 바이트는 CanonicalBytes가 따로 맞춘다).
func Normalize(d Declaration) Declaration {
	out := d
	out.Types = make([]TypeDeclaration, len(d.Types))
	for i, t := range d.Types {
		t.OwnerOnlyFields = append([]string{}, t.OwnerOnlyFields...)
		sort.Strings(t.OwnerOnlyFields)
		out.Types[i] = t
	}
	sort.SliceStable(out.Types, func(i, j int) bool { return out.Types[i].Type < out.Types[j].Type })
	return out
}

// CredentialField는 선언 칸 값 가운데 인증 헤더·쿠키·토큰·비밀번호 모양(dlp.CredentialText가 가리는 형식)이 든 첫 칸의
// 이름이다. 없으면 빈 값이다. 자격은 선언에 넣지 않고 등록된 시험 계정 세션에서만 붙는다(계약 §3). 돌려주는 이름에는 값이 없다.
func CredentialField(d Declaration) string {
	type field struct{ name, value string }
	fields := []field{{"identity.path", d.Identity.Path}, {"identity.subject_id_path", d.Identity.SubjectIDPath}}
	for i, t := range d.Types {
		p := fmt.Sprintf("types[%d].", i)
		fields = append(fields, field{p + "type", t.Type}, field{p + "create.path", t.Create.Path},
			field{p + "create.body_template", t.Create.BodyTemplate}, field{p + "create.marker_field", t.Create.MarkerField},
			field{p + "create.id_path", t.Create.IDPath}, field{p + "read.path_template", t.Read.PathTemplate},
			field{p + "read.data_path", t.Read.DataPath}, field{p + "read.status_path", t.Read.StatusPath},
			field{p + "read.normal_value", t.Read.NormalValue})
		for j, f := range t.OwnerOnlyFields {
			fields = append(fields, field{fmt.Sprintf("%sowner_only_fields[%d]", p, j), f})
		}
	}
	kinds := map[string]BodyKind{}
	for i, t := range d.Types {
		kinds[fmt.Sprintf("types[%d].create.body_template", i)] = t.Create.Kind
	}
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		for _, form := range credentialForms(f.name, f.value, kinds[f.name]) {
			if dlp.CredentialText(form) != form {
				return f.name
			}
		}
	}
	return ""
}

// credentialForms는 칸 값을 파서가 읽는 모양들로 풀어 돌려준다. 이스케이프로 자격 이름·값을 숨겨도 저장되는 값에서 드러나면
// 걸리게 한다: 본문 틀은 JSON·form으로 풀어 "키=값"·"키: 값"·값을, 경로는 퍼센트 인코딩을 푼 모양을 함께 본다.
func credentialForms(name, value string, kind BodyKind) []string {
	forms := []string{value}
	switch {
	case strings.HasSuffix(name, "create.body_template") && kind == BodyJSON:
		var parsed any
		dec := json.NewDecoder(strings.NewReader(value))
		dec.UseNumber()
		if dec.Decode(&parsed) == nil {
			forms = append(forms, jsonCredentialForms(parsed)...)
		}
	case strings.HasSuffix(name, "create.body_template") && kind == BodyForm:
		if values, err := url.ParseQuery(value); err == nil {
			for k, vs := range values {
				forms = append(forms, k)
				for _, v := range vs {
					forms = append(forms, k+"="+v, v)
				}
			}
		}
	case strings.HasSuffix(name, ".path") || strings.HasSuffix(name, ".path_template"):
		if decoded, err := url.PathUnescape(value); err == nil && decoded != value {
			forms = append(forms, decoded)
		}
	}
	return forms
}

func jsonCredentialForms(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			// 값 종류와 관계없이 키=<값 JSON>을 본다: 자격 이름 키에는 배열·객체·불리언·null 값도 자격 모양이다(#705 이경준 리뷰).
			if raw, err := json.Marshal(child); err == nil {
				out = append(out, k+"="+string(raw))
			}
			switch c := child.(type) {
			case string:
				out = append(out, k+"="+strconv.Quote(c), k+": "+c, c)
			case json.Number:
				out = append(out, k+"="+c.String())
			default:
				out = append(out, jsonCredentialForms(c)...)
			}
		}
	case []any:
		for _, child := range x {
			if c, ok := child.(string); ok {
				out = append(out, c)
				continue
			}
			out = append(out, jsonCredentialForms(child)...)
		}
	}
	return out
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
		OwnerAccountLabel      string            `json:"owner_account_label,omitempty"`
		CrossAccountLabel      string            `json:"cross_account_label,omitempty"`
		AccountsSamePrivilege  bool              `json:"accounts_same_privilege,omitempty"`
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
		Identity:   d.Identity, Types: types, OwnerAccountLabel: d.OwnerAccountLabel, CrossAccountLabel: d.CrossAccountLabel,
		AccountsSamePrivilege: d.AccountsSamePrivilege})
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

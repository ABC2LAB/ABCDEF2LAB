package diagnosisprep

import (
	"regexp"
	"time"

	"github.com/Autumn-27/artex/e3contract"
)

// DeclarationInput은 정책 선언 저장(declaration_put)의 입력이다(E③ 확정 경로, docs/contracts/e3-product-confirmation-interface.md §3).
// 선언자·시각·대상·대상 판·형식 판은 화면이 보내지 않고 준비 프로세스가 인증 주체·저장 시각·대상 기록으로 채운다.
// 대상 판 기대값은 ProductRequest.ExpectedRevision에 싣는다.
type DeclarationInput struct {
	Identity e3contract.IdentitySpec      `json:"identity"`
	Types    []e3contract.TypeDeclaration `json:"types"`
}

// 선언 상태다. none은 선언이 없음, active는 지금 대상 판에 묶인 유효 선언, invalid는 대상 판이 바뀌었거나(재승인·철회)
// 승인이 만료돼 무효인 선언이다.
const (
	DeclarationNone    = "none"
	DeclarationActive  = "active"
	DeclarationInvalid = "invalid"
)

// ProductDeclaration은 대상 하나의 정책 선언 조회 결과다. 동결 digest는 조회에 넣지 않는다(실행 시작 때 동결한다).
type ProductDeclaration struct {
	State          string    `json:"state"`
	InvalidReason  string    `json:"invalid_reason,omitempty"`
	TargetRevision uint64    `json:"target_revision,omitempty"`
	DeclaredBy     string    `json:"declared_by,omitempty"`
	DeclaredAt     time.Time `json:"declared_at,omitempty"`
	// Declaration은 state가 none이면 비어 있다. IPC는 null을 받지 않으므로 생략하고, 공개 API가 null로 투영한다.
	Declaration *e3contract.Declaration `json:"declaration,omitempty"`
}

// declarationField는 형식 오류 응답의 칸 이름 모양이다(e3contract.DeclarationFieldError·저장 API가 만드는 이름). 값은 담지 않는다.
var declarationField = regexp.MustCompile(`^[a-z_]+(\[[0-9]{1,3}\])?(\.[a-z_]+(\[[0-9]{1,3}\])?){0,3}$`)

// ValidDeclarationField는 형식 오류 응답의 칸 이름 검사다.
func ValidDeclarationField(s string) bool { return len(s) <= 96 && declarationField.MatchString(s) }

func validProductDeclaration(p ProductDeclaration) bool {
	switch p.State {
	case DeclarationNone:
		return p.InvalidReason == "" && p.TargetRevision == 0 && p.DeclaredBy == "" && p.DeclaredAt.IsZero() && p.Declaration == nil
	case DeclarationActive, DeclarationInvalid:
		if (p.State == DeclarationActive) != (p.InvalidReason == "") {
			return false
		}
		if p.State == DeclarationInvalid && p.InvalidReason != "target_reapproved" && p.InvalidReason != "target_expired" {
			return false
		}
		d := p.Declaration
		return d != nil && d.Validate() == nil && p.TargetRevision > 0 && p.DeclaredBy == d.DeclaredBy && p.DeclaredAt.Equal(d.DeclaredAt)
	}
	return false
}

// ResetRequest는 리셋 확인(reset_confirm)의 입력이다. 운영자가 진술(ProductResetState.Statement)에 동의했다는 뜻이고,
// WritesRef는 운영자가 화면에서 본 쓰기 기록 줄의 고유 참조(ProductResetState.LastWritesRef)다. 저장 시점의 기록과 다르면
// 반영하지 않는다.
type ResetRequest struct {
	StatementConfirmed bool   `json:"statement_confirmed"`
	WritesRef          string `json:"writes_ref"`
}

// ProductResetState는 대상의 확정 단계 쓰기와 리셋 확인 상태다(E③ 결정 §6.1). ResetRequired면 운영자가 리셋 진술을
// 남기기 전까지 다음 회차를 시작하지 않는다. 객체 ID·표식은 담지 않고 개수만 보인다.
type ProductResetState struct {
	ResetRequired     bool      `json:"reset_required"`
	LastWritesJob     string    `json:"last_writes_job,omitempty"`
	LastWritesAt      time.Time `json:"last_writes_at,omitempty"`
	LastWritesObjects int       `json:"last_writes_objects,omitempty"`
	// LastWritesRef는 마지막 쓰기 기록 줄의 고유 참조다. 리셋 동의(ResetRequest.WritesRef)에 그대로 보낸다.
	LastWritesRef string    `json:"last_writes_ref,omitempty"`
	ConfirmedBy   string    `json:"confirmed_by,omitempty"`
	ConfirmedAt   time.Time `json:"confirmed_at,omitempty"`
	Statement     string    `json:"statement"`
}

func validProductResetState(s ProductResetState) bool {
	if !validText(s.Statement, 1024) || s.LastWritesObjects < 0 || s.LastWritesObjects > e3contract.MaxDeclarationTypes {
		return false
	}
	if s.LastWritesJob == "" {
		return !s.ResetRequired && s.LastWritesAt.IsZero() && s.LastWritesObjects == 0 && s.LastWritesRef == "" && s.ConfirmedBy == "" &&
			s.ConfirmedAt.IsZero()
	}
	if !ValidResetWritesRef(s.LastWritesRef) {
		return false
	}
	confirmed := s.ConfirmedBy != ""
	return reference.MatchString(s.LastWritesJob) && !s.LastWritesAt.IsZero() && s.LastWritesObjects > 0 &&
		s.ResetRequired != confirmed && confirmed != s.ConfirmedAt.IsZero() && (!confirmed || reference.MatchString(s.ConfirmedBy))
}

// ValidResetWritesRef는 리셋 확인이 가리키는 쓰기 기록 줄 참조(SHA-256 소문자 16진)의 모양이다.
func ValidResetWritesRef(s string) bool { return writesRefPattern.MatchString(s) }

var writesRefPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

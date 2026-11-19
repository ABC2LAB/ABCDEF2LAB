package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// 대상 정책 선언(E③ 확정 경로, docs/e3-confirm-path-decision.md §4, docs/contracts/e3-product-confirmation-interface.md §3).
// 선언은 대상 등록에 붙고 보호 저널(declarations/)에 남는다. 이 저널이 감사 기록이다: 누가·언제·어느 대상 판에 무엇을
// 선언했는지. 대상 판이 바뀌면(재승인·철회) 저장된 선언은 남아도 무효로 읽는다(target_reapproved). 실행 시작 때의 동결은
// 이 저널의 마지막 유효 선언으로 한다(후속 PR).

const declarationDeclare = "declare"

// productDeclarationEvent는 선언 저널 한 줄이다. declare 줄은 선언·digest를, confirmation_writes 줄은 회차·객체를,
// reset_confirmed 줄은 진술 해시를 담는다. 보호 저널은 null을 받지 않으므로 해당 없는 칸은 생략한다.
type productDeclarationEvent struct {
	TargetID        string                      `json:"target_id"`
	Action          string                      `json:"action"`
	Actor           string                      `json:"actor"`
	At              time.Time                   `json:"at"`
	TargetRevision  uint64                      `json:"target_revision"`
	DigestSHA256    string                      `json:"digest_sha256,omitempty"`
	Declaration     *e3contract.Declaration     `json:"declaration,omitempty"`
	JobRef          string                      `json:"job_ref,omitempty"`
	Objects         []productConfirmationObject `json:"objects,omitempty"`
	StatementSHA256 string                      `json:"statement_sha256,omitempty"`
	// WritesRef는 reset_confirmed 줄이 동의한 쓰기 기록 줄의 고유 참조다.
	WritesRef string `json:"writes_ref,omitempty"`
}

type productDeclarationState struct {
	revision    uint64
	digest      string
	declaration e3contract.Declaration
}

// declarationInputError는 선언 입력이 계약 모양이 아닌 경우다. field는 칸 이름만이고 입력 값은 없다.
type declarationInputError struct{ field string }

func (e *declarationInputError) Error() string { return "invalid_declaration" }

func targetApprovalRevision(revision uint64) string { return strconv.FormatUint(revision, 10) }

// replayDeclaration은 저널 한 줄을 상태에 반영한다. 대상 저널을 먼저 다 읽은 뒤에 부른다. 형식·결속·digest를 다시 봐서
// 저널에 손댄 줄을 받지 않는다. 자격 모양 검사는 저장 때만 한다: 검사 규칙(dlp)이 넓어져 옛 줄이 걸리면 설치 전체가
// 열리지 않기 때문이다.
func (r *productTargetRegistry) replayDeclaration(e productDeclarationEvent) error {
	if e.Action == declarationConfirmationWrites || e.Action == declarationResetConfirmed || e.Action == declarationConfirmationWriteSettled {
		return r.replayReset(e)
	}
	rec, ok := r.records[e.TargetID]
	if e.Declaration == nil || e.JobRef != "" || len(e.Objects) != 0 || e.StatementSHA256 != "" || e.WritesRef != "" {
		return errPreparation
	}
	d := *e.Declaration
	if !ok || e.Action != declarationDeclare || e.Actor != rec.Principal || e.At.IsZero() || e.TargetRevision < 1 ||
		e.TargetRevision > rec.Target.Revision || d.Validate() != nil || d.TargetRef != e.TargetID ||
		d.TargetApprovalRevision != targetApprovalRevision(e.TargetRevision) || d.DeclaredBy != e.Actor ||
		!d.DeclaredAt.Equal(e.At) {
		return errPreparation
	}
	digest, err := d.Digest()
	if err != nil || digest != e.DigestSHA256 {
		return errPreparation
	}
	r.declarations[e.TargetID] = &productDeclarationState{revision: e.TargetRevision, digest: digest, declaration: e3contract.Normalize(d)}
	return nil
}

// declarationView는 지금 대상 판·시각 기준의 선언 상태다. 조회와 이후 실행 동결이 이 판정 하나를 쓴다.
func declarationView(rec productTargetRecord, st *productDeclarationState, now time.Time) diagnosisprep.ProductDeclaration {
	if st == nil {
		return diagnosisprep.ProductDeclaration{State: diagnosisprep.DeclarationNone}
	}
	d := e3contract.Normalize(st.declaration)
	v := diagnosisprep.ProductDeclaration{State: diagnosisprep.DeclarationActive, TargetRevision: st.revision,
		DeclaredBy: d.DeclaredBy, DeclaredAt: d.DeclaredAt, Declaration: &d}
	switch {
	case rec.Target.State != "approved" || rec.Target.Revision != st.revision:
		v.State, v.InvalidReason = diagnosisprep.DeclarationInvalid, "target_reapproved"
	case !now.Before(rec.Target.ExpiresAt):
		v.State, v.InvalidReason = diagnosisprep.DeclarationInvalid, "target_expired"
	}
	return v
}

func (r *productTargetRegistry) declaration(principal, id string) (diagnosisprep.ProductDeclaration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, _, err := r.accountTarget(principal, id)
	if err != nil {
		return diagnosisprep.ProductDeclaration{}, err
	}
	return declarationView(rec, r.declarations[id], r.now()), nil
}

// putDeclaration은 승인된 대상의 승인자가 선언을 통째로 저장한다. 화면이 본 대상 판(expected)과 지금 판이 같을 때만 그
// 판에 결속한다(2026-11-18 사용자 결정). 선언자·시각·대상·판은 서버가 채운다.
func (r *productTargetRegistry) putDeclaration(principal, id string, expected uint64, in diagnosisprep.DeclarationInput) (diagnosisprep.ProductDeclaration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, _, err := r.accountTarget(principal, id)
	if err != nil {
		return diagnosisprep.ProductDeclaration{}, err
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	if rec.Target.State != "approved" || !now.Before(rec.Target.ExpiresAt) {
		return diagnosisprep.ProductDeclaration{}, errors.New("target_not_approved")
	}
	if expected != rec.Target.Revision {
		return diagnosisprep.ProductDeclaration{}, errors.New("declaration_target_revision_mismatch")
	}
	d := e3contract.Normalize(e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: id,
		TargetApprovalRevision: targetApprovalRevision(rec.Target.Revision), DeclaredBy: principal, DeclaredAt: now,
		Identity: in.Identity, Types: in.Types})
	// 자격 모양을 먼저 본다. 형식 검사 오류보다 앞서야 자격이 든 칸을 정확히 가리킨다.
	if field := e3contract.CredentialField(d); field != "" {
		return diagnosisprep.ProductDeclaration{}, &declarationInputError{field: field}
	}
	if err := d.Validate(); err != nil {
		var fe *e3contract.DeclarationFieldError
		if !errors.As(err, &fe) {
			return diagnosisprep.ProductDeclaration{}, &declarationInputError{field: "declaration"}
		}
		return diagnosisprep.ProductDeclaration{}, &declarationInputError{field: fe.Field}
	}
	if field := declarationScopeField(rec, d); field != "" {
		return diagnosisprep.ProductDeclaration{}, &declarationInputError{field: field}
	}
	digest, err := d.Digest()
	if err != nil {
		return diagnosisprep.ProductDeclaration{}, errPreparation
	}
	e := productDeclarationEvent{TargetID: id, Action: declarationDeclare, Actor: principal, At: now,
		TargetRevision: rec.Target.Revision, DigestSHA256: digest, Declaration: &d}
	raw, err := json.Marshal(e)
	if err != nil || r.check() != nil {
		return diagnosisprep.ProductDeclaration{}, errPreparation
	}
	if err := r.declarationJournal.Append(raw); err != nil {
		return diagnosisprep.ProductDeclaration{}, errPreparation
	}
	if err := r.replayDeclaration(e); err != nil {
		return diagnosisprep.ProductDeclaration{}, errPreparation
	}
	return declarationView(rec, r.declarations[id], now), nil
}

// declarationScopeField는 선언의 요청이 대상 승인 범위(경로 접두·정규 경로·메서드) 안인지 보고, 밖이면 그 칸 이름이다.
// E²가 요청마다 다시 보지만, 범위 밖 선언은 확정 단계에서 늘 거부되므로 저장 때 알린다.
func declarationScopeField(rec productTargetRecord, d e3contract.Declaration) string {
	methods := map[string]bool{}
	for _, m := range rec.Target.Methods {
		methods[m] = true
	}
	within := func(p string) bool {
		v, err := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: rec.Target.Origin + p, ProfileID: "target"})
		if err != nil || v.TargetURL != rec.Target.Origin+p {
			return false
		}
		for _, prefix := range rec.Target.PathPrefixes {
			if urlPathWithin(p, prefix) {
				return true
			}
		}
		return false
	}
	if !methods["GET"] || !within(d.Identity.Path) {
		return "identity.path"
	}
	for i, t := range d.Types {
		if !t.Confirmable() {
			continue
		}
		prefix := fmt.Sprintf("types[%d].", i)
		switch {
		case !methods[t.Create.Method]:
			return prefix + "create.method"
		case !within(t.Create.Path):
			return prefix + "create.path"
		// 경로 틀의 {id}는 조각 하나를 대신하므로 자리표시 값으로 바꿔 접두·정규 경로를 본다.
		case !within(strings.Replace(t.Read.PathTemplate, e3contract.IDPlaceholder, "0", 1)):
			return prefix + "read.path_template"
		}
	}
	return ""
}

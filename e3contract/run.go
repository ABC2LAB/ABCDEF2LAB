package e3contract

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// Account는 C14 인증 준비가 끝난 시험 계정이다. 서버가 실행 결속에서 채운다.
type Account struct {
	Label         string
	Authenticated bool
}

// Input은 확정 단계 한 회의 입력이다. 모두 서버가 실행 결속에서 채우고 모델이 쓴 값은 없다.
type Input struct {
	Binding   RunBinding
	TargetRef string
	// WriteConsent는 대상이 쓰기를 승인했고 리셋할 수 있다고 등록됐는지다(#596 §3 조건 1).
	WriteConsent bool
	Owner, Cross Account
	// PolicyValidFrom·PolicyValidUntil은 대상 승인 기간과 실행 기간이 겹치는 구간이다(#596 §4.2).
	PolicyValidFrom, PolicyValidUntil time.Time
	Candidates                        []Candidate
	// AdmittedTypes는 실행 시작 때 떼어 둔 확정 몫에 든 종류 수다(사용자 결정 2026-11-18 ②: 요청·시간 모두 실행 상한의
	// 1/4 안). 확정할 종류가 이보다 많으면 넘치는 종류는 요청을 보내지 않고 UNDECIDABLE(예산)로 남는다. 0이면 아무
	// 종류도 들지 않은 것이다.
	AdmittedTypes int
	// RequestTimeout은 확정 요청 하나의 전용 시간 상한이다(E② 요청 시간 상한과 따로, 파일럿 실측 뒤 확정).
	RequestTimeout time.Duration
}

// Deps는 확정 단계가 부르는 갈래들이다. 하나라도 없으면 확정 단계를 돌지 않는다.
type Deps struct {
	Transport    Transport
	Journal      Journal
	Declarations DeclarationReader
	Reset        ResetGate
	// Writes는 생성 요청의 쓰기 기록이다(기록하고 나서 보내기). 없으면 연결 미구현(not_wired)이다.
	Writes WriteRecorder
	// Random은 표식과 요청 본문 칸 객체 ID(id_field)의 난수원이다. 없으면 crypto/rand다(제품 생성자는 두지 않는다).
	Random io.Reader
	// Now는 관찰이 하나도 없을 때 판정 시각이다. 없으면 time.Now다.
	Now func() time.Time
	// Logger는 상태·건수만 남긴다. 없으면 남기지 않는다.
	Logger *slog.Logger
	// Started는 확정 단계가 실제로 대상 요청을 시작하기 직전(표식을 만들기 전)에 한 번 불린다. 꺼짐·시작 거절·몫에 든
	// 종류 없음이면 부르지 않는다. 감독자는 이때만 엔진을 정리 상태로 두어, 꺼진 회차의 흐름이 E③가 없을 때와 같게 한다.
	Started func()
	// Relogin은 신원 확인 직전에 시험 계정 하나를 다시 로그인시킨다(사용자 결정 2026-11-20 (가)). 대상에 실제로 보낸 요청
	// 수를 돌려준다(확정 몫으로 센다). 학습 로그인 재사용은 로그인 화면 GET·대조 GET까지 둘 이상일 수 있다(모델 로그인 계약
	// §6-2). 결과는 판정 근거가 아니고 바로 뒤의 신원 확인이 근거다. 없으면 다시 로그인하지 않는다.
	Relogin func(ctx context.Context, label string) (requests int)
}

// Run은 모델 단계가 끝난 뒤 확정 단계 한 회를 돈다(#596 §5). 순서: 조건 → 정책 동결본 대조 → 리셋 확인 → 표식 생성·등록 →
// 신원 확인 → 종류마다 생성·A 조회·B 교차 → 판정 → 후보 연결. 결속·진위가 깨지면 그때까지의 판정을 버리고
// StatusIntegrityFailed로 끝낸다.
func Run(ctx context.Context, deps Deps, in Input) RunResult {
	r := &run{deps: deps, in: in, seen: map[string]bool{}}
	res := r.run(ctx)
	if deps.Logger != nil {
		deps.Logger.Info("E③ 확정 단계 끝", "status", string(res.Status), "reason", res.Reason,
			"verdicts", len(res.Verdicts), "requests", res.Requests)
	}
	return res
}

// RunStopped는 확정 단계를 돌리지 못하고 실행이 끝났을 때(중지·취소·실패·시간 초과 마감) 그 회차의 확정 단계 상태를 요청
// 없이 정한다(#738 이동찬 리뷰). 비활성 근거가 있으면 Run과 같은 disabled(사유)·start_refused·진위 실패 결과를 돌려주고,
// 비활성 근거가 없어 확정 단계가 돌 수 있었으면 interrupted(사유 없음)다. 표식 등록·Transport·Journal·쓰기 기록을 부르지
// 않으므로 대상 요청은 0건이다. 결과 파일이 없을 때 비활성으로 추정하지 않도록, 감독자는 중지 마감에서도 이것을 기록한다.
func RunStopped(ctx context.Context, deps Deps, in Input) RunResult {
	r := &run{deps: deps, in: in, seen: map[string]bool{}, stoppedBeforeStage: true}
	res := r.run(ctx)
	if deps.Logger != nil {
		deps.Logger.Info("E③ 확정 단계 중지 기록", "status", string(res.Status), "reason", res.Reason)
	}
	return res
}

// RunWithoutExecution은 모델 단계가 E② 실행을 한 번도 잡지 않은 채 스스로 끝났을 때(intent 없음 등) 확정 단계 상태를 요청
// 없이 정한다(사용자 결정 2026-11-20 분기점 2). 비활성 근거가 있으면 RunStopped와 같은 결과(꺼진 회차는 실제 사유)이고,
// 확정 단계가 돌 수 있었으면 start_refused(e2_execution_unavailable)다(2026-11-22 F12: 후보 유무로 가르지 않고, 인증
// 준비를 안 했으니 계정 인증 결과 없음으로 끄지 않는다). 표식 등록·Transport·Journal·쓰기 기록을 부르지 않는다.
func RunWithoutExecution(ctx context.Context, deps Deps, in Input) RunResult {
	r := &run{deps: deps, in: in, seen: map[string]bool{}, stoppedBeforeStage: true, noExecution: true}
	res := r.run(ctx)
	if deps.Logger != nil {
		deps.Logger.Info("E③ 확정 단계 실행 없음 기록", "status", string(res.Status), "reason", res.Reason)
	}
	return res
}

type run struct {
	deps Deps
	// stoppedBeforeStage는 RunStopped다. 요청을 시작하기 전(비활성 판정까지만) 멈춘다.
	stoppedBeforeStage bool
	// noExecution은 RunWithoutExecution이다. E② 실행이 한 번도 없어 요청을 시작하지 않는다.
	noExecution bool
	in          Input
	marker      Marker
	// random은 이 회차의 난수원이다(Deps.Random, 없으면 crypto/rand). 표식과 요청 본문 칸 ID가 같은 출처에서 새 바이트를 읽는다.
	random    io.Reader
	decl      Declaration
	requests  int
	seen      map[string]bool
	integrity IntegrityReason
	budget    bool // 확정 단계 전용 몫이 다 됐다. 더 보내지 않는다.
	stopped   bool // 회차 중지로 끊겼다.
	lastAt    time.Time
	// bindingValid는 실행 결속이 검증을 통과했는지다. 통과한 결속만 결과에 싣는다.
	bindingValid bool
	// writes는 생성 요청이 대상에 남겼을 수 있는 쓰기다. 진위 실패로 판정을 버려도 남긴다.
	writes []ConfirmationWrite
	// objectDigest는 지금 확정 중인 종류의 생성 객체 ID digest다(confirmType 안에서만).
	objectDigest string
}

func (r *run) result(status ConfirmationStatus, reason string) RunResult {
	res := RunResult{Version: RunResultVersion, Status: status, Reason: reason, Verdicts: []Verdict{}, Skipped: []SkippedType{},
		Candidates: r.suspectedCandidates(""), Requests: r.requests, Writes: append([]ConfirmationWrite{}, r.writes...)}
	if r.bindingValid {
		b := r.in.Binding
		res.Binding = &b
	}
	return res
}

func (r *run) integrityResult() RunResult {
	res := r.result(StatusIntegrityFailed, string(r.integrity))
	res.Candidates = r.suspectedCandidates(string(StatusIntegrityFailed))
	return res
}

func (r *run) suspectedCandidates(reason string) []CandidateConfirmation {
	out := make([]CandidateConfirmation, 0, len(r.in.Candidates))
	for _, c := range r.validCandidates() {
		out = append(out, CandidateConfirmation{CandidateRef: c.Ref, Level: oracle.Suspected, Reason: reason})
	}
	return out
}

// validCandidates는 참조가 계약 모양인 후보만이다. 모양이 틀린 후보 참조는 결과에 옮기지 않는다.
func (r *run) validCandidates() []Candidate {
	var out []Candidate
	for _, c := range r.in.Candidates {
		if refPattern.MatchString(c.Ref) {
			out = append(out, c)
		}
	}
	return out
}

func (r *run) run(ctx context.Context) RunResult {
	if reason := r.disabledReason(); reason != "" {
		return r.result(StatusDisabled, string(reason))
	}
	decl, err := r.deps.Declarations.ReadFrozen(ctx, r.in.Binding.Policy)
	if err != nil {
		return r.result(StatusStartRefused, string(RefusePolicySourceUnavailable))
	}
	if VerifyFrozen(r.in.Binding.Policy, decl) != nil {
		r.integrity = IntegrityPolicyDigestMismatch
		return r.integrityResult()
	}
	if decl.TargetRef != r.in.TargetRef {
		r.integrity = IntegrityPolicyTargetMismatch
		return r.integrityResult()
	}
	// 계정 역할은 동결한 선언이 정한다(계약 §4). 역할이 없거나, 입력의 A·B가 선언한 역할과 다르거나, 같은 권한 등급 확인이
	// 없으면 요청 없이 끈다.
	if decl.OwnerAccountLabel == "" {
		return r.result(StatusDisabled, string(DisabledAccountRolesUndeclared))
	}
	if decl.OwnerAccountLabel != r.in.Owner.Label || decl.CrossAccountLabel != r.in.Cross.Label {
		return r.result(StatusDisabled, string(DisabledAccountsUnverified))
	}
	if !decl.AccountsSamePrivilege {
		return r.result(StatusDisabled, string(DisabledPrivilegeUnconfirmed))
	}
	r.decl = decl
	var types []TypeDeclaration
	var skipped []SkippedType
	for _, t := range decl.Types {
		if t.Confirmable() {
			types = append(types, t)
		} else {
			skipped = append(skipped, SkippedType{ResourceType: t.Type, Reason: "not_owner_only"})
		}
	}
	if len(types) == 0 {
		return r.result(StatusDisabled, string(DisabledNoConfirmableType))
	}
	// 중지 기록(RunStopped): 비활성 근거가 없어 확정 단계가 돌 수 있었다. 요청을 시작하지 않고 중단으로 남긴다.
	if r.stoppedBeforeStage {
		if r.noExecution {
			// E② 실행이 한 번도 없었다(모델 단계가 진단 의도를 하나도 실행하지 않음 등). 확정 단계가 돌 수 있었는데 E② 실행이
			// 없어 시작하지 못한 것이다(e2_execution_unavailable, 사용자 결정 2026-11-22 F12). 제품이 후보를 넘기지 않아(이번 판
			// 미구현) 후보 유무로 가르면 늘 후보 없음이 되므로 가르지 않는다.
			return r.result(StatusStartRefused, string(RefuseE2ExecutionUnavailable))
		}
		return r.result(StatusInterrupted, "")
	}
	// 몫에 들지 않은 종류는 요청을 보내지 않고 UNDECIDABLE(예산)이다. 든 종류가 없으면 표식·신원 요청도 보내지 않는다.
	admitted, overflow := types, []TypeDeclaration(nil)
	if len(types) > r.in.AdmittedTypes {
		admitted, overflow = types[:r.in.AdmittedTypes], types[r.in.AdmittedTypes:]
	}
	if len(admitted) == 0 {
		return r.budgetOnly(skipped, overflow)
	}
	types = admitted
	if refusal := CheckRunStart(ctx, r.deps.Reset, r.in.Binding, r.in.TargetRef); refusal != "" {
		return r.result(StatusStartRefused, string(refusal))
	}
	if r.deps.Started != nil {
		r.deps.Started()
	}
	random := r.deps.Random
	if random == nil {
		random = rand.Reader
	}
	r.random = random
	marker, err := NewMarker(random)
	if err != nil {
		return r.result(StatusStartRefused, string(RefuseMarkerGenerationFailed))
	}
	if err := r.deps.Transport.RegisterMarker(ctx, r.in.Binding, marker); err != nil {
		if errors.Is(err, ErrAuthorityRevoked) {
			r.integrity = IntegrityAuthorityRevoked
			return r.integrityResult()
		}
		return r.result(StatusStartRefused, string(RefuseMarkerRegistrationFailed))
	}
	r.marker = marker
	r.relogin(ctx)

	subjects, identityReason, evidence := r.identity(ctx)
	if r.integrity != "" {
		return r.integrityResult()
	}
	var verdicts []Verdict
	for _, t := range types {
		if r.stopped {
			break
		}
		var v Verdict
		if identityReason != "" {
			v = r.verdict(t, subjects, unknownFacts(), evidence, identityReason)
		} else {
			v = r.confirmType(ctx, t, subjects, evidence)
		}
		if r.integrity != "" {
			return r.integrityResult()
		}
		if r.stopped && v.ResourceType == "" {
			break
		}
		verdicts = append(verdicts, v)
	}
	if !r.stopped {
		for _, t := range overflow {
			verdicts = append(verdicts, r.verdict(t, unknownSubjects(), unknownFacts(), nil, ReasonConfirmationBudgetExhaust))
		}
	}
	status := StatusCompleted
	if r.stopped {
		status = StatusInterrupted
	}
	res := r.result(status, "")
	res.Verdicts = verdicts
	if res.Verdicts == nil {
		res.Verdicts = []Verdict{}
	}
	if skipped != nil {
		res.Skipped = skipped
	}
	res.Candidates = r.linkCandidates(types, verdicts)
	// 판정의 후보 참조는 검증된 연결에서만 채운다. 한 종류에 후보 여럿이 이어질 수 있다.
	for i := range res.Verdicts {
		for _, c := range res.Candidates {
			if c.ResourceType == res.Verdicts[i].ResourceType && c.Reason == "" {
				res.Verdicts[i].CandidateRefs = append(res.Verdicts[i].CandidateRefs, c.CandidateRef)
			}
		}
	}
	return res
}

func (r *run) disabledReason() DisabledReason {
	d := r.deps
	if d.Transport == nil || d.Journal == nil || d.Declarations == nil || d.Reset == nil || d.Writes == nil {
		return DisabledNotWired
	}
	if r.in.Binding.Policy == (PolicySource{}) {
		return DisabledNoDeclaration
	}
	if r.in.Binding.Validate() != nil || !refPattern.MatchString(r.in.TargetRef) {
		return DisabledBindingInvalid
	}
	r.bindingValid = true
	if r.in.Binding.Policy.ValidateProduct() != nil {
		return DisabledPolicyKind
	}
	if !r.in.WriteConsent {
		return DisabledWriteNotConsented
	}
	// 계정 역할은 동결한 선언이 정한다(계약 §4). 입력의 A·B가 둘 다 비어 있으면 선언에 역할이 없을 수 있으니 선언을 읽은 뒤
	// 사유를 정한다(역할 없음 account_roles_undeclared / 계정 없음 test_accounts_unavailable). 요청은 그 전에 나가지 않는다.
	// E② 실행이 한 번도 없었으면(실행 없음 기록) 인증 준비도 하지 않았다(인증 준비는 E② 실행을 잡을 때 한다). 그때 인증 결과가
	// 없는 것은 계정 실패가 아니므로 test_accounts_unavailable로 가리지 않는다(리허설 F12). 라벨 형식은 그대로 본다.
	o, c := r.in.Owner, r.in.Cross
	unauthenticated := !r.noExecution && (!o.Authenticated || !c.Authenticated)
	if (o.Label != "" || c.Label != "") && (unauthenticated || !refPattern.MatchString(o.Label) ||
		!refPattern.MatchString(c.Label) || o.Label == c.Label) {
		return DisabledAccountsUnverified
	}
	if r.in.PolicyValidFrom.IsZero() || !r.in.PolicyValidFrom.Before(r.in.PolicyValidUntil) {
		return DisabledPolicyWindow
	}
	if r.in.AdmittedTypes < 0 || r.in.RequestTimeout <= 0 {
		return DisabledShareUnavailable
	}
	return ""
}

// budgetOnly는 확정할 종류가 있지만 몫에 하나도 들지 않은 회차다. 요청을 보내지 않고 종류마다 UNDECIDABLE(예산)이다.
func (r *run) budgetOnly(skipped []SkippedType, overflow []TypeDeclaration) RunResult {
	res := r.result(StatusCompleted, "")
	for _, t := range overflow {
		res.Verdicts = append(res.Verdicts, r.verdict(t, unknownSubjects(), unknownFacts(), nil, ReasonConfirmationBudgetExhaust))
	}
	if skipped != nil {
		res.Skipped = skipped
	}
	return res
}

func unknownSubjects() oracle.SubjectContext {
	return oracle.SubjectContext{OwnerStatus: oracle.SubjectUnknown, EvaluatedStatus: oracle.SubjectUnknown}
}

// call은 요청 하나를 부르고 관찰을 검증한다. ok=false면 그 요청의 결과를 쓸 수 없다(결과 불명이거나 진위 실패).
// 진위 실패는 r.integrity에 남는다.
func (r *run) call(ctx context.Context, want Observation, send func() (Observation, error)) (Observation, bool) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.budget = true // 확정 단계 시간 몫이 끝났다.
		} else {
			r.stopped = true
		}
		return Observation{}, false
	}
	r.requests++
	obs, err := send()
	if errors.Is(err, ErrAuthorityRevoked) {
		r.integrity = IntegrityAuthorityRevoked
		return Observation{}, false
	}
	// E²가 돌려준 기록이 계약 모양이 아니거나 요청과 맞지 않는 것은 결과 불명이 아니라 진위 실패다(v3 계약 §7).
	if errors.Is(err, ErrObservation) {
		r.integrity = IntegrityObservationInvalid
		return Observation{}, false
	}
	if errors.Is(err, ErrObservationMismatch) {
		r.integrity = IntegrityObservationMismatch
		return Observation{}, false
	}
	if err != nil {
		return Observation{}, false // 결과 불명. 다시 보내지 않는다.
	}
	if obs.Validate() != nil {
		r.integrity = IntegrityObservationInvalid
		return Observation{}, false
	}
	if obs.Binding != r.in.Binding {
		r.integrity = IntegrityBindingMismatch
		return Observation{}, false
	}
	if obs.Purpose != want.Purpose || obs.Role != want.Role || obs.AccountLabel != want.AccountLabel ||
		obs.ResourceType != want.ResourceType || obs.Create != want.Create {
		r.integrity = IntegrityObservationMismatch
		return Observation{}, false
	}
	key := obs.Ref.CallRef + "\x00" + obs.Ref.AttemptID
	if r.seen[key] {
		r.integrity = IntegrityObservationReused
		return Observation{}, false
	}
	r.seen[key] = true
	// 대상에 보내지 않은 관찰은 저널 대조도 대상 요청 없는 로컬 검증이다. 승인 만료처럼 확정 단계 시간 몫이 끝나는 순간에 온
	// 거부를 끝난 문맥으로 대조하면 검증을 못 마쳐 보내지 않은 생성이 쓰기 unknown(리셋 확인 대상)으로 남는다(리허설 F15).
	// 그래서 취소 없는 문맥에 요청 시간 상한을 걸어 끝까지 대조한다. 보낸 관찰은 지금처럼 호출자 문맥이다(중지·시간 몫 끝 처리).
	sealCtx, cancelSeal := ctx, context.CancelFunc(func() {})
	if !obs.Sent {
		sealCtx, cancelSeal = context.WithTimeout(context.WithoutCancel(ctx), r.in.RequestTimeout)
	}
	sealed, err := r.deps.Journal.Sealed(sealCtx, r.in.Binding, obs.Ref)
	cancelSeal()
	if err != nil {
		// 실행 문맥이 끝나(운영자 중지·확정 단계 시간 몫 끝) 조회를 마치지 못한 것은 무결성 실패가 아니라 중지다(계약 §12, v3
		// 계약 §5). 검증하지 못한 관찰은 쓰지 않는다. 문맥이 살아 있는데 취소 오류가 오면 아래처럼 무결성 실패다.
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				r.budget = true
			} else {
				r.stopped = true
			}
			return Observation{}, false
		}
		// 검증된 스냅샷에 이벤트가 없는 것과 저널을 검증하지 못한 것을 나눠 남긴다(v3 계약 §5·§7). 그 밖의 오류도 검증을
		// 마치지 못한 것이라 무결성 실패로 본다. 어느 쪽이든 진위 실패라 판정 행을 만들지 않는다.
		r.integrity = IntegrityJournalIntegrityFailed
		if errors.Is(err, ErrJournalEventMissing) {
			r.integrity = IntegrityJournalEventMissing
		} else if errors.Is(err, ErrJournalBindingMismatch) {
			r.integrity = IntegrityBindingMismatch
		}
		return Observation{}, false
	}
	if !sealed.Same(obs) {
		r.integrity = IntegrityJournalEventMismatch
		return Observation{}, false
	}
	if obs.At.After(r.lastAt) {
		r.lastAt = obs.At
	}
	if obs.NotSent == NotSentBudgetExhausted {
		r.budget = true
	}
	return obs, true
}

// relogin은 신원 확인 직전에 A·B를 다시 로그인시킨다(사용자 결정 2026-11-20 (가)). 모델 단계가 길어 대상이 준 세션이
// 만료됐어도 확정 단계가 새 세션으로 돌게 하려는 것이다. 다시 로그인이 실패하거나 보내지 못해도 멈추지 않는다: 바로 뒤의
// 신원 확인이 그 세션으로 판정 근거를 정한다(만료면 product_identity_unverified). 보낸 요청은 확정 몫으로 센다. 문맥이
// 끝났으면 보내지 않는다(뒤의 신원 확인이 중지·시간 몫 끝으로 처리한다).
func (r *run) relogin(ctx context.Context) {
	if r.deps.Relogin == nil {
		return
	}
	for _, label := range []string{r.in.Owner.Label, r.in.Cross.Label} {
		if ctx.Err() != nil {
			return
		}
		if n := r.deps.Relogin(ctx, label); n > 0 {
			r.requests += n
		}
	}
}

// identity는 A·B 신원을 확인한다(#596 §5.1). 실패하면 그 대상의 모든 종류가 UNDECIDABLE이고 생성·조회를 하지 않는다.
func (r *run) identity(ctx context.Context) (oracle.SubjectContext, ProductReason, []CallRef) {
	subjects := oracle.SubjectContext{OwnerStatus: oracle.SubjectUnknown, EvaluatedStatus: oracle.SubjectUnknown}
	var evidence []CallRef
	digests := map[Role]string{}
	reason := ProductReason("")
	for _, acct := range []struct {
		role  Role
		label string
	}{{RoleOwner, r.in.Owner.Label}, {RoleCross, r.in.Cross.Label}} {
		if r.budget {
			return subjects, ReasonConfirmationBudgetExhaust, evidence
		}
		want := Observation{Purpose: PurposeIdentity, Role: acct.role, AccountLabel: acct.label}
		obs, ok := r.call(ctx, want, func() (Observation, error) {
			return r.deps.Transport.Identity(ctx, IdentityRequest{Binding: r.in.Binding, Spec: r.decl.Identity,
				Role: acct.role, AccountLabel: acct.label, Timeout: r.in.RequestTimeout})
		})
		if r.integrity != "" || r.stopped {
			return subjects, ReasonIdentityUnverified, evidence
		}
		if r.budget {
			return subjects, ReasonConfirmationBudgetExhaust, evidence
		}
		if ok {
			evidence = append(evidence, obs.Ref)
		}
		// 신원 요청도 확정 요청이다. 시간 상한으로 끊겼으면 결과 불명의 한 갈래(product_request_timeout)로 남겨 상한 실측
		// 집계에 들어가게 한다(#727 이동찬 리뷰). 다른 계정이 다른 이유로 실패해도 시간 초과를 우선한다.
		if ok && obs.Sent && obs.TimedOut {
			reason = ReasonRequestTimeout
			continue
		}
		if !ok || !obs.Sent || !obs.ResponseComplete || obs.Normal != oracle.StateYes || obs.SubjectDigest == "" {
			if reason == "" {
				reason = ReasonIdentityUnverified
			}
			continue
		}
		digests[acct.role] = obs.SubjectDigest
	}
	if reason != "" {
		return subjects, reason, evidence
	}
	subjects = oracle.SubjectContext{OwnerStatus: oracle.SubjectVerified, EvaluatedStatus: oracle.SubjectVerified,
		OwnerRef: digests[RoleOwner], EvaluatedRef: digests[RoleCross]}
	if digests[RoleOwner] == digests[RoleCross] {
		return subjects, ReasonSubjectsIdentical, evidence
	}
	return subjects, "", evidence
}

// confirmType은 종류 하나를 생성 → A 조회 → B 교차로 시험한다. 앞 단계가 판정에 쓸 수 없는 결과면 뒤 요청을 보내지 않는다.
func (r *run) confirmType(ctx context.Context, t TypeDeclaration, subjects oracle.SubjectContext, identityEvidence []CallRef) Verdict {
	// 이 종류의 판정에만 생성 객체 digest를 싣는다. 다른 종류·종류 밖 판정에 남지 않게 끝나면 지운다.
	r.objectDigest = ""
	defer func() { r.objectDigest = "" }()
	evidence := append([]CallRef(nil), identityEvidence...)
	facts := unknownFacts()
	if r.budget {
		return r.verdict(t, subjects, facts, evidence, ReasonConfirmationBudgetExhaust)
	}
	// 요청 본문 칸 ID(id_field)면 회차·종류마다 표식과 따로 새 난수 ID를 만든다. 본문은 확정 단계가 틀에 표식·ID를 넣어 만든다.
	var objectID string
	if t.Create.IDField != "" {
		id, err := NewObjectID(r.random, r.marker)
		if err != nil {
			return r.verdict(t, subjects, facts, evidence, ReasonCreateRequestUnavailable)
		}
		objectID = id
	}
	body, err := AssembleBody(t.Create, r.marker, objectID)
	if err != nil {
		return r.verdict(t, subjects, facts, evidence, ReasonCreateRequestUnavailable)
	}
	// 기록하고 나서 보낸다: 생성 요청 전에 쓰기를 먼저 남긴다(id_field면 그 ID와 함께, 아니면 ID 모름). 남기지 못하면 보내지 않는다.
	if err := r.deps.Writes.Reserve(ctx, r.in.Binding, t.Type, objectID); err != nil {
		return r.verdict(t, subjects, facts, evidence, ReasonWriteRecordUnavailable)
	}
	before := r.requests
	// 생성은 한 번만 보낸다. ID 충돌 등으로 실패해도 다른 ID로 다시 보내지 않는다(그 종류는 UNDECIDABLE).
	created, ok := r.call(ctx, Observation{Purpose: PurposeCreate, Role: RoleOwner, AccountLabel: r.in.Owner.Label,
		ResourceType: t.Type}, func() (Observation, error) {
		return r.deps.Transport.Create(ctx, CreateRequest{Binding: r.in.Binding, ResourceType: t.Type, Spec: t.Create,
			AccountLabel: r.in.Owner.Label, Marker: r.marker, Body: body, ObjectID: objectID, Timeout: r.in.RequestTimeout})
	})
	r.settleWrite(ctx, r.recordWrite(t.Type, objectID, created, ok, r.requests > before))
	if r.integrity != "" || r.stopped && !ok {
		return Verdict{}
	}
	if !ok {
		return r.verdict(t, subjects, facts, evidence, r.unusableReason(Observation{}, false, true))
	}
	evidence = append(evidence, created.Ref)
	if reason := r.notUsable(created, true); reason != "" {
		return r.verdict(t, subjects, facts, evidence, reason)
	}
	if created.Normal != oracle.StateYes || created.ObjectIDDigest == "" {
		return r.verdict(t, subjects, facts, evidence, ReasonCreateFailed)
	}
	r.objectDigest = created.ObjectIDDigest

	owner, reason := r.read(ctx, t, RoleOwner, r.in.Owner.Label, created.Ref, objectID)
	if r.integrity != "" || r.stopped && reason == "" && owner == (Observation{}) {
		return Verdict{}
	}
	if owner != (Observation{}) {
		evidence = append(evidence, owner.Ref)
	}
	if reason != "" {
		return r.verdict(t, subjects, facts, evidence, reason)
	}
	facts.OwnerNormalData = owner.Normal
	if owner.Marker.Presence == MarkerAbsent {
		// A에서 지원 방식으로 표식이 안 보이면 B의 미관찰을 차단 근거로 쓸 수 없다. B 요청을 보내지 않는다.
		return r.verdict(t, subjects, facts, evidence, ReasonOwnerMarkerUnobserved)
	}
	if owner.Normal != oracle.StateYes {
		return r.verdict(t, subjects, facts, evidence, ReasonDataCheckFailed)
	}

	cross, reason := r.read(ctx, t, RoleCross, r.in.Cross.Label, created.Ref, objectID)
	if r.integrity != "" || r.stopped && reason == "" && cross == (Observation{}) {
		return Verdict{}
	}
	if cross != (Observation{}) {
		evidence = append(evidence, cross.Ref)
	}
	if reason != "" {
		return r.verdict(t, subjects, facts, evidence, reason)
	}
	// 조회 둘은 같은 생성 관찰을 읽었다(call이 Create 참조를 대조했다).
	facts.SameResource = oracle.StateYes
	facts.CrossNormalData = cross.Normal
	if CrossDenied(cross.StatusCode) {
		// 거부 응답은 상태를 먼저 본다. 표식 검사가 "있음"인 거부는 read가 모순으로 돌려보냈다. 거부가 접근 통제 때문인지
		// 세션 만료 때문인지 가리려고 B 신원을 한 번 더 확인한다(사용자 결정 2026-11-20 S21 (나)). 살아 있을 때만 접근
		// 거부(정상 데이터 아님)이고, 만료면 세션 만료, 확인하지 못하면 확인 못 함으로 남긴다(둘 다 B 신원 미확인 → UNDECIDABLE).
		recheck, reason := r.recheckCross(ctx, subjects)
		if r.integrity != "" {
			return Verdict{}
		}
		if recheck != (Observation{}) {
			evidence = append(evidence, recheck.Ref)
		}
		if reason != "" {
			unverified := subjects
			unverified.EvaluatedStatus = oracle.SubjectUnknown
			facts.CrossNormalData = oracle.StateUnknown
			return r.verdict(t, unverified, facts, evidence, reason)
		}
		facts.CrossNormalData = oracle.StateNo
	}
	if cross.Marker.Status == MarkerComplete && cross.Marker.Presence == MarkerPresent {
		facts.NontrivialDataMatch = oracle.StateYes
	} else {
		facts.NontrivialDataMatch = oracle.StateNo
	}
	// B 요청은 프로그램이 만들고, E²가 보낸 요청에 표식이 없음을 확인했을 때만 반사를 배제한다.
	if cross.RequestMarkerFree {
		facts.ReflectionExcluded = oracle.StateYes
	} else {
		facts.ReflectionExcluded = oracle.StateNo
	}
	// 표식은 이 회차에 새로 만든 무작위 값이고 A의 객체 한 칸에만 넣었다. 공통 화면 틀이 담을 수 없다.
	facts.TemplateExcluded = oracle.StateYes
	return r.verdict(t, subjects, facts, evidence, "")
}

// recheckCross는 교차 조회가 거부로 끝났을 때 B 신원을 한 번 더 확인한다(확정 몫의 종류당 1건, 거부일 때만 씀). 처음
// 신원과 같은 주체가 정상 응답하면 사유 없음(살아 있음), 신원 응답이 401·403이면 세션 만료, 그 밖에는 확인 못 함이다.
// 만료여도 다시 로그인해서 다시 조회하지 않는다: 확정 직전에 이미 다시 로그인했으므로 확정 단계 몇 초 사이에 만료되는
// 것은 수명이 비정상적으로 짧은 대상뿐이고, 사유만 정확히 남긴다.
func (r *run) recheckCross(ctx context.Context, subjects oracle.SubjectContext) (Observation, ProductReason) {
	if r.budget || r.stopped {
		return Observation{}, ReasonCrossDenialUnverified
	}
	want := Observation{Purpose: PurposeIdentity, Role: RoleCross, AccountLabel: r.in.Cross.Label}
	obs, ok := r.call(ctx, want, func() (Observation, error) {
		return r.deps.Transport.Identity(ctx, IdentityRequest{Binding: r.in.Binding, Spec: r.decl.Identity,
			Role: RoleCross, AccountLabel: r.in.Cross.Label, Timeout: r.in.RequestTimeout})
	})
	if r.integrity != "" {
		return Observation{}, ""
	}
	if !ok {
		return Observation{}, ReasonCrossDenialUnverified
	}
	if !obs.Sent || obs.TimedOut || !obs.ResponseComplete {
		return obs, ReasonCrossDenialUnverified
	}
	if obs.Normal == oracle.StateYes && obs.SubjectDigest != "" && obs.SubjectDigest == subjects.EvaluatedRef {
		return obs, ""
	}
	if obs.StatusCode == 401 || obs.StatusCode == 403 {
		return obs, ReasonSessionExpired
	}
	return obs, ReasonCrossDenialUnverified
}

// read는 조회 하나를 부르고, 판정에 쓸 수 없으면 사유를 돌려준다. 관찰을 못 받았으면 빈 관찰이다.
func (r *run) read(ctx context.Context, t TypeDeclaration, role Role, label string, create CallRef, objectID string) (Observation, ProductReason) {
	if r.budget {
		return Observation{}, ReasonConfirmationBudgetExhaust
	}
	obs, ok := r.call(ctx, Observation{Purpose: PurposeRead, Role: role, AccountLabel: label, ResourceType: t.Type,
		Create: create}, func() (Observation, error) {
		return r.deps.Transport.Read(ctx, ReadRequest{Binding: r.in.Binding, ResourceType: t.Type, Spec: t.Read,
			MarkerField: t.Create.MarkerField, Role: role, AccountLabel: label, Create: create, ObjectID: objectID, Marker: r.marker,
			Timeout: r.in.RequestTimeout})
	})
	if r.integrity != "" || (r.stopped && !ok) {
		return Observation{}, ""
	}
	if !ok {
		return Observation{}, r.unusableReason(Observation{}, false, false)
	}
	if reason := r.notUsable(obs, false); reason != "" {
		return obs, reason
	}
	if obs.Marker.Field != t.Create.MarkerField {
		r.integrity = IntegrityObservationMismatch
		return Observation{}, ""
	}
	if role == RoleCross && CrossDenied(obs.StatusCode) {
		// 교차 조회의 거부 응답은 표식 검사보다 상태를 먼저 본다(사용자 결정 2026-11-20). 거부 본문에서 표식을 못 찾거나
		// 검사하지 못한 것은 데이터 검사 실패가 아니라 정상 데이터가 아닌 응답이다(오라클 data_no_match). 거부인데 표식이
		// 보이면 모순이라 판정하지 않는다.
		if obs.Marker.Status == MarkerComplete && obs.Marker.Presence == MarkerPresent {
			return obs, ReasonDataCheckContradiction
		}
		return obs, ""
	}
	switch obs.Marker.Status {
	case MarkerPartial:
		return obs, ReasonPartialResponse
	case MarkerUnsupported:
		return obs, ReasonEncodingUnsupported
	case MarkerFailed:
		return obs, ReasonDataCheckFailed
	}
	return obs, ""
}

// recordWrite는 생성 요청 하나가 대상에 남겼을 수 있는 쓰기를 남긴다. 부르지 않았거나 보내지 않은 것이 확실하면 남기지
// 않는다. 객체 ID를 꺼낸 정상 응답만 created이고, 나머지(시간 초과·결과 불명·관찰 불일치·비정상 응답)는 unknown이다.
// recordWrite는 생성 요청이 대상에 남겼을 수 있는 쓰기를 결과에 더한다. 보내지 않은 것이 확실하면 더하지 않고
// not_sent를 돌려준다(쓰기 기록 갱신에 쓴다).
func (r *run) recordWrite(resourceType, objectID string, obs Observation, ok, invoked bool) ConfirmationWrite {
	if !invoked || ok && !obs.Sent {
		return ConfirmationWrite{ResourceType: resourceType, Outcome: WriteNotSent, ObjectID: objectID}
	}
	w := ConfirmationWrite{ResourceType: resourceType, Outcome: WriteUnknown, ObjectID: objectID}
	if ok {
		w.Ref = obs.Ref
		if obs.ResponseComplete && obs.Normal == oracle.StateYes && obs.ObjectIDDigest != "" {
			w.Outcome, w.ObjectIDDigest = WriteCreated, obs.ObjectIDDigest
		}
	}
	r.writes = append(r.writes, w)
	return w
}

// settleWrite는 미리 남긴 쓰기 기록을 생성 결과로 갱신한다. unknown은 이미 기록과 같아 부르지 않는다. 회차가 멈춰도
// 갱신은 끝까지 한다(멈춘 문맥으로 갱신을 놓치면 보내지 않은 생성도 리셋 확인이 필요해진다). 실패해도 미리 남긴 기록
// (ID 모름)이 다음 회차를 막으므로 판정은 이어 간다.
func (r *run) settleWrite(ctx context.Context, w ConfirmationWrite) {
	if w.Outcome == WriteUnknown {
		return
	}
	if err := r.deps.Writes.Settle(context.WithoutCancel(ctx), r.in.Binding, w); err != nil && r.deps.Logger != nil {
		r.deps.Logger.Warn("E③ 쓰기 기록 갱신 실패", "resource_type", w.ResourceType, "outcome", string(w.Outcome))
	}
}

// notUsable은 보내지 않았거나 결과가 불명인 관찰의 사유다. 쓸 수 있으면 빈 값이다.
func (r *run) notUsable(obs Observation, create bool) ProductReason {
	if obs.Sent && obs.ResponseComplete {
		return ""
	}
	return r.unusableReason(obs, true, create)
}

func (r *run) unusableReason(obs Observation, received, create bool) ProductReason {
	if r.budget {
		return ReasonConfirmationBudgetExhaust
	}
	if received && obs.Sent && obs.TimedOut {
		return ReasonRequestTimeout
	}
	if !received || obs.Sent {
		return ReasonRequestResultUnknown
	}
	switch obs.NotSent {
	case NotSentApprovalDenied:
		if create {
			return ReasonCreateApprovalDenied
		}
	case NotSentApprovalTimeout:
		if create {
			return ReasonCreateApprovalTimeout
		}
	case NotSentBudgetExhausted:
		return ReasonConfirmationBudgetExhaust
	}
	return ReasonRequestDenied
}

func unknownFacts() oracle.ComparisonFacts {
	u := oracle.StateUnknown
	return oracle.ComparisonFacts{SameResource: u, OwnerNormalData: u, CrossNormalData: u, NontrivialDataMatch: u,
		ReflectionExcluded: u, TemplateExcluded: u}
}

// verdict는 모은 사실로 oracle.Evaluate를 불러 판정 행을 만든다. 판정 값은 오라클 결과 그대로다.
func (r *run) verdict(t TypeDeclaration, subjects oracle.SubjectContext, facts oracle.ComparisonFacts, evidence []CallRef,
	reason ProductReason) Verdict {
	observedAt := r.lastAt
	if observedAt.IsZero() {
		now := r.deps.Now
		if now == nil {
			now = time.Now
		}
		observedAt = now()
	}
	// owner_only 선언: B는 기대 접근이 거부이고, 공유·위임은 없다고 선언됐다. 동결본을 대조한 뒤에만 여기 온다.
	c := oracle.CaseInput{
		Policy: oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone,
			Delegation: oracle.ResolvedNone, HasRegisteredSource: true, ValidFrom: r.in.PolicyValidFrom,
			ValidUntil: r.in.PolicyValidUntil, HintMatchesCanonical: true},
		Subjects: subjects,
		Facts:    facts,
	}
	out := oracle.Evaluate(c, observedAt)
	v := Verdict{ResourceType: t.Type, Value: out.Value, PolicyCheck: out.PolicyCheck, IdentityCheck: out.IdentityCheck,
		DataCheck: out.DataCheck, OracleReasons: out.ReasonCodes, ProductReasons: []ProductReason{},
		CandidateRefs: []string{}, Evidence: evidence, ObjectIDDigest: r.objectDigest, ObservedAt: observedAt.UTC(), Case: c}
	if v.Evidence == nil {
		v.Evidence = []CallRef{}
	}
	if reason != "" {
		v.ProductReasons = []ProductReason{reason}
	}
	return v
}

// linkCandidates는 후보를 선언된 조회 경로 틀과 맞춘다(#596 §9). 정확히 하나와 맞을 때만 잇는다.
func (r *run) linkCandidates(types []TypeDeclaration, verdicts []Verdict) []CandidateConfirmation {
	byType := map[string]Verdict{}
	for _, v := range verdicts {
		byType[v.ResourceType] = v
	}
	out := make([]CandidateConfirmation, 0, len(r.in.Candidates))
	for _, c := range r.validCandidates() {
		var matched []string
		if c.Method == "GET" {
			for _, t := range types {
				if pathPatternMatches(c.PathPattern, t.Read.PathTemplate) {
					matched = append(matched, t.Type)
				}
			}
		}
		cc := CandidateConfirmation{CandidateRef: c.Ref, Level: oracle.Suspected}
		switch len(matched) {
		case 0:
			cc.Reason = "link_none"
		case 1:
			v, judged := byType[matched[0]]
			if !judged {
				cc.Reason = "link_none"
				break
			}
			cc.ResourceType = matched[0]
			if v.Value == oracle.Confirmed {
				cc.Level = oracle.Confirmed
			}
		default:
			cc.Reason = "link_ambiguous"
		}
		out = append(out, cc)
	}
	return out
}

// pathPatternMatches는 후보 경로 패턴과 조회 틀이 조각마다 같은지다. 가린 조각({id} 등 {…})은 가린 조각끼리만 맞는다.
func pathPatternMatches(pattern, template string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(template, "/")
	if len(a) != len(b) || !strings.HasPrefix(pattern, "/") {
		return false
	}
	for i := range a {
		am := strings.HasPrefix(a[i], "{") && strings.HasSuffix(a[i], "}")
		bm := strings.HasPrefix(b[i], "{") && strings.HasSuffix(b[i], "}")
		if am != bm || (!am && a[i] != b[i]) {
			return false
		}
	}
	return true
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/server"
)

// 선택 후보 검증의 학습 로그인 재사용(docs/contracts/selected-candidate-learned-login.md).
//   - verified 방법만 쓴다(2026-11-27 팀 결정, 개발 계획 "#950 학습 방법 로그인과 9칸-1 후속"). verified는 같은 방법·지금 대상
//     판·자격 판의 E③ 다시 로그인 성공 줄(login_method_verified)이 그 방법이 지금 것이 된 시각(since, ⑤-B 읽기 경계) 뒤에 있고
//     해제되지 않았다는 뜻이다. learned만 있거나 해제됐으면 learned_login_unverified로 막는다.
//   - 원본 회차 보호 기록 재대조(e2_audited·exploration_observed)는 verified를 대신하지 않는다. verified 방법의 원본 시도가
//     지금도 보호 기록과 맞는지 더 보는 데만 쓴다(맞지 않으면 그 사유로 막음).
//   - 저장 경계가 없는 빌드(읽기 구현 없음)와 저장된 방법이 없을 때(learned_login_none)만 로그인 설정(9칸)으로 계획한다.
//     방법이 있는데 위 확인이 실패하거나 읽기에 실패하면 그 사유로 막고 9칸·다른 계정으로 대신하지 않는다.
//   - 선택 검증의 로그인 성공은 E③ verified가 아니다. 실패·결과 불명이면 verified의 현재 유효성만 해제한다
//     (candidate_login_reservation.go).

// productSelectedRunRefPrefix는 제품 회차 run_ref의 앞부분이다(policy.RunRef = "run-" + 작업 ID, url_preparation_linux.go).
const productSelectedRunRefPrefix = "run-"

// productCandidateAccount는 계정 참조(라벨·자격 판 digest)에 맞는 대상 계정이다. 자격이 지금 보호 파일과 같을 때만 준다.
type productCandidateAccount struct {
	rec        productTargetRecord
	label      string
	credential string
	path       string
}

// candidateAccount는 원본 관찰의 계정 참조를 지금 대상 계정으로 되돌린다. 계정이 지워졌거나 자격이 바뀌면(참조가 다른
// 판을 가리킴) 없다.
func (r *productTargetRegistry) candidateAccount(principal, targetID, accountRef string) (productCandidateAccount, learnedlogin.Reason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, byTarget, err := r.accountTarget(principal, targetID)
	if err != nil {
		return productCandidateAccount{}, learnedlogin.ReasonRecordInvalid
	}
	var found *productAccountState
	for _, st := range byTarget {
		if st.credentialSHA256 != "" && server.CandidateAccountRevision(st.label, st.credentialSHA256) == accountRef {
			if found != nil {
				return productCandidateAccount{}, learnedlogin.ReasonCredentialUnavailable
			}
			found = st
		}
	}
	if found == nil {
		return productCandidateAccount{}, learnedlogin.ReasonCredentialChanged
	}
	if !r.credentialConfigured(targetID, found) {
		return productCandidateAccount{}, learnedlogin.ReasonCredentialUnavailable
	}
	return productCandidateAccount{rec: rec, label: found.label, credential: found.credentialSHA256, path: r.credentialPath(targetID, found.label)}, ""
}

// productLearnedMethodStateReader는 지금 배운 방법과 그 방법이 지금 것이 된 시각(since)을 한 번에 읽는 경계다(⑤-B 저장의
// login_method_learned 상태). at(관찰 시각)은 같은 방법을 다시 배우면 바뀌지만 since는 바뀌지 않는다. verified는 since 뒤의
// 검증만 인정한다(loginMethodVerifiedFor). 구현이 없으면 저장된 방법이 있어도 verified를 판단할 수 없어 쓰지 않는다.
type productLearnedMethodStateReader interface {
	learnedLoginMethodState(targetID, label string, revision uint64, credentialSHA256 string) (learnedlogin.Learned, time.Time, learnedlogin.Reason)
}

// verifySelectedLearnedSource는 저장된 학습 방법의 원본 시도를 원본 회차의 보호 기록으로 다시 대조하고 출처 수준을 준다.
// 탐색 단계 시도는 탐색 경계 근거 v2(exploration_observed), 진단 단계 시도는 원본 회차 E② 감사 저널 사본을 실행 감사 키로
// 끝까지 검증해 같은 호출·시도 결과와 대조한다(e2_audited). 실행 메모리의 감사 조회는 회차 뒤에 쓰지 않는다.
func verifySelectedLearnedSource(ctx context.Context, runsDir string, learned learnedlogin.Learned, label, targetID, origin string, revision uint64, approvedAt time.Time, credential string) (string, learnedlogin.Reason) {
	jobID, ok := strings.CutPrefix(learned.Source.RunRef, productSelectedRunRefPrefix)
	if !ok || runsDir == "" {
		return "", learnedlogin.ReasonRecordInvalid
	}
	binding, err := openRunBinding(runsDir, jobID)
	if err != nil {
		return "", learnedlogin.ReasonRecordInvalid
	}
	defer binding.Close()
	policy, taskID := binding.policy, binding.taskID
	if policy.RunRef != learned.Source.RunRef || policy.Auth == nil || policy.ValidFrom.Before(approvedAt) {
		return "", learnedlogin.ReasonRecordInvalid
	}
	bound := false
	for _, account := range policy.Auth.Accounts {
		bound = bound || account.Label == label && account.Credential.SHA256 == credential &&
			account.Credential.File == bindingCredentialName(targetID, label, credential)
	}
	if !bound {
		return "", learnedlogin.ReasonCredentialChanged
	}
	loginOrigin := false
	for _, rule := range policy.Rules {
		loginOrigin = loginOrigin || rule.Ref == policy.Auth.RuleRef && rule.Origin == origin
	}
	if !loginOrigin {
		return "", learnedlogin.ReasonRecordInvalid
	}
	stateDir := filepath.Join(runsDir, jobID, "state")
	// 인계 실패 표지가 있거나 기록이 손상됐으면 그 회차에서 배운 방법을 믿지 않는다(E③ 재사용 §6-3과 같은 판독기).
	rows, _, err := learnedlogin.ReadConfirmationHistory(stateDir)
	if err != nil {
		return "", learnedlogin.ReasonRecordInvalid
	}
	sel, ok := learnedlogin.Select(rows, learned.Source.RunRef, []string{label})[label]
	if !ok || sel.Reason != "" {
		return "", learnedlogin.ReasonRecordInvalid
	}
	// 저장된 방법은 그 회차가 고른 바로 그 시도여야 한다(저장 줄과 회차 기록이 다른 방법을 가리키면 손상).
	if sel.Learned.Source.AttemptRef != learned.Source.AttemptRef || !sel.Learned.Source.At.Equal(learned.Source.At) ||
		sel.Learned.Method.Digest() != learned.Method.Digest() {
		return "", learnedlogin.ReasonRecordInvalid
	}
	switch sel.Row.Stage {
	case learnedlogin.StageDiagnosis:
		events, verified, err := runJournalEvents(ctx, binding)
		if err != nil || !verified {
			return "", learnedlogin.ReasonTransportUnverified
		}
		lookup := persistedAuditLookup(events)
		if provenance, reason := learnedlogin.VerifyTransport(sel.Row, learned.Source.RunRef, lookup); reason != "" || provenance != learnedlogin.ProvenanceE2Audited {
			return "", learnedlogin.ReasonTransportUnverified
		}
		if reason := learnedlogin.VerifyFieldSources(sel.Row, learned.Source.RunRef, nil, lookup); reason != "" {
			return "", reason
		}
		return server.CandidateLoginTrustE2Audited, ""
	case learnedlogin.StageExploration:
	default:
		return "", learnedlogin.ReasonTransportUnverified
	}
	if policy.ExplorationLoginEvidence != "required/v2" {
		// 경계 근거를 결속하지 않은 이전 회차(exploration_unaudited)는 출처를 확인했다고 말할 수 없다.
		return "", learnedlogin.ReasonTransportUnverified
	}
	evidence, found, err := learnedlogin.ReadExplorationEvidence(stateDir)
	if err != nil || !found || evidence.Schema != learnedlogin.ExplorationEvidenceSchema || taskID <= 0 ||
		evidence.TaskID != strconv.FormatInt(taskID, 10) || evidence.SourceDigest != policy.ExplorationSourceDigest {
		return "", learnedlogin.ReasonTransportUnverified
	}
	provenance, reason := learnedlogin.VerifyExplorationEvidence(sel.Row, evidence, jobID, targetID, revision)
	if reason != "" || provenance != learnedlogin.ProvenanceExplorationObserved {
		return "", learnedlogin.ReasonTransportUnverified
	}
	if reason := learnedlogin.VerifyFieldSources(sel.Row, learned.Source.RunRef, &evidence, nil); reason != "" {
		return "", reason
	}
	return server.CandidateLoginTrustExplorationObserved, ""
}

// persistedAuditLookup은 검증한 감사 저널 사건에서 시도 하나의 결과 사건을 찾는다(e2AuditLookup과 같은 칸 변환).
func persistedAuditLookup(events []e2proxy.Event) func(callRef, attemptID string) (learnedlogin.AuditedSend, bool) {
	return func(callRef, attemptID string) (learnedlogin.AuditedSend, bool) {
		for _, ev := range events {
			if ev.Stage != "result" || ev.CallRef != callRef || ev.AttemptID != attemptID {
				continue
			}
			return learnedlogin.AuditedSend{CallRef: ev.CallRef, AttemptID: ev.AttemptID, RunRef: ev.Scope.RunRef,
				ModelCall: ev.Program == "" && ev.E3Audit == nil && ev.IntentID > 0, Method: ev.Method, Allowed: ev.Decision == "allowed",
				RequestWritten: ev.RequestWritten, ResponseReceived: ev.ResponseReceived, ResponseComplete: ev.ResponseComplete,
				StatusCode: ev.StatusCode, AuditComplete: ev.AuditComplete}, true
		}
		return learnedlogin.AuditedSend{}, false
	}
}

// candidateLogin은 선택 후보 검증의 인증 준비 원천을 고른다(계획·실행 직전 모두 같은 함수). 값은 없다. holder는 지금
// 송신 중인 실행의 예약 참조다(계획·실행 전 확인은 빈 값). 저장된 학습 방법은 대상 레지스트리가 읽기 경계
// (productLearnedMethodReader, ⑤-B)를 구현할 때만 있다.
func (p *productRuntime) candidateLogin(principal, targetID, accountRef, holder string) diagnosisprep.ProductCandidateLogin {
	if p == nil || p.targets == nil {
		return diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginNone, Reason: string(learnedlogin.ReasonRecordInvalid)}
	}
	return candidateLoginChoice(p.targets, p.learnedMethodReader(), p.config.RunsDir, principal, targetID, accountRef, holder)
}

func (p *productRuntime) learnedMethodReader() productLearnedMethodReader {
	if p.learnedMethods != nil {
		return p.learnedMethods
	}
	reader, _ := any(p.targets).(productLearnedMethodReader)
	return reader
}

// candidateLearnedState는 지금 verified인 학습 방법과 계정·since다. 계획·예약·송신 직전 확인이 같은 판독을 쓴다.
type candidateLearnedState struct {
	account productCandidateAccount
	learned learnedlogin.Learned
	since   time.Time
}

// candidateLearnedLookup은 계정·저장 방법·since를 읽는다. 방법이 없으면 ReasonNone이다(9칸 전환 판단은 호출자).
func candidateLearnedLookup(targets *productTargetRegistry, reader productLearnedMethodReader, principal, targetID, accountRef string) (candidateLearnedState, string) {
	account, reason := targets.candidateAccount(principal, targetID, accountRef)
	if reason != "" {
		return candidateLearnedState{}, string(reason)
	}
	if reader == nil {
		return candidateLearnedState{account: account}, string(learnedlogin.ReasonNone)
	}
	if _, reason := reader.learnedLoginMethod(targetID, account.label, account.rec.Target.Revision, account.credential); reason != "" {
		return candidateLearnedState{account: account}, string(reason)
	}
	stateReader, ok := reader.(productLearnedMethodStateReader)
	if !ok {
		return candidateLearnedState{}, candidateReasonVerificationUnavailable
	}
	stored, since, reason := stateReader.learnedLoginMethodState(targetID, account.label, account.rec.Target.Revision, account.credential)
	switch {
	case reason == learnedlogin.ReasonNone:
		// 두 읽기 사이에 방법이 사라졌다. 방법 없음으로 보고 9칸으로 넘어가지 않는다.
		return candidateLearnedState{}, candidateReasonVerificationUnavailable
	case reason != "":
		return candidateLearnedState{}, string(reason)
	case since.IsZero() || since.After(stored.Source.At):
		return candidateLearnedState{}, candidateReasonVerificationUnavailable
	}
	return candidateLearnedState{account: account, learned: stored, since: since}, ""
}

func candidateLoginChoice(targets *productTargetRegistry, reader productLearnedMethodReader, runsDir, principal, targetID, accountRef, holder string) diagnosisprep.ProductCandidateLogin {
	none := func(reason string) diagnosisprep.ProductCandidateLogin {
		return diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginNone, Reason: reason}
	}
	state, reason := candidateLearnedLookup(targets, reader, principal, targetID, accountRef)
	if reason == string(learnedlogin.ReasonNone) {
		// 저장 경계가 없는 빌드이거나 저장된 방법이 없을 때만 로그인 설정(9칸)으로 계획한다(전환 기간 호환).
		login, err := targets.targetLogin(principal, targetID)
		if err != nil || login == nil {
			return none(string(learnedlogin.ReasonNone))
		}
		return diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginConfigured, Login: login}
	}
	if reason != "" {
		return none(reason)
	}
	account, stored := state.account, state.learned
	m := stored.Method.Clone()
	if m.Validate() != "" || m.Method != "POST" {
		return none(string(learnedlogin.ReasonFormatUnsupported))
	}
	verifiedRef, gate := targets.candidateLoginGate(targetID, account.label, m.Digest(), account.rec.Target.Revision, account.credential, state.since, holder)
	if gate != "" {
		return none(gate)
	}
	// verified 방법의 원본 시도가 지금도 원본 회차 보호 기록과 맞는지 다시 본다(verified를 대신하지 않는 추가 확인).
	if _, reason := verifySelectedLearnedSource(context.Background(), runsDir, stored, account.label, targetID, account.rec.Target.Origin,
		account.rec.Target.Revision, account.rec.Target.ApprovedAt, account.credential); reason != "" {
		return none(string(reason))
	}
	return diagnosisprep.ProductCandidateLogin{Source: diagnosisprep.CandidateLoginLearned, Trust: server.CandidateLoginTrustVerified,
		VerifiedRef: verifiedRef, Method: &m}
}

// reserveCandidateLearnedLogin은 승인 계획의 verified 사건(verifiedRef)이 지금도 같을 때 그 계정의 선택 로그인을 예약한다.
// 실패면 고정 사유이고 대상에 보내지 않는다.
func (p *productRuntime) reserveCandidateLearnedLogin(principal, targetID, accountRef, verifiedRef, ref string) (productCandidateAccount, string) {
	if p == nil || p.targets == nil {
		return productCandidateAccount{}, candidateReasonUnresolved
	}
	state, reason := candidateLearnedLookup(p.targets, p.learnedMethodReader(), principal, targetID, accountRef)
	if reason != "" {
		if reason == string(learnedlogin.ReasonNone) {
			reason = candidateReasonVerificationUnavailable
		}
		return productCandidateAccount{}, reason
	}
	a := state.account
	if reason := p.targets.reserveCandidateLogin(targetID, a.label, state.learned.Method.Digest(), a.rec.Target.Revision, a.credential, state.since,
		verifiedRef, ref); reason != "" {
		return productCandidateAccount{}, reason
	}
	return a, ""
}

// candidateAccountMaterial은 선택 계정 하나의 자격 보호 파일을 읽는다. 9칸이 없는 대상은 일반 결속이 계정을 싣지 않으므로
// (product_accounts.go runAccounts) 학습 로그인 실행은 이 계정 하나만 따로 싣는다. 해시가 결속 판과 다르면 쓰지 않는다.
func (a productCandidateAccount) material() ([]byte, server.E2HTTPFilePin, error) {
	raw, err := readPrivateFile(a.path, maxCredentialFileBytes)
	if err != nil {
		return nil, server.E2HTTPFilePin{}, errPreparation
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != a.credential {
		clear(raw)
		return nil, server.E2HTTPFilePin{}, errPreparation
	}
	return raw, server.E2HTTPFilePin{File: bindingCredentialName(a.rec.Target.ID, a.label, a.credential), SHA256: a.credential}, nil
}

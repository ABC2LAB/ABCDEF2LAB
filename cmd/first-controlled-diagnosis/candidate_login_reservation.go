package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/server"
)

// 선택 후보 학습 로그인의 verified 생명주기(docs/contracts/selected-candidate-learned-login.md §5, 2026-11-27 팀 결정).
//   - 선택 검증은 verified 방법만 쓴다. 성공해도 verified를 새로 쓰지 않는다(E③ 다시 로그인과 신원 대조만 verified를 만든다).
//   - 실제 로그인 POST를 보낸 뒤 분명한 실패·결과 불명·첫 인증 읽기 막힘이면 그 verified의 현재 유효성을 해제한다
//     (login_method_verified_cleared). 과거 verified 줄은 지우거나 고치지 않는다. 보내지 않았으면(not_sent) 해제하지 않는다.
//   - 같은 대상·계정의 선택 로그인은 한 번에 하나다. 로그인을 보낼 수 있는 구간(E② 실행 시작~결과 판정)을 송신 전에 계정 저널에
//     예약(candidate_login_reserved)으로 남기고, 결과를 남긴 뒤에만 닫는다(candidate_login_finished 또는 해제 줄).
//     예약을 남기지 못하면 보내지 않는다. 결과를 남기지 못하면 예약이 열린 채로 남아 그 계정의 다음 계획·실행을 막는다.
//   - 이 프로세스가 잡지 않은 열린 예약(재시작 뒤 남은 예약)은 결과를 모르는 로그인이다. 안전한 쪽(verified 해제, 사유
//     candidate_login_interrupted)으로만 닫는다. 그 기록도 못 남기면 계속 막는다.
//   - 계정 저널은 디렉터리 배타 잠금(diagnosisprep 보호 저널 flock)이라 한 프로세스만 쓴다. 그래서 "이 프로세스가 잡은 예약"
//     메모리 집합으로 다른 프로세스 예약을 착각하지 않는다.

const (
	accountCandidateLoginReserved = "candidate_login_reserved"
	accountCandidateLoginFinished = "candidate_login_finished"
	accountLoginVerifiedCleared   = "login_method_verified_cleared"
)

// 예약 종료 결과(candidate_login_finished의 evidence). authenticated: 로그인과 첫 인증 읽기까지 막힘 없음. not_sent: 로그인
// POST를 보내지 않음. superseded: 예약 뒤 같은 방법의 더 새 E③ verified가 생겨 옛 검증을 해제할 대상이 없음.
const (
	candidateLoginFinishedAuthenticated = "authenticated"
	candidateLoginFinishedNotSent       = "not_sent"
	candidateLoginFinishedSuperseded    = "superseded"
)

// verified 해제 사유(login_method_verified_cleared의 evidence, 값 없음).
const (
	candidateLoginClearedFailed       = "candidate_login_failed"
	candidateLoginClearedUnknown      = "candidate_login_result_unknown"
	candidateLoginClearedReadDenied   = "candidate_first_read_denied"
	candidateLoginClearedUnconfirmed  = "candidate_auth_unconfirmed"
	candidateLoginClearedInterrupted  = "candidate_login_interrupted"
	candidateLoginReservationRefLimit = 64
)

// 선택 후보 학습 로그인 선택·예약의 고정 거절 사유(값 없음, API reason).
const (
	// learned_login_unverified: 학습 방법이 있지만 지금 verified가 아니다(learned뿐이거나 해제됨·판본 다름).
	candidateReasonUnverified = "learned_login_unverified"
	// learned_login_verification_unavailable: 방법이 지금 것이 된 시각(since)을 읽을 경계가 없거나 읽지 못했다.
	candidateReasonVerificationUnavailable = "learned_login_verification_unavailable"
	// candidate_login_in_progress: 같은 대상·계정의 다른 선택 로그인이 진행 중이다.
	candidateReasonInProgress = "candidate_login_in_progress"
	// candidate_login_unresolved: 앞선 선택 로그인의 결과·해제를 남기지 못해 예약이 열려 있다.
	candidateReasonUnresolved = "candidate_login_unresolved"
)

var candidateLoginReservationRef = regexp.MustCompile(`^candidate-[1-9][0-9]{0,18}$`)

func validCandidateLoginEvidence(action, evidence string) bool {
	switch action {
	case accountCandidateLoginFinished:
		return evidence == candidateLoginFinishedAuthenticated || evidence == candidateLoginFinishedNotSent || evidence == candidateLoginFinishedSuperseded
	case accountLoginVerifiedCleared:
		switch evidence {
		case candidateLoginClearedFailed, candidateLoginClearedUnknown, candidateLoginClearedReadDenied, candidateLoginClearedUnconfirmed,
			candidateLoginClearedInterrupted:
			return true
		}
	}
	return false
}

// candidateLoginReservationRefFor는 선택 검증 요청 하나의 예약 참조다. 요청마다 실행은 한 번이다.
func candidateLoginReservationRefFor(requestID int64) string {
	return "candidate-" + strconv.FormatInt(requestID, 10)
}

// productCandidateLoginReservation은 계정의 열린 선택 로그인 예약이다(값 없음). 예약할 때의 verified 줄(방법·회차·시도)을 같이
// 둔다. 해제는 이 verified가 지금도 그 방법의 마지막 검증일 때만 한다(늦게 온 실패가 더 새 검증을 지우지 않음).
type productCandidateLoginReservation struct {
	ref                string
	methodSHA256       string
	verifiedRunRef     string
	verifiedAttemptRef string
	at                 time.Time
}

// candidateVerifiedRef는 verified 사건 하나의 비밀 없는 참조다. 같은 방법이라도 다른 검증 사건(재검증)이면 값이 다르다.
func candidateVerifiedRef(targetID, label string, v productLoginVerified) string {
	sum := sha256.Sum256([]byte("candidate-login-verified/v1\n" + targetID + "\n" + label + "\n" + v.methodSHA256 + "\n" +
		strconv.FormatUint(v.targetRevision, 10) + "\n" + v.credentialSHA256 + "\n" + v.runRef + "\n" + v.attemptRef + "\n" +
		v.at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func candidateLoginKey(targetID, label string) string { return targetID + "\x00" + label }

// replayCandidateLogin은 예약·종료·해제 줄을 반영한다. 쓰는 쪽(아래 registry 메서드)이 잠금 안에서 확인한 순서만 받는다:
// 예약은 열린 예약이 없고 그 verified가 해제되지 않았을 때, 종료·해제는 같은 참조의 열린 예약이 있을 때뿐이다. 해제는 예약의
// verified가 지금도 그 방법의 마지막 검증일 때만 쓰므로 다르면 저널 손상이다.
func (r *productTargetRegistry) replayCandidateLogin(e productAccountEvent) error {
	st := r.accounts[e.TargetID][e.Label]
	if st == nil || !candidateLoginReservationRef.MatchString(e.ReservationRef) || e.Login != nil || e.StatementSHA256 != "" || e.JobRef != "" {
		return errPreparation
	}
	switch e.Action {
	case accountCandidateLoginReserved:
		v := st.loginVerified[e.MethodSHA256]
		if st.candidateLogin != nil || e.Evidence != "" || e.Stage != productLoginVerifiedStage || !urlDigestValid(e.CredentialSHA256) ||
			v == nil || v.cleared || v.runRef != e.RunRef || v.attemptRef != e.AttemptRef || v.credentialSHA256 != e.CredentialSHA256 {
			return errPreparation
		}
		st.candidateLogin = &productCandidateLoginReservation{ref: e.ReservationRef, methodSHA256: e.MethodSHA256, verifiedRunRef: e.RunRef,
			verifiedAttemptRef: e.AttemptRef, at: e.At}
		return nil
	case accountCandidateLoginFinished:
		if st.candidateLogin == nil || st.candidateLogin.ref != e.ReservationRef || !validCandidateLoginEvidence(e.Action, e.Evidence) ||
			e.MethodSHA256 != "" || e.RunRef != "" || e.AttemptRef != "" || e.Stage != "" || e.CredentialSHA256 != "" {
			return errPreparation
		}
		st.candidateLogin = nil
		return nil
	case accountLoginVerifiedCleared:
		res := st.candidateLogin
		if res == nil || res.ref != e.ReservationRef || !validCandidateLoginEvidence(e.Action, e.Evidence) || e.MethodSHA256 != res.methodSHA256 ||
			e.RunRef != res.verifiedRunRef || e.AttemptRef != res.verifiedAttemptRef || e.Stage != productLoginVerifiedStage {
			return errPreparation
		}
		v := st.loginVerified[e.MethodSHA256]
		if v == nil || v.cleared || v.runRef != e.RunRef || v.attemptRef != e.AttemptRef || v.credentialSHA256 != e.CredentialSHA256 {
			return errPreparation
		}
		v.cleared, v.clearedReason = true, e.Evidence
		st.candidateLogin = nil
		return nil
	}
	return errPreparation
}

// candidateVerifiedLocked는 지금 verified인 방법의 검증 줄과 그 참조다(해제된 것·판본 다른 것·since 앞의 것 제외). r.mu를
// 잡은 채로 부른다.
func (r *productTargetRegistry) candidateVerifiedLocked(targetID, label, methodSHA256 string, revision uint64, credential string, since time.Time) (*productLoginVerified, string) {
	rec, ok := r.records[targetID]
	st := r.accounts[targetID][label]
	if !ok || st == nil || rec.Target.Revision != revision || st.credentialSHA256 != credential {
		return nil, ""
	}
	v := loginMethodVerifiedFor(rec, st, methodSHA256, since)
	if v == nil {
		return nil, ""
	}
	return v, candidateVerifiedRef(targetID, label, *v)
}

// candidateLoginGate는 선택 후보 학습 로그인에 쓸 수 있는 verified인지와 그 참조다. holder는 지금 송신 중인 실행의 예약
// 참조(실행 전·계획 때는 빈 값)다. 다른 실행의 열린 예약이 있으면 막고, 이 프로세스가 잡지 않은 열린 예약(재시작 뒤 남은
// 예약)은 안전한 쪽(verified 해제)으로 닫아 본 뒤 판단한다.
func (r *productTargetRegistry) candidateLoginGate(targetID, label, methodSHA256 string, revision uint64, credential string, since time.Time, holder string) (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return "", candidateReasonUnresolved
	}
	st := r.accounts[targetID][label]
	if st == nil {
		return "", candidateReasonUnverified
	}
	if res := st.candidateLogin; res != nil {
		switch {
		case holder != "" && res.ref == holder && r.candidateActive[candidateLoginKey(targetID, label)] == holder:
			// 이 실행이 잡은 예약이다. 아래에서 그 verified가 지금도 같은지 본다.
		case r.candidateActive[candidateLoginKey(targetID, label)] == res.ref:
			return "", candidateReasonInProgress
		default:
			if r.closeOrphanCandidateLoginLocked(targetID, label) != nil {
				return "", candidateReasonUnresolved
			}
		}
	}
	v, ref := r.candidateVerifiedLocked(targetID, label, methodSHA256, revision, credential, since)
	if v == nil {
		return "", candidateReasonUnverified
	}
	if holder != "" && (st.candidateLogin == nil || st.candidateLogin.ref != holder || st.candidateLogin.methodSHA256 != methodSHA256 ||
		st.candidateLogin.verifiedRunRef != v.runRef || st.candidateLogin.verifiedAttemptRef != v.attemptRef) {
		return "", candidateReasonUnverified
	}
	return ref, ""
}

// closeOrphanCandidateLoginLocked는 이 프로세스가 잡지 않은 열린 예약을 결과 불명으로 닫는다: 예약의 verified가 지금도 그
// 방법의 마지막 검증이면 해제(candidate_login_interrupted), 아니면(더 새 검증이 있음) 해제 없이 종료(superseded)다. r.mu를
// 잡은 채로 부른다.
func (r *productTargetRegistry) closeOrphanCandidateLoginLocked(targetID, label string) error {
	st := r.accounts[targetID][label]
	if st == nil || st.candidateLogin == nil {
		return nil
	}
	return r.closeCandidateLoginLocked(targetID, label, st.candidateLogin.ref, "", candidateLoginClearedInterrupted)
}

// closeCandidateLoginLocked는 열린 예약을 닫는다. finished가 있으면 그 결과로 종료하고, 아니면 cleared 사유로 verified를
// 해제한다(예약의 verified가 지금도 마지막 검증일 때만, 아니면 superseded 종료). 같은 참조의 열린 예약이 없으면(이미 닫음)
// 아무것도 쓰지 않는다(멱등). r.mu를 잡은 채로 부른다.
func (r *productTargetRegistry) closeCandidateLoginLocked(targetID, label, ref, finished, cleared string) error {
	rec, ok := r.records[targetID]
	st := r.accounts[targetID][label]
	if !ok || st == nil || st.candidateLogin == nil || st.candidateLogin.ref != ref {
		return nil
	}
	res := st.candidateLogin
	now := time.Now().UTC()
	if finished == "" {
		v := st.loginVerified[res.methodSHA256]
		if v != nil && !v.cleared && v.runRef == res.verifiedRunRef && v.attemptRef == res.verifiedAttemptRef {
			return r.appendAccount(productAccountEvent{TargetID: targetID, Label: label, Action: accountLoginVerifiedCleared, Actor: rec.Principal,
				At: now, TargetRevision: v.targetRevision, CredentialSHA256: v.credentialSHA256, MethodSHA256: res.methodSHA256, RunRef: v.runRef,
				Stage: productLoginVerifiedStage, AttemptRef: v.attemptRef, Evidence: cleared, ReservationRef: ref})
		}
		finished = candidateLoginFinishedSuperseded
	}
	return r.appendAccount(productAccountEvent{TargetID: targetID, Label: label, Action: accountCandidateLoginFinished, Actor: rec.Principal, At: now,
		TargetRevision: rec.Target.Revision, Evidence: finished, ReservationRef: ref})
}

// reserveCandidateLogin은 송신 전에 그 계정의 선택 로그인 예약을 남긴다. verifiedRef가 지금 verified 사건과 같고 열린 예약이
// 없을 때만 쓴다. 쓰지 못하면(저장 실패 포함) 고정 사유이고 대상에 보내지 않는다.
func (r *productTargetRegistry) reserveCandidateLogin(targetID, label, methodSHA256 string, revision uint64, credential string, since time.Time, verifiedRef, ref string) string {
	if !candidateLoginReservationRef.MatchString(ref) || len(ref) > candidateLoginReservationRefLimit {
		return candidateReasonUnresolved
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return candidateReasonUnresolved
	}
	rec, ok := r.records[targetID]
	st := r.accounts[targetID][label]
	if !ok || st == nil {
		return candidateReasonUnverified
	}
	key := candidateLoginKey(targetID, label)
	if res := st.candidateLogin; res != nil {
		if r.candidateActive[key] == res.ref {
			return candidateReasonInProgress
		}
		if r.closeOrphanCandidateLoginLocked(targetID, label) != nil {
			return candidateReasonUnresolved
		}
	}
	v, current := r.candidateVerifiedLocked(targetID, label, methodSHA256, revision, credential, since)
	if v == nil || current != verifiedRef {
		return candidateReasonUnverified
	}
	if r.appendAccount(productAccountEvent{TargetID: targetID, Label: label, Action: accountCandidateLoginReserved, Actor: rec.Principal,
		At: time.Now().UTC(), TargetRevision: rec.Target.Revision, CredentialSHA256: credential, MethodSHA256: methodSHA256, RunRef: v.runRef,
		Stage: productLoginVerifiedStage, AttemptRef: v.attemptRef, ReservationRef: ref}) != nil {
		return candidateReasonUnresolved
	}
	if r.candidateActive == nil {
		r.candidateActive = map[string]string{}
	}
	r.candidateActive[key] = ref
	return ""
}

// finishCandidateLogin은 이 프로세스가 잡은 예약을 결과로 닫는다. cleared가 있으면 verified 해제, 아니면 finished 결과로
// 종료한다. 기록하지 못하면 예약은 열린 채로 남고 더는 이 실행 것으로 보지 않는다(다음 판단 때 결과 불명으로 다시 닫아 본다).
func (r *productTargetRegistry) finishCandidateLogin(targetID, label, ref, finished, cleared string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := candidateLoginKey(targetID, label)
	if r.candidateActive[key] == ref {
		delete(r.candidateActive, key)
	}
	if r.check() != nil {
		return errPreparation
	}
	return r.closeCandidateLoginLocked(targetID, label, ref, finished, cleared)
}

// candidateSelectedLoginCapture는 선택 실행 하나의 학습 로그인 결과(E② 영수증 근거, 서버 훅)와 재현 관찰을 모아 예약을 어떻게
// 닫을지 정한다. 훅은 실행 시작의 학습 로그인 바로 뒤 한 번 불린다(권한 재확인 실패로 실행이 멈춰도 송신 여부가 남음).
type candidateSelectedLoginCapture struct {
	mu           sync.Mutex
	reported     bool
	outcome      server.E2HTTPSelectedLoginOutcome
	observations []candidateverify.StoredObservationV2
}

func (c *candidateSelectedLoginCapture) record(o server.E2HTTPSelectedLoginOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reported {
		// 한 실행에 학습 로그인은 한 번뿐이다. 두 번째 보고는 확신할 수 없는 상태로 본다.
		c.outcome.Result, c.outcome.Transport = server.E2HTTPAuthResultUnknown, learnedlogin.TransportUnknown
		return
	}
	c.reported, c.outcome = true, o
}

func (c *candidateSelectedLoginCapture) done(observations []candidateverify.StoredObservationV2) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observations = append([]candidateverify.StoredObservationV2(nil), observations...)
}

// classify는 예약을 닫을 결과다: finished(verified 유지) 또는 cleared(verified 해제 사유) 하나다(팀 결정 2026-11-27).
//   - 학습 로그인을 시작하지 않았거나 로그인 POST를 전달하지 않았으면(not_sent) 유지한다. 준비 GET만 보낸 경우도 같다.
//   - 보낸 뒤 분명한 실패(400 이상·로그인 화면·세션 없음)면 candidate_login_failed, 결과 불명이면 candidate_login_result_unknown.
//   - 로그인 뒤 이 계정의 첫 인증 읽기(가장 앞선 재현·대조 관찰)가 401·403이면 candidate_first_read_denied, 그 읽기를 끝까지 받지
//     못했으면 candidate_auth_unconfirmed다(확신이 없으면 해제).
func (c *candidateSelectedLoginCapture) classify(accountRef string) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reported || !c.outcome.LoginPossiblySent() {
		return candidateLoginFinishedNotSent, ""
	}
	switch c.outcome.Result {
	case server.E2HTTPAuthAuthenticated:
	case server.E2HTTPAuthLoginFailed, server.E2HTTPAuthSessionNotFound:
		return "", candidateLoginClearedFailed
	default:
		return "", candidateLoginClearedUnknown
	}
	var first *e2proxy.HTTPObservation
	for i := range c.observations {
		o := &c.observations[i].Source.Observation
		if o.RequestAccountRef != accountRef {
			continue
		}
		if first == nil || o.Sequence < first.Sequence {
			first = o
		}
	}
	switch {
	case first == nil || !first.ResponseComplete || first.StatusCode == 0:
		return "", candidateLoginClearedUnconfirmed
	case first.StatusCode == http.StatusUnauthorized || first.StatusCode == http.StatusForbidden:
		return "", candidateLoginClearedReadDenied
	}
	return candidateLoginFinishedAuthenticated, ""
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/server"
)

// 확정 단계 학습 로그인 재사용(docs/contracts/model-login-attempts.md §6·§6-1·§6-2).
//   - 담당(2026-11-26 #969 정정): 배운 방법의 저장(learned, 계정 저널 login_method_learned)과 계정 목록 API는 모델 로그인
//     PR(⑤-B, 최민준)이 하고, 이 파일은 E③ 재사용과 그 성공의 verified 줄(login_method_verified)만 쓴다.
//   - 재사용: 확정 단계 직전 다시 로그인은 이 회차 성공 방법을 먼저, 없으면 저장된 방법(⑤-B가 productLearnedMethodReader를
//     구현해야 쓸 수 있음)을 쓴다. 둘 다 없으면 지금 로그인 설정(9칸)으로 다시 로그인한다(전환 기간 호환). 배운 방법이 있는데
//     실패하면 로그인 설정으로 대신하지 않는다.
//   - 검증: 프로그램 로그인 세션으로 연 확정 단계 신원 응답이 열림이고 같은 경로의 세션 없는 대조 응답이 막힘이며, 두 계정의
//     신원이 다르고, 그 사이 세션이 바뀌지 않았고, 확정 단계가 끝까지 돌았을 때만 succeeded이고 verified 줄을 남긴다.

// productLearnedLoginMaxFailures는 같은 아이디의 회차당 연속 실패 상한이다(계약 §5 기본 3, 사용자 결정 2026-11-26).
const productLearnedLoginMaxFailures = 3

// login_method_verified 줄의 고정 값이다(계약 §6-1-2).
const (
	productLoginVerifiedStage    = learnedlogin.StageConfirmation
	productLoginVerifiedEvidence = "e3_relogin"
)

// productLearnedMethodReader는 대상 계정 저널의 배운 방법(learned)을 읽는 경계다. 저장은 모델 로그인 PR(⑤-B)이 하므로 그
// PR이 레지스트리에 구현한다. 구현이 없으면(지금 dev) 저장된 방법은 쓰지 않는다. 결속한 대상 판·자격 판과 같은 방법만 준다.
type productLearnedMethodReader interface {
	learnedLoginMethod(targetID, label string, revision uint64, credentialSHA256 string) (learnedlogin.Learned, learnedlogin.Reason)
}

// productLoginVerified는 계정의 마지막 login_method_verified 줄이다. 값은 없다.
type productLoginVerified struct {
	methodSHA256     string
	targetRevision   uint64
	credentialSHA256 string
	runRef           string
	attemptRef       string
	at               time.Time
}

var (
	productLoginRunRef     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	productLoginAttemptRef = regexp.MustCompile(`^` + learnedlogin.StageConfirmation + `-[1-9][0-9]{0,8}$`)
)

// replayLoginMethodVerified는 login_method_verified 줄을 반영한다. 그때 계정이 있고, 그때 자격 판에 묶였으며, 칸이 계약
// 모양이어야 한다. learned 방법과의 대조는 상태를 계산할 때 한다(계약 §6-1-2: "지금 learned 방법의 해시와 같을 때만 상태에
// 반영"). 그래서 learned 저장이 아직 없거나 다른 방법이어도 줄 자체는 받는다. 같은 방법의 앞선 줄보다 대상 판·시각이 앞서면
// 쓰는 쪽(recordLoginMethodVerified)이 만들지 않는 줄이라 저널 손상이다.
func (r *productTargetRegistry) replayLoginMethodVerified(rec productTargetRecord, e productAccountEvent) error {
	st := r.accounts[e.TargetID][e.Label]
	if st == nil || !urlDigestValid(e.MethodSHA256) || !urlDigestValid(e.CredentialSHA256) || e.CredentialSHA256 != st.credentialSHA256 ||
		!productLoginRunRef.MatchString(e.RunRef) || e.Stage != productLoginVerifiedStage || !productLoginAttemptRef.MatchString(e.AttemptRef) ||
		e.Evidence != productLoginVerifiedEvidence || e.Login != nil || e.StatementSHA256 != "" || e.JobRef != "" {
		return errPreparation
	}
	if cur := st.loginVerified[e.MethodSHA256]; cur != nil && (e.TargetRevision < cur.targetRevision || e.At.Before(cur.at)) {
		return errPreparation
	}
	if st.loginVerified == nil {
		st.loginVerified = map[string]*productLoginVerified{}
	}
	st.loginVerified[e.MethodSHA256] = &productLoginVerified{methodSHA256: e.MethodSHA256, targetRevision: e.TargetRevision, credentialSHA256: e.CredentialSHA256,
		runRef: e.RunRef, attemptRef: e.AttemptRef, at: e.At}
	return nil
}

// loginMethodVerifiedFor는 계정 목록 API(⑤-B)가 상태를 계산할 때 쓴다. 지금 배운 방법의 해시(learnedMethodSHA256)로 그 방법의
// verified 줄을 찾고, 그 줄이 지금 대상 판·지금 자격 판과 같으며 그 방법이 지금 것이 된 뒤(methodSince, ⑤-B가 다른 방법에서
// 이 방법으로 바꾼 시각. 같은 방법을 다시 배우면 바뀌지 않음)에 검증됐을 때만 준다. 아니면 nil(verified 아님)이다. 그래서
// "A 검증 → B 배움 → A 다시 배움"에서 옛 A 검증은 다시 보이지 않는다(§6-1-1: 방법이 바뀌면 이전 검증 표시는 버림).
func loginMethodVerifiedFor(rec productTargetRecord, st *productAccountState, learnedMethodSHA256 string, methodSince time.Time) *productLoginVerified {
	v := st.loginVerified[learnedMethodSHA256]
	if v == nil || learnedMethodSHA256 == "" || v.targetRevision != rec.Target.Revision || st.credentialSHA256 == "" ||
		v.credentialSHA256 != st.credentialSHA256 || v.at.Before(methodSince) {
		return nil
	}
	out := *v
	return &out
}

// recordLoginMethodVerified는 확정 단계 프로그램 로그인이 그 방법(methodSHA256)으로 성공했다고 남긴다. at은 성공을 판정한
// 시각이다. 회차를 시작할 때 결속한 대상 판·자격 판이 지금도 같을 때만 쓴다(재현 도중 판이 바뀐 오래된 결과는 남기지 않음).
// 같은 방법·판·자격의 verified가 이미 있거나 같은 방법의 더 뒤 결과가 있으면 다시 쓰지 않는다. 레지스트리 잠금 안에서
// 확인하고 쓴다.
func (r *productTargetRegistry) recordLoginMethodVerified(targetID, label string, boundRevision uint64, boundCredential, methodSHA256, runRef, attemptRef string, at time.Time) (bool, error) {
	if !urlDigestValid(methodSHA256) || !productLoginRunRef.MatchString(runRef) || !productLoginAttemptRef.MatchString(attemptRef) || at.IsZero() {
		return false, errPreparation
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return false, errPreparation
	}
	rec, ok := r.records[targetID]
	if !ok {
		return false, errPreparation
	}
	st := r.accounts[targetID][label]
	if st == nil || rec.Target.Revision != boundRevision || st.credentialSHA256 == "" || st.credentialSHA256 != boundCredential {
		return false, nil
	}
	if cur := st.loginVerified[methodSHA256]; cur != nil && (cur.targetRevision == boundRevision && cur.credentialSHA256 == boundCredential ||
		at.UTC().Before(cur.at)) {
		return false, nil
	}
	if err := r.appendAccount(productAccountEvent{TargetID: targetID, Label: label, Action: accountLoginMethodVerified, Actor: rec.Principal, At: at.UTC(),
		TargetRevision: boundRevision, CredentialSHA256: boundCredential, MethodSHA256: methodSHA256, RunRef: runRef, Stage: productLoginVerifiedStage,
		AttemptRef: attemptRef, Evidence: productLoginVerifiedEvidence}); err != nil {
		return false, errPreparation
	}
	return true, nil
}

// usableLearnedAccounts는 확정 단계 계정 역할에 배운 방법을 반영한다(계약 §6·§6-1: 이 회차 성공이나 저장된 방법이 있으면 그
// 계정을 확정 단계에 쓰고, 둘 다 없을 때만 쓰지 않는다). 인증 결과가 authenticated가 아니어도 쓸 수 있는 배운 방법이 있으면
// Authenticated로 둔다. 실제 인증은 확정 직전 다시 로그인과 바로 뒤의 신원 확인이 정한다(실패하면 신원 미확인 UNDECIDABLE).
func usableLearnedAccounts(owner, cross e3contract.Account, usable func(label string) bool) (e3contract.Account, e3contract.Account) {
	for _, acct := range []*e3contract.Account{&owner, &cross} {
		if acct.Label != "" && !acct.Authenticated && usable != nil && usable(acct.Label) {
			acct.Authenticated = true
		}
	}
	return owner, cross
}

// productLearnedLogin은 회차 하나의 확정 단계 학습 로그인 재사용 상태다.
type productLearnedLogin struct {
	registry *productTargetRegistry
	// stored는 저장된 배운 방법을 읽는 경계다(⑤-B가 레지스트리에 구현). 없으면 이 회차 방법만 쓴다.
	stored   productLearnedMethodReader
	runsDir  string
	jobID    string
	targetID string
	// revision은 회차를 시작할 때 결속한 대상 판이다.
	revision uint64
	runRef   string
	// credentials는 실행 정책에 결속한 계정별 자격 판이다.
	credentials map[string]string
	selections  map[string]learnedlogin.Selection
	rows        []learnedlogin.Row
	rowsErr     error
	failures    map[string]int
	// failureGuard는 기록 집계가 바뀌어도 줄여서는 안 되는 재로그인 차단용 수다.
	failureGuard map[string]int
	nextAttempt  int
	// identityPath는 동결 선언의 신원 경로다. 세션 없는 대조 요청을 이 경로로 한 번 보낸다.
	identityPath string

	mu       sync.Mutex
	contrast *server.E2HTTPLoginContrastResult
	attempts map[string]*productProgramLogin
	identity map[string]e3contract.Observation
}

// productProgramLogin은 확정 단계에서 계정 하나를 배운 방법으로 다시 로그인한 결과다.
type productProgramLogin struct {
	learned learnedlogin.Learned
	result  server.E2HTTPLearnedLoginResult
	row     learnedlogin.Row
	// contrastProbes는 이 시도 동안 보낸 대조 요청 수다.
	contrastProbes int
}

// newProductLearnedLogin은 회차 기록과 실행 정책에서 학습 로그인 재사용 입력을 모은다. 기록을 읽지 못하면 이 회차 방법은
// 없다고 보고(사유 record_invalid) 저장된 방법만 쓴다.
func newProductLearnedLogin(registry *productTargetRegistry, runsDir, jobID string, target productTargetRecord, runRef string, credentials map[string]string, identityPath string) *productLearnedLogin {
	ll := &productLearnedLogin{registry: registry, runsDir: runsDir, jobID: jobID, targetID: target.Target.ID, revision: target.Target.Revision,
		runRef: runRef, credentials: credentials, identityPath: identityPath, attempts: map[string]*productProgramLogin{}, identity: map[string]e3contract.Observation{}}
	if reader, ok := any(registry).(productLearnedMethodReader); ok && registry != nil {
		ll.stored = reader
	}
	labels := make([]string, 0, len(credentials))
	for label := range credentials {
		labels = append(labels, label)
	}
	var skipped int
	ll.rows, skipped, ll.rowsErr = learnedlogin.ReadRows(filepath.Join(runsDir, jobID, "state"))
	if ll.rowsErr != nil {
		log.Printf("[e3-confirmation] 회차 %s 로그인 시도 기록을 읽지 못함 (%s, 형식 오류 %d건)", jobID, learnedlogin.ReasonRecordInvalid, skipped)
		ll.selections = map[string]learnedlogin.Selection{}
		for _, label := range labels {
			ll.selections[label] = learnedlogin.Selection{Reason: learnedlogin.ReasonRecordInvalid}
		}
	} else {
		ll.selections = learnedlogin.Select(ll.rows, runRef, labels)
	}
	ll.failures, ll.failureGuard = learnedlogin.ReplayFailureCounts(ll.rows, runRef, productLearnedLoginMaxFailures)
	ll.nextAttempt = learnedlogin.NextAttemptNumber(ll.rows)
	return ll
}

// plan은 계정의 확정 단계 로그인 방법이다: 이 회차 적격 성공 → 저장된 방법 → 없음(사유).
func (ll *productLearnedLogin) plan(label string) (learnedlogin.Learned, string, learnedlogin.Reason) {
	if sel, ok := ll.selections[label]; ok && sel.Reason == "" {
		return sel.Learned, "current_run", ""
	}
	credential, bound := ll.credentials[label]
	if !bound || ll.stored == nil {
		return learnedlogin.Learned{}, "", learnedlogin.ReasonNone
	}
	stored, reason := ll.stored.learnedLoginMethod(ll.targetID, label, ll.revision, credential)
	if reason != "" {
		return learnedlogin.Learned{}, "", reason
	}
	return stored, "stored", ""
}

// usable은 그 계정에 확정 단계에서 쓸 배운 방법(이 회차 성공 또는 저장된 방법)이 있는지다.
func (ll *productLearnedLogin) usable(label string) bool {
	_, _, reason := ll.plan(label)
	return reason == ""
}

// relogin은 확정 단계 직전 다시 로그인이다. 배운 방법이 있으면 그것으로만 로그인하고(실패해도 로그인 설정으로 대신하지
// 않음), 없으면 legacy(로그인 설정 9칸 다시 로그인)를 부른다. 대상에 보낸 요청 수를 돌려준다.
func (ll *productLearnedLogin) relogin(ctx context.Context, label string, learned server.E2HTTPLearnedRelogin, legacy server.E2HTTPAccountRelogin) int {
	plan, source, reason := ll.plan(label)
	if reason != "" {
		log.Printf("[e3-confirmation] 회차 %s 배운 로그인 방법 없음: %s", ll.jobID, reason)
		if legacy == nil {
			return 0
		}
		result := legacy.Relogin(ctx, label)
		log.Printf("[e3-confirmation] 회차 %s 확정 전 다시 로그인(로그인 설정): %s", ll.jobID, result)
		if server.E2HTTPAuthSent(result) {
			return 1
		}
		return 0
	}
	requests, probes := 0, 0
	ll.mu.Lock()
	tryContrast := ll.contrast == nil && ll.identityPath != ""
	ll.mu.Unlock()
	if tryContrast {
		res := learned.LoginContrast(ctx, ll.identityPath)
		ll.mu.Lock()
		ll.contrast = &res
		ll.mu.Unlock()
		if res.Sent {
			requests++
			probes++
		}
	}
	res := learned.LearnedRelogin(ctx, server.E2HTTPLearnedLoginRequest{Label: label, Method: plan.Method, PriorFailures: ll.failureGuardCopy(),
		MaxFailures: productLearnedLoginMaxFailures})
	requests += res.Requests
	log.Printf("[e3-confirmation] 회차 %s 확정 전 다시 로그인(배운 방법·%s): %s %s 요청 %d", ll.jobID, source, res.Result, res.Reason, res.Requests)
	attempt := &productProgramLogin{learned: plan, result: res, contrastProbes: probes}
	attempt.row = ll.programRow(label, attempt)
	ll.mu.Lock()
	ll.attempts[label] = attempt
	if attempt.row.Outcome == learnedlogin.OutcomeFailed && res.Identifier != "" {
		ll.failures[res.Identifier]++
		ll.failureGuard[res.Identifier]++
	}
	ll.mu.Unlock()
	ll.appendRow(attempt.row)
	return requests
}

func (ll *productLearnedLogin) failureGuardCopy() map[string]int {
	ll.mu.Lock()
	defer ll.mu.Unlock()
	return maps.Clone(ll.failureGuard)
}

// programRow는 확정 단계 프로그램 로그인 한 행이다(§3, actor program·stage confirmation). 로그인 응답만 본 결과이고,
// 성공 여부(대조)는 확정 단계 뒤 finish가 같은 시도 행을 덧붙여 정한다.
func (ll *productLearnedLogin) programRow(label string, a *productProgramLogin) learnedlogin.Row {
	m, res := a.learned.Method, a.result
	ll.mu.Lock()
	n := ll.nextAttempt
	ll.nextAttempt++
	prior := ll.failures[res.Identifier]
	ll.mu.Unlock()
	at := res.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	row := learnedlogin.Row{Schema: learnedlogin.RowSchema, AttemptRef: learnedlogin.StageConfirmation + "-" + strconv.Itoa(n), RunRef: ll.runRef,
		Stage: learnedlogin.StageConfirmation, Actor: learnedlogin.ActorProgram, TransportRef: "", At: at.Format(time.RFC3339Nano),
		AccountLabel: label, Identifier: res.Identifier, PasswordRegistered: res.Identifier != "", Method: m.Method, Path: m.Path,
		QueryKeys: []string{}, Format: m.Format, IdentifierField: m.IdentifierField, PasswordField: m.PasswordField,
		OtherFields: append([]string{}, m.OtherFields...), FieldsFromResponse: append([]string{}, m.FieldsFromResponse...),
		Status: res.Status, RedirectPath: res.RedirectPath, LoginFormReturned: res.LoginFormReturned, SessionSource: learnedlogin.SourceNone,
		SessionNames: []string{}, SessionSeenAnonymous: []string{}, Followup: learnedlogin.FollowupNone, ContrastProbes: a.contrastProbes,
		ConsecutiveFailures: prior}
	switch res.Transport {
	case learnedlogin.TransportSent:
		row.Transport, row.TransportRef = learnedlogin.TransportSent, res.TransportRef
	case learnedlogin.TransportUnknown:
		row.Transport, row.TransportRef, row.Outcome = learnedlogin.TransportUnknown, res.TransportRef, learnedlogin.OutcomeUnknown
		return row
	default:
		row.Transport, row.Outcome, row.Refusal = learnedlogin.TransportNotSent, learnedlogin.OutcomeNotSent, string(res.Reason)
		if row.Refusal == "" {
			row.Refusal = string(learnedlogin.ReasonNotSent)
		}
		return row
	}
	switch {
	case res.Result == server.E2HTTPAuthAuthenticated:
		row.SessionSource, row.SessionNames, row.SessionSentAs = m.SessionSource, append([]string{}, m.SessionNames...), m.SessionSentAs
		row.Outcome = learnedlogin.OutcomeUnconfirmed
	case res.Reason == learnedlogin.ReasonAuthUnconfirmed:
		// 응답을 끝까지 판정하지 못했다(보류). 실패 신호를 본 것이 아니므로 연속 실패에 세지 않는다.
		row.Outcome = learnedlogin.OutcomeUnconfirmed
	default:
		row.Outcome = learnedlogin.OutcomeFailed
		row.ConsecutiveFailures = prior + 1
	}
	return row
}

// observeIdentity는 확정 단계 신원 관찰을 받는다(Transport 감싸기). 계정마다 처음 관찰만 쓴다.
func (ll *productLearnedLogin) observeIdentity(label string, obs e3contract.Observation) {
	ll.mu.Lock()
	defer ll.mu.Unlock()
	// 두 계정 신원이 다른지도 보므로 배운 방법을 쓰지 않은 계정의 신원도 받는다. 확정 단계 신원은 다시 로그인 뒤에만 나간다.
	if _, seen := ll.identity[label]; !seen {
		ll.identity[label] = obs
	}
}

// finish는 확정 단계가 끝난 뒤 프로그램 로그인 성공을 판정하고(행 덧붙이기), 이 회차 배운 방법과 검증 결과를 계정 저널에
// 남긴다. bindingRef는 지금 계정 결속 참조다(다시 로그인 뒤 세션이 바뀌었는지 본다). accepted는 확정 단계가 끝까지 돌아
// (completed) 신원 확인을 통과시킨 판정의 근거 호출이다(E② 봉인 대조·관찰 검증을 지난 신원 관찰만 든다). 신원 탭은 그
// 검증 전에 관찰을 받으므로, 근거에 들지 않은 관찰(진위 실패·봉인 조회 시한 끝·중단·꺼진 회차)로는 성공을 세우지 않는다
// (Codex 2·3차 리뷰).
func (ll *productLearnedLogin) finish(bindingRef func(label string) string, accepted map[productEvidenceKey]bool) {
	ll.mu.Lock()
	attempts := make(map[string]*productProgramLogin, len(ll.attempts))
	for k, v := range ll.attempts {
		attempts[k] = v
	}
	identity := make(map[string]e3contract.Observation, len(ll.identity))
	for k, v := range ll.identity {
		identity[k] = v
	}
	contrast := ll.contrast
	ll.mu.Unlock()
	loginPaths := map[string]bool{}
	for _, a := range attempts {
		for _, p := range a.result.LoginPaths {
			loginPaths[p] = true
		}
	}
	paths := make([]string, 0, len(loginPaths))
	for p := range loginPaths {
		paths = append(paths, p)
	}
	blocked := contrast != nil && contrast.Sent && contrast.Reason == "" && learnedlogin.Blocked(contrast.Status, contrast.RedirectPath, paths)
	// 두 계정(A·B) 신원이 다르다는 근거는 정상 신원 관찰이 둘 이상 있고 그 값이 서로 다를 때만이다. 상대 계정 관찰이 없으면
	// 값이 "하나뿐"이어도 다르다는 근거가 아니다(Codex 리뷰).
	normalIdentity := func(label string, obs e3contract.Observation) bool {
		// 신원 관찰에는 리다이렉트 목적지가 없으므로 2xx만 열림으로 본다(로그인 화면으로 가는 3xx를 열림으로 세지 않게).
		return accepted[evidenceKey(obs.Ref)] && obs.AccountLabel == label && obs.Sent && obs.ResponseComplete && obs.Normal == oracle.StateYes && obs.SubjectDigest != "" &&
			obs.StatusCode >= 200 && obs.StatusCode <= 299 && learnedlogin.Open(obs.StatusCode, "", paths)
	}
	digests, subjects := map[string]int{}, 0
	for label, obs := range identity {
		if normalIdentity(label, obs) {
			digests[obs.SubjectDigest]++
			subjects++
		}
	}
	verified := map[string]*productProgramLogin{}
	for label, a := range attempts {
		if a.row.Outcome != learnedlogin.OutcomeUnconfirmed || a.result.Result != server.E2HTTPAuthAuthenticated {
			continue
		}
		obs, ok := identity[label]
		open := ok && normalIdentity(label, obs) && subjects >= 2 && digests[obs.SubjectDigest] == 1
		same := bindingRef != nil && a.result.BindingRef != "" && bindingRef(label) == a.result.BindingRef
		row := a.row
		if open && blocked && same {
			row.Outcome, row.Followup, row.ContrastSource, row.ConsecutiveFailures = learnedlogin.OutcomeSucceeded, learnedlogin.FollowupContrast, learnedlogin.ContrastProbe, 0
			verified[label] = a
		} else {
			log.Printf("[e3-confirmation] 회차 %s 프로그램 로그인 성공 근거 부족: %s", ll.jobID, learnedlogin.ReasonAuthUnconfirmed)
			if ok && obs.Sent && obs.Normal == oracle.StateYes {
				row.Followup = "no_login_form"
			}
		}
		if row.Outcome != a.row.Outcome || row.Followup != a.row.Followup {
			ll.appendRow(row)
		}
	}
	// 배운 방법(learned) 저장은 모델 로그인 PR(⑤-B)이 한다. 여기서는 프로그램 재현 성공(verified)만 남긴다.
	if ll.registry == nil {
		return
	}
	// verified 줄의 at은 성공을 판정한 시각이다(계약 §6-1-2). 행의 at(로그인을 보낸 시각)과 다르다.
	judged := time.Now().UTC()
	for label, a := range verified {
		if _, err := ll.registry.recordLoginMethodVerified(ll.targetID, label, ll.revision, ll.credentials[label], a.learned.Method.Digest(), ll.runRef,
			a.row.AttemptRef, judged); err != nil {
			log.Printf("[e3-confirmation] 회차 %s 로그인 방법 검증 저장 실패", ll.jobID)
		}
	}
}

// appendRow는 회차 상태 디렉터리의 로그인 시도 기록에 한 행을 덧붙인다(0600, 링크 아님). 쓰지 못해도 확정 단계는 멈추지
// 않는다(감사 기록이 아님, 판정기와 같은 처리). 디렉터리 밖은 가리키지 못한다(os.Root).
func (ll *productLearnedLogin) appendRow(row learnedlogin.Row) {
	if err := appendLoginAttemptRow(ll.runsDir, ll.jobID, row); err != nil {
		log.Printf("[e3-confirmation] 회차 %s 로그인 시도 기록을 쓰지 못함", ll.jobID)
	}
}

func appendLoginAttemptRow(runsDir, jobID string, row learnedlogin.Row) error {
	raw, err := json.Marshal(row)
	if err != nil || !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID {
		return errPreparation
	}
	parent, err := os.OpenRoot(runsDir)
	if err != nil {
		return errPreparation
	}
	defer parent.Close()
	root, err := parent.OpenRoot(filepath.Join(jobID, "state"))
	if err != nil {
		return errPreparation
	}
	defer root.Close()
	if info, statErr := root.Lstat(learnedlogin.AttemptsFile); statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
		return errPreparation
	} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return errPreparation
	}
	f, err := root.OpenFile(learnedlogin.AttemptsFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return errPreparation
	}
	_, writeErr := f.Write(append(raw, '\n'))
	return errors.Join(writeErr, f.Close())
}

// productEvidenceKey는 확정 단계 근거 호출 하나다(호출·시도 참조).
type productEvidenceKey struct{ callRef, attemptID string }

func evidenceKey(ref e3contract.CallRef) productEvidenceKey {
	return productEvidenceKey{callRef: ref.CallRef, attemptID: ref.AttemptID}
}

// acceptedIdentityEvidence는 확정 단계가 completed로 끝났을 때 신원 확인이 통과한(identity_check yes) 판정의 근거 호출이다.
// 그 밖의 결과(진위 실패·중단·꺼짐)는 빈 집합이다.
func acceptedIdentityEvidence(res e3contract.RunResult) map[productEvidenceKey]bool {
	out := map[productEvidenceKey]bool{}
	if res.Status != e3contract.StatusCompleted {
		return out
	}
	for _, v := range res.Verdicts {
		if v.IdentityCheck != oracle.StateYes {
			continue
		}
		for _, ref := range v.Evidence {
			out[evidenceKey(ref)] = true
		}
	}
	return out
}

// productIdentityTap은 확정 단계 Transport를 감싸 신원 관찰을 학습 로그인 판정에 넘긴다. 요청·관찰은 바꾸지 않는다.
type productIdentityTap struct {
	e3contract.Transport
	observe func(label string, obs e3contract.Observation)
}

func (t productIdentityTap) Identity(ctx context.Context, req e3contract.IdentityRequest) (e3contract.Observation, error) {
	obs, err := t.Transport.Identity(ctx, req)
	if err == nil && t.observe != nil {
		t.observe(req.AccountLabel, obs)
	}
	return obs, err
}

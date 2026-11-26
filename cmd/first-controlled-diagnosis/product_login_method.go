package main

import (
	"errors"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/learnedlogin"
)

// 배운 로그인 방법(learned) 저장 — docs/contracts/model-login-attempts.md §6-1. 모델 로그인 PR(⑤-B, 최민준)이 저장하고,
// 확정 단계 재사용·verified(product_learned_login.go, 이동찬)가 읽는다. 값(아이디·비밀번호·세션)은 저널·API에 없다.

const (
	accountLoginMethodLearned = "login_method_learned"
	// 배운 근거: 회차에 이미 오간 세션 없는 막힘과 대조(observed) · 프로그램 대조 요청으로 대조(probe).
	productLoginEvidenceObserved = "observed_contrast"
	productLoginEvidenceProbe    = "probe_contrast"
	// 계정 목록 응답 상태(§6-1-3).
	productLoginMethodNone     = "none"
	productLoginMethodLearned  = "learned"
	productLoginMethodVerified = "verified"
)

var productLoginLearnedAttemptRef = regexp.MustCompile(`^(` + learnedlogin.StageExploration + `|` + learnedlogin.StageDiagnosis + `)-[1-9][0-9]{0,8}$`)

// productLoginLearned는 계정의 마지막 login_method_learned 줄이다. since는 이 방법이 지금 방법이 된 시각이다(같은 방법을
// 다시 배우면 그대로, 다른 방법으로 바뀌면 그 줄의 시각). verified는 since 뒤의 검증만 인정한다(loginMethodVerifiedFor).
type productLoginLearned struct {
	method           learnedlogin.Method
	methodSHA256     string
	targetRevision   uint64
	credentialSHA256 string
	runRef           string
	stage            string
	attemptRef       string
	evidence         string
	at               time.Time
	since            time.Time
}

// replayLoginMethodLearned는 login_method_learned 줄을 반영한다. 그때 계정이 있고 그때 자격 판에 묶였으며, 방법이 재현할 수
// 있는 모양이고 해시가 방법과 같아야 한다. 앞 줄보다 시각이 앞서면 쓰는 쪽이 만들지 않는 줄이라 저널 손상이다.
func (r *productTargetRegistry) replayLoginMethodLearned(e productAccountEvent) error {
	st := r.accounts[e.TargetID][e.Label]
	if st == nil || e.LoginMethod == nil || e.LoginMethod.Validate() != "" || e.LoginMethod.Digest() != e.MethodSHA256 ||
		!urlDigestValid(e.CredentialSHA256) || e.CredentialSHA256 != st.credentialSHA256 || !productLoginRunRef.MatchString(e.RunRef) ||
		!productLoginLearnedAttemptRef.MatchString(e.AttemptRef) || !strings.HasPrefix(e.AttemptRef, e.Stage+"-") ||
		e.Evidence != productLoginEvidenceObserved && e.Evidence != productLoginEvidenceProbe || e.Login != nil || e.StatementSHA256 != "" || e.JobRef != "" {
		return errPreparation
	}
	cur := st.loginLearned
	if cur != nil && e.At.Before(cur.at) {
		return errPreparation
	}
	since := e.At
	if cur != nil && cur.methodSHA256 == e.MethodSHA256 && cur.targetRevision == e.TargetRevision && cur.credentialSHA256 == e.CredentialSHA256 {
		since = cur.since
	}
	st.loginLearned = &productLoginLearned{method: e.LoginMethod.Clone(), methodSHA256: e.MethodSHA256, targetRevision: e.TargetRevision,
		credentialSHA256: e.CredentialSHA256, runRef: e.RunRef, stage: e.Stage, attemptRef: e.AttemptRef, evidence: e.Evidence, at: e.At, since: since}
	return nil
}

// recordLoginMethodLearned는 모델 로그인 성공(§4 authenticated_contrast)에서 배운 방법을 남긴다. 회차를 시작할 때 결속한 대상
// 판·자격 판이 지금도 같을 때만 쓴다. 같은 시도를 이미 남겼거나 더 뒤 결과가 있으면 다시 쓰지 않는다. 레지스트리 잠금 안에서
// 확인하고 쓴다.
func (r *productTargetRegistry) recordLoginMethodLearned(targetID, label string, boundRevision uint64, boundCredential string, learned learnedlogin.Learned, evidence string) (bool, error) {
	m := learned.Method.Clone()
	stage, _, _ := strings.Cut(learned.Source.AttemptRef, "-")
	if m.Validate() != "" || !productLoginRunRef.MatchString(learned.Source.RunRef) || !productLoginLearnedAttemptRef.MatchString(learned.Source.AttemptRef) ||
		learned.Source.At.IsZero() || evidence != productLoginEvidenceObserved && evidence != productLoginEvidenceProbe {
		return false, errPreparation
	}
	at := learned.Source.At.UTC()
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
	if cur := st.loginLearned; cur != nil && (cur.runRef == learned.Source.RunRef && cur.attemptRef == learned.Source.AttemptRef || at.Before(cur.at)) {
		return false, nil
	}
	if err := r.appendAccount(productAccountEvent{TargetID: targetID, Label: label, Action: accountLoginMethodLearned, Actor: rec.Principal, At: at,
		TargetRevision: boundRevision, CredentialSHA256: boundCredential, LoginMethod: &m, MethodSHA256: m.Digest(), RunRef: learned.Source.RunRef,
		Stage: stage, AttemptRef: learned.Source.AttemptRef, Evidence: evidence}); err != nil {
		return false, errPreparation
	}
	return true, nil
}

// learnedLoginMethod는 확정 단계 재사용이 읽는 저장된 방법이다(productLearnedMethodReader). 회차에 결속한 대상 판·자격 판과
// 같은 방법만 준다.
func (r *productTargetRegistry) learnedLoginMethod(targetID, label string, revision uint64, credentialSHA256 string) (learnedlogin.Learned, learnedlogin.Reason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return learnedlogin.Learned{}, learnedlogin.ReasonRecordInvalid
	}
	st := r.accounts[targetID][label]
	if st == nil || st.loginLearned == nil {
		return learnedlogin.Learned{}, learnedlogin.ReasonNone
	}
	l := st.loginLearned
	switch {
	case l.targetRevision != revision:
		return learnedlogin.Learned{}, learnedlogin.ReasonTargetChanged
	case l.credentialSHA256 != credentialSHA256 || st.credentialSHA256 != credentialSHA256:
		return learnedlogin.Learned{}, learnedlogin.ReasonCredentialChanged
	}
	return learnedlogin.Learned{Method: l.method.Clone(), Source: learnedlogin.Source{RunRef: l.runRef, AttemptRef: l.attemptRef, At: l.at}}, ""
}

// loginMethodView는 계정 목록 응답의 login_method다(§6-1-3). 저장된 방법이 지금 대상 판·지금 자격 판에 묶여 있을 때만 쓰고,
// 아니면 none과 그 까닭이다. verified는 지금 방법이 된 뒤의 검증만이다.
func loginMethodView(rec productTargetRecord, st *productAccountState) *diagnosisprep.ProductLoginMethod {
	l := st.loginLearned
	switch {
	case l == nil:
		return &diagnosisprep.ProductLoginMethod{State: productLoginMethodNone}
	case l.targetRevision != rec.Target.Revision:
		return &diagnosisprep.ProductLoginMethod{State: productLoginMethodNone, ClearedReason: "target_reapproved"}
	case st.credentialSHA256 == "" || l.credentialSHA256 != st.credentialSHA256:
		return &diagnosisprep.ProductLoginMethod{State: productLoginMethodNone, ClearedReason: "credential_changed"}
	}
	m := l.method.Clone()
	out := &diagnosisprep.ProductLoginMethod{State: productLoginMethodLearned,
		Learned: &diagnosisprep.ProductLoginMethodLearned{At: l.at, RunRef: l.runRef, Stage: l.stage, AttemptRef: l.attemptRef, Evidence: l.evidence},
		Method: &diagnosisprep.ProductLoginMethodObject{HTTPMethod: m.Method, Path: m.Path, Format: m.Format, IdentifierField: m.IdentifierField,
			PasswordField: m.PasswordField, OtherFields: m.OtherFields, FieldsFromResponse: m.FieldsFromResponse, SessionSource: m.SessionSource,
			SessionNames: m.SessionNames, SessionSentAs: m.SessionSentAs}}
	if v := loginMethodVerifiedFor(rec, st, l.methodSHA256, l.since); v != nil {
		out.State = productLoginMethodVerified
		out.Verified = &diagnosisprep.ProductLoginMethodProof{At: v.at, RunRef: v.runRef, AttemptRef: v.attemptRef, Evidence: productLoginVerifiedEvidence}
	}
	return out
}

// learnLoginMethods는 이 회차 로그인 시도 기록에서 결속 계정마다 배운 방법(§6 적격 성공 가운데 마지막)을 골라 남긴다.
// 확정 단계 직전(재사용·verified보다 먼저)과 회차 마감 때 부른다. 같은 시도는 다시 쓰지 않는다. 읽거나 쓰지 못하면 로그만
// 남긴다(배운 방법이 없을 뿐, 회차는 멈추지 않는다).
func (r *productTargetRegistry) learnLoginMethods(runsDir, jobID string, target productTargetRecord) {
	if r == nil {
		return
	}
	binding, err := openRunBinding(runsDir, jobID)
	if err != nil {
		return
	}
	policy := binding.policy
	if binding.Close() != nil {
		log.Printf("[login-method] 회차 %s 실행 정책 닫기 실패", jobID)
	}
	if policy.Auth == nil || !policy.Auth.ModelLogin || len(policy.Auth.Accounts) == 0 {
		return
	}
	credentials := map[string]string{}
	labels := make([]string, 0, len(policy.Auth.Accounts))
	for _, a := range policy.Auth.Accounts {
		credentials[a.Label] = a.Credential.SHA256
		labels = append(labels, a.Label)
	}
	rows, _, err := learnedlogin.ReadRows(filepath.Join(runsDir, jobID, "state"))
	if err != nil {
		log.Printf("[login-method] 회차 %s 로그인 시도 기록을 읽지 못함(배운 방법 없음)", jobID)
		return
	}
	if r.learnFromRows(target, policy.RunRef, credentials, labels, rows) != nil {
		log.Printf("[login-method] 회차 %s 배운 로그인 방법을 남기지 못함", jobID)
	}
}

// learnFromRows는 회차 기록 행에서 결속 계정마다 적격 성공(learnedlogin.Select) 가운데 마지막을 남긴다. 근거는 그 시도의
// contrast_source다(대조를 세운 행만 적격이라 늘 있다).
func (r *productTargetRegistry) learnFromRows(target productTargetRecord, runRef string, credentials map[string]string, labels []string, rows []learnedlogin.Row) error {
	evidence := map[string]string{}
	for _, row := range rows {
		switch row.ContrastSource {
		case "observed":
			evidence[row.AttemptRef] = productLoginEvidenceObserved
		case "probe":
			evidence[row.AttemptRef] = productLoginEvidenceProbe
		}
	}
	var errs []error
	for label, sel := range learnedlogin.Select(rows, runRef, labels) {
		if sel.Reason != "" || evidence[sel.Learned.Source.AttemptRef] == "" {
			continue
		}
		if _, err := r.recordLoginMethodLearned(target.Target.ID, label, target.Target.Revision, credentials[label], sel.Learned, evidence[sel.Learned.Source.AttemptRef]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

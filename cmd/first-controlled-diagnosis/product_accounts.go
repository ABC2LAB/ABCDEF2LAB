package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 대상 계정 분류(C14 결정 1·2, docs/account-login-decision.md). 계정은 대상 등록에 붙고 다른 대상에서 쓸 수 없다.
// 분류 기본값은 real이고, test는 그 대상을 승인한 운영자가 진술에 동의해 표시한 판이 지금도 같은 승인 판일 때만이다.
// 대상 판이 바뀌면(재승인·철회) 저장된 표시는 남아도 real로 읽는다(target_reapproved). 표시를 떼면 manual_unmark다.
// 이 저널이 감사 기록이다: 누가·언제·어느 대상 판·어느 라벨을 등록·표시·해제했는지. 자격 값은 넣지 않는다.

// productAccountStatement는 시험 계정 표시 때 승인자가 동의하는 진술이다. 저널에는 이 문장의 SHA-256만 남긴다.
const productAccountStatement = "이 계정은 우리 조직이 소유한 일회용 시험 계정이고 실제 사용자 데이터가 없으며, 대상 운영 주체가 이 계정으로 시험하는 것을 허락했다."

func productAccountStatementSHA256() string {
	sum := sha256.Sum256([]byte(productAccountStatement))
	return hex.EncodeToString(sum[:])
}

type productAccountEvent struct {
	TargetID        string    `json:"target_id"`
	Label           string    `json:"label"`
	Action          string    `json:"action"`
	Actor           string    `json:"actor"`
	At              time.Time `json:"at"`
	TargetRevision  uint64    `json:"target_revision"`
	StatementSHA256 string    `json:"statement_sha256,omitempty"`
}

const (
	accountRegister = "register"
	accountMarkTest = "mark_test"
	accountUnmark   = "unmark"
)

type productAccountState struct {
	label          string
	registeredBy   string
	registeredAt   time.Time
	marked         bool
	markedRevision uint64
	classifiedBy   string
	classifiedAt   time.Time
	unmarked       bool
}

// replayAccount는 저널 한 줄을 상태에 반영한다. 대상 저널을 먼저 다 읽은 뒤에 부른다.
func (r *productTargetRegistry) replayAccount(e productAccountEvent) error {
	rec, ok := r.records[e.TargetID]
	if !ok || !diagnosisprep.ValidAccountLabel(e.Label) || e.Actor != rec.Principal || e.At.IsZero() || e.TargetRevision < 1 || e.TargetRevision > rec.Target.Revision {
		return errPreparation
	}
	byTarget := r.accounts[e.TargetID]
	if byTarget == nil {
		byTarget = map[string]*productAccountState{}
		r.accounts[e.TargetID] = byTarget
	}
	st := byTarget[e.Label]
	switch e.Action {
	case accountRegister:
		if st != nil || len(byTarget) >= diagnosisprep.MaxTargetAccounts || e.StatementSHA256 != "" {
			return errPreparation
		}
		byTarget[e.Label] = &productAccountState{label: e.Label, registeredBy: e.Actor, registeredAt: e.At}
	case accountMarkTest:
		if st == nil || e.StatementSHA256 != productAccountStatementSHA256() {
			return errPreparation
		}
		st.marked, st.unmarked, st.markedRevision, st.classifiedBy, st.classifiedAt = true, false, e.TargetRevision, e.Actor, e.At
	case accountUnmark:
		if st == nil || !st.marked || e.StatementSHA256 != "" {
			return errPreparation
		}
		st.marked, st.unmarked, st.classifiedBy, st.classifiedAt = false, true, e.Actor, e.At
	default:
		return errPreparation
	}
	return nil
}

func (r *productTargetRegistry) appendAccount(e productAccountEvent) error {
	raw, err := json.Marshal(e)
	if err != nil || r.check() != nil {
		return errPreparation
	}
	if err = r.accountJournal.Append(raw); err != nil {
		return err
	}
	return r.replayAccount(e)
}

// accountView는 지금 대상 판 기준의 분류다.
func accountView(rec productTargetRecord, st *productAccountState) diagnosisprep.ProductAccount {
	a := diagnosisprep.ProductAccount{Label: st.label, Classification: "real", RegisteredBy: st.registeredBy, RegisteredAt: st.registeredAt}
	switch {
	case st.marked:
		a.ClassifiedBy, a.ClassifiedAt, a.MarkedRevision = st.classifiedBy, st.classifiedAt, st.markedRevision
		if rec.Target.State == "approved" && rec.Target.Revision == st.markedRevision {
			a.Classification = "test"
		} else {
			a.Reason = "target_reapproved"
		}
	case st.unmarked:
		a.ClassifiedBy, a.ClassifiedAt, a.Reason = st.classifiedBy, st.classifiedAt, "manual_unmark"
	}
	return a
}

// accountTarget은 운영자 소유 대상과 그 계정 묶음을 찾는다. r.mu를 잡은 채로 부른다.
func (r *productTargetRegistry) accountTarget(principal, id string) (productTargetRecord, map[string]*productAccountState, error) {
	if r.check() != nil {
		return productTargetRecord{}, nil, errPreparation
	}
	rec, ok := r.records[id]
	if !ok || rec.Principal != principal {
		return productTargetRecord{}, nil, errors.New("target_not_found")
	}
	return rec, r.accounts[id], nil
}

func (r *productTargetRegistry) listAccounts(principal, id string) ([]diagnosisprep.ProductAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, byTarget, err := r.accountTarget(principal, id)
	if err != nil {
		return nil, err
	}
	out := make([]diagnosisprep.ProductAccount, 0, len(byTarget))
	for _, st := range byTarget {
		out = append(out, accountView(rec, st))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// registerAccount는 대상에 계정 라벨을 붙인다. 분류는 real로 시작한다. 철회된 대상에는 붙이지 않는다.
func (r *productTargetRegistry) registerAccount(principal, id, label string) (diagnosisprep.ProductAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, byTarget, err := r.accountTarget(principal, id)
	if err != nil {
		return diagnosisprep.ProductAccount{}, err
	}
	switch {
	case !diagnosisprep.ValidAccountLabel(label):
		return diagnosisprep.ProductAccount{}, errors.New("invalid_account_label")
	case rec.Target.State == "revoked":
		return diagnosisprep.ProductAccount{}, errors.New("target_not_approved")
	case byTarget[label] != nil:
		return diagnosisprep.ProductAccount{}, errors.New("account_exists")
	case len(byTarget) >= diagnosisprep.MaxTargetAccounts:
		return diagnosisprep.ProductAccount{}, errors.New("account_capacity")
	}
	if err := r.appendAccount(productAccountEvent{TargetID: id, Label: label, Action: accountRegister, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision}); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	return accountView(rec, r.accounts[id][label]), nil
}

// markTestAccount는 승인된 대상의 승인자가 진술에 동의해 계정을 시험 계정으로 표시한다. 표시는 지금 대상 판에만 묶인다.
func (r *productTargetRegistry) markTestAccount(principal, id, label string, statementConfirmed bool) (diagnosisprep.ProductAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, byTarget, err := r.accountTarget(principal, id)
	if err != nil {
		return diagnosisprep.ProductAccount{}, err
	}
	st := byTarget[label]
	switch {
	case st == nil:
		return diagnosisprep.ProductAccount{}, errors.New("account_not_found")
	case !statementConfirmed:
		return diagnosisprep.ProductAccount{}, errors.New("statement_required")
	case rec.Target.State != "approved" || !r.now().Before(rec.Target.ExpiresAt):
		return diagnosisprep.ProductAccount{}, errors.New("target_not_approved")
	}
	// 승인자 = 대상 등록 기록의 운영자(DNS 소유 증명과 본인 진술을 한 사람)다.
	if principal != rec.Principal {
		return diagnosisprep.ProductAccount{}, errors.New("operator_required")
	}
	if view := accountView(rec, st); view.Classification == "test" {
		return view, nil
	}
	if err := r.appendAccount(productAccountEvent{TargetID: id, Label: label, Action: accountMarkTest, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision,
		StatementSHA256: productAccountStatementSHA256()}); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	return accountView(rec, st), nil
}

// unmarkAccount는 시험 계정 표시를 뗀다(더 안전한 쪽이라 대상 상태와 관계없이 받는다). 표시가 없으면 기록하지 않는다.
func (r *productTargetRegistry) unmarkAccount(principal, id, label string) (diagnosisprep.ProductAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, byTarget, err := r.accountTarget(principal, id)
	if err != nil {
		return diagnosisprep.ProductAccount{}, err
	}
	st := byTarget[label]
	if st == nil {
		return diagnosisprep.ProductAccount{}, errors.New("account_not_found")
	}
	if !st.marked {
		return accountView(rec, st), nil
	}
	if err := r.appendAccount(productAccountEvent{TargetID: id, Label: label, Action: accountUnmark, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision}); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	return accountView(rec, st), nil
}

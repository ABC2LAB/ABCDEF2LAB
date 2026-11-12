package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// Login은 login_config 줄의 대상 로그인 방식(자격 값 없음), CredentialSHA256은 credential 줄의 보호 파일 해시다.
	Login            *diagnosisprep.TargetLogin `json:"login,omitempty"`
	CredentialSHA256 string                     `json:"credential_sha256,omitempty"`
}

const (
	accountRegister    = "register"
	accountMarkTest    = "mark_test"
	accountUnmark      = "unmark"
	accountLoginConfig = "login_config"
	accountCredential  = "credential"
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
	// credentialSHA256은 마지막으로 쓴 자격 보호 파일의 해시다. 파일이 지금도 이 해시일 때만 자격이 있다고 본다.
	credentialSHA256 string
}

// replayAccount는 저널 한 줄을 상태에 반영한다. 대상 저널을 먼저 다 읽은 뒤에 부른다.
func (r *productTargetRegistry) replayAccount(e productAccountEvent) error {
	rec, ok := r.records[e.TargetID]
	if !ok || e.Actor != rec.Principal || e.At.IsZero() || e.TargetRevision < 1 || e.TargetRevision > rec.Target.Revision {
		return errPreparation
	}
	if e.Action == accountLoginConfig {
		if e.Label != "" || e.Login == nil || !validTargetLoginFor(rec, *e.Login) || e.StatementSHA256 != "" || e.CredentialSHA256 != "" {
			return errPreparation
		}
		login := *e.Login
		login.Cookies = append([]string(nil), login.Cookies...)
		r.logins[e.TargetID] = login
		return nil
	}
	if !diagnosisprep.ValidAccountLabel(e.Label) || e.Login != nil || (e.CredentialSHA256 != "") != (e.Action == accountCredential) {
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
		// 해제하면 자격도 지운다(실계정 자격을 필요 이상 들고 있지 않는다).
		st.marked, st.unmarked, st.classifiedBy, st.classifiedAt, st.credentialSHA256 = false, true, e.Actor, e.At, ""
	case accountCredential:
		if st == nil || !urlDigestValid(e.CredentialSHA256) || e.StatementSHA256 != "" {
			return errPreparation
		}
		st.credentialSHA256 = e.CredentialSHA256
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

// accountView는 지금 대상 판·시각 기준의 분류다. 목록과 이후 실행 결속이 이 판정 하나를 쓴다. 대상 판이 바뀌면(재승인·철회)
// target_reapproved, 승인이 만료됐으면 target_expired로 real이다(판단할 수 없으면 실계정).
func accountView(rec productTargetRecord, st *productAccountState, now time.Time) diagnosisprep.ProductAccount {
	a := diagnosisprep.ProductAccount{Label: st.label, Classification: "real", RegisteredBy: st.registeredBy, RegisteredAt: st.registeredAt}
	switch {
	case st.marked:
		a.ClassifiedBy, a.ClassifiedAt, a.MarkedRevision = st.classifiedBy, st.classifiedAt, st.markedRevision
		switch {
		case rec.Target.State != "approved" || rec.Target.Revision != st.markedRevision:
			a.Reason = "target_reapproved"
		case !now.Before(rec.Target.ExpiresAt):
			a.Reason = "target_expired"
		default:
			a.Classification = "test"
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
		view := accountView(rec, st, r.now())
		view.CredentialConfigured = r.credentialConfigured(id, st)
		out = append(out, view)
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
	return accountView(rec, r.accounts[id][label], r.now()), nil
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
	// 승인자 = 대상 등록 기록의 운영자(DNS 소유 증명과 본인 진술을 한 사람)다. accountTarget이 이미 그 운영자만 받는다.
	if view := accountView(rec, st, r.now()); view.Classification == "test" {
		return view, nil
	}
	if err := r.appendAccount(productAccountEvent{TargetID: id, Label: label, Action: accountMarkTest, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision,
		StatementSHA256: productAccountStatementSHA256()}); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	return accountView(rec, st, r.now()), nil
}

// unmarkAccount는 시험 계정 표시를 뗀다(더 안전한 쪽이라 대상 상태와 관계없이 받는다). 표시가 없으면 기록하지 않는다.
// 결정안은 해제를 "그 대상에 권한이 있는 누구나"로 둔다. 지금 대상 권한은 등록한 운영자 한 사람뿐이라 accountTarget과
// 같다. 권한 모델이 넓어지면 표시(승인자만)는 그대로 두고 해제 쪽만 넓힌다.
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
		return accountView(rec, st, r.now()), nil
	}
	// 해제는 더 안전한 쪽이라 자격 파일을 못 지워도 기록한다. 저널에서 자격 해시가 빠지므로 남은 파일은 쓰이지 않는다.
	if err := r.removeCredential(id, label); err != nil {
		log.Printf("[preparation] 시험 계정 해제 때 자격 파일 삭제 실패: 대상=%s", id)
	}
	if err := r.appendAccount(productAccountEvent{TargetID: id, Label: label, Action: accountUnmark, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision}); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	return accountView(rec, st, r.now()), nil
}

// validTargetLoginFor는 로그인 방식이 형식에 맞고 경로가 그 대상의 승인 경로 접두 안이며 정규 경로인지 본다.
func validTargetLoginFor(rec productTargetRecord, l diagnosisprep.TargetLogin) bool {
	if !diagnosisprep.ValidTargetLogin(l) {
		return false
	}
	v, err := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: rec.Target.Origin + l.Path, ProfileID: "target"})
	if err != nil || v.TargetURL != rec.Target.Origin+l.Path {
		return false
	}
	for _, prefix := range rec.Target.PathPrefixes {
		if prefix == "/" || l.Path == prefix || strings.HasPrefix(l.Path, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}

func (r *productTargetRegistry) credentialPath(targetID, label string) string {
	return filepath.Join(r.accountDir, "credentials", targetID+"."+label+".json")
}

// credentialConfigured는 보호 파일이 저널의 마지막 해시와 같은지 본다. 읽지 못하거나 다르면 자격이 없다고 본다.
func (r *productTargetRegistry) credentialConfigured(targetID string, st *productAccountState) bool {
	if st.credentialSHA256 == "" {
		return false
	}
	raw, err := readPrivateFile(r.credentialPath(targetID, st.label), maxCredentialFileBytes)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(raw)
	clear(raw)
	return hex.EncodeToString(sum[:]) == st.credentialSHA256
}

// maxCredentialFileBytes는 자격 보호 파일 상한이다. 허용 길이(사용자명 256·비밀번호 512바이트)의 모든 바이트가 JSON에서
// 6바이트(\u00XX)로 늘어나도(768×6) 난수·칸 이름을 더해 넘지 않는다. 쓰기 전에 같은 상한으로 검사한다.
const maxCredentialFileBytes = 8 << 10

// credentialFile은 보호 파일 내용이다. Nonce는 쓸 때마다 새로 뽑아, 저널의 파일 해시로 흔한 사용자명·비밀번호를
// 대조하지 못하게 한다(파일 = 저널 해시 무결성 검사는 그대로).
type credentialFile struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nonce    string `json:"nonce"`
}

func encodeCredentialFile(c diagnosisprep.AccountCredential) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(credentialFile{Username: c.Username, Password: c.Password, Nonce: productRandom(32)}); err != nil {
		return nil, err
	}
	if b.Len() > maxCredentialFileBytes {
		clear(b.Bytes())
		return nil, errors.New("invalid_account_credential")
	}
	return b.Bytes(), nil
}

// removeCredential은 계정의 자격 보호 파일을 지운다. 없으면 이미 지워진 것이다.
func (r *productTargetRegistry) removeCredential(targetID, label string) error {
	if err := os.Remove(r.credentialPath(targetID, label)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// targetLogin은 대상의 로그인 방식이다. 설정하지 않았으면 nil이다.
func (r *productTargetRegistry) targetLogin(principal, id string) (*diagnosisprep.TargetLogin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, _, err := r.accountTarget(principal, id); err != nil {
		return nil, err
	}
	login, ok := r.logins[id]
	if !ok {
		return nil, nil
	}
	login.Cookies = append([]string(nil), login.Cookies...)
	return &login, nil
}

// setTargetLogin은 대상의 로그인 방식을 정한다(대상 승인자, 철회 안 된 대상). 자격 값은 없다.
func (r *productTargetRegistry) setTargetLogin(principal, id string, login diagnosisprep.TargetLogin) (*diagnosisprep.TargetLogin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, _, err := r.accountTarget(principal, id)
	if err != nil {
		return nil, err
	}
	if rec.Target.State == "revoked" {
		return nil, errors.New("target_not_approved")
	}
	if !validTargetLoginFor(rec, login) {
		return nil, errors.New("invalid_target_login")
	}
	login.Cookies = append([]string(nil), login.Cookies...)
	if err := r.appendAccount(productAccountEvent{TargetID: id, Action: accountLoginConfig, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision, Login: &login}); err != nil {
		return nil, errPreparation
	}
	out := r.logins[id]
	out.Cookies = append([]string(nil), out.Cookies...)
	return &out, nil
}

// setAccountCredential은 계정 자격을 보호 파일(0600)에 원자적으로 쓰고 저널에 해시만 남긴다. 값은 돌려주지 않는다.
func (r *productTargetRegistry) setAccountCredential(principal, id, label string, cred diagnosisprep.AccountCredential) (diagnosisprep.ProductAccount, error) {
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
	case rec.Target.State == "revoked":
		return diagnosisprep.ProductAccount{}, errors.New("target_not_approved")
	case !diagnosisprep.ValidAccountCredential(cred):
		return diagnosisprep.ProductAccount{}, errors.New("invalid_account_credential")
	}
	raw, err := encodeCredentialFile(cred)
	if err != nil {
		return diagnosisprep.ProductAccount{}, errors.New("invalid_account_credential")
	}
	defer clear(raw)
	sum := sha256.Sum256(raw)
	if err := writePrivateFileAtomic(r.credentialPath(id, label), raw); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	if err := r.appendAccount(productAccountEvent{TargetID: id, Label: label, Action: accountCredential, Actor: principal, At: r.now(), TargetRevision: rec.Target.Revision,
		CredentialSHA256: hex.EncodeToString(sum[:])}); err != nil {
		return diagnosisprep.ProductAccount{}, errPreparation
	}
	view := accountView(rec, st, r.now())
	view.CredentialConfigured = r.credentialConfigured(id, st)
	return view, nil
}

// writePrivateFileAtomic은 같은 디렉터리의 새 임시 파일(0600)에 쓰고 fsync한 뒤 이름을 바꾸고 디렉터리를 fsync한다.
func writePrivateFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	// CreateTemp는 0600으로 만든다.
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

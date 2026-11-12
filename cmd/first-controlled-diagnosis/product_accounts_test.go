package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// accountTestTarget은 DNS 증명·TLS 확인을 가짜로 둔 레지스트리에 대상 하나를 등록한다(approve면 승인까지).
func accountTestTarget(t *testing.T, approve bool) (*productTargetRegistry, string, diagnosisprep.ProductTarget) {
	t.Helper()
	r, root := targetTestRegistry(t)
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(24 * time.Hour), nil
	}
	target, err := r.register("ARTEX", targetTestSpec("a.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if approve {
		txt[target.ChallengeName] = []string{target.ChallengeValue}
		if target, err = r.approve(t.Context(), "ARTEX", target.ID); err != nil {
			t.Fatal(err)
		}
	}
	return r, root, target
}

func accountByLabel(t *testing.T, r *productTargetRegistry, id, label string) diagnosisprep.ProductAccount {
	t.Helper()
	list, err := r.listAccounts("ARTEX", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Label == label {
			return a
		}
	}
	t.Fatalf("account %s missing", label)
	return diagnosisprep.ProductAccount{}
}

// 계정은 real로 시작하고, 승인된 대상의 승인자가 진술에 동의해야 test가 된다. 대상 판이 바뀌면(철회) real로 돌아가고
// 사유는 target_reapproved, 표시를 떼면 manual_unmark다. 다시 열어도(저널 재생) 같은 상태다.
func TestProductAccountClassificationLifecycle(t *testing.T) {
	r, root, pending := accountTestTarget(t, false)
	if _, err := r.registerAccount("ARTEX", pending.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if a := accountByLabel(t, r, pending.ID, "tester-a"); a.Classification != "real" || a.Reason != "" || a.RegisteredBy != "ARTEX" {
		t.Fatalf("new account %+v", a)
	}
	if _, err := r.markTestAccount("ARTEX", pending.ID, "tester-a", true); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("pending target allowed a test mark: %v", err)
	}
	txt := map[string][]string{pending.ChallengeName: {pending.ChallengeValue}}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	approved, err := r.approve(t.Context(), "ARTEX", pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", false); err == nil || err.Error() != "statement_required" {
		t.Fatalf("mark without the statement: %v", err)
	}
	if _, err := r.markTestAccount("other", approved.ID, "tester-a", true); err == nil || err.Error() != "target_not_found" {
		t.Fatalf("another principal marked: %v", err)
	}
	if _, err := r.markTestAccount("ARTEX", approved.ID, "missing", true); err == nil || err.Error() != "account_not_found" {
		t.Fatalf("missing account marked: %v", err)
	}
	marked, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", true)
	if err != nil || marked.Classification != "test" || marked.MarkedRevision != approved.Revision || marked.ClassifiedBy != "ARTEX" || marked.ClassifiedAt.IsZero() {
		t.Fatalf("mark: %+v %v", marked, err)
	}
	// 같은 판에 다시 표시해도 새 기록을 만들지 않는다.
	before := accountJournalLines(t, root)
	if again, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", true); err != nil || again.Classification != "test" || accountJournalLines(t, root) != before {
		t.Fatalf("repeated mark: %+v %v", again, err)
	}
	if _, err := r.revoke("ARTEX", approved.ID); err != nil {
		t.Fatal(err)
	}
	if a := accountByLabel(t, r, approved.ID, "tester-a"); a.Classification != "real" || a.Reason != "target_reapproved" || a.MarkedRevision != approved.Revision {
		t.Fatalf("revoked target kept the test mark: %+v", a)
	}
	if _, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", true); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("revoked target allowed a new mark: %v", err)
	}
	if _, err := r.registerAccount("ARTEX", approved.ID, "tester-b"); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("revoked target accepted a new account: %v", err)
	}
	unmarked, err := r.unmarkAccount("ARTEX", approved.ID, "tester-a")
	if err != nil || unmarked.Classification != "real" || unmarked.Reason != "manual_unmark" {
		t.Fatalf("unmark: %+v %v", unmarked, err)
	}
	// 다시 열면 저널 재생으로 같은 상태다.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if a := accountByLabel(t, reopened, approved.ID, "tester-a"); a.Classification != "real" || a.Reason != "manual_unmark" {
		t.Fatalf("replayed account %+v", a)
	}
}

// 표시를 뗀 계정은 real(manual_unmark)이고, 같은 승인 판에서 다시 표시할 수 있다.
func TestProductAccountUnmarkThenRemark(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if a, err := r.unmarkAccount("ARTEX", target.ID, "tester-a"); err != nil || a.Classification != "real" || a.Reason != "" {
		t.Fatalf("unmark of an unmarked account: %+v %v", a, err)
	}
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
		t.Fatal(err)
	}
	if a, err := r.unmarkAccount("ARTEX", target.ID, "tester-a"); err != nil || a.Classification != "real" || a.Reason != "manual_unmark" {
		t.Fatalf("unmark: %+v %v", a, err)
	}
	if a, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil || a.Classification != "test" {
		t.Fatalf("remark: %+v %v", a, err)
	}
}

func TestProductAccountRegistrationLimits(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	for _, label := range []string{"", "Upper", "user@example.test", strings.Repeat("a", 33), "-lead"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err == nil || err.Error() != "invalid_account_label" {
			t.Errorf("label %q accepted: %v", label, err)
		}
	}
	if _, err := r.registerAccount("ARTEX", "target-missing", "tester-a"); err == nil || err.Error() != "target_not_found" {
		t.Fatalf("missing target: %v", err)
	}
	for i := 0; i < diagnosisprep.MaxTargetAccounts; i++ {
		if _, err := r.registerAccount("ARTEX", target.ID, "tester-"+string(rune('a'+i))); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err == nil || err.Error() != "account_exists" {
		t.Fatalf("duplicate label: %v", err)
	}
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-z"); err == nil || err.Error() != "account_capacity" {
		t.Fatalf("over capacity: %v", err)
	}
	if list, err := r.listAccounts("ARTEX", target.ID); err != nil || len(list) != diagnosisprep.MaxTargetAccounts || list[0].Label != "tester-a" {
		t.Fatalf("list: %d %v", len(list), err)
	}
}

// 저널 재생은 진술 해시가 다르거나 없는 대상·판을 가리키는 줄을 받지 않는다(설치 정지).
func TestProductAccountJournalReplayRejectsForgedEvents(t *testing.T) {
	for name, forge := range map[string]func(productAccountEvent) productAccountEvent{
		"statement": func(e productAccountEvent) productAccountEvent { e.StatementSHA256 = strings.Repeat("0", 64); return e },
		"revision":  func(e productAccountEvent) productAccountEvent { e.TargetRevision = 99; return e },
		"actor":     func(e productAccountEvent) productAccountEvent { e.Actor = "other"; return e },
		"target":    func(e productAccountEvent) productAccountEvent { e.TargetID = "target-forged"; return e },
	} {
		t.Run(name, func(t *testing.T) {
			r, root, target := accountTestTarget(t, true)
			if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
				t.Fatal(err)
			}
			e := forge(productAccountEvent{TargetID: target.ID, Label: "tester-a", Action: accountMarkTest, Actor: "ARTEX", At: time.Now().UTC(), TargetRevision: target.Revision, StatementSHA256: productAccountStatementSHA256()})
			raw, _ := json.Marshal(e)
			if err := r.accountJournal.Append(raw); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil }); err == nil {
				reopened.Close()
				t.Fatal("forged account event replayed")
			}
		})
	}
}

func accountJournalLines(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	size := int64(0)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	return int(size)
}

// 표시는 그 표시를 한 대상 판에만 묶인다. 다른(이전) 판에 남은 표시는 지금 승인 판에서 test로 읽지 않는다.
func TestProductAccountMarkFromAnotherRevisionReadsReal(t *testing.T) {
	r, root, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	e := productAccountEvent{TargetID: target.ID, Label: "tester-a", Action: accountMarkTest, Actor: "ARTEX", At: time.Now().UTC(), TargetRevision: target.Revision - 1, StatementSHA256: productAccountStatementSHA256()}
	raw, _ := json.Marshal(e)
	if err := r.accountJournal.Append(raw); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if a := accountByLabel(t, reopened, target.ID, "tester-a"); a.Classification != "real" || a.Reason != "target_reapproved" {
		t.Fatalf("mark from revision %d read as %+v on revision %d", e.TargetRevision, a, target.Revision)
	}
}

func cookieLogin() diagnosisprep.TargetLogin {
	return diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "json", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
}

// 로그인 방식은 승인 경로 접두 안의 정규 경로·POST·형식에 맞는 필드·세션 위치 하나만 받고, 다시 열어도 같다.
func TestProductTargetLoginConfig(t *testing.T) {
	r, root, target := accountTestTarget(t, true)
	if got, err := r.targetLogin("ARTEX", target.ID); err != nil || got != nil {
		t.Fatalf("unset login: %+v %v", got, err)
	}
	tokenLogin := diagnosisprep.TargetLogin{Method: "POST", Path: "/read/api/login", Format: "form", UsernameField: "user", PasswordField: "pass", JSONField: "data.token", Header: "Authorization", Prefix: "Bearer "}
	rawToken := diagnosisprep.TargetLogin{Method: "POST", Path: "/read/api/login", Format: "json", UsernameField: "user", PasswordField: "pass", JSONField: "token", Header: "X-Auth-Token"}
	// 마지막에 쓴 방식이 남는다(순서를 고정한다).
	for _, l := range []diagnosisprep.TargetLogin{cookieLogin(), rawToken, tokenLogin} {
		if got, err := r.setTargetLogin("ARTEX", target.ID, l); err != nil || got.Path != l.Path || got.Header != l.Header {
			t.Fatalf("%+v login rejected: %v", l, err)
		}
	}
	bad := map[string]func(*diagnosisprep.TargetLogin){
		"outside prefix": func(l *diagnosisprep.TargetLogin) { l.Path = "/admin/login" },
		"prefix sibling": func(l *diagnosisprep.TargetLogin) { l.Path = "/reader/login" },
		"get method":     func(l *diagnosisprep.TargetLogin) { l.Method = "GET" },
		"query":          func(l *diagnosisprep.TargetLogin) { l.Path = "/read/login?next=/" },
		"semicolon":      func(l *diagnosisprep.TargetLogin) { l.Path = "/read/login;jsessionid=x" },
		"dot segment":    func(l *diagnosisprep.TargetLogin) { l.Path = "/read/../admin/login" },
		"both modes":     func(l *diagnosisprep.TargetLogin) { l.JSONField, l.Header = "token", "Authorization" },
		"no mode":        func(l *diagnosisprep.TargetLogin) { l.Cookies = nil },
		"same fields":    func(l *diagnosisprep.TargetLogin) { l.PasswordField = l.UsernameField },
		"format":         func(l *diagnosisprep.TargetLogin) { l.Format = "xml" },
		"cookie header": func(l *diagnosisprep.TargetLogin) {
			l.Cookies, l.JSONField, l.Header = nil, "token", "Cookie"
		},
		"prefix newline": func(l *diagnosisprep.TargetLogin) {
			l.Cookies, l.JSONField, l.Header, l.Prefix = nil, "token", "Authorization", "Bearer\n"
		},
	}
	for name, change := range bad {
		l := cookieLogin()
		change(&l)
		if _, err := r.setTargetLogin("ARTEX", target.ID, l); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.targetLogin("ARTEX", target.ID); err != nil || got == nil || got.JSONField != "data.token" || got.Header != "Authorization" {
		t.Fatalf("replayed login: %+v %v", got, err)
	}
}

// 자격은 보호 파일(0600)에만 있고 저널·목록·응답에는 해시 여부만 남는다. 파일이 바뀌면 자격이 없다고 본다.
func TestProductAccountCredentialStorage(t *testing.T) {
	r, root, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	const user, pass = "seeded-user-77aa", "seeded-pass-77aa"
	if _, err := r.setAccountCredential("ARTEX", target.ID, "missing", diagnosisprep.AccountCredential{Username: user, Password: pass}); err == nil || err.Error() != "account_not_found" {
		t.Fatalf("missing account: %v", err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: user, Password: "line\nbreak"}); err == nil {
		t.Fatal("control character accepted")
	}
	a, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: user, Password: pass})
	if err != nil || !a.CredentialConfigured {
		t.Fatalf("credential: %+v %v", a, err)
	}
	file := r.credentialPath(target.ID, "tester-a")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode: %v %v", info, err)
	}
	// 보호 파일 밖(저널·연락 기록 등 대상 디렉터리의 다른 파일)에는 자격 값이 없다.
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || path == file {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && (strings.Contains(string(raw), pass) || strings.Contains(string(raw), user)) {
			t.Errorf("credential value leaked into %s", filepath.Base(path))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := r.listAccounts("ARTEX", target.ID)
	encoded, _ := json.Marshal(list)
	if err != nil || len(list) != 1 || !list[0].CredentialConfigured || strings.Contains(string(encoded), pass) || strings.Contains(string(encoded), user) {
		t.Fatalf("list: %s %v", encoded, err)
	}
	// 파일을 바꾸면(해시 불일치) 자격이 없다고 본다.
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: user, Password: ""}); err == nil || err.Error() != "invalid_account_credential" {
		t.Fatalf("empty password: %v", err)
	}
	if err := os.WriteFile(file, []byte(`{"username":"other","password":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := accountByLabel(t, r, target.ID, "tester-a"); a.CredentialConfigured {
		t.Fatal("tampered credential file still configured")
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: user, Password: pass}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if a := accountByLabel(t, reopened, target.ID, "tester-a"); !a.CredentialConfigured {
		t.Fatal("replayed credential not configured")
	}
}

// 자격 줄은 해시 형식이 맞고 있는 계정을 가리켜야 재생된다. 로그인 줄은 승인 경로 밖이면 재생되지 않는다.
func TestProductAccountJournalReplayRejectsForgedCredentialAndLogin(t *testing.T) {
	outside := cookieLogin()
	outside.Path = "/admin/login"
	for name, e := range map[string]productAccountEvent{
		"credential hash":    {Label: "tester-a", Action: accountCredential, CredentialSHA256: "not-a-digest"},
		"credential account": {Label: "tester-z", Action: accountCredential, CredentialSHA256: strings.Repeat("a", 64)},
		"login outside":      {Action: accountLoginConfig, Login: &outside},
		"login with label":   {Label: "tester-a", Action: accountLoginConfig, Login: func() *diagnosisprep.TargetLogin { l := cookieLogin(); return &l }()},
	} {
		t.Run(name, func(t *testing.T) {
			r, root, target := accountTestTarget(t, true)
			if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
				t.Fatal(err)
			}
			e.TargetID, e.Actor, e.At, e.TargetRevision = target.ID, "ARTEX", time.Now().UTC(), target.Revision
			raw, _ := json.Marshal(e)
			if err := r.accountJournal.Append(raw); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil }); err == nil {
				reopened.Close()
				t.Fatal("forged event replayed")
			}
		})
	}
}

// 철회된 대상에는 자격·로그인 방식을 새로 쓰지 않는다.
func TestProductAccountCredentialRejectedOnRevokedTarget(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "p"}); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("credential on revoked target: %v", err)
	}
	if _, err := os.Stat(r.credentialPath(target.ID, "tester-a")); !os.IsNotExist(err) {
		t.Fatal("credential file written for a revoked target")
	}
	if _, err := r.setTargetLogin("ARTEX", target.ID, cookieLogin()); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("login on revoked target: %v", err)
	}
}

// 승인이 만료되면 표시한 계정도 real(target_expired)로 읽고 새 표시를 받지 않는다(판단할 수 없으면 실계정).
func TestProductAccountExpiredApprovalReadsReal(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if a, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil || a.Classification != "test" {
		t.Fatalf("mark: %+v %v", a, err)
	}
	expired := target.ExpiresAt.Add(time.Second)
	r.now = func() time.Time { return expired }
	if a := accountByLabel(t, r, target.ID, "tester-a"); a.Classification != "real" || a.Reason != "target_expired" {
		t.Fatalf("expired approval kept the test mark: %+v", a)
	}
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("expired target accepted a mark: %v", err)
	}
}

// 제품 제어 경로(준비 프로세스 쪽)가 계정·로그인·자격 조작을 레지스트리로 잇고, 응답은 계약 검사를 통과하며, 목록에는 진술
// 문장이 실리고, 자격 값은 응답에 없고 요청 구조체의 값 참조는 쓴 뒤 비운다.
func TestProductControlAccountDispatch(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	endpoint := productPreparationEndpoint{product: &productRuntime{targets: r}}
	call := func(p *diagnosisprep.ProductRequest) diagnosisprep.ProductResponse {
		t.Helper()
		req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl, Product: p}
		resp, err := endpoint.Call(t.Context(), req)
		// 전송 계층(UnixServer)은 같은 요청 구조체로 응답을 다시 검증한다. 준비 쪽이 요청을 훼손하면 여기서 걸린다.
		if err != nil || resp.Error != "" || diagnosisprep.ValidateResponse(req, resp) != nil || resp.Product == nil || resp.Product.Reason != "" {
			t.Fatalf("%s: %v %+v", p.Operation, err, resp)
		}
		return *resp.Product
	}
	if got := call(&diagnosisprep.ProductRequest{Operation: "account_register", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"}}); got.Account == nil || got.Account.Label != "tester-a" {
		t.Fatalf("register: %+v", got)
	}
	list := call(&diagnosisprep.ProductRequest{Operation: "accounts", TargetID: target.ID})
	if len(list.Accounts) != 1 || list.AccountStatement != productAccountStatement {
		t.Fatalf("accounts: %+v", list)
	}
	login := cookieLogin()
	if got := call(&diagnosisprep.ProductRequest{Operation: "target_login_set", TargetID: target.ID, Login: &login}); got.Login == nil || got.Login.Path != login.Path {
		t.Fatalf("login: %+v", got)
	}
	cred := &diagnosisprep.AccountCredential{Username: "seeded-user-91bb", Password: "seeded-pass-91bb"}
	got := call(&diagnosisprep.ProductRequest{Operation: "account_credential_set", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"}, Credential: cred})
	encoded, _ := json.Marshal(got)
	if got.Account == nil || !got.Account.CredentialConfigured || strings.Contains(string(encoded), "seeded-") {
		t.Fatalf("credential response: %s", encoded)
	}
	if got := call(&diagnosisprep.ProductRequest{Operation: "account_mark_test", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a", StatementConfirmed: true}}); got.Account == nil || got.Account.Classification != "test" {
		t.Fatalf("mark: %+v", got)
	}
}

// 허용 길이의 자격이 JSON에서 가장 크게 늘어나도(모든 바이트가 \u00XX) 저장한 즉시 읽혀 설정됨으로 보인다.
func TestProductAccountCredentialWorstCaseEncoding(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	for name, cred := range map[string]diagnosisprep.AccountCredential{
		"control bytes": {Username: strings.Repeat("\x01", 256), Password: strings.Repeat("\x1f", 512)},
		"html bytes":    {Username: strings.Repeat("<", 256), Password: strings.Repeat("&", 512)},
		"quotes":        {Username: strings.Repeat(`"`, 256), Password: strings.Repeat(`\`, 512)},
	} {
		a, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", cred)
		if err != nil || !a.CredentialConfigured {
			t.Errorf("%s: %+v %v", name, a, err)
		}
	}
}

// 같은 자격을 두 번 넣어도 저널의 파일 해시가 다르다(파일마다 새 난수). 사용자명·비밀번호 사전으로 해시를 대조하지 못한다.
func TestProductAccountCredentialHashIsSalted(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	cred := diagnosisprep.AccountCredential{Username: "admin", Password: "admin"}
	hashes := map[string]bool{}
	for i := 0; i < 2; i++ {
		if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", cred); err != nil {
			t.Fatal(err)
		}
		hashes[r.accounts[target.ID]["tester-a"].credentialSHA256] = true
	}
	plain, _ := json.Marshal(cred)
	sum := sha256.Sum256(plain)
	if len(hashes) != 2 || hashes[hex.EncodeToString(sum[:])] {
		t.Fatalf("credential hash not salted: %v", hashes)
	}
}

// 시험 계정 표시를 떼면 자격 파일도 지우고(재생 뒤에도 미설정), 대상을 철회하면 그 대상 계정의 자격 파일을 지운다.
func TestProductAccountCredentialRemovedOnUnmarkAndRevoke(t *testing.T) {
	r, root, target := accountTestTarget(t, true)
	for _, label := range []string{"tester-a", "tester-b"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err != nil {
			t.Fatal(err)
		}
		if _, err := r.setAccountCredential("ARTEX", target.ID, label, diagnosisprep.AccountCredential{Username: "u-" + label, Password: "p-" + label}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
		t.Fatal(err)
	}
	if a, err := r.unmarkAccount("ARTEX", target.ID, "tester-a"); err != nil || a.CredentialConfigured {
		t.Fatalf("unmark kept the credential: %+v %v", a, err)
	}
	if _, err := os.Stat(r.credentialPath(target.ID, "tester-a")); !os.IsNotExist(err) {
		t.Fatal("credential file left after unmark")
	}
	if !accountByLabel(t, r, target.ID, "tester-b").CredentialConfigured {
		t.Fatal("unmarking one account removed another account's credential")
	}
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.credentialPath(target.ID, "tester-b")); !os.IsNotExist(err) {
		t.Fatal("credential file left after target revoke")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, label := range []string{"tester-a", "tester-b"} {
		if accountByLabel(t, reopened, target.ID, label).CredentialConfigured {
			t.Fatalf("%s configured after replay", label)
		}
	}
}

// 해제 때 자격 파일을 못 지워도(디렉터리 쓰기 불가) 해제는 기록되고, 저널에서 자격 해시가 빠져 남은 파일은 쓰이지 않는다.
func TestProductAccountUnmarkDropsCredentialEvenIfFileStays(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(r.credentialPath(target.ID, "tester-a"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	a, err := r.unmarkAccount("ARTEX", target.ID, "tester-a")
	if err != nil || a.Reason != "manual_unmark" || a.CredentialConfigured {
		t.Fatalf("unmark with an undeletable file: %+v %v", a, err)
	}
	// 디렉터리를 되돌려 파일을 다시 읽을 수 있어도 자격 해시가 빠졌으니 미설정이다.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.credentialPath(target.ID, "tester-a")); err != nil {
		t.Fatal("fixture expected the file to stay")
	}
	if accountByLabel(t, r, target.ID, "tester-a").CredentialConfigured {
		t.Fatal("unmarked account still uses the leftover credential file")
	}
}

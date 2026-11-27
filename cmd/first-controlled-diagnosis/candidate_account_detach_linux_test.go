//go:build linux

package main

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

// 계정을 결속한 채로 읽힌 익명 후속 검증 실행이다(9칸-1: 자격만 있어도 결속). 자격 사본 · 인증 칸 · 실행 계정 · 외부 계정
// 동의 · 시험 계정 헤더 모드가 다 켜져 있다.
func detachTestProfile(t *testing.T) (*urlPreparationRuntime, *urlLoadedProfile, string, []byte) {
	t.Helper()
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	l := connectionTestProfile(r, rec)
	l.policy.Rules[0].Ref = "candidate-exact"
	l.policy.Rules[0].PathMatch = "exact"
	l.policy.Rules[0].PathScopeRef = "candidate-exact"
	l.policy.Rules[0].ApprovalDisplay = server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}
	l.policy.MaxRequests = 1
	l.profile = urlPreparationProfile{ValidFrom: time.Now().Add(-time.Minute), ValidUntil: time.Now().Add(time.Hour), Budget: diagnosisprep.Budget{DurationSeconds: 30}}
	l.launch.ShutdownTimeoutSeconds = 1
	l.launch.E2HostSocket = "/run/e2-host/audit.sock"
	sha := strings.Repeat("c", 64)
	name := bindingCredentialName(target.ID, "tester-a", sha)
	secret := []byte(`{"username":"detach-user-7f3a","password":"detach-pass-91be"}`)
	l.materials = map[string][]byte{name: append([]byte(nil), secret...)}
	l.policy.Auth = &server.E2HTTPFileAuth{ModelLogin: true, Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-a", Credential: server.E2HTTPFilePin{File: name, SHA256: sha}}}}
	l.policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	l.productAccounts = []productRunAccount{{Label: "tester-a", CredentialSHA256: sha}}
	l.externalTestAccounts = true
	job := "candidate-detached"
	if err := l.prepareTargetConnection(t.Context(), nil, job); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	if err := os.Chmod(runs, 0700); err != nil {
		t.Fatal(err)
	}
	return &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runs}}, l, job, secret
}

// 계정 떼기: 자격 사본을 0으로 지우고 발급 목록에서 빼며, 인증 칸 · 실행 계정 · 외부 계정 동의를 끄고 헤더 모드를 푼다.
// 떼고도 그 대상의 자격 사본이 발급 목록에 남으면 오류다(닫힌 쪽).
func TestDetachAccountsClearsCredentialCopies(t *testing.T) {
	_, l, _, _ := detachTestProfile(t)
	name := l.policy.Auth.Accounts[0].Credential.File
	held := l.materials[name]
	detached, err := l.detachAccounts()
	if err != nil || len(detached) != 1 || detached[0] != name {
		t.Fatalf("detach: %v %v", detached, err)
	}
	if l.policy.Auth != nil || len(l.productAccounts) != 0 || l.externalTestAccounts || l.policy.HeaderMode != e2proxy.HeaderPreserve {
		t.Fatalf("account state left: auth=%v accounts=%d external=%v header=%s", l.policy.Auth, len(l.productAccounts), l.externalTestAccounts, l.policy.HeaderMode)
	}
	if _, ok := l.materials[name]; ok {
		t.Fatal("credential copy still issued")
	}
	for _, b := range held {
		if b != 0 {
			t.Fatal("credential copy not cleared in memory")
		}
	}
	// 인증 칸에 없는 그 대상의 자격 사본이 남았으면 뗀 것이 아니다.
	_, left, _, _ := detachTestProfile(t)
	left.materials[bindingCredentialName(left.productTarget.Target.ID, "tester-b", strings.Repeat("d", 64))] = []byte("x")
	if _, err := left.detachAccounts(); err == nil {
		t.Fatal("detach passed with a credential copy left")
	}
}

// 계정이 있는 대상의 저장 후보 검증(익명 GET 하나): 떼고 발급하면 정책에 인증 칸이 없고 헤더 모드는 preserve(계정 헤더를 싣지
// 않음), 외부 계정 동의는 꺼지고, 권한 디렉터리에 자격 파일이 없으며 발급한 파일 어디에도 자격 값이 없다. 사본이 보이거나 외부
// 계정 동의가 켜진 발급은 받지 않는다.
func TestAnonymousCandidateIssueCarriesNoAccount(t *testing.T) {
	b, l, job, secret := detachTestProfile(t)
	detached, err := l.detachAccounts()
	if err != nil {
		t.Fatal(err)
	}
	launch, err := b.issue(l, job, server.FirstDiagnosisPreparedTask{TaskID: 91, ExplorationID: 92})
	if err != nil {
		t.Fatal(err)
	}
	if !anonymousIssueClean(launch, detached) {
		t.Fatal("clean anonymous issue rejected")
	}
	raw, err := os.ReadFile(launch.HTTP.PolicyFile)
	if err != nil {
		t.Fatal(err)
	}
	var issued server.E2HTTPFilePolicy
	if e2config.StrictJSON(raw, &issued) != nil || issued.Auth != nil || issued.HeaderMode != e2proxy.HeaderPreserve || launch.HTTP.ExternalTestAccounts {
		t.Fatalf("issued policy carries account state: auth=%v header=%s external=%v", issued.Auth, issued.HeaderMode, launch.HTTP.ExternalTestAccounts)
	}
	root := launch.HTTP.AuthorityRoot
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(d.Name(), bindingCredentialTargetPrefix(l.productTarget.Target.ID)) {
			t.Errorf("credential file issued: %s", d.Name())
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range []string{"detach-user-7f3a", "detach-pass-91be", string(secret)} {
			if strings.Contains(string(body), value) {
				t.Errorf("%s carries a credential value", d.Name())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, detached[0]), []byte("leak"), 0600); err != nil {
		t.Fatal(err)
	}
	if anonymousIssueClean(launch, detached) {
		t.Fatal("issue with a visible credential copy accepted")
	}
	if err := os.Remove(filepath.Join(root, detached[0])); err != nil {
		t.Fatal(err)
	}
	launch.HTTP.ExternalTestAccounts = true
	if anonymousIssueClean(launch, detached) {
		t.Fatal("issue with external account consent accepted")
	}
}

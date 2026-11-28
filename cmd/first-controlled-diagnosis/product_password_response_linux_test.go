//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

func TestPasswordResponseAccountBindingKeepsLegacyDigest(t *testing.T) {
	login := cookieLogin()
	legacy := productRunAccounts{Login: &login, Accounts: []productRunAccount{{Label: "tester-a", CredentialSHA256: strings.Repeat("a", 64)}}}
	oldRaw, _ := json.Marshal(struct {
		Login    diagnosisprep.TargetLogin `json:"login"`
		Accounts []productRunAccount       `json:"accounts"`
	}{login, legacy.Accounts})
	oldDigest, err := runAccountsDigest(legacy)
	if err != nil || oldDigest != urlSHA(oldRaw) {
		t.Fatal("구형 계정 결속 지문 변경")
	}
	modern := legacy
	modern.PasswordResponse = e2proxy.PasswordResponseMaskSpans
	newDigest, err := runAccountsDigest(modern)
	if err != nil || newDigest == oldDigest {
		t.Fatal("새 정책이 구형 결속과 충돌")
	}
	// 11/23 model_visible_v1 결속도 다른 지문이고, 앞의 두 결속과 같이 복원된다.
	visible := legacy
	visible.PasswordResponse = e2proxy.PasswordResponseModelVisible
	visibleDigest, err := runAccountsDigest(visible)
	if err != nil || visibleDigest == oldDigest || visibleDigest == newDigest {
		t.Fatal("model_visible_v1 결속 지문이 다른 결속과 충돌")
	}
	for _, accounts := range []productRunAccounts{legacy, modern, visible} {
		digest, _ := runAccountsDigest(accounts)
		dir := t.TempDir()
		os.Chmod(dir, 0700)
		policy := server.E2HTTPFilePolicy{Auth: &server.E2HTTPFileAuth{Login: &login, PasswordResponse: accounts.PasswordResponse, Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-a", Credential: server.E2HTTPFilePin{File: "account.json", SHA256: strings.Repeat("a", 64)}}}}}
		raw, _ := json.Marshal(policy)
		if os.WriteFile(filepath.Join(dir, "policy.json"), raw, 0600) != nil {
			t.Fatal("합성 결속 쓰기 실패")
		}
		restored, got, err := accountsFromBinding(dir, digest[:16])
		if err != nil || got != digest || restored.PasswordResponse != accounts.PasswordResponse {
			t.Fatal("과거·신규 결속 복원 실패", err)
		}
		if _, _, err := accountsFromBinding(dir, strings.Repeat("b", 16)); err == nil {
			t.Fatal("다른 결속 지문 수용")
		}
	}
}

package main

import (
	"bytes"
	"testing"

	"github.com/Autumn-27/artex/server"
)

func passwordProfile(materials map[string][]byte, files ...string) *urlLoadedProfile {
	auth := &server.E2HTTPFileAuth{}
	for i, file := range files {
		auth.Accounts = append(auth.Accounts, server.E2HTTPFileAuthAccount{Label: string(rune('a' + i)), Credential: server.E2HTTPFilePin{File: file}})
	}
	return &urlLoadedProfile{policy: server.E2HTTPFilePolicy{Auth: auth}, materials: materials}
}

// 탐색 지침에 넣을 자격은 이 실행의 결속 계정 자격 사본(메모리)에서 만든다. 기록에서 비밀번호를 가리지 않으므로(절대규칙 2,
// 2026-11-26 DLP 폐기) 짧은 비밀번호 계정도 빠지지 않는다(#967의 임시 제외를 풂). 회차가 끝나면(clearMaterials) 자격 사본을
// 메모리에서 지운다.
func TestExplorationCredentialsFromRunMaterials(t *testing.T) {
	materials := map[string][]byte{
		"cred-a.json": []byte(`{"username":"tester-a","password":"alpha-password"}`),
		"cred-b.json": []byte(`{"username":"tester-b","password":"12345"}`),
		"cred-c.json": []byte(`{"username":"tester-c","password":"x"}`),
	}
	l := passwordProfile(materials, "cred-a.json", "cred-b.json", "cred-c.json")
	creds, err := l.explorationCredentials()
	if err != nil || len(creds) != 3 {
		t.Fatalf("자격 목록: %d %v", len(creds), err)
	}
	for i, want := range []struct{ label, username, password string }{{"a", "tester-a", "alpha-password"}, {"b", "tester-b", "12345"}, {"c", "tester-c", "x"}} {
		if creds[i].Label != want.label || creds[i].Username != want.username || creds[i].Password != want.password {
			t.Fatalf("자격 %d: 라벨 %q 아이디 %q", i, creds[i].Label, creds[i].Username)
		}
	}
	l.clearMaterials()
	for name, raw := range materials {
		if !bytes.Equal(raw, make([]byte, len(raw))) {
			t.Fatalf("%s: 실행이 끝난 뒤 자격 사본이 메모리에 남음", name)
		}
	}
}

// 결속 계정의 자격 사본이 없거나 읽을 수 없으면 자격 목록을 만들지 못한 것이다(그 실행은 탐색을 열지 않는다). 계정을
// 결속하지 않은 실행은 빈 목록이다.
func TestExplorationCredentialsFailClosed(t *testing.T) {
	for name, materials := range map[string]map[string][]byte{
		"missing":        {},
		"broken":         {"cred-a.json": []byte(`{"password":`)},
		"empty username": {"cred-a.json": []byte(`{"username":"","password":"alpha-password"}`)},
		"empty password": {"cred-a.json": []byte(`{"username":"tester-a","password":""}`)},
	} {
		if creds, err := passwordProfile(materials, "cred-a.json").explorationCredentials(); err == nil || creds != nil {
			t.Fatalf("%s: 자격 목록을 만듦", name)
		}
	}
	creds, err := (&urlLoadedProfile{}).explorationCredentials()
	if err != nil || creds == nil || len(creds) != 0 {
		t.Fatal("계정 없는 실행은 빈 목록이어야 함")
	}
}

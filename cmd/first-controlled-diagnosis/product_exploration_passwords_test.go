package main

import (
	"bytes"
	"fmt"
	"strings"
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

// 탐색 기록 가림 목록은 이 실행의 결속 계정 자격 사본(메모리)에서 만들고, E②가 받지 않는 짧은 값은 넣지 않으며, 회차가
// 끝나면(clearMaterials) 지운다. 지운 뒤 들어온 기록은 통째로 가린다(#875 발견 3, 2026-11-24 사용자 결정).
func TestExplorationPasswordsFromRunMaterials(t *testing.T) {
	materials := map[string][]byte{
		"cred-a.json": []byte(`{"username":"tester-a","password":"alpha-password"}`),
		"cred-b.json": []byte(`{"username":"tester-b","password":"bravo-password"}`),
		"cred-c.json": []byte(`{"username":"tester-c","password":"12345"}`),
	}
	l := passwordProfile(materials, "cred-a.json", "cred-b.json", "cred-c.json")
	p, err := l.explorationPasswords()
	if err != nil || p == nil {
		t.Fatal(err)
	}
	if again, _ := l.explorationPasswords(); again != p {
		t.Fatal("password list rebuilt within one run")
	}
	record := `{"users":[{"password":"alpha-password"},{"password":"bravo-password"},{"password":"12345"}]}`
	got := string(p.redact([]byte(record)))
	if strings.Contains(got, "alpha-password") || strings.Contains(got, "bravo-password") || !strings.Contains(got, `"12345"`) {
		t.Fatalf("redaction: %s", got)
	}
	if formatted := fmt.Sprintf("%v %+v", p, p); strings.Contains(formatted, "alpha-password") {
		t.Fatal("password list formats its values")
	}
	held := append([][]byte(nil), p.values...)
	if len(held) != 2 {
		t.Fatalf("password list size %d", len(held))
	}
	l.clearMaterials()
	// 목록을 비우는 것만이 아니라 값 바이트 자체를 0으로 지운다.
	for _, v := range held {
		if !bytes.Equal(v, make([]byte, len(v))) {
			t.Fatal("password bytes left after run end")
		}
	}
	if p.values != nil || !p.closed || !bytes.Equal(materials["cred-a.json"], make([]byte, len(materials["cred-a.json"]))) {
		t.Fatal("run end left passwords in memory")
	}
	if got := string(p.redact([]byte(record))); got != "[REDACTED]" {
		t.Fatalf("record after run end: %s", got)
	}
}

// 결속 계정의 자격 사본이 없거나 읽을 수 없으면 가림 목록을 만들지 못한 것이다(탐색을 가림 없이 열지 않는다).
// 계정을 결속하지 않은 실행은 빈 목록이다.
func TestExplorationPasswordsFailClosed(t *testing.T) {
	for name, materials := range map[string]map[string][]byte{
		"missing":        {},
		"broken":         {"cred-a.json": []byte(`{"password":`)},
		"empty password": {"cred-a.json": []byte(`{"username":"tester-a","password":""}`)},
	} {
		if p, err := passwordProfile(materials, "cred-a.json").explorationPasswords(); err == nil || p != nil {
			t.Fatalf("%s: list built", name)
		}
	}
	l := &urlLoadedProfile{}
	p, err := l.explorationPasswords()
	if err != nil || p == nil || string(p.redact([]byte("no accounts"))) != "no accounts" {
		t.Fatal("unbound run")
	}
}

// 기록에서 가릴 수 없는 짧은 비밀번호(1~5바이트)의 계정은 탐색 모델 지침에 넣지 않는다. 가림 목록(explorationPasswords)과
// 같은 기준이라, 지침에 든 비밀번호는 모두 활동 기록에서 가려진다(#967 동찬 리뷰 P1).
func TestExplorationCredentialsSkipUnmaskablePasswords(t *testing.T) {
	materials := map[string][]byte{
		"cred-a.json": []byte(`{"username":"tester-a","password":"alpha-password"}`),
		"cred-b.json": []byte(`{"username":"tester-b","password":"12345"}`),
		"cred-c.json": []byte(`{"username":"tester-c","password":"x"}`),
	}
	l := passwordProfile(materials, "cred-a.json", "cred-b.json", "cred-c.json")
	creds, err := l.explorationCredentials()
	if err != nil || len(creds) != 1 || creds[0].Username != "tester-a" {
		t.Fatalf("짧은 비밀번호 계정이 지침에 감: %d %v", len(creds), err)
	}
	p, err := l.explorationPasswords()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range creds {
		if strings.Contains(string(p.redact([]byte("pw "+c.Password))), c.Password) {
			t.Fatal("지침에 준 비밀번호가 활동 기록에서 안 가려짐")
		}
	}
}

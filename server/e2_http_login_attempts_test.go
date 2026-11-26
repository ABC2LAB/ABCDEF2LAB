package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/loginattempt"
)

// 탐색 단계 행을 회차 기록에 덧붙이면(0600) 진단 단계 읽기가 같은 행을 돌려준다. 빈 목록은 파일을 만들지 않는다.
func TestAppendLoginAttemptRowsFeedsDiagnosisSeed(t *testing.T) {
	dir := t.TempDir()
	if err := AppendLoginAttemptRows(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, E2HTTPLoginAttemptsFile)); !os.IsNotExist(err) {
		t.Fatal("빈 목록이 파일을 만듦")
	}
	rows := []loginattempt.Row{{Schema: loginattempt.Schema, AttemptRef: "exploration-1", Stage: loginattempt.StageExploration, Identifier: "user2", Outcome: loginattempt.OutcomeFailed},
		{Schema: loginattempt.Schema, AttemptRef: "exploration-2", Stage: loginattempt.StageExploration, Identifier: "user2", Outcome: loginattempt.OutcomeFailed}}
	if err := AppendLoginAttemptRows(dir, rows[:1]); err != nil {
		t.Fatal(err)
	}
	if err := AppendLoginAttemptRows(dir, rows[1:]); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, E2HTTPLoginAttemptsFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("기록 파일: %v %v", info, err)
	}
	got, _, err := readLoginAttemptRows(filepath.Join(dir, E2HTTPLoginAttemptsFile))
	if err != nil || len(got) != 2 || got[1].AttemptRef != "exploration-2" {
		t.Fatalf("읽은 행: %v %+v", err, got)
	}
	if AppendLoginAttemptRows("", rows) == nil {
		t.Fatal("상태 디렉터리 없이 씀")
	}
}

// 앞 단계 기록을 넘겨받지 못했으면(표지 파일·깨진 줄) 진단 판정기는 회차 공통 상한을 지킬 수 없어 모든 로그인 시도를 보내기
// 전에 막는다. 표지가 없고 기록이 온전하면 막지 않는다(#969 동찬 리뷰 P2).
func TestLoginHistoryUnavailableRefusesLogins(t *testing.T) {
	login := loginattempt.Request{Method: "POST", Path: "/login", ContentType: "application/x-www-form-urlencoded", Body: []byte("username=u1&password=p-123456")}
	open := func(t *testing.T, dir string) *loginattempt.Tracker {
		t.Helper()
		policy := E2HTTPFilePolicy{RunRef: "run-1", StateDir: dir, Rules: []E2HTTPFileRule{{Ref: "auth-login", Origin: "http://target.test"}},
			Auth: &E2HTTPFileAuth{RuleRef: "auth-login", ModelLogin: true}, LoginAttempts: &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}}
		tr, err := newE2HTTPLoginAttempts(t.Context(), policy, nil, newE2HTTPSessionStore(nil, "run-1", nil), newLoginContrastProber(nil))
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	clean := t.TempDir()
	if p := open(t, clean).Prepare(login, true); p.Refusal != "" {
		t.Fatalf("기록 없는 회차의 로그인을 막음: %+v", p)
	}
	marked := t.TempDir()
	if err := MarkLoginHistoryUnavailable(marked); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(marked, E2HTTPLoginHistoryUnavailableFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("표지 파일: %v %v", info, err)
	}
	if p := open(t, marked).Prepare(login, true); p.Refusal != loginattempt.RefusalHistoryUnavailable {
		t.Fatalf("인계 실패 표지가 있는데 로그인을 보냄: %+v", p)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, E2HTTPLoginAttemptsFile), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := open(t, broken).Prepare(login, true); p.Refusal != loginattempt.RefusalHistoryUnavailable {
		t.Fatalf("깨진 기록인데 로그인을 보냄: %+v", p)
	}
}

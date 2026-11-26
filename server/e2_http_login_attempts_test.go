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
	got, err := readLoginAttemptRows(filepath.Join(dir, E2HTTPLoginAttemptsFile))
	if err != nil || len(got) != 2 || got[1].AttemptRef != "exploration-2" {
		t.Fatalf("읽은 행: %v %+v", err, got)
	}
	if AppendLoginAttemptRows("", rows) == nil {
		t.Fatal("상태 디렉터리 없이 씀")
	}
}

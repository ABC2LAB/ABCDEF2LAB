// Package ci는 scripts/ci 셸 스크립트의 회귀 시험만 둔다(시험 PostgreSQL이 있는 Go 시험 작업에서 돈다).
package ci

import (
	"bytes"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pg-run.sh는 env 파일의 DSN마다 실제 쿼리로 접속을 확인하고, 하나라도 안 되면 명령을 실행하지 않는다. DB 이름이나
// 사용자가 틀린(잘린) DSN은 서버가 살아 있어도 거부해야 한다 — pg_isready로는 통과했다(#981 리뷰). DSN 값은 내보내지 않는다.
func TestPGRunChecksEachDSNWithQuery(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		// DB 사유 SKIP이다. go-test.sh는 이를 실패로 본다(시험 PostgreSQL 없이 통과로 보이지 않게).
		t.Skip("시험 PostgreSQL DSN(ARTEX_PG_DSN) 없음")
	}
	good, err := url.Parse(dsn)
	if err != nil || good.Scheme != "postgres" || good.Query().Get("host") == "" || good.Query().Get("user") == "" {
		t.Fatal("ARTEX_PG_DSN이 pg-up.sh 모양(postgres:///DB?host=소켓&user=사용자)이 아님")
	}
	change := func(edit func(u *url.URL, q url.Values)) string {
		u := *good
		q := u.Query()
		edit(&u, q)
		u.RawQuery = q.Encode()
		return u.String()
	}
	const secret = "synthetic-missing-marker"
	for _, tc := range []struct {
		name, value, want string
		code              int
	}{
		{"valid", dsn, "", 0},
		{"wrong database", change(func(u *url.URL, _ url.Values) { u.Path = "/" + secret + "_db" }), "DSN에 접속하지 못함: ARTEX_PG_DSN", 2},
		{"wrong user", change(func(_ *url.URL, q url.Values) { q.Set("user", secret+"_role") }), "DSN에 접속하지 못함: ARTEX_PG_DSN", 2},
		{"no server at socket", change(func(_ *url.URL, q url.Values) { q.Set("host", filepath.Join(t.TempDir(), "s")) }), "DSN에 접속하지 못함: ARTEX_PG_DSN", 2},
		{"empty", "", "DSN이 비어 있음: ARTEX_PG_DSN", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lines := "ARTEX_PG_DSN=" + tc.value + "\n"
			if bin := os.Getenv("CI_PG_BIN"); bin != "" {
				lines += "CI_PG_BIN=" + bin + "\n"
			}
			envFile := filepath.Join(dir, "pg.env")
			if err := os.WriteFile(envFile, []byte(lines), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "ran")
			cmd := exec.Command("bash", "pg-run.sh", envFile, "touch", marker)
			// 바깥 환경의 DSN이 스크립트 검사를 대신 채우지 않게 지운다.
			cmd.Env = append(os.Environ(), "ARTEX_PG_DSN=")
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(marker)
			ran := statErr == nil
			if code != tc.code || ran != (tc.code == 0) || tc.want != "" && !strings.Contains(out.String(), tc.want) {
				t.Fatalf("code=%d ran=%v out=%q", code, ran, out.String())
			}
			if strings.Contains(out.String(), secret) || tc.value != "" && strings.Contains(out.String(), tc.value) {
				t.Fatal("DSN 값이 출력에 나옴")
			}
		})
	}
}

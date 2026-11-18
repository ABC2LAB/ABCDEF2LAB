package repocheck

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// targetNames는 평가 과녁 이름이다. 제품 코드는 처음 보는 대상을 설정만 바꿔 다뤄야 하므로(CLAUDE.md "최종 목표")
// 이 이름이 주석이 아닌 코드(식별자·문자열·문자 리터럴)에 들어오면 실패한다.
var targetNames = regexp.MustCompile(`(?i)vampi|juice`)

// excludedDir은 제품 코드가 아닌 평가·fixture 디렉터리다. 디렉터리 이름 조각으로만 뺀다(파일 이름으로는 빼지 않는다).
func excludedDir(name string) bool {
	switch {
	case name == "testdata" || name == "node_modules" || name == "web":
		return true
	case strings.HasPrefix(name, "."): // .git·.github·.claude(로컬 작업 트리)
		return true
	case strings.Contains(name, "fixture"):
		return true
	case name == "eval" || strings.HasPrefix(name, "eval-") || strings.Contains(name, "-eval-"):
		return true
	}
	return false
}

// targetNameHits는 Go 소스 하나에서 주석을 뺀 토큰 중 대상 이름이 든 줄을 돌려준다.
func targetNameHits(name string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file := fset.AddFile(name, fset.Base(), len(src))
	var errs scanner.ErrorList
	var s scanner.Scanner
	s.Init(file, src, func(pos token.Position, msg string) { errs.Add(pos, msg) }, 0)
	var hits []string
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		value := lit
		// 문자열·문자 리터럴은 Go가 읽는 값으로 풀어서 본다(예: "\x6auice"는 juice다). 풀지 못하면 표기 그대로 본다.
		if tok == token.STRING || tok == token.CHAR {
			if unquoted, err := strconv.Unquote(lit); err == nil {
				value = unquoted
			}
		}
		if value != "" && (targetNames.MatchString(value) || targetNames.MatchString(lit)) {
			// 리터럴 값은 출력하지 않고 위치와 토큰 종류만 남긴다.
			hits = append(hits, fmt.Sprintf("%s:%d %s", name, fset.Position(pos).Line, tok))
		}
	}
	if len(errs) > 0 {
		return nil, errs.Err()
	}
	return hits, nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod를 찾지 못함")
		}
		dir = parent
	}
}

func TestProductGoCodeHasNoTargetNames(t *testing.T) {
	root := repoRoot(t)
	var hits []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && excludedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found, err := targetNameHits(filepath.ToSlash(rel), src)
		if err != nil {
			return err
		}
		scanned++
		hits = append(hits, found...)
		return nil
	})
	if err != nil {
		t.Fatalf("레포를 읽지 못함: %v", err)
	}
	// 공허하지 않음: 제품 패키지들이 실제로 검사 범위에 들어왔는지 본다.
	if scanned < 500 {
		t.Fatalf("검사한 Go 파일이 %d개뿐 — 제외 규칙이 너무 넓다", scanned)
	}
	if len(hits) > 0 {
		t.Fatalf("제품 Go 코드에 평가 과녁 이름이 들어옴(대상 이름은 설정으로 받는다):\n%s", strings.Join(hits, "\n"))
	}
}

func TestTargetNameHitsIgnoresComments(t *testing.T) {
	cases := map[string]int{
		"package p\n// VAmPI 회차에서 확인\n/* juice */\nvar x = 1\n":       0,
		"package p\nvar origin = \"https://juice.example\"\n":         1,
		"package p\nconst target = `VAmPI`\n":                         1,
		"package p\nfunc juiceShop() {}\n":                            1,
		"package p\ntype T struct { A string `json:\"vampi_id\"` }\n": 1,
		"package p\nvar r = 'j'\n":                                    0,
		"package p\nvar e = \"\\x6auice\"\n":                          1,
		"package p\nvar u = \"\\u0076\\u0061mpi\"\n":                  1,
		"package p\nvar o = \"\\152uice\"\n":                          1,
		"package p\nvar c = '\\x6a'\n":                                0,
	}
	for src, want := range cases {
		hits, err := targetNameHits("x.go", []byte(src))
		if err != nil || len(hits) != want {
			t.Fatalf("%q: %d건(기대 %d) %v", src, len(hits), want, err)
		}
		for _, h := range hits {
			if targetNames.MatchString(h) {
				t.Fatalf("출력에 리터럴 값이 남음: %s", h)
			}
		}
	}
}

func TestExcludedDir(t *testing.T) {
	for name, want := range map[string]bool{
		"product-fixture": true, "kgfixture": true, "explorefixture": true, "eval-meter": true,
		"e2-eval-service": true, "eval": true, "testdata": true, ".claude": true, "web": true,
		"server": false, "e2proxy": false, "diagnosisflow": false, "first-controlled-diagnosis": false, "evaluation": false,
	} {
		if got := excludedDir(name); got != want {
			t.Fatalf("%s: %v", name, got)
		}
	}
}

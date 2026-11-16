package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2sandbox"
)

// 이미지에 들어가는 저장소의 api-recon 파일로 탐색 직접 Bash 작업 공간을 만든다. 목록의 파일은 모두 있고, 브라우저 런타임
// 수집 스크립트는 넣지 않는다(docs/sandbox-data-decision.md §2-2).
func TestExplorationSkillSeedFromRepositorySkill(t *testing.T) {
	dir := filepath.Join("..", "..", "skills", "api-recon")
	seed, err := explorationSkillSeed(dir)
	if err != nil {
		t.Fatal("repository api-recon seed", err)
	}
	limit := e2sandbox.WorkspaceLimit(e2sandbox.Config{})
	for _, name := range explorationSkillFiles {
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := e2sandbox.ReadWorkspaceFile(seed, "api-recon/"+name, limit)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s not carried unchanged: %v", name, err)
		}
	}
	// 작업 공간의 지시문이 스크립트 실행을 막으면 탐색 지시문과 반대로 읽혀 효과가 없다(#579 윤재상 리뷰). 파이썬 스크립트는
	// 실행하라고, node 스크립트는 실행하지 말라고 적혀 있어야 한다.
	skill, _ := e2sandbox.ReadWorkspaceFile(seed, "api-recon/SKILL.md", limit)
	reference, _ := e2sandbox.ReadWorkspaceFile(seed, "api-recon/reference.md", limit)
	text := string(skill) + "\n" + string(reference)
	for _, banned := range []string{"Do not run copied versions", "not enabled product transports", "Never run direct fetch"} {
		if strings.Contains(text, banned) {
			t.Fatalf("workspace instructions still forbid running the scripts: %q", banned)
		}
	}
	if !strings.Contains(string(skill), "run the Python scripts") || !strings.Contains(string(skill), "Do not run the Node/Puppeteer scripts") {
		t.Fatal("workspace SKILL.md does not tell how to run the scripts")
	}
	for _, name := range []string{"api-recon/scripts/runtime_harvest.js", "api-recon/scripts/preload.js", "api-recon/scripts/package.json"} {
		if _, err := e2sandbox.ReadWorkspaceFile(seed, name, limit); err == nil {
			t.Fatalf("%s must not be in the exploration sandbox", name)
		}
	}
}

// 파일 하나라도 없거나, 일반 파일이 아니거나, 상한을 넘으면 작업 공간을 만들지 않는다(탐색은 skill 없이 계속).
func TestExplorationSkillSeedRejectsIncompleteOrUnsafeFiles(t *testing.T) {
	write := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		for _, name := range explorationSkillFiles {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if os.MkdirAll(filepath.Dir(path), 0o700) != nil || os.WriteFile(path, []byte("owned "+name), 0o600) != nil {
				t.Fatal("fixture")
			}
		}
		return dir
	}
	if _, err := explorationSkillSeed(write(t)); err != nil {
		t.Fatal("complete fixture rejected", err)
	}
	for name, change := range map[string]func(string) error{
		"missing": func(dir string) error { return os.Remove(filepath.Join(dir, "scripts", "spider_mpa.py")) },
		"symlink": func(dir string) error {
			path := filepath.Join(dir, "reference.md")
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink("/etc/hostname", path)
		},
		"oversized": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(strings.Repeat("x", maxExplorationSkillBytes+1)), 0o600)
		},
	} {
		dir := write(t)
		if err := change(dir); err != nil {
			t.Fatal(err)
		}
		if seed, err := explorationSkillSeed(dir); err == nil || seed != nil {
			t.Fatalf("%s skill accepted", name)
		}
	}
}

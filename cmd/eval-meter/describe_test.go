package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestDescribeListsMarkerFieldsFromTheStructs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"-role", "describe"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	var got describeOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Format != describeFormat {
		t.Fatalf("format %q", got.Format)
	}
	marker := got.Markers["llm-verified"]
	want := map[string]bool{"rate_limited": true, "rejected_sampling": true, "rejected_provider": true, "role": true, "generations": true}
	seen := map[string]bool{}
	for _, name := range marker.Required {
		seen[name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("llm-verified fields miss %q: %v", name, marker.Required)
		}
	}
	row := got.Markers["engine-row"]
	if len(row.Optional) != 1 || row.Optional[0] != "ingress" {
		t.Fatalf("engine-row optional %v", row.Optional)
	}
	// 구조체 칸 수와 같다(코드에서 바로 뽑는다).
	if len(marker.Required)+len(marker.Optional) != len(jsonFields(verifiedMarker{}).Required)+len(jsonFields(verifiedMarker{}).Optional) {
		t.Fatal("field count drift")
	}
}

func TestDescribeRefusesOtherFlags(t *testing.T) {
	for _, args := range [][]string{{"-role", "describe", "-listen", ":1"}, {"-role", "describe", "-log-dir", "/x"},
		{"-role", "describe", "-path-log"}, {"-role", "describe", "extra"}} {
		var out, errOut bytes.Buffer
		if code := run(args, nil, &out, &errOut); code == 0 || out.Len() != 0 {
			t.Fatalf("%v: code %d out %q", args, code, out.String())
		}
	}
	t.Setenv(llmKeyEnv, "sk-should-not-be-here")
	var out, errOut bytes.Buffer
	if code := run([]string{"-role", "describe"}, nil, &out, &errOut); code == 0 {
		t.Fatal("describe must refuse the LLM key like other non-gateway roles")
	}
}

func TestBuildInfoFields(t *testing.T) {
	saved := sourceRevision
	t.Cleanup(func() { sourceRevision = saved })
	sourceRevision = "0123456789abcdef0123456789abcdef01234567"
	info := &debug.BuildInfo{GoVersion: "go1.x", Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "true"}}}
	got := buildFromInfo(info, true)
	if got.SourceRevision != sourceRevision || got.VCSRevision != "abc" || got.VCSModified == nil || !*got.VCSModified || got.GoVersion != "go1.x" {
		t.Fatalf("%+v", got)
	}
	none := buildFromInfo(nil, false)
	if none.SourceRevision != sourceRevision || none.VCSRevision != "" || none.VCSModified != nil {
		t.Fatalf("%+v", none)
	}
}

// Go job(setup-go로 go.mod 판이 보장됨)에서 describe 출력을 러너의 Python 점검기로 대조한다(#684 리뷰: 평가 job에는 Go 판이
// 보장되지 않아 그쪽 실제 빌드 시험은 건너뛸 수 있다). 표식 칸이 러너 기대와 어긋나면 여기서 깨진다.
func TestDescribeMatchesTheRunnerCheck(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 없음")
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	saved := sourceRevision
	t.Cleanup(func() { sourceRevision = saved })
	sourceRevision = sha
	var out, errOut bytes.Buffer
	if code := run([]string{"-role", "describe"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("describe code %d: %s", code, errOut.String())
	}
	script := "import json, sys; sys.path.insert(0, 'scripts/eval'); from meter_describe import check; " +
		"print(json.dumps(check(json.load(sys.stdin), sys.argv[1])))"
	cmd := exec.Command(python, "-I", "-B", "-c", script, sha)
	cmd.Dir = filepath.Join("..", "..")
	cmd.Stdin = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	got, err := cmd.Output()
	if err != nil {
		t.Fatalf("python check: %v: %s", err, stderr.String())
	}
	if strings.TrimSpace(string(got)) != "[]" {
		t.Fatalf("runner check reasons: %s", got)
	}
}

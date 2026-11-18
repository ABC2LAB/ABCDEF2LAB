package main

import (
	"bytes"
	"encoding/json"
	"runtime/debug"
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

package learnedlogin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func explorationEvidenceFixture() ExplorationEvidence {
	return ExplorationEvidence{Schema: ExplorationEvidenceSchema, RunRef: "run-owned", SessionRef: "explore-owned", JobID: "owned", TaskID: "19", TargetRef: "target-owned", TargetRevision: 3,
		SourceDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Sends: []ExplorationSend{{Seq: 17, Method: "POST", Path: "/login", Status: 302, BoundaryForwarded: true, ApprovedTarget: true, ObservationForwarded: true, ResponseReceived: true}}}
}

func TestVerifyExplorationEvidence(t *testing.T) {
	e := explorationEvidenceFixture()
	row := Row{RunRef: e.RunRef, Stage: StageExploration, TransportRef: "17", Method: "POST", Path: "/login", Status: 302}
	if p, reason := VerifyExplorationEvidence(row, e, e.JobID, e.TargetRef, e.TargetRevision); reason != "" || p != ProvenanceExplorationObserved {
		t.Fatalf("독립 관찰 대조 %q %q", p, reason)
	}
	for name, change := range map[string]func(*ExplorationEvidence, *Row){
		"other run":             func(v *ExplorationEvidence, _ *Row) { v.RunRef = "run-other" },
		"other job":             func(v *ExplorationEvidence, _ *Row) { v.JobID = "other" },
		"other target":          func(v *ExplorationEvidence, _ *Row) { v.TargetRef = "other" },
		"other revision":        func(v *ExplorationEvidence, _ *Row) { v.TargetRevision++ },
		"other sequence":        func(_ *ExplorationEvidence, r *Row) { r.TransportRef = "18" },
		"noncanonical sequence": func(_ *ExplorationEvidence, r *Row) { r.TransportRef = "017" },
		"other method":          func(_ *ExplorationEvidence, r *Row) { r.Method = "GET" },
		"other path":            func(_ *ExplorationEvidence, r *Row) { r.Path = "/other" },
		"other status":          func(_ *ExplorationEvidence, r *Row) { r.Status = 401 },
		"denied":                func(v *ExplorationEvidence, _ *Row) { v.Sends[0].BoundaryForwarded = false },
		"external":              func(v *ExplorationEvidence, _ *Row) { v.Sends[0].ApprovedTarget = false },
		"not received":          func(v *ExplorationEvidence, _ *Row) { v.Sends[0].ResponseReceived = false },
		"no observation":        func(v *ExplorationEvidence, _ *Row) { v.Sends[0].ObservationForwarded = false },
		"duplicate sequence":    func(v *ExplorationEvidence, _ *Row) { v.Sends = append(v.Sends, v.Sends[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			bad, r := explorationEvidenceFixture(), row
			change(&bad, &r)
			if _, reason := VerifyExplorationEvidence(r, bad, e.JobID, e.TargetRef, e.TargetRevision); reason != ReasonTransportUnverified {
				t.Fatalf("근거 불일치 허용: %q", reason)
			}
		})
	}
}

func TestExplorationEvidenceProtectedFile(t *testing.T) {
	dir := t.TempDir()
	e := explorationEvidenceFixture()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteExplorationEvidence(dir, e); err != nil {
		t.Fatal(err)
	}
	if err := WriteExplorationEvidence(dir, e); err != nil {
		t.Fatalf("동일 근거 재시도: %v", err)
	}
	got, found, err := ReadExplorationEvidence(dir)
	if err != nil || !found || len(got.Sends) != 1 || got.Sends[0].Seq != 17 {
		t.Fatalf("보호 근거 읽기 %+v %v %v", got, found, err)
	}
	if info, err := os.Stat(filepath.Join(dir, ExplorationEvidenceFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("보호 파일 권한 %v %v", info, err)
	}
	if err := os.Remove(filepath.Join(dir, ExplorationEvidenceFile)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadExplorationEvidence(dir); err != nil || found {
		t.Fatalf("기존 회차 호환 %v %v", found, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ExplorationEvidenceFile), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadExplorationEvidence(dir); err == nil {
		t.Fatal("손상된 근거를 무시함")
	}
}

func TestExplorationEvidenceStrictProjection(t *testing.T) {
	e := explorationEvidenceFixture()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string][]byte{
		"duplicate top":  bytes.Replace(raw, []byte(`"schema":`), []byte(`"schema":"other","schema":`), 1),
		"case alias":     bytes.Replace(raw, []byte(`"task_id":`), []byte(`"Task_ID":"20","task_id":`), 1),
		"duplicate send": bytes.Replace(raw, []byte(`"seq":`), []byte(`"seq":18,"seq":`), 1),
		"wrong task":     bytes.Replace(raw, []byte(`"task_id":"19"`), []byte(`"task_id":"019"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ExplorationEvidenceFile), changed, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ReadExplorationEvidence(dir); err == nil {
				t.Fatal("손상된 또는 모호한 JSON 근거 허용")
			}
		})
	}
}

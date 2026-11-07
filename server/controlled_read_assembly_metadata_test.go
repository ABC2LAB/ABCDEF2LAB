package server

import (
	"encoding/json"
	"github.com/Autumn-27/artex/e3record"
	"strings"
	"testing"
)

func TestControlledAssemblyMetadataRejectsAmbiguousJSON(t *testing.T) {
	for _, body := range []string{`{"format":"a","format":"b"}`, `{"Format":"a"}`, `{"format":null}`, `{"source":{"ModeVersion":1,"ModeVersion":2}}`, `{"source":{"Completed":false,"Extra":1}}`, `{"format":"a"}{}`} {
		var doc ControlledReadAssemblyMetadata
		if decodeControlledAssemblyJSON([]byte(body), &doc) == nil {
			t.Fatalf("입력 수용: %s", strings.ReplaceAll(body, "a", "x"))
		}
	}
}
func TestControlledAssemblyMetadataRejectsCollectorFacts(t *testing.T) {
	base := ControlledReadAssemblyMetadata{SnapshotRef: "s1", TargetOrigin: "http://127.0.0.1", Source: e3record.Source{ExplorationID: 1, ModeVersion: 1, KGSnapshotRef: "kg1", KGSnapshotDigest: [32]byte{1}, ModelTag: "model", CanaryFields: []string{"marker"}}}
	if !validControlledMetadataProjection(base) {
		t.Fatal("pre-execution projection 거부")
	}
	for _, edit := range []func(*e3record.Source){func(s *e3record.Source) { s.Completed = true }, func(s *e3record.Source) { s.Finalized = true }, func(s *e3record.Source) { s.EvalRunID = "eval" }, func(s *e3record.Source) { s.EvidenceRevision = 1 }, func(s *e3record.Source) { s.FinalCheckpointSequence = 1 }, func(s *e3record.Source) { s.Expected.Scope.RunRef = "run" }} {
		c := base
		edit(&c.Source)
		if validControlledMetadataProjection(c) {
			t.Fatal("미래 수집기 사실을 metadata로 수용")
		}
	}
}

func TestControlledAssemblyMetadataMarshalRoundTrip(t *testing.T) {
	original := ControlledReadAssemblyMetadata{Format: ControlledReadAssemblyMetadataFormat, SnapshotRef: "snapshot1", TargetOrigin: "http://127.0.0.1", Source: e3record.Source{CanaryFields: []string{"marker"}}}
	body, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ControlledReadAssemblyMetadata
	if decodeControlledAssemblyJSON(body, &decoded) != nil || len(decoded.Source.TrafficEvidenceIDs) != 0 || decoded.Source.CanaryFields[0] != "marker" {
		t.Fatal("직렬화된 실행 전 입력을 폐쇄 파서가 거부했습니다")
	}
	if original.Source.TrafficEvidenceIDs != nil {
		t.Fatal("직렬화가 원본 Source를 변경했습니다")
	}
}

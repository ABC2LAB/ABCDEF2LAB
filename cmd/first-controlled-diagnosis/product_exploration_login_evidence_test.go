package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/loginattempt"
	"github.com/Autumn-27/artex/server"
)

func TestBuildExplorationLoginEvidence(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	b := exploration.Binding{SessionRef: "explore-one", RunRef: "run-one", JobID: "one", TaskID: "1", TargetRef: "target-one", TargetRevision: 2}
	row := loginattempt.Row{Stage: loginattempt.StageExploration, RunRef: b.RunRef, TransportRef: "17", Transport: loginattempt.TransportSent,
		Method: "POST", Path: "/login", Status: 302}
	rec := exploreboundary.Record{SessionRef: b.SessionRef, Seq: 17, Decision: "forwarded", Dest: exploreboundary.DestApproved, Method: "POST", Status: 302}
	obs := diagnosisflow.ExplorationObservation{Binding: diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID,
		TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest}, Seq: 17, Method: "POST", Path: "/login",
		Status: 302, Forwarded: true, ResponseReceived: true, Transport: "http"}
	read := func(o diagnosisflow.ExplorationObservation) func(uint64) (diagnosisflow.ExplorationObservation, error) {
		return func(seq uint64) (diagnosisflow.ExplorationObservation, error) { return o, nil }
	}
	if got, err := buildExplorationLoginEvidence(b, digest, []loginattempt.Row{row, row}, []exploreboundary.Record{rec}, read(obs)); err != nil ||
		len(got.Sends) != 1 || got.Sends[0].Path != "/login" {
		t.Fatalf("경계 증거 인계 %+v %v", got, err)
	}
	for name, change := range map[string]func(*loginattempt.Row, *exploreboundary.Record, *diagnosisflow.ExplorationObservation){
		"different run": func(r *loginattempt.Row, _ *exploreboundary.Record, _ *diagnosisflow.ExplorationObservation) {
			r.RunRef = "other"
		},
		"missing sequence": func(r *loginattempt.Row, _ *exploreboundary.Record, _ *diagnosisflow.ExplorationObservation) {
			r.TransportRef = "18"
		},
		"denied": func(_ *loginattempt.Row, r *exploreboundary.Record, _ *diagnosisflow.ExplorationObservation) {
			r.Decision = "denied"
		},
		"external": func(_ *loginattempt.Row, r *exploreboundary.Record, _ *diagnosisflow.ExplorationObservation) {
			r.Dest = exploreboundary.DestExternal
		},
		"other method": func(_ *loginattempt.Row, _ *exploreboundary.Record, o *diagnosisflow.ExplorationObservation) {
			o.Method = "GET"
		},
		"other path": func(_ *loginattempt.Row, _ *exploreboundary.Record, o *diagnosisflow.ExplorationObservation) {
			o.Path = "/other"
		},
		"other status": func(_ *loginattempt.Row, _ *exploreboundary.Record, o *diagnosisflow.ExplorationObservation) {
			o.Status = 401
		},
		"no response": func(_ *loginattempt.Row, _ *exploreboundary.Record, o *diagnosisflow.ExplorationObservation) {
			o.ResponseReceived = false
		},
		"other session": func(_ *loginattempt.Row, _ *exploreboundary.Record, o *diagnosisflow.ExplorationObservation) {
			o.Binding.SessionRef = "other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, record, observation := row, rec, obs
			change(&r, &record, &observation)
			if _, err := buildExplorationLoginEvidence(b, digest, []loginattempt.Row{r}, []exploreboundary.Record{record}, read(observation)); err == nil {
				t.Fatal("불일치 경계 관찰을 허용함")
			}
		})
	}
	if _, err := buildExplorationLoginEvidence(b, digest, []loginattempt.Row{row}, []exploreboundary.Record{rec, rec}, read(obs)); err == nil {
		t.Fatal("중복 경계 순번을 허용함")
	}
	if _, err := buildExplorationLoginEvidence(b, digest, []loginattempt.Row{row}, []exploreboundary.Record{rec}, func(uint64) (diagnosisflow.ExplorationObservation, error) {
		return diagnosisflow.ExplorationObservation{}, errors.New("lost observation")
	}); err == nil {
		t.Fatal("관찰 읽기 실패를 허용함")
	}
}

func TestBuildExplorationLoginEvidenceIncludesCSRFGET(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	b := exploration.Binding{SessionRef: "explore-one", RunRef: "run-one", JobID: "one", TaskID: "1", TargetRef: "target-one", TargetRevision: 2}
	row := loginattempt.Row{Stage: loginattempt.StageExploration, RunRef: b.RunRef, TransportRef: "17", Transport: loginattempt.TransportSent,
		Method: "POST", Path: "/login", Status: 302, FieldSources: []loginattempt.RowFieldSource{{Destination: "_csrf", SourceMethod: "GET",
			SourcePath: "/api/csrf", SourceType: "json_field", SourceField: "csrfToken", SourceRef: "16", SourceStatus: 200}}}
	flow := diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID,
		TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest}
	records := []exploreboundary.Record{
		{SessionRef: b.SessionRef, Seq: 16, Decision: "forwarded", Dest: exploreboundary.DestApproved, Method: "GET", Status: 200},
		{SessionRef: b.SessionRef, Seq: 17, Decision: "forwarded", Dest: exploreboundary.DestApproved, Method: "POST", Status: 302},
	}
	read := func(seq uint64) (diagnosisflow.ExplorationObservation, error) {
		method, path, status := "GET", "/api/csrf", 200
		if seq == 17 {
			method, path, status = "POST", "/login", 302
		}
		return diagnosisflow.ExplorationObservation{Binding: flow, Seq: seq, Method: method, Path: path, Status: status,
			Forwarded: true, ResponseReceived: true, Transport: "http"}, nil
	}
	evidence, err := buildExplorationLoginEvidence(b, digest, []loginattempt.Row{row}, records, read)
	if err != nil || len(evidence.Sends) != 2 {
		t.Fatalf("GET 출처 인계 %+v %v", evidence, err)
	}
	records[0].Dest = exploreboundary.DestExternal
	if _, err := buildExplorationLoginEvidence(b, digest, []loginattempt.Row{row}, records, read); err == nil {
		t.Fatal("외부 GET을 출처로 수용함")
	}
}

func TestExplorationLoginEvidenceHandoverFailureKeepsMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := server.MarkLoginHistoryUnavailable(dir); err != nil {
		t.Fatal(err)
	}
	read := func() ([]loginattempt.Row, uint64, error) { return nil, 0, nil }
	if handOverExplorationLogins(dir, read, nil, func([]loginattempt.Row) error { return errExplorationLoginEvidence }) {
		t.Fatal("근거 인계 실패를 성공으로 처리함")
	}
	if _, err := os.Lstat(filepath.Join(dir, server.E2HTTPLoginHistoryUnavailableFile)); err != nil {
		t.Fatal("실패 표지를 지움", err)
	}
	if _, found, err := learnedlogin.ReadExplorationEvidence(dir); err != nil || found {
		t.Fatalf("부분 근거를 발행함 %v %v", found, err)
	}
}

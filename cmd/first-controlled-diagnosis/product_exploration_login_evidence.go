package main

import (
	"context"
	"errors"
	"strconv"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/loginattempt"
)

var errExplorationLoginEvidence = errors.New("exploration login evidence unavailable")

// explorationLoginEvidence reads the closed boundary's private records and
// observations, independently from its login-attempt rows. The boundary's
// path_pattern is masked, so exact path comparison uses the private observation.
func (h *productExplorationHost) explorationLoginEvidence(ctx context.Context, b exploration.Binding, digest string, rows []loginattempt.Row) (learnedlogin.ExplorationEvidence, error) {
	if h == nil || digest == "" {
		return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
	}
	flow := diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest}
	reader := exploreboundary.ControlReader{Client: h.control, Binding: b, Flow: flow}
	records, omitted, err := reader.Records(ctx)
	if err != nil || omitted != 0 {
		return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
	}
	return buildExplorationLoginEvidence(b, digest, rows, records, func(seq uint64) (diagnosisflow.ExplorationObservation, error) {
		return reader.ReadObservation(ctx, flow, seq)
	})
}

// buildExplorationLoginEvidence validates both independent boundary products
// before a protected projection is published. A sent row may be appended more
// than once as its contrast outcome changes; its request sequence is unique.
func buildExplorationLoginEvidence(b exploration.Binding, digest string, rows []loginattempt.Row, records []exploreboundary.Record,
	read func(uint64) (diagnosisflow.ExplorationObservation, error)) (learnedlogin.ExplorationEvidence, error) {
	e := learnedlogin.ExplorationEvidence{Schema: learnedlogin.ExplorationEvidenceSchema, RunRef: b.RunRef, SessionRef: b.SessionRef, JobID: b.JobID,
		TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest, Sends: []learnedlogin.ExplorationSend{}}
	flow := diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest}
	bySeq := map[uint64]exploreboundary.Record{}
	for _, rec := range records {
		if rec.Seq == 0 || rec.SessionRef != b.SessionRef {
			return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
		}
		if _, duplicate := bySeq[rec.Seq]; duplicate {
			return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
		}
		bySeq[rec.Seq] = rec
	}
	seen := map[uint64]learnedlogin.ExplorationSend{}
	add := func(ref, method, path string, status int) error {
		seq, err := strconv.ParseUint(ref, 10, 64)
		if err != nil || seq == 0 || strconv.FormatUint(seq, 10) != ref {
			return errExplorationLoginEvidence
		}
		if previous, duplicated := seen[seq]; duplicated {
			if previous.Method != method || previous.Path != path || previous.Status != status {
				return errExplorationLoginEvidence
			}
			return nil
		}
		rec, ok := bySeq[seq]
		if !ok || rec.Decision != "forwarded" || rec.Dest != exploreboundary.DestApproved || rec.Method != method || rec.Status != status {
			return errExplorationLoginEvidence
		}
		obs, err := read(seq)
		if err != nil || obs.Binding != flow || obs.Seq != seq || obs.Method != method || obs.Path != path || obs.Status != status ||
			!obs.Forwarded || !obs.ResponseReceived || obs.Transport != "http" {
			return errExplorationLoginEvidence
		}
		send := learnedlogin.ExplorationSend{Seq: seq, Method: obs.Method, Path: obs.Path, Status: obs.Status,
			BoundaryForwarded: true, ApprovedTarget: true, ObservationForwarded: true, ResponseReceived: true}
		seen[seq] = send
		e.Sends = append(e.Sends, send)
		return nil
	}
	for _, row := range rows {
		if row.Stage != loginattempt.StageExploration || row.RunRef != b.RunRef {
			return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
		}
		if row.Transport != loginattempt.TransportSent {
			continue
		}
		if add(row.TransportRef, row.Method, row.Path, row.Status) != nil {
			return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
		}
		for _, source := range row.FieldSources {
			if source.SourceMethod != "GET" || source.SourceStatus < 200 || source.SourceStatus > 299 ||
				add(source.SourceRef, source.SourceMethod, source.SourcePath, source.SourceStatus) != nil {
				return learnedlogin.ExplorationEvidence{}, errExplorationLoginEvidence
			}
		}
	}
	return e, nil
}

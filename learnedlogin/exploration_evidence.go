package learnedlogin

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/internal/privateinput"
)

// ExplorationEvidenceFile is a protected projection of the exploration boundary's
// request records and private observations. It contains no credentials, response
// bodies, cookies, or CSRF values. It is not an E② signed audit journal.
const (
	ExplorationEvidenceFile   = "exploration-login-evidence.json"
	ExplorationEvidenceSchema = "jindo.exploration-login-evidence/v2"
	explorationEvidenceV1     = "jindo.exploration-login-evidence/v1"
	maxExplorationSends       = 4096
	maxExplorationFileBytes   = 4 << 20
)

var ErrExplorationEvidence = errors.New("learnedlogin: exploration evidence unavailable")

type ExplorationSend struct {
	Seq                  uint64 `json:"seq"`
	Method               string `json:"method"`
	Path                 string `json:"path"`
	Status               int    `json:"status"`
	BoundaryForwarded    bool   `json:"boundary_forwarded"`
	ApprovedTarget       bool   `json:"approved_target"`
	ObservationForwarded bool   `json:"observation_forwarded"`
	ResponseReceived     bool   `json:"response_received"`
}

type ExplorationEvidence struct {
	Schema         string            `json:"schema"`
	RunRef         string            `json:"run_ref"`
	SessionRef     string            `json:"session_ref"`
	JobID          string            `json:"job_id"`
	TaskID         string            `json:"task_id,omitempty"`
	TargetRef      string            `json:"target_ref"`
	TargetRevision uint64            `json:"target_revision"`
	SourceDigest   string            `json:"source_digest"`
	Sends          []ExplorationSend `json:"sends"`
}

func (e ExplorationEvidence) valid() bool {
	if (e.Schema != ExplorationEvidenceSchema && e.Schema != explorationEvidenceV1) ||
		(e.Schema == ExplorationEvidenceSchema && !validTaskID(e.TaskID)) || (e.Schema == explorationEvidenceV1 && e.TaskID != "") ||
		!validRef(e.RunRef) || !validRef(e.SessionRef) || !validRef(e.JobID) ||
		!validRef(e.TargetRef) || e.TargetRevision == 0 || len(e.Sends) > maxExplorationSends || len(e.SourceDigest) != 64 {
		return false
	}
	if _, err := hex.DecodeString(e.SourceDigest); err != nil {
		return false
	}
	seen := map[uint64]bool{}
	for _, send := range e.Sends {
		if send.Seq == 0 || seen[send.Seq] || (send.Method != "POST" && send.Method != "GET") || !validPath(send.Path) || send.Status < 100 || send.Status > 599 {
			return false
		}
		seen[send.Seq] = true
	}
	return true
}

func validTaskID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

// VerifyExplorationEvidence compares a learned row with a separate boundary
// projection. The boundary does not record account identity or response body
// completion, so this provenance intentionally claims neither property.
func VerifyExplorationEvidence(row Row, e ExplorationEvidence, jobID, targetRef string, revision uint64) (Provenance, Reason) {
	if !e.valid() || row.Stage != StageExploration || row.RunRef != e.RunRef || e.JobID != jobID ||
		e.TargetRef != targetRef || e.TargetRevision != revision {
		return "", ReasonTransportUnverified
	}
	seq, err := strconv.ParseUint(row.TransportRef, 10, 64)
	if err != nil || seq == 0 || strconv.FormatUint(seq, 10) != row.TransportRef {
		return "", ReasonTransportUnverified
	}
	for _, send := range e.Sends {
		if send.Seq != seq {
			continue
		}
		if send.Method != row.Method || send.Path != row.Path || send.Status != row.Status || !send.BoundaryForwarded ||
			!send.ApprovedTarget || !send.ObservationForwarded || !send.ResponseReceived {
			return "", ReasonTransportUnverified
		}
		return ProvenanceExplorationObserved, ""
	}
	return "", ReasonTransportUnverified
}

// WriteExplorationEvidence publishes a complete protected snapshot before the
// exploration handoff marker is cleared. A failed write leaves that marker in
// place, so neither diagnosis login attempts nor E③ can use a partial handoff.
func WriteExplorationEvidence(stateDir string, e ExplorationEvidence) error {
	if privateinput.CheckDirectory(stateDir, true) != nil || !e.valid() {
		return ErrExplorationEvidence
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > maxExplorationFileBytes {
		return ErrExplorationEvidence
	}
	path := filepath.Join(stateDir, ExplorationEvidenceFile)
	if _, err := os.Lstat(path); err == nil {
		old, found, readErr := ReadExplorationEvidence(stateDir)
		if readErr != nil || !found || !reflect.DeepEqual(old, e) {
			return ErrExplorationEvidence
		}
		return syncExplorationEvidenceDir(stateDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrExplorationEvidence
	}
	f, err := os.CreateTemp(stateDir, ".exploration-login-evidence-*")
	if err != nil {
		return ErrExplorationEvidence
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return ErrExplorationEvidence
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return ErrExplorationEvidence
	}
	if err = f.Close(); err != nil || privateinput.CheckDirectory(stateDir, true) != nil {
		return ErrExplorationEvidence
	}
	if os.Link(f.Name(), path) != nil {
		old, found, readErr := ReadExplorationEvidence(stateDir)
		if readErr != nil || !found || !reflect.DeepEqual(old, e) {
			return ErrExplorationEvidence
		}
	}
	return syncExplorationEvidenceDir(stateDir)
}

func syncExplorationEvidenceDir(stateDir string) error {
	dir, err := os.Open(stateDir)
	if err != nil {
		return ErrExplorationEvidence
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrExplorationEvidence
	}
	return nil
}

// ReadExplorationEvidence distinguishes legacy runs without a file from a
// present but damaged file. Both cases retain their existing row history.
func ReadExplorationEvidence(stateDir string) (ExplorationEvidence, bool, error) {
	if privateinput.CheckDirectory(stateDir, true) != nil {
		return ExplorationEvidence{}, false, ErrExplorationEvidence
	}
	path := filepath.Join(stateDir, ExplorationEvidenceFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return ExplorationEvidence{}, false, nil
	} else if err != nil {
		return ExplorationEvidence{}, false, ErrExplorationEvidence
	}
	raw, err := privateinput.ReadFile(path, maxExplorationFileBytes)
	if err != nil {
		return ExplorationEvidence{}, true, ErrExplorationEvidence
	}
	defer clear(raw)
	var e ExplorationEvidence
	if e2config.StrictJSON(raw, &e) != nil || !exactExplorationEvidenceFields(raw, e.Schema) || !e.valid() {
		return ExplorationEvidence{}, true, ErrExplorationEvidence
	}
	return e, true, nil
}

func exactExplorationEvidenceFields(raw []byte, schema string) bool {
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		return false
	}
	top := []string{"schema", "run_ref", "session_ref", "job_id", "target_ref", "target_revision", "source_digest", "sends"}
	if schema == ExplorationEvidenceSchema {
		top = append(top, "task_id")
	}
	if !exactExplorationKeys(document, top) {
		return false
	}
	var sends []map[string]json.RawMessage
	if json.Unmarshal(document["sends"], &sends) != nil {
		return false
	}
	fields := []string{"seq", "method", "path", "status", "boundary_forwarded", "approved_target", "observation_forwarded", "response_received"}
	for _, send := range sends {
		if !exactExplorationKeys(send, fields) {
			return false
		}
	}
	return true
}

func exactExplorationKeys(document map[string]json.RawMessage, fields []string) bool {
	if len(document) != len(fields) {
		return false
	}
	for _, name := range fields {
		if _, ok := document[name]; !ok {
			return false
		}
	}
	return true
}

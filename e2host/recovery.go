package e2host

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
)

const recoveryVersion = "jindo.e2host-operator-recovery/v1"

var operatorRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$`)

// RecoveryStatus reports only a verified current head. Open remains open even
// after operator quarantine and can never be used as sealed E③ evidence.
type RecoveryStatus struct {
	Head               e2checkpoint.Head `json:"head"`
	Pending            []string          `json:"pending_attempts"`
	PendingStateChange bool              `json:"pending_state_change"`
	Recovered          bool              `json:"operator_recovered"`
}

type RecoveryRequest struct {
	RunRef             string
	ExpectedRevision   uint64
	ExpectedCheckpoint string
	Operator           string
}

type recoveryRecord struct {
	Version     string            `json:"version"`
	Head        e2checkpoint.Head `json:"head"`
	Pending     []string          `json:"pending_attempts"`
	Operator    string            `json:"operator"`
	DockerProof string            `json:"docker_proof_sha256"`
	At          time.Time         `json:"at"`
	MAC         string            `json:"mac"`
}

func (h *Host) recoveryMAC(rec recoveryRecord) (string, error) {
	rec.MAC = ""
	body, err := json.Marshal(rec)
	if err != nil {
		return "", ErrUnavailable
	}
	mac := hmac.New(sha256.New, h.key)
	_, _ = mac.Write([]byte(recoveryVersion + "\x00"))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (r *run) readRecoveryRecord() (recoveryRecord, bool, error) {
	path := filepath.Join(r.directory, "operator-recovery.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return recoveryRecord{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
		info.Size() < 1 || info.Size() > 64<<10 {
		return recoveryRecord{}, false, ErrUnavailable
	}
	data, err := readSnapshot(path)
	if err != nil {
		return recoveryRecord{}, false, ErrUnavailable
	}
	var rec recoveryRecord
	if json.Unmarshal(data, &rec) != nil {
		return recoveryRecord{}, false, ErrUnavailable
	}
	return rec, true, nil
}

func (h *Host) verifyRecoveryRecord(rec recoveryRecord, head e2checkpoint.Head, events []e2proxy.Event) error {
	if rec.Version != recoveryVersion || rec.Head != head || !operatorRef.MatchString(rec.Operator) ||
		len(rec.DockerProof) != 64 || rec.At.IsZero() {
		return ErrUnavailable
	}
	if _, err := hex.DecodeString(rec.DockerProof); err != nil {
		return ErrUnavailable
	}
	pending, valid := unfinishedAttempts(events)
	if !valid || len(pending) == 0 || len(pending) != len(rec.Pending) ||
		pendingStateChange(events, pending) {
		return ErrUnavailable
	}
	for i, ref := range pending {
		if rec.Pending[i] != ref {
			return ErrUnavailable
		}
	}
	expected, err := h.recoveryMAC(rec)
	if err != nil || subtle.ConstantTimeCompare([]byte(expected), []byte(rec.MAC)) != 1 {
		return ErrUnavailable
	}
	return nil
}

func pendingStateChange(events []e2proxy.Event, pending []string) bool {
	refs := make(map[string]bool, len(pending))
	for _, ref := range pending {
		refs[ref] = true
	}
	for _, event := range events {
		if event.Stage == "intent" && refs[event.AttemptID] &&
			event.Method != "GET" && event.Method != "HEAD" {
			return true
		}
	}
	return false
}

// InspectUnfinished checks every journal and the requested open head while
// keeping the Host socket closed. A malformed peer journal blocks inspection.
func (h *Host) InspectUnfinished(ctx context.Context, runRef string) (RecoveryStatus, error) {
	if h == nil || runRef == "" || ctx == nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	h.scanMu.Lock()
	defer h.scanMu.Unlock()
	if h.CheckEpoch(ctx, h.db) != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	var status RecoveryStatus
	if h.checkRestoredHeads(ctx, false, runRef, &status) != nil ||
		status.Head.Phase != e2checkpoint.Open {
		return RecoveryStatus{}, ErrUnavailable
	}
	return status, nil
}

// RecoverUnfinished is only invoked by the offline operator CLI while it holds
// host.lock. Docker termination is checked inside the scan lock. No result,
// interrupt, seal or HTTP action is fabricated.
func (h *Host) RecoverUnfinished(ctx context.Context, request RecoveryRequest, dockerSocket, project, selfID string) (RecoveryStatus, error) {
	if h == nil || ctx == nil || request.RunRef == "" || request.ExpectedRevision == 0 ||
		!operatorRef.MatchString(request.Operator) {
		return RecoveryStatus{}, ErrUnavailable
	}
	h.scanMu.Lock()
	defer h.scanMu.Unlock()
	if h.CheckEpoch(ctx, h.db) != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	var status RecoveryStatus
	if h.checkRestoredHeads(ctx, false, request.RunRef, &status) != nil ||
		status.Head.Phase != e2checkpoint.Open ||
		status.Head.Revision != request.ExpectedRevision ||
		status.Head.Checkpoint.MAC != request.ExpectedCheckpoint {
		return RecoveryStatus{}, ErrUnavailable
	}
	if status.Recovered {
		return status, nil
	}
	if status.PendingStateChange {
		return RecoveryStatus{}, ErrUnavailable
	}
	proof, err := VerifyDockerTermination(ctx, dockerSocket, project, selfID)
	if err != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	rec := recoveryRecord{Version: recoveryVersion, Head: status.Head, Pending: status.Pending,
		Operator: request.Operator, DockerProof: proof, At: time.Now().UTC()}
	rec.MAC, err = h.recoveryMAC(rec)
	if err != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	// Recheck immediately before the irreversible link. A started container or
	// changed inventory invalidates the original termination observation.
	currentProof, err := VerifyDockerTermination(ctx, dockerSocket, project, selfID)
	if err != nil || currentProof != proof {
		return RecoveryStatus{}, ErrUnavailable
	}
	digest := sha256.Sum256([]byte(request.RunRef))
	dir := filepath.Join(h.root, hex.EncodeToString(digest[:]))
	if writeNewPrivateFile(dir, "operator-recovery.json", data) != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	if h.checkRestoredHeads(ctx, false, "", nil) != nil {
		return RecoveryStatus{}, ErrUnavailable
	}
	status.Recovered = true
	return status, nil
}

// writeNewPrivateFile never overwrites an existing recovery decision. A crash
// before the hard link leaves only an unused temporary file; a crash afterward
// leaves the complete fsynced record for idempotent inspection.
func writeNewPrivateFile(dir, name string, data []byte) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrUnavailable
	}
	tmp := filepath.Join(dir, name+"."+hex.EncodeToString(nonce[:])+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrUnavailable
	}
	defer os.Remove(tmp)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if os.Link(tmp, filepath.Join(dir, name)) != nil {
		return ErrUnavailable
	}
	parent, err := os.Open(dir)
	if err != nil {
		return ErrUnavailable
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

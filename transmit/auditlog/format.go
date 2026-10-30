// Package auditlog provides a synchronous, tamper-evident file Recorder for E②.
// The caller must keep its directory, HMAC key and trusted checkpoints outside
// the agent's authority. It does not establish OS privilege separation itself.
package auditlog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

const (
	Version        = 1
	KeyBytes       = 32
	MaxRecordBytes = 16 * 1024 // Includes the required newline framing.
	journalIDBytes = 16
)

var (
	ErrConfig    = errors.New("auditlog: invalid configuration")
	ErrEvent     = errors.New("auditlog: invalid event")
	ErrExists    = errors.New("auditlog: file already exists")
	ErrStorage   = errors.New("auditlog: storage unavailable")
	ErrClosed    = errors.New("auditlog: recorder closed")
	ErrFailed    = errors.New("auditlog: recorder failed")
	ErrIntegrity = errors.New("auditlog: integrity verification failed")
	ErrRead      = errors.New("auditlog: input unavailable")
)

// Config is trusted server configuration. Keys need 32 random bytes from the
// operator's secret store; they are copied and never written to the journal.
type Config struct {
	Scope  transmit.Scope
	KeyRef string
	Key    []byte `json:"-"`
}

// Checkpoint is an authenticated prefix commitment. Store the newest checkpoint
// outside the agent's write authority. An older checkpoint cannot detect rollback
// to that older prefix. Sequence counts events; zero identifies the header alone.
type Checkpoint struct {
	Version   int
	JournalID string
	Scope     transmit.Scope
	KeyRef    string
	Sequence  uint64
	MAC       string
}

// Summary is returned only after verification against the complete checkpoint.
// It intentionally exposes no event contents or raw input/error text.
type Summary struct {
	Checkpoint Checkpoint
	Intents    uint64
	Results    uint64
}

type header struct {
	Version   int            `json:"version"`
	JournalID string         `json:"journal_id"`
	Scope     transmit.Scope `json:"scope"`
	KeyRef    string         `json:"key_ref"`
	CreatedAt time.Time      `json:"created_at"`
}

type headerLine struct {
	Header header `json:"header"`
	MAC    string `json:"mac"`
}

type eventPayload struct {
	Sequence uint64         `json:"sequence"`
	Previous string         `json:"previous"`
	Event    transmit.Event `json:"event"`
}

type eventLine struct {
	Record eventPayload `json:"record"`
	MAC    string       `json:"mac"`
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var filePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validRef(value string) bool {
	return refPattern.MatchString(value) && !strings.Contains(value, "://")
}

func validScope(scope transmit.Scope) bool {
	for _, ref := range []string{scope.RunRef, scope.TaskRef, scope.CaseRef, scope.ResetRef, scope.PolicyRef} {
		if !validRef(ref) {
			return false
		}
	}
	return true
}

func validTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 }

// Restrict string aliases too: Go's Reason type alone does not reject arbitrary
// text. Never persist an unknown reason, raw method, URL or OS error as a field.
func validEvent(event transmit.Event, scope transmit.Scope) bool {
	if event.Scope != scope || event.Attempt == 0 || !validTime(event.At) {
		return false
	}
	for _, ref := range []string{event.AccountRef, event.SessionRef, event.ResourceRef, event.ApprovalRef} {
		if ref != "" && !validRef(ref) {
			return false
		}
	}
	if event.Method != "GET" && event.Method != "UNSUPPORTED" {
		return false
	}
	if event.Stage == "intent" || event.Admitted {
		if event.Method != "GET" || event.AccountRef == "" || event.SessionRef == "" || event.ResourceRef == "" || event.ApprovalRef == "" {
			return false
		}
	}
	if event.Admitted != validTime(event.AdmittedAt) || !event.Admitted && (event.RequestWritten || event.ResponseReceived || !event.AdmittedAt.IsZero()) {
		return false
	}
	if event.ResponseReceived {
		if event.StatusCode < 100 || event.StatusCode > 599 {
			return false
		}
	} else if event.StatusCode != 0 {
		return false
	}
	switch event.Stage {
	case "intent":
		return event.Reason == "" && !event.Admitted
	case "result":
		if event.Reason == transmit.Completed && (!event.Admitted || !event.ResponseReceived) {
			return false
		}
		switch event.Reason {
		case transmit.Completed, transmit.Stopped, transmit.Canceled, transmit.UnsupportedAction,
			transmit.UnknownResource, transmit.UnknownAccount, transmit.UnverifiedAccount,
			transmit.ExpiredAccount, transmit.AccountNotApproved, transmit.ExpiredApproval,
			transmit.BudgetExhausted, transmit.AuditUnavailable, transmit.TransportError,
			transmit.TimedOut, transmit.RedirectRefused, transmit.ResponseTooLarge,
			transmit.UnsupportedResponse, transmit.InvalidCandidate, transmit.CanaryMismatch:
			return true
		}
	}
	return false
}

func sign(key []byte, domain string, value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", ErrEvent
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ABC2LAB.auditlog." + domain + ".v1\x00"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func validHex(value string, size int) bool {
	if len(value) != size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && hex.EncodeToString(decoded) == value
}

func equalMAC(a, b string) bool {
	if !validHex(a, sha256.Size) || !validHex(b, sha256.Size) {
		return false
	}
	x, err := hex.DecodeString(a)
	if err != nil || len(x) != sha256.Size {
		return false
	}
	y, err := hex.DecodeString(b)
	return err == nil && len(y) == sha256.Size && hmac.Equal(x, y)
}

func encodeLine(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > MaxRecordBytes {
		return nil, ErrEvent
	}
	return append(body, '\n'), nil
}

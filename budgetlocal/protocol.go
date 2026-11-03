// Package budgetlocal carries an operator's explicit approval over a local
// Unix socket to the process that owns the cumulative budget ledger.
package budgetlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

const (
	maxMessageBytes = 4096
	wireRejected    = "rejected"
	wireUnknown     = "unknown"
)

var (
	ErrInvalid     = errors.New("budgetlocal: invalid request")
	ErrUnavailable = errors.New("budgetlocal: unavailable")
	ErrDenied      = errors.New("budgetlocal: rejected")
	ErrUnknown     = errors.New("budgetlocal: outcome unknown")
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

// Request carries no issuer claim: the server derives the issuer from the
// kernel-authenticated peer UID and never accepts one from JSON.
type Request struct {
	RequestRef  string    `json:"approval_request_ref"`
	TaskRef     string    `json:"task_ref"`
	PolicyRef   string    `json:"policy_ref"`
	ResetRef    string    `json:"reset_ref"`
	MaxRequests uint64    `json:"max_requests"`
	ValidFrom   time.Time `json:"valid_from"`
	ValidUntil  time.Time `json:"valid_until"`
}

// ClientConfig is pinned in an operator-owned private file. The server UID is
// trusted configuration, never inferred from a user-supplied socket path.
type ClientConfig struct {
	SocketPath string `json:"socket_path"`
	ServerUID  uint32 `json:"server_uid"`
}

// Response contains non-secret approval metadata only. It is not authority to
// transmit and does not include the ledger or any credential.
type Response struct {
	RequestRef  string    `json:"approval_request_ref"`
	TaskRef     string    `json:"task_ref"`
	BudgetRef   string    `json:"budget_ref"`
	Epoch       string    `json:"epoch"`
	State       string    `json:"state"`
	PolicyRef   string    `json:"policy_ref"`
	ResetRef    string    `json:"reset_ref"`
	MaxRequests uint64    `json:"max_requests"`
	ValidFrom   time.Time `json:"valid_from"`
	ValidUntil  time.Time `json:"valid_until"`
}

type wireResponse struct {
	Response
	Error string `json:"error,omitempty"`
}

// AuthorizeAndIssue must hold the server's task/policy/reset registration
// synchronization from authorization through BudgetAuthority.Issue. The
// request's IssuerRef is derived by the socket adapter from SO_PEERCRED.
// Return ErrDenied only for a definitive denial known to precede issuance.
// Any other callback failure, deadline or record returned together with an error
// has an unknown outcome and must be retried with the same RequestRef.
// The callback must honor cancellation. A nil callback is never accepted.
type AuthorizeAndIssue func(context.Context, uint32, db.BudgetApprovalRequest, time.Time) (db.BudgetApprovalRecord, error)

func validRef(ref string) bool {
	return refPattern.MatchString(ref) && !strings.Contains(ref, "://")
}

func validRequest(r Request) bool {
	return validRef(r.RequestRef) && validRef(r.TaskRef) &&
		validRef(r.PolicyRef) && validRef(r.ResetRef) &&
		r.MaxRequests > 0 && r.MaxRequests <= uint64(math.MaxInt64) &&
		!r.ValidFrom.IsZero() && !r.ValidUntil.IsZero() &&
		r.ValidFrom.Before(r.ValidUntil) &&
		r.ValidFrom.Year() >= 1 && r.ValidUntil.Year() <= 9999
}

// ParseRequest enforces a single exact JSON object with no unknown fields.
func ParseRequest(data []byte) (Request, error) {
	if len(data) == 0 || len(data) > maxMessageBytes || !singleTopLevelKeys(data) {
		return Request{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request Request
	if decoder.Decode(&request) != nil {
		return Request{}, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !validRequest(request) {
		return Request{}, ErrInvalid
	}
	return request, nil
}

func ParseClientConfig(data []byte) (ClientConfig, error) {
	if len(data) == 0 || len(data) > maxMessageBytes || !singleTopLevelKeys(data) {
		return ClientConfig{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config ClientConfig
	if decoder.Decode(&config) != nil {
		return ClientConfig{}, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !filepath.IsAbs(config.SocketPath) ||
		config.ServerUID == 0 || config.ServerUID == ^uint32(0) {
		return ClientConfig{}, ErrInvalid
	}
	return config, nil
}

func singleTopLevelKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		// encoding/json case-folds field names, including some Unicode aliases.
		// Accept only the exact ASCII spelling used by this protocol.
		for _, c := range key {
			if c < 'a' || c > 'z' {
				if c < '0' || c > '9' {
					if c != '_' {
						return false
					}
				}
			}
		}
		if seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	close, err := decoder.Token()
	if err != nil || close != json.Delim('}') {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

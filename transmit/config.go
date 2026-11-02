// Package transmit implements E② for server-registered, read-only HTTP requests.
// It does not intercept other ARTEX, shell, browser or MCP network paths.
package transmit

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

// Scope binds a transmitter to one server-issued execution snapshot. PolicyRef
// identifies execution approval, not the application's expected-access policy.
type Scope struct {
	RunRef    string
	TaskRef   string
	CaseRef   string
	ResetRef  string
	PolicyRef string
}

// Session is supplied by the trusted authentication adapter, never by the model.
// Verified means the adapter checked this identity; this package cannot log in.
type Session struct {
	Account   idor.AccountRef
	ExpiresAt time.Time
	Headers   http.Header `json:"-"`
}

// Resource is a fixed request binding. ReadOnly must be explicitly authorized:
// a GET method alone does not establish absence of side effects. Accounts grants
// permission to perform this test, including cross-access tests, not app access.
type Resource struct {
	Ref         string
	URL         string `json:"-"`
	ApprovalRef string
	ReadOnly    bool
	Accounts    []string
	View        *JSONViewRule
}

// JSONViewRule recognizes one explicitly registered fixture response shape.
// Only exact nontrivial marker values in DataPath's direct fields can leave the
// transmitter. Nil rules and unrecognized shapes remain unclassified.
type JSONViewRule struct {
	StatusPath  []string
	NormalValue string
	ErrorValues []string
	DataPath    []string
	Markers     []idor.Marker
}

// Config is trusted server configuration. NewHTTP copies all mutable fields.
// ConnectIP is an operator-approved binding; the transmitter never resolves DNS.
type Config struct {
	Scope          Scope
	Origin         string
	ConnectIP      netip.Addr
	ExpiresAt      time.Time
	Sessions       []Session
	Resources      []Resource
	MaxRequests    uint64
	MinInterval    time.Duration
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	RootCAs        *x509.CertPool `json:"-"`
	Recorder       Recorder       `json:"-"`
	Health         *HealthConfig  `json:"-"`
}

// Recorder must acknowledge durable intent before a request can be admitted.
// Implementations must honor ctx, be concurrency safe, and never call Send back
// on the same transmitter. Stop may be called from Record. A DB/seal adapter is
// intentionally outside this package; a successful no-op sink is not auditing.
type Recorder interface {
	Record(ctx context.Context, event Event) error
}

type Reason string

const (
	Completed           Reason = "completed"
	Stopped             Reason = "stopped"
	Canceled            Reason = "canceled"
	UnsupportedAction   Reason = "unsupported_action"
	UnknownResource     Reason = "unknown_resource"
	UnknownAccount      Reason = "unknown_account"
	UnverifiedAccount   Reason = "unverified_account"
	ExpiredAccount      Reason = "expired_account"
	AccountNotApproved  Reason = "account_not_approved"
	ExpiredApproval     Reason = "expired_approval"
	BudgetExhausted     Reason = "budget_exhausted"
	AuditUnavailable    Reason = "audit_unavailable"
	TransportError      Reason = "transport_error"
	TimedOut            Reason = "timed_out"
	RedirectRefused     Reason = "redirect_refused"
	ResponseTooLarge    Reason = "response_too_large"
	UnsupportedResponse Reason = "unsupported_response"
	InvalidCandidate    Reason = "invalid_candidate"
	CanaryMismatch      Reason = "canary_mismatch"
)

// Receipt deliberately contains no URLs, request inputs, credentials, bodies or
// raw errors. Admitted means reserved for Do; it does not prove delivery. A false
// RequestWritten does not prove zero bytes were sent (a write may be partial).
type Receipt struct {
	Scope            Scope
	Attempt          uint64
	AccountRef       string
	SessionRef       string
	ResourceRef      string
	ApprovalRef      string
	Method           string
	Reason           Reason
	Admitted         bool
	AdmittedAt       time.Time
	RequestWritten   bool
	ResponseReceived bool
	StatusCode       int
	At               time.Time
}

// Event.Stage is intent or result. An intent is not a transmission record.
type Event struct {
	Stage string
	Receipt
}

type binding struct {
	resource Resource
	url      *url.URL
	accounts map[string]bool
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var headerPattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func invalid(field string) error { return errors.New("transmit: invalid " + field) }

func authority(u *url.URL) (string, error) {
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return "", invalid("URL")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "%\\") {
		return "", invalid("host")
	}
	if _, err := netip.ParseAddr(host); err != nil {
		for _, c := range host {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
				return "", invalid("host")
			}
		}
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", invalid("port")
	}
	return net.JoinHostPort(host, strconv.Itoa(p)), nil
}

func copyRule(rule *JSONViewRule) (*JSONViewRule, error) {
	if rule == nil {
		return nil, nil
	}
	if len(rule.StatusPath) == 0 || rule.NormalValue == "" || len(rule.Markers) == 0 {
		return nil, invalid("view rule")
	}
	for _, path := range [][]string{rule.StatusPath, rule.DataPath} {
		if len(path) > maxJSONDepth {
			return nil, invalid("view path")
		}
		for _, part := range path {
			if part == "" {
				return nil, invalid("view path")
			}
		}
	}
	for _, value := range rule.ErrorValues {
		if value == rule.NormalValue || value == "" {
			return nil, invalid("view status")
		}
	}
	fields := make(map[string]bool, len(rule.Markers))
	for _, marker := range rule.Markers {
		if marker.Field == "" || marker.Value == "" || fields[marker.Field] {
			return nil, invalid("view marker")
		}
		fields[marker.Field] = true
	}
	return &JSONViewRule{
		StatusPath: append([]string(nil), rule.StatusPath...), NormalValue: rule.NormalValue,
		ErrorValues: append([]string(nil), rule.ErrorValues...), DataPath: append([]string(nil), rule.DataPath...),
		Markers: append([]idor.Marker(nil), rule.Markers...),
	}, nil
}

func copySessions(sessions []Session) (map[string]Session, error) {
	out := make(map[string]Session, len(sessions))
	handles := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		a := session.Account
		if !refPattern.MatchString(a.Label) || !refPattern.MatchString(a.Handle) || session.ExpiresAt.IsZero() {
			return nil, invalid("session")
		}
		if _, exists := out[a.Label]; exists {
			return nil, invalid("duplicate account")
		}
		if handles[a.Handle] {
			return nil, invalid("duplicate session")
		}
		handles[a.Handle] = true
		headers := make(http.Header, len(session.Headers))
		for key, values := range session.Headers {
			if !headerPattern.MatchString(key) {
				return nil, invalid("session header")
			}
			canonical := http.CanonicalHeaderKey(key)
			switch canonical {
			case "Host", "Connection", "Proxy-Connection", "Proxy-Authorization", "Transfer-Encoding", "Content-Length", "Accept-Encoding", "Trailer", "Upgrade", "Te":
				return nil, invalid("session header")
			}
			for _, value := range values {
				for _, c := range value {
					if c == 127 || c < 32 && c != '\t' {
						return nil, invalid("session header")
					}
				}
				headers[canonical] = append(headers[canonical], value)
			}
		}
		session.Headers = headers
		out[a.Label] = session
	}
	return out, nil
}

func checkAccount(req idor.AccessRequest, session Session, now time.Time) Reason {
	if req.As.Handle != session.Account.Handle || req.As.Status != session.Account.Status || session.Account.Status != oracle.SubjectVerified {
		return UnverifiedAccount
	}
	if !now.Before(session.ExpiresAt) {
		return ExpiredAccount
	}
	return ""
}

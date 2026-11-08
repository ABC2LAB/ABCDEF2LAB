// Package netboundary owns the registered-read send seam of the E② network
// boundary. It does not isolate tools or intercept their other network paths.
package netboundary

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"net/url"
	"reflect"
	"sync"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

var ErrConfig = errors.New("netboundary: invalid configuration")

// Authority errors are deliberately fixed; callers must not publish a raw
// authority error, which may contain a credential or request data.
var (
	ErrRevoked          = errors.New("netboundary: approval revoked")
	ErrExpired          = errors.New("netboundary: approval expired")
	ErrBindingMismatch  = errors.New("netboundary: binding mismatch")
	ErrAuditUnavailable = errors.New("netboundary: audit unavailable")
)

// Check contains only trusted, host-registered references. ResourceDigest
// binds the exact URL, including path and query, without exposing it in a
// receipt. Ledger and Recorder let the host compare actual instance identity,
// rather than accepting a new ledger with the same key or an unrelated journal.
// The host must compare all fields with its protected current registration.
type Check struct {
	Scope          transmit.Scope
	Account        idor.AccountRef
	ResourceRef    string
	ApprovalRef    string
	ResourceDigest [32]byte
	Origin         string
	ConnectIP      netip.Addr
	BudgetKey      budget.Key
	BudgetBinding  budget.Binding
	Ledger         *budget.Ledger    `json:"-"`
	Recorder       transmit.Recorder `json:"-"`
	PlainHTTP      bool
}

// Permit holds the host's revocation barrier through the entire send and
// result acknowledgement. A revocation must wait for all older permits to be
// released before reporting completion.
type Permit interface{ Release() }

// Authority is implemented by a trusted host, never from model input. Acquire
// must check the current task/run/reset/policy, account/session, exact resource,
// approval, expiry, revocation, the shared Ledger instance, the exact
// Publisher instance, and its independent current audit head. It must honor
// ctx while waiting and return a fresh, single-use Permit.
// A local callback or an old receipt is not an authority implementation.
type Authority interface {
	Acquire(context.Context, Check) (Permit, error)
}

// Config is built only from protected host inputs. Publisher must already be
// open for Transmitter.Scope; Open never manufactures a journal, key or grant.
type Config struct {
	Transmitter transmit.Config
	Publisher   *auditpublisher.Publisher
	Authority   Authority
	AllowHTTP   bool
}

type State string

const (
	BlockedPreSend       State = "blocked_pre_send"
	SendPartialOrUnknown State = "send_partial_or_unknown"
	ResponseObserved     State = "response_observed"
	AuditUnknown         State = "audit_unknown"
)

type Reason string

const (
	UnsupportedPath     Reason = "unsupported_path"
	ApprovalUnavailable Reason = "approval_unavailable"
	Revoked             Reason = "revoked"
	Expired             Reason = "expired"
	BindingMismatch     Reason = "binding_mismatch"
	BudgetUnavailable   Reason = "budget_unavailable"
	BudgetExhausted     Reason = "budget_exhausted"
	TimedOut            Reason = "timed_out"
	AuditUnavailable    Reason = "audit_unavailable"
	TransportUnknown    Reason = "transport_unknown"
)

// Outcome preserves admission and transmission uncertainty. Captured is a
// bounded comparator observation, not an access decision or delivery proof.
type Outcome struct {
	State    State
	Reason   Reason
	Receipt  transmit.Receipt
	Capture  idor.Capture `json:"-"`
	Captured bool
}

type registeredResource struct {
	ref      string
	approval string
	digest   [32]byte
}

// Boundary owns one run's transmitter. A separate host/B implementation must
// still isolate tool processes and prove that their direct sockets are blocked.
type Boundary struct {
	tx        *transmit.HTTPTransmitter
	authority Authority
	ctx       context.Context
	cancel    context.CancelFunc
	scope     transmit.Scope
	origin    string
	connectIP netip.Addr
	budgetKey budget.Key
	binding   budget.Binding
	ledger    *budget.Ledger
	recorder  transmit.Recorder
	plainHTTP bool
	accounts  map[string]idor.AccountRef
	resources map[string]registeredResource

	mu      sync.Mutex
	closed  bool
	failed  bool
	pending sync.WaitGroup
}

// Open requires the real Publisher. Its last ACK only validates initial scope;
// Authority must still read the independent current head on each Acquire.
func Open(ctx context.Context, cfg Config) (*Boundary, error) {
	if cfg.Publisher == nil || !isPresent(cfg.Authority) || cfg.Transmitter.Recorder != nil {
		return nil, ErrConfig
	}
	head, err := cfg.Publisher.AcknowledgedHead()
	if err != nil || head.Phase != auditcheckpoint.Open || head.Checkpoint.Scope != cfg.Transmitter.Scope {
		return nil, ErrConfig
	}
	return open(ctx, cfg.Transmitter, cfg.Publisher, cfg.Authority, cfg.AllowHTTP)
}

// open permits focused synthetic tests with a recorder; production calls Open.
func open(ctx context.Context, cfg transmit.Config, recorder transmit.Recorder, authority Authority, allowHTTP bool) (*Boundary, error) {
	if ctx == nil || ctx.Err() != nil || !isPresent(recorder) || !isPresent(authority) ||
		cfg.CumulativeBudget == nil || cfg.CumulativeBudget.Ledger == nil {
		return nil, ErrConfig
	}
	origin, err := url.Parse(cfg.Origin)
	if err != nil || (origin.Scheme != "https" && (origin.Scheme != "http" || !allowHTTP)) {
		return nil, ErrConfig
	}
	cfg.Recorder = recorder
	runCtx, cancel := context.WithCancel(ctx)
	tx, err := transmit.NewHTTP(runCtx, cfg)
	if err != nil {
		cancel()
		return nil, ErrConfig
	}
	b := &Boundary{
		tx: tx, authority: authority, ctx: runCtx, cancel: cancel,
		scope: cfg.Scope, origin: cfg.Origin, connectIP: cfg.ConnectIP,
		budgetKey: cfg.CumulativeBudget.Key, binding: cfg.CumulativeBudget.Binding,
		ledger: cfg.CumulativeBudget.Ledger, recorder: recorder,
		plainHTTP: origin.Scheme == "http",
		accounts:  make(map[string]idor.AccountRef, len(cfg.Sessions)),
		resources: make(map[string]registeredResource, len(cfg.Resources)),
	}
	for _, session := range cfg.Sessions {
		b.accounts[session.Account.Label] = session.Account
	}
	for _, resource := range cfg.Resources {
		b.resources[resource.Ref] = registeredResource{
			ref: resource.Ref, approval: resource.ApprovalRef,
			digest: sha256.Sum256([]byte(resource.URL)),
		}
	}
	return b, nil
}

// SendRegisteredRead accepts references only. It rebuilds the GET from the
// immutable registry; method, URL, headers and body are never caller inputs.
func (b *Boundary) SendRegisteredRead(accountRef, resourceRef string) (out Outcome) {
	out.State, out.Reason = BlockedPreSend, ApprovalUnavailable
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed || b.failed {
		out.Reason = Revoked
		if b.failed {
			out.State, out.Reason = AuditUnknown, AuditUnavailable
		}
		b.mu.Unlock()
		return
	}
	b.pending.Add(1)
	b.mu.Unlock()
	defer b.pending.Done()

	account, accountOK := b.accounts[accountRef]
	resource, resourceOK := b.resources[resourceRef]
	if !accountOK {
		out.Reason = BindingMismatch
		return
	}
	if !resourceOK {
		out.Reason = UnsupportedPath
		return
	}
	request := idor.AccessRequest{As: account, Method: "GET", ResourceRef: resource.ref}
	if b.tx.Preview(request).Status != transmit.PreviewRegistrationMatch {
		out.Reason = BindingMismatch
		return
	}
	check := Check{
		Scope: b.scope, Account: account, ResourceRef: resource.ref,
		ApprovalRef: resource.approval, ResourceDigest: resource.digest,
		Origin: b.origin, ConnectIP: b.connectIP, BudgetKey: b.budgetKey,
		BudgetBinding: b.binding, Ledger: b.ledger, Recorder: b.recorder,
		PlainHTTP: b.plainHTTP,
	}
	permit, err := b.authority.Acquire(b.ctx, check)
	if err != nil {
		if isPresent(permit) {
			permit.Release()
		}
		out.Reason = authorityReason(err)
		if errors.Is(err, ErrAuditUnavailable) {
			out.State = AuditUnknown
			b.failAudit()
		}
		return
	}
	if !isPresent(permit) {
		out.State, out.Reason = AuditUnknown, AuditUnavailable
		b.failAudit()
		return
	}
	defer permit.Release()
	b.mu.Lock()
	blocked := b.closed || b.failed || b.ctx.Err() != nil
	b.mu.Unlock()
	if blocked {
		out.Reason = Revoked
		return
	}

	out.Capture, out.Receipt, out.Captured = b.tx.SendDetailed(request)
	if out.Receipt.Reason == transmit.AuditUnavailable {
		out.State, out.Reason = AuditUnknown, AuditUnavailable
		out.Capture, out.Captured = idor.Capture{}, false
		b.failAudit()
		return
	}
	if !out.Receipt.Admitted {
		out.State, out.Reason = BlockedPreSend, transmitReason(out.Receipt.Reason)
		return
	}
	if out.Receipt.ResponseReceived {
		out.State = ResponseObserved
		out.Reason = ""
		return
	}
	out.State, out.Reason = SendPartialOrUnknown, TransportUnknown
	return
}

// CloseAdmission stops this run's new sends and cancels its in-flight requests.
// It does not close a B-owned socket, tool process or Publisher.
func (b *Boundary) CloseAdmission() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	b.tx.Stop()
}

// StopAndWait drains A-owned sends and result records. The caller must handle
// B/C process drain and Publisher Seal/Abort separately.
func (b *Boundary) StopAndWait() {
	if b == nil {
		return
	}
	b.CloseAdmission()
	b.tx.StopAndWait()
	b.pending.Wait()
}

func (b *Boundary) failAudit() {
	b.mu.Lock()
	b.failed = true
	b.mu.Unlock()
	b.cancel()
	b.tx.Stop()
}

func authorityReason(err error) Reason {
	switch {
	case errors.Is(err, ErrRevoked):
		return Revoked
	case errors.Is(err, ErrExpired):
		return Expired
	case errors.Is(err, ErrBindingMismatch):
		return BindingMismatch
	case errors.Is(err, ErrAuditUnavailable):
		return AuditUnavailable
	default:
		return ApprovalUnavailable
	}
}

func transmitReason(reason transmit.Reason) Reason {
	switch reason {
	case transmit.BudgetUnavailable:
		return BudgetUnavailable
	case transmit.BudgetExhausted:
		return BudgetExhausted
	case transmit.TimedOut:
		return TimedOut
	case transmit.ExpiredApproval, transmit.ExpiredAccount:
		return Expired
	case transmit.AuditUnavailable:
		return AuditUnavailable
	case transmit.Stopped, transmit.Canceled:
		return Revoked
	case transmit.UnsupportedAction, transmit.UnknownResource:
		return UnsupportedPath
	default:
		return BindingMismatch
	}
}

func isPresent(value any) bool {
	if value == nil {
		return false
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !v.IsNil()
	default:
		return true
	}
}

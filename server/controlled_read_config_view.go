package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/transmit"
)

// ControlledReadConfigView carries metadata only, never a transmission Config.
// A copy is not an execution capability or proof of identity/approval truth.
type ControlledReadConfigView struct {
	Scope     transmit.Scope
	ExpiresAt time.Time
	Sessions  []ControlledReadSessionView
	Resources []ControlledReadResourceView
}
type ControlledReadSessionView struct {
	Account   idor.AccountRef
	ExpiresAt time.Time
}
type ControlledReadResourceView struct {
	Ref, ApprovalRef string
	ReadOnly         bool
	Accounts         []string
}

// ControlledReadConfigHandle can only be minted inside the runtime package.
// Zero values/JSON cannot manufacture its private state. A pointer remains
// comparable when later carried by ControlledReadVerificationRequest.
type ControlledReadConfigHandle struct{ state *controlledReadConfigState }
type controlledReadConfigState struct {
	mu           sync.Mutex
	life         context.Context
	task, intent int64
	until        time.Time
	view         ControlledReadConfigView
	selected     bool
	request      ControlledReadVerificationRequest
	closed       bool
	fixture      *kgfixture.Snapshot
}

// Formatting must not inspect the lifecycle context's private values.
func (ControlledReadConfigHandle) Format(s fmt.State, _ rune) {
	fmt.Fprint(s, "controlled_read_config_handle")
}
func (ControlledReadConfigHandle) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// Runtime integration must call this with the same privately held, immutable
// cfg passed to NewHTTP, after assigning the actual execution Scope. It does
// not load/validate configuration or authenticate its producer. The runtime
// installs the handle only after the transmitter and task binding succeed.
func newControlledReadConfigHandle(life context.Context, task, intent int64, cfg transmit.Config) (*ControlledReadConfigHandle, func(), error) {
	if life == nil || life.Err() != nil || task <= 0 || intent <= 0 || cfg.Scope.TaskRef != strconv.FormatInt(task, 10) || cfg.Scope.RunRef == "" || len(cfg.Sessions) == 0 || cfg.ExpiresAt.IsZero() {
		return nil, nil, ErrControlledFindingUnavailable
	}
	v := ControlledReadConfigView{Scope: cfg.Scope, ExpiresAt: cfg.ExpiresAt}
	until := cfg.ExpiresAt
	for _, s := range cfg.Sessions {
		if s.ExpiresAt.IsZero() {
			return nil, nil, ErrControlledFindingUnavailable
		}
		v.Sessions = append(v.Sessions, ControlledReadSessionView{s.Account, s.ExpiresAt})
		if s.ExpiresAt.Before(until) {
			until = s.ExpiresAt
		}
	}
	for _, r := range cfg.Resources {
		v.Resources = append(v.Resources, ControlledReadResourceView{r.Ref, r.ApprovalRef, r.ReadOnly, append([]string(nil), r.Accounts...)})
	}
	if !time.Now().Before(until) {
		return nil, nil, ErrControlledFindingUnavailable
	}
	s := &controlledReadConfigState{life: life, task: task, intent: intent, until: until, view: v}
	h := &ControlledReadConfigHandle{state: s}
	close := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		s.life = nil
		s.view = ControlledReadConfigView{}
	}
	return h, close, nil
}

func cloneControlledReadConfigView(v ControlledReadConfigView) ControlledReadConfigView {
	v.Sessions = append([]ControlledReadSessionView(nil), v.Sessions...)
	v.Resources = append([]ControlledReadResourceView(nil), v.Resources...)
	for i := range v.Resources {
		v.Resources[i].Accounts = append([]string(nil), v.Resources[i].Accounts...)
	}
	return v
}

// View begins one metadata query. The first valid request pins the one case
// selected by verification; other task/intent/scope/account/resource requests
// are refused. Close/cancellation/earliest registered expiry deny new queries.
// A successful View (including ConfigView) pins the request even if a later
// Bind is refused. An already returned copy is not a live authority.
func (h *ControlledReadConfigHandle) View(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadConfigView, error) {
	if h == nil || h.state == nil || ctx == nil || ctx.Err() != nil {
		return ControlledReadConfigView{}, ErrControlledFindingUnavailable
	}
	s := h.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.life == nil || s.life.Err() != nil || !time.Now().Before(s.until) || r.TaskID != s.task || r.IntentID != s.intent || r.Scope != s.view.Scope || s.selected && r != s.request {
		return ControlledReadConfigView{}, ErrControlledFindingUnavailable
	}
	account, resource := false, false
	for _, v := range s.view.Sessions {
		account = account || v.Account.Label == r.AccountRef
	}
	for _, v := range s.view.Resources {
		resource = resource || v.Ref == r.ResourceRef
	}
	if !account || !resource || ctx.Err() != nil {
		return ControlledReadConfigView{}, ErrControlledFindingUnavailable
	}
	s.selected, s.request = true, r
	return cloneControlledReadConfigView(s.view), nil
}

// Bind uses only the handle's fresh private metadata projection with #165.
// It exposes no Config conversion API and performs no KG/Source query. C must
// obtain this handle from the actual runtime, not mint one from a caller DTO.
func (h *ControlledReadConfigHandle) Bind(ctx context.Context, r ControlledReadVerificationRequest, expected evidencebind.Context, kg idor.PolicyResolver, source e3record.SourceReader) (ControlledReadFindingPlan, error) {
	v, err := h.View(ctx, r)
	if err != nil {
		return ControlledReadFindingPlan{}, err
	}
	cfg := transmit.Config{Scope: v.Scope, ExpiresAt: v.ExpiresAt}
	for _, s := range v.Sessions {
		cfg.Sessions = append(cfg.Sessions, transmit.Session{Account: s.Account, ExpiresAt: s.ExpiresAt})
	}
	for _, r := range v.Resources {
		cfg.Resources = append(cfg.Resources, transmit.Resource{Ref: r.Ref, ApprovalRef: r.ApprovalRef, ReadOnly: r.ReadOnly, Accounts: r.Accounts})
	}
	plan, err := BindControlledReadRealInputs(ctx, r, expected, cfg, kg, source)
	if err != nil {
		return ControlledReadFindingPlan{}, err
	}
	if ctx.Err() != nil {
		return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
	}
	// View's copy may outlive the runtime while the pure adapter runs. The
	// final locked check orders a successful Bind before close/revocation.
	s := h.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.life == nil || s.life.Err() != nil || !time.Now().Before(s.until) {
		return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
	}
	if s.fixture != nil && !sameControlledReadFixture(kg, s.fixture) {
		return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
	}
	return plan, nil
}

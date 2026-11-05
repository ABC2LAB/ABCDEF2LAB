package netboundary

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

type syntheticRecorder struct {
	mu        sync.Mutex
	events    []transmit.Event
	failStage string
}

func (r *syntheticRecorder) Record(_ context.Context, event transmit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failStage == event.Stage {
		return errors.New("private-error-data")
	}
	r.events = append(r.events, event)
	return nil
}

func (r *syntheticRecorder) count(stage string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Stage == stage {
			count++
		}
	}
	return count
}

type syntheticAuthority struct {
	token       chan struct{}
	mu          sync.Mutex
	calls       int
	checks      []Check
	err         error
	permitOnErr bool
	releases    atomic.Int32
	revoked     bool
	nilPermit   bool
	expected    Check
	strict      bool
	headOK      bool
}

func newAuthority() *syntheticAuthority {
	a := &syntheticAuthority{token: make(chan struct{}, 1), strict: true, headOK: true}
	a.token <- struct{}{}
	return a
}

func (a *syntheticAuthority) Acquire(ctx context.Context, check Check) (Permit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.token:
	}
	a.mu.Lock()
	a.calls++
	a.checks = append(a.checks, check)
	err := a.err
	nilPermit := a.nilPermit
	if a.revoked {
		err = ErrRevoked
	} else if a.strict && check != a.expected {
		err = ErrBindingMismatch
	} else if !a.headOK {
		err = ErrAuditUnavailable
	}
	a.mu.Unlock()
	if err != nil {
		if a.permitOnErr {
			return &syntheticPermit{release: func() { a.releases.Add(1); a.token <- struct{}{} }}, err
		}
		a.token <- struct{}{}
		return nil, err
	}
	if nilPermit {
		a.token <- struct{}{}
		return nil, nil
	}
	return &syntheticPermit{release: func() { a.token <- struct{}{} }}, nil
}

func (a *syntheticAuthority) revoke() {
	<-a.token
	a.mu.Lock()
	a.revoked = true
	a.mu.Unlock()
	a.token <- struct{}{}
}

type syntheticPermit struct {
	once    sync.Once
	release func()
}

func (p *syntheticPermit) Release() { p.once.Do(p.release) }

func setup(t *testing.T, target string, rootCAs *x509.CertPool, maxRequests uint64, recorder *syntheticRecorder, authority *syntheticAuthority, allowHTTP bool) (*Boundary, *budget.Ledger) {
	return setupAt(t, target, target, rootCAs, maxRequests, recorder, authority, allowHTTP)
}

func setupAt(t *testing.T, target, registeredOrigin string, rootCAs *x509.CertPool, maxRequests uint64, recorder *syntheticRecorder, authority *syntheticAuthority, allowHTTP bool) (*Boundary, *budget.Ledger) {
	t.Helper()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 5})
	if err != nil {
		t.Fatal(err)
	}
	key := budget.Key{TaskRef: "task:1", BudgetRef: "budget:1"}
	binding := budget.Binding{PolicyRef: "policy:1", ResetRef: "reset:1"}
	if err := ledger.Register(budget.Grant{
		Key: key, Binding: binding, MaxRequests: maxRequests,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
	}, now); err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://"))
	if err != nil {
		t.Fatal(err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		t.Fatal(err)
	}
	cfg := transmit.Config{
		Scope:  transmit.Scope{RunRef: "run:1", TaskRef: key.TaskRef, CaseRef: "case:1", ResetRef: binding.ResetRef, PolicyRef: binding.PolicyRef},
		Origin: registeredOrigin, ConnectIP: ip, ExpiresAt: now.Add(time.Hour),
		Sessions:    []transmit.Session{{Account: idor.AccountRef{Label: "account:1", Handle: "session:1", Status: oracle.SubjectVerified}, ExpiresAt: now.Add(time.Hour)}},
		Resources:   []transmit.Resource{{Ref: "item:1", URL: registeredOrigin + "/read?fixed=1", ApprovalRef: "approval:1", ReadOnly: true, Accounts: []string{"account:1"}}},
		MaxRequests: 5, RequestTimeout: 2 * time.Second, MaxBodyBytes: 1024,
		RootCAs:          rootCAs,
		CumulativeBudget: &transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding},
	}
	authority.mu.Lock()
	authority.expected = Check{
		Scope: cfg.Scope, Account: cfg.Sessions[0].Account,
		ResourceRef: cfg.Resources[0].Ref, ApprovalRef: cfg.Resources[0].ApprovalRef,
		ResourceDigest: sha256.Sum256([]byte(cfg.Resources[0].URL)),
		Origin:         cfg.Origin, ConnectIP: cfg.ConnectIP,
		BudgetKey: key, BudgetBinding: binding, Ledger: ledger, Recorder: recorder,
		PlainHTTP: strings.HasPrefix(cfg.Origin, "http://"),
	}
	authority.mu.Unlock()
	b, err := open(context.Background(), cfg, recorder, authority, allowHTTP)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.StopAndWait)
	return b, ledger
}

func TestRegisteredHTTPGetUsesCurrentGateBudgetAndAudit(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/read?fixed=1" {
			t.Errorf("unexpected registered request: %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	recorder, authority := &syntheticRecorder{}, newAuthority()
	b, ledger := setup(t, target.URL, nil, 2, recorder, authority, true)
	out := b.SendRegisteredRead("account:1", "item:1")
	if out.State != ResponseObserved || out.Reason != "" || !out.Receipt.Admitted || !out.Receipt.ResponseReceived || !out.Captured ||
		received.Load() != 1 || recorder.count("intent") != 1 || recorder.count("result") != 1 {
		t.Fatalf("unexpected outcome: %+v, received=%d", out, received.Load())
	}
	authority.mu.Lock()
	if authority.calls != 1 || len(authority.checks) != 1 || authority.checks[0].Scope != out.Receipt.Scope ||
		authority.checks[0].BudgetKey.BudgetRef != "budget:1" || authority.checks[0].ApprovalRef != "approval:1" ||
		authority.checks[0].Ledger != ledger || authority.checks[0].Recorder != recorder ||
		!authority.checks[0].PlainHTTP || authority.checks[0].ResourceDigest == ([32]byte{}) {
		t.Errorf("current gate did not receive exact protected binding")
	}
	authority.mu.Unlock()
	remaining, err := ledger.Remaining(b.budgetKey, b.binding, time.Now())
	if err != nil || remaining != 1 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func TestSuccessfulRedirectClearsApprovalReason(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.URL.Path == "/read" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	b, _ := setup(t, target.URL, nil, 2, &syntheticRecorder{}, newAuthority(), true)
	out := b.SendRegisteredRead("account:1", "item:1")
	if out.State != ResponseObserved || out.Reason != "" || !out.Receipt.Admitted || !out.Receipt.ResponseReceived || received.Load() != 1 {
		t.Fatalf("redirect success retained a failure reason: %+v received=%d", out, received.Load())
	}
}

func TestAuthorityPermitErrorReleasesPermit(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	authority := newAuthority()
	authority.err = errors.New("authority unavailable")
	authority.permitOnErr = true
	b, _ := setup(t, target.URL, nil, 2, &syntheticRecorder{}, authority, true)
	out := b.SendRegisteredRead("account:1", "item:1")
	if out.Receipt.Admitted || out.State != BlockedPreSend || received.Load() != 0 || authority.releases.Load() != 1 {
		t.Fatalf("permit returned with error was not released: %+v receives=%d releases=%d", out, received.Load(), authority.releases.Load())
	}
}

func TestAuditFailureRemainsTerminalAfterAuthorityRecovers(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	authority := newAuthority()
	authority.headOK = false
	b, _ := setup(t, target.URL, nil, 2, &syntheticRecorder{}, authority, true)
	first := b.SendRegisteredRead("account:1", "item:1")
	authority.mu.Lock()
	authority.headOK = true
	authority.mu.Unlock()
	second := b.SendRegisteredRead("account:1", "item:1")
	if first.State != AuditUnknown || second.State != AuditUnknown || second.Reason != AuditUnavailable || second.Receipt.Admitted || received.Load() != 0 {
		t.Fatalf("audit failure was recoverable: first=%+v second=%+v received=%d", first, second, received.Load())
	}
}

func TestTransmitReasonMappings(t *testing.T) {
	for _, tc := range []struct {
		input transmit.Reason
		want  Reason
	}{
		{transmit.TimedOut, TimedOut},
		{transmit.ExpiredApproval, Expired},
		{transmit.BudgetUnavailable, BudgetUnavailable},
		{transmit.Stopped, Revoked},
	} {
		if got := transmitReason(tc.input); got != tc.want {
			t.Errorf("transmitReason(%q)=%q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFixedIPIgnoresEnvironmentProxy(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if !strings.HasPrefix(r.Host, "approved.test:") {
			t.Errorf("registered authority lost: %s", r.Host)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	registered := strings.Replace(target.URL, "127.0.0.1", "approved.test", 1)
	if registered == target.URL {
		t.Fatal("test target did not use loopback IPv4")
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	b, _ := setupAt(t, target.URL, registered, nil, 2, &syntheticRecorder{}, newAuthority(), true)
	if out := b.SendRegisteredRead("account:1", "item:1"); out.State != ResponseObserved || received.Load() != 1 {
		t.Fatalf("fixed-IP direct HTTP failed: %+v, received=%d", out, received.Load())
	}
}

func TestHTTPSCertificateValidationAndNoFallback(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	b, _ := setup(t, target.URL, roots, 2, &syntheticRecorder{}, newAuthority(), false)
	if out := b.SendRegisteredRead("account:1", "item:1"); out.State != ResponseObserved || received.Load() != 1 {
		t.Fatalf("valid TLS refused: %+v, received=%d", out, received.Load())
	}
	bad, _ := setup(t, target.URL, nil, 2, &syntheticRecorder{}, newAuthority(), false)
	out := bad.SendRegisteredRead("account:1", "item:1")
	if out.State != SendPartialOrUnknown || out.Reason != TransportUnknown || !out.Receipt.Admitted ||
		out.Receipt.ResponseReceived || received.Load() != 1 {
		t.Fatalf("invalid TLS was not contained: %+v, received=%d", out, received.Load())
	}
	wrongName := strings.Replace(target.URL, "127.0.0.1", "wrong-name.test", 1)
	if wrongName == target.URL {
		t.Fatal("test target did not use loopback IPv4")
	}
	mismatch, _ := setupAt(t, target.URL, wrongName, roots, 2, &syntheticRecorder{}, newAuthority(), false)
	out = mismatch.SendRegisteredRead("account:1", "item:1")
	if out.State != SendPartialOrUnknown || out.Receipt.ResponseReceived || received.Load() != 1 {
		t.Fatalf("mismatched TLS name reached application: %+v, received=%d", out, received.Load())
	}
}

func TestUnknownAuthorityAndMissingPermitFailClosed(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	for _, mode := range []string{"unknown", "audit", "missing-permit"} {
		t.Run(mode, func(t *testing.T) {
			authority := newAuthority()
			switch mode {
			case "unknown":
				authority.err = errors.New("secret authority detail")
			case "audit":
				authority.err = ErrAuditUnavailable
			case "missing-permit":
				authority.nilPermit = true
			}
			b, ledger := setup(t, target.URL, nil, 2, &syntheticRecorder{}, authority, true)
			out := b.SendRegisteredRead("account:1", "item:1")
			if out.Receipt.Admitted || received.Load() != 0 {
				t.Fatalf("unverified gate allowed send: %+v", out)
			}
			if mode == "unknown" && (out.State != BlockedPreSend || out.Reason != ApprovalUnavailable) {
				t.Fatalf("unknown gate error: %+v", out)
			}
			if mode != "unknown" && (out.State != AuditUnknown || out.Reason != AuditUnavailable) {
				t.Fatalf("audit/permit uncertainty promoted: %+v", out)
			}
			remaining, err := ledger.Remaining(b.budgetKey, b.binding, time.Now())
			if err != nil || remaining != 2 {
				t.Fatalf("gate failure reserved budget: %d %v", remaining, err)
			}
			encoded, err := json.Marshal(out)
			if err != nil || strings.Contains(string(encoded), "secret authority detail") {
				t.Fatalf("authority detail leaked: %s %v", encoded, err)
			}
		})
	}
}

func TestCurrentBindingAndHeadMismatchFailClosed(t *testing.T) {
	for _, mode := range []string{"scope", "account", "resource-digest", "budget", "recorder", "head"} {
		t.Run(mode, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				received.Add(1)
				_, _ = w.Write([]byte("{}"))
			}))
			defer target.Close()
			recorder, authority := &syntheticRecorder{}, newAuthority()
			b, ledger := setup(t, target.URL, nil, 2, recorder, authority, true)
			authority.mu.Lock()
			switch mode {
			case "scope":
				authority.expected.Scope.RunRef = "run:other"
			case "account":
				authority.expected.Account.Handle = "session:other"
			case "resource-digest":
				authority.expected.ResourceDigest = sha256.Sum256([]byte(target.URL + "/other"))
			case "budget":
				authority.expected.BudgetKey.BudgetRef = "budget:other"
			case "recorder":
				authority.expected.Recorder = &syntheticRecorder{}
			case "head":
				authority.headOK = false
			}
			authority.mu.Unlock()
			out := b.SendRegisteredRead("account:1", "item:1")
			if mode == "head" {
				if out.State != AuditUnknown || out.Reason != AuditUnavailable {
					t.Fatalf("current head failure promoted: %+v", out)
				}
			} else if out.State != BlockedPreSend || out.Reason != BindingMismatch {
				t.Fatalf("binding mismatch admitted: %+v", out)
			}
			remaining, err := ledger.Remaining(b.budgetKey, b.binding, time.Now())
			if err != nil || remaining != 2 || received.Load() != 0 || recorder.count("intent") != 0 {
				t.Fatalf("mismatch passed downstream: remaining=%d receive=%d intent=%d err=%v", remaining, received.Load(), recorder.count("intent"), err)
			}
		})
	}
}

func TestFreshLedgerCannotRechargeExhaustedGrant(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	first, originalLedger := setup(t, target.URL, nil, 1, &syntheticRecorder{}, newAuthority(), true)
	if out := first.SendRegisteredRead("account:1", "item:1"); out.State != ResponseObserved {
		t.Fatalf("initial approved request failed: %+v", out)
	}
	secondAuthority := newAuthority()
	second, freshLedger := setup(t, target.URL, nil, 1, &syntheticRecorder{}, secondAuthority, true)
	secondAuthority.mu.Lock()
	secondAuthority.expected.Ledger = originalLedger // Host retained the original instance.
	secondAuthority.mu.Unlock()
	out := second.SendRegisteredRead("account:1", "item:1")
	if out.State != BlockedPreSend || out.Reason != BindingMismatch || out.Receipt.Admitted || received.Load() != 1 {
		t.Fatalf("fresh ledger recharged the grant: %+v receive=%d", out, received.Load())
	}
	remaining, err := freshLedger.Remaining(second.budgetKey, second.binding, time.Now())
	if err != nil || remaining != 1 {
		t.Fatalf("fresh ledger was reserved before host rejection: %d %v", remaining, err)
	}
}

func TestUnknownReferencesAndPlainHTTPDefaultBlock(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	recorder, authority := &syntheticRecorder{}, newAuthority()
	b, ledger := setup(t, target.URL, nil, 2, recorder, authority, true)
	for _, pair := range [][2]string{{"other", "item:1"}, {"account:1", "other"}} {
		out := b.SendRegisteredRead(pair[0], pair[1])
		if out.State != BlockedPreSend || out.Receipt.Admitted {
			t.Fatalf("unregistered reference admitted: %+v", out)
		}
	}
	if received.Load() != 0 || recorder.count("intent") != 0 || authority.calls != 0 {
		t.Fatalf("unregistered request reached a downstream boundary")
	}
	remaining, err := ledger.Remaining(b.budgetKey, b.binding, time.Now())
	if err != nil || remaining != 2 {
		t.Fatalf("unregistered request reserved budget: %d %v", remaining, err)
	}
	cfg := transmit.Config{
		Scope: b.scope, Origin: target.URL, ConnectIP: b.connectIP,
		ExpiresAt:   time.Now().Add(time.Hour),
		Sessions:    []transmit.Session{{Account: b.accounts["account:1"], ExpiresAt: time.Now().Add(time.Hour)}},
		Resources:   []transmit.Resource{{Ref: "item:1", URL: target.URL + "/read?fixed=1", ApprovalRef: "approval:1", ReadOnly: true, Accounts: []string{"account:1"}}},
		MaxRequests: 2, RequestTimeout: time.Second, MaxBodyBytes: 1024,
		CumulativeBudget: &transmit.CumulativeBudget{Ledger: ledger, Key: b.budgetKey, Binding: b.binding},
	}
	valid, err := open(context.Background(), cfg, recorder, authority, true)
	if err != nil {
		t.Fatalf("control configuration should be valid: %v", err)
	}
	valid.StopAndWait()
	_, err = open(context.Background(), cfg, recorder, authority, false)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("plain HTTP without host approval accepted: %v", err)
	}
}

func TestRevocationAndBudgetExhaustion(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	recorder, authority := &syntheticRecorder{}, newAuthority()
	b, _ := setup(t, target.URL, nil, 1, recorder, authority, true)
	if out := b.SendRegisteredRead("account:1", "item:1"); out.State != ResponseObserved {
		t.Fatalf("first request failed: %+v", out)
	}
	if out := b.SendRegisteredRead("account:1", "item:1"); out.State != BlockedPreSend || out.Reason != BudgetExhausted {
		t.Fatalf("budget exhaustion ignored: %+v", out)
	}
	authority.revoke()
	if out := b.SendRegisteredRead("account:1", "item:1"); out.State != BlockedPreSend || out.Reason != Revoked {
		t.Fatalf("revocation ignored: %+v", out)
	}
	if received.Load() != 1 {
		t.Fatalf("unexpected extra target receive: %d", received.Load())
	}
}

func TestAuditFailureBlocksAndPreservesUncertainty(t *testing.T) {
	for _, stage := range []string{"intent", "result"} {
		t.Run(stage, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				received.Add(1)
				_, _ = w.Write([]byte("{}"))
			}))
			defer target.Close()
			recorder := &syntheticRecorder{failStage: stage}
			b, _ := setup(t, target.URL, nil, 2, recorder, newAuthority(), true)
			out := b.SendRegisteredRead("account:1", "item:1")
			if out.State != AuditUnknown || out.Reason != AuditUnavailable || out.Captured {
				t.Fatalf("audit failure promoted: %+v", out)
			}
			if stage == "intent" && (received.Load() != 0 || out.Receipt.Admitted) {
				t.Fatalf("intent failure sent: %+v", out)
			}
			if stage == "result" && (received.Load() != 1 || !out.Receipt.Admitted) {
				t.Fatalf("result uncertainty lost: %+v", out)
			}
			again := b.SendRegisteredRead("account:1", "item:1")
			if again.State != AuditUnknown || received.Load() > 1 {
				t.Fatalf("audit failure allowed another send: %+v", again)
			}
			encoded, err := json.Marshal(out)
			if err != nil || strings.Contains(string(encoded), "private-error-data") || strings.Contains(string(encoded), "/read?fixed=1") {
				t.Fatalf("private error/request leaked in outcome: %s %v", encoded, err)
			}
		})
	}
}

func TestRevocationWaitsForOlderSend(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	authority := newAuthority()
	b, _ := setup(t, target.URL, nil, 2, &syntheticRecorder{}, authority, true)
	sent := make(chan Outcome, 1)
	go func() { sent <- b.SendRegisteredRead("account:1", "item:1") }()
	<-entered
	revoked := make(chan struct{})
	go func() { authority.revoke(); close(revoked) }()
	select {
	case <-revoked:
		t.Fatal("revocation completed while an older send held its permit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-sent
	<-revoked
	if out := b.SendRegisteredRead("account:1", "item:1"); out.State != BlockedPreSend || out.Reason != Revoked {
		t.Fatalf("post-barrier send admitted: %+v", out)
	}
}

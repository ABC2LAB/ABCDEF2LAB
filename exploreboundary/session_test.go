package exploreboundary

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

// 시험용 승인 대상·결속이다. 실제 대상이 아니라 문서용 이름과 사설 주소를 쓴다.
const (
	fixtureHost      = "target.example"
	fixturePort      = 8443
	fixtureMaxReq    = 3
	fixtureMaxExt    = 2
	fixtureMaxConc   = 2
	fixtureBudgetSec = 60
	fixtureDenied    = 8
)

var (
	fixtureConnect = netip.MustParseAddr("10.20.30.40")
	fixturePublic  = netip.MustParseAddr("8.8.8.8")
	fixtureSPKI    = strings.Repeat("ab", 32)
	fixtureDigest  = strings.Repeat("cd", 32)
)

type fakeSource struct {
	mu     sync.Mutex
	target Target
	err    error
}

func (f *fakeSource) Current(context.Context, string, uint64) (Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target, f.err
}
func (f *fakeSource) set(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

type fakeResolver map[string][]netip.Addr

func (r fakeResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	if v, ok := r[host]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func fixtureBinding(write, comm string) exploration.Binding {
	limits := exploration.Limits{TimeBudgetSeconds: fixtureBudgetSec, MaxRequests: fixtureMaxReq, MaxConcurrency: fixtureMaxConc, MaxResponseBytes: 4096}
	if comm == exploration.ExternalAllowed {
		limits.MaxExternalRequests = fixtureMaxExt
	}
	return exploration.Binding{SessionRef: "session-1", RunRef: "run-1", JobID: "job-1", TaskID: "7", TargetRef: "target-1", TargetRevision: 2, WriteMode: write, CommMode: comm, Limits: limits}
}

func fixtureSession(t *testing.T, write, comm string, alter func(*Target, *Policy)) (*Session, *fakeSource, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	target := Target{Ref: "target-1", Revision: 2, Host: fixtureHost, Port: fixturePort, PathPrefixes: []string{"/app"}, Methods: []string{"GET", "HEAD", "POST", "DELETE"},
		ConnectIP: fixtureConnect, SPKISHA256: fixtureSPKI, ExpiresAt: clock.now.Add(time.Hour), RiskyWords: []string{"logout", "reset"}, ExcludedPrefixes: []string{"/app/admin"}}
	policy := Policy{Resolver: fakeResolver{"cdn.example": {fixturePublic}, "intra.example": {netip.MustParseAddr("192.168.1.5")}, "meta.example": {netip.MustParseAddr("169.254.169.254")},
		"mixed.example": {fixturePublic, netip.MustParseAddr("10.0.0.9")}, "control.example": {netip.MustParseAddr("8.8.4.4")}},
		ControlEndpoints: []netip.AddrPort{netip.MustParseAddrPort("8.8.4.4:443")}, PathPattern: func(p string) string { return "pattern:" + p }, MaxDeniedRecords: fixtureDenied, Now: clock.Now}
	if alter != nil {
		alter(&target, &policy)
	}
	source := &fakeSource{target: target}
	s, err := Open(t.Context(), fixtureBinding(write, comm), fixtureDigest, source, policy)
	if err != nil {
		t.Fatal(err)
	}
	return s, source, clock
}

func approved(method, path string) Request {
	return Request{Ingress: BrowserIngress, Method: method, Scheme: "https", Host: fixtureHost, Port: fixturePort, EscapedPath: path, FetchMode: "cors"}
}

func TestOpenReadsApprovedTargetFromSourceOnly(t *testing.T) {
	clock := time.Now()
	good := Target{Ref: "target-1", Revision: 2, Host: fixtureHost, Port: fixturePort, PathPrefixes: []string{"/app"}, Methods: []string{"GET"}, ConnectIP: fixtureConnect, SPKISHA256: fixtureSPKI, ExpiresAt: clock.Add(time.Hour)}
	policy := Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: 1}
	for name, mutate := range map[string]func(*Target){
		"other_revision": func(t *Target) { t.Revision = 3 },
		"other_ref":      func(t *Target) { t.Ref = "target-2" },
		"expired":        func(t *Target) { t.ExpiresAt = clock.Add(-time.Second) },
		"no_spki":        func(t *Target) { t.SPKISHA256 = "" },
		"no_prefix":      func(t *Target) { t.PathPrefixes = nil },
		"dot_prefix":     func(t *Target) { t.PathPrefixes = []string{"/app/../x"} },
	} {
		target := good
		mutate(&target)
		if _, err := Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal), fixtureDigest, &fakeSource{target: target}, policy); err == nil {
			t.Fatal("invalid approved target opened a session:", name)
		}
	}
	if _, err := Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal), fixtureDigest, &fakeSource{err: ErrTargetEnded}, policy); !errors.Is(err, ErrTargetEnded) {
		t.Fatal("revoked target opened a session")
	}
	if _, err := Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal), "short", &fakeSource{target: good}, policy); err == nil {
		t.Fatal("invalid source digest opened a session")
	}
	if _, err := Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal), fixtureDigest, &fakeSource{target: good}, Policy{}); err == nil {
		t.Fatal("incomplete policy opened a session")
	}
}

func TestApprovedRequestForwardsToPinnedAddressAndRecordsWithoutValues(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	req := approved("GET", "/app/items/42")
	req.HasAuth = true
	d, ticket := s.Admit(t.Context(), req)
	if !d.Forward || d.Dest != DestApproved || d.Dial != netip.AddrPortFrom(fixtureConnect, fixturePort) || d.VerifySPKI != fixtureSPKI || ticket == nil {
		t.Fatalf("approved read was not forwarded to the pinned address: %+v", d)
	}
	ticket.Done(Result{Status: 200, Bytes: 12, ResponseReceived: true, QueryKeys: []string{"q", ""}, ContentType: "application/json"})
	ticket.Done(Result{Status: 500})
	records, omitted := s.Records()
	if len(records) != 1 || omitted != 0 || records[0].Decision != "forwarded" || records[0].Status != 200 || records[0].PathPattern != "pattern:/app/items/42" || !records[0].HasAuthHeader {
		t.Fatalf("forwarded record: %+v", records)
	}
	flow := diagnosisflow.ExplorationBinding{SessionRef: "session-1", RunRef: "run-1", JobID: "job-1", TaskID: "7", TargetRef: "target-1", TargetRevision: 2, SourceDigest: fixtureDigest}
	seqs, err := s.Sequences(flow)
	if err != nil || len(seqs) != 1 {
		t.Fatal("observed sequence missing", seqs, err)
	}
	o, err := s.Reader().ReadObservation(t.Context(), flow, seqs[0])
	if err != nil || o.Path != "/app/items/42" || o.Source != SourceBrowserXHR || o.Status != 200 || len(o.QueryKeys) != 1 || !o.Forwarded {
		t.Fatalf("private observation: %+v %v", o, err)
	}
	other := flow
	other.SourceDigest = strings.Repeat("ef", 32)
	if _, err := s.Reader().ReadObservation(t.Context(), other, seqs[0]); err == nil {
		t.Fatal("private reader accepted another binding")
	}
	if st := s.Status(); st.TargetRequests != 1 || st.ActiveConnections != 0 || !st.Active {
		t.Fatalf("state: %+v", st)
	}
}

func TestApprovedOriginDenials(t *testing.T) {
	for _, tc := range []struct {
		name, write, method, path string
		websocket                 bool
		alter                     func(*Request)
		want                      string
	}{
		{"outside_prefix", exploration.ReadOnly, "GET", "/other", false, nil, ReasonOutOfScopePath},
		{"dot_segment", exploration.ReadOnly, "GET", "/app/../other", false, nil, ReasonOutOfScopePath},
		// 정리하면 승인 경로 안이 되는 비정규 경로도 원문 그대로가 아니면 막는다(대상이 다르게 해석할 수 있다).
		{"dot_inside_scope", exploration.ReadOnly, "GET", "/app/x/../y", false, nil, ReasonOutOfScopePath},
		{"single_dot_inside_scope", exploration.ReadOnly, "GET", "/app/./y", false, nil, ReasonOutOfScopePath},
		{"encoded", exploration.ReadOnly, "GET", "/app/%2e%2e/other", false, nil, ReasonOutOfScopePath},
		{"double_slash", exploration.ReadOnly, "GET", "/app//x", false, nil, ReasonOutOfScopePath},
		{"excluded", exploration.ApprovedWrites, "GET", "/app/admin/users", false, nil, ReasonOutOfScopePath},
		{"risky", exploration.ApprovedWrites, "GET", "/app/password-reset", false, nil, ReasonRiskyPath},
		{"read_only_post", exploration.ReadOnly, "POST", "/app/cart", false, nil, ReasonMethodNotApproved},
		{"delete_even_when_writes", exploration.ApprovedWrites, "DELETE", "/app/cart/1", false, nil, ReasonMethodNotApproved},
		{"unapproved_method", exploration.ApprovedWrites, "PUT", "/app/cart/1", false, nil, ReasonMethodNotApproved},
		{"ws_read_only", exploration.ReadOnly, "GET", "/app/socket", true, nil, ReasonWSReadOnly},
		{"other_port", exploration.ReadOnly, "GET", "/app", false, func(r *Request) { r.Port = 9443 }, ReasonOutOfScopeOrigin},
		{"plain_http", exploration.ReadOnly, "GET", "/app", false, func(r *Request) { r.Scheme = "http" }, ReasonProtocolUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixtureSession(t, tc.write, exploration.FullLocal, nil)
			req := approved(tc.method, tc.path)
			req.WebSocket = tc.websocket
			if tc.alter != nil {
				tc.alter(&req)
			}
			d, ticket := s.Admit(t.Context(), req)
			if d.Forward || ticket != nil || d.Reason != tc.want {
				t.Fatalf("decision=%+v want %s", d, tc.want)
			}
			if st := s.Status(); !st.Active || st.TargetRequests != 0 {
				t.Fatal("one denied request changed the session", st)
			}
		})
	}
	// 양성 대조: 쓰기 승인 회차는 승인 메서드의 쓰기와 WebSocket을 보낸다.
	s, _, _ := fixtureSession(t, exploration.ApprovedWrites, exploration.FullLocal, nil)
	for _, req := range []Request{approved("POST", "/app/cart"), func() Request { r := approved("GET", "/app/socket"); r.WebSocket = true; return r }()} {
		d, ticket := s.Admit(t.Context(), req)
		if !d.Forward {
			t.Fatalf("approved write was refused: %+v", d)
		}
		ticket.Done(Result{Status: 200})
	}
}

func TestTargetRequestCapClosesSessionButConcurrencyOnlyRefuses(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	var open []*Ticket
	for i := 0; i < fixtureMaxConc; i++ {
		d, ticket := s.Admit(t.Context(), approved("GET", "/app/a"))
		if !d.Forward {
			t.Fatal("request under limits refused", d)
		}
		open = append(open, ticket)
	}
	if d, _ := s.Admit(t.Context(), approved("GET", "/app/a")); d.Forward || d.Reason != ReasonCapExhausted || !s.Status().Active {
		t.Fatal("concurrency limit closed the session or admitted a request", d)
	}
	for _, ticket := range open {
		ticket.Done(Result{})
	}
	d, ticket := s.Admit(t.Context(), approved("GET", "/app/a"))
	if !d.Forward {
		t.Fatal("slot was not reusable after completion")
	}
	ticket.Done(Result{})
	if d, _ := s.Admit(t.Context(), approved("GET", "/app/a")); d.Forward || d.Reason != ReasonCapExhausted {
		t.Fatal("request cap admitted another request", d)
	}
	st := s.Status()
	if st.Active || st.ClosedReason != exploration.CapExhausted || st.TargetRequests != fixtureMaxReq {
		t.Fatalf("request cap did not close the session: %+v", st)
	}
	if d, _ := s.Admit(t.Context(), approved("GET", "/app/a")); d.Forward || d.Reason != ReasonCapExhausted {
		t.Fatal("closed session admitted a request", d)
	}
}

func TestTimeBudgetRevocationAndSourceFailureStopNewSends(t *testing.T) {
	s, _, clock := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	clock.add(fixtureBudgetSec * time.Second)
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || d.Reason != ReasonCapExhausted || s.Status().ClosedReason != exploration.CapExhausted {
		t.Fatal("time budget did not close the session", d)
	}
	s, source, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	source.set(ErrTargetEnded)
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || d.Reason != ReasonTargetEnded || s.Status().ClosedReason != exploration.TargetEnded {
		t.Fatal("revoked target kept sending", d)
	}
	source.set(nil)
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward {
		t.Fatal("session reopened after revocation")
	}
	s, source, _ = fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	source.set(errors.New("registry unavailable"))
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || s.Status().ClosedReason != exploration.BoundaryUnavailable {
		t.Fatal("registry failure did not stop new sends", d)
	}
	s, source, _ = fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	source.mu.Lock()
	source.target.ConnectIP = netip.MustParseAddr("10.20.30.41")
	source.mu.Unlock()
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || s.Status().ClosedReason != exploration.BoundaryUnavailable {
		t.Fatal("changed approved address under the same revision kept sending", d)
	}
}

func TestExternalRequestsFollowModeIngressMethodAndAddress(t *testing.T) {
	external := func(host string) Request {
		return Request{Ingress: BrowserIngress, Method: "GET", Scheme: "https", Host: host, Port: 443, EscapedPath: "/lib.js", FetchMode: "no-cors", FetchDest: "script"}
	}
	local, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	if d, _ := local.Admit(t.Context(), external("cdn.example")); d.Forward || d.Reason != ReasonExternalBlockedLocal {
		t.Fatal("full-local mode sent an external request", d)
	}
	for _, tc := range []struct {
		name  string
		alter func(*Request)
		want  string
		dest  DestClass
	}{
		{"direct", func(r *Request) { r.Ingress = DirectIngress }, ReasonExternalDirect, DestExternal},
		{"post", func(r *Request) { r.Method = "POST" }, ReasonExternalMethod, DestExternal},
		{"navigate", func(r *Request) { r.FetchMode = "navigate" }, ReasonExternalNavigation, DestExternal},
		{"websocket", func(r *Request) { r.WebSocket = true }, ReasonProtocolUnsupported, DestExternal},
		{"internal", func(r *Request) { r.Host = "intra.example" }, ReasonInternalAddress, DestInternal},
		{"metadata", func(r *Request) { r.Host = "meta.example" }, ReasonMetadataAddress, DestMetadata},
		{"metadata_literal", func(r *Request) { r.Host = "169.254.169.254" }, ReasonMetadataAddress, DestMetadata},
		{"mixed", func(r *Request) { r.Host = "mixed.example" }, ReasonInternalAddress, DestInternal},
		{"control", func(r *Request) { r.Host = "control.example" }, ReasonControlEndpoint, DestControl},
		{"unresolved", func(r *Request) { r.Host = "nowhere.example" }, ReasonUnresolved, DestExternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, nil)
			req := external("cdn.example")
			tc.alter(&req)
			if d, ticket := s.Admit(t.Context(), req); d.Forward || ticket != nil || d.Reason != tc.want || d.Dest != tc.dest {
				t.Fatalf("decision=%+v want %s/%s", d, tc.want, tc.dest)
			}
		})
	}
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, nil)
	for i := 0; i < fixtureMaxExt; i++ {
		d, ticket := s.Admit(t.Context(), external("cdn.example"))
		if !d.Forward || d.Dest != DestExternal || d.Dial != netip.AddrPortFrom(fixturePublic, 443) || d.VerifySPKI != "" {
			t.Fatalf("external page resource was not forwarded: %+v", d)
		}
		ticket.Done(Result{Status: 200})
	}
	if d, _ := s.Admit(t.Context(), external("cdn.example")); d.Forward || d.Reason != ReasonCapExhausted || !s.Status().Active {
		t.Fatal("external cap admitted a request or closed the session", d)
	}
	records, _ := s.Records()
	for _, r := range records {
		if r.Dest == DestExternal && (r.PathPattern != "" || r.ExternalHost != "cdn.example") {
			t.Fatalf("external record leaked a path or lost its host: %+v", r)
		}
	}
	if seqs, _ := s.Sequences(s.flow); len(seqs) != 0 {
		t.Fatal("external resources became observed target endpoints")
	}
}

func TestApprovedTargetOnControlEndpointIsRefused(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(_ *Target, p *Policy) {
		p.ControlEndpoints = []netip.AddrPort{netip.AddrPortFrom(fixtureConnect, fixturePort)}
	})
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || d.Reason != ReasonControlEndpoint {
		t.Fatal("approved origin pointed at a control endpoint was forwarded", d)
	}
}

func TestDeniedRecordsAreBoundedAndCounted(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	for i := 0; i < fixtureDenied+3; i++ {
		s.Admit(t.Context(), approved("GET", "/other"))
	}
	records, omitted := s.Records()
	if len(records) != fixtureDenied || omitted != 3 {
		t.Fatal("denied record bound", len(records), omitted)
	}
}

// 마감은 진행 중 요청이 끝난 뒤에만 연결 0을 돌려준다. 기다리지 못하면 상태 검증이 실패해 탐색 몫을 모름으로 남긴다(P1-E).
func TestCloseConfirmsOnlyAfterInflightRequestsFinish(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	_, ticket := s.Admit(t.Context(), approved("GET", "/app"))
	short, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	state, err := s.Close(short, exploration.Cancelled)
	if err != nil || state.Active || state.ActiveConnections != 1 || state.Validate(s.binding) == nil {
		t.Fatalf("close with an inflight request was confirmed: %+v", state)
	}
	select {
	case <-s.Closed():
	default:
		t.Fatal("close signal not raised for the network layer")
	}
	if d, _ := s.Admit(t.Context(), approved("GET", "/app")); d.Forward {
		t.Fatal("closing session admitted a request")
	}
	ticket.Done(Result{})
	state, err = s.Close(t.Context(), exploration.Cancelled)
	if err != nil || state.ActiveConnections != 0 || state.Validate(s.binding) != nil || state.TargetRequests != 1 || state.ClosedReason != exploration.Cancelled {
		t.Fatalf("drained close was not confirmed: %+v", state)
	}
	if _, err := s.Reader().Session(t.Context()); err == nil {
		t.Fatal("private reader stayed open after close")
	}
}

func TestConcurrentAdmissionsNeverExceedRequestCap(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(_ *Target, p *Policy) {})
	var wg sync.WaitGroup
	var mu sync.Mutex
	forwarded := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, ticket := s.Admit(context.Background(), approved("GET", "/app"))
			if d.Forward {
				mu.Lock()
				forwarded++
				mu.Unlock()
				ticket.Done(Result{})
			}
		}()
	}
	wg.Wait()
	if forwarded > fixtureMaxReq || s.Status().TargetRequests != uint64(forwarded) {
		t.Fatal("concurrent admissions exceeded the request cap", forwarded, s.Status())
	}
}

// 마감은 진행 중 요청이 끝날 때까지 실제로 기다린 뒤 연결 0을 돌려준다.
func TestCloseWaitsForConcurrentCompletion(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	_, ticket := s.Admit(t.Context(), approved("GET", "/app"))
	go func() {
		<-s.Closed()
		time.Sleep(20 * time.Millisecond)
		ticket.Done(Result{Status: 200})
	}()
	state, err := s.Close(t.Context(), exploration.Completed)
	if err != nil || state.ActiveConnections != 0 || state.Validate(s.binding) != nil {
		t.Fatalf("close returned before the inflight request finished: %+v", state)
	}
}

// 리뷰 재현(#482): 대상 조회·이름 풀이를 기다리는 동안 시간 상한이 지나거나 대상이 철회되면 그 요청을 허용하지 않는다.
type slowSource struct {
	*fakeSource
	during func()
}

func (s slowSource) Current(ctx context.Context, ref string, revision uint64) (Target, error) {
	t, err := s.fakeSource.Current(ctx, ref, revision)
	if s.during != nil {
		s.during()
	}
	return t, err
}

type hookResolver struct {
	fakeResolver
	during func()
}

func (r hookResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if r.during != nil {
		r.during()
	}
	return r.fakeResolver.LookupNetIP(ctx, network, host)
}

func TestAdmissionRechecksTimeAndAuthorityAfterBlockingWaits(t *testing.T) {
	// (1) 대상 조회 중 시간 상한을 넘긴 승인 GET
	s, source, clock := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	armed := false
	s.source = slowSource{fakeSource: source, during: func() {
		if armed {
			clock.add((fixtureBudgetSec + 1) * time.Second)
		}
	}}
	armed = true
	if d, ticket := s.Admit(t.Context(), approved("GET", "/app")); d.Forward || ticket != nil || d.Reason != ReasonCapExhausted || s.Status().Active || s.Status().TargetRequests != 0 {
		t.Fatalf("request admitted after the time budget passed during the target lookup: %+v %+v", d, s.Status())
	}
	// (2) 이름 풀이 중 대상이 철회된 외부 GET
	s, source, _ = fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, nil)
	s.policy.Resolver = hookResolver{fakeResolver: fakeResolver{"cdn.example": {fixturePublic}}, during: func() { source.set(ErrTargetEnded) }}
	ext := Request{Ingress: BrowserIngress, Method: "GET", Scheme: "https", Host: "cdn.example", Port: 443, EscapedPath: "/lib.js", FetchMode: "no-cors"}
	if d, ticket := s.Admit(t.Context(), ext); d.Forward || ticket != nil || d.Reason != ReasonTargetEnded || s.Status().ExternalRequests != 0 {
		t.Fatalf("external request admitted after revocation during DNS: %+v %+v", d, s.Status())
	}
	// (3) 이름 풀이 중 시간 상한이 지난 외부 GET
	s, _, clock = fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, nil)
	s.policy.Resolver = hookResolver{fakeResolver: fakeResolver{"cdn.example": {fixturePublic}}, during: func() { clock.add((fixtureBudgetSec + 1) * time.Second) }}
	if d, ticket := s.Admit(t.Context(), ext); d.Forward || ticket != nil || d.Reason != ReasonCapExhausted {
		t.Fatalf("external request admitted after the time budget passed during DNS: %+v", d)
	}
	// (4) 호출이 취소된 요청
	s, _, _ = fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if d, ticket := s.Admit(canceled, approved("GET", "/app")); d.Forward || ticket != nil {
		t.Fatal("canceled caller was admitted", d)
	}
}

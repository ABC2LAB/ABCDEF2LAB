package exploreboundary

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

// ReasonUnresolved는 경계가 직접 푼 이름에 주소가 없을 때다. 계약 §4 목록에 더한 값이다.
const ReasonUnresolved = "unresolved_host"

// 관찰 출처(§7). 입구와 브라우저 이동 여부로만 정한다. 직접 입구는 헤더와 상관없이 API Recon 실행이다.
const (
	SourceBrowserNavigation = "browser_navigation"
	SourceBrowserXHR        = "browser_xhr"
	SourceBrowserForm       = "browser_form"
	SourceAPIReconRuntime   = "api_recon_runtime"
)

// Record는 요청마다 한 행인 경계 기록이다(§4). 헤더·쿠키·본문·query 값은 넣지 않는다.
// 제품 회차의 기록은 보호 저장소에만 두고 모델 입력·공유 보고서·export에 그대로 넣지 않는다.
type Record struct {
	SessionRef    string    `json:"session_ref"`
	Seq           uint64    `json:"seq"`
	At            time.Time `json:"at"`
	Decision      string    `json:"decision"`
	Reason        string    `json:"reason,omitempty"`
	Ingress       Ingress   `json:"ingress"`
	Dest          DestClass `json:"dest_class"`
	Method        string    `json:"method"`
	Status        int       `json:"status,omitempty"`
	Bytes         int64     `json:"bytes,omitempty"`
	PathPattern   string    `json:"path_pattern,omitempty"`
	ExternalHost  string    `json:"external_host,omitempty"`
	FetchDest     string    `json:"fetch_dest,omitempty"`
	FetchMode     string    `json:"fetch_mode,omitempty"`
	HasAuthHeader bool      `json:"has_auth_header"`
}

// Result는 전달한 요청의 결과 메타데이터다. 값 대신 이름·형식·해시만 담는다.
type Result struct {
	Status              int
	Bytes               int64
	ResponseReceived    bool
	ResponseContentType string
	ResponseSHA256      [32]byte
	QueryKeys           []string
	BodyKeys            []string
	ContentType         string
}

// Session은 한 탐색 회차의 경계 상태다. 서버만 Open·Close하고 실행기는 이 값을 만들거나 넘기지 못한다.
type Session struct {
	policy       Policy
	source       TargetSource
	binding      exploration.Binding
	flow         diagnosisflow.ExplorationBinding
	target       Target
	deadline     time.Time
	mu           sync.Mutex
	active       bool
	reason       exploration.Reason
	closedCh     chan struct{}
	idle         chan struct{}
	targetCount  uint64
	externalSent uint64
	inflight     int
	seq          uint64
	records      []Record
	deniedRows   int
	omitted      uint64
	observations map[uint64]diagnosisflow.ExplorationObservation
}

func validTarget(t Target, b exploration.Binding) bool {
	spki, err := hex.DecodeString(t.SPKISHA256)
	if t.Ref != b.TargetRef || t.Revision != b.TargetRevision || t.Host == "" || t.Host != strings.ToLower(t.Host) || t.Port == 0 ||
		!t.ConnectIP.IsValid() || err != nil || len(spki) != 32 || len(t.PathPrefixes) == 0 || len(t.Methods) == 0 || t.ExpiresAt.IsZero() {
		return false
	}
	for _, p := range append(append([]string{}, t.PathPrefixes...), t.ExcludedPrefixes...) {
		if _, ok := canonicalPath(p); !ok {
			return false
		}
	}
	return true
}

// Open은 서버가 넘긴 결속으로 대상 등록 저장소에서 승인 대상을 직접 읽어 세션을 연다.
func Open(ctx context.Context, b exploration.Binding, sourceDigest string, source TargetSource, p Policy) (*Session, error) {
	digest, err := hex.DecodeString(sourceDigest)
	if ctx == nil || b.Validate() != nil || err != nil || len(digest) != 32 || source == nil || !p.valid() {
		return nil, ErrConfig
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	target, err := source.Current(ctx, b.TargetRef, b.TargetRevision)
	if err != nil {
		return nil, err
	}
	now := p.Now()
	if !validTarget(target, b) || !now.Before(target.ExpiresAt) {
		return nil, ErrConfig
	}
	deadline := now.Add(time.Duration(b.Limits.TimeBudgetSeconds) * time.Second)
	if target.ExpiresAt.Before(deadline) {
		deadline = target.ExpiresAt
	}
	idle := make(chan struct{})
	close(idle)
	return &Session{policy: p, source: source, binding: b, target: target, deadline: deadline, active: true, closedCh: make(chan struct{}), idle: idle,
		flow:         diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: sourceDigest},
		observations: map[uint64]diagnosisflow.ExplorationObservation{}}, nil
}

// Closed는 세션이 닫히면 닫힌다. 네트워크 조립은 이 신호로 진행 중인 연결을 끊는다(§6).
func (s *Session) Closed() <-chan struct{} { return s.closedCh }

func (s *Session) closeLocked(reason exploration.Reason) {
	if !s.active {
		return
	}
	s.active = false
	s.reason = reason
	close(s.closedCh)
}

func (s *Session) stateLocked() exploration.State {
	return exploration.State{Binding: s.binding, Active: s.active, ClosedReason: s.reason, TargetRequests: s.targetCount, ExternalRequests: s.externalSent, ActiveConnections: s.inflight}
}

// Status는 현재 상태다. 닫혔어도 진행 중 연결이 남아 있으면 ActiveConnections가 0이 아니다.
func (s *Session) Status() exploration.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// Close는 새 요청을 막고 진행 중 요청이 끝나기를 ctx 안에서 기다린다. 기다리지 못하면 연결이 남은 상태를 그대로
// 돌려주므로 호출자는 마감을 확정할 수 없다(탐색 몫을 모름으로 남긴다).
func (s *Session) Close(ctx context.Context, reason exploration.Reason) (exploration.State, error) {
	if ctx == nil || !reason.Valid() {
		return exploration.State{}, ErrConfig
	}
	s.mu.Lock()
	s.closeLocked(reason)
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
	}
	return s.Status(), nil
}

// Check는 시간 상한과 대상 철회·만료를 확인해 해당하면 세션을 닫는다. 서버가 주기적으로도 부른다.
func (s *Session) Check(ctx context.Context) error {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return ErrUnavailable
	}
	if !s.policy.Now().Before(s.deadline) {
		s.closeLocked(exploration.CapExhausted)
		s.mu.Unlock()
		return ErrUnavailable
	}
	s.mu.Unlock()
	target, err := s.source.Current(ctx, s.binding.TargetRef, s.binding.TargetRevision)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case errors.Is(err, ErrTargetEnded):
		s.closeLocked(exploration.TargetEnded)
		return ErrTargetEnded
	case err != nil || !validTarget(target, s.binding) || target.ConnectIP != s.target.ConnectIP || target.SPKISHA256 != s.target.SPKISHA256:
		// 저장소 장애나 같은 판인데 값이 다르면 승인 근거를 잃은 것이다. 새 송신을 열지 않는다.
		s.closeLocked(exploration.BoundaryUnavailable)
		return ErrUnavailable
	}
	// 조회 동안 시간이 지났을 수 있다. 조회 뒤 시각으로 다시 본다.
	if !s.active || !s.liveLocked(ctx) {
		return ErrUnavailable
	}
	return nil
}

// liveLocked는 최종 허용 직전의 시간 상한·호출 취소 검사다. 이름 풀이·대상 조회 같은 대기 뒤에도 지금 시각으로 다시 본다.
// 시간 상한이 지났으면 세션을 닫는다. 호출자는 s.mu를 보유한다.
func (s *Session) liveLocked(ctx context.Context) bool {
	if !s.active || ctx.Err() != nil {
		return false
	}
	if !s.policy.Now().Before(s.deadline) {
		s.closeLocked(exploration.CapExhausted)
		return false
	}
	return true
}

// Ticket은 전달을 허용한 요청 하나의 마감 핸들이다. Done은 정확히 한 번만 효과가 있다.
type Ticket struct {
	s        *Session
	seq      uint64
	req      Request
	dest     DestClass
	path     string
	external string
	once     sync.Once
}

func (s *Session) recordLocked(r Record) {
	if r.Decision == "denied" {
		if s.deniedRows >= s.policy.MaxDeniedRecords {
			s.omitted++
			return
		}
		s.deniedRows++
	}
	s.records = append(s.records, r)
}

func (s *Session) denyLocked(req Request, dest DestClass, reason string, external string) Decision {
	s.seq++
	pattern := ""
	if dest == DestApproved {
		pattern = s.policy.PathPattern(req.EscapedPath)
	}
	s.recordLocked(Record{SessionRef: s.binding.SessionRef, Seq: s.seq, At: s.policy.Now().UTC(), Decision: "denied", Reason: reason, Ingress: req.Ingress, Dest: dest, Method: req.Method,
		PathPattern: pattern, ExternalHost: external, FetchDest: req.FetchDest, FetchMode: req.FetchMode, HasAuthHeader: req.HasAuth})
	return Decision{Reason: reason, Dest: dest}
}

// Refuse는 경계 조립이 판정 전에 막은 요청(평문 요청, 구현 전 WebSocket, 요청 본문 상한 초과)을 차단 행으로 남긴다.
func (s *Session) Refuse(req Request, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dest := DestExternal
	if strings.ToLower(strings.TrimSuffix(req.Host, ".")) == s.target.Host && req.Port == s.target.Port {
		dest = DestApproved
	}
	s.denyLocked(req, dest, reason, "")
}

func (s *Session) admitLocked(req Request, dest DestClass, dial netip.AddrPort, spki, canonical, external string) (Decision, *Ticket) {
	s.seq++
	if dest == DestApproved {
		s.targetCount++
	} else {
		s.externalSent++
	}
	if s.inflight == 0 {
		s.idle = make(chan struct{})
	}
	s.inflight++
	return Decision{Forward: true, Dest: dest, Dial: dial, VerifySPKI: spki}, &Ticket{s: s, seq: s.seq, req: req, dest: dest, path: canonical, external: external}
}

// Admit은 요청 하나를 판정한다. 막힌 요청은 그 요청만 실패하고, 세션을 닫는 것은 시간·대상 요청 상한·대상 철회·
// 경계 장애뿐이다(§3·§6). 외부 자원 상한·동시 연결 상한은 그 요청만 막는다.
func (s *Session) Admit(ctx context.Context, req Request) (Decision, *Ticket) {
	if ctx == nil || (req.Ingress != BrowserIngress && req.Ingress != DirectIngress) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.denyLocked(req, DestExternal, ReasonProtocolUnsupported, ""), nil
	}
	host := strings.ToLower(strings.TrimSuffix(req.Host, "."))
	approvedOrigin := host == s.target.Host && req.Port == s.target.Port
	if err := s.Check(ctx); err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		reason := s.closedReasonLocked()
		dest := DestExternal
		if approvedOrigin {
			dest = DestApproved
		}
		return s.denyLocked(req, dest, reason, ""), nil
	}
	if req.Scheme != "https" || req.Method == "" || req.Method == "CONNECT" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.denyLocked(req, DestExternal, ReasonProtocolUnsupported, ""), nil
	}
	if approvedOrigin {
		return s.admitApproved(ctx, req)
	}
	if host == s.target.Host {
		// 같은 이름의 다른 포트는 승인 origin이 아니다. 외부 자원으로도 보내지 않는다.
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.denyLocked(req, DestExternal, ReasonOutOfScopeOrigin, ""), nil
	}
	return s.admitExternal(ctx, req, host)
}

// closedReasonLocked는 닫힌 세션의 차단 사유다. 호출자는 s.mu를 보유한다.
func (s *Session) closedReasonLocked() string {
	switch s.reason {
	case exploration.TargetEnded:
		return ReasonTargetEnded
	case exploration.CapExhausted:
		return ReasonCapExhausted
	}
	return ReasonSessionClosed
}

func (s *Session) admitApproved(ctx context.Context, req Request) (Decision, *Ticket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check 뒤 잠금 전에 Close가 끼어들 수 있다. 닫힌 세션·지난 시간 상한은 새 송신을 열지 않는다.
	if !s.liveLocked(ctx) {
		return s.denyLocked(req, DestApproved, s.closedReasonLocked(), ""), nil
	}
	dial := netip.AddrPortFrom(s.target.ConnectIP, s.target.Port)
	if class := s.policy.classify(dial); class == DestControl {
		return s.denyLocked(req, DestControl, ReasonControlEndpoint, ""), nil
	}
	canonical, ok := canonicalPath(req.EscapedPath)
	inScope := false
	for _, prefix := range s.target.PathPrefixes {
		inScope = inScope || within(canonical, prefix)
	}
	for _, prefix := range s.target.ExcludedPrefixes {
		inScope = inScope && !within(canonical, prefix)
	}
	switch {
	case !ok || !inScope:
		return s.denyLocked(req, DestApproved, ReasonOutOfScopePath, ""), nil
	case risky(canonical, s.target.RiskyWords):
		return s.denyLocked(req, DestApproved, ReasonRiskyPath, ""), nil
	case req.WebSocket && s.binding.WriteMode != exploration.ApprovedWrites:
		return s.denyLocked(req, DestApproved, ReasonWSReadOnly, ""), nil
	case req.WebSocket && req.Method != "GET":
		return s.denyLocked(req, DestApproved, ReasonProtocolUnsupported, ""), nil
	case req.Method == "DELETE" || !hasMethod(s.target.Methods, req.Method):
		return s.denyLocked(req, DestApproved, ReasonMethodNotApproved, ""), nil
	case s.binding.WriteMode == exploration.ReadOnly && req.Method != "GET" && req.Method != "HEAD":
		return s.denyLocked(req, DestApproved, ReasonMethodNotApproved, ""), nil
	case s.inflight >= s.binding.Limits.MaxConcurrency:
		return s.denyLocked(req, DestApproved, ReasonCapExhausted, ""), nil
	case s.targetCount >= s.binding.Limits.MaxRequests:
		s.closeLocked(exploration.CapExhausted)
		return s.denyLocked(req, DestApproved, ReasonCapExhausted, ""), nil
	}
	return s.admitLocked(req, DestApproved, dial, s.target.SPKISHA256, canonical, "")
}

func (s *Session) admitExternal(ctx context.Context, req Request, host string) (Decision, *Ticket) {
	deny := func(class DestClass, reason string) (Decision, *Ticket) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.denyLocked(req, class, reason, host), nil
	}
	switch {
	case s.binding.CommMode != exploration.ExternalAllowed:
		return deny(DestExternal, ReasonExternalBlockedLocal)
	case req.Ingress != BrowserIngress:
		return deny(DestExternal, ReasonExternalDirect)
	case req.WebSocket:
		return deny(DestExternal, ReasonProtocolUnsupported)
	case req.Method != "GET" && req.Method != "HEAD":
		return deny(DestExternal, ReasonExternalMethod)
	case req.FetchMode == "navigate":
		return deny(DestExternal, ReasonExternalNavigation)
	}
	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		lookupCtx, cancel := context.WithDeadline(ctx, s.deadline)
		resolved, lookupErr := s.policy.Resolver.LookupNetIP(lookupCtx, "ip", host)
		cancel()
		if lookupErr != nil || len(resolved) == 0 {
			return deny(DestExternal, ReasonUnresolved)
		}
		addresses = resolved
	}
	// 이름 풀이를 기다리는 동안 대상이 철회·만료됐을 수 있다. 대기 뒤 권한을 다시 확인한다.
	if err := s.Check(ctx); err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.denyLocked(req, DestExternal, s.closedReasonLocked(), host), nil
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Less(addresses[j]) })
	// 풀린 주소 중 하나라도 내부·메타데이터·제어면 막는다. 섞인 응답으로 내부 주소를 고르게 하는 우회를 막는다.
	for _, ip := range addresses {
		if class := s.policy.classify(netip.AddrPortFrom(ip, req.Port)); class != DestExternal {
			return deny(class, reasonFor(class))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.liveLocked(ctx) {
		return s.denyLocked(req, DestExternal, s.closedReasonLocked(), host), nil
	}
	if s.externalSent >= s.binding.Limits.MaxExternalRequests || s.inflight >= s.binding.Limits.MaxConcurrency {
		return s.denyLocked(req, DestExternal, ReasonCapExhausted, host), nil
	}
	return s.admitLocked(req, DestExternal, netip.AddrPortFrom(addresses[0].Unmap(), req.Port), "", "", host)
}

func sourceFor(req Request) (source, kind string) {
	if req.Ingress == DirectIngress {
		return SourceAPIReconRuntime, "api"
	}
	if req.FetchMode == "navigate" {
		if req.Method == "POST" {
			return SourceBrowserForm, "form"
		}
		return SourceBrowserNavigation, "page"
	}
	return SourceBrowserXHR, "api"
}

func boundedKeys(keys []string) []string {
	const maxKeys, maxKeyBytes = 32, 64
	out := []string{}
	for _, k := range keys {
		if k != "" && len(k) <= maxKeyBytes && len(out) < maxKeys {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Done은 전달한 요청의 결과를 기록하고 진행 중 수를 줄인다. 승인 대상 요청만 private 관찰로 남긴다.
func (t *Ticket) Done(r Result) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		s := t.s
		s.mu.Lock()
		defer s.mu.Unlock()
		at := s.policy.Now().UTC()
		pattern := ""
		if t.dest == DestApproved {
			pattern = s.policy.PathPattern(t.req.EscapedPath)
		}
		s.recordLocked(Record{SessionRef: s.binding.SessionRef, Seq: t.seq, At: at, Decision: "forwarded", Ingress: t.req.Ingress, Dest: t.dest, Method: t.req.Method, Status: r.Status, Bytes: r.Bytes,
			PathPattern: pattern, ExternalHost: t.external, FetchDest: t.req.FetchDest, FetchMode: t.req.FetchMode, HasAuthHeader: t.req.HasAuth})
		if t.dest == DestApproved {
			source, kind := sourceFor(t.req)
			transport := "http"
			if t.req.WebSocket {
				transport = "websocket"
			}
			s.observations[t.seq] = diagnosisflow.ExplorationObservation{Binding: s.flow, Seq: t.seq, At: at, Method: t.req.Method, Path: t.path, Source: source, Kind: kind, Transport: transport,
				Status: r.Status, Forwarded: true, ResponseReceived: r.ResponseReceived, QueryKeys: boundedKeys(r.QueryKeys), BodyKeys: boundedKeys(r.BodyKeys), ContentType: r.ContentType,
				ResponseContentType: r.ResponseContentType, ResponseSHA256: r.ResponseSHA256}
		}
		s.inflight--
		if s.inflight == 0 {
			close(s.idle)
		}
	})
}

// Records는 보호 저장소로 옮길 경계 기록과 상한으로 생략한 차단 행 수다. 공개 경로로 내보내지 않는다.
func (s *Session) Records() ([]Record, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record{}, s.records...), s.omitted
}

// Reader는 서버만 쓰는 private 관찰 채널이다. diagnosisflow가 observed 발견을 같은 결속으로만 받게 한다.
func (s *Session) Reader() diagnosisflow.ExplorationReader { return sessionReader{s} }

// Sequences는 승인 대상으로 실제 전달한 요청의 순번이다(차단·외부 자원 제외).
func (s *Session) Sequences(b diagnosisflow.ExplorationBinding) ([]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b != s.flow {
		return nil, ErrConfig
	}
	out := make([]uint64, 0, len(s.observations))
	for seq := range s.observations {
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

type sessionReader struct{ s *Session }

func (r sessionReader) Session(context.Context) (diagnosisflow.ExplorationBinding, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if !r.s.active {
		return diagnosisflow.ExplorationBinding{}, ErrUnavailable
	}
	return r.s.flow, nil
}

func (r sessionReader) ReadObservation(_ context.Context, b diagnosisflow.ExplorationBinding, seq uint64) (diagnosisflow.ExplorationObservation, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	o, ok := r.s.observations[seq]
	if b != r.s.flow || !ok {
		return diagnosisflow.ExplorationObservation{}, ErrUnavailable
	}
	o.QueryKeys = append([]string(nil), o.QueryKeys...)
	o.BodyKeys = append([]string(nil), o.BodyKeys...)
	return o, nil
}

// NetResolver는 표준 resolver를 Resolver로 쓴다. 설치가 고정 resolver 주소를 줄 때 그 Dial만 쓴다.
type NetResolver struct{ *net.Resolver }

func (r NetResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return r.Resolver.LookupNetIP(ctx, network, host)
}

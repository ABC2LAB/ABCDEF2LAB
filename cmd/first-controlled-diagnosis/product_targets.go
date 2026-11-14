package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

type productTargetPolicy struct {
	Workflow         *diagnosisflow.Limits `json:"workflow,omitempty"`
	UpstreamCASHA256 string                `json:"upstream_ca_sha256,omitempty"`
	Format           string                `json:"format"`
	AllowedPorts     []int                 `json:"allowed_ports"`
	PrivateCIDRs     []string              `json:"private_cidrs,omitempty"`
	ResolverAddress  string                `json:"resolver_address,omitempty"`
	MaxRequests      int64                 `json:"max_requests"`
	MaxTargets       int                   `json:"max_targets"`
	UpstreamCAFile   string                `json:"upstream_ca_file,omitempty"`
	// Exploration은 탐색 경계·브라우저 샌드박스 연결이다(선택). 없으면 탐색은 NOT_RUN이다.
	Exploration *productExplorationPolicy `json:"exploration,omitempty"`
}
type productTargetRecord struct {
	Target           diagnosisprep.ProductTarget `json:"target"`
	Principal        string                      `json:"principal"`
	ReadonlyBasis    string                      `json:"readonly_basis"`
	ValidHours       int64                       `json:"valid_hours"`
	SelectedIP       string                      `json:"selected_ip,omitempty"`
	ReversibleWrites bool                        `json:"reversible_writes,omitempty"`
	// WriteConsent는 쓰기 동의를 받을 때 화면이 보인 뜻의 판이다. productWriteConsentAuto는 "사용자 모드는 요청마다 확인,
	// AI 자동 모드는 건별 확인 없이 보냄"에 동의한 것이다(C21). 이 칸이 없는 이전 등록은 "요청마다 확인"에 동의한
	// 것이라 자동 모드에서 쓰기를 쓰지 않는다(재등록하면 새 판, #525 이경준 리뷰).
	WriteConsent   string    `json:"write_consent,omitempty"`
	ProofExpiresAt time.Time `json:"proof_expires_at"`
}

const productMaxTargetAddresses = 32
const productMaxTargetApprovalHours = diagnosisprep.MaxTargetApprovalHours
const productTargetProbeTimeout = 3 * time.Second

const productWriteConsentAuto = "auto_without_confirmation_v1"

// autoWritesAllowed는 이 대상의 쓰기 승인을 AI 자동 모드가 건별 확인 없이 쓸 수 있는지다.
func (r productTargetRecord) autoWritesAllowed() bool {
	return r.ReversibleWrites && r.WriteConsent == productWriteConsentAuto
}

type productTargetRegistry struct {
	mu          sync.Mutex
	policy      productTargetPolicy
	policyCheck func() error
	journal     *diagnosisprep.ProtectedJournal
	contacts    productContactJournal
	// accountJournal은 대상 계정 등록·시험 계정 표시·해제 저널이다(product_accounts.go). accounts는 그 상태다.
	accountJournal productContactJournal
	accountDir     string
	accounts       map[string]map[string]*productAccountState
	// logins는 대상별 로그인 방식이다(계정 저널의 login_config 줄).
	logins    map[string]diagnosisprep.TargetLogin
	records   map[string]productTargetRecord
	approvals map[string]productTargetRecord
	resolver  *net.Resolver
	roots     *x509.CertPool
	now       func() time.Time
	// 시험 의존성은 HTTP나 설치 JSON에서 받지 않는다.
	lookupTXT func(context.Context, string) ([]string, error)
	lookupIP  func(context.Context, string) ([]netip.Addr, error)
	probe     func(context.Context, string, string, int) (string, time.Time, error)
	// reach는 http 대상의 연결 확인이다. TLS가 없어 SPKI·인증서 만료를 얻지 않는다(C22).
	reach           func(context.Context, string, int) error
	endpointAllowed func(string, netip.Addr) bool
}

// productTargetPlain은 http 대상인지다. http 대상은 TLS 고정(SPKI) 없이 DNS 증명·주소 집합으로 승인한다(C22).
func productTargetPlain(t diagnosisprep.ProductTarget) bool {
	return strings.HasPrefix(t.Origin, "http://")
}

// productTargetTLSValid는 승인 대상의 TLS 고정값이 scheme과 맞는지다. https는 SPKI가 있어야 하고 http는 비어 있어야 한다.
func productTargetTLSValid(t diagnosisprep.ProductTarget) bool {
	if productTargetPlain(t) {
		return t.TLSSPKISHA256 == ""
	}
	return urlDigestValid(t.TLSSPKISHA256)
}

func productTargetDigest(r productTargetRecord) string { b, _ := json.Marshal(r); return urlSHA(b) }
func productTargetCopy(r productTargetRecord) productTargetRecord {
	b, _ := json.Marshal(r)
	var out productTargetRecord
	_ = json.Unmarshal(b, &out)
	return out
}
func newProductTargetRegistry(dir string, p productTargetPolicy, check func() error) (*productTargetRegistry, error) {
	if !validProductTargetPolicy(p) || check == nil || check() != nil {
		return nil, errPreparation
	}
	resolver := net.DefaultResolver
	if p.ResolverAddress != "" {
		host, port, e := net.SplitHostPort(p.ResolverAddress)
		ip, ie := netip.ParseAddr(host)
		n, ne := strconv.Atoi(port)
		if e != nil || ie != nil || ne != nil || n < 1 || n > 65535 || !ip.IsPrivate() {
			return nil, errPreparation
		}
		address := p.ResolverAddress
		resolver = &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}}
	}
	var roots *x509.CertPool
	if p.UpstreamCAFile != "" {
		raw, e := readPrivateFile(p.UpstreamCAFile, 1<<20)
		if e != nil {
			return nil, errPreparation
		}
		roots = x509.NewCertPool()
		ok := roots.AppendCertsFromPEM(raw)
		clear(raw)
		if !ok {
			return nil, errPreparation
		}
	}
	journal, events, e := diagnosisprep.OpenProtectedJournal(dir)
	if e != nil {
		return nil, e
	}
	contactDir := filepath.Join(dir, "tls-contacts")
	if err := os.Mkdir(contactDir, 0700); err != nil && !os.IsExist(err) {
		_ = journal.Close()
		return nil, err
	}
	// 첫 접촉 전에 감사 디렉터리의 부모 엔트리도 동기 저장한다.
	parent, err := os.Open(dir)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	contacts, _, err := diagnosisprep.OpenProtectedJournal(contactDir)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	accountDir := filepath.Join(dir, "accounts")
	if err := os.Mkdir(accountDir, 0700); err != nil && !os.IsExist(err) {
		_ = errors.Join(journal.Close(), contacts.Close())
		return nil, err
	}
	accountJournal, accountEvents, err := diagnosisprep.OpenProtectedJournal(accountDir)
	if err != nil {
		_ = errors.Join(journal.Close(), contacts.Close())
		return nil, err
	}
	r := &productTargetRegistry{policy: p, policyCheck: check, journal: journal, contacts: contacts, accountJournal: accountJournal, accountDir: accountDir, accounts: map[string]map[string]*productAccountState{}, logins: map[string]diagnosisprep.TargetLogin{},
		records: map[string]productTargetRecord{}, approvals: map[string]productTargetRecord{}, resolver: resolver, roots: roots, now: func() time.Time { return time.Now().UTC() }}
	r.lookupTXT = resolver.LookupTXT
	r.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return resolver.LookupNetIP(ctx, "ip", host)
	}
	r.probe = r.probeTLS
	r.reach = r.reachTCP
	for _, event := range events {
		var rec productTargetRecord
		if decodeLaunchJSON(event, &rec) != nil || r.replay(rec) != nil {
			_ = r.Close()
			return nil, errPreparation
		}
	}
	// 계정 저널은 대상 저널 뒤에 읽는다(계정 줄은 있는 대상·판만 가리킬 수 있다).
	for _, event := range accountEvents {
		var e productAccountEvent
		if decodeLaunchJSON(event, &e) != nil || r.replayAccount(e) != nil {
			_ = r.Close()
			return nil, errPreparation
		}
	}
	return r, nil
}
func (r *productTargetRegistry) replay(next productTargetRecord) error {
	t := next.Target
	old, exists := r.records[t.ID]
	if !managementReference.MatchString(t.ID) || next.Principal != "ARTEX" || t.Revision < 1 || t.Origin == "" || len(next.ReadonlyBasis) < 20 ||
		next.WriteConsent != "" && (next.WriteConsent != productWriteConsentAuto || !next.ReversibleWrites) {
		return errPreparation
	}
	if !exists {
		if t.Revision != 1 || t.State != "pending" {
			return errPreparation
		}
	} else {
		if t.Revision != old.Target.Revision+1 || t.Origin != old.Target.Origin || !reflect.DeepEqual(t.PathPrefixes, old.Target.PathPrefixes) || !reflect.DeepEqual(t.Methods, old.Target.Methods) || t.Requests != old.Target.Requests || next.Principal != old.Principal || next.ReadonlyBasis != old.ReadonlyBasis || next.WriteConsent != old.WriteConsent || next.ReversibleWrites != old.ReversibleWrites || t.ChallengeName != old.Target.ChallengeName || t.ChallengeValue != old.Target.ChallengeValue || next.ValidHours != old.ValidHours || next.ProofExpiresAt != old.ProofExpiresAt {
			return errPreparation
		}
		if !(old.Target.State == "pending" && t.State == "approved" || old.Target.State == "approved" && t.State == "revoked") {
			return errPreparation
		}
	}
	if t.State == "approved" {
		if !productTargetTLSValid(t) || next.SelectedIP == "" || len(t.Addresses) < 1 || t.ApprovedAt.IsZero() || !t.ExpiresAt.After(t.ApprovedAt) {
			return errPreparation
		}
		r.approvals[t.ID] = productTargetCopy(next)
	}
	r.records[t.ID] = productTargetCopy(next)
	return nil
}
func (r *productTargetRegistry) check() error {
	if r.policyCheck() != nil || r.journal.Check() != nil || r.contacts.Check() != nil || r.accountJournal.Check() != nil {
		return errPreparation
	}
	return nil
}
func (r *productTargetRegistry) append(next productTargetRecord) error {
	raw, e := json.Marshal(next)
	if e != nil || r.check() != nil {
		return errPreparation
	}
	if e = r.journal.Append(raw); e != nil {
		return e
	}
	return r.replay(next)
}
func (r *productTargetRegistry) Close() error {
	return errors.Join(r.journal.Close(), r.contacts.Close(), r.accountJournal.Close())
}
func (r *productTargetRegistry) register(principal string, s diagnosisprep.TargetRegistration) (diagnosisprep.ProductTarget, error) {
	if principal != "ARTEX" {
		return diagnosisprep.ProductTarget{}, errors.New("operator_required")
	}
	in, e := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: s.URL, ProfileID: "target"})
	if e != nil {
		return diagnosisprep.ProductTarget{}, errors.New("invalid_target")
	}
	u, _ := url.Parse(in.TargetURL)
	if _, e = netip.ParseAddr(u.Hostname()); e == nil {
		return diagnosisprep.ProductTarget{}, errors.New("dns_ownership_required")
	}
	port := urlPort(u)
	allowed := false
	for _, v := range r.policy.AllowedPorts {
		allowed = allowed || v == port
	}
	if !allowed {
		return diagnosisprep.ProductTarget{}, errors.New("target_port_denied")
	}
	if s.Requests < 1 || s.Requests > r.policy.MaxRequests || s.ValidHours < 1 || s.ValidHours > productMaxTargetApprovalHours || len(strings.TrimSpace(s.ReadonlyBasis)) < 20 || len(s.ReadonlyBasis) > 1024 || len(s.PathPrefixes) < 1 || len(s.PathPrefixes) > 16 || len(s.Methods) < 1 || len(s.Methods) > 5 {
		return diagnosisprep.ProductTarget{}, errors.New("readonly_scope_required")
	}
	paths := []string{}
	size := 0
	for _, p := range s.PathPrefixes {
		v, e := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: u.Scheme + "://" + u.Host + p, ProfileID: "target"})
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || e != nil || v.TargetURL != u.Scheme+"://"+u.Host+p {
			return diagnosisprep.ProductTarget{}, errors.New("invalid_path_scope")
		}
		size += len(p)
		paths = append(paths, p)
	}
	if size > 2048 {
		return diagnosisprep.ProductTarget{}, errors.New("invalid_path_scope")
	}
	sort.Strings(paths)
	methods := append([]string(nil), s.Methods...)
	sort.Strings(methods)
	for i, m := range methods {
		if (m != "GET" && m != "HEAD" && !(r.policy.Workflow != nil && r.policy.Workflow.AllowReversibleWrites && s.ReversibleWrites && (m == "POST" || m == "PUT" || m == "PATCH"))) || i > 0 && methods[i-1] == m {
			return diagnosisprep.ProductTarget{}, errors.New("readonly_scope_required")
		}
	}
	for i, p := range paths {
		if i > 0 && paths[i-1] == p {
			return diagnosisprep.ProductTarget{}, errors.New("invalid_path_scope")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return diagnosisprep.ProductTarget{}, errPreparation
	}
	if len(r.records) >= r.policy.MaxTargets {
		return diagnosisprep.ProductTarget{}, errors.New("target_capacity")
	}
	id := "target-" + productRandom(16)
	t := diagnosisprep.ProductTarget{ID: id, Origin: u.Scheme + "://" + u.Host, State: "pending", Revision: 1, PathPrefixes: paths, Methods: methods, Requests: s.Requests, Port: port, ChallengeName: "_abc2lab-challenge." + u.Hostname(), ChallengeValue: "abc2lab-proof-v1-" + productRandom(32)}
	consent := ""
	if s.ReversibleWrites {
		consent = productWriteConsentAuto
	}
	rec := productTargetRecord{Target: t, Principal: principal, ReversibleWrites: s.ReversibleWrites, WriteConsent: consent, ReadonlyBasis: strings.TrimSpace(s.ReadonlyBasis), ValidHours: s.ValidHours, ProofExpiresAt: r.now().Add(30 * time.Minute)}
	if r.append(rec) != nil {
		return diagnosisprep.ProductTarget{}, errPreparation
	}
	return productTargetCopy(rec).Target, nil
}
func (r *productTargetRegistry) addresses(ctx context.Context, host string) ([]string, error) {
	ips, e := r.lookupIP(ctx, host)
	if e != nil || len(ips) < 1 {
		return nil, errors.New("target_dns_unavailable")
	}
	set := map[string]bool{}
	for _, raw := range ips {
		ip := raw.Unmap()
		if !r.addressAllowed(ip) {
			return nil, errors.New("target_address_denied")
		}
		set[ip.String()] = true
	}
	if len(set) > productMaxTargetAddresses {
		return nil, errors.New("target_dns_unavailable")
	}
	out := []string{}
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out, nil
}
func (r *productTargetRegistry) addressAllowed(ip netip.Addr) bool {
	if !urlTargetIPAllowed(ip, true) || !ip.IsValid() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.Is4In6() || netip.MustParsePrefix("fd00:ec2::/64").Contains(ip) {
		return false
	}
	if ip.IsPrivate() {
		for _, raw := range r.policy.PrivateCIDRs {
			if netip.MustParsePrefix(raw).Contains(ip) {
				return true
			}
		}
		return false
	}
	return urlTargetIPAllowed(ip, false)
}
func (r *productTargetRegistry) proof(ctx context.Context, t diagnosisprep.ProductTarget) error {
	records, e := r.lookupTXT(ctx, t.ChallengeName)
	if e != nil {
		return errors.New("ownership_not_verified")
	}
	for _, v := range records {
		if v == t.ChallengeValue {
			return nil
		}
	}
	return errors.New("ownership_not_verified")
}
func (r *productTargetRegistry) probeTLS(ctx context.Context, host, ip string, port int) (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, productTargetProbeTimeout)
	defer cancel()
	conn, e := (&net.Dialer{Timeout: productTargetProbeTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if e != nil {
		return "", time.Time{}, errors.New("target_tls_unavailable")
	}
	defer conn.Close()
	c := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: r.roots, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if c.HandshakeContext(ctx) != nil || len(c.ConnectionState().PeerCertificates) == 0 {
		return "", time.Time{}, errors.New("target_tls_unverified")
	}
	cert := c.ConnectionState().PeerCertificates[0]
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:]), cert.NotAfter, nil
}

// Share the remaining overall deadline so an early stalled address cannot use
// every later address's time. The existing three-second dial limit also bounds TLS.
func productTargetProbeContext(ctx context.Context, remaining int) (context.Context, context.CancelFunc) {
	timeout := productTargetProbeTimeout
	if deadline, ok := ctx.Deadline(); ok && remaining > 0 {
		if share := time.Until(deadline) / time.Duration(remaining); share < timeout {
			timeout = share
		}
	}
	return context.WithTimeout(ctx, timeout)
}
func (r *productTargetRegistry) reachTCP(ctx context.Context, ip string, port int) error {
	conn, e := (&net.Dialer{Timeout: productTargetProbeTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if e != nil {
		return errors.New("target_unreachable")
	}
	return conn.Close()
}
func (r *productTargetRegistry) approve(ctx context.Context, principal, id string) (diagnosisprep.ProductTarget, error) {
	if r.check() != nil {
		return diagnosisprep.ProductTarget{}, errPreparation
	}
	r.mu.Lock()
	rec, ok := r.records[id]
	r.mu.Unlock()
	if !ok || rec.Principal != principal {
		return diagnosisprep.ProductTarget{}, errors.New("target_not_found")
	}
	if rec.Target.State == "approved" {
		return rec.Target, nil
	}
	if rec.Target.State != "pending" || !r.now().Before(rec.ProofExpiresAt) {
		return diagnosisprep.ProductTarget{}, errors.New("ownership_challenge_expired")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if e := r.proof(ctx, rec.Target); e != nil {
		return diagnosisprep.ProductTarget{}, e
	}
	u, _ := url.Parse(rec.Target.Origin)
	addresses, e := r.addresses(ctx, u.Hostname())
	if e != nil {
		return diagnosisprep.ProductTarget{}, e
	}
	if r.checkDestinations(rec.Target.Origin, addresses) != nil {
		return diagnosisprep.ProductTarget{}, errors.New("target_address_denied")
	}
	var spki string
	var selected string
	for index, ip := range addresses {
		if ctx.Err() != nil {
			return diagnosisprep.ProductTarget{}, errors.New("target_connection_timeout")
		}
		probeCtx, stopProbe := productTargetProbeContext(ctx, len(addresses)-index)
		if probeCtx.Err() != nil {
			stopProbe()
			return diagnosisprep.ProductTarget{}, errors.New("target_connection_timeout")
		}
		spki, _, e = r.probeWithAudit(probeCtx, "approve", rec, u.Hostname(), ip)
		stopProbe()
		if errors.Is(e, errPreparation) {
			return diagnosisprep.ProductTarget{}, errPreparation
		}
		if e == nil {
			selected = ip
			break
		}
	}
	if e != nil || selected == "" {
		if errors.Is(e, errTargetCertificateExpired) {
			return diagnosisprep.ProductTarget{}, e
		}
		if productTargetPlain(rec.Target) {
			return diagnosisprep.ProductTarget{}, errors.New("target_unreachable")
		}
		return diagnosisprep.ProductTarget{}, errors.New("target_tls_unverified")
	}
	next := productTargetCopy(rec)
	next.Target.State = "approved"
	next.Target.Revision++
	next.Target.Addresses = addresses
	next.Target.TLSSPKISHA256 = spki
	next.SelectedIP = selected
	next.Target.ApprovedAt = r.now()
	next.Target.ExpiresAt = next.Target.ApprovedAt.Add(time.Duration(rec.ValidHours) * time.Hour)
	if !next.Target.ApprovedAt.Before(next.Target.ExpiresAt) {
		return diagnosisprep.ProductTarget{}, errors.New("target_approval_expired")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.records[id]; current.Target.Revision != rec.Target.Revision {
		return diagnosisprep.ProductTarget{}, errors.New("target_changed")
	}
	if r.append(next) != nil {
		return diagnosisprep.ProductTarget{}, errPreparation
	}
	return productTargetCopy(next).Target, nil
}
func (r *productTargetRegistry) lookup(principal, targetURL string, reportID string) (productTargetRecord, error) {
	u, e := url.Parse(targetURL)
	if e != nil {
		return productTargetRecord{}, errors.New("target_not_approved")
	}
	origin := u.Scheme + "://" + u.Host
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return productTargetRecord{}, errPreparation
	}
	if reportID != "" {
		v, ok := r.approvals[reportID]
		if ok && v.Principal == principal && v.Target.Origin == origin {
			return productTargetCopy(v), nil
		}
		return productTargetRecord{}, errors.New("target_not_approved")
	}
	var match *productTargetRecord
	for _, v := range r.records {
		if v.Principal != principal || v.Target.Origin != origin || v.Target.State != "approved" || !r.now().Before(v.Target.ExpiresAt) {
			continue
		}
		for _, p := range v.Target.PathPrefixes {
			if urlPathWithin(u.Path, p) {
				copy := v
				if match == nil || copy.Target.ApprovedAt.After(match.Target.ApprovedAt) {
					match = &copy
				}
				break
			}
		}
	}
	if match == nil {
		return productTargetRecord{}, errors.New("target_not_approved")
	}
	return productTargetCopy(*match), nil
}
func (r *productTargetRegistry) current(rec productTargetRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return errPreparation
	}
	now, ok := r.records[rec.Target.ID]
	if ok && (now.Target.State == "revoked" || productTargetDigest(now) == productTargetDigest(rec) && !r.now().Before(now.Target.ExpiresAt)) {
		return server.ErrFirstDiagnosisTargetEnded
	}
	if !ok || now.Target.State != "approved" || productTargetDigest(now) != productTargetDigest(rec) || !r.now().Before(now.Target.ExpiresAt) {
		return errPreparation
	}
	return nil
}

// recheck는 조회·시험용 재검증이다. 실행은 connectionForStart가 돌려준 증거를 그 회차에 결속한다.
func (r *productTargetRegistry) recheck(ctx context.Context, rec productTargetRecord) error {
	_, err := r.connectionForStart(ctx, rec, "")
	return err
}

// connectionForStart는 승인 기록을 바꾸지 않는다. 현재 origin 권한 아래 새 연결 증거만 만든다.
func (r *productTargetRegistry) connectionForStart(ctx context.Context, rec productTargetRecord, jobID string) (*productTargetConnection, error) {
	if r.current(rec) != nil {
		return nil, errors.New("target_approval_changed")
	}
	if jobID != "" && !managementReference.MatchString(jobID) {
		return nil, errPreparation
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := r.proof(ctx, rec.Target); err != nil {
		return nil, err
	}
	u, _ := url.Parse(rec.Target.Origin)
	addresses, err := r.addresses(ctx, u.Hostname())
	if err != nil {
		return nil, err
	}
	if r.checkDestinations(rec.Target.Origin, addresses) != nil {
		return nil, errors.New("target_address_denied")
	}
	for index, ip := range addresses {
		if ctx.Err() != nil {
			return nil, errors.New("target_connection_timeout")
		}
		if r.current(rec) != nil {
			return nil, errors.New("target_approval_changed")
		}
		probeCtx, stopProbe := productTargetProbeContext(ctx, len(addresses)-index)
		if probeCtx.Err() != nil {
			stopProbe()
			return nil, errors.New("target_connection_timeout")
		}
		spki, _, err := r.probeWithAuditForRun(probeCtx, "start", rec, u.Hostname(), ip, jobID)
		stopProbe()
		if errors.Is(err, errPreparation) {
			return nil, errPreparation
		}
		if err != nil {
			continue
		}
		if ctx.Err() != nil {
			return nil, errors.New("target_connection_timeout")
		}
		if err := r.current(rec); err != nil {
			return nil, errors.New("target_approval_changed")
		}
		return &productTargetConnection{IP: ip, SPKI: spki, Addresses: append([]string(nil), addresses...), jobID: jobID}, nil
	}
	if productTargetPlain(rec.Target) {
		return nil, errors.New("target_unreachable")
	}
	return nil, errors.New("target_tls_unverified")
}

func (r *productTargetRegistry) view(principal, id string) (*diagnosisprep.ProductTarget, []diagnosisprep.ProductTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return nil, nil, errPreparation
	}
	list := []diagnosisprep.ProductTarget{}
	for _, v := range r.records {
		if v.Principal != principal {
			continue
		}
		if id != "" {
			if v.Target.ID == id {
				t := productTargetCopy(v).Target
				return &t, nil, nil
			}
			continue
		}
		t := v.Target
		t.PathPrefixes = nil
		t.Methods = nil
		t.Addresses = nil
		t.ChallengeName = ""
		t.ChallengeValue = ""
		t.TLSSPKISHA256 = ""
		list = append(list, t)
	}
	if id != "" {
		return nil, nil, errors.New("target_not_found")
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return nil, list, nil
}
func (r *productTargetRegistry) revoke(principal, id string) (diagnosisprep.ProductTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok || rec.Principal != principal || rec.Target.State != "approved" {
		return diagnosisprep.ProductTarget{}, errors.New("target_not_found")
	}
	rec.Target.State = "revoked"
	rec.Target.Revision++
	if r.append(rec) != nil {
		return diagnosisprep.ProductTarget{}, errPreparation
	}
	// 철회된 대상의 계정 자격은 더 쓸 데가 없으니 지운다. 못 지운 파일은 저널 해시와 같아도 철회 대상이라 쓰이지 않는다.
	for label := range r.accounts[id] {
		if err := r.removeCredential(id, label); err != nil {
			log.Printf("[preparation] 대상 철회 때 자격 파일 삭제 실패: 대상=%s", id)
		}
	}
	return rec.Target, nil
}

// 레지스트리 디렉터리는 설치 보안 상태이며 대상 입력으로 지정하지 않는다.
func productTargetDirectory(root string) (string, error) {
	p := filepath.Join(root, "targets")
	if e := os.Mkdir(p, 0700); e != nil && !os.IsExist(e) {
		return "", e
	}
	if checkDirectory(p, true) != nil {
		return "", errPreparation
	}
	return p, nil
}

func validProductTargetPolicy(p productTargetPolicy) bool {
	if p.Workflow != nil && !p.Workflow.Valid() {
		return false
	}
	if p.Format != "abc2lab.product-target-policy/v1" || p.MaxRequests < 1 || p.MaxRequests > diagnosisprep.MaxTargetRequests || p.MaxTargets < 2 || p.MaxTargets > 64 || len(p.AllowedPorts) < 1 || len(p.AllowedPorts) > 8 {
		return false
	}
	for _, port := range p.AllowedPorts {
		if port < 1 || port > 65535 {
			return false
		}
	}
	for _, raw := range p.PrivateCIDRs {
		n, e := netip.ParsePrefix(raw)
		if e != nil || n.String() != raw || !n.Addr().IsPrivate() || n.Addr().Is4() && n.Bits() < 24 || n.Addr().Is6() && n.Bits() < 64 {
			return false
		}
	}

	return true
}

func (r *productTargetRegistry) resolve(principal, raw string) (*diagnosisprep.ProductTarget, error) {
	input, e := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: raw, ProfileID: "target"})
	if e != nil {
		return nil, errPreparation
	}
	if rec, e := r.lookup(principal, input.TargetURL, ""); e == nil {
		t := rec.Target
		return &t, nil
	}
	u, _ := url.Parse(input.TargetURL)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.check() != nil {
		return nil, errPreparation
	}
	var pending *productTargetRecord
	for _, v := range r.records {
		if v.Principal != principal || v.Target.Origin != u.Scheme+"://"+u.Host || v.Target.State != "pending" || !r.now().Before(v.ProofExpiresAt) {
			continue
		}
		for _, path := range v.Target.PathPrefixes {
			if urlPathWithin(u.Path, path) {
				copy := v
				if pending == nil || copy.ProofExpiresAt.After(pending.ProofExpiresAt) {
					pending = &copy
				}
				break
			}
		}
	}
	if pending == nil {
		return nil, nil
	}
	t := productTargetCopy(*pending).Target
	return &t, nil
}

func (r *productTargetRegistry) checkDestinations(origin string, addresses []string) error {
	if r.endpointAllowed == nil || len(addresses) == 0 {
		return errPreparation
	}
	for _, raw := range addresses {
		ip, e := netip.ParseAddr(raw)
		if e != nil || !r.endpointAllowed(origin, ip) {
			return errPreparation
		}
	}
	return nil
}

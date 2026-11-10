package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/Autumn-27/artex/diagnosisprep"
)

type productTargetPolicy struct {
	UpstreamCASHA256 string   `json:"upstream_ca_sha256,omitempty"`
	Format           string   `json:"format"`
	AllowedPorts     []int    `json:"allowed_ports"`
	PrivateCIDRs     []string `json:"private_cidrs,omitempty"`
	ResolverAddress  string   `json:"resolver_address,omitempty"`
	MaxRequests      int64    `json:"max_requests"`
	MaxTargets       int      `json:"max_targets"`
	UpstreamCAFile   string   `json:"upstream_ca_file,omitempty"`
}
type productTargetRecord struct {
	Target         diagnosisprep.ProductTarget `json:"target"`
	Principal      string                      `json:"principal"`
	ReadonlyBasis  string                      `json:"readonly_basis"`
	ValidHours     int64                       `json:"valid_hours"`
	SelectedIP     string                      `json:"selected_ip,omitempty"`
	ProofExpiresAt time.Time                   `json:"proof_expires_at"`
}
type productTargetRegistry struct {
	mu          sync.Mutex
	policy      productTargetPolicy
	policyCheck func() error
	journal     *diagnosisprep.ProtectedJournal
	records     map[string]productTargetRecord
	approvals   map[string]productTargetRecord
	resolver    *net.Resolver
	roots       *x509.CertPool
	now         func() time.Time
	// Test dependencies never come from HTTP or installation JSON.
	lookupTXT       func(context.Context, string) ([]string, error)
	lookupIP        func(context.Context, string) ([]netip.Addr, error)
	probe           func(context.Context, string, string, int) (string, time.Time, error)
	endpointAllowed func(string, netip.Addr) bool
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
	r := &productTargetRegistry{policy: p, policyCheck: check, journal: journal, records: map[string]productTargetRecord{}, approvals: map[string]productTargetRecord{}, resolver: resolver, roots: roots, now: func() time.Time { return time.Now().UTC() }}
	r.lookupTXT = resolver.LookupTXT
	r.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return resolver.LookupNetIP(ctx, "ip", host)
	}
	r.probe = r.probeTLS
	for _, event := range events {
		var rec productTargetRecord
		if decodeLaunchJSON(event, &rec) != nil || r.replay(rec) != nil {
			_ = journal.Close()
			return nil, errPreparation
		}
	}
	return r, nil
}
func (r *productTargetRegistry) replay(next productTargetRecord) error {
	t := next.Target
	old, exists := r.records[t.ID]
	if !managementReference.MatchString(t.ID) || next.Principal != "ARTEX" || t.Revision < 1 || t.Origin == "" || len(next.ReadonlyBasis) < 20 {
		return errPreparation
	}
	if !exists {
		if t.Revision != 1 || t.State != "pending" {
			return errPreparation
		}
	} else {
		if t.Revision != old.Target.Revision+1 || t.Origin != old.Target.Origin || !reflect.DeepEqual(t.PathPrefixes, old.Target.PathPrefixes) || !reflect.DeepEqual(t.Methods, old.Target.Methods) || t.Requests != old.Target.Requests || next.Principal != old.Principal || next.ReadonlyBasis != old.ReadonlyBasis || t.ChallengeName != old.Target.ChallengeName || t.ChallengeValue != old.Target.ChallengeValue || next.ValidHours != old.ValidHours || next.ProofExpiresAt != old.ProofExpiresAt {
			return errPreparation
		}
		if !(old.Target.State == "pending" && t.State == "approved" || old.Target.State == "approved" && t.State == "revoked") {
			return errPreparation
		}
	}
	if t.State == "approved" {
		if !urlDigestValid(t.TLSSPKISHA256) || next.SelectedIP == "" || len(t.Addresses) < 1 || t.ApprovedAt.IsZero() || !t.ExpiresAt.After(t.ApprovedAt) {
			return errPreparation
		}
		r.approvals[t.ID] = productTargetCopy(next)
	}
	r.records[t.ID] = productTargetCopy(next)
	return nil
}
func (r *productTargetRegistry) check() error {
	if r.policyCheck() != nil || r.journal.Check() != nil {
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
func (r *productTargetRegistry) Close() error { return r.journal.Close() }
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
	if s.Requests < 1 || s.Requests > r.policy.MaxRequests || s.ValidHours < 1 || s.ValidHours > 24 || len(strings.TrimSpace(s.ReadonlyBasis)) < 20 || len(s.ReadonlyBasis) > 1024 || len(s.PathPrefixes) < 1 || len(s.PathPrefixes) > 16 || len(s.Methods) < 1 || len(s.Methods) > 2 {
		return diagnosisprep.ProductTarget{}, errors.New("readonly_scope_required")
	}
	paths := []string{}
	size := 0
	for _, p := range s.PathPrefixes {
		v, e := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: u.Scheme + "://" + u.Host + p, ProfileID: "target"})
		if e != nil || v.TargetURL != u.Scheme+"://"+u.Host+p {
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
		if m != "GET" && m != "HEAD" || i > 0 && methods[i-1] == m {
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
	rec := productTargetRecord{Target: t, Principal: principal, ReadonlyBasis: strings.TrimSpace(s.ReadonlyBasis), ValidHours: s.ValidHours, ProofExpiresAt: r.now().Add(30 * time.Minute)}
	if r.append(rec) != nil {
		return diagnosisprep.ProductTarget{}, errPreparation
	}
	return productTargetCopy(rec).Target, nil
}
func (r *productTargetRegistry) addresses(ctx context.Context, host string) ([]string, error) {
	ips, e := r.lookupIP(ctx, host)
	if e != nil || len(ips) < 1 || len(ips) > 8 {
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
	conn, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
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
	var expiry time.Time
	var selected string
	for _, ip := range addresses {
		spki, expiry, e = r.probe(ctx, u.Hostname(), ip, rec.Target.Port)
		if e == nil {
			selected = ip
			break
		}
	}
	if e != nil || selected == "" {
		return diagnosisprep.ProductTarget{}, errors.New("target_tls_unverified")
	}
	next := productTargetCopy(rec)
	next.Target.State = "approved"
	next.Target.Revision++
	next.Target.Addresses = addresses
	next.Target.TLSSPKISHA256 = spki
	next.SelectedIP = selected
	next.Target.ApprovedAt = r.now()
	next.Target.ExpiresAt = r.now().Add(time.Duration(rec.ValidHours) * time.Hour)
	if expiry.Before(next.Target.ExpiresAt) {
		next.Target.ExpiresAt = expiry
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
	if !ok || now.Target.State != "approved" || productTargetDigest(now) != productTargetDigest(rec) || !r.now().Before(now.Target.ExpiresAt) {
		return errPreparation
	}
	return nil
}
func (r *productTargetRegistry) recheck(ctx context.Context, rec productTargetRecord) error {
	if r.current(rec) != nil {
		return errors.New("target_approval_changed")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if e := r.proof(ctx, rec.Target); e != nil {
		return e
	}
	u, _ := url.Parse(rec.Target.Origin)
	addresses, e := r.addresses(ctx, u.Hostname())
	if e != nil {
		return e
	}
	if r.checkDestinations(rec.Target.Origin, addresses) != nil {
		return errors.New("target_address_denied")
	}
	if !reflect.DeepEqual(addresses, rec.Target.Addresses) {
		return errors.New("target_dns_changed")
	}
	spki, _, e := r.probe(ctx, u.Hostname(), rec.SelectedIP, rec.Target.Port)
	if e != nil || spki != rec.Target.TLSSPKISHA256 {
		return errors.New("target_tls_changed")
	}
	return r.current(rec)
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
	return rec.Target, nil
}

// Registry directories are installation security state, not target inputs.
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
	if p.Format != "abc2lab.product-target-policy/v1" || p.MaxRequests < 1 || p.MaxRequests > 100 || p.MaxTargets < 2 || p.MaxTargets > 64 || len(p.AllowedPorts) < 1 || len(p.AllowedPorts) > 8 {
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
	if r.endpointAllowed == nil {
		return nil
	}
	for _, raw := range addresses {
		ip, e := netip.ParseAddr(raw)
		if e != nil || !r.endpointAllowed(origin, ip) {
			return errPreparation
		}
	}
	return nil
}

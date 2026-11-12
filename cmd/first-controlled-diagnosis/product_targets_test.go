package main

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/diagnosisprep"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func targetTestRegistry(t *testing.T) (*productTargetRegistry, string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("protected preparation uses an unprivileged owner")
	}
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private root")
	}
	r, e := newProductTargetRegistry(root, productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{443}, PrivateCIDRs: []string{"10.200.1.0/24"}, MaxTargets: 32, MaxRequests: 4}, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	r.endpointAllowed = func(string, netip.Addr) bool { return true }
	t.Cleanup(func() { _ = r.Close() })
	return r, root
}
func targetTestSpec(host string) diagnosisprep.TargetRegistration {
	return diagnosisprep.TargetRegistration{URL: "https://" + host + "/read", PathPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD"}, ReadonlyBasis: "Approved owned fixture: product listing GET and HEAD do not mutate application state.", Requests: 4, ValidHours: 4}
}
func TestProductTargetsOwnershipDNSAndRoundIsolation(t *testing.T) {
	r, root := targetTestRegistry(t)
	ctx := t.Context()
	txt := map[string][]string{}
	ips := map[string][]netip.Addr{"a.example.test": {netip.MustParseAddr("10.200.1.10")}, "b.example.test": {netip.MustParseAddr("10.200.1.20")}}
	probes := 0
	spki := strings.Repeat("a", 64)
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(_ context.Context, name string) ([]netip.Addr, error) { return ips[name], nil }
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		probes++
		return spki, time.Now().Add(24 * time.Hour), nil
	}
	a, e := r.register("ARTEX", targetTestSpec("a.example.test"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.approve(ctx, "ARTEX", a.ID); e == nil || e.Error() != "ownership_not_verified" || probes != 0 {
		t.Fatal("URL or operator declaration alone approved a target")
	}
	if _, e = r.register("unprivileged", targetTestSpec("a.example.test")); e == nil || e.Error() != "operator_required" {
		t.Fatal("nonoperator registered a target")
	}
	txt[a.ChallengeName] = []string{a.ChallengeValue}
	a, e = r.approve(ctx, "ARTEX", a.ID)
	if e != nil {
		t.Fatal(e)
	}
	ar, e := r.lookup("ARTEX", "https://a.example.test/read/item", "")
	if e != nil {
		t.Fatal(e)
	}
	digest := productTargetDigest(ar)
	b, e := r.register("ARTEX", targetTestSpec("b.example.test"))
	if e != nil {
		t.Fatal(e)
	}
	txt[b.ChallengeName] = []string{a.ChallengeValue}
	if _, e = r.approve(ctx, "ARTEX", b.ID); e == nil || e.Error() != "ownership_not_verified" {
		t.Fatal("another target's proof was borrowed")
	}
	txt[b.ChallengeName] = []string{b.ChallengeValue}
	b, e = r.approve(ctx, "ARTEX", b.ID)
	if e != nil {
		t.Fatal(e)
	}
	br, e := r.lookup("ARTEX", "https://b.example.test/read", "")
	if e != nil {
		t.Fatal(e)
	}
	if ar.Target.ID == br.Target.ID || ar.SelectedIP == br.SelectedIP || r.current(ar) != nil || productTargetDigest(ar) != digest {
		t.Fatal("new target approval mutated another target")
	}
	if _, e = r.lookup("ARTEX", "https://a.example.test/outside", ""); e == nil {
		t.Fatal("unapproved path acquired authority")
	}
	if _, e = r.lookup("ARTEX", "https://unapproved.example.test/read", ""); e == nil {
		t.Fatal("unapproved origin acquired authority")
	}
	ips["a.example.test"] = []netip.Addr{netip.MustParseAddr("10.200.1.11")}
	if e = r.recheck(ctx, ar); e == nil || e.Error() != "target_dns_changed" {
		t.Fatal("DNS change was not fenced")
	}
	ips["a.example.test"] = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	before := probes
	if e = r.recheck(ctx, ar); e == nil || e.Error() != "target_address_denied" || probes != before {
		t.Fatal("metadata address reached TLS probing")
	}
	ips["a.example.test"] = []netip.Addr{netip.MustParseAddr("10.200.1.10")}
	spki = strings.Repeat("b", 64)
	if e = r.recheck(ctx, ar); e == nil || e.Error() != "target_tls_changed" {
		t.Fatal("TLS identity change was not fenced")
	}
	spki = strings.Repeat("a", 64)
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if e != nil {
		t.Fatal("approval journal did not survive restart", e)
	}
	defer reopened.Close()
	restored, e := reopened.lookup("ARTEX", "https://a.example.test/read", "")
	if e != nil || productTargetDigest(restored) != digest {
		t.Fatal("restart changed the scope binding")
	}
	if _, e = reopened.revoke("ARTEX", ar.Target.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.lookup("ARTEX", "https://a.example.test/read", ""); e == nil {
		t.Fatal("revoked approval admitted a new run")
	}
	if historic, e := reopened.lookup("ARTEX", "https://a.example.test/read", ar.Target.ID); e != nil || productTargetDigest(historic) != digest {
		t.Fatal("revocation destroyed saved-report identity")
	}
}
func TestProductTargetPolicyDeniesPrivateAndUnsafeInputs(t *testing.T) {
	r, _ := targetTestRegistry(t)
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "224.0.0.1", "fd00:ec2::254", "::1", "fe80::1", "10.200.2.1"} {
		if r.addressAllowed(netip.MustParseAddr(ip)) {
			t.Fatal("unsafe address allowed", ip)
		}
	}
	if !r.addressAllowed(netip.MustParseAddr("10.200.1.10")) {
		t.Fatal("explicit owned-lab envelope was lost")
	}
	r.policy.PrivateCIDRs = nil
	if r.addressAllowed(netip.MustParseAddr("10.200.1.10")) {
		t.Fatal("private target allowed by default")
	}
	for _, change := range []func(*diagnosisprep.TargetRegistration){func(s *diagnosisprep.TargetRegistration) { s.URL = "https://a.example.test:9443/read" }, func(s *diagnosisprep.TargetRegistration) { s.PathPrefixes = []string{"/%2e"} }, func(s *diagnosisprep.TargetRegistration) { s.Methods = []string{"POST"} }, func(s *diagnosisprep.TargetRegistration) { s.ReadonlyBasis = "checkbox" }, func(s *diagnosisprep.TargetRegistration) { s.Requests = 5 }} {
		s := targetTestSpec("a.example.test")
		change(&s)
		if _, e := r.register("ARTEX", s); e == nil {
			t.Fatal("unsafe registration accepted")
		}
	}
}
func TestProductTargetRevocationCancelsOnlyMatchingRun(t *testing.T) {
	r, _ := targetTestRegistry(t)
	ctx := t.Context()
	r.lookupTXT = func(context.Context, string) ([]string, error) { return nil, errors.New("unused") }
	// 소유한 합성 증명으로 실제 영속 전이와 같은 경로를 채운다.
	approve := func(host, ip string) productTargetRecord {
		t.Helper()
		v, e := r.register("ARTEX", targetTestSpec(host))
		if e != nil {
			t.Fatal(e)
		}
		r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{v.ChallengeValue}, nil }
		r.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr(ip)}, nil }
		r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
			return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
		}
		if _, e = r.approve(ctx, "ARTEX", v.ID); e != nil {
			t.Fatal(e)
		}
		record, e := r.lookup("ARTEX", "https://"+host+"/read", "")
		if e != nil {
			t.Fatal(e)
		}
		return record
	}
	a, b := approve("a.example.test", "10.200.1.10"), approve("b.example.test", "10.200.1.20")
	p := &productRuntime{targets: r}
	ac, ca := context.WithCancel(ctx)
	defer ca()
	bc, cb := context.WithCancel(ctx)
	defer cb()
	doneA, e := p.attach(a, "job-a", ca)
	if e != nil {
		t.Fatal(e)
	}
	defer doneA()
	doneB, e := p.attach(b, "job-b", cb)
	if e != nil {
		t.Fatal(e)
	}
	defer doneB()
	if _, e = p.revoke("ARTEX", a.Target.ID); e != nil {
		t.Fatal(e)
	}
	if ac.Err() == nil || bc.Err() != nil {
		t.Fatal("revocation mixed two run contexts")
	}
}

func TestProductTargetExceptionsCannotAllowMetadataOrProtectedServices(t *testing.T) {
	r, _ := targetTestRegistry(t)
	r.policy.PrivateCIDRs = append(r.policy.PrivateCIDRs, "fd20:ce::/64", "fd00:ec2::/64")
	for _, raw := range []string{"fd20:ce::254", "fd00:ec2::254"} {
		if r.addressAllowed(netip.MustParseAddr(raw)) {
			t.Fatal("CIDR exception admitted metadata")
		}
	}
	v, e := r.register("ARTEX", targetTestSpec("model.example.test"))
	if e != nil {
		t.Fatal(e)
	}
	r.lookupTXT = func(context.Context, string) ([]string, error) { return []string{v.ChallengeValue}, nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		t.Fatal("protected service reached TLS probe")
		return "", time.Time{}, nil
	}
	r.endpointAllowed = func(origin string, ip netip.Addr) bool { return false }
	if _, e = r.approve(t.Context(), "ARTEX", v.ID); e == nil || e.Error() != "target_address_denied" {
		t.Fatal("protected endpoint acquired target approval")
	}
}

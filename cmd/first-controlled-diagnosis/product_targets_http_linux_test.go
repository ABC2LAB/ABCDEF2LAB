//go:build linux

package main

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// http 대상(C22)은 DNS 소유 증명·주소 집합으로 승인하고 TLS 대신 연결만 확인한다. SPKI는 비고 인증서 만료 상한도
// 없다. 시작 때 재확인도 연결만 본다. 탐색 경계에는 http 대상으로 넘긴다.
func TestProductHTTPTargetApprovesWithoutTLS(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("protected preparation uses an unprivileged owner")
	}
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private root")
	}
	policy := withTargetPolicyDefaults(productTargetPolicy{PrivateCIDRs: []string{"10.200.1.0/24"}})
	if len(policy.AllowedPorts) != 2 || policy.AllowedPorts[0] != 80 || policy.AllowedPorts[1] != 443 {
		t.Fatal("install default ports", policy.AllowedPorts)
	}
	r, e := newProductTargetRegistry(root, policy, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = r.Close() })
	r.endpointAllowed = func(string, netip.Addr) bool { return true }
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10"), netip.MustParseAddr("10.200.1.11")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		t.Fatal("http target reached the TLS probe")
		return "", time.Time{}, nil
	}
	var reached []string
	down := map[string]bool{"10.200.1.10": true}
	r.reach = func(_ context.Context, ip string, port int) error {
		reached = append(reached, ip)
		if port != 80 || down[ip] {
			return errors.New("target_unreachable")
		}
		return nil
	}
	spec := targetTestSpec("plain.example.test")
	spec.URL = "http://plain.example.test/read"
	target, e := r.register("ARTEX", spec)
	if e != nil || target.Origin != "http://plain.example.test" || target.Port != 80 {
		t.Fatalf("http target registration: %+v %v", target, e)
	}
	txt[target.ChallengeName] = []string{target.ChallengeValue}
	approved, e := r.approve(t.Context(), "ARTEX", target.ID)
	if e != nil || approved.TLSSPKISHA256 != "" || approved.ExpiresAt != approved.ApprovedAt.Add(4*time.Hour) || strings.Join(reached, ",") != "10.200.1.10,10.200.1.11" {
		t.Fatalf("http approval: %+v %v reached=%v", approved, e, reached)
	}
	rec, e := r.lookup("ARTEX", "http://plain.example.test/read/item", "")
	if e != nil || rec.SelectedIP != "10.200.1.11" {
		t.Fatal("approved http target lookup", rec.SelectedIP, e)
	}
	if _, e = r.lookup("ARTEX", "https://plain.example.test/read/item", ""); e == nil {
		t.Fatal("https origin borrowed the http approval")
	}
	if e = r.recheck(t.Context(), rec); e != nil {
		t.Fatal("http recheck", e)
	}
	down["10.200.1.11"] = true
	if e = r.recheck(t.Context(), rec); e == nil || e.Error() != "target_unreachable" {
		t.Fatal("unreachable http target passed recheck", e)
	}
	bt, e := exploreTargetOf(rec, nil)
	if e != nil || bt.Scheme != "http" || bt.SPKISHA256 != "" || bt.Port != 80 {
		t.Fatalf("boundary target: %+v %v", bt, e)
	}
	// 승인 기록의 TLS 고정값은 scheme과 맞아야 한다(재생 때도 같은 검사).
	bad := productTargetCopy(rec)
	bad.Target.TLSSPKISHA256 = strings.Repeat("a", 64)
	if productTargetTLSValid(bad.Target) {
		t.Fatal("http approval with a TLS pin accepted")
	}
	bad.Target.Origin, bad.Target.TLSSPKISHA256 = "https://plain.example.test", ""
	if productTargetTLSValid(bad.Target) {
		t.Fatal("https approval without a TLS pin accepted")
	}
	// 모든 주소에 닿지 못하면 승인하지 않는다.
	other := targetTestSpec("down.example.test")
	other.URL = "http://down.example.test/read"
	pending, e := r.register("ARTEX", other)
	if e != nil {
		t.Fatal(e)
	}
	txt[pending.ChallengeName] = []string{pending.ChallengeValue}
	if _, e = r.approve(t.Context(), "ARTEX", pending.ID); e == nil || e.Error() != "target_unreachable" {
		t.Fatal("unreachable http target approved", e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := newProductTargetRegistry(root, policy, func() error { return nil })
	if e != nil {
		t.Fatal("http approval did not survive restart", e)
	}
	defer reopened.Close()
	if restored, e := reopened.lookup("ARTEX", "http://plain.example.test/read", ""); e != nil || productTargetDigest(restored) != productTargetDigest(rec) {
		t.Fatal("restart changed the http approval", e)
	}
	// 설치 조직이 443만 허용하면 http 기본 포트 대상은 등록하지 않는다.
	strictRoot := t.TempDir()
	if os.Chmod(strictRoot, 0700) != nil {
		t.Fatal("private root")
	}
	strict, e := newProductTargetRegistry(strictRoot, withTargetPolicyDefaults(productTargetPolicy{AllowedPorts: []int{443}}), func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer strict.Close()
	if _, e = strict.register("ARTEX", spec); e == nil || e.Error() != "target_port_denied" {
		t.Fatal("http target bypassed the installation port policy", e)
	}
}

func TestURLPortFollowsScheme(t *testing.T) {
	for raw, want := range map[string]int{"http://a.test/x": 80, "https://a.test/x": 443, "http://a.test:8080/x": 8080, "https://a.test:8443/x": 8443} {
		u, _ := url.Parse(raw)
		if got := urlPort(u); got != want {
			t.Errorf("%s: %d want %d", raw, got, want)
		}
	}
}

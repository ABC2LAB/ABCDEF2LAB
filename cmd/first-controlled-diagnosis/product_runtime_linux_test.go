//go:build linux

package main

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductModelsBindIndependentTargetsWithoutNetworkOnInspect(t *testing.T) {
	f := newURLNegativeFixture(t)
	completeProductTemplateForTest(f)
	m := f.config.Profiles[0]
	m.TargetRef = ""
	m.ScopeRef = ""
	m.AllowPrivateTarget = false
	m.Attestation = urlNonmutatingAttestation{}
	f.policy.Rules = f.policy.Rules[:0]
	m.PolicySHA256 = urlNegativeWrite(t, f.launch.HTTP.PolicyFile, f.policy)
	f.config.Profiles = nil
	policyPath := filepath.Join(filepath.Dir(f.path), "targets-policy.json")
	digest := urlNegativeWrite(t, policyPath, productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{443}, PrivateCIDRs: []string{"10.200.1.0/24"}, MaxRequests: 4, MaxTargets: 32})
	f.config.Product = &urlProductInstallation{Format: "abc2lab.product-runtime/v2", TargetPolicy: urlProtectedPin{File: policyPath, SHA256: digest}, Models: []urlPreparationProfile{m}}
	urlNegativeWrite(t, f.path, f.config)
	cfg, pin, e := readURLInstallation(f.path)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := newProductRuntime(cfg, pin)
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.targets.Close()
	if profiles, e := runtime.profiles(); e != nil || len(profiles) != 1 {
		t.Fatal("targetless model catalog", e)
	}
	r := runtime.targets
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(_ context.Context, name string) ([]netip.Addr, error) {
		ip := "10.200.1.10"
		if strings.HasPrefix(name, "b.") {
			ip = "10.200.1.20"
		}
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
	}
	approve := func(host string) diagnosisprep.ProductTarget {
		spec := targetTestSpec(host)
		spec.Requests = m.Budget.Requests
		v, e := r.register("ARTEX", spec)
		if e != nil {
			t.Fatal(e)
		}
		txt[v.ChallengeName] = []string{v.ChallengeValue}
		v, e = r.approve(t.Context(), "ARTEX", v.ID)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	a := approve("a.example.test")
	input := diagnosisprep.Input{ProfileID: m.ID, TargetURL: "https://a.example.test/read/item"}
	one, e := runtime.load("ARTEX", input, false, "")
	if e != nil {
		t.Fatal("load A", e)
	}
	defer one.clearMaterials()
	record, lookupErr := r.lookup("ARTEX", input.TargetURL, "")
	legacyName := "bound-" + urlSHA([]byte(productTargetDigest(record) + "\n" + m.ID + "\n" + m.Launch.SHA256))[:32]
	if lookupErr != nil || filepath.Base(filepath.Dir(one.launch.HTTP.PolicyFile)) != legacyName {
		t.Fatal("existing target binding path changed; stored report recovery would diverge")
	}
	b := approve("b.example.test")
	two, e := runtime.load("ARTEX", diagnosisprep.Input{ProfileID: m.ID, TargetURL: "https://b.example.test/read/item"}, false, "")
	if e != nil {
		t.Fatal("load B", e)
	}
	defer two.clearMaterials()
	again, e := runtime.load("ARTEX", input, false, "")
	if e != nil {
		t.Fatal(e)
	}
	defer again.clearMaterials()
	if one.inspection.InstallationDigest != again.inspection.InstallationDigest || one.inspection.InstallationDigest == two.inspection.InstallationDigest || one.profile.TargetRef != a.ID || two.profile.TargetRef != b.ID {
		t.Fatal("target binding mixed between runs")
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("inspection performed DNS")
		return nil, nil
	}
	if _, e = runtime.load("ARTEX", input, false, ""); e != nil {
		t.Fatal("inspection required network", e)
	}
	if _, e = runtime.load("ARTEX", diagnosisprep.Input{ProfileID: m.ID, TargetURL: "https://unapproved.example.test/read"}, false, ""); e == nil {
		t.Fatal("unapproved target loaded")
	}
	// A cached derived file must still equal the trusted template + approval.
	raw, e := os.ReadFile(one.launch.HTTP.PolicyFile)
	if e != nil {
		t.Fatal(e)
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		t.Fatal("policy")
	}
	body["max_requests"] = 99
	urlNegativeWrite(t, one.launch.HTTP.PolicyFile, body)
	if _, e = runtime.load("ARTEX", input, false, ""); e == nil {
		t.Fatal("tampered cached binding trusted")
	}
	f.assertNoConnections()
}

// Product catalog now validates the actual encoder bounds before exposing a model.
// The generic negative fixture intentionally omits them, so supply a valid product template.
func completeProductTemplateForTest(f *urlNegativeFixture) {
	f.policy.Limits = server.E2HTTPFileLimits{MaxAttempts: 20, MaxCalls: 8, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeoutMS: 5000, CallTimeoutMS: 20000, MaxDenials: 8, MaxTransient: 1, MaxUnknown: 1}
	f.policy.Proxy = server.E2HTTPFileProxy{HeaderTimeoutMS: 1000, RequestTimeoutMS: 20000, MaxHeaderBytes: 32768, MaxConnections: 4}
	f.policy.Runtime = server.E2HTTPFileRuntime{MaxWorkerTurns: 8, MaxToolCalls: 8, WorkerTimeoutMS: 30000, MaxPlannerRounds: 6, MaxIntentRuns: 1, DrainTimeoutMS: 10000, OutputLimit: 4096}
}

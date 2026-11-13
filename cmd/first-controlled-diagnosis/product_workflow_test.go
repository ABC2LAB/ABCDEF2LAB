//go:build linux

package main

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
)

func TestWorkflowProfileBindsCompleteInputAndKeepsApprovedWritesInAutomaticMode(t *testing.T) {
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
	limits := &diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096, AllowReversibleWrites: true}
	policyPath := filepath.Join(filepath.Dir(f.path), "workflow-target-policy.json")
	digest := urlNegativeWrite(t, policyPath, productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{443}, PrivateCIDRs: []string{"10.200.1.0/24"}, MaxRequests: 4, MaxTargets: 32, Workflow: limits})
	f.config.Product = &urlProductInstallation{Format: "abc2lab.product-runtime/v2", TargetPolicy: urlProtectedPin{File: policyPath, SHA256: digest}, Models: []urlPreparationProfile{m}}
	urlNegativeWrite(t, f.path, f.config)
	cfg, pin, err := readURLInstallation(f.path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newProductRuntime(cfg, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.targets.Close()
	profiles, err := runtime.profiles()
	if err != nil || len(profiles) != 1 || profiles[0].WorkflowRequestLimit != 4 || profiles[0].Workflow == nil {
		t.Fatal("installation request cap missing", err)
	}
	r := runtime.targets
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
	}
	spec := targetTestSpec("owned.example.test")
	spec.Requests = m.Budget.Requests
	spec.Methods = []string{"GET", "HEAD", "POST"}
	spec.ReversibleWrites = true
	target, err := r.register("ARTEX", spec)
	if err != nil {
		t.Fatal(err)
	}
	txt[target.ChallengeName] = []string{target.ChallengeValue}
	if _, err = r.approve(t.Context(), "ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	input := diagnosisprep.Input{TargetURL: "https://owned.example.test/read", ProfileID: m.ID, DisplayName: "First owned run", WorkflowMode: diagnosisflow.Manual, ExcludedPaths: "/read/excluded"}
	one, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("manual scope", err)
	}
	defer one.clearMaterials()
	if one.policy.ApprovalMode != e2proxy.ApprovalWorkbenchV1 || one.policy.Workflow == nil || one.policy.Workflow.InputSHA256 != workflowInputDigest(input) {
		t.Fatal("manual policy lost input binding")
	}
	input.DisplayName = "Second owned run"
	two, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("different display name collided with cached policy", err)
	}
	defer two.clearMaterials()
	if two.launch.HTTP.PolicyFile == one.launch.HTTP.PolicyFile {
		t.Fatal("distinct signed inputs shared mutable derived policy")
	}
	input.WorkflowMode = diagnosisflow.Auto
	auto, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("automatic scope", err)
	}
	defer auto.clearMaterials()
	if auto.policy.ApprovalMode != e2proxy.ApprovalPreauthorizedScope {
		t.Fatal("automatic mode changed policy")
	}
	// 자동 모드는 대상에 승인한 쓰기를 건별 확인 없이 쓴다(C21).
	if !reflect.DeepEqual(auto.inspection.Scope.Methods, []string{"GET", "HEAD", "POST"}) {
		t.Fatal("automatic mode lost approved write methods", auto.inspection.Scope.Methods)
	}
	for _, rule := range auto.policy.Rules {
		if !reflect.DeepEqual(rule.Methods, []string{"GET", "HEAD", "POST"}) {
			t.Fatal("automatic mode rule lost approved write methods", rule.Methods)
		}
	}
	// "요청마다 확인"에 동의한 이전 등록(WriteConsent 없음)은 자동 모드에서 쓰기를 쓰지 않는다. 사용자 모드는 그대로 쓴다
	// (#525 이경준 리뷰). 같은 기록을 다시 읽어도 결과가 같다.
	r.mu.Lock()
	legacy := r.records[target.ID]
	if legacy.WriteConsent != productWriteConsentAuto {
		r.mu.Unlock()
		t.Fatal("new registration did not record the automatic-write consent")
	}
	legacy.WriteConsent = ""
	r.records[target.ID], r.approvals[target.ID] = legacy, legacy
	r.mu.Unlock()
	input.DisplayName = "Legacy consent run"
	old, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("legacy automatic scope", err)
	}
	old.clearMaterials()
	if !reflect.DeepEqual(old.inspection.Scope.Methods, []string{"GET", "HEAD"}) {
		t.Fatal("legacy consent sent writes in automatic mode", old.inspection.Scope.Methods)
	}
	input.WorkflowMode = diagnosisflow.Manual
	manualLegacy, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("legacy manual scope", err)
	}
	manualLegacy.clearMaterials()
	if !reflect.DeepEqual(manualLegacy.inspection.Scope.Methods, []string{"GET", "HEAD", "POST"}) {
		t.Fatal("legacy consent lost writes in manual mode", manualLegacy.inspection.Scope.Methods)
	}
	input.WorkflowMode = diagnosisflow.Auto
	// Installation opt-in, target consent and ordinary-mode write filtering are
	// separate boundaries: a signed POST target must not widen the legacy path.
	input.WorkflowMode = ""
	ordinary, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("ordinary profile", err)
	}
	ordinary.clearMaterials()
	for _, rule := range ordinary.policy.Rules {
		for _, method := range rule.Methods {
			if method != "GET" && method != "HEAD" {
				t.Fatal("ordinary mode inherited writes")
			}
		}
	}
	for _, rule := range one.policy.Rules {
		if !rule.ApprovalDisplay.PublicPath {
			t.Fatal("manual approval cannot distinguish paths")
		}
	}
	installed := runtime.targets.policy.Workflow
	runtime.targets.policy.Workflow = nil
	input.WorkflowMode = diagnosisflow.Manual
	if loaded, err := runtime.load("ARTEX", input, false, ""); err == nil {
		loaded.clearMaterials()
		t.Fatal("workflow without installation opt-in")
	}
	runtime.targets.policy.Workflow = installed
	installed.AllowReversibleWrites = false
	if _, err := r.register("ARTEX", spec); err == nil {
		t.Fatal("write target bypassed installation consent")
	}
	installed.AllowReversibleWrites = true
	spec.ReversibleWrites = false
	if _, err := r.register("ARTEX", spec); err == nil {
		t.Fatal("write target bypassed target consent")
	}
	// Historical workflow pins are preserved at capacity. Ordinary rounds have
	// their own bounded reserve and cannot be starved by workflow display names.
	root := filepath.Join(runtime.config.StateDir, "bindings")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(entries); i < 512; i++ {
		if err = os.Mkdir(filepath.Join(root, fmt.Sprintf("owned-reserved-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	input.DisplayName = "Full workflow cache"
	if loaded, err := runtime.load("ARTEX", input, false, ""); err == nil {
		loaded.clearMaterials()
		t.Fatal("workflow binding cap removed")
	}
	// A different approved target makes this ordinary binding new as well.
	spec.ReversibleWrites = true
	spec.ReadonlyBasis += " Updated owned scope."
	changed, err := r.register("ARTEX", spec)
	if err != nil {
		t.Fatal(err)
	}
	txt[changed.ChallengeName] = []string{changed.ChallengeValue}
	if _, err = r.approve(t.Context(), "ARTEX", changed.ID); err != nil {
		t.Fatal(err)
	}
	input.WorkflowMode = ""
	fallback, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("ordinary round blocked by workflow cache", err)
	}
	defer fallback.clearMaterials()
	if filepath.Base(filepath.Dir(filepath.Dir(fallback.launch.HTTP.PolicyFile))) != "ordinary" {
		t.Fatal("ordinary reserve not used")
	}
	f.assertNoConnections()
}

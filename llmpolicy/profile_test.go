package llmpolicy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/llmpolicy"
)

const profileBaseURL = "http://model.example:8080/v1"

func profileAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("invalid test address %q: %v", raw, err)
	}
	return addr
}

func profileTarget(id int64, format, model, baseURL string) llmpolicy.ProfileTarget {
	return llmpolicy.ProfileTarget{ID: id, Format: format, Model: model, BaseURL: baseURL}
}

func profilePin(t *testing.T, target llmpolicy.ProfileTarget, addresses ...string) llmpolicy.ProfilePin {
	t.Helper()
	pinned := make([]netip.Addr, 0, len(addresses))
	for _, raw := range addresses {
		pinned = append(pinned, profileAddr(t, raw))
	}
	return llmpolicy.ProfilePin{Target: target, Addresses: pinned}
}

func profileInput(t *testing.T, rawURL, resolved, connected string) llmpolicy.Input {
	t.Helper()
	return llmpolicy.Input{
		URL:         rawURL,
		Transport:   llmpolicy.Direct,
		ResolvedIPs: []netip.Addr{profileAddr(t, resolved)},
		ConnectIP:   profileAddr(t, connected),
	}
}

func requireProfileError(t *testing.T, err error, want error) {
	t.Helper()
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("error = %v, want errors.Is(..., %v)", err, want)
	}
}

func TestProfileResolverAllowsPinnedV1OriginAndBlocksOtherOrigin(t *testing.T) {
	target := profileTarget(7, "openai-compatible", "qwen:7b/chat.v1", profileBaseURL)
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{
		profilePin(t, target, "127.0.0.1"),
	})
	if err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	policy, err := resolver.Resolve(target)
	if err != nil {
		t.Fatalf("valid profile target rejected: %v", err)
	}
	if decision := policy.Decide(profileInput(t, profileBaseURL+"/chat", "127.0.0.1", "127.0.0.1")); !decision.Allowed() {
		t.Fatalf("pinned local origin was blocked: %v", decision)
	}
	if decision := policy.Decide(profileInput(t, "http://other.example:8080/v1/chat", "127.0.0.1", "127.0.0.1")); decision.Allowed() {
		t.Fatalf("unregistered origin was allowed: %v", decision)
	}
}

func TestNewProfileResolverRejectsInvalidSource(t *testing.T) {
	valid := profileTarget(1, "format", "model", profileBaseURL)
	longFormat := strings.Repeat("f", 65)
	longModel := strings.Repeat("m", 1025)
	longBaseURL := profileBaseURL + "/" + strings.Repeat("p", 8192)
	invalidUTF8 := string([]byte{0xff})
	tooMany := make([]llmpolicy.ProfilePin, 65)
	for i := range tooMany {
		tooMany[i] = profilePin(t, profileTarget(int64(i+1), "format", "model", profileBaseURL), "127.0.0.1")
	}
	cases := []struct {
		name string
		pins []llmpolicy.ProfilePin
	}{
		{"nil", nil},
		{"empty", []llmpolicy.ProfilePin{}},
		{"zero-id", []llmpolicy.ProfilePin{profilePin(t, profileTarget(0, "format", "model", profileBaseURL), "127.0.0.1")}},
		{"negative-id", []llmpolicy.ProfilePin{profilePin(t, profileTarget(-1, "format", "model", profileBaseURL), "127.0.0.1")}},
		{"empty-format", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "", "model", profileBaseURL), "127.0.0.1")}},
		{"empty-model", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "", profileBaseURL), "127.0.0.1")}},
		{"empty-base-url", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", ""), "127.0.0.1")}},
		{"format-whitespace", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, " format", "model", profileBaseURL), "127.0.0.1")}},
		{"model-whitespace", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model ", profileBaseURL), "127.0.0.1")}},
		{"format-control", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format\n", "model", profileBaseURL), "127.0.0.1")}},
		{"model-control", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model\x00", profileBaseURL), "127.0.0.1")}},
		{"format-invalid-utf8", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, invalidUTF8, "model", profileBaseURL), "127.0.0.1")}},
		{"model-invalid-utf8", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", invalidUTF8, profileBaseURL), "127.0.0.1")}},
		{"format-too-long", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, longFormat, "model", profileBaseURL), "127.0.0.1")}},
		{"model-too-long", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", longModel, profileBaseURL), "127.0.0.1")}},
		{"base-url-too-long", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", longBaseURL), "127.0.0.1")}},
		{"too-many-bindings", tooMany},
		{"missing-addresses", []llmpolicy.ProfilePin{{Target: valid}}},
		{"public-address", []llmpolicy.ProfilePin{profilePin(t, valid, "8.8.8.8")}},
		{"mixed-addresses", []llmpolicy.ProfilePin{profilePin(t, valid, "127.0.0.1", "8.8.8.8")}},
		{"invalid-address", []llmpolicy.ProfilePin{{Target: valid, Addresses: []netip.Addr{{}}}}},
		{"zone-address", []llmpolicy.ProfilePin{{Target: valid, Addresses: []netip.Addr{profileAddr(t, "fe80::1%eth0")}}}},
		{"duplicate-addresses", []llmpolicy.ProfilePin{profilePin(t, valid, "127.0.0.1", "127.0.0.1")}},
		{"mapped-duplicate-addresses", []llmpolicy.ProfilePin{{Target: valid, Addresses: []netip.Addr{profileAddr(t, "192.168.0.1"), profileAddr(t, "::ffff:192.168.0.1")}}}},
		{"literal-address-mismatch", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", "http://127.0.0.1:8080/v1"), "127.0.0.2")}},
		{"public-literal-origin", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", "http://8.8.8.8:8080/v1"), "127.0.0.1")}},
		{"query", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", profileBaseURL+"?x=1"), "127.0.0.1")}},
		{"empty-query", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", profileBaseURL+"?"), "127.0.0.1")}},
		{"fragment", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", profileBaseURL+"#x"), "127.0.0.1")}},
		{"userinfo", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", "http://user:secret@model.example:8080/v1"), "127.0.0.1")}},
		{"backslash", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", "http://model.example:8080\\v1"), "127.0.0.1")}},
		{"control-url", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", "http://model.example:8080/v1\n"), "127.0.0.1")}},
		{"malformed-url", []llmpolicy.ProfilePin{profilePin(t, profileTarget(1, "format", "model", "not-a-url"), "127.0.0.1")}},
		{"duplicate-id", []llmpolicy.ProfilePin{
			profilePin(t, valid, "127.0.0.1"),
			profilePin(t, profileTarget(1, "other", "model", profileBaseURL), "127.0.0.1"),
		}},
		{"duplicate-id-same-content", []llmpolicy.ProfilePin{
			profilePin(t, valid, "127.0.0.1"),
			profilePin(t, valid, "127.0.0.1"),
		}},
		{"valid-then-invalid", []llmpolicy.ProfilePin{
			profilePin(t, valid, "127.0.0.1"),
			profilePin(t, profileTarget(2, "format", "model", profileBaseURL+"?bad=1"), "127.0.0.1"),
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			resolver, err := llmpolicy.NewProfileResolver(test.pins)
			if resolver != nil {
				t.Fatalf("invalid source returned a resolver: %v", resolver)
			}
			requireProfileError(t, err, llmpolicy.ErrInvalidProfilePolicy)
		})
	}
}

func TestNewProfileResolverAcceptsBoundaryAndPunctuationValues(t *testing.T) {
	target := profileTarget(21, strings.Repeat("f", 64), strings.Repeat("m", 1024), profileBaseURL)
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{profilePin(t, target, "127.0.0.1")})
	if err != nil {
		t.Fatalf("boundary-sized profile rejected: %v", err)
	}
	policy, err := resolver.Resolve(target)
	if err != nil || policy == nil {
		t.Fatalf("boundary-sized profile could not resolve: policy=%v err=%v", policy, err)
	}
	punctuated := profileTarget(22, "openai-compatible:v1/alt.model", "qwen:7b/chat.v1", profileBaseURL)
	resolver, err = llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{profilePin(t, punctuated, "127.0.0.1")})
	if err != nil {
		t.Fatalf("punctuated profile rejected: %v", err)
	}
	if policy, err = resolver.Resolve(punctuated); err != nil || policy == nil {
		t.Fatalf("punctuated profile could not resolve: policy=%v err=%v", policy, err)
	}
	tooManyAllowed := make([]llmpolicy.ProfilePin, 64)
	for i := range tooManyAllowed {
		tooManyAllowed[i] = profilePin(t, profileTarget(int64(i+100), "format", "model", profileBaseURL), "127.0.0.1")
	}
	lastTarget := tooManyAllowed[len(tooManyAllowed)-1].Target
	resolver, err = llmpolicy.NewProfileResolver(tooManyAllowed)
	if err != nil {
		t.Fatalf("64 profile bindings rejected: %v", err)
	}
	policy, err = resolver.Resolve(lastTarget)
	if err != nil || policy == nil {
		t.Fatalf("64th profile binding could not resolve: policy=%v err=%v", policy, err)
	}
	if decision := policy.Decide(profileInput(t, profileBaseURL+"/chat", "127.0.0.1", "127.0.0.1")); !decision.Allowed() {
		t.Fatalf("64th profile binding blocked an approved request: %v", decision)
	}
}

func TestProfileResolverRejectsMissingAndMismatchedTargets(t *testing.T) {
	target := profileTarget(9, "format", "model", profileBaseURL)
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{profilePin(t, target, "127.0.0.1")})
	if err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	missing := []struct {
		name   string
		target llmpolicy.ProfileTarget
	}{
		{"unknown-id", profileTarget(99, target.Format, target.Model, target.BaseURL)},
	}
	for _, test := range missing {
		t.Run(test.name, func(t *testing.T) {
			policy, err := resolver.Resolve(test.target)
			if policy != nil {
				t.Fatalf("missing target returned policy: %v", policy)
			}
			requireProfileError(t, err, llmpolicy.ErrProfilePolicyMissing)
		})
	}
	mismatches := []struct {
		name   string
		target llmpolicy.ProfileTarget
	}{
		{"format", profileTarget(target.ID, "other-format", target.Model, target.BaseURL)},
		{"model", profileTarget(target.ID, target.Format, "other-model", target.BaseURL)},
		{"base-url", profileTarget(target.ID, target.Format, target.Model, "http://model.example:8080/v1/encoded")},
		{"host-case", profileTarget(target.ID, target.Format, target.Model, "http://MODEL.EXAMPLE:8080/v1")},
		{"changed-port", profileTarget(target.ID, target.Format, target.Model, "http://model.example/v1")},
		{"scheme-case", profileTarget(target.ID, target.Format, target.Model, "HTTP://model.example:8080/v1")},
		{"trailing-slash", profileTarget(target.ID, target.Format, target.Model, "http://model.example:8080/v1/")},
		{"encoded-path", profileTarget(target.ID, target.Format, target.Model, "http://model.example:8080/%76%31")},
	}
	for _, test := range mismatches {
		t.Run(test.name, func(t *testing.T) {
			policy, err := resolver.Resolve(test.target)
			if policy != nil {
				t.Fatalf("mismatched target returned policy: %v", policy)
			}
			requireProfileError(t, err, llmpolicy.ErrProfileTargetMismatch)
		})
	}
	runtimeInvalid := []struct {
		name   string
		target llmpolicy.ProfileTarget
	}{
		{"zero-id", profileTarget(0, target.Format, target.Model, target.BaseURL)},
		{"negative-id", profileTarget(-1, target.Format, target.Model, target.BaseURL)},
		{"empty-format", profileTarget(target.ID, "", target.Model, target.BaseURL)},
		{"whitespace-model", profileTarget(target.ID, target.Format, " model", target.BaseURL)},
		{"empty-model", profileTarget(target.ID, target.Format, "", target.BaseURL)},
		{"empty-base-url", profileTarget(target.ID, target.Format, target.Model, "")},
		{"invalid-url", profileTarget(target.ID, target.Format, target.Model, "http://model.example:8080/v1?query")},
		{"control-url", profileTarget(target.ID, target.Format, target.Model, "http://model.example:8080/v1\n")},
		{"format-control", profileTarget(target.ID, "format\n", target.Model, target.BaseURL)},
		{"model-control", profileTarget(target.ID, target.Format, "model\x00", target.BaseURL)},
		{"format-invalid-utf8", profileTarget(target.ID, string([]byte{0xff}), target.Model, target.BaseURL)},
		{"model-invalid-utf8", profileTarget(target.ID, target.Format, string([]byte{0xff}), target.BaseURL)},
		{"format-too-long", profileTarget(target.ID, strings.Repeat("f", 65), target.Model, target.BaseURL)},
		{"model-too-long", profileTarget(target.ID, target.Format, strings.Repeat("m", 1025), target.BaseURL)},
		{"base-url-too-long", profileTarget(target.ID, target.Format, target.Model, target.BaseURL+"/"+strings.Repeat("p", 8192))},
	}
	for _, test := range runtimeInvalid {
		t.Run(test.name, func(t *testing.T) {
			policy, err := resolver.Resolve(test.target)
			if policy != nil {
				t.Fatalf("invalid runtime target returned policy: %v", policy)
			}
			requireProfileError(t, err, llmpolicy.ErrInvalidProfileTarget)
		})
	}
	var nilResolver *llmpolicy.ProfileResolver
	if policy, err := nilResolver.Resolve(target); policy != nil {
		t.Fatalf("nil resolver returned policy: %v", policy)
	} else {
		requireProfileError(t, err, llmpolicy.ErrProfilePolicyMissing)
	}
	zeroResolver := &llmpolicy.ProfileResolver{}
	if policy, err := zeroResolver.Resolve(target); policy != nil {
		t.Fatalf("zero resolver returned policy: %v", policy)
	} else {
		requireProfileError(t, err, llmpolicy.ErrProfilePolicyMissing)
	}
	if policy, err := resolver.Resolve(llmpolicy.ProfileTarget{}); policy != nil {
		t.Fatalf("zero target returned policy: %v", policy)
	} else {
		requireProfileError(t, err, llmpolicy.ErrInvalidProfileTarget)
	}
	port80Target := profileTarget(target.ID, target.Format, target.Model, "http://model.example:80/v1")
	port80Resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{profilePin(t, port80Target, "127.0.0.1")})
	if err != nil {
		t.Fatalf("valid default-port source rejected: %v", err)
	}
	if policy, err := port80Resolver.Resolve(profileTarget(port80Target.ID, port80Target.Format, port80Target.Model, "http://model.example/v1")); policy != nil {
		t.Fatalf("canonical default-port variant returned policy: %v", policy)
	} else {
		requireProfileError(t, err, llmpolicy.ErrProfileTargetMismatch)
	}
}

func TestProfileResolverSnapshotsInputsAndReturnedPolicy(t *testing.T) {
	target := profileTarget(11, "format:local/v1", "model-1", profileBaseURL)
	addresses := []netip.Addr{profileAddr(t, "127.0.0.1")}
	pins := []llmpolicy.ProfilePin{{Target: target, Addresses: addresses}}
	resolver, err := llmpolicy.NewProfileResolver(pins)
	if err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	addresses[0] = profileAddr(t, "127.0.0.2")
	pins[0].Target.Model = "mutated-model"
	pins[0].Target.BaseURL = "http://other.example:8080/v1"
	policy, err := resolver.Resolve(target)
	if err != nil || policy == nil {
		t.Fatalf("snapshot was affected by caller mutation: policy=%v err=%v", policy, err)
	}
	if decision := policy.Decide(profileInput(t, profileBaseURL+"/chat", "127.0.0.1", "127.0.0.1")); !decision.Allowed() {
		t.Fatalf("original snapshot no longer allows request: %v", decision)
	}
	*policy = llmpolicy.Policy{}
	second, err := resolver.Resolve(target)
	if err != nil || second == nil {
		t.Fatalf("resolver was affected by returned policy replacement: policy=%v err=%v", second, err)
	}
	if decision := second.Decide(profileInput(t, profileBaseURL+"/chat", "127.0.0.1", "127.0.0.1")); !decision.Allowed() {
		t.Fatalf("registry policy changed after returned policy replacement: %v", decision)
	}
}

func TestProfileResolverKeepsSameOriginPinsIsolatedByID(t *testing.T) {
	base := "http://shared.example:8080/v1"
	first := profileTarget(1, "format", "model-a", base)
	second := profileTarget(2, "format", "model-b", base)
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{
		profilePin(t, first, "127.0.0.1"),
		profilePin(t, second, "127.0.0.2"),
	})
	if err != nil {
		t.Fatalf("valid profiles rejected: %v", err)
	}
	firstPolicy, err := resolver.Resolve(first)
	if err != nil {
		t.Fatalf("first profile rejected: %v", err)
	}
	secondPolicy, err := resolver.Resolve(second)
	if err != nil {
		t.Fatalf("second profile rejected: %v", err)
	}
	firstRequest := profileInput(t, base+"/chat", "127.0.0.1", "127.0.0.1")
	secondRequest := profileInput(t, base+"/chat", "127.0.0.2", "127.0.0.2")
	if !firstPolicy.Decide(firstRequest).Allowed() || secondPolicy.Decide(firstRequest).Allowed() {
		t.Fatal("profile one did not retain its ID-specific address pin")
	}
	if !secondPolicy.Decide(secondRequest).Allowed() || firstPolicy.Decide(secondRequest).Allowed() {
		t.Fatal("profile two did not retain its ID-specific address pin")
	}
}

func TestProfileResolverConcurrentResolve(t *testing.T) {
	target := profileTarget(13, "format", "model", profileBaseURL)
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{profilePin(t, target, "127.0.0.1")})
	if err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	const workers = 32
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wait.Done()
			policy, err := resolver.Resolve(target)
			if err != nil || policy == nil {
				t.Errorf("concurrent resolve failed: policy=%v err=%v", policy, err)
				return
			}
			if decision := policy.Decide(profileInput(t, profileBaseURL+"/chat", "127.0.0.1", "127.0.0.1")); !decision.Allowed() {
				t.Errorf("concurrent resolved policy blocked request: %v", decision)
			}
			*policy = llmpolicy.Policy{}
			again, err := resolver.Resolve(target)
			if err != nil || again == nil {
				t.Errorf("resolver changed after concurrent returned policy replacement: policy=%v err=%v", again, err)
				return
			}
			if decision := again.Decide(profileInput(t, profileBaseURL+"/chat", "127.0.0.1", "127.0.0.1")); !decision.Allowed() {
				t.Errorf("registry policy changed after concurrent returned policy replacement: %v", decision)
			}
		}()
	}
	wait.Wait()
}

func TestProfileResolverRedactsMetadata(t *testing.T) {
	secret := "fixture-profile-secret"
	target := profileTarget(17, "format-"+secret, "model-"+secret, "http://secret.example:8080/v1")
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{profilePin(t, target, "127.0.0.1")})
	if err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	for _, rendered := range []string{fmt.Sprint(resolver), fmt.Sprintf("%+v", resolver), fmt.Sprintf("%#v", resolver)} {
		if strings.Contains(rendered, secret) || strings.Contains(rendered, target.BaseURL) {
			t.Fatalf("resolver formatting exposed profile metadata: %q", rendered)
		}
	}
	encoded, err := json.Marshal(resolver)
	if err != nil {
		t.Fatalf("resolver JSON failed: %v", err)
	}
	if string(encoded) != "{}" || strings.Contains(string(encoded), secret) {
		t.Fatalf("resolver JSON exposed profile metadata: %s", encoded)
	}
	_, err = resolver.Resolve(profileTarget(target.ID, target.Format+"-mismatch", target.Model, target.BaseURL))
	if err == nil || !errors.Is(err, llmpolicy.ErrProfileTargetMismatch) {
		t.Fatalf("mismatched target returned wrong error: %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), target.BaseURL) {
		t.Fatalf("resolver error exposed profile metadata: %v", err)
	}
}

package kgfixture

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

const sentinel = "kgfx-SENTINEL-canary-9d1e"

func validRegistry() map[string]any {
	return map[string]any{
		"format":           FormatV1,
		"snapshot_id":      "kgfx-test-1",
		"run_id":           "prep-run-1",
		"reset_generation": "reset-1",
		"generation_ref":   "gen-1",
		"instance_ref":     "target-1",
		"principals":       []any{map[string]any{"label": "account-a"}, map[string]any{"label": "account-b"}},
		"resources": []any{
			map[string]any{
				"resource_ref": "note:1001",
				"owner_ref":    "account-a",
				"policies": []any{map[string]any{
					"evaluated_ref": "account-b", "expected_access": "deny", "sharing": "resolved_none",
					"delegation": "unknown", "valid_from": "2026-11-05T00:00:00Z", "valid_until": "2026-11-06T00:00:00.5Z",
				}},
				"canaries": []any{
					map[string]any{"field": "body", "value": sentinel, "trivial": false},
					map[string]any{"field": "id", "value": "1001", "trivial": true},
				},
			},
			map[string]any{
				"resource_ref": "note:1002",
				"owner_ref":    "account-b",
				"policies":     []any{},
				"canaries":     []any{},
			},
		},
	}
}

func encode(t *testing.T, registry map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func resourceAt(registry map[string]any, i int) map[string]any {
	return registry["resources"].([]any)[i].(map[string]any)
}

func policyOf(registry map[string]any) map[string]any {
	return resourceAt(registry, 0)["policies"].([]any)[0].(map[string]any)
}

func canaryOf(registry map[string]any) map[string]any {
	return resourceAt(registry, 0)["canaries"].([]any)[0].(map[string]any)
}

func mustParse(t *testing.T, body []byte) *Snapshot {
	t.Helper()
	snapshot, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return snapshot
}

func codeOf(err error) Code {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

var (
	accountA = idor.AccountRef{Label: "account-a"}
	accountB = idor.AccountRef{Label: "account-b"}
)

func TestParseFreezesRegistry(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	if s.Ref() != "kgfx-test-1" || s.RunID() != "prep-run-1" || s.ResetGeneration() != "reset-1" ||
		s.GenerationRef() != "gen-1" || s.InstanceRef() != "target-1" {
		t.Fatalf("identity %q %q %q %q %q", s.Ref(), s.RunID(), s.ResetGeneration(), s.GenerationRef(), s.InstanceRef())
	}
	digest := s.Digest()
	version := regexp.MustCompile(`^kg-fixture/v1:kgfx-test-1:[0-9a-f]{64}$`)
	if !version.MatchString(s.PolicyVersion()) || !strings.HasSuffix(s.PolicyVersion(), ":"+hexOf(digest)) {
		t.Fatalf("policy version %q", s.PolicyVersion())
	}
	if got := s.Principals(); len(got) != 2 || got[0] != "account-a" || got[1] != "account-b" {
		t.Fatalf("principals %v", got)
	}
	if got := s.ResourceRefs(); len(got) != 2 || got[0] != "note:1001" || got[1] != "note:1002" {
		t.Fatalf("resources %v", got)
	}
	policy, ok := s.ResolvePolicy("note:1001", accountA, accountB)
	want := oracle.PolicyAssertion{
		ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolutionUnknown,
		HasRegisteredSource: true, HintMatchesCanonical: true,
		ValidFrom:  time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		ValidUntil: time.Date(2026, 11, 6, 0, 0, 0, 500_000_000, time.UTC),
	}
	if !ok || policy != want {
		t.Fatalf("policy %+v ok=%v", policy, ok)
	}
	markers, ok := s.Canaries("note:1001", accountA)
	if !ok || len(markers) != 2 || markers[0] != (idor.Marker{Field: "body", Value: sentinel}) ||
		markers[1] != (idor.Marker{Field: "id", Value: "1001", Trivial: true}) {
		t.Fatalf("canaries %+v ok=%v", markers, ok)
	}
	if s.Err() != nil {
		t.Fatalf("fresh snapshot err %v", s.Err())
	}
}

func hexOf(digest [32]byte) string { return hex.EncodeToString(digest[:]) }

func TestDigestIsDeterministicFromContent(t *testing.T) {
	base := mustParse(t, encode(t, validRegistry()))
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, encode(t, validRegistry()), "", "    "); err != nil {
		t.Fatal(err)
	}
	aliased := strings.Replace(pretty.String(), `"format"`, `"\u0066ormat"`, 1)
	if got := mustParse(t, []byte(aliased)); got.Digest() != base.Digest() || got.PolicyVersion() != base.PolicyVersion() {
		t.Fatal("whitespace or an escaped key alias changed the digest")
	}
	changed := validRegistry()
	canaryOf(changed)["value"] = sentinel[:len(sentinel)-1] + "f"
	if mustParse(t, encode(t, changed)).Digest() == base.Digest() {
		t.Fatal("a one-byte content change kept the digest")
	}
}

func TestParseRejects(t *testing.T) {
	type mutation func(map[string]any)
	cases := []struct {
		name string
		edit mutation
		body string
		want Code
	}{
		{name: "empty input", body: "-", want: CodeUnreadable},
		{name: "oversized input", body: "+", want: CodeUnreadable},
		{name: "not JSON", body: "{", want: CodeFormat},
		{name: "top-level array", body: "[]", want: CodeFormat},
		{name: "trailing data", body: "@trailing", want: CodeFormat},
		{name: "invalid UTF-8 byte", body: "@utf8", want: CodeFormat},
		{name: "duplicate root key", body: "@dupkey", want: CodeDuplicate},
		{name: "duplicate key by escape", body: "@dupalias", want: CodeDuplicate},
		{name: "duplicate nested key", body: "@dupnested", want: CodeDuplicate},
		{name: "fraction number", body: "@float", want: CodeFormat},
		{name: "nesting limit", body: "@deep", want: CodeFormat},
		{name: "unknown root field", edit: func(m map[string]any) { m["note"] = "x" }, want: CodeUnknownField},
		{name: "unknown principal field", edit: func(m map[string]any) { m["principals"].([]any)[0].(map[string]any)["role"] = "x" }, want: CodeUnknownField},
		{name: "unknown resource field", edit: func(m map[string]any) { resourceAt(m, 0)["url"] = "x" }, want: CodeUnknownField},
		{name: "unknown policy field", edit: func(m map[string]any) { policyOf(m)["approval_ref"] = "x" }, want: CodeUnknownField},
		{name: "unknown canary field", edit: func(m map[string]any) { canaryOf(m)["note"] = "x" }, want: CodeUnknownField},
		{name: "wrong format", edit: func(m map[string]any) { m["format"] = "abc2lab.kg-fixture-registry/v2" }, want: CodeFormat},
		{name: "empty string", edit: func(m map[string]any) { m["run_id"] = "" }, want: CodeFormat},
		{name: "null value", edit: func(m map[string]any) { m["instance_ref"] = nil }, want: CodeFormat},
		{name: "integer in string field", edit: func(m map[string]any) { m["snapshot_id"] = 7 }, want: CodeFormat},
		{name: "raw non-ASCII", edit: func(m map[string]any) { canaryOf(m)["value"] = "kgfx-é" }, want: CodeFormat},
		{name: "control character", edit: func(m map[string]any) { canaryOf(m)["value"] = "kgfx\tx" }, want: CodeFormat},
		{name: "DEL character", edit: func(m map[string]any) { canaryOf(m)["field"] = "body\x7f" }, want: CodeFormat},
		{name: "snapshot id over 49 bytes", edit: func(m map[string]any) { m["snapshot_id"] = strings.Repeat("s", maxSnapshotIDBytes+1) }, want: CodeRefFormat},
		{name: "snapshot id with scheme", edit: func(m map[string]any) { m["snapshot_id"] = "a://b" }, want: CodeRefFormat},
		{name: "run id bad first char", edit: func(m map[string]any) { m["run_id"] = "-run" }, want: CodeRefFormat},
		{name: "reset generation bad char", edit: func(m map[string]any) { m["reset_generation"] = "reset 1" }, want: CodeRefFormat},
		{name: "generation ref too long", edit: func(m map[string]any) { m["generation_ref"] = strings.Repeat("g", maxRefBytes+1) }, want: CodeRefFormat},
		{name: "instance ref bad char", edit: func(m map[string]any) { m["instance_ref"] = "target#1" }, want: CodeRefFormat},
		{name: "no principals", edit: func(m map[string]any) { m["principals"] = []any{} }, want: CodeFormat},
		{name: "principal not an object", edit: func(m map[string]any) { m["principals"] = []any{"account-a"} }, want: CodeFormat},
		{name: "duplicate principal", edit: func(m map[string]any) {
			m["principals"] = append(m["principals"].([]any), map[string]any{"label": "account-a"})
		}, want: CodeDuplicate},
		{name: "principal label grammar", edit: func(m map[string]any) { m["principals"].([]any)[1].(map[string]any)["label"] = "account b" }, want: CodeRefFormat},
		{name: "no resources", edit: func(m map[string]any) { m["resources"] = []any{} }, want: CodeFormat},
		{name: "resource ref without colon", edit: func(m map[string]any) { resourceAt(m, 0)["resource_ref"] = "note1001" }, want: CodeRefFormat},
		{name: "resource ref empty type", edit: func(m map[string]any) { resourceAt(m, 0)["resource_ref"] = ":1001" }, want: CodeRefFormat},
		{name: "resource ref empty id", edit: func(m map[string]any) { resourceAt(m, 0)["resource_ref"] = "note:" }, want: CodeRefFormat},
		{name: "duplicate resource ref", edit: func(m map[string]any) { resourceAt(m, 1)["resource_ref"] = "note:1001" }, want: CodeDuplicate},
		{name: "owner not a principal", edit: func(m map[string]any) { resourceAt(m, 0)["owner_ref"] = "account-c" }, want: CodeFormat},
		{name: "evaluated not a principal", edit: func(m map[string]any) { policyOf(m)["evaluated_ref"] = "account-c" }, want: CodeFormat},
		{name: "duplicate evaluated policy", edit: func(m map[string]any) {
			r := resourceAt(m, 0)
			r["policies"] = append(r["policies"].([]any), policyOf(m))
		}, want: CodeDuplicate},
		{name: "expected access value", edit: func(m map[string]any) { policyOf(m)["expected_access"] = "allowed" }, want: CodeFormat},
		{name: "sharing value", edit: func(m map[string]any) { policyOf(m)["sharing"] = "none" }, want: CodeFormat},
		{name: "delegation value", edit: func(m map[string]any) { policyOf(m)["delegation"] = "" }, want: CodeFormat},
		{name: "time offset not Z", edit: func(m map[string]any) { policyOf(m)["valid_from"] = "2026-11-05T09:00:00+09:00" }, want: CodeFormat},
		{name: "time lowercase z", edit: func(m map[string]any) { policyOf(m)["valid_from"] = "2026-11-05T00:00:00z" }, want: CodeFormat},
		{name: "time without seconds", edit: func(m map[string]any) { policyOf(m)["valid_until"] = "2026-11-06T00:00Z" }, want: CodeFormat},
		{name: "time out of range", edit: func(m map[string]any) { policyOf(m)["valid_until"] = "2026-13-06T00:00:00Z" }, want: CodeFormat},
		{name: "time year zero", edit: func(m map[string]any) { policyOf(m)["valid_from"] = "0000-01-01T00:00:00Z" }, want: CodeFormat},
		{name: "validity not increasing", edit: func(m map[string]any) { policyOf(m)["valid_until"] = "2026-11-05T00:00:00Z" }, want: CodeFormat},
		{name: "policies not an array", edit: func(m map[string]any) { resourceAt(m, 0)["policies"] = map[string]any{} }, want: CodeFormat},
		{name: "canary trivial missing", edit: func(m map[string]any) { delete(canaryOf(m), "trivial") }, want: CodeTrivialMissing},
		{name: "canary trivial not a bool", edit: func(m map[string]any) { canaryOf(m)["trivial"] = "false" }, want: CodeFormat},
		{name: "canary empty value", edit: func(m map[string]any) { canaryOf(m)["value"] = "" }, want: CodeFormat},
		{name: "canary empty field", edit: func(m map[string]any) { canaryOf(m)["field"] = "" }, want: CodeFormat},
		{name: "canary value repeated on another resource", edit: func(m map[string]any) {
			resourceAt(m, 1)["canaries"] = []any{map[string]any{"field": "body", "value": sentinel, "trivial": false}}
		}, want: CodeCanaryNotUnique},
		{name: "canary field repeated on the same resource", edit: func(m map[string]any) {
			resourceAt(m, 0)["canaries"].([]any)[1].(map[string]any)["field"] = "body"
		}, want: CodeDuplicate},
		{name: "canary value repeated on the same resource", edit: func(m map[string]any) {
			resourceAt(m, 0)["canaries"].([]any)[1].(map[string]any)["value"] = sentinel
		}, want: CodeCanaryNotUnique},
	}
	for _, key := range rootFields {
		key := key
		cases = append(cases, struct {
			name string
			edit mutation
			body string
			want Code
		}{name: "missing root " + key, edit: func(m map[string]any) { delete(m, key) }, want: CodeFormat})
	}
	for _, key := range policyFields {
		key := key
		cases = append(cases, struct {
			name string
			edit mutation
			body string
			want Code
		}{name: "missing policy " + key, edit: func(m map[string]any) { delete(policyOf(m), key) }, want: CodeFormat})
	}
	for _, key := range []string{"resource_ref", "owner_ref", "policies", "canaries"} {
		key := key
		cases = append(cases, struct {
			name string
			edit mutation
			body string
			want Code
		}{name: "missing resource " + key, edit: func(m map[string]any) { delete(resourceAt(m, 0), key) }, want: CodeFormat})
	}
	valid := string(encode(t, validRegistry()))
	raw := map[string]string{
		"-":          "",
		"+":          `{"format":"` + strings.Repeat("x", MaxRegistryBytes) + `"}`,
		"@trailing":  valid + " {}",
		"@utf8":      strings.Replace(valid, sentinel, "kgfx-\xff", 1),
		"@dupkey":    strings.Replace(valid, `{"format"`, `{"format":"x","format"`, 1),
		"@dupalias":  strings.Replace(valid, `{"format"`, `{"\u0066ormat":"x","format"`, 1),
		"@dupnested": strings.Replace(valid, `"trivial":false`, `"trivial":false,"trivial":true`, 1),
		"@float":     strings.Replace(valid, `{"format"`, `{"x":1.5,"format"`, 1),
		"@deep":      strings.Replace(valid, `{"format"`, `{"x":[[[[[[[[["deep"]]]]]]]]],"format"`, 1),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body []byte
			if c.edit != nil {
				registry := validRegistry()
				c.edit(registry)
				body = encode(t, registry)
			} else if replaced, ok := raw[c.body]; ok {
				if replaced == valid {
					t.Fatal("raw case did not change the input")
				}
				body = []byte(replaced)
			} else {
				body = []byte(c.body)
			}
			snapshot, err := Parse(body)
			if snapshot != nil || codeOf(err) != c.want {
				t.Fatalf("Parse = %v, %v; want code %s", snapshot, err, c.want)
			}
			if err.Error() != "kgfixture: "+string(c.want) {
				t.Fatalf("error text %q carries more than the code", err.Error())
			}
		})
	}
}

func TestBoundaryInputsAccepted(t *testing.T) {
	registry := validRegistry()
	// The same field on another resource is a different canary key and stays allowed.
	resourceAt(registry, 1)["canaries"] = []any{map[string]any{"field": "body", "value": "kgfx-other-body", "trivial": false}}
	registry["snapshot_id"] = strings.Repeat("s", maxSnapshotIDBytes)
	registry["generation_ref"] = strings.Repeat("g", maxRefBytes)
	canaryOf(registry)["value"] = `say "hi" \ ok ~`
	s := mustParse(t, encode(t, registry))
	if len(s.PolicyVersion()) != maxRefBytes || !validRef(s.PolicyVersion()) {
		t.Fatalf("policy version %d bytes valid=%v", len(s.PolicyVersion()), validRef(s.PolicyVersion()))
	}
}

func TestResolvePolicyMatchesExactly(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	for _, c := range []struct {
		name             string
		ref              string
		owner, evaluated idor.AccountRef
	}{
		{"unknown resource", "note:9999", accountA, accountB},
		{"prefix of a resource", "note:100", accountA, accountB},
		{"case variant", "Note:1001", accountA, accountB},
		{"trailing space", "note:1001 ", accountA, accountB},
		{"owner mismatch", "note:1001", accountB, accountB},
		{"owner label case", "note:1001", idor.AccountRef{Label: "Account-a"}, accountB},
		{"no policy for evaluated", "note:1001", accountA, accountA},
		{"resource without policies", "note:1002", accountB, accountA},
	} {
		if policy, ok := s.ResolvePolicy(c.ref, c.owner, c.evaluated); ok || policy != (oracle.PolicyAssertion{}) {
			t.Errorf("%s: ResolvePolicy = %+v, %v", c.name, policy, ok)
		}
	}
}

func TestCanariesMatchExactly(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	for _, c := range []struct {
		name  string
		ref   string
		owner idor.AccountRef
	}{
		{"unknown resource", "note:9999", accountA},
		{"partial id", "note:10", accountA},
		{"owner mismatch", "note:1001", accountB},
		{"no canaries", "note:1002", accountB},
	} {
		if markers, ok := s.Canaries(c.ref, c.owner); ok || markers != nil {
			t.Errorf("%s: Canaries = %v, %v", c.name, markers, ok)
		}
	}
	if markers, ok := s.Markers("note:1002"); !ok || len(markers) != 0 {
		t.Fatalf("Markers on a resource without canaries = %v, %v", markers, ok)
	}
	if _, ok := s.Markers("note:9999"); ok {
		t.Fatal("Markers on an unknown resource")
	}
}

func TestReturnedSlicesAreCopies(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	markers, _ := s.Canaries("note:1001", accountA)
	markers[0].Trivial = true
	again, _ := s.Canaries("note:1001", accountA)
	viaMarkers, _ := s.Markers("note:1001")
	viaMarkers[1].Value = "changed"
	principals := s.Principals()
	principals[0] = "changed"
	refs := s.ResourceRefs()
	refs[0] = "changed"
	final, _ := s.Markers("note:1001")
	if again[0].Trivial || final[1].Value != "1001" || s.Principals()[0] != "account-a" || s.ResourceRefs()[0] != "note:1001" {
		t.Fatal("caller edits reached the snapshot")
	}
}

func TestInvalidateEndsEveryLookup(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	owner, cross := s, s // the owner and cross lookups share one snapshot (contract §4)
	if _, ok := owner.ResolvePolicy("note:1001", accountA, accountB); !ok {
		t.Fatal("lookup failed before invalidation")
	}
	s.Invalidate()
	if _, ok := cross.ResolvePolicy("note:1001", accountA, accountB); ok {
		t.Fatal("ResolvePolicy after Invalidate")
	}
	if _, ok := cross.Canaries("note:1001", accountA); ok {
		t.Fatal("Canaries after Invalidate")
	}
	if _, ok := owner.Markers("note:1001"); ok {
		t.Fatal("Markers after Invalidate")
	}
	if codeOf(s.Err()) != CodeSnapshotInvalid || s.Err().Error() != "kgfixture: snapshot_invalid" {
		t.Fatalf("Err = %v", s.Err())
	}
	if s.PolicyVersion() == "" || s.Ref() != "kgfx-test-1" {
		t.Fatal("identity values must stay readable for the record after invalidation")
	}
}

func TestConcurrentLookupsAndInvalidate(t *testing.T) {
	s := mustParse(t, encode(t, validRegistry()))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.ResolvePolicy("note:1001", accountA, accountB)
				s.Canaries("note:1001", accountA)
				s.Markers("note:1001")
			}
		}()
	}
	s.Invalidate()
	wg.Wait()
	if _, ok := s.ResolvePolicy("note:1001", accountA, accountB); ok {
		t.Fatal("lookup succeeded after Invalidate")
	}
}

// Errors carry the code only: no canary value, key name or other file content.
func TestErrorsCarryNoContent(t *testing.T) {
	edits := []func(map[string]any){
		func(m map[string]any) { m[sentinel] = "x" },
		func(m map[string]any) { m["snapshot_id"] = sentinel + " bad" },
		func(m map[string]any) {
			resourceAt(m, 1)["canaries"] = []any{map[string]any{"field": "f", "value": sentinel, "trivial": true}}
		},
		func(m map[string]any) { canaryOf(m)["value"] = sentinel + "\n" },
	}
	for i, edit := range edits {
		registry := validRegistry()
		edit(registry)
		_, err := Parse(encode(t, registry))
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), sentinel) {
			t.Errorf("case %d: err %v", i, err)
		}
	}
}

// The policy version must pass the reference checks of the packages that store
// it; pin our grammar to theirs so a change on either side fails here.
func TestReferenceGrammarMatchesConsumers(t *testing.T) {
	for _, path := range []string{"../evidencebind/check.go", "../transmit/config.go"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(source, []byte("regexp.MustCompile(`"+refPattern.String()+"`)")) {
			t.Errorf("%s no longer uses the reference grammar %s", path, refPattern.String())
		}
	}
	evidencebind, err := os.ReadFile("../evidencebind/check.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(evidencebind, []byte(`!strings.Contains(ref, "://")`)) {
		t.Error("evidencebind no longer bans :// in references")
	}
}

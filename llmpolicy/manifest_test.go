package llmpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

const manifestValid = `{"version":1,"profiles":[{"profile_id":7,"format":"openai","model":"local:test","base_url":"http://model.internal:11434/v1","addresses":["10.0.0.1"]}]}`

func manifestProfile() map[string]any {
	return map[string]any{"profile_id": 7, "format": "openai", "model": "local:test", "base_url": "http://model.internal:11434/v1", "addresses": []string{"10.0.0.1"}}
}

func manifestEncode(t *testing.T, profiles ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 1, "profiles": profiles})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func manifestRejected(t *testing.T, body []byte) {
	t.Helper()
	resolver, err := ParseProfileManifest(body)
	if resolver != nil || err != ErrInvalidProfileManifest || !errors.Is(err, ErrInvalidProfileManifest) {
		t.Fatalf("expected nil resolver and fixed error; got %v %v", resolver, err)
	}
}

func manifestAllowed(t *testing.T, body []byte, target ProfileTarget, ip netip.Addr) {
	t.Helper()
	resolver, err := ParseProfileManifest(body)
	if err != nil || resolver == nil {
		t.Fatalf("parse failed: %v", err)
	}
	policy, err := resolver.Resolve(target)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if decision := policy.Decide(Input{URL: target.BaseURL + "/chat/completions", Transport: Direct, ResolvedIPs: []netip.Addr{ip}, ConnectIP: ip}); !decision.Allowed() {
		t.Fatalf("positive control denied: %s", decision.Reason())
	}
}

func TestProfileManifestValid(t *testing.T) {
	target := ProfileTarget{ID: 7, Format: "openai", Model: "local:test", BaseURL: "http://model.internal:11434/v1"}
	for _, body := range []string{manifestValid, " \n\t" + manifestValid + "\r\n", strings.Replace(manifestValid, `"version":1`, `"\u0076ersion":1`, 1)} {
		manifestAllowed(t, []byte(body), target, netip.MustParseAddr("10.0.0.1"))
	}
	p := manifestProfile()
	p["profile_id"] = int64(math.MaxInt64)
	target.ID = math.MaxInt64
	manifestAllowed(t, manifestEncode(t, p), target, netip.MustParseAddr("10.0.0.1"))
}

func TestProfileManifestRejectsStructure(t *testing.T) {
	cases := map[string]string{
		"empty": "", "whitespace": " \r\n", "null": "null", "array": "[]", "scalar": "1", "object": "{}",
		"missing version": `{"profiles":[]}`, "missing profiles": `{"version":1}`,
		"version null": `{"version":null,"profiles":[]}`, "version string": `{"version":"1","profiles":[]}`,
		"version float":       strings.Replace(manifestValid, `"version":1`, `"version":1.0`, 1),
		"version exponent":    strings.Replace(manifestValid, `"version":1`, `"version":1e0`, 1),
		"version unsupported": strings.Replace(manifestValid, `"version":1`, `"version":2`, 1),
		"profiles null":       `{"version":1,"profiles":null}`, "profiles object": `{"version":1,"profiles":{}}`,
		"profiles empty": `{"version":1,"profiles":[]}`, "profile null": `{"version":1,"profiles":[null]}`,
		"profile array": `{"version":1,"profiles":[[]]}`, "profile string": `{"version":1,"profiles":["data"]}`,
		"syntax": manifestValid[:len(manifestValid)-1], "trailing object": manifestValid + `{}`, "trailing null": manifestValid + ` null`,
		"trailing garbage": manifestValid + ` unexpected`, "bom": "\xef\xbb\xbf" + manifestValid,
		"unknown root":              strings.Replace(manifestValid, `"version":1`, `"enabled":true,"version":1`, 1),
		"case root":                 strings.Replace(manifestValid, `"profiles"`, `"Profiles"`, 1),
		"duplicate root":            strings.Replace(manifestValid, `"version":1`, `"version":1,"version":1`, 1),
		"escaped duplicate root":    strings.Replace(manifestValid, `"version":1`, `"version":1,"\u0076ersion":1`, 1),
		"unknown profile":           strings.Replace(manifestValid, `"profile_id":7`, `"api_key":"SYNTHETIC_SECRET","profile_id":7`, 1),
		"case profile":              strings.Replace(manifestValid, `"model"`, `"Model"`, 1),
		"duplicate profile":         strings.Replace(manifestValid, `"model":"local:test"`, `"model":"local:test","model":"other"`, 1),
		"escaped duplicate profile": strings.Replace(manifestValid, `"profile_id":7`, `"profile_id":7,"profile_\u0069d":7`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) { manifestRejected(t, []byte(body)) })
	}
}

func TestProfileManifestRequiredFieldsAndTypes(t *testing.T) {
	for _, key := range []string{"profile_id", "format", "model", "base_url", "addresses"} {
		t.Run("missing/"+key, func(t *testing.T) { p := manifestProfile(); delete(p, key); manifestRejected(t, manifestEncode(t, p)) })
		for _, value := range []any{nil, true, []any{}, map[string]any{}} {
			t.Run(fmt.Sprintf("type/%s/%T", key, value), func(t *testing.T) { p := manifestProfile(); p[key] = value; manifestRejected(t, manifestEncode(t, p)) })
		}
	}
	for _, id := range []any{0, -1, 1.5, "7", json.Number("1e1"), json.Number("9223372036854775808")} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			p := manifestProfile()
			p["profile_id"] = id
			manifestRejected(t, manifestEncode(t, p))
		})
	}
	for _, key := range []string{"format", "model", "base_url"} {
		t.Run("empty/"+key, func(t *testing.T) { p := manifestProfile(); p[key] = ""; manifestRejected(t, manifestEncode(t, p)) })
	}
}

func TestProfileManifestRejectsPolicyFailures(t *testing.T) {
	for name, values := range map[string]any{
		"empty": []string{}, "null": []string(nil), "public": []string{"8.8.8.8"}, "invalid": []string{"not-an-ip"},
		"duplicate": []string{"10.0.0.1", "10.0.0.1"}, "normalized duplicate": []string{"10.0.0.1", "::ffff:10.0.0.1"},
		"zone": []string{"fe80::1%eth0"}, "nonstring": []any{7}, "scalar": "10.0.0.1",
	} {
		t.Run(name, func(t *testing.T) {
			p := manifestProfile()
			p["addresses"] = values
			manifestRejected(t, manifestEncode(t, p))
		})
	}
	for _, url := range []string{"https://example.com?key=synthetic", "http://user:password@model.internal", "http://10.0.0.2:11434", "http://model.internal:11434?"} {
		t.Run(url, func(t *testing.T) {
			p := manifestProfile()
			p["base_url"] = url
			manifestRejected(t, manifestEncode(t, p))
		})
	}
	t.Run("duplicate ID", func(t *testing.T) { manifestRejected(t, manifestEncode(t, manifestProfile(), manifestProfile())) })
	t.Run("bad later profile", func(t *testing.T) {
		bad := manifestProfile()
		bad["profile_id"] = 8
		bad["addresses"] = []string{"8.8.8.8"}
		manifestRejected(t, manifestEncode(t, manifestProfile(), bad))
	})
}

func TestProfileManifestLimits(t *testing.T) {
	for _, tc := range []struct {
		key   string
		limit int
	}{{"format", maxProfileFormatBytes}, {"model", maxProfileModelBytes}, {"base_url", maxURLBytes}} {
		t.Run(tc.key, func(t *testing.T) {
			p := manifestProfile()
			value := strings.Repeat("x", tc.limit)
			if tc.key == "base_url" {
				prefix := "http://model.internal:11434/"
				value = prefix + strings.Repeat("x", tc.limit-len(prefix))
			}
			p[tc.key] = value
			target := ProfileTarget{ID: 7, Format: p["format"].(string), Model: p["model"].(string), BaseURL: p["base_url"].(string)}
			// The exact maximum BaseURL is a valid target; request path growth is separate.
			r, err := ParseProfileManifest(manifestEncode(t, p))
			if err != nil {
				t.Fatalf("at limit: %v", err)
			}
			if _, err = r.Resolve(target); err != nil {
				t.Fatalf("at limit resolve: %v", err)
			}
			p[tc.key] = value + "x"
			manifestRejected(t, manifestEncode(t, p))
		})
	}
	t.Run("profiles", func(t *testing.T) {
		profiles := make([]map[string]any, maxProfileBindings+1)
		for i := range profiles {
			profiles[i] = manifestProfile()
			profiles[i]["profile_id"] = i + 1
		}
		if _, err := ParseProfileManifest(manifestEncode(t, profiles[:maxProfileBindings]...)); err != nil {
			t.Fatalf("at count limit: %v", err)
		}
		manifestRejected(t, manifestEncode(t, profiles...))
	})
	t.Run("addresses", func(t *testing.T) {
		ips := make([]string, maxAddresses+1)
		for i := range ips {
			ips[i] = fmt.Sprintf("10.0.0.%d", i+1)
		}
		p := manifestProfile()
		p["addresses"] = ips[:maxAddresses]
		if _, err := ParseProfileManifest(manifestEncode(t, p)); err != nil {
			t.Fatalf("at address limit: %v", err)
		}
		p["addresses"] = ips
		manifestRejected(t, manifestEncode(t, p))
	})
	t.Run("bytes", func(t *testing.T) {
		body := append([]byte(manifestValid), bytes.Repeat([]byte(" "), MaxProfileManifestBytes-len(manifestValid))...)
		if _, err := ParseProfileManifest(body); err != nil {
			t.Fatalf("at byte limit: %v", err)
		}
		manifestRejected(t, append(body, ' '))
	})
}

func TestProfileManifestEncoding(t *testing.T) {
	for _, model := range []string{`\ud800`, `\udc00`, `\ud800x`, `\ud800\u0041`, `\ud800\ud800`, `\uZZZZ`, "\xff"} {
		t.Run(fmt.Sprintf("%q", model), func(t *testing.T) {
			body := strings.Replace(manifestValid, "local:test", model, 1)
			manifestRejected(t, []byte(body))
		})
	}
	for _, tc := range []struct{ encoded, decoded string }{{`model-\ud83d\ude00`, "model-😀"}, {`model-\ufffd`, "model-�"}, {`model-\\ud800`, `model-\ud800`}} {
		t.Run(tc.encoded, func(t *testing.T) {
			body := []byte(strings.Replace(manifestValid, "local:test", tc.encoded, 1))
			manifestAllowed(t, body, ProfileTarget{ID: 7, Format: "openai", Model: tc.decoded, BaseURL: "http://model.internal:11434/v1"}, netip.MustParseAddr("10.0.0.1"))
		})
	}
}

func TestProfileManifestIsolationAndSafeErrors(t *testing.T) {
	body := []byte(manifestValid)
	snapshot := bytes.Clone(body)
	r, err := ParseProfileManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, snapshot) {
		t.Fatal("parser modified input")
	}
	clear(body)
	target := ProfileTarget{ID: 7, Format: "openai", Model: "local:test", BaseURL: "http://model.internal:11434/v1"}
	if _, err := r.Resolve(target); err != nil {
		t.Fatal("registry retained mutable input")
	}
	changed := target
	changed.Model = "other"
	if _, err := r.Resolve(changed); err != ErrProfileTargetMismatch {
		t.Fatal("tuple binding lost")
	}
	bad := strings.Replace(manifestValid, `"addresses":["10.0.0.1"]`, `"addresses":["SECRET_INVALID_ADDRESS"]`, 1)
	_, err = ParseProfileManifest([]byte(bad))
	if err != ErrInvalidProfileManifest {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, output := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		for _, raw := range []string{"SECRET_INVALID_ADDRESS", "model.internal", "local:test", "10.0.0.1"} {
			if strings.Contains(output, raw) {
				t.Fatal("error contains input metadata")
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolver, err := ParseProfileManifest(snapshot)
			if err != nil {
				t.Errorf("concurrent parse: %v", err)
				return
			}
			if _, err := resolver.Resolve(target); err != nil {
				t.Errorf("concurrent resolve: %v", err)
			}
		}()
	}
	wg.Wait()
}

func FuzzParseProfileManifest(f *testing.F) {
	for _, body := range []string{manifestValid, `{}`, `null`, manifestValid + `{}`, strings.Replace(manifestValid, "local:test", `\ud800`, 1)} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r, err := ParseProfileManifest(body)
		if err != nil {
			if r != nil || err != ErrInvalidProfileManifest {
				t.Fatal("partial registry or unsafe error")
			}
			return
		}
		if r == nil {
			t.Fatal("success without registry")
		}
	})
}

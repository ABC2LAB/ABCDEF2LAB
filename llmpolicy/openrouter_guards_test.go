package llmpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestOpenRouterDialRejectsUnpinnedAddressBeforeLookup(t *testing.T) {
	for _, tc := range []struct {
		network, address string
		allowed          bool
	}{
		{"tcp", "openrouter.ai:443", true}, {"tcp", "other.invalid:443", false}, {"tcp", "openrouter.ai:80", false}, {"udp", "openrouter.ai:443", false},
	} {
		t.Run(tc.network+"/"+tc.address, func(t *testing.T) {
			lookups, dials := 0, 0
			conn, err := openRouterDialWith(t.Context(), tc.network, tc.address, func(context.Context) ([]netip.Addr, error) {
				lookups++
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}, func(_ context.Context, network, address string) (net.Conn, error) {
				dials++
				if network != "tcp" || address != "8.8.8.8:443" {
					t.Fatal("unchecked dial destination", network, address)
				}
				a, b := net.Pipe()
				t.Cleanup(func() { a.Close(); b.Close() })
				return openRouterPeerConn{Conn: a, peer: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}}, nil
			})
			if tc.allowed {
				if err != nil || conn == nil || lookups != 1 || dials != 1 {
					t.Fatal("pinned control failed", err, lookups, dials)
				}
			} else if !errors.Is(err, ErrOpenRouterPolicy) || conn != nil || lookups != 0 || dials != 0 {
				t.Fatal("foreign address reached lookup/dial", err, lookups, dials)
			}
		})
	}
}

func TestOpenRouterWireGuardsWithOtherwiseValidMessages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"control", nil},
		{"model", func(body map[string]any) { body["model"] = "other/model" }},
		{"tool-choice", func(body map[string]any) { body["tool_choice"] = "auto" }},
		{"unknown-key", func(body map[string]any) { body["user"] = "synthetic-user" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := openRouterFixture(t)
			hits := 0
			client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { hits++; return openRouterResponse(openRouterReply), nil }))
			req := openRouterRequest()
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			req.Body.Close()
			var body map[string]any
			if err = json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(body)
			}
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req.Body = io.NopCloser(strings.NewReader(string(raw)))
			resp, err := client.Do(req)
			if tc.change == nil {
				if err != nil || hits != 1 || p.Snapshot().Requests != 1 {
					t.Fatal("normal message control rejected", err, hits)
				}
				resp.Body.Close()
			} else if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || hits != 0 || p.Snapshot().Requests != 0 {
				t.Fatal("invalid field reached wire or reservation", err, hits, p.Snapshot().Requests)
			}
		})
	}
}

func TestOpenRouterRebuildsOnlyCredentialAndContentTypeHeaders(t *testing.T) {
	p := openRouterFixture(t)
	hits := 0
	client := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		hits++
		if len(r.Header) != 2 || r.Header.Get("Authorization") != "Bearer synthetic-credential" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("outbound header projection changed", r.Header)
		}
		return openRouterResponse(openRouterReply), nil
	}))
	req := openRouterRequest()
	req.Header.Set("Authorization", "Bearer synthetic-credential")
	req.Header.Set("Content-Type", "text/plain")
	for _, key := range []string{"Cookie", "X-Session", "X-Forwarded-For", "HTTP-Referer", "X-Title"} {
		req.Header.Set(key, "synthetic-private-header")
	}
	resp, err := client.Do(req)
	if err != nil || hits != 1 {
		t.Fatal("valid header projection rejected", err, hits)
	}
	resp.Body.Close()
	if req.Header.Get("Cookie") != "synthetic-private-header" {
		t.Fatal("source headers mutated")
	}
}

func TestOpenRouterNon200RejectsOtherwiseValidAccounting(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204, 400, 401, 403, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := openRouterFixture(t)
			hits := 0
			client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
				hits++
				response := openRouterResponse(openRouterReply)
				response.StatusCode = status
				return response, nil
			}))
			resp, err := client.Do(openRouterRequest())
			if status == 200 {
				if err != nil || hits != 1 || p.Snapshot().Blocked {
					t.Fatal("200 control rejected", err)
				}
				resp.Body.Close()
				return
			}
			if !errors.Is(err, ErrOpenRouterPolicy) || resp != nil || hits != 1 || !p.Snapshot().Blocked || p.Snapshot().Requests != 1 {
				t.Fatal("non-200 became successful usage", status, err, hits)
			}
			if _, err = client.Do(openRouterRequest()); !errors.Is(err, ErrOpenRouterPolicy) || hits != 1 {
				t.Fatal("failed response allowed another send", status, err, hits)
			}
		})
	}
}

func TestOpenRouterManifestPinsBaseAndModelIndependently(t *testing.T) {
	good := openRouterFixture(t).manifest
	for _, field := range []string{"control", "base_url", "model"} {
		t.Run(field, func(t *testing.T) {
			m := good
			if field == "base_url" {
				m.BaseURL = "https://other.invalid/api/v1"
			}
			if field == "model" {
				m.Model = "other/model"
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := ParseOpenRouterManifest(raw)
			if field == "control" {
				if err != nil || policy == nil {
					t.Fatal("manifest control rejected", err)
				}
			} else if !errors.Is(err, ErrOpenRouterPolicy) || policy != nil {
				t.Fatal("foreign manifest tuple accepted", field, err)
			}
		})
	}
}

func TestOpenRouterResolvePinsEveryTargetField(t *testing.T) {
	p := openRouterFixture(t)
	if resolved, err := p.Resolve(p.Target()); err != nil || resolved != p {
		t.Fatal("matching target control rejected", err)
	}
	for _, field := range []string{"id", "format", "base_url", "model"} {
		t.Run(field, func(t *testing.T) {
			target := p.Target()
			switch field {
			case "id":
				target.ID++
			case "format":
				target.Format = "anthropic"
			case "base_url":
				target.BaseURL = "https://other.invalid/api/v1"
			case "model":
				target.Model = "other/model"
			}
			if resolved, err := p.Resolve(target); !errors.Is(err, ErrOpenRouterPolicy) || resolved != nil {
				t.Fatal("foreign target resolved", field, err)
			}
		})
	}
}

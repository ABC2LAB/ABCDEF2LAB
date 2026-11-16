package exploreboundary

import (
	"github.com/Autumn-27/artex/exploration"
	"io"
	"net/http"
	"testing"
)

func TestExactPathExplorationScopeAndQuery(t *testing.T) {
	s, source, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(target *Target, policy *Policy) {
		target.PathPrefixes = []string{"=/", "/users/v1"}
		target.ExcludedPrefixes = nil
		policy.MaxDeniedRecords = 64
	})
	for _, tc := range []struct {
		method, path string
		query, allow bool
	}{{"GET", "/", false, true}, {"HEAD", "/", false, true}, {"GET", "/createdb", false, false}, {"HEAD", "/createdb", false, false}, {"GET", "/", true, false}, {"POST", "/", false, false}, {"GET", "/%2fcreatedb", false, false}, {"GET", "/%252fcreatedb", false, false}, {"GET", "//", false, false}, {"GET", "/./", false, false}, {"GET", "/users/v1/123", false, true}, {"GET", "/users/v10", false, false}} {
		req := approved(tc.method, tc.path)
		req.HasQuery = tc.query
		d, ticket := s.Admit(t.Context(), req)
		if d.Forward != tc.allow {
			t.Errorf("탐색 경계 승인 불일치: %s %s %v", tc.method, tc.path, d)
		}
		if ticket != nil {
			ticket.Done(Result{Status: 200, ResponseReceived: true})
		}
	}
	source.set(ErrConfig)
	d, _ := s.Admit(t.Context(), approved("GET", "/"))
	if d.Forward {
		t.Fatal("철회 이후 탐색 송신")
	}
}

func TestExactPathActualExplorationProxyQuery(t *testing.T) {
	for _, ingress := range []Ingress{DirectIngress, BrowserIngress} {
		t.Run(string(ingress), func(t *testing.T) {
			f := newIngressFixture(t, ingress, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "owned root") }, func(target *Target) { target.PathPrefixes = []string{"=/"} })
			for _, tc := range []struct {
				path   string
				status int
			}{{"/", 200}, {"/createdb", 403}, {"/?action=createdb", 403}, {"/?", 403}} {
				response, err := f.client.Get(f.target + tc.path)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != tc.status {
					t.Errorf("실제 탐색 프록시 %s: %d", tc.path, response.StatusCode)
				}
			}
			if f.hits.Load() != 1 {
				t.Fatal("탐색 범위 밖 실제 수신", f.hits.Load())
			}
		})
	}
}

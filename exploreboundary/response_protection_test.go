package exploreboundary

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/exploration"
)

func TestProductProxyProtectsResponseBeforeToolIngress(t *testing.T) {
	for _, ingress := range []Ingress{BrowserIngress, DirectIngress} {
		t.Run(string(ingress), func(t *testing.T) {
			f := newIngressFixture(t, ingress, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/app/reflection":
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"password":"synthetic-reflection"}`)
				case "/app/encoded":
					io.WriteString(w, "pass%77ord%3Dsynthetic-encoded")
				default:
					io.WriteString(w, "ordinary navigation content")
				}
			}, nil)
			for _, name := range []string{"reflection", "encoded", "ordinary"} {
				resp, err := f.client.Get(f.target + "/app/" + name)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal("failed to read the boundary response")
				}
				if name == "ordinary" {
					if resp.StatusCode != http.StatusOK || string(body) != "ordinary navigation content" {
						t.Fatal("ordinary content was lost")
					}
					continue
				}
				if resp.StatusCode != http.StatusBadGateway || len(body) != 0 || resp.Header.Get(ResultHeader) != "response_withheld" {
					t.Errorf("%s: response reached the tool ingress", name)
				}
			}
			records, _ := f.session.Records()
			if len(records) != 3 || f.hits.Load() != 3 {
				t.Fatal("response policy changed recorded request admission")
			}
			for _, r := range records[:2] {
				if r.Decision != "forwarded" || r.Reason != "response_withheld" || strings.Contains(r.Reason, "synthetic") {
					t.Fatal("missing safe outcome on a forwarded request")
				}
			}
			f = newIngressFixture(t, ingress, exploration.ReadOnly, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				io.WriteString(w, "uninspectable synthetic response")
			}, nil)
			resp, err := f.client.Get(f.target + "/app/invalid")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || len(body) != 0 || resp.StatusCode != http.StatusBadGateway || resp.Header.Get(ResultHeader) != "response_withheld" {
				t.Fatal("invalid encoded response reached the tool ingress")
			}
		})
	}
}

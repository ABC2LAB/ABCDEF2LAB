package e2proxy

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTLSApprovalPinPreventsHTTPToChangedIdentity(t *testing.T) {
	for _, correct := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "changed"}[correct], func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				_, _ = io.WriteString(w, "owned TLS response")
			}))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			cfg.RootCAs = roots
			digest := sha256.Sum256(target.Certificate().RawSubjectPublicKeyInfo)
			cfg.Rules[0].TLSSPKISHA256 = hex.EncodeToString(digest[:])
			if !correct {
				cfg.Rules[0].TLSSPKISHA256 = strings.Repeat("0", 64)
			}
			_, call := coreOpen(t, cfg)
			response, _, e := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if correct {
				if e != nil || response == nil || response.StatusCode != 200 || received.Load() != 1 {
					t.Fatal("approved identity did not receive HTTP")
				}
			} else if e == nil || received.Load() != 0 {
				t.Fatal("changed TLS identity received HTTP")
			}
		})
	}
}
func TestTLSPinRejectsNoncanonicalDigestAndPlainHTTP(t *testing.T) {
	for _, pin := range []string{"short", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		cfg := coreConfig(t, "https://example.test", &coreRecorder{}, 1)
		cfg.Rules[0].TLSSPKISHA256 = pin
		if _, e := compileRules(cfg.Rules); e != ErrConfig {
			t.Fatal("invalid TLS pin accepted")
		}
	}
	cfg := coreConfig(t, "http://example.test", &coreRecorder{}, 1)
	cfg.Rules[0].TLSSPKISHA256 = strings.Repeat("a", 64)
	if _, e := compileRules(cfg.Rules); e != ErrConfig {
		t.Fatal("HTTP silently ignored a TLS pin")
	}
}

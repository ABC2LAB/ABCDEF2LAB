package transmit

import (
	"context"
	"crypto/x509"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

type configFuzzRecorder struct{ calls int }

func (r *configFuzzRecorder) Record(context.Context, Event) error {
	r.calls++
	return nil
}

func configFuzzConfig(rec Recorder) Config {
	view := &JSONViewRule{
		StatusPath: []string{"status"}, NormalValue: "ok",
		ErrorValues: []string{"error"}, DataPath: []string{"data"},
		Markers: []idor.Marker{{Field: "marker", Value: "fixture-canary"}},
	}
	return Config{
		Scope:  Scope{RunRef: "run-fuzz", TaskRef: "task-fuzz", CaseRef: "case-fuzz", ResetRef: "reset-fuzz", PolicyRef: "policy-fuzz"},
		Origin: "http://target.test:8080", ConnectIP: netip.MustParseAddr("127.0.0.1"),
		ExpiresAt:   time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		Sessions:    []Session{{Account: idor.AccountRef{Label: "owner", Handle: "handle-owner", Status: oracle.SubjectVerified}, ExpiresAt: time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC), Headers: http.Header{"X-Fixture": {"owner"}}}},
		Resources:   []Resource{{Ref: "resource-fuzz", URL: "http://target.test:8080/resource", ApprovalRef: "approval-fuzz", ReadOnly: true, Accounts: []string{"owner"}, View: view}},
		MaxRequests: 4, RequestTimeout: time.Second, MaxBodyBytes: 4096,
		RootCAs: x509.NewCertPool(), Recorder: rec,
	}
}

func FuzzHTTPConfigURLContract(f *testing.F) {
	for mode := uint8(0); mode < 24; mode++ {
		f.Add(mode, "fixture")
	}
	f.Fuzz(func(t *testing.T, mode uint8, suffix string) {
		if len(suffix) > 64 {
			suffix = suffix[:64]
		}
		rec := &configFuzzRecorder{}
		cfg := configFuzzConfig(rec)
		accepted := true
		wantAuthority := "target.test:8080"
		switch mode % 24 {
		case 0:
			cfg.Origin = "http://TARGET.TEST:8080"
			cfg.Resources[0].URL = "http://TARGET.TEST:8080/resource"
		case 1:
			cfg.Origin = "http://target.test"
			cfg.Resources[0].URL = "http://target.test:80/" + url.PathEscape(suffix)
			wantAuthority = "target.test:80"
		case 2:
			cfg.Origin = "https://TARGET.TEST"
			cfg.Resources[0].URL = "https://target.test/resource"
			wantAuthority = "target.test:443"
		case 3:
			cfg.Origin = "http://[::1]"
			cfg.Resources[0].URL = "http://[::1]/resource"
			wantAuthority = "[::1]:80"
		case 4:
			cfg.Origin += "/"
			cfg.Resources[0].URL += "?q=" + url.QueryEscape(suffix)
		case 5:
			cfg.Origin += "/base"
			accepted = false
		case 6:
			cfg.Origin += "?probe=1"
			accepted = false
		case 7:
			cfg.Resources[0].URL = "https://target.test:8080/resource"
			accepted = false
		case 8:
			cfg.Resources[0].URL = "http://other.test:8080/resource"
			accepted = false
		case 9:
			cfg.Resources[0].URL = "http://target.test:8080/%zz"
			accepted = false
		case 10:
			cfg.Resources[0].URL = "http://target.test:99999/resource"
			accepted = false
		case 11:
			cfg.Origin = "//target.test:8080"
			accepted = false
		case 12:
			cfg.Origin += "?"
			accepted = false
		case 13:
			cfg.Resources[0].URL += "#fragment"
			accepted = false
		case 14:
			cfg.Resources[0].URL = "http://fixture@target.test:8080/resource"
			accepted = false
		case 15:
			cfg.Resources[0].URL = "http:resource"
			accepted = false
		case 16:
			cfg.Resources[0].URL = "/resource"
			accepted = false
		case 17:
			cfg.Resources[0].URL = "http://target.test:8081/resource"
			accepted = false
		case 18:
			cfg.Resources[0].URL = "http://target.test:0/resource"
			accepted = false
		case 19:
			cfg.Origin = "http://fixture@target.test:8080"
			accepted = false
		case 20:
			cfg.Origin += "#fragment"
			accepted = false
		case 21:
			cfg.Resources[0].URL = "ftp://target.test:8080/resource"
			accepted = false
		case 22:
			cfg.Resources[0].URL = "http://[fe80::1%25fixture]:8080/resource"
			accepted = false
		case 23:
			// These generated ports are all positive and below 65536.
			wantAuthority = "target.test:" + strconv.Itoa(1+len(suffix)*256+int(mode))
			cfg.Origin = "http://" + wantAuthority
			cfg.Resources[0].URL = cfg.Origin + "/" + url.PathEscape(suffix)
		}
		tx, err := NewHTTP(context.Background(), cfg)
		if tx != nil {
			defer tx.Stop()
		}
		if rec.calls != 0 {
			t.Fatalf("constructor recorded %d events", rec.calls)
		}
		if accepted {
			if err != nil {
				t.Fatalf("accepted URL mode %d rejected: %v", mode%24, err)
			}
			if got := tx.resources["resource-fuzz"].url.Host; got != wantAuthority {
				t.Fatalf("canonical authority = %q, want %q", got, wantAuthority)
			}
		} else if err == nil {
			t.Fatalf("invalid URL mode %d accepted", mode%24)
		}
	})
}

func FuzzHTTPConfigSnapshotIsolation(f *testing.F) {
	f.Add(uint8(0), "mutated")
	f.Add(uint8(1), "alternate")
	f.Fuzz(func(t *testing.T, selector uint8, value string) {
		if len(value) > 64 {
			value = value[:64]
		}
		rec := &configFuzzRecorder{}
		cfg := configFuzzConfig(rec)
		want := configFuzzConfig(&configFuzzRecorder{})
		tx, err := NewHTTP(context.Background(), cfg)
		if err != nil {
			t.Fatalf("positive fixture rejected: %v", err)
		}
		defer tx.Stop()
		if !reflect.DeepEqual(cfg.Sessions, want.Sessions) || !reflect.DeepEqual(cfg.Resources, want.Resources) || !cfg.RootCAs.Equal(want.RootCAs) {
			t.Fatal("constructor mutated caller configuration")
		}
		mutation := "changed-" + value
		// Mutate every caller-owned slice/map after construction. The transmitter
		// must retain its private snapshot and construction must not record traffic.
		cfg.Sessions[0].Headers["X-Fixture"][0] = mutation
		cfg.Sessions[0].Headers["X-New"] = []string{mutation}
		cfg.Sessions[0].Account.Handle = "changed-handle"
		cfg.Sessions[0].ExpiresAt = time.Time{}
		cfg.Resources[0].Accounts[0] = "mutated-account"
		cfg.Resources[0].Accounts = append(cfg.Resources[0].Accounts, "extra")
		cfg.Resources[0].View.StatusPath[0] = "mutated"
		cfg.Resources[0].View.ErrorValues[0] = "mutated-error"
		cfg.Resources[0].View.DataPath[0] = "mutated-data"
		cfg.Resources[0].View.Markers[0] = idor.Marker{Field: "changed-field", Value: mutation}
		cfg.Resources[0].View.NormalValue = mutation
		cfg.Resources[0].View.Markers = append(cfg.Resources[0].View.Markers, idor.Marker{Field: "new", Value: "new"})
		cfg.Resources[0].URL += "/" + strconv.Itoa(int(selector))
		cfg.Resources[0].Ref = "changed-resource"
		cfg.Resources[0].ApprovalRef = "changed-approval"
		cfg.Resources[0].ReadOnly = false
		// CertPool equality compares certificate membership; no parsing, TLS
		// handshake or network is needed to exercise caller-side pool mutation.
		cfg.RootCAs.AddCert(&x509.Certificate{Raw: []byte(mutation), RawSubject: []byte("fixture-subject")})
		roots := tx.client.Transport.(*http.Transport).TLSClientConfig.RootCAs
		if roots == cfg.RootCAs || !roots.Equal(want.RootCAs) || roots.Equal(cfg.RootCAs) {
			t.Fatal("root certificate pool alias leaked")
		}
		if !reflect.DeepEqual(tx.sessions["owner"], want.Sessions[0]) {
			t.Fatal("session snapshot alias leaked")
		}
		binding, ok := tx.resources["resource-fuzz"]
		if !ok {
			t.Fatal("resource snapshot missing")
		}
		if !reflect.DeepEqual(binding.resource, want.Resources[0]) || !binding.accounts["owner"] || binding.accounts["mutated-account"] || len(binding.accounts) != 1 {
			t.Fatal("resource snapshot alias leaked")
		}
		if rec.calls != 0 {
			t.Fatalf("constructor recorded %d events", rec.calls)
		}
	})
}

func FuzzHTTPConfigRejectsInvalidRegistration(f *testing.F) {
	for selector := uint8(0); selector < 38; selector++ {
		f.Add(selector, "x")
	}
	f.Fuzz(func(t *testing.T, selector uint8, suffix string) {
		if len(suffix) > 32 {
			suffix = suffix[:32]
		}
		rec := &configFuzzRecorder{}
		cfg := configFuzzConfig(rec)
		control, err := NewHTTP(context.Background(), cfg)
		if err != nil {
			t.Fatalf("valid registry rejected: %v", err)
		}
		defer control.Stop()
		ctx := context.Background()
		invalidRef := "!invalid-" + suffix
		switch selector % 38 {
		case 0:
			cfg.Scope.RunRef = invalidRef
		case 1:
			cfg.MaxRequests = 0
		case 2:
			cfg.ConnectIP = netip.MustParseAddr("0.0.0.0")
		case 3:
			cfg.ConnectIP = netip.MustParseAddr("224.0.0.1")
		case 4:
			cfg.ConnectIP = netip.MustParseAddr("::1").WithZone("zone")
		case 5:
			other := cfg.Sessions[0]
			other.Account.Handle = "different-handle"
			cfg.Sessions = append(cfg.Sessions, other)
		case 6:
			cfg.Sessions = append(cfg.Sessions, Session{Account: idor.AccountRef{Label: "other", Handle: "handle-owner", Status: oracle.SubjectVerified}, ExpiresAt: cfg.ExpiresAt.Add(time.Hour)})
		case 7:
			cfg.Resources = append(cfg.Resources, cfg.Resources[0])
		case 8:
			cfg.Resources[0].Accounts = []string{"unregistered"}
		case 9:
			cfg.Resources[0].ReadOnly = false
		case 10:
			cfg.Resources[0].ApprovalRef = invalidRef
		case 11, 12, 13, 14:
			refs := []*string{&cfg.Scope.TaskRef, &cfg.Scope.CaseRef, &cfg.Scope.ResetRef, &cfg.Scope.PolicyRef}
			*refs[int(selector%38)-11] = invalidRef
		case 15:
			cfg.Recorder = nil
		case 16:
			ctx = nil
		case 17:
			cfg.ExpiresAt = time.Time{}
		case 18:
			cfg.MinInterval = -time.Nanosecond
		case 19:
			cfg.RequestTimeout = 0
		case 20:
			cfg.MaxBodyBytes = 0
		case 21:
			cfg.MaxBodyBytes = math.MaxInt64
		case 22:
			cfg.ConnectIP = netip.Addr{}
		case 23:
			cfg.Sessions = nil
		case 24:
			cfg.Resources = nil
		case 25:
			cfg.Sessions[0].Account.Label = invalidRef
		case 26:
			cfg.Sessions[0].Account.Handle = invalidRef
		case 27:
			cfg.Sessions[0].ExpiresAt = time.Time{}
		case 28:
			cfg.Resources[0].Ref = invalidRef
		case 29:
			cfg.Resources[0].Accounts = nil
		case 30:
			cfg.Sessions[0].Headers["X\rFixture"] = []string{"fixture"}
		case 31:
			cfg.Sessions[0].Headers["Host"] = []string{"outside.test"}
		case 32:
			cfg.Sessions[0].Headers["X-Fixture"] = []string{"fixture\r\n" + suffix}
		case 33:
			cfg.Resources[0].View.StatusPath = nil
		case 34:
			cfg.Resources[0].View.NormalValue = ""
		case 35:
			cfg.Resources[0].View.ErrorValues[0] = cfg.Resources[0].View.NormalValue
		case 36:
			cfg.Resources[0].View.Markers = nil
		case 37:
			cfg.Resources[0].View.Markers = append(cfg.Resources[0].View.Markers, cfg.Resources[0].View.Markers[0])
		}
		tx, err := NewHTTP(ctx, cfg)
		if tx != nil {
			tx.Stop()
		}
		if err == nil {
			t.Fatalf("invalid registration selector %d accepted", selector%38)
		}
		if rec.calls != 0 {
			t.Fatalf("constructor recorded %d events", rec.calls)
		}
	})
}

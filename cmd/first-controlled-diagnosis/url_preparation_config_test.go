package main

import (
	"github.com/jackc/pgx/v5"
	"net/netip"
	"testing"
)

func TestURLPreparationPrivateTargetNeverAllowsMetadata(t *testing.T) {
	for _, raw := range []string{"169.254.169.254", "169.254.170.2", "0.0.0.0", "224.0.0.1", "100.100.100.200", "fe80::1", "::ffff:127.0.0.1", "fd00:ec2::254", "fd20:ce::254", "168.63.129.16"} {
		if urlTargetIPAllowed(netip.MustParseAddr(raw), true) || urlTargetIPAllowed(netip.MustParseAddr(raw), false) {
			t.Fatalf("protected address accepted: %s", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.20.0.1", "::1"} {
		ip := netip.MustParseAddr(raw)
		if urlTargetIPAllowed(ip, false) || !urlTargetIPAllowed(ip, true) {
			t.Fatal("private exception not exact")
		}
	}
}
func TestURLPreparationProtectedEndpointConflicts(t *testing.T) {
	c := urlPreparationInstallation{ReportListenAddr: "127.0.0.1:9000", ProtectedEndpoints: []urlProtectedEndpoint{{Host: "internal-model.example", Port: 443, Addresses: []string{"10.0.0.2"}}}}
	for _, tc := range []struct {
		origin, ip string
		blocked    bool
	}{{"https://internal-model.example", "10.0.0.8", true}, {"https://other.example", "10.0.0.2", true}, {"https://127.0.0.1:9000", "127.0.0.1", true}, {"https://10.0.0.2:8443", "10.0.0.2", false}} {
		if got := urlProtectedConflict(c, tc.origin, netip.MustParseAddr(tc.ip)); got != tc.blocked {
			t.Fatalf("wrong conflict for %s", tc.origin)
		}
	}
}
func TestURLPreparationPathPrefixIsSegmentScoped(t *testing.T) {
	for _, tc := range []struct {
		path, prefix string
		allowed      bool
	}{{"/read", "/read", true}, {"/read/item", "/read", true}, {"/reader", "/read", false}, {"/anything", "/", true}} {
		if urlPathWithin(tc.path, tc.prefix) != tc.allowed {
			t.Fatal("prefix mismatch")
		}
	}
}

func TestURLPreparationDatabasePinsIncludeEveryFallback(t *testing.T) {
	c := urlPreparationInstallation{ProtectedEndpoints: []urlProtectedEndpoint{{Host: "db", Port: 5432, Addresses: []string{"127.0.0.1"}}}}
	for _, tc := range []struct {
		dsn   string
		allow bool
	}{
		{"host=/tmp/owned-pg port=5432", true},
		{"host=127.0.0.1 port=5432", true},
		{"host=127.0.0.1,127.0.0.2 port=5432,5432", false},
		{"host=127.0.0.1,127.0.0.1 port=5432,5433", false},
		{"host=localhost port=5432", false},
	} {
		conn, err := pgx.ParseConfig(tc.dsn)
		if err != nil {
			t.Fatal(err)
		}
		if urlDatabaseProtected(c, conn) != tc.allow {
			t.Fatalf("pin decision mismatch for test DSN")
		}
	}
}

func TestURLPreparationDatabaseRequiresExplicitIdentity(t *testing.T) {
	base := "host='/tmp/owned-pg' port=5432 user='owned' password='' dbname='owned' sslmode=disable search_path='owned'"
	if _, err := urlDatabaseExplicitKeys(base); err != nil {
		t.Fatal("explicit fixture denied")
	}
	for _, dsn := range []string{"service=owned", "host=127.0.0.1 port=5432", base + " host='127.0.0.2'", base + " malformed", base + " options='unterminated"} {
		if _, err := urlDatabaseExplicitKeys(dsn); err == nil {
			t.Fatal("ambiguous identity accepted")
		}
	}
}

func TestURLPreparationDatabaseRejectsIndirectSettings(t *testing.T) {
	base := "host='/tmp/owned-pg' port=5432 user='owned' password='' dbname='owned' sslmode=disable"
	for _, suffix := range []string{" service='owned'", " servicefile='/unused'", " passfile='/unused'", " sslrootcert='/unused'", " options='x\ty'", "\tuser=other", "\x7f"} {
		if _, err := urlParseDatabase(base + suffix); err == nil {
			t.Fatal("indirect or ambiguous DSN accepted")
		}
	}
	t.Setenv("PGOPTIONS", "-c search_path=other")
	if _, err := urlParseDatabase(base); err == nil {
		t.Fatal("environment identity accepted")
	}
}

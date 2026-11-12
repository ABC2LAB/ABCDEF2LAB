package targetaddr

import (
	"net/netip"
	"testing"
)

func TestAllowedAndMetadataVectors(t *testing.T) {
	for _, tc := range []struct {
		ip                        string
		public, private, metadata bool
	}{
		{"8.8.8.8", true, true, false},
		{"2606:4700::1111", true, true, false},
		{"10.0.0.1", false, true, false},
		{"127.0.0.1", false, true, false},
		{"169.254.169.254", false, false, true},
		{"fe80::1", false, false, true},
		{"fd00:ec2::254", false, false, true},
		{"168.63.129.16", false, false, true},
		{"100.64.0.1", false, false, false},
		{"0.0.0.0", false, false, false},
		{"224.0.0.1", false, false, false},
		{"2001:db8::1", false, false, false},
		{"::ffff:8.8.8.8", false, false, false},
	} {
		ip := netip.MustParseAddr(tc.ip)
		if Allowed(ip, false) != tc.public || Allowed(ip, true) != tc.private || Metadata(ip) != tc.metadata {
			t.Fatalf("%s: public=%v private=%v metadata=%v", tc.ip, Allowed(ip, false), Allowed(ip, true), Metadata(ip))
		}
	}
	if Allowed(netip.Addr{}, true) || Metadata(netip.Addr{}) {
		t.Fatal("invalid address accepted")
	}
}

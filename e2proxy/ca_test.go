package e2proxy

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestExecutionCAIsFreshAndPublicPEMContainsNoKey(t *testing.T) {
	a, public, err := NewExecutionCA("run-a", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := NewExecutionCA("run-b", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(public)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("public certificate format")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || cert.Subject.SerialNumber != "run-a" || cert.CheckSignatureFrom(cert) != nil {
		t.Fatal("CA properties")
	}
	if a.Leaf.SerialNumber.Cmp(b.Leaf.SerialNumber) == 0 || a.PrivateKey == b.PrivateKey {
		t.Fatal("CA reused")
	}
}
func TestExecutionCARejectsUnboundOrExpiredRun(t *testing.T) {
	for _, item := range []struct {
		ref    string
		expiry time.Time
	}{{"", time.Now().Add(time.Hour)}, {"run", time.Now().Add(-time.Second)}, {"run", time.Now().Add(25 * time.Hour)}} {
		if _, _, err := NewExecutionCA(item.ref, item.expiry); err == nil {
			t.Fatal("invalid CA accepted")
		}
	}
}

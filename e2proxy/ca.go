package e2proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"
)

// NewExecutionCA is a trusted-host factory. It creates a fresh key for this
// execution, returns the key only as a Go value, and returns a separate public
// certificate for the read-only tool mount. It modifies no OS trust store.
func NewExecutionCA(runRef string, expires time.Time) (tls.Certificate, []byte, error) {
	now := time.Now()
	if !validRef(runRef) || !now.Before(expires) || expires.Sub(now) > maxExecutionCALifetime {
		return tls.Certificate{}, nil, ErrConfig
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, ErrConfig
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil || serial.Sign() == 0 {
		return tls.Certificate{}, nil, ErrConfig
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "E2 execution CA", SerialNumber: runRef},
		NotBefore: now.Add(-time.Minute), NotAfter: expires, IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, ErrConfig
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, ErrConfig
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, public, nil
}

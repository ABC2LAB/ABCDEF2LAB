package llmpolicy_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
)

const localModelHost = "model.internal"

type tlsFixture struct {
	server  *httptest.Server
	origin  string
	address netip.Addr
	certPEM []byte
	seen    atomic.Int64
	hello   atomic.Int64
	host    atomic.Value
	sni     atomic.Value
	active  atomic.Pointer[tls.Certificate]
}

func newTLSFixture(t *testing.T, dnsName string, notBefore, notAfter time.Time) *tlsFixture {
	t.Helper()
	cert, certPEM := makeCertificate(t, dnsName, notBefore, notAfter)
	f := &tlsFixture{certPEM: certPEM}
	f.active.Store(&cert)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen.Add(1)
		f.host.Store(r.Host)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	f.server = httptest.NewUnstartedServer(handler)
	f.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.server.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return f.active.Load(), nil
		},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			f.hello.Add(1)
			f.sni.Store(hello.ServerName)
			return nil, nil
		},
	}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)

	host, port, err := net.SplitHostPort(f.server.Listener.Addr().String())
	if err != nil {
		t.Fatal("TLS fixture listener address unavailable")
	}
	f.address = netip.MustParseAddr(host)
	f.origin = "https://" + localModelHost + ":" + port
	return f
}

func makeCertificate(t *testing.T, dnsName string, notBefore, notAfter time.Time) (tls.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("TLS fixture key generation failed")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal("TLS fixture serial generation failed")
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal("TLS fixture certificate generation failed")
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal("TLS fixture certificate loading failed")
	}
	return cert, certPEM
}

func newPolicy(t *testing.T, fixture *tlsFixture) *llmpolicy.Policy {
	t.Helper()
	policy, err := llmpolicy.New([]llmpolicy.Endpoint{{
		Origin:    fixture.origin,
		Addresses: []netip.Addr{fixture.address},
	}})
	if err != nil {
		t.Fatal("local TLS policy setup failed")
	}
	return policy
}

func clientFor(t *testing.T, policy *llmpolicy.Policy, fixture *tlsFixture, roots *x509.CertPool) *http.Client {
	t.Helper()
	client, err := llmpolicy.NewClient(policy, llmpolicy.ClientOptions{
		RootCAs: roots,
		LookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{fixture.address}, nil
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal("local TLS client setup failed")
	}
	return client
}

func rootsFor(t *testing.T, certPEM []byte) *x509.CertPool {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("TLS fixture root setup failed")
	}
	return roots
}

func assertTLSFailure(t *testing.T, fixture *tlsFixture, err error) {
	t.Helper()
	if err == nil || !errors.Is(err, llmpolicy.ErrTransport) {
		t.Fatal("TLS handshake failure was not reported as a transport error")
	}
	if fixture.hello.Load() == 0 {
		t.Fatal("TLS handshake did not reach the server")
	}
	if fixture.seen.Load() != 0 {
		t.Fatal("TLS failure reached the HTTP handler")
	}
}

func TestLocalTLSAllowsPinnedOriginAndPreservesHostAndSNI(t *testing.T) {
	now := time.Now()
	fixture := newTLSFixture(t, localModelHost, now.Add(-time.Minute), now.Add(time.Hour))
	client := clientFor(t, newPolicy(t, fixture), fixture, rootsFor(t, fixture.certPEM))

	response, err := client.Get(fixture.origin + "/v1/models")
	if err != nil {
		t.Fatal("trusted local TLS request was not sent")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 1 || fixture.seen.Load() != 1 {
		t.Fatal("trusted local TLS request did not reach the handler exactly once")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "ok" {
		t.Fatal("trusted local TLS response body was not preserved")
	}
	if got := fixture.host.Load().(string); got != strings.TrimPrefix(fixture.origin, "https://") {
		t.Fatal("request Host was not preserved")
	}
	if got := fixture.sni.Load().(string); got != localModelHost {
		t.Fatal("TLS SNI was not preserved")
	}
}

func TestLocalTLSRejectsHostOverrideBeforeHTTP(t *testing.T) {
	now := time.Now()
	fixture := newTLSFixture(t, localModelHost, now.Add(-time.Minute), now.Add(time.Hour))
	client := clientFor(t, newPolicy(t, fixture), fixture, rootsFor(t, fixture.certPEM))
	request, err := http.NewRequest(http.MethodGet, fixture.origin+"/v1/models", nil)
	if err != nil {
		t.Fatal("local TLS request setup failed")
	}
	request.Host = "other.internal"
	response, err := client.Do(request)
	if err == nil {
		response.Body.Close()
		t.Fatal("Host override was not blocked")
	}
	if !errors.Is(err, llmpolicy.ErrRequestBlocked) {
		t.Fatal("Host override was not reported as a blocked request")
	}
	if fixture.hello.Load() != 0 {
		t.Fatal("Host override started a TLS handshake")
	}
	if fixture.seen.Load() != 0 {
		t.Fatal("Host override reached the TLS handler")
	}
}

func TestLocalTLSRejectsWrongDNSName(t *testing.T) {
	now := time.Now()
	fixture := newTLSFixture(t, "other.internal", now.Add(-time.Minute), now.Add(time.Hour))
	client := clientFor(t, newPolicy(t, fixture), fixture, rootsFor(t, fixture.certPEM))
	_, err := client.Get(fixture.origin + "/v1/models")
	assertTLSFailure(t, fixture, err)
}

func TestLocalTLSRejectsUntrustedCA(t *testing.T) {
	now := time.Now()
	fixture := newTLSFixture(t, localModelHost, now.Add(-time.Minute), now.Add(time.Hour))
	client := clientFor(t, newPolicy(t, fixture), fixture, x509.NewCertPool())
	_, err := client.Get(fixture.origin + "/v1/models")
	assertTLSFailure(t, fixture, err)
}

func TestLocalTLSRejectsExpiredLeaf(t *testing.T) {
	now := time.Now()
	fixture := newTLSFixture(t, localModelHost, now.Add(-2*time.Hour), now.Add(-time.Hour))
	client := clientFor(t, newPolicy(t, fixture), fixture, rootsFor(t, fixture.certPEM))
	_, err := client.Get(fixture.origin + "/v1/models")
	assertTLSFailure(t, fixture, err)
}

func TestLocalTLSUsesRootPoolSnapshot(t *testing.T) {
	now := time.Now()
	fixture := newTLSFixture(t, localModelHost, now.Add(-time.Minute), now.Add(time.Hour))
	roots := rootsFor(t, fixture.certPEM)
	client := clientFor(t, newPolicy(t, fixture), fixture, roots)
	rotated, rotatedPEM := makeCertificate(t, localModelHost, now.Add(-time.Minute), now.Add(time.Hour))
	fixture.active.Store(&rotated)
	if !roots.AppendCertsFromPEM(rotatedPEM) {
		t.Fatal("TLS fixture root rotation failed")
	}
	// The caller can mutate its pool after construction, but the transport
	// must keep the cloned trust snapshot and reject the newly added leaf.
	_, err := client.Get(fixture.origin + "/v1/models")
	assertTLSFailure(t, fixture, err)
}

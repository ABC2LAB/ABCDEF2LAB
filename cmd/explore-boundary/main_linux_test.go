//go:build linux

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/pathmask"
)

// 보호 입력 검사는 상위 경로 권한을 본다. 기존 시험처럼 /var/tmp 아래 비공개 디렉터리를 쓴다.
func protectedDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "ebx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func writeFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func validConfig(t *testing.T) config {
	t.Helper()
	dir := protectedDir(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "boundary-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	writeFile(t, filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	writeFile(t, filepath.Join(dir, "ca.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	mask := make([]byte, pathmask.KeyBytes)
	rand.Read(mask)
	writeFile(t, filepath.Join(dir, "mask.key"), mask)
	return config{Format: configFormat, BrowserListen: "10.94.241.10:8080", DirectListen: "10.94.242.10:8080", ControlSocket: filepath.Join(dir, "control.sock"), EngineUID: uint32(os.Getuid()),
		CACertFile: filepath.Join(dir, "ca.pem"), CAKeyFile: filepath.Join(dir, "ca.key"), PathMaskKeyFile: filepath.Join(dir, "mask.key"), ControlEndpoints: []string{"10.94.240.20:8931"},
		MaxRequestBytes: 4096, MaxResponseBytes: 1 << 20, MaxTunnels: 8, MaxDeniedRecords: 256, TimeoutMS: 5000, CloseWaitMS: 2000, LeafValiditySec: 3600}
}

func TestBuildAcceptsSeparatedIngressAndProtectedInputs(t *testing.T) {
	if _, err := build(validConfig(t)); err != nil {
		t.Fatal("valid boundary configuration rejected", err)
	}
}

func TestBuildRejectsUnsafeConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*config){
		"format":           func(c *config) { c.Format = "other" },
		"loopback_listen":  func(c *config) { c.BrowserListen = "127.0.0.1:8080" },
		"wildcard_listen":  func(c *config) { c.DirectListen = "0.0.0.0:8080" },
		"shared_interface": func(c *config) { c.DirectListen = "10.94.241.10:8081" },
		"missing_mask_key": func(c *config) { c.PathMaskKeyFile = filepath.Join(filepath.Dir(c.CACertFile), "absent") },
		"ca_not_ca":        func(c *config) { c.CAKeyFile = c.PathMaskKeyFile },
		"bad_control":      func(c *config) { c.ControlEndpoints = []string{"llm-gateway:443"} },
		"zero_limit":       func(c *config) { c.MaxDeniedRecords = 0 },
	} {
		c := validConfig(t)
		mutate(&c)
		if _, err := build(c); err == nil {
			t.Fatal("unsafe boundary configuration accepted:", name)
		}
	}
}

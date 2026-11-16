//go:build linux

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploreboundary"
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

// 원자료 출력은 두 칸을 함께 줄 때만 켜진다. 한쪽만 준 설정은 켜려다 빠뜨린 것이라 시작하지 않는다.
func TestBuildEvidenceNeedsBothFields(t *testing.T) {
	evidence := func(t *testing.T) string {
		dir := filepath.Join(protectedDir(t), "evidence")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for name, mutate := range map[string]func(*config){
		"dir_only":     func(c *config) { c.EvidenceDir = evidence(t) },
		"max_only":     func(c *config) { c.EvidenceMaxBytes = 1 << 20 },
		"negative_max": func(c *config) { c.EvidenceDir, c.EvidenceMaxBytes = evidence(t), -1 },
		"missing_dir":  func(c *config) { c.EvidenceDir, c.EvidenceMaxBytes = filepath.Join(t.TempDir(), "absent"), 1<<20 },
	} {
		c := validConfig(t)
		mutate(&c)
		if _, err := build(c); err == nil {
			t.Fatal("incomplete evidence configuration accepted:", name)
		}
	}
	c := validConfig(t)
	if p, err := build(c); err != nil || p.manager.Policy.Evidence != nil {
		t.Fatal("boundary without evidence fields must not write evidence", err)
	}
}

// 평가 upstream(계약 §4.2)은 원자료를 쓰는 평가·수용시험 회차에서만 켠다. 원자료 칸 없이 주면 제품 설정에 잘못 들어간
// 것으로 보고 시작하지 않는다. 주면 두 입구 Proxy가 같은 주소를 받고, 이름·입구 자신·제어 통로·모양이 틀린 주소는 거부한다.
func TestBuildEvalUpstreamOnlyWithEvidence(t *testing.T) {
	withEvidence := func(t *testing.T) config {
		c := validConfig(t)
		c.EvidenceDir = filepath.Join(protectedDir(t), "evidence")
		if err := os.Mkdir(c.EvidenceDir, 0o700); err != nil {
			t.Fatal(err)
		}
		c.EvidenceMaxBytes = 1 << 20
		return c
	}
	c := validConfig(t)
	c.EvalUpstream = "10.94.243.10:8081"
	if _, err := build(c); err == nil || err.Error() != "eval_upstream_invalid" {
		t.Fatal("eval upstream without boundary evidence accepted", err)
	}
	if p, err := build(validConfig(t)); err != nil || p.browser.EvalUpstream().IsValid() || p.direct.EvalUpstream().IsValid() {
		t.Fatal("product boundary has an eval upstream", err)
	}
	c = withEvidence(t)
	c.EvalUpstream = "10.94.243.10:8081"
	p, err := build(c)
	want := netip.MustParseAddrPort("10.94.243.10:8081")
	if err != nil || p.browser.EvalUpstream() != want || p.direct.EvalUpstream() != want {
		t.Fatal("eval upstream not passed to both ingress proxies", err)
	}
	for name, raw := range map[string]string{"name": "eval-meter:8081", "no_port": "10.94.243.10", "port_zero": "10.94.243.10:0",
		"unspecified": "0.0.0.0:8081", "browser_ingress": c.BrowserListen, "direct_ingress": "[::ffff:10.94.242.10]:8080",
		"control_endpoint": c.ControlEndpoints[0], "zone": "[fe80::1%eth0]:8081"} {
		bad := withEvidence(t)
		bad.EvalUpstream = raw
		if _, err := build(bad); err == nil || err.Error() != "eval_upstream_invalid" {
			t.Errorf("%s eval upstream accepted: %v", name, err)
		}
	}
}

// 원자료를 켠 경계는 내릴 때 입구를 닫은 뒤 표식을 마지막에 쓴다. 표식의 키 참조는 경로 가림 키에서 나온다.
func TestShutdownSealsEvidence(t *testing.T) {
	c := validConfig(t)
	c.EvidenceDir = filepath.Join(protectedDir(t), "evidence")
	if err := os.Mkdir(c.EvidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	c.EvidenceMaxBytes = 1 << 20
	p, err := build(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.EvidenceDir, exploreboundary.EvidenceRowsName)); err != nil {
		t.Fatal("rows file not created at start", err)
	}
	if err := p.shutdown(nil, exploreboundary.SealedByShutdown); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.EvidenceDir, exploreboundary.EvidenceMarkerName))
	if err != nil {
		t.Fatal("marker missing after shutdown", err)
	}
	var marker exploreboundary.EvidenceMarker
	key, _ := os.ReadFile(c.PathMaskKeyFile)
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Format != exploreboundary.EvidenceFormat || marker.PathKeyRef != pathmask.KeyRef(key) ||
		marker.SealedBy != exploreboundary.SealedByShutdown || marker.Session != nil {
		t.Fatalf("shutdown marker: %+v %v", marker, err)
	}
	// 같은 디렉터리로 다시 띄우면 이전 회차 원자료에 이어 쓰지 않는다.
	if _, err := build(c); err == nil {
		t.Fatal("boundary reused a sealed evidence directory")
	}
}

//go:build linux

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
	"github.com/Autumn-27/artex/loginattempt"
	"github.com/Autumn-27/artex/server"
)

type hostTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
	pem  []byte
}

func newHostTestCA(t *testing.T, name string) hostTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return hostTestCA{cert: cert, key: key, pool: pool, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca hostTestCA) leaf(t *testing.T, host string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:])
}

type noExternalResolver struct{}

func (noExternalResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errors.New("no external names in this test")
}

type hostFixture struct {
	host    *productExplorationHost
	rec     productTargetRecord
	binding exploration.Binding
	flow    diagnosisflow.ExplorationBinding
	target  string
	hits    *atomic.Int64
	ended   *atomic.Bool
	digest  string
	policy  productExplorationPolicy
	caPEM   []byte
}

// newHostFixture는 실제 탐색 경계(Manager·직접 입구 프록시·제어 소켓)를 같은 프로세스에 띄우고, 승인 대상 역할의 TLS
// 서버와 그 대상 등록 기록을 만든다. Docker·모델은 쓰지 않는다.
func newHostFixture(t *testing.T) hostFixture {
	t.Helper()
	const host = "owned.test"
	upstreamCA, boundaryCA := newHostTestCA(t, "upstream-ca"), newHostTestCA(t, "boundary-ca")
	leaf, spki := upstreamCA.leaf(t, host)
	hits := &atomic.Int64{}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<a href="/read/item">item</a>`)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	_, portText, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)

	manager := &exploreboundary.Manager{Policy: exploreboundary.Policy{Resolver: noExternalResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: 16, Now: time.Now},
		CloseWait: time.Second}
	proxy, err := exploreboundary.NewProxy(exploreboundary.ProxyConfig{Ingress: exploreboundary.DirectIngress, Current: manager.Current, CA: boundaryCA.cert, CAKey: boundaryCA.key,
		Roots: upstreamCA.pool, MaxRequestBytes: 1 << 16, MaxResponseBytes: 1 << 16, MaxTunnels: 8, Timeout: 5 * time.Second, LeafValidity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	manager.Proxies = []*exploreboundary.Proxy{proxy}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	// unix 소켓 경로 상한 때문에 짧은 임시 디렉터리를 쓴다.
	dir, err := os.MkdirTemp("", "jx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "c.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = manager.Serve(ctx, listener, func(net.Conn) bool { return true }) }()
	t.Cleanup(func() { listener.Close() })

	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, boundaryCA.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(boundaryCA.pem)
	policy := productExplorationPolicy{ControlSocket: socket, DirectProxy: front.URL, BoundaryCAFile: caFile, BoundaryCASHA256: hex.EncodeToString(sum[:]), MCPURL: "http://explore-mcp:8931/mcp"}
	h, err := newProductExplorationHost(policy, boundaryCA.pem)
	if err != nil {
		t.Fatal(err)
	}
	h.bashRootParent = dir
	origin := "https://" + host + ":" + portText
	rec := productTargetRecord{SelectedIP: "127.0.0.1", Target: diagnosisprep.ProductTarget{ID: "target-1", Revision: 2, Origin: origin, State: "approved", Port: port,
		PathPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD"}, TLSSPKISHA256: spki, ExpiresAt: time.Now().Add(time.Hour)}}
	binding := exploration.Binding{SessionRef: "explore-host-1", RunRef: "run-job-1", JobID: "job-1", TaskID: "7", TargetRef: "target-1", TargetRevision: 2,
		WriteMode: exploration.ReadOnly, CommMode: exploration.FullLocal, Limits: exploration.Limits{TimeBudgetSeconds: 60, MaxRequests: 5, MaxConcurrency: 2, MaxResponseBytes: 4096}}
	digest := strings.Repeat("a", 64)
	flow := diagnosisflow.ExplorationBinding{SessionRef: binding.SessionRef, RunRef: binding.RunRef, JobID: binding.JobID, TaskID: binding.TaskID, TargetRef: binding.TargetRef, TargetRevision: binding.TargetRevision, SourceDigest: digest}
	return hostFixture{host: h, rec: rec, binding: binding, flow: flow, target: origin, hits: hits, ended: &atomic.Bool{}, digest: digest, policy: policy, caPEM: boundaryCA.pem}
}

func (f hostFixture) current(productTargetRecord) error {
	if f.ended.Load() {
		return server.ErrFirstDiagnosisTargetEnded
	}
	return nil
}

// 설치 정책의 탐색 블록으로 만든 host가 실제 경계를 열고, 정적 fetch 본문을 같은 순번 관찰과 맞추고, 제외 경로를 막고,
// 브라우저 MCP 정보를 결속대로 주고, 마감하면 새 요청을 받지 않는다.
func TestProductExplorationHostDrivesRealBoundary(t *testing.T) {
	f := newHostFixture(t)
	host, err := f.host.factory(f.rec, f.current, f.digest, []string{"/read/admin"}, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}, &explorationPasswords{}, productExplorationLogin(nil))(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	if host.Reader == nil || host.Fetch == nil || host.Sequences == nil || host.Session.Boundary == nil || host.Session.StartBrowser == nil || host.Session.Redact == nil {
		t.Fatal("host is missing a capability the pipeline requires")
	}
	// 도구 결과의 자격 형식은 모델·활동 기록으로 가기 전에 가린다(S09). 주소는 남긴다.
	masked := string(host.Session.Redact([]byte("Set-Cookie: sid=synthetic-session-77aa\nAuthorization: Bearer synthetic-bearer-77aa\n" +
		`{"password":"synthetic-pass-77aa"} next ` + f.target + "/read/item")))
	for _, marker := range []string{"synthetic-session-77aa", "synthetic-bearer-77aa", "synthetic-pass-77aa"} {
		if strings.Contains(masked, marker) {
			t.Fatalf("exploration output kept credential marker %s: %q", marker, masked)
		}
	}
	if !strings.Contains(masked, f.target+"/read/item") {
		t.Fatalf("exploration output lost the page address: %q", masked)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.Fetch(t.Context(), f.target+"/read")
	if err != nil || result.Seq == 0 || !result.Complete || result.Reason != "" || f.hits.Load() != 1 {
		t.Fatalf("static fetch through the real boundary: %+v %v hits=%d", result, err, f.hits.Load())
	}
	o, err := host.Reader.ReadObservation(t.Context(), f.flow, result.Seq)
	if err != nil || o.Path != "/read" || o.ResponseSHA256 != sha256.Sum256(result.Body) {
		t.Fatalf("observation for the fetched body: %+v %v", o, err)
	}
	seqs, err := host.Sequences(t.Context(), f.flow)
	if err != nil || len(seqs) != 1 || seqs[0] != result.Seq {
		t.Fatalf("sequences: %v %v", seqs, err)
	}
	if _, err := host.Fetch(t.Context(), f.target+"/read/admin/users"); !errors.Is(err, exploreboundary.ErrFetchRefused) || f.hits.Load() != 1 {
		t.Fatalf("excluded path reached the target: %v hits=%d", err, f.hits.Load())
	}
	browser, err := host.Session.StartBrowser(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	info := browser.Info()
	u, _ := url.Parse(info.MCP.URL)
	if info.Binding != f.binding || info.PackageVersion != exploration.MCPPackageVersion || info.MCP.Name != exploration.MCPServerName || info.MCP.Transport != "http" || u.Host != "explore-mcp:8931" {
		t.Fatalf("browser lease info: %+v", info)
	}
	other := f.binding
	other.SessionRef = "explore-host-other"
	if _, err := host.Session.StartBrowser(t.Context(), other); !errors.Is(err, exploration.ErrBinding) {
		t.Fatal("browser lease accepted another session binding")
	}
	state, err := lease.Close(t.Context(), exploration.Completed)
	if err != nil || state.Active || state.TargetRequests != 1 {
		t.Fatalf("close: %+v %v", state, err)
	}
	if _, err := host.Fetch(t.Context(), f.target+"/read"); err == nil && f.hits.Load() != 1 {
		t.Fatal("closed session still reached the target")
	}
}

// 대상 승인이 철회·만료되면 경계를 열지 않는다. 다른 대상·판으로는 host를 만들지 않는다.
func TestProductExplorationHostHonorsTargetApproval(t *testing.T) {
	f := newHostFixture(t)
	f.ended.Store(true)
	host, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}, &explorationPasswords{}, productExplorationLogin(nil))(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := host.Session.Boundary.Open(t.Context(), f.binding); err == nil || lease != nil && f.hits.Load() != 0 {
		t.Fatalf("revoked target opened the boundary: %v", err)
	}
	wrong := f.binding
	wrong.TargetRevision = 3
	if _, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}, &explorationPasswords{}, productExplorationLogin(nil))(t.Context(), wrong, &agent.Worker{}); !errors.Is(err, exploration.ErrBinding) {
		t.Fatal("host accepted another target revision")
	}
}

func TestProductExplorationPolicyValidation(t *testing.T) {
	good := productExplorationPolicy{ControlSocket: "/run/explore-control/control.sock", DirectProxy: "http://explore-boundary:3129", BoundaryCAFile: "/run/explore-ca/ca.pem",
		BoundaryCASHA256: strings.Repeat("a", 64), MCPURL: "http://explore-mcp:8931/mcp"}
	if good.validate() != nil {
		t.Fatal("valid policy rejected")
	}
	for name, change := range map[string]func(*productExplorationPolicy){
		"relative socket": func(p *productExplorationPolicy) { p.ControlSocket = "control.sock" },
		"long socket": func(p *productExplorationPolicy) {
			p.ControlSocket = "/" + strings.Repeat("s", maxExplorationSocketPath)
		},
		"https proxy":       func(p *productExplorationPolicy) { p.DirectProxy = "https://explore-boundary:3129" },
		"proxy path":        func(p *productExplorationPolicy) { p.DirectProxy = "http://explore-boundary:3129/x" },
		"proxy no port":     func(p *productExplorationPolicy) { p.DirectProxy = "http://explore-boundary" },
		"proxy credentials": func(p *productExplorationPolicy) { p.DirectProxy = "http://u:p@explore-boundary:3129" },
		"relative ca":       func(p *productExplorationPolicy) { p.BoundaryCAFile = "ca.pem" },
		"bad ca digest":     func(p *productExplorationPolicy) { p.BoundaryCASHA256 = strings.Repeat("A", 64) },
		"mcp path":          func(p *productExplorationPolicy) { p.MCPURL = "http://explore-mcp:8931/other" },
		"mcp https":         func(p *productExplorationPolicy) { p.MCPURL = "https://explore-mcp:8931/mcp" },
		"external cap":      func(p *productExplorationPolicy) { p.MaxExternalRequests = maxExplorationExternal + 1 },
	} {
		p := good
		change(&p)
		if p.validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := newProductExplorationHost(good, []byte("not a certificate")); err == nil {
		t.Fatal("host built without a boundary CA")
	}
}

func TestExploreTargetOfUsesRegisteredAddressAndPin(t *testing.T) {
	rec := productTargetRecord{SelectedIP: "10.0.0.5", Target: diagnosisprep.ProductTarget{ID: "t", Revision: 4, Origin: "https://app.owned.test:8443", Port: 8443,
		PathPrefixes: []string{"/api"}, Methods: []string{"GET"}, TLSSPKISHA256: strings.Repeat("b", 64), ExpiresAt: time.Unix(2000000000, 0)}}
	target, err := exploreTargetOf(rec, []string{"/api/admin"})
	if err != nil || target.Host != "app.owned.test" || target.Port != 8443 || target.ConnectIP != netip.MustParseAddr("10.0.0.5") || target.SPKISHA256 != rec.Target.TLSSPKISHA256 ||
		target.Ref != "t" || target.Revision != 4 || strings.Join(target.ExcludedPrefixes, ",") != "/api/admin" || len(target.RiskyWords) == 0 || !target.ExpiresAt.Equal(rec.Target.ExpiresAt) {
		t.Fatalf("target: %+v %v", target, err)
	}
	for name, change := range map[string]func(*productTargetRecord){
		"no ip":    func(r *productTargetRecord) { r.SelectedIP = "" },
		"http":     func(r *productTargetRecord) { r.Target.Origin = "http://app.owned.test:8443" },
		"no pin":   func(r *productTargetRecord) { r.Target.TLSSPKISHA256 = "" },
		"bad port": func(r *productTargetRecord) { r.Target.Port = 0 },
	} {
		r := rec
		change(&r)
		if _, err := exploreTargetOf(r, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// 설치 정책에 탐색 블록이 있으면 실행 옵션이 실제 host를 쓴다. 외부 자원 상한은 외부 통신 승인 모드에서만 준다.
func TestProductExplorationOptionsUseInstalledHost(t *testing.T) {
	runtime, loaded, execution, flow := productExplorationFixture(t, "target-owned", "7", 1)
	f := newHostFixture(t)
	f.host.maxExternalRequests = 6
	runtime.product = &productRuntime{exploration: f.host, targets: &productTargetRegistry{}}
	options, ok := runtime.productExplorationOptions(loaded, execution, "7", flow)
	if !ok || options.OpenHost == nil || options.Binding.Limits.MaxExternalRequests != 6 || options.Binding.Validate() != nil {
		t.Fatalf("installed exploration host not used: %+v", options.Binding.Limits)
	}
	loaded.launch.ModelExecutionMode = "local"
	options, _ = runtime.productExplorationOptions(loaded, execution, "7", flow)
	if options.OpenHost == nil || options.Binding.Limits.MaxExternalRequests != 0 || options.Binding.Validate() != nil {
		t.Fatalf("full local mode got external requests: %+v", options.Binding.Limits)
	}
	runtime.product.exploration = nil
	if options, _ = runtime.productExplorationOptions(loaded, execution, "7", flow); options.OpenHost != nil {
		t.Fatal("exploration host invented without an installed policy block")
	}
}

// 탐색 host는 이 회차 비밀번호 목록으로 저장 기록 가림을 건다. 목록을 만들지 못한 실행(nil)은 탐색을 열지 않는다(#875 발견 3).
func TestProductExplorationHostRecordRedaction(t *testing.T) {
	f := newHostFixture(t)
	connection := &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}
	if _, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, connection, nil, productExplorationLogin(nil))(t.Context(), f.binding, &agent.Worker{}); !errors.Is(err, exploration.ErrBinding) {
		t.Fatalf("host opened without a password list: %v", err)
	}
	passwords := &explorationPasswords{values: [][]byte{[]byte("tester-password")}}
	host, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, connection, passwords, productExplorationLogin(nil))(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	record := []byte(`{"password":"tester-password"}`)
	if host.Session.RedactRecord == nil || strings.Contains(string(host.Session.RedactRecord(record)), "tester-password") {
		t.Fatal("record redaction not bound to the run password list")
	}
	// 모델 입력 가림(Redact)은 정확값 가림을 하지 않는다(11/23 규칙). 자격 꼴 가림만 있다.
	if !strings.Contains(string(host.Session.Redact([]byte("tester-password"))), "tester-password") {
		t.Fatal("model input redaction changed")
	}
}

// 실행 옵션이 이 실행의 결속 계정 자격(실행 자료)으로 만든 목록을 host의 기록 가림까지 잇고, 실행 끝(clearMaterials)에
// 지운다(#875 발견 3, 2026-11-24 사용자 결정).
func TestProductExplorationOptionsBindRunPasswords(t *testing.T) {
	runtime, loaded, execution, flow := productExplorationFixture(t, "target-owned", "7", 1)
	f := newHostFixture(t)
	runtime.product = &productRuntime{exploration: f.host, targets: &productTargetRegistry{}}
	rec := f.rec
	rec.Target.Requests = 20
	loaded.productTarget = &rec
	loaded.productConnection = &productTargetConnection{IP: rec.SelectedIP, SPKI: rec.Target.TLSSPKISHA256, jobID: execution.Job.ID}
	loaded.policy.Auth = &server.E2HTTPFileAuth{Accounts: []server.E2HTTPFileAuthAccount{{Label: "a", Credential: server.E2HTTPFilePin{File: "cred-a.json"}}}}
	loaded.materials = map[string][]byte{"cred-a.json": []byte(`{"username":"tester-a","password":"run-tester-password"}`)}
	options, ok := runtime.productExplorationOptions(loaded, execution, "7", flow)
	if !ok || options.OpenHost == nil {
		t.Fatal("installed exploration host not used")
	}
	host, err := options.OpenHost(t.Context(), options.Binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(host.Session.RedactRecord([]byte(`{"password":"run-tester-password"}`))); strings.Contains(got, "run-tester-password") {
		t.Fatalf("run password left in record: %s", got)
	}
	loaded.clearMaterials()
	if got := string(host.Session.RedactRecord([]byte("after run"))); got != dlp.Redacted {
		t.Fatalf("record after run end: %s", got)
	}
}

// 모델 직접 로그인: 탐색 host는 경계 로그인 판정 설정 없이는 열지 않는다(연속 실패 상한 없이 로그인 POST가 나가지 않게).
// 판정 설정은 실행 자료의 자격과 제품 기본 상한으로 만든다.
func TestProductExplorationHostRequiresLoginConfig(t *testing.T) {
	f := newHostFixture(t)
	connection := &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}
	passwords := &explorationPasswords{values: [][]byte{[]byte("tester-password")}}
	if _, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, connection, passwords, nil)(t.Context(), f.binding, &agent.Worker{}); !errors.Is(err, exploration.ErrBinding) {
		t.Fatalf("판정 설정 없이 탐색을 엶: %v", err)
	}
	login := productExplorationLogin([]agent.E2HTTPAccountCredential{{Label: "tester-a", Username: "user1", Password: "alpha-pass-1"}})
	if len(login.Accounts) != 1 || login.Accounts[0].Label != "tester-a" || login.Accounts[0].Password != "alpha-pass-1" ||
		login.MaxConsecutiveFailures != productLoginConsecutiveFailures || login.MaxContrastProbes != productLoginContrastProbes || login.TokenMinBytes != productLoginTokenMinBytes {
		t.Fatalf("판정 설정: %+v", login.Accounts)
	}
	if empty := productExplorationLogin(nil); empty == nil || len(empty.Accounts) != 0 || empty.MaxConsecutiveFailures != productLoginConsecutiveFailures {
		t.Fatal("계정 없는 회차도 상한을 지켜야 함")
	}
}

// 탐색 로그인 요약(계약 §6): 탐색 단계 행만으로 계정별 결과를 §4 규칙으로 정해 0600 파일로 남긴다. 등록 계정마다 값이 있고
// 시도가 없는 계정은 not_attempted다.
func TestWriteLoginSummaryFromExplorationRows(t *testing.T) {
	dir := t.TempDir()
	rows := []loginattempt.Row{
		{Schema: loginattempt.Schema, AttemptRef: "exploration-1", AccountLabel: "tester-a", Outcome: loginattempt.OutcomeFailed},
		{Schema: loginattempt.Schema, AttemptRef: "exploration-2", AccountLabel: "tester-a", Outcome: loginattempt.OutcomeUnconfirmed},
		{Schema: loginattempt.Schema, AttemptRef: "exploration-3", AccountLabel: "tester-b", Outcome: loginattempt.OutcomeUnconfirmed},
		{Schema: loginattempt.Schema, AttemptRef: "exploration-3", AccountLabel: "tester-b", Outcome: loginattempt.OutcomeSucceeded},
	}
	if err := writeLoginSummary(dir, loginattempt.AccountAuthOfRows(rows, []string{"tester-a", "tester-b", "tester-c"})); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, loginSummaryFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("요약 파일: %v %v", info, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, loginSummaryFile))
	var got struct {
		Format      string            `json:"format"`
		Exploration map[string]string `json:"exploration"`
	}
	if json.Unmarshal(raw, &got) != nil || got.Format != loginSummaryFormat || got.Exploration["tester-a"] != loginattempt.AuthUnconfirmed ||
		got.Exploration["tester-b"] != loginattempt.AuthAuthenticated || got.Exploration["tester-c"] != loginattempt.AuthNotAttempted {
		t.Fatalf("요약: %s", raw)
	}
}

// 탐색 로그인 기록 인계(#969 동찬 리뷰 P2): 표지는 탐색 시작 때 쓰고 모두 넘겼을 때만 지운다. 경계 조회 실패·빠진 행·기록
// 쓰기 실패면 표지가 남고, 기록과 표지 저장이 모두 실패해도 시작 때 표지가 남아 진단 단계가 로그인 시도를 보내지 않는다.
func TestExplorationLoginHandoverKeepsMarkerUntilConfirmed(t *testing.T) {
	rows := []loginattempt.Row{{Schema: loginattempt.Schema, AttemptRef: "exploration-1", Identifier: "tester@owned.test", AccountLabel: "tester-a", Outcome: loginattempt.OutcomeFailed}}
	marker := func(dir string) bool {
		_, err := os.Lstat(filepath.Join(dir, server.E2HTTPLoginHistoryUnavailableFile))
		return err == nil
	}
	ok := func() ([]loginattempt.Row, uint64, error) { return rows, 0, nil }
	for _, tc := range []struct {
		name       string
		read       func() ([]loginattempt.Row, uint64, error)
		readOnly   bool
		block      string
		handedOver bool
	}{
		{"handed over", ok, false, "", true},
		{"no attempts", func() ([]loginattempt.Row, uint64, error) { return nil, 0, nil }, false, "", true},
		{"read failure", func() ([]loginattempt.Row, uint64, error) { return nil, 0, errors.New("boundary unavailable") }, false, "", false},
		{"omitted rows", func() ([]loginattempt.Row, uint64, error) { return rows, 1, nil }, false, "", false},
		// 기록 쓰기만 실패한다(기록 자리에 디렉터리). 표지는 지울 수 있어도 남아야 한다.
		{"record write fails", ok, false, server.E2HTTPLoginAttemptsFile, false},
		// 기록은 넘겼지만 표지를 지우지 못한다(표지 자리에 비어 있지 않은 디렉터리). 인계 확인이 아니다.
		{"marker removal fails", ok, false, server.E2HTTPLoginHistoryUnavailableFile, false},
		// 기록 쓰기와 표지 다시 쓰기·지우기가 모두 실패한다(디렉터리 쓰기 불가).
		{"record and marker writes fail", ok, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.block != "" {
				if err := os.MkdirAll(filepath.Join(dir, tc.block, "keep"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.block != server.E2HTTPLoginHistoryUnavailableFile {
				if err := server.MarkLoginHistoryUnavailable(dir); err != nil {
					t.Fatal(err)
				}
			}
			if tc.readOnly {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			if got := handOverExplorationLogins(dir, tc.read, []string{"tester-a"}); got != tc.handedOver || marker(dir) == tc.handedOver {
				t.Fatalf("인계 %v·표지 %v, 기대 인계 %v", got, marker(dir), tc.handedOver)
			}
		})
	}
}

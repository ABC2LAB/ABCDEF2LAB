//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/miekg/dns"
)

func TestC35IssuedPolicyUsesJobConnectionAndPreservesDraft(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec, err := r.lookup("ARTEX", target.Origin+"/read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.20")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("b", 64), time.Now().Add(time.Hour), nil
	}
	l := connectionTestProfile(r, rec)
	now := time.Now()
	l.profile = urlPreparationProfile{ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Budget: diagnosisprep.Budget{DurationSeconds: 30}}
	l.launch.ShutdownTimeoutSeconds = 1
	l.inspection.InstallationDigest = "unchanged-installation"
	originalRules := append([]server.E2HTTPFileRule(nil), l.policy.Rules...)
	originalInspection := l.inspection
	if err = l.prepareTargetConnection(t.Context(), nil, "job-policy"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(l.inspection, originalInspection) || originalRules[0].ConnectIP != rec.SelectedIP {
		t.Fatal("접속 갱신이 초안이나 공유 템플릿을 변경")
	}
	runs := t.TempDir()
	if os.Chmod(runs, 0700) != nil {
		t.Fatal("fixture permissions")
	}
	b := &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runs}}
	actual := server.FirstDiagnosisPreparedTask{TaskID: 42, ExplorationID: 43}
	if _, err = b.issue(l, "other-job", actual); err == nil {
		t.Fatal("다른 회차의 접속 증거 수용")
	}
	connection := l.productConnection
	l.productConnection = nil
	if _, err = b.issue(l, "job-policy", actual); err == nil {
		t.Fatal("접속 증거 없이 권위 발급")
	}
	l.productConnection = connection
	launch, err := b.issue(l, "job-policy", actual)
	if err != nil {
		t.Fatal("양성 권위 발급 실패", err)
	}
	raw, err := os.ReadFile(launch.HTTP.PolicyFile)
	if err != nil {
		t.Fatal(err)
	}
	var policy server.E2HTTPFilePolicy
	if json.Unmarshal(raw, &policy) != nil || policy.RunRef != "run-job-policy" || policy.Rules[0].ConnectIP != "10.200.1.20" || policy.Rules[0].TLSSPKISHA256 != strings.Repeat("b", 64) || policy.Rules[0].Origin != rec.Target.Origin || policy.Rules[0].PathPrefix != "/read" {
		t.Fatal("실제 실행 정책에 회차별 증거·범위 미결속")
	}
	var authority server.FirstDiagnosisHTTPApproval
	a, err := os.ReadFile(launch.HTTP.AuthorityFile)
	if err != nil || json.Unmarshal(a, &authority) != nil || authority.PolicySHA256 != urlSHA(raw) || authority.TaskID != 42 {
		t.Fatal("실제 권위가 새 정책 digest와 불일치")
	}
	if _, err := os.Stat(filepath.Join(runs, "other-job")); !os.IsNotExist(err) {
		t.Fatal("다른 job 거부 전에 권위 파일 생성")
	}
}

func TestC35ExplorationConnectionCannotCrossJobs(t *testing.T) {
	f := newHostFixture(t)
	connection := &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}
	// 승인 이력의 옛 주소로 연결하면 실패한다. 탐색도 새 실행 증거를 써야 수신기에 도달한다.
	f.rec.SelectedIP = "192.0.2.254"
	f.rec.Target.TLSSPKISHA256 = strings.Repeat("e", 64)
	factory := f.host.factory(f.rec, f.current, f.digest, nil, 4096, connection)
	connection.IP = "10.200.1.99" // factory가 참조 대신 값을 보존해야 한다.
	host, err := factory(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal("양성 탐색 결속 실패", err)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal("실제 탐색 경계 결속 실패", err)
	}
	t.Cleanup(func() { _, _ = lease.Close(context.Background(), exploration.Completed) })
	result, err := host.Fetch(t.Context(), f.target+"/read")
	if err != nil || !result.Complete || f.hits.Load() != 1 {
		t.Fatal("탐색 경계가 이전 주소·SPKI 또는 변경된 참조를 사용", err)
	}
	wrong := f.binding
	wrong.JobID = "another-job"
	if _, err := factory(t.Context(), wrong, &agent.Worker{}); err == nil {
		t.Fatal("탐색에서 다른 job의 IP/SPKI 사용")
	}
	if _, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, nil)(t.Context(), f.binding, &agent.Worker{}); err == nil {
		t.Fatal("접속 검증 없이 탐색 host 조립")
	}
}

type c35DNSState struct {
	mu      sync.Mutex
	ips     []string
	proof   string
	queries atomic.Int64
}

func (s *c35DNSState) ServeDNS(w dns.ResponseWriter, request *dns.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries.Add(1)
	answer := new(dns.Msg)
	answer.SetReply(request)
	for _, q := range request.Question {
		switch q.Qtype {
		case dns.TypeTXT:
			if s.proof != "" {
				answer.Answer = append(answer.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{s.proof}})
			}
		case dns.TypeA:
			for _, ip := range s.ips {
				answer.Answer = append(answer.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP(ip)})
			}
		}
	}
	_ = w.WriteMsg(answer)
}
func (s *c35DNSState) change(ips []string, proof string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ips, s.proof = append([]string(nil), ips...), proof
}

type c35Receiver struct {
	certificate atomic.Pointer[tls.Certificate]
	hits        atomic.Int64
	server      *http.Server
	port        int
}

func newC35Receiver(t *testing.T, ip string, port int, cert tls.Certificate) *c35Receiver {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		t.Fatal("소유 IP 수신기 기동 실패", err)
	}
	r := &c35Receiver{port: listener.Addr().(*net.TCPAddr).Port}
	r.certificate.Store(&cert)
	r.server = &http.Server{ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		w.Header().Set("Connection", "close")
		if req.URL.Path == "/read/redirect" {
			w.Header().Set("Location", "https://unapproved.example.test/read")
			w.WriteHeader(302)
			return
		}
		_, _ = io.WriteString(w, "owned fixture")
	})}
	go func() {
		_ = r.server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return r.certificate.Load(), nil }}))
	}()
	t.Cleanup(func() { _ = r.server.Close() })
	return r
}

// opt-in 주소도 이 호스트의 실제 인터페이스에 있는 사설 IPv4 두 개여야 한다. 정책 예외는 만들지 않는다.
func c35OwnedIPs(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("ARTEX_C35_OWNED_IPS")
	if raw == "" {
		t.Skip("소유 DNS/TLS 수신기 주소 opt-in 미제공")
	}
	ips := strings.Split(raw, ",")
	if len(ips) != 2 || ips[0] == ips[1] {
		t.Fatal("서로 다른 소유 주소 두 개 필요")
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]bool{}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil {
			owned[prefix.Addr().Unmap().String()] = true
		}
	}
	for _, raw := range ips {
		ip, err := netip.ParseAddr(raw)
		if err != nil || !ip.Is4() || !ip.IsPrivate() || !owned[raw] {
			t.Fatal("호스트 미소유·비사설 주소로 시험 금지")
		}
	}
	return ips
}

type c35E2Recorder struct{}

func (c35E2Recorder) Record(context.Context, e2proxy.Event) error { return nil }

func c35E2Call(t *testing.T, r *productTargetRegistry, l *urlLoadedProfile, roots *x509.CertPool, job string) *e2proxy.Call {
	t.Helper()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 20})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: job, BudgetRef: job}, Binding: budget.Binding{PolicyRef: job, ResetRef: "owned-reset"}, MaxRequests: 20, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	if ledger.Register(grant, now) != nil {
		t.Fatal("예산 준비 실패")
	}
	rule := l.policy.Rules[0]
	cfg := e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: transmit.Scope{TaskRef: job, RunRef: job, CaseRef: "owned-case", ResetRef: "owned-reset", PolicyRef: job}, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "v1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: grant.ValidUntil,
		Rules:   []e2proxy.Rule{{Ref: "owned-rule", Origin: rule.Origin, ConnectIP: netip.MustParseAddr(rule.ConnectIP), TLSSPKISHA256: rule.TLSSPKISHA256, PathPrefix: rule.PathPrefix, PathScopeRef: "owned-read", Methods: rule.Methods}},
		RootCAs: roots, Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: c35E2Recorder{}, CheckAuthority: func() error { return r.current(*l.productTarget) },
		Limits: e2proxy.Limits{MaxAttempts: 20, MaxCalls: 2, MaxConcurrent: 1, MaxBodyBytes: 4096, RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 20, MaxTransient: 20, MaxUnknown: 20}}
	execution, err := e2proxy.NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal("E② 구성 실패", err)
	}
	call, err := execution.BeginCall(t.Context(), "owned-command")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = call.Close(context.Background()); _ = execution.Stop(context.Background()) })
	return call
}

func TestC35OwnedDNSTLSRotationAndE2Isolation(t *testing.T) {
	ips := c35OwnedIPs(t)
	ca := newHostTestCA(t, "owned-c35-ca")
	leafA, pinA := ca.leaf(t, "rotation.example.test")
	leafB, pinB := ca.leaf(t, "rotation.example.test")
	one := newC35Receiver(t, ips[0], 0, leafA)
	two := newC35Receiver(t, ips[1], one.port, leafB)
	state := &c35DNSState{ips: []string{ips[0]}}
	packet, err := net.ListenPacket("udp", net.JoinHostPort(ips[0], "0"))
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	dnsServer := &dns.Server{PacketConn: packet, Handler: state, NotifyStartedFunc: func() { close(ready) }}
	go func() { _ = dnsServer.ActivateAndServe() }()
	<-ready
	t.Cleanup(func() { _ = dnsServer.Shutdown() })
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("fixture permissions")
	}
	policy := productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{one.port}, PrivateCIDRs: []string{ips[0] + "/32", ips[1] + "/32"}, MaxTargets: 32, MaxRequests: 4, ResolverAddress: packet.LocalAddr().String()}
	r, err := newProductTargetRegistry(root, policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	r.roots = ca.pool
	r.endpointAllowed = func(string, netip.Addr) bool { return true }
	spec := targetTestSpec("rotation.example.test")
	spec.URL = "https://rotation.example.test:" + strconv.Itoa(one.port) + "/read"
	target, err := r.register("ARTEX", spec)
	if err != nil {
		t.Fatal(err)
	}
	state.change([]string{ips[0]}, target.ChallengeValue)
	if _, err = r.approve(t.Context(), "ARTEX", target.ID); err != nil {
		t.Fatal("실제 DNS/TLS 승인 실패", err)
	}
	rec, err := r.lookup("ARTEX", spec.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	original := productTargetCopy(rec)
	prepare := func(job string) *urlLoadedProfile {
		t.Helper()
		l := connectionTestProfile(r, rec)
		if err := l.prepareTargetConnection(t.Context(), nil, job); err != nil {
			t.Fatal("실제 시작 검사 실패", err)
		}
		return l
	}
	send := func(call *e2proxy.Call, path string, want bool) e2proxy.Receipt {
		t.Helper()
		req, err := http.NewRequest("GET", path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, receipt, err := call.Do(t.Context(), req)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		if want && (err != nil || response == nil || !receipt.ResponseReceived) || !want && (err == nil || response != nil) {
			t.Fatal("실제 E② 전송 결과 불일치", receipt.Reason)
		}
		return receipt
	}
	first := prepare("owned-job-a")
	callA := c35E2Call(t, r, first, ca.pool, "owned-job-a")
	send(callA, spec.URL, true)
	state.change([]string{ips[1]}, target.ChallengeValue)
	second := prepare("owned-job-b")
	callB := c35E2Call(t, r, second, ca.pool, "owned-job-b")
	send(callB, spec.URL, true)
	send(callA, spec.URL, true) // DNS가 바뀌어도 이전 실행은 주소·SPKI를 유지한다.
	if one.hits.Load() != 2 || two.hits.Load() != 1 || first.productConnection.IP != ips[0] || first.productConnection.SPKI != pinA || second.productConnection.IP != ips[1] || second.productConnection.SPKI != pinB || !reflect.DeepEqual(original, r.records[target.ID]) {
		t.Fatal("실제 실행별 주소·SPKI·승인 이력 혼입")
	}
	one.certificate.Store(&leafB)
	send(callA, spec.URL, false)
	if one.hits.Load() != 2 {
		t.Fatal("기존 실행이 변경된 인증서로 HTTP 송신")
	}
	state.change([]string{ips[0]}, target.ChallengeValue)
	third := prepare("owned-job-c")
	callC := c35E2Call(t, r, third, ca.pool, "owned-job-c")
	send(callC, spec.URL, true)
	send(callC, spec.URL+"/redirect", true)
	before := one.hits.Load() + two.hits.Load()
	for _, path := range []string{"https://unapproved.example.test/read", strings.Replace(spec.URL, "/read", "/outside", 1)} {
		send(callC, path, false)
	}
	if one.hits.Load()+two.hits.Load() != before {
		t.Fatal("리다이렉트·범위 이탈 전송")
	}
	badLeaf, _ := ca.leaf(t, "wrong.example.test")
	otherCA := newHostTestCA(t, "untrusted-c35-ca")
	untrusted, _ := otherCA.leaf(t, "rotation.example.test")
	parsed, _ := x509.ParseCertificate(leafB.Certificate[0])
	parsed.NotAfter = time.Now().Add(-time.Second)
	parsed.NotBefore = time.Now().Add(-time.Hour)
	expiredDER, err := x509.CreateCertificate(rand.Reader, parsed, ca.cert, parsed.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	expired := tls.Certificate{Certificate: [][]byte{expiredDER}, PrivateKey: leafB.PrivateKey}
	for _, item := range []struct {
		name  string
		ips   []string
		proof string
		cert  tls.Certificate
	}{
		{"mixed_metadata", []string{ips[0], "169.254.169.254"}, target.ChallengeValue, leafB},
		{"policy_outside", []string{ips[0], "192.0.2.254"}, target.ChallengeValue, leafB},
		{"txt_missing", []string{ips[0]}, "", leafB},
		{"hostname", []string{ips[0]}, target.ChallengeValue, badLeaf},
		{"untrusted", []string{ips[0]}, target.ChallengeValue, untrusted},
		{"expired", []string{ips[0]}, target.ChallengeValue, expired},
	} {
		t.Run(item.name, func(t *testing.T) {
			state.change(item.ips, item.proof)
			one.certificate.Store(&item.cert)
			l := connectionTestProfile(r, rec)
			if err := l.prepareTargetConnection(t.Context(), nil, "negative-"+item.name); err == nil || l.productConnection != nil {
				t.Fatal("실제 DNS/TLS 음성 검증을 성공으로 승격")
			}
			if one.hits.Load()+two.hits.Load() != before {
				t.Fatal("실패한 시작이 대상 HTTP에 도달")
			}
		})
	}
	state.change([]string{ips[0]}, target.ChallengeValue)
	one.certificate.Store(&leafB)
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	dnsBefore := state.queries.Load()
	if l := connectionTestProfile(r, rec); l.prepareTargetConnection(t.Context(), nil, "after-revoke") == nil {
		t.Fatal("철회 후 새 실행 수용")
	}
	for _, call := range []*e2proxy.Call{callA, callB, callC} {
		receipt := send(call, spec.URL, false)
		if !receipt.NotSent || receipt.ForwardAttempted {
			t.Fatal("철회 후 실제 송신 시도")
		}
	}
	if state.queries.Load() != dnsBefore || one.hits.Load()+two.hits.Load() != before {
		t.Fatal("철회 후 새 DNS·HTTP 접촉")
	}
	if r.Close() != nil {
		t.Fatal("감사 마감 실패")
	}
	journal, records, err := diagnosisprep.OpenProtectedJournal(filepath.Join(root, "tls-contacts"))
	if err != nil {
		t.Fatal("감사 복원 실패")
	}
	defer journal.Close()
	wantPins := map[string]string{"owned-job-a": pinA, "owned-job-b": pinB, "owned-job-c": pinB}
	for _, raw := range records {
		var event productTLSContact
		if json.Unmarshal(raw, &event) != nil {
			t.Fatal("감사 형식")
		}
		if pin, ok := wantPins[event.JobID]; ok && event.Phase == "completed" && event.Result == "verified" {
			if event.TLSSPKISHA256 != pin || event.TargetID != target.ID {
				t.Fatal("감사에 회차별 SPKI 미결속")
			}
			delete(wantPins, event.JobID)
		}
	}
	if len(wantPins) != 0 {
		t.Fatal("실제 TLS 접촉의 회차별 감사 누락")
	}
}

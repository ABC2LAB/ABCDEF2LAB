//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
	"github.com/miekg/dns"
)

// 보호 파일 검증과 DNS 이전 경계를 위한 fixture다. DB·모델·대상은 연결만 세고 닫는다.
// 실제 DB, 모델, helper 또는 외부 DNS를 실행하지 않는다.
type urlNegativeFixture struct {
	t                       *testing.T
	path                    string
	config                  urlPreparationInstallation
	launch, apiLaunch       launchConfig
	policy                  server.E2HTTPFilePolicy
	input                   diagnosisprep.Input
	database, model, target *urlNegativeProbe
}

type urlNegativeProbe struct {
	listener net.Listener
	calls    atomic.Int64
	done     chan struct{}
}

func newURLNegativeProbe(t *testing.T) *urlNegativeProbe {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &urlNegativeProbe{listener: listener, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.calls.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-p.done
	})
	return p
}

func (p *urlNegativeProbe) port() int {
	return p.listener.Addr().(*net.TCPAddr).Port
}

func urlNegativeWrite(t *testing.T, path string, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return urlSHA(raw)
}

func newURLNegativeFixture(t *testing.T) *urlNegativeFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("보호 설치의 비-root 운영자 요구 사항")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	f := &urlNegativeFixture{t: t, path: filepath.Join(root, "installation.json"),
		database: newURLNegativeProbe(t), model: newURLNegativeProbe(t), target: newURLNegativeProbe(t)}
	origin := fmt.Sprintf("https://url-runtime-gates.invalid:%d", f.target.port())
	now := time.Now().UTC()
	budget := diagnosisprep.Budget{Requests: 2, ModelRequests: 2, InputTokens: 8192, OutputTokens: 2048, DurationSeconds: 30}
	material := func(name, value string, mode os.FileMode) server.E2HTTPFilePin {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), mode); err != nil {
			t.Fatal(err)
		}
		return server.E2HTTPFilePin{File: name, SHA256: urlSHA([]byte(value))}
	}
	helper := material("helper", "#!/bin/sh\nexit 1\n", 0700)
	f.policy = server.E2HTTPFilePolicy{
		Format: server.E2HTTPFilePolicyFormat, OwnerRef: "ARTEX",
		HeaderMode: e2proxy.HeaderPreserve, ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(5 * time.Minute), MaxRequests: uint64(budget.Requests),
		Helper: filepath.Join(root, helper.File), HelperSHA256: helper.SHA256,
		CACert:   material("ca.pem", "owned-unused-ca", 0600),
		CAKey:    material("ca-key.pem", "owned-unused-key", 0600),
		AuditKey: material("audit-key", strings.Repeat("a", 32), 0600),
		Rules: []server.E2HTTPFileRule{{
			Ref: "owned-rule", Origin: origin, ConnectIP: "127.0.0.1", PathPrefix: "/read",
			PathScopeRef: "owned-path", Methods: []string{"GET"},
			ApprovalDisplay: server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}},
		}},
		Limits: server.E2HTTPFileLimits{MaxConcurrent: 1, MaxBodyBytes: 1024},
	}
	f.launch = launchConfig{
		Format: launchFormat, Mode: "general_http", ListenAddr: "127.0.0.1:28080",
		DataDir: dir("data"), KeyDir: dir("keys"), SkillDir: dir("skills"),
		LocalLLMManifestPath: filepath.Join(root, "models.json"), ModelExecutionMode: "local_only",
		ModelProfileID: 1, ModelFormat: "openai", ModelBaseURL: "http://" + f.model.listener.Addr().String() + "/v1", ModelRef: "owned-synthetic",
		RuntimeDSNFile: filepath.Join(root, "runtime.dsn"), ShutdownTimeoutSeconds: 1,
		HTTP: &httpLaunch{PolicyFile: filepath.Join(root, "policy.json"), AuthorityFile: filepath.Join(root, "authority.json"), AuthorityRoot: root},
	}
	dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=owned password='' dbname=owned sslmode=disable connect_timeout=1", f.database.port())
	if err := os.WriteFile(f.launch.RuntimeDSNFile, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	f.apiLaunch = f.launch
	f.apiLaunch.ListenAddr = "127.0.0.1:28081"
	f.config = urlPreparationInstallation{
		Format: urlPreparationFormat, OperatorUID: uint32(os.Geteuid()),
		StateDir: dir("state"), RunsDir: dir("runs"), SocketPath: filepath.Join(root, "prepare.sock"),
		ReportListenAddr: f.launch.ListenAddr, APILaunch: urlProtectedPin{File: filepath.Join(root, "api-launch.json")},
		ProtectedEndpoints: []urlProtectedEndpoint{
			{Host: "127.0.0.1", Port: f.database.port(), Addresses: []string{"127.0.0.1"}},
			{Host: "127.0.0.1", Port: f.model.port(), Addresses: []string{"127.0.0.1"}},
		},
		Profiles: []urlPreparationProfile{{
			ID: "owned-profile", Label: "소유 런타임 경계 시험", Principal: "ARTEX",
			TargetRef: "owned-target", ScopeRef: "owned-scope", ModelProfileRef: "owned-model", ModelLabel: "합성 모델",
			DelegationRef: "owned-delegation", DeriveRuns: true, MaxRuns: 1,
			ValidFrom: now.Add(-30 * time.Second), ValidUntil: now.Add(4 * time.Minute),
			Budget: budget, AggregateBudget: budget, Launch: urlProtectedPin{File: filepath.Join(root, "launch.json")},
			AllowPrivateTarget: true, ModelLimitsRef: "owned-limits", InputTokensPerCall: 4096, OutputTokensPerCall: 1024,
			Attestation: urlNonmutatingAttestation{Ref: "owned-attestation", Verified: true, PathPrefixes: []string{"/read"},
				Methods: []string{"GET"}, QueryPolicy: "any", BodyPolicy: "bounded", MaxBodyBytes: 1024},
		}},
	}
	f.input = diagnosisprep.Input{TargetURL: origin + "/read/item", ProfileID: "owned-profile"}
	f.write()
	return f
}

func (f *urlNegativeFixture) write() {
	f.t.Helper()
	p := &f.config.Profiles[0]
	p.ManifestSHA256 = urlNegativeWrite(f.t, f.apiLaunch.LocalLLMManifestPath, map[string]any{
		"version": 1, "profiles": []any{map[string]any{
			"profile_id": f.apiLaunch.ModelProfileID, "format": f.apiLaunch.ModelFormat,
			"model": f.apiLaunch.ModelRef, "base_url": f.apiLaunch.ModelBaseURL, "addresses": []string{"127.0.0.1"},
		}},
	})
	p.PolicySHA256 = urlNegativeWrite(f.t, f.launch.HTTP.PolicyFile, f.policy)
	p.Launch.SHA256 = urlNegativeWrite(f.t, p.Launch.File, f.launch)
	f.config.APILaunch.SHA256 = urlNegativeWrite(f.t, f.config.APILaunch.File, f.apiLaunch)
	urlNegativeWrite(f.t, f.path, f.config)
}

func (f *urlNegativeFixture) runtime() *urlPreparationRuntime {
	f.t.Helper()
	b, err := newURLPreparationRuntime(f.t.Context(), f.path)
	if err != nil {
		f.t.Fatalf("유효한 설치 fixture 거부: %v", err)
	}
	return b
}

func (f *urlNegativeFixture) inspect(b *urlPreparationRuntime) diagnosisprep.Inspection {
	f.t.Helper()
	got, err := b.Inspect(f.t.Context(), "ARTEX", f.input)
	if err != nil || got.ReasonCode != "" || got.Scope == nil || got.InstallationDigest == "" || got.DelegationRef != f.config.Profiles[0].DelegationRef {
		f.t.Fatalf("정상 대조 Inspect 실패: err=%v reason=%s scope=%t", err, got.ReasonCode, got.Scope != nil)
	}
	return got
}

func (f *urlNegativeFixture) load() (*urlLoadedProfile, error) {
	f.t.Helper()
	c, pin, err := readURLInstallation(f.path)
	if err != nil {
		f.t.Fatalf("설치 읽기 실패: %v", err)
	}
	return urlLoadProfile(c, pin, c.Profiles[0])
}

func (f *urlNegativeFixture) assertNoConnections() {
	f.t.Helper()
	if d, m, target := f.database.calls.Load(), f.model.calls.Load(), f.target.calls.Load(); d != 0 || m != 0 || target != 0 {
		f.t.Fatalf("거부 경계 이후 연결 발생: db=%d model=%d target=%d", d, m, target)
	}
}

// 사례별 resolver를 사용하며 전역 DNS를 바꾸지 않는다. Dial은 net.Pipe만 사용한다.
func urlNegativeDNS(t *testing.T, addresses ...string) (*net.Resolver, *atomic.Int64) {
	t.Helper()
	queries := &atomic.Int64{}
	resolver := &net.Resolver{PreferGo: true, StrictErrors: true}
	resolver.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, peer := net.Pipe()
		go func() {
			defer peer.Close()
			_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
			wire := &dns.Conn{Conn: peer}
			request, err := wire.ReadMsg()
			if err != nil {
				return
			}
			queries.Add(1)
			response := new(dns.Msg)
			response.SetReply(request)
			response.Authoritative = true
			for _, q := range request.Question {
				if !strings.EqualFold(q.Name, "url-runtime-gates.invalid.") {
					response.Rcode = dns.RcodeNameError
					continue
				}
				for _, raw := range addresses {
					ip := netip.MustParseAddr(raw)
					header := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 1}
					switch {
					case q.Qtype == dns.TypeA && ip.Is4():
						response.Answer = append(response.Answer, &dns.A{Hdr: header, A: net.IP(ip.AsSlice())})
					case q.Qtype == dns.TypeAAAA && ip.Is6():
						response.Answer = append(response.Answer, &dns.AAAA{Hdr: header, AAAA: net.IP(ip.AsSlice())})
					}
				}
			}
			_ = wire.WriteMsg(response)
		}()
		return client, nil
	}
	return resolver, queries
}

func TestURLPreparationInspectRejectsUnapprovedInput(t *testing.T) {
	for _, name := range []string{"origin", "outside_path", "sibling_path", "model_mode"} {
		t.Run(name, func(t *testing.T) {
			f := newURLNegativeFixture(t)
			resolver, queries := urlNegativeDNS(t, "127.0.0.1")
			b := f.runtime()
			b.resolver = resolver
			f.inspect(b)
			input := f.input
			reason := "path_not_approved"
			switch name {
			case "origin":
				input.TargetURL = strings.Replace(input.TargetURL, "url-runtime-gates.invalid", "different.invalid", 1)
				reason = "target_not_approved"
			case "outside_path":
				input.TargetURL = f.policy.Rules[0].Origin + "/other"
			case "sibling_path":
				input.TargetURL = f.policy.Rules[0].Origin + "/reader"
			case "model_mode":
				f.launch.ModelExecutionMode = "openrouter"
				f.launch.LocalLLMManifestPath = ""
				f.launch.OpenRouterManifestPath = filepath.Join(filepath.Dir(f.path), "unused-openrouter.json")
				f.launch.ModelBaseURL, f.launch.ModelRef = llmpolicy.OpenRouterBaseURL, llmpolicy.OpenRouterModel
				f.write()
				reason = "model_mode_unsupported"
			}
			got, err := b.Inspect(t.Context(), "ARTEX", input)
			if err != nil || got.ReasonCode != reason || got.Scope != nil || got.InstallationDigest != "" {
				t.Fatalf("허용되지 않은 입력을 거부하지 않음: err=%v reason=%s scope=%t", err, got.ReasonCode, got.Scope != nil)
			}
			if queries.Load() != 0 {
				t.Fatal("Inspect에서 DNS 조회 발생")
			}
			f.assertNoConnections()
		})
	}
}

func TestURLPreparationLoadProfileRejectsUnsafePolicy(t *testing.T) {
	tests := []struct {
		name, reason string
		change       func(*urlNegativeFixture)
	}{
		{"protected_host", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.config.ProtectedEndpoints = append(f.config.ProtectedEndpoints, urlProtectedEndpoint{Host: "url-runtime-gates.invalid", Port: f.target.port(), Addresses: []string{"127.0.0.2"}})
		}},
		{"protected_address", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.config.ProtectedEndpoints = append(f.config.ProtectedEndpoints, urlProtectedEndpoint{Host: "other.invalid", Port: f.target.port(), Addresses: []string{"127.0.0.1"}})
		}},
		{"report_endpoint", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.config.ReportListenAddr = f.target.listener.Addr().String()
		}},
		{"api_endpoint", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.apiLaunch.ListenAddr = f.target.listener.Addr().String()
		}},
		{"credentialed_rule", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.policy.Rules[0].Credentials = &server.E2HTTPFilePin{File: "unused-credentials.json", SHA256: urlSHA([]byte("unused"))}
		}},
		{"attestation_path", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.config.Profiles[0].Attestation.PathPrefixes = []string{"/other"}
		}},
		{"attestation_method", "installation_target_invalid", func(f *urlNegativeFixture) {
			f.config.Profiles[0].Attestation.Methods = []string{"HEAD"}
		}},
		{"zero_concurrency", "installation_policy_invalid", func(f *urlNegativeFixture) {
			f.policy.Limits.MaxConcurrent = 0
		}},
		{"multiple_concurrency", "installation_policy_invalid", func(f *urlNegativeFixture) {
			f.policy.Limits.MaxConcurrent = 2
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newURLNegativeFixture(t)
			valid, err := f.load()
			if err != nil || valid == nil || valid.inspection.Scope == nil {
				t.Fatalf("정상 대조 profile 거부: %v", err)
			}
			valid.clearMaterials()
			tc.change(f)
			f.write()
			loaded, err := f.load()
			if loaded != nil {
				defer loaded.clearMaterials()
			}
			var failure urlPreparationError
			if !errors.As(err, &failure) || string(failure) != tc.reason || loaded != nil {
				t.Fatalf("정확한 정책 경계에서 거부하지 않음: err=%v loaded=%t", err, loaded != nil)
			}
			f.assertNoConnections()
		})
	}
}

func TestURLPreparationDNSRejectsMixedPinnedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		allowed   bool
	}{
		{"pinned_only", []string{"127.0.0.1"}, true},
		{"pin_then_different", []string{"127.0.0.1", "127.0.0.2"}, false},
		{"different_then_pin", []string{"127.0.0.2", "127.0.0.1"}, false},
		{"mixed_family", []string{"127.0.0.1", "::1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver, queries := urlNegativeDNS(t, tc.addresses...)
			l := &urlLoadedProfile{origin: "https://url-runtime-gates.invalid", address: netip.MustParseAddr("127.0.0.1")}
			err := l.dnsCheck(t.Context(), resolver)
			if (err == nil) != tc.allowed || queries.Load() == 0 {
				t.Fatalf("DNS 승인 pin 대조 실패: allowed=%t err=%v queries=%d", tc.allowed, err, queries.Load())
			}
		})
	}
}

func TestURLPreparationExecuteChecksDNSBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		addresses    []string
		wantDatabase bool
	}{
		{"pinned_control", "database_unavailable", []string{"127.0.0.1"}, true},
		{"mixed_denied", "target_dns_mismatch", []string{"127.0.0.1", "127.0.0.2"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newURLNegativeFixture(t)
			resolver, queries := urlNegativeDNS(t, tc.addresses...)
			b := f.runtime()
			b.resolver = resolver
			inspection := f.inspect(b)
			var updates []diagnosisprep.Update
			err := b.Execute(t.Context(), diagnosisprep.Execution{
				Principal: "ARTEX", Job: diagnosisprep.Job{ID: "owned-dns-gate", Input: f.input, Scope: inspection.Scope}, Inspection: inspection,
			}, func(u diagnosisprep.Update) error { updates = append(updates, u); return nil })
			if err != nil || queries.Load() == 0 || len(updates) < 2 {
				t.Fatalf("Execute의 DNS 경계까지 도달하지 못함: err=%v queries=%d updates=%d", err, queries.Load(), len(updates))
			}
			last := updates[len(updates)-1]
			if last.State != diagnosisprep.Failed || last.Phase != diagnosisprep.Closure || last.ReasonCode != tc.reason || !last.DrainComplete {
				t.Fatalf("예상한 거부 결과 없음: state=%s phase=%s reason=%s drained=%t", last.State, last.Phase, last.ReasonCode, last.DrainComplete)
			}
			for _, u := range updates {
				if u.Phase != diagnosisprep.ScopeCheck && u.Phase != diagnosisprep.Closure ||
					u.TaskID != "" || u.Started || u.ResultReady || u.AuthorityState != diagnosisprep.AuthorityNotIssued ||
					u.ModelUsage != nil && u.ModelUsage.Requests != 0 {
					t.Fatal("DNS 또는 DB 거부 뒤 task·모델·실행 권위가 진행됨")
				}
			}
			if tc.wantDatabase {
				if f.database.calls.Load() == 0 || f.model.calls.Load() != 0 || f.target.calls.Load() != 0 {
					t.Fatal("정상 DNS 대조가 소유 DB probe만 호출하지 않음")
				}
			} else {
				f.assertNoConnections()
			}
			entries, err := os.ReadDir(f.config.RunsDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("거부된 실행에서 회차 권위를 발급함")
			}
		})
	}
}

func TestURLPreparationExecuteRejectsChangedInspectionBinding(t *testing.T) {
	for _, change := range []string{"installation_digest", "delegation_ref"} {
		t.Run(change, func(t *testing.T) {
			f := newURLNegativeFixture(t)
			resolver, queries := urlNegativeDNS(t, "127.0.0.1")
			b := f.runtime()
			b.resolver = resolver
			inspection := f.inspect(b)
			switch change {
			case "installation_digest":
				f.config.Profiles[0].Label += " 변경"
				urlNegativeWrite(t, f.path, f.config)
				current := f.inspect(b)
				if current.InstallationDigest == inspection.InstallationDigest || current.DelegationRef != inspection.DelegationRef {
					t.Fatal("설치 digest만 바뀐 대조 구성 실패")
				}
			case "delegation_ref":
				oldRef := inspection.DelegationRef
				f.config.Profiles[0].DelegationRef = "replacement-delegation"
				urlNegativeWrite(t, f.path, f.config)
				inspection = f.inspect(b)
				// digest 검사가 대신 차단하지 않도록 현재 digest와 오래된 위임 참조를 결합한다.
				inspection.DelegationRef = oldRef
			}
			publishes := 0
			err := b.Execute(t.Context(), diagnosisprep.Execution{
				Principal: "ARTEX", Job: diagnosisprep.Job{ID: "owned-binding-gate", Input: f.input, Scope: inspection.Scope}, Inspection: inspection,
			}, func(diagnosisprep.Update) error { publishes++; return nil })
			if !errors.Is(err, errPreparation) || publishes != 0 || queries.Load() != 0 {
				t.Fatalf("변경된 결속으로 실행 준비 시작: err=%v publishes=%d queries=%d", err, publishes, queries.Load())
			}
			f.assertNoConnections()
			entries, err := os.ReadDir(f.config.RunsDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("결속 불일치에서 회차 권위를 발급함")
			}
		})
	}
}

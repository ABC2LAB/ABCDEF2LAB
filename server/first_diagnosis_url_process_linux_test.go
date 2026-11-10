//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

// 실제 설치 준비만 공통 fixture로 만든다. task·intent·회차 권위는 공개 Start 뒤 제품 코드가 만든다.
type urlProcessFixture struct {
	firstTargetNano                                                                     atomic.Int64
	t                                                                                   *testing.T
	ctx                                                                                 context.Context
	root, binary, installation, apiLaunch, socket, apiAddr, reportAddr, target, dataDir string
	installer                                                                           *db.DB
	allowed, denied, modelCalls, plannerCalls, goalCalls, workerCalls                   atomic.Int64
	plannerEntered                                                                      chan struct{}
	releasePlanner                                                                      chan struct{}
	releaseOnce                                                                         sync.Once
	multi                                                                               *urlMultiIntentModel
	external                                                                            bool
}

func urlTestWrite(t *testing.T, path string, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil || os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("소유 설치 fixture 저장 실패")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func urlTestAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	if l.Close() != nil {
		t.Fatal("fixture 포트 반환 실패")
	}
	return address
}

func newURLProcessFixture(t *testing.T) *urlProcessFixture {
	return newURLProcessFixtureModel(t, nil)
}

func newURLProcessFixtureModel(t *testing.T, multi *urlMultiIntentModel) *urlProcessFixture {
	return newURLProcessFixtureMode(t, multi, false)
}
func newURLProcessFixtureMode(t *testing.T, multi *urlMultiIntentModel, external bool) *urlProcessFixture {
	t.Helper()
	helper := firstDiagnosisNativeHelper(t)
	database := newControlledLocalDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	f := &urlProcessFixture{t: t, ctx: ctx, root: firstDiagnosisPrivateTemp(t), installer: &db.DB{DB: database.Installer}, plannerEntered: make(chan struct{}), releasePlanner: make(chan struct{})}
	f.multi, f.external = multi, external
	if multi != nil {
		t.Cleanup(multi.release)
		if f.installer.SetSetting(settingWorkers, "2") != nil {
			t.Fatal("동시 Worker 두 개 설치 실패")
		}
	}
	t.Cleanup(func() { f.releaseOnce.Do(func() { close(f.releasePlanner) }) })
	f.binary = filepath.Join(f.root, "first-controlled-diagnosis")
	// 초기 task가 없음을 먼저 확인한다. 기존 실행 fixture를 가져오지 않는다.
	var initial int
	if f.installer.QueryRow("SELECT count(*) FROM tasks").Scan(&initial) != nil || initial != 0 {
		t.Fatal("초기 task가 없는 전용 DB 필요")
	}
	pass, err := bcrypt.GenerateFromPassword([]byte("synthetic-owned-combined-fixture"), bcrypt.MinCost)
	if err != nil || f.installer.SetSetting(authPassKey, string(pass)) != nil {
		t.Fatal("시험 인증 설치 실패")
	}
	validFrom, validUntil := time.Now().Add(-time.Minute), time.Now().Add(8*time.Minute)
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "owned URL acceptance CA"}, NotBefore: validFrom, NotAfter: validUntil, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "owned URL target"}, NotBefore: validFrom, NotAfter: validUntil, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allowed" && r.Method == "GET" {
			f.firstTargetNano.CompareAndSwap(0, time.Now().UnixNano())
			f.allowed.Add(1)
		} else {
			f.denied.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"owned_fixture":true}`)
	}))
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key}}}
	target.StartTLS()
	t.Cleanup(target.Close)
	f.target = target.URL
	model := httptest.NewServer(http.HandlerFunc(f.model))
	t.Cleanup(model.Close)
	profile := &db.LLMProfile{Name: "owned-url-synthetic", Format: "openai", Model: "local:synthetic", BaseURL: model.URL + "/v1", APIKey: "owned-synthetic-key", Streaming: false, ContextWindowK: 128, MaxTokens: 2048}
	if external {
		profile.Model, profile.BaseURL, profile.ContextWindowK = llmpolicy.OpenRouterModel, llmpolicy.OpenRouterBaseURL, 256
	}
	profileID, err := f.installer.SaveProfile(profile)
	if err != nil || f.installer.SetActiveProfile(profileID) != nil {
		t.Fatal("합성 모델 설치 실패")
	}
	manifest := filepath.Join(f.root, "models.json")
	manifestHash := urlTestWrite(t, manifest, map[string]any{"version": 1, "profiles": []any{map[string]any{"profile_id": profileID, "format": "openai", "model": "local:synthetic", "base_url": profile.BaseURL, "addresses": []string{"127.0.0.1"}}}})
	if external {
		manifestHash = urlTestWrite(t, manifest, map[string]any{
			"version": 1, "profile_id": profileID, "format": "openai", "base_url": llmpolicy.OpenRouterBaseURL, "model": llmpolicy.OpenRouterModel,
			"provider_slug": "fixture/endpoint", "expected_provider_name": "Fixture", "approval_id": "url-owned-approval", "expires_at": validUntil,
			"strict_privacy_ack": true, "account_spend_cap_ack": true, "max_requests": 20, "max_input_tokens": 20 * 262144, "max_output_tokens": 20 * 2048, "max_cost_usd_micros": 6000000,
			"max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 2048, "timeout_seconds": 10, "max_concurrent": 1,
			"max_input_price_usd_micros_per_million": 1000000, "max_output_price_usd_micros_per_million": 1000000,
		})
	}
	cfg, _, policy := e2HostPolicyFixture(t, 9*time.Minute)
	// 이 파일은 기존 설치 템플릿일 뿐이다. 실제 ID 결속과 marker는 제품이 새 runs 디렉터리에 발급한다.
	policy.ValidFrom, policy.ValidUntil = validFrom, validUntil
	policy.OwnerRef = "ARTEX"
	policy.Helper = helper
	helperRaw, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	helperSum := sha256.Sum256(helperRaw)
	policy.HelperSHA256 = hex.EncodeToString(helperSum[:])
	policy.StateDir = firstDiagnosisPrivateTemp(t)
	policy.Rules = []E2HTTPFileRule{{ApprovalDisplay: RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}, Ref: "owned-url-rule", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/allowed", PathScopeRef: "owned-url-path", Methods: []string{"GET"}}}
	if multi != nil {
		// 두 송신 뒤 마지막 Planner의 기존 양수 잔여 예산 검사를 유지한다.
		policy.MaxRequests = 3
	}
	policy.Runtime.MaxWorkerTurns = 6
	policy.Runtime.MaxPlannerRounds = 4
	policy.Runtime.MaxToolCalls = 5
	policy.Runtime.WorkerTimeoutMS = 10000
	policy.Limits.MaxCalls = 5
	policy.Limits.CallTimeoutMS = 4000
	policy.Limits.RequestTimeoutMS = 2000
	policy.Proxy.RequestTimeoutMS = 2000
	policy.Limits.MaxDenials = 2
	upstream := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	upstreamSum := sha256.Sum256(upstream)
	if os.WriteFile(filepath.Join(cfg.AuthorityRoot, "upstream.pem"), upstream, 0600) != nil {
		t.Fatal("대상 private CA 설치 실패")
	}
	policy.UpstreamCA = &E2HTTPFilePin{File: "upstream.pem", SHA256: hex.EncodeToString(upstreamSum[:])}
	policyHash := urlTestWrite(t, cfg.PolicyFile, policy)
	keyDir, dataDir, skillDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	f.dataDir = dataDir
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		files, _ := filepath.Glob(filepath.Join(dataDir, "diagnostic-logs", "*", "*.jsonl"))
		for _, path := range files {
			raw, _ := os.ReadFile(path)
			for _, line := range bytes.Split(raw, []byte("\n")) {
				var e diagnosticlog.Event
				if json.Unmarshal(line, &e) == nil && (e.Status == "failed" || e.Status == "incomplete" || e.Status == "denied") {
					t.Logf("synthetic failure event=%s stage=%s reason=%s", e.Event, e.Stage, e.ReasonCode)
				}
			}
		}
	})
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("owned-url-jwt-material-at-least-32"), 0600) != nil {
		t.Fatal("시험 JWT 설치 실패")
	}
	dsn := urlRuntimeDSN(t, ctx, database.Runtime)
	// TCP 시험 DB도 실제 설치와 같이 제어 endpoint에 명시한다. 소켓 DB는 연결 IP가 없다.
	protectedEndpoints := []any{map[string]any{"host": "127.0.0.1", "port": model.Listener.Addr().(*net.TCPAddr).Port, "addresses": []string{"127.0.0.1"}}}
	databaseConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("소유 DB endpoint 파싱 실패")
	}
	if !strings.HasPrefix(databaseConfig.Host, "/") {
		address, err := netip.ParseAddr(databaseConfig.Host)
		if err != nil {
			t.Fatal("소유 DB는 Unix 소켓 또는 숫자 IP가 필요함")
		}
		protectedEndpoints = append(protectedEndpoints, map[string]any{"host": address.String(), "port": int(databaseConfig.Port), "addresses": []string{address.String()}})
	}
	dsnPath := filepath.Join(f.root, "runtime-dsn")
	if os.WriteFile(dsnPath, []byte(dsn), 0600) != nil {
		t.Fatal("보호 DSN 설치 실패")
	}
	f.apiAddr, f.reportAddr = urlTestAddress(t), urlTestAddress(t)
	launch := map[string]any{"format": "abc2lab.first-diagnosis-launch/v1", "mode": "general_http", "listen_addr": f.reportAddr, "data_dir": dataDir, "key_dir": keyDir, "skill_dir": skillDir, "local_llm_manifest_path": manifest, "model_profile_id": profileID, "model_format": "openai", "model_base_url": profile.BaseURL, "model_ref": "local:synthetic", "runtime_dsn_file": dsnPath, "shutdown_timeout_seconds": 10, "general_http": map[string]any{"policy_file": cfg.PolicyFile, "authority_file": cfg.AuthorityFile, "authority_root": cfg.AuthorityRoot}}
	if external {
		launch["local_llm_manifest_path"], launch["model_execution_mode"], launch["openrouter_manifest_path"], launch["model_ref"] = "", "openrouter", manifest, llmpolicy.OpenRouterModel
	}
	template := filepath.Join(f.root, "template.json")
	launchHash := urlTestWrite(t, template, launch)
	launch["listen_addr"] = f.apiAddr
	f.apiLaunch = filepath.Join(f.root, "api-launch.json")
	apiLaunchHash := urlTestWrite(t, f.apiLaunch, launch)
	f.socket = filepath.Join(f.root, "prepare.sock")
	f.installation = filepath.Join(f.root, "installation.json")
	budget := diagnosisprep.Budget{Requests: int64(policy.MaxRequests), ModelRequests: 20, InputTokens: 20 * 131072, OutputTokens: 20 * 2048, DurationSeconds: 120}
	if external {
		budget.InputTokens, budget.CostUSDMicros = 20*262144, 6000000
	}
	inputPerCall := 131072
	if external {
		inputPerCall = 262144
	}
	urlTestWrite(t, f.installation, map[string]any{
		"format": "abc2lab.url-preparation-installation/v1", "operator_uid": os.Geteuid(), "state_dir": firstDiagnosisPrivateTemp(t), "runs_dir": firstDiagnosisPrivateTemp(t), "socket_path": f.socket, "report_listen_addr": f.reportAddr, "api_launch": map[string]any{"file": f.apiLaunch, "sha256": apiLaunchHash},
		"protected_endpoints": protectedEndpoints,
		"profiles": []any{map[string]any{
			"id": "owned-url", "label": "소유 HTTPS 합성 진단", "principal": "ARTEX", "target_ref": "owned-url-target", "scope_ref": "owned-readonly", "model_profile_ref": "local-synthetic", "model_label": "로컬 합성 모델",
			"delegation_ref": "owned-url-delegation", "derive_runs": true, "valid_from": validFrom.Add(time.Second), "valid_until": validUntil.Add(-time.Second), "max_runs": 1, "aggregate_budget": budget, "budget": budget,
			"launch": map[string]any{"file": template, "sha256": launchHash}, "policy_sha256": policyHash, "manifest_sha256": manifestHash, "allow_private_target": true,
			"attestation":      map[string]any{"ref": "owned-nonmutating-range", "verified": true, "path_prefixes": []string{"/allowed"}, "methods": []string{"GET"}, "query_policy": "any", "body_policy": "bounded", "max_body_bytes": policy.Limits.MaxBodyBytes},
			"model_limits_ref": "owned-model-limits", "input_tokens_per_call": inputPerCall, "output_tokens_per_call": 2048,
		}},
	})
	build := exec.CommandContext(ctx, "go", "build", "-p", "1", "-buildvcs=false", "-o", f.binary, "../cmd/first-controlled-diagnosis")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOMEMLIMIT=2GiB", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	if external {
		source, err := filepath.Abs("../llmpolicy/openrouter_transport.go")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		needle := "resp, err := t.base.RoundTrip(copy)"
		if strings.Count(string(raw), needle) != 1 {
			t.Fatal("test overlay boundary changed")
		}
		// Guard/SDK/provider/usage logic is untouched. Only the already-authorized
		// transport's physical destination is redirected in this test-built binary.
		replacement := `cloneURL := *copy.URL
 cloneURL.Scheme, cloneURL.Host, cloneURL.Path = "http", ` + strconv.Quote(model.Listener.Addr().String()) + `, "/v1/chat/completions"
 copy.URL = &cloneURL
 resp, err := t.base.RoundTrip(copy)`
		dial := `DialContext: func(ctx context.Context, network,address string)(net.Conn,error){if address != ` + strconv.Quote(model.Listener.Addr().String()) + ` {return nil, ErrOpenRouterPolicy};return (&net.Dialer{}).DialContext(ctx,"tcp",address)},`
		raw = []byte(strings.Replace(string(raw), "DialContext: openRouterDial,", dial, 1))
		overlaySource := filepath.Join(f.root, "openrouter_transport.go")
		if os.WriteFile(overlaySource, []byte(strings.Replace(string(raw), needle, replacement, 1)), 0600) != nil {
			t.Fatal("test overlay write")
		}
		overlay := filepath.Join(f.root, "overlay.json")
		urlTestWrite(t, overlay, map[string]any{"Replace": map[string]string{source: overlaySource}})
		build.Args = append(build.Args[:2], append([]string{"-overlay", overlay}, build.Args[2:]...)...)
	}
	if build.Run() != nil {
		t.Fatal("URL 준비 서비스 소스 빌드 실패")
	}
	return f
}

func urlRuntimeDSN(t *testing.T, ctx context.Context, pool *sql.DB) string {
	t.Helper()
	connection, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("runtime 연결 실패")
	}
	defer connection.Close()
	var dsn string
	err = connection.Raw(func(raw any) error {
		conn, ok := raw.(*stdlib.Conn)
		if !ok {
			return errors.New("fixture driver")
		}
		c := conn.Conn().Config()
		quote := func(v string) string {
			return "'" + strings.ReplaceAll(strings.ReplaceAll(v, string(rune(92)), strings.Repeat(string(rune(92)), 2)), "'", string(rune(92))+"'") + "'"
		}
		dsn = "host=" + quote(c.Host) + " port=" + strconv.Itoa(int(c.Port)) + " user=" + quote(c.User) + " password=" + quote(c.Password) + " dbname=" + quote(c.Database) + " sslmode=disable"
		for k, v := range c.RuntimeParams {
			dsn += " " + k + "=" + quote(v)
		}
		return nil
	})
	if err != nil {
		t.Fatal("runtime DSN 추출 실패")
	}
	return dsn
}

type urlSyntheticMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
}

func (f *urlProcessFixture) model(w http.ResponseWriter, r *http.Request) {
	f.modelCalls.Add(1)
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		http.Error(w, "unsupported", 400)
		return
	}
	var input struct {
		Stream    bool   `json:"stream"`
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Provider  struct {
			Only           []string `json:"only"`
			AllowFallbacks bool     `json:"allow_fallbacks"`
			ZDR            bool     `json:"zdr"`
			DataCollection string   `json:"data_collection"`
		} `json:"provider"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []urlSyntheticMessage `json:"messages"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&input) != nil || input.Stream {
		http.Error(w, "bad synthetic request", 400)
		return
	}
	if f.external && (input.Model != llmpolicy.OpenRouterModel || input.MaxTokens != 2048 || len(input.Provider.Only) != 1 || input.Provider.Only[0] != "fixture/endpoint" || input.Provider.AllowFallbacks || !input.Provider.ZDR || input.Provider.DataCollection != "deny" || r.Header.Get("Authorization") != "Bearer owned-synthetic-key") {
		f.t.Error("OpenRouter SDK/provider pin mismatch")
		w.WriteHeader(400)
		return
	}
	names := map[string]bool{}
	for _, tool := range input.Tools {
		names[tool.Function.Name] = true
	}
	message := map[string]any{"role": "assistant", "content": "The owned synthetic scope has no additional actions."}
	finish := "stop"
	tool := func(name, id string, args any) {
		raw, _ := json.Marshal(args)
		message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
		finish = "tool_calls"
	}
	switch {
	case names["set_goals"] && !names["add_intent"] && !names["Bash"]:
		if f.goalCalls.Add(1) == 1 {
			tool("set_goals", "url-goal", map[string]any{"goals": []any{map[string]any{"text": "소유 HTTPS 응답을 관찰하고 수동 검토 후보를 저장합니다.", "vulnclass": "access_control"}}})
		}
	case names["add_intent"] && !names["Bash"]:
		if f.plannerCalls.Add(1) == 1 {
			close(f.plannerEntered)
			select {
			case <-f.releasePlanner:
			case <-r.Context().Done():
				return
			}
			intents := []any{map[string]any{"summary": "Inspect " + f.target + "/allowed through the enforced Bash boundary and preserve a manual-review candidate.", "priority": 5}}
			if f.multi != nil {
				intents = append(intents, map[string]any{"summary": "Independently repeat the owned read-only observation at " + f.target + "/allowed and preserve a separate manual-review candidate for the second intent.", "priority": 4})
			}
			tool("add_intent", "url-planner-intent", map[string]any{"intents": intents})
		}
	case names["Bash"]:
		call := f.workerCalls.Add(1)
		if f.multi != nil {
			if !f.multi.respond(f, r.Context(), call, input.Messages, tool) {
				return
			}
			break
		}
		switch call {
		case 1:
			tool("Bash", "url-allowed", map[string]any{"command": "curl --silent --show-error --max-time 2 " + f.target + "/allowed", "timeout_ms": 2500})
		case 3:
			tool("Bash", "url-denied", map[string]any{"command": "curl --silent --show-error --max-time 2 " + f.target + "/denied", "timeout_ms": 2500})
		case 2:
			ref := ""
			for _, m := range input.Messages {
				if m.Role != "tool" {
					continue
				}
				var content string
				if json.Unmarshal(m.Content, &content) != nil {
					continue
				}
				var result e2runner.Result
				if json.Unmarshal([]byte(content), &result) == nil && result.Trusted.Version == e2proxy.Version && result.Trusted.Call.CallRef != "" {
					ref = result.Trusted.Call.CallRef
					break
				}
			}
			if ref == "" {
				f.t.Error("허용 실제 관찰 call_ref 누락")
			} else {
				tool("submit_diagnostic_candidate", "url-candidate", map[string]any{"call_ref": ref, "vulnclass": "access_control", "name": "URL 흐름 검토 후보", "summary": "소유 HTTPS 실제 관찰의 수동 검토 후보입니다.", "remediation": "실제 접근 권한을 검토하세요."})
			}
		}
	default:
		f.t.Error("합성 모델에 알 수 없는 역할 도착")
	}
	w.Header().Set("Content-Type", "application/json")
	modelName := "local:synthetic"
	if f.external {
		modelName = llmpolicy.OpenRouterModel
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "owned-url-model", "object": "chat.completion", "created": time.Now().Unix(), "model": modelName, "provider": "Fixture", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20, "cost": 0.000001}})
}

type urlOwnedProcess struct {
	command *exec.Cmd
	once    sync.Once
}

func (f *urlProcessFixture) process(args ...string) *urlOwnedProcess {
	f.t.Helper()
	c := exec.CommandContext(f.ctx, f.binary, args...)
	c.Stdout, c.Stderr = io.Discard, io.Discard
	c.Env = append(os.Environ(), "GOMAXPROCS=2", "GOMEMLIMIT=2GiB")
	if c.Start() != nil {
		f.t.Fatal("URL 독립 프로세스 시작 실패")
	}
	p := &urlOwnedProcess{command: c}
	f.t.Cleanup(p.stop)
	return p
}
func (p *urlOwnedProcess) stop() {
	p.once.Do(func() { _ = p.command.Process.Signal(syscall.SIGTERM); _ = p.command.Wait() })
}
func (f *urlProcessFixture) startAPI() *urlOwnedProcess {
	p := f.process("serve-url-api", "-installation", f.installation, "-config", f.apiLaunch, "-socket", f.socket, "-runtime-api-origin", "http://"+f.reportAddr)
	waitFirstDiagnosisProcessHTTP(f.t, f.ctx, "http://"+f.apiAddr)
	return p
}

type urlAPIClient struct {
	t             *testing.T
	ctx           context.Context
	origin, token string
}

func (f *urlProcessFixture) api() *urlAPIClient {
	c := &urlAPIClient{t: f.t, ctx: f.ctx, origin: "http://" + f.apiAddr}
	var login struct {
		Token string `json:"token"`
	}
	c.call("POST", "/api/auth/login", "", map[string]string{"username": "ARTEX", "password": "synthetic-owned-combined-fixture"}, &login)
	if login.Token == "" {
		f.t.Fatal("실제 API 로그인 실패")
	}
	c.token = login.Token
	return c
}
func (c *urlAPIClient) call(method, path, key string, body any, out any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(c.ctx, method, c.origin+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if err != nil {
		c.t.Fatal("실제 URL API 연결 실패")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.t.Fatalf("URL API %s %s status=%d", method, path, response.StatusCode)
	}
	if html, ok := out.(*[]byte); ok {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		if readErr != nil || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") || !bytes.Contains(body, []byte("<html")) {
			c.t.Fatal("실제 저장 HTML 보고서 확인 실패")
		}
		*html = body
		return
	}
	if out != nil && json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(out) != nil {
		c.t.Fatal("URL API 응답 해석 실패")
	}
}
func (c *urlAPIClient) job(method, path, key string, body any) diagnosisprep.Job {
	var response struct {
		Job diagnosisprep.Job `json:"draft"`
	}
	c.call(method, path, key, body, &response)
	if response.Job.ID == "" {
		c.t.Fatal("URL API 초안 결속 누락")
	}
	return response.Job
}

func urlStartBody(revision uint64, external bool) map[string]any {
	body := map[string]any{"version": diagnosisapi.URLVersion, "expected_revision": revision}
	if external {
		body["external_transfer_confirmed"] = true
	}
	return body
}

type urlStartReceipt struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Key      string `json:"key"`
}

func runURLProcessAcceptance(t *testing.T, browser bool) {
	runURLProcessAcceptanceMode(t, browser, false)
}
func runURLProcessAcceptanceMode(t *testing.T, browser, external bool) {
	if os.Getenv("ARTEX_URL_DIAGNOSIS_TEST") != "1" {
		t.Skip("NOT_RUN: 소유 로컬 URL 프로세스 수용 시험 opt-in 필요")
	}
	if browser && (os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE") == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT") == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT") == "") {
		t.Skip("NOT_RUN: 설치된 브라우저·Node와 전용 출력 경로 필요")
	}
	f := newURLProcessFixtureMode(t, nil, external)
	service := f.startPreparation()
	api := f.startAPI()
	client := f.api()
	var receipt urlStartReceipt
	var ui *firstDiagnosisBrowserFixture
	if browser {
		ui = startFirstDiagnosisBrowserFixture(t, f.ctx, "http://"+f.apiAddr, f.target+"/allowed")
		_, err := ui.result("url-draft")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		var catalog struct {
			Profiles []diagnosisprep.Profile `json:"profiles"`
		}
		client.call("GET", diagnosisapi.URLBasePath+"/catalog", "", nil, &catalog)
		if len(catalog.Profiles) != 1 {
			t.Fatal("설치 catalog 불일치")
		}
		job := client.job("POST", diagnosisapi.URLBasePath+"/drafts", "owned-create-001", map[string]any{"version": diagnosisapi.URLVersion, "input": diagnosisprep.Input{TargetURL: f.target + "/allowed", ProfileID: "owned-url", DisplayName: "URL 프로세스 합성 수용"}})
		recovered := client.job("GET", diagnosisapi.URLBasePath+"/drafts", "owned-create-001", nil)
		if recovered.ID != job.ID {
			t.Fatal("create receipt 복원 불일치")
		}
		job = client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/validate", "", map[string]any{"version": diagnosisapi.URLVersion, "expected_revision": job.Revision})
		if job.State != diagnosisprep.Ready || job.Scope == nil {
			var initialTasks int
			if f.installer.QueryRow("SELECT count(*) FROM tasks").Scan(&initialTasks) != nil {
				t.Fatal("검증 실패 집계 조회 불가")
			}
			t.Fatalf("설치 승인 범위 미확인 state=%s reason=%s tasks=%d model=%d allowed=%d denied=%d", job.State, job.ReasonCode, initialTasks, f.modelCalls.Load(), f.allowed.Load(), f.denied.Load())
		}
		receipt = urlStartReceipt{ID: job.ID, Revision: job.Revision, Key: "owned-start-001"}
	}
	var taskCount int
	if f.installer.QueryRow("SELECT count(*) FROM tasks").Scan(&taskCount) != nil || taskCount != 0 || f.modelCalls.Load() != 0 || f.allowed.Load() != 0 || f.denied.Load() != 0 {
		t.Fatal("명시적 시작 전 task·모델·대상 부작용 발생")
	}
	startedAt := time.Now()
	if browser {
		raw, err := ui.result("url-lost-start")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			StartReceipt urlStartReceipt `json:"startReceipt"`
		}
		if json.Unmarshal(raw, &result) != nil {
			t.Fatal("브라우저 start receipt 누락")
		}
		receipt = result.StartReceipt
	} else {
		client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+receipt.ID+"/start", receipt.Key, urlStartBody(receipt.Revision, external))
	}
	if receipt.ID == "" || receipt.Key == "" || receipt.Revision < 1 {
		t.Fatal("실제 start 요청 결속 누락")
	}
	select {
	case <-f.plannerEntered:
	case <-time.After(25 * time.Second):
		status := client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+receipt.ID+"/status", "", nil)
		t.Fatalf("실제 Planner 모델 준비 도달 실패 state=%s phase=%s reason=%s model=%d goals=%d planner=%d worker=%d", status.State, status.Phase, status.ReasonCode, f.modelCalls.Load(), f.goalCalls.Load(), f.plannerCalls.Load(), f.workerCalls.Load())
	}
	// 공개 API만 교체한다. Unix IPC 소유 준비 프로세스의 실제 Planner는 계속 살아 있어야 한다.
	api.stop()
	api = f.startAPI()
	_ = api
	recovered := client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+receipt.ID+"/status", "", nil)
	if recovered.TaskID == "" || !recovered.PreparationStarted || recovered.Started {
		t.Fatal("API 재시작 후 실제 준비 상태 복원 실패")
	}
	duplicate := client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+receipt.ID+"/start", receipt.Key, urlStartBody(receipt.Revision, external))
	if duplicate.ID != recovered.ID || duplicate.TaskID != recovered.TaskID {
		t.Fatal("같은 start key가 다른 실행 생성")
	}
	if browser {
		ui.run("url-recover")
	}
	f.releaseOnce.Do(func() { close(f.releasePlanner) })
	var final diagnosisprep.Job
	for deadline := time.Now().Add(75 * time.Second); ; {
		final = client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+receipt.ID+"/status", "", nil)
		if final.State == diagnosisprep.Completed || final.State == diagnosisprep.Cancelled || final.State == diagnosisprep.Failed || final.State == diagnosisprep.Unknown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("URL 실행 실제 마감 시간 초과")
		}
		time.Sleep(100 * time.Millisecond)
	}
	finishedAt := time.Now()
	if final.State != diagnosisprep.Completed || !final.Started || !final.DrainComplete || !final.ResultReady || final.AuthorityState != diagnosisprep.AuthorityConsumed {
		t.Fatalf("URL 마감 미확인 state=%s phase=%s reason=%s started=%v drain=%v result=%v allowed=%d denied=%d model=%d goal=%d planner=%d worker=%d", final.State, final.Phase, final.ReasonCode, final.Started, final.DrainComplete, final.ResultReady, f.allowed.Load(), f.denied.Load(), f.modelCalls.Load(), f.goalCalls.Load(), f.plannerCalls.Load(), f.workerCalls.Load())
	}
	var intents, plannerIntents, candidates, confirmed, runs, blockedIntents int
	for _, q := range []struct {
		query string
		value *int
	}{
		{"SELECT count(*) FROM tasks", &taskCount},
		{"SELECT count(*) FROM exploration_nodes WHERE kind='intent'", &intents},
		{"SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND origin='planner'", &plannerIntents},
		{"SELECT count(*) FROM exploration_nodes WHERE origin='e2_diagnostic_candidate_v1'", &candidates},
		{"SELECT count(*) FROM findings", &confirmed},
		{"SELECT count(*) FROM exploration_nodes WHERE kind='intent' AND state='blocked' AND blocked_reason='e2_http_policy_limited'", &blockedIntents},
		{"SELECT count(*) FROM exploration_nodes WHERE origin='" + diagnosisRunOrigin + "'", &runs},
	} {
		if f.installer.QueryRow(q.query).Scan(q.value) != nil {
			t.Fatal("수용 집계 조회 실패")
		}
	}
	if taskCount != 1 || intents != 1 || plannerIntents != 1 || candidates != 1 || confirmed != 0 || runs != 1 || blockedIntents != 1 || f.allowed.Load() != 1 || f.denied.Load() != 0 {
		t.Fatalf("task=%d intent=%d planner_intent=%d candidate=%d confirmed=%d run=%d policy_blocked_intents=%d allowed=%d denied=%d", taskCount, intents, plannerIntents, candidates, confirmed, runs, blockedIntents, f.allowed.Load(), f.denied.Load())
	}
	if final.ModelUsage == nil || !final.ModelUsage.UsageKnown || final.ModelUsage.Requests != f.modelCalls.Load() || final.ModelUsage.CostUSDMicros != func() int64 {
		if external {
			return f.modelCalls.Load()
		}
		return 0
	}() {
		t.Fatal("실제 모델 요청·사용량 집계 불일치")
	}
	if external {
		var record diagnosisRunRecord
		client.call("GET", "/api/tasks/"+final.TaskID+"/diagnosis-run", "", nil, &record)
		if record.Model == nil || len(record.ModelPreparation) != 1 {
			t.Fatal("saved stage usage missing")
		}
		prep := record.ModelPreparation[0].Usage
		if prep.Requests != 4 || record.Model.Requests != f.modelCalls.Load()-4 || prep.CostMicros+record.Model.CostMicros != final.ModelUsage.CostUSDMicros || final.ModelUsage.ReservedCostUSDMicros != f.modelCalls.Load()*264192 {
			t.Fatal("preparation double-counted in execution usage")
		}
	}
	if browser {
		ui.run("url-report")
	}
	var reportBefore, reportAfter []byte
	reportPath := "/api/report/diagnostic?task=" + final.TaskID
	client.call("GET", reportPath, "", nil, &reportBefore)
	callsBefore, allowedBefore, deniedBefore := f.modelCalls.Load(), f.allowed.Load(), f.denied.Load()
	// 마감 후 준비 프로세스만 재시작한다. 저장 결과 조회는 실행을 복원하거나 새로 승인하지 않는다.
	service.stop()
	service = f.startPreparation()
	restored := client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+receipt.ID+"/status", "", nil)
	beforeFacts, beforeErr := json.Marshal(final)
	afterFacts, afterErr := json.Marshal(restored)
	if beforeErr != nil || afterErr != nil || len(final.SourceDigest) != 64 || !bytes.Equal(beforeFacts, afterFacts) {
		t.Fatal("준비 서비스 재시작 뒤 저장 성공 사실·수정 번호·source digest 변경")
	}
	client.call("GET", reportPath, "", nil, &reportAfter)
	if !bytes.Equal(urlStoredReportComparable(t, reportBefore, final.TaskID), urlStoredReportComparable(t, reportAfter, final.TaskID)) {
		t.Fatal("준비 서비스 재시작 뒤 저장 보고서 내용 변경")
	}
	time.Sleep(200 * time.Millisecond)
	if f.modelCalls.Load() != callsBefore || f.allowed.Load() != allowedBefore || f.denied.Load() != deniedBefore {
		t.Fatal("저장 보고서 복원에서 모델·대상 추가 호출 발생")
	}
	t.Log("URL_STORED_REPORT_RESTART preparation_restart=1 source_digest_preserved=true job_facts_preserved=true report_content_preserved=true additional_model_calls=0 additional_target_sends=0")
	api.stop()
	service.stop()
	f.verifySummary(final, startedAt, finishedAt)
	t.Logf("URL_ACCEPTANCE browser=%v tasks=%d planner_intents=%d candidates=%d confirmed=%d allowed_sends=%d denied_sends=%d runs=%d policy_blocked_intents=%d model_calls=%d goal_calls=%d planner_calls=%d worker_calls=%d api_restart=1 same_key_replay=1 drain=true result=true", browser, taskCount, plannerIntents, candidates, confirmed, f.allowed.Load(), f.denied.Load(), runs, blockedIntents, f.modelCalls.Load(), f.goalCalls.Load(), f.plannerCalls.Load(), f.workerCalls.Load())
}

// 조회 때마다 바뀌는 내보내기 생성 시각 한 곳만 제외하고 저장 보고서 전체를 비교한다.
func urlStoredReportComparable(t *testing.T, body []byte, taskID string) []byte {
	t.Helper()
	marker := []byte("<p>작업 #" + taskID + " · 전체 저장 회차 · 생성 ")
	if bytes.Count(body, marker) != 1 {
		t.Fatal("저장 보고서 작업 결속·생성 시각 표시 누락")
	}
	before, remainder, _ := bytes.Cut(body, marker)
	generated, after, found := bytes.Cut(remainder, []byte("</p>"))
	if !found || len(generated) == 0 || bytes.ContainsAny(generated, "<>") {
		t.Fatal("저장 보고서 생성 시각 경계 불일치")
	}
	result := append([]byte{}, before...)
	result = append(result, marker...)
	result = append(result, []byte("[export-time]</p>")...)
	return append(result, after...)
}

func TestFirstDiagnosisURLOpenRouterIndependentProcessesOwnedPostgres(t *testing.T) {
	runURLProcessAcceptanceMode(t, false, true)
}

func TestFirstDiagnosisURLIndependentProcessesOwnedPostgres(t *testing.T) {
	runURLProcessAcceptance(t, false)
}

// 기존 JSONL을 코드 안에서 집계한다. 로그 원문·토큰값·DB payload는 출력하지 않는다.
func (f *urlProcessFixture) verifySummary(job diagnosisprep.Job, started, finished time.Time) {
	t := f.t
	t.Helper()
	files, err := filepath.Glob(filepath.Join(f.dataDir, "diagnostic-logs", "*", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatal("기존 진단 JSONL 관측 파일 누락")
	}
	type summary struct {
		ModelRequests   int64          `json:"model_requests"`
		ReservedInput   int64          `json:"reserved_input_tokens"`
		ReservedOutput  int64          `json:"reserved_output_tokens"`
		CostUSDMicros   int64          `json:"cost_usd_micros"`
		FirstModelMS    int64          `json:"preparation_to_first_model_ms"`
		RuntimeTotalMS  int64          `json:"runtime_total_ms_including_test_wait"`
		Files           int            `json:"files"`
		Events          map[string]int `json:"event_counts"`
		ModelRoles      map[string]int `json:"model_role_counts"`
		TaskBoundEvents int            `json:"task_bound_events"`
		DistinctRuns    int            `json:"distinct_runs"`
		DistinctCalls   int            `json:"distinct_calls"`
		PreparationMS   int64          `json:"preparation_to_gateway_ms_including_test_wait"`
		FirstTargetMS   int64          `json:"start_to_first_target_ms_including_test_wait"`
		TotalMS         int64          `json:"start_to_stored_result_ms_including_test_wait"`
		ModelInput      int64          `json:"model_input_tokens"`
		ModelOutput     int64          `json:"model_output_tokens"`
		ActivityRows    int            `json:"recorded_activity_rows"`
		PlannerRounds   int            `json:"recorded_planner_rounds"`
		ResultRows      int            `json:"recorded_result_rows"`
		ResultInput     int64          `json:"recorded_result_input_tokens"`
		ResultOutput    int64          `json:"recorded_result_output_tokens"`
	}
	out := summary{ModelRequests: job.ModelUsage.Requests, ReservedInput: job.ModelUsage.ReservedInputTokens, ReservedOutput: job.ModelUsage.ReservedOutputTokens, CostUSDMicros: job.ModelUsage.CostUSDMicros, Files: len(files), Events: map[string]int{}, ModelRoles: map[string]int{}, ModelInput: job.ModelUsage.InputTokens, ModelOutput: job.ModelUsage.OutputTokens}
	runs, calls := map[string]bool{}, map[string]bool{}
	task := mustFirstDiagnosisTaskNumber(job.TaskID)
	var gateway time.Time
	var supervisorSummaries, cliSummaries int
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > diagnosticlog.MaxBytes {
			t.Fatal("진단 관측 파일 보호·한도 불일치")
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal("진단 집계 파일 열기 실패")
		}
		decoder := json.NewDecoder(io.LimitReader(file, diagnosticlog.MaxBytes+1))
		decoder.DisallowUnknownFields()
		var previous uint64
		for {
			var event diagnosticlog.Event
			err = decoder.Decode(&event)
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = file.Close()
				t.Fatal("진단 JSONL 집계 해석 실패")
			}
			if event.Version != 1 || event.Sequence <= previous || event.TaskID != 0 && event.TaskID != task {
				_ = file.Close()
				t.Fatal("진단 이벤트 task·순서 불일치")
			}
			previous = event.Sequence
			if event.Component == "openrouter" {
				if event.Event == "model_request_started" {
					event.Event = "model_call_started"
				}
				if event.Event == "model_request_finished" {
					event.Event = "model_call_finished"
				}
			}
			out.Events[event.Event]++
			if event.TaskID == task {
				out.TaskBoundEvents++
			}
			if event.RunRef != "" {
				runs[event.RunRef] = true
			}
			if event.CallRef != "" {
				if event.TaskID != task || event.IntentID < 1 {
					_ = file.Close()
					t.Fatal("실제 E2 호출의 task·intent 결속 누락")
				}
				calls[event.CallRef] = true
			}
			if event.Event == "model_call_started" {
				out.ModelRoles[event.Role]++
			}
			if event.Event == "model_call_finished" && event.Status != "succeeded" {
				_ = file.Close()
				t.Fatal("실제 모델 호출 마감 관측 불일치")
			}
			if event.Event == "run_started" && (gateway.IsZero() || event.Timestamp.Before(gateway)) {
				gateway = event.Timestamp
			}
			if event.Component == "cli" && event.Event == "model_request_started" {
				out.FirstModelMS = event.DurationMS
			}
			if event.Component == "cli" && event.Event == "stage_finished" && event.Stage == "admission" {
				out.PreparationMS = event.DurationMS
			}
			if event.Event == "run_summary" && event.Component == "cli" {
				out.RuntimeTotalMS = event.DurationMS
				cliSummaries++
				if event.Status != "succeeded" || event.ExecutionClosed == nil || !*event.ExecutionClosed || event.ResultStored == nil || !*event.ResultStored || event.Complete == nil || !*event.Complete || event.Count != int(f.modelCalls.Load()) || event.InputTokens != job.ModelUsage.InputTokens || event.OutputTokens != job.ModelUsage.OutputTokens || event.ReservedInputTokens != job.ModelUsage.ReservedInputTokens || event.ReservedOutputTokens != job.ModelUsage.ReservedOutputTokens || event.CostMicros != job.ModelUsage.CostUSDMicros {
					_ = file.Close()
					t.Fatal("준비·실행 통합 모델 사용량 로그 불일치")
				}
			}
			if event.Event == "run_summary" && event.Component == "supervisor" {
				supervisorSummaries++
				if event.TargetRequests != 1 || event.Approved != 1 || event.Denied != 1 || event.Candidates != 1 || event.Status != "succeeded" ||
					event.ResultStored == nil || !*event.ResultStored || event.AuditComplete == nil || !*event.AuditComplete ||
					event.ExecutionClosed == nil || !*event.ExecutionClosed || event.ObservationComplete == nil || !*event.ObservationComplete {
					_ = file.Close()
					t.Fatal("기존 JSONL 집계와 실제 송신·후보·마감 불일치")
				}
			}
		}
		if file.Close() != nil {
			t.Fatal("진단 집계 파일 마감 실패")
		}
	}
	out.DistinctRuns, out.DistinctCalls = len(runs), len(calls)
	if out.Events["model_call_started"] != int(f.modelCalls.Load()) || out.Events["model_call_finished"] != int(f.modelCalls.Load()) ||
		out.ModelRoles["goals"] != int(f.goalCalls.Load()) || out.ModelRoles["planner"] != int(f.plannerCalls.Load()) || out.ModelRoles["worker"] != int(f.workerCalls.Load()) ||
		out.ModelRoles["unknown"] != 0 || out.TaskBoundEvents < 1 || len(runs) != 1 || len(calls) != 2 ||
		out.Events["planner_finished"] < 1 || out.Events["worker_finished"] != 1 || out.Events["candidate_saved"] != 1 ||
		supervisorSummaries != 1 || cliSummaries != 1 || out.Events["run_finished"] != 1 || out.Events["result_save_finished"] != 1 || out.Events["diagnosis_finished"] < 1 {
		t.Fatalf("기존 진단 관측 집계 누락: events=%v roles=%v runs=%d calls=%d", out.Events, out.ModelRoles, len(runs), len(calls))
	}
	if gateway.IsZero() || gateway.Before(started) || f.firstTargetNano.Load() == 0 {
		t.Fatal("준비·첫 요청 실제 시각 관측 누락")
	}
	if out.PreparationMS < 0 || out.RuntimeTotalMS < out.PreparationMS {
		t.Fatal("기존 준비·전체 시간 계측 불일치")
	}
	out.FirstTargetMS = time.Unix(0, f.firstTargetNano.Load()).Sub(started).Milliseconds()
	out.TotalMS = finished.Sub(started).Milliseconds()
	// engine-metrics의 result/round 정의를 같은 소유 schema의 실제 task에 적용한다.
	// 그 CLI는 public 전용이므로 여기서는 고정 집계 SQL만 쓰며 payload는 읽지 않는다.
	err = f.installer.QueryRowContext(f.ctx, `SELECT count(*),
 count(*) FILTER(WHERE worker='planner' AND kind='round'),
 count(*) FILTER(WHERE kind='result'),
 COALESCE(sum(input_tokens) FILTER(WHERE kind='result'),0),
 COALESCE(sum(output_tokens) FILTER(WHERE kind='result'),0)
 FROM activity WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1)`, task).Scan(&out.ActivityRows, &out.PlannerRounds, &out.ResultRows, &out.ResultInput, &out.ResultOutput)
	if err != nil || out.ActivityRows < 1 || out.ResultRows < 1 {
		t.Fatal("기존 엔진 집계 조회 실패")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal("안전한 수용 요약 생성 실패")
	}
	t.Logf("URL_DIAGNOSTIC_SUMMARY %s", raw)
}

func (f *urlProcessFixture) startPreparation() *urlOwnedProcess {
	f.t.Helper()
	service := f.process("prepare-service", "-installation", f.installation)
	for deadline := time.Now().Add(10 * time.Second); ; {
		if info, err := os.Stat(f.socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			waitFirstDiagnosisProcessHTTP(f.t, f.ctx, "http://"+f.reportAddr)
			return service
		}
		if time.Now().After(deadline) {
			f.t.Fatal("비공개 준비 서비스 소켓 기동 실패")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 실제 모델 응답 대기 중 중지를 시험한다. 실행 전 취소를 취약점 없음이나 모델 사용량 확인으로 바꾸지 않는다.
func TestFirstDiagnosisURLStopDuringPreparationOwnedPostgres(t *testing.T) {
	if os.Getenv("ARTEX_URL_DIAGNOSIS_TEST") != "1" {
		t.Skip("NOT_RUN: 소유 로컬 URL 프로세스 수용 시험 opt-in 필요")
	}
	f := newURLProcessFixture(t)
	f.startPreparation()
	f.startAPI()
	client := f.api()
	job := client.job("POST", diagnosisapi.URLBasePath+"/drafts", "owned-stop-create", map[string]any{"version": diagnosisapi.URLVersion, "input": diagnosisprep.Input{TargetURL: f.target + "/allowed", ProfileID: "owned-url", DisplayName: "URL 모델 준비 중지 수용"}})
	job = client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/validate", "", map[string]any{"version": diagnosisapi.URLVersion, "expected_revision": job.Revision})
	if job.State != diagnosisprep.Ready {
		t.Fatalf("중지 fixture 승인 실패 reason=%s", job.ReasonCode)
	}
	if f.modelCalls.Load() != 0 || f.allowed.Load() != 0 || f.denied.Load() != 0 {
		t.Fatal("중지 fixture 시작 전 부작용")
	}
	job = client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/start", "owned-stop-start", map[string]any{"version": diagnosisapi.URLVersion, "expected_revision": job.Revision})
	select {
	case <-f.plannerEntered:
	case <-time.After(25 * time.Second):
		t.Fatal("실제 중지 대상 Planner HTTP 호출 미도달")
	}
	before := client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/status", "", nil)
	calls := f.modelCalls.Load()
	if calls != 3 || before.TaskID == "" || before.Started || before.AuthorityState != diagnosisprep.AuthorityNotIssued {
		t.Fatal("실제 준비 중지 시점 불일치")
	}
	client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/stop", "", map[string]any{"version": diagnosisapi.URLVersion, "expected_revision": before.Revision})
	var after diagnosisprep.Job
	for deadline := time.Now().Add(20 * time.Second); ; {
		after = client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/status", "", nil)
		if after.State == diagnosisprep.Unknown || after.State == diagnosisprep.Cancelled || after.State == diagnosisprep.Failed || after.State == diagnosisprep.Completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("실제 준비 중지 마감 시간 초과")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if after.State != diagnosisprep.Unknown || after.TaskID != before.TaskID || !after.PreparationStarted || after.Started ||
		after.AuthorityState != diagnosisprep.AuthorityNotIssued || !after.DrainComplete || after.ResultReady ||
		after.ModelUsage == nil || after.ModelUsage.UsageKnown || after.ModelUsage.Requests != calls {
		t.Fatalf("준비 중지 사실 불일치 state=%s phase=%s reason=%s started=%v drain=%v result=%v authority=%s", after.State, after.Phase, after.ReasonCode, after.Started, after.DrainComplete, after.ResultReady, after.AuthorityState)
	}
	time.Sleep(200 * time.Millisecond)
	var tasks, intents, runs int
	if f.installer.QueryRow("SELECT count(*) FROM tasks").Scan(&tasks) != nil ||
		f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE kind='intent'").Scan(&intents) != nil ||
		f.installer.QueryRow("SELECT count(*) FROM exploration_nodes WHERE origin=$1", diagnosisRunOrigin).Scan(&runs) != nil {
		t.Fatal("중지 후 집계 실패")
	}
	if tasks != 1 || intents != 0 || runs != 0 || f.modelCalls.Load() != calls || f.allowed.Load() != 0 || f.denied.Load() != 0 {
		t.Fatal("준비 중지 후 신규 모델·대상·실행 부작용")
	}
	t.Logf("URL_PREPARATION_STOP tasks=%d intents=%d runs=%d model_calls=%d additional_model_calls=0 target_sends=0 authority=not_issued started=false drain=true result=false state=unknown usage_known=false", tasks, intents, runs, calls)
}

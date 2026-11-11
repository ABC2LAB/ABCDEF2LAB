//go:build linux

package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/pathmask"
	"github.com/Autumn-27/artex/transmit"
	"github.com/jackc/pgx/v5"
)

// 합성 권위는 정확한 digest/task/intent만 허용한다. 실제 Planner 수용 시험이 아니다.
type standaloneAuthorityFixture struct {
	ctx    context.Context
	digest [32]byte
}

func (a *standaloneAuthorityFixture) Context() context.Context { return a.ctx }
func (a *standaloneAuthorityFixture) CheckRoot(ctx context.Context) error {
	return errors.Join(ctx.Err(), a.ctx.Err())
}
func (a *standaloneAuthorityFixture) AuthorizePolicy(ctx context.Context, d [32]byte) error {
	if d != a.digest {
		return ErrE2Control
	}
	return a.CheckRoot(ctx)
}

type standaloneSupervisorFixture struct{}

func (standaloneSupervisorFixture) AuthorizeExecution(_ context.Context, s transmit.Scope, intent int64) error {
	if s.TaskRef != "11" || s.RunRef != "fixture-run" || s.PolicyRef != "fixture-policy" || s.CaseRef != "fixture-case" || s.ResetRef != strings.Repeat("a", 64) || (intent != 17 && intent != 18) {
		return ErrE2Control
	}
	return nil
}

type standaloneReadinessFixture struct{ reject bool }

func (r *standaloneReadinessFixture) CheckReady(context.Context) error {
	if r.reject {
		return ErrE2Control
	}
	return nil
}

func standaloneFixture(t *testing.T, target string, realHelper bool, adjust ...func(*E2HTTPFilePolicy)) (*E2StandaloneBackend, *standaloneReadinessFixture, E2HTTPFilePolicy) {
	t.Helper()
	return standaloneFixtureWithPathKey(t, target, realHelper, nil, adjust...)
}

// pathKey가 있으면 수용시험 회차 설정(e2config v2, path_key_file)으로 만든다.
func standaloneFixtureWithPathKey(t *testing.T, target string, realHelper bool, pathKey []byte, adjust ...func(*E2HTTPFilePolicy)) (*E2StandaloneBackend, *standaloneReadinessFixture, E2HTTPFilePolicy) {
	t.Helper()
	host, _, p := e2HostPolicyFixture(t)
	if target != "" {
		p.Rules[0].Origin = target
		p.Rules[0].PathPrefix = "/allowed"
	}
	if realHelper {
		helper := filepath.Join(host.AuthorityRoot, "e2-bash-sandbox")
		cmd := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p=2", "-buildvcs=false", "-o", helper, "../cmd/e2-bash-sandbox")
		cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("합성 helper 빌드 실패: %s", output)
		}
		if os.Chmod(helper, 0700) != nil {
			t.Fatal("helper mode")
		}
		d, err := e2HTTPExecutableDigest(helper)
		if err != nil {
			t.Fatal(err)
		}
		p.Helper = helper
		p.HelperSHA256 = hex.EncodeToString(d[:])
		p.Runtime.WorkerTimeoutMS = 5000
		p.Runtime.DrainTimeoutMS = 5000
		p.Limits.CallTimeoutMS = 5000
	}
	for _, change := range adjust {
		change(&p)
	}
	raw, err := json.Marshal(p)
	if err != nil || os.WriteFile(host.PolicyFile, raw, 0600) != nil {
		t.Fatal("policy write")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(raw)}
	f, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: host.PolicyFile, Authority: a, Supervisor: standaloneSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	config := e2config.Config{Format: e2config.Format, EvalRunID: "eval-fixture", ReservationRef: "reservation-fixture", ModelTag: "model-fixture", HeaderMode: string(p.HeaderMode), PolicyFile: filepath.Base(host.PolicyFile), AuditKeyFile: p.AuditKey.File, Listen: ":18432", Upstream: "http://eval-meter-upstream:18432", EvidenceDir: firstDiagnosisPrivateTemp(t), ControlDir: e2ControlPrivateTestDir(t)}
	if pathKey != nil {
		config.Format, config.PathKeyFile = e2config.FormatPathKey, "path.key"
		if os.WriteFile(filepath.Join(host.AuthorityRoot, "path.key"), pathKey, 0600) != nil {
			t.Fatal("path key write")
		}
	}
	wire, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.AuthorityRoot, "runner.json")
	if os.WriteFile(path, wire, 0600) != nil {
		t.Fatal("config write")
	}
	c, err := e2config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ready := &standaloneReadinessFixture{}
	b, err := NewE2StandaloneBackend(ctx, E2StandaloneOptions{Config: c, Authority: a, Supervisor: standaloneSupervisorFixture{}, Readiness: ready, ExecutionRef: "execution-fixture", ExpectedPolicyFingerprintSHA256: f.PolicyFingerprintSHA256})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Dispose(); err != nil {
			t.Error(err)
		}
	})
	return b, ready, p
}
func TestE2StandaloneBindIsOneTimeAndPrebindHasNoExecution(t *testing.T) {
	b, ready, p := standaloneFixture(t, "", false)
	q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
	if _, err := b.Execute(t.Context(), q, "true"); err == nil {
		t.Fatal("prebind execution")
	}
	if _, err := os.Stat(filepath.Join(p.StateDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("prebind journal")
	}
	v, err := b.Ready(t.Context())
	if err != nil || v.State != "service_ready" || v.ConfigSHA256 != b.options.Config.ConfigSHA256 || v.ConfigSHA256 == b.factory.PolicyConfigSHA256 {
		t.Fatal("config digests conflated")
	}
	reset := strings.Repeat("a", 64)
	for _, bad := range []string{"container-name", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if b.BindReset(t.Context(), bad) == nil {
			t.Fatal("invalid reset")
		}
	}
	if b.BindReset(t.Context(), reset) != nil || b.BindReset(t.Context(), reset) != nil || b.BindReset(t.Context(), strings.Repeat("b", 64)) == nil {
		t.Fatal("binding not immutable")
	}
	ready.reject = true
	if _, err := b.Execute(t.Context(), q, "true"); err == nil {
		t.Fatal("unready execution")
	}
	ready.reject = false
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err != nil {
		t.Fatal(err)
	}
	final, err := b.Finalize(t.Context(), "close-one")
	if err != nil || final.State != "no_execution" {
		t.Fatal("no-execution final")
	}
}
func TestE2StandaloneFailedBindCannotSwitchContainer(t *testing.T) {
	b, r, _ := standaloneFixture(t, "", false)
	r.reject = true
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) == nil {
		t.Fatal("unready bind")
	}
	r.reject = false
	if b.BindReset(t.Context(), strings.Repeat("b", 64)) == nil || b.BindReset(t.Context(), strings.Repeat("a", 64)) == nil {
		t.Fatal("failed bind resumed")
	}
}
func TestE2StandaloneRejectsChangedPreparedConfig(t *testing.T) {
	b, _, _ := standaloneFixture(t, "", false)
	b.options.Config.HeaderMode = "evaluation_caller_passthrough"
	v, _ := b.Ready(t.Context())
	if v.State == "service_ready" || b.BindReset(t.Context(), strings.Repeat("a", 64)) == nil {
		t.Fatal("mutable config accepted")
	}
}
func TestE2StandaloneActualRunnerAllowDenyAndExport(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 격리 시험")
	}
	probe := exec.CommandContext(t.Context(), "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("사용자 namespace 환경 차단")
		}
		t.Fatal("namespace probe")
	}
	var received atomic.Int64
	attemptHeaders := make(chan string, 4)
	allowedAttempt := ""
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		attemptHeaders <- r.Header.Get(e2proxy.AttemptHeader)
		_, _ = w.Write([]byte("fixture"))
	}))
	defer target.Close()
	b, _, p := standaloneFixture(t, target.URL, true)
	rawBefore, err := os.ReadFile(b.options.Config.PolicyPath)
	if err != nil {
		t.Fatal(err)
	}
	reset := strings.Repeat("a", 64)
	if b.BindReset(t.Context(), reset) != nil {
		t.Fatal("bind")
	}
	q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
	for _, path := range []string{"/allowed", "/denied"} {
		r, err := b.Execute(t.Context(), q, "curl --silent --max-time 2 '"+target.URL+path+"' >/dev/null")
		if err != nil || !r.Trusted.Final {
			t.Fatalf("actual runner final=%t err=%v", r.Trusted.Final, err)
		}
		if path == "/allowed" {
			if len(r.Trusted.Call.Observations) != 1 {
				t.Fatal("allowed observation missing")
			}
			allowedAttempt = r.Trusted.Call.Observations[0].AttemptRef
			if allowedAttempt == "" || allowedAttempt != <-attemptHeaders {
				t.Fatal("attempt header mismatch")
			}
		} else if r.Trusted.Call.Counts.ForwardAttempted != 0 || r.Trusted.Call.Counts.Denied != 1 {
			t.Fatal("denied forwarding")
		}
	}
	if received.Load() != 1 {
		t.Fatalf("received=%d", received.Load())
	}
	direct, err := b.Execute(t.Context(), q, "curl --silent --noproxy '*' --max-time 2 '"+target.URL+"/allowed' >/dev/null")
	if !errors.Is(err, e2runner.ErrUnavailable) || !direct.Trusted.Final || direct.Trusted.Classification != "tool_failure" || direct.Trusted.Call.Counts.ForwardAttempted != 0 || direct.Trusted.Call.Counts.Denied != 0 || received.Load() != 1 {
		t.Fatalf("direct isolation: err=%v final=%t classification=%s counts=%+v received=%d", err, direct.Trusted.Final, direct.Trusted.Classification, direct.Trusted.Call.Counts, received.Load())
	}
	q.IntentID = 999
	if _, err := b.Execute(t.Context(), q, "true"); err == nil {
		t.Fatal("unknown intent accepted")
	}
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err != nil {
		t.Fatal(err)
	}
	if b.acquired.Execution.Snapshot().State != "blocked" {
		t.Fatal("ACK before fence")
	}
	final, err := b.Finalize(t.Context(), "close-one")
	if err != nil || final.State != "complete" {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	again, err := b.Finalize(t.Context(), "close-one")
	if err != nil || !reflectFinal(final, again) {
		t.Fatal("non-idempotent finalize")
	}
	rawAfter, _ := os.ReadFile(b.options.Config.PolicyPath)
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatal("policy rewritten")
	}
	journal, err := os.ReadFile(filepath.Join(p.StateDir, "events.jsonl"))
	if err != nil || !bytes.Contains(journal, []byte(reset)) || bytes.Contains(journal, []byte("fixture-reset")) {
		t.Fatal("journal reset binding")
	}
	manifest, err := os.ReadFile(filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, "manifest.json"))
	if err != nil || !bytes.Contains(manifest, []byte(reset)) {
		t.Fatal("manifest reset binding")
	}
	// 공식 회차 설정(v1)에는 시도 경로 투영이 없다.
	if _, err := os.Stat(filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, E2AttemptPathsFile)); !os.IsNotExist(err) {
		t.Fatal("v1 configuration wrote attempt paths")
	}
	matched := false
	for _, line := range bytes.Split(journal, []byte{'\n'}) {
		var row struct {
			Record *struct{ Event e2proxy.Event }
		}
		if json.Unmarshal(line, &row) == nil && row.Record != nil {
			e := row.Record.Event
			if e.AttemptID == allowedAttempt && e.ForwardAttempted {
				matched = true
			}
		}
	}
	if !matched {
		t.Fatal("trusted attempt missing from journal")
	}
	verify := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "-p=2", "../cmd/e2-audit-verify", "--journal-format", "e2-http-v1", "--journal", filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, e2export.JournalFile), "--checkpoint", filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, e2export.CheckpointFile), "--key-file", b.options.Config.AuditKeyPath)
	verify.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("actual audit verifier failed: %s", out)
	}
	b.publisher.Dispose()
}
func reflectFinal(a, b E2ControlFinal) bool {
	return a.State == b.State && a.ManifestSHA256 == b.ManifestSHA256 && a.ExecutionRef == b.ExecutionRef
}

// DB가 필요 없는 자식은 부모 suite의 PostgreSQL advisory lock을 다시 획득하면 안 된다.
// 빈 DSN은 config.json으로 fallback하므로, 연결 전에 파싱이 실패하는 명시적 값을 쓴다.
// 부모 환경과 다른 PostgreSQL 시험의 환경은 변경하지 않는다.
const standaloneChildNoDatabaseDSN = "postgres://%"

func standaloneChildEnv(parent []string) []string {
	child := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		if !strings.HasPrefix(entry, "ARTEX_PG_DSN=") {
			child = append(child, entry)
		}
	}
	return append(child, "ARTEX_PG_DSN="+standaloneChildNoDatabaseDSN)
}

func TestE2StandaloneSubprocessOwnsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2StandaloneActualRunnerAllowDenyAndExport$", "-test.v", "-test.timeout=90s")
	cmd.Env = standaloneChildEnv(os.Environ())
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("독립 프로세스 lifecycle 실패: %s", output)
	}
	if bytes.Contains(output, []byte("--- SKIP:")) {
		t.Skip("하위 프로세스 Linux 격리 불가")
	}
	if !bytes.Contains(output, []byte("--- PASS: TestE2StandaloneActualRunnerAllowDenyAndExport")) {
		t.Fatal("하위 프로세스 검증 결과 없음")
	}
}

func TestE2StandaloneRejectsPreparedAuditKeyMismatch(t *testing.T) {
	for _, mode := range []string{"path", "digest"} {
		t.Run(mode, func(t *testing.T) {
			b, _, p := standaloneFixture(t, "", false)
			if mode == "path" {
				p.AuditKey.File = p.CAKey.File
			} else {
				p.AuditKey.SHA256 = strings.Repeat("0", 64)
			}
			raw, err := json.Marshal(p)
			if err != nil || os.WriteFile(b.options.Config.PolicyPath, raw, 0600) != nil {
				t.Fatal("policy write")
			}
			c, err := e2config.Load(filepath.Join(filepath.Dir(b.options.Config.PolicyPath), "runner.json"))
			if err != nil {
				t.Fatal(err)
			}
			o := b.options
			o.Config = c
			o.Authority = &standaloneAuthorityFixture{ctx: b.ctx, digest: sha256.Sum256(raw)}
			if backend, err := NewE2StandaloneBackend(t.Context(), o); err == nil || backend != nil {
				t.Fatal("prepared audit key mismatch accepted")
			}
		})
	}
}

// 실제 별도 OS 프로세스와 UDS wire를 검증한다. 역할 UID 숫자·task 권위는 합성 fixture이며
// 실제 다른 UID/mount 또는 인증된 원격 Planner 수용을 주장하지 않는다.
func TestE2StandaloneSubprocessControlWire(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 제어 fixture")
	}
	if os.Getenv("ABC2LAB_E2_STANDALONE_TEST_CHILD") == "1" {
		var received atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); _, _ = w.Write([]byte("fixture")) }))
		defer target.Close()
		b, _, _ := standaloneFixture(t, target.URL, true)
		uid := uint32(os.Geteuid())
		service, err := StartE2ControlService(t.Context(), E2ControlOptions{ControlDirectory: b.options.Config.ControlDir, EvalRunID: b.options.Config.EvalRunID, ReservationRef: b.options.Config.ReservationRef, RunnerUID: uid, EngineUID: uid + 1, ToolsUID: uid + 2, EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: 5 * time.Second, MaxConnections: 2, Backend: b})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if service.Close(ctx) != nil {
				t.Error("service close")
			}
		}()
		fmt.Println("E2_TEST_SOCKET=" + filepath.Join(b.options.Config.ControlDir, E2ControlSocket))
		input := bufio.NewScanner(os.Stdin)
		if !input.Scan() || input.Text() != "run" {
			t.Fatal("run handshake")
		}
		q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
		for _, path := range []string{"/allowed", "/denied"} {
			r, err := b.Execute(t.Context(), q, "curl --silent --max-time 2 '"+target.URL+path+"' >/dev/null")
			if err != nil || !r.Trusted.Final {
				t.Fatal("child actual runner")
			}
		}
		if received.Load() != 1 {
			t.Fatal("child upstream count")
		}
		fmt.Println("E2_TEST_CALLS=2_RECEIVED=1")
		if !input.Scan() || input.Text() != "done" {
			t.Fatal("final handshake")
		}
		head, err := b.Head(t.Context())
		if err != nil || head.State != "complete" {
			t.Fatal("child final incomplete")
		}
		return
	}
	probe := exec.CommandContext(t.Context(), "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("사용자 namespace 환경 차단")
		}
		t.Fatal("namespace probe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2StandaloneSubprocessControlWire$", "-test.v", "-test.timeout=90s")
	cmd.Env = append(standaloneChildEnv(os.Environ()), "ABC2LAB_E2_STANDALONE_TEST_CHILD=1")
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if cmd.Start() != nil {
		t.Fatal("child start")
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
	}()
	scan := bufio.NewScanner(stdout)
	readUntil := func(prefix string) string {
		t.Helper()
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, prefix) {
				return strings.TrimPrefix(line, prefix)
			}
		}
		t.Fatalf("child handshake missing: %s", prefix)
		return ""
	}
	socket := readUntil("E2_TEST_SOCKET=")
	request := func(kind string) map[string]any {
		t.Helper()
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if conn.SetDeadline(time.Now().Add(2*time.Second)) != nil {
			t.Fatal("deadline")
		}
		q := map[string]any{"format": E2ControlFormat, "type": kind, "eval_run_id": "eval-fixture", "reservation_ref": "reservation-fixture"}
		if kind == "bind" {
			q["reset_generation"] = strings.Repeat("a", 64)
		}
		if kind == "close" || kind == "close_status" {
			q["close_request_ref"] = "close-wire"
		}
		if json.NewEncoder(conn).Encode(q) != nil {
			t.Fatal("request encode")
		}
		var reply map[string]any
		if json.NewDecoder(conn).Decode(&reply) != nil {
			t.Fatal("response decode")
		}
		return reply
	}
	if request("ready")["state"] != "service_ready" || request("head")["state"] != "no_execution" {
		t.Fatal("wire prebind")
	}
	if request("bind")["reset_generation"] != strings.Repeat("a", 64) {
		t.Fatal("wire bind")
	}
	if _, err := fmt.Fprintln(stdin, "run"); err != nil {
		t.Fatal(err)
	}
	readUntil("E2_TEST_CALLS=2_RECEIVED=1")
	if request("head")["state"] != "running" {
		t.Fatal("wire execution")
	}
	if request("close")["admission_closed"] != true {
		t.Fatal("wire close fence")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := request("close_status")
		if v["state"] == "complete" {
			break
		}
		if v["state"] != "closing" || time.Now().After(deadline) {
			t.Fatal("wire final incomplete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := fmt.Fprintln(stdin, "done"); err != nil {
		t.Fatal(err)
	}
	if stdin.Close() != nil {
		t.Fatal("stdin close")
	}
	for scan.Scan() {
	}
	if scan.Err() != nil {
		t.Fatal(scan.Err())
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("child exit failed: %s", stderr.String())
	}
}

func TestE2StandaloneZeroValueFailsClosed(t *testing.T) {
	for _, b := range []*E2StandaloneBackend{nil, {}} {
		if v, err := b.Ready(t.Context()); err == nil && v.State == "service_ready" {
			t.Fatal("zero ready")
		}
		if _, err := b.Execute(t.Context(), E2HTTPRequest{}, "true"); err == nil {
			t.Fatal("zero execute")
		}
		if b.BindReset(t.Context(), strings.Repeat("a", 64)) == nil {
			t.Fatal("zero bind")
		}
		if _, err := b.Head(t.Context()); err == nil {
			t.Fatal("zero head")
		}
		if _, err := b.CloseAdmission(t.Context(), "close-one"); err == nil {
			t.Fatal("zero close")
		}
		if _, err := b.Finalize(t.Context(), "close-one"); err == nil {
			t.Fatal("zero final")
		}
		if err := b.Dispose(); b != nil && err == nil {
			t.Fatal("zero dispose")
		}
	}
}

func TestE2StandaloneFailedPublicationStillReleasesJournal(t *testing.T) {
	b, _, _ := standaloneFixture(t, "", false)
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	lease, err := b.factory.Acquire(b.ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17})
	if err != nil {
		t.Fatal(err)
	}
	b.lease = &lease
	forged := e2runner.Result{Trusted: e2runner.TrustedResult{Call: e2proxy.CallResult{CallRef: "not-an-actual-call"}}}
	if b.publisher.ObserveResult(t.Context(), forged) == nil {
		t.Fatal("observer accepted forged result")
	}
	released := false
	original := b.lease.Release
	b.lease.Release = func(ctx context.Context) error {
		released = true
		if ctx.Err() != nil {
			t.Fatal("cleanup inherited canceled context")
		}
		return original(ctx)
	}
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err != nil {
		t.Fatal(err)
	}
	final, err := b.Finalize(t.Context(), "close-one")
	if err != nil || final.State != "incomplete" || !released {
		t.Fatal("failed publication cleanup")
	}
	if !errors.Is(b.acquired.Journal.Record(t.Context(), e2proxy.Event{}), e2proxy.ErrJournalClosed) {
		t.Fatal("journal leaked")
	}
}

func TestE2StandaloneFailedStopHasAbortCleanup(t *testing.T) {
	b, _, _ := standaloneFixture(t, "", false)
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	lease, err := b.factory.Acquire(b.ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17})
	if err != nil {
		t.Fatal(err)
	}
	b.lease = &lease
	if b.acquired.Journal.Close() != nil {
		t.Fatal("force closed journal")
	}
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err == nil {
		t.Fatal("failed stop ACK")
	}
	if _, err := b.Finalize(t.Context(), "close-one"); err == nil {
		t.Fatal("failed stop publication")
	}
	released := false
	original := b.lease.Release
	b.lease.Release = func(ctx context.Context) error { released = true; return original(ctx) }
	err = b.Dispose()
	if !released || !b.disposed || err == nil {
		t.Fatal("abort did not clean and retain failure")
	}
	if _, err := os.Stat(filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, e2export.ManifestFile)); !os.IsNotExist(err) {
		t.Fatal("abort published manifest")
	}
}

func TestE2StandaloneRuntimeToolCallLimitIsAtomic(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 격리 시험")
	}
	probe := exec.CommandContext(t.Context(), "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("사용자 namespace 환경 차단")
		}
		t.Fatal("namespace probe")
	}
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); _, _ = w.Write([]byte("fixture")) }))
	defer target.Close()
	b, _, _ := standaloneFixture(t, target.URL, true, func(p *E2HTTPFilePolicy) { p.Runtime.MaxToolCalls = 1 })
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
	done := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := b.Execute(t.Context(), q, "curl --silent --max-time 2 '"+target.URL+"/allowed' >/dev/null")
			done <- err
		}()
	}
	close(start)
	accepted := 0
	for i := 0; i < 2; i++ {
		if <-done == nil {
			accepted++
		}
	}
	if accepted != 1 || received.Load() != 1 || len(b.acquired.Execution.Calls()) != 1 {
		t.Fatalf("runtime tool call limit failed: accepted=%d received=%d", accepted, received.Load())
	}
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err != nil {
		t.Fatal(err)
	}
	final, err := b.Finalize(t.Context(), "close-one")
	if err != nil || final.State != "complete" {
		t.Fatal("limited call final")
	}
}

func TestE2StandaloneChildEnvironmentDoesNotChangeParentDatabase(t *testing.T) {
	before, present := os.LookupEnv("ARTEX_PG_DSN")
	parent := []string{"ARTEX_PG_DSN=postgres://synthetic-parent/db", "PATH=/synthetic/bin", "ARTEX_FIRST_DIAGNOSIS_TEST_DSN=postgres://synthetic-owner/db", "ARTEX_PG_DSN=postgres://duplicate-parent/db"}
	original := append([]string(nil), parent...)
	child := standaloneChildEnv(parent)
	count := 0
	for _, entry := range child {
		if strings.HasPrefix(entry, "ARTEX_PG_DSN=") {
			count++
			if entry != "ARTEX_PG_DSN="+standaloneChildNoDatabaseDSN {
				t.Fatal("child inherited parent DB")
			}
		}
	}
	if count != 1 || len(child) != 3 || strings.Join(parent, "\n") != strings.Join(original, "\n") || strings.Join(child[:2], "\n") != "PATH=/synthetic/bin\nARTEX_FIRST_DIAGNOSIS_TEST_DSN=postgres://synthetic-owner/db" {
		t.Fatal("child filter changed unrelated inputs")
	}
	after, stillPresent := os.LookupEnv("ARTEX_PG_DSN")
	if before != after || present != stillPresent {
		t.Fatal("parent database environment changed")
	}
	// database/sql은 파싱을 첫 Ping까지 미룬다. pgx 파싱 거부를 확인하여
	// TestMain의 Ping이 실제 연결/advisory lock까지 갈 수 없음을 검증한다.
	if _, err := pgx.ParseConfig(standaloneChildNoDatabaseDSN); err == nil {
		t.Fatal("child DSN did not fail before connection")
	}
}

// 이미 획득된 실제 lease를 써서 factory의 중복 승인 검사에 기대지 않는다.
func TestE2StandaloneRejectsUnauthorizedIntentBeforeRegistration(t *testing.T) {
	b, _, _ := standaloneFixture(t, "", false)
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
	lease, err := b.factory.Acquire(b.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	b.lease = &lease
	q.IntentID = 999
	if _, err := b.Execute(t.Context(), q, ""); !errors.Is(err, ErrE2Control) {
		t.Fatal("unapproved intent did not fail at authorization")
	}
	if b.acquired.Execution.HasIntent(999) || len(b.acquired.Execution.Calls()) != 0 || b.toolCalls != 0 || b.failed {
		t.Fatal("unapproved intent reached registration or runner")
	}
}

func TestE2StandalonePrebindStopsBeforeAuthorityOrAcquisition(t *testing.T) {
	b, _, p := standaloneFixture(t, "", false)
	// 하위 승인/획득 거부가 prebind 가드 누락을 가리지 않도록 호출 자체를 계측한다.
	authorized, acquired := 0, 0
	b.factory.AuthorizeIntent = func(context.Context, E2HTTPRequest) error { authorized++; return nil }
	b.factory.Acquire = func(context.Context, E2HTTPRequest) (E2HTTPRunOptions, error) {
		acquired++
		return E2HTTPRunOptions{}, ErrE2Control
	}
	if _, err := b.Execute(t.Context(), E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}, ""); !errors.Is(err, ErrE2Control) {
		t.Fatal("prebind execute accepted")
	}
	if authorized != 0 || acquired != 0 || b.lease != nil || b.failed {
		t.Fatal("prebind execute entered a later boundary")
	}
	if _, err := os.Stat(filepath.Join(p.StateDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("prebind allocated journal")
	}
}

type standaloneChangingAuthorityFixture struct {
	E2HTTPPolicyAuthority
	change func()
}

func (a standaloneChangingAuthorityFixture) AuthorizePolicy(ctx context.Context, digest [32]byte) error {
	a.change()
	return a.E2HTTPPolicyAuthority.AuthorizePolicy(ctx, digest)
}

func TestE2StandaloneConstructorCrossChecksMetadata(t *testing.T) {
	for _, mode := range []string{"policy_digest", "fingerprint", "header_mode"} {
		t.Run(mode, func(t *testing.T) {
			b, _, _ := standaloneFixture(t, "", false)
			o := b.options
			switch mode {
			case "fingerprint":
				o.ExpectedPolicyFingerprintSHA256 = strings.Repeat("f", 64)
				if o.ExpectedPolicyFingerprintSHA256 == b.factory.PolicyFingerprintSHA256 {
					t.Fatal("fixture fingerprint collision")
				}
			case "header_mode":
				// 준비 파일 자체는 올바르게 다시 동결하되 실제 policy와 header mode만 다르게 한다.
				c := *o.Config
				c.HeaderMode = string(e2proxy.HeaderProtectedSessionProposal)
				raw, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(filepath.Dir(c.PolicyPath), "runner.json")
				if os.WriteFile(path, raw, 0600) != nil {
					t.Fatal("config write")
				}
				o.Config, err = e2config.Load(path)
				if err != nil {
					t.Fatal(err)
				}
			case "policy_digest":
				// 정상 불변 Config에서는 두 digest가 동일하다. 최종 방어 비교만 검증하기 위해
				// 초기 Check/ReadPolicy 뒤 권위 callback 시점의 변경을 명시적으로 합성한다.
				original := o.Config.PolicySHA256
				t.Cleanup(func() { o.Config.PolicySHA256 = original })
				o.Authority = standaloneChangingAuthorityFixture{E2HTTPPolicyAuthority: o.Authority, change: func() { o.Config.PolicySHA256 = strings.Repeat("f", 64) }}
			}
			got, err := NewE2StandaloneBackend(t.Context(), o)
			if got != nil {
				if disposeErr := got.Dispose(); disposeErr != nil {
					t.Error(disposeErr)
				}
			}
			if got != nil || !errors.Is(err, ErrE2Control) {
				t.Fatal("constructor metadata mismatch accepted")
			}
		})
	}
}

func TestE2StandaloneObservationFailureLatchesAndPreventsPublish(t *testing.T) {
	b, _, _ := standaloneFixture(t, "", false)
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
	lease, err := b.factory.Acquire(b.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	b.lease = &lease
	publisher := b.publisher
	b.publisher = nil
	// 빈 명령은 실제 Runner가 call 생성 전에 거부한다. 관찰 공급자 실패만 주입하여
	// 실행/저널 자체는 건강하게 남겨, failed 검사 누락 시 Publish 성공을 검출한다.
	_, err = b.Execute(t.Context(), q, "")
	b.publisher = publisher
	if !errors.Is(err, ErrE2Control) || !b.failed || len(b.acquired.Execution.Calls()) != 0 {
		t.Fatal("observation failure not latched")
	}
	ready, err := b.Ready(t.Context())
	if err != nil || ready.State != "failed" {
		t.Fatal("failed observation still ready")
	}
	calls := b.toolCalls
	if _, err := b.Execute(t.Context(), q, ""); !errors.Is(err, ErrE2Control) || b.toolCalls != calls {
		t.Fatal("failed observation admitted another command")
	}
	if _, err := b.CloseAdmission(t.Context(), "close-observer"); err != nil {
		t.Fatal(err)
	}
	final, err := b.Finalize(t.Context(), "close-observer")
	if err != nil || final.State != "incomplete" {
		t.Fatal("failed observer produced completion")
	}
	if _, err := os.Stat(filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, e2export.ManifestFile)); !os.IsNotExist(err) {
		t.Fatal("failed observer invoked publication")
	}
	// 정상 Publisher는 아직 Publish를 시도하지 않았다. Finalize가 실패 latch를 존중했으면
	// 독립적인 검증용 호출에서 complete 산출물을 만들 수 있어야 한다.
	if _, err := publisher.Publish(t.Context(), "probe-not-backend-final"); err != nil {
		t.Fatal("fixture publisher was not independently healthy")
	}
}

func TestE2StandaloneCloseReferencesRemainImmutable(t *testing.T) {
	b, _, _ := standaloneFixture(t, "", false)
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	if _, err := b.Finalize(t.Context(), "close-one"); !errors.Is(err, ErrE2Control) || b.final != nil {
		t.Fatal("finalized before close")
	}
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CloseAdmission(t.Context(), "close-other"); !errors.Is(err, ErrE2Control) || b.closeRef != "close-one" {
		t.Fatal("close reference replaced")
	}
	if _, err := b.Finalize(t.Context(), "close-other"); !errors.Is(err, ErrE2Control) || b.final != nil {
		t.Fatal("wrong initial finalize reference accepted")
	}
	first, err := b.Finalize(t.Context(), "close-one")
	if err != nil || first.State != "no_execution" {
		t.Fatal("original close failed")
	}
	if _, err := b.Finalize(t.Context(), "close-other"); !errors.Is(err, ErrE2Control) {
		t.Fatal("cached final returned to wrong reference")
	}
	again, err := b.Finalize(t.Context(), "close-one")
	if err != nil || !reflectFinal(first, again) {
		t.Fatal("original final changed")
	}
}

func TestE2StandaloneAcquisitionRejectsResetMismatchAtBackend(t *testing.T) {
	source, _, _ := standaloneFixture(t, "", false)
	// 두 core는 같은 실제 정책만 읽고 아직 자원을 획득하지 않았다.
	target, err := NewE2StandaloneBackend(t.Context(), source.options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := target.Dispose(); err != nil {
			t.Error(err)
		}
	})
	if source.BindReset(t.Context(), strings.Repeat("a", 64)) != nil || target.BindReset(t.Context(), strings.Repeat("b", 64)) != nil {
		t.Fatal("bind")
	}
	lease, err := source.factory.Acquire(source.ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17})
	if err != nil {
		t.Fatal(err)
	}
	source.lease = &lease
	acquired := *source.acquired
	acquired.AuditKey, err = source.options.Config.ReadAuditKey()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(acquired.AuditKey)
	// 단순 err!=nil은 exporter의 중복 reset 검사로도 통과한다. backend 고유 오류와
	// 소유권 미이전을 함께 확인해 backend reset 비교 삭제를 검출한다.
	if err := target.onAcquire(t.Context(), acquired); !errors.Is(err, ErrE2Control) || target.acquired != nil || target.publisher != nil {
		t.Fatal("mismatched reset passed backend acquisition boundary")
	}
}

// 수용시험 회차(v2)는 감사 저널의 모든 결과 시도마다 한 행씩, 계측기와 같은 규칙·같은 키로 가린 경로를 남긴다.
func TestE2StandaloneWritesAttemptPathsForPathKeyConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 격리 시험")
	}
	probe := exec.CommandContext(t.Context(), "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("사용자 namespace 환경 차단")
		}
		t.Fatal("namespace probe")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("fixture")) }))
	defer target.Close()
	key := bytes.Repeat([]byte{37}, pathmask.KeyBytes)
	b, _, p := standaloneFixtureWithPathKey(t, target.URL, true, key)
	if b.BindReset(t.Context(), strings.Repeat("a", 64)) != nil {
		t.Fatal("bind")
	}
	q := E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}
	// 제품 기본 거부 한도(2)에 닿지 않게 거부는 한 번만 보낸다. 같은 경로 반복은 단위 시험이 본다.
	for _, path := range []string{"/allowed/item", "/denied/jimsmithexample"} {
		if r, err := b.Execute(t.Context(), q, "curl --silent --max-time 2 '"+target.URL+path+"' >/dev/null"); err != nil || !r.Trusted.Final {
			t.Fatalf("runner final=%t err=%v", r.Trusted.Final, err)
		}
	}
	if _, err := b.CloseAdmission(t.Context(), "close-one"); err != nil {
		t.Fatal(err)
	}
	if final, err := b.Finalize(t.Context(), "close-one"); err != nil || final.State != "complete" {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	journal, err := os.ReadFile(filepath.Join(p.StateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[float64]string{}
	for _, line := range bytes.Split(journal, []byte{'\n'}) {
		var row struct {
			Record *struct{ Event e2proxy.Event }
		}
		if json.Unmarshal(line, &row) == nil && row.Record != nil && row.Record.Event.Stage == "result" {
			decisions[float64(row.Record.Event.Sequence)] = row.Record.Event.Decision
		}
	}
	rows := readAttemptPathRows(t, filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, E2AttemptPathsFile))
	if len(rows) != len(decisions) || len(rows) != 2 {
		t.Fatalf("rows %d journal results %d", len(rows), len(decisions))
	}
	denied := 0
	for _, r := range rows {
		seq := r["sequence"].(float64)
		if decisions[seq] == "" || decisions[seq] != r["decision"] || r["path_key_ref"] != pathmask.KeyRef(key) {
			t.Fatalf("row does not match journal: %v (%s)", r, decisions[seq])
		}
		if r["decision"] == "denied" {
			denied++
			if pattern, _ := pathmask.Mask("/denied/jimsmithexample", key); r["path_pattern"] != pattern {
				t.Fatalf("denied pattern %v", r["path_pattern"])
			}
		}
	}
	raw, _ := os.ReadFile(filepath.Join(b.options.Config.EvidenceDir, b.options.ExecutionRef, E2AttemptPathsFile))
	if denied != 1 || bytes.Contains(raw, []byte("jimsmithexample")) || bytes.Contains(raw, []byte("denied/")) {
		t.Fatalf("denied=%d or raw path leaked", denied)
	}
	b.publisher.Dispose()
}

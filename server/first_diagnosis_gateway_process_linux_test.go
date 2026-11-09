//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

// 보호 config와 실제 loopback 모델 프로토콜만 전달한다. 자식에 provider 객체를 주입하지 않는다.
type firstDiagnosisGatewayProcessFixture struct {
	ctx                                                     context.Context
	binary, root, launchPath, addr, dataDir, keyDir, taskID string
	explorationID                                           int64
	installer                                               *db.DB
	hits, modelCalls                                        *atomic.Int64
}

func prepareFirstDiagnosisGatewayProcessFixture(t *testing.T) *firstDiagnosisGatewayProcessFixture {
	helper := firstDiagnosisNativeHelper(t)
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	root := firstDiagnosisPrivateTemp(t)
	binary := filepath.Join(root, "first-controlled-diagnosis")
	build := exec.CommandContext(ctx, "go", "build", "-p", "1", "-o", binary, "../cmd/first-controlled-diagnosis")
	if err := build.Run(); err != nil {
		t.Fatal("독립 CLI 빌드 실패")
	}
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.CreateTask("독립 프로세스 합성 진단", "승인된 loopback 관찰을 저장합니다", nil, 60, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = installer.Exec("UPDATE tasks SET paused=true,queued=false WHERE id=$1", task.ID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true; s.Queued = false })
	if _, err = task.Store.AddIntent(map[string]any{"action": "Inspect the owned loopback fixture through Bash and submit a diagnostic candidate."}, 1, nil, "synthetic process acceptance"); err != nil {
		t.Fatal(err)
	}
	pass, _ := bcrypt.GenerateFromPassword([]byte("synthetic-owned-combined-fixture"), bcrypt.MinCost)
	if installer.SetSetting(authPassKey, string(pass)) != nil {
		t.Fatal("시험 인증 준비 실패")
	}
	var hits, modelCalls, workerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/allowed" {
			t.Error("범위 밖 대상 수신")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"owned_fixture":true}`)
	}))
	t.Cleanup(target.Close)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" {
			t.Error("예상하지 않은 모델 경로")
			http.Error(w, "unsupported", 400)
			return
		}
		var input struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		isWorker := false
		for _, tool := range input.Tools {
			if tool.Function.Name == "Bash" {
				isWorker = true
			}
		}
		message := map[string]any{"role": "assistant", "content": "No further authorized actions in this owned fixture."}
		finish := "stop"
		toolCall := func(name, id string, args any) {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		if isWorker {
			call := workerCalls.Add(1)
			if call == 1 {
				toolCall("Bash", "gateway-bash", map[string]any{"command": "curl --silent --show-error --max-time 1 " + target.URL + "/allowed", "timeout_ms": 1500})
			} else if call == 2 {
				var callRef string
				for _, message := range input.Messages {
					if message.Role != "tool" {
						continue
					}
					var text string
					if json.Unmarshal(message.Content, &text) != nil {
						continue
					}
					var observation e2runner.Result
					if json.Unmarshal([]byte(text), &observation) == nil && observation.Trusted.Version == e2proxy.Version {
						callRef = observation.Trusted.Call.CallRef
					}
				}
				if callRef != "" {
					toolCall("submit_diagnostic_candidate", "gateway-candidate", map[string]any{"call_ref": callRef, "vulnclass": "access_control", "name": "독립 프로세스 검토 후보", "summary": "소유 loopback 실제 관찰을 검토합니다.", "remediation": "접근 권한을 확인하세요."})
				} else {
					t.Error("실제 관찰 call_ref 누락")
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "owned-gateway-model", "object": "chat.completion", "created": time.Now().Unix(), "model": "local:synthetic", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	}))
	t.Cleanup(model.Close)
	profile := &db.LLMProfile{Name: "owned-independent-gateway", Format: "openai", Model: "local:synthetic", BaseURL: model.URL + "/v1", APIKey: "owned-synthetic-key", Streaming: false, ContextWindowK: 12}
	profileID, err := installer.SaveProfile(profile)
	if err != nil || installer.SetActiveProfile(profileID) != nil {
		t.Fatal("모델 profile 준비 실패")
	}
	firstDiagnosisPinFixtureTaskProfile(t, installer, task)
	manifest := filepath.Join(root, "models.json")
	manifestData, _ := json.Marshal(map[string]any{"version": 1, "profiles": []any{map[string]any{"profile_id": profileID, "format": "openai", "model": "local:synthetic", "base_url": profile.BaseURL, "addresses": []string{"127.0.0.1"}}}})
	if os.WriteFile(manifest, manifestData, 0600) != nil {
		t.Fatal("모델 pin 준비 실패")
	}
	cfg, grant, policy := e2HostPolicyFixture(t, 8*time.Minute)
	grant.TaskID = mustFirstDiagnosisTaskNumber(task.ID)
	grant.ExplorationID = task.ExpID
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	helperDigest := sha256.Sum256(helperBytes)
	policy.Helper = helper
	policy.HelperSHA256 = hex.EncodeToString(helperDigest[:])
	policy.StateDir = filepath.Join(root, "state")
	if os.Mkdir(policy.StateDir, 0700) != nil {
		t.Fatal("state 준비 실패")
	}
	policy.OwnerRef = "ARTEX"
	policy.ApprovalMode = e2proxy.ApprovalPreauthorizedScope
	policy.Rules = []E2HTTPFileRule{{Ref: "gateway-rule", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/allowed", PathScopeRef: "gateway-path", Methods: []string{"GET"}}}
	policy.Runtime.MaxWorkerTurns = 5
	policy.Runtime.MaxPlannerRounds = 4
	policyRaw, _ := json.Marshal(policy)
	if os.WriteFile(cfg.PolicyFile, policyRaw, 0600) != nil {
		t.Fatal("policy 준비 실패")
	}
	policyDigest := sha256.Sum256(policyRaw)
	grant.PolicySHA256 = hex.EncodeToString(policyDigest[:])
	e2HostWriteApproval(t, cfg, grant)
	keyDir, dataDir, skillDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("owned-gateway-jwt-material-at-least-32"), 0600) != nil {
		t.Fatal("JWT 준비 실패")
	}
	var runtimeDSN string
	connection, err := f.Runtime.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error {
		conn, ok := raw.(*stdlib.Conn)
		if !ok {
			return errors.New("unexpected fixture driver")
		}
		config := conn.Conn().Config()
		quote := func(value string) string {
			return "'" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "\\'") + "'"
		}
		runtimeDSN = "host=" + quote(config.Host) + " port=" + strconv.Itoa(int(config.Port)) + " user=" + quote(config.User) + " password=" + quote(config.Password) + " dbname=" + quote(config.Database) + " sslmode=disable"
		for key, value := range config.RuntimeParams {
			runtimeDSN += " " + key + "=" + quote(value)
		}
		return nil
	})
	_ = connection.Close()
	if err != nil {
		t.Fatal("소유 runtime DSN 추출 실패")
	}
	dsnPath := filepath.Join(root, "runtime-dsn")
	if os.WriteFile(dsnPath, []byte(runtimeDSN), 0600) != nil {
		t.Fatal("보호 runtime DSN 준비 실패")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	launch := map[string]any{
		"format": "abc2lab.first-diagnosis-launch/v1", "mode": "general_http", "listen_addr": addr,
		"data_dir": dataDir, "key_dir": keyDir, "skill_dir": skillDir, "local_llm_manifest_path": manifest,
		"model_profile_id": profileID, "model_format": "openai", "model_base_url": profile.BaseURL, "model_ref": "local:synthetic",
		"runtime_dsn_file": dsnPath, "shutdown_timeout_seconds": 10,
		"general_http": map[string]any{"policy_file": cfg.PolicyFile, "authority_file": cfg.AuthorityFile, "authority_root": cfg.AuthorityRoot},
	}
	launchRaw, _ := json.Marshal(launch)
	launchPath := filepath.Join(root, "launch.json")
	if os.WriteFile(launchPath, launchRaw, 0600) != nil {
		t.Fatal("보호 launch 준비 실패")
	}
	return &firstDiagnosisGatewayProcessFixture{ctx: ctx, binary: binary, root: root, launchPath: launchPath, addr: addr, dataDir: dataDir, keyDir: keyDir, taskID: task.ID, explorationID: task.ExpID, installer: installer, hits: &hits, modelCalls: &modelCalls}
}
func TestFirstDiagnosisGatewayIndependentProcessOwnedPostgres(t *testing.T) {
	f := prepareFirstDiagnosisGatewayProcessFixture(t)
	ctx, binary, launchPath, dataDir := f.ctx, f.binary, f.launchPath, f.dataDir
	hits, modelCalls, installer := f.hits, f.modelCalls, f.installer
	command := exec.CommandContext(ctx, binary, "gateway", "-config", launchPath, "-preparation-ref", "owned-process-preparation")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if command.Start() != nil {
		t.Fatal("독립 프로세스 시작 실패")
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = command.Process.Kill() })
	client, err := diagnosis.NewClient(stdout, stdin, func() error { return errors.Join(stdin.Close(), stdout.Close()) })
	if err != nil {
		t.Fatal(err)
	}
	call := func(kind diagnosis.Command) diagnosis.Response {
		t.Helper()
		request := diagnosis.Request{Version: diagnosis.Version, RequestID: "process-" + string(kind), Command: kind, TaskID: f.taskID, PreparationRef: "owned-process-preparation"}
		if kind == diagnosis.Start {
			request.IdempotencyKey = "single-process-start"
		}
		response, err := client.Call(ctx, request)
		if err != nil {
			_ = filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return nil
				}
				f, openErr := os.Open(path)
				if openErr != nil {
					return nil
				}
				defer f.Close()
				decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
				for {
					var event map[string]any
					if decoder.Decode(&event) != nil {
						break
					}
					if event["status"] == "failed" || event["status"] == "incomplete" {
						t.Logf("diagnostic stage=%v status=%v reason=%v", event["stage"], event["status"], event["reason_code"])
					}
				}
				return nil
			})
			t.Fatalf("Gateway %s 통신 실패; stderr bytes=%d", kind, stderr.Len())
		}
		if response.Error != "" {
			t.Fatalf("Gateway %s 거부: %s", kind, response.Error)
		}
		return response
	}
	if response := call(diagnosis.Validate); response.Snapshot.State != diagnosis.Prepared {
		t.Fatal("prepared 누락")
	}
	if hits.Load() != 0 || modelCalls.Load() != 0 {
		t.Fatal("시작 전 송신")
	}
	call(diagnosis.Start)
	call(diagnosis.Start)
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for {
		response := call(diagnosis.Status)
		if response.Snapshot.State == diagnosis.Completed {
			break
		}
		if response.Snapshot.State == diagnosis.Failed || response.Snapshot.State == diagnosis.Unknown {
			t.Fatalf("독립 실행 종료=%s drain=%v result=%v", response.Snapshot.State, response.Snapshot.DrainComplete, response.Snapshot.ResultReady)
		}
		select {
		case <-deadline.C:
			t.Fatal("독립 실행 시간 초과")
		case <-time.After(20 * time.Millisecond):
		}
	}
	result := call(diagnosis.Result)
	if !result.Snapshot.DrainComplete || !result.Snapshot.ResultReady || hits.Load() != 1 {
		t.Fatal("마감/실제 수신 불일치")
	}
	var candidates, confirmed, runs int
	if installer.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND origin='e2_diagnostic_candidate_v1'", f.explorationID).Scan(&candidates) != nil {
		t.Fatal("후보 조회 실패")
	}
	if installer.QueryRowContext(ctx, "SELECT count(*) FROM findings WHERE task_id=$1", mustFirstDiagnosisTaskNumber(f.taskID)).Scan(&confirmed) != nil {
		t.Fatal("finding 조회 실패")
	}
	if installer.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2", f.explorationID, diagnosisRunOrigin).Scan(&runs) != nil {
		t.Fatal("마감 조회 실패")
	}
	if candidates != 1 || confirmed != 0 || runs != 1 {
		t.Fatalf("후보=%d finding=%d 저장마감=%d", candidates, confirmed, runs)
	}
	_ = stdin.Close()
	if err = command.Wait(); err != nil {
		t.Fatalf("Gateway 프로세스 마감 실패; stderr bytes=%d", stderr.Len())
	}
	t.Logf("actual child process=%d; target receipts=%d; synthetic model requests=%d; candidates=%d; confirmed=%d; saved runs=%d", command.Process.Pid, hits.Load(), modelCalls.Load(), candidates, confirmed, runs)
}

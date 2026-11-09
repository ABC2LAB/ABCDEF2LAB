//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisapi"
)

// 테스트 운영자만 보호 승인 파일을 갱신한다. 브라우저는 실제 공개 API만 호출한다.
func TestFirstDiagnosisManagedBrowserIndependentProcessesOwnedPostgres(t *testing.T) {
	if os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE") == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT") == "" {
		t.Skip("NOT_RUN: 설치된 브라우저와 Node 드라이버 경로가 필요함")
	}
	f := prepareFirstDiagnosisGatewayProcessFixture(t)
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	responseRead, responseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []*os.File{commandRead, commandWrite, responseRead, responseWrite} {
		t.Cleanup(func() { _ = file.Close() })
	}
	gateway := exec.CommandContext(f.ctx, f.binary, "gateway", "-config", f.launchPath, "-preparation-ref", "owned-process-preparation")
	gateway.Stdin, gateway.Stdout, gateway.Stderr = commandRead, responseWrite, io.Discard
	if gateway.Start() != nil {
		t.Fatal("gateway 자식 기동 실패")
	}
	t.Cleanup(func() { _ = gateway.Process.Kill(); _ = gateway.Wait() })
	_ = commandRead.Close()
	_ = responseWrite.Close()
	registryPath := filepath.Join(f.root, "management-registry.json")
	profile := diagnosisapi.Profile{ID: "owned-loopback", Label: "소유 loopback 합성 진단", Configuration: diagnosisapi.Configuration{TargetRef: "owned-loopback", ScopeRef: "allowed-get", ModelProfileRef: "local-synthetic", ApprovalMode: "preauthorized_scope", Budget: diagnosisapi.Budget{Requests: 2, Tokens: 10000, CostUSDMicros: 0, DurationSeconds: 60}}}
	registry := map[string]any{"format": "abc2lab.diagnosis-management-registry/v1", "profiles": []diagnosisapi.Profile{profile}, "approvals": []any{}}
	writeRegistry := func() {
		t.Helper()
		raw, _ := json.Marshal(registry)
		if os.WriteFile(registryPath, raw, 0600) != nil {
			t.Fatal("보호 registry 갱신 실패")
		}
	}
	writeRegistry()
	raw, err := os.ReadFile(f.launchPath)
	if err != nil {
		t.Fatal(err)
	}
	var launch map[string]any
	if json.Unmarshal(raw, &launch) != nil {
		t.Fatal("launch fixture 디코딩 실패")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	managementAddr := listener.Addr().String()
	_ = listener.Close()
	launch["listen_addr"] = managementAddr
	managementPath := filepath.Join(f.root, "management-launch.json")
	raw, _ = json.Marshal(launch)
	if os.WriteFile(managementPath, raw, 0600) != nil {
		t.Fatal("관리 launch 준비 실패")
	}
	manage := exec.CommandContext(f.ctx, f.binary, "manage", "-config", managementPath, "-registry", registryPath, "-gateway-read-fd", "3", "-gateway-write-fd", "4", "-runtime-api-origin", "http://"+f.addr)
	manage.ExtraFiles = []*os.File{responseRead, commandWrite}
	manage.Stdout = io.Discard
	manage.Stderr = io.Discard
	if manage.Start() != nil {
		t.Fatal("manage 자식 기동 실패")
	}
	t.Cleanup(func() { _ = manage.Process.Signal(syscall.SIGTERM); _ = manage.Wait() })
	_ = responseRead.Close()
	_ = commandWrite.Close()
	waitFirstDiagnosisProcessHTTP(t, f.ctx, "http://"+managementAddr)
	browser := startFirstDiagnosisBrowserFixture(t, f.ctx, "http://"+managementAddr, f.taskID)
	result, err := browser.result("management-draft")
	if err != nil {
		t.Fatal("실제 브라우저 초안 생성 실패")
	}
	var observation struct {
		DraftBinding struct {
			ID           string `json:"id"`
			Revision     uint64 `json:"revision"`
			SourceDigest string `json:"source_digest"`
		} `json:"draftBinding"`
	}
	if json.Unmarshal(result, &observation) != nil || observation.DraftBinding.ID == "" {
		t.Fatal("브라우저 초안 결속 누락")
	}
	digest, err := diagnosisapi.ConfigurationDigest(profile.Configuration)
	if err != nil || digest != observation.DraftBinding.SourceDigest {
		t.Fatal("브라우저 설정과 운영자 profile 불일치")
	}
	if f.hits.Load() != 0 || f.modelCalls.Load() != 0 {
		t.Fatal("승인 전 모델·대상 송신")
	}
	binding := observation.DraftBinding
	registry["approvals"] = []any{map[string]any{"principal": "ARTEX", "draft_id": binding.ID, "revision": binding.Revision, "source_digest": binding.SourceDigest, "task_id": f.taskID, "preparation_ref": "owned-process-preparation", "expires_at": time.Now().Add(5 * time.Minute)}}
	writeRegistry()
	browser.run("management-start")
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		var runs int
		err = f.installer.QueryRowContext(f.ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2", f.explorationID, diagnosisRunOrigin).Scan(&runs)
		if err != nil {
			t.Fatal("저장 마감 조회 실패")
		}
		if runs == 1 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("독립 실행 저장 마감 시간 초과")
		case <-time.After(50 * time.Millisecond):
		}
	}
	closed := false
	for limit := time.Now().Add(10 * time.Second); time.Now().Before(limit); {
		conn, dialErr := net.DialTimeout("tcp", f.addr, 200*time.Millisecond)
		if dialErr != nil {
			closed = true
			break
		}
		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	if !closed {
		t.Fatal("gateway runtime HTTP 마감 미확인")
	}
	// gateway가 실행 자원을 닫은 후 기존 읽기 전용 CLI를 같은 고정 포트에 올린다.
	report := exec.CommandContext(f.ctx, f.binary, "report", "-config", f.launchPath, "-task-id", f.taskID)
	report.Stdout = io.Discard
	report.Stderr = io.Discard
	if report.Start() != nil {
		t.Fatal("저장 보고서 자식 기동 실패")
	}
	t.Cleanup(func() { _ = report.Process.Signal(syscall.SIGTERM); _ = report.Wait() })
	waitFirstDiagnosisProcessHTTP(t, f.ctx, "http://"+f.addr)
	browser.run("management-report")
	var candidates, confirmed int
	if f.installer.QueryRowContext(f.ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND origin='e2_diagnostic_candidate_v1'", f.explorationID).Scan(&candidates) != nil || f.installer.QueryRowContext(f.ctx, "SELECT count(*) FROM findings WHERE task_id=$1", mustFirstDiagnosisTaskNumber(f.taskID)).Scan(&confirmed) != nil {
		t.Fatal("결과 집계 실패")
	}
	if candidates != 1 || confirmed != 0 || f.hits.Load() != 1 {
		t.Fatalf("후보=%d 확정=%d 수신=%d", candidates, confirmed, f.hits.Load())
	}
	t.Logf("actual gateway/manage/report processes; browser draft+approval+start+report; candidates=%d confirmed=%d receipts=%d", candidates, confirmed, f.hits.Load())
}
func waitFirstDiagnosisProcessHTTP(t *testing.T, ctx context.Context, origin string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", origin+"/api/auth/status", nil)
		response, err := client.Do(req)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("실제 자식 HTTP 준비 시간 초과")
		case <-ctx.Done():
			t.Fatal("실제 자식 준비 취소")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

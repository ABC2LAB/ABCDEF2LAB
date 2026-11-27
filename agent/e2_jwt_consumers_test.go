package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// 합성 provider·도구 관찰로 실제 Worker 활동 기록 → 후보 DB → 내부/공유 보고서 소비 경계를 검사한다.
// 실제 모델 탐지나 대상 HTTP·샌드박스 송신 시험으로 계산하지 않는다.
// 시험 계정 실행의 세션 값(JWT 조각 포함)은 활동 기록·후보에 가리지 않고 남는다(절대규칙 2, 2026-11-26 DLP 폐기). 같은 길을
// 지나는 우리 제어 비밀(설정한 보호 값)은 활동 기록·후보·보고서 어디에도 남지 않는다.
func TestSessionValuesKeptAndControlSecretMaskedThroughWorkerCandidateReportsOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("일회용 PostgreSQL DSN 없음")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	task, err := pg.CreateTask("합성 JWT 기록 경계", "합성 관찰만", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
	defer pg.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	store := pg.Exploration(task.ExplorationID)
	id, err := store.AddIntent(map[string]any{"summary": "합성 관찰"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	execution, _, binding := e2WorkerFixtureTargetMode(t, "https://owned.invalid", "/", e2proxy.HeaderTestAccountPassthrough, task.ID, id)
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encode([]byte(`{"sub":"tester-a","role":"synthetic-only"}`))
	mac := hmac.New(sha256.New, []byte("synthetic-consumer-signing-key"))
	mac.Write([]byte(unsigned))
	token := unsigned + "." + encode(mac.Sum(nil))
	if execution.AddRedactValues([]byte(token)) != nil {
		t.Fatal("합성 세션 등록 실패")
	}
	const control = "synthetic-control-secret-0123456789-abcdef"
	if err := execution.AddProtectedValues([]byte(control)); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	command := "printf '" + parts[1] + " " + parts[1] + " " + parts[2] + " " + control + "'"
	provider := &e2SequenceProvider{commands: []string{command}}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 4)
	state := &e2HTTPToolState{}
	run := func(context.Context, string) (e2runner.Result, error) {
		return e2runner.Result{ToolOutput: "ordinary observed body " + parts[1] + " " + parts[2] + " leaked " + control, Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(id), Call: e2proxy.CallResult{IntentID: id, CallRef: "synthetic-jwt-call", Final: true, AuditComplete: true}, Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed"}}, nil
	}
	bash := newE2HTTPBashTool(execution, run, time.Second, state, binding.Control, json.RawMessage(`{"summary":"synthetic"}`), id)
	count := 0
	emit := func(activity db.Activity) {
		count++
		if _, err := store.AppendActivity(activity); err != nil {
			t.Error("실제 활동 저장 실패")
		}
	}
	opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{bash}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 4, MaxDuration: 5 * time.Second, NonStreaming: true}
	if _, _, err := worker.runE2HTTPSession(t.Context(), "worker", &db.Node{ID: id, Kind: db.KindIntent}, emit, opts, "assigned synthetic intent", execution, state, nil); err != nil {
		t.Fatal("실제 Worker 활동 경로 실패", err)
	}
	if count == 0 {
		t.Fatal("활동 기록이 실행되지 않음")
	}
	input, _ := json.Marshal(map[string]string{"call_ref": "synthetic-jwt-call", "vulnclass": "IDOR", "name": "합성 검토 후보", "summary": "관찰 " + parts[1] + " " + parts[2] + " " + control})
	submit := newE2DiagnosticCandidateTool(store, binding, state)
	result, err := submit.Call(t.Context(), input, nil)
	if err != nil || result.IsError {
		t.Fatal("후보 저장 경로 실패")
	}
	candidates, err := store.ListDiagnosticCandidates(task.ID, binding.RunRef, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatal("후보 DB 조회 실패")
	}
	assertNoControl := func(raw []byte) {
		t.Helper()
		if strings.Contains(string(raw), control) {
			t.Fatal("제어 비밀이 저장·출력 경계에 남음")
		}
	}
	raw, _ := json.Marshal(candidates)
	assertNoControl(raw)
	for _, kept := range []string{parts[1], parts[2]} {
		if !strings.Contains(candidates[0].Summary, kept) || !strings.Contains(candidates[0].Evidence, kept) {
			t.Fatal("시험 계정 세션 값을 후보에서 가림")
		}
	}
	activities, _, err := store.ActivityList(&id, 0, 100)
	if err != nil || len(activities) == 0 {
		t.Fatal("활동 DB 조회 실패")
	}
	sessionKept := false
	for _, activity := range activities {
		raw, _ := json.Marshal(activity)
		assertNoControl(raw)
		detail, err := store.ActivityDetailWithSources(activity.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertNoControl([]byte(detail))
		sessionKept = sessionKept || strings.Contains(string(raw)+detail, parts[1])
	}
	if !sessionKept {
		t.Fatal("시험 계정 세션 값을 활동 기록에서 가림")
	}
	candidate := candidates[0]
	for _, shared := range []bool{false, true} {
		doc := report.DiagnosticReport{TaskID: task.ID, GeneratedAt: time.Now().UTC(), Shared: shared, Entries: []report.DiagnosticEntry{{ID: fmt.Sprintf("candidate-%d", candidate.ID), Kind: "candidate", Run: candidate.RunRef, IntentID: id, Title: candidate.Name, VulnClass: candidate.VulnClass, Verification: candidate.Verification, Outcome: "review", Summary: candidate.Summary, Evidence: candidate.Evidence, EvidenceVersion: candidate.EvidenceVersion, Digest: candidate.EvidenceDigest, ObservedAt: candidate.ObservedAt}}}
		html, err := report.DiagnosticHTML(doc)
		if err != nil {
			t.Fatal(err)
		}
		assertNoControl(html)
		if !shared && !strings.Contains(string(html), "ordinary observed body") {
			t.Fatal("비민감 관찰을 통째로 제거함")
		}
	}
}

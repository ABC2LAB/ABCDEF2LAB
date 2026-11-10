//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"golang.org/x/crypto/bcrypt"
)

// 실제 PG16의 독립 LOGIN·held FD·production 기동·원본 Engine을 통과한다.
// 설치 입력/모델/대상은 소유한 합성 fixture이며 실제 Juice Shop/LLM 수용 근거가 아니다.
func TestFirstDiagnosisRegisteredProductionThroughStoredFinding(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("M01 비특권 운영자 UID가 필요함")
	}
	f := newControlledLocalDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(f.Context, 90*time.Second)
	defer cancel()
	b := localBindingFixture(t)
	base := filepath.Dir(b.State.Path)
	mkdir := func(name string) string {
		t.Helper()
		path := filepath.Join(base, name)
		if os.Mkdir(path, 0700) != nil {
			t.Fatal("소유 시험 디렉터리 생성 실패")
		}
		return path
	}
	dataDir, skillDir, keyDir, snapshotDir := mkdir("data"), mkdir("skills"), mkdir("auth"), mkdir("snapshots")
	installer := &pgdb.DB{DB: f.Installer}
	manifest := firstDiagnosisSupervisorModelManifest(t, installer)
	profile, err := installer.ActiveProfile()
	if err != nil || profile == nil || profile.ID <= 0 {
		t.Fatal("실제 설치된 모델 profile ID를 읽지 못함")
	}
	profileID := profile.ID
	password := "synthetic-registered-owned-password"
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(passwordHash)) != nil {
		t.Fatal("소유 설치자의 기존 인증 자료 준비 실패")
	}
	manager, err := NewFirstDiagnosisManager(ctx, dataDir, f.Runtime)
	if err != nil {
		t.Fatal("실제 최소 권한 runtime Manager 생성 실패")
	}
	defer func() {
		if manager.Close() != nil {
			t.Error("소유 Manager 종료 실패")
		}
	}()
	task, err := manager.CreateTask("Owned registered production fixture", "Verify one frozen owner/cross observation", &profileID, 120, 60)
	if err != nil || task == nil || task.Store == nil {
		t.Fatal("실제 task 생성 실패")
	}
	var chainCount int
	var chainID, taskProfile, activeProfile int64
	if err := manager.pg.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(p.profile_id),0),t.llm_profile_id,t.active_llm_profile_id FROM tasks t JOIN task_llm_profiles p ON p.task_id=t.id WHERE t.id=$1 GROUP BY t.llm_profile_id,t.active_llm_profile_id`, task.ID).Scan(&chainCount, &chainID, &taskProfile, &activeProfile); err != nil || chainCount != 1 || chainID != profileID || taskProfile != profileID || activeProfile != profileID {
		t.Fatal("실제 task의 단일 profile 체인을 명시적으로 결속하지 못함")
	}
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "Perform the registered peer read for the owned document."}, 10, nil, "human")
	if err != nil || intentID <= 0 {
		t.Fatal("실제 task의 intent 생성 실패")
	}
	if _, err = installer.ExecContext(ctx, "UPDATE tasks SET paused=true,queued=false WHERE id=$1", task.ID); err != nil {
		t.Fatal("준비 task의 정지 상태 저장 실패")
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true; state.Queued = false })
	b.Execution = ControlledReadLocalExecution{TaskID: mustFirstDiagnosisTaskNumber(task.ID), IntentID: intentID, CaseRef: "registered-owned-case", ResetRef: "registered-owned-reset", PolicyRef: "registered-owned-policy"}
	var hits, ownerHits, peerHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/document" || r.URL.RawQuery != "" {
			t.Error("등록 GET 밖의 요청이 대상에 도달함")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Header.Get("X-Session") {
		case runtimeSecret:
			ownerHits.Add(1)
		case "synthetic-peer-session":
			peerHits.Add(1)
		default:
			t.Error("동결한 owner/cross 세션이 아님")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","data":{"note":%q,"private":%q}}`, verificationTestMarker, runtimeSecret)
	}))
	defer target.Close()
	expires := time.Now().UTC().Add(10 * time.Minute)
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone, HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: time.Now().UTC().Add(-time.Minute), ValidUntil: expires}
	registry := map[string]any{"format": kgfixture.FormatV1, "snapshot_id": "registered-owned-kg", "run_id": "registered-owned-preparation", "reset_generation": b.Execution.ResetRef, "generation_ref": "registered-owned-generation", "instance_ref": "registered-owned-instance", "principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "peer"}}, "resources": []any{map[string]any{"resource_ref": "doc:one", "owner_ref": "owner", "policies": []any{map[string]any{"evaluated_ref": "peer", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none", "valid_from": policy.ValidFrom.Format(time.RFC3339Nano), "valid_until": policy.ValidUntil.Format(time.RFC3339Nano)}}, "canaries": []any{map[string]any{"field": "note", "value": verificationTestMarker, "trivial": false}}}}}
	registryBytes := kgBindingJSON(t, registry)
	frozen, err := kgfixture.Parse(registryBytes)
	if err != nil {
		t.Fatal("소유 KG fixture 파싱 실패")
	}
	owner := idor.AccountRef{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified}
	peer := idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified}
	expected := evidencebind.Context{Scope: transmit.Scope{TaskRef: task.ID, CaseRef: b.Execution.CaseRef, ResetRef: b.Execution.ResetRef, PolicyRef: b.Execution.PolicyRef}, IntentID: intentID, ResourceRef: "doc:one", ReadApprovalRef: "read-approval", KGPolicyVersion: frozen.PolicyVersion(), Owner: owner, Evaluated: peer}
	source := e3record.Source{Expected: expected, ExplorationID: task.ExpID, KGSnapshotRef: frozen.Ref(), KGSnapshotDigest: frozen.Digest(), GenerationRef: frozen.GenerationRef(), InstanceRef: frozen.InstanceRef(), ResetGeneration: frozen.ResetGeneration(), ModelTag: "synthetic-registered-model", Policy: policy, CanaryFields: []string{"note"}, TrafficEvidenceIDs: []int64{}, Severity: "medium"}
	metadata := ControlledReadAssemblyMetadata{Format: ControlledReadAssemblyMetadataFormat, SnapshotRef: "approved", TargetOrigin: target.URL, Source: source}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal("소유 metadata 인코딩 실패")
	}
	snapshot := snapshotJSON{TaskID: task.ID, IntentID: intentID, CaseRef: b.Execution.CaseRef, ResetRef: b.Execution.ResetRef, PolicyRef: b.Execution.PolicyRef, Origin: target.URL, ConnectIP: "127.0.0.1", ExpiresAt: expires, MaxRequests: 2, MinIntervalMS: 500, RequestTimeoutMS: 5000, MaxBodyBytes: 4096, KeyRef: b.Key.KeyRef, KeyFile: "audit.key", Sessions: []sessionJSON{{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{"X-Session": "owner.secret"}}, {Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{"X-Session": "peer.secret"}}}, Resources: []resourceJSON{{Ref: "doc:one", URL: target.URL + "/document", ApprovalRef: "read-approval", ReadOnly: true, Accounts: []string{"owner", "peer"}, View: &transmit.JSONViewRule{StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"}, Markers: []idor.Marker{{Field: "note", Value: verificationTestMarker}}}}}}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal("소유 등록 snapshot 인코딩 실패")
	}
	keyBytes, err := os.ReadFile(b.Key.File)
	if err != nil {
		t.Fatal("소유 감사키 fixture 읽기 실패")
	}
	defer clear(keyBytes)
	metadataFile := filepath.Join(base, "inputs", "metadata.json")
	pins := []struct {
		path string
		body []byte
	}{{filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-registered-jwt-material")}, {metadataFile, metadataBytes}, {filepath.Join(snapshotDir, "approved.json"), snapshotBytes}, {filepath.Join(snapshotDir, "registry.json"), registryBytes}, {filepath.Join(snapshotDir, "audit.key"), keyBytes}, {filepath.Join(snapshotDir, "owner.secret"), []byte(runtimeSecret)}, {filepath.Join(snapshotDir, "peer.secret"), []byte("synthetic-peer-session")}}
	b.Files = nil
	for _, pin := range pins {
		if os.WriteFile(pin.path, pin.body, 0600) != nil {
			t.Fatal("보호 입력 fixture 저장 실패")
		}
		digest := sha256.Sum256(pin.body)
		b.Files = append(b.Files, ControlledReadLocalFile{Path: pin.path, SHA256: hex.EncodeToString(digest[:])})
	}
	modelBytes, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal("실제 모델 profile 계약 fixture 읽기 실패")
	}
	modelDigest := sha256.Sum256(modelBytes)
	b.Files = append(b.Files, ControlledReadLocalFile{Path: manifest, SHA256: hex.EncodeToString(modelDigest[:])})
	b.PolicySHA256 = []string{}
	bindingFile := filepath.Join(base, "inputs", "host.json")
	f.InstallBinding(t, b, bindingFile)
	supervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		t.Fatal("실제 등록 Supervisor 생성 실패")
	}
	runtime, err := StartFirstDiagnosisControlled(ctx, manager, f.Authority, FirstDiagnosisControlledStartOptions{BindingFile: bindingFile, MetadataFile: metadataFile, SnapshotDir: snapshotDir, StateDir: b.State.Path, RegistryFile: "registry.json", SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir, LocalLLMManifestPath: manifest, Supervisor: supervisor, Shutdown: supervisor.Shutdown(), CleanupTimeout: 10 * time.Second})
	if err != nil || runtime == nil {
		if runtime != nil {
			cleanup, finish := context.WithTimeout(context.Background(), 10*time.Second)
			_ = runtime.Close(cleanup)
			finish()
		}
		t.Fatalf("실제 등록 production 기동 실패: %v", err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := runtime.Close(closeCtx); err != nil {
			t.Error("실제 등록 production 최종 정리 실패")
		}
	}()
	if runtime.TaskID() != b.Execution.TaskID || runtime.IntentID() != intentID || runtime.SnapshotRef() != "approved" || runtime.local.runtimeDB != manager.pg.DB {
		t.Fatal("기동점이 검증된 실제 task/intent/DB를 바꿈")
	}
	model := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	// SDK provider만 test-only로 교체한다. 원본 Planner/Worker/taskLLMRuntime.current의
	// 실제 단일 profile/role binding guard와 승인된 cfg tuple은 그대로 통과한다.
	sdkConfig, configured := runtime.assembly.server.loadProfileConfig(profileID)
	if !configured {
		t.Fatal("실제 단일 task profile 설정을 다시 읽지 못함")
	}
	runtime.assembly.server.provCacheMu.Lock()
	runtime.assembly.server.provByProfile[profileID] = &provEntry{prov: model, cfg: sdkConfig}
	runtime.assembly.server.provCacheMu.Unlock()
	if err := supervisor.Run(ctx, task.ID, intentID, "approved"); err != nil {
		node, _ := task.Store.GetNode(intentID)
		state := "missing"
		if node != nil {
			state = node.State
		}
		t.Fatalf("실제 등록 production 실행 실패: target=%d owner=%d peer=%d model_calls=%d intent=%s cause=%v", hits.Load(), ownerHits.Load(), peerHits.Load(), model.calls, state, err)
	}
	if hits.Load() != 2 || ownerHits.Load() != 1 || peerHits.Load() != 1 || model.calls != 2 {
		t.Fatal("같은 실행의 owner/cross 2 GET 또는 결정적 Worker 결속 위반")
	}
	firstDiagnosisRegisteredStored(t, ctx, manager, runtime, source, keyBytes)
	// 소유자 GET은 정상 대조다. 교차 주체의 판정 대상 셀로 기록하지 않는다.
	projection, err := readAccessMap(ctx, manager.pg.DB, runtime.TaskID())
	if err != nil {
		t.Fatal("정상 검증 뒤 지도 조회 실패", err)
	}
	ownerCell, peerCell := accessMapFind(t, projection, "owner"), accessMapFind(t, projection, "peer")
	if ownerCell.State != "blank" || ownerCell.ProjectionError != nil || ownerCell.RecordID != nil {
		t.Fatal("소유자 정상 대조를 판정 기록 누락으로 표시함")
	}
	if peerCell.State != "confirmed" || peerCell.ProjectionError != nil || peerCell.RecordID == nil {
		reason := "none"
		if peerCell.ProjectionError != nil {
			reason = *peerCell.ProjectionError
		}
		t.Fatalf("교차 계정 저장 판정 지도 연결 실패: state=%s reason=%s", peerCell.State, reason)
	}
	firstDiagnosisRegisteredHTTP(t, runtime, password)
	closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := runtime.Close(closeCtx); err != nil || !runtime.ResourcesClosed() {
		t.Fatal("실제 등록 마감 뒤 권위/FD/서버 반환을 확인하지 못함")
	}
	if err := supervisor.Run(closeCtx, task.ID, intentID, "approved"); err == nil {
		t.Fatal("마감한 회차의 등록 권한이 재사용됨")
	}
	if hits.Load() != 2 {
		t.Fatal("마감 뒤 신규 대상 송신 발생")
	}
}

func firstDiagnosisRegisteredStored(t *testing.T, ctx context.Context, m *Manager, r *FirstDiagnosisControlledRuntime, want e3record.Source, key []byte) {
	t.Helper()
	var findings, issuances, revisions int
	if err := m.pg.QueryRowContext(ctx, "SELECT count(*) FROM findings WHERE task_id=$1", r.TaskID()).Scan(&findings); err != nil {
		t.Fatal("저장 finding 조회 실패")
	}
	if err := m.pg.QueryRowContext(ctx, "SELECT count(*) FROM e3_verdict_issuances WHERE task_id=$1", r.TaskID()).Scan(&issuances); err != nil {
		t.Fatal("실제 E③ issuance 조회 실패")
	}
	if err := m.pg.QueryRowContext(ctx, "SELECT count(*) FROM evidence_revision_initials WHERE task_id=$1 AND intent_id=$2", r.TaskID(), r.IntentID()).Scan(&revisions); err != nil {
		t.Fatal("실제 초기 revision 조회 실패")
	}
	if findings != 1 || issuances != 1 || revisions != 1 {
		t.Fatalf("같은 실행 저장 결과 부족: finding=%d issuance=%d initial_revision=%d", findings, issuances, revisions)
	}
	var binding, payload, manifest []byte
	var verdict string
	if err := m.pg.QueryRowContext(ctx, "SELECT i.binding_canonical,v.payload_canonical,m.canonical,v.oracle_value FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.task_id=$1", r.TaskID()).Scan(&binding, &payload, &manifest, &verdict); err != nil {
		t.Fatal("저장 E③ 결속 읽기 실패")
	}
	var got e3record.Source
	if json.Unmarshal(binding, &got) != nil || verdict != "confirmed" || got.Expected.IntentID != want.Expected.IntentID || got.Expected.Scope.TaskRef != want.Expected.Scope.TaskRef || got.Expected.Scope.RunRef == "" || got.Expected.Scope.ResetRef != want.Expected.Scope.ResetRef || got.KGSnapshotRef != want.KGSnapshotRef || got.KGSnapshotDigest != want.KGSnapshotDigest || got.GenerationRef != want.GenerationRef || got.InstanceRef != want.InstanceRef || got.ResetGeneration != want.ResetGeneration || got.EvidenceRevision == 0 || got.FinalCheckpointSequence == 0 || !got.Completed || !got.Finalized || got.EvalRunID != "" {
		t.Fatal("실제 Source/KG/Config/마감/revision 저장 결속 불일치")
	}

	expected := want.Expected
	expected.Scope.RunRef = got.Expected.Scope.RunRef
	if got.Expected != expected || got.FinalCheckpointScope != expected.Scope || got.FinalCheckpointRef == "" || got.EvidenceDigest == ([32]byte{}) || got.RecordCreatedAt.IsZero() || got.ModelTag != want.ModelTag || !sameControlledPreparationPolicy(got.Policy, want.Policy) {
		t.Fatal("저장된 전체 Context·정책·수집기 사실이 같은 실행에 결속되지 않음")
	}
	var consistent int
	if err := m.pg.QueryRowContext(ctx, `SELECT count(*) FROM evidence_revision_initials r
 JOIN e3_evidence_manifests m ON m.task_id=r.task_id AND m.execution_ref=r.execution_ref AND m.evidence_revision=r.revision
 JOIN e3_verdict_issuances i ON i.manifest_id=m.id AND i.task_id=r.task_id AND i.exploration_id=r.exploration_id AND i.intent_id=r.intent_id AND i.execution_ref=r.execution_ref
 JOIN e3_verdict_links l ON l.issuance_id=i.id AND l.task_id=i.task_id
 JOIN findings f ON f.id=l.finding_id AND f.task_id=l.task_id AND f.evidence_version=l.evidence_version
 JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=i.exploration_id AND n.kind='finding' AND n.state='confirmed'
 WHERE r.task_id=$1 AND r.intent_id=$2 AND r.execution_ref=$3 AND r.revision=$4 AND i.eval_run_id IS NULL`, r.TaskID(), r.IntentID(), got.Expected.Scope.RunRef, got.EvidenceRevision).Scan(&consistent); err != nil || consistent != 1 {
		t.Fatal("초기 revision·issuance·confirmed finding/node가 같은 실행에 속하지 않음")
	}
	task, ok := m.Task(strconv.FormatInt(r.TaskID(), 10))
	if !ok {
		t.Fatal("저장된 실제 task 수명 소유 누락")
	}
	intent, err := task.Store.GetNode(r.IntentID())
	if err != nil || intent == nil || intent.State != "done" || !task.lifecycleSnapshot().Paused {
		t.Fatal("판정 저장과 실제 intent 완료·task 정지가 일치하지 않음")
	}
	for _, body := range [][]byte{binding, payload, manifest} {
		for _, private := range []string{runtimeSecret, verificationTestMarker, "synthetic-peer-session"} {
			if bytes.Contains(body, []byte(private)) {
				t.Fatal("원문 세션/관찰값이 저장 근거에 노출됨")
			}
		}
	}
	paths, err := filepath.Glob(filepath.Join(r.local.binding.Journals.Path, "*.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatal("동일 실행의 실제 감사 정본이 없거나 중복됨")
	}
	journal, err := os.Open(paths[0])
	if err != nil {
		t.Fatal("실제 감사 정본 열기 실패")
	}
	defer journal.Close()
	head, err := r.local.AuditSourceOptions().LatestCheckpoint(ctx, got.Expected.Scope)
	if err != nil {
		t.Fatal("실제 DB ACK의 최종 봉인 head 읽기 실패")
	}
	digest, digestErr := ControlledReadCheckpointDigest(head.Checkpoint)
	if digestErr != nil || digest != got.FinalCheckpointDigest || head.Ref != got.FinalCheckpointRef {
		t.Fatal("최종 Source의 checkpoint 참조·digest가 실제 봉인 ACK와 다름")
	}
	summary, err := auditlog.Verify(ctx, journal, key, head.Checkpoint)
	if err != nil || summary.Intents != 2 || summary.Results != 2 || head.Checkpoint.Scope != got.Expected.Scope || head.Checkpoint.Sequence != got.FinalCheckpointSequence {
		t.Fatal("실제 감사 MAC/SQL ACK와 2 GET·최종 Source가 같은 회차가 아님")
	}
}

func firstDiagnosisRegisteredHTTP(t *testing.T, r *FirstDiagnosisControlledRuntime, password string) {
	t.Helper()
	handler := r.Handler()
	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"ABC2LAB","password":"`+password+`"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	var auth struct {
		Token string `json:"token"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &auth) != nil || auth.Token == "" {
		t.Fatal("실제 기존 인증 경로 실패")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/exploration/findings?task_id="+strconv.FormatInt(r.TaskID(), 10), nil)
	request.Header.Set("Authorization", "Bearer "+auth.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"finding_id"`)) {
		t.Fatalf("실제 기존 결과 조회 실패: status=%d", response.Code)
	}
	for _, private := range []string{runtimeSecret, verificationTestMarker, "synthetic-peer-session"} {
		if bytes.Contains(response.Body.Bytes(), []byte(private)) {
			t.Fatal("기존 결과 조회에 원문 비밀/관찰값 노출")
		}
	}
}

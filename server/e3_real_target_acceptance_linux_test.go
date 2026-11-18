//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	"golang.org/x/crypto/bcrypt"
)

// realTargetSpec는 실제 대상 등록 GET 회차의 보호 입력이다(0600 파일, 저장소 밖). 대상 주소·경로·응답 판정 규칙은
// 코드가 아니라 이 파일이 정한다. 세션 헤더 값과 표식 값은 따로 0600 파일로 받고 출력하지 않는다.
type realTargetSpec struct {
	Origin          string   `json:"origin"`
	ConnectIP       string   `json:"connect_ip"`
	ResourceRef     string   `json:"resource_ref"`
	ResourcePath    string   `json:"resource_path"`
	HeaderName      string   `json:"header_name"`
	OwnerHeaderFile string   `json:"owner_header_file"`
	PeerHeaderFile  string   `json:"peer_header_file"`
	CanaryFile      string   `json:"canary_file"`
	CanaryField     string   `json:"canary_field"`
	StatusPath      []string `json:"status_path"`
	NormalValue     string   `json:"normal_value"`
	ErrorValues     []string `json:"error_values"`
	DataPath        []string `json:"data_path"`
	MaxBodyBytes    int64    `json:"max_body_bytes"`
}

func readRealTargetPrivate(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("실제 대상 보호 입력은 0600 일반 파일이어야 함")
	}
	body, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		t.Fatal("실제 대상 보호 입력 읽기 실패")
	}
	return bytes.TrimSpace(body)
}

// 실제 대상·실제 시험 계정 세션으로 등록 GET 확정 경로를 한 번 돈다(E③ 대체 경로 시연, 사용자 결정 2026-11-17).
// 실제 PG16·production 기동(StartFirstDiagnosisControlled)·실제 Supervisor·원본 Engine·등록 GET 전송기·E③ 저장을
// 그대로 쓰고, 모델 자리만 각본 provider(유료 호출 없음)로 바꾼다. 대상 준비(시험 계정·표식 심기)는 이 시험 밖의
// 준비 쓰기이고, 이 시험은 GET 2건(소유자 정상 대조·교차 계정)만 보낸다. 출력은 건수·상태만 남긴다.
func TestE3RegisteredGETRealTargetConfirmed(t *testing.T) {
	specPath := os.Getenv("ARTEX_E3_REAL_TARGET_SPEC")
	if specPath == "" {
		t.Skip("실제 대상 보호 입력(ARTEX_E3_REAL_TARGET_SPEC)이 없음")
	}
	if os.Geteuid() == 0 {
		t.Skip("M01 비특권 운영자 UID가 필요함")
	}
	var spec realTargetSpec
	if json.Unmarshal(readRealTargetPrivate(t, specPath), &spec) != nil || spec.Origin == "" || spec.ConnectIP == "" || spec.ResourceRef == "" || !strings.HasPrefix(spec.ResourcePath, "/") || spec.HeaderName == "" || spec.CanaryField == "" || len(spec.StatusPath) == 0 || spec.NormalValue == "" || spec.MaxBodyBytes <= 0 {
		t.Fatal("실제 대상 보호 입력 형식 오류")
	}
	ownerHeader, peerHeader := readRealTargetPrivate(t, spec.OwnerHeaderFile), readRealTargetPrivate(t, spec.PeerHeaderFile)
	canary := string(readRealTargetPrivate(t, spec.CanaryFile))
	defer clear(ownerHeader)
	defer clear(peerHeader)

	f := newControlledLocalDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(f.Context, 120*time.Second)
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
	loginSecret := make([]byte, 16)
	if _, err = rand.Read(loginSecret); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(loginSecret)
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
	task, err := manager.CreateTask("Registered GET real target", "Verify one frozen owner/cross observation on the approved practice target", &profileID, 120, 60)
	if err != nil || task == nil || task.Store == nil {
		t.Fatal("실제 task 생성 실패")
	}
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "Perform the registered peer read for the owned resource."}, 10, nil, "human")
	if err != nil || intentID <= 0 {
		t.Fatal("실제 task의 intent 생성 실패")
	}
	if _, err = installer.ExecContext(ctx, "UPDATE tasks SET paused=true,queued=false WHERE id=$1", task.ID); err != nil {
		t.Fatal("준비 task의 정지 상태 저장 실패")
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true; state.Queued = false })
	b.Execution = ControlledReadLocalExecution{TaskID: mustFirstDiagnosisTaskNumber(task.ID), IntentID: intentID, CaseRef: "real-target-case", ResetRef: "real-target-reset", PolicyRef: "real-target-policy"}

	expires := time.Now().UTC().Add(10 * time.Minute)
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone, HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: time.Now().UTC().Add(-time.Minute), ValidUntil: expires}
	registry := map[string]any{"format": kgfixture.FormatV1, "snapshot_id": "real-target-kg", "run_id": "real-target-preparation", "reset_generation": b.Execution.ResetRef, "generation_ref": "real-target-generation", "instance_ref": "real-target-instance", "principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "peer"}}, "resources": []any{map[string]any{"resource_ref": spec.ResourceRef, "owner_ref": "owner", "policies": []any{map[string]any{"evaluated_ref": "peer", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none", "valid_from": policy.ValidFrom.Format(time.RFC3339Nano), "valid_until": policy.ValidUntil.Format(time.RFC3339Nano)}}, "canaries": []any{map[string]any{"field": spec.CanaryField, "value": canary, "trivial": false}}}}}
	registryBytes := kgBindingJSON(t, registry)
	frozen, err := kgfixture.Parse(registryBytes)
	if err != nil {
		t.Fatal("실제 대상 KG 레지스트리 파싱 실패")
	}
	owner := idor.AccountRef{Label: "owner", Handle: "session-owner", Status: oracle.SubjectVerified}
	peer := idor.AccountRef{Label: "peer", Handle: "session-peer", Status: oracle.SubjectVerified}
	expected := evidencebind.Context{Scope: transmit.Scope{TaskRef: task.ID, CaseRef: b.Execution.CaseRef, ResetRef: b.Execution.ResetRef, PolicyRef: b.Execution.PolicyRef}, IntentID: intentID, ResourceRef: spec.ResourceRef, ReadApprovalRef: "read-approval", KGPolicyVersion: frozen.PolicyVersion(), Owner: owner, Evaluated: peer}
	source := e3record.Source{Expected: expected, ExplorationID: task.ExpID, KGSnapshotRef: frozen.Ref(), KGSnapshotDigest: frozen.Digest(), GenerationRef: frozen.GenerationRef(), InstanceRef: frozen.InstanceRef(), ResetGeneration: frozen.ResetGeneration(), ModelTag: "scripted-registered-model", Policy: policy, CanaryFields: []string{spec.CanaryField}, TrafficEvidenceIDs: []int64{}, Severity: "medium"}
	metadata := ControlledReadAssemblyMetadata{Format: ControlledReadAssemblyMetadataFormat, SnapshotRef: "approved", TargetOrigin: spec.Origin, Source: source}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal("metadata 인코딩 실패")
	}
	view := &transmit.JSONViewRule{StatusPath: spec.StatusPath, NormalValue: spec.NormalValue, ErrorValues: spec.ErrorValues, DataPath: spec.DataPath, Markers: []idor.Marker{{Field: spec.CanaryField, Value: canary}}}
	snapshot := snapshotJSON{TaskID: task.ID, IntentID: intentID, CaseRef: b.Execution.CaseRef, ResetRef: b.Execution.ResetRef, PolicyRef: b.Execution.PolicyRef, Origin: spec.Origin, ConnectIP: spec.ConnectIP, ExpiresAt: expires, MaxRequests: 2, MinIntervalMS: 500, RequestTimeoutMS: 5000, MaxBodyBytes: spec.MaxBodyBytes, KeyRef: b.Key.KeyRef, KeyFile: "audit.key", Sessions: []sessionJSON{{Label: "owner", Handle: "session-owner", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{spec.HeaderName: "owner.secret"}}, {Label: "peer", Handle: "session-peer", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{spec.HeaderName: "peer.secret"}}}, Resources: []resourceJSON{{Ref: spec.ResourceRef, URL: strings.TrimSuffix(spec.Origin, "/") + spec.ResourcePath, ApprovalRef: "read-approval", ReadOnly: true, Accounts: []string{"owner", "peer"}, View: view}}}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal("등록 snapshot 인코딩 실패")
	}
	keyBytes, err := os.ReadFile(b.Key.File)
	if err != nil {
		t.Fatal("감사키 fixture 읽기 실패")
	}
	defer clear(keyBytes)
	jwtMaterial := make([]byte, 32)
	if _, err = rand.Read(jwtMaterial); err != nil {
		t.Fatal(err)
	}
	metadataFile := filepath.Join(base, "inputs", "metadata.json")
	pins := []struct {
		path string
		body []byte
	}{{filepath.Join(keyDir, jwtKeyFilename), []byte(hex.EncodeToString(jwtMaterial))}, {metadataFile, metadataBytes}, {filepath.Join(snapshotDir, "approved.json"), snapshotBytes}, {filepath.Join(snapshotDir, "registry.json"), registryBytes}, {filepath.Join(snapshotDir, "audit.key"), keyBytes}, {filepath.Join(snapshotDir, "owner.secret"), ownerHeader}, {filepath.Join(snapshotDir, "peer.secret"), peerHeader}}
	b.Files = nil
	for _, pin := range pins {
		if os.WriteFile(pin.path, pin.body, 0600) != nil {
			t.Fatal("보호 입력 저장 실패")
		}
		digest := sha256.Sum256(pin.body)
		b.Files = append(b.Files, ControlledReadLocalFile{Path: pin.path, SHA256: hex.EncodeToString(digest[:])})
	}
	modelBytes, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal("모델 profile 계약 읽기 실패")
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
		t.Fatal("실제 등록 production 기동 실패")
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := runtime.Close(closeCtx); err != nil {
			t.Error("실제 등록 production 최종 정리 실패")
		}
	}()
	// 모델 자리만 각본 provider로 바꾼다(유료 호출 없음). 교차 계정의 등록 GET 도구를 한 번 부른다.
	model := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":` + strconv.Quote(spec.ResourceRef) + `}`}
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
		t.Fatalf("실제 대상 등록 실행 실패: model_calls=%d intent=%s", model.calls, state)
	}
	// 구조·결속·감사 대조는 합성 통합 시험과 같은 검사기로 본다(GET 2건, 확정 1건).
	firstDiagnosisRegisteredStored(t, ctx, manager, runtime, source, keyBytes)
	privates := [][]byte{ownerHeader, peerHeader, []byte(canary)}
	var binding, payload, manifestBody []byte
	var verdict string
	if err := manager.pg.QueryRowContext(ctx, "SELECT i.binding_canonical,v.payload_canonical,m.canonical,v.oracle_value FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.task_id=$1", runtime.TaskID()).Scan(&binding, &payload, &manifestBody, &verdict); err != nil {
		t.Fatal("저장 E③ 결속 읽기 실패")
	}
	for _, body := range [][]byte{binding, payload, manifestBody} {
		for _, private := range privates {
			if bytes.Contains(body, private) {
				t.Fatal("세션·표식 원문이 저장 근거에 남음")
			}
		}
	}
	projection, err := readAccessMap(ctx, manager.pg.DB, runtime.TaskID())
	if err != nil {
		t.Fatal("검증 뒤 지도 조회 실패")
	}
	peerCell := accessMapFind(t, projection, "peer")
	if peerCell.State != "confirmed" || peerCell.ProjectionError != nil || peerCell.RecordID == nil {
		t.Fatalf("교차 계정 지도 셀이 확정이 아님: state=%s", peerCell.State)
	}
	handler := runtime.Handler()
	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"ABC2LAB","password":"`+password+`"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	var auth struct {
		Token string `json:"token"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &auth) != nil || auth.Token == "" {
		t.Fatal("기존 인증 경로 실패")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/exploration/findings?task_id="+strconv.FormatInt(runtime.TaskID(), 10), nil)
	request.Header.Set("Authorization", "Bearer "+auth.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"finding_id"`)) {
		t.Fatalf("결과 조회 실패: status=%d", response.Code)
	}
	for _, private := range privates {
		if bytes.Contains(response.Body.Bytes(), private) {
			t.Fatal("결과 조회에 세션·표식 원문 노출")
		}
	}
	t.Logf("verdict=%s map_peer=%s model_calls=%d stored_findings=1", verdict, peerCell.State, model.calls)
}

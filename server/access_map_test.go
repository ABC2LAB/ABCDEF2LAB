package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

// 판정은 기존 E③ 준비·저장 경로로 넣는다. GET은 모델·대상을 실행하지 않는다.
type accessMapTestFixture struct {
	pg      *db.DB
	store   *accessMapStore
	b       accessMapBinding
	in      e3record.Input
	source  e3record.Source
	markers db.TaskModeMarkerReader
}

func newAccessMapTestFixture(t *testing.T) *accessMapTestFixture {
	t.Helper()
	pg := approvalTestDB(t)
	task, err := pg.CreateTask("synthetic access map", "owned fixture only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := pg.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "synthetic read"}, 1, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	markers := func(_ context.Context, id int64) (taskmode.Marker, error) {
		return taskmode.Marker{TaskID: id, State: taskmode.MarkerPresent}, nil
	}
	mode, err := pg.RegisterTaskMode(t.Context(), task.ID, taskmode.ModeControlled, markers)
	if err != nil {
		t.Fatal(err)
	}
	in, source := finalizationRecordFixture()
	registry := map[string]any{"format": kgfixture.FormatV1, "snapshot_id": "map-snapshot", "run_id": "preparation-run", "reset_generation": "reset-1", "generation_ref": "generation-1", "instance_ref": "instance-1",
		"principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "reader"}, map[string]any{"label": "unregistered"}},
		"resources": []any{map[string]any{"resource_ref": "document:one", "owner_ref": "owner", "policies": []any{map[string]any{"evaluated_ref": "reader", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none", "valid_from": source.Policy.ValidFrom, "valid_until": source.Policy.ValidUntil}},
			"canaries": []any{map[string]any{"field": "note", "value": verificationTestMarker, "trivial": false}}}}}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := kgfixture.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	store := &accessMapStore{pool: pg.DB}
	registration, err := store.invalidate(t.Context(), strconv.FormatInt(task.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	spec := controlledReadSpec{TaskID: strconv.FormatInt(task.ID, 10), IntentID: intent, CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1", fixture: snapshot, mapRegistration: registration}
	if err := store.register(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	scope := transmit.Scope{TaskRef: spec.TaskID, RunRef: "execution-" + spec.TaskID, CaseRef: spec.CaseRef, ResetRef: spec.ResetRef, PolicyRef: spec.PolicyRef}
	if err := store.start(t.Context(), spec, scope); err != nil {
		t.Fatal(err)
	}
	c := in.Expected
	c.Scope = scope
	c.IntentID = intent
	c.KGPolicyVersion = snapshot.PolicyVersion()
	in.Expected = c
	in.Evidence.Policy.Context = c
	in.Evidence.Owner.Context, in.Evidence.Cross.Context, in.Evidence.Result.Context = c, c, c
	in.Evidence.Owner.Receipt.Scope, in.Evidence.Cross.Receipt.Scope = scope, scope
	in.Evidence.Owner.Evidence.Scope, in.Evidence.Cross.Evidence.Scope = scope, scope
	in.Evidence.Owner.Evidence.IntentID, in.Evidence.Cross.Evidence.IntentID = intent, intent
	in.Evidence.Result.OwnerEvidence, in.Evidence.Result.CrossEvidence = in.Evidence.Owner.Evidence, in.Evidence.Cross.Evidence
	source.Expected = c
	source.FinalCheckpointScope = scope
	source.ExplorationID = task.ExplorationID
	source.ModeVersion = mode.Version
	source.KGSnapshotRef, source.KGSnapshotDigest = snapshot.Ref(), snapshot.Digest()
	source.GenerationRef, source.InstanceRef, source.ResetGeneration = snapshot.GenerationRef(), snapshot.InstanceRef(), snapshot.ResetGeneration()
	source.EvidenceDigest, err = e3record.EvidenceDigest(in.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := snapshot.MapResources()
	if err != nil {
		t.Fatal(err)
	}
	b := accessMapBinding{Version: 1, TaskID: task.ID, ExplorationID: task.ExplorationID, IntentID: intent, Registration: registration, Ready: true, ModeVersion: mode.Version, Scope: scope, SnapshotRef: snapshot.Ref(), SnapshotDigest: snapshot.Digest(), PolicyVersion: snapshot.PolicyVersion(), GenerationRef: snapshot.GenerationRef(), InstanceRef: snapshot.InstanceRef(), Principals: snapshot.Principals(), Resources: resources, Attempts: []accessMapAttempt{}}
	return &accessMapTestFixture{pg, store, b, in, source, markers}
}

func (f *accessMapTestFixture) save(t *testing.T) db.ControlledFindingResult {
	t.Helper()
	prepared, err := e3record.Prepare(t.Context(), f.in, func(context.Context, evidencebind.Context) (e3record.Source, error) { return f.source, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.pg.RecordControlledFinding(t.Context(), prepared, f.markers)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func accessMapFind(t *testing.T, response accessMapResponse, principal string) accessMapCell {
	t.Helper()
	for _, c := range response.Cells {
		if c.Principal == principal {
			return c
		}
	}
	t.Fatal("셀을 찾을 수 없습니다")
	return accessMapCell{}
}

func TestAccessMapOwnedPostgresProjectionAndScope(t *testing.T) {
	f := newAccessMapTestFixture(t)
	first := f.save(t)
	read := func() accessMapResponse {
		t.Helper()
		result, err := readAccessMap(t.Context(), f.pg.DB, f.b.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := read()
	c := accessMapFind(t, result, "reader")
	if c.State != "confirmed" || c.OracleValue == nil || *c.OracleValue != oracle.Confirmed || c.RecordID == nil || *c.RecordID != first.VerdictID || c.Applicable == nil || !*c.Applicable || c.ProjectionError != nil {
		t.Fatal("정본 확정 셀 불일치", c.State, c.ProjectionError)
	}
	blank := accessMapFind(t, result, "unregistered")
	if blank.State != "blank" || blank.Applicable != nil || blank.RecordID != nil || blank.OracleValue != nil || blank.ObservedAt != nil {
		t.Fatal("등록 근거 없는 셀을 미시도·적용불가로 추정함")
	}
	raw, _ := json.Marshal(result)
	for _, private := range []string{verificationTestMarker, "session-a", "session-b", "payload_canonical", "traffic_evidence_refs", "evidence_ref"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("지도 응답에 비공개·비계약 필드 포함")
		}
	}
	// 동일 scope·동일 판정이어도 둘 중 하나를 골라서는 안 된다.
	f.source.EvidenceRevision++
	f.save(t)
	c = accessMapFind(t, read(), "reader")
	if c.State != "unknown" || c.ProjectionError == nil || *c.ProjectionError != "selection_ambiguous" || c.RecordID != nil || c.OracleValue != nil {
		t.Fatal("복수 판정을 임의로 선택함")
	}
	// 현재 포인터가 바뀌면 과거 confirmed가 그대로 저장돼 있어도 조회 범위는 닫힌다.
	if _, err := f.store.invalidate(t.Context(), f.b.Scope.TaskRef); err != nil {
		t.Fatal(err)
	}
	if _, err := readAccessMap(t.Context(), f.pg.DB, f.b.TaskID); !errors.Is(err, errAccessMapScope) {
		t.Fatal("무효화 뒤 과거 지도 반환", err)
	}
	next := f.b
	next.Scope.RunRef, next.Scope.ResetRef, next.GenerationRef = "next-execution", "reset-2", "generation-2"
	next.SnapshotDigest = sha256.Sum256([]byte("synthetic next generation"))
	next.PolicyVersion = "kg-fixture/v1:" + next.SnapshotRef + ":" + hex.EncodeToString(next.SnapshotDigest[:])
	if err := f.store.update(t.Context(), next.TaskID, func(b *accessMapBinding) error { *b = next; return nil }); err != nil {
		t.Fatal(err)
	}
	result = read()
	c = accessMapFind(t, result, "reader")
	if result.RunRef != next.Scope.RunRef || result.ResetGeneration != "reset-2" || result.PolicyVersion != next.PolicyVersion || c.State != "unknown" || c.RecordID != nil || c.OracleValue != nil {
		t.Fatal("새 세대가 과거 확정 판정을 재사용함")
	}
}

func TestAccessMapOwnedPostgresAuthenticationAndRoutes(t *testing.T) {
	f := newAccessMapTestFixture(t)
	f.save(t)
	s := &Server{ctx: t.Context(), m: &Manager{pg: f.pg}, jwtKey: []byte("synthetic-map-signing-key-material"), firstDiagnosis: true, firstModel: &firstDiagnosisModelPin{TaskID: f.b.Scope.TaskRef, TaskNumber: f.b.TaskID, ExplorationID: f.b.ExplorationID}}
	hash, err := s.reloadPassHash()
	if err != nil {
		t.Fatal(err)
	}
	token, err := signJWT(s.signingKey(hash))
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + f.b.Scope.TaskRef + "/access-map"
	for _, tc := range []struct {
		name, method, path, auth string
		want                     int
	}{
		{"authorized", "GET", path, "Bearer " + token, 200},
		{"missing-token", "GET", path, "", 401},
		{"invalid-token", "GET", path, "Bearer invalid", 401},
		{"foreign-task", "GET", "/api/tasks/" + strconv.FormatInt(f.b.TaskID+1, 10) + "/access-map", "Bearer " + token, 403},
		{"history-query", "GET", path + "?run_ref=past", "Bearer " + token, 403},
		{"case-query", "GET", path + "?case_ref=case-1", "Bearer " + token, 403},
		{"head", "HEAD", path, "Bearer " + token, 403},
		{"write", "POST", path, "Bearer " + token, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if w.Code == 200 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("현재 지도 캐시 허용")
			}
		})
	}
	// 직접 핸들러도 외곽 허용 목록 없이 다른 task를 열지 않는다.
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.serveAccessMap(w, r, f.b.TaskID+1)
	if w.Code != 403 {
		t.Fatal("직접 핸들러 task 결속 누락")
	}
}

func TestAccessMapOwnedPostgresRecordCorruption(t *testing.T) {
	for _, column := range []string{"model_tag", "reset_generation", "harness_generation_ref", "target_instance_ref", "kg_policy_version"} {
		t.Run(column, func(t *testing.T) {
			f := newAccessMapTestFixture(t)
			f.save(t)
			// 소유 시험 DB에서만 불변 가드를 잠시 해제해 손상 방어를 검사한다.
			tx, err := f.pg.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`ALTER TABLE e3_verdict_issuances DISABLE TRIGGER trg_e3_issuance_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`UPDATE e3_verdict_issuances SET `+column+`='mismatched' WHERE task_id=$1`, f.b.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`ALTER TABLE e3_verdict_issuances ENABLE TRIGGER trg_e3_issuance_immutable`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			result, err := readAccessMap(t.Context(), f.pg.DB, f.b.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			c := accessMapFind(t, result, "reader")
			if c.State != "unknown" || c.ProjectionError == nil || *c.ProjectionError != "record_invalid" {
				t.Fatal("잘못 결속된 판정 승격")
			}
		})
	}
}

func TestAccessMapOwnedPostgresCanonicalIntegrity(t *testing.T) {
	for _, scenario := range []string{"payload-digest", "manifest-digest", "payload-signal", "source-completion", "applicability", "duplicate-current", "mode-version", "deleted-task"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccessMapTestFixture(t)
			result := f.save(t)
			taskToRead := f.b.TaskID
			if scenario == "duplicate-current" {
				if _, err := f.pg.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) SELECT exploration_id,kind,state,origin,payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2`, f.b.ExplorationID, accessMapOrigin); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "mode-version" {
				if err := f.store.update(t.Context(), f.b.TaskID, func(b *accessMapBinding) error { b.ModeVersion++; return nil }); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "deleted-task" {
				row, err := f.pg.CreateTask("synthetic deleted map task", "no controlled execution", nil, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				taskToRead = row.ID
				if _, err := f.pg.Exec(`UPDATE tasks SET deleted_at=NOW() WHERE id=$1`, taskToRead); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "applicability" {
				if err := f.store.update(t.Context(), f.b.TaskID, func(b *accessMapBinding) error { b.Resources[0].Principals = nil; return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				tx, err := f.pg.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for _, table := range []struct{ name, trigger string }{{"e3_oracle_verdicts", "trg_e3_verdict_immutable"}, {"e3_evidence_manifests", "trg_e3_manifest_immutable"}, {"e3_verdict_issuances", "trg_e3_issuance_immutable"}} {
					if _, err = tx.Exec(`ALTER TABLE ` + table.name + ` DISABLE TRIGGER ` + table.trigger); err != nil {
						t.Fatal(err)
					}
				}
				switch scenario {
				case "payload-digest":
					_, err = tx.Exec(`UPDATE e3_oracle_verdicts SET payload_digest=$1 WHERE id=$2`, make([]byte, 32), result.VerdictID)
				case "manifest-digest":
					_, err = tx.Exec(`UPDATE e3_evidence_manifests SET digest=$1 WHERE id=$2`, make([]byte, 32), result.ManifestID)
				case "payload-signal":
					var raw []byte
					if err = tx.QueryRow(`SELECT payload_canonical FROM e3_oracle_verdicts WHERE id=$1`, result.VerdictID).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					var payload map[string]any
					if err = json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					payload["cross_normal_data"] = "no"
					raw, err = json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256(raw)
					_, err = tx.Exec(`UPDATE e3_oracle_verdicts SET payload_canonical=$1,payload_digest=$2 WHERE id=$3`, raw, digest[:], result.VerdictID)
				case "source-completion":
					_, err = tx.Exec(`UPDATE e3_verdict_issuances SET binding_canonical=convert_to(jsonb_set(convert_from(binding_canonical,'UTF8')::jsonb,'{Completed}','false')::text,'UTF8') WHERE task_id=$1`, f.b.TaskID)
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, table := range []struct{ name, trigger string }{{"e3_oracle_verdicts", "trg_e3_verdict_immutable"}, {"e3_evidence_manifests", "trg_e3_manifest_immutable"}, {"e3_verdict_issuances", "trg_e3_issuance_immutable"}} {
					if _, err = tx.Exec(`ALTER TABLE ` + table.name + ` ENABLE TRIGGER ` + table.trigger); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			response, err := readAccessMap(t.Context(), f.pg.DB, taskToRead)
			if scenario == "deleted-task" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("삭제된 작업 지도 노출", err)
				}
				return
			}
			if scenario == "duplicate-current" || scenario == "mode-version" {
				if !errors.Is(err, errAccessMapScope) {
					t.Fatal("잘못된 현재 범위를 선택함", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cell := accessMapFind(t, response, "reader")
			if cell.State != "unknown" || cell.ProjectionError == nil || *cell.ProjectionError != "record_invalid" {
				t.Fatal("손상된 정본을 판정으로 투영함")
			}
		})
	}
}

func TestAccessMapProjectionStates(t *testing.T) {
	in, source := finalizationRecordFixture()
	prepared, err := e3record.Prepare(t.Context(), in, func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := prepared.Snapshot()
	var record accessMapRecord
	if err := json.Unmarshal(snapshot.Canonical, &record); err != nil {
		t.Fatal(err)
	}
	t.Run("canary-field-set", func(t *testing.T) {
		r, s := record, source
		r.CanaryFields, s.CanaryFields = []string{"note", "display name"}, []string{"display name", "note"}
		if !r.valid(s) {
			t.Fatal("유효한 표식 필드 집합의 순서를 계약으로 오인함")
		}
		r.CanaryFields = []string{"note", "note"}
		if r.valid(s) {
			t.Fatal("누락·중복 표식 필드를 수용함")
		}
	})
	b := accessMapBinding{Principals: []string{"reader"}, Resources: []kgfixture.MapResource{{ResourceRef: record.ResourceRef, OwnerRef: record.OwnerRef, Principals: []string{"reader"}}}}
	for _, tc := range []struct {
		name  string
		edit  func(*accessMapRecord)
		valid bool
		want  string
	}{
		{"confirmed", func(*accessMapRecord) {}, true, "confirmed"},
		{"suspected", func(r *accessMapRecord) { r.OracleValue = oracle.Suspected }, true, "suspected"},
		{"undecidable", func(r *accessMapRecord) { r.OracleValue = oracle.Undecidable }, true, "unknown"},
		{"allowed-observed", func(r *accessMapRecord) { r.OracleValue = oracle.Allowed; r.ExpectedAccess = oracle.AccessAllow }, true, "allow-ok"},
		{"allowed-without-cross", func(r *accessMapRecord) {
			r.OracleValue = oracle.Allowed
			r.ExpectedAccess = oracle.AccessAllow
			r.CrossNormalData = oracle.StateUnknown
		}, true, "unknown"},
		{"allowed-expired-observation", func(r *accessMapRecord) {
			r.OracleValue = oracle.Allowed
			r.ExpectedAccess = oracle.AccessAllow
			r.ObservedAt = r.PolicyValidUntil
		}, true, "unknown"},
		{"confirmed-invalid-source", func(*accessMapRecord) {}, false, "unknown"},
		{"no-app-block-proof", func(r *accessMapRecord) {
			r.OracleValue = oracle.Undecidable
			r.NontrivialDataMatch = oracle.StateNo
			r.CrossNormalData = oracle.StateNo
		}, true, "unknown"},
		{"wrong-owner", func(r *accessMapRecord) { r.OwnerRef = "other-owner" }, true, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := record
			tc.edit(&r)
			response := projectAccessMap(b, []accessMapStoredRecord{{id: 1, record: r, valid: tc.valid}})
			c := response.Cells[0]
			if c.State != tc.want {
				t.Fatalf("state=%s want=%s", c.State, tc.want)
			}
			if c.OracleValue != nil && *c.OracleValue != r.OracleValue {
				t.Fatal("오라클 원값 변경")
			}
		})
	}
	b.Resources[0].Principals = nil
	b.Attempts = []accessMapAttempt{{Principal: "reader", Resource: record.ResourceRef}}
	c := projectAccessMap(b, nil).Cells[0]
	if c.State != "unknown" || c.ProjectionError == nil || *c.ProjectionError != "record_missing_after_attempt" {
		t.Fatal("시도 후 기록 누락을 blank로 표시")
	}
	// 중복은 동일한 판정이라도 모호하다.
	r := accessMapStoredRecord{id: 1, record: record, valid: true}
	c = projectAccessMap(b, []accessMapStoredRecord{r, r}).Cells[0]
	if c.State != "unknown" || c.RecordID != nil || c.ProjectionError == nil || *c.ProjectionError != "selection_ambiguous" {
		t.Fatal("중복 판정 덮어쓰기")
	}
}

func TestAccessMapRequestShape(t *testing.T) {
	for _, path := range []string{"/api/tasks/17/access-map", "/api/tasks/017/access-map", "/api/tasks/0/access-map", "/api/tasks/-1/access-map", "/api/tasks/17/access-map/", "/api/tasks/17/access-map?run_ref=x", "/api/tasks/17/access-map?case_ref=x", "/api/tasks/17/access-map?"} {
		_, ok := accessMapRequestTask(httptest.NewRequest("GET", path, nil))
		if ok != (path == "/api/tasks/17/access-map") {
			t.Fatal("조회 키 검증 불일치", path)
		}
	}
}

func TestAccessMapRuntimeRegistrationAndClosureOwnedPostgres(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker)
	}))
	defer target.Close()
	f := newControlledVerificationDBFixture(t, target.URL)
	registry := map[string]any{"format": kgfixture.FormatV1, "snapshot_id": "runtime-map", "run_id": "preparation-1", "reset_generation": "reset-1", "generation_ref": "generation-1", "instance_ref": "instance-1",
		"principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "peer"}},
		"resources":  []any{map[string]any{"resource_ref": "doc:one", "owner_ref": "owner", "policies": []any{map[string]any{"evaluated_ref": "peer", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none", "valid_from": time.Now().Add(-time.Hour).UTC(), "valid_until": time.Now().Add(time.Hour).UTC()}}, "canaries": []any{map[string]any{"field": "note", "value": verificationTestMarker, "trivial": false}}}}}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.store.snapshotDir, "kg.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.s.configureControlledReadFixtureRegistries(map[string]string{"approved": "kg.json"}); err != nil {
		t.Fatal(err)
	}
	f.registerAndResume(t)
	taskID, err := strconv.ParseInt(f.task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readAccessMap(t.Context(), f.m.pg.DB, taskID); !errors.Is(err, errAccessMapScope) {
		t.Fatal("실행 전 임의 run 선택", err)
	}
	run, finish, err := f.e.prepareControlledRead(t.Context(), f.task, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	defer finish(false)
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	_, _, _ = runtimeWorker(t, provider).Execute(run, "worker", taskID, nil, nil, f.intent, nil, nil, nil, nil)
	if err = finish(false); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatal("합성 조회 송신 수 불일치", hits.Load())
	}
	result, err := readAccessMap(t.Context(), f.m.pg.DB, taskID)
	if err != nil {
		t.Fatal(err)
	}
	cell := accessMapFind(t, result, "peer")
	if cell.State != "unknown" || cell.ProjectionError == nil || *cell.ProjectionError != "record_missing_after_attempt" {
		t.Fatal("실제 시도 후 판정 누락을 blank로 표시")
	}
	if hits.Load() != 1 {
		t.Fatal("지도 GET이 새 대상 요청 실행")
	}
	// 실패한 재등록은 직전 실행을 현재 지도에 남기지 않는다.
	f.e.Pause(f.task.ID, agent.AbortPausedByUser)
	if err = f.m.ApplyTaskPause(f.task.ID); err != nil {
		t.Fatal(err)
	}
	registry["reset_generation"] = "reset-2"
	raw, err = json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.store.snapshotDir, "kg.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/tasks/"+f.task.ID+"/intents/"+strconv.FormatInt(f.intent.ID, 10)+"/controlled-read", strings.NewReader(`{"snapshot_ref":"approved"}`))
	r.SetPathValue("id", f.task.ID)
	r.SetPathValue("iid", strconv.FormatInt(f.intent.ID, 10))
	w := httptest.NewRecorder()
	f.s.registerControlledReadHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("세대 pairing 불일치 등록 허용", w.Code)
	}
	if _, err = readAccessMap(t.Context(), f.m.pg.DB, taskID); !errors.Is(err, errAccessMapScope) {
		t.Fatal("실패한 세대 교체 후 이전 지도 재사용", err)
	}
}

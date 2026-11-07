//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/auditpublisher"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// 실제 PG16 행 잠금·독립 LOGIN·물리 FD를 시험한다. 테스트 supervisor는 실제 모델 수용이 아니다.
func TestControlledLocalHostOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	ctx, runtimePool, authorityPool := f.Context, f.Runtime, f.Authority
	installer := &pgdb.DB{DB: f.Installer}
	b := localBindingFixture(t)
	// runtime 역할로 실제 task와 intent를 생성해 승인에 결속한다.
	managerPool := runtimePool
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), managerPool)
	if err != nil {
		managerPool.Close()
		t.Fatal("runtime manager 생성 실패")
	}
	defer manager.Close()
	task, err := manager.CreateTask("Owned local host fixture", "Verify exact installed host binding", nil, 60, 60)
	if err != nil {
		t.Fatal("실제 task 생성 실패")
	}
	intent, err := task.Store.AddIntent(map[string]any{"action": "Check one registered read"}, 1, nil, "synthetic fixture")
	if err != nil {
		t.Fatal("실제 intent 생성 실패")
	}
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	b.Execution.TaskID = taskID
	b.Execution.IntentID = intent
	bindingFile := filepath.Join(filepath.Dir(b.State.Path), "inputs", "host.json")
	raw, _ := f.InstallBinding(t, b, bindingFile)
	supervisor := localTestSupervisor{authorize: func(call context.Context, s transmit.Scope, i int64) error {
		var count int
		if runtimePool.QueryRowContext(call, "SELECT count(*) FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.id=$2 AND n.kind='intent'", taskID, i).Scan(&count) != nil || count != 1 || s.TaskRef != task.ID {
			return ErrControlledReadLocalHost
		}
		return nil
	}, quiescent: func(context.Context, int64) error {
		return errors.New("no production child supervisor in this component fixture")
	}}
	hostCfg := ControlledReadLocalHostConfig{BindingFile: bindingFile, Supervisor: supervisor, Limits: auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 32, Timeout: 10 * time.Second}}
	if host, err := AcquireControlledReadLocalHost(ctx, installer.DB, installer.DB, hostCfg); err == nil {
		_ = host.Close(ctx)
		t.Fatal("installer pool accepted as runtime")
	}
	if untrusted, err := AcquireControlledReadLocalHost(ctx, runtimePool, installer.DB, hostCfg); err == nil {
		_ = untrusted.Close(ctx)
		t.Fatal("설치자 pool을 제품 host authority로 수용")
	}
	tampered := bytes.Replace(raw, []byte("publisher-test"), []byte("publisher-fake"), 1)
	if os.WriteFile(bindingFile, tampered, 0600) != nil {
		t.Fatal("음성 fixture 작성 실패")
	}
	if host, err := AcquireControlledReadLocalHost(ctx, runtimePool, authorityPool, hostCfg); err == nil {
		_ = host.Close(ctx)
		t.Fatal("DB anchor와 다른 binding 수용")
	}
	if os.WriteFile(bindingFile, raw, 0600) != nil {
		t.Fatal("fixture 복구 실패")
	}
	host, err := AcquireControlledReadLocalHost(ctx, runtimePool, authorityPool, hostCfg)
	if err != nil {
		t.Fatal("실제 설치 공급 획득 실패")
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if host.Close(closeCtx) != nil {
			t.Error("실제 공급 정리 실패")
		}
	}()
	sources, err := host.Sources()
	if err != nil {
		t.Fatal("실제 Sources 획득 실패")
	}
	defer sources.Root.Release()
	keyStore, err := auditkeys.New(runtimePool, auditkeys.Config{Namespace: b.Key.Namespace, PolicyRef: b.Key.PolicyRef}, sources.Keys)
	if err != nil {
		t.Fatal(err)
	}
	record, err := keyStore.Inspect(ctx, b.Key.KeyRef)
	if err != nil || record.State != auditkeys.Active || record.Revision != 2 {
		t.Fatal("실제 활성 불변 키 매핑 inspect 실패")
	}
	scope := transmit.Scope{TaskRef: task.ID, RunRef: "run-owned-actual", CaseRef: b.Execution.CaseRef, ResetRef: b.Execution.ResetRef, PolicyRef: b.Execution.PolicyRef}
	if host.AuthorizeExecution(ctx, scope, intent) != nil {
		t.Fatal("실제 task/intent 승인 실패")
	}
	// 실제 runtime DB를 통과하는 최초 journal header의 동기 SQL ACK까지 확인한다.
	plan, err := host.PublisherFactory()(ctx, ControlledReadPublisherRequest{Scope: scope, IntentID: intent, KeyRef: b.Key.KeyRef, Filename: scope.RunRef + ".jsonl"})
	if err != nil {
		t.Fatal("실제 publisher 공급 실패")
	}
	checkpointHost := &controlledCheckpointHost{real: plan.Host, epoch: plan.Epoch, scope: scope, keyRef: b.Key.KeyRef, publisher: plan.PublisherRef, drain: &controlledHTTPDrain{scope: scope}}
	checkpointStore, err := auditcheckpoint.New(plan.Database, checkpointHost, plan.Limits)
	if err != nil {
		plan.RootScope.Release()
		t.Fatal("runtime checkpoint store 생성 실패")
	}
	publishStart := time.Now()
	publisher, err := auditpublisher.Open(ctx, auditpublisher.Config{Store: checkpointStore, Epoch: plan.Epoch, PublisherRef: plan.PublisherRef, RootScope: plan.RootScope, Filename: scope.RunRef + ".jsonl", Scope: scope, KeyRef: b.Key.KeyRef, Key: host.key, MaxSnapshotBytes: plan.Limits.MaxBytes, BindCloseWitness: checkpointHost.bindClose})
	if err != nil {
		t.Fatal("최소 host authority 역할의 최초 publisher SQL ACK 실패")
	}
	t.Logf("actual publisher header ACK duration=%s", time.Since(publishStart))
	head, err := publisher.AcknowledgedHead()
	if err != nil || head.Revision != 1 || head.Phase != auditcheckpoint.Open {
		_ = publisher.Abort()
		t.Fatal("최초 durable header ACK 불일치")
	}
	// 같은 실제 host/key/snapshot이어도 runtime pool로 writer를 만들면 반드시 SQL에서 거부된다.
	runtimeWriter, err := auditcheckpoint.New(runtimePool, checkpointHost, plan.Limits)
	if err != nil {
		_ = publisher.Abort()
		t.Fatal("runtime 권한 음성 store 준비 실패")
	}
	headerBytes, err := os.ReadFile(filepath.Join(b.Journals.Path, scope.RunRef+".jsonl"))
	if err != nil {
		_ = publisher.Abort()
		t.Fatal("소유 합성 header snapshot 읽기 실패")
	}
	_, err = runtimeWriter.Publish(ctx, auditcheckpoint.Request{Epoch: plan.Epoch, OperationRef: "fixture-runtime-epoch-denial", PublisherRef: plan.PublisherRef, Kind: auditcheckpoint.Register, Candidate: head.Checkpoint}, bytes.NewReader(headerBytes))
	clear(headerBytes)
	if !errors.Is(err, auditcheckpoint.ErrStorage) {
		_ = publisher.Abort()
		t.Fatal("runtime epoch 잠금 권한 부재가 actual Store.Publish에서 거부되지 않음")
	}
	if _, err = host.AuditSourceOptions().LatestCheckpoint(ctx, scope); err == nil {
		_ = publisher.Abort()
		t.Fatal("열린 journal을 최종 감사로 수용")
	}
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"owned synthetic"}`))
	}))
	defer target.Close()
	account := idor.AccountRef{Label: "fixture-owner", Handle: "fixture-session", Status: oracle.SubjectVerified}
	transmitter, err := transmit.NewHTTP(host.Context(), transmit.Config{Scope: scope, Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"), ExpiresAt: b.ExpiresAt, MaxRequests: 2, MinInterval: 500 * time.Millisecond, RequestTimeout: 10 * time.Second, MaxBodyBytes: 1 << 20, Sessions: []transmit.Session{{Account: account, ExpiresAt: b.ExpiresAt}}, Resources: []transmit.Resource{{Ref: "fixture-resource", URL: target.URL + "/document", ApprovalRef: "fixture-read-approval", ReadOnly: true, Accounts: []string{account.Label}}}, Recorder: publisher})
	if err != nil {
		_ = publisher.Abort()
		t.Fatal("실제 transmitter 조립 실패")
	}
	checkpointHost.drain.bind(transmitter)
	_, receipt, ok := transmitter.SendDetailed(idor.AccessRequest{As: account, Method: http.MethodGet, ResourceRef: "fixture-resource"})
	if !ok || receipt.Reason != transmit.Completed || hits.Load() != 1 {
		transmitter.StopAndWait()
		_ = publisher.Abort()
		t.Fatal("허용된 합성 target GET 또는 동기 감사 ACK 실패")
	}
	_, denied, ok := transmitter.SendDetailed(idor.AccessRequest{As: account, Method: http.MethodGet, ResourceRef: "outside-registration"})
	if ok || denied.Admitted || hits.Load() != 1 {
		transmitter.StopAndWait()
		_ = publisher.Abort()
		t.Fatal("미등록 요청이 합성 target에 도달함")
	}
	if !checkpointHost.drain.stopAndWait(transmitter) {
		_ = publisher.Abort()
		t.Fatal("실제 transmitter drain 증거 실패")
	}
	sealed, err := publisher.Seal(ctx)
	if err != nil || sealed.Phase != auditcheckpoint.Sealed || sealed.Checkpoint.Sequence != 3 {
		t.Fatal("실제 drain·CloseWitness 뒤 최종 SQL Seal ACK 실패")
	}
	anchor, err := host.AuditSourceOptions().LatestCheckpoint(ctx, scope)
	if err != nil || anchor.Checkpoint != sealed.Checkpoint {
		t.Fatal("같은 회차의 실제 최종 head 조회 실패")
	}
	_, stopped, ok := transmitter.SendDetailed(idor.AccessRequest{As: account, Method: http.MethodGet, ResourceRef: "fixture-resource"})
	if ok || stopped.Reason != transmit.Stopped || hits.Load() != 1 {
		t.Fatal("drain 뒤 새 송신이 허용됨")
	}

	changed := scope
	changed.RunRef = "run-other"
	if host.AuthorizeExecution(ctx, changed, intent) == nil {
		t.Fatal("동일 설치 권위로 새 회차 재사용")
	}
	// 설치 key/epoch 변경은 실제 SQL 행 잠금으로 막힌다.
	lockedCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = installer.ExecContext(lockedCtx, "UPDATE audit_checkpoint_epochs SET generation=generation+1 WHERE store_ref=$1", b.StoreRef)
	stop()
	if err == nil {
		t.Fatal("보유 epoch 행 잠금이 변경을 막지 못함")
	}
	lockedCtx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = installer.ExecContext(lockedCtx, "UPDATE audit_key_registry_keys SET state='retiring',revision=3 WHERE namespace=$1 AND key_ref=$2", b.Key.Namespace, b.Key.KeyRef)
	stop()
	if err == nil {
		t.Fatal("보유 key 행 잠금이 변경을 막지 못함")
	}
	if host.CheckRoot(ctx) != nil {
		t.Fatal("다른 연결의 거부가 실제 lease를 취소함")
	}
	closeCtx, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	err = host.Close(closeCtx)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("live root 보유 중 Close가 완료됨")
	}
	sources.Root.Release()
	closeCtx, stop = context.WithTimeout(ctx, 5*time.Second)
	err = host.Close(closeCtx)
	stop()
	if err != nil {
		t.Fatal("모든 lease 반환 후 실제 Close 실패")
	}
	if retry, err := AcquireControlledReadLocalHost(ctx, runtimePool, authorityPool, hostCfg); err == nil {
		_ = retry.Close(ctx)
		t.Fatal("종료 후 설치 권위 재사용")
	}
}

// 동일 전용 DB 안에서도 각 호출이 별도 schema·LOGIN을 소유한다.
type controlledLocalDatabaseFixture struct {
	Context                       context.Context
	Installer, Runtime, Authority *sql.DB
}

func newControlledLocalDatabaseFixture(t *testing.T) *controlledLocalDatabaseFixture {
	t.Helper()
	dsn := os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_DSN")
	if dsn == "" {
		t.Skip("단일 호스트 전용 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("전용 일회용 DB의 명시적 소유 확인 필요")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("전용 DB 구성 실패")
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random)
	schema := "local_host_" + suffix
	role := "local_rt_" + suffix
	authorityRole := "local_auth_" + suffix
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	exec := func(pool *sql.DB, statement string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal("소유 DB fixture 설정 실패")
		}
	}
	exec(admin, "CREATE SCHEMA "+ident(schema))
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+ident(schema)+" CASCADE"); err != nil {
			t.Error("소유 schema 정리 실패")
		}
	})
	installCfg := cfg.Copy()
	installCfg.RuntimeParams["search_path"] = schema
	installDSN := dsn
	if parsed, e := url.Parse(dsn); e == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		installDSN = parsed.String()
	} else {
		installDSN = dsn + " search_path=" + schema
	}
	installer, err := pgdb.Open(installDSN)
	if err != nil {
		t.Fatal("격리 설치 schema migration 실패")
	}
	t.Cleanup(func() { _ = installer.Close() })
	for _, migrate := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate} {
		if migrate(ctx, installer.DB) != nil {
			t.Fatal("전용 core migration 실패")
		}
	}
	var dbName string
	if installer.QueryRowContext(ctx, "SELECT current_database()").Scan(&dbName) != nil {
		t.Fatal("전용 DB 식별 실패")
	}
	exec(admin, "REVOKE CREATE,TEMP ON DATABASE "+ident(dbName)+" FROM PUBLIC")
	password := make([]byte, 24)
	if _, err = rand.Read(password); err != nil {
		t.Fatal(err)
	}
	exec(admin, "CREATE ROLE "+ident(role)+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+hex.EncodeToString(password)+"'")
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP OWNED BY "+ident(role)); err != nil {
			t.Error("소유 runtime 권한 정리 실패")
		}
		if _, err := admin.ExecContext(context.Background(), "DROP ROLE "+ident(role)); err != nil {
			t.Error("소유 runtime 역할 정리 실패")
		}
	})
	exec(admin, "GRANT CONNECT ON DATABASE "+ident(dbName)+" TO "+ident(role))
	exec(admin, "GRANT USAGE ON SCHEMA "+ident(schema)+" TO "+ident(role))
	for _, name := range []string{"tasks", "explorations", "exploration_nodes", "task_llm_profiles", "task_control_modes", "findings"} {
		exec(installer.DB, "GRANT SELECT,INSERT,UPDATE ON TABLE "+ident(name)+" TO "+ident(role))
	}
	for _, name := range []string{"traffic_evidence_snapshots", "finding_traffic_bindings", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links", "evidence_revision_initials", "evidence_revision_operations", "exploration_edges", "activity", "task_scope", "task_constraints", "exploration_anchors"} {
		exec(installer.DB, "GRANT SELECT,INSERT ON TABLE "+ident(name)+" TO "+ident(role))
	}
	for _, name := range []string{"llm_profiles", "settings", "agents", "agent_prompts", "task_categories", "task_relations", "task_archives", "intercept_rules", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules", "audit_checkpoint_history", "audit_checkpoint_heads", "audit_checkpoint_operations", "audit_checkpoint_epochs", "audit_key_registry_keys", "audit_key_registry_history", "audit_key_registry_operations"} {
		exec(installer.DB, "GRANT SELECT ON TABLE "+ident(name)+" TO "+ident(role))
	}
	exec(installer.DB, "GRANT USAGE ON ALL SEQUENCES IN SCHEMA "+ident(schema)+" TO "+ident(role))
	exec(installer.DB, "GRANT EXECUTE ON FUNCTION try_inet(text) TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(id) ON TABLE e3_evidence_manifests,e3_verdict_issuances TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(id) ON assets TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(task_id) ON task_asset_links TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(node_id) ON exploration_anchors TO "+ident(role))

	authorityPassword := make([]byte, 24)
	if _, err = rand.Read(authorityPassword); err != nil {
		t.Fatal(err)
	}
	exec(admin, "CREATE ROLE "+ident(authorityRole)+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+hex.EncodeToString(authorityPassword)+"'")
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP OWNED BY "+ident(authorityRole)); err != nil {
			t.Error("소유 authority 권한 정리 실패")
		}
		if _, err := admin.ExecContext(context.Background(), "DROP ROLE "+ident(authorityRole)); err != nil {
			t.Error("소유 authority 역할 정리 실패")
		}
	})
	exec(admin, "GRANT CONNECT ON DATABASE "+ident(dbName)+" TO "+ident(authorityRole))
	exec(admin, "GRANT USAGE ON SCHEMA "+ident(schema)+" TO "+ident(authorityRole))
	for _, name := range []string{"audit_checkpoint_epochs", "audit_key_registry_keys", "audit_key_registry_history"} {
		exec(installer.DB, "GRANT SELECT ON TABLE "+ident(name)+" TO "+ident(authorityRole))
	}
	exec(installer.DB, "GRANT UPDATE(store_ref) ON TABLE audit_checkpoint_epochs TO "+ident(authorityRole))
	exec(installer.DB, "GRANT UPDATE(key_ref) ON TABLE audit_key_registry_keys,audit_key_registry_history TO "+ident(authorityRole))
	for _, name := range []string{"audit_checkpoint_history", "audit_checkpoint_operations"} {
		exec(installer.DB, "GRANT SELECT,INSERT ON TABLE "+ident(name)+" TO "+ident(authorityRole))
	}
	exec(installer.DB, "GRANT SELECT,INSERT,UPDATE ON TABLE audit_checkpoint_heads TO "+ident(authorityRole))
	authorityCfg := installCfg.Copy()
	authorityCfg.User = authorityRole
	authorityCfg.Password = hex.EncodeToString(authorityPassword)
	authorityPool := stdlib.OpenDB(*authorityCfg)
	t.Cleanup(func() { _ = authorityPool.Close() })
	authorityPool.SetMaxOpenConns(3)
	if CheckFirstDiagnosisAuthorityDatabase(ctx, authorityPool) != nil {
		t.Fatal("별도 최소 host authority 권한 수용 실패")
	}
	runtimeCfg := installCfg.Copy()
	runtimeCfg.User = role
	runtimeCfg.Password = hex.EncodeToString(password)
	runtimePool := stdlib.OpenDB(*runtimeCfg)
	t.Cleanup(func() { _ = runtimePool.Close() })
	runtimePool.SetMaxOpenConns(4)
	if CheckFirstDiagnosisRuntimeDatabase(ctx, runtimePool) != nil {
		t.Fatal("별도 runtime LOGIN의 최소 권한 수용 실패")
	}
	if _, err := runtimePool.ExecContext(ctx, `UPDATE audit_checkpoint_heads SET revision=revision WHERE false`); err == nil {
		t.Fatal("runtime가 checkpoint writer DML을 보유함")
	}
	return &controlledLocalDatabaseFixture{Context: ctx, Installer: installer.DB, Runtime: runtimePool, Authority: authorityPool}
}

// InstallBinding은 소유 시험 schema의 설치자만 사용한다. 제품 권한 발급 API가 아니다.
func (f *controlledLocalDatabaseFixture) InstallBinding(t *testing.T, b ControlledReadLocalBinding, bindingFile string) ([]byte, [32]byte) {
	t.Helper()
	if b.Key.Revision != 2 || b.Key.PredecessorRef != "" {
		t.Fatal("시험 key 설치는 독립 active revision2만 지원")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal("시험 binding encoding 실패")
	}
	if os.MkdirAll(filepath.Dir(bindingFile), 0700) != nil || os.WriteFile(bindingFile, raw, 0600) != nil {
		t.Fatal("보호 binding 작성 실패")
	}
	digest := sha256.Sum256(raw)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := f.Installer.ExecContext(f.Context, statement, args...); err != nil {
			t.Fatal("소유 시험 설치 자료 입력 실패")
		}
	}
	exec("INSERT INTO audit_checkpoint_epochs(store_ref,epoch_ref,generation,anchor_digest) VALUES($1,$2,$3,$4)", b.StoreRef, b.EpochRef, int64(b.Generation), digest[:])
	exec("INSERT INTO audit_key_registry_keys(namespace,key_ref,secret_version_ref,secret_identity,state,revision) VALUES($1,$2,$3,$4,'prepared',1)", b.Key.Namespace, b.Key.KeyRef, b.Key.SecretVersionRef, b.Key.Identity)
	exec("UPDATE audit_key_registry_keys SET state='active',revision=2 WHERE namespace=$1 AND key_ref=$2", b.Key.Namespace, b.Key.KeyRef)
	exec(`INSERT INTO audit_key_registry_history(namespace,key_ref,revision,from_state,state,operation_ref,actor_ref,policy_ref,policy_revision,mapping_ref,authorization_ref,activation_ref,drain_ref,dependency_ref,copy_lifetime_ref,disposal_ref,unused_ref,changed_at) VALUES($1,$2,2,'prepared','active','fixture-activate','fixture-installer',$3,1,'fixture-mapping','fixture-authorization','fixture-active','','','','','',clock_timestamp())`, b.Key.Namespace, b.Key.KeyRef, b.Key.PolicyRef)
	return raw, digest
}

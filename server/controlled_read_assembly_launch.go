package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"net/http"
	"strconv"
	"time"
)

// 설정은 실제 공급 어댑터의 파일 위치이며 JSON 입력으로 RootTrust를 만들지 않는다.
type FirstDiagnosisControlledSupervisor interface {
	ControlledReadLocalSupervisor
	Attach(*Server) error
}

type FirstDiagnosisControlledStartOptions struct {
	BindingFile, MetadataFile, SnapshotDir, StateDir, RegistryFile string
	SkillDir, DataDir, KeyDir, LocalLLMManifestPath                string
	Supervisor                                                     FirstDiagnosisControlledSupervisor
	Shutdown                                                       ControlledReadAssemblyShutdown
	CleanupTimeout                                                 time.Duration
}
type FirstDiagnosisControlledRuntime struct {
	assembly         *ControlledReadAssemblyRuntime
	local            *ControlledReadLocalHost
	taskID, intentID int64
	snapshotRef      string
}

func StartFirstDiagnosisControlled(ctx context.Context, manager *Manager, authorityDB *sql.DB, o FirstDiagnosisControlledStartOptions) (*FirstDiagnosisControlledRuntime, error) {
	if ctx == nil || ctx.Err() != nil || manager == nil || manager.pg == nil || authorityDB == nil || nilFinalizationValue(o.Supervisor) || o.CleanupTimeout <= 0 {
		return nil, ErrControlledReadHostConfiguration
	}
	limits := auditlog.ExtensionLimits{MaxBytes: 8 << 20, MaxEvents: 4096, Timeout: 10 * time.Second}
	local, err := AcquireControlledReadLocalHost(ctx, manager.pg.DB, authorityDB, ControlledReadLocalHostConfig{BindingFile: o.BindingFile, Supervisor: o.Supervisor, Limits: limits})
	if err != nil {
		return nil, err
	}
	runtime := &FirstDiagnosisControlledRuntime{local: local}
	fail := func(cause error) (*FirstDiagnosisControlledRuntime, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.CleanupTimeout)
		defer cancel()
		if closeErr := runtime.Close(cleanup); closeErr != nil {
			return runtime, controlledReadAssemblyStageError{"launch_cleanup", closeErr}
		}
		return nil, cause
	}
	for _, path := range []string{o.LocalLLMManifestPath, o.KeyDir + "/jwt.key"} {
		body, readErr := privateinput.ReadFile(path, 1<<20)
		if readErr != nil {
			return fail(ErrControlledReadHostConfiguration)
		}
		digest := sha256.Sum256(body)
		clear(body)
		if local.AuthorizeInput(ctx, path, digest) != nil {
			return fail(ErrControlledReadHostConfiguration)
		}
	}
	if localWithin(local.binding.Keys.Path, o.DataDir) || localWithin(o.DataDir, local.binding.Keys.Path) {
		return fail(ErrControlledReadHostConfiguration)
	}
	if local.Directory() != o.StateDir {
		return fail(ErrControlledReadHostConfiguration)
	}
	doc, inputs, err := LoadControlledReadAssemblyInputs(ctx, manager.pg.DB, local, o.MetadataFile)
	if err != nil {
		return fail(err)
	}
	runtime.taskID, _ = strconv.ParseInt(doc.Source.Expected.Scope.TaskRef, 10, 64)
	runtime.intentID = doc.Source.Expected.IntentID
	runtime.snapshotRef = doc.SnapshotRef
	if CheckControlledReadAssemblyInputFiles(ctx, local, o.SnapshotDir, doc.SnapshotRef, o.RegistryFile) != nil {
		return fail(ErrControlledReadHostConfiguration)
	}
	// 기동 전 검사는 등록 시의 동일 객체 검사를 대신하지 않는다.
	registry, err := loadControlledAssemblyRegistry(o.SnapshotDir, o.RegistryFile)
	if err != nil || !sameControlledAssemblySnapshot(registry, doc.Source) {
		return fail(err)
	}
	p := ControlledReadAssemblyPreparation{SnapshotRef: doc.SnapshotRef, TargetOrigin: doc.TargetOrigin, ModelTag: doc.Source.ModelTag, Metadata: doc.Source, KG: registry}
	if CheckControlledReadAssemblyPreparation(ctx, manager, o.SnapshotDir, p) != nil {
		return fail(ErrControlledReadHostConfiguration)
	}
	sources, err := local.Sources()
	if err != nil {
		return fail(err)
	}
	runtime.assembly, err = StartControlledReadAssembly(ctx, manager, ControlledReadAssemblyStartOptions{Host: ControlledReadAssemblyHostConfig{Sources: sources, Publisher: local.PublisherFactory(), AuditSource: local.AuditSourceOptions(), Inputs: inputs, FinalizationScope: local.FinalizationScopes(), FixtureRegistries: map[string]string{doc.SnapshotRef: o.RegistryFile}, LocalLLMManifestPath: o.LocalLLMManifestPath}, SkillDir: o.SkillDir, DataDir: o.DataDir, KeyDir: o.KeyDir, SnapshotDir: o.SnapshotDir, InputAuthority: local, Shutdown: o.Shutdown, CleanupTimeout: o.CleanupTimeout})
	if err != nil {
		if runtime.assembly != nil {
			return runtime, err
		}
		return fail(err)
	}
	if err = o.Supervisor.Attach(runtime.assembly.server); err != nil {
		// admission 전 전용 생성자는 실행 loop를 시작하지 않았고 Handler도 아직 공개하지 않았다.
		runtime.assembly.phase = 3
		return fail(err)
	}
	task, ok := manager.Task(strconv.FormatInt(runtime.taskID, 10))
	if !ok || task == nil || task.ExpID != doc.Source.ExplorationID || task.Store == nil || task.Store.ID() != doc.Source.ExplorationID || runtime.assembly.server.bindFirstDiagnosisModel(ctx, task) != nil || !runtime.assembly.server.engine.ReadyFor(task) {
		return fail(ErrControlledReadHostConfiguration)
	}

	return runtime, nil
}
func (r *FirstDiagnosisControlledRuntime) Handler() http.Handler {
	if r == nil || r.assembly == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			http.Error(w, "첫 진단을 사용할 수 없습니다", http.StatusServiceUnavailable)
		})
	}
	return r.assembly.Handler()
}
func (r *FirstDiagnosisControlledRuntime) Close(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrControlledReadHostConfiguration
	}
	var resultErr error
	if r.assembly != nil {
		resultErr = r.assembly.Close(ctx)
		if resultErr != nil && !r.assembly.closed.Load() {
			return resultErr
		}
	}
	if r.local != nil {
		return errors.Join(resultErr, r.local.Close(ctx))
	}
	return resultErr
}

func (r *FirstDiagnosisControlledRuntime) TaskID() int64 {
	if r == nil {
		return 0
	}
	return r.taskID
}
func (r *FirstDiagnosisControlledRuntime) IntentID() int64 {
	if r == nil {
		return 0
	}
	return r.intentID
}
func (r *FirstDiagnosisControlledRuntime) SnapshotRef() string {
	if r == nil {
		return ""
	}
	return r.snapshotRef
}

// 판정 성공 여부와 자원 반환 완료 여부를 분리한다.
func (r *FirstDiagnosisControlledRuntime) ResourcesClosed() bool {
	if r == nil || r.local == nil || r.assembly != nil && !r.assembly.closed.Load() {
		return false
	}
	select {
	case <-r.local.cleanupDone:
		return r.local.cleanupErr == nil
	default:
		return false
	}
}

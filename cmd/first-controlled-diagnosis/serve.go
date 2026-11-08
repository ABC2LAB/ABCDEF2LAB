package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/server"
)

type launchRuntime interface {
	Handler() http.Handler
	Close(context.Context) error
	TaskID() int64
}

type launchedDiagnosis struct {
	manager     *server.Manager
	authorityDB *sql.DB
	supervisor  *server.FirstDiagnosisSupervisor
	runtime     launchRuntime
}

func (d *launchedDiagnosis) Close(ctx context.Context) error {
	if d == nil || ctx == nil {
		return errPreparation
	}
	var resultErr error
	if d.runtime != nil {
		if d.supervisor != nil {
			resultErr = closeAndSaveDiagnosis(ctx, d.runtime, d.supervisor.PersistRunOutcome)
		} else {
			resultErr = d.runtime.Close(ctx)
		}
		if resultErr != nil {
			status, ok := d.runtime.(interface{ ResourcesClosed() bool })
			if !ok || !status.ResourcesClosed() {
				return resultErr
			}
		}
	}
	if d.authorityDB != nil {
		if err := d.authorityDB.Close(); err != nil {
			return errors.Join(resultErr, errPreparation)
		}
		d.authorityDB = nil
	}
	if d.manager != nil {
		d.manager.Close()
		d.manager = nil
	}
	return resultErr
}

func startLaunch(ctx context.Context, c launchConfig) (*launchedDiagnosis, error) {
	if checkLaunchFiles(c) != nil {
		return nil, errPreparation
	}
	pool, err := openLaunchDatabase(ctx, c.RuntimeDSNFile)
	if err != nil {
		return nil, errPreparation
	}
	if checkLaunchDatabaseRole(ctx, pool, c) != nil || checkLaunchProfile(ctx, pool, c) != nil {
		_ = pool.Close()
		return nil, errPreparation
	}
	manager, err := server.NewFirstDiagnosisManager(ctx, c.DataDir, pool)
	if err != nil {
		_ = pool.Close()
		return nil, errPreparation
	}
	d := &launchedDiagnosis{manager: manager}
	fail := func(cause error) (*launchedDiagnosis, error) {
		cleanup, cancel := launchCleanupContext(ctx, c)
		defer cancel()
		if closeErr := d.Close(cleanup); closeErr != nil {
			return d, errPreparation
		}
		return nil, cause
	}
	d.supervisor, err = server.NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		return fail(err)
	}
	if c.Mode == "general_http" {
		o := c.HTTP
		httpRuntime, startErr := server.StartFirstDiagnosisHTTP(ctx, manager, server.FirstDiagnosisHTTPStartOptions{Host: server.FirstDiagnosisHTTPHostConfig{PolicyFile: o.PolicyFile, AuthorityFile: o.AuthorityFile, AuthorityRoot: o.AuthorityRoot}, SkillDir: c.SkillDir, DataDir: c.DataDir, KeyDir: c.KeyDir, LocalLLMManifestPath: c.LocalLLMManifestPath, Supervisor: d.supervisor, Shutdown: d.supervisor.Shutdown(), CleanupTimeout: time.Duration(c.ShutdownTimeoutSeconds) * time.Second})
		err = startErr
		if httpRuntime != nil {
			d.runtime = httpRuntime
		}
	} else {
		o := c.Registered
		d.authorityDB, err = openLaunchDatabase(ctx, o.AuthorityDSNFile)
		if err != nil {
			return fail(err)
		}
		controlledRuntime, startErr := server.StartFirstDiagnosisControlled(ctx, manager, d.authorityDB, server.FirstDiagnosisControlledStartOptions{BindingFile: o.HostBindingFile, MetadataFile: o.MetadataFile, SnapshotDir: o.SnapshotDir, StateDir: o.StateDir, RegistryFile: o.RegistryFile, SkillDir: c.SkillDir, DataDir: c.DataDir, KeyDir: c.KeyDir, LocalLLMManifestPath: c.LocalLLMManifestPath, Supervisor: d.supervisor, Shutdown: d.supervisor.Shutdown(), CleanupTimeout: time.Duration(c.ShutdownTimeoutSeconds) * time.Second})
		err = startErr
		if controlledRuntime != nil {
			d.runtime = controlledRuntime
		}
	}
	if err != nil {
		// 부분 handle은 이미 마감을 시도했을 수 있으므로 자동 반복하거나 소유를 버리지 않는다.
		if d.runtime != nil {
			return d, errPreparation
		}
		return fail(err)
	}
	if d.runtime != nil && checkLaunchTaskProfile(ctx, pool, d.runtime.TaskID(), c) != nil {
		return fail(errPreparation)
	}
	if d.runtime == nil || d.runtime.TaskID() <= 0 {
		return fail(errPreparation)
	}
	return d, nil
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer, execute bool) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected launch configuration")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "기동이 취소되었습니다")
		return 1
	}
	c, err := readLaunchConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, "실행 설정을 사용할 수 없습니다")
		return 1
	}
	d, err := startLaunch(ctx, c)
	if err != nil {
		fmt.Fprintln(stderr, "엄격 기동을 완료할 수 없습니다. 부분 자원과 마감 결과를 확인해야 합니다")
		return 1
	}
	listener, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		cleanup, cancel := launchCleanupContext(ctx, c)
		closeErr := d.Close(cleanup)
		cancel()
		if closeErr != nil {
			fmt.Fprintln(stderr, "기동 실패 뒤 마감을 확인할 수 없습니다")
		} else {
			fmt.Fprintln(stderr, "수신 주소를 사용할 수 없습니다")
		}
		return 1
	}
	httpServer := &http.Server{Handler: d.runtime.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	serving := make(chan error, 1)
	go func() { serving <- httpServer.Serve(listener) }()
	if json.NewEncoder(stdout).Encode(map[string]any{"service_ready": true, "mode": c.Mode, "diagnosis": "NOT_RUN"}) != nil {
		_ = listener.Close()
		cleanup, cancel := launchCleanupContext(ctx, c)
		_ = d.Close(cleanup)
		cancel()
		return 1
	}
	var runErr error
	if execute {
		var intentID int64
		var snapshot string
		if registered, ok := d.runtime.(*server.FirstDiagnosisControlledRuntime); ok {
			intentID = registered.IntentID()
			snapshot = registered.SnapshotRef()
		}
		finished := make(chan error, 1)
		go func() {
			finished <- d.supervisor.Run(ctx, strconv.FormatInt(d.runtime.TaskID(), 10), intentID, snapshot)
		}()
		select {
		case runErr = <-finished:
		case <-ctx.Done():
			runErr = ctx.Err()
		case serveErr := <-serving:
			if !errors.Is(serveErr, http.ErrServerClosed) {
				runErr = errPreparation
			}
		}
	} else {
		select {
		case <-ctx.Done():
		case serveErr := <-serving:
			if !errors.Is(serveErr, http.ErrServerClosed) {
				runErr = errPreparation
			}
		}
	}
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	httpErr := httpServer.Shutdown(cleanup)
	closeErr := d.Close(cleanup)
	if runErr != nil || httpErr != nil || closeErr != nil {
		fmt.Fprintln(stderr, "진단 또는 마감의 성공을 확인할 수 없습니다")
		return 1
	}
	if execute {
		report := map[string]any{"service_ready": false, "mode": c.Mode, "execution": "PASS", "closure": "PASS"}
		if c.Mode == "registered_get" {
			report["storage"] = "PASS"
		} else {
			report["e3"] = "NOT_APPLICABLE"
		}
		if json.NewEncoder(stdout).Encode(report) != nil {
			return 1
		}
	}
	return 0
}

// 자원 마감과 결과 실패를 구별하고 DB를 닫기 전에 저장 실패를 반환한다.
func closeAndSaveDiagnosis(ctx context.Context, runtime launchRuntime, persist func(context.Context, error) error) error {
	resultErr := runtime.Close(ctx)
	closureErr := resultErr
	if status, ok := runtime.(interface{ ResourcesClosed() bool }); ok && status.ResourcesClosed() {
		closureErr = nil
	}
	return errors.Join(resultErr, persist(ctx, closureErr))
}

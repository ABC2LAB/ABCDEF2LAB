//go:build linux

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
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

func urlOperatorUID() uint32 { return uint32(os.Geteuid()) }

func (b *urlPreparationRuntime) issue(l *urlLoadedProfile, jobID string, actual server.FirstDiagnosisPreparedTask) (launchConfig, error) {
	c := l.launch
	if l.current() != nil || !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID || jobID == "." || jobID == ".." || actual.TaskID <= 0 || actual.ExplorationID <= 0 || checkDirectory(b.config.RunsDir, true) != nil {
		return c, errPreparation
	}
	parent, err := os.OpenRoot(b.config.RunsDir)
	if err != nil {
		return c, errPreparation
	}
	defer parent.Close()
	// 중복 회차·부분 준비의 디렉터리를 덮어쓰거나 삭제하여 재발급하지 않는다.
	if parent.Mkdir(jobID, 0700) != nil {
		return c, errPreparation
	}
	root, err := parent.OpenRoot(jobID)
	if err != nil {
		return c, errPreparation
	}
	defer root.Close()
	if root.Mkdir("state", 0700) != nil {
		return c, errPreparation
	}
	write := func(name string, raw []byte) error {
		if !managementReference.MatchString(name) {
			return errPreparation
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errPreparation
		}
		_, writeErr := f.Write(raw)
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return errPreparation
		}
		return nil
	}
	for name, raw := range l.materials {
		if write(name, raw) != nil {
			return c, errPreparation
		}
	}
	runRoot := filepath.Join(b.config.RunsDir, jobID)
	policy := l.policy
	now := time.Now().UTC()
	policy.RunRef = "run-" + jobID
	policy.BudgetRef = "budget-" + jobID
	policy.PolicyRef = "policy-" + jobID
	policy.StateDir = filepath.Join(runRoot, "state")
	policy.ValidFrom = now
	policy.ValidUntil = now.Add(time.Duration(l.profile.Budget.DurationSeconds+int64(c.ShutdownTimeoutSeconds)) * time.Second)
	if policy.ValidUntil.After(l.profile.ValidUntil) {
		policy.ValidUntil = l.profile.ValidUntil
	}
	if !policy.ValidUntil.After(now) || policy.ValidUntil.Sub(now) > 15*time.Minute {
		return c, errPreparation
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return c, errPreparation
	}
	if write("policy.json", raw) != nil {
		return c, errPreparation
	}
	approval := server.FirstDiagnosisHTTPApproval{Format: server.FirstDiagnosisHTTPAuthorityFormat, ApprovalRef: "approval-" + jobID, OperatorUID: urlOperatorUID(), TaskID: actual.TaskID, ExplorationID: actual.ExplorationID, PolicySHA256: urlSHA(raw), ValidFrom: policy.ValidFrom, ValidUntil: policy.ValidUntil}
	raw, err = json.Marshal(approval)
	if err != nil || write("authority.json", raw) != nil {
		return c, errPreparation
	}
	dir, err := root.Open(".")
	if err != nil {
		return c, errPreparation
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	parentDir, err := parent.Open(".")
	if err != nil {
		return c, errPreparation
	}
	parentSync := parentDir.Sync()
	parentClose := parentDir.Close()
	if syncErr != nil || closeErr != nil || parentSync != nil || parentClose != nil || l.current() != nil {
		return c, errPreparation
	}
	c.HTTP = &httpLaunch{PolicyFile: filepath.Join(runRoot, "policy.json"), AuthorityFile: filepath.Join(runRoot, "authority.json"), AuthorityRoot: runRoot}
	return c, nil
}

func runURLPreparationService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("prepare-service", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("installation", "", "protected installation delegation")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, "prepare-service -installation <protected-file>")
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		return 1
	}
	backend, err := newURLPreparationRuntime(ctx, *path)
	if err != nil {
		fmt.Fprintln(stderr, "보호된 준비 설치를 확인할 수 없습니다")
		return 1
	}
	service, err := diagnosisprep.New(ctx, backend.config.StateDir, backend)
	if err != nil {
		fmt.Fprintln(stderr, "영속 준비 상태를 확인할 수 없습니다")
		return 1
	}
	backend.endpoint = service
	ipc, err := diagnosisprep.ListenUnix(ctx, backend.config.SocketPath, service)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = service.Shutdown(cleanup)
		cancel()
		fmt.Fprintln(stderr, "비공개 준비 연결을 열 수 없습니다")
		return 1
	}
	listener, err := net.Listen("tcp", backend.config.ReportListenAddr)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = ipc.Close(cleanup)
		_ = service.Shutdown(cleanup)
		cancel()
		return 1
	}
	httpServer := &http.Server{Handler: backend, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	serving := make(chan error, 1)
	go func() { serving <- httpServer.Serve(listener) }()
	outputErr := json.NewEncoder(stdout).Encode(map[string]any{"service_ready": true, "mode": "url_preparation", "execution": "EXPLICIT_START_REQUIRED", "storage": "durable"})
	var serveErr error
	if outputErr == nil {
		select {
		case <-ctx.Done():
		case serveErr = <-serving:
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	// 새 명령 차단 뒤 실제 실행을 drain하고 마지막으로 보고서 DB를 닫는다.
	closeErr := ipc.Close(cleanup)
	closeErr = errors.Join(closeErr, service.Shutdown(cleanup))
	httpErr := httpServer.Shutdown(cleanup)
	if httpErr == nil {
		closeErr = errors.Join(closeErr, backend.closeReports())
	}
	if outputErr != nil || serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) || closeErr != nil || httpErr != nil {
		fmt.Fprintln(stderr, "준비 서비스 마감 결과를 확인할 수 없습니다")
		return 1
	}
	return 0
}

func runURLDiagnosisAPI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve-url-api", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	installationPath := flags.String("installation", "", "protected installation metadata")
	path := flags.String("config", "", "protected API launch configuration")
	productUI := flags.Bool("product-ui", false, "serve the installed Workbench")
	credentialDSN := flags.String("credential-dsn-file", "", "protected model-key writer DSN")
	socket := flags.String("socket", "", "installed private preparation socket")
	origin := flags.String("runtime-api-origin", "", "installed loopback report origin")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" || *socket == "" || *origin == "" || *installationPath == "" {
		fmt.Fprintln(stderr, "serve-url-api -installation <protected-installation> -config <protected-launch> -socket <private-socket> -runtime-api-origin <loopback-origin>")
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		return 1
	}
	installed, installationPin, installErr := readURLInstallation(*installationPath)
	if installErr != nil || installed.APILaunch.File != *path || installed.SocketPath != *socket || *origin != "http://"+installed.ReportListenAddr {
		fmt.Fprintln(stderr, "API 설치 결속을 확인할 수 없습니다")
		return 1
	}
	raw, apiPin, err := urlReadPin(*path, maxLaunchBytes, false)
	defer clear(raw)
	if err != nil || apiPin.digest != installed.APILaunch.SHA256 {
		return 1
	}
	c, err := parseLaunchConfig(raw)
	if err != nil || apiPin.check() != nil || installationPin.check() != nil {
		return 1
	}

	// API 프로세스는 manifest·Planner·실행 권위를 열지 않는다.
	endpoint, err := diagnosisprep.NewUnixClient(*socket)
	if err != nil {
		fmt.Fprintln(stderr, "비공개 준비 연결을 확인할 수 없습니다")
		return 1
	}
	defer endpoint.Close()
	dsnRaw, dsnPin, err := urlReadPin(c.RuntimeDSNFile, 16<<10, false)
	clear(dsnRaw)
	if err != nil {
		return 1
	}
	pool, err := urlOpenDatabase(ctx, installed, dsnPin)
	if err != nil {
		return 1
	}
	defer pool.Close()
	var handler http.Handler
	if *productUI {
		var writer *sql.DB
		models := []server.ProductModelBinding{}
		if *credentialDSN != "" {
			writerRaw, writerPin, writerErr := urlReadPin(*credentialDSN, 16<<10, false)
			clear(writerRaw)
			if writerErr != nil {
				return 1
			}
			writer, err = urlOpenDatabase(ctx, installed, writerPin)
			if err != nil {
				return 1
			}
			defer writer.Close()
			for _, p := range installed.Profiles {
				raw, pin, e := urlReadPin(p.Launch.File, maxLaunchBytes, false)
				if e != nil {
					return 1
				}
				launch, e := parseLaunchConfig(raw)
				clear(raw)
				if e != nil || pin.digest != p.Launch.SHA256 {
					return 1
				}
				if launch.ModelExecutionMode == "openrouter" {
					models = append(models, server.ProductModelBinding{ProfileRef: p.ID, ProfileID: launch.ModelProfileID, Model: launch.ModelRef, Check: func() error {
						if installationPin.check() != nil || writerPin.check() != nil {
							return errPreparation
						}
						return pin.check()
					}})
				}
			}
		}
		handler, err = server.NewProductDiagnosisHandler(ctx, pool, c.KeyDir, c.DataDir, endpoint, *origin, writer, models)
	} else {
		handler, err = server.NewURLDiagnosisManagementHandler(ctx, pool, c.KeyDir, c.DataDir, endpoint, *origin)
	}
	if err != nil {
		return 1
	}
	if apiPin.check() != nil || installationPin.check() != nil {
		return 1
	}
	listener, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		return 1
	}
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	serving := make(chan error, 1)
	go func() { serving <- httpServer.Serve(listener) }()
	outputErr := json.NewEncoder(stdout).Encode(map[string]any{"service_ready": true, "mode": "url_diagnosis_api", "storage": "private_service_durable"})
	var serveErr error
	if outputErr == nil {
		select {
		case <-ctx.Done():
		case serveErr = <-serving:
		}
	}
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	if httpServer.Shutdown(cleanup) != nil || outputErr != nil || serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return 1
	}
	return 0
}

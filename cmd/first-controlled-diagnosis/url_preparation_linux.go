//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/server"
)

func urlOperatorUID() uint32 { return uint32(os.Geteuid()) }

func (b *urlPreparationRuntime) issue(l *urlLoadedProfile, jobID string, actual server.FirstDiagnosisPreparedTask) (launchConfig, error) {
	return b.issueForPurpose(l, jobID, actual, false)
}

// issueSelectedCandidate mints a fresh E② authority for an approved non-IDOR
// follow-up. An active declaration on the original target must never grant
// E③ capabilities to this separate task.
func (b *urlPreparationRuntime) issueSelectedCandidate(l *urlLoadedProfile, jobID string, actual server.FirstDiagnosisPreparedTask) (launchConfig, error) {
	if l == nil || l.productTarget == nil || l.productRegistry == nil || l.productConnection == nil ||
		l.policy.Workflow != nil || l.policy.E3WriteConsent || l.policy.E3Resettable ||
		len(l.policy.Rules) < 1 || len(l.policy.Rules) > 4 {
		return launchConfig{}, errPreparation
	}
	if l.policy.SelectedAccountRef == "" {
		if l.policy.Auth != nil || l.policy.HeaderMode != e2proxy.HeaderPreserve || len(l.productAccounts) != 0 {
			return launchConfig{}, errPreparation
		}
	} else if l.policy.Auth == nil || len(l.policy.Auth.Accounts) != 1 || len(l.productAccounts) != 1 ||
		l.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough ||
		server.CandidateAccountRevision(l.policy.Auth.Accounts[0].Label, l.policy.Auth.Accounts[0].Credential.SHA256) != l.policy.SelectedAccountRef ||
		l.productAccounts[0].Label != l.policy.Auth.Accounts[0].Label || l.productAccounts[0].CredentialSHA256 != l.policy.Auth.Accounts[0].Credential.SHA256 {
		return launchConfig{}, errPreparation
	}
	ordinary, loginRules := 0, 0
	for _, rule := range l.policy.Rules {
		if rule.Ref == "" || rule.PathScopeRef == "" || rule.Credentials != nil || len(rule.Methods) != 1 ||
			rule.Origin != l.productTarget.Target.Origin || rule.ConnectIP != l.productConnection.IP ||
			rule.TLSSPKISHA256 != l.productConnection.SPKI {
			return launchConfig{}, errPreparation
		}
		if rule.Purpose == e2proxy.PurposeAuthPrep {
			if l.policy.Auth == nil || l.policy.Auth.Login == nil || rule.Ref != l.policy.Auth.RuleRef || rule.PathPrefix != l.policy.Auth.Login.Path ||
				rule.Methods[0] != l.policy.Auth.Login.Method || rule.PathMatch != "" {
				return launchConfig{}, errPreparation
			}
			loginRules++
		} else if rule.Purpose == "" && rule.PathMatch == "exact" &&
			(rule.Methods[0] == "GET" || rule.Methods[0] == "HEAD") {
			ordinary++
		} else {
			return launchConfig{}, errPreparation
		}
	}
	if ordinary < 1 || ordinary > 3 || (l.policy.Auth == nil && loginRules != 0) || (l.policy.Auth != nil && loginRules != 1) {
		return launchConfig{}, errPreparation
	}
	return b.issueForPurpose(l, jobID, actual, true)
}

func (b *urlPreparationRuntime) issueForPurpose(l *urlLoadedProfile, jobID string, actual server.FirstDiagnosisPreparedTask, selectedCandidate bool) (launchConfig, error) {
	c := l.launch
	if l.productTarget != nil && (l.productConnection == nil || l.productConnection.jobID != jobID) {
		return c, errPreparation
	}
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
	write := func(name string, raw []byte) error { return writeRootPrivateFile(root, name, raw) }
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
	var ok bool
	if policy.ValidUntil, ok = urlAuthorityUntil(now, l.profile.Budget.DurationSeconds, int64(c.ShutdownTimeoutSeconds), l.profile.ValidUntil); !ok {
		return c, errPreparation
	}
	// E③ 정책 선언 동결(결정 §4.2): 지금 대상 판에 유효한 선언이 있으면 그 정본 바이트와 정책 출처를 회차 디렉터리에
	// 쓴다. 선언이 없으면 쓰지 않고, 그 회차의 확정 단계는 꺼진다.
	var e3Declaration e3contract.Declaration
	var e3Declared bool
	if !selectedCandidate && l.productTarget != nil && l.productRegistry != nil {
		if declaration, active := l.productRegistry.activeDeclaration(*l.productTarget); active {
			e3Declaration, e3Declared = declaration, true
			frozen, frozenPolicy, err := freezeDeclaration(declaration, jobID, policy.ValidFrom, policy.ValidUntil,
				l.productTarget.Target.ApprovedAt, l.productTarget.Target.ExpiresAt)
			if err != nil || write(productFrozenDeclarationFile, frozen) != nil || write(productFrozenPolicyFile, frozenPolicy) != nil {
				return c, errPreparation
			}
		}
		// E③ 세대·인스턴스 참조(v3 계약 §6): 지금 대상 상태와 이 회차로 발급해 동결한다(회차 사이에는 재사용하지 않음).
		// 확정 단계를 시작할 때 리셋 게이트가 지금 대상과 다시 대조한다. 대상 상태를 모르면 새 난수 참조라 확정 단계는
		// target_changed로 멈춘다.
		refs, err := l.productRegistry.issueTargetRefs(l.productTarget.Target.ID, jobID, rand.Reader)
		if err != nil {
			return c, errPreparation
		}
		raw, err := refs.marshal()
		if err != nil || write(productTargetRefsFile, raw) != nil {
			return c, errPreparation
		}
	}
	// 목적 규칙은 동결한 선언과 승인 대상에서만 파생한다. 몫은 실행 정책과 권위 digest에 함께 결속한다.
	e3Types := 0
	if !selectedCandidate && e3Declared && c.E2HostSocket != "" {
		// 회차 시작 때 재확인한 pin을 써서 모델 규칙과 E③ 규칙의 연결 주소가 정확히 같다.
		e3Target := *l.productTarget
		e3Target.SelectedIP = l.productConnection.IP
		e3Target.Target.TLSSPKISHA256 = l.productConnection.SPKI
		policy, e3Types = productE3PolicyRules(policy, e3Target, e3Declaration)
	}
	policy = withE3ConfirmationShare(policy, l.profile.Budget.DurationSeconds, e3Types)
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
	c.HTTP = &httpLaunch{PolicyFile: filepath.Join(runRoot, "policy.json"), AuthorityFile: filepath.Join(runRoot, "authority.json"), AuthorityRoot: runRoot, ExternalTestAccounts: l.externalTestAccounts}
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
	quiet, err := releaseQuiet(os.Getenv(releaseQuietEnv))
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
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
	if err := service.SetReleaseQuiet(quiet); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = service.Shutdown(cleanup)
		cancel()
		fmt.Fprintln(stderr, "멈춘 작업 해제 기준을 확인할 수 없습니다")
		return 1
	}
	backend.endpoint = service
	var ipcEndpoint diagnosisprep.Endpoint = service
	if backend.product != nil {
		ipcEndpoint = productPreparationEndpoint{base: service, product: backend.product, backend: backend}
		defer backend.product.targets.Close()
	}
	ipc, err := diagnosisprep.ListenUnix(ctx, backend.config.SocketPath, ipcEndpoint)
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
	if backend.product != nil && backend.recoverCandidateVerifications(ctx) != nil {
		log.Print("[candidate-verification] recovery state unavailable; no execution replayed")
	}
	outputErr := json.NewEncoder(stdout).Encode(map[string]any{"service_ready": true, "mode": "url_preparation", "execution": "EXPLICIT_START_REQUIRED", "storage": "durable"})
	var serveErr error
	restart := false
	if outputErr == nil {
		select {
		case <-ctx.Done():
		case serveErr = <-serving:
		case <-service.RestartRequested():
			// 운영자 해제(C04): 감사 기록이 저널에 남았다. 새 명령을 막고 정상 종료한다. 미마감 실행이 있으니
			// Shutdown은 저널 잠금을 프로세스 종료까지 쥐고, 다시 뜬 init이 그 잠금을 얻는 것이 종료 증거가 된다.
			restart = true
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	// 새 명령 차단 뒤 실제 실행을 drain하고 마지막으로 보고서 DB를 닫는다.
	closeErr := ipc.Close(cleanup)
	closeErr = errors.Join(closeErr, service.Shutdown(cleanup))
	httpErr := httpServer.Shutdown(cleanup)
	if httpErr == nil {
		closeErr = errors.Join(closeErr, backend.waitCandidateWorkers(cleanup))
		closeErr = errors.Join(closeErr, backend.closeReports())
	}
	if restart {
		if closeErr != nil || httpErr != nil {
			log.Print("운영자 해제: 마감을 다 확인하지 못한 채 종료합니다(새 init이 저널 잠금으로 종료를 확인합니다)")
		}
		fmt.Fprintln(stderr, "운영자 해제: 준비 서비스를 정상 종료했습니다. 컨테이너 재시작 정책이 다시 띄웁니다")
		return exitRestartRequested
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
			for _, p := range productModels(installed) {
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
	lost := watchSharedNetwork(ctx, sharedNetworkCheckInterval, net.Interfaces, preparationAnswers(endpoint))
	networkLost := false
	if outputErr == nil {
		select {
		case <-ctx.Done():
		case serveErr = <-serving:
		case <-lost:
			networkLost = true
		}
	}
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	shutdownErr := httpServer.Shutdown(cleanup)
	if networkLost {
		return exitSharedNetworkLost
	}
	if shutdownErr != nil || outputErr != nil || serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return 1
	}
	return 0
}

// urlAuthorityUntil은 회차 권한의 끝 시각이다: 실행 시간 + 정리 시간이고, 설치 위임 기한을 넘지 않는다. 구간이 비거나 일반
// HTTP 권한 상한(server.MaxFirstDiagnosisHTTPAuthority)을 넘으면 거부한다.
func urlAuthorityUntil(now time.Time, runSeconds, shutdownSeconds int64, delegationUntil time.Time) (time.Time, bool) {
	until := now.Add(time.Duration(runSeconds+shutdownSeconds) * time.Second)
	if until.After(delegationUntil) {
		until = delegationUntil
	}
	return until, until.After(now) && until.Sub(now) <= server.MaxFirstDiagnosisHTTPAuthority
}

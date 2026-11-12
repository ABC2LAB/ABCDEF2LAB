package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/server"
)

// gatewayRunner는 기존 엄격 기동 결과만 감싼다. 브라우저 입력·경로를 실행하지 않는다.
type gatewayRunner struct {
	launch     *launchedDiagnosis
	httpServer *http.Server
	runErr     error
}

func (r *gatewayRunner) Run(ctx context.Context) error {
	var intent int64
	var snapshot string
	if registered, ok := r.launch.runtime.(*server.FirstDiagnosisControlledRuntime); ok {
		intent, snapshot = registered.IntentID(), registered.SnapshotRef()
	}
	r.runErr = r.launch.supervisor.Run(ctx, strconv.FormatInt(r.launch.runtime.TaskID(), 10), intent, snapshot)
	return r.runErr
}
func (r *gatewayRunner) Close(ctx context.Context) (diagnosis.Closure, error) {
	// 실행 자체의 실패는 실제 drain·저장 실패와 구별한다.
	httpErr := r.httpServer.Shutdown(ctx)
	runOutcome := r.launch.runtime.Close(ctx)
	drained := runOutcome == nil
	if status, ok := r.launch.runtime.(interface{ ResourcesClosed() bool }); ok {
		drained = status.ResourcesClosed()
	}
	closureErr := runOutcome
	if drained {
		closureErr = nil
	}
	stored, saveErr := r.launch.supervisor.PersistRunOutcomeStored(ctx, closureErr)
	result := diagnosis.Closure{DrainComplete: drained && httpErr == nil, ResultReady: stored && saveErr == nil, Result: r.launch.supervisor.RunResult()}
	if !drained {
		return result, errors.Join(httpErr, runOutcome, saveErr)
	}
	if r.launch.authorityDB != nil {
		if err := r.launch.authorityDB.Close(); err != nil {
			result.DrainComplete = false
			return result, errors.Join(httpErr, saveErr, err)
		}
		r.launch.authorityDB = nil
	}
	if r.launch.manager != nil {
		r.launch.manager.Close()
		r.launch.manager = nil
	}
	// 정확히 같은 Run 오류만 별도 Run 반환값에서 보존한다. 다른 결과 검증 오류는 숨기지 않는다.
	if runOutcome != nil && (!errors.Is(runOutcome, server.ErrFirstDiagnosisOutcome) || !onlyRunOutcome(runOutcome, r.runErr)) {
		return result, errors.Join(httpErr, saveErr, runOutcome)
	}
	return result, errors.Join(httpErr, saveErr)
}

// runGateway는 운영자가 미리 실행한 독립 프로세스의 비공개 pipe 명령 루프다.
// HTTP 요청으로 프로세스를 만들지 않고 보호 config 경로도 pipe DTO로 받지 않는다.
func runGateway(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) (code int) {
	restorePipeSignal := handleGatewayBrokenPipe()
	defer restorePipeSignal()
	flags := flag.NewFlagSet("gateway", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected launch configuration")
	preparation := flags.String("preparation-ref", "", "operator-issued preparation reference")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" || *preparation == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	// 참조 형식은 준비 전 검사하여 잘못된 요청이 단회 권위를 소비하지 않게 한다.
	if _, err := diagnosis.NewPrepared(ctx, "1", *preparation, unavailableGatewayRunner{}, time.Second); err != nil {
		fmt.Fprintln(stderr, "실행 서비스 참조를 확인할 수 없습니다")
		return 2
	}
	c, err := readLaunchConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, "실행 설정을 사용할 수 없습니다")
		return 1
	}
	// v1 결과는 일반 HTTP 종료 기록이다. 등록 GET의 KG/E③ 결과 계약은 아직 없다.
	// 로그·DB·단회 권위 준비보다 먼저 거부하며 기존 run/serve 모드는 변경하지 않는다.
	if c.Mode != "general_http" {
		fmt.Fprintln(stderr, "gateway_registered_get_unsupported")
		return 2
	}
	ctx, diagnostics, err := beginLaunchDiagnostics(ctx, c, "serve", 0, stderr)
	if err != nil {
		return 1
	}
	stderr = diagnostics.stderr
	defer diagnostics.finish(&code)
	d, err := startLaunch(ctx, c)
	if err != nil {
		fmt.Fprintln(stderr, "엄격 기동을 완료할 수 없습니다. 부분 자원과 마감을 확인해야 합니다")
		return 1
	}
	cleanupLaunch := func() { cleanup, cancel := launchCleanupContext(ctx, c); defer cancel(); _ = d.Close(cleanup) }
	if diagnostics.bindTask(d.runtime.TaskID()) != nil || diagnostics.ready() != nil {
		cleanupLaunch()
		return 1
	}
	listener, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		cleanupLaunch()
		fmt.Fprintln(stderr, "수신 주소를 사용할 수 없습니다")
		return 1
	}
	httpServer := &http.Server{Handler: d.runtime.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	serviceCtx, cancelService := context.WithCancel(ctx)
	defer cancelService()
	runner := &gatewayRunner{launch: d, httpServer: httpServer}
	service, err := diagnosis.NewPrepared(serviceCtx, strconv.FormatInt(d.runtime.TaskID(), 10), *preparation, runner, time.Duration(c.ShutdownTimeoutSeconds)*time.Second)
	if err != nil {
		_ = listener.Close()
		cleanupLaunch()
		return 1
	}
	serving := make(chan error, 1)
	go func() {
		serveErr := httpServer.Serve(listener)
		serving <- serveErr
		if !errors.Is(serveErr, http.ErrServerClosed) {
			cancelService()
		}
	}()
	// 취소 시 stdin 읽기도 끝나야 마감에 진입할 수 있다. pipe는 이 프로세스 전용이다.
	stopInput := context.AfterFunc(serviceCtx, func() {
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stopInput()
	protocolErr := diagnosis.Serve(serviceCtx, input, stdout, service)
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	closeErr := service.Shutdown(cleanup)
	if protocolErr != nil || closeErr != nil {
		fmt.Fprintln(stderr, "실행 서비스 통신 또는 마감을 확인할 수 없습니다")
		return 1
	}
	return 0
}

type unavailableGatewayRunner struct{}

func (unavailableGatewayRunner) Run(context.Context) error { return diagnosis.ErrUnavailable }
func (unavailableGatewayRunner) Close(context.Context) (diagnosis.Closure, error) {
	return diagnosis.Closure{}, nil
}

// joined 오류 안의 추가 마감·저장 실패를 실행 실패라는 이유로 지우지 않는다.
func onlyRunOutcome(err, runErr error) bool {
	if err == nil || runErr == nil {
		return false
	}
	if err == runErr {
		return true
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		children := many.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !onlyRunOutcome(child, runErr) {
				return false
			}
		}
		return true
	}
	if one, ok := err.(interface{ Unwrap() error }); ok {
		return onlyRunOutcome(one.Unwrap(), runErr)
	}
	return false
}

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/transmit"
)

// 러너는 연결을 한 번에 하나만 연다. 끝나지 않는 연결이 쌓여도 다음 요청을 받을 여유만 둔다.
const e2EvalControlConnections = 4

// E2EvalServiceOptions는 평가 회차 eval-e2 프로세스의 입력이다. 경로만 받고 값은 파일에서 읽는다.
type E2EvalServiceOptions struct {
	ConfigPath   string        `json:"-"`
	IOTimeout    time.Duration `json:"-"`
	CloseTimeout time.Duration `json:"-"`
	// 비어 있으면 엔진 호출 경로를 열지 않고 실행을 전부 거절한다.
	EngineTokenPath string `json:"-"`
}

func (E2EvalServiceOptions) String() string   { return "server.E2EvalServiceOptions[protected]" }
func (E2EvalServiceOptions) GoString() string { return "server.E2EvalServiceOptions[protected]" }

// E2EvalService는 러너 제어 채널과 독립 backend의 수명을 묶는다.
//
// 엔진 토큰이 없으면 호출 경로를 열지 않고 실행 확인을 전부 거절한다. 그때는 이 서비스만으로
// 대상에 요청이 나가지 않으며, service_ready도 실행 권한이나 도구 격리를 뜻하지 않는다.
type E2EvalService struct {
	cancel  context.CancelFunc
	control *E2ControlService
	backend *E2StandaloneBackend
	remote  *e2RemoteServer
}

func (*E2EvalService) String() string   { return "server.E2EvalService[protected]" }
func (*E2EvalService) GoString() string { return "server.E2EvalService[protected]" }

// 실패 단계는 고정 어휘만 쓴다. 로그에 경로·값이 섞이지 않게 하려는 것이다.
func e2EvalFailure(stage string) error { return fmt.Errorf("%w: %s", ErrE2Control, stage) }

func StartE2EvalService(parent context.Context, o E2EvalServiceOptions) (*E2EvalService, error) {
	if parent == nil || parent.Err() != nil || o.ConfigPath == "" {
		return nil, e2EvalFailure("input")
	}
	config, err := e2config.Load(o.ConfigPath)
	if err != nil || config.Check() != nil {
		return nil, e2EvalFailure("config")
	}
	raw, err := config.ReadPolicy()
	if err != nil {
		return nil, e2EvalFailure("policy")
	}
	defer clear(raw)
	// 동결 값 대조는 러너가 service_ready에서 한다. 여기서는 읽은 파일의 값을 그대로 보고한다.
	fingerprint, err := E2HTTPPolicyContentFingerprint(raw, true)
	if err != nil {
		return nil, e2EvalFailure("policy")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, e2EvalFailure("execution_ref")
	}
	var supervisor E2HTTPExecutionSupervisor = e2EvalNoExecution{}
	var token []byte
	if o.EngineTokenPath != "" {
		if token, err = readE2RemoteToken(o.EngineTokenPath); err != nil {
			return nil, e2EvalFailure("engine_token")
		}
		defer clear(token)
		supervisor = &e2EvalTaskBinding{}
	}
	ctx, cancel := context.WithCancel(parent)
	backend, err := NewE2StandaloneBackend(ctx, E2StandaloneOptions{
		Config:                          config,
		Authority:                       e2EvalAuthority{ctx: ctx, digest: sha256.Sum256(raw)},
		Supervisor:                      supervisor,
		Readiness:                       e2EvalReadiness{},
		ExecutionRef:                    "exec-" + hex.EncodeToString(nonce[:]),
		ExpectedPolicyFingerprintSHA256: fingerprint,
	})
	if err != nil {
		cancel()
		return nil, e2EvalFailure("backend")
	}
	control, err := StartE2ControlService(ctx, E2ControlOptions{
		ControlDirectory:     config.ControlDir,
		EvalRunID:            config.EvalRunID,
		ReservationRef:       config.ReservationRef,
		RunnerUID:            e2EvalEffectiveUID(),
		Isolation:            E2ControlIsolationToolSandbox,
		EvaluationDeployment: true,
		IOTimeout:            o.IOTimeout,
		CloseTimeout:         o.CloseTimeout,
		MaxConnections:       e2EvalControlConnections,
		Backend:              backend,
	})
	if err != nil {
		cancel()
		return nil, errors.Join(e2EvalFailure("control"), backend.Dispose())
	}
	service := &E2EvalService{cancel: cancel, control: control, backend: backend}
	if token != nil {
		service.remote, err = startE2RemoteServer(ctx, config.Listen, token, backend)
		if err != nil {
			closing, stop := context.WithTimeout(context.Background(), o.CloseTimeout)
			defer stop()
			return nil, errors.Join(e2EvalFailure("engine_listener"), service.Close(closing))
		}
	}
	return service, nil
}

// Close는 제어 채널을 먼저 닫아 새 요청을 막은 뒤 backend를 정리한다.
func (s *E2EvalService) Close(ctx context.Context) error {
	if s == nil || s.cancel == nil {
		return ErrE2Control
	}
	// 새 실행 요청부터 막아야 제어 채널이 닫히는 동안 실행이 생기지 않는다.
	remoteErr := s.remote.close(ctx)
	controlErr := s.control.Close(ctx)
	s.cancel()
	return errors.Join(remoteErr, controlErr, s.backend.Dispose())
}

// 러너가 회차마다 만들어 읽기 전용으로 건넨 정책만 받는다.
type e2EvalAuthority struct {
	ctx    context.Context
	digest [32]byte
}

func (a e2EvalAuthority) Context() context.Context { return a.ctx }
func (a e2EvalAuthority) CheckRoot(ctx context.Context) error {
	return errors.Join(ctx.Err(), a.ctx.Err())
}
func (a e2EvalAuthority) AuthorizePolicy(ctx context.Context, digest [32]byte) error {
	if digest != a.digest {
		return ErrE2Control
	}
	return a.CheckRoot(ctx)
}

// 엔진 호출이 실제 task·intent에 결속되기 전까지는 어떤 실행도 승인하지 않는다.
type e2EvalNoExecution struct{}

func (e2EvalNoExecution) AuthorizeExecution(context.Context, transmit.Scope, int64) error {
	return ErrE2Control
}

// 첫 task에 고정한다. 호출자인 엔진은 토큰으로 인증된 신뢰 프로세스이고, 한 회차에 task는 하나다.
type e2EvalTaskBinding struct {
	mu   sync.Mutex
	task string
}

func (b *e2EvalTaskBinding) AuthorizeExecution(_ context.Context, scope transmit.Scope, intent int64) error {
	if intent <= 0 || scope.TaskRef == "" {
		return ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.task == "" {
		b.task = scope.TaskRef
	}
	if b.task != scope.TaskRef {
		return ErrE2Control
	}
	return nil
}

// 데이터 경로가 없는 동안에는 확인할 라우팅이 없다. 설정 재검사는 backend가 매번 한다.
type e2EvalReadiness struct{}

func (e2EvalReadiness) CheckReady(ctx context.Context) error { return ctx.Err() }

package diagnosis

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Runner는 보호된 준비와 권위 검사를 끝낸 실행 한 개만 담는다.
type Closure struct {
	DrainComplete bool
	ResultReady   bool
	// Result는 실행이 정상 마감됐을 때의 결과다. 없거나 계약에 맞지 않으면 완료로 보이지 않는다.
	Result *RunResult
}

type Runner interface {
	Run(context.Context) error
	Close(context.Context) (Closure, error)
}
type Service struct {
	mu             sync.Mutex
	ctx            context.Context
	runner         Runner
	snapshot       Snapshot
	startKey       string
	cancel         context.CancelFunc
	done           chan struct{}
	closed         bool
	closeOnce      sync.Once
	closeState     Closure
	closeErr       error
	cleanupTimeout time.Duration
}

// NewPrepared는 운영자가 기존 CLI 검사를 통과한 실행에만 사용한다.
// 입력 참조는 URL·파일 경로·모델 키나 실행 권위를 생성하지 않는다.
func NewPrepared(ctx context.Context, task, preparation string, runner Runner, cleanupTimeout time.Duration) (*Service, error) {
	if ctx == nil || ctx.Err() != nil || runner == nil || !validTask(task) || !reference.MatchString(preparation) || cleanupTimeout <= 0 {
		return nil, ErrProtocol
	}
	return &Service{ctx: ctx, runner: runner, cleanupTimeout: cleanupTimeout, snapshot: Snapshot{TaskID: task, PreparationRef: preparation, State: Prepared}}, nil
}
func (s *Service) Call(ctx context.Context, r Request) (Response, error) {
	if ctx == nil || ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	if !validRequest(r) {
		return errorResponse(r, "invalid_request"), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	if r.TaskID != s.snapshot.TaskID || r.PreparationRef != s.snapshot.PreparationRef {
		return errorResponse(r, "binding_mismatch"), nil
	}
	if s.ctx.Err() != nil || s.closed {
		return errorResponse(r, "service_closed"), nil
	}
	switch r.Command {
	case Start:
		if s.startKey != "" {
			if s.startKey != r.IdempotencyKey {
				return errorResponse(r, "already_started"), nil
			}
			break
		}
		if s.snapshot.State != Prepared {
			return errorResponse(r, "not_prepared"), nil
		}
		s.startKey = r.IdempotencyKey
		s.snapshot.Started, s.snapshot.State = true, Running
		runCtx, cancel := context.WithCancel(s.ctx)
		s.cancel, s.done = cancel, make(chan struct{})
		go s.execute(runCtx)
	case Stop:
		if s.snapshot.State == Prepared {
			return errorResponse(r, "not_started"), nil
		}
		if s.snapshot.State == Running {
			s.snapshot.State = Stopping
			s.cancel()
		}
	case Result:
		if !s.snapshot.ResultReady {
			return errorResponse(r, "result_not_ready"), nil
		}
	}
	snapshot := s.snapshot
	return Response{Version: Version, RequestID: r.RequestID, Snapshot: &snapshot}, nil
}
func (s *Service) closeResources() (Closure, error) {
	s.closeOnce.Do(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), s.cleanupTimeout)
		defer cancel()
		s.closeState, s.closeErr = s.runner.Close(cleanup)
	})
	return s.closeState, s.closeErr
}
func (s *Service) execute(ctx context.Context) {
	err := s.runner.Run(ctx)
	closure, closeErr := s.closeResources()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case closeErr != nil || !closure.DrainComplete || !closure.ResultReady:
		s.snapshot.State = Unknown
	case s.snapshot.State == Stopping || errors.Is(err, context.Canceled):
		s.snapshot.State = Cancelled
	case err != nil:
		s.snapshot.State = Failed
	case closure.Result == nil || !closure.Result.Valid():
		// 마감은 확인됐지만 실행 결과를 모르면 완료라고 하지 않는다(절대 규칙 5).
		s.snapshot.State = Unknown
	default:
		s.snapshot.State = Completed
		result := *closure.Result
		s.snapshot.Result = &result
	}
	// 실행 종료는 취약점 없음과 다르다. Close에는 기존 감사·저장 마감이 포함된다.
	s.snapshot.DrainComplete = closure.DrainComplete
	s.snapshot.ResultReady = closure.ResultReady
	close(s.done)
}

// Shutdown은 통신 종료도 신규 실행 차단·취소·마감으로 다룬다.
func (s *Service) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	done := s.done
	if done == nil {
		s.snapshot.State = Stopping
	}
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ErrUnavailable
		}
	}
	closure, err := s.closeResources()
	s.mu.Lock()
	if done == nil {
		s.snapshot.State = Cancelled
		if err != nil {
			s.snapshot.State = Unknown
		}
		s.snapshot.DrainComplete = closure.DrainComplete
	}
	s.mu.Unlock()
	return err
}

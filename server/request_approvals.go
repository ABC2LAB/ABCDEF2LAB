package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

var errRequestApproval = errors.New("request_approval_unavailable")

const requestApprovalStoreTimeout = 2 * time.Second

// 호출 취소만 보존한다. 취소와 경합한 실제 저장 장애는 감사 실패로 남긴다.
func requestApprovalStoreError(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil && errors.Is(err, cause) {
		return cause
	}
	return errRequestApproval
}

// 표시 허용 필드는 서명된 호스트 정책으로만 지정한다. 모델 출력으로 만들지 않는다.
type RequestApprovalDisplay struct {
	PublicPath bool     `json:"public_path"`
	QueryKeys  []string `json:"query_keys"`
	JSONFields []string `json:"json_fields"`
}
type requestApprovalStore interface {
	InsertRequestApproval(context.Context, db.RequestApproval) error
	GetRequestApproval(context.Context, int64, string, string) (db.RequestApproval, error)
	ListRequestApprovals(context.Context, int64, string) ([]db.RequestApproval, error)
	SwapRequestApproval(context.Context, db.RequestApproval, bool) error
}
type requestApprovalWaiter struct {
	ctx      context.Context
	decision chan struct{}
	failed   bool
}
type requestApprovalBinding struct {
	cancel        context.CancelFunc
	stopRequested bool
	drainComplete bool
	closureFailed bool
	phase         string
	outcome       string
	execution     *e2proxy.Execution
	ctx           context.Context
	run           string
	rules         map[string]E2HTTPFileRule
}

// durable 기록은 재생 권한이 아니다. 이 프로세스가 가진 live waiter만 결정·소비한다.
type RequestApprovalService struct {
	mu       sync.Mutex
	store    requestApprovalStore
	bindings map[int64]requestApprovalBinding
	waiters  map[string]*requestApprovalWaiter
}

func newRequestApprovalService(store requestApprovalStore) *RequestApprovalService {
	return &RequestApprovalService{store: store, bindings: map[int64]requestApprovalBinding{}, waiters: map[string]*requestApprovalWaiter{}}
}
func (s *RequestApprovalService) bind(ctx context.Context, task int64, owner, run string, rules []E2HTTPFileRule) error {
	if s == nil || e2HTTPNil(s.store) || ctx == nil || ctx.Err() != nil || task <= 0 || owner != "ARTEX" || run == "" {
		return errRequestApproval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, exists := s.bindings[task]
	if exists && (existing.run != run || existing.rules != nil || existing.phase == "closed" || existing.phase == "failed" || existing.stopRequested || existing.ctx.Err() != nil) {
		return errRequestApproval
	}
	copied := map[string]E2HTTPFileRule{}
	for _, r := range rules {
		r.ApprovalDisplay.QueryKeys = append([]string(nil), r.ApprovalDisplay.QueryKeys...)
		r.ApprovalDisplay.JSONFields = append([]string(nil), r.ApprovalDisplay.JSONFields...)
		copied[r.Ref] = r
	}
	if !exists {
		existing = requestApprovalBinding{ctx: ctx, run: run, phase: "prepared"}
	}
	existing.rules = copied
	s.bindings[task] = existing
	return nil
}
func approvalSecretKey(key string) bool {
	key = strings.ToLower(key)
	for _, secret := range []string{"password", "passwd", "secret", "token", "cookie", "authorization", "credential", "api_key", "apikey", "session"} {
		if strings.Contains(key, secret) {
			return true
		}
	}
	return false
}
func approvalPreview(p e2proxy.RequestApprovalProposal, rule E2HTTPFileRule) (string, string, map[string]string, error) {
	u, err := url.Parse(p.URL)
	if err != nil || u.User != nil {
		return "", "", nil, errRequestApproval
	}
	path := rule.PathPrefix
	if rule.ApprovalDisplay.PublicPath {
		path = dlp.DiagnosticText(u.EscapedPath())
	} else if u.Path != rule.PathPrefix {
		path = strings.TrimSuffix(path, "/") + "/[REDACTED]"
	}
	preview := map[string]string{}
	add := func(key, value string) {
		if len(value) <= 256 && !approvalSecretKey(key) {
			preview[key] = value
		}
	}
	query := u.Query()
	for _, key := range rule.ApprovalDisplay.QueryKeys {
		if values, ok := query[key]; ok {
			v, _ := json.Marshal(values)
			add("query."+key, string(v))
		}
	}
	var body map[string]json.RawMessage
	if len(p.Body) > 0 && json.Unmarshal(p.Body, &body) == nil {
		for _, key := range rule.ApprovalDisplay.JSONFields {
			if value, ok := body[key]; ok {
				if len(value) > 0 && value[0] != '{' && value[0] != '[' {
					add("body."+key, string(value))
				}
			}
		}
	}
	if len(p.Body) > 0 || u.RawQuery != "" || !rule.ApprovalDisplay.PublicPath {
		preview["표시 제한"] = "호스트가 공개로 지정한 경로와 입력만 표시합니다. 비공개 입력·상태 변경 영향은 확인되지 않았습니다."
	}
	return u.Scheme + "://" + u.Host, path, preview, nil
}
func (s *RequestApprovalService) AwaitRequestApproval(ctx context.Context, p e2proxy.RequestApprovalProposal) (bool, error) {
	var status string
	return s.awaitRequestApproval(ctx, p, &status)
}

// AwaitRequestApprovalOutcome은 거절과 만료를 나눠 돌려준다(e2proxy.RequestApprovalOutcomeGate). E③ 확정 단계 생성 요청의
// 미송신 사유(approval_denied·approval_timeout)에 쓰인다. 허용 판단은 AwaitRequestApproval과 같다.
func (s *RequestApprovalService) AwaitRequestApprovalOutcome(ctx context.Context, p e2proxy.RequestApprovalProposal) (e2proxy.RequestApprovalOutcome, error) {
	var status string
	allowed, err := s.awaitRequestApproval(ctx, p, &status)
	switch {
	case allowed:
		return e2proxy.RequestApprovalAllowed, err
	case err == nil && status == "denied":
		return e2proxy.RequestApprovalDenied, nil
	case err == nil && status == "expired":
		return e2proxy.RequestApprovalExpired, nil
	default:
		return e2proxy.RequestApprovalUndecided, err
	}
}

// awaitRequestApproval은 결정을 기다리고, 끝난 모양(진단 로그의 status 값)을 resolved에 남긴다.
func (s *RequestApprovalService) awaitRequestApproval(ctx context.Context, p e2proxy.RequestApprovalProposal, resolved *string) (allowed bool, outcome error) {
	status, reason := "failed", "approval_unavailable"
	defer func() {
		if errors.Is(outcome, context.Canceled) {
			status, reason = "cancelled", "cancelled"
		}
		if errors.Is(outcome, context.DeadlineExceeded) {
			status, reason = "timeout", "timeout"
		}
		*resolved = status
		task, _ := strconv.ParseInt(p.Receipt.Scope.TaskRef, 10, 64)
		if task < 0 {
			task = 0
		}
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "request_approval_resolved", Stage: "request_approval", Status: status, ReasonCode: reason, TaskID: task, RunRef: p.Receipt.Scope.RunRef, IntentID: p.Receipt.IntentID, CallRef: p.Receipt.CallRef, AttemptRef: p.Receipt.AttemptID})
	}()
	task, err := strconv.ParseInt(p.Receipt.Scope.TaskRef, 10, 64)
	if err != nil || ctx == nil || p.OwnerRef != "ARTEX" {
		return false, errRequestApproval
	}
	s.mu.Lock()
	binding, ok := s.bindings[task]
	rule, ruleOK := binding.rules[p.Receipt.RuleRef]
	if !ok || binding.phase == "closed" || binding.phase == "failed" || binding.stopRequested || !ruleOK || binding.run != p.Receipt.Scope.RunRef || s.waiters[p.Receipt.AttemptID] != nil {
		s.mu.Unlock()
		return false, errRequestApproval
	}
	if ctx.Err() != nil || binding.ctx.Err() != nil || !time.Now().Before(p.ExpiresAt) {
		s.mu.Unlock()
		status, reason = "cancelled", "cancelled"
		if !time.Now().Before(p.ExpiresAt) {
			status, reason = "expired", "expired"
		}
		return false, nil
	}
	origin, path, preview, err := approvalPreview(p, rule)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	// json은 필드와 헤더 키를 결정적으로 정렬한다. 원문은 해시 계산 후 저장하지 않는다.
	raw, err := json.Marshal(struct {
		Method, URL, Host, Account, Session, Policy string
		Headers                                     any
		Body                                        []byte
	}{p.Method, p.URL, p.Host, p.AccountRef, p.SessionRef, p.Receipt.PolicyRevision, p.Headers, p.Body})
	if err != nil {
		s.mu.Unlock()
		return false, errRequestApproval
	}
	digest := sha256.Sum256(raw)
	clear(raw)
	a := db.RequestApproval{ID: p.Receipt.AttemptID, TaskID: task, OwnerRef: p.OwnerRef, RunRef: p.Receipt.Scope.RunRef, IntentID: p.Receipt.IntentID, CallRef: p.Receipt.CallRef, AttemptID: p.Receipt.AttemptID, PolicyRevision: p.Receipt.PolicyRevision, AccountRef: p.AccountRef, SessionRef: p.SessionRef, RequestSHA256: hex.EncodeToString(digest[:]), Method: p.Method, TargetOrigin: origin, Path: path, InputPreview: preview, BodyBytes: len(p.Body), CreatedAt: time.Now().UTC(), ExpiresAt: p.ExpiresAt, State: "pending", ExecutionState: "not_sent"}
	insertCtx, insertCancel := context.WithTimeout(ctx, requestApprovalStoreTimeout)
	err = s.store.InsertRequestApproval(insertCtx, a)
	insertCancel()
	if err != nil {
		s.mu.Unlock()
		return false, requestApprovalStoreError(ctx, err)
	}
	waiter := &requestApprovalWaiter{ctx: ctx, decision: make(chan struct{})}
	s.waiters[a.ID] = waiter
	s.mu.Unlock()
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "request_approval_pending", Stage: "request_approval", Status: "pending", TaskID: task, RunRef: a.RunRef, IntentID: a.IntentID, CallRef: a.CallRef, AttemptRef: a.AttemptID})
	timer := time.NewTimer(time.Until(a.ExpiresAt))
	defer timer.Stop()
	select {
	case <-waiter.decision:
	case <-ctx.Done():
	case <-binding.ctx.Done():
	case <-timer.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if waiter.failed {
		return false, errRequestApproval
	}
	// 취소가 결정과 경합해도 확인 모드의 fallback allow는 없다.
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), requestApprovalStoreTimeout)
	defer persistCancel()
	current, err := s.store.GetRequestApproval(persistCtx, task, p.OwnerRef, a.ID)
	if err != nil {
		return false, errRequestApproval
	}
	if ctx.Err() != nil || binding.ctx.Err() != nil || !time.Now().Before(a.ExpiresAt) {
		if current.State == "pending" || current.State == "allowed" {
			now := time.Now()
			current.State = "cancelled"
			if !now.Before(a.ExpiresAt) {
				current.State = "expired"
			}
			// 닫힌 까닭은 대기가 끝난 지금 가린다. 운영자 종료 표지는 대기 시작 뒤에 바뀌므로 지금 결속을 본다.
			current.ClosedReason = approvalClosedReason(ctx, s.bindings[task], a.ExpiresAt, now)
			current.ExecutionState = "not_sent"
			if s.store.SwapRequestApproval(persistCtx, current, false) != nil {
				return false, errRequestApproval
			}
		}
		status, reason = "cancelled", "cancelled"
		if !time.Now().Before(a.ExpiresAt) {
			status, reason = "expired", "expired"
		}
		return false, nil
	}
	if current.State != "allowed" || current.Consumed {
		status, reason = "denied", "policy_denied"
		return false, nil
	}
	current.Consumed = true
	current.ExecutionState = "awaiting_dispatch"
	consumeCtx, consumeCancel := context.WithTimeout(ctx, requestApprovalStoreTimeout)
	err = s.store.SwapRequestApproval(consumeCtx, current, true)
	consumeCancel()
	if err != nil {
		if errors.Is(err, db.ErrRequestApprovalConflict) && !time.Now().Before(current.ExpiresAt) {
			status, reason = "expired", "expired"
			return false, nil
		}
		return false, requestApprovalStoreError(ctx, err)
	}
	status, reason = "allowed", "complete"
	return true, nil
}

// 승인 대기가 결정 없이 닫힌 까닭(closed_reason)의 고정 코드다. 화면이 '취소'·'만료'의 이유를 가린다(2026-11-25 경준
// 요청). E② 전송 결과(reason)와 섞지 않는다.
const (
	approvalClosedModelStage = "model_stage_closed" // 모델 단계 마감으로 call 문맥이 끊김
	approvalClosedTimeout    = "request_timeout"    // 요청·call·회차 기한
	approvalClosedOperator   = "operator_stopped"   // 운영자 종료로 실행 결속이 끝남
	approvalClosedRun        = "run_closed"         // 그 밖에 실행이 닫힘
	approvalClosedExpired    = "approval_expired"   // 승인 기한 만료(state=expired와 같이)
)

// approvalClosedReason은 승인 대기가 결정 없이 끝난 까닭이다. 승인 기한 → 운영자 종료 → 승인 대기 문맥의 원인(모델 단계
// 마감·기한, E② Call.do가 싣는다) → 실행 결속 문맥의 기한 → 그 밖 순서로 본다.
func approvalClosedReason(wait context.Context, binding requestApprovalBinding, expiresAt, now time.Time) string {
	switch {
	case !now.Before(expiresAt):
		return approvalClosedExpired
	case binding.stopRequested:
		return approvalClosedOperator
	}
	cause := context.Cause(wait)
	switch {
	case errors.Is(cause, e2proxy.ErrModelStageClosed):
		return approvalClosedModelStage
	case errors.Is(cause, context.DeadlineExceeded) || errors.Is(wait.Err(), context.DeadlineExceeded):
		return approvalClosedTimeout
	case binding.ctx != nil && errors.Is(context.Cause(binding.ctx), context.DeadlineExceeded):
		return approvalClosedTimeout
	}
	return approvalClosedRun
}

// projectedClosedReason은 응답에 싣는 closed_reason이다. cancelled·expired에만 싣는다. 만료는 늘 approval_expired다.
// 취소는 둘로 나눈다. 읽는 이 순간 대기 건(pending·미소비 allowed)을 취소로 투영한 경우(projected)만 지금 결속을 보고 운영자
// 종료면 operator_stopped, 아니면 run_closed다. 이미 cancelled로 저장된 기록은 저장한 까닭을 쓰고, 까닭이 없으면(이 칸 이전
// 기록) 실제 원인을 모르므로 run_closed다. 지금의 운영자 종료를 지난 취소에 덧씌우지 않는다(#953 이동찬 P2).
func projectedClosedReason(a db.RequestApproval, b requestApprovalBinding, bound, projected bool) string {
	switch a.State {
	case "expired":
		return approvalClosedExpired
	case "cancelled":
		if projected {
			if bound && b.stopRequested {
				return approvalClosedOperator
			}
			return approvalClosedRun
		}
		if a.ClosedReason != "" && a.ClosedReason != approvalClosedExpired {
			return a.ClosedReason
		}
		return approvalClosedRun
	}
	return ""
}

func (s *RequestApprovalService) authorized(task int64, owner string) bool {
	_, ok := s.bindings[task]
	return ok && owner == "ARTEX"
}
func (s *RequestApprovalService) decorate(a db.RequestApproval) db.RequestApproval {
	b, bok := s.bindings[a.TaskID]
	w := s.waiters[a.ID]
	a.Live = bok && b.phase != "closed" && b.phase != "failed" && !b.stopRequested && b.ctx.Err() == nil && b.run == a.RunRef && w != nil && !w.failed && (w.ctx.Err() == nil || a.Consumed)
	// 결과 저장/감사 실패로 경계가 닫혔다면 남은 dispatch 상태는 성공이나 대기가 아니다.
	if b.execution != nil && b.execution.Snapshot().Reason == e2proxy.AuditFailed {
		a.Live = false
	}
	if !a.Live && a.Consumed && (a.ExecutionState == "awaiting_dispatch" || a.ExecutionState == "dispatch_pending") {
		a.ExecutionState = "unknown"
	}
	stored := a.State
	if a.State == "pending" || a.State == "allowed" && !a.Consumed {
		if !time.Now().Before(a.ExpiresAt) {
			a.State = "expired"
		} else if !a.Live {
			a.State = "cancelled"
		}
	}
	a.ClosedReason = projectedClosedReason(a, b, bok, a.State != stored)
	a.CanDecide = a.Live && a.State == "pending" && time.Now().Before(a.ExpiresAt)
	return a
}
func (s *RequestApprovalService) list(ctx context.Context, task int64, owner string) ([]db.RequestApproval, error) {
	ctx, cancel := context.WithTimeout(ctx, requestApprovalStoreTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorized(task, owner) {
		return nil, errRequestApproval
	}
	rows, err := s.store.ListRequestApprovals(ctx, task, owner)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i] = s.decorate(rows[i])
	}
	return rows, nil
}
func (s *RequestApprovalService) get(ctx context.Context, task int64, owner, id string) (db.RequestApproval, error) {
	ctx, cancel := context.WithTimeout(ctx, requestApprovalStoreTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorized(task, owner) {
		return db.RequestApproval{}, errRequestApproval
	}
	a, err := s.store.GetRequestApproval(ctx, task, owner, id)
	if err != nil {
		return a, err
	}
	return s.decorate(a), nil
}
func (s *RequestApprovalService) decide(ctx context.Context, task int64, owner, id, digest, decision string) (db.RequestApproval, error) {
	var observed *db.RequestApproval
	var diagnosticContext context.Context
	defer func() {
		if observed != nil {
			_ = diagnosticlog.Emit(diagnosticContext, diagnosticlog.Event{Component: "server", Event: "request_approval_decided", Stage: "request_approval", Status: observed.State, TaskID: task, RunRef: observed.RunRef, IntentID: observed.IntentID, CallRef: observed.CallRef, AttemptRef: observed.AttemptID})
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, requestApprovalStoreTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorized(task, owner) {
		return db.RequestApproval{}, errRequestApproval
	}
	a, err := s.store.GetRequestApproval(ctx, task, owner, id)
	if err != nil {
		return a, err
	}
	a = s.decorate(a)
	if !a.CanDecide || a.RequestSHA256 != digest || decision != "allow" && decision != "deny" {
		return a, db.ErrRequestApprovalConflict
	}
	a.State = "denied"
	if decision == "allow" {
		a.State = "allowed"
	}
	now := time.Now().UTC()
	a.DecidedBy = owner
	a.DecidedAt = &now
	a.Live = false
	a.CanDecide = false
	if err = s.store.SwapRequestApproval(ctx, a, true); err != nil {
		s.waiters[id].failed = !errors.Is(err, db.ErrRequestApprovalConflict) || time.Now().Before(a.ExpiresAt)
		close(s.waiters[id].decision)
		return a, err
	}
	close(s.waiters[id].decision)
	observed = &a
	diagnosticContext = s.bindings[task].ctx
	return s.decorate(a), nil
}
func (s *RequestApprovalService) observe(ctx context.Context, event e2proxy.Event) error {
	return s.observeAttempt(ctx, event, false)
}

// observeE3는 확정 단계 요청(E③ 프로그램 요청)의 승인 이력을 그 시도의 결과로 갱신한다. 앞 기록기(Host ACK·화면 사본)가
// 성공한 사건만 오고, 승인 기록이 같은 회차·시도에 결속돼 있을 때만 바꾼다. 내보내기 단계(dispatch_pending)와 결과(응답
// 완료 completed·결과 불명 unknown·미송신 not_sent)는 일반 요청과 같다. 일반 observe와 달리 실패를 감사 실패로 올리지
// 않는다: 확정 사건의 정본은 Host 저널이고, 갱신하지 못한 승인 기록은 화면에서 결과 불명으로 보인다(decorate). 전에는
// 확정 사건을 받지 않아 보낸 확정 생성 요청의 승인 기록이 awaiting_dispatch로 남았다(2026-11-25 화면 회차).
func (s *RequestApprovalService) observeE3(ctx context.Context, event e2proxy.Event) {
	if err := s.observeAttempt(ctx, event, true); err != nil {
		log.Printf(logT("[request-approval] 확정 단계 요청 승인 이력을 갱신하지 못함(화면은 결과 불명): 단계=%s"), event.Stage)
	}
}

// unknownAfterStoreFailureLocked는 확정 사건(confirmation)의 승인 이력을 읽거나 저장하지 못했을 때 그 시도를 바로 결과
// 불명으로 보이게 한다. 일반 관찰은 저장 실패가 감사 실패로 실행을 래치해 decorate가 결과 불명으로 투영하지만, 확정 사건은
// 감사 실패로 올리지 않으므로 대기자를 실패로 표시해야 실행 중 조회에서도 decorate가 소비된 승인의 awaiting_dispatch·
// dispatch_pending을 unknown으로 바꾼다(#952 이동찬 P2). 승인 대기는 이미 끝난 뒤라 대기 결과에는 영향이 없다. s.mu를 쥐고 부른다.
func (s *RequestApprovalService) unknownAfterStoreFailureLocked(id string, confirmation bool) {
	if w := s.waiters[id]; confirmation && w != nil {
		w.failed = true
	}
}

// observeAttempt는 시도 사건 하나로 그 승인 기록을 갱신한다. sameRun이면 승인 기록의 회차가 사건의 회차와 같아야 한다.
func (s *RequestApprovalService) observeAttempt(ctx context.Context, event e2proxy.Event, sameRun bool) error {
	ctx, cancel := context.WithTimeout(ctx, requestApprovalStoreTimeout)
	defer cancel()
	if event.AttemptID == "" || event.Stage != "intent" && event.Stage != "result" {
		return nil
	}
	task, err := strconv.ParseInt(event.Scope.TaskRef, 10, 64)
	if err != nil {
		return errRequestApproval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters[event.AttemptID] == nil {
		return nil
	}
	a, err := s.store.GetRequestApproval(ctx, task, "ARTEX", event.AttemptID)
	if err != nil {
		s.unknownAfterStoreFailureLocked(event.AttemptID, sameRun)
		return errRequestApproval
	}
	if sameRun && a.RunRef != event.Scope.RunRef {
		return errRequestApproval
	}
	if event.Stage == "intent" {
		if !a.Consumed || a.State != "allowed" {
			return errRequestApproval
		}
		a.ExecutionState = "dispatch_pending"
	} else {
		a.Reason = string(event.Reason)
		switch {
		case event.ResponseComplete:
			a.ExecutionState = "completed"
		case event.ForwardAttempted:
			a.ExecutionState = "unknown"
		default:
			a.ExecutionState = "not_sent"
		}
	}
	if err = s.store.SwapRequestApproval(ctx, a, false); err != nil {
		s.unknownAfterStoreFailureLocked(event.AttemptID, sameRun)
		return errRequestApproval
	}
	// 결과의 영속화 뒤에는 이 시도의 대기 문맥을 보존할 필요가 없다.
	// 저장 실패 때는 남겨 두어 decorate가 결과 불명 상태를 계속 투영한다.
	if event.Stage == "result" {
		delete(s.waiters, event.AttemptID)
	}
	return nil
}

func (s *RequestApprovalService) attachExecution(task int64, execution *e2proxy.Execution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[task]
	binding.execution = execution
	s.bindings[task] = binding
}

// admitRun은 실행 획득 전에도 실제 등록 task에만 중지 권한을 결속한다.
func (s *RequestApprovalService) admitRun(ctx context.Context, task int64, run string, cancel context.CancelFunc) error {
	if s == nil || ctx == nil || ctx.Err() != nil || task <= 0 || run == "" || cancel == nil {
		return errRequestApproval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.bindings[task]; exists {
		return errRequestApproval
	}
	s.bindings[task] = requestApprovalBinding{ctx: ctx, run: run, cancel: cancel, phase: "prepared"}
	return nil
}
func (s *RequestApprovalService) userStopped(task int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindings[task].stopRequested
}

type requestApprovalRunState struct {
	Phase            string                 `json:"phase"`
	Outcome          string                 `json:"outcome"`
	DrainComplete    bool                   `json:"drain_complete"`
	ClosureFailed    bool                   `json:"closure_failed"`
	RunRef           string                 `json:"run_ref"`
	StopRequested    bool                   `json:"stop_requested"`
	ContextCancelled bool                   `json:"context_cancelled"`
	Execution        *e2proxy.ExecutionView `json:"execution,omitempty"`
}

func (s *RequestApprovalService) runState(task int64) requestApprovalRunState {
	s.mu.Lock()
	b := s.bindings[task]
	s.mu.Unlock()
	state := requestApprovalRunState{Phase: b.phase, Outcome: b.outcome, DrainComplete: b.drainComplete, ClosureFailed: b.closureFailed, RunRef: b.run, StopRequested: b.stopRequested, ContextCancelled: b.ctx != nil && b.ctx.Err() != nil}
	if b.execution != nil {
		view := b.execution.Snapshot()
		state.Execution = &view
	}
	return state
}
func (s *RequestApprovalService) stop(ctx context.Context, task int64, owner string) error {
	s.mu.Lock()
	binding, ok := s.bindings[task]
	if !ok || owner != "ARTEX" {
		s.mu.Unlock()
		return errRequestApproval
	}
	if !binding.stopRequested && (binding.phase == "closed" || binding.phase == "failed") {
		s.mu.Unlock()
		return db.ErrRequestApprovalConflict
	}
	if !binding.stopRequested {
		binding.phase = "stopping"
	}
	binding.stopRequested = true
	s.bindings[task] = binding
	s.mu.Unlock()
	// task 취소는 신규 planner/worker도 막는다. 진행 중 송신의 롤백을 뜻하지 않는다.
	if binding.cancel != nil {
		binding.cancel()
	}
	if binding.execution == nil {
		return nil
	}
	return binding.execution.Stop(ctx)
}

// 감독자가 실제 drain과 자원 종료를 확인한 결과만 공개한다.
func (s *RequestApprovalService) finishStop(task int64, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[task]
	if !ok || !b.stopRequested {
		return
	}
	b.phase = "closed"
	b.outcome = "cancelled"
	if err != nil {
		b.phase = "failed"
	}
	b.drainComplete = err == nil
	b.closureFailed = err != nil
	s.bindings[task] = b
}

// 단계와 결과는 감독자가 확인한 사건으로만 갱신한다.
func (s *RequestApprovalService) runStarted(task int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[task]
	if !ok || b.stopRequested || b.phase == "closed" || b.phase == "failed" {
		return
	}
	b.phase = "running"
	s.bindings[task] = b
}

// runFinished는 종료 결정과 중지 접수를 같은 잠금으로 직렬화한다. true면 감독자가 중지를 마감해야 한다.
func (s *RequestApprovalService) runFinished(task int64, err error, closureFailed bool) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[task]
	if !ok {
		return false
	}
	if b.phase == "closed" || b.phase == "failed" {
		return false
	}
	if b.stopRequested {
		return true
	}
	if err == nil {
		b.phase = "closed"
		b.outcome = "completed"
		b.drainComplete = true
	} else {
		b.phase = "failed"
		b.outcome = "failed"
		b.closureFailed = closureFailed
		if b.cancel != nil {
			b.cancel()
		}
	}
	s.bindings[task] = b
	return false
}

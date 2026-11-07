package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
)

var errRequestApproval = errors.New("request_approval_unavailable")

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
	execution *e2proxy.Execution
	ctx       context.Context
	run       string
	rules     map[string]E2HTTPFileRule
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
	if _, exists := s.bindings[task]; exists {
		return errRequestApproval
	}
	copied := map[string]E2HTTPFileRule{}
	for _, r := range rules {
		r.ApprovalDisplay.QueryKeys = append([]string(nil), r.ApprovalDisplay.QueryKeys...)
		r.ApprovalDisplay.JSONFields = append([]string(nil), r.ApprovalDisplay.JSONFields...)
		copied[r.Ref] = r
	}
	s.bindings[task] = requestApprovalBinding{ctx: ctx, run: run, rules: copied}
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
		path = u.EscapedPath()
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
	task, err := strconv.ParseInt(p.Receipt.Scope.TaskRef, 10, 64)
	if err != nil || ctx == nil || p.OwnerRef != "ARTEX" {
		return false, errRequestApproval
	}
	s.mu.Lock()
	binding, ok := s.bindings[task]
	rule, ruleOK := binding.rules[p.Receipt.RuleRef]
	if !ok || !ruleOK || binding.run != p.Receipt.Scope.RunRef || s.waiters[p.Receipt.AttemptID] != nil {
		s.mu.Unlock()
		return false, errRequestApproval
	}
	if ctx.Err() != nil || binding.ctx.Err() != nil || !time.Now().Before(p.ExpiresAt) {
		s.mu.Unlock()
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
	if err = s.store.InsertRequestApproval(ctx, a); err != nil {
		s.mu.Unlock()
		return false, errRequestApproval
	}
	waiter := &requestApprovalWaiter{ctx: ctx, decision: make(chan struct{})}
	s.waiters[a.ID] = waiter
	s.mu.Unlock()
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
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer persistCancel()
	current, err := s.store.GetRequestApproval(persistCtx, task, p.OwnerRef, a.ID)
	if err != nil {
		return false, errRequestApproval
	}
	if ctx.Err() != nil || binding.ctx.Err() != nil || !time.Now().Before(a.ExpiresAt) {
		if current.State == "pending" || current.State == "allowed" {
			current.State = "cancelled"
			if !time.Now().Before(a.ExpiresAt) {
				current.State = "expired"
			}
			current.ExecutionState = "not_sent"
			if s.store.SwapRequestApproval(persistCtx, current, false) != nil {
				return false, errRequestApproval
			}
		}
		return false, nil
	}
	if current.State != "allowed" || current.Consumed {
		return false, nil
	}
	current.Consumed = true
	current.ExecutionState = "awaiting_dispatch"
	if err := s.store.SwapRequestApproval(ctx, current, true); err != nil {
		if errors.Is(err, db.ErrRequestApprovalConflict) && !time.Now().Before(current.ExpiresAt) {
			return false, nil
		}
		return false, errRequestApproval
	}
	return true, nil
}
func (s *RequestApprovalService) authorized(task int64, owner string) bool {
	_, ok := s.bindings[task]
	return ok && owner == "ARTEX"
}
func (s *RequestApprovalService) decorate(a db.RequestApproval) db.RequestApproval {
	b, bok := s.bindings[a.TaskID]
	w := s.waiters[a.ID]
	a.Live = bok && b.ctx.Err() == nil && b.run == a.RunRef && w != nil && !w.failed && (w.ctx.Err() == nil || a.Consumed)
	// 결과 저장/감사 실패로 경계가 닫혔다면 남은 dispatch 상태는 성공이나 대기가 아니다.
	if b.execution != nil && b.execution.Snapshot().Reason == e2proxy.AuditFailed {
		a.Live = false
	}
	if !a.Live && a.Consumed && (a.ExecutionState == "awaiting_dispatch" || a.ExecutionState == "dispatch_pending") {
		a.ExecutionState = "unknown"
	}
	if a.State == "pending" || a.State == "allowed" && !a.Consumed {
		if !time.Now().Before(a.ExpiresAt) {
			a.State = "expired"
		} else if !a.Live {
			a.State = "cancelled"
		}
	}
	a.CanDecide = a.Live && a.State == "pending" && time.Now().Before(a.ExpiresAt)
	return a
}
func (s *RequestApprovalService) list(ctx context.Context, task int64, owner string) ([]db.RequestApproval, error) {
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
	return s.decorate(a), nil
}
func (s *RequestApprovalService) observe(ctx context.Context, event e2proxy.Event) error {
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
		return errRequestApproval
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
func (s *RequestApprovalService) stop(ctx context.Context, task int64, owner string) error {
	s.mu.Lock()
	binding, ok := s.bindings[task]
	s.mu.Unlock()
	if !ok || owner != "ARTEX" || binding.execution == nil {
		return errRequestApproval
	}
	return binding.execution.Stop(ctx)
}

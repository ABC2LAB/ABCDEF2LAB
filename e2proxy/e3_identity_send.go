package e2proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

// E3AccountBindings는 현재 실행·계정·세션 판을 함께 검사하는 신뢰 저장소다.
// 서버의 C14 세션 저장소가 구현한다. 값은 감사 이벤트에 넣지 않는다.
type E3AccountBindings interface {
	AccountBindingRef(runRef, label string) (string, bool)
	BoundSession(runRef, label, ref string) (http.Header, error)
}

type E3IdentityInput struct {
	Binding      e3contract.RunBinding
	Role         e3contract.Role
	AccountLabel string
	Spec         e3contract.IdentitySpec
	Marker       e3contract.Marker
	Accounts     E3AccountBindings
	DigestKey    []byte
	Timeout      time.Duration
}

type e3ProgramCall struct {
	audit E3Audit
	// approval은 이 요청이 사용자 승인을 기다렸을 때의 결과다(승인 대기 없음이면 빈 값).
	approval RequestApprovalOutcome
	// timeoutAfterApproval이면 요청 시간 상한(timeout)을 승인을 받은 뒤 송신부터 센다(사용자 승인 모드의 생성 요청,
	// 사용자 결정 2026-11-21). sendCtx는 그때 만든 송신 문맥이다.
	timeoutAfterApproval bool
	sendCtx              context.Context
	kind                 ProgramCallKind
	session              http.Header
	marker               []byte
	subjectPath          string
	digestKey            []byte
	timeout              time.Duration
	accounts             E3AccountBindings
	runRef               string
	label                string
	accountRef           string
	idPath               string
	objectID             string
	createdID            string
	dataPath             string
	statusPath           string
	normalValue          string
	markerField          string
}

var e3SubjectPathPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

func (c *e3ProgramCall) current() bool {
	ref, ok := c.accounts.AccountBindingRef(c.runRef, c.label)
	if !ok || ref != c.accountRef {
		return false
	}
	current, err := c.accounts.BoundSession(c.runRef, c.label, ref)
	if err != nil {
		return false
	}
	current, err = cloneHeaderClass(current, true)
	return err == nil && reflect.DeepEqual(current, c.session)
}

// SendE3Identity는 등록된 보호 표식과 현재 C14 세션으로 신원 GET 한 건을 기존 E② 전송 경계에 보낸다.
// 제품 Host Recorder가 Sync·현재 head 게시 ACK를 확인한 결과 행만 반환한다. 신원 원문은 반환하지 않는다.
func (e *Execution) SendE3Identity(ctx context.Context, in E3IdentityInput) (Event, error) {
	if e == nil || ctx == nil || ctx.Err() != nil || in.Binding.Validate() != nil ||
		in.Binding.Policy.ValidateProduct() != nil || in.Accounts == nil || !in.Marker.Valid() ||
		(in.Role != e3contract.RoleOwner && in.Role != e3contract.RoleCross) ||
		!validJournalRef(in.AccountLabel) || !validE3RulePath(in.Spec.Path, false) ||
		!e3SubjectPathPattern.MatchString(in.Spec.SubjectIDPath) || len(in.DigestKey) != sha256.Size || in.Timeout <= 0 {
		return Event{}, ErrConfig
	}
	marker := in.Marker.Reveal()
	defer clear(marker)
	e.mu.Lock()
	bound := e.cfg.AuditVersion == JournalVersionV3 && e.cfg.E3Verifier != nil && e.cfg.E3ConfirmationRequests > 0 && e.cfg.Scope.RunRef == in.Binding.RunRef &&
		e.cfg.Scope.TaskRef == in.Binding.TaskRef && e.cfg.Scope.PolicyRef == in.Binding.E2PolicyRef &&
		bytes.Equal(e.marker, marker) && !e.stopped && !e.auditFailed
	var origin string
	for _, rule := range e.rules {
		if rule.Purpose == PurposeE3Identity && rule.PathPrefix == in.Spec.Path && rule.methods["GET"] {
			origin = rule.origin.Scheme + "://" + rule.authority
			break
		}
	}
	e.mu.Unlock()
	if !bound {
		return Event{}, ErrE3JournalUnavailable
	}
	if origin == "" {
		return Event{}, ErrDenied
	}
	ref, ok := in.Accounts.AccountBindingRef(in.Binding.RunRef, in.AccountLabel)
	if !ok || !validJournalRef(ref) {
		return Event{}, ErrDenied
	}
	session, err := in.Accounts.BoundSession(in.Binding.RunRef, in.AccountLabel, ref)
	if err != nil {
		return Event{}, ErrDenied
	}
	session, err = cloneHeaderClass(session, true)
	if err != nil || len(session) == 0 || !e.authSessionSafe(session) {
		return Event{}, ErrDenied
	}
	for name, values := range session {
		if e3contract.ContainsMarker([]byte(name), in.Marker) {
			return Event{}, ErrDenied
		}
		for _, value := range values {
			if e3contract.ContainsMarker([]byte(value), in.Marker) {
				return Event{}, ErrDenied
			}
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+in.Spec.Path, nil)
	if err != nil || e3contract.ContainsMarker([]byte(request.URL.String()), in.Marker) {
		return Event{}, ErrConfig
	}
	call, err := e.beginCall(ctx, 0, ProgramE3Identity, "", false, false)
	if err != nil {
		return Event{}, err
	}
	metadata := &e3ProgramCall{kind: ProgramE3Identity, audit: E3Audit{Role: in.Role, AccountLabel: in.AccountLabel,
		AccountBindingRef: ref, E3: E3Binding{JobRef: in.Binding.JobRef,
			GenerationRef: in.Binding.GenerationRef, InstanceRef: in.Binding.InstanceRef,
			Policy: in.Binding.Policy}}, session: session, marker: bytes.Clone(marker),
		subjectPath: in.Spec.SubjectIDPath, digestKey: bytes.Clone(in.DigestKey), timeout: in.Timeout,
		accounts: in.Accounts, runRef: in.Binding.RunRef, label: in.AccountLabel, accountRef: ref}
	defer clear(metadata.marker)
	defer clear(metadata.digestKey)
	requestCtx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	_, _, receipt, sendErr := call.doWithE3(requestCtx, request, PurposeE3Identity, nil, metadata)
	return e.e3Result(ctx, requestCtx, call, receipt, sendErr, metadata)
}

func (e *Execution) e3Result(ctx, requestCtx context.Context, call *Call, receipt Receipt, sendErr error, metadata *e3ProgramCall) (Event, error) {
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), e.cfg.Limits.RequestTimeout)
	_, closeErr := call.Close(closeCtx)
	closeCancel()
	if closeErr != nil {
		return Event{}, closeErr
	}
	if receipt.Reason == Revoked {
		return Event{}, e3contract.ErrAuthorityRevoked
	}
	if receipt.Decision == "allowed" && receipt.NotSent && receipt.Reason == Closed {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		if err := requestCtx.Err(); err != nil {
			return Event{}, err
		}
		// 송신 문맥은 doWithE3가 끝날 때 취소되므로(defer) 여기서는 늘 오류가 있다. 송신 상한 초과만 따로 가르고, 철회·마감은
		// 아래에서 가른다(#839 윤재상 리뷰).
		if metadata.sendCtx != nil && errors.Is(metadata.sendCtx.Err(), context.DeadlineExceeded) {
			return Event{}, metadata.sendCtx.Err()
		}
		if !metadata.current() {
			return Event{}, e3contract.ErrAuthorityRevoked
		}
		return Event{}, ErrClosed
	}
	e.mu.Lock()
	var result Event
	for i := len(e.events) - 1; i >= 0; i-- {
		ev := e.events[i]
		if ev.Stage == "result" && ev.CallRef == call.ref && ev.AttemptID == receipt.AttemptID && ev.Sequence == receipt.Sequence {
			result = ev
			break
		}
	}
	e.mu.Unlock()
	if result.Stage != "result" || !result.AuditComplete || !validE3AuditEvent(result) {
		if sendErr != nil {
			return Event{}, sendErr
		}
		return Event{}, ErrAudit
	}
	return result, nil
}

func (c *e3ProgramCall) finish(r *Receipt, ctx context.Context) {
	if !r.ForwardAttempted && r.Decision != "allowed" {
		// 운영자 거절·승인 만료는 계약(e3contract.Transport)대로 따로 남긴다. 감사 사유는 그대로다(거절·만료는
		// policy_denied, 거절과 동시에 문맥이 끝났으면 closed).
		approvalEnded := r.Reason == PolicyDenied || r.Reason == Closed
		switch {
		case c.approval == RequestApprovalDenied && approvalEnded:
			c.audit.NotSentReason = e3contract.NotSentApprovalDenied
		case c.approval == RequestApprovalExpired && approvalEnded:
			c.audit.NotSentReason = e3contract.NotSentApprovalTimeout
		case r.Reason == BudgetExhausted || r.Reason == AttemptLimit:
			c.audit.NotSentReason = e3contract.NotSentBudgetExhausted
		default:
			c.audit.NotSentReason = e3contract.NotSentPolicyDenied
		}
	}
	if r.ForwardAttempted && !r.ResponseComplete && deadlinePassed(ctx) {
		c.audit.TimedOut = true
	}
	*r.E3Audit = c.audit
}

func (c *e3ProgramCall) inspect(response *http.Response, body []byte, r *Receipt) {
	r.ResponseComplete, r.Reason = true, Complete
	digest := sha256.Sum256(body)
	c.audit.BodyDigestSHA256 = hex.EncodeToString(digest[:])
	complete, markerFree := true, true
	c.audit.BodyComplete, c.audit.RequestMarkerFree = &complete, &markerFree
	c.audit.Normal = oracle.StateUnknown
	if c.kind == ProgramE3Create {
		c.audit.RequestMarkerFree = nil
		c.inspectCreate(response, body)
		return
	}
	if c.kind == ProgramE3Read {
		c.inspectRead(response, body)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.audit.Normal = oracle.StateNo
		return
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root any
	if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF {
		return
	}
	var value any = root
	for _, field := range strings.Split(c.subjectPath, ".") {
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		value, ok = obj[field]
		if !ok {
			return
		}
	}
	var subject string
	switch v := value.(type) {
	case string:
		subject = v
	case json.Number:
		subject = string(v)
	}
	if subject == "" {
		return
	}
	h := hmac.New(sha256.New, c.digestKey)
	_, _ = h.Write([]byte("e3-subject\x00"))
	_, _ = h.Write([]byte(subject))
	c.audit.SubjectDigest = hex.EncodeToString(h.Sum(nil))
	c.audit.Normal = oracle.StateYes
}

// deadlinePassed는 요청 문맥이 기한에 닿았는지다. 송신은 E② 요청 시간 상한(workCtx)으로도 끊기는데, 그 기한이 호출자 기한과
// 거의 같으면 호출자 문맥의 타이머 콜백이 늦어 Err()가 아직 비어 있을 수 있다(부하 중 간헐, 2026-11-21). 기한 시각이 지났으면
// 시간 초과로 본다.
func deadlinePassed(ctx context.Context) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

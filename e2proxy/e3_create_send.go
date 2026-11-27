package e2proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

type E3CreateInput struct {
	Binding      e3contract.RunBinding
	ResourceType string
	AccountLabel string
	Spec         e3contract.CreateSpec
	Marker       e3contract.Marker
	Body         e3contract.RequestBody
	ObjectID     string
	Accounts     E3AccountBindings
	DigestKey    []byte
	Timeout      time.Duration
}

var e3IssuedObjectID = regexp.MustCompile(`^e3id-[0-9a-f]{32}$`)

// SendE3Create는 선언된 쓰기 경로에 표식을 심는 요청 한 건을 E②로 송신한다.
// 응답 ID 원문은 실행 메모리에만 두고 v3 감사에는 digest만 남긴다.
func (e *Execution) SendE3Create(ctx context.Context, in E3CreateInput) (Event, error) {
	if e == nil || ctx == nil || ctx.Err() != nil || in.Binding.Validate() != nil ||
		in.Binding.Policy.ValidateProduct() != nil || !e3ResourceTypePattern.MatchString(in.ResourceType) ||
		!validJournalRef(in.AccountLabel) || in.Accounts == nil || !in.Marker.Valid() || !in.Body.Valid() ||
		!validE3RulePath(in.Spec.Path, false) || (in.Spec.Method != http.MethodPost && in.Spec.Method != http.MethodPut) ||
		in.Body.Kind() != in.Spec.Kind || (in.Spec.IDPath == "") == (in.Spec.IDField == "") ||
		in.Spec.IDPath != "" && !e3SubjectPathPattern.MatchString(in.Spec.IDPath) ||
		in.Spec.IDField != "" && (!e3SubjectPathPattern.MatchString(in.Spec.IDField) || !e3IssuedObjectID.MatchString(in.ObjectID)) ||
		in.Spec.IDField == "" && in.ObjectID != "" || len(in.DigestKey) != sha256.Size || in.Timeout <= 0 {
		return Event{}, ErrConfig
	}
	marker := in.Marker.Reveal()
	defer clear(marker)
	body := in.Body.Reveal()
	defer clear(body)
	expected, err := e3contract.AssembleBody(in.Spec, in.Marker, in.ObjectID)
	if err != nil {
		return Event{}, ErrConfig
	}
	expectedBody := expected.Reveal()
	matched := bytes.Equal(body, expectedBody)
	clear(expectedBody)
	if !matched || !e3contract.ContainsMarker(body, in.Marker) {
		return Event{}, ErrConfig
	}
	e.mu.Lock()
	bound := e.cfg.AuditVersion == JournalVersionV3 && e.cfg.E3Verifier != nil && e.cfg.E3ConfirmationRequests > 0 &&
		e.cfg.E3WriteConsent && e.cfg.E3Resettable && e.cfg.Scope.RunRef == in.Binding.RunRef &&
		e.cfg.Scope.TaskRef == in.Binding.TaskRef && e.cfg.Scope.PolicyRef == in.Binding.E2PolicyRef &&
		bytes.Equal(e.marker, marker) && !e.stopped && !e.auditFailed
	var origin string
	for _, rule := range e.rules {
		if rule.Purpose == PurposeE3Create && rule.PathPrefix == in.Spec.Path && rule.methods[in.Spec.Method] {
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
	request, err := http.NewRequestWithContext(ctx, in.Spec.Method, origin+in.Spec.Path, bytes.NewReader(body))
	if err != nil || e3contract.ContainsMarker([]byte(request.URL.String()), in.Marker) {
		return Event{}, ErrConfig
	}
	contentType := "application/json"
	if in.Spec.Kind == e3contract.BodyForm {
		contentType = "application/x-www-form-urlencoded"
	}
	request.Header.Set("Content-Type", contentType)
	call, err := e.beginCall(ctx, 0, ProgramE3Create, "", false, false)
	if err != nil {
		return Event{}, err
	}
	metadata := &e3ProgramCall{kind: ProgramE3Create, audit: E3Audit{Role: e3contract.RoleOwner,
		AccountLabel: in.AccountLabel, AccountBindingRef: ref, ResourceType: in.ResourceType,
		E3: E3Binding{JobRef: in.Binding.JobRef, GenerationRef: in.Binding.GenerationRef,
			InstanceRef: in.Binding.InstanceRef, Policy: in.Binding.Policy}},
		session: session, marker: bytes.Clone(marker), digestKey: bytes.Clone(in.DigestKey),
		timeout: in.Timeout, accounts: in.Accounts, runRef: in.Binding.RunRef,
		label: in.AccountLabel, accountRef: ref, idPath: in.Spec.IDPath, objectID: in.ObjectID}
	defer clear(metadata.marker)
	defer clear(metadata.digestKey)
	// 사용자 승인 모드(요청별 승인 경계가 있는 실행)에서는 운영자 승인 대기를 요청 시간 상한에 넣지 않는다. 대기는 승인
	// 기한(E② 호출 기한)과 호출자 문맥(확정 단계 시간 몫) 안에서만 하고, 상한은 승인을 받은 뒤 송신부터 센다(사용자 결정
	// 2026-11-21). 자동 모드는 지금처럼 이 요청 전체가 상한 안이다.
	requestCtx, cancel := ctx, context.CancelFunc(func() {})
	if e.needsRequestApproval(PurposeE3Create, in.Spec.Method, body, RequestView{}) {
		metadata.timeoutAfterApproval = true
	} else {
		requestCtx, cancel = context.WithTimeout(ctx, in.Timeout)
	}
	defer cancel()
	_, _, receipt, sendErr := call.doWithE3(requestCtx, request, PurposeE3Create, nil, metadata)
	result, err := e.e3Result(ctx, requestCtx, call, receipt, sendErr, metadata)
	if err != nil {
		return Event{}, err
	}
	if result.ResponseComplete && result.E3Audit != nil && result.E3Audit.Normal == oracle.StateYes && metadata.createdID != "" {
		e.mu.Lock()
		if e.e3Created == nil {
			e.e3Created = make(map[e3contract.CallRef]string)
		}
		e.e3Created[e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}] = metadata.createdID
		e.mu.Unlock()
	}
	return result, nil
}

func (c *e3ProgramCall) inspectCreate(response *http.Response, body []byte) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.audit.Normal = oracle.StateNo
		return
	}
	id := c.objectID
	if c.idPath != "" {
		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			c.audit.Normal = oracle.StateUnknown
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var root any
		if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF {
			c.audit.Normal = oracle.StateUnknown
			return
		}
		value := root
		for _, field := range strings.Split(c.idPath, ".") {
			obj, ok := value.(map[string]any)
			if !ok {
				return
			}
			value, ok = obj[field]
			if !ok {
				return
			}
		}
		switch v := value.(type) {
		case string:
			id = v
		case json.Number:
			id = string(v)
		}
	}
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/;?{}%\\\r\n") {
		return
	}
	c.audit.ObjectIDDigest = e3ObjectDigest(c.digestKey, id)
	c.createdID = id
	c.audit.Normal = oracle.StateYes
}

func e3ObjectDigest(key []byte, id string) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte("e3-object\x00"))
	_, _ = h.Write([]byte(id))
	return hex.EncodeToString(h.Sum(nil))
}

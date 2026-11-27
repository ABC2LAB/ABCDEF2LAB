package e2proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

type E3ReadInput struct {
	Binding      e3contract.RunBinding
	ResourceType string
	Role         e3contract.Role
	AccountLabel string
	Spec         e3contract.ReadSpec
	MarkerField  string
	CreateRef    e3contract.CallRef
	ObjectID     string
	Marker       e3contract.Marker
	Accounts     E3AccountBindings
	DigestKey    []byte
	Timeout      time.Duration
}

// SendE3Read는 Host가 검증한 생성 사건의 ID만 선언된 경로 틀에 넣어 GET 한 건을 보낸다.
func (e *Execution) SendE3Read(ctx context.Context, in E3ReadInput) (Event, error) {
	if e == nil || ctx == nil || ctx.Err() != nil || in.Binding.Validate() != nil ||
		in.Binding.Policy.ValidateProduct() != nil || !e3ResourceTypePattern.MatchString(in.ResourceType) ||
		(in.Role != e3contract.RoleOwner && in.Role != e3contract.RoleCross) ||
		!validJournalRef(in.AccountLabel) || in.Accounts == nil || !in.Marker.Valid() ||
		!validE3RulePath(in.Spec.PathTemplate, true) ||
		(in.Spec.DataPath != "." && !e3SubjectPathPattern.MatchString(in.Spec.DataPath)) ||
		!e3SubjectPathPattern.MatchString(in.MarkerField) ||
		(in.Spec.StatusPath == "") != (in.Spec.NormalValue == "") ||
		in.Spec.StatusPath != "" && !e3SubjectPathPattern.MatchString(in.Spec.StatusPath) ||
		!validJournalRef(in.CreateRef.CallRef) || !validJournalHex(in.CreateRef.AttemptID, randomRefBytes) ||
		in.CreateRef.Sequence == 0 || len(in.DigestKey) != sha256.Size || in.Timeout <= 0 {
		return Event{}, ErrConfig
	}
	marker := in.Marker.Reveal()
	defer clear(marker)
	e.mu.Lock()
	bound := e.cfg.AuditVersion == JournalVersionV3 && e.cfg.E3Verifier != nil && e.cfg.E3ConfirmationRequests > 0 &&
		e.cfg.Scope.RunRef == in.Binding.RunRef && e.cfg.Scope.TaskRef == in.Binding.TaskRef &&
		e.cfg.Scope.PolicyRef == in.Binding.E2PolicyRef && bytes.Equal(e.marker, marker) && !e.stopped && !e.auditFailed
	id := e.e3Created[in.CreateRef]
	var origin string
	for _, rule := range e.rules {
		if rule.Purpose == PurposeE3Read && rule.PathPrefix == in.Spec.PathTemplate && rule.methods["GET"] {
			origin = rule.origin.Scheme + "://" + rule.authority
			break
		}
	}
	verifier := e.cfg.E3Verifier
	e.mu.Unlock()
	if !bound {
		return Event{}, ErrE3JournalUnavailable
	}
	if id == "" || in.ObjectID != "" && in.ObjectID != id || origin == "" {
		return Event{}, ErrDenied
	}
	created, err := verifier.Verified(ctx, in.Binding, in.CreateRef)
	if err != nil {
		return Event{}, e3contract.ErrJournalIntegrity
	}
	if created.Version != JournalVersionV3 || created.Stage != "result" || created.Program != ProgramE3Create ||
		!created.AuditComplete || !created.ResponseComplete || created.E3Audit == nil ||
		created.CallRef != in.CreateRef.CallRef || created.AttemptID != in.CreateRef.AttemptID ||
		created.Sequence != in.CreateRef.Sequence || created.Scope.RunRef != in.Binding.RunRef ||
		created.Scope.TaskRef != in.Binding.TaskRef || created.Scope.PolicyRef != in.Binding.E2PolicyRef ||
		created.E3Audit.E3 != (E3Binding{JobRef: in.Binding.JobRef, GenerationRef: in.Binding.GenerationRef,
			InstanceRef: in.Binding.InstanceRef, Policy: in.Binding.Policy}) ||
		created.E3Audit.ResourceType != in.ResourceType || created.E3Audit.Role != e3contract.RoleOwner ||
		created.E3Audit.Normal != oracle.StateYes ||
		created.E3Audit.ObjectIDDigest != e3ObjectDigest(in.DigestKey, id) || !validE3AuditEvent(created) {
		return Event{}, e3contract.ErrJournalIntegrity
	}
	ref, ok := in.Accounts.AccountBindingRef(in.Binding.RunRef, in.AccountLabel)
	if !ok || !validJournalRef(ref) {
		return Event{}, ErrDenied
	}
	if in.Role == e3contract.RoleOwner && (in.AccountLabel != created.E3Audit.AccountLabel ||
		ref != created.E3Audit.AccountBindingRef) ||
		in.Role == e3contract.RoleCross && in.AccountLabel == created.E3Audit.AccountLabel {
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
	actual := strings.Replace(in.Spec.PathTemplate, "{id}", url.PathEscape(id), 1)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+actual, nil)
	if err != nil || e3contract.ContainsMarker([]byte(request.URL.String()), in.Marker) {
		return Event{}, ErrDenied
	}
	call, err := e.beginCall(ctx, 0, ProgramE3Read, "", false, false)
	if err != nil {
		return Event{}, err
	}
	metadata := &e3ProgramCall{kind: ProgramE3Read, audit: E3Audit{Role: in.Role,
		AccountLabel: in.AccountLabel, AccountBindingRef: ref, ResourceType: in.ResourceType,
		CreateRef: in.CreateRef, E3: E3Binding{JobRef: in.Binding.JobRef,
			GenerationRef: in.Binding.GenerationRef, InstanceRef: in.Binding.InstanceRef,
			Policy: in.Binding.Policy}}, session: session, marker: bytes.Clone(marker),
		digestKey: bytes.Clone(in.DigestKey), timeout: in.Timeout, accounts: in.Accounts,
		runRef: in.Binding.RunRef, label: in.AccountLabel, accountRef: ref,
		dataPath: in.Spec.DataPath, statusPath: in.Spec.StatusPath,
		normalValue: in.Spec.NormalValue, markerField: in.MarkerField}
	defer clear(metadata.marker)
	defer clear(metadata.digestKey)
	requestCtx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	_, _, receipt, sendErr := call.doWithE3(requestCtx, request, PurposeE3Read, nil, metadata)
	return e.e3Result(ctx, requestCtx, call, receipt, sendErr, metadata)
}

func (c *e3ProgramCall) inspectRead(response *http.Response, body []byte) {
	c.audit.Marker = e3contract.MarkerCheck{Status: e3contract.MarkerFailed, Field: c.markerField}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.audit.Normal = oracle.StateNo
	} else {
		c.audit.Normal = oracle.StateYes
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		c.audit.Marker.Status = e3contract.MarkerUnsupported
		if c.audit.Normal == oracle.StateYes {
			c.audit.Normal = oracle.StateUnknown
		}
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root any
	if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF {
		c.audit.Normal = oracle.StateUnknown
		return
	}
	if c.statusPath != "" && c.audit.Normal == oracle.StateYes {
		value, ok := e3JSONPath(root, c.statusPath)
		if !ok {
			c.audit.Normal = oracle.StateUnknown
		} else if value == c.normalValue {
			c.audit.Normal = oracle.StateYes
		} else {
			c.audit.Normal = oracle.StateNo
		}
	}
	data := root
	if c.dataPath != "." {
		var ok bool
		data, ok = e3JSONPath(root, c.dataPath)
		if !ok {
			return
		}
	}
	value, ok := e3JSONPath(data, c.markerField)
	if !ok {
		return
	}
	field, ok := value.(string)
	if !ok {
		return
	}
	present, method, supported := inspectProgramMarker([]byte(field), c.marker)
	if !supported {
		c.audit.Marker.Status = e3contract.MarkerUnsupported
		return
	}
	c.audit.Marker.Status = e3contract.MarkerComplete
	if present {
		c.audit.Marker.Presence = e3contract.MarkerPresent
		c.audit.Marker.Method = e3contract.MatchMethod(method)
	} else {
		c.audit.Marker.Presence = e3contract.MarkerAbsent
	}
}

func e3JSONPath(root any, path string) (any, bool) {
	value := root
	for _, part := range strings.Split(path, ".") {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	switch v := value.(type) {
	case string:
		return v, true
	case json.Number:
		return string(v), true
	default:
		return value, true
	}
}

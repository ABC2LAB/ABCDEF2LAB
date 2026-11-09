package diagnosisapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
)

const managementCallTimeout = 10 * time.Second

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var errInput = errors.New("진단 요청을 확인할 수 없습니다")

type storedDraft struct {
	mu        sync.Mutex
	draft     Draft
	owner     string
	validated *Binding
	startKey  string
}
type createReceipt struct{ digest, id string }
type Handler struct {
	mu      sync.Mutex
	options Options
	drafts  map[string]*storedDraft
	creates map[string]createReceipt
}

// New는 별도 실행 프로세스와 이미 연결된 Gateway만 받는다. 프로세스·권위를 만들지 않는다.
// 초안과 HTTP 멱등성 기록은 메모리 한정이다. 재시작 후 초안은 소실되며 실행 marker는 Gateway의 책임이다.
func New(options Options) (*Handler, error) {
	if options.Authenticate == nil {
		return nil, errInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.CallTimeout <= 0 || options.CallTimeout > managementCallTimeout {
		options.CallTimeout = managementCallTimeout
	}
	if options.MaxDrafts <= 0 {
		options.MaxDrafts = 1000
	}
	return &Handler{options: options, drafts: make(map[string]*storedDraft), creates: make(map[string]createReceipt)}, nil
}

type requestBody struct {
	Version          string         `json:"version"`
	ExpectedRevision uint64         `json:"expected_revision,omitempty"`
	Configuration    *Configuration `json:"configuration,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"version": Version, "error": code})
}
func writeDraft(w http.ResponseWriter, status int, d Draft) {
	writeJSON(w, status, struct {
		Version string `json:"version"`
		Draft   Draft  `json:"draft"`
	}{Version, d})
}

// duplicateKeys는 Go JSON decoder의 마지막 중복 키 우선 동작을 요청 경계에서 막는다.
func duplicateKeys(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	var visit func() error
	visit = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				text, ok := key.(string)
				if !ok || seen[strings.ToLower(text)] {
					return errInput
				}
				seen[strings.ToLower(text)] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return errInput
		}
		_, err = dec.Token()
		return err
	}
	if visit() != nil {
		return true
	}
	_, err := dec.Token()
	return err != io.EOF
}
func decode(w http.ResponseWriter, r *http.Request) (requestBody, error) {
	var body requestBody
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return body, errInput
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil || duplicateKeys(raw) {
		return body, errInput
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || body.Version != Version {
		return body, errInput
	}
	canonical, _ := json.Marshal(body)
	var input, known any
	if json.Unmarshal(raw, &input) != nil || json.Unmarshal(canonical, &known) != nil || !canonicalKeys(input, known) {
		return body, errInput
	}
	return body, nil
}

// Go decoder의 대소문자 alias를 허용하지 않고 공개 JSON 필드명만 받는다.
func canonicalKeys(input, known any) bool {
	switch value := input.(type) {
	case map[string]any:
		fields, ok := known.(map[string]any)
		if !ok {
			return false
		}
		for key, item := range value {
			expected, exists := fields[key]
			if !exists || !canonicalKeys(item, expected) {
				return false
			}
		}
	case []any:
		items, ok := known.([]any)
		if !ok || len(value) != len(items) {
			return false
		}
		for i, item := range value {
			if !canonicalKeys(item, items[i]) {
				return false
			}
		}
	}
	return true
}

func validConfiguration(c Configuration) bool {
	return refPattern.MatchString(c.TargetRef) && refPattern.MatchString(c.ScopeRef) && refPattern.MatchString(c.ModelProfileRef) &&
		(c.ApprovalMode == "confirm_every_request" || c.ApprovalMode == "preauthorized_scope") &&
		c.Budget.Requests > 0 && c.Budget.Requests <= 1_000_000 && c.Budget.Tokens > 0 && c.Budget.Tokens <= 1_000_000_000 &&
		c.Budget.CostUSDMicros >= 0 && c.Budget.CostUSDMicros <= 1_000_000_000_000 && c.Budget.DurationSeconds > 0 && c.Budget.DurationSeconds <= 86400
}
func digest(c Configuration) string {
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (h *Handler) profiles(ctx context.Context, owner string) ([]Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, h.options.CallTimeout)
	defer cancel()
	if h.options.Registry == nil {
		return nil, errInput
	}
	profiles, err := h.options.Registry.Profiles(ctx, owner)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range profiles {
		if !refPattern.MatchString(p.ID) || p.Label == "" || len(p.Label) > 256 || !validConfiguration(p.Configuration) || seen[p.ID] {
			return nil, errInput
		}
		seen[p.ID] = true
	}
	if profiles == nil {
		profiles = []Profile{}
	}
	return profiles, nil
}
func containsConfiguration(profiles []Profile, c Configuration) bool {
	for _, p := range profiles {
		if p.Configuration == c {
			return true
		}
	}
	return false
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 명시적 Bearer만 허용한다. 인증 자체는 호스트의 기존 검증기에 위임한다.
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) == "" {
		fail(w, 401, "authentication_required")
		return
	}
	owner, err := h.options.Authenticate(r)
	if err != nil || owner == "" {
		fail(w, 401, "authentication_required")
		return
	}
	if r.URL.RawQuery != "" || strings.Contains(r.URL.Path, "%") {
		fail(w, 400, "invalid_request")
		return
	}
	if r.URL.Path == BasePath+"/catalog" && r.Method == http.MethodGet {
		profiles, err := h.profiles(r.Context(), owner)
		if err != nil {
			fail(w, 503, "operator_registry_unavailable")
			return
		}
		writeJSON(w, 200, struct {
			Version  string    `json:"version"`
			Profiles []Profile `json:"profiles"`
		}{Version, profiles})
		return
	}
	if r.URL.Path == BasePath+"/drafts" && r.Method == http.MethodGet {
		key := r.Header.Get("Idempotency-Key")
		if !refPattern.MatchString(key) {
			fail(w, 400, "idempotency_key_required")
			return
		}
		h.mu.Lock()
		receipt, ok := h.creates[owner+"\x00"+key]
		stored := h.drafts[receipt.id]
		h.mu.Unlock()
		if !ok || stored == nil {
			fail(w, 404, "draft_not_found")
			return
		}
		stored.mu.Lock()
		defer stored.mu.Unlock()
		writeDraft(w, 200, stored.draft)
		return
	}
	if r.URL.Path == BasePath+"/drafts" && r.Method == http.MethodPost {
		h.create(w, r, owner)
		return
	}
	tail, ok := strings.CutPrefix(r.URL.Path, BasePath+"/drafts/")
	if !ok {
		fail(w, 404, "route_not_found")
		return
	}
	parts := strings.Split(tail, "/")
	if len(parts) > 2 || !refPattern.MatchString(parts[0]) {
		fail(w, 404, "route_not_found")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if !(r.Method == http.MethodPatch && action == "" || r.Method == http.MethodGet && action == "status" || r.Method == http.MethodPost && (action == "validate" || action == "start" || action == "stop")) {
		fail(w, 405, "method_not_allowed")
		return
	}
	var body requestBody
	if r.Method != http.MethodGet {
		body, err = decode(w, r)
		if err != nil {
			fail(w, 400, "invalid_request")
			return
		}
	}
	h.mu.Lock()
	stored := h.drafts[parts[0]]
	h.mu.Unlock()
	if stored == nil || stored.owner != owner {
		fail(w, 404, "draft_not_found")
		return
	}
	stored.mu.Lock()
	defer stored.mu.Unlock()
	if r.Method == http.MethodGet {
		h.status(w, r, stored)
		return
	}
	if body.ExpectedRevision != stored.draft.Revision {
		fail(w, 400, "invalid_request_or_revision")
		return
	}
	if action == "" {
		h.update(w, r, stored, body)
		return
	}
	if body.Configuration != nil {
		fail(w, 400, "invalid_request")
		return
	}
	switch action {
	case "validate":
		h.validate(w, r, stored)
	case "start":
		h.start(w, r, stored)
	case "stop":
		h.stop(w, r, stored)
	}
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request, owner string) {
	body, err := decode(w, r)
	key := r.Header.Get("Idempotency-Key")
	if err != nil || body.Configuration == nil || body.ExpectedRevision != 0 || !refPattern.MatchString(key) || !validConfiguration(*body.Configuration) {
		fail(w, 400, "invalid_request")
		return
	}
	profiles, err := h.profiles(r.Context(), owner)
	if err != nil {
		fail(w, 503, "operator_registry_unavailable")
		return
	}
	if !containsConfiguration(profiles, *body.Configuration) {
		fail(w, 403, "configuration_not_registered")
		return
	}
	hash := digest(*body.Configuration)
	h.mu.Lock()
	receiptKey := owner + "\x00" + key
	if receipt, ok := h.creates[receiptKey]; ok {
		stored := h.drafts[receipt.id]
		h.mu.Unlock()
		if receipt.digest != hash {
			fail(w, 409, "idempotency_conflict")
			return
		}
		stored.mu.Lock()
		defer stored.mu.Unlock()
		writeDraft(w, 200, stored.draft)
		return
	}
	if len(h.drafts) >= h.options.MaxDrafts {
		h.mu.Unlock()
		fail(w, 503, "draft_capacity_reached")
		return
	}
	id, err := newID()
	if err != nil {
		h.mu.Unlock()
		fail(w, 503, "draft_unavailable")
		return
	}
	d := Draft{ID: id, Revision: 1, Configuration: *body.Configuration, SourceDigest: hash, State: "draft"}
	h.drafts[id] = &storedDraft{draft: d, owner: owner}
	h.creates[receiptKey] = createReceipt{hash, id}
	h.mu.Unlock()
	writeDraft(w, 201, d)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, s *storedDraft, body requestBody) {
	if s.startKey != "" {
		fail(w, 409, "already_started")
		return
	}
	if body.Configuration == nil || !validConfiguration(*body.Configuration) {
		fail(w, 400, "invalid_request")
		return
	}
	profiles, err := h.profiles(r.Context(), s.owner)
	if err != nil {
		fail(w, 503, "operator_registry_unavailable")
		return
	}
	if !containsConfiguration(profiles, *body.Configuration) {
		fail(w, 403, "configuration_not_registered")
		return
	}
	s.draft.Configuration = *body.Configuration
	s.draft.SourceDigest = digest(*body.Configuration)
	s.draft.Revision++
	s.draft.State = "draft"
	s.draft.TaskID = ""
	s.draft.Snapshot = nil
	s.validated = nil
	writeDraft(w, 200, s.draft)
}
func (h *Handler) binding(ctx context.Context, s *storedDraft) (*Binding, error) {
	ctx, cancel := context.WithTimeout(ctx, h.options.CallTimeout)
	defer cancel()
	if h.options.Registry == nil {
		return nil, errInput
	}
	b, err := h.options.Registry.Approval(ctx, s.owner, s.draft)
	if err != nil || b == nil {
		return b, err
	}
	task, taskErr := strconv.ParseInt(b.TaskID, 10, 64)
	if b.Principal != s.owner || b.DraftID != s.draft.ID || b.Revision != s.draft.Revision || b.SourceDigest != s.draft.SourceDigest ||
		taskErr != nil || task <= 0 || strconv.FormatInt(task, 10) != b.TaskID || !refPattern.MatchString(b.PreparationRef) || !h.options.Now().Before(b.ExpiresAt) {
		return nil, errInput
	}
	copy := *b
	// 같은 만료 시각은 파싱할 때 생긴 Location 포인터·monotonic 값과 무관하게 비교한다.
	copy.ExpiresAt = copy.ExpiresAt.UTC()
	return &copy, nil
}
func (h *Handler) call(ctx context.Context, s *storedDraft, b *Binding, cmd diagnosis.Command, key string) error {
	ctx, cancel := context.WithTimeout(ctx, h.options.CallTimeout)
	defer cancel()
	if h.options.Gateway == nil {
		return diagnosis.ErrUnavailable
	}
	id, err := newID()
	if err != nil {
		return err
	}
	response, err := h.options.Gateway.Call(ctx, diagnosis.Request{Version: diagnosis.Version, RequestID: id, Command: cmd, TaskID: b.TaskID, PreparationRef: b.PreparationRef, IdempotencyKey: key})
	if err != nil {
		return err
	}
	if response.Version != diagnosis.Version || response.RequestID != id || response.Error != "" || response.Snapshot == nil ||
		response.Snapshot.TaskID != b.TaskID || response.Snapshot.PreparationRef != b.PreparationRef {
		return diagnosis.ErrProtocol
	}
	if response.Snapshot.State == diagnosis.Prepared && (response.Snapshot.Started || response.Snapshot.DrainComplete || response.Snapshot.ResultReady) ||
		response.Snapshot.State == diagnosis.Unknown && !response.Snapshot.Started ||
		(response.Snapshot.State == diagnosis.Running || response.Snapshot.State == diagnosis.Stopping) &&
			(!response.Snapshot.Started || response.Snapshot.DrainComplete || response.Snapshot.ResultReady) {
		return diagnosis.ErrProtocol
	}
	if (response.Snapshot.State == diagnosis.Completed || response.Snapshot.State == diagnosis.Cancelled || response.Snapshot.State == diagnosis.Failed) &&
		(!response.Snapshot.Started || !response.Snapshot.DrainComplete || !response.Snapshot.ResultReady) {
		return diagnosis.ErrProtocol
	}
	if cmd == diagnosis.Start && !response.Snapshot.Started ||
		cmd == diagnosis.Stop && (response.Snapshot.State == diagnosis.Prepared || response.Snapshot.State == diagnosis.Running) {
		return diagnosis.ErrProtocol
	}
	snap := *response.Snapshot
	switch snap.State {
	case diagnosis.Prepared, diagnosis.Running, diagnosis.Stopping, diagnosis.Completed, diagnosis.Cancelled, diagnosis.Failed, diagnosis.Unknown:
	default:
		return diagnosis.ErrProtocol
	}
	s.draft.Snapshot = &snap
	s.draft.TaskID = snap.TaskID
	return nil
}
func (h *Handler) validate(w http.ResponseWriter, r *http.Request, s *storedDraft) {
	if s.startKey != "" {
		fail(w, 409, "already_started")
		return
	}
	s.validated = nil
	s.draft.Snapshot = nil
	s.draft.TaskID = ""
	b, err := h.binding(r.Context(), s)
	if err != nil {
		s.draft.State = "awaiting_approval"
		fail(w, 409, "approval_binding_invalid")
		return
	}
	if b == nil {
		s.draft.State = "awaiting_approval"
		writeDraft(w, 200, s.draft)
		return
	}
	if h.call(r.Context(), s, b, diagnosis.Validate, "") != nil {
		s.draft.State = "unknown"
		fail(w, 503, "execution_service_unavailable")
		return
	}
	if s.draft.Snapshot.State != diagnosis.Prepared || s.draft.Snapshot.Started {
		s.draft.State = "unknown"
		fail(w, 409, "not_prepared")
		return
	}
	s.validated = b
	s.draft.State = "prepared"
	writeDraft(w, 200, s.draft)
}
func (h *Handler) start(w http.ResponseWriter, r *http.Request, s *storedDraft) {
	key := r.Header.Get("Idempotency-Key")
	if !refPattern.MatchString(key) {
		fail(w, 400, "idempotency_key_required")
		return
	}
	if s.startKey != "" {
		if s.startKey != key {
			fail(w, 409, "already_started")
			return
		}
		// 응답 불명 뒤 같은 POST도 재전송하지 않는다. GET status로 먼저 관측한다.
		writeDraft(w, 200, s.draft)
		return
	}
	if s.validated == nil || s.draft.State != "prepared" {
		fail(w, 409, "approval_required")
		return
	}
	b, err := h.binding(r.Context(), s)
	if err != nil || b == nil || *b != *s.validated {
		s.validated = nil
		s.draft.State = "awaiting_approval"
		s.draft.Snapshot = nil
		s.draft.TaskID = ""
		fail(w, 409, "approval_changed")
		return
	}
	s.startKey = key
	s.draft.State = "unknown"
	s.draft.Snapshot = nil
	if h.call(r.Context(), s, b, diagnosis.Start, key) != nil {
		fail(w, 503, "start_outcome_unknown")
		return
	}
	s.draft.State = string(s.draft.Snapshot.State)
	writeDraft(w, 202, s.draft)
}
func (h *Handler) status(w http.ResponseWriter, r *http.Request, s *storedDraft) {
	if s.startKey != "" && s.validated != nil {
		if h.call(r.Context(), s, s.validated, diagnosis.Status, "") != nil {
			s.draft.State = "unknown"
			s.draft.Snapshot = nil
			fail(w, 503, "execution_service_unavailable")
			return
		}
		s.draft.State = string(s.draft.Snapshot.State)
	}
	writeDraft(w, 200, s.draft)
}
func (h *Handler) stop(w http.ResponseWriter, r *http.Request, s *storedDraft) {
	if s.startKey == "" || s.validated == nil {
		fail(w, 409, "not_started")
		return
	}
	if s.draft.State != "running" {
		fail(w, 409, "not_running")
		return
	}
	s.draft.State = "unknown"
	if h.call(r.Context(), s, s.validated, diagnosis.Stop, "") != nil {
		s.draft.Snapshot = nil
		fail(w, 503, "stop_outcome_unknown")
		return
	}
	s.draft.State = string(s.draft.Snapshot.State)
	writeDraft(w, 202, s.draft)
}

// BoundTask는 기존 공개 결과/승인 API bridge에 task 권한만 제공한다.
// HTTP 입력으로 새 결속을 만들지 않고, validate에서 실제 Gateway가 확인한 준비만 사용한다.
func (h *Handler) BoundTask(ctx context.Context, principal, task string) bool {
	h.mu.Lock()
	candidates := make([]*storedDraft, 0, len(h.drafts))
	for _, stored := range h.drafts {
		if stored.owner == principal {
			candidates = append(candidates, stored)
		}
	}
	h.mu.Unlock()
	for _, stored := range candidates {
		stored.mu.Lock()
		binding := stored.validated
		if binding == nil || binding.TaskID != task {
			stored.mu.Unlock()
			continue
		}
		allowed := stored.startKey != ""
		if !allowed {
			current, err := h.binding(ctx, stored)
			allowed = err == nil && current != nil && *current == *binding
		}
		stored.mu.Unlock()
		if allowed {
			return true
		}
	}
	return false
}

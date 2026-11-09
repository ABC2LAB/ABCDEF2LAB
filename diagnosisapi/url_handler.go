package diagnosisapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/diagnosisprep"
)

const URLVersion = "diagnosis-api.v2"
const URLBasePath = "/api/diagnoses/v2"

// URLOptions는 운영자가 연결한 비공개 준비 서비스만 받는다.
// HTTP 요청에서 프로세스·모델·실행 권위를 만들거나 접속 주소를 선택하지 않는다.
type URLOptions struct {
	Authenticate Authenticate
	Endpoint     diagnosisprep.Endpoint
	CallTimeout  time.Duration
}
type URLHandler struct{ options URLOptions }

func NewURL(options URLOptions) (*URLHandler, error) {
	if options.Authenticate == nil {
		return nil, errInput
	}
	if options.CallTimeout <= 0 || options.CallTimeout > managementCallTimeout {
		options.CallTimeout = managementCallTimeout
	}
	return &URLHandler{options: options}, nil
}

type urlRequestBody struct {
	ExternalTransferConfirmed bool                 `json:"external_transfer_confirmed,omitempty"`
	Version                   string               `json:"version"`
	ExpectedRevision          uint64               `json:"expected_revision,omitempty"`
	Input                     *diagnosisprep.Input `json:"input,omitempty"`
}

func urlFail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"version": URLVersion, "error": code})
}
func urlOwnerContext(owner string) string {
	sum := sha256.Sum256([]byte("diagnosis-api.v2/owner\x00" + owner))
	return hex.EncodeToString(sum[:])
}
func decodeURL(w http.ResponseWriter, r *http.Request) (urlRequestBody, error) {
	var body urlRequestBody
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return body, errInput
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil || !utf8.Valid(raw) || duplicateKeys(raw) {
		return body, errInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.Version != URLVersion {
		return body, errInput
	}
	canonical, err := json.Marshal(body)
	var input, known any
	if err != nil || json.Unmarshal(raw, &input) != nil || json.Unmarshal(canonical, &known) != nil || !canonicalKeys(input, known) {
		return body, errInput
	}
	return body, nil
}
func (h *URLHandler) authenticate(r *http.Request) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer ")) == "" {
		return "", errInput
	}
	owner, err := h.options.Authenticate(r)
	if err != nil || owner == "" || len(owner) > 256 {
		return "", errInput
	}
	return owner, nil
}
func urlKey(r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	return r.Header.Get("Idempotency-Key"), len(values) == 1 && refPattern.MatchString(values[0])
}

func (h *URLHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	owner, err := h.authenticate(r)
	if err != nil {
		urlFail(w, 401, "authentication_required")
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Opaque != "" || strings.ContainsAny(r.URL.Path, "%\\") {
		urlFail(w, 400, "invalid_request")
		return
	}
	req := diagnosisprep.Request{Version: diagnosisprep.Version, Principal: owner}
	var mutation, created bool
	switch r.URL.Path {
	case URLBasePath + "/catalog":
		if r.Method != http.MethodGet {
			urlFail(w, 405, "method_not_allowed")
			return
		}
		req.Command = diagnosisprep.Catalog
	case URLBasePath + "/drafts":
		switch r.Method {
		case http.MethodGet:
			req.Command = diagnosisprep.Lookup
		case http.MethodPost:
			req.Command, mutation, created = diagnosisprep.Create, true, true
		default:
			urlFail(w, 405, "method_not_allowed")
			return
		}
		key, ok := urlKey(r)
		if !ok {
			urlFail(w, 400, "idempotency_key_required")
			return
		}
		req.IdempotencyKey = key
	default:
		tail, ok := strings.CutPrefix(r.URL.Path, URLBasePath+"/drafts/")
		parts := strings.Split(tail, "/")
		if !ok || len(parts) != 2 || !refPattern.MatchString(parts[0]) {
			urlFail(w, 404, "route_not_found")
			return
		}
		req.JobID = parts[0]
		switch parts[1] {
		case "status":
			if r.Method != http.MethodGet {
				urlFail(w, 405, "method_not_allowed")
				return
			}
			req.Command = diagnosisprep.Status
		case "validate", "start", "stop":
			if r.Method != http.MethodPost {
				urlFail(w, 405, "method_not_allowed")
				return
			}
			req.Command, mutation = diagnosisprep.Command(parts[1]), true
		default:
			urlFail(w, 404, "route_not_found")
			return
		}
	}
	if mutation {
		body, err := decodeURL(w, r)
		if err != nil || (body.ExternalTransferConfirmed && req.Command != diagnosisprep.Start) {
			urlFail(w, 400, "invalid_request")
			return
		}
		req.ExternalTransferConfirmed = body.ExternalTransferConfirmed
		if created {
			if body.Input == nil || body.ExpectedRevision != 0 || !validURLInput(*body.Input) {
				urlFail(w, 400, "invalid_request")
				return
			}
			req.Input = body.Input
		} else {
			if body.Input != nil || body.ExpectedRevision == 0 {
				urlFail(w, 400, "invalid_request")
				return
			}
			req.ExpectedRevision = body.ExpectedRevision
		}
	} else if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		urlFail(w, 400, "invalid_request")
		return
	}
	if req.Command == diagnosisprep.Start {
		key, ok := urlKey(r)
		if !ok {
			urlFail(w, 400, "idempotency_key_required")
			return
		}
		req.IdempotencyKey = key
	}
	// 느린 본문을 읽는 동안 로그아웃·비밀번호 변경·주체 교체가 생기면 전달하지 않는다.
	currentOwner, err := h.authenticate(r)
	if err != nil || currentOwner != owner {
		urlFail(w, 401, "authentication_required")
		return
	}
	response, err := h.call(r.Context(), req)
	if err != nil {
		urlFail(w, 503, "preparation_service_unavailable")
		return
	}
	if response.Error != "" {
		status, known := urlErrorStatus(response.Error)
		if !known {
			urlFail(w, 503, "preparation_service_unavailable")
			return
		}
		urlFail(w, status, response.Error)
		return
	}
	if req.Command == diagnosisprep.Catalog {
		profiles := response.Profiles
		if profiles == nil {
			profiles = []diagnosisprep.Profile{}
		}
		writeJSON(w, 200, struct {
			Version      string                  `json:"version"`
			OwnerContext string                  `json:"owner_context"`
			Profiles     []diagnosisprep.Profile `json:"profiles"`
		}{URLVersion, urlOwnerContext(owner), profiles})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, struct {
		Version      string             `json:"version"`
		OwnerContext string             `json:"owner_context"`
		Draft        *diagnosisprep.Job `json:"draft"`
	}{URLVersion, urlOwnerContext(owner), response.Job})
}

func validURLInput(input diagnosisprep.Input) bool {
	_, err := diagnosisprep.CanonicalInput(input)
	return err == nil
}
func urlErrorStatus(code string) (int, bool) {
	switch code {
	case "invalid_request":
		return 400, true
	case "forbidden":
		return 403, true
	case "not_found":
		return 404, true
	case "revision_conflict", "idempotency_conflict", "approval_changed", "approval_required", "busy", "budget_exhausted":
		return 409, true
	case "capacity", "unavailable", "unknown", "service_closed":
		return 503, true
	default:
		return 503, false
	}
}
func (h *URLHandler) call(ctx context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	if h.options.Endpoint == nil {
		return diagnosisprep.Response{}, diagnosisprep.ErrUnavailable
	}
	id, err := newID()
	if err != nil {
		return diagnosisprep.Response{}, err
	}
	req.RequestID = id
	if diagnosisprep.ValidateRequest(req) != nil {
		return diagnosisprep.Response{}, diagnosisprep.ErrProtocol
	}
	callContext, cancel := context.WithTimeout(ctx, h.options.CallTimeout)
	defer cancel()
	response, err := h.options.Endpoint.Call(callContext, req)
	if err != nil {
		return diagnosisprep.Response{}, err
	}
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil || len(raw) > diagnosisprep.MaxFrameBytes || diagnosisprep.ValidateResponse(req, response) != nil {
		return diagnosisprep.Response{}, diagnosisprep.ErrProtocol
	}
	return response, nil
}

// BoundTask는 영속 준비 영수증의 소유권을 조회한다. HTTP 세션의 메모리 초안은 권위가 아니다.
func (h *URLHandler) BoundTask(ctx context.Context, owner, task string) bool {
	n, err := strconv.ParseInt(task, 10, 64)
	if owner == "" || err != nil || n <= 0 || strconv.FormatInt(n, 10) != task {
		return false
	}
	response, err := h.call(ctx, diagnosisprep.Request{Version: diagnosisprep.Version, Command: diagnosisprep.AuthorizeTask, Principal: owner, TaskID: task})
	return err == nil && response.Error == "" && response.Authorized != nil && *response.Authorized
}

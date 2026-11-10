package diagnosisapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

type urlTestEndpoint struct {
	calls []diagnosisprep.Request
	reply func(diagnosisprep.Request) (diagnosisprep.Response, error)
}

func (e *urlTestEndpoint) Call(_ context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.calls = append(e.calls, req)
	if e.reply != nil {
		return e.reply(req)
	}
	response := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID}
	switch req.Command {
	case diagnosisprep.Catalog:
		response.Profiles = []diagnosisprep.Profile{{ID: "owned-read", Label: "소유 읽기 대상", ModelProfileRef: "local", ModelLabel: "합성 모델"}}
	case diagnosisprep.AuthorizeTask:
		allowed := true
		response.Authorized = &allowed
		job := urlTestJob()
		job.TaskID, job.PreparationStarted, job.State, job.Phase = req.TaskID, true, diagnosisprep.Unknown, diagnosisprep.Planning
		response.Job = &job
	default:
		job := urlTestJob()
		response.Job = &job
	}
	return response, nil
}
func urlTestJob() diagnosisprep.Job {
	return diagnosisprep.Job{ID: "job-1", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: diagnosisprep.Input{TargetURL: "https://owned.example/", ProfileID: "owned-read"}, State: diagnosisprep.AwaitingApproval, Phase: diagnosisprep.DraftPhase, AuthorityState: diagnosisprep.AuthorityNotIssued}
}
func urlTestHandler(t *testing.T, endpoint diagnosisprep.Endpoint) *URLHandler {
	t.Helper()
	handler, err := NewURL(URLOptions{Authenticate: func(*http.Request) (string, error) { return "owner", nil }, Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
func urlTestRequest(handler http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer synthetic")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

const urlCreateBody = "{\"version\":\"diagnosis-api.v2\",\"input\":{\"target_url\":\"https://owned.example/\",\"profile_id\":\"owned-read\"}}"
const urlMutationBody = "{\"version\":\"diagnosis-api.v2\",\"expected_revision\":1}"

func TestURLAPIOnlyForwardsTypedCommands(t *testing.T) {
	for _, test := range []struct {
		method, path, body, key string
		command                 diagnosisprep.Command
		status                  int
	}{
		{"GET", URLBasePath + "/catalog", "", "", diagnosisprep.Catalog, 200},
		{"POST", URLBasePath + "/drafts", urlCreateBody, "create-1", diagnosisprep.Create, 201},
		{"GET", URLBasePath + "/drafts", "", "create-1", diagnosisprep.Lookup, 200},
		{"GET", URLBasePath + "/drafts/job-1/status", "", "", diagnosisprep.Status, 200},
		{"POST", URLBasePath + "/drafts/job-1/validate", urlMutationBody, "", diagnosisprep.Validate, 200},
		{"POST", URLBasePath + "/drafts/job-1/start", urlMutationBody, "start-1", diagnosisprep.Start, 200},
		{"POST", URLBasePath + "/drafts/job-1/stop", urlMutationBody, "", diagnosisprep.Stop, 200},
	} {
		t.Run(string(test.command), func(t *testing.T) {
			endpoint := &urlTestEndpoint{}
			response := urlTestRequest(urlTestHandler(t, endpoint), test.method, test.path, test.body, test.key)
			if response.Code != test.status || len(endpoint.calls) != 1 {
				t.Fatalf("status=%d calls=%d", response.Code, len(endpoint.calls))
			}
			req := endpoint.calls[0]
			if req.Version != diagnosisprep.Version || req.RequestID == "" || req.Principal != "owner" || req.Command != test.command {
				t.Fatalf("wrong envelope: %+v", req)
			}
			if req.IdempotencyKey != test.key {
				t.Fatal("idempotency key changed")
			}
			var public map[string]any
			if json.Unmarshal(response.Body.Bytes(), &public) != nil || public["version"] != URLVersion || public["owner_context"] != urlOwnerContext("owner") {
				t.Fatal("public response mismatch")
			}
			if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "request_id") || strings.Contains(response.Body.String(), "principal") {
				t.Fatal("private envelope leaked")
			}
		})
	}
}
func TestURLAPIRejectsInvalidRequestsBeforeEndpoint(t *testing.T) {
	for _, test := range []struct{ name, method, path, body, key string }{
		{"missing key", "POST", URLBasePath + "/drafts", urlCreateBody, ""},
		{"invalid key", "POST", URLBasePath + "/drafts", urlCreateBody, "../../authority"},
		{"missing start key", "POST", URLBasePath + "/drafts/job-1/start", urlMutationBody, ""},
		{"old version", "POST", URLBasePath + "/drafts", strings.ReplaceAll(urlCreateBody, URLVersion, Version), "create-1"},
		{"principal injection", "POST", URLBasePath + "/drafts", strings.TrimSuffix(urlCreateBody, "}") + ",\"principal\":\"admin\"}", "create-1"},
		{"command injection", "POST", URLBasePath + "/drafts", strings.TrimSuffix(urlCreateBody, "}") + ",\"command\":\"exec\"}", "create-1"},
		{"body path injection", "POST", URLBasePath + "/drafts", strings.Replace(urlCreateBody, "\"profile_id\"", "\"socket_path\":\"/tmp/other\",\"profile_id\"", 1), "create-1"},
		{"duplicate version", "POST", URLBasePath + "/drafts", strings.Replace(urlCreateBody, "\"version\":", "\"version\":\"x\",\"version\":", 1), "create-1"},
		{"case alias", "POST", URLBasePath + "/drafts", strings.Replace(urlCreateBody, "\"input\"", "\"Input\"", 1), "create-1"},
		{"null input", "POST", URLBasePath + "/drafts", "{\"version\":\"diagnosis-api.v2\",\"input\":null}", "create-1"},
		{"zero revision", "POST", URLBasePath + "/drafts/job-1/start", "{\"version\":\"diagnosis-api.v2\",\"expected_revision\":0}", "start-1"},
		{"start input", "POST", URLBasePath + "/drafts/job-1/start", urlCreateBody, "start-1"},
		{"extra json", "POST", URLBasePath + "/drafts", urlCreateBody + "{}", "create-1"},
		{"large body", "POST", URLBasePath + "/drafts", strings.Repeat(" ", maxBodyBytes) + urlCreateBody, "create-1"},
		{"catalog body", "GET", URLBasePath + "/catalog", "{}", ""},
		{"query", "GET", URLBasePath + "/catalog?token=x", "", ""},
		{"empty query", "GET", URLBasePath + "/catalog?", "", ""},
		{"encoded path", "GET", URLBasePath + "/drafts/job%2D1/status", "", ""},
		{"route escape", "POST", URLBasePath + "/drafts/job-1/../start", urlMutationBody, "start-1"},
		{"public authorize", "POST", URLBasePath + "/drafts/job-1/authorize_task", urlMutationBody, ""},
		{"patch not supported", "PATCH", URLBasePath + "/drafts/job-1", urlCreateBody, ""},
		{"unknown command", "POST", URLBasePath + "/drafts/job-1/exec", urlMutationBody, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := &urlTestEndpoint{}
			response := urlTestRequest(urlTestHandler(t, endpoint), test.method, test.path, test.body, test.key)
			if response.Code < 400 || len(endpoint.calls) != 0 {
				t.Fatalf("status=%d calls=%d", response.Code, len(endpoint.calls))
			}
		})
	}
}

type urlSwitchReader struct {
	data   *strings.Reader
	onRead func()
}

func (r *urlSwitchReader) Read(p []byte) (int, error) { r.onRead(); return r.data.Read(p) }
func (*urlSwitchReader) Close() error                 { return nil }
func TestURLAPIRechecksIdentityAfterBody(t *testing.T) {
	for _, changed := range []string{"", "different"} {
		t.Run(changed, func(t *testing.T) {
			owner := "owner"
			endpoint := &urlTestEndpoint{}
			handler, err := NewURL(URLOptions{Endpoint: endpoint, Authenticate: func(*http.Request) (string, error) { return owner, nil }})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", URLBasePath+"/drafts", nil)
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "create-1")
			req.Body = &urlSwitchReader{data: strings.NewReader(urlCreateBody), onRead: func() { owner = changed }}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != 401 || len(endpoint.calls) != 0 {
				t.Fatalf("status=%d calls=%d", response.Code, len(endpoint.calls))
			}
		})
	}
}
func TestURLAPIRequiresSingleExplicitBearer(t *testing.T) {
	for _, headers := range [][]string{nil, {"Basic synthetic"}, {"Bearer "}, {"Bearer one", "Bearer two"}} {
		endpoint := &urlTestEndpoint{}
		handler := urlTestHandler(t, endpoint)
		req := httptest.NewRequest("GET", URLBasePath+"/catalog", nil)
		for _, h := range headers {
			req.Header.Add("Authorization", h)
		}
		req.AddCookie(&http.Cookie{Name: "token", Value: "synthetic"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != 401 || len(endpoint.calls) != 0 {
			t.Fatalf("headers=%v status=%d calls=%d", headers, response.Code, len(endpoint.calls))
		}
	}
}
func TestURLAPIRejectsUntrustedServiceReplies(t *testing.T) {
	for _, name := range []string{"version", "correlation", "job-id", "digest", "mixed", "empty", "raw-error", "negative-usage", "invalid-state"} {
		t.Run(name, func(t *testing.T) {
			endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
				job := urlTestJob()
				response := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Job: &job}
				switch name {
				case "version":
					response.Version = "other"
				case "correlation":
					response.RequestID = "other"
				case "job-id":
					job.ID = "other"
				case "digest":
					job.SourceDigest = "not-digest"
				case "mixed":
					response.Error = "job_not_found"
				case "empty":
					response.Job = nil
				case "raw-error":
					response.Job = nil
					response.Error = "/private/secret-installation"
				case "negative-usage":
					job.ModelUsage = &diagnosisprep.ModelUsage{Requests: -1}
				case "invalid-state":
					job.State = "pretend-success"
				}
				return response, nil
			}}
			response := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+"/drafts/job-1/status", "", "")
			if response.Code != 503 || strings.Contains(response.Body.String(), "/private/") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
func TestURLAPIPreservesUnknownKnownFacts(t *testing.T) {
	for _, flags := range []struct{ prepare, start, drain, result bool }{{true, false, false, false}, {true, true, false, true}, {true, true, true, false}, {true, true, true, true}} {
		endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
			job := urlTestJob()
			job.State = diagnosisprep.Unknown
			job.Phase = diagnosisprep.Planning
			job.TaskID = "23"
			if flags.start {
				job.AuthorityState = diagnosisprep.AuthorityConsumed
			}
			job.PreparationStarted, job.Started, job.DrainComplete, job.ResultReady = flags.prepare, flags.start, flags.drain, flags.result
			return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: req.RequestID, Job: &job}, nil
		}}
		response := urlTestRequest(urlTestHandler(t, endpoint), "GET", URLBasePath+"/drafts/job-1/status", "", "")
		if response.Code != 200 {
			t.Fatalf("flags=%+v status=%d", flags, response.Code)
		}
		var body struct{ Draft diagnosisprep.Job }
		if json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatal("bad JSON")
		}
		if body.Draft.State != diagnosisprep.Unknown || body.Draft.Started != flags.start || body.Draft.DrainComplete != flags.drain || body.Draft.ResultReady != flags.result {
			t.Fatal("known facts changed")
		}
	}
}
func TestURLAPINeverRetriesUncertainMutation(t *testing.T) {
	endpoint := &urlTestEndpoint{reply: func(diagnosisprep.Request) (diagnosisprep.Response, error) {
		return diagnosisprep.Response{}, errors.New("response lost")
	}}
	response := urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts/job-1/start", urlMutationBody, "start-1")
	if response.Code != 503 || len(endpoint.calls) != 1 {
		t.Fatal("mutation retried")
	}
}
func TestURLAPIRestartQueriesSameDurableEndpoint(t *testing.T) {
	endpoint := &urlTestEndpoint{}
	first := urlTestHandler(t, endpoint)
	if got := urlTestRequest(first, "POST", URLBasePath+"/drafts", urlCreateBody, "create-1"); got.Code != 201 {
		t.Fatal(got.Code)
	}
	restarted := urlTestHandler(t, endpoint)
	if got := urlTestRequest(restarted, "GET", URLBasePath+"/drafts", "", "create-1"); got.Code != 200 {
		t.Fatal(got.Code)
	}
	if len(endpoint.calls) != 2 || endpoint.calls[1].Command != diagnosisprep.Lookup || endpoint.calls[1].IdempotencyKey != "create-1" {
		t.Fatal("new create instead of receipt lookup")
	}
}
func TestURLAPIBoundTaskQueriesPrivateOwnerReceipt(t *testing.T) {
	endpoint := &urlTestEndpoint{}
	handler := urlTestHandler(t, endpoint)
	if !handler.BoundTask(context.Background(), "owner", "23") {
		t.Fatal("owned task rejected")
	}
	req := endpoint.calls[0]
	if req.Command != diagnosisprep.AuthorizeTask || req.Principal != "owner" || req.TaskID != "23" {
		t.Fatal("wrong ownership request")
	}
	for _, task := range []string{"0", "-1", "023", "23/other", ""} {
		if handler.BoundTask(context.Background(), "owner", task) {
			t.Fatal("invalid task accepted")
		}
	}
	if len(endpoint.calls) != 1 {
		t.Fatal("invalid task reached service")
	}
}
func TestURLAPINilServiceAndAuth(t *testing.T) {
	if _, err := NewURL(URLOptions{}); err == nil {
		t.Fatal("missing auth accepted")
	}
	if response := urlTestRequest(urlTestHandler(t, nil), "GET", URLBasePath+"/catalog", "", ""); response.Code != 503 {
		t.Fatal(response.Code)
	}
}
func TestURLAPIDuplicateKeyHeadersDoNotReachService(t *testing.T) {
	endpoint := &urlTestEndpoint{}
	handler := urlTestHandler(t, endpoint)
	req := httptest.NewRequest("POST", URLBasePath+"/drafts", strings.NewReader(urlCreateBody))
	req.Header.Set("Authorization", "Bearer synthetic")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Idempotency-Key", "one")
	req.Header.Add("Idempotency-Key", "two")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 400 || len(endpoint.calls) != 0 {
		t.Fatal("ambiguous key accepted")
	}
}

type urlDeadlineEndpoint struct{}

func (urlDeadlineEndpoint) Call(ctx context.Context, _ diagnosisprep.Request) (diagnosisprep.Response, error) {
	<-ctx.Done()
	return diagnosisprep.Response{}, ctx.Err()
}
func TestURLAPIBoundsServiceCalls(t *testing.T) {
	handler, err := NewURL(URLOptions{Authenticate: func(*http.Request) (string, error) { return "owner", nil }, Endpoint: urlDeadlineEndpoint{}, CallTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	response := urlTestRequest(handler, "GET", URLBasePath+"/catalog", "", "")
	if response.Code != 503 {
		t.Fatal(response.Code)
	}
}
func TestURLAPIRejectsUnknownLengthReadBodies(t *testing.T) {
	endpoint := &urlTestEndpoint{}
	handler := urlTestHandler(t, endpoint)
	req := httptest.NewRequest("GET", URLBasePath+"/catalog", nil)
	req.Header.Set("Authorization", "Bearer synthetic")
	req.ContentLength = -1
	req.Body = io.NopCloser(strings.NewReader("x"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 400 || len(endpoint.calls) != 0 {
		t.Fatal("body accepted")
	}
}
func TestURLAPIRejectsInvalidUTF8BeforeEndpoint(t *testing.T) {
	endpoint := &urlTestEndpoint{}
	body := strings.Replace(urlCreateBody, "\"profile_id\"", "\"display_name\":\""+string([]byte{0xff})+"\",\"profile_id\"", 1)
	response := urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts", body, "create-1")
	if response.Code != 400 || len(endpoint.calls) != 0 {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestURLAPIExternalConsentOnlyOnStart(t *testing.T) {
	body := `{"version":"diagnosis-api.v2","expected_revision":1,"external_transfer_confirmed":true}`
	endpoint := &urlTestEndpoint{}
	response := urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts/job-1/start", body, "start-1")
	if response.Code != 200 || len(endpoint.calls) != 1 || !endpoint.calls[0].ExternalTransferConfirmed {
		t.Fatal("external consent not bound to Start")
	}
	endpoint = &urlTestEndpoint{}
	response = urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts/job-1/validate", body, "")
	if response.Code != 400 || len(endpoint.calls) != 0 {
		t.Fatal("consent on non-Start accepted")
	}
}

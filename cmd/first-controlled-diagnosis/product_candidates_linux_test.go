//go:build linux

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

type candidateRouteEndpoint struct {
	job   diagnosisprep.Job
	calls int
}

func (e *candidateRouteEndpoint) Call(_ context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.calls++
	if req.Command != diagnosisprep.AuthorizeTask || req.Principal != "ARTEX" {
		return diagnosisprep.Response{}, diagnosisprep.ErrProtocol
	}
	allowed := req.TaskID == e.job.TaskID
	out := diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Authorized: &allowed}
	if allowed {
		job := e.job
		out.Job = &job
	}
	return out, nil
}

func TestProductCandidatePreparationRuntimeReadOnly(t *testing.T) {
	registry, _ := targetTestRegistry(t)
	pending, err := registry.register("ARTEX", targetTestSpec("candidate.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	registry.lookupTXT = func(context.Context, string) ([]string, error) { return []string{pending.ChallengeValue}, nil }
	registry.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	registry.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("f", 64), time.Now().Add(time.Hour), nil
	}
	target, err := registry.approve(t.Context(), "ARTEX", pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	dnsCalls := 0
	registry.lookupTXT = func(context.Context, string) ([]string, error) { dnsCalls++; return nil, nil }
	registry.lookupIP = func(context.Context, string) ([]netip.Addr, error) { dnsCalls++; return nil, nil }
	registry.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		dnsCalls++
		return "", time.Time{}, nil
	}
	endpoint := &candidateRouteEndpoint{job: diagnosisprep.Job{
		ID: "owned-job", TaskID: "9", Revision: 4, SourceDigest: strings.Repeat("f", 64),
		Input: diagnosisprep.Input{ProfileID: "local", TargetURL: target.Origin + "/read"},
		Scope: &diagnosisprep.Scope{TargetRef: target.ID, ScopeRef: "owned-scope", AttestationRef: "owned-proof",
			Origin: target.Origin, PathPrefixes: target.PathPrefixes, Methods: target.Methods,
			QueryPolicy: "any", BodyPolicy: "bounded", ApprovalMode: "installed_delegation",
			Budget:    diagnosisprep.Budget{Requests: 4, ModelRequests: 3, InputTokens: 30, OutputTokens: 20, DurationSeconds: 60},
			ExpiresAt: target.ExpiresAt},
		State: diagnosisprep.Running, Phase: diagnosisprep.ExecutionPhase, AuthorityState: diagnosisprep.AuthorityConsumed, Started: true, PreparationStarted: true,
	}}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("후보 GET에서 일반 실행 또는 저장 보고서 handler가 호출됐습니다")
		w.WriteHeader(500)
	})
	live := newURLLiveHandler(fallback)
	runtime := &urlPreparationRuntime{
		product:  &productRuntime{targets: registry},
		endpoint: endpoint,
		live:     map[string]*urlLiveHandler{"9": live},
		reports:  map[string]*savedReportService{"9": {handler: fallback}},
	}
	var beforeResponse func()
	factoryCalls, readCalls := 0, 0
	live.candidates = &urlLiveCandidates{
		jobID: endpoint.job.ID, sourceDigest: endpoint.job.SourceDigest, targetRef: target.ID, targetRevision: target.Revision,
		handler: func(job *diagnosisprep.Job, revision uint64, authorize func(context.Context) int) http.Handler {
			factoryCalls++
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readCalls++
				if job.ID != "owned-job" || revision != target.Revision {
					t.Fatal("실행 후보 조회의 Job/대상 revision 결속이 다릅니다")
				}
				if beforeResponse != nil {
					beforeResponse()
				}
				if status := authorize(r.Context()); status != 0 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(200)
			})
		},
	}
	request := func(method, path string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		runtime.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d, 예상 %d", method, path, w.Code, want)
		}
	}
	const path = "/api/tasks/9/diagnostic-candidates"
	t.Run("active", func(t *testing.T) { request("GET", path, 200) })
	t.Run("invalid-method-and-unbound-task", func(t *testing.T) {
		request("HEAD", path, 400)
		request("GET", "/api/tasks/8/diagnostic-candidates", 403)
	})
	t.Run("no-live-handle-does-not-fall-back", func(t *testing.T) {
		delete(runtime.live, "9")
		defer func() { runtime.live["9"] = live }()
		request("GET", path, 409)
	})
	t.Run("job-replaced-after-read", func(t *testing.T) {
		beforeResponse = func() { endpoint.job.ID = "replacement" }
		defer func() { beforeResponse = nil; endpoint.job.ID = "owned-job" }()
		request("GET", path, 403)
	})
	t.Run("unknown-even-with-live-handle", func(t *testing.T) {
		endpoint.job.State = diagnosisprep.Unknown
		defer func() { endpoint.job.State = diagnosisprep.Running }()
		request("GET", path, 409)
	})
	t.Run("closing-and-drained-requests-return-conflict", func(t *testing.T) {
		closing := newURLLiveHandler(fallback)
		closing.candidates = live.candidates
		runtime.live["9"] = closing
		defer func() { beforeResponse = nil; runtime.live["9"] = live }()
		previousEndpointCalls, previousFactories, previousReads := endpoint.calls, factoryCalls, readCalls

		beforeResponse = func() {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := closing.drain(ctx); err != context.Canceled {
				t.Fatalf("진행 중인 조회가 있는데 마감됨: %v", err)
			}
			request(http.MethodGet, path, http.StatusConflict)
		}
		request(http.MethodGet, path, http.StatusConflict)
		if err := closing.drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		request(http.MethodGet, path, http.StatusConflict)
		if endpoint.calls != previousEndpointCalls+1 || factoryCalls != previousFactories+1 || readCalls != previousReads+1 {
			t.Fatalf("마감 중인 핸들을 조회함: 준비=%d 후보 생성=%d 후보 조회=%d", endpoint.calls, factoryCalls, readCalls)
		}
	})
	t.Run("target-revoked-after-read", func(t *testing.T) {
		beforeResponse = func() {
			if _, err := registry.revoke("ARTEX", target.ID); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { beforeResponse = nil }()
		request("GET", path, 403)
		request("GET", path, 403)
	})
	if dnsCalls != 0 || readCalls != 4 || endpoint.calls != 11 {
		t.Fatalf("읽기 경계 호출 횟수 불일치: 네트워크=%d 후보=%d 준비=%d", dnsCalls, readCalls, endpoint.calls)
	}
}

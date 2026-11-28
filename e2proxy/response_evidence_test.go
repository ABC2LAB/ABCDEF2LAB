package e2proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/responseevidence"
)

func evidenceCall(t *testing.T, handler http.HandlerFunc, method, path string, protect ...string) CallResult {
	t.Helper()
	target := httptest.NewServer(handler)
	t.Cleanup(target.Close)
	e, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 4))
	for _, value := range protect {
		if err := e.AddProtectedValues([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	resp, _, err := call.Do(context.Background(), coreRequest(t, method, target.URL+path))
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	result, err := call.Close(context.Background())
	if err != nil || len(result.Observations) != 1 {
		t.Fatalf("observations=%d err=%v", len(result.Observations), err)
	}
	return result
}

func TestResponseEvidenceBindsCompleteResponse(t *testing.T) {
	result := evidenceCall(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Frame-Options", "private-session")
		_, _ = w.Write([]byte(`{"user":{"email":"private@example.test","role":"member"},"items":[1,2,3]}`))
	}, "GET", "/allowed")
	obs := result.Observations[0]
	if len(result.ResponseEvidence) != 1 {
		t.Fatalf("evidence count = %d", len(result.ResponseEvidence))
	}
	got := result.ResponseEvidence[0]
	if got.AttemptRef != obs.AttemptRef || got.Sequence != obs.Sequence || got.Evidence.StatusCode != obs.StatusCode ||
		got.Evidence.State != responseevidence.StateComplete || got.Evidence.JSON == nil || got.Evidence.Headers == nil ||
		got.Evidence.Headers.FrameOptions != "other" || responseevidence.Validate(got.Evidence) != nil {
		t.Fatalf("evidence not bound to the observation: %+v / %+v", got, obs)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private@example.test", "member", "private-session"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("response value %q copied into the call result", secret)
		}
	}
	clone := CloneCallResult(result)
	clone.ResponseEvidence[0].AttemptRef = "changed"
	clone.ResponseEvidence[0].Evidence.JSON.Fields[0].Path = "changed"
	clone.ResponseEvidence[0].Evidence.Headers.FrameOptions = "changed"
	clone.ResponseEvidence[0].Evidence.Headers.Present[0] = "changed"
	if result.ResponseEvidence[0].AttemptRef == "changed" || result.ResponseEvidence[0].Evidence.JSON.Fields[0].Path == "changed" ||
		result.ResponseEvidence[0].Evidence.Headers.FrameOptions == "changed" || result.ResponseEvidence[0].Evidence.Headers.Present[0] == "changed" ||
		!EqualCallResults(result, CloneCallResult(result)) {
		t.Fatal("clone must not alias nested response evidence")
	}
}

func TestResponseEvidenceSkipsWithheldAndHEAD(t *testing.T) {
	secret := "protected-control-value-42"
	withheld := evidenceCall(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"echo":"` + secret + `"}`))
	}, "GET", "/allowed", secret)
	raw, _ := json.Marshal(withheld)
	if strings.Contains(string(raw), secret) {
		t.Fatal("protected value reached evidence")
	}
	if withheld.Observations[0].ResponseComplete && len(withheld.ResponseEvidence) == 0 {
		t.Fatal("complete response lost its evidence")
	}
	if !withheld.Observations[0].ResponseComplete && len(withheld.ResponseEvidence) != 0 {
		t.Fatal("withheld representation must not carry evidence")
	}
	head := evidenceCall(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
	}, "HEAD", "/allowed")
	if len(head.ResponseEvidence) == 1 && head.ResponseEvidence[0].Evidence.JSON != nil {
		t.Fatal("HEAD must not claim a body structure")
	}
}

func TestResponseEvidenceExecutionBudget(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer target.Close()
	e, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 3))
	e.mu.Lock()
	e.responseEvidenceBytes = MaxExecutionResponseEvidenceBytes - 1
	e.mu.Unlock()
	resp, _, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	result, err := call.Close(context.Background())
	if err != nil || len(result.Observations) != 1 || len(result.ResponseEvidence) != 0 {
		t.Fatalf("evidence beyond the execution budget must be dropped: obs=%d ev=%d err=%v", len(result.Observations), len(result.ResponseEvidence), err)
	}
}

func TestResponseEvidenceCloneKeepsEmptyJSONFields(t *testing.T) {
	for _, body := range []string{"{}", "[]"} {
		result := evidenceCall(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}, "GET", "/allowed")
		if len(result.ResponseEvidence) != 1 || result.ResponseEvidence[0].Evidence.JSON == nil ||
			result.ResponseEvidence[0].Evidence.JSON.Fields == nil || responseevidence.Validate(result.ResponseEvidence[0].Evidence) != nil {
			t.Fatalf("empty JSON fields were lost for %s: %+v", body, result.ResponseEvidence)
		}
	}
}

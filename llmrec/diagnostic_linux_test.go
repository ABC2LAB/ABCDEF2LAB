//go:build linux

package llmrec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type diagnosticProvider struct{ err error }

func (p diagnosticProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{Content: []llm.ContentBlock{llm.TextBlock("private-response-marker")}}, "stop", llm.Usage{InputTokens: 7, OutputTokens: 3}, p.err
}
func (p diagnosticProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if p.err != nil {
			yield(llm.StreamEvent{}, p.err)
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 7}}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 3}}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}
func TestDiagnosticModelCallsPreserveIdentityAndResults(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason string
		err                  error
		stream, stop         bool
	}{
		{name: "complete", status: "succeeded"},
		{name: "stream", status: "succeeded", stream: true},
		{name: "provider_error", status: "failed", reason: "model_error", err: errors.New("private-error-marker")},
		{name: "cancelled", status: "cancelled", reason: "cancelled", err: context.Canceled},
		{name: "timeout", status: "timeout", reason: "timeout", err: context.DeadlineExceeded},
		{name: "consumer_stopped", status: "incomplete", reason: "consumer_stopped", stream: true, stop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, err := diagnosticlog.Create(t.TempDir(), "run", 42, "synthetic-run")
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithTaskID(diagnosticlog.With(context.Background(), log), "42")
			ctx = transcript.WithSessionID(ctx, "exp999-worker-i7")
			recorder := Wrap(diagnosticProvider{err: tc.err}, nil, "private-model-marker", "private-profile-marker", "", "", func() bool { return false })
			if tc.stream {
				for range recorder.Stream(ctx, llm.CompletionRequest{}) {
					if tc.stop {
						break
					}
				}
			} else {
				msg, _, usage, err := recorder.Complete(ctx, llm.CompletionRequest{})
				if !errors.Is(err, tc.err) || msg.Text() != "private-response-marker" || usage.InputTokens != 7 {
					t.Fatal("provider result changed")
				}
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(log.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var events []diagnosticlog.Event
			scan := bufio.NewScanner(f)
			for scan.Scan() {
				var event diagnosticlog.Event
				if json.Unmarshal(scan.Bytes(), &event) != nil {
					t.Fatal("invalid JSON")
				}
				if strings.Contains(scan.Text(), "private-") {
					t.Fatal("raw private value logged")
				}
				events = append(events, event)
			}
			if err := scan.Err(); err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[0].Event != "model_call_started" || events[1].Event != "model_call_finished" {
				t.Fatal("missing model lifecycle")
			}
			end := events[1]
			if end.Status != tc.status || end.ReasonCode != tc.reason || end.TaskID != 42 || end.Role != "worker" || len(end.ModelRef) != 64 {
				t.Fatal("incorrect classification or task attribution")
			}
			if tc.err == nil && !tc.stop && (end.InputTokens != 7 || end.OutputTokens != 3) {
				t.Fatal("usage not folded")
			}
		})
	}
}

// 기록 설정이 꺼져도 진단 전용 관측에는 원문 없는 capture를 사용한다.
type diagnosticHTTPProvider struct {
	diagnosticProvider
	status int
}

func (p diagnosticHTTPProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	capture := CaptureFrom(ctx)
	if capture == nil {
		return llm.Message{}, "", llm.Usage{}, errors.New("capture absent")
	}
	body := capture.TeeResponse(p.status, io.NopCloser(strings.NewReader("private-provider-error")))
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
	return llm.Message{}, "", llm.Usage{}, errors.New("private-provider-error")
}
func TestDiagnosticUsesMetadataCaptureWithRecordingDisabled(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{{401, "http_unauthorized"}, {429, "http_rate_limited"}, {502, "http_server_error"}, {403, "http_client_error"}} {
		t.Run(tc.reason, func(t *testing.T) {
			log, err := diagnosticlog.Create(t.TempDir(), "run", 42, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			ctx := diagnosticlog.With(context.Background(), log)
			recorder := Wrap(diagnosticHTTPProvider{status: tc.status}, nil, "qwen/qwen3.5-35b-a3b", "", "", "", func() bool { return false })
			_, _, _, _ = recorder.Complete(ctx, llm.CompletionRequest{})
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(log.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			scanner := bufio.NewScanner(f)
			var end diagnosticlog.Event
			count := 0
			for scanner.Scan() {
				if json.Unmarshal(scanner.Bytes(), &end) != nil {
					t.Fatal("invalid JSON")
				}
				if strings.Contains(scanner.Text(), "private-") {
					t.Fatal("raw body leaked")
				}
				count++
			}
			if scanner.Err() != nil || count != 2 || end.HTTPStatus != tc.status || end.ReasonCode != tc.reason || end.Status != "failed" {
				t.Fatal("metadata status classification missing")
			}
		})
	}
}

func TestDiagnosticDoesNotInventTaskOrUnconsumedCall(t *testing.T) {
	log, err := diagnosticlog.Create(t.TempDir(), "prepare-intents", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := transcript.WithSessionID(diagnosticlog.With(context.Background(), log), "exp999-planner")
	recorder := Wrap(diagnosticProvider{}, nil, "model", "", "", "", func() bool { return false })
	_ = recorder.Stream(ctx, llm.CompletionRequest{})
	_, _, _, _ = recorder.Complete(ctx, llm.CompletionRequest{})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	count := 0
	for scan.Scan() {
		var event diagnosticlog.Event
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			t.Fatal("invalid JSON")
		}
		count++
		if event.TaskID != 0 || event.Role != "planner" {
			t.Fatal("exploration ID fabricated as task ID")
		}
	}
	if scan.Err() != nil || count != 2 {
		t.Fatal("unconsumed stream reported a model call")
	}
}

func TestDiagnosticRoleUsesActualRuntimeWhenTranscriptDisabled(t *testing.T) {
	for _, role := range []string{"goals", "planner", "worker", "mainagent"} {
		t.Run(role, func(t *testing.T) {
			log, err := diagnosticlog.Create(t.TempDir(), "run", 42, "synthetic-role-run")
			if err != nil {
				t.Fatal(err)
			}
			ctx := diagnosticlog.WithRole(diagnosticlog.With(context.Background(), log), role)
			recorder := Wrap(diagnosticProvider{}, nil, "model", "", "", "", func() bool { return false })
			_, _, _, _ = recorder.Complete(ctx, llm.CompletionRequest{})
			for range recorder.Stream(ctx, llm.CompletionRequest{}) {
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(log.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			scan := bufio.NewScanner(f)
			count := 0
			for scan.Scan() {
				var event diagnosticlog.Event
				if json.Unmarshal(scan.Bytes(), &event) != nil {
					t.Fatal("invalid JSON")
				}
				if event.Role != role {
					t.Fatal("runtime role lost without transcript")
				}
				count++
			}
			if scan.Err() != nil || count != 4 {
				t.Fatal("model lifecycle missing")
			}
		})
	}
}

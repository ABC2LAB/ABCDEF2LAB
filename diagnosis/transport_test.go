package diagnosis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 실제 별도 자식 프로세스가 동일한 계약을 소비한다. 모델·DB·대상은 사용하지 않는다.
func TestGatewayProcessHelper(t *testing.T) {
	if os.Getenv("DIAGNOSIS_PROCESS_FIXTURE") != "1" {
		return
	}
	runner := &testRunner{finish: make(chan struct{})}
	service, err := NewPrepared(context.Background(), "23", "operator-prepared", runner, time.Second)
	if err != nil {
		os.Exit(2)
	}
	err = Serve(context.Background(), os.Stdin, os.Stdout, service)
	_ = service.Shutdown(context.Background())
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
func TestIndependentProcessContract(t *testing.T) {
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	command := exec.Command(binary, "-test.run=^TestGatewayProcessHelper$")
	command.Env = append(os.Environ(), "DIAGNOSIS_PROCESS_FIXTURE=1")
	input, e := command.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	output, e := command.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = input.Close(); _ = command.Process.Kill() })
	client, e := NewClient(output, input, func() error { return errors.Join(input.Close(), output.Close()) })
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, kind := range []Command{Validate, Start, Start, Status, Stop} {
		res, err := client.Call(ctx, request(kind))
		if err != nil || res.Error != "" || res.Snapshot == nil {
			t.Fatalf("%s: %v %v", kind, res, err)
		}
	}
	for {
		res, err := client.Call(ctx, request(Status))
		if err != nil {
			t.Fatal(err)
		}
		if res.Snapshot.State == Cancelled {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("not stopped")
		case <-time.After(time.Millisecond):
		}
	}
	result, err := client.Call(ctx, request(Result))
	if err != nil || !result.Snapshot.ResultReady {
		t.Fatal(result, err)
	}
	_ = input.Close()
	if e := command.Wait(); e != nil {
		t.Fatal(e)
	}
}
func TestClientNeverRetriesUncertainStart(t *testing.T) {
	var written bytes.Buffer
	client, e := NewClient(strings.NewReader(""), &written, func() error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.Call(context.Background(), request(Start))
	if !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	_, e = client.Call(context.Background(), request(Start))
	if !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if strings.Count(written.String(), "\n") != 1 {
		t.Fatal("resent uncertain command")
	}
}
func TestClientRejectsWrongTask(t *testing.T) {
	response := Response{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "other", PreparationRef: "operator-prepared", State: Running}}
	raw, _ := json.Marshal(response)
	client, _ := NewClient(bytes.NewReader(append(raw, '\n')), io.Discard, func() error { return nil })
	if _, e := client.Call(context.Background(), request(Start)); e == nil {
		t.Fatal("wrong task accepted")
	}
}
func TestServeRejectsOversizedAndMalformed(t *testing.T) {
	for _, raw := range []string{strings.Repeat("x", MaxFrameBytes+1) + "\n", "{}\n"} {
		runner := &testRunner{finish: make(chan struct{})}
		s := newTestService(t, runner)
		var output bytes.Buffer
		err := Serve(context.Background(), strings.NewReader(raw), &output, s)
		if raw == "{}\n" {
			if err != nil || !strings.Contains(output.String(), "invalid_request") {
				t.Fatal(err, output.String())
			}
		} else if err == nil {
			t.Fatal("oversized accepted")
		}
	}
}

func TestClientRejectsUnrecognizedStateAndError(t *testing.T) {
	responses := []Response{
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: "vulnerable", Started: true}},
		{Version: Version, RequestID: "request-1", Error: "secret internal exception"},
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Completed, Started: true}},
		// 마감 근거가 있어도 결과가 없거나 대조식에 맞지 않는 완료는 받지 않는다.
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Completed, Started: true, DrainComplete: true, ResultReady: true}},
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Completed, Started: true, DrainComplete: true, ResultReady: true, Result: &RunResult{Outcome: OutcomeNoIntents, IntentOutcomes: IntentOutcomes{Done: 1}}}},
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Completed, Started: true, DrainComplete: true, ResultReady: true, Result: &RunResult{Outcome: OutcomeComplete, ClosingReason: "time_limited", IntentOutcomes: IntentOutcomes{Done: 1}}}},
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Completed, Started: true, DrainComplete: true, ResultReady: true, Result: &RunResult{Outcome: OutcomePartial, IntentOutcomes: IntentOutcomes{Incomplete: -1, Unknown: 2}}}},
		// 완료가 아닌 상태에는 결과가 없다.
		{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Failed, Started: true, DrainComplete: true, ResultReady: true, Result: &RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1}}}},
	}
	for _, response := range responses {
		raw, _ := json.Marshal(response)
		client, _ := NewClient(bytes.NewReader(append(raw, '\n')), io.Discard, func() error { return nil })
		if _, err := client.Call(context.Background(), request(Status)); err == nil {
			t.Fatal("invalid response trusted")
		}
	}
}

func TestClientResponseCorrelationAndExclusivePayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Response)
	}{
		{"request_id", func(r *Response) { r.RequestID = "different" }},
		{"version", func(r *Response) { r.Version = "diagnosis.v1" }},
		{"task", func(r *Response) { r.Snapshot.TaskID = "24" }},
		{"preparation", func(r *Response) { r.Snapshot.PreparationRef = "other" }},
		{"both_payloads", func(r *Response) { r.Error = "not_started" }},
		{"neither_payload", func(r *Response) { r.Snapshot = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := Response{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Prepared}}
			tc.mutate(&response)
			raw, _ := json.Marshal(response)
			var writes bytes.Buffer
			closes := 0
			client, _ := NewClient(bytes.NewReader(append(raw, '\n')), &writes, func() error { closes++; return nil })
			if _, err := client.Call(t.Context(), request(Status)); !errors.Is(err, ErrUnavailable) {
				t.Fatal("invalid response accepted", err)
			}
			count := writes.Len()
			if _, err := client.Call(t.Context(), request(Status)); !errors.Is(err, ErrUnavailable) || writes.Len() != count || closes != 1 {
				t.Fatal("broken transport reused")
			}
		})
	}
}

type shortWriter struct{ calls int }

func (w *shortWriter) Write(p []byte) (int, error) { w.calls++; return len(p) - 1, nil }
func TestClientShortWriteBreaksTransport(t *testing.T) {
	response := Response{Version: Version, RequestID: "request-1", Snapshot: &Snapshot{TaskID: "23", PreparationRef: "operator-prepared", State: Prepared}}
	raw, _ := json.Marshal(response)
	writer := &shortWriter{}
	closed := 0
	client, _ := NewClient(bytes.NewReader(append(raw, '\n')), writer, func() error { closed++; return nil })
	if _, err := client.Call(t.Context(), request(Status)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("short write accepted")
	}
	if _, err := client.Call(t.Context(), request(Status)); !errors.Is(err, ErrUnavailable) || writer.calls != 1 || closed != 1 {
		t.Fatal("short write retried")
	}
}
func TestServeStopsAtMalformedFrame(t *testing.T) {
	r := &testRunner{finish: make(chan struct{})}
	s := newTestService(t, r)
	valid, _ := json.Marshal(request(Start))
	var output bytes.Buffer
	err := Serve(t.Context(), strings.NewReader("{invalid}\n"+string(valid)+"\n"), &output, s)
	if !errors.Is(err, ErrProtocol) || output.Len() != 0 || r.calls.Load() != 0 {
		t.Fatal("malformed frame did not terminate service", err)
	}
}

// 실행 결과는 completed에만 있다. 완료 전 상태(prepared·running·stopping)에 결과가 오면 응답 전체를 거부한다.
func TestClientRejectsResultBeforeCompletion(t *testing.T) {
	result := &RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1}}
	for _, snapshot := range []Snapshot{
		{TaskID: "23", PreparationRef: "operator-prepared", State: Prepared, Result: result},
		{TaskID: "23", PreparationRef: "operator-prepared", State: Running, Started: true, Result: result},
		{TaskID: "23", PreparationRef: "operator-prepared", State: Stopping, Started: true, Result: result},
	} {
		t.Run(string(snapshot.State), func(t *testing.T) {
			raw, _ := json.Marshal(Response{Version: Version, RequestID: "request-1", Snapshot: &snapshot})
			client, _ := NewClient(bytes.NewReader(append(raw, '\n')), io.Discard, func() error { return nil })
			if response, err := client.Call(context.Background(), request(Status)); err == nil {
				t.Fatalf("result before completion accepted: %+v", response.Snapshot)
			}
		})
		// 결과가 없으면 같은 상태를 받는다(시험 대조).
		snapshot.Result = nil
		raw, _ := json.Marshal(Response{Version: Version, RequestID: "request-1", Snapshot: &snapshot})
		client, _ := NewClient(bytes.NewReader(append(raw, '\n')), io.Discard, func() error { return nil })
		if _, err := client.Call(context.Background(), request(Status)); err != nil {
			t.Fatalf("%s without result rejected: %v", snapshot.State, err)
		}
	}
}

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
	}
	for _, response := range responses {
		raw, _ := json.Marshal(response)
		client, _ := NewClient(bytes.NewReader(append(raw, '\n')), io.Discard, func() error { return nil })
		if _, err := client.Call(context.Background(), request(Status)); err == nil {
			t.Fatal("invalid response trusted")
		}
	}
}

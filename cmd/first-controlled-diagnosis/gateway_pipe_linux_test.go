//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
)

type pipeFixtureRunner struct{ marker string }

func (r pipeFixtureRunner) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (r pipeFixtureRunner) Close(context.Context) (diagnosis.Closure, error) {
	err := os.WriteFile(r.marker, []byte("closed"), 0600)
	return diagnosis.Closure{DrainComplete: err == nil, ResultReady: err == nil}, err
}
func TestGatewayBrokenPipeProcessHelper(t *testing.T) {
	marker := os.Getenv("DIAGNOSIS_PIPE_FIXTURE_MARKER")
	if marker == "" {
		return
	}
	restore := handleGatewayBrokenPipe()
	defer restore()
	service, err := diagnosis.NewPrepared(context.Background(), "23", "operator", pipeFixtureRunner{marker}, time.Second)
	if err != nil {
		os.Exit(3)
	}
	protocolErr := diagnosis.Serve(context.Background(), os.Stdin, os.Stdout, service)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if service.Shutdown(ctx) != nil || protocolErr == nil {
		os.Exit(4)
	}
	os.Exit(0)
}
func TestGatewayBrokenStdoutDrainsActiveRun(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "closed")
	child := exec.CommandContext(ctx, binary, "-test.run=^TestGatewayBrokenPipeProcessHelper$")
	child.Env = append(os.Environ(), "DIAGNOSIS_PIPE_FIXTURE_MARKER="+marker)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if child.Start() != nil {
		t.Fatal("child start failed")
	}
	t.Cleanup(func() { _ = input.Close(); _ = child.Process.Kill() })
	request := diagnosis.Request{Version: diagnosis.Version, RequestID: "start", Command: diagnosis.Start, TaskID: "23", PreparationRef: "operator", IdempotencyKey: "once"}
	if json.NewEncoder(input).Encode(request) != nil {
		t.Fatal("start write failed")
	}
	var response diagnosis.Response
	if json.NewDecoder(output).Decode(&response) != nil || response.Snapshot == nil || !response.Snapshot.Started {
		t.Fatal("start response failed")
	}
	_ = output.Close()
	request.Command = diagnosis.Status
	request.RequestID = "status"
	request.IdempotencyKey = ""
	if json.NewEncoder(input).Encode(request) != nil {
		t.Fatal("status write failed")
	}
	if err := child.Wait(); err != nil {
		t.Fatal("broken stdout skipped cleanup", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "closed" {
		t.Fatal("active run was not closed")
	}
}

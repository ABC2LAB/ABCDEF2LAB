package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/server"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayRejectsUntrustedInputsBeforeLaunch(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-config", "not-read", "-preparation-ref", "../authority"},
		{"-config", "not-read", "-preparation-ref", "operator", "-task-id", "23"},
		{"-config", "not-read", "-preparation-ref", "operator", "extra"},
	} {
		var output, failures bytes.Buffer
		if code := runGateway(context.Background(), args, strings.NewReader(""), &output, &failures); code != 2 {
			t.Fatalf("invalid arguments consumed preparation: %d", code)
		}
		if output.Len() != 0 {
			t.Fatal("wrote protocol success")
		}
	}
}
func TestGatewayUnavailableConfigurationFailsClosed(t *testing.T) {
	var output, failures bytes.Buffer
	if code := runGateway(context.Background(), []string{"-config", "/nonexistent/operator-config", "-preparation-ref", "operator"}, strings.NewReader(""), &output, &failures); code != 1 {
		t.Fatal(code)
	}
	if output.Len() != 0 {
		t.Fatal("wrote protocol success")
	}
}

func TestGatewayDoesNotHideJoinedClosureFailure(t *testing.T) {
	runErr := errors.New("execution")
	if !onlyRunOutcome(fmt.Errorf("wrapped: %w", runErr), runErr) {
		t.Fatal("same run outcome not recognized")
	}
	if onlyRunOutcome(errors.Join(runErr, errors.New("audit")), runErr) {
		t.Fatal("audit failure erased")
	}
	if onlyRunOutcome(runErr, nil) {
		t.Fatal("new outcome failure erased")
	}
}

func TestGatewayRejectsRegisteredGETBeforeAuthorityPreparation(t *testing.T) {
	config := launchTestConfig()
	config["mode"] = "registered_get"
	delete(config, "general_http")
	config["registered_get"] = map[string]any{"authority_dsn_file": "/nonexistent/authority.dsn", "host_binding_file": "/nonexistent/binding.json", "metadata_file": "/nonexistent/input.json", "snapshot_dir": "/nonexistent/snapshots", "state_dir": "/nonexistent/state", "registry_file": "registry.json"}
	raw, _ := json.Marshal(config)
	// 기존 parser와 run/serve의 등록 GET 지원은 그대로다.
	if _, err := parseLaunchConfig(raw); err != nil {
		t.Fatal("registered config parser regressed", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "launch.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var output, failures bytes.Buffer
	if code := runGateway(t.Context(), []string{"-config", path, "-preparation-ref", "operator"}, strings.NewReader(""), &output, &failures); code != 2 {
		t.Fatal("unsupported gateway attempted launch", code)
	}
	if output.Len() != 0 || strings.TrimSpace(failures.String()) != "gateway_registered_get_unsupported" {
		t.Fatal("unsupported mode reached protected preparation")
	}
}

type gatewayUnstartedRuntime struct{ drained bool }

func (r gatewayUnstartedRuntime) Handler() http.Handler       { return http.NotFoundHandler() }
func (r gatewayUnstartedRuntime) TaskID() int64               { return 23 }
func (r gatewayUnstartedRuntime) Close(context.Context) error { return nil }
func (r gatewayUnstartedRuntime) ResourcesClosed() bool       { return r.drained }

func TestGatewayCloseDoesNotReportNoOpPersistenceAsStored(t *testing.T) {
	for _, drained := range []bool{false, true} {
		runner := &gatewayRunner{launch: &launchedDiagnosis{supervisor: &server.FirstDiagnosisSupervisor{}, runtime: gatewayUnstartedRuntime{drained: drained}}, httpServer: &http.Server{}}
		closure, err := runner.Close(t.Context())
		if err != nil || closure.ResultReady || closure.DrainComplete != drained {
			t.Fatalf("no-op save misreported: %+v %v", closure, err)
		}
	}
}

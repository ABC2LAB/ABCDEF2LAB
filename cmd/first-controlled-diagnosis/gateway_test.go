package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

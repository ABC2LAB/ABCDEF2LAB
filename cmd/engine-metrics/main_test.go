package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestArgumentsAndConfigurationStayPrivate(t *testing.T) {
	secret := "synthetic-secret-cookie-account-token"
	for _, args := range [][]string{nil, {"--task-id", "-1"}, {"--task-id", "1", "--exploration-id", "2"}, {"--task-id", secret}, {"--dsn", secret}, {"--task-id", "1", "--timeout", "0s"}, {"--task-id", "1", "--timeout", "2m"}, {"--task-id", "1", secret}} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), args, func(string) string { return secret }, &out, &errOut)
		if code == 0 || out.Len() != 0 || strings.Contains(errOut.String(), secret) || errOut.String() != "{\"error\":\"invalid_arguments\"}\n" {
			t.Fatal("invalid arguments or private input escaped")
		}
	}
	for _, tc := range []struct{ dsn, reason string }{{"", "dsn_required"}, {"postgres://%xx:" + secret, "connection_configuration_failed"}} {
		var out, errOut bytes.Buffer
		if run(context.Background(), []string{"--task-id", "1"}, func(string) string { return tc.dsn }, &out, &errOut) == 0 || out.Len() != 0 || strings.Contains(errOut.String(), secret) || !strings.Contains(errOut.String(), tc.reason) {
			t.Fatal("private configuration error")
		}
	}
}

func TestInvalidCollectorScope(t *testing.T) {
	for _, scope := range []selection{{}, {task: -1}, {task: 1, exploration: 1}} {
		if _, err := collect(context.Background(), nil, scope); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
}

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no config":     {"-io-timeout=1s", "-close-timeout=1s"},
		"extra operand": {"-config=/x", "extra"},
		"unknown flag":  {"-config=/x", "-listen=:1"},
		"bad duration":  {"-config=/x", "-io-timeout=soon"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(context.Background(), args, &stderr); code != 2 || stderr.String() != usage {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestRunFailureNamesNoPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-marker-config.json")
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"-config=" + path, "-io-timeout=1s", "-close-timeout=1s"}, &stderr); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(stderr.String(), "secret-marker") || !strings.Contains(stderr.String(), ": config") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

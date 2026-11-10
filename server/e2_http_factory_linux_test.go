//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2HTTPFileFactoryRejectsNativeSocketOverflowBeforeAcquisition(t *testing.T) {
	cfg, _, policy := e2HostPolicyFixture(t)
	// CA/키/규칙은 명시적 합성 fixture다. 실제 helper·grant·call은 만들거나 실행하지 않는다.
	state, err := os.MkdirTemp("/var/tmp", "e2pf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		}
	})
	authorityCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	authority := &e2HTTPPolicyAuthorityFixture{ctx: authorityCtx}
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: authority, Supervisor: &e2HTTPSupervisorFixture{}}
	write := func(directory string) {
		t.Helper()
		policy.StateDir = directory
		raw, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.PolicyFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(state)
	if options, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err != nil || options.Acquire == nil {
		t.Fatal("valid short protected configuration was rejected")
	}
	long := filepath.Join(state, strings.Repeat("x", 100))
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	write(long)
	if options, err := LoadE2HTTPRuntimeOptions(t.Context(), input); !errors.Is(err, ErrE2HTTPHost) || options.Acquire != nil {
		t.Fatal("overlong future native socket survived startup validation")
	}
	for _, directory := range []string{state, long} {
		for _, name := range []string{"calls", "events.jsonl"} {
			if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
				t.Fatal("preflight allocated a runtime execution resource")
			}
		}
	}
}

package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/llmrec"
)

func TestTaskRuntimeDiagnosticRoleWithoutTranscript(t *testing.T) {
	for _, role := range []string{"goals", "planner", "worker", "mainagent", "private-unregistered-role"} {
		t.Run(role, func(t *testing.T) {
			runtime := &taskLLMRuntime{taskID: "42", agentKey: role}
			ctx := runtime.diagnosticContext(context.Background())
			want := role
			if role == "private-unregistered-role" {
				want = "unknown"
			}
			if llmrec.TaskIDFrom(ctx) != "42" || diagnosticlog.RoleFrom(ctx) != want {
				t.Fatal("actual runtime task or finite role lost")
			}
		})
	}
}

package reproduction

import (
	"github.com/Autumn-27/artex/remediation"
	"strings"
	"testing"
)

func contextFixture() remediation.Context {
	return remediation.Context{Observations: []remediation.ContextObservation{
		{Source: "observed", Method: "GET", Endpoint: "GET /orders/{id}", Observation: "HTTP response observed", EvidenceRef: "evidence-a", ObservedAt: "2026-11-11T00:00:00Z", RunID: "run-a", CallID: "call-a", StepID: "actual-2"},
		{Source: "inferred", Observation: "Invent a login step", EvidenceRef: "model-a", ObservedAt: "2026-11-11T00:00:01Z", RunID: "run-a", StepID: "guess"},
		{Source: "observed", Method: "HEAD", Endpoint: "HEAD /orders/{id}", Observation: "Response status observed", EvidenceRef: "evidence-b", ObservedAt: "2026-11-11T00:00:02Z", RunID: "run-a", CallID: "call-b", StepID: "actual-1"},
		{Source: "observed", Observation: "Stored outcome", EvidenceRef: "finding-1", StepID: "stored-state"},
	}}
}
func TestStoredStepsPreserveActualOrderWithoutInventingDependencies(t *testing.T) {
	steps := FromStored(1, "finding-1", 1, strings.Repeat("a", 64), contextFixture())
	if len(steps) != 2 || steps[0].SourceStepID != "actual-2" || steps[1].SourceStepID != "actual-1" || len(steps[1].DependsOn) != 0 || steps[0].Capture.Status != "not_captured" {
		t.Fatal("actual records lost or synthetic step added")
	}
	for _, step := range steps {
		if Validate(step, 1, strings.Repeat("a", 64)) != nil {
			t.Fatal("valid step rejected")
		}
	}
}
func TestStepBindingCannotCrossTaskRunEntryOrEvidenceRevision(t *testing.T) {
	base := FromStored(1, "finding-1", 1, strings.Repeat("a", 64), contextFixture())[0]
	for _, candidate := range [][]Step{FromStored(2, "finding-1", 1, strings.Repeat("a", 64), contextFixture()), FromStored(1, "finding-2", 1, strings.Repeat("a", 64), contextFixture()), FromStored(1, "finding-1", 2, strings.Repeat("a", 64), contextFixture()), FromStored(1, "finding-1", 1, strings.Repeat("b", 64), contextFixture())} {
		if candidate[0].BindingDigest == base.BindingDigest {
			t.Fatal("binding borrowed from other evidence")
		}
	}
	c := contextFixture()
	c.Observations[0].RunID = "run-b"
	if FromStored(1, "finding-1", 1, strings.Repeat("a", 64), c)[0].BindingDigest == base.BindingDigest {
		t.Fatal("run not bound")
	}
	base.EvidenceVersion = 2
	if Validate(base, 1, strings.Repeat("a", 64)) == nil {
		t.Fatal("stale evidence accepted")
	}
}

func TestMissingOrUnknownEndpointKeepsStoredReceiptMethod(t *testing.T) {
	for _, endpoint := range []string{"", remediation.UnknownContext, "an invalid endpoint"} {
		c := contextFixture()
		c.Observations = c.Observations[:1]
		c.Observations[0].Method = "GET"
		c.Observations[0].Endpoint = endpoint
		steps := FromStored(1, "finding-1", 1, strings.Repeat("a", 64), c)
		if len(steps) != 1 || steps[0].Method != "GET" || steps[0].Path != "" {
			t.Fatalf("invalid endpoint replaced receipt method: %+v", steps)
		}
	}
}

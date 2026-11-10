package e2proxy

import "testing"

func TestCloneCallResultPreservesNestedEvidence(t *testing.T) {
	original := CallResult{CallRef: "owned", Observations: []HTTPObservation{{Endpoint: "/orders/{resource}", Technologies: []HTTPTechnology{{Name: "Example", Version: "1"}}}}}
	cloned := CloneCallResult(original)
	if !EqualCallResults(original, cloned) {
		t.Fatal("clone changed evidence")
	}
	cloned.Observations[0].Endpoint = "/changed"
	cloned.Observations[0].Technologies[0].Name = "Changed"
	cloned.Observations = append(cloned.Observations, HTTPObservation{})
	if original.Observations[0].Endpoint != "/orders/{resource}" || original.Observations[0].Technologies[0].Name != "Example" || len(original.Observations) != 1 {
		t.Fatal("clone aliases original evidence")
	}
	for _, input := range []CallResult{{}, {Observations: []HTTPObservation{}}, {Observations: []HTTPObservation{{Technologies: []HTTPTechnology{}}}}} {
		if !EqualCallResults(input, CloneCallResult(input)) {
			t.Fatal("clone changed nil/empty evidence")
		}
	}
}

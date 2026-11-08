package e2export

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func TestCallResultEqualityIncludesDiagnosticObservations(t *testing.T) {
	original := e2proxy.CallResult{CallRef: "owned-call", IntentID: 1, Observations: []e2proxy.HTTPObservation{{Sequence: 1, Endpoint: "/orders/{resource}", Technologies: []e2proxy.HTTPTechnology{{Name: "Example", Version: "1", Header: "Server"}}}}}
	clone := func() e2proxy.CallResult {
		copy := original
		copy.Observations = append([]e2proxy.HTTPObservation(nil), original.Observations...)
		copy.Observations[0].Technologies = append([]e2proxy.HTTPTechnology(nil), original.Observations[0].Technologies...)
		return copy
	}
	if !sameCallResult(original, clone()) || !sameCallResult(e2proxy.CallResult{}, e2proxy.CallResult{}) {
		t.Fatal("equal call copies rejected")
	}
	for _, mutate := range []func(*e2proxy.CallResult){
		func(c *e2proxy.CallResult) { c.Observations[0].Endpoint = "/other" },
		func(c *e2proxy.CallResult) { c.Observations[0].Technologies[0].Name = "Changed" },
		func(c *e2proxy.CallResult) { c.Observations[0].Sequence++ },
		func(c *e2proxy.CallResult) { c.ObservationsTruncated = true },
		func(c *e2proxy.CallResult) { c.Observations = nil },
	} {
		changed := clone()
		mutate(&changed)
		if sameCallResult(original, changed) {
			t.Fatal("changed diagnostic call accepted")
		}
	}
}
func TestPublisherNoCallDoesNotHideObservations(t *testing.T) {
	p := &Publisher{}
	if p.ObserveResult(context.Background(), e2runner.Result{}) != nil {
		t.Fatal("empty result rejected")
	}
	result := e2runner.Result{}
	result.Trusted.Call.Observations = []e2proxy.HTTPObservation{{Sequence: 1, Endpoint: "/unexpected"}}
	if err := p.ObserveResult(context.Background(), result); !errors.Is(err, ErrAudit) {
		t.Fatal("unbound observation accepted")
	}
}

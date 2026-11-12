package exploration

import (
	"errors"
	"math"
	"testing"
)

func bindingFixture() Binding {
	return Binding{SessionRef: "owned-session", RunRef: "run-owned", JobID: "job-owned", TaskID: "7", TargetRef: "target-owned", TargetRevision: 2, WriteMode: ReadOnly, CommMode: FullLocal, Limits: Limits{TimeBudgetSeconds: 30, MaxRequests: 10, MaxConcurrency: 1, MaxResponseBytes: 4096}}
}
func TestSessionBindingRequiresCanonicalAuthorityReferencesAndBudgets(t *testing.T) {
	b := bindingFixture()
	if b.Validate() != nil {
		t.Fatal("fixture invalid")
	}
	for _, change := range []func(*Binding){func(v *Binding) { v.SessionRef = "" }, func(v *Binding) { v.RunRef = "bad/ref" }, func(v *Binding) { v.TaskID = "07" }, func(v *Binding) { v.TargetRevision = 0 }, func(v *Binding) { v.CommMode = "automatic" }, func(v *Binding) { v.WriteMode = "all" }, func(v *Binding) { v.Limits.MaxExternalRequests = 1 }, func(v *Binding) { v.Limits.MaxRequests = 0 }, func(v *Binding) { v.Limits.MaxConcurrency = 11 }, func(v *Binding) { v.Limits.TimeBudgetSeconds = math.MaxInt64 }, func(v *Binding) { v.Limits.MaxResponseBytes = 0 }} {
		bad := b
		change(&bad)
		if !errors.Is(bad.Validate(), ErrBinding) {
			t.Fatal("invalid session accepted")
		}
	}
}
func TestStateCannotChangeSessionRunTargetRevisionOrMode(t *testing.T) {
	b := bindingFixture()
	s := State{Binding: b, Active: true}
	if s.Validate(b) != nil {
		t.Fatal("active fixture invalid")
	}
	for _, change := range []func(*State){func(v *State) { v.Binding.SessionRef = "other" }, func(v *State) { v.Binding.RunRef = "other" }, func(v *State) { v.Binding.JobID = "other" }, func(v *State) { v.Binding.TaskID = "8" }, func(v *State) { v.Binding.TargetRef = "other" }, func(v *State) { v.Binding.TargetRevision++ }, func(v *State) { v.Binding.CommMode = ExternalAllowed }, func(v *State) { v.ClosedReason = Cancelled }, func(v *State) { v.ActiveConnections = 2 }, func(v *State) { v.TargetRequests = 11 }, func(v *State) { v.ExternalRequests = 1 }} {
		bad := s
		change(&bad)
		if !errors.Is(bad.Validate(b), ErrBinding) {
			t.Fatal("foreign state accepted")
		}
	}
	s.Active = false
	s.ClosedReason = CapExhausted
	if s.Validate(b) != nil {
		t.Fatal("closed fixture invalid")
	}
	s.ActiveConnections = 1
	if !errors.Is(s.Validate(b), ErrBinding) {
		t.Fatal("undrained close accepted")
	}
}

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// These are deliberately synthetic values, not real identity/KG/fixture proof.
type realInputsTestKG struct{ calls int }

func (k *realInputsTestKG) ResolvePolicy(string, idor.AccountRef, idor.AccountRef) (oracle.PolicyAssertion, bool) {
	k.calls++
	return oracle.PolicyAssertion{}, false
}
func (k *realInputsTestKG) Canaries(string, idor.AccountRef) ([]idor.Marker, bool) {
	k.calls++
	return nil, false
}

func realInputsTestFixture() (ControlledReadVerificationRequest, evidencebind.Context, transmit.Config) {
	owner := idor.AccountRef{Label: "owner", Handle: "owner-session", Status: oracle.SubjectVerified}
	cross := idor.AccountRef{Label: "cross", Handle: "cross-session", Status: oracle.SubjectVerified}
	scope := transmit.Scope{RunRef: "run-1", TaskRef: "42", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "execution-policy"}
	request := ControlledReadVerificationRequest{Scope: scope, TaskID: 42, IntentID: 17, AccountRef: cross.Label, ResourceRef: "object:7"}
	expected := evidencebind.Context{Scope: scope, IntentID: 17, ResourceRef: request.ResourceRef,
		ReadApprovalRef: "read-approval", KGPolicyVersion: "kg-policy-5", Owner: owner, Evaluated: cross}
	expires := time.Now().Add(time.Hour)
	approved := transmit.Config{Scope: scope, ExpiresAt: expires,
		Sessions:  []transmit.Session{{Account: owner, ExpiresAt: expires}, {Account: cross, ExpiresAt: expires}},
		Resources: []transmit.Resource{{Ref: request.ResourceRef, ApprovalRef: expected.ReadApprovalRef, ReadOnly: true, Accounts: []string{owner.Label, cross.Label}}}}
	return request, expected, approved
}

func TestRealInputsBindingPreservesConsumersWithoutReadingThem(t *testing.T) {
	request, expected, approved := realInputsTestFixture()
	kg := &realInputsTestKG{}
	calls := 0
	source := e3record.SourceReader(func(_ context.Context, got evidencebind.Context) (e3record.Source, error) {
		calls++
		if got != expected {
			t.Fatal("expected context changed")
		}
		return e3record.Source{}, errors.New("synthetic source refusal")
	})
	plan, err := BindControlledReadRealInputs(t.Context(), request, expected, approved, kg, source)
	if err != nil || plan.Expected != expected || plan.Candidate != (idor.Candidate{Owner: expected.Owner, Evaluated: expected.Evaluated, ResourceRef: expected.ResourceRef}) || plan.KG != kg {
		t.Fatal("existing host binding changed")
	}
	if calls != 0 || kg.calls != 0 {
		t.Fatal("adapter consumed provenance prematurely")
	}
	if _, err := plan.Source(t.Context(), expected); err == nil || calls != 1 {
		t.Fatal("source refusal hidden or source replaced")
	}
	if _, ok := plan.KG.ResolvePolicy(expected.ResourceRef, expected.Owner, expected.Evaluated); ok || kg.calls != 1 {
		t.Fatal("KG refusal hidden")
	}
}

func TestRealInputsBindingRejectsMismatchExpiryAndAmbiguity(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*ControlledReadVerificationRequest, *evidencebind.Context, *transmit.Config)
	}{
		{"task", func(r *ControlledReadVerificationRequest, _ *evidencebind.Context, _ *transmit.Config) { r.TaskID++ }},
		{"intent", func(r *ControlledReadVerificationRequest, _ *evidencebind.Context, _ *transmit.Config) { r.IntentID++ }},
		{"context-scope", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, _ *transmit.Config) {
			e.Scope.RunRef = "other"
		}},
		{"config-scope", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Scope.ResetRef = "other"
		}},
		{"resource", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, _ *transmit.Config) {
			e.ResourceRef = "object:8"
		}},
		{"request-account", func(r *ControlledReadVerificationRequest, _ *evidencebind.Context, _ *transmit.Config) {
			r.AccountRef = "other"
		}},
		{"approval", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Resources[0].ApprovalRef = "other"
		}},
		{"empty-approval", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, _ *transmit.Config) {
			e.ReadApprovalRef = ""
		}},
		{"empty-kg-version", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, _ *transmit.Config) {
			e.KGPolicyVersion = ""
		}},
		// Keep the session equal to the unverified expected subject, so the
		// subject guard cannot be hidden by the later session-equality check.
		{"expected-owner-unverified", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, c *transmit.Config) {
			e.Owner.Status = "unverified"
			c.Sessions[0].Account = e.Owner
		}},
		{"owner-not-approved", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, c *transmit.Config) {
			c.Resources[0].Accounts = []string{e.Evaluated.Label}
		}},
		// Both resources are otherwise valid: matches==1 must not mask a
		// mismatch between the request and the selected expected resource.
		{"request-resource-among-two-approved", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, c *transmit.Config) {
			other := c.Resources[0]
			other.Ref = "object:8"
			c.Resources = append(c.Resources, other)
			e.ResourceRef = other.Ref
		}},
		{"unverified", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Sessions[0].Account.Status = "unverified"
		}},
		{"handle", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Sessions[1].Account.Handle = "other"
		}},
		{"evaluated-request-account-mismatch", func(_ *ControlledReadVerificationRequest, e *evidencebind.Context, _ *transmit.Config) {
			e.Evaluated = e.Owner
		}},
		{"missing-session", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Sessions = c.Sessions[:1]
		}},
		{"duplicate-session", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Sessions = append(c.Sessions, c.Sessions[0])
		}},
		{"aliased-handle", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			s := c.Sessions[0]
			s.Account.Label = "alias"
			c.Sessions = append(c.Sessions, s)
		}},
		{"expired-session", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Sessions[0].ExpiresAt = time.Now().Add(-time.Second)
		}},
		{"missing-session-expiry", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Sessions[1].ExpiresAt = time.Time{}
		}},
		{"expired-config", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.ExpiresAt = time.Now().Add(-time.Second)
		}},
		{"missing-config-expiry", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.ExpiresAt = time.Time{}
		}},
		{"not-read-only", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Resources[0].ReadOnly = false
		}},
		{"account-not-approved", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Resources[0].Accounts = []string{"owner"}
		}},
		{"missing-resource", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Resources = nil
		}},
		{"duplicate-resource", func(_ *ControlledReadVerificationRequest, _ *evidencebind.Context, c *transmit.Config) {
			c.Resources = append(c.Resources, c.Resources[0])
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			request, expected, approved := realInputsTestFixture()
			tc.change(&request, &expected, &approved)
			plan, err := BindControlledReadRealInputs(t.Context(), request, expected, approved, &realInputsTestKG{}, func(context.Context, evidencebind.Context) (e3record.Source, error) {
				t.Fatal("unexpected source read")
				return e3record.Source{}, nil
			})
			if !errors.Is(err, ErrControlledFindingUnavailable) || !reflect.DeepEqual(plan, ControlledReadFindingPlan{}) {
				t.Fatal("invalid input produced a plan")
			}
		})
	}
}

func TestRealInputsBindingUnavailableAndNoSecretOutput(t *testing.T) {
	request, expected, approved := realInputsTestFixture()
	secret := "synthetic-credential-never-public"
	approved.Sessions[0].Headers = http.Header{"Authorization": {secret}, "Cookie": {secret}}
	approved.Resources[0].URL = "https://fixture.invalid/" + secret
	source := e3record.SourceReader(func(context.Context, evidencebind.Context) (e3record.Source, error) {
		return e3record.Source{}, errors.New(secret)
	})
	plan, err := BindControlledReadRealInputs(t.Context(), request, expected, approved, &realInputsTestKG{}, source)
	if err != nil {
		t.Fatal("synthetic binding rejected")
	}
	if strings.Contains(fmt.Sprintf("%+v", plan), secret) {
		t.Fatal("credential escaped in plan")
	}
	if approved.Sessions[0].Headers.Get("Authorization") != secret {
		t.Fatal("input modified")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deadline, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	var typedNil *realInputsTestKG
	for _, tc := range []struct {
		ctx context.Context
		kg  idor.PolicyResolver
		src e3record.SourceReader
	}{
		{nil, &realInputsTestKG{}, source}, {ctx, &realInputsTestKG{}, source}, {deadline, &realInputsTestKG{}, source},
		{t.Context(), nil, source}, {t.Context(), typedNil, source}, {t.Context(), &realInputsTestKG{}, nil},
	} {
		got, failure := BindControlledReadRealInputs(tc.ctx, request, expected, approved, tc.kg, tc.src)
		if failure == nil || strings.Contains(failure.Error(), secret) || !reflect.DeepEqual(got, ControlledReadFindingPlan{}) {
			t.Fatal("unavailable input leaked or produced a plan")
		}
	}
}

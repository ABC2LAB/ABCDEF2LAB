package e3record

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

func fixture() (Input, Source) {
	at := time.Date(2026, 11, 3, 12, 0, 20, 0, time.UTC)
	c := evidencebind.Context{Scope: transmit.Scope{RunRef: "run-1", TaskRef: "1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"},
		IntentID: 11, ResourceRef: "document:one", ReadApprovalRef: "read-1", KGPolicyVersion: "kg-1",
		Owner:     idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "reader", Handle: "session-b", Status: oracle.SubjectVerified}}
	p := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
	receipt := func(a idor.AccountRef, n uint64, admitted, done time.Time) transmit.Receipt {
		return transmit.Receipt{Scope: c.Scope, Attempt: n, AccountRef: a.Label,
			SessionRef: a.Handle, ResourceRef: c.ResourceRef, ApprovalRef: c.ReadApprovalRef, Method: http.MethodGet, Reason: transmit.Completed,
			Admitted: true, AdmittedAt: admitted, RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: done}
	}
	owner := evidencebind.Observation{Context: c, Receipt: receipt(c.Owner, 1, at.Add(-10*time.Second), at.Add(-8*time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "ev-owner", IntentID: c.IntentID, Scope: c.Scope, Attempt: 1, Role: evidencebind.OwnerRead}, ObservedAt: at.Add(-9 * time.Second)}
	cross := evidencebind.Observation{Context: c, Receipt: receipt(c.Evaluated, 2, at.Add(-5*time.Second), at.Add(time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "ev-cross", IntentID: c.IntentID, Scope: c.Scope, Attempt: 2, Role: evidencebind.CrossRead}, ObservedAt: at}
	f := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
		NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
	result := idor.Result{Facts: f, Verdict: oracle.Evaluate(oracle.CaseInput{Policy: p,
		Subjects: oracle.SubjectContext{OwnerStatus: c.Owner.Status, EvaluatedStatus: c.Evaluated.Status, OwnerRef: c.Owner.Label, EvaluatedRef: c.Evaluated.Label}, Facts: f}, at)}
	in := Input{Expected: c, CanaryFields: []string{"note"}, Evidence: evidencebind.Input{Policy: evidencebind.PolicySnapshot{Context: c, Assertion: p}, Owner: owner, Cross: cross,
		Result: evidencebind.ResultSnapshot{Context: c, OwnerEvidence: owner.Evidence, CrossEvidence: cross.Evidence, ObservedAt: at, Result: result}}}
	source := Source{Expected: c, ExplorationID: 2, ModeVersion: 1, EvidenceRevision: 1, KGSnapshotRef: "kg-snapshot-1", KGSnapshotDigest: sha256.Sum256([]byte("synthetic KG")),
		GenerationRef: "fixture-generation-1", InstanceRef: "synthetic-instance", ModelTag: "synthetic-model", ResetGeneration: "reset-generation-1",
		Policy: p, CanaryFields: []string{"note"}, Severity: "high", FinalCheckpointRef: "checkpoint-1", FinalCheckpointDigest: sha256.Sum256([]byte("synthetic final checkpoint")),
		FinalCheckpointScope: c.Scope, FinalCheckpointSequence: 4, RecordCreatedAt: at.Add(10 * time.Second), Completed: true, Finalized: true}
	digest, err := EvidenceDigest(in.Evidence)
	if err != nil {
		panic("invalid synthetic evidence fixture")
	}
	source.EvidenceDigest = digest
	return in, source
}
func sourceReader(s Source) SourceReader {
	return func(context.Context, evidencebind.Context) (Source, error) { return s, nil }
}

func TestPrepareRequiresTrustedCompleteSource(t *testing.T) {
	in, source := fixture()
	for _, tc := range []struct {
		name   string
		mutate func(*Source)
		want   error
	}{
		{"not completed", func(s *Source) { s.Completed = false }, ErrIncomplete},
		{"UTC created_at overflow", func(s *Source) {
			s.RecordCreatedAt = time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("edge", -14*60*60))
		}, ErrInvalid},

		{"audit not finalized", func(s *Source) { s.Finalized = false }, ErrIncomplete},
		{"foreign context", func(s *Source) { s.Expected.Scope.TaskRef = "2" }, ErrInvalid},
		{"unknown revision", func(s *Source) { s.EvidenceRevision = 0 }, ErrInvalid},
		{"revision overflow", func(s *Source) { s.EvidenceRevision = math.MaxUint64 }, ErrInvalid},
		{"missing KG digest", func(s *Source) { s.KGSnapshotDigest = [32]byte{} }, ErrInvalid},
		{"missing generation", func(s *Source) { s.GenerationRef = "" }, ErrInvalid},
		{"missing instance", func(s *Source) { s.InstanceRef = "" }, ErrInvalid},
		{"checkpoint scope", func(s *Source) { s.FinalCheckpointScope.RunRef = "foreign" }, ErrInvalid},
		{"checkpoint absent", func(s *Source) { s.FinalCheckpointDigest = [32]byte{} }, ErrInvalid},
		{"field mismatch", func(s *Source) { s.CanaryFields = []string{"other"} }, ErrInvalid},
		{"invalid UTF8", func(s *Source) { s.ModelTag = string([]byte{0xff}) }, ErrInvalid},
		{"wrong policy", func(s *Source) { s.Policy.ExpectedAccess = oracle.AccessAllow }, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := cloneSource(source)
			tc.mutate(&s)
			p, err := Prepare(t.Context(), in, sourceReader(s))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if _, valid := p.Snapshot(); valid {
				t.Fatal("invalid source issued prepared record")
			}
		})
	}
	if _, err := Prepare(t.Context(), in, nil); !errors.Is(err, ErrSource) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Prepare(ctx, in, sourceReader(source)); !errors.Is(err, ErrSource) {
		t.Fatal(err)
	}
	if _, err := Prepare(t.Context(), in, func(context.Context, evidencebind.Context) (Source, error) {
		return Source{}, errors.New("PRIVATE_KG_ERROR")
	}); !errors.Is(err, ErrSource) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal(err)
	}
}

func TestPreparedIsDeterministicAndImmutable(t *testing.T) {
	in, source := fixture()
	p, err := Prepare(t.Context(), in, sourceReader(source))
	if err != nil {
		t.Fatal(err)
	}
	first, ok := p.Snapshot()
	if !ok || first.Value != oracle.Confirmed {
		t.Fatal("missing confirmed prepared record")
	}
	if bytes.Contains(first.Canonical, []byte("finding_id")) {
		t.Fatal("mutable finding link entered canonical verdict")
	}
	if !bytes.Contains(first.Canonical, []byte("2026-11-03T12:00:20.000000Z")) {
		t.Fatal("verdict time not UTC microseconds")
	}
	again, err := Prepare(t.Context(), in, sourceReader(source))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := again.Snapshot()
	if first.InputDigest != second.InputDigest || first.OperationKey != second.OperationKey || !bytes.Equal(first.Canonical, second.Canonical) {
		t.Fatal("same input is not deterministic")
	}
	first.Canonical[0] = 0
	first.Manifest[0] = 0
	first.Binding[0] = 0
	first.Source.CanaryFields[0] = "tampered"
	third, _ := p.Snapshot()
	if third.Canonical[0] == 0 || third.Manifest[0] == 0 || third.Binding[0] == 0 || third.Source.CanaryFields[0] != "note" {
		t.Fatal("returned slices mutate authority")
	}
	raw, err := json.Marshal(p)
	if err != nil || string(raw) != "{}" {
		t.Fatal("prepared authority serialized")
	}
}

func TestPrepareRejectsPrecisionChangeAndForgedVerdict(t *testing.T) {
	in, source := fixture()
	in.Evidence.Result.Result.Verdict.Value = oracle.Allowed
	if _, err := Prepare(t.Context(), in, sourceReader(source)); !errors.Is(err, ErrInvalid) {
		t.Fatal("forged result accepted", err)
	}
	in, source = fixture()
	// At the original observation time this policy is valid. Truncation makes
	// its upper bound equal to the observation, which would change the verdict.
	source.Policy.ValidUntil = in.Evidence.Result.ObservedAt.Add(500 * time.Nanosecond)
	in.Evidence.Policy.Assertion = source.Policy
	source.EvidenceDigest, _ = EvidenceDigest(in.Evidence)

	if _, err := Prepare(t.Context(), in, sourceReader(source)); !errors.Is(err, ErrPrecision) {
		t.Fatal("precision changed historical verdict", err)
	}
}

func TestCanonicalSharedPythonVectors(t *testing.T) {
	got, err := canonical(map[string]any{"z": int64(math.MaxInt64), "a": []any{"<>&\u2028\u2029", true, nil, "é", "\n"}})
	want := "{\"a\":[\"<>&\u2028\u2029\",true,null,\"é\",\"\\n\"],\"z\":9223372036854775807}"
	if err != nil || string(got) != want {
		t.Fatalf("canonical mismatch: %q %v", got, err)
	}
	for _, value := range []any{map[string]any{"bad": string([]byte{0xff})}, float64(1), uint64(math.MaxUint64), strings.Repeat("x", MaxCanonicalBytes)} {
		if _, err := canonical(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsupported canonical input accepted: %T %v", value, err)
		}
	}
}

func TestPrepareRejectsCoherentEvidenceNotIssuedBySource(t *testing.T) {
	for _, kind := range []string{"observed_at", "evidence_ref", "facts_and_verdict", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			in, source := fixture()
			switch kind {
			case "observed_at":
				in.Evidence.Cross.ObservedAt = in.Evidence.Cross.ObservedAt.Add(time.Millisecond)
				in.Evidence.Result.ObservedAt = in.Evidence.Cross.ObservedAt
			case "evidence_ref":
				in.Evidence.Cross.Evidence.ID = "other-consistent-ref"
				in.Evidence.Result.CrossEvidence = in.Evidence.Cross.Evidence
			case "facts_and_verdict":
				in.Evidence.Result.Result.Facts.CrossNormalData = oracle.StateUnknown
				in.Evidence.Result.Result.Verdict = oracle.Evaluate(oracle.CaseInput{Policy: source.Policy,
					Subjects: oracle.SubjectContext{OwnerStatus: in.Expected.Owner.Status, EvaluatedStatus: in.Expected.Evaluated.Status,
						OwnerRef: in.Expected.Owner.Label, EvaluatedRef: in.Expected.Evaluated.Label}, Facts: in.Evidence.Result.Result.Facts}, in.Evidence.Cross.ObservedAt)
			case "receipt":
				in.Evidence.Cross.Receipt.StatusCode = 201
			}
			if checked := evidencebind.Check(in.Expected, in.Evidence); !checked.BindingValid {
				t.Fatal("negative fixture must remain structurally consistent")
			}
			if _, err := Prepare(t.Context(), in, sourceReader(source)); !errors.Is(err, ErrInvalid) {
				t.Fatal("coherent forged evidence accepted", err)
			}
		})
	}
}

func TestPrepareKeepsRecordCreationSeparateFromObservation(t *testing.T) {
	in, source := fixture()
	prepared, err := Prepare(t.Context(), in, sourceReader(source))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := prepared.Snapshot()
	var value map[string]any
	if err := json.Unmarshal(snapshot.Canonical, &value); err != nil {
		t.Fatal(err)
	}
	if value["created_at"] == value["observed_at"] || value["created_at"] != canonicalTime(source.RecordCreatedAt) {
		t.Fatal("record creation time inferred from observation")
	}
	source.RecordCreatedAt = time.Time{}
	if _, err := Prepare(t.Context(), in, sourceReader(source)); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing record creation time accepted", err)
	}
}

func TestPrepareRequiresWholeResourceAndConfirmedFieldNames(t *testing.T) {
	in, source := fixture()
	in.CanaryFields = nil
	source.CanaryFields = nil
	if _, err := Prepare(t.Context(), in, sourceReader(source)); !errors.Is(err, ErrInvalid) {
		t.Fatal("confirmed without canary field names", err)
	}
	in, source = fixture()
	c := in.Expected
	c.ResourceRef = "missing-colon"
	in.Expected = c
	source.Expected = c
	in.Evidence.Policy.Context = c
	in.Evidence.Owner.Context = c
	in.Evidence.Cross.Context = c
	in.Evidence.Result.Context = c
	in.Evidence.Owner.Receipt.ResourceRef = c.ResourceRef
	in.Evidence.Cross.Receipt.ResourceRef = c.ResourceRef
	source.EvidenceDigest, _ = EvidenceDigest(in.Evidence)
	if checked := evidencebind.Check(c, in.Evidence); !checked.BindingValid {
		t.Fatal("must remain structurally bound")
	}
	if _, err := Prepare(t.Context(), in, sourceReader(source)); !errors.Is(err, ErrInvalid) {
		t.Fatal("unstructured resource accepted", err)
	}
}

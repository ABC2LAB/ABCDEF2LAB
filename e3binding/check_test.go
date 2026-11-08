package e3binding

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

func fixture() (Target, Input) {
	target := Target{TaskRef: "task-7", FindingID: 11, RetestID: 13, OriginalEvidenceRevision: 2}
	ctx := evidencebind.Context{
		Scope:    transmit.Scope{RunRef: "retest-run", TaskRef: target.TaskRef, CaseRef: "case-new", ResetRef: "reset-new", PolicyRef: "execution-policy-new"},
		IntentID: 17, ResourceRef: "document:item-1", ReadApprovalRef: "read-new", KGPolicyVersion: "kg-new",
		Owner:     idor.AccountRef{Label: "owner", Handle: "session-owner", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "cross", Handle: "session-cross", Status: oracle.SubjectVerified},
	}
	return target, Input{
		Finding:   Finding{ID: target.FindingID, TaskRef: target.TaskRef, SourceRunRef: "original-run", ResourceRef: ctx.ResourceRef, OriginalEvidenceRevision: target.OriginalEvidenceRevision},
		Retest:    Retest{ID: target.RetestID, FindingID: target.FindingID, OriginalEvidenceRevision: target.OriginalEvidenceRevision, Expected: ctx},
		Execution: Execution{RetestID: target.RetestID, FindingID: target.FindingID, OriginalEvidenceRevision: target.OriginalEvidenceRevision, Context: ctx},
	}
}

func requireDecision(t *testing.T, got Decision, consistent bool, reason Reason) {
	t.Helper()
	if got.Consistent() != consistent || got.Reason() != reason {
		t.Fatalf("decision: got consistent=%v reason=%s, want %v/%s", got.Consistent(), got.Reason(), consistent, reason)
	}
}

func TestCheckConsistentReferences(t *testing.T) {
	target, input := fixture()
	requireDecision(t, Check(target, input), true, Consistent)
	t.Run("same numeric IDs in distinct namespaces", func(t *testing.T) {
		target.RetestID = target.FindingID
		input.Retest.ID = target.RetestID
		input.Execution.RetestID = target.RetestID
		requireDecision(t, Check(target, input), true, Consistent)
	})
	t.Run("maximum numeric values", func(t *testing.T) {
		target, input := fixture()
		target.FindingID = math.MaxInt64
		target.RetestID = math.MaxInt64
		target.OriginalEvidenceRevision = math.MaxUint64
		input.Finding.ID = target.FindingID
		input.Finding.OriginalEvidenceRevision = target.OriginalEvidenceRevision
		input.Retest.ID = target.RetestID
		input.Retest.FindingID = target.FindingID
		input.Retest.OriginalEvidenceRevision = target.OriginalEvidenceRevision
		input.Execution.RetestID = target.RetestID
		input.Execution.FindingID = target.FindingID
		input.Execution.OriginalEvidenceRevision = target.OriginalEvidenceRevision
		requireDecision(t, Check(target, input), true, Consistent)
	})
}

func TestCheckRequiresTargetAndRecords(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Target, *Input)
		reason Reason
	}{
		{"target zero", func(t *Target, i *Input) { *t = Target{} }, InvalidTarget},
		{"target task", func(t *Target, i *Input) { t.TaskRef = "" }, InvalidTarget},
		{"target finding", func(t *Target, i *Input) { t.FindingID = 0 }, InvalidTarget},
		{"target negative finding", func(t *Target, i *Input) { t.FindingID = -1 }, InvalidTarget},
		{"target retest", func(t *Target, i *Input) { t.RetestID = 0 }, InvalidTarget},
		{"target negative retest", func(t *Target, i *Input) { t.RetestID = -1 }, InvalidTarget},
		{"target revision", func(t *Target, i *Input) { t.OriginalEvidenceRevision = 0 }, InvalidTarget},
		{"finding zero", func(t *Target, i *Input) { i.Finding = Finding{} }, InvalidFinding},
		{"finding id", func(t *Target, i *Input) { i.Finding.ID = -1 }, InvalidFinding},
		{"finding task", func(t *Target, i *Input) { i.Finding.TaskRef = "" }, InvalidFinding},
		{"finding resource", func(t *Target, i *Input) { i.Finding.ResourceRef = "" }, InvalidFinding},
		{"finding source run", func(t *Target, i *Input) { i.Finding.SourceRunRef = "" }, InvalidFinding},
		{"finding revision", func(t *Target, i *Input) { i.Finding.OriginalEvidenceRevision = 0 }, InvalidFinding},
		{"retest zero", func(t *Target, i *Input) { i.Retest = Retest{} }, InvalidRetest},
		{"retest id", func(t *Target, i *Input) { i.Retest.ID = -1 }, InvalidRetest},
		{"retest finding", func(t *Target, i *Input) { i.Retest.FindingID = 0 }, InvalidRetest},
		{"retest revision", func(t *Target, i *Input) { i.Retest.OriginalEvidenceRevision = 0 }, InvalidRetest},
		{"execution zero", func(t *Target, i *Input) { i.Execution = Execution{} }, InvalidExecution},
		{"execution retest", func(t *Target, i *Input) { i.Execution.RetestID = -1 }, InvalidExecution},
		{"execution finding", func(t *Target, i *Input) { i.Execution.FindingID = 0 }, InvalidExecution},
		{"execution revision", func(t *Target, i *Input) { i.Execution.OriginalEvidenceRevision = 0 }, InvalidExecution},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, input := fixture()
			tc.mutate(&target, &input)
			requireDecision(t, Check(target, input), false, tc.reason)
		})
	}
}

func TestCheckRejectsForeignAssociations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
		reason Reason
	}{
		{"finding ID", func(i *Input) { i.Finding.ID++ }, FindingMismatch},
		{"finding task", func(i *Input) { i.Finding.TaskRef = "other-task" }, FindingMismatch},
		{"finding revision", func(i *Input) { i.Finding.OriginalEvidenceRevision++ }, FindingMismatch},
		{"retest ID", func(i *Input) { i.Retest.ID++ }, RetestMismatch},
		{"retest finding", func(i *Input) { i.Retest.FindingID++ }, RetestMismatch},
		{"retest revision", func(i *Input) { i.Retest.OriginalEvidenceRevision++ }, RetestMismatch},
		{"retest task", func(i *Input) { i.Retest.Expected.Scope.TaskRef = "other-task" }, RetestMismatch},
		{"retest resource", func(i *Input) { i.Retest.Expected.ResourceRef = "document:other" }, RetestMismatch},
		{"execution retest", func(i *Input) { i.Execution.RetestID++ }, ExecutionMismatch},
		{"execution finding", func(i *Input) { i.Execution.FindingID++ }, ExecutionMismatch},
		{"execution revision", func(i *Input) { i.Execution.OriginalEvidenceRevision++ }, ExecutionMismatch},
		{"coherent foreign finding", func(i *Input) { i.Finding.ID++; i.Retest.FindingID++; i.Execution.FindingID++ }, FindingMismatch},
		{"coherent foreign retest", func(i *Input) { i.Retest.ID++; i.Execution.RetestID++ }, RetestMismatch},
		{"coherent foreign task", func(i *Input) {
			i.Finding.TaskRef = "other-task"
			i.Retest.Expected.Scope.TaskRef = "other-task"
			i.Execution.Context.Scope.TaskRef = "other-task"
		}, FindingMismatch},
		{"coherent stale revision", func(i *Input) {
			i.Finding.OriginalEvidenceRevision--
			i.Retest.OriginalEvidenceRevision--
			i.Execution.OriginalEvidenceRevision--
		}, FindingMismatch},
		{"original execution replay", func(i *Input) {
			i.Retest.Expected.Scope.RunRef = i.Finding.SourceRunRef
			i.Execution.Context = i.Retest.Expected
		}, SourceExecutionReused},
		{"coherent resource substitution", func(i *Input) {
			i.Retest.Expected.ResourceRef = "document:other"
			i.Execution.Context = i.Retest.Expected
		}, RetestMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, input := fixture()
			tc.mutate(&input)
			requireDecision(t, Check(target, input), false, tc.reason)
		})
	}
}

func contextLeaves(v reflect.Value, prefix string, visit func(string, reflect.Value)) {
	for n := 0; n < v.NumField(); n++ {
		field := v.Field(n)
		name := v.Type().Field(n).Name
		if prefix != "" {
			name = prefix + "." + name
		}
		if field.Kind() == reflect.Struct {
			contextLeaves(field, name, visit)
		} else {
			visit(name, field)
		}
	}
}

func TestCheckRequiresEveryContextField(t *testing.T) {
	_, sample := fixture()
	var fields []string
	contextLeaves(reflect.ValueOf(sample.Retest.Expected), "", func(name string, _ reflect.Value) { fields = append(fields, name) })
	for _, name := range fields {
		for _, record := range []string{"expected", "actual"} {
			t.Run("missing/"+record+"/"+name, func(t *testing.T) {
				target, input := fixture()
				ctx := &input.Retest.Expected
				reason := InvalidRetest
				if record == "actual" {
					ctx = &input.Execution.Context
					reason = InvalidExecution
				}
				contextLeaves(reflect.ValueOf(ctx).Elem(), "", func(path string, field reflect.Value) {
					if path == name {
						field.SetZero()
					}
				})
				requireDecision(t, Check(target, input), false, reason)
			})
		}
		t.Run("foreign actual/"+name, func(t *testing.T) {
			target, input := fixture()
			contextLeaves(reflect.ValueOf(&input.Execution.Context).Elem(), "", func(path string, field reflect.Value) {
				if path != name {
					return
				}
				switch field.Kind() {
				case reflect.Int64:
					field.SetInt(field.Int() + 1)
				case reflect.String:
					if strings.HasSuffix(name, ".Status") {
						field.SetString(string(oracle.SubjectUnknown))
					} else {
						field.SetString("other-" + field.String())
					}
				default:
					t.Fatalf("uncovered context type %s", field.Type())
				}
			})
			requireDecision(t, Check(target, input), false, ContextMismatch)
		})
	}
}

func TestCheckValidatesReferenceSyntax(t *testing.T) {
	for n, ref := range []string{"https://sensitive.example", " bad", "bad value", "bad\n", "bad\\ref", "-bad", "한글", strings.Repeat("x", 129)} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			target, input := fixture()
			target.TaskRef = ref
			requireDecision(t, Check(target, input), false, InvalidTarget)
		})
	}
	t.Run("maximum reference", func(t *testing.T) {
		target, input := fixture()
		target.TaskRef = strings.Repeat("x", 128)
		input.Finding.TaskRef = target.TaskRef
		input.Retest.Expected.Scope.TaskRef = target.TaskRef
		input.Execution.Context = input.Retest.Expected
		requireDecision(t, Check(target, input), true, Consistent)
	})
	t.Run("punctuation", func(t *testing.T) {
		target, input := fixture()
		input.Finding.ResourceRef = "document:part/sub_part.id-1"
		input.Retest.Expected.ResourceRef = input.Finding.ResourceRef
		input.Execution.Context = input.Retest.Expected
		requireDecision(t, Check(target, input), true, Consistent)
	})
	for _, record := range []string{"expected", "actual"} {
		for _, invalid := range []string{"duplicate label", "duplicate handle", "unknown enum", "negative intent"} {
			t.Run(record+"/"+invalid, func(t *testing.T) {
				target, input := fixture()
				ctx := &input.Retest.Expected
				reason := InvalidRetest
				if record == "actual" {
					ctx = &input.Execution.Context
					reason = InvalidExecution
				}
				switch invalid {
				case "duplicate label":
					ctx.Evaluated.Label = ctx.Owner.Label
				case "duplicate handle":
					ctx.Evaluated.Handle = ctx.Owner.Handle
				case "unknown enum":
					ctx.Owner.Status = "unexpected"
				case "negative intent":
					ctx.IntentID = -1
				}
				requireDecision(t, Check(target, input), false, reason)
			})
		}
	}
}

func TestCheckReasonPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
		reason Reason
	}{
		{"record syntax before associations", func(i *Input) { i.Finding.ID++; i.Retest = Retest{} }, InvalidRetest},
		{"execution syntax before associations", func(i *Input) { i.Finding.ID++; i.Execution.Context.Owner.Status = "invalid" }, InvalidExecution},
		{"finding before retest", func(i *Input) { i.Finding.ID++; i.Retest.ID++ }, FindingMismatch},
		{"retest before execution", func(i *Input) { i.Retest.ID++; i.Execution.RetestID++ }, RetestMismatch},
		{"replay before execution mismatch", func(i *Input) { i.Retest.Expected.Scope.RunRef = i.Finding.SourceRunRef; i.Execution.RetestID++ }, SourceExecutionReused},
		{"execution IDs before context", func(i *Input) { i.Execution.RetestID++; i.Execution.Context.Scope.RunRef = "different" }, ExecutionMismatch},
		{"malformed source run", func(i *Input) { i.Finding.SourceRunRef = "https://invalid.example" }, InvalidFinding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, input := fixture()
			tc.mutate(&input)
			requireDecision(t, Check(target, input), false, tc.reason)
		})
	}
}

func TestCheckPermitsNewExecutionGenerations(t *testing.T) {
	target, input := fixture()
	// The new retest owns its independent reset/policy/case/intent/session scope.
	input.Retest.Expected.Scope.ResetRef = "another-new-reset"
	input.Retest.Expected.Scope.PolicyRef = "another-new-policy"
	input.Retest.Expected.Scope.CaseRef = "another-new-case"
	input.Retest.Expected.IntentID++
	input.Retest.Expected.ReadApprovalRef = "another-read-approval"
	input.Retest.Expected.KGPolicyVersion = "another-kg-version"
	input.Retest.Expected.Owner.Handle = "another-owner-session"
	input.Execution.Context = input.Retest.Expected
	requireDecision(t, Check(target, input), true, Consistent)
	for _, status := range []oracle.SubjectStatus{oracle.SubjectVerified, oracle.SubjectUnknown, oracle.SubjectExpired, oracle.SubjectMismatch} {
		t.Run(string(status), func(t *testing.T) {
			target, input := fixture()
			input.Retest.Expected.Evaluated.Status = status
			input.Execution.Context = input.Retest.Expected
			requireDecision(t, Check(target, input), true, Consistent)
		})
	}
}

func TestCheckIsPureAndDoesNotRetainMetadata(t *testing.T) {
	target, input := fixture()
	before := input
	got := Check(target, input)
	requireDecision(t, got, true, Consistent)
	for range 5 {
		if Check(target, input) != got {
			t.Fatal("unstable repeated decision")
		}
	}
	if input != before {
		t.Fatal("input mutated")
	}
	var zero Decision
	requireDecision(t, zero, false, Unknown)
	input.Execution.Context.Scope.RunRef = "SENSITIVE_REF_MUST_NOT_ESCAPE"
	denied := Check(target, input)
	body, err := json.Marshal(denied)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(body), fmt.Sprint(denied), fmt.Sprintf("%+v", denied), fmt.Sprintf("%#v", denied)} {
		for _, raw := range []string{target.TaskRef, input.Finding.ResourceRef, "SENSITIVE_REF_MUST_NOT_ESCAPE", "session-owner"} {
			if strings.Contains(output, raw) {
				t.Fatal("decision retained input metadata")
			}
		}
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if Check(target, before) != got {
					t.Error("concurrent decision differs")
				}
			}
		}()
	}
	wg.Wait()
}

func FuzzCheckRejectsForeignRun(f *testing.F) {
	for _, ref := range []string{"retest-run", "original-run", "other-run", "", "http://invalid", strings.Repeat("x", 129)} {
		f.Add(ref)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		target, input := fixture()
		input.Execution.Context.Scope.RunRef = ref
		got := Check(target, input)
		if got.Consistent() != (ref == input.Retest.Expected.Scope.RunRef) {
			t.Fatal("foreign run accepted or expected run rejected")
		}
	})
}

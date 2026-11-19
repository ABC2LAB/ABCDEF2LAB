package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/oracle"
)

func storeTestResult(status e3contract.ConfirmationStatus) e3contract.RunResult {
	binding := e3contract.RunBinding{JobRef: "job-store", RunRef: "run-store", TaskRef: "9", E2PolicyRef: "e2-policy-store",
		GenerationRef: "generation-store", InstanceRef: "instance-store", Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration,
			Ref: "declaration-job-store", DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "3"}}
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	return e3contract.RunResult{Version: e3contract.RunResultVersion, Status: status, Binding: &binding, Verdicts: []e3contract.Verdict{
		{ResourceType: "note", Value: oracle.Confirmed, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateYes, DataCheck: oracle.StateYes,
			OracleReasons: []oracle.JudgeReason{oracle.ReasonPolicyDenyResolved}, ProductReasons: []e3contract.ProductReason{}, CandidateRefs: []string{"7"},
			Evidence:       []e3contract.CallRef{{CallRef: "call-1", AttemptID: "attempt-1", Sequence: 1}, {CallRef: "call-2", AttemptID: "attempt-2", Sequence: 1}},
			ObjectIDDigest: strings.Repeat("d", 64), ObservedAt: at},
		{ResourceType: "secret", Value: oracle.Undecidable, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateUnknown, DataCheck: oracle.StateUnknown,
			OracleReasons: []oracle.JudgeReason{}, ProductReasons: []e3contract.ProductReason{e3contract.ReasonCreateApprovalDenied}, CandidateRefs: []string{},
			Evidence: []e3contract.CallRef{{CallRef: "call-3", AttemptID: "attempt-3", Sequence: 1}}, ObservedAt: at},
	}}
}

type storeSpy struct {
	stored []db.ProductVerdict
	fail   string
}

func (s *storeSpy) store(_ context.Context, v db.ProductVerdict) (db.ProductVerdictResult, error) {
	if v.ResourceType == s.fail {
		return db.ProductVerdictResult{}, errors.New("synthetic store failure")
	}
	s.stored = append(s.stored, v)
	return db.ProductVerdictResult{Created: true}, nil
}

// 확정 단계 판정은 회차당 근거 체크포인트 하나와 함께 실행 역할로 저장한다(E1 계약 §2·§4). 체크포인트는 마지막 근거 호출을
// Host에 다시 검증받아 얻는다.
func TestProductE3StoreVerdicts(t *testing.T) {
	cp := e3v3.Checkpoint{JournalID: "journal-store", Sequence: 14, MAC: strings.Repeat("c", 64)}
	for name, tc := range map[string]struct {
		status     e3contract.ConfirmationStatus
		noEvidence bool
		cpErr      error
		noCPFunc   bool
		fail       string
		want       int
		wantCP     bool
		asked      int
	}{
		"completed":            {status: e3contract.StatusCompleted, want: 2, wantCP: true, asked: 1},
		"interrupted":          {status: e3contract.StatusInterrupted, want: 2, wantCP: true, asked: 1},
		"checkpoint_failed":    {status: e3contract.StatusCompleted, cpErr: errors.New("ack unknown"), want: 2, asked: 1},
		"no_checkpoint_func":   {status: e3contract.StatusCompleted, noCPFunc: true, want: 2},
		"no_evidence":          {status: e3contract.StatusCompleted, noEvidence: true, want: 2},
		"one_store_fails":      {status: e3contract.StatusCompleted, fail: "note", want: 1, wantCP: true, asked: 1},
		"integrity_failed":     {status: e3contract.StatusIntegrityFailed},
		"disabled":             {status: e3contract.StatusDisabled},
		"start_refused_status": {status: e3contract.StatusStartRefused},
	} {
		t.Run(name, func(t *testing.T) {
			res := storeTestResult(tc.status)
			if tc.noEvidence {
				for i := range res.Verdicts {
					res.Verdicts[i].Evidence = []e3contract.CallRef{}
				}
			}
			spy := &storeSpy{fail: tc.fail}
			c := &productE3Confirmation{jobID: "job-store", store: spy.store, modelTag: "openrouter:qwen:qwen3.6-35b-a3b"}
			var asked []e3contract.CallRef
			var checkpoint productE3CheckpointFunc = func(_ context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Checkpoint, error) {
				if b != *res.Binding {
					t.Fatal("다른 결속으로 물음")
				}
				asked = append(asked, ref)
				return cp, tc.cpErr
			}
			if tc.noCPFunc {
				checkpoint = nil
			}
			c.storeVerdicts(t.Context(), "9", res, checkpoint)
			if len(spy.stored) != tc.want || len(asked) != tc.asked {
				t.Fatalf("저장 %d·체크포인트 물음 %d", len(spy.stored), len(asked))
			}
			if tc.asked == 1 && asked[0] != (e3contract.CallRef{CallRef: "call-3", AttemptID: "attempt-3", Sequence: 1}) {
				t.Fatalf("마지막 근거 호출로 묻지 않음: %+v", asked)
			}
			for _, v := range spy.stored {
				if (v.Checkpoint != nil) != tc.wantCP || tc.wantCP && *v.Checkpoint != (db.ProductCheckpoint{JournalID: cp.JournalID, Sequence: cp.Sequence, MAC: cp.MAC}) {
					t.Fatalf("체크포인트: %+v", v.Checkpoint)
				}
				if v.TaskID != 9 || v.ModelTag != c.modelTag || v.Binding.RunRef != "run-store" || v.Binding.Policy.Ref != "declaration-job-store" ||
					v.Binding.Policy.DigestSHA256 != [32]byte{0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa} {
					t.Fatalf("결속: %+v", v)
				}
				switch v.ResourceType {
				case "note":
					if v.ResourceRef != "note:"+strings.Repeat("d", 64) || v.Value != oracle.Confirmed || len(v.CandidateRefs) != 1 ||
						!tc.noEvidence && (len(v.Evidence) != 2 || v.Evidence[1].CallRef != "call-2") {
						t.Fatalf("note: %+v", v)
					}
				case "secret":
					if v.ResourceRef != "secret:unknown" || len(v.ProductReasons) != 1 || v.ProductReasons[0] != string(e3contract.ReasonCreateApprovalDenied) {
						t.Fatalf("secret: %+v", v)
					}
				}
			}
		})
	}
	// 저장 연결이 없으면 묻지도 저장하지도 않는다.
	c := &productE3Confirmation{jobID: "job-store"}
	c.storeVerdicts(t.Context(), "9", storeTestResult(e3contract.StatusCompleted), func(context.Context, e3contract.RunBinding, e3contract.CallRef) (e3v3.Checkpoint, error) {
		t.Fatal("저장 연결 없이 체크포인트를 물음")
		return e3v3.Checkpoint{}, nil
	})
}

func TestProductE3ModelTag(t *testing.T) {
	for _, tc := range []struct {
		scope *diagnosisprep.Scope
		want  string
	}{
		{nil, "profile-42"},
		{&diagnosisprep.Scope{}, "profile-42"},
		{&diagnosisprep.Scope{Model: &diagnosisprep.ModelInfo{Mode: "openrouter", Model: "qwen/qwen3.6-35b-a3b"}}, "openrouter:qwen:qwen3.6-35b-a3b"},
		{&diagnosisprep.Scope{Model: &diagnosisprep.ModelInfo{Mode: "openrouter", Model: "a b@c"}}, "openrouter:a-b-c"},
		{&diagnosisprep.Scope{Model: &diagnosisprep.ModelInfo{Mode: "openrouter", Model: strings.Repeat("m", 200)}}, "openrouter:" + strings.Repeat("m", 117)},
	} {
		if got := productE3ModelTag(tc.scope, 42); got != tc.want {
			t.Fatalf("%+v: %q", tc.scope, got)
		}
	}
}

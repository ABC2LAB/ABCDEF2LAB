package e3v3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/e3contract/e3v3/v3fake"
	"github.com/Autumn-27/artex/oracle"
)

var t0 = time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)

const frozenRef = "decl-frozen-1"

func declaration() e3contract.Declaration {
	ownerOnly := func(typ, path string) e3contract.TypeDeclaration {
		return e3contract.TypeDeclaration{Type: typ, Sharing: e3contract.SharingOwnerOnly, OwnerOnlyFields: []string{"body"},
			Create: e3contract.CreateSpec{Path: path, Method: "POST", Kind: e3contract.BodyJSON, BodyTemplate: `{"title":"t"}`,
				MarkerField: "body", IDPath: "id"},
			Read: e3contract.ReadSpec{PathTemplate: path + "/{id}", DataPath: "."}}
	}
	return e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: "target-1",
		TargetApprovalRevision: "approval-3", DeclaredBy: "operator-1", DeclaredAt: t0,
		OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b", AccountsSamePrivilege: true,
		Identity: e3contract.IdentitySpec{Path: "/api/me", SubjectIDPath: "id"},
		Types:    []e3contract.TypeDeclaration{ownerOnly("note", "/api/notes"), ownerOnly("secret", "/api/secrets")}}
}

func seed() []byte {
	var out []byte
	sum := sha256.Sum256([]byte("e3v3-test"))
	for len(out) < 1024 {
		out = append(out, sum[:]...)
		sum = sha256.Sum256(sum[:])
	}
	return out
}

// world는 대상 동작 하나와 그 위의 확정 단계 입력이다.
type world struct {
	target *e3fake.Transport
	writes *e3fake.Writes
	decls  e3fake.Declarations
	in     e3contract.Input
}

func newWorld(t *testing.T, behaviors map[e3fake.Key]e3fake.Behavior) *world {
	t.Helper()
	decl := declaration()
	src, err := e3contract.Freeze(decl, frozenRef)
	if err != nil {
		t.Fatal(err)
	}
	target := e3fake.New()
	target.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleOwner}] = e3fake.Behavior{SubjectID: "user-a"}
	target.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleCross}] = e3fake.Behavior{SubjectID: "user-b"}
	for k, b := range behaviors {
		target.Behaviors[k] = b
	}
	return &world{target: target, writes: &e3fake.Writes{Transport: target},
		decls: e3fake.Declarations{Stored: map[string]e3contract.Declaration{frozenRef: decl}},
		in: e3contract.Input{
			Binding: e3contract.RunBinding{JobRef: "job-1", RunRef: "run-1", TaskRef: "task-1", E2PolicyRef: "e2-policy-1",
				GenerationRef: "gen-1", InstanceRef: "inst-1", Policy: src},
			TargetRef: "target-1", WriteConsent: true,
			Owner:           e3contract.Account{Label: "tester-a", Authenticated: true},
			Cross:           e3contract.Account{Label: "tester-b", Authenticated: true},
			PolicyValidFrom: t0.Add(-time.Hour), PolicyValidUntil: t0.Add(24 * time.Hour),
			AdmittedTypes: 8, RequestTimeout: 10 * time.Second,
			Candidates: []e3contract.Candidate{{Ref: "cand-note", Method: "GET", PathPattern: "/api/notes/{id}"}},
		}}
}

func (w *world) run(transport e3contract.Transport, journal e3contract.Journal) e3contract.RunResult {
	deps := e3contract.Deps{Transport: transport, Journal: journal, Declarations: w.decls,
		Reset: e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}}, Writes: w.writes,
		Random: bytes.NewReader(seed()), Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler)}
	return e3contract.Run(context.Background(), deps, w.in)
}

// viaV3는 같은 대상 동작을 가짜 v3 E²와 어댑터로 돌린다. edit로 가짜 E²의 반례 손잡이를 정한다.
func viaV3(t *testing.T, behaviors map[e3fake.Key]e3fake.Behavior, edit func(*v3fake.E2)) (e3contract.RunResult, *v3fake.E2) {
	t.Helper()
	w := newWorld(t, behaviors)
	e2 := v3fake.New(w.target, []byte("run-audit-key-for-tests-only-32b"))
	if edit != nil {
		edit(e2)
	}
	a, err := e3v3.New(e2)
	if err != nil {
		t.Fatal(err)
	}
	return w.run(a, a), e2
}

func key(p e3contract.Purpose, r e3contract.Role, typ string) e3fake.Key {
	return e3fake.Key{Purpose: p, Role: r, Type: typ}
}

var crossNote = key(e3contract.PurposeRead, e3contract.RoleCross, "note")

func noConfirmed(t *testing.T, res e3contract.RunResult) {
	t.Helper()
	for _, v := range res.Verdicts {
		if v.Value == oracle.Confirmed {
			t.Fatalf("CONFIRMED 판정이 나옴: %+v", v)
		}
	}
}

func integrityFailed(t *testing.T, res e3contract.RunResult, reason e3contract.IntegrityReason) {
	t.Helper()
	noConfirmed(t, res)
	if res.Status != e3contract.StatusIntegrityFailed || res.Reason != string(reason) || len(res.Verdicts) != 0 {
		t.Fatalf("진위 실패 %s 기대: %+v", reason, res)
	}
}

// 같은 대상 동작이면 어댑터·가짜 v3를 거친 확정 단계 결과가 e3fake 직결과 바이트로 같다(관찰을 잃거나 바꾸지 않음).
func TestAdapterMatchesDirectTransport(t *testing.T) {
	createNote := key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")
	cases := map[string]map[e3fake.Key]e3fake.Behavior{
		"normal":             nil,
		"policy_denied":      {createNote: {NotSent: e3contract.NotSentPolicyDenied}},
		"approval_denied":    {createNote: {NotSent: e3contract.NotSentApprovalDenied}},
		"approval_timeout":   {createNote: {NotSent: e3contract.NotSentApprovalTimeout}},
		"budget_exhausted":   {crossNote: {NotSent: e3contract.NotSentBudgetExhausted}},
		"create_timed_out":   {createNote: {TimedOut: true}},
		"read_incomplete":    {crossNote: {Incomplete: true}},
		"no_object_id":       {createNote: {NoObjectID: true}},
		"marker_absent":      {crossNote: {Presence: e3contract.MarkerAbsent}},
		"marker_partial":     {crossNote: {MarkerStatus: e3contract.MarkerPartial}},
		"marker_unsupported": {crossNote: {MarkerStatus: e3contract.MarkerUnsupported}},
		"marker_in_request":  {crossNote: {MarkerInRequest: true}},
		"cross_not_normal":   {crossNote: {Status: 404, Normal: oracle.StateNo}},
		"no_subject":         {key(e3contract.PurposeIdentity, e3contract.RoleCross, ""): {}},
	}
	for name, behaviors := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, behaviors)
			direct := w.run(w.target, w.target)
			adapted, _ := viaV3(t, behaviors, nil)
			want, _ := json.Marshal(direct)
			got, _ := json.Marshal(adapted)
			if !bytes.Equal(want, got) {
				t.Fatalf("어댑터 결과가 다름\n직결 %s\n어댑터 %s", want, got)
			}
		})
	}
	// 정상 대상이면 두 종류 모두 CONFIRMED까지 간다(어댑터가 확정 경로를 막지 않음).
	res, _ := viaV3(t, nil, nil)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 2 {
		t.Fatalf("%+v", res)
	}
	for _, v := range res.Verdicts {
		if v.Value != oracle.Confirmed {
			t.Fatalf("정상 대상이 확정되지 않음: %+v", v)
		}
	}
}

// 이벤트가 계약 §2.3 단계별 필수 조건을 어기면 결과 불명이 아니라 진위 실패(observation_invalid)다.
func TestAdapterEventStageRules(t *testing.T) {
	cases := map[string]func(*e3v3.Event){
		"version":                 func(e *e3v3.Event) { e.Version = "e2-http-v2" },
		"program":                 func(e *e3v3.Event) { e.Program = "auth_prep" },
		"intent_id":               func(e *e3v3.Event) { e.IntentID = 7 },
		"scope":                   func(e *e3v3.Event) { e.Scope.TaskRef = "" },
		"policy_revision":         func(e *e3v3.Event) { e.PolicyRevision = "" },
		"account_binding_ref":     func(e *e3v3.Event) { e.AccountBindingRef = "" },
		"e3_binding":              func(e *e3v3.Event) { e.E3.GenerationRef = "" },
		"e3_binding_policy":       func(e *e3v3.Event) { e.E3.Policy.DigestSHA256 = "short" },
		"read_without_create_ref": func(e *e3v3.Event) { e.CreateRef = e3contract.CallRef{} },
		"intent_as_last":          func(e *e3v3.Event) { e.Stage = e3v3.StageIntent },
		"result_with_not_sent":    func(e *e3v3.Event) { e.NotSentReason = e3contract.NotSentPolicyDenied },
		"result_not_forwarded":    func(e *e3v3.Event) { e.ForwardAttempted = false },
		"result_without_rule":     func(e *e3v3.Event) { e.RuleRef = "" },
		"complete_not_written":    func(e *e3v3.Event) { e.RequestWritten = false },
		"control_stage":           func(e *e3v3.Event) { e.Stage = "control" },
		"sent_but_denied":         func(e *e3v3.Event) { e.Decision = e3v3.DecisionDenied },
		"decision_unknown":        func(e *e3v3.Event) { e.Decision = "maybe" },
		"marker_presence_partial": func(e *e3v3.Event) { e.Marker.Status = e3contract.MarkerPartial },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			res, _ := viaV3(t, nil, func(e2 *v3fake.E2) {
				e2.Recorded = func(e *e3v3.Event) {
					if e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross && e.ResourceType == "note" {
						edit(e)
					}
				}
			})
			integrityFailed(t, res, e3contract.IntegrityObservationInvalid)
		})
	}
	// 송신 전 거부(result, not_sent)의 사유가 연결 전 거부 넷이 아니면(예: 연결 뒤 실패를 not_sent로 옮김) 진위 실패다.
	for name, edit := range map[string]func(*e3v3.Event){
		"not_sent_transport_failed": func(e *e3v3.Event) { e.NotSentReason = "transport_failed" },
		"not_sent_with_response":    func(e *e3v3.Event) { e.StatusCode = 200 },
		"not_sent_forwarded":        func(e *e3v3.Event) { e.ForwardAttempted = true },
		"not_sent_allowed":          func(e *e3v3.Event) { e.Decision, e.RuleRef = e3v3.DecisionAllowed, "rule-x" },
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := viaV3(t, map[e3fake.Key]e3fake.Behavior{crossNote: {NotSent: e3contract.NotSentPolicyDenied}},
				func(e2 *v3fake.E2) {
					e2.Recorded = func(e *e3v3.Event) {
						if e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross && e.ResourceType == "note" {
							edit(e)
						}
					}
				})
			integrityFailed(t, res, e3contract.IntegrityObservationInvalid)
		})
	}
}

// 이벤트의 계정 결속 참조가 그 실행·라벨에 지금 결속된 참조가 아니면(다시 로그인한 세션·다른 실행의 세션) 진위 실패다.
func TestAdapterAccountBindingRef(t *testing.T) {
	res, _ := viaV3(t, nil, func(e2 *v3fake.E2) {
		e2.Recorded = func(e *e3v3.Event) {
			if e.Role == e3contract.RoleCross && e.Program == e3contract.PurposeRead {
				e.AccountBindingRef = "acct-run-1-tester-b-old-session"
			}
		}
	})
	integrityFailed(t, res, e3contract.IntegrityObservationMismatch)
	// 요청 뒤 다시 로그인해 지금 결속이 바뀌었으면 그 요청의 이벤트는 지금 결속과 다르다(진위 실패).
	res, _ = viaV3(t, nil, func(e2 *v3fake.E2) {
		e2.AccountRefs = map[string]string{"run-1/tester-b": "acct-run-1-tester-b-relogin"}
	})
	integrityFailed(t, res, e3contract.IntegrityObservationMismatch)
	// 지금 결속을 확인할 수 없으면 그 요청은 결과 불명이다(확정하지 않음, 진위 실패도 아님).
	res, _ = viaV3(t, nil, func(e2 *v3fake.E2) { e2.AccountRefErr = errors.New("session store unavailable") })
	noConfirmed(t, res)
	if res.Status == e3contract.StatusIntegrityFailed || res.Status == e3contract.StatusCompleted && len(res.Verdicts) == 0 {
		t.Fatalf("%+v", res)
	}
}

// 계약 §9 반례: 아래는 모두 관찰을 돌려주지 않고 진위 실패다.
func TestAdapterJournalCounterexamples(t *testing.T) {
	key := []byte("run-audit-key-for-tests-only-32b")
	cases := map[string]struct {
		edit   func(*v3fake.E2)
		reason e3contract.IntegrityReason
	}{
		"no_current_head": {func(e2 *v3fake.E2) { e2.Trusted.Fail = errors.New("trusted store down") },
			e3contract.IntegrityJournalIntegrityFailed},
		// 끝 행 삭제: 남은 앞부분의 MAC·체인은 맞지만 신뢰 저장소의 현재 head와 다르다.
		"tail_deleted": {func(e2 *v3fake.E2) { e2.Snapshot = func(r []v3fake.Record) []v3fake.Record { return r[:len(r)-1] } },
			e3contract.IntegrityJournalIntegrityFailed},
		"mac_corrupted": {func(e2 *v3fake.E2) {
			e2.Snapshot = func(r []v3fake.Record) []v3fake.Record {
				r[0].Raw = bytes.Replace(r[0].Raw, []byte(`"tester-a"`), []byte(`"tester-x"`), 1)
				return r
			}
		}, e3contract.IntegrityJournalIntegrityFailed},
		"event_missing": {func(e2 *v3fake.E2) {
			e2.Unrecorded = func(e e3v3.Event) bool { return e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross }
		}, e3contract.IntegrityJournalEventMissing},
		// intent만 있고 result가 없는 호출은 이벤트 없음이 아니라 불완전 기록(무결성 실패)이다.
		"result_missing": {func(e2 *v3fake.E2) {
			e2.Unrecorded = func(e e3v3.Event) bool {
				return e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross && e.Stage == e3v3.StageResult
			}
		}, e3contract.IntegrityJournalIntegrityFailed},
		"returned_differs": {func(e2 *v3fake.E2) {
			e2.Returned = func(e *e3v3.Event) {
				if e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross {
					e.BodyDigestSHA256 = strings.Repeat("a", 64)
				}
			}
		}, e3contract.IntegrityJournalEventMismatch},
		"binding_mismatch": {func(e2 *v3fake.E2) {
			e2.Recorded = func(e *e3v3.Event) { e.E3.InstanceRef = "inst-other" }
		}, e3contract.IntegrityBindingMismatch},
		"role_mismatch": {func(e2 *v3fake.E2) {
			e2.Recorded = func(e *e3v3.Event) {
				if e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross {
					e.Role, e.AccountLabel = e3contract.RoleOwner, "tester-a"
					e.AccountBindingRef = "acct-run-1-tester-a"
				}
			}
		}, e3contract.IntegrityObservationMismatch},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res, _ := viaV3(t, nil, c.edit)
			integrityFailed(t, res, c.reason)
		})
	}

	// 과거 체크포인트와 과거 스냅샷은 서로 맞는다(진본). 그래도 신뢰 저장소의 현재 head가 그 뒤면 무결성 실패다.
	t.Run("stale_pair", func(t *testing.T) {
		var past []v3fake.Record
		res, e2 := viaV3(t, nil, func(e2 *v3fake.E2) {
			e2.Snapshot = func(r []v3fake.Record) []v3fake.Record {
				if past == nil && len(r) >= 2 {
					past = append([]v3fake.Record(nil), r[:len(r)-1]...)
				}
				if past != nil {
					return append([]v3fake.Record(nil), past...)
				}
				return r
			}
		})
		integrityFailed(t, res, e3contract.IntegrityJournalIntegrityFailed)
		cp := v3fake.Checkpoint{RunRef: "run-1", Index: uint64(len(past)), MAC: past[len(past)-1].MAC}
		if _, err := v3fake.Verify(key, past, cp); err != nil {
			t.Fatalf("과거 쌍은 서로 맞아야 함(반례 조건): %v", err)
		}
		if len(e2.Records()) <= len(past) {
			t.Fatal("현재 head가 과거 head 뒤가 아님")
		}
	})

	// 다른 실행의 저널: MAC·체인·끝이 그 저널의 체크포인트와 맞아도 이 실행의 것이 아니면 실패다.
	t.Run("foreign_run", func(t *testing.T) {
		_, other := viaV3(t, nil, nil)
		foreign := other.Records()
		var last e3v3.Event
		if err := json.Unmarshal(foreign[len(foreign)-1].Raw, &last); err != nil {
			t.Fatal(err)
		}
		cp := v3fake.Checkpoint{RunRef: "run-2", Index: last.Index, MAC: foreign[len(foreign)-1].MAC}
		if _, err := v3fake.Verify(key, foreign, cp); !errors.Is(err, e3contract.ErrJournalIntegrity) {
			t.Fatalf("다른 실행 저널을 받음: %v", err)
		}
		res, _ := viaV3(t, nil, func(e2 *v3fake.E2) {
			e2.Snapshot = func([]v3fake.Record) []v3fake.Record { return foreign }
		})
		integrityFailed(t, res, e3contract.IntegrityJournalIntegrityFailed)
	})

	// 검증 조회가 요청한 호출이 아닌 이벤트를 돌려주면 무결성 실패다.
	t.Run("verified_other_call", func(t *testing.T) {
		w := newWorld(t, nil)
		e2 := v3fake.New(w.target, key)
		a, _ := e3v3.New(&otherCall{E2: e2})
		integrityFailed(t, w.run(a, a), e3contract.IntegrityJournalIntegrityFailed)
	})
}

// otherCall은 검증 조회에서 처음 검증한 이벤트를 다음 호출에도 돌려준다(다른 호출의 이벤트).
type otherCall struct {
	*v3fake.E2
	first *e3v3.Event
}

func (o *otherCall) Verified(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Event, error) {
	if o.first != nil {
		return *o.first, nil
	}
	e, err := o.E2.Verified(ctx, b, ref)
	if err == nil {
		o.first = &e
	}
	return e, err
}

// 로컬 Sync 뒤 정본 head 게시가 실패하면 그 요청은 관찰 성공 없이 결과 불명이고, 그 뒤 송신은 없다(계약 §5).
func TestAdapterPublishFailure(t *testing.T) {
	createNote := key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")
	var sent int
	res, e2 := viaV3(t, nil, func(e2 *v3fake.E2) {
		e2.PublishFail = func(e e3v3.Event) bool {
			return e.Program == e3contract.PurposeCreate && e.ResourceType == "note"
		}
	})
	noConfirmed(t, res)
	if res.Status == e3contract.StatusIntegrityFailed || res.Status == e3contract.StatusCompleted && len(res.Verdicts) == 0 {
		t.Fatalf("%+v", res)
	}
	for _, v := range res.Verdicts {
		if v.Value != oracle.Undecidable {
			t.Fatalf("게시 실패 뒤 판정: %+v", v)
		}
	}
	// 생성 요청은 보냈을 수 있으니 쓰기 기록은 ID 모름으로 남는다.
	var unknown bool
	for _, w := range res.Writes {
		unknown = unknown || w.ResourceType == "note" && w.Outcome == e3contract.WriteUnknown
	}
	if !unknown {
		t.Fatalf("결과 불명 생성의 쓰기 기록: %+v", res.Writes)
	}
	// 게시 실패 뒤로는 대상 요청이 없다(대상 동작 모델에 남은 호출 = 게시 실패한 생성까지).
	for i, c := range e2.Target.Calls() {
		if c.Key == createNote {
			sent = len(e2.Target.Calls()) - i - 1
		}
	}
	if sent != 0 {
		t.Fatalf("게시 실패 뒤 요청 %d건", sent)
	}
}

// 표식 등록 실패(E²의 v3 감사 미연결 등)면 요청 없이 시작 거절이고, 권한 철회는 그대로 진위 실패다.
func TestAdapterRegisterAndAuthority(t *testing.T) {
	res, e2 := viaV3(t, nil, func(e2 *v3fake.E2) { e2.RegisterErr = errors.New("e2proxy: e3 journal v3 unavailable") })
	noConfirmed(t, res)
	if res.Status != e3contract.StatusStartRefused || res.Reason != string(e3contract.RefuseMarkerRegistrationFailed) ||
		len(e2.Records()) != 0 {
		t.Fatalf("%+v records=%d", res, len(e2.Records()))
	}
	res, _ = viaV3(t, map[e3fake.Key]e3fake.Behavior{crossNote: {Err: e3contract.ErrAuthorityRevoked}}, nil)
	integrityFailed(t, res, e3contract.IntegrityAuthorityRevoked)
}

// v3 이벤트 JSON 칸 이름은 계약 §2와 같다(E² 구현·평가 읽기와 맞추는 기준). 원문 칸은 없다.
func TestEventJSONFieldNames(t *testing.T) {
	_, e2 := viaV3(t, nil, nil)
	var reads []map[string]any
	for _, rec := range e2.Records() {
		var m map[string]any
		if err := json.Unmarshal(rec.Raw, &m); err != nil {
			t.Fatal(err)
		}
		if m["program"] == "e3_read" && m["stage"] == "result" {
			reads = append(reads, m)
		}
	}
	if len(reads) == 0 {
		t.Fatal("조회 result 이벤트 없음")
	}
	for _, k := range []string{"version", "index", "stage", "program", "intent_id", "scope", "call_ref", "attempt_id", "sequence",
		"policy_revision", "rule_ref", "method", "decision", "reason", "forward_attempted", "request_written", "response_received",
		"response_complete", "not_sent", "audit_complete", "status_code", "at", "role", "account_label", "account_binding_ref",
		"e3_binding", "resource_type", "create_ref", "request_marker_free", "marker", "body_digest_sha256", "body_complete"} {
		if _, ok := reads[0][k]; !ok {
			t.Fatalf("칸 %s 없음: %v", k, reads[0])
		}
	}
	for _, k := range []string{"url", "path", "body", "marker_value", "object_id", "subject_id", "session", "cookie", "authorization"} {
		if _, ok := reads[0][k]; ok {
			t.Fatalf("원문 칸 %s가 이벤트에 있음", k)
		}
	}
	scope := reads[0]["scope"].(map[string]any)
	binding := reads[0]["e3_binding"].(map[string]any)
	for _, k := range []string{"run_ref", "task_ref", "policy_ref"} {
		if _, ok := scope[k]; !ok {
			t.Fatalf("scope.%s 없음", k)
		}
	}
	for _, k := range []string{"job_ref", "generation_ref", "instance_ref", "policy"} {
		if _, ok := binding[k]; !ok {
			t.Fatalf("e3_binding.%s 없음", k)
		}
	}
	// 표식 값은 저널 어디에도 없다(등록한 표식은 E² 밖으로 나오지 않음).
	m, err := e3contract.NewMarker(bytes.NewReader(seed()))
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range e2.Records() {
		if bytes.Contains(rec.Raw, m.Reveal()) {
			t.Fatal("저널에 표식 값")
		}
	}
}

// Event.Check는 계약 §2.3 규칙을 그 자리에서 지킨다(관찰 검증이 뒤에서 다시 막더라도 v3 이벤트 규칙으로 먼저 거부).
func TestEventCheck(t *testing.T) {
	_, e2 := viaV3(t, map[e3fake.Key]e3fake.Behavior{crossNote: {NotSent: e3contract.NotSentPolicyDenied}}, nil)
	var read, denied e3v3.Event
	for _, rec := range e2.Records() {
		var e e3v3.Event
		if err := json.Unmarshal(rec.Raw, &e); err != nil {
			t.Fatal(err)
		}
		switch {
		case e.Program == e3contract.PurposeRead && e.Stage == e3v3.StageResult && e.NotSent:
			denied = e
		case e.Program == e3contract.PurposeRead && e.Stage == e3v3.StageResult:
			read = e
		}
	}
	if read.CallRef == "" || denied.CallRef == "" || read.Check() != nil || denied.Check() != nil {
		t.Fatalf("기준 이벤트: read=%v denied=%v", read.Check(), denied.Check())
	}
	cases := map[string]struct {
		base  e3v3.Event
		edit  func(*e3v3.Event)
		field string
	}{
		"scope":                   {read, func(e *e3v3.Event) { e.Scope.PolicyRef = "" }, "scope"},
		"e3_binding_policy":       {read, func(e *e3v3.Event) { e.E3.Policy.DigestSHA256 = "short" }, "e3_binding"},
		"read_create_ref":         {read, func(e *e3v3.Event) { e.CreateRef.Sequence = 0 }, "read_fields"},
		"identity_resource_type":  {read, func(e *e3v3.Event) { e.Program = e3contract.PurposeIdentity }, "identity_fields"},
		"create_with_create_ref":  {read, func(e *e3v3.Event) { e.Program = e3contract.PurposeCreate }, "create_fields"},
		"intent_as_last":          {read, func(e *e3v3.Event) { e.Stage = e3v3.StageIntent }, "intent_without_result"},
		"denied_but_sent":         {denied, func(e *e3v3.Event) { e.NotSent, e.NotSentReason = false, "" }, "decision"},
		"allowed_without_rule":    {read, func(e *e3v3.Event) { e.RuleRef = "" }, "rule_ref"},
		"decision_unknown_sent":   {read, func(e *e3v3.Event) { e.Decision = "maybe" }, "decision"},
		"decision_unknown_denied": {denied, func(e *e3v3.Event) { e.Decision = "maybe" }, "decision"},
		"not_sent_unknown_reason": {denied, func(e *e3v3.Event) { e.NotSentReason = "unknown" }, "not_sent_reason"},
		"allowed_but_not_sent":    {denied, func(e *e3v3.Event) { e.Decision, e.RuleRef = e3v3.DecisionAllowed, "rule-x" }, "not_sent_decision"},
		"allowed_closed_in_check": {denied, func(e *e3v3.Event) {
			e.Decision, e.RuleRef, e.NotSentReason, e.Reason = e3v3.DecisionAllowed, "rule-x", "", e3v3.ReasonClosed
		}, "not_sent_decision"},
		"not_sent_received": {denied, func(e *e3v3.Event) { e.ResponseReceived = true }, "not_sent_has_response"},
	}
	for name, c := range cases {
		e := c.base
		c.edit(&e)
		if err := e.Check(); !errors.Is(err, e3contract.ErrObservation) || !strings.HasSuffix(err.Error(), "v3 "+c.field) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// intent 뒤 송신 전 중단(계약 §2.3): 실행 문맥이 끝났으면 관찰 없이 결과 불명·중단이고, 생성이면 쓰기 기록은 ID 모름으로
// 남는다(거두지 않음). 문맥이 살아 있는데 이 행이 오면 관찰 계약 위반이다.
func TestAdapterStoppedAfterIntent(t *testing.T) {
	createNote := func(r e3v3.Request) bool { return r.Program == e3contract.PurposeCreate && r.ResourceType == "note" }
	w := newWorld(t, nil)
	e2 := v3fake.New(w.target, []byte("run-audit-key-for-tests-only-32b"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e2.StopAfterIntent, e2.Stop = createNote, cancel
	a, _ := e3v3.New(e2)
	deps := e3contract.Deps{Transport: a, Journal: a, Declarations: w.decls,
		Reset: e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}}, Writes: w.writes,
		Random: bytes.NewReader(seed()), Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler)}
	res := e3contract.Run(ctx, deps, w.in)
	noConfirmed(t, res)
	if res.Status != e3contract.StatusInterrupted || res.Reason != "" {
		t.Fatalf("중단: %+v", res)
	}
	var note []e3contract.ConfirmationWrite
	for _, wr := range res.Writes {
		if wr.ResourceType == "note" {
			note = append(note, wr)
		}
	}
	if len(note) != 1 || note[0].Outcome != e3contract.WriteUnknown {
		t.Fatalf("중단된 생성의 쓰기 기록은 ID 모름으로 남아야 함: %+v", res.Writes)
	}
	for _, c := range w.target.Calls() {
		if c.Key.Purpose == e3contract.PurposeCreate && c.Key.Type == "note" {
			t.Fatal("중단된 생성이 대상에 닿음")
		}
	}
	// 문맥이 살아 있는데 중단 행이 오면 진위 실패다.
	res, _ = viaV3(t, nil, func(e2 *v3fake.E2) { e2.StopAfterIntent = createNote })
	integrityFailed(t, res, e3contract.IntegrityObservationInvalid)
	// 중단 행 모양이 조금이라도 다르면(예: 사유가 closed가 아님) 중단으로 보지 않는다.
	ev := e3v3.Event{Stage: e3v3.StageResult, Decision: e3v3.DecisionAllowed, NotSent: true, Reason: e3v3.ReasonClosed}
	if !ev.StoppedAfterIntent() {
		t.Fatal("중단 행을 못 알아봄")
	}
	for name, edit := range map[string]func(*e3v3.Event){
		"reason":     func(e *e3v3.Event) { e.Reason = "denied" },
		"forwarded":  func(e *e3v3.Event) { e.ForwardAttempted = true },
		"with_cause": func(e *e3v3.Event) { e.NotSentReason = e3contract.NotSentPolicyDenied },
		"denied":     func(e *e3v3.Event) { e.Decision = e3v3.DecisionDenied },
		"response":   func(e *e3v3.Event) { e.StatusCode = 200 },
	} {
		x := ev
		edit(&x)
		if x.StoppedAfterIntent() {
			t.Fatalf("%s: 중단 행으로 봄", name)
		}
	}
}

// 가짜 v3를 거쳐도 상태 줄만 받고 본문이 끊긴 결과는 결과 불명이다(response_received=true·status_code·response_complete=false,
// E② 일반 행 규칙과 같은 모양). 진위 실패로 버리지 않는다(#774 리뷰).
func TestAdapterIncompleteWithStatusIsResultUnknown(t *testing.T) {
	res, e2 := viaV3(t, map[e3fake.Key]e3fake.Behavior{crossNote: {Incomplete: true, Status: 200}}, nil)
	if res.Status != e3contract.StatusCompleted {
		t.Fatalf("진위 실패로 끝남: %+v", res)
	}
	found := false
	for _, rec := range e2.Records() {
		var e e3v3.Event
		if err := json.Unmarshal(rec.Raw, &e); err != nil {
			t.Fatal(err)
		}
		if e.Program == e3contract.PurposeRead && e.Stage == e3v3.StageResult && e.Role == e3contract.RoleCross && e.ResourceType == "note" {
			found = true
			if !e.ResponseReceived || e.ResponseComplete || e.StatusCode != 200 {
				t.Fatalf("가짜 v3 이벤트 모양: %+v", e)
			}
		}
	}
	for _, v := range res.Verdicts {
		if v.ResourceType == "note" && (v.Value != oracle.Undecidable || !slices.Contains(v.ProductReasons, e3contract.ReasonRequestResultUnknown)) {
			t.Fatalf("note: %+v", v)
		}
	}
	if !found {
		t.Fatal("교차 조회 결과 이벤트 없음")
	}
}

// 실제 E²(e2live)는 결속 참조 조회·저널 검증에서 끝난 문맥이면 그 오류를 돌려준다. 사용자 승인 모드의 생성 승인이 확정 단계
// 시간 몫 끝과 같은 순간에 만료되면, 어댑터를 거쳐도 보내지 않은 생성(approval_timeout)으로 끝나야 한다: 판정 사유
// product_create_approval_timeout, 쓰기 기록 거둠, 결과 쓰기 없음(리허설 F15, 2026-11-21).
func TestAdapterApprovalExpiryAtShareEnd(t *testing.T) {
	createNote := key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")
	w := newWorld(t, map[e3fake.Key]e3fake.Behavior{createNote: {NotSent: e3contract.NotSentApprovalTimeout}})
	ctx, expire := deadlineAtWill()
	live := &liveLike{E2: v3fake.New(w.target, []byte("run-audit-key-for-tests-only-32b")), expire: expire, expireType: "note"}
	a, err := e3v3.New(live)
	if err != nil {
		t.Fatal(err)
	}
	deps := e3contract.Deps{Transport: a, Journal: a, Declarations: w.decls,
		Reset: e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}}, Writes: w.writes,
		Random: bytes.NewReader(seed()), Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler)}
	res := e3contract.Run(ctx, deps, w.in)
	noConfirmed(t, res)
	if res.Status == e3contract.StatusIntegrityFailed {
		t.Fatalf("승인 만료를 진위 실패로: %+v", res)
	}
	var note *e3contract.Verdict
	for i := range res.Verdicts {
		if res.Verdicts[i].ResourceType == "note" {
			note = &res.Verdicts[i]
		}
	}
	if note == nil || note.Value != oracle.Undecidable || !slices.Equal(note.ProductReasons, []e3contract.ProductReason{e3contract.ReasonCreateApprovalTimeout}) {
		t.Fatalf("note: %+v", note)
	}
	for _, wr := range res.Writes {
		if wr.ResourceType == "note" {
			t.Fatalf("보내지 않은 생성이 결과 쓰기에 남음: %+v", wr)
		}
	}
	withdrawn := 0
	for _, e := range w.writes.Events() {
		if e.Type == "note" && !e.Reserve && e.Write.Outcome == e3contract.WriteNotSent {
			withdrawn++
		}
	}
	if withdrawn != 1 {
		t.Fatalf("쓰기 기록을 거두지 않음(%d건)", withdrawn)
	}
}

// liveLike는 가짜 v3 E²에 실제 E²(e2live.Live)처럼 결속 참조 조회·저널 검증 앞에서 문맥 끝을 확인하는 동작을 더한다.
// expireType 생성 요청의 결과를 돌려주는 순간 호출자 문맥의 마감을 지나게 한다(승인 기한 = 시간 몫 끝).
type liveLike struct {
	*v3fake.E2
	expire     func()
	expireType string
}

func (l *liveLike) Send(ctx context.Context, req e3v3.Request) (e3v3.Event, error) {
	ev, err := l.E2.Send(ctx, req)
	if req.Program == e3contract.PurposeCreate && req.ResourceType == l.expireType {
		l.expire()
	}
	return ev, err
}

func (l *liveLike) AccountBindingRef(ctx context.Context, b e3contract.RunBinding, label string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return l.E2.AccountBindingRef(ctx, b, label)
}

func (l *liveLike) Verified(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Event, error) {
	if err := ctx.Err(); err != nil {
		return e3v3.Event{}, err
	}
	return l.E2.Verified(ctx, b, ref)
}

// deadlineAtWill은 expire를 부르면 그때부터 마감이 지난 문맥이다.
func deadlineAtWill() (context.Context, func()) {
	d := &willDeadline{Context: context.Background(), done: make(chan struct{})}
	return d, d.expire
}

type willDeadline struct {
	context.Context
	done    chan struct{}
	once    sync.Once
	expired atomic.Bool
}

func (d *willDeadline) Done() <-chan struct{} { return d.done }
func (d *willDeadline) Err() error {
	if d.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}
func (d *willDeadline) expire() {
	d.once.Do(func() {
		d.expired.Store(true)
		close(d.done)
	})
}

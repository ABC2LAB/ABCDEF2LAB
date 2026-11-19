package e2proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/oracle"
)

func e3Bool(value bool) *bool { return &value }

func e3JournalFixture(program ProgramCallKind) (Event, Event) {
	result := journalV3Event()
	result.Program, result.IntentID, result.CommandRef = program, 0, ""
	result.E3Audit = &E3Audit{Role: e3contract.RoleOwner, AccountLabel: "owner-a",
		AccountBindingRef: "account-binding-a", E3: E3Binding{JobRef: "job-1",
			GenerationRef: "generation-1", InstanceRef: "instance-1",
			Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration,
				Ref: "declaration-1", DigestSHA256: strings.Repeat("a", 64),
				TargetApprovalRevision: "approval-1"}},
		Normal: oracle.StateYes, BodyDigestSHA256: strings.Repeat("b", 64), BodyComplete: e3Bool(true)}
	switch program {
	case ProgramE3Identity:
		result.E3Audit.SubjectDigest = strings.Repeat("c", 64)
		result.E3Audit.RequestMarkerFree = e3Bool(true)
	case ProgramE3Create:
		result.Method = "POST"
		result.E3Audit.ResourceType = "item"
		result.E3Audit.ObjectIDDigest = strings.Repeat("d", 64)
	case ProgramE3Read:
		result.E3Audit.Role = e3contract.RoleCross
		result.E3Audit.ResourceType = "item"
		result.E3Audit.CreateRef = e3contract.CallRef{CallRef: "create-1",
			AttemptID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Sequence: 1}
		result.E3Audit.RequestMarkerFree = e3Bool(true)
		result.E3Audit.Marker = e3contract.MarkerCheck{Status: e3contract.MarkerComplete,
			Presence: e3contract.MarkerPresent, Method: e3contract.MatchExact, Field: "owner"}
	}
	intent := result
	intent.E3Audit = new(E3Audit)
	*intent.E3Audit = *result.E3Audit
	intent.Index, intent.Stage = 1, "intent"
	intent.NotSent, intent.ForwardAttempted, intent.RequestWritten = true, false, false
	intent.ResponseReceived, intent.ResponseComplete, intent.StatusCode = false, false, 0
	intent.E3Audit.Normal, intent.E3Audit.RequestMarkerFree = "", nil
	intent.E3Audit.Marker, intent.E3Audit.BodyDigestSHA256 = e3contract.MarkerCheck{}, ""
	intent.E3Audit.BodyComplete, intent.E3Audit.ObjectIDDigest, intent.E3Audit.SubjectDigest = nil, "", ""
	result.Index = 2
	return intent, result
}

func TestJournalV3E3StructuredEventsAndMAC(t *testing.T) {
	for _, program := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
		t.Run(string(program), func(t *testing.T) {
			intent, result := e3JournalFixture(program)
			w := newJournalTestWriter()
			j, err := newJournalWithHeader(w, journalTestKey, journalHeader{
				Version: JournalVersionV3, Scope: result.Scope, KeyRef: "audit-ref", CreatedAt: result.At})
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			for _, event := range []Event{intent, result} {
				if err := j.Record(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			data, head := w.bytes(), j.Checkpoint()
			rows, summary, err := VerifyJournalEvents(t.Context(), bytes.NewReader(data), journalTestKey, head)
			if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows, []Event{intent, result}) || summary.Events != 2 {
				t.Fatal("E③ 사건 왕복 검증 실패", err)
			}
			intentJSON, err := json.Marshal(rows[0])
			if err != nil {
				t.Fatal(err)
			}
			var intentFields map[string]json.RawMessage
			if err := json.Unmarshal(intentJSON, &intentFields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"normal", "request_marker_free", "marker", "body_digest_sha256", "body_complete", "object_id_digest", "subject_digest"} {
				if _, present := intentFields[field]; present {
					t.Fatal("송신 전 사건에 응답 필드 포함", field)
				}
			}
			encoded, err := json.Marshal(rows[1])
			if err != nil {
				t.Fatal(err)
			}
			var resultFields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &resultFields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"normal", "body_digest_sha256", "body_complete"} {
				if _, present := resultFields[field]; !present {
					t.Fatal("완료 결과의 필수 필드 누락", field)
				}
			}
			if _, present := resultFields["request_marker_free"]; present != (program != ProgramE3Create) {
				t.Fatal("요청 표식 검사 필드가 목적과 불일치")
			}
			var adapter e3v3.Event
			if err := json.Unmarshal(encoded, &adapter); err != nil {
				t.Fatal(err)
			}
			// 기존 transmit.Scope의 저널 JSON은 대문자 키다. E③ 어댑터는
			// 이를 소문자 태그가 있는 Scope DTO로 명시적으로 옮겨야 한다.
			adapter.Scope = e3v3.Scope{RunRef: result.Scope.RunRef,
				TaskRef: result.Scope.TaskRef, PolicyRef: result.Scope.PolicyRef}
			if err := adapter.Check(); err != nil {
				t.Fatal("E③ 구조화 필드가 어댑터 계약과 불일치", err)
			}
			changed := bytes.Replace(data, []byte(`"account_label":"owner-a"`), []byte(`"account_label":"owner-b"`), 1)
			if bytes.Equal(changed, data) {
				t.Fatal("변조 반례가 필드를 찾지 못함")
			}
			if rows, _, err := VerifyJournalEvents(t.Context(), bytes.NewReader(changed), journalTestKey, head); !errors.Is(err, ErrJournalIntegrity) || rows != nil {
				t.Fatal("E③ 계정 필드 변조를 승인", err)
			}
		})
	}
}

func TestJournalV3E3RejectsMissingAndWrongFields(t *testing.T) {
	_, base := e3JournalFixture(ProgramE3Read)
	base.Index = 1
	cases := map[string]func(*Event){
		"missing metadata":   func(e *Event) { e.E3Audit = nil },
		"worker attribution": func(e *Event) { e.IntentID = 7 },
		"wrong method":       func(e *Event) { e.Method = "POST" },
		"missing account":    func(e *Event) { e.AccountBindingRef = "" },
		"wrong policy":       func(e *Event) { e.E3.Policy.Kind = e3contract.PolicyKGSnapshot },
		"missing create":     func(e *Event) { e.CreateRef = e3contract.CallRef{} },
		"wrong role":         func(e *Event) { e.Role = "visitor" },
		"missing marker":     func(e *Event) { e.Marker = e3contract.MarkerCheck{} },
		"invalid resource":   func(e *Event) { e.ResourceType = "item?raw=secret" },
		"not sent lie":       func(e *Event) { e.NotSent = true },
		"raw digest":         func(e *Event) { e.BodyDigestSHA256 = "raw" },
		"wrong stage":        func(e *Event) { e.Stage = "control" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			event := base
			event.E3Audit = new(E3Audit)
			*event.E3Audit = *base.E3Audit
			change(&event)
			if validJournalRecordEvent(event, JournalVersionV3) {
				t.Fatal("잘못된 E③ 사건 허용")
			}
		})
	}
	intent, _ := e3JournalFixture(ProgramE3Create)
	intent.ResourceType = "item?raw=secret"
	if validJournalRecordEvent(intent, JournalVersionV3) {
		t.Fatal("송신 전 사건에 잘못된 자원 종류 허용")
	}
	_, identity := e3JournalFixture(ProgramE3Identity)
	identity.SubjectDigest = ""
	if validJournalRecordEvent(identity, JournalVersionV3) {
		t.Fatal("완료된 신원 응답에 주체 digest 누락 허용")
	}
	for _, version := range []string{VersionV1, Version} {
		event := base
		event.Version = version
		if validJournalRecordEvent(event, version) {
			t.Fatal("구버전에서 E③ 필드 허용", version)
		}
		ordinary := journalTestEvent()
		ordinary.Version = version
		ordinary.E3Audit = base.E3Audit
		if validJournalRecordEvent(ordinary, version) {
			t.Fatal("구버전 일반 사건에 E③ 필드 허용", version)
		}
	}
	ordinary := journalV3Event()
	ordinary.E3Audit = base.E3Audit
	if validJournalRecordEvent(ordinary, JournalVersionV3) {
		t.Fatal("일반 E② 사건에 E③ 필드 허용")
	}
	if ValidCallAttribution(0, ProgramE3Read) {
		t.Fatal("E③ 프로그램 송신이 활성화됨")
	}
}

func TestJournalV3E3BeforeSendAndStoppedAfterIntent(t *testing.T) {
	_, denied := e3JournalFixture(ProgramE3Create)
	denied.Index, denied.Decision, denied.Reason = 1, "denied", PolicyDenied
	denied.NotSent, denied.ForwardAttempted, denied.RequestWritten = true, false, false
	denied.ResponseReceived, denied.ResponseComplete, denied.StatusCode = false, false, 0
	denied.Normal, denied.BodyDigestSHA256, denied.ObjectIDDigest = "", "", ""
	denied.BodyComplete = nil
	denied.NotSentReason = e3contract.NotSentPolicyDenied
	if !validJournalRecordEvent(denied, JournalVersionV3) {
		t.Fatal("확정된 송신 전 거부 행 차단")
	}
	stopped := denied
	stopped.Decision, stopped.Reason = "allowed", Closed
	stopped.NotSentReason = ""
	if !validJournalRecordEvent(stopped, JournalVersionV3) {
		t.Fatal("intent 뒤 실행 문맥 종료 행 차단")
	}
	stopped.Reason = Complete
	if validJournalRecordEvent(stopped, JournalVersionV3) {
		t.Fatal("살아 있는 문맥의 미송신 성공 행 허용")
	}
}

// 응답 상태 줄을 받았으나 본문이 끝나지 않은 시도는 결과 불명으로 보존한다.
// 세 프로그램 목적 모두 상태 코드와 미완료 사실을 MAC으로 묶고 관찰을 완료로 승격하지 않는다.
func TestJournalV3E3IncompleteResponseWithStatus(t *testing.T) {
	for _, program := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
		t.Run(string(program), func(t *testing.T) {
			intent, result := e3JournalFixture(program)
			result.Reason = Unknown
			result.ResponseComplete = false
			result.StatusCode = 200
			result.Normal = ""
			result.RequestMarkerFree = nil
			result.Marker = e3contract.MarkerCheck{}
			result.BodyDigestSHA256 = ""
			result.BodyComplete = nil
			result.ObjectIDDigest = ""
			result.SubjectDigest = ""
			if !validJournalRecordEvent(result, JournalVersionV3) {
				t.Fatal("상태 줄을 받은 미완료 응답 차단")
			}
			w := newJournalTestWriter()
			j, err := newJournalWithHeader(w, journalTestKey, journalHeader{
				Version: JournalVersionV3, Scope: result.Scope, KeyRef: "audit-ref", CreatedAt: result.At})
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			for _, event := range []Event{intent, result} {
				if err := j.Record(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			rows, _, err := VerifyJournalEvents(t.Context(), bytes.NewReader(w.bytes()), journalTestKey, j.Checkpoint())
			if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows[1], result) {
				t.Fatal("미완료 응답의 검증된 사건 조회 실패", err)
			}
			encoded, err := json.Marshal(rows[1])
			if err != nil {
				t.Fatal(err)
			}
			var adapter e3v3.Event
			if err := json.Unmarshal(encoded, &adapter); err != nil {
				t.Fatal(err)
			}
			adapter.Scope = e3v3.Scope{RunRef: result.Scope.RunRef,
				TaskRef: result.Scope.TaskRef, PolicyRef: result.Scope.PolicyRef}
			if err := adapter.Check(); err != nil {
				t.Fatal("미완료 응답이 E③ 계약에서 거부됨", err)
			}
		})
	}
}

// 목적별 메서드와 단계별 응답 칸 금지를 독립 반례로 고정한다.
func TestJournalV3E3StageAndMethodBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		program ProgramCallKind
		change  func(*Event)
	}{
		{"identity POST", ProgramE3Identity, func(e *Event) { e.Method = "POST" }},
		{"create GET", ProgramE3Create, func(e *Event) { e.Method = "GET" }},
		{"create DELETE", ProgramE3Create, func(e *Event) { e.Method = "DELETE" }},
		{"create response marker flag", ProgramE3Create, func(e *Event) { e.RequestMarkerFree = e3Bool(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, event := e3JournalFixture(tc.program)
			tc.change(&event)
			if validJournalRecordEvent(event, JournalVersionV3) {
				t.Fatal("목적·메서드 또는 응답 칸 불일치를 허용")
			}
		})
	}
	_, create := e3JournalFixture(ProgramE3Create)
	create.Method = "PUT"
	if !validJournalRecordEvent(create, JournalVersionV3) {
		t.Fatal("선언 가능한 PUT 생성 행 차단")
	}
	for _, tc := range []struct {
		name   string
		change func(*Event)
	}{
		{"timeout", func(e *Event) { e.TimedOut = true }},
		{"normal", func(e *Event) { e.Normal = oracle.StateYes }},
		{"marker flag", func(e *Event) { e.RequestMarkerFree = e3Bool(true) }},
	} {
		t.Run("intent "+tc.name, func(t *testing.T) {
			intent, _ := e3JournalFixture(ProgramE3Identity)
			tc.change(&intent)
			if validJournalRecordEvent(intent, JournalVersionV3) {
				t.Fatal("송신 전 사건에 응답 칸 허용")
			}
		})
	}
	_, closed := e3JournalFixture(ProgramE3Read)
	closed.Stage, closed.AttemptID, closed.Sequence, closed.Decision = "call_closed", "", 0, ""
	closed.RuleRef, closed.Method, closed.StatusCode = "", "", 0
	closed.ForwardAttempted, closed.RequestWritten = false, false
	closed.ResponseReceived, closed.ResponseComplete, closed.NotSent = false, false, false
	audit := closed.E3Audit
	closed.E3Audit = nil
	if !validJournalRecordEvent(closed, JournalVersionV3) {
		t.Fatal("시도 밖 E③ 호출 마감 행 차단")
	}
	closed.E3Audit = audit
	if validJournalRecordEvent(closed, JournalVersionV3) {
		t.Fatal("호출 마감 행에 E③ 시도 칸 허용")
	}
}

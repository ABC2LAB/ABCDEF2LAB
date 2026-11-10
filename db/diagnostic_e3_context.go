package db

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/remediation"
	"sort"
	"strconv"
	"time"
)

// 해시를 확인한 manifest의 독립 결속을 재검사한다. 본문·계정 값은 내보내지 않는다.
func diagnosticE3Context(in *remediation.Input, task int64, raw []byte, value string) error {
	var m struct {
		Version  int64
		Evidence evidencebind.Input
	}
	if json.Unmarshal(raw, &m) != nil || m.Version != 1 {
		return ErrDiagnosticRemediation
	}
	expected := m.Evidence.Policy.Context
	if expected.Scope.TaskRef != strconv.FormatInt(task, 10) {
		return ErrDiagnosticRemediation
	}
	checked := evidencebind.Check(expected, m.Evidence)
	if !checked.BindingValid || string(checked.EffectiveValue) != value {
		return ErrDiagnosticRemediation
	}
	observations := []remediation.ContextObservation{}
	for _, record := range []struct {
		title, id string
		o         evidencebind.Observation
	}{{"소유 계정의 실제 조회", "owner-read", m.Evidence.Owner}, {"평가 계정의 실제 교차 조회", "cross-read", m.Evidence.Cross}} {
		r := record.o.Receipt
		observations = append(observations, remediation.ContextObservation{Source: "observed", Method: r.Method, Observation: fmt.Sprintf("%s: 요청 기록 %t, 응답 기록 %t, HTTP %d, 송신 결과 %s", record.title, r.RequestWritten, r.ResponseReceived, r.StatusCode, r.Reason), EvidenceRef: record.o.Evidence.ID, ObservedAt: record.o.ObservedAt.UTC().Format(time.RFC3339Nano), RunID: expected.Scope.RunRef, StepID: record.id})
	}
	// 실제 관측 시각 순서만 사용한다. 시간의 근접성으로 회차나 의존 관계를 추론하지 않는다.
	sort.SliceStable(observations, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, observations[i].ObservedAt)
		b, _ := time.Parse(time.RFC3339Nano, observations[j].ObservedAt)
		return a.Before(b)
	})
	observations = append(observations, remediation.ContextObservation{Source: "observed", Observation: "저장된 E③ 구조화 판정: " + value, EvidenceRef: in.FindingID, ObservedAt: m.Evidence.Result.ObservedAt.UTC().Format(time.RFC3339Nano), RunID: expected.Scope.RunRef, StepID: "e3-comparison", DependsOn: []string{"owner-read", "cross-read"}})
	in.Context.Observations = append(in.Context.Observations, observations...)
	return nil
}

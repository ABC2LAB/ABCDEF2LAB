package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func TestFirstDiagnosisSupervisorErrorsKeepCauseWithoutDisclosure(t *testing.T) {
	const canary = "synthetic-private-db-path-key-canary"
	cause := errors.Join(context.Canceled, e2proxy.ErrAudit, e2proxy.ErrDrain, errors.New(canary))
	operation := firstDiagnosisFailure(cause)
	outcome := firstDiagnosisOutcomeFailure{cause: operation}
	for name, value := range map[string]error{"operation": operation, "outcome": outcome} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(value, context.Canceled) || !errors.Is(value, e2proxy.ErrAudit) || !errors.Is(value, e2proxy.ErrDrain) || !errors.Is(value, ErrFirstDiagnosisSupervisor) {
				t.Fatal("취소·감사·drain 원인이 errors.Is에서 사라짐")
			}
			if errors.Unwrap(value) == nil {
				t.Fatal("검토 가능한 원인 연결이 사라짐")
			}
			if name == "outcome" && !errors.Is(value, ErrFirstDiagnosisOutcome) {
				t.Fatal("실패 결과 sentinel이 사라짐")
			}
			encoded, err := json.Marshal(map[string]any{"error": value})
			if err != nil {
				t.Fatal("고정 오류 JSON 출력 실패")
			}
			rendered := fmt.Sprintf("%v %+v %#v %s", value, value, value, value) + string(encoded)
			if strings.Contains(rendered, canary) || strings.Contains(rendered, "cause:") {
				t.Fatal("원인 또는 private 자료가 공유 오류 출력으로 노출됨")
			}
		})
	}
}

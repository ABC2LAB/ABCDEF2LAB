//go:build linux

package server

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestE2RuntimeControlRefusesInventedUIDSeparation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 UID fixture")
	}
	ctx := t.Context()
	backend, err := NewE2RuntimeControlBackend(ctx, E2RuntimeControlOptions{EvalRunID: "eval-fixture", ReservationRef: "reservation-fixture", ModelTag: "model-fixture", ExecutionRef: "execution-fixture", OutputDirectory: t.TempDir(), ExpectedPolicyFingerprintSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	// role 숫자만 다르고 실제 엔진은 현재 서비스와 같은 프로세스다. 이것은 다른 UID 증거가 아니다.
	options := E2ControlOptions{ControlDirectory: t.TempDir(), EvalRunID: "eval-fixture", ReservationRef: "reservation-fixture", RunnerUID: uid, EngineUID: uid + 1, ToolsUID: uid + 2, EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: time.Second, MaxConnections: 2, Backend: backend}
	service, err := StartE2ControlService(ctx, options)
	if err == nil {
		_ = service.Close(ctx)
		t.Fatal("임의 role 번호로 embedded 엔진의 평가 UID 경계를 우회함")
	}
}

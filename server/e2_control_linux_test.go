//go:build linux

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func e2ControlPrivateTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "e2ctl-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestE2ControlRejectsEmbeddedRoleAndMissingBoundary(t *testing.T) {
	_, b := newE2ControlStateFixture(t)
	uid := uint32(os.Geteuid())
	opts := E2ControlOptions{ControlDirectory: e2ControlPrivateTestDir(t), EvalRunID: "eval_one", ReservationRef: "reservation_one", RunnerUID: uid, EngineUID: uid, ToolsUID: uid, EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: time.Second, MaxConnections: 2, Backend: b}
	if _, err := StartE2ControlService(t.Context(), opts); err == nil {
		t.Fatal("같은 실제 UID의 embedded 구성을 평가 격리로 승격")
	}
	opts.EngineUID = uid + 1
	opts.ToolsUID = uid + 2
	opts.EvaluationDeployment = false
	if _, err := StartE2ControlService(t.Context(), opts); err == nil {
		t.Fatal("비평가 배치에 평가 채널 활성화")
	}
	opts.EvaluationDeployment = true
	var missing *e2ControlTestBackend
	opts.Backend = missing
	if _, err := StartE2ControlService(t.Context(), opts); err == nil {
		t.Fatal("typed nil 공급자 수용")
	}
}
func TestE2ControlActualUnixWireAndClose(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 평가 채널 fixture")
	}
	dir, err := os.MkdirTemp("", "e2ctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if os.RemoveAll(dir) != nil {
			t.Error("제어 fixture 정리 실패")
		}
	})
	_, b := newE2ControlStateFixture(t)
	uid := uint32(os.Geteuid())
	// 역할 숫자는 합성 구성이다. 이 시험은 실제 서로 다른 UID/mount 배치 수용이 아니다.
	s, err := StartE2ControlService(t.Context(), E2ControlOptions{ControlDirectory: dir, EvalRunID: "eval_one", ReservationRef: "reservation_one", RunnerUID: uid, EngineUID: uid + 1, ToolsUID: uid + 2, EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: time.Second, MaxConnections: 2, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if s.Close(ctx) != nil {
			t.Error("제어 채널 종료 실패")
		}
	})
	conn, err := net.Dial("unix", filepath.Join(dir, E2ControlSocket))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	request := map[string]any{"format": E2ControlFormat, "type": "ready", "eval_run_id": "eval_one", "reservation_ref": "reservation_one"}
	data, _ := json.Marshal(request)
	data = append(data, '\n')
	if _, err = conn.Write(data); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(reply, &got) != nil || got["state"] != "service_ready" || got["format"] != E2ControlFormat {
		t.Fatal("실제 UDS wire 실패")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s.Close(ctx) != nil || s.Close(ctx) != nil {
		t.Fatal("제어 채널 종료 멱등 실패")
	}
	if _, err := os.Lstat(filepath.Join(dir, E2ControlSocket)); !os.IsNotExist(err) {
		t.Fatal("자기 socket 정리 실패")
	}
}

func TestE2ControlToolSandboxNeedsTheOwningBackend(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 제어 fixture")
	}
	_, fake := newE2ControlStateFixture(t)
	real, _, _ := standaloneFixture(t, "", false)
	base := E2ControlOptions{ControlDirectory: e2ControlPrivateTestDir(t), EvalRunID: "eval_one", ReservationRef: "reservation_one", RunnerUID: uint32(os.Geteuid()), EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: time.Second, MaxConnections: 2}
	for name, change := range map[string]func(*E2ControlOptions){
		"샌드박스가 없는 backend": func(o *E2ControlOptions) { o.Isolation, o.Backend = E2ControlIsolationToolSandbox, fake },
		"엔진 uid 선언": func(o *E2ControlOptions) {
			o.Isolation, o.Backend, o.EngineUID = E2ControlIsolationToolSandbox, real, 1
		},
		"도구 uid 선언":       func(o *E2ControlOptions) { o.Isolation, o.Backend, o.ToolsUID = E2ControlIsolationToolSandbox, real, 1 },
		"모르는 격리 값":        func(o *E2ControlOptions) { o.Isolation, o.Backend = "other", real },
		"uid 분리인데 uid 없음": func(o *E2ControlOptions) { o.Backend = real },
	} {
		t.Run(name, func(t *testing.T) {
			opts := base
			change(&opts)
			if _, err := StartE2ControlService(t.Context(), opts); err == nil {
				t.Fatal("격리 선언을 잘못 수용")
			}
		})
	}
	opts := base
	opts.Isolation, opts.Backend = E2ControlIsolationToolSandbox, real
	service, err := StartE2ControlService(t.Context(), opts)
	if err != nil {
		t.Fatal("샌드박스를 소유한 backend 거부:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// 조건을 하나씩만 어겨야 서로 가리지 않는다.
func TestE2ControlSeparateUIDsCheckEachRole(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 제어 fixture")
	}
	_, fake := newE2ControlStateFixture(t)
	uid := uint32(os.Geteuid())
	base := E2ControlOptions{ControlDirectory: e2ControlPrivateTestDir(t), EvalRunID: "eval_one", ReservationRef: "reservation_one", RunnerUID: uid, EngineUID: uid + 1, ToolsUID: uid + 2, EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: time.Second, MaxConnections: 2, Backend: fake}
	for name, change := range map[string]func(*E2ControlOptions){
		"엔진 uid 없음":  func(o *E2ControlOptions) { o.EngineUID = 0 },
		"도구 uid 없음":  func(o *E2ControlOptions) { o.ToolsUID = 0 },
		"엔진이 러너 uid": func(o *E2ControlOptions) { o.EngineUID = uid },
		"도구가 러너 uid": func(o *E2ControlOptions) { o.ToolsUID = uid },
	} {
		t.Run(name, func(t *testing.T) {
			opts := base
			change(&opts)
			if _, err := StartE2ControlService(t.Context(), opts); err == nil {
				t.Fatal("uid 분리가 안 된 구성을 수용")
			}
		})
	}
	service, err := StartE2ControlService(t.Context(), base)
	if err != nil {
		t.Fatal("uid가 분리된 구성 거부:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

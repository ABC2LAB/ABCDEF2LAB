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

func TestE2ControlRejectsEmbeddedRoleAndMissingBoundary(t *testing.T) {
	_, b := newE2ControlStateFixture(t)
	uid := uint32(os.Geteuid())
	opts := E2ControlOptions{ControlDirectory: t.TempDir(), EvalRunID: "eval_one", ReservationRef: "reservation_one", RunnerUID: uid, EngineUID: uid, ToolsUID: uid, EvaluationDeployment: true, IOTimeout: time.Second, CloseTimeout: time.Second, MaxConnections: 2, Backend: b}
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

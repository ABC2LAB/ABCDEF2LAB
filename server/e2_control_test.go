package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

type e2ControlTestBackend struct {
	mu                                sync.Mutex
	ready                             E2ControlReady
	final                             E2ControlFinal
	gate                              chan struct{}
	bindFail, closeFail               bool
	bindCalls, closeCalls, finalCalls atomic.Int32
}

func e2ControlTestReady() E2ControlReady {
	return E2ControlReady{State: "service_ready", ConfigSHA256: strings.Repeat("a", 64), PolicyFingerprintSHA256: strings.Repeat("b", 64), HeaderMode: e2proxy.HeaderPreserve}
}
func (b *e2ControlTestBackend) Ready(context.Context) (E2ControlReady, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ready, nil
}
func (b *e2ControlTestBackend) BindReset(context.Context, string) error {
	b.bindCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bindFail {
		return errors.New("private-bind-canary")
	}
	return nil
}
func (b *e2ControlTestBackend) Head(context.Context) (E2ControlHead, error) {
	return E2ControlHead{State: "no_execution"}, nil
}
func (b *e2ControlTestBackend) CloseAdmission(context.Context, string) (E2ControlHead, error) {
	b.closeCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closeFail {
		return E2ControlHead{}, errors.New("private-close-canary")
	}
	return E2ControlHead{State: "running", ExecutionRef: "execution_one", RunRef: "run_one", EngineTaskID: "17", PolicyFingerprintSHA256: strings.Repeat("b", 64)}, nil
}
func (b *e2ControlTestBackend) Finalize(ctx context.Context, _ string) (E2ControlFinal, error) {
	b.finalCalls.Add(1)
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return E2ControlFinal{}, ctx.Err()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.final, nil
}
func newE2ControlStateFixture(t *testing.T) (*E2ControlService, *e2ControlTestBackend) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	backend := &e2ControlTestBackend{ready: e2ControlTestReady(), gate: make(chan struct{}), final: E2ControlFinal{State: "complete", ExecutionRef: "execution_one", ManifestSHA256: strings.Repeat("c", 64), Checkpoint: &E2ControlCheckpoint{Sequence: 3, MAC: strings.Repeat("d", 64)}}}
	service := &E2ControlService{opts: E2ControlOptions{EvalRunID: "eval_one", ReservationRef: "reservation_one", Backend: backend, CloseTimeout: time.Second}, ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1)}
	return service, backend
}
func e2ControlAsk(t *testing.T, s *E2ControlService, kind string, extra map[string]any) map[string]any {
	t.Helper()
	request := map[string]any{"format": E2ControlFormat, "type": kind, "eval_run_id": "eval_one", "reservation_ref": "reservation_one"}
	for k, v := range extra {
		request[k] = v
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return s.handle(t.Context(), data)
}
func TestE2ControlClosedRequestCodec(t *testing.T) {
	good := `{"format":"abc2lab.e2-runner-control/v1","type":"ready","eval_run_id":"eval_one","reservation_ref":"reservation_one"}`
	cases := []struct {
		name, wire string
		valid      bool
	}{{"valid", good, true}, {"duplicate", strings.Replace(good, `"type":"ready"`, `"type":"ready","type":"ready"`, 1), false}, {"unknown", strings.Replace(good, `"type":"ready"`, `"type":"ready","extra":1`, 1), false}, {"wrong_type", strings.Replace(good, `"eval_run_id":"eval_one"`, `"eval_run_id":[]`, 1), false}, {"array", "[]", false}, {"trailing", good + " {}", false}, {"version", strings.Replace(good, "/v1", "/v2", 1), false}, {"NaN", strings.Replace(good, `"eval_run_id":"eval_one"`, `"eval_run_id":NaN`, 1), false}, {"oversize", strings.Repeat("x", e2ControlMaxMessage+1), false}, {"bad_utf8", string([]byte{0xff}), false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeE2ControlRequest([]byte(tc.wire))
			if (err == nil) != tc.valid {
				t.Fatalf("요청 경계 불일치: valid=%t", tc.valid)
			}
		})
	}
}
func TestE2ControlBindCloseAndPolling(t *testing.T) {
	s, b := newE2ControlStateFixture(t)
	if got := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"}); got["code"] != "not_ready" {
		t.Fatal("결속 전 종료 수용")
	}
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_one"}); got["type"] != "bind" {
		t.Fatal("결속 실패")
	}
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_one"}); got["type"] != "bind" || b.bindCalls.Load() != 1 {
		t.Fatal("같은 결속의 부작용 재실행")
	}
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_two"}); got["code"] != "bind_conflict" {
		t.Fatal("결속 교체 수용")
	}
	first := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"})
	second := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"})
	if !reflect.DeepEqual(first, second) || first["admission_closed"] != true || b.closeCalls.Load() != 1 {
		t.Fatal("종료 ACK 멱등 실패")
	}
	if got := e2ControlAsk(t, s, "close_status", map[string]any{"close_request_ref": "close_one"}); got["state"] != "closing" || got["manifest_sha256"] != nil || got["checkpoint"] != (*E2ControlCheckpoint)(nil) {
		t.Fatal("게시 전 완료 또는 마감 누출")
	}
	if got := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_other"}); got["code"] != "close_ref_conflict" {
		t.Fatal("종료 참조 교체 수용")
	}
	close(b.gate)
	s.mu.Lock()
	done := s.finalDone
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("종료 결과 대기 실패")
	}
	got := e2ControlAsk(t, s, "close_status", map[string]any{"close_request_ref": "close_one"})
	if got["state"] != "complete" || got["manifest_sha256"] != strings.Repeat("c", 64) || b.finalCalls.Load() != 1 {
		t.Fatal("마감 결과 결속 실패")
	}
	if again := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"}); !reflect.DeepEqual(again, first) {
		t.Fatal("완료 뒤 ACK 교체")
	}
}
func TestE2ControlUnknownMutationKeepsReference(t *testing.T) {
	s, b := newE2ControlStateFixture(t)
	b.bindFail = true
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_one"}); got["code"] != "unavailable" {
		t.Fatal("결속 불명 수용")
	}
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_two"}); got["code"] != "bind_conflict" {
		t.Fatal("불명 결속 교체")
	}
	b.mu.Lock()
	b.bindFail = false
	b.mu.Unlock()
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_one"}); got["type"] != "bind" || b.bindCalls.Load() != 2 {
		t.Fatal("같은 결속 재확인 실패")
	}
	b.mu.Lock()
	b.closeFail = true
	b.mu.Unlock()
	if got := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"}); got["code"] != "unavailable" {
		t.Fatal("종료 불명 수용")
	}
	if got := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_other"}); got["code"] != "close_ref_conflict" {
		t.Fatal("불명 종료 참조 교체")
	}
	b.mu.Lock()
	b.closeFail = false
	b.final = E2ControlFinal{State: "incomplete", Reason: "audit_incomplete", ExecutionRef: "execution_one"}
	b.mu.Unlock()
	close(b.gate)
	if got := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"}); got["admission_closed"] != true || b.closeCalls.Load() != 2 {
		t.Fatal("같은 종료 재확인 실패")
	}
	s.mu.Lock()
	done := s.finalDone
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("불명 종료 완료 실패")
	}
	got := e2ControlAsk(t, s, "close_status", map[string]any{"close_request_ref": "close_one"})
	if got["state"] != "incomplete" || got["checkpoint"] != (*E2ControlCheckpoint)(nil) {
		t.Fatal("미완료를 성공 head로 변환")
	}
	bytes, _ := json.Marshal(got)
	if strings.Contains(string(bytes), "canary") {
		t.Fatal("원문 오류 누출")
	}
}
func TestE2ControlQueryReflectsCurrentState(t *testing.T) {
	s, b := newE2ControlStateFixture(t)
	b.ready = E2ControlReady{State: "starting"}
	if got := e2ControlAsk(t, s, "ready", nil); got["state"] != "starting" || got["config_sha256"] != nil {
		t.Fatal("초기 준비값 누출")
	}
	b.mu.Lock()
	b.ready = e2ControlTestReady()
	b.mu.Unlock()
	if got := e2ControlAsk(t, s, "ready", nil); got["state"] != "service_ready" {
		t.Fatal("준비 조회를 과거 답으로 고정")
	}
	b.mu.Lock()
	b.ready.Reason = "private-canary"
	b.mu.Unlock()
	if got := e2ControlAsk(t, s, "ready", nil); got["code"] != "unavailable" {
		t.Fatal("미등록 사유 수용")
	}
	if got := e2ControlAsk(t, s, "head", map[string]any{"eval_run_id": "other"}); got["code"] != "other_run" {
		t.Fatal("다른 회차 수용")
	}
}

func TestE2ControlRejectsFinalizationReplacement(t *testing.T) {
	s, b := newE2ControlStateFixture(t)
	if got := e2ControlAsk(t, s, "bind", map[string]any{"reset_generation": "reset_one"}); got["type"] != "bind" {
		t.Fatal("결속 실패")
	}
	b.mu.Lock()
	b.final.ExecutionRef = "execution_other"
	b.mu.Unlock()
	close(b.gate)
	if got := e2ControlAsk(t, s, "close", map[string]any{"close_request_ref": "close_one"}); got["admission_closed"] != true {
		t.Fatal("admission 종료 실패")
	}
	s.mu.Lock()
	done := s.finalDone
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("종료 결과 대기 실패")
	}
	got := e2ControlAsk(t, s, "close_status", map[string]any{"close_request_ref": "close_one"})
	if got["state"] != "incomplete" || got["reason"] != "publish_failed" || got["execution_ref"] != "execution_one" || got["checkpoint"] != (*E2ControlCheckpoint)(nil) {
		t.Fatal("다른 실행의 완료 근거 수용")
	}
}

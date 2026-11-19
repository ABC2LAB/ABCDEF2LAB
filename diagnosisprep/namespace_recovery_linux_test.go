//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

const (
	firstContainerNamespace = "pid:[4026531001]@5000"
	// docker restart는 같은 namespace 번호를 다시 준다(실측). 시작 tick만 다르다.
	secondContainerNamespace = "pid:[4026531001]@7000"
)

// releaseLease는 PID 1 서비스 프로세스가 끝나 커널이 잠금을 푸는 것만 모의한다.
func releaseLease(t *testing.T, s *Service) {
	t.Helper()
	if err := s.journal.close(); err != nil {
		t.Fatal(err)
	}
}

func unknownNamespaceFixture(t *testing.T, namespace string) (*Service, *fakeBackend, string, string, Job) {
	t.Helper()
	boot := executionBootID()
	if boot == "" {
		t.Skip("boot ID를 읽을 수 없는 환경")
	}
	b := &fakeBackend{inspection: fixtureInspection()}
	b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
	}
	root := privateDir(t)
	s, err := newServiceForRuntime(context.Background(), root, b, boot, namespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := create(t, s, "ns-first")
	if start(t, s, first, "ns-first-start").Error != "" {
		t.Fatal("첫 시작 실패")
	}
	awaitExecutionReturned(t, s, first.ID)
	return s, b, root, boot, first
}

func TestContainerRestartRecoveryRequiresNewInitInstance(t *testing.T) {
	s, b, root, boot, first := unknownNamespaceFixture(t, firstContainerNamespace)
	if got := s.jobs[first.ID].ExecutionInitInstance; got != firstContainerNamespace {
		t.Fatalf("실행 namespace 미기록: %q", got)
	}
	before := status(t, s, first.ID)
	if s.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	// 미마감 기록이 있는 PID 1 서비스는 종료 뒤에도 잠금을 프로세스 종료까지 쥔다.
	if _, err := New(context.Background(), root, b); err == nil {
		t.Fatal("PID 1 서비스가 미마감 상태로 잠금을 놓음")
	}
	releaseLease(t, s)

	// 같은 init 인스턴스(그 init이 아직 살아 있을 수 있음)는 증거가 아니다.
	same, err := newServiceForRuntime(context.Background(), root, b, boot, firstContainerNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := create(t, same, "ns-next")
	if start(t, same, next, "ns-next-start").Error != "busy" {
		t.Fatal("같은 init 인스턴스로 잠금 해제")
	}
	if same.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	releaseLease(t, same)

	// 지금 서비스가 PID 1이 아니면 namespace를 비교하지 않는다.
	notInit, err := newServiceForRuntime(context.Background(), root, b, boot, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if notInit.jobs[first.ID].ResourcesRecovered || start(t, notInit, next, "ns-next-start").Error != "busy" {
		t.Fatal("PID 1이 아닌 서비스가 namespace 증거로 복구")
	}
	if notInit.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}

	// 다른 boot 값은 boot 경로 몫이다. 여기서는 같은 boot의 다른 namespace만 시험한다.
	recovered, err := newServiceForRuntime(context.Background(), root, b, boot, secondContainerNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Shutdown(context.Background())
	r := recovered.jobs[first.ID]
	if !r.ExecutionReturned || !r.ResourcesRecovered {
		t.Fatal("다른 컨테이너 namespace에서 복구하지 않음")
	}
	if got := status(t, recovered, first.ID); !reflect.DeepEqual(got, before) {
		t.Fatal("복구가 원래 결과를 바꿈")
	}
	if b.calls.Load() != 1 {
		t.Fatal("복구가 실행을 재생")
	}
	if start(t, recovered, next, "ns-next-start").Error != "" {
		t.Fatal("확인된 컨테이너 종료 뒤 잠금 유지")
	}
}

func TestNamespaceRecoveryNeedsRecordedInitNamespace(t *testing.T) {
	// PID 1이 아닌 서비스가 시작한 실행은 namespace 증거가 없다.
	s, b, root, boot, first := unknownNamespaceFixture(t, "")
	if s.jobs[first.ID].ExecutionInitInstance != "" {
		t.Fatal("PID 1이 아닌 서비스가 namespace 기록")
	}
	if s.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	later, err := newServiceForRuntime(context.Background(), root, b, boot, secondContainerNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer later.Shutdown(context.Background())
	next := create(t, later, "ns-next")
	if later.jobs[first.ID].ResourcesRecovered || start(t, later, next, "ns-next-start").Error != "busy" {
		t.Fatal("기록 없는 namespace로 복구")
	}
}

func TestInitInstanceEvidenceIsValidatedAndImmutable(t *testing.T) {
	s, _, _, _, first := unknownNamespaceFixture(t, firstContainerNamespace)
	defer func() {
		_ = s.Shutdown(context.Background())
		releaseLease(t, s)
	}()
	old := s.jobs[first.ID]
	// 복구 표시만 더하는 변경은 허용되므로, 같은 변경에 namespace를 바꾸면 그 칸만 거부 사유가 된다.
	recovered := cloneReceipt(old)
	recovered.ResourcesRecovered = true
	if !validReceiptChange(old, recovered) {
		t.Fatal("복구 표시 변경 거부")
	}
	recovered.ExecutionInitInstance = secondContainerNamespace
	if validReceiptChange(old, recovered) {
		t.Fatal("시작 뒤 namespace 변경 허용")
	}
	for name, mutate := range map[string]func(*receipt){
		"형식 밖":    func(r *receipt) { r.ExecutionInitInstance = "net:[4026531001]@5000" },
		"boot 없음": func(r *receipt) { r.ExecutionBootID = "" },
	} {
		r := cloneReceipt(old)
		mutate(&r)
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := replayPreparationReceipts(context.Background(), []json.RawMessage{raw}, 1); err == nil {
			t.Fatalf("%s namespace 기록 허용", name)
		}
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayPreparationReceipts(context.Background(), []json.RawMessage{raw}, 1); err != nil {
		t.Fatal("정상 namespace 기록 거부")
	}
	if _, err := newServiceForRuntime(context.Background(), privateDir(t), &fakeBackend{inspection: fixtureInspection()}, executionBootID(), "pid:[4026531001]", nil); err == nil {
		t.Fatal("형식 밖 서비스 namespace 허용")
	}
}

func TestServiceInitInstanceOnlyForInit(t *testing.T) {
	// go test 프로세스는 PID 1이 아니다. 실제 컨테이너 init 판정은 제품 수용에서 본다.
	if serviceInitInstance() != "" {
		t.Fatal("init이 아닌 프로세스가 namespace 증거를 냄")
	}
}

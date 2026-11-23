//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// releaseBackend는 해제 근거(엔진 활동·탐색 경계·자원 마감 확인)를 시험에서 정한다.
type releaseBackend struct {
	*fakeBackend
	mu         sync.Mutex
	activity   map[string]time.Time
	closeErr   error
	clearErr   error
	verifyErr  error
	openJob    string // 열린 탐색 경계 세션의 작업(빈 값 = 없음)
	onClose    func() // 경계 닫기 중에 일어나는 일(엔진 활동 등)
	closeCalls int
	clearCalls int
	verifies   int
}

func (b *releaseBackend) LastActivity(e Execution) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.activity[e.Job.ID]
	return at, ok
}
func (b *releaseBackend) CloseExploration(_ context.Context, e Execution) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeCalls++
	if b.onClose != nil {
		b.onClose()
	}
	if b.closeErr != nil {
		return false, b.closeErr
	}
	if b.openJob == "" {
		return false, nil
	}
	if b.openJob != e.Job.ID {
		return false, ErrExplorationBusy
	}
	b.openJob = ""
	return true, nil
}
func (b *releaseBackend) ExplorationClear(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clearCalls++
	if b.clearErr != nil {
		return b.clearErr
	}
	if b.openJob != "" {
		return errors.New("열린 세션")
	}
	return nil
}
func (b *releaseBackend) VerifyClosure(context.Context, Execution) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.verifies++
	return b.verifyErr
}
func (b *releaseBackend) set(f func(*releaseBackend)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b)
}

// releaseClock은 해제 근거 시계다. 새 서비스의 완료 시각(실제 시계)보다 앞서도록 과거에서 시작한다.
type releaseClock struct {
	mu sync.Mutex
	t  time.Time
}

func newReleaseClock() *releaseClock { return &releaseClock{t: time.Now().UTC().Add(-3 * time.Hour)} }
func (c *releaseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *releaseClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type releaseFixture struct {
	s       *Service
	b       *releaseBackend
	clock   *releaseClock
	root    string
	boot    string
	job     Job
	unblock chan struct{}
}

func newReleaseService(t *testing.T, root string, b *releaseBackend, boot, init string, clock *releaseClock) *Service {
	t.Helper()
	s, err := newServiceForRuntime(context.Background(), root, b, boot, init, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.restartGrace = time.Millisecond
	if clock != nil {
		s.releaseClock = clock.Now
	}
	s.mu.Unlock()
	return s
}

// hungFixture는 실행이 시작 확인을 게시한 뒤 취소를 무시하고 매달린 작업을 만든다. 운영자는 '종료'를 눌렀다(stopping).
func hungFixture(t *testing.T) *releaseFixture {
	t.Helper()
	boot := executionBootID()
	if boot == "" {
		t.Skip("boot ID를 읽을 수 없는 환경")
	}
	f := &releaseFixture{clock: newReleaseClock(), root: privateDir(t), boot: boot, unblock: make(chan struct{})}
	published := make(chan struct{})
	f.b = &releaseBackend{fakeBackend: &fakeBackend{inspection: fixtureInspection()}, activity: map[string]time.Time{}}
	f.b.run = func(ctx context.Context, e Execution, p Publish) error {
		if f.b.calls.Load() > 1 {
			return completeRun(ctx, e, p) // 해제 뒤 새 시작은 정상 실행이다
		}
		if err := p(Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true}); err != nil {
			return err
		}
		close(published)
		<-f.unblock // 취소를 무시한다
		return nil
	}
	t.Cleanup(func() {
		select {
		case <-f.unblock:
		default:
			close(f.unblock)
		}
	})
	f.s = newReleaseService(t, f.root, f.b, boot, firstContainerNamespace, f.clock)
	f.job = create(t, f.s, "hung")
	if start(t, f.s, f.job, "hung-start").Error != "" {
		t.Fatal("시작 실패")
	}
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("시작 게시를 기다리지 못함")
	}
	r := request(Stop)
	r.JobID = f.job.ID
	r.ExpectedRevision = status(t, f.s, f.job.ID).Revision
	if res := call(t, f.s, r); res.Error != "" || res.Job.State != Stopping {
		t.Fatalf("종료 요청 실패: %#v", res)
	}
	f.job = status(t, f.s, f.job.ID)
	return f
}

// unknownFixture는 실행이 반환했지만 자원 마감 확인(VerifyClosure)이 계속 실패하는 결과 불명 작업을 만든다.
func unknownFixture(t *testing.T) *releaseFixture {
	t.Helper()
	boot := executionBootID()
	if boot == "" {
		t.Skip("boot ID를 읽을 수 없는 환경")
	}
	f := &releaseFixture{clock: newReleaseClock(), root: privateDir(t), boot: boot}
	f.b = &releaseBackend{fakeBackend: &fakeBackend{inspection: fixtureInspection()}, activity: map[string]time.Time{}, verifyErr: errors.New("자원 마감 확인 실패")}
	f.b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
	}
	f.s = newReleaseService(t, f.root, f.b, boot, firstContainerNamespace, f.clock)
	f.job = create(t, f.s, "unknown")
	if start(t, f.s, f.job, "unknown-start").Error != "" {
		t.Fatal("시작 실패")
	}
	awaitExecutionReturned(t, f.s, f.job.ID)
	f.job = status(t, f.s, f.job.ID)
	return f
}

func releaseStatus(t *testing.T, s Endpoint) ReleaseView {
	t.Helper()
	res := call(t, s, request(ReleaseStatus))
	if res.Error != "" || res.Release == nil {
		t.Fatalf("해제 상태 조회 실패: %#v", res)
	}
	return *res.Release
}

func release(t *testing.T, s Endpoint, j Job, reason string) Response {
	t.Helper()
	r := request(Release)
	r.RequestID = "release-" + j.ID[len(j.ID)-6:]
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	r.Release = &ReleaseRequest{Reason: reason}
	return call(t, s, r)
}

// receiptOf는 실행 goroutine과 겹치지 않게 잠금 아래에서 영수증 사본을 읽는다.
func receiptOf(s *Service, id string) receipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneReceipt(s.jobs[id])
}

func restartRequested(s *Service) bool {
	select {
	case <-s.RestartRequested():
		return true
	case <-time.After(200 * time.Millisecond):
		return false
	}
}

// shutdownLikeExit는 준비 서비스가 정상 종료한 뒤 프로세스가 끝나 커널이 저널 잠금을 푸는 것까지 모의한다.
func shutdownLikeExit(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = s.Shutdown(ctx) // 매달린 실행이면 시간 초과가 정상이다
	releaseLease(t, s)
}

func assertReleased(t *testing.T, f *releaseFixture, next *Service) {
	t.Helper()
	rec := receiptOf(next, f.job.ID).OperatorRelease
	if rec == nil || rec.Pending() || rec.NewInit != secondContainerNamespace || rec.PreviousInit != firstContainerNamespace || rec.BoundaryCheckedAt == nil {
		t.Fatalf("해제 완료 기록이 없음: %#v", rec)
	}
	if r := receiptOf(next, f.job.ID); !r.ExecutionReturned || !r.ResourcesRecovered || r.Job.State != Unknown {
		t.Fatalf("결과 불명 기록을 유지하며 잠금만 풀어야 함: %#v", r.Job)
	}
	if f.b.calls.Load() != 1 {
		t.Fatalf("해제가 이전 실행을 다시 실행함: %d", f.b.calls.Load())
	}
	other := create(t, next, "after-release")
	if res := start(t, next, other, "after-release-start"); res.Error != "" {
		t.Fatalf("해제 뒤 새 시작 실패: %s", res.Error)
	}
}

func TestReleaseHungExecutionAfterQuietRestartsAndCompletes(t *testing.T) {
	f := hungFixture(t)
	other := create(t, f.s, "blocked")
	if start(t, f.s, other, "blocked-start").Error != "busy" {
		t.Fatal("매달린 실행이 새 시작을 막지 않음")
	}
	f.clock.Advance(DefaultReleaseQuiet)
	view := releaseStatus(t, f.s)
	if !view.Eligible || view.JobID != f.job.ID || view.JobState != Stopping || view.LastActivityAt != nil {
		t.Fatalf("기준 시간이 지났는데 해제 불가: %#v", view)
	}
	res := release(t, f.s, f.job, "실행이 응답하지 않아 해제")
	if res.Error != "" || res.Release.Refusal != ReleasePending || res.Release.Record == nil {
		t.Fatalf("해제 실패: %#v", res)
	}
	rec := res.Release.Record
	if rec.ExplorationSession != ExplorationSessionNone {
		t.Fatalf("열린 세션이 없었는데 경계 세션 값이 %q", rec.ExplorationSession)
	}
	if rec.Principal != "owner" || rec.Reason != "실행이 응답하지 않아 해제" || rec.JobState != Stopping || rec.PreviousInit != firstContainerNamespace ||
		rec.QuietSeconds != int64(DefaultReleaseQuiet/time.Second) || rec.ReceiptUpdatedAt.IsZero() || !rec.Pending() {
		t.Fatalf("감사 기록 칸이 빠짐: %#v", rec)
	}
	if !restartRequested(f.s) {
		t.Fatal("감사 기록 뒤 재시작을 요청하지 않음")
	}
	// 재시작 전에는 잠금을 풀지 않는다.
	if start(t, f.s, other, "blocked-start").Error != "busy" {
		t.Fatal("재시작 전 잠금 해제")
	}
	shutdownLikeExit(t, f.s)
	next := newReleaseService(t, f.root, f.b, f.boot, secondContainerNamespace, nil)
	defer next.Shutdown(context.Background())
	if after := releaseStatus(t, next); after.Refusal != ReleaseNotStuck || after.Record == nil || after.Record.Pending() {
		t.Fatalf("해제 뒤 상태가 기록을 보여 주지 않음: %#v", after)
	}
	assertReleased(t, f, next)
}

func TestReleaseClosureVerificationFailingAfterQuietRestartsAndCompletes(t *testing.T) {
	f := unknownFixture(t)
	other := create(t, f.s, "blocked")
	if start(t, f.s, other, "blocked-start").Error != "busy" {
		t.Fatal("마감 확인 실패가 새 시작을 막지 않음")
	}
	f.b.set(func(b *releaseBackend) { b.activity[f.job.ID] = f.clock.Now().Add(10 * time.Minute) })
	f.clock.Advance(DefaultReleaseQuiet)
	if view := releaseStatus(t, f.s); view.Refusal != ReleaseNotQuiet || view.LastActivityAt == nil {
		t.Fatalf("엔진 활동 뒤 기준 시간이 안 지났는데 해제 가능: %#v", view)
	}
	f.clock.Advance(10 * time.Minute)
	if res := release(t, f.s, f.job, "자원 마감 확인이 계속 실패"); res.Error != "" {
		t.Fatalf("해제 실패: %s", res.Error)
	}
	if !restartRequested(f.s) {
		t.Fatal("재시작 요청 없음")
	}
	shutdownLikeExit(t, f.s)
	next := newReleaseService(t, f.root, f.b, f.boot, secondContainerNamespace, nil)
	defer next.Shutdown(context.Background())
	assertReleased(t, f, next)
	if rec := receiptOf(next, f.job.ID).OperatorRelease; rec.LastActivityAt == nil || rec.JobState != Unknown {
		t.Fatalf("마지막 활동 근거가 감사 기록에 없음: %#v", rec)
	}
}

func TestReleaseRefusedBeforeQuiet(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet - time.Second)
	view := releaseStatus(t, f.s)
	if view.Eligible || view.Refusal != ReleaseNotQuiet || view.EligibleAt == nil || !view.EligibleAt.Equal(view.ReceiptUpdatedAt.Add(DefaultReleaseQuiet)) {
		t.Fatalf("기준 시간 전에 해제 가능으로 보임: %#v", view)
	}
	if res := release(t, f.s, f.job, "너무 이름"); res.Error != "release_"+ReleaseNotQuiet {
		t.Fatalf("기준 시간 전 해제가 거절되지 않음: %#v", res)
	}
	if receiptOf(f.s, f.job.ID).OperatorRelease != nil || restartRequested(f.s) {
		t.Fatal("거절된 해제가 기록·재시작을 남김")
	}
	if err := f.s.SetReleaseQuiet(MinReleaseQuiet - time.Second); err == nil {
		t.Fatal("하한보다 짧은 기준을 받음")
	}
	if err := f.s.SetReleaseQuiet(MinReleaseQuiet); err != nil {
		t.Fatal(err)
	}
	if view := releaseStatus(t, f.s); view.QuietSeconds != int64(MinReleaseQuiet/time.Second) {
		t.Fatalf("기준 설정이 반영되지 않음: %d", view.QuietSeconds)
	}
}

func TestReleaseRefusedWhileOtherScanRuns(t *testing.T) {
	f := hungFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	// 막는 작업이 아닌 다른 작업을 풀려고 하면 거절한다.
	idle := create(t, f.s, "idle")
	if res := release(t, f.s, idle, "다른 작업"); res.Error != "release_"+ReleaseOtherActive {
		t.Fatalf("다른 점검이 막는 중인데 거절하지 않음: %#v", res)
	}
	// 다른 작업의 탐색 경계 세션이 열려 있으면 거절한다.
	f.b.set(func(b *releaseBackend) { b.openJob = idle.ID })
	if res := release(t, f.s, f.job, "탐색 중"); res.Error != "release_"+ReleaseOtherActive {
		t.Fatalf("다른 작업이 탐색 중인데 거절하지 않음: %#v", res)
	}
	if receiptOf(f.s, f.job.ID).OperatorRelease != nil || restartRequested(f.s) {
		t.Fatal("거절된 해제가 기록·재시작을 남김")
	}
}

func TestReleaseRefusedWhileJobStillRunning(t *testing.T) {
	boot := executionBootID()
	if boot == "" {
		t.Skip("boot ID를 읽을 수 없는 환경")
	}
	clock := newReleaseClock()
	unblock := make(chan struct{})
	defer close(unblock)
	published := make(chan struct{})
	b := &releaseBackend{fakeBackend: &fakeBackend{inspection: fixtureInspection()}, activity: map[string]time.Time{}}
	b.run = func(ctx context.Context, _ Execution, p Publish) error {
		if err := p(Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true}); err != nil {
			return err
		}
		close(published)
		<-unblock
		return nil
	}
	s := newReleaseService(t, privateDir(t), b, boot, firstContainerNamespace, clock)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	job := create(t, s, "running")
	if start(t, s, job, "running-start").Error != "" {
		t.Fatal("시작 실패")
	}
	<-published
	clock.Advance(DefaultReleaseQuiet)
	job = status(t, s, job.ID)
	if res := release(t, s, job, "실행 중"); res.Error != "release_"+ReleaseJobRunning {
		t.Fatalf("실행 중인 작업을 해제하려 함: %#v", res)
	}
}

func TestReleaseRequiresOwningPrincipal(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	r := request(Release)
	r.Principal = "someone-else"
	r.JobID = f.job.ID
	r.ExpectedRevision = f.job.Revision
	r.Release = &ReleaseRequest{Reason: "남의 작업"}
	if res := call(t, f.s, r); res.Error != "not_found" {
		t.Fatalf("다른 주체의 해제를 받음: %#v", res)
	}
	if receiptOf(f.s, f.job.ID).OperatorRelease != nil {
		t.Fatal("다른 주체 요청이 기록을 남김")
	}
}

func TestReleaseRefusedWhenExplorationSessionNotClosed(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	f.b.set(func(b *releaseBackend) { b.openJob = f.job.ID; b.closeErr = errors.New("닫힘 확인 실패") })
	if res := release(t, f.s, f.job, "경계 확인 불가"); res.Error != "release_"+ReleaseExplorationOpen {
		t.Fatalf("탐색 경계 세션이 안 닫혔는데 해제: %#v", res)
	}
	if receiptOf(f.s, f.job.ID).OperatorRelease != nil || restartRequested(f.s) {
		t.Fatal("경계 확인 실패가 기록·재시작을 남김")
	}
	// 세션을 닫을 수 있으면 닫고 그 사실을 감사 기록에 남긴다.
	f.b.set(func(b *releaseBackend) { b.closeErr = nil })
	res := release(t, f.s, f.job, "경계 세션 닫고 해제")
	if res.Error != "" || res.Release.Record.ExplorationSession != ExplorationSessionClosed {
		t.Fatalf("경계 세션 닫기가 감사 기록에 없음: %#v", res)
	}
}

func TestReleaseCompletionWaitsForBoundaryAndNewInit(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	if res := release(t, f.s, f.job, "재시작 확인"); res.Error != "" {
		t.Fatal(res.Error)
	}
	shutdownLikeExit(t, f.s)
	// 같은 init 인스턴스(컨테이너가 실제로 재시작되지 않음)는 증거가 아니다.
	same := newReleaseService(t, f.root, f.b, f.boot, firstContainerNamespace, nil)
	if rec := receiptOf(same, f.job.ID).OperatorRelease; rec == nil || !rec.Pending() {
		t.Fatal("같은 init에서 해제를 마침")
	}
	if view := releaseStatus(t, same); view.Refusal != ReleasePending || view.JobID != f.job.ID {
		t.Fatalf("대기 중 해제가 보이지 않음: %#v", view)
	}
	shutdownLikeExit(t, same)
	// 새 init이어도 탐색 경계 세션 없음을 확인하지 못하면 잠금을 유지한다.
	f.b.set(func(b *releaseBackend) { b.clearErr = errors.New("경계 응답 없음") })
	next := newReleaseService(t, f.root, f.b, f.boot, secondContainerNamespace, nil)
	defer next.Shutdown(context.Background())
	other := create(t, next, "waiting")
	if start(t, next, other, "waiting-start").Error != "busy" {
		t.Fatal("경계 확인 전 잠금 해제")
	}
	if rec := receiptOf(next, f.job.ID).OperatorRelease; !rec.Pending() {
		t.Fatal("경계 확인 전 해제 완료 기록")
	}
	// 경계가 확인되면 다음 조회에서 마친다.
	f.b.set(func(b *releaseBackend) { b.clearErr = nil })
	if view := releaseStatus(t, next); view.Refusal != ReleaseNotStuck || view.Record == nil || view.Record.Pending() {
		t.Fatalf("경계 확인 뒤 해제를 마치지 않음: %#v", view)
	}
	if start(t, next, other, "waiting-start").Error != "" {
		t.Fatal("해제 완료 뒤 새 시작 실패")
	}
}

func TestReleaseUnsupportedWithoutInitEvidence(t *testing.T) {
	boot := executionBootID()
	if boot == "" {
		t.Skip("boot ID를 읽을 수 없는 환경")
	}
	clock := newReleaseClock()
	b := &releaseBackend{fakeBackend: &fakeBackend{inspection: fixtureInspection()}, activity: map[string]time.Time{}, verifyErr: errors.New("실패")}
	b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
	}
	s := newReleaseService(t, privateDir(t), b, boot, "", clock)
	defer s.Shutdown(context.Background())
	job := create(t, s, "no-init")
	if start(t, s, job, "no-init-start").Error != "" {
		t.Fatal("시작 실패")
	}
	awaitExecutionReturned(t, s, job.ID)
	clock.Advance(DefaultReleaseQuiet)
	if view := releaseStatus(t, s); view.Refusal != ReleaseUnsupported {
		t.Fatalf("재기동 증거가 없는 설치에서 해제 가능으로 보임: %#v", view)
	}
}

func TestReleaseRecordChangesAreRestricted(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	if res := release(t, f.s, f.job, "전이 시험"); res.Error != "" {
		t.Fatal(res.Error)
	}
	pending := cloneReceipt(receiptOf(f.s, f.job.ID))
	base := cloneReceipt(pending)
	base.OperatorRelease = nil
	completed := cloneReceipt(pending)
	now := time.Now().UTC()
	completed.OperatorRelease.NewInit, completed.OperatorRelease.CompletedAt, completed.OperatorRelease.BoundaryCheckedAt = secondContainerNamespace, &now, &now
	completed.OperatorRelease.CompletedBoot = completed.ExecutionBootID
	completed.ExecutionReturned, completed.ResourcesRecovered = true, true
	if !validReceiptChange(base, pending) || !validReceiptChange(pending, completed) || !validStoredRelease(completed) {
		t.Fatal("정상 전이를 거부")
	}
	removed := cloneReceipt(pending)
	removed.OperatorRelease = nil
	reworded := cloneReceipt(pending)
	reworded.OperatorRelease.Reason = "바꾼 사유"
	unrecovered := cloneReceipt(completed)
	unrecovered.ResourcesRecovered = false
	sameInit := cloneReceipt(completed)
	sameInit.OperatorRelease.NewInit = firstContainerNamespace
	resolved := cloneReceipt(base)
	resolved.ExecutionReturned, resolved.ResourcesRecovered = true, true
	onResolved := cloneReceipt(resolved)
	onResolved.OperatorRelease = pending.OperatorRelease
	for name, c := range map[string][2]receipt{
		"기록 삭제":       {pending, removed},
		"사유 변경":       {pending, reworded},
		"회수 증거 없는 완료": {pending, unrecovered},
		"완료 기록 변경":    {completed, reworded},
		"풀린 실행에 기록":   {resolved, onResolved},
	} {
		if validReceiptChange(c[0], c[1]) {
			t.Errorf("%s 전이를 받음", name)
		}
	}
	if validStoredRelease(sameInit) {
		t.Error("같은 boot·같은 init으로 마친 기록을 받음")
	}
	// 재부팅 뒤에는 init 문자열이 우연히 같아도 boot가 달라 받는다(다음 기동이 읽지 못하는 기록을 남기지 않음).
	rebooted := cloneReceipt(sameInit)
	rebooted.OperatorRelease.CompletedBoot = otherBoot(t, sameInit.ExecutionBootID)
	if !validStoredRelease(rebooted) {
		t.Error("재부팅 뒤 같은 init 문자열의 완료 기록을 거부")
	}
}

func otherBoot(t *testing.T, boot string) string {
	t.Helper()
	other := "00000000-0000-4000-8000-000000000000"
	if other == boot {
		other = "00000000-0000-4000-8000-000000000001"
	}
	return other
}

// 해제를 기록한 프로세스의 시계가 다음 프로세스보다 앞서 있어도(시계 보정) 완료 기록이 다음 기동에서 읽힌다.
func TestReleaseCompletionSurvivesClockStepBack(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(6*time.Hour + DefaultReleaseQuiet) // 실제 시계보다 3시간 앞
	if res := release(t, f.s, f.job, "시계가 앞선 해제"); res.Error != "" {
		t.Fatal(res.Error)
	}
	shutdownLikeExit(t, f.s)
	next := newReleaseService(t, f.root, f.b, f.boot, secondContainerNamespace, nil)
	rec := receiptOf(next, f.job.ID).OperatorRelease
	if rec.Pending() || rec.CompletedAt.Before(rec.RequestedAt) {
		t.Fatalf("완료 시각이 기록 시각보다 앞섬: %#v", rec)
	}
	shutdownLikeExit(t, next)
	third, err := newServiceForRuntime(context.Background(), f.root, f.b, f.boot, secondContainerNamespace, nil)
	if err != nil {
		t.Fatalf("다음 기동이 해제 완료 기록을 읽지 못함: %v", err)
	}
	defer third.Shutdown(context.Background())
}

// 재부팅 뒤 init 문자열이 우연히 같아도 boot가 달라 해제를 마치고, 그 기록을 다음 기동이 읽는다.
func TestReleaseCompletionAfterRebootWithSameInitString(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	if res := release(t, f.s, f.job, "재부팅"); res.Error != "" {
		t.Fatal(res.Error)
	}
	shutdownLikeExit(t, f.s)
	boot := otherBoot(t, f.boot)
	next := newReleaseService(t, f.root, f.b, boot, firstContainerNamespace, nil)
	rec := receiptOf(next, f.job.ID).OperatorRelease
	if rec.Pending() || rec.NewInit != rec.PreviousInit || rec.CompletedBoot != boot {
		t.Fatalf("재부팅 뒤 해제를 마치지 않음: %#v", rec)
	}
	shutdownLikeExit(t, next)
	third, err := newServiceForRuntime(context.Background(), f.root, f.b, boot, firstContainerNamespace, nil)
	if err != nil {
		t.Fatalf("다음 기동이 재부팅 뒤 완료 기록을 읽지 못함: %v", err)
	}
	defer third.Shutdown(context.Background())
}

// 경계를 닫는 동안 엔진 활동이 다시 생기면 기록·재시작 없이 거절한다.
func TestReleaseRefusedWhenActivityResumesDuringBoundaryClose(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	f.b.set(func(b *releaseBackend) {
		b.onClose = func() { b.activity[f.job.ID] = f.clock.Now() } // 잠금 밖에서 엔진이 다시 움직임
	})
	if res := release(t, f.s, f.job, "닫는 중 활동"); res.Error != "release_"+ReleaseNotQuiet {
		t.Fatalf("닫는 동안 생긴 활동을 무시함: %#v", res)
	}
	if receiptOf(f.s, f.job.ID).OperatorRelease != nil || restartRequested(f.s) {
		t.Fatal("거절된 해제가 기록·재시작을 남김")
	}
}

// 응답을 잃고 같은 작업을 다시 보내면 '이미 기록됨(재시작 대기)'을 돌려준다. 다른 작업이라고 하지 않는다.
func TestReleaseResendForSameJobReportsPending(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(DefaultReleaseQuiet)
	if res := release(t, f.s, f.job, "첫 요청"); res.Error != "" {
		t.Fatal(res.Error)
	}
	if res := release(t, f.s, f.job, "다시 보냄"); res.Error != "release_"+ReleasePending {
		t.Fatalf("같은 작업 재전송: %#v", res)
	}
}

func TestReleaseReasonRejectsFormatCharacters(t *testing.T) {
	for _, reason := range []string{"정상\u202e사유", "폭없는\u200b문자", "줄\u2028구분", "탭\t문자", "   "} {
		if validReleaseReason(reason) {
			t.Errorf("서식·제어 문자 사유를 받음: %q", reason)
		}
	}
	if !validReleaseReason("응답 없음, 담당자 확인") {
		t.Error("평범한 사유를 거부")
	}
}

// 작업 A를 해제한 뒤 시계가 뒤로 보정되고 작업 B를 해제해도, 해제 상태의 최근 기록은 B다(벽시계가 아니라 기록 순서).
func TestReleaseLatestRecordFollowsSequenceNotClock(t *testing.T) {
	f := unknownFixture(t)
	f.clock.Advance(6*time.Hour + DefaultReleaseQuiet) // A는 실제 시계보다 3시간 앞선 시각으로 기록된다
	if res := release(t, f.s, f.job, "A 해제"); res.Error != "" {
		t.Fatal(res.Error)
	}
	shutdownLikeExit(t, f.s)
	clock := newReleaseClock() // 다음 프로세스는 3시간 전 시각에서 시작(시계 역행)
	next := newReleaseService(t, f.root, f.b, f.boot, secondContainerNamespace, clock)
	if rec := receiptOf(next, f.job.ID).OperatorRelease; rec.Pending() {
		t.Fatal("A 해제를 마치지 않음")
	}
	b := create(t, next, "job-b")
	if start(t, next, b, "job-b-start").Error != "" {
		t.Fatal("B 시작 실패")
	}
	awaitExecutionReturned(t, next, b.ID)
	b = status(t, next, b.ID)
	clock.Advance(DefaultReleaseQuiet)
	res := release(t, next, b, "B 해제")
	if res.Error != "" || res.Release.Record.JobID != b.ID || res.Release.Record.Sequence != 2 {
		t.Fatalf("B 해제 응답: %#v", res)
	}
	if !res.Release.Record.RequestedAt.Before(receiptOf(next, f.job.ID).OperatorRelease.RequestedAt) {
		t.Fatal("시험 전제(시계 역행)가 성립하지 않음")
	}
	if view := releaseStatus(t, next); view.Record == nil || view.Record.JobID != b.ID {
		t.Fatalf("시계가 뒤로 간 뒤 최근 기록이 B가 아님: %#v", view.Record)
	}
	shutdownLikeExit(t, next)
	third := newReleaseService(t, f.root, f.b, f.boot, "pid:[4026531001]@9000", clock)
	defer third.Shutdown(context.Background())
	if view := releaseStatus(t, third); view.Record == nil || view.Record.JobID != b.ID || view.Record.Pending() {
		t.Fatalf("재접속 뒤 최근 기록이 B 완료가 아님: %#v", view.Record)
	}
}

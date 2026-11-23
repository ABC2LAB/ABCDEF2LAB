//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"log"
	"reflect"
	"sort"
	"time"
)

// restartResponseGrace는 해제 응답을 API에 돌려줄 짧은 여유다. 그 뒤 재시작 요청을 알린다.
// 응답을 잃어도 감사 기록은 이미 저널에 있으므로 화면은 재접속 뒤 release_status로 결과를 안다.
const restartResponseGrace = time.Second

// SetReleaseQuiet은 활동 없음 기준을 정한다. 기본보다 짧게 하면 정상 점검을 멈춘 것으로 볼 수 있어 하한 아래는 거부한다.
func (s *Service) SetReleaseQuiet(d time.Duration) error {
	if d < MinReleaseQuiet || d%time.Second != 0 {
		return ErrProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quiet = d
	return nil
}

// RestartRequested는 운영자 해제가 감사 기록을 남긴 뒤 닫힌다. 실행 파일은 이 신호로 Shutdown하고 프로세스를 끝낸다.
// 컨테이너 재시작 정책이 준비 서비스를 다시 띄운다. 이 서비스는 Docker를 다루지 않는다.
func (s *Service) RestartRequested() <-chan struct{} { return s.restart }

func (s *Service) requestRestartLocked() {
	s.restartOnce.Do(func() {
		time.AfterFunc(s.restartGrace, func() { close(s.restart) })
	})
}

// blocksStart는 새 시작을 막는 작업인지 본다(Start의 busy 판정과 같다). 상태 게시와 실제 backend 반환은 별개라
// 종료와 drain(또는 자원 회수) 증거를 모두 요구한다.
func blocksStart(r receipt) bool {
	return active(r.Job.State) || r.StartKey != "" && (!r.ExecutionReturned || !r.Job.DrainComplete && !r.ResourcesRecovered)
}

func (s *Service) execution(r receipt) Execution {
	return Execution{Principal: r.Principal, Job: r.Job, Inspection: r.Inspection.runtime()}
}

// releaseEvidence는 해제를 기록한 뒤 이전 실행이 끝났다는 #559 증거가 지금 있는지 본다.
// 같은 boot에서는 다른 init 인스턴스가 배타 저널 잠금을 얻었어야 하고(이 서비스가 지금 잠금을 쥔 채 동작 중),
// 다른 boot면 이전 커널의 실행이 모두 끝났다. 새 init을 기록해야 하므로 지금 서비스가 PID 1일 때만 쓴다.
func (s *Service) releaseEvidence(r receipt) bool {
	if s.initInstance == "" || !bootIDPattern.MatchString(s.boot) || r.ExecutionBootID == "" || r.OperatorRelease == nil {
		return false
	}
	if r.ExecutionBootID != s.boot {
		return true
	}
	return s.initInstance != r.ExecutionInitInstance && s.initInstance != r.OperatorRelease.PreviousInit
}

// completeReleasesLocked는 기다리는 해제를 마친다. 새 init 증거와 탐색 경계 세션 없음이 모두 확인될 때만
// 실행 반환·자원 회수 증거를 남겨 새 시작 잠금을 푼다. 결과·예약은 바꾸지 않는다. 확인이 안 되면 다음 호출에서 다시 본다.
func (s *Service) completeReleasesLocked(ctx context.Context) {
	if s.failed || s.released {
		return
	}
	ids := make([]string, 0)
	for id, r := range s.jobs {
		if r.OperatorRelease != nil && r.OperatorRelease.Pending() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := s.jobs[id]
		if !s.releaseEvidence(r) {
			continue
		}
		if boundary, ok := s.backend.(ExplorationBoundary); ok {
			if err := boundary.ExplorationClear(ctx); err != nil {
				log.Print("진단 준비: 탐색 경계에 열린 세션이 없음을 확인하지 못해 해제를 보류합니다")
				continue
			}
		}
		// 해제를 기록한 프로세스와 지금 프로세스는 벽시계가 다를 수 있다(시계 보정). 완료 시각이 기록 시각보다 앞서지 않게 둔다.
		now := s.releaseClock().UTC()
		if now.Before(r.OperatorRelease.RequestedAt) {
			now = r.OperatorRelease.RequestedAt
		}
		next := cloneReceipt(r)
		next.ExecutionReturned, next.ResourcesRecovered = true, true
		next.OperatorRelease.NewInit = s.initInstance
		next.OperatorRelease.CompletedBoot = s.boot
		next.OperatorRelease.CompletedAt = &now
		checked := now
		next.OperatorRelease.BoundaryCheckedAt = &checked
		// 저장은 재생 검사를 거치지 않는다. 다음 기동에서 읽지 못할 기록을 남기지 않도록 같은 검사를 먼저 돈다.
		if !validReceiptChange(r, next) || !validStoredRelease(next) {
			log.Print("진단 준비: 해제 완료 기록이 검사를 통과하지 못해 해제를 보류합니다")
			continue
		}
		if s.save(next) != nil {
			log.Print("진단 준비: 해제 완료 기록을 저장하지 못했습니다")
			return
		}
		log.Print("진단 준비: 새 init과 탐색 경계 세션 없음을 확인해 운영자 해제를 마쳤습니다")
	}
}

// latestReleaseLocked는 기록 순서(Sequence)가 가장 큰 해제 기록이다. 벽시계(RequestedAt)로 고르지 않는다.
func (s *Service) latestReleaseLocked() *ReleaseRecord {
	var latest *ReleaseRecord
	for _, r := range s.jobs {
		if r.OperatorRelease == nil {
			continue
		}
		if latest == nil || r.OperatorRelease.Sequence > latest.Sequence {
			latest = cloneReceipt(r).OperatorRelease
		}
	}
	return latest
}

func (s *Service) nextReleaseSequenceLocked() uint64 {
	var top uint64
	for _, r := range s.jobs {
		if r.OperatorRelease != nil && r.OperatorRelease.Sequence > top {
			top = r.OperatorRelease.Sequence
		}
	}
	return top + 1
}

// releaseViewLocked는 지금 해제할 수 있는지와 그 근거를 만든다. 상태를 바꾸지 않는다.
func (s *Service) releaseViewLocked() ReleaseView {
	now := s.releaseClock().UTC()
	view := ReleaseView{QuietSeconds: int64(s.quiet / time.Second), Now: now, Record: s.latestReleaseLocked()}
	var blockers []receipt
	for _, r := range s.jobs {
		if r.OperatorRelease != nil && r.OperatorRelease.Pending() {
			view.JobID, view.JobRevision, view.JobState, view.Refusal = r.Job.ID, r.Job.Revision, r.Job.State, ReleasePending
			return view
		}
		if blocksStart(r) {
			blockers = append(blockers, r)
		}
	}
	if len(blockers) == 0 {
		view.Refusal = ReleaseNotStuck
		return view
	}
	sort.Slice(blockers, func(i, j int) bool { return blockers[i].Job.ID < blockers[j].Job.ID })
	b := blockers[0]
	view.JobID, view.JobRevision, view.JobState = b.Job.ID, b.Job.Revision, b.Job.State
	switch {
	case len(blockers) > 1:
		view.Refusal = ReleaseOtherActive
		return view
	case b.Job.State != Unknown && b.Job.State != Stopping:
		view.Refusal = ReleaseJobRunning
		return view
	case s.initInstance == "" || b.ExecutionInitInstance == "" || b.ExecutionBootID == "":
		// 재기동 증거(#559)를 쓸 수 없는 실행·서비스는 재시작해도 잠금이 안 풀린다.
		view.Refusal = ReleaseUnsupported
		return view
	}
	reporter, ok := s.backend.(ActivityReporter)
	if !ok {
		view.Refusal = ReleaseUnsupported
		return view
	}
	updated := s.updatedAt[b.Job.ID].UTC()
	view.ReceiptUpdatedAt = &updated
	latest := updated
	if at, ok := reporter.LastActivity(s.execution(b)); ok {
		at = at.UTC()
		view.LastActivityAt = &at
		if at.After(latest) {
			latest = at
		}
	}
	eligibleAt := latest.Add(s.quiet)
	view.EligibleAt = &eligibleAt
	if now.Before(eligibleAt) {
		view.Refusal = ReleaseNotQuiet
		return view
	}
	view.Eligible = true
	return view
}

// releaseLocked는 해제를 기록하고 재시작을 요청한다. 순서: 근거 확인 → 탐색 경계 세션 닫기·닫힘 확인 → 감사 기록 →
// 재시작 요청. 잠금 해제는 여기서 하지 않는다(새 init이 completeReleasesLocked에서 한다).
func (s *Service) releaseLocked(ctx context.Context, r Request, record receipt) Response {
	if r.ExpectedRevision != record.Job.Revision {
		return responseError(r, "revision_conflict")
	}
	view := s.releaseViewLocked()
	refusal := view.Refusal
	if view.JobID != "" && view.JobID != record.Job.ID {
		// 요청한 작업이 아니라 다른 작업이 새 시작을 막거나 해제를 기다린다.
		refusal = ReleaseOtherActive
	}
	// 같은 작업의 대기(ReleasePending)는 그대로 돌려준다: 응답을 잃고 다시 보낸 경우 이미 기록됐다는 뜻이다.
	if refusal != "" {
		return responseError(r, "release_"+refusal)
	}
	closed := false
	if boundary, ok := s.backend.(ExplorationBoundary); ok {
		var err error
		closed, err = boundary.CloseExploration(ctx, s.execution(record))
		if errors.Is(err, ErrExplorationBusy) {
			return responseError(r, "release_"+ReleaseOtherActive)
		}
		if err != nil {
			log.Print("진단 준비: 탐색 경계 세션을 닫지 못해 해제를 거절합니다")
			return responseError(r, "release_"+ReleaseExplorationOpen)
		}
	}
	// 경계 닫기를 기다리는 동안 엔진 활동이 다시 생겼을 수 있다(엔진 활동 시각은 이 잠금과 따로 갱신된다). 다시 보고 거절한다.
	after := s.releaseViewLocked()
	if !after.Eligible || after.JobID != record.Job.ID {
		refusal := after.Refusal
		if refusal == "" || after.JobID != record.Job.ID {
			refusal = ReleaseOtherActive
		}
		log.Print("진단 준비: 탐색 경계를 닫는 동안 활동이 생겨 해제를 거절합니다")
		return responseError(r, "release_"+refusal)
	}
	rec := ReleaseRecord{JobID: record.Job.ID, Sequence: s.nextReleaseSequenceLocked(), Principal: r.Principal, RequestID: r.RequestID, Reason: r.Release.Reason,
		RequestedAt: s.releaseClock().UTC(), LastActivityAt: after.LastActivityAt, ReceiptUpdatedAt: *after.ReceiptUpdatedAt,
		QuietSeconds: after.QuietSeconds, JobState: record.Job.State, PreviousInit: record.ExecutionInitInstance, ExplorationSession: explorationSession(closed)}
	next := cloneReceipt(record)
	next.OperatorRelease = &rec
	if !validReceiptChange(record, next) || !validStoredRelease(next) {
		log.Print("진단 준비: 해제 기록이 검사를 통과하지 못해 해제를 거절합니다")
		return responseError(r, "unavailable")
	}
	if err := s.save(next); err != nil {
		return responseError(r, "unavailable")
	}
	log.Print("진단 준비: 운영자 해제를 기록했습니다. 준비 서비스를 정상 종료하고 다시 시작합니다")
	s.requestRestartLocked()
	out := s.releaseViewLocked()
	// 응답의 기록은 방금 남긴 것이다(벽시계로 고르는 '최신'은 시계가 뒤로 가면 다른 작업 기록일 수 있다).
	written := cloneReceipt(next).OperatorRelease
	out.Record = written
	job := cloneReceipt(next).Job
	return Response{Version: Version, RequestID: r.RequestID, Job: &job, Release: &out}
}

func explorationSession(closed bool) string {
	if closed {
		return ExplorationSessionClosed
	}
	return ExplorationSessionNone
}

// validReleaseChange는 해제 감사 기록의 영수증 전이를 검사한다. 기록은 막힌 실행에만 붙고, 붙을 때는 다른 칸을
// 바꾸지 않는다. 완료는 새 init과 경계 확인을 채우며 실행 반환·자원 회수 증거를 함께 남긴다. 완료된 기록은 바꾸지 않는다.
func validReleaseChange(old, next receipt) bool {
	switch {
	case old.OperatorRelease == nil && next.OperatorRelease == nil:
		return true
	case next.OperatorRelease == nil:
		return false
	case old.OperatorRelease == nil:
		added := next.OperatorRelease
		return added.Pending() && old.StartKey != "" && (!old.ExecutionReturned || !old.Job.DrainComplete && !old.ResourcesRecovered) &&
			reflect.DeepEqual(old.Job, next.Job) && old.ExecutionReturned == next.ExecutionReturned && old.ResourcesRecovered == next.ResourcesRecovered &&
			added.PreviousInit == old.ExecutionInitInstance
	case !old.OperatorRelease.Pending():
		return reflect.DeepEqual(old.OperatorRelease, next.OperatorRelease)
	case next.OperatorRelease.Pending():
		return reflect.DeepEqual(old.OperatorRelease, next.OperatorRelease)
	}
	done := *next.OperatorRelease
	done.NewInit, done.CompletedBoot, done.CompletedAt, done.BoundaryCheckedAt = "", "", nil, nil
	return reflect.DeepEqual(*old.OperatorRelease, done) && reflect.DeepEqual(old.Job, next.Job) && next.ExecutionReturned && next.ResourcesRecovered
}

// validStoredRelease는 저널 재생 때 기록 하나의 모양과 영수증 결속을 검사한다.
func validStoredRelease(r receipt) bool {
	rel := r.OperatorRelease
	if rel == nil {
		return true
	}
	if !validReleaseRecordShape(*rel) || rel.JobID != r.Job.ID || rel.Principal != r.Principal || r.StartKey == "" || rel.PreviousInit != r.ExecutionInitInstance {
		return false
	}
	if rel.Pending() {
		return true
	}
	// 같은 boot면 init이 바뀌어야 하고, 다른 boot(재부팅)면 init 문자열이 우연히 같아도 된다.
	newInit := rel.NewInit != rel.PreviousInit || rel.CompletedBoot != r.ExecutionBootID
	return bootIDPattern.MatchString(rel.CompletedBoot) && newInit && r.ExecutionReturned && r.ResourcesRecovered
}

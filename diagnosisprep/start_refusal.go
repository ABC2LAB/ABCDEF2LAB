package diagnosisprep

// 새 시작 거절 사유(C04 세분화, docs/url-diagnosis-preparation.md '새 시작 거절 사유'). 오류 코드는 계속 "busy"이고
// busy_reason이 왜 막혔는지 고정 코드로 더한다. 판단은 운영자 해제 상태(releaseViewLocked)와 같은 근거를 쓰며, 이 값은 새
// 시작을 허용하거나 잠금을 풀지 않는다. 작업 ID·시각·활동 기록 같은 운영자 화면의 근거는 싣지 않는다.
const (
	// BusyRunActive는 다른 회차가 준비·실행 중이라는 뜻이다(정상). 그 회차가 끝나면 시작할 수 있다.
	BusyRunActive = "run_active"
	// BusyRunStopping은 다른 회차가 중지·마감 중이거나 끝 상태를 게시했지만 실행 반환·마감 확인 전이라는 뜻이다.
	BusyRunStopping = "run_stopping"
	// BusyCandidateVerification은 선택 후보 검증 실행이 진행 중이라는 뜻이다.
	BusyCandidateVerification = "candidate_verification_active"
	// BusyClosureUnconfirmed는 이전 회차가 결과 불명이고 자원 마감을 확인하지 못했다는 뜻이다. 운영자 해제 기준(활동 없음)에는
	// 아직 닿지 않았다. 결과 불명은 완료·실패로 바꾸지 않는다.
	BusyClosureUnconfirmed = "closure_unconfirmed"
	// BusyReleaseAvailable은 결과 불명·미마감 회차가 운영자 해제(재기동형) 조건을 채웠다는 뜻이다. 운영자가 해제해야 풀린다.
	BusyReleaseAvailable = "release_available"
	// BusyReleasePendingRestart는 운영자 해제를 기록했고 준비 서비스 재기동·경계 확인을 기다린다는 뜻이다.
	BusyReleasePendingRestart = "release_pending_restart"
	// BusyRecoveryUnsupported는 이 설치·회차에서 재기동형 해제를 쓸 수 없다는 뜻이다(재기동 증거·활동 조회 없음). 운영자 복구가 필요하다.
	BusyRecoveryUnsupported = "recovery_unsupported"
	// BusyMultipleBlockers는 둘 이상의 작업이 새 시작을 막는다는 뜻이다. 운영자 확인이 필요하다.
	BusyMultipleBlockers = "multiple_blockers"
)

var busyReasons = []string{BusyRunActive, BusyRunStopping, BusyCandidateVerification, BusyClosureUnconfirmed,
	BusyReleaseAvailable, BusyReleasePendingRestart, BusyRecoveryUnsupported, BusyMultipleBlockers}

// ValidBusyReason은 닫힌 사유 목록에 있는지다.
func ValidBusyReason(code string) bool {
	for _, c := range busyReasons {
		if c == code {
			return true
		}
	}
	return false
}

// busyReasonFor는 운영자 해제 판단(view)과 막는 작업의 상태로 거절 사유를 고른다. 해제 판단을 새로 하지 않는다.
// 같은 근거를 운영자 해제 API가 보여 주므로, 화면 안내와 해제 버튼 조건이 어긋나지 않는다.
func busyReasonFor(view ReleaseView) string {
	switch {
	case view.Refusal == ReleasePending:
		return BusyReleasePendingRestart
	case view.Refusal == ReleaseOtherActive:
		return BusyMultipleBlockers
	case view.Eligible:
		return BusyReleaseAvailable
	case view.Refusal == ReleaseJobRunning:
		// 준비·실행 중이면 정상 실행, 끝 상태를 게시했지만 실행이 아직 반환되지 않았으면 마감 중이다.
		if active(view.JobState) {
			return BusyRunActive
		}
		return BusyRunStopping
	case view.JobState == Stopping:
		// 중지 중인 회차는 활동이 최근이거나 해제 증거를 쓸 수 없어도 정상 중지가 진행 중일 수 있다. 운영자 해제 기준에
		// 닿으면 위 release_available이 된다.
		return BusyRunStopping
	case view.Refusal == ReleaseUnsupported:
		return BusyRecoveryUnsupported
	case view.Refusal == ReleaseNotQuiet:
		return BusyClosureUnconfirmed
	}
	return ""
}

package diagnosisprep

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

// 멈춘 작업 해제(C04, 재기동형). 결과 불명 작업 하나가 설치 전체의 새 시작을 막을 때 운영자가 쓴다.
// 해제는 결과·예약·실행 횟수를 바꾸지 않는다. 준비 서비스는 근거를 확인하고 탐색 경계 세션을 닫은 뒤 감사 기록을
// 남기고 스스로 정상 종료한다. 컨테이너 재시작 정책으로 다시 뜬 새 init 인스턴스가 #559 증거(다른 init이 배타 저널
// 잠금을 얻음 = 이전 PID namespace가 끝나 커널이 남은 프로세스를 모두 종료)와 탐색 경계 세션 없음을 확인한 뒤에만
// 새 시작 잠금을 푼다. 서버·화면은 Docker를 다루지 않는다.
// 해제 감사 기록의 탐색 경계 세션 값.
const (
	ExplorationSessionNone   = "none"
	ExplorationSessionClosed = "closed"
)

const (
	ReleaseStatus Command = "release_status"
	Release       Command = "release"
)

// 활동 없음 기준. Planner는 정상일 때도 heartbeat(600초)까지 조용할 수 있어 그보다 넉넉한 값만 받는다.
const (
	DefaultReleaseQuiet = 1200 * time.Second
	MinReleaseQuiet     = 900 * time.Second
)

// MaxReleaseReasonBytes는 운영자가 적는 해제 사유의 상한이다(감사 기록 한 줄).
const MaxReleaseReasonBytes = 500

// 해제를 못 하는 사유다. 해제 요청이 거절되면 응답 오류 코드는 "release_" + 사유다.
const (
	ReleaseNotStuck        = "not_stuck"        // 새 시작을 막는 작업이 없다
	ReleaseJobRunning      = "job_running"      // 막는 작업이 아직 실행 상태다(먼저 '종료')
	ReleaseNotQuiet        = "not_quiet"        // 엔진 활동·영수증 갱신이 기준 시간 안에 있었다
	ReleaseOtherActive     = "other_active"     // 다른 작업이 새 시작을 막거나 탐색 중이다
	ReleaseExplorationOpen = "exploration_open" // 탐색 경계 세션을 닫거나 닫힘을 확인하지 못했다
	ReleaseUnsupported     = "unsupported"      // init 인스턴스 증거나 엔진 활동 조회가 없는 설치다
	ReleasePending         = "pending_restart"  // 해제 기록 뒤 재시작·경계 확인을 기다린다
)

var releaseRefusals = []string{ReleaseNotStuck, ReleaseJobRunning, ReleaseNotQuiet, ReleaseOtherActive, ReleaseExplorationOpen, ReleaseUnsupported, ReleasePending}

// ReleaseRequest는 해제 요청의 본문이다. 사유는 감사 기록에 그대로 남는다.
type ReleaseRequest struct {
	Reason string `json:"reason"`
}

// ReleaseRecord는 해제 감사 기록이다. 준비 저널의 그 작업 영수증에 붙어 재시작 뒤에도 남는다.
// 시각은 모두 준비 서비스 프로세스 시계(UTC)다. NewInit·CompletedAt·BoundaryCheckedAt은 새 init이 확인한 뒤에 채운다.
type ReleaseRecord struct {
	JobID string `json:"job_id"`
	// Sequence는 이 설치에서 해제를 기록한 순서(1부터)다. 최근 기록은 벽시계가 아니라 이 순서로 고른다(시계 보정과 무관).
	Sequence         uint64     `json:"sequence"`
	Principal        string     `json:"principal"`
	RequestID        string     `json:"request_id"`
	Reason           string     `json:"reason"`
	RequestedAt      time.Time  `json:"requested_at"`
	LastActivityAt   *time.Time `json:"last_activity_at,omitempty"` // 없음 = 그 작업의 엔진 활동 기록이 프로세스에 없음
	ReceiptUpdatedAt time.Time  `json:"receipt_updated_at"`
	QuietSeconds     int64      `json:"quiet_seconds"`
	JobState         State      `json:"job_state"`
	PreviousInit     string     `json:"previous_init"`
	// ExplorationSession은 해제 때 그 작업의 탐색 경계 세션이 어땠는지다: "none"(열린 세션 없음, 닫을 것 없었음)·"closed"(열려
	// 있던 세션을 닫고 닫힘을 확인함). 닫지 못하면 해제를 거절하므로 '못 닫음'은 기록되지 않는다.
	ExplorationSession string `json:"exploration_session"`
	NewInit            string `json:"new_init,omitempty"`
	// CompletedBoot은 해제를 마친 서비스의 boot ID다. 재부팅 뒤에는 init 문자열이 우연히 같을 수 있어 boot와 함께 본다.
	CompletedBoot     string     `json:"completed_boot,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	BoundaryCheckedAt *time.Time `json:"boundary_checked_at,omitempty"`
}

// Pending은 기록은 남았지만 새 init 확인 전인지다.
func (r ReleaseRecord) Pending() bool { return r.CompletedAt == nil }

// ReleaseView는 해제 판단의 근거다. 화면은 Eligible일 때만 해제 버튼을 켠다.
type ReleaseView struct {
	JobID            string         `json:"job_id,omitempty"`
	JobRevision      uint64         `json:"job_revision,omitempty"`
	JobState         State          `json:"job_state,omitempty"`
	Eligible         bool           `json:"eligible"`
	Refusal          string         `json:"refusal,omitempty"`
	QuietSeconds     int64          `json:"quiet_seconds"`
	Now              time.Time      `json:"now"`
	LastActivityAt   *time.Time     `json:"last_activity_at,omitempty"`
	ReceiptUpdatedAt *time.Time     `json:"receipt_updated_at,omitempty"`
	EligibleAt       *time.Time     `json:"eligible_at,omitempty"`
	Record           *ReleaseRecord `json:"record,omitempty"` // 가장 최근 해제 감사 기록(대기 중 포함)
}

// ActivityReporter는 그 실행의 엔진(Planner·Worker) 마지막 활동 시각을 준비 서비스 프로세스 안에서 알려 준다.
// ok=false는 이 프로세스에 그 작업의 활동 기록이 없다는 뜻이다. Service 잠금 아래에서 불리므로 Service를 부르지 않는다.
type ActivityReporter interface {
	LastActivity(Execution) (time.Time, bool)
}

// ExplorationBoundary는 준비 컨테이너 밖에서 살아남는 요청 경로(탐색 경계 세션)를 닫고 확인한다.
// 탐색이 없는 설치는 구현하지 않거나 늘 nil을 돌려준다. Service 잠금 아래에서 불린다.
type ExplorationBoundary interface {
	// CloseExploration은 그 실행의 열린 세션을 닫고 닫힘(활성 아님·연결 0)을 확인한다. closed는 닫은 세션이 있었는지다.
	// 다른 실행의 세션이 열려 있으면 ErrExplorationBusy다.
	CloseExploration(context.Context, Execution) (closed bool, err error)
	// ExplorationClear는 열린 세션이 하나도 없음을 확인한다.
	ExplorationClear(context.Context) error
}

var ErrExplorationBusy = errors.New("다른 실행의 탐색 경계 세션이 열려 있습니다")

func validReleaseReason(s string) bool {
	if !validText(s, MaxReleaseReasonBytes) || strings.TrimSpace(s) == "" {
		return false
	}
	// 감사 사유는 화면에 그대로 보인다. 방향 바꿈(U+202E)·폭 없는 문자 같은 서식 문자와 줄·문단 구분자는 겉보기를 바꾸므로 받지 않는다.
	for _, r := range s {
		if unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

func validReleaseRefusal(code string) bool {
	for _, c := range releaseRefusals {
		if c == code {
			return true
		}
	}
	return false
}

func validReleaseError(code string) bool {
	// release_pending_restart는 같은 작업을 다시 보냈을 때(응답 유실 뒤 재시도) 이미 기록돼 재시작을 기다린다는 뜻이다.
	suffix, ok := strings.CutPrefix(code, "release_")
	return ok && validReleaseRefusal(suffix)
}

func validReleaseRecordShape(r ReleaseRecord) bool {
	completed := r.CompletedAt != nil
	if r.Sequence == 0 || !reference.MatchString(r.JobID) || !validText(r.Principal, 256) || !reference.MatchString(r.RequestID) || !validReleaseReason(r.Reason) ||
		r.RequestedAt.IsZero() || r.ReceiptUpdatedAt.IsZero() || r.ReceiptUpdatedAt.After(r.RequestedAt) ||
		(r.LastActivityAt != nil && r.LastActivityAt.After(r.RequestedAt)) ||
		r.QuietSeconds < int64(MinReleaseQuiet/time.Second) || (r.JobState != Unknown && r.JobState != Stopping) ||
		!initInstancePattern.MatchString(r.PreviousInit) || (r.ExplorationSession != ExplorationSessionNone && r.ExplorationSession != ExplorationSessionClosed) {
		return false
	}
	if completed != (r.BoundaryCheckedAt != nil) || completed != (r.NewInit != "") || completed != (r.CompletedBoot != "") {
		return false
	}
	// 새 init이 이전 init과 다른지는 boot와 함께 봐야 해서 영수증 검사(validStoredRelease)가 맡는다.
	return !completed || (initInstancePattern.MatchString(r.NewInit) && !r.CompletedAt.Before(r.RequestedAt))
}

func validReleaseView(v ReleaseView) bool {
	if v.QuietSeconds < int64(MinReleaseQuiet/time.Second) || v.Now.IsZero() || (v.Eligible && v.Refusal != "") || (!v.Eligible && !validReleaseRefusal(v.Refusal)) {
		return false
	}
	if v.JobID == "" {
		if v.Refusal != ReleaseNotStuck || v.JobRevision != 0 || v.JobState != "" {
			return false
		}
	} else if !reference.MatchString(v.JobID) || v.JobRevision == 0 {
		return false
	}
	return v.Record == nil || validReleaseRecordShape(*v.Record)
}

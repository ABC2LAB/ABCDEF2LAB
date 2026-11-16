package exploreboundary

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/pathmask"
)

// 평가·수용시험 회차의 경계 원자료(docs/contracts/exploration-boundary.md §4.1). 러너가 만든 근거 디렉터리에 경계가 직접
// 쓰고, 러너가 경계 종료 뒤 최종 바이트의 hash·카운터를 계산해 자기 키로 서명한다. 경계는 서명 키를 갖지 않는다.
// 제품 설치는 이 출력을 켜지 않는다(제품 경계 기록은 export하지 않는다).
const (
	EvidenceFormat     = "abc2lab.explore-boundary-export/v1"
	EvidenceRowsName   = "boundary-records.jsonl"
	EvidenceMarkerName = "boundary-sealed.json"
	// evidenceBufferBytes는 행 쓰기 버퍼다. 탐색 요청마다 fsync하지 않고, 세션 마감·봉인 때 flush·fsync한다.
	evidenceBufferBytes = 64 << 10
)

// 봉인 계기. 러너가 경계를 내릴 때(정상)와 경계가 스스로 멈출 때(입구 리슨 실패 등)를 가른다.
const (
	SealedByShutdown = "shutdown"
	SealedByFailure  = "failure"
)

// ErrEvidence는 원자료를 쓸 수 없을 때다. 원자료가 불완전해지면 그 세션은 새 송신을 멈춘다.
var ErrEvidence = errors.New("explore_boundary_evidence_unavailable")

// EvidenceSession은 표식에 남기는 세션 결속과 마감이다. 원자료 디렉터리 하나에 세션은 하나다.
type EvidenceSession struct {
	SessionRef     string    `json:"session_ref"`
	RunRef         string    `json:"run_ref"`
	JobID          string    `json:"job_id"`
	TaskID         string    `json:"task_id"`
	TargetRef      string    `json:"target_ref"`
	TargetRevision uint64    `json:"target_revision"`
	SourceDigest   string    `json:"source_digest"`
	OpenedAt       time.Time `json:"opened_at"`
	// 아래는 세션이 닫혔을 때만 채운다. ClosedAtSeal은 경계를 내릴 때까지 열려 있던 세션을 봉인이 닫은 경우다.
	ClosedAt     *time.Time         `json:"closed_at"`
	ClosedReason exploration.Reason `json:"closed_reason"`
	RowsAtClose  *uint64            `json:"rows_at_close"`
	ClosedAtSeal bool               `json:"closed_at_seal"`
}

// EvidenceMarker는 봉인 표식이다. 행 파일을 flush·fsync한 뒤 마지막에 원자적으로 쓴다. 카운터는 경계 자신의 값이고,
// 러너는 이것을 믿지 않고 행에서 다시 세어 대조한다.
type EvidenceMarker struct {
	Format      string           `json:"format"`
	PathProfile string           `json:"path_profile"`
	PathKeyRef  string           `json:"path_key_ref"`
	MaxBytes    int64            `json:"max_bytes"`
	Session     *EvidenceSession `json:"session"`
	SealedAt    time.Time        `json:"sealed_at"`
	SealedBy    string           `json:"sealed_by"`
	// Rows·Bytes·RowsSHA256은 행 파일에 실제로 쓴 내용이다. 바이트 상한에 닿은 뒤의 행은 쓰지 않고 수만 센다.
	Rows           uint64 `json:"rows"`
	Bytes          int64  `json:"bytes"`
	RowsSHA256     string `json:"rows_sha256"`
	Truncated      bool   `json:"truncated"`
	RowsNotWritten uint64 `json:"rows_not_written"`
	// InflightAtSeal은 봉인 때 끝나지 않은 전달 요청 수다. 0이 아니면 그 행이 원자료에 없다.
	InflightAtSeal int `json:"inflight_at_seal"`
	// LogComplete는 쓰기·flush·fsync가 한 번도 실패하지 않았을 때만 참이다.
	LogComplete bool `json:"log_complete"`
}

// evidenceFile은 행 파일이다. 리눅스 구현은 근거 디렉터리의 O_EXCL 0600 파일이다.
type evidenceFile interface {
	io.Writer
	Sync() error
	Close() error
}

// Evidence는 경계 원자료 쓰기다. 행은 세션 잠금 안에서 버퍼에 쓴다(잠금 순서: 세션 → Evidence).
type Evidence struct {
	mu         sync.Mutex
	file       evidenceFile
	buf        *bufio.Writer
	sum        hash.Hash
	publish    func(name string, body []byte) error
	keyRef     string
	maxBytes   int64
	bytes      int64
	rows       uint64
	notWritten uint64
	truncated  bool
	failed     bool
	sealed     bool
	session    *EvidenceSession
}

func newEvidence(file evidenceFile, publish func(string, []byte) error, keyRef string, maxBytes int64) (*Evidence, error) {
	if file == nil || publish == nil || keyRef == "" || maxBytes <= 0 {
		return nil, ErrConfig
	}
	return &Evidence{file: file, buf: bufio.NewWriterSize(file, evidenceBufferBytes), sum: sha256.New(), publish: publish, keyRef: keyRef, maxBytes: maxBytes}, nil
}

func (e *Evidence) opened(b diagnosisflow.ExplorationBinding, at time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil || e.sealed {
		return ErrEvidence
	}
	e.session = &EvidenceSession{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision,
		SourceDigest: b.SourceDigest, OpenedAt: at}
	return nil
}

// row는 기록 행 하나를 쓴다. 메모리 차단 행 상한과 별개로 모든 행을 쓴다. 바이트 상한에 닿으면 그 뒤 행은 수만 세고
// 오류로 보지 않는다(회차 판정은 러너가 UNDECIDABLE로 둔다). 쓰기 실패만 오류다.
func (e *Evidence) row(r Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sealed {
		return nil
	}
	if e.failed {
		return ErrEvidence
	}
	line, err := json.Marshal(r)
	if err != nil {
		e.failed = true
		return ErrEvidence
	}
	line = append(line, '\n')
	if e.truncated || e.bytes+int64(len(line)) > e.maxBytes {
		e.truncated = true
		e.notWritten++
		return nil
	}
	if _, err := e.buf.Write(line); err != nil {
		e.failed = true
		return ErrEvidence
	}
	e.sum.Write(line)
	e.bytes += int64(len(line))
	e.rows++
	return nil
}

// flushLocked는 버퍼를 비우고 디스크에 내린다.
func (e *Evidence) flushLocked() {
	if e.failed {
		return
	}
	if err := e.buf.Flush(); err != nil {
		e.failed = true
		return
	}
	if err := e.file.Sync(); err != nil {
		e.failed = true
	}
}

// closed는 세션 마감을 남기고 그때까지의 행을 디스크에 내린다.
func (e *Evidence) closed(reason exploration.Reason, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sealed || e.session == nil || e.session.ClosedAt != nil {
		return
	}
	rows := e.rows
	e.session.ClosedAt, e.session.ClosedReason, e.session.RowsAtClose = &at, reason, &rows
	e.flushLocked()
}

// seal은 행 파일을 flush·fsync·닫고 표식을 마지막에 쓴다. 한 번만 효과가 있다.
func (e *Evidence) seal(inflight int, closedAtSeal bool, by string, at time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sealed {
		return ErrEvidence
	}
	e.sealed = true
	e.flushLocked()
	if err := e.file.Close(); err != nil {
		e.failed = true
	}
	if e.session != nil && closedAtSeal {
		e.session.ClosedAtSeal = true
	}
	marker := EvidenceMarker{Format: EvidenceFormat, PathProfile: pathmask.Profile, PathKeyRef: e.keyRef, MaxBytes: e.maxBytes, Session: e.session,
		SealedAt: at, SealedBy: by, Rows: e.rows, Bytes: e.bytes, RowsSHA256: hex.EncodeToString(e.sum.Sum(nil)), Truncated: e.truncated,
		RowsNotWritten: e.notWritten, InflightAtSeal: inflight, LogComplete: !e.failed}
	body, err := json.Marshal(marker)
	if err != nil {
		return ErrEvidence
	}
	if err := e.publish(EvidenceMarkerName, append(body, '\n')); err != nil {
		return ErrEvidence
	}
	return nil
}

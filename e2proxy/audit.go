package e2proxy

// This file uses the durability pattern of transmit/auditlog, but defines a
// separate wire version. General HTTP events must not masquerade as registered
// GET evidence or become E3 verdicts merely because their audit authenticates.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

const (
	JournalVersion        = Version
	JournalVersionV3      = "e2-http-v3"
	journalIDBytes        = 16
	journalMinKeyBytes    = 32
	journalMaxKeyBytes    = 4096
	journalMaxLineBytes   = 16384
	journalMaxBytes       = 256 << 20
	journalMaxEvents      = 100000
	journalMaxNameBytes   = 128
	journalMaxRefBytes    = 128
	journalMaxMethodBytes = 16
	journalMaxEmptyReads  = 100
)

var (
	ErrJournalConfig    = errors.New("e2proxy: invalid journal configuration")
	ErrJournalStorage   = errors.New("e2proxy: journal storage unavailable")
	ErrJournalExists    = errors.New("e2proxy: journal already exists")
	ErrJournalEvent     = errors.New("e2proxy: invalid journal event")
	ErrJournalClosed    = errors.New("e2proxy: journal closed")
	ErrJournalFailed    = errors.New("e2proxy: journal incomplete")
	ErrJournalIntegrity = errors.New("e2proxy: journal integrity mismatch")
	ErrJournalLimit     = errors.New("e2proxy: journal verification limit")
)

// JournalCheckpoint is a Sync-confirmed prefix, not a delivered Record ACK.
// Cancellation after a successful Sync advances this prefix but poisons the
// Journal and returns an error. Keep it distinct from Execution ACK ordinals and
// AuditComplete; it cannot certify that the caller accepted the persisted event.
// The host must retain it outside
// the journal if rollback or truncation must be detected. It is not permission
// to send and proves no key ownership, epoch, independent storage, or E3 result.
type JournalCheckpoint struct {
	Version   string
	JournalID string
	Sequence  uint64
	MAC       string
	Scope     transmit.Scope
}

// JournalSummary authenticates a bounded stream against its supplied key.
// Without an expected checkpoint, a valid older prefix remains valid; this
// function does not invent the caller's trusted current head.
type JournalSummary struct {
	Checkpoint JournalCheckpoint
	Events     uint64
}

type journalWriter interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type journalHeader struct {
	Version   string
	JournalID string
	CreatedAt time.Time
	Scope     transmit.Scope `json:",omitzero"`
	KeyRef    string         `json:",omitempty"`
}

type journalRecord struct {
	Index    uint64
	Previous string
	Event    Event
}

type journalLine struct {
	Kind   string
	Header *journalHeader `json:",omitempty"`
	Record *journalRecord `json:",omitempty"`
	MAC    string
}

// Journal owns a fresh append-only file. A Write/Sync/cancellation ambiguity
// permanently closes further admission; there is no repair or resume in place.
type Journal struct {
	gate     chan struct{}
	mu       sync.Mutex
	writer   journalWriter
	key      []byte
	head     JournalCheckpoint
	size     int64 // gate를 잡은 쓰기 경로에서만 접근한다.
	failed   bool
	closed   bool
	closeErr error
	// screen은 화면 사본(CreateScreenJournal)이다. 일반 v2 행에 더해 확정 단계 사건의 화면 투영(E3ScreenProjection)을 받는다.
	screen bool
}

// CreateJournal creates a Linux durable journal in an existing trusted 0700
// directory. The host supplies a key; this constructor issues no authority.
// name is one component. O_EXCL and os.Root reject replacement/traversal.
func CreateJournal(directory, name string, key []byte) (*Journal, error) {
	return createJournal(directory, name, key, journalHeader{Version: JournalVersion})
}

// CreateScreenJournal은 Host 모드 실행의 화면 사본(v2) 저널을 만든다. CreateJournal과 같고, 확정 단계 사건의 화면
// 투영(E3ScreenProjection)도 기록한다. 확정 사건의 정본은 Host v3 저널이고, 이 사본은 작업대 e2-flow·요청 목록이
// VerifyScreenJournalEvents로만 읽는다. 일반 v2·v3·평가 저널 검증은 확정 행을 계속 거부한다(2026-11-25 사용자·이동찬 결정).
func CreateScreenJournal(directory, name string, key []byte) (*Journal, error) {
	j, err := createJournal(directory, name, key, journalHeader{Version: JournalVersion})
	if err != nil {
		return nil, err
	}
	j.screen = true
	return j, nil
}

// ScreenCopy는 이 저널이 화면 사본(CreateScreenJournal)인지 알려 준다.
func (j *Journal) ScreenCopy() bool { return j != nil && j.screen }

// CreateJournalV3는 실행 범위와 서버가 발급한 비민감 키 참조가 헤더에 결속된 일반 E② 저널을 만든다.
// Checkpoint는 로컬 Sync 지점이다. 외부 정본 게시와 현재성 검증은 별도 권한 경계의 책임이다.
func CreateJournalV3(directory, name string, key []byte, scope transmit.Scope, keyRef string) (*Journal, error) {
	return createJournal(directory, name, key, journalHeader{Version: JournalVersionV3, Scope: scope, KeyRef: keyRef})
}

func createJournal(directory, name string, key []byte, header journalHeader) (*Journal, error) {
	if runtime.GOOS != "linux" || !validJournalName(name) || len(key) < journalMinKeyBytes || len(key) > journalMaxKeyBytes || !validJournalHeaderBinding(header) {
		return nil, ErrJournalConfig
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrJournalStorage
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrJournalConfig
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, ErrJournalStorage
	}
	defer dir.Close()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrJournalExists
		}
		return nil, ErrJournalStorage
	}
	header.CreatedAt = time.Now().UTC()
	j, err := newJournalWithHeader(file, key, header)
	if err != nil {
		return nil, err
	}
	if err := dir.Sync(); err != nil {
		j.Close()
		return nil, ErrJournalStorage
	}
	return j, nil
}

func newJournalWithHeader(writer journalWriter, key []byte, header journalHeader) (*Journal, error) {
	if writer == nil || len(key) < journalMinKeyBytes || len(key) > journalMaxKeyBytes || !validJournalHeaderBinding(header) {
		return nil, ErrJournalConfig
	}
	ownedKey := bytes.Clone(key)
	fail := func() (*Journal, error) { writer.Close(); clear(ownedKey); return nil, ErrJournalStorage }
	var nonce [journalIDBytes]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail()
	}
	header.JournalID = hex.EncodeToString(nonce[:])
	mac, err := journalMAC(ownedKey, header.Version, "header", header)
	if err != nil {
		return fail()
	}
	data, err := encodeJournalLine(journalLine{Kind: "header", Header: &header, MAC: mac})
	if err != nil {
		return fail()
	}
	if n, err := writer.Write(data); err != nil || n != len(data) {
		return fail()
	}
	if err := writer.Sync(); err != nil {
		return fail()
	}
	return &Journal{writer: writer, key: ownedKey, gate: make(chan struct{}, 1), size: int64(len(data)),
		head: header.checkpoint(mac)}, nil
}

// Record returns success only after the entire authenticated event is synced.
// Cancellation after IO starts poisons the writer even if bytes may be durable.
// File IO is synchronous; context cancellation never abandons an active write.
func (j *Journal) Record(ctx context.Context, event Event) error {
	if j == nil || j.gate == nil || ctx == nil {
		return ErrJournalConfig
	}
	select {
	case j.gate <- struct{}{}:
		defer func() { <-j.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	head, failed, closed := j.head, j.failed, j.closed
	j.mu.Unlock()
	if closed {
		return ErrJournalClosed
	}
	if failed {
		return ErrJournalFailed
	}
	if !validJournalRecordEventFor(event, head.Version, j.screen) || head.Sequence == math.MaxUint64 ||
		head.Sequence >= journalMaxEvents || event.Index != head.Sequence+1 || head.scopeMismatch(event.Scope) {
		return ErrJournalEvent
	}
	event.At = event.At.UTC()
	record := journalRecord{Index: head.Sequence + 1, Previous: head.MAC, Event: event}
	mac, err := journalMAC(j.key, head.Version, "event", record)
	if err != nil {
		return ErrJournalEvent
	}
	data, err := encodeJournalLine(journalLine{Kind: "event", Record: &record, MAC: mac})
	if err != nil {
		return ErrJournalEvent
	}
	if head.Version == JournalVersionV3 && j.size+int64(len(data)) > journalMaxBytes {
		return ErrJournalLimit
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := j.writer.Write(data)
	if err != nil || n != len(data) {
		j.poisonJournal()
		return ErrJournalStorage
	}
	if err := j.writer.Sync(); err != nil {
		j.poisonJournal()
		return ErrJournalStorage
	}
	j.size += int64(len(data))
	j.mu.Lock()
	j.head.Sequence, j.head.MAC, j.head.Scope = record.Index, mac, event.Scope
	j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		j.poisonJournal()
		return err
	}
	return nil
}

func (j *Journal) poisonJournal() {
	j.mu.Lock()
	j.failed = true
	j.mu.Unlock()
}

// String and GoString keep accidental diagnostic formatting from exposing the
// privately retained HMAC key. Public progress comes from Checkpoint instead.
func (j *Journal) String() string   { return "e2proxy.Journal{private}" }
func (j *Journal) GoString() string { return j.String() }

func (j *Journal) Checkpoint() JournalCheckpoint {
	if j == nil {
		return JournalCheckpoint{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.head
}

// Close joins the writer, erases its key copy, and reports a stable failure.
// It does not erase data, manufacture a final event, or prove child/flow drain.
func (j *Journal) Close() error {
	if j == nil || j.gate == nil {
		return ErrJournalClosed
	}
	j.gate <- struct{}{}
	defer func() { <-j.gate }()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return j.closeErr
	}
	j.closed = true
	if err := j.writer.Close(); err != nil {
		j.closeErr = ErrJournalStorage
	} else if j.failed {
		j.closeErr = ErrJournalFailed
	}
	clear(j.key)
	return j.closeErr
}

// VerifyJournal verifies canonical v1/v2 records with an optional checkpoint.
// A v3 journal requires a checkpoint obtained independently of the input.
// This method checks equality against that checkpoint. The caller supplies its
// authority and freshness. A blocking Reader must itself support cancellation.
func VerifyJournal(ctx context.Context, source io.Reader, key []byte, expected ...JournalCheckpoint) (JournalSummary, error) {
	return verifyJournal(ctx, source, key, nil, nil, expected...)
}

// VerifyJournalV3Extension은 읽기 전용 v3 검증이다. 같은 입력을 한 번 읽으면서
// 별도로 보관한 두 체크포인트가 같은 인증 체인에 순서대로 있는지 확인한다.
// 두 체크포인트가 같아도 검증할 수 있다. 입력과 체크포인트의 현재성은 제공자가 확인해야 한다.
func VerifyJournalV3Extension(ctx context.Context, source io.Reader, key []byte, prior, expected JournalCheckpoint) (JournalSummary, error) {
	if prior.Version != JournalVersionV3 || expected.Version != JournalVersionV3 ||
		prior.JournalID != expected.JournalID || prior.Scope != expected.Scope || prior.Sequence > expected.Sequence {
		return JournalSummary{}, ErrJournalIntegrity
	}
	return verifyJournal(ctx, source, key, nil, &prior, expected)
}

// VerifyJournalEvents는 VerifyJournal과 같은 검증을 하고 기록 전체가 검증됐을 때만 사건을 돌려준다.
// 마지막 행 삭제나 과거 기록으로의 교체를 감지하려면 저널과 별도로 보관한 신뢰할 수 있는 체크포인트를 넘긴다.
// v1/v2는 체크포인트를 생략하면 정상적으로 인증된 이전 접두부도 읽을 수 있다. v3는 체크포인트가 필수다.
func VerifyJournalEvents(ctx context.Context, source io.Reader, key []byte, expected ...JournalCheckpoint) ([]Event, JournalSummary, error) {
	var events []Event
	summary, err := verifyJournal(ctx, source, key, func(e Event) { events = append(events, e) }, nil, expected...)
	if err != nil {
		return nil, JournalSummary{}, err
	}
	return events, summary, nil
}

// VerifyScreenJournalEvents는 VerifyJournalEvents와 같고, 화면 사본(CreateScreenJournal)의 확정 단계 투영 행도 받는다.
// 작업대 e2-flow·요청 목록만 쓴다.
func VerifyScreenJournalEvents(ctx context.Context, source io.Reader, key []byte, expected ...JournalCheckpoint) ([]Event, JournalSummary, error) {
	var events []Event
	summary, err := verifyJournalMode(ctx, source, key, func(e Event) { events = append(events, e) }, nil, true, expected...)
	if err != nil {
		return nil, JournalSummary{}, err
	}
	return events, summary, nil
}

func verifyJournal(ctx context.Context, source io.Reader, key []byte, visit func(Event), prior *JournalCheckpoint, expected ...JournalCheckpoint) (JournalSummary, error) {
	return verifyJournalMode(ctx, source, key, visit, prior, false, expected...)
}

func verifyJournalMode(ctx context.Context, source io.Reader, key []byte, visit func(Event), prior *JournalCheckpoint, screen bool, expected ...JournalCheckpoint) (JournalSummary, error) {
	if ctx == nil || source == nil || len(key) < journalMinKeyBytes || len(key) > journalMaxKeyBytes || len(expected) > 1 {
		return JournalSummary{}, ErrJournalConfig
	}
	if err := ctx.Err(); err != nil {
		return JournalSummary{}, err
	}
	ownedKey := bytes.Clone(key)
	defer clear(ownedKey)
	reader := bufio.NewReaderSize(io.LimitReader(&journalContextReader{ctx: ctx, source: source}, journalMaxBytes+1), journalMaxLineBytes+1)
	var total int64
	readLine := func() (journalLine, error) {
		if err := ctx.Err(); err != nil {
			return journalLine{}, err
		}
		data, err := reader.ReadSlice('\n')
		total += int64(len(data))
		if total > journalMaxBytes || len(data) > journalMaxLineBytes {
			return journalLine{}, ErrJournalLimit
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return journalLine{}, ErrJournalLimit
			}
			if errors.Is(err, io.EOF) && len(data) == 0 {
				return journalLine{}, io.EOF
			}
			return journalLine{}, ErrJournalIntegrity
		}
		if err := ctx.Err(); err != nil {
			return journalLine{}, err
		}
		var line journalLine
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&line); err != nil {
			return journalLine{}, ErrJournalIntegrity
		}
		canonical, err := encodeJournalLine(line)
		if err != nil || !bytes.Equal(canonical, data) {
			return journalLine{}, ErrJournalIntegrity
		}
		return line, nil
	}
	line, err := readLine()
	if err != nil {
		return JournalSummary{}, integrityUnlessContext(ctx, err)
	}
	if line.Kind != "header" || line.Header == nil || line.Record != nil ||
		!validJournalHeaderBinding(*line.Header) || !validJournalHex(line.Header.JournalID, journalIDBytes) ||
		!validJournalTime(line.Header.CreatedAt) || !validJournalHex(line.MAC, sha256.Size) {
		return JournalSummary{}, ErrJournalIntegrity
	}
	version := line.Header.Version
	head := line.Header.checkpoint(line.MAC)
	if version == JournalVersionV3 {
		if len(expected) != 1 || expected[0].Version != version || expected[0].JournalID != head.JournalID ||
			expected[0].Scope != head.Scope {
			return JournalSummary{}, ErrJournalIntegrity
		}
	}
	want, err := journalMAC(ownedKey, version, "header", *line.Header)
	if err != nil || !hmac.Equal([]byte(want), []byte(line.MAC)) {
		return JournalSummary{}, ErrJournalIntegrity
	}
	if prior != nil && prior.Sequence == 0 && head != *prior {
		return JournalSummary{}, ErrJournalIntegrity
	}
	for {
		line, err = readLine()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return JournalSummary{}, integrityUnlessContext(ctx, err)
		}
		if head.Sequence >= journalMaxEvents {
			return JournalSummary{}, ErrJournalLimit
		}
		if line.Kind != "event" || line.Header != nil || line.Record == nil ||
			line.Record.Index != head.Sequence+1 || line.Record.Event.Index != line.Record.Index || line.Record.Previous != head.MAC ||
			!validJournalHex(line.MAC, sha256.Size) || !validJournalRecordEventFor(line.Record.Event, version, screen) ||
			head.scopeMismatch(line.Record.Event.Scope) {
			return JournalSummary{}, ErrJournalIntegrity
		}
		want, err = journalMAC(ownedKey, version, "event", *line.Record)
		if err != nil || !hmac.Equal([]byte(want), []byte(line.MAC)) {
			return JournalSummary{}, ErrJournalIntegrity
		}
		head.Sequence, head.MAC, head.Scope = line.Record.Index, line.MAC, line.Record.Event.Scope
		if prior != nil && head.Sequence == prior.Sequence && head != *prior {
			return JournalSummary{}, ErrJournalIntegrity
		}
		if visit != nil {
			visit(line.Record.Event)
		}
	}
	if err := ctx.Err(); err != nil {
		return JournalSummary{}, err
	}
	if len(expected) == 1 && head != expected[0] {
		return JournalSummary{}, ErrJournalIntegrity
	}
	return JournalSummary{Checkpoint: head, Events: head.Sequence}, nil
}

func integrityUnlessContext(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrJournalLimit) {
		return err
	}
	return ErrJournalIntegrity
}

func encodeJournalLine(line journalLine) ([]byte, error) {
	data, err := json.Marshal(line)
	if err != nil || len(data)+1 > journalMaxLineBytes {
		return nil, ErrJournalEvent
	}
	return append(data, '\n'), nil
}

func (header journalHeader) checkpoint(mac string) JournalCheckpoint {
	// 키 참조는 헤더 MAC과 그 뒤 체인에 이미 포함된다.
	return JournalCheckpoint{Version: header.Version, JournalID: header.JournalID, MAC: mac, Scope: header.Scope}
}

func (head JournalCheckpoint) scopeMismatch(scope transmit.Scope) bool {
	return (head.Version == JournalVersionV3 || head.Sequence > 0) && head.Scope != scope
}

func validJournalHeaderBinding(header journalHeader) bool {
	if header.Version == JournalVersionV3 {
		for _, ref := range [...]string{header.Scope.TaskRef, header.Scope.RunRef, header.Scope.CaseRef, header.Scope.ResetRef, header.Scope.PolicyRef, header.KeyRef} {
			if !validJournalRef(ref) {
				return false
			}
		}
		return true
	}
	return knownJournalVersion(header.Version) && header.Scope == (transmit.Scope{}) && header.KeyRef == ""
}

// v3는 일반 감사 행에 v2 검사를 재사용하고 E③ 전용 칸은 별도 검사한다.
// 판 표기는 실제 MAC 계산과 저장에 그대로 쓴다. 일반 호출의 프로그램 귀속은 바꾸지 않는다.
func validJournalRecordEvent(event Event, version string) bool {
	if event.Version != version {
		return false
	}
	if version != JournalVersionV3 && event.Program.isE3() {
		return false
	}
	if version == JournalVersionV3 {
		if event.Program.isE3() {
			if event.Stage == "call_closed" {
				if event.E3Audit != nil {
					return false
				}
			} else if !validE3AuditEvent(event) {
				return false
			}
			// 일반 v2 귀속 검사는 지원되는 프로그램 호출과 같은 형태만 허용한다.
			// 원본 Program은 이벤트와 MAC에 그대로 남는다.
			event.Program = ProgramAuthPrep
		} else if event.E3Audit != nil {
			return false
		}
		event.Version = Version
		version = Version
	} else if event.E3Audit != nil {
		return false
	}
	return validJournalEvent(event, version)
}

// validJournalRecordEventFor는 저널 종류에 맞춘 행 검사다. 화면 사본(screen)의 v2 행만 확정 단계 투영을 더 받는다.
// 투영은 메서드·명령 참조·자른 응답 표시가 없어야 하고, 나머지는 일반 프로그램 호출 행과 같은 v2 규칙으로 본다. E3Audit이
// 있거나 Worker intent에 귀속된 행은 그 v2 규칙(E3Audit 거부·호출 귀속 검사)이 거부한다.
func validJournalRecordEventFor(event Event, version string, screen bool) bool {
	if !screen || version == JournalVersionV3 || !event.Program.isE3() {
		return validJournalRecordEvent(event, version)
	}
	if event.Method != "" || event.CommandRef != "" || event.ResponseTruncated {
		return false
	}
	// 일반 v2 귀속 검사는 지원되는 프로그램 호출과 같은 형태만 허용한다(v3 검사와 같은 방식). 원본 Program은 행과 MAC에 남는다.
	event.Program = ProgramAuthPrep
	return validJournalRecordEvent(event, version)
}

// knownJournalVersion은 기존 행 검증기가 담당하는 판이다.
func knownJournalVersion(version string) bool { return version == Version || version == VersionV1 }

// journalMAC은 판을 MAC 도메인에 넣는다. 저장된 v1 기록은 v1 문자열로 다시 계산한다.
func journalMAC(key []byte, version, domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(version + "\x00" + domain + "\x00"))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func validJournalName(name string) bool {
	if len(name) == 0 || len(name) > journalMaxNameBytes || name == "." || name == ".." {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
			return false
		}
	}
	return true
}

func validJournalRef(ref string) bool {
	if len(ref) == 0 || len(ref) > journalMaxRefBytes || strings.Contains(ref, "://") {
		return false
	}
	for i, ch := range ref {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			continue
		}
		if i > 0 && (ch == '_' || ch == '-' || ch == '.' || ch == ':' || ch == '/') {
			continue
		}
		return false
	}
	return true
}

func validJournalHex(text string, size int) bool {
	if len(text) != 2*size {
		return false
	}
	for _, ch := range text {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func validJournalTime(at time.Time) bool { return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 }

// validJournalEvent는 그 판의 규칙으로 행 하나를 본다. v1에는 withheld·response_truncated가 없다.
func validJournalEvent(event Event, version string) bool {
	if event.Index == 0 || event.Version != version || !knownJournalVersion(version) || !validJournalTime(event.At) {
		return false
	}
	if version == VersionV1 && (event.Reason == Withheld || event.ResponseTruncated || event.Program != "") {
		return false
	}
	// 보류 결과는 허용해서 보냈고 대상이 답한 뒤 표현만 주지 않은 것이다. 끝까지 전달한 응답이 아니다. call_closed 행은
	// call의 마지막 사유를 요약으로 실으므로 withheld일 수 있다. 자른 응답 표시는 보류 결과 행에만 붙는다.
	if event.Stage == "intent" && event.Reason == Withheld || event.ResponseTruncated && (event.Stage != "result" || event.Reason != Withheld) ||
		event.Stage == "result" && event.Reason == Withheld && (event.Decision != "allowed" || !event.ResponseReceived || event.ResponseComplete) {
		return false
	}
	for _, ref := range []string{event.Scope.TaskRef, event.Scope.RunRef, event.Scope.CaseRef, event.Scope.ResetRef, event.Scope.PolicyRef, event.PolicyRevision} {
		if !validJournalRef(ref) {
			return false
		}
	}
	for _, ref := range []string{event.CallRef, event.CommandRef, event.RuleRef} {
		if ref != "" && !validJournalRef(ref) {
			return false
		}
	}
	switch event.Stage {
	case "intent", "result":
		if !ValidCallAttribution(event.IntentID, event.Program) || event.Sequence == 0 || !validJournalRef(event.CallRef) || !validJournalHex(event.AttemptID, randomRefBytes) {
			return false
		}
		if event.Decision != "allowed" && event.Decision != "denied" && event.Decision != "unsupported" {
			return false
		}
		if event.Decision == "allowed" && event.RuleRef == "" {
			return false
		}
		if event.Decision != "allowed" && !event.NotSent {
			return false
		}
		if event.Stage == "intent" && (event.Decision != "allowed" || event.ForwardAttempted || event.RequestWritten || event.ResponseReceived || event.ResponseComplete) {
			return false
		}
	case "control":
		if event.Program != "" || event.IntentID < 0 || event.AttemptID != "" || event.Sequence != 0 || event.Decision != "" {
			return false
		}
	case "call_closed":
		if !ValidCallAttribution(event.IntentID, event.Program) || !validJournalRef(event.CallRef) || event.AttemptID != "" || event.Sequence != 0 || event.Decision != "" {
			return false
		}
	default:
		return false
	}
	if len(event.Method) > journalMaxMethodBytes {
		return false
	}
	for _, ch := range event.Method {
		if !(ch >= 'A' && ch <= 'Z' || ch == '-') {
			return false
		}
	}
	if event.NotSent && event.ForwardAttempted || event.RequestWritten && !event.ForwardAttempted ||
		event.ResponseReceived && !event.ForwardAttempted || event.ResponseComplete && !event.ResponseReceived ||
		event.ForwardAttempted && event.Decision != "allowed" {
		return false
	}
	if event.ResponseReceived {
		if event.StatusCode < 100 || event.StatusCode > 599 {
			return false
		}
	} else if event.StatusCode != 0 {
		return false
	}
	return validJournalReason(string(event.Reason))
}

func validJournalReason(reason string) bool {
	switch Reason(reason) {
	case Complete, Unsupported, PolicyDenied, WorkflowPaused, Closed, AuditFailed, Expired,
		Revoked, BudgetExhausted, TransportFailed, Unknown, AttemptLimit, DenialLimit,
		TransientLimit, UnknownLimit, Invalid, Withheld:
		return true
	}
	return false
}

type journalContextReader struct {
	ctx        context.Context
	source     io.Reader
	emptyReads int
}

func (r *journalContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	if n < 0 || n > len(p) {
		return 0, ErrJournalIntegrity
	}
	if canceled := r.ctx.Err(); canceled != nil {
		return n, canceled
	}
	if n == 0 && err == nil {
		r.emptyReads++
		if r.emptyReads >= journalMaxEmptyReads {
			return 0, io.ErrNoProgress
		}
	} else {
		r.emptyReads = 0
	}
	return n, err
}

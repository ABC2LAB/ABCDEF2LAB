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
	failed   bool
	closed   bool
	closeErr error
}

// CreateJournal creates a Linux durable journal in an existing trusted 0700
// directory. The host supplies a key; this constructor issues no authority.
// name is one component. O_EXCL and os.Root reject replacement/traversal.
func CreateJournal(directory, name string, key []byte) (*Journal, error) {
	if runtime.GOOS != "linux" || !validJournalName(name) || len(key) < journalMinKeyBytes || len(key) > journalMaxKeyBytes {
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
	j, err := newJournal(file, key, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := dir.Sync(); err != nil {
		j.Close()
		return nil, ErrJournalStorage
	}
	return j, nil
}

func newJournal(writer journalWriter, key []byte, at time.Time) (*Journal, error) {
	if writer == nil || len(key) < journalMinKeyBytes || len(key) > journalMaxKeyBytes {
		return nil, ErrJournalConfig
	}
	ownedKey := bytes.Clone(key)
	fail := func() (*Journal, error) { writer.Close(); clear(ownedKey); return nil, ErrJournalStorage }
	var nonce [journalIDBytes]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail()
	}
	header := journalHeader{Version: JournalVersion, JournalID: hex.EncodeToString(nonce[:]), CreatedAt: at}
	mac, err := journalMAC(ownedKey, JournalVersion, "header", header)
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
	return &Journal{writer: writer, key: ownedKey, gate: make(chan struct{}, 1),
		head: JournalCheckpoint{Version: JournalVersion, JournalID: header.JournalID, MAC: mac}}, nil
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
	if !validJournalEvent(event, JournalVersion) || head.Sequence == math.MaxUint64 ||
		head.Sequence >= journalMaxEvents || event.Index != head.Sequence+1 || head.Sequence > 0 && event.Scope != head.Scope {
		return ErrJournalEvent
	}
	event.At = event.At.UTC()
	record := journalRecord{Index: head.Sequence + 1, Previous: head.MAC, Event: event}
	mac, err := journalMAC(j.key, JournalVersion, "event", record)
	if err != nil {
		return ErrJournalEvent
	}
	data, err := encodeJournalLine(journalLine{Kind: "event", Record: &record, MAC: mac})
	if err != nil {
		return ErrJournalEvent
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

// VerifyJournal accepts canonical newline records of this version or e2-http-v1 (read only). The
// optional single expected head detects valid-prefix truncation/rollback. It
// authenticates bytes, not the supplied key's operational authority. A blocking
// Reader must itself support cancellation; no reader goroutine is abandoned.
func VerifyJournal(ctx context.Context, source io.Reader, key []byte, expected ...JournalCheckpoint) (JournalSummary, error) {
	return verifyJournal(ctx, source, key, nil, expected...)
}

// VerifyJournalEvents는 VerifyJournal과 같은 검증을 하고, 기록 전체가 검증됐을 때만 사건 목록을 돌려준다. 하나라도
// 틀리면 사건을 하나도 돌려주지 않는다(일부만 보이고 "정상"으로 읽히지 않게, 작업대 흐름 화면 계약 §4).
func VerifyJournalEvents(ctx context.Context, source io.Reader, key []byte) ([]Event, JournalSummary, error) {
	var events []Event
	summary, err := verifyJournal(ctx, source, key, func(e Event) { events = append(events, e) })
	if err != nil {
		return nil, JournalSummary{}, err
	}
	return events, summary, nil
}

func verifyJournal(ctx context.Context, source io.Reader, key []byte, visit func(Event), expected ...JournalCheckpoint) (JournalSummary, error) {
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
		!knownJournalVersion(line.Header.Version) || !validJournalHex(line.Header.JournalID, journalIDBytes) ||
		!validJournalTime(line.Header.CreatedAt) || !validJournalHex(line.MAC, sha256.Size) {
		return JournalSummary{}, ErrJournalIntegrity
	}
	version := line.Header.Version
	want, err := journalMAC(ownedKey, version, "header", *line.Header)
	if err != nil || !hmac.Equal([]byte(want), []byte(line.MAC)) {
		return JournalSummary{}, ErrJournalIntegrity
	}
	head := JournalCheckpoint{Version: version, JournalID: line.Header.JournalID, MAC: line.MAC}
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
			!validJournalHex(line.MAC, sha256.Size) || !validJournalEvent(line.Record.Event, version) ||
			head.Sequence > 0 && line.Record.Event.Scope != head.Scope {
			return JournalSummary{}, ErrJournalIntegrity
		}
		want, err = journalMAC(ownedKey, version, "event", *line.Record)
		if err != nil || !hmac.Equal([]byte(want), []byte(line.MAC)) {
			return JournalSummary{}, ErrJournalIntegrity
		}
		head.Sequence, head.MAC, head.Scope = line.Record.Index, line.MAC, line.Record.Event.Scope
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

// knownJournalVersion은 검증할 수 있는 판이다. 새 기록은 JournalVersion으로만 쓴다.
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
	if version == VersionV1 && (event.Reason == Withheld || event.ResponseTruncated) {
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
		if event.IntentID <= 0 || event.Sequence == 0 || !validJournalRef(event.CallRef) || !validJournalHex(event.AttemptID, randomRefBytes) {
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
		if event.IntentID < 0 || event.AttemptID != "" || event.Sequence != 0 || event.Decision != "" {
			return false
		}
	case "call_closed":
		if event.IntentID <= 0 || !validJournalRef(event.CallRef) || event.AttemptID != "" || event.Sequence != 0 || event.Decision != "" {
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
	case Complete, Unsupported, PolicyDenied, Closed, AuditFailed, Expired,
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

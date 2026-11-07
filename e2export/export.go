// Package e2export는 닫힌 일반 HTTP 실행의 검증된 감사 투영을 게시한다.
package e2export

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
)

const (
	ManifestFormat  = "abc2lab.e2-runner-manifest/v1"
	ManifestFile    = "manifest.json"
	AttemptsFile    = "attempts.jsonl"
	JournalFile     = "journal.jsonl"
	CheckpointFile  = "checkpoint.json"
	MaxJournalBytes = 256 << 20
)

var (
	ErrConfig   = errors.New("E② 내보내기 구성이 올바르지 않습니다")
	ErrDrain    = errors.New("E② 실행 종료가 확인되지 않았습니다")
	ErrAudit    = errors.New("E② 감사 마감이 확인되지 않았습니다")
	ErrPublish  = errors.New("E② 결과를 게시하지 못했습니다")
	refPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
	taskPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	hexPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	filePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// Config의 참조는 이미 승인된 실제 실행에서 공급한다. 이 타입은 권한을 발급하지 않는다.
type Config struct {
	Execution               *e2proxy.Execution `json:"-"`
	Runner                  *e2runner.Runner   `json:"-"`
	Journal                 *e2proxy.Journal   `json:"-"`
	JournalDirectory        string             `json:"-"`
	JournalName             string             `json:"-"`
	OutputDirectory         string             `json:"-"`
	AuditKey                []byte             `json:"-"`
	ExecutionRef            string             `json:"-"`
	EvalRunID               string             `json:"-"`
	ReservationRef          string             `json:"-"`
	ModelTag                string             `json:"-"`
	ResetGeneration         string             `json:"-"`
	EngineTaskID            string             `json:"-"`
	PolicyFingerprintSHA256 string             `json:"-"`
}

func (Config) String() string   { return "e2export.Config[protected]" }
func (Config) GoString() string { return "e2export.Config[protected]" }

type Manifest struct {
	Format                  string                           `json:"format"`
	RunRef                  string                           `json:"run_ref"`
	EvalRunID               string                           `json:"eval_run_id"`
	ModelTag                string                           `json:"model_tag"`
	ResetGeneration         string                           `json:"reset_generation"`
	ReservationRef          string                           `json:"reservation_ref"`
	EngineTaskID            string                           `json:"engine_task_id"`
	Scope                   transmit.Scope                   `json:"scope"`
	PolicyRevision          string                           `json:"policy_revision"`
	PolicyFingerprintSHA256 string                           `json:"policy_fingerprint_sha256"`
	HeaderMode              e2proxy.HeaderMode               `json:"header_mode"`
	Evaluation              *e2proxy.EvaluationCallerContext `json:"evaluation,omitempty"`
	State                   string                           `json:"state"`
	AttemptsFile            string                           `json:"attempts_file"`
	AttemptsSHA256          string                           `json:"attempts_sha256"`
	Rows                    uint64                           `json:"rows"`
	LastEventIndex          uint64                           `json:"last_event_index"`
	LastAttemptSequence     uint64                           `json:"last_attempt_sequence"`
	AuditJournalFile        string                           `json:"audit_journal_file"`
	AuditCheckpointFile     string                           `json:"audit_checkpoint_file"`
	AuditCheckpointSHA256   string                           `json:"audit_checkpoint_sha256"`
	CloseRequestRef         string                           `json:"close_request_ref"`
}

type Result struct {
	ExecutionRef   string
	ManifestSHA256 string
	Checkpoint     e2proxy.JournalCheckpoint
}

type Publisher struct {
	observeMu           sync.Mutex
	observed            map[string]bool
	observerFailed      bool
	scope               transmit.Scope
	policyRevision      string
	mu                  sync.Mutex
	cfg                 Config
	key                 []byte
	source, output      *exportDirectory
	attempted, disposed bool
	closeRef            string
	result              Result
	resultErr           error
}

func validRef(value string) bool {
	return refPattern.MatchString(value) && !bytes.Contains([]byte(value), []byte("://"))
}

func New(cfg Config) (*Publisher, error) {
	if cfg.Execution == nil || cfg.Runner == nil || cfg.Journal == nil || len(cfg.AuditKey) != 32 || !filePattern.MatchString(cfg.JournalName) ||
		!validRef(cfg.ExecutionRef) || !validRef(cfg.EvalRunID) || !validRef(cfg.ReservationRef) || !validRef(cfg.ModelTag) || !validRef(cfg.ResetGeneration) ||
		!taskPattern.MatchString(cfg.EngineTaskID) || !hexPattern.MatchString(cfg.PolicyFingerprintSHA256) {
		return nil, ErrConfig
	}
	v := cfg.Execution.Snapshot()
	if v.Version != e2proxy.Version || v.Scope.TaskRef != cfg.EngineTaskID || v.Scope.ResetRef != cfg.ResetGeneration || v.Scope.RunRef == cfg.EvalRunID || !validRef(v.Scope.RunRef) {
		return nil, ErrConfig
	}
	if v.HeaderMode == e2proxy.HeaderEvaluationCallerPassthrough {
		if v.Evaluation == nil || v.Evaluation.RunRef != v.Scope.RunRef {
			return nil, ErrConfig
		}
	} else if v.Evaluation != nil {
		return nil, ErrConfig
	}
	source, err := openExportDirectory(cfg.JournalDirectory, false)
	if err != nil {
		return nil, ErrConfig
	}
	output, err := openExportDirectory(cfg.OutputDirectory, true)
	if err != nil {
		source.close()
		return nil, ErrConfig
	}
	if _, err := output.root.Lstat(ManifestFile); err == nil {
		source.close()
		output.close()
		return nil, ErrConfig
	}
	p := &Publisher{cfg: cfg, key: bytes.Clone(cfg.AuditKey), source: source, output: output, observed: make(map[string]bool), scope: v.Scope, policyRevision: v.PolicyRevision}
	p.cfg.AuditKey = nil
	return p, nil
}

// Publish는 실제 Runner·Execution 종료와 감사 검증 뒤 manifest를 마지막에 게시한다.
// 상위 서버는 호출 전에 Planner/Worker writer를 join해야 한다.
func (p *Publisher) Publish(ctx context.Context, closeRef string) (Result, error) {
	if p == nil || ctx == nil || !validRef(closeRef) {
		return Result{}, ErrConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attempted {
		if p.closeRef != closeRef {
			return Result{}, ErrConfig
		}
		return p.result, p.resultErr
	}
	if p.disposed {
		return Result{}, ErrPublish
	}
	p.attempted, p.closeRef = true, closeRef
	p.result, p.resultErr = p.publish(ctx, closeRef)
	return p.result, p.resultErr
}

func (p *Publisher) publish(ctx context.Context, closeRef string) (Result, error) {
	if ctx.Err() != nil || p.cfg.Runner.Close(ctx) != nil {
		return Result{}, ErrDrain
	}
	if p.cfg.Execution.Stop(ctx) != nil {
		return Result{}, ErrAudit
	}
	view := p.cfg.Execution.Snapshot()
	if view.State != "blocked" || (view.Reason != e2proxy.Closed && view.Reason != e2proxy.Revoked) || !view.AuditComplete || view.Scope != p.scope || view.PolicyRevision != p.policyRevision {
		return Result{}, ErrAudit
	}
	p.observeMu.Lock()
	defer p.observeMu.Unlock()
	if p.observerFailed {
		return Result{}, ErrAudit
	}
	for _, call := range p.cfg.Execution.Calls() {
		if !p.observed[call.CallRef] || !call.Final || !call.AuditComplete {
			return Result{}, ErrDrain
		}
	}
	head := p.cfg.Journal.Checkpoint()
	if p.cfg.Journal.Close() != nil || p.cfg.Journal.Checkpoint() != head {
		return Result{}, ErrAudit
	}
	journal, err := p.source.read(p.cfg.JournalName, MaxJournalBytes)
	if err != nil {
		return Result{}, ErrAudit
	}
	verified, err := e2proxy.VerifyJournal(ctx, bytes.NewReader(journal), p.key, head)
	if err != nil || verified.Checkpoint != head || head.Sequence > 0 && head.Scope != view.Scope {
		return Result{}, ErrAudit
	}
	attempts, rows, lastSequence, err := project(journal)
	if err != nil || rows != head.Sequence {
		return Result{}, ErrAudit
	}
	checkpoint, err := json.Marshal(head)
	if err != nil {
		return Result{}, ErrPublish
	}
	checkpoint = append(checkpoint, '\n')
	manifest := Manifest{Format: ManifestFormat, RunRef: view.Scope.RunRef, EvalRunID: p.cfg.EvalRunID, ModelTag: p.cfg.ModelTag, ResetGeneration: p.cfg.ResetGeneration,
		ReservationRef: p.cfg.ReservationRef, EngineTaskID: p.cfg.EngineTaskID, Scope: view.Scope, PolicyRevision: view.PolicyRevision, PolicyFingerprintSHA256: p.cfg.PolicyFingerprintSHA256,
		HeaderMode: view.HeaderMode, Evaluation: view.Evaluation, State: "complete", AttemptsFile: AttemptsFile, AttemptsSHA256: digest(attempts), Rows: rows, LastEventIndex: rows,
		LastAttemptSequence: lastSequence, AuditJournalFile: JournalFile, AuditCheckpointFile: CheckpointFile, AuditCheckpointSHA256: digest(checkpoint), CloseRequestRef: closeRef}
	data, err := json.Marshal(manifest)
	if err != nil {
		return Result{}, ErrPublish
	}
	data = append(data, '\n')
	if ctx.Err() != nil {
		return Result{}, ErrPublish
	}
	// 같은 디렉터리의 원본 저널은 검증한 동일 파일을 그대로 쓴다.
	if !p.source.same(p.output) || p.cfg.JournalName != JournalFile {
		if p.output.write(JournalFile, journal) != nil {
			return Result{}, ErrPublish
		}
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{AttemptsFile, attempts}, {CheckpointFile, checkpoint}, {ManifestFile, data}} {
		if ctx.Err() != nil || p.output.write(file.name, file.data) != nil {
			return Result{}, ErrPublish
		}
	}
	return Result{ExecutionRef: p.cfg.ExecutionRef, ManifestSHA256: digest(data), Checkpoint: head}, nil
}

func project(journal []byte) ([]byte, uint64, uint64, error) {
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	scanner.Buffer(make([]byte, 16384), 16385)
	if !scanner.Scan() {
		return nil, 0, 0, ErrAudit
	}
	var out bytes.Buffer
	var rows, lastSequence uint64
	for scanner.Scan() {
		var line struct {
			Kind   string
			Record *struct{ Event json.RawMessage }
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Kind != "event" || line.Record == nil {
			return nil, 0, 0, ErrAudit
		}
		var event e2proxy.Event
		if json.Unmarshal(line.Record.Event, &event) != nil || event.Index != rows+1 {
			return nil, 0, 0, ErrAudit
		}
		rows++
		if event.Sequence > lastSequence {
			lastSequence = event.Sequence
		}
		out.Write(line.Record.Event)
		out.WriteByte('\n')
	}
	if scanner.Err() != nil {
		return nil, 0, 0, ErrAudit
	}
	return out.Bytes(), rows, lastSequence, nil
}

// ObserveResult는 concrete Runner가 반환한 신뢰 필드만 보관하고 도구 원문은 버린다.
func (p *Publisher) ObserveResult(ctx context.Context, result e2runner.Result) error {
	if p == nil || ctx == nil {
		return ErrAudit
	}
	trusted := result.Trusted
	if trusted.Call.CallRef == "" {
		if sameCallResult(trusted.Call, e2proxy.CallResult{}) && !trusted.Final && !trusted.ProcessBashExited && !trusted.ProcessDrained && !trusted.ProxyDrained {
			return nil
		}
		return ErrAudit
	}
	view := p.cfg.Execution.Snapshot()
	valid := trusted.Version == e2proxy.Version && trusted.Execution.Scope == view.Scope && trusted.Execution.HeaderMode == view.HeaderMode &&
		trusted.Call.IntentID > 0 && p.cfg.Execution.HasIntent(trusted.Call.IntentID) && trusted.Final && trusted.Call.Final && trusted.Call.AuditComplete &&
		trusted.ProcessBashExited && trusted.ProcessDrained && trusted.ProxyDrained
	found := false
	for _, call := range p.cfg.Execution.Calls() {
		if call.CallRef == trusted.Call.CallRef && sameCallResult(call, trusted.Call) {
			found = true
			break
		}
	}
	p.observeMu.Lock()
	defer p.observeMu.Unlock()
	if !valid || !found {
		p.observerFailed = true
		return ErrAudit
	}
	p.observed[trusted.Call.CallRef] = true
	return nil
}

// ObserveEvent는 원래 저널의 동기 ACK 뒤에만 호출한다. 투영 원본은 이 callback이 아니다.
func (p *Publisher) ObserveEvent(ctx context.Context, event e2proxy.Event) error {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return ErrAudit
	}
	if event.Version != e2proxy.Version || event.Scope != p.scope || event.PolicyRevision != p.policyRevision {
		return ErrAudit
	}
	return nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Dispose는 이 게시자가 보유한 검증 키 사본과 디렉터리 FD만 해제한다.
func (p *Publisher) Dispose() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return
	}
	p.disposed = true
	clear(p.key)
	p.source.close()
	p.output.close()
}
func (*Publisher) String() string   { return "e2export.Publisher[private]" }
func (*Publisher) GoString() string { return "e2export.Publisher[private]" }

// 관측/기술 slice까지 비교해 감사 마감 뒤 바뀐 진단 projection을 받아들이지 않는다.
func sameCallResult(left, right e2proxy.CallResult) bool {
	return e2proxy.EqualCallResults(left, right)
}

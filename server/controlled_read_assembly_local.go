// 첫 단일 호스트의 기존 설치 권위를 소비한다. 새 epoch·키·승인을 발급하지 않는다.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/controlledhost"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var ErrControlledReadLocalHost = errors.New("첫 진단 호스트 권위를 사용할 수 없습니다")

// Supervisor는 실제 서버의 실행 소유·자식 수명 검사를 제공한다. 설정 파일의 주장은 대체물이 아니다.
type ControlledReadLocalSupervisor interface {
	CheckTaskQuiescent(context.Context, int64) error
	AuthorizeExecution(context.Context, transmit.Scope, int64) error
}

type ControlledReadLocalHostConfig struct {
	BindingFile string
	Supervisor  ControlledReadLocalSupervisor
	Limits      auditlog.ExtensionLimits
}

// ControlledReadLocalBinding은 첫 단일 호스트 공급부의 팀 검토용 초안이다.
// 설치자가 기존 epoch.anchor_digest에 원문 SHA256을 등록한 비밀 아닌 승인 자료를 소비한다.
// 디렉터리 inode·키 해시는 독립 DB 앵커와 배타적 수명 안에서만 의미가 있다.
type ControlledReadLocalBinding struct {
	Version      int                          `json:"version"`
	StoreRef     string                       `json:"store_ref"`
	EpochRef     string                       `json:"epoch_ref"`
	Generation   uint64                       `json:"generation"`
	PublisherRef string                       `json:"publisher_ref"`
	ExpiresAt    time.Time                    `json:"expires_at"`
	State        ControlledReadLocalDirectory `json:"state"`
	Journals     ControlledReadLocalDirectory `json:"journals"`
	Descriptors  ControlledReadLocalDirectory `json:"descriptors"`
	Keys         ControlledReadLocalDirectory `json:"keys"`
	Key          ControlledReadLocalKey       `json:"key"`
	Execution    ControlledReadLocalExecution `json:"execution"`
	PolicySHA256 []string                     `json:"policy_sha256"`
	Files        []ControlledReadLocalFile    `json:"files"`
}
type ControlledReadLocalDirectory struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type ControlledReadLocalFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type ControlledReadLocalKey struct {
	Namespace        string `json:"namespace"`
	PolicyRef        string `json:"policy_ref"`
	KeyRef           string `json:"key_ref"`
	SecretVersionRef string `json:"secret_version_ref"`
	Identity         string `json:"identity"`
	PredecessorRef   string `json:"predecessor_ref"`
	Revision         uint64 `json:"revision"`
	File             string `json:"file"`
	SHA256           string `json:"sha256"`
}
type ControlledReadLocalExecution struct {
	TaskID    int64  `json:"task_id"`
	IntentID  int64  `json:"intent_id"`
	CaseRef   string `json:"case_ref"`
	ResetRef  string `json:"reset_ref"`
	PolicyRef string `json:"policy_ref"`
}

type ControlledReadLocalHost struct {
	ctx             context.Context
	cancel          context.CancelFunc
	binding         ControlledReadLocalBinding
	epoch           auditcheckpoint.Epoch
	limits          auditlog.ExtensionLimits
	supervisor      ControlledReadLocalSupervisor
	runtimeDB       *sql.DB
	authorityDB     *sql.DB
	physical        *controlledLocalPhysical
	key             []byte
	attestation     auditkeys.Attestation
	authorityTx     *sql.Tx
	resourceCtx     context.Context
	resourceCancel  context.CancelFunc
	txMu            sync.Mutex
	mu              sync.Mutex
	closed          bool
	active          int
	sourcesIssued   bool
	scope           transmit.Scope
	intent          int64
	journalID       string
	publisherIssued bool
	drained         chan struct{}
	cleanupOnce     sync.Once
	cleanupDone     chan struct{}
	cleanupErr      error
}

type controlledLocalNamespace struct {
	parents []*os.File
	parts   []string
	target  *os.File
}

type controlledLocalPhysical struct {
	state, journals, descriptors, keys, writer *os.File
	namespaces                                 []*controlledLocalNamespace
}

// Acquire는 기존 행을 읽으며 키 상태·epoch·승인 행을 새로 만들지 않는다.
// authorityDB는 행 잠금용 설치 권위 접속이며 runtimeDB는 M03 제한 역할이다.
func AcquireControlledReadLocalHost(ctx context.Context, runtimeDB, authorityDB *sql.DB, cfg ControlledReadLocalHostConfig) (*ControlledReadLocalHost, error) {
	if ctx == nil || ctx.Err() != nil || runtimeDB == nil || authorityDB == nil || nilFinalizationValue(cfg.Supervisor) || cfg.BindingFile == "" || !validLocalLimits(cfg.Limits) || authorityDB.Stats().MaxOpenConnections == 1 {
		return nil, ErrControlledReadLocalHost
	}
	raw, err := privateinput.ReadFile(cfg.BindingFile, 128<<10)
	if err != nil {
		return nil, ErrControlledReadLocalHost
	}
	defer clear(raw)
	binding, err := decodeControlledReadLocalBinding(raw, time.Now())
	if err != nil || CheckFirstDiagnosisRuntimeDatabase(ctx, runtimeDB) != nil || CheckFirstDiagnosisAuthorityDatabase(ctx, authorityDB) != nil {
		return nil, ErrControlledReadLocalHost
	}
	digest := sha256.Sum256(raw)
	life, cancel := context.WithDeadline(ctx, binding.ExpiresAt)
	h := &ControlledReadLocalHost{ctx: life, cancel: cancel, binding: binding, limits: cfg.Limits, supervisor: cfg.Supervisor, runtimeDB: runtimeDB, authorityDB: authorityDB, drained: make(chan struct{}), cleanupDone: make(chan struct{})}
	success := false
	defer func() {
		if !success {
			h.Revoke()
			if h.authorityTx != nil {
				_ = h.authorityTx.Rollback()
			}
			if h.resourceCancel != nil {
				h.resourceCancel()
			}
			if h.physical != nil {
				_ = closeControlledLocalPhysical(h.physical)
			}
			clear(h.key)
		}
	}()
	// 자원 해제까지 행 잠금을 유지한다. 작업 권한은 항상 life에서 전파한다.
	resourceCtx, resourceCancel := context.WithCancel(context.Background())
	h.resourceCtx = resourceCtx
	h.resourceCancel = resourceCancel
	stopStartup := context.AfterFunc(ctx, resourceCancel)
	defer stopStartup()
	h.authorityTx, err = authorityDB.BeginTx(resourceCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, ErrControlledReadLocalHost
	}
	if err = h.pinDatabase(ctx, digest); err != nil {
		return nil, ErrControlledReadLocalHost
	}
	h.physical, err = openControlledLocalPhysical(binding, digest)
	if err != nil {
		return nil, ErrControlledReadLocalHost
	}
	h.key, err = privateinput.ReadFile(binding.Key.File, auditlog.KeyBytes+1)
	if err != nil || len(h.key) != auditlog.KeyBytes || hex.EncodeToString(sha256Bytes(h.key)) != binding.Key.SHA256 {
		return nil, ErrControlledReadLocalHost
	}
	if h.CheckRoot(ctx) != nil {
		return nil, ErrControlledReadLocalHost
	}
	if !stopStartup() || resourceCtx.Err() != nil || life.Err() != nil {
		return nil, ErrControlledReadLocalHost
	}
	success = true
	go h.watchDatabase()
	return h, nil
}

func sha256Bytes(data []byte) []byte { s := sha256.Sum256(data); return s[:] }
func validLocalLimits(v auditlog.ExtensionLimits) bool {
	return v.MaxBytes > 0 && v.MaxBytes <= auditlog.MaxExtensionBytes && v.MaxEvents > 0 && v.MaxEvents <= auditlog.MaxExtensionEvents && v.Timeout > 0 && v.Timeout <= auditlog.MaxExtensionTimeout
}

func decodeControlledReadLocalBinding(raw []byte, now time.Time) (ControlledReadLocalBinding, error) {
	var b ControlledReadLocalBinding
	if strictJSON(raw, &b) != nil || b.Version != 1 || !localRef(b.StoreRef) || !localRef(b.EpochRef) || b.Generation == 0 || b.Generation > math.MaxInt64 || !localRef(b.PublisherRef) || !b.ExpiresAt.After(now) || b.ExpiresAt.After(now.Add(15*time.Minute)) {
		return b, ErrControlledReadLocalHost
	}
	// 승인된 원문에서 공백·중복 키 등 다른 표현으로 분기하지 않는다.
	canonical, err := json.Marshal(b)
	if err != nil || !bytes.Equal(raw, canonical) {
		return b, ErrControlledReadLocalHost
	}
	dirs := []ControlledReadLocalDirectory{b.State, b.Journals, b.Descriptors, b.Keys}
	for _, d := range dirs {
		if !localCleanPath(d.Path) || d.Device == 0 || d.Inode == 0 {
			return b, ErrControlledReadLocalHost
		}
	}
	if b.Journals.Path != filepath.Join(b.State.Path, "journals") || localWithin(b.Keys.Path, b.State.Path) || localWithin(b.State.Path, b.Keys.Path) || localWithin(b.Keys.Path, b.Descriptors.Path) || localWithin(b.Descriptors.Path, b.Keys.Path) {
		return b, ErrControlledReadLocalHost
	}
	k := b.Key
	for _, ref := range []string{k.Namespace, k.PolicyRef, k.KeyRef, k.SecretVersionRef, k.Identity} {
		if !localRef(ref) {
			return b, ErrControlledReadLocalHost
		}
	}
	if k.PredecessorRef != "" && !localRef(k.PredecessorRef) || k.Revision == 0 || k.Revision > math.MaxInt64 || !localCleanPath(k.File) || !localWithin(k.File, b.Keys.Path) || !validHex(k.SHA256, 64) {
		return b, ErrControlledReadLocalHost
	}
	e := b.Execution
	if e.TaskID <= 0 || e.IntentID <= 0 || !localRef(e.CaseRef) || !localRef(e.ResetRef) || !localRef(e.PolicyRef) {
		return b, ErrControlledReadLocalHost
	}
	if len(b.PolicySHA256) > 32 || len(b.Files) == 0 || len(b.Files) > 256 {
		return b, ErrControlledReadLocalHost
	}
	seen := map[string]bool{}
	for _, d := range b.PolicySHA256 {
		if !validHex(d, 64) || seen[d] {
			return b, ErrControlledReadLocalHost
		}
		seen[d] = true
	}
	seen = map[string]bool{}
	for _, f := range b.Files {
		if !localCleanPath(f.Path) || !validHex(f.SHA256, 64) || seen[f.Path] {
			return b, ErrControlledReadLocalHost
		}
		seen[f.Path] = true
	}
	return b, nil
}
func localCleanPath(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/"
}
func localWithin(path, root string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !filepath.IsAbs(r) && !(len(r) > 3 && r[:3] == "../")
}

func (h *ControlledReadLocalHost) pinDatabase(ctx context.Context, digest [32]byte) error {
	tx := h.authorityTx
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL idle_in_transaction_session_timeout=0`); err != nil {
		return err
	}
	var runtimeRole, runtimeSession, authorityRole, authoritySession string
	if h.runtimeDB.QueryRowContext(ctx, `SELECT current_user,session_user`).Scan(&runtimeRole, &runtimeSession) != nil || tx.QueryRowContext(ctx, `SELECT current_user,session_user`).Scan(&authorityRole, &authoritySession) != nil || runtimeRole != runtimeSession || authorityRole != authoritySession || runtimeRole == authorityRole {
		return ErrControlledReadLocalHost
	}
	var runtimeIdentity, authorityIdentity string
	const identity = `SELECT current_database()||':'||current_schema()||':'||(SELECT oid::text FROM pg_database WHERE datname=current_database())||':'||pg_postmaster_start_time()::text||':'||COALESCE(inet_server_addr()::text,'local')||':'||current_setting('port') WHERE current_setting('server_version_num')::int BETWEEN 160000 AND 169999`
	if h.runtimeDB.QueryRowContext(ctx, identity).Scan(&runtimeIdentity) != nil || tx.QueryRowContext(ctx, identity).Scan(&authorityIdentity) != nil || runtimeIdentity != authorityIdentity {
		return ErrControlledReadLocalHost
	}
	var epochRef string
	var generation int64
	var anchor []byte
	if tx.QueryRowContext(ctx, `SELECT epoch_ref,generation,anchor_digest FROM audit_checkpoint_epochs WHERE store_ref=$1 FOR SHARE`, h.binding.StoreRef).Scan(&epochRef, &generation, &anchor) != nil || epochRef != h.binding.EpochRef || generation != int64(h.binding.Generation) || !bytes.Equal(anchor, digest[:]) {
		return ErrControlledReadLocalHost
	}
	// 새 회차의 단회 기동이다. 기존 회차의 복원·재개 권한은 제공하지 않는다.
	var count int64
	if tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM audit_checkpoint_history WHERE store_ref=$1)+(SELECT count(*) FROM audit_checkpoint_operations WHERE store_ref=$1)`, h.binding.StoreRef).Scan(&count) != nil || count != 0 {
		return ErrControlledReadLocalHost
	}
	k := h.binding.Key
	var binding auditkeys.Binding
	var state string
	var revision int64
	if tx.QueryRowContext(ctx, `SELECT key_ref,secret_version_ref,secret_identity,COALESCE(predecessor_ref,''),state,revision FROM audit_key_registry_keys WHERE namespace=$1 AND key_ref=$2 FOR SHARE`, k.Namespace, k.KeyRef).Scan(&binding.KeyRef, &binding.SecretVersionRef, &binding.Identity, &binding.PredecessorRef, &state, &revision) != nil || binding != (auditkeys.Binding{KeyRef: k.KeyRef, SecretVersionRef: k.SecretVersionRef, Identity: k.Identity, PredecessorRef: k.PredecessorRef}) || state != string(auditkeys.Active) || revision != int64(k.Revision) {
		return ErrControlledReadLocalHost
	}
	a := &h.attestation
	var policy string
	var policyRevision int64
	if tx.QueryRowContext(ctx, `SELECT actor_ref,policy_ref,policy_revision,mapping_ref,authorization_ref,activation_ref,drain_ref,dependency_ref,copy_lifetime_ref,disposal_ref,unused_ref FROM audit_key_registry_history WHERE namespace=$1 AND key_ref=$2 AND revision=$3 FOR SHARE`, k.Namespace, k.KeyRef, revision).Scan(&a.ActorRef, &policy, &policyRevision, &a.MappingRef, &a.AuthorizationRef, &a.ActivationRef, &a.DrainRef, &a.DependencyRef, &a.CopyLifetimeRef, &a.DisposalRef, &a.UnusedRef) != nil || policy != k.PolicyRef || policyRevision <= 0 {
		return ErrControlledReadLocalHost
	}
	for _, r := range []string{a.ActorRef, a.MappingRef, a.AuthorizationRef, a.ActivationRef} {
		if !localRef(r) {
			return ErrControlledReadLocalHost
		}
	}
	a.Identity = k.Identity
	a.PolicyRevision = uint64(policyRevision)
	h.epoch = auditcheckpoint.Epoch{StoreRef: h.binding.StoreRef, EpochRef: epochRef, Generation: uint64(generation), AnchorDigest: digest}
	return nil
}

func (h *ControlledReadLocalHost) Context() context.Context {
	if h == nil {
		return nil
	}
	return h.ctx
}
func (h *ControlledReadLocalHost) Anchor() *os.File {
	if h == nil || h.physical == nil {
		return nil
	}
	return h.physical.state
}
func (h *ControlledReadLocalHost) Directory() string {
	if h == nil {
		return ""
	}
	return h.binding.State.Path
}
func (h *ControlledReadLocalHost) CheckRoot(ctx context.Context) error {
	if h == nil || ctx == nil || ctx.Err() != nil || h.ctx == nil || h.ctx.Err() != nil || h.physical == nil {
		return ErrControlledReadLocalHost
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return ErrControlledReadLocalHost
	}
	if checkControlledLocalPhysical(h.physical, h.binding) != nil {
		h.Revoke()
		return ErrControlledReadLocalHost
	}
	return nil
}
func (h *ControlledReadLocalHost) CheckTaskQuiescent(ctx context.Context, id int64) error {
	t, err := h.enter(ctx)
	if err != nil {
		return err
	}
	defer t.Release()
	if id != h.binding.Execution.TaskID || h.supervisor.CheckTaskQuiescent(t.ctx, id) != nil {
		return ErrControlledReadLocalHost
	}
	return h.CheckRoot(t.ctx)
}
func (h *ControlledReadLocalHost) AuthorizePolicy(ctx context.Context, digest [32]byte) error {
	if h.CheckRoot(ctx) != nil {
		return ErrControlledReadLocalHost
	}
	v := hex.EncodeToString(digest[:])
	for _, d := range h.binding.PolicySHA256 {
		if d == v {
			return nil
		}
	}
	return ErrControlledReadLocalHost
}
func (h *ControlledReadLocalHost) AuthorizeInput(ctx context.Context, path string, digest [32]byte) error {
	if h.CheckRoot(ctx) != nil {
		return ErrControlledReadLocalHost
	}
	v := hex.EncodeToString(digest[:])
	for _, f := range h.binding.Files {
		if f.Path == path && f.SHA256 == v {
			return nil
		}
	}
	return ErrControlledReadLocalHost
}
func (h *ControlledReadLocalHost) AuthorizeExecution(ctx context.Context, s transmit.Scope, intent int64) error {
	t, err := h.enter(ctx)
	if err != nil {
		return err
	}
	defer t.Release()
	ctx = t.ctx
	e := h.binding.Execution
	if s.TaskRef != strconv.FormatInt(e.TaskID, 10) || intent != e.IntentID || s.CaseRef != e.CaseRef || s.ResetRef != e.ResetRef || s.PolicyRef != e.PolicyRef || !safeFile.MatchString(s.RunRef) {
		return ErrControlledReadLocalHost
	}
	if h.supervisor.AuthorizeExecution(ctx, s, intent) != nil || h.CheckRoot(ctx) != nil {
		return ErrControlledReadLocalHost
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx.Err() != nil || h.scope != (transmit.Scope{}) && (h.scope != s || h.intent != intent) {
		return ErrControlledReadLocalHost
	}
	h.scope = s
	h.intent = intent
	return nil
}

func (h *ControlledReadLocalHost) enter(ctx context.Context) (*controlledLocalTicket, error) {
	if h.CheckRoot(ctx) != nil {
		return nil, ErrControlledReadLocalHost
	}
	h.mu.Lock()
	if h.closed || h.ctx.Err() != nil {
		h.mu.Unlock()
		return nil, ErrControlledReadLocalHost
	}
	h.active++
	h.mu.Unlock()
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.ctx, cancel)
	t := &controlledLocalTicket{ctx: call, h: h, finish: func() { stop(); cancel() }}
	if call.Err() != nil || h.CheckRoot(call) != nil {
		t.Release()
		return nil, ErrControlledReadLocalHost
	}
	return t, nil
}

type controlledLocalTicket struct {
	ctx    context.Context
	h      *ControlledReadLocalHost
	once   sync.Once
	finish func()
}

func (t *controlledLocalTicket) Context() context.Context { return t.ctx }
func (t *controlledLocalTicket) Release() {
	t.once.Do(func() {
		t.finish()
		t.h.mu.Lock()
		t.h.active--
		if t.h.closed && t.h.active == 0 {
			close(t.h.drained)
		}
		t.h.mu.Unlock()
	})
}
func (h *ControlledReadLocalHost) Revoke() {
	if h == nil || h.cancel == nil {
		return
	}
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		if h.active == 0 {
			close(h.drained)
		}
	}
	h.mu.Unlock()
	h.cancel()
}

// Close의 시간 초과는 자원 해제를 뜻하지 않는다. 남은 ticket 반환까지 DB·FD·키를 소유한다.
func (h *ControlledReadLocalHost) Close(ctx context.Context) error {
	if h == nil || ctx == nil {
		return ErrControlledReadLocalHost
	}
	h.Revoke()
	h.cleanupOnce.Do(func() {
		go func() {
			<-h.drained
			h.txMu.Lock()
			if h.authorityTx != nil {
				if err := h.authorityTx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					h.cleanupErr = ErrControlledReadLocalHost
				}
			}
			h.txMu.Unlock()
			if h.resourceCancel != nil {
				h.resourceCancel()
			}
			if closeControlledLocalPhysical(h.physical) != nil {
				h.cleanupErr = ErrControlledReadLocalHost
			}
			clear(h.key)
			close(h.cleanupDone)
		}()
	})
	select {
	case <-h.cleanupDone:
		return h.cleanupErr
	default:
	}
	select {
	case <-h.cleanupDone:
		return h.cleanupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *ControlledReadLocalHost) watchDatabase() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(h.resourceCtx, 2*time.Second)
			h.txMu.Lock()
			var one int
			err := h.authorityTx.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
			h.txMu.Unlock()
			cancel()
			if err != nil || one != 1 {
				h.Revoke()
				return
			}
		}
	}
}

type controlledLocalRoot struct{ *controlledLocalTicket }

func (r *controlledLocalRoot) Anchor() *os.File  { return r.h.Anchor() }
func (r *controlledLocalRoot) Directory() string { return r.h.Directory() }
func (r *controlledLocalRoot) CheckRoot(ctx context.Context) error {
	if r.ctx.Err() != nil {
		return ErrControlledReadLocalHost
	}
	return r.h.CheckRoot(ctx)
}
func (r *controlledLocalRoot) CheckTaskQuiescent(ctx context.Context, id int64) error {
	if r.ctx.Err() != nil {
		return ErrControlledReadLocalHost
	}
	return r.h.CheckTaskQuiescent(ctx, id)
}
func (h *ControlledReadLocalHost) Sources() (controlledhost.Sources, error) {
	if h == nil {
		return controlledhost.Sources{}, ErrControlledReadLocalHost
	}
	t, err := h.enter(h.ctx)
	if err != nil {
		return controlledhost.Sources{}, err
	}
	h.mu.Lock()
	if h.sourcesIssued {
		h.mu.Unlock()
		t.Release()
		return controlledhost.Sources{}, ErrControlledReadLocalHost
	}
	h.sourcesIssued = true
	h.mu.Unlock()
	return controlledhost.Sources{Root: &controlledLocalRoot{t}, Keys: controlledLocalKeys{h}, Checkpoints: controlledLocalCheckpoints{h}, Revisions: controlledLocalRevisions{h}, DescriptorRoots: controlledLocalDescriptors{h}}, nil
}

func localRef(s string) bool { return safeRef.MatchString(s) && !strings.Contains(s, "://") }

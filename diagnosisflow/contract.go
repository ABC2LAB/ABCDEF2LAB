// Package diagnosisflow는 프로그램 소유 탐색·선택의 추가 제한을 관리한다.
// 대상 권위는 발급하지 않으며 E②의 기존 범위·예산을 확대하지 않는다.
package diagnosisflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"time"
)

const Version = "abc2lab.workbench-flow/v1"
const Auto = "ai_auto_v1"
const Manual = "user_approval_v1"

var ErrUnavailable = errors.New("workflow_unavailable")
var ErrDenied = errors.New("workflow_request_denied")
var ErrConflict = errors.New("workflow_revision_conflict")
var ErrProjection = e2proxy.ErrBoundaryProjection

type Limits struct {
	Format                string `json:"format"`
	MaxPages              int    `json:"max_pages"`
	MaxEndpoints          int    `json:"max_endpoints"`
	MaxResponseBytes      int    `json:"max_response_bytes"`
	AllowReversibleWrites bool   `json:"allow_reversible_writes"`
}

func (l Limits) Valid() bool {
	return l.Format == Version && l.MaxPages >= 1 && l.MaxPages <= 32 && l.MaxEndpoints >= 1 && l.MaxEndpoints <= 32 && l.MaxResponseBytes >= 4096 && l.MaxResponseBytes <= 2<<20
}
func ValidMode(mode string) bool { return mode == Auto || mode == Manual }

type Endpoint struct {
	DiscoveryConfidence   string `json:"discovery_confidence,omitempty"`
	ExplorationSessionRef string `json:"exploration_session_ref,omitempty"`
	ExplorationSeq        uint64 `json:"exploration_seq,omitempty"`
	Transport             string `json:"transport,omitempty"`
	ObservationError      string `json:"observation_error,omitempty"`
	ID                    string `json:"id"`
	Method                string `json:"method"`
	Path                  string `json:"path"`
	Kind                  string `json:"kind"`
	Source                string `json:"source"`
	From                  string `json:"from"`
	InScope               bool   `json:"in_scope"`
	Change                string `json:"change"`
	Visited               bool   `json:"visited"`
	Status                int    `json:"status,omitempty"`
	Selected              bool   `json:"selected"`
	Duplicates            int    `json:"duplicates"`
	CallRef               string `json:"call_ref,omitempty"`
	Request               string `json:"request,omitempty"`
	Response              string `json:"response,omitempty"`
	// 탐색 실행기가 보고한 요청 모양(탐색 계약 §7). 이름만 있고 값은 없다.
	QueryKeys   []string `json:"query_keys,omitempty"`
	BodyKeys    []string `json:"body_keys,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
}
type Event struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Method string    `json:"method,omitempty"`
	Path   string    `json:"path,omitempty"`
	Status int       `json:"status,omitempty"`
}
type Counts struct {
	InventoryStored     int    `json:"inventory_stored,omitempty"`
	InventoryOmitted    uint64 `json:"inventory_omitted,omitempty"`
	ExplorationRequests uint64 `json:"exploration_requests,omitempty"`
	Found               int    `json:"found"`
	Visited             int    `json:"visited"`
	Queued              int    `json:"queued"`
	Sent                uint64 `json:"sent"`
	Attempted           uint64 `json:"attempted"`
	Unknown             uint64 `json:"unknown"`
	Blocked             uint64 `json:"blocked"`
	Held                uint64 `json:"held,omitempty"`
	Omitted             uint64 `json:"omitted,omitempty"`
	InvalidPaths        uint64 `json:"invalid_paths,omitempty"`
	// 탐색 실행기 보고 중 형식 오류·범위 밖이라 버린 수(탐색 실행기 결정안 §2-3).
	DiscoveriesDropped uint64 `json:"discoveries_dropped,omitempty"`
}
type Command struct {
	Key       string `json:"key"`
	Operation string `json:"operation"`
	Revision  uint64 `json:"revision"`
}
type Plan struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Title string `json:"title"`
}
type Activity struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Worker  string    `json:"worker"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
}
type Snapshot struct {
	ExplorationStatus  string     `json:"exploration_status,omitempty"`
	ExplorationReason  string     `json:"exploration_reason,omitempty"`
	ExplorationPartial bool       `json:"exploration_partial,omitempty"`
	Failure            string     `json:"failure,omitempty"`
	DetailTruncated    bool       `json:"detail_truncated,omitempty"`
	ReversibleWrites   bool       `json:"reversible_writes"`
	Commands           []Command  `json:"commands"`
	Plan               []Plan     `json:"plan,omitempty"`
	Activity           []Activity `json:"activity,omitempty"`
	Format             string     `json:"format"`
	JobID              string     `json:"job_id"`
	TaskID             string     `json:"task_id"`
	RunRef             string     `json:"run_ref"`
	SourceDigest       string     `json:"source_digest"`
	Revision           uint64     `json:"revision"`
	Mode               string     `json:"mode"`
	Stage              string     `json:"stage"`
	Paused             bool       `json:"paused"`
	DeepRuns           uint64     `json:"deep_runs"`
	DeepClosed         uint64     `json:"deep_closed"`
	InitialCompleted   bool       `json:"initial_completed"`
	CurrentPath        string     `json:"current_path,omitempty"`
	Limits             Limits     `json:"limits"`
	Counts             Counts     `json:"counts"`
	Endpoints          []Endpoint `json:"endpoints"`
	Events             []Event    `json:"events"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
type commandReceipt struct {
	Fingerprint string `json:"fingerprint"`
	Operation   string `json:"operation"`
}
type Record struct {
	Exploration        *explorationState         `json:"exploration,omitempty"`
	DiscoveryInventory []DiscoveryEvidence       `json:"discovery_inventory,omitempty"`
	Snapshot           Snapshot                  `json:"snapshot"`
	Owner              string                    `json:"owner"`
	Commands           map[string]commandReceipt `json:"commands"`
}
type Store interface {
	Create(context.Context, Record) error
	Save(context.Context, uint64, Record) error
	Read(context.Context, string, string) (Record, error)
}
type Binding struct {
	TargetRef                                                                string
	TargetRevision                                                           uint64
	JobID, TaskID, RunRef, SourceDigest, InputDigest, Owner, Mode, TargetURL string
	PathPrefixes, Methods, Excluded                                          []string
	ReadonlyPrefixes                                                         []string
	ReversibleWrites                                                         bool
	Limits                                                                   Limits
	Current                                                                  func() error
}
type Fetch func(context.Context, string) (e2runner.Result, error)

func endpointID(origin, method, path string) string {
	sum := sha256.Sum256([]byte(origin + "\x00" + method + "\x00" + path))
	return "endpoint-" + hex.EncodeToString(sum[:12])
}
func clone(r Record) Record {
	raw, _ := json.Marshal(r)
	var out Record
	_ = json.Unmarshal(raw, &out)
	return out
}

// Boundary는 요청 파서의 정규화된 값만 받는다. 모델 stdout을 전송 근거로 삼지 않는다.
var _ e2proxy.RequestBoundary = (*Controller)(nil)

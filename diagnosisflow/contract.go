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

// MaxWorkflowPages는 크롤 페이지 수의 코드 상한이다. 페이지마다 요청 하나라 송신량은 E② 요청 예산이 묶고(제품은
// 요청 예산/2로 더 줄임, product_workflow.go), 페이지 크기는 MaxResponseBytes가 묶는다. 그래서 탐색 인벤토리 상한과
// 같은 256으로 둔다(VAmPI 실제 회차 24b).
const MaxWorkflowPages = 256

// MaxWorkflowEndpoints는 공개 끝점 목록(모델 지시·화면 선택 목록)의 개수 코드 상한이다. 송신량이 아니라 발견만
// 자르는 상한이라 탐색 인벤토리 상한과 같은 256으로 두고, 실제로는 목록이 공개 투영(MaxProjectionBytes 48KiB)에
// 들어가는 만큼만 받는다(endpointFitsLocked, 개발 계획 "끝점 바이트 절단"). 넘쳐 빠진 수는 counts.omitted에 남는다.
const MaxWorkflowEndpoints = 256

func (l Limits) Valid() bool {
	return l.Format == Version && l.MaxPages >= 1 && l.MaxPages <= MaxWorkflowPages && l.MaxEndpoints >= 1 && l.MaxEndpoints <= MaxWorkflowEndpoints && l.MaxResponseBytes >= 4096 && l.MaxResponseBytes <= 2<<20
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
	// CallRef·AttemptID는 대상에 요청을 보낸 줄(kind=request)에만 붙는 E② 호출·시도 식별자다. 작업대가 이 둘로 요청 상세
	// (e2-flow/requests/{call_ref}/{attempt_id})를 연다. 다시 보낸 요청은 시도마다 다른 attempt_id를 갖는다.
	CallRef   string `json:"call_ref,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
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
	// 고른 끝점 중 모델 지시 목록의 바이트 상한(MaxSelectedDataBytes) 때문에 빠진 수다. 회차 결과에 늘 남긴다
	// (2026-11-16 사용자 결정, 공식 프로토콜 고정 전에 상한을 다시 정한다).
	ModelOmitted uint64 `json:"model_omitted,omitempty"`
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

package diagnosisflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"strings"
	"time"
)

const (
	SourceBrowserNavigation = "browser_navigation"
	SourceBrowserXHR        = "browser_xhr"
	SourceBrowserForm       = "browser_form"
	SourceAPIReconRuntime   = "api_recon_runtime"
	// 승인·요청 상한과 다른 저장 상한이다. 공개 선택 목록이 차도 발견 근거를 따로 보존한다.
	maxDiscoveryInventory      = 256
	maxDiscoveryInventoryBytes = 64 << 10
)

var ErrExplorationBinding = errors.New("exploration_binding_invalid")
var ErrExplorationObservation = errors.New("exploration_observation_invalid")
var ErrExplorationClosed = errors.New("exploration_session_closed")

// ExplorationBinding은 모델 입력이 아니라 호스트가 연 세션의 결속이다.
type ExplorationBinding struct {
	SessionRef     string `json:"session_ref"`
	RunRef         string `json:"run_ref"`
	JobID          string `json:"job_id"`
	TaskID         string `json:"task_id"`
	TargetRef      string `json:"target_ref"`
	TargetRevision uint64 `json:"target_revision"`
	SourceDigest   string `json:"source_digest"`
}

// ExplorationObservation에는 헤더·본문·query 값이 없다. private 경계 reader가 실제 요청에서 만든다.
// 운영 기록의 가린 path_pattern만으로 이 자료를 복원하거나 모델 보고를 승격하지 않는다.
type ExplorationObservation struct {
	Binding             ExplorationBinding `json:"binding"`
	Seq                 uint64             `json:"seq"`
	At                  time.Time          `json:"at"`
	Method              string             `json:"method"`
	Path                string             `json:"path"`
	Source              string             `json:"source"`
	Kind                string             `json:"kind"`
	Transport           string             `json:"transport"`
	Status              int                `json:"status"`
	Forwarded           bool               `json:"forwarded"`
	ResponseReceived    bool               `json:"response_received"`
	QueryKeys           []string           `json:"query_keys,omitempty"`
	BodyKeys            []string           `json:"body_keys,omitempty"`
	ContentType         string             `json:"content_type,omitempty"`
	ResponseContentType string             `json:"-"`
	ResponseSHA256      [32]byte           `json:"-"`
}

// ExplorationReader는 공개 도구가 아니며 경계 프로세스의 사적 자료 채널에 연결한다.
// Session은 현재 활성 결속을 반환하고, 닫힘·철회·장애는 오류로 돌려줘야 한다.
type ExplorationReader interface {
	Session(context.Context) (ExplorationBinding, error)
	ReadObservation(context.Context, ExplorationBinding, uint64) (ExplorationObservation, error)
}

// DiscoveryEvidence는 전체 발견의 작은 영속 목록이다. 공개 Snapshot과 구분하며 값은 보존하지 않는다.
type DiscoveryEvidence struct {
	InScope     bool      `json:"in_scope"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Kind        string    `json:"kind"`
	Source      string    `json:"source"`
	Confidence  string    `json:"confidence"`
	SessionRef  string    `json:"session_ref,omitempty"`
	Seq         uint64    `json:"seq,omitempty"`
	At          time.Time `json:"at,omitempty"`
	Transport   string    `json:"transport,omitempty"`
	Status      int       `json:"status,omitempty"`
	QueryKeys   []string  `json:"query_keys,omitempty"`
	BodyKeys    []string  `json:"body_keys,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
}
type explorationState struct {
	Binding      ExplorationBinding `json:"binding"`
	ClosedReason string             `json:"closed_reason,omitempty"`
	LastSequence uint64             `json:"last_sequence,omitempty"`
	LastDigest   string             `json:"last_digest,omitempty"`
}

func (c *Controller) matchingExploration(b ExplorationBinding) bool {
	return b.SessionRef != "" && len(b.SessionRef) <= 256 && b.RunRef != "" && b.RunRef == c.binding.RunRef && b.JobID == c.binding.JobID && b.TaskID == c.binding.TaskID && b.SourceDigest != "" && b.SourceDigest == c.binding.SourceDigest && c.binding.TargetRef != "" && b.TargetRef == c.binding.TargetRef && b.TargetRevision > 0 && b.TargetRevision == c.binding.TargetRevision
}

// BindExploration은 호출자가 주장한 세션 대신 고정한 host reader에서 결속을 읽는다.
func (c *Controller) BindExploration(ctx context.Context, reader ExplorationReader) (ExplorationBinding, error) {
	if c == nil || reader == nil || ctx == nil || ctx.Err() != nil {
		return ExplorationBinding{}, ErrExplorationBinding
	}
	b, err := reader.Session(ctx)
	if err != nil {
		return b, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.claimed || !c.matchingExploration(b) || c.binding.Current() != nil || c.record.Snapshot.Stage != "crawling" {
		return b, ErrExplorationBinding
	}
	if c.record.Exploration != nil {
		return b, ErrConflict
	}
	c.explorationReader = reader
	c.record.Exploration = &explorationState{Binding: b}
	if err = c.saveLocked(ctx, false, "exploration_bound", "", "", 0); err != nil {
		return b, err
	}
	return b, nil
}
func (c *Controller) explorationReaderFor(ctx context.Context, b ExplorationBinding) (ExplorationReader, error) {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrExplorationBinding
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.record.Exploration
	if c.poisoned || state == nil || b != state.Binding || !c.matchingExploration(b) || c.binding.Current() != nil || c.explorationReader == nil {
		return nil, ErrExplorationBinding
	}
	if state.ClosedReason != "" || c.record.Snapshot.Stage != "crawling" {
		return nil, ErrExplorationClosed
	}
	return c.explorationReader, nil
}
func (c *Controller) readExploration(ctx context.Context, b ExplorationBinding, seq uint64) (ExplorationObservation, error) {
	reader, err := c.explorationReaderFor(ctx, b)
	if err != nil {
		return ExplorationObservation{}, err
	}
	active, err := reader.Session(ctx)
	if err != nil || active != b {
		return ExplorationObservation{}, ErrExplorationBinding
	}
	if seq == 0 {
		return ExplorationObservation{}, ErrExplorationObservation
	}
	o, err := reader.ReadObservation(ctx, b, seq)
	if err != nil {
		return o, ErrUnavailable
	}
	if o.Binding != b || o.Seq != seq || !o.Forwarded || o.At.IsZero() || o.At.After(time.Now().Add(time.Second)) {
		return o, ErrExplorationObservation
	}
	if o.Source != SourceBrowserNavigation && o.Source != SourceBrowserXHR && o.Source != SourceBrowserForm && o.Source != SourceAPIReconRuntime {
		return o, ErrExplorationObservation
	}
	if o.Kind != "page" && o.Kind != "api" && o.Kind != "form" && o.Kind != "script" {
		return o, ErrExplorationObservation
	}
	if o.Transport != "http" && o.Transport != "sse" && o.Transport != "websocket" {
		return o, ErrExplorationObservation
	}
	if normalizedMethod(o.Method) != o.Method || o.Method == "UNKNOWN" || o.ResponseReceived && boundedStatus(o.Status) == 0 || !o.ResponseReceived && o.Status != 0 {
		return o, ErrExplorationObservation
	}
	if _, ok := discoveryKeys(o.QueryKeys); !ok {
		return o, ErrExplorationObservation
	}
	if _, ok := discoveryKeys(o.BodyKeys); !ok {
		return o, ErrExplorationObservation
	}
	if _, ok := discoveryMediaType(o.ContentType); !ok {
		return o, ErrExplorationObservation
	}
	return o, nil
}
func observationDigest(o ExplorationObservation) string {
	b, _ := json.Marshal([]any{o, o.ResponseContentType, hex.EncodeToString(o.ResponseSHA256[:])})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReportObservedは pinned readerだけを読む。seqをモデル JSONに足しても、この経路には入らない。
func (c *Controller) ReportObserved(ctx context.Context, b ExplorationBinding, seq uint64) (int, error) {
	o, err := c.readExploration(ctx, b, seq)
	if err != nil {
		return 0, err
	}
	return c.reportObservedRecord(ctx, b, seq, o)
}
func (c *Controller) reportObservedRecord(ctx context.Context, b ExplorationBinding, seq uint64, o ExplorationObservation) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.record.Exploration == nil || c.record.Exploration.Binding != b || c.record.Exploration.ClosedReason != "" || c.record.Snapshot.Stage != "crawling" || c.record.Snapshot.Paused || c.binding.Current() != nil {
		return 0, ErrExplorationBinding
	}
	state := c.record.Exploration
	digest := observationDigest(o)
	if seq <= state.LastSequence {
		if seq == state.LastSequence && digest == state.LastDigest {
			return 0, nil
		}
		return 0, ErrConflict
	}
	path, ok := c.resolve(o.Path, "/")
	if !ok || path != o.Path || strings.ContainsAny(o.Path, "?#") || !c.allowedPath(path) || !has(c.binding.Methods, o.Method) {
		return 0, ErrExplorationObservation
	}
	state.LastSequence, state.LastDigest = seq, digest
	fact := DiscoveryEvidence{Method: o.Method, Path: path, Kind: o.Kind, Source: o.Source, Confidence: "observed", InScope: true, SessionRef: b.SessionRef, Seq: seq, At: o.At, Transport: o.Transport, Status: o.Status, QueryKeys: mergeDiscoveryKeys(nil, o.QueryKeys), BodyKeys: mergeDiscoveryKeys(nil, o.BodyKeys), ContentType: o.ContentType}
	c.inventoryLocked(fact)
	e := c.addLocked(o.Method, path, o.Kind, o.Source, "/")
	accepted := 0
	if e != nil {
		if e.DiscoveryConfidence != "observed" {
			e.QueryKeys = nil
			e.BodyKeys = nil
			e.ContentType = ""
		}
		e.Source = o.Source
		e.DiscoveryConfidence = "observed"
		e.ExplorationSessionRef = b.SessionRef
		e.ExplorationSeq = seq
		e.Transport = o.Transport
		e.QueryKeys = mergeDiscoveryKeys(e.QueryKeys, o.QueryKeys)
		e.BodyKeys = mergeDiscoveryKeys(e.BodyKeys, o.BodyKeys)
		if e.ContentType == "" {
			e.ContentType = o.ContentType
		}
		e.Visited = e.Visited || o.ResponseReceived
		e.Status = boundedStatus(o.Status)
		e.InScope, e.Change = c.endpointScope(o.Method, path, e.Kind, o.Source)
		accepted = 1
	}
	c.record.Snapshot.Counts.ExplorationRequests++
	return accepted, c.saveLocked(ctx, false, "exploration_observed", o.Method, path, o.Status)
}
func (c *Controller) inventoryLocked(fact DiscoveryEvidence) {
	fact.QueryKeys = mergeDiscoveryKeys(nil, fact.QueryKeys)
	fact.BodyKeys = mergeDiscoveryKeys(nil, fact.BodyKeys)
	old := c.record.DiscoveryInventory
	for i, entry := range old {
		if fact.Confidence == "inferred" && entry.Confidence == "inferred" && entry.Method == fact.Method && entry.Path == fact.Path && entry.Source == fact.Source && entry.Kind == fact.Kind && entry.Transport == fact.Transport {
			fact.QueryKeys = mergeDiscoveryKeys(entry.QueryKeys, fact.QueryKeys)
			fact.BodyKeys = mergeDiscoveryKeys(entry.BodyKeys, fact.BodyKeys)
			if fact.ContentType == "" {
				fact.ContentType = entry.ContentType
			}
			old = append([]DiscoveryEvidence(nil), old...)
			old[i] = fact
			raw, _ := json.Marshal(old)
			if len(raw) > maxDiscoveryInventoryBytes {
				c.record.Snapshot.Counts.InventoryOmitted++
				return
			}
			c.record.DiscoveryInventory = old
			return
		}
	}
	next := append(append([]DiscoveryEvidence(nil), old...), fact)
	raw, _ := json.Marshal(next)
	if len(next) > maxDiscoveryInventory || len(raw) > maxDiscoveryInventoryBytes {
		c.record.Snapshot.Counts.InventoryOmitted++
		return
	}
	c.record.DiscoveryInventory = next
	c.record.Snapshot.Counts.InventoryStored = len(next)
}

// DiscoveryInventoryは公開 Snapshotと別の保護資料だ。返す配列は保存状態から切り離す。
func (c *Controller) DiscoveryInventory() []DiscoveryEvidence {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clone(c.record).DiscoveryInventory
}
func explorationCloseReason(reason string) bool {
	return reason == "completed" || reason == "cap_exhausted" || reason == "boundary_unavailable" || reason == "cancelled" || reason == "target_ended" || reason == "time_budget" || reason == "page_limit"
}

// FinishExplorationは部分発見を保ち、E②診断の成功・失敗を捏造せず選択段階へ進める。
func (c *Controller) FinishExploration(ctx context.Context, b ExplorationBinding, reason string) error {
	if !explorationCloseReason(reason) {
		return ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.record.Exploration == nil || c.record.Exploration.Binding != b || c.record.Exploration.ClosedReason != "" || c.record.Snapshot.Stage != "crawling" {
		return ErrExplorationBinding
	}
	c.record.Exploration.ClosedReason = reason
	s := &c.record.Snapshot
	s.ExplorationReason = reason
	s.ExplorationPartial = s.ExplorationPartial || reason != "completed"
	s.InitialCompleted = true
	s.CurrentPath = ""
	if reason == "cancelled" || reason == "target_ended" {
		s.Stage = "stopped"
	} else if c.binding.Mode == Manual {
		s.Stage = "awaiting_selection"
	} else {
		s.Stage = "diagnosing"
		for i := range s.Endpoints {
			e := &s.Endpoints[i]
			e.Selected = e.InScope && e.Change == "attested_read" && e.Kind != "script" && (e.ObservationError == "" || e.ObservationError == "body_incomplete")
		}
	}
	return c.saveLocked(ctx, false, "exploration_finished", "", "", 0)
}

type ExplorationFetchResult struct {
	Seq      uint64
	Body     []byte
	Complete bool
	Reason   string
}
type ExplorationFetch func(context.Context, string) (ExplorationFetchResult, error)

// CrawlExplorationは既存Crawlと別の入口だ。E② receipt・監査・attemptを探査証明として受け取らない。
func (c *Controller) CrawlExploration(ctx context.Context, b ExplorationBinding, fetch ExplorationFetch) (bool, error) {
	if fetch == nil {
		return true, ErrUnavailable
	}
	if _, err := c.explorationReaderFor(ctx, b); err != nil {
		return true, err
	}
	c.mu.Lock()
	if c.claimed {
		c.mu.Unlock()
		return false, nil
	}
	c.claimed = true
	c.mu.Unlock()
	attempted := map[string]bool{}
	for page := 0; page < c.binding.Limits.MaxPages; page++ {
		if err := c.Wait(ctx); err != nil {
			return true, err
		}
		c.mu.Lock()
		path, id, rank := "", "", 0
		for _, e := range c.record.Snapshot.Endpoints {
			if e.Method == "GET" && e.InScope && e.Change == "attested_read" && !e.Visited && !attempted[e.ID] {
				r := crawlRank(e)
				if id == "" || r < rank {
					path, id, rank = e.Path, e.ID, r
				}
			}
		}
		if path == "" {
			c.mu.Unlock()
			return true, nil
		}
		attempted[id] = true
		c.record.Snapshot.CurrentPath = path
		c.mu.Unlock()
		result, err := fetch(ctx, c.origin+path)
		if err != nil {
			clear(result.Body)
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			// 대상 요청 하나의 실패와 경계 장애는 다르다. 경계 장애는 Reason으로 명시한다.
			if result.Reason != "" {
				return true, c.FinishExploration(ctx, b, result.Reason)
			}
			c.mu.Lock()
			if e := c.endpointLocked("GET", path); e != nil {
				e.ObservationError = "request_failed"
			}
			c.record.Snapshot.ExplorationPartial = true
			err = c.saveLocked(ctx, false, "exploration_request_failed", "GET", path, 0)
			c.mu.Unlock()
			if err != nil {
				return true, err
			}
			continue
		}
		if result.Reason != "" {
			clear(result.Body)
			if !explorationCloseReason(result.Reason) {
				return true, ErrExplorationObservation
			}
			return true, c.FinishExploration(ctx, b, result.Reason)
		}
		o, err := c.readExploration(ctx, b, result.Seq)
		if err != nil {
			clear(result.Body)
			return true, err
		}
		if o.Method != "GET" || o.Path != path {
			clear(result.Body)
			return true, ErrExplorationObservation
		}
		pageError := ""
		if !result.Complete || len(result.Body) > c.binding.Limits.MaxResponseBytes {
			pageError = "body_incomplete"
		} else if !o.ResponseReceived || o.Status >= 400 {
			pageError = "http_error"
		} else if o.ResponseSHA256 == ([32]byte{}) || sha256.Sum256(result.Body) != o.ResponseSHA256 {
			clear(result.Body)
			return true, ErrExplorationObservation
		}
		if _, err = c.reportObservedRecord(ctx, b, result.Seq, o); err != nil {
			clear(result.Body)
			return true, err
		}
		c.mu.Lock()
		if pageError == "" {
			ct, _, _ := mime.ParseMediaType(o.ResponseContentType)
			c.discoverBodyLocked(result.Body, ct, path)
		} else if e := c.endpointLocked("GET", path); e != nil {
			e.ObservationError = pageError
		}
		clear(result.Body)
		err = c.saveLocked(ctx, false, "exploration_page", "GET", path, o.Status)
		c.mu.Unlock()
		if err != nil {
			return true, err
		}
	}
	c.mu.Lock()
	c.record.Snapshot.CurrentPath = ""
	if c.record.Snapshot.Counts.Queued > 0 {
		c.record.Snapshot.ExplorationPartial = true
	}
	err := c.saveLocked(ctx, false, "exploration_static_finished", "", "", 0)
	c.mu.Unlock()
	return true, err
}

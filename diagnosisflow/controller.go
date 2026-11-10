package diagnosisflow

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/e2proxy"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Controller struct {
	mu         sync.Mutex
	binding    Binding
	store      Store
	record     Record
	changed    chan struct{}
	poisoned   bool
	claimed    bool
	attempts   map[string]bool
	origin     string
	nativePath string
}

func New(ctx context.Context, b Binding, store Store) (*Controller, error) {
	u, err := url.Parse(b.TargetURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !ValidMode(b.Mode) || !b.Limits.Valid() || b.Current == nil || b.Current() != nil || store == nil || b.JobID == "" || b.TaskID == "" || b.SourceDigest == "" || b.Owner == "" || len(b.PathPrefixes) == 0 || len(b.ReadonlyPrefixes) == 0 {
		return nil, ErrUnavailable
	}
	c := &Controller{binding: b, store: store, changed: make(chan struct{}), attempts: map[string]bool{}, origin: u.Scheme + "://" + u.Host}
	c.record = Record{Owner: b.Owner, Commands: map[string]commandReceipt{}, Snapshot: Snapshot{Format: Version, JobID: b.JobID, TaskID: b.TaskID, RunRef: b.RunRef, SourceDigest: b.SourceDigest, Mode: b.Mode, ReversibleWrites: b.ReversibleWrites, Commands: []Command{}, Stage: "crawling", Limits: b.Limits, Endpoints: []Endpoint{}, Events: []Event{}}}
	path, ok := canonicalPath(u)
	if !ok {
		return nil, ErrDenied
	}
	c.addLocked("GET", path, "page", "start", path)
	if err = c.saveLocked(ctx, true, "created", "", u.Path, 0); err != nil {
		return nil, err
	}
	return c, nil
}
func within(path, prefix string) bool {
	return prefix == "/" || path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/")
}
func has(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
func highRisk(path string) bool {
	for _, segment := range strings.Split(strings.ToLower(path), "/") {
		for _, word := range []string{"delete", "remove", "reset", "logout", "checkout", "payment", "purchase", "transfer", "destroy", "truncate", "shutdown"} {
			if strings.Contains(segment, word) {
				return true
			}
		}
	}
	return false
}
func safePath(path string) string { return dlp.DiagnosticText(path) }
func (c *Controller) endpointLocked(method, path string) *Endpoint {
	for i := range c.record.Snapshot.Endpoints {
		v := &c.record.Snapshot.Endpoints[i]
		if v.Method == method && v.Path == path {
			return v
		}
	}
	return nil
}
func (c *Controller) allowedPath(path string) bool {
	if highRisk(path) {
		return false
	}
	allowed := false
	for _, p := range c.binding.PathPrefixes {
		allowed = allowed || within(path, p)
	}
	for _, p := range c.binding.Excluded {
		if excludedWithin(path, p) {
			return false
		}
	}
	return allowed
}
func (c *Controller) readPath(path string) bool {
	for _, p := range c.binding.ReadonlyPrefixes {
		if within(path, p) {
			return true
		}
	}
	return false
}
func (c *Controller) addLocked(method, path, kind, source, from string) *Endpoint {
	method = normalizedMethod(method)
	if path == "" {
		path = "/"
	}
	parsed, err := url.Parse(c.origin + path)
	if err != nil {
		c.record.Snapshot.Counts.InvalidPaths++
		return nil
	}
	canonical, ok := canonicalPath(parsed)
	if !ok || canonical != path {
		c.record.Snapshot.Counts.InvalidPaths++
		return nil
	}
	if old := c.endpointLocked(method, path); old != nil {
		old.Duplicates++
		// A discovered GET form is evidence of possible side effects, even if a link
		// or seed for the same method/path was seen first. Never upgrade it back.
		if kind == "form" {
			old.Kind, old.Source, old.From = kind, source, from
			if old.InScope {
				old.Change = "possible"
			}
		}
		return old
	}
	change := "possible"
	if (method == "GET" || method == "HEAD") && kind != "form" && c.readPath(path) {
		change = "attested_read"
	}
	in := has(c.binding.Methods, method) && c.allowedPath(path)
	if !in {
		change = "blocked"
	}
	e := Endpoint{ID: endpointID(c.origin, method, path), Method: method, Path: path, Kind: kind, Source: source, From: from, InScope: in, Change: change}
	if len(c.record.Snapshot.Endpoints) >= c.binding.Limits.MaxEndpoints {
		c.record.Snapshot.Counts.Omitted++
		if in {
			for i, old := range c.record.Snapshot.Endpoints {
				if !old.InScope && !old.Selected && !old.Visited {
					c.record.Snapshot.Endpoints[i] = e
					return &c.record.Snapshot.Endpoints[i]
				}
			}
		}
		return nil
	}
	c.record.Snapshot.Endpoints = append(c.record.Snapshot.Endpoints, e)
	return &c.record.Snapshot.Endpoints[len(c.record.Snapshot.Endpoints)-1]
}
func (c *Controller) saveLocked(ctx context.Context, create bool, kind, method, path string, status int) error {
	if c.poisoned {
		return ErrProjection
	}
	old := c.record.Snapshot.Revision
	c.record.Snapshot.Revision++
	c.record.Snapshot.UpdatedAt = time.Now().UTC()
	if kind != "" {
		c.record.Snapshot.Events = append(c.record.Snapshot.Events, Event{At: c.record.Snapshot.UpdatedAt, Kind: kind, Method: method, Path: safePath(path), Status: status})
		if len(c.record.Snapshot.Events) > 32 {
			c.record.Snapshot.Events = c.record.Snapshot.Events[len(c.record.Snapshot.Events)-32:]
		}
	}
	counts := &c.record.Snapshot.Counts
	counts.Found = len(c.record.Snapshot.Endpoints)
	counts.Visited = 0
	counts.Queued = 0
	for _, e := range c.record.Snapshot.Endpoints {
		if e.Visited {
			counts.Visited++
		}
		if !e.Visited && e.InScope && e.Change == "attested_read" && e.Method == "GET" {
			counts.Queued++
		}
	}

	c.record.Snapshot = compactProjection(c.record.Snapshot)
	var err error
	if create {
		err = c.store.Create(ctx, clone(c.record))
	} else {
		err = c.store.Save(ctx, old, clone(c.record))
	}
	if err != nil {
		c.poisoned = true
		c.record.Snapshot.Stage = "failed"
		c.record.Snapshot.Failure = "workflow_projection_failed"
		c.record.Snapshot.Paused = false
		close(c.changed)
		c.changed = make(chan struct{})
		return ErrProjection
	}
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}
func (c *Controller) Snapshot() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return compactProjection(Project(clone(c.record).Snapshot)), nil
}
func (c *Controller) Wait(ctx context.Context) error {
	for {
		c.mu.Lock()
		bad := c.poisoned
		paused := c.record.Snapshot.Paused
		stage := c.record.Snapshot.Stage
		changed := c.changed
		c.mu.Unlock()
		if bad {
			return ErrProjection
		}
		if c.binding.Current() != nil {
			return ErrUnavailable
		}
		if stage == "completed" || stage == "stopped" || stage == "failed" || stage == "unknown" {
			return ErrDenied
		}
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (c *Controller) BeforePlanning(ctx context.Context) error {
	for {
		if err := c.Wait(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		stage := c.record.Snapshot.Stage
		changed := c.changed
		c.mu.Unlock()
		if stage == "crawling" || stage == "diagnosing" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (c *Controller) BeforeWork(ctx context.Context) error {
	for {
		if err := c.Wait(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		stage := c.record.Snapshot.Stage
		changed := c.changed
		c.mu.Unlock()
		if stage == "diagnosing" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (c *Controller) Instructions() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var selected []string
	for _, e := range c.record.Snapshot.Endpoints {
		if e.Selected {
			selected = append(selected, e.ID)
		}
	}
	data, _ := json.Marshal(selected)
	return "\nWorkbench workflow mode: " + c.record.Snapshot.Mode + ". Workbench workflow phase: " + c.record.Snapshot.Stage + ". Only selected endpoints may be tested after initial discovery. Endpoint references below are opaque program-owned IDs. Do not expand scope for newly found endpoints. Selected endpoint references (JSON): " + string(data)
}
func (c *Controller) Command(ctx context.Context, owner, op, key string, revision uint64, ids []string) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || owner != c.binding.Owner || c.binding.Current() != nil {
		return Snapshot{}, ErrUnavailable
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	raw, _ := json.Marshal([]any{op, revision, sorted})
	fp := endpointID(c.binding.JobID, op, string(raw))
	if old, ok := c.record.Commands[key]; ok {
		if old.Fingerprint != fp {
			return Snapshot{}, ErrConflict
		}
		return Project(clone(c.record).Snapshot), nil
	}
	s := &c.record.Snapshot
	if key == "" || revision != s.Revision || len(c.record.Commands) >= 32 && op != "resume" {
		return Snapshot{}, ErrConflict
	}
	switch op {
	case "selection":
		if s.Mode != Manual || s.Stage != "awaiting_selection" || s.Paused || len(ids) == 0 || len(ids) > len(s.Endpoints) {
			return Snapshot{}, ErrDenied
		}
		chosen := map[string]bool{}
		for _, id := range ids {
			if chosen[id] {
				return Snapshot{}, ErrDenied
			}
			chosen[id] = true
		}
		for _, e := range s.Endpoints {
			if chosen[e.ID] {
				if !e.InScope || e.Change == "blocked" || e.Change != "attested_read" && !c.binding.ReversibleWrites {
					return Snapshot{}, ErrDenied
				}
				delete(chosen, e.ID)
			}
		}
		if len(chosen) != 0 {
			return Snapshot{}, ErrDenied
		}
		for i := range s.Endpoints {
			s.Endpoints[i].Selected = has(ids, s.Endpoints[i].ID)
		}
		s.Stage = "diagnosing"
	case "pause":
		if len(c.record.Commands) >= 31 || s.Paused || (s.Stage != "crawling" && s.Stage != "diagnosing") {
			return Snapshot{}, ErrDenied
		}
		s.Paused = true
	case "resume":
		if !s.Paused {
			return Snapshot{}, ErrDenied
		}
		s.Paused = false
	default:
		return Snapshot{}, ErrDenied
	}
	if len(c.record.Commands) >= 32 && op == "resume" {
		delete(c.record.Commands, s.Commands[0].Key)
		s.Commands = s.Commands[1:]
	}
	c.record.Commands[key] = commandReceipt{Fingerprint: fp, Operation: op}
	s.Commands = append(s.Commands, Command{Key: key, Operation: op, Revision: s.Revision + 1})
	if err := c.saveLocked(ctx, false, op, "", "", 0); err != nil {
		return Snapshot{}, err
	}
	return Project(clone(c.record).Snapshot), nil
}
func (c *Controller) requestEndpoint(v e2proxy.RequestView) (string, bool) {
	u, err := url.Parse(v.URL)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != c.origin || u.RawPath != "" || strings.Contains(u.Path, "%") {
		return "", false
	}
	return canonicalPath(u)
}
func (c *Controller) Before(ctx context.Context, v e2proxy.RequestView) error {
	path, ok := c.requestEndpoint(v)
	c.mu.Lock()
	native := ok && v.Method == "GET" && c.nativePath == path && c.record.Snapshot.Stage == "crawling" && !c.poisoned
	c.mu.Unlock()
	if native {
		if c.binding.Current() != nil {
			return ErrUnavailable
		}
	} else if err := c.Wait(ctx); err != nil {
		return err
	}
	path, ok = c.requestEndpoint(v)
	if !ok {
		return ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.endpointLocked(v.Method, path)
	if v.BodyBytes > 65536 || e == nil || !e.InScope || e.Change == "blocked" || !c.allowedPath(path) {
		return ErrDenied
	}
	s := c.record.Snapshot
	if s.Stage == "crawling" {
		if v.Method != "GET" || v.BodyPresent || e.Change != "attested_read" {
			return ErrDenied
		}
		return nil
	}
	if s.Stage != "diagnosing" || !e.Selected {
		return ErrDenied
	}
	if s.Mode == Auto && (v.BodyPresent || e.Change != "attested_read" || v.Method != "GET" && v.Method != "HEAD") {
		return ErrDenied
	}
	if e.Change != "attested_read" && !c.binding.ReversibleWrites {
		return ErrDenied
	}
	return nil
}
func (c *Controller) PreapprovedRead(v e2proxy.RequestView) bool {
	if v.BodyPresent || v.Method != "GET" && v.Method != "HEAD" {
		return false
	}
	path, ok := c.requestEndpoint(v)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.endpointLocked(v.Method, path)
	return !c.poisoned && e != nil && e.InScope && e.Change == "attested_read"
}
func (c *Controller) After(ctx context.Context, v e2proxy.RequestView, r e2proxy.Receipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned {
		return ErrProjection
	}
	if c.attempts[r.AttemptID] {
		return nil
	}
	c.attempts[r.AttemptID] = true
	path, ok := c.requestEndpoint(v)
	if !ok {
		path = "/{outside}"
	}
	if r.ForwardAttempted {
		c.record.Snapshot.Counts.Attempted++
	}
	if r.RequestWritten {
		c.record.Snapshot.Counts.Sent++
	}
	if r.Reason == e2proxy.Unknown {
		c.record.Snapshot.Counts.Unknown++
	}
	if r.Decision == "denied" || r.Decision == "unsupported" {
		c.record.Snapshot.Counts.Blocked++
	}
	if ok {
		e := c.endpointLocked(v.Method, path)
		if e != nil && r.ForwardAttempted {
			e.Visited = r.ResponseReceived
			e.Status = boundedStatus(r.StatusCode)
			e.CallRef = r.CallRef
			e.Request = v.Method + " " + safePath(path) + " HTTP/1.1"
			e.Response = "HTTP status " + statusText(r.StatusCode)
			if !r.ResponseComplete {
				e.Response += " · response incomplete"
			}
		}
	}
	return c.saveLocked(ctx, false, "request", normalizedMethod(v.Method), path, boundedStatus(r.StatusCode))
}
func statusText(n int) string { raw, _ := json.Marshal(n); return string(raw) }
func (c *Controller) Close(ctx context.Context, stage string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stage != "completed" && stage != "stopped" && stage != "failed" && stage != "unknown" {
		return ErrDenied
	}
	if c.poisoned {
		return ErrProjection
	}
	if c.record.Snapshot.Failure != "" && stage != "unknown" {
		stage = "failed"
	}
	c.record.Snapshot.Stage = stage
	c.record.Snapshot.Paused = false
	return c.saveLocked(ctx, false, stage, "", "", 0)
}

func (c *Controller) Matches(mode, inputDigest string) bool {
	return c != nil && c.binding.Mode == mode && c.binding.InputDigest == inputDigest
}

// 공개 projection은 본문·자격값을 반환하지 않고 경로의 비밀도 기존 DLP로 마스킹한다.
func Project(s Snapshot) Snapshot {
	s.CurrentPath = safePath(s.CurrentPath)
	for i := range s.Endpoints {
		e := &s.Endpoints[i]
		e.Path = safePath(e.Path)
		e.From = safePath(e.From)
		e.Request = safePath(e.Request)
		e.Response = safePath(e.Response)
	}
	return compactProjection(s)
}

func excludedWithin(path, pattern string) bool {
	parts := strings.Split(strings.Trim(pattern, "/"), "/")
	actual := strings.Split(strings.Trim(path, "/"), "/")
	if pattern == "/" {
		return true
	}
	if len(actual) < len(parts) {
		return false
	}
	for i, p := range parts {
		if p != "*" && p != actual[i] {
			return false
		}
	}
	return true
}

func (c *Controller) SnapshotWithDisplay(ctx context.Context) (Snapshot, error) {
	s, err := c.Snapshot()
	if err != nil {
		return s, err
	}
	if s.Failure != "" {
		return s, nil
	}
	if reader, ok := c.store.(interface {
		Display(context.Context, string, string) ([]Plan, []Activity, error)
	}); ok {
		s.Plan, s.Activity, err = reader.Display(ctx, c.binding.Owner, c.binding.JobID)
	}
	return compactProjection(s), err
}

func (c *Controller) StartDeepWork(ctx context.Context) error {
	if err := c.BeforeWork(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record.Snapshot.DeepRuns++
	return c.saveLocked(ctx, false, "deep_worker_started", "", "", 0)
}
func (c *Controller) FinishDeepWork(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record.Snapshot.DeepClosed++
	return c.saveLocked(ctx, false, "deep_worker_finished", "", "", 0)
}
func (c *Controller) DeepReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.poisoned && c.record.Snapshot.Stage == "diagnosing" && c.record.Snapshot.DeepRuns > 0 && c.record.Snapshot.DeepClosed == c.record.Snapshot.DeepRuns
}

func (c *Controller) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.poisoned || c.record.Snapshot.Failure != "" || c.record.Snapshot.Stage == "failed"
}

func (c *Controller) Fail(ctx context.Context, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned {
		return ErrProjection
	}
	c.record.Snapshot.Failure = reason
	c.record.Snapshot.Stage = "failed"
	c.record.Snapshot.Paused = false
	return c.saveLocked(ctx, false, "failed", "", "", 0)
}
func (c *Controller) SelectedData() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rows []map[string]string
	for _, e := range c.record.Snapshot.Endpoints {
		if e.Selected {
			rows = append(rows, map[string]string{"endpoint_ref": e.ID, "method": e.Method, "path": e.Path})
		}
	}
	raw, _ := json.Marshal(rows)
	return string(raw)
}

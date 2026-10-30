package server

// This file is the private, operator-attested input boundary for controlled
// reads. Snapshot JSON contains references and non-secret policy only; session
// headers and audit keys are loaded from separate 0600 files.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var (
	errControlledReadStorage  = errors.New("controlled read storage unavailable")
	errControlledReadSnapshot = errors.New("controlled read snapshot invalid")
)

// These are safety ceilings for the first registered-read path, not defaults.
// Each approval still supplies its own smaller per-execution limits.
const (
	maxControlledBodyBytes       = 4 << 20
	maxControlledRequests        = 10000
	maxControlledTimeoutMS       = int64((2 * time.Minute) / time.Millisecond)
	maxControlledSessions        = 64
	maxControlledResources       = 256
	maxControlledHeaders         = 32
	maxControlledHeaderNameBytes = 64
)

type controlledReadSpec struct {
	TaskID    string
	IntentID  int64
	CaseRef   string
	ResetRef  string
	PolicyRef string
	HTTP      transmit.Config
	KeyRef    string
	Key       []byte `json:"-"`
}

type controlledReadStore struct{ snapshotDir, stateDir string }

var safeSnapshotRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)
var safeRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var numericID = regexp.MustCompile(`^[1-9][0-9]*$`)
var safeFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func openControlledReadStore(snapshotDir, stateDir string) (*controlledReadStore, error) {
	if runtime.GOOS != "linux" || snapshotDir == "" || stateDir == "" {
		return nil, errControlledReadStorage
	}
	for _, dir := range []string{snapshotDir, stateDir} {
		if err := mkdirPrivate(dir); err != nil {
			return nil, errControlledReadStorage
		}
	}
	if err := mkdirPrivate(filepath.Join(stateDir, "journals")); err != nil {
		return nil, errControlledReadStorage
	}
	return &controlledReadStore{snapshotDir: snapshotDir, stateDir: stateDir}, nil
}

func mkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0o700 {
		return errors.New("directory")
	}
	return nil
}

type markerFile struct {
	TaskID     string `json:"task_id"`
	IntentID   int64  `json:"intent_id"`
	Controlled bool   `json:"controlled"`
}

func (s *controlledReadStore) taskMarkerPath(taskID string) (string, error) {
	if !numericID.MatchString(taskID) {
		return "", errControlledReadSnapshot
	}
	return filepath.Join(s.stateDir, "controlled-"+taskID+".task.marker"), nil
}

func (s *controlledReadStore) markerPath(taskID string, intentID int64) (string, error) {
	if !numericID.MatchString(taskID) || intentID <= 0 {
		return "", errControlledReadSnapshot
	}
	return filepath.Join(s.stateDir, "controlled-"+taskID+"-"+strconv.FormatInt(intentID, 10)+".marker"), nil
}

func (s *controlledReadStore) mark(taskID string, intentID int64) error {
	tp, err := s.taskMarkerPath(taskID)
	if err != nil {
		return err
	}
	if err := s.createOrVerifyTaskMarker(tp, taskID); err != nil {
		return err
	}
	p, err := s.markerPath(taskID, intentID)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(markerFile{taskID, intentID, true})
	b = append(b, '\n')
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return errControlledReadStorage
		}
		return s.verifyMarker(p, taskID, intentID)
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return errControlledReadStorage
	}
	if err := syncDir(s.stateDir); err != nil {
		return errControlledReadStorage
	}
	return nil
}

func (s *controlledReadStore) createOrVerifyTaskMarker(path, taskID string) error {
	b, _ := json.Marshal(markerFile{TaskID: taskID, Controlled: true})
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, err = f.Write(b); err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return errControlledReadStorage
		}
		if err := syncDir(s.stateDir); err != nil {
			return errControlledReadStorage
		}
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return errControlledReadStorage
	}
	return s.verifyTaskMarker(path, taskID)
}

func (s *controlledReadStore) verifyTaskMarker(path, taskID string) error {
	b, err := readPrivate(path, 4096)
	if err != nil {
		return errControlledReadStorage
	}
	var m markerFile
	if err := strictJSON(b, &m); err != nil || m.TaskID != taskID || m.IntentID != 0 || !m.Controlled {
		return errControlledReadSnapshot
	}
	return nil
}

func (s *controlledReadStore) taskMarked(taskID string) (bool, error) {
	p, err := s.taskMarkerPath(taskID)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, errControlledReadStorage
	}
	return true, s.verifyTaskMarker(p, taskID)
}

func (s *controlledReadStore) verifyMarker(p, taskID string, intentID int64) error {
	b, err := readPrivate(p, 4096)
	if err != nil {
		return errControlledReadStorage
	}
	var m markerFile
	if err := strictJSON(b, &m); err != nil || m.TaskID != taskID || m.IntentID != intentID || !m.Controlled {
		return errControlledReadSnapshot
	}
	return nil
}

func (s *controlledReadStore) marked(taskID string, intentID int64) (bool, error) {
	task, err := s.taskMarked(taskID)
	if err != nil || task {
		return task, err
	}
	p, err := s.markerPath(taskID, intentID)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, errControlledReadStorage
	}
	return true, s.verifyMarker(p, taskID, intentID)
}

type snapshotJSON struct {
	TaskID           string         `json:"task_id"`
	IntentID         int64          `json:"intent_id"`
	CaseRef          string         `json:"case_ref"`
	ResetRef         string         `json:"reset_ref"`
	PolicyRef        string         `json:"policy_ref"`
	Origin           string         `json:"origin"`
	ConnectIP        string         `json:"connect_ip"`
	ExpiresAt        time.Time      `json:"expires_at"`
	MaxRequests      uint64         `json:"max_requests"`
	MinIntervalMS    int64          `json:"min_interval_ms"`
	RequestTimeoutMS int64          `json:"request_timeout_ms"`
	MaxBodyBytes     int64          `json:"max_body_bytes"`
	KeyRef           string         `json:"key_ref"`
	KeyFile          string         `json:"key_file"`
	Sessions         []sessionJSON  `json:"sessions"`
	Resources        []resourceJSON `json:"resources"`
}
type sessionJSON struct {
	Label       string               `json:"label"`
	Handle      string               `json:"handle"`
	Status      oracle.SubjectStatus `json:"status"`
	ExpiresAt   time.Time            `json:"expires_at"`
	HeaderFiles map[string]string    `json:"header_files"`
}
type resourceJSON struct {
	Ref         string                 `json:"ref"`
	URL         string                 `json:"url"`
	ApprovalRef string                 `json:"approval_ref"`
	ReadOnly    bool                   `json:"read_only"`
	Accounts    []string               `json:"accounts"`
	View        *transmit.JSONViewRule `json:"view"`
}

func (s *controlledReadStore) load(taskID string, intentID int64, snapshotRef string) (controlledReadSpec, error) {
	var out controlledReadSpec
	if !numericID.MatchString(taskID) || intentID <= 0 || !safeSnapshotRef.MatchString(snapshotRef) {
		return out, errControlledReadSnapshot
	}
	b, err := readPrivate(filepath.Join(s.snapshotDir, snapshotRef+".json"), 1<<20)
	if err != nil {
		return out, errControlledReadSnapshot
	}
	var in snapshotJSON
	if err := strictJSON(b, &in); err != nil || in.TaskID != taskID || in.IntentID != intentID || in.KeyRef == "" || !safeRef.MatchString(in.KeyRef) {
		return out, errControlledReadSnapshot
	}
	if !safeRef.MatchString(in.CaseRef) || !safeRef.MatchString(in.ResetRef) || !safeRef.MatchString(in.PolicyRef) || in.ExpiresAt.IsZero() || !in.ExpiresAt.After(time.Now()) || in.MaxRequests == 0 || in.MinIntervalMS < 0 || in.RequestTimeoutMS <= 0 || in.MaxBodyBytes <= 0 || !safeFile.MatchString(in.KeyFile) {
		return out, errControlledReadSnapshot
	}
	if in.MaxBodyBytes > maxControlledBodyBytes || in.MaxRequests > maxControlledRequests || in.RequestTimeoutMS > maxControlledTimeoutMS || len(in.Sessions) == 0 || len(in.Sessions) > maxControlledSessions || len(in.Resources) == 0 || len(in.Resources) > maxControlledResources {
		return out, errControlledReadSnapshot
	}
	origin, err := url.Parse(in.Origin)
	if err != nil || origin.Scheme != "http" && origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Fragment != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Path != "" && origin.Path != "/" {
		return out, errControlledReadSnapshot
	}
	ip, err := netip.ParseAddr(in.ConnectIP)
	if err != nil || !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return out, errControlledReadSnapshot
	}
	key, err := readPrivate(filepath.Join(s.snapshotDir, in.KeyFile), 33)
	if err != nil || len(key) != auditlog.KeyBytes {
		return out, errControlledReadSnapshot
	}
	cfg := transmit.Config{Scope: transmit.Scope{TaskRef: taskID, CaseRef: in.CaseRef, ResetRef: in.ResetRef, PolicyRef: in.PolicyRef}, Origin: in.Origin, ConnectIP: ip, ExpiresAt: in.ExpiresAt, MaxRequests: in.MaxRequests, MinInterval: time.Duration(in.MinIntervalMS) * time.Millisecond, RequestTimeout: time.Duration(in.RequestTimeoutMS) * time.Millisecond, MaxBodyBytes: in.MaxBodyBytes}
	if in.MinIntervalMS > math.MaxInt64/int64(time.Millisecond) || in.RequestTimeoutMS > math.MaxInt64/int64(time.Millisecond) || cfg.MinInterval < 0 || cfg.RequestTimeout <= 0 {
		return out, errControlledReadSnapshot
	}
	sessionLabels := map[string]bool{}
	for _, x := range in.Sessions {
		if x.Label == "" || x.Handle == "" || x.Status != oracle.SubjectVerified || x.ExpiresAt.IsZero() {
			return out, errControlledReadSnapshot
		}
		if sessionLabels[x.Label] {
			return out, errControlledReadSnapshot
		}
		if len(x.HeaderFiles) > maxControlledHeaders {
			return out, errControlledReadSnapshot
		}
		sessionLabels[x.Label] = true
		h := make(http.Header)
		seenHeaders := make(map[string]bool, len(x.HeaderFiles))
		for name, file := range x.HeaderFiles {
			canonical := http.CanonicalHeaderKey(name)
			if !safeFile.MatchString(file) || file == in.KeyFile || name == "" || len(name) > maxControlledHeaderNameBytes || strings.EqualFold(name, "Host") || seenHeaders[canonical] {
				return out, errControlledReadSnapshot
			}
			seenHeaders[canonical] = true
			v, e := readPrivate(filepath.Join(s.snapshotDir, file), 8193)
			if e != nil || len(v) > 8192 {
				return out, errControlledReadSnapshot
			}
			value := strings.TrimSuffix(string(v), "\n")
			if bytes.Equal([]byte(value), key) {
				return out, errControlledReadSnapshot
			}
			h.Set(canonical, value)
		}
		cfg.Sessions = append(cfg.Sessions, transmit.Session{Account: idor.AccountRef{Label: x.Label, Handle: x.Handle, Status: x.Status}, ExpiresAt: x.ExpiresAt, Headers: h})
	}
	resourceRefs := map[string]bool{}
	for _, x := range in.Resources {
		if !safeRef.MatchString(x.Ref) || resourceRefs[x.Ref] || x.URL == "" || !safeRef.MatchString(x.ApprovalRef) || !x.ReadOnly || len(x.Accounts) == 0 {
			return out, errControlledReadSnapshot
		}
		u, e := url.Parse(x.URL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery == "" && u.ForceQuery {
			return out, errControlledReadSnapshot
		}
		resourceRefs[x.Ref] = true
		cfg.Resources = append(cfg.Resources, transmit.Resource{Ref: x.Ref, URL: x.URL, ApprovalRef: x.ApprovalRef, ReadOnly: x.ReadOnly, Accounts: append([]string(nil), x.Accounts...), View: x.View})
	}
	return controlledReadSpec{TaskID: taskID, IntentID: intentID, CaseRef: in.CaseRef, ResetRef: in.ResetRef, PolicyRef: in.PolicyRef, HTTP: cfg, KeyRef: in.KeyRef, Key: key}, nil
}

func (s *controlledReadStore) saveCheckpoint(ctx context.Context, cp auditlog.Checkpoint) error {
	if ctx == nil {
		return errControlledReadStorage
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if !validHex(cp.JournalID, 32) || !validHex(cp.MAC, 64) || cp.Version <= 0 || !safeRef.MatchString(cp.KeyRef) || !safeRef.MatchString(cp.Scope.TaskRef) || !safeRef.MatchString(cp.Scope.RunRef) || !safeRef.MatchString(cp.Scope.CaseRef) || !safeRef.MatchString(cp.Scope.ResetRef) || !safeRef.MatchString(cp.Scope.PolicyRef) {
		return errControlledReadStorage
	}
	dir := filepath.Join(s.stateDir, "journals")
	p := filepath.Join(dir, cp.JournalID+".checkpoint")
	tmp := filepath.Join(dir, "."+cp.JournalID+"."+randomSuffix()+".tmp")
	b, e := json.Marshal(cp)
	if e != nil {
		return errControlledReadStorage
	}
	f, e := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return errControlledReadStorage
	}
	if _, e = f.Write(append(b, '\n')); e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return errControlledReadStorage
	}
	if e = os.Rename(tmp, p); e != nil {
		_ = os.Remove(tmp)
		return errControlledReadStorage
	}
	if e = syncDir(dir); e != nil {
		return errControlledReadStorage
	}
	return nil
}

func (s *controlledReadStore) journalDirectory() string { return filepath.Join(s.stateDir, "journals") }
func validHex(v string, n int) bool {
	if len(v) != n || strings.ToLower(v) != v {
		return false
	}
	_, e := hex.DecodeString(v)
	return e == nil
}
func randomSuffix() string {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "tmp"
	}
	return hex.EncodeToString(b[:])
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	e = f.Sync()
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
func readPrivate(path string, max int64) ([]byte, error) {
	st, e := os.Lstat(path)
	if e != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Size() > max {
		return nil, errControlledReadStorage
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errControlledReadStorage
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, errControlledReadStorage
	}
	return b, nil
}

func strictJSON(data []byte, dst any) error {
	if e := scanJSON(data); e != nil {
		return e
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if e := dec.Decode(dst); e != nil {
		return e
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return errors.New("json trailing")
	}
	return nil
}

func scanJSON(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	if err := scanJSONValue(d); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("json trailing")
	}
	return nil
}

func scanJSONValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				ks, ok := key.(string)
				if !ok {
					return errors.New("json key")
				}
				if _, exists := seen[ks]; exists {
					return errors.New("json duplicate")
				}
				seen[ks] = struct{}{}
				if err := scanJSONValue(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := scanJSONValue(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
	}
	return nil
}

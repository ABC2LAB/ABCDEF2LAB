package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

// 작업대 실데이터 읽기 API(docs/contracts/workbench-live-data.md §3·§4). 실행 결속 디렉터리(runs/<job>)에 남은 정책
// 파일과 전송 통제 감사 기록을 읽어 표시용으로 투영한다. 작업 결속(AuthorizeTask)을 확인한 뒤에만 부른다. 키·자격·CA·
// helper·상태 디렉터리 경로와 명령 원문·URL·헤더·본문은 내보내지 않는다.
const (
	controlPolicyViewFormat = "abc2lab.control-policy-view/v1"
	e2FlowViewFormat        = "abc2lab.e2-flow-view/v1"
	// e2FlowTailCalls는 처음 조회(after 없음)에 주는 최근 호출 수, e2FlowPageEvents는 이어 받기 한 번의 사건 수다.
	e2FlowTailCalls  = 200
	e2FlowPageEvents = 1000
	maxRunAuditKey   = 4 << 10
	maxRunAuthority  = 64 << 10
)

// liveDataView는 task 경로가 작업대 실데이터 읽기 API인지다("control-policy"·"e2-flow", 아니면 "").
func liveDataView(path string) string {
	tail, ok := strings.CutPrefix(path, "/api/tasks/")
	if !ok {
		return ""
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[1] != "control-policy" && parts[1] != "e2-flow" {
		return ""
	}
	return parts[1]
}

var errRunNotBound = errors.New("run_not_bound")

type controlPolicyRule struct {
	Ref          string   `json:"ref"`
	Purpose      string   `json:"purpose"`
	Origin       string   `json:"origin"`
	ConnectIP    string   `json:"connect_ip"`
	PathPrefix   string   `json:"path_prefix"`
	PathScopeRef string   `json:"path_scope_ref"`
	Methods      []string `json:"methods"`
	TLSPinned    bool     `json:"tls_pinned"`
}

type controlPolicyLimits struct {
	MaxRequests          uint64 `json:"max_requests"`
	MaxAttempts          uint64 `json:"max_attempts"`
	MaxConcurrent        int    `json:"max_concurrent"`
	MaxBodyBytes         int64  `json:"max_body_bytes"`
	MaxResponseBodyBytes int64  `json:"max_response_body_bytes"`
	RequestTimeoutMS     int64  `json:"request_timeout_ms"`
}

type controlPolicyView struct {
	Format                  string              `json:"format"`
	PolicyRef               string              `json:"policy_ref"`
	PolicyRevision          string              `json:"policy_revision"`
	PolicyFingerprintSHA256 string              `json:"policy_fingerprint_sha256,omitempty"`
	ApprovalMode            string              `json:"approval_mode"`
	HeaderMode              string              `json:"header_mode"`
	ValidFrom               time.Time           `json:"valid_from"`
	ValidUntil              time.Time           `json:"valid_until"`
	Rules                   []controlPolicyRule `json:"rules"`
	Limits                  controlPolicyLimits `json:"limits"`
	Accounts                []string            `json:"accounts"`
	AuditBound              bool                `json:"audit_bound"`
}

// runBinding은 한 실행의 결속 디렉터리를 연 것이다. 정책 파일은 운영자 승인(authority.json)의 해시와 같아야 한다.
type runBinding struct {
	root        *os.Root
	dir         string
	policy      server.E2HTTPFilePolicy
	fingerprint string
}

func openRunBinding(runsDir, jobID string) (*runBinding, error) {
	if !managementReference.MatchString(jobID) || filepath.Base(jobID) != jobID {
		return nil, errRunNotBound
	}
	parent, err := os.OpenRoot(runsDir)
	if err != nil {
		return nil, errRunNotBound
	}
	defer parent.Close()
	root, err := parent.OpenRoot(jobID)
	if err != nil {
		return nil, errRunNotBound
	}
	raw, err := readRunFile(root, "policy.json", e2config.MaxPolicyBytes)
	if err != nil {
		_ = root.Close()
		return nil, errRunNotBound
	}
	authorityRaw, err := readRunFile(root, "authority.json", maxRunAuthority)
	var approval server.FirstDiagnosisHTTPApproval
	if err != nil || e2config.StrictJSON(authorityRaw, &approval) != nil || urlSHA(raw) != approval.PolicySHA256 {
		_ = root.Close()
		return nil, errPreparation
	}
	// 실행 factory가 받는 형식 그대로 읽는다(닫힌 형식·집행 의미·감사 키 pin, 진단 워크플로 칸·작업대 승인 방식 포함).
	// 평가 단독 E② 서비스용 검증기는 워크플로 칸을 몰라 AI 자동 워크플로 실행을 늘 거부했다(11/13 실제 점검 409).
	policy, fingerprint, err := server.ValidateE2HTTPRunPolicyDocument(raw)
	if err != nil {
		_ = root.Close()
		return nil, errPreparation
	}
	// 자격 pin 규칙이 있으면 지문에 헤더 이름이 들어가 pin 파일 없이는 실행과 같은 값을 못 낸다. 그때는 비운다.
	for _, rule := range policy.Rules {
		if rule.Credentials != nil {
			fingerprint = ""
		}
	}
	return &runBinding{root: root, dir: filepath.Join(runsDir, jobID), policy: policy, fingerprint: fingerprint}, nil
}

func (b *runBinding) Close() error { return b.root.Close() }

func readRunFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errPreparation
	}
	return raw, nil
}

func controlPolicyOf(b *runBinding) controlPolicyView {
	p := b.policy
	view := controlPolicyView{Format: controlPolicyViewFormat, PolicyRef: p.PolicyRef, PolicyRevision: p.PolicyRevision, ApprovalMode: string(p.ApprovalMode), HeaderMode: string(p.HeaderMode),
		ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil, Rules: []controlPolicyRule{}, Accounts: []string{},
		Limits: controlPolicyLimits{MaxRequests: p.MaxRequests, MaxAttempts: p.Limits.MaxAttempts, MaxConcurrent: p.Limits.MaxConcurrent, MaxBodyBytes: p.Limits.MaxBodyBytes,
			MaxResponseBodyBytes: p.Limits.MaxResponseBodyBytes, RequestTimeoutMS: p.Limits.RequestTimeoutMS},
		AuditBound: p.AuditKey.File != "" && urlDigestValid(p.AuditKey.SHA256)}
	if view.Limits.MaxResponseBodyBytes == 0 {
		// 전달 상한을 따로 정하지 않은 정책은 요청 본문 상한을 같이 쓴다(C16 결정안 3).
		view.Limits.MaxResponseBodyBytes = p.Limits.MaxBodyBytes
	}
	view.PolicyFingerprintSHA256 = b.fingerprint
	for _, r := range p.Rules {
		view.Rules = append(view.Rules, controlPolicyRule{Ref: r.Ref, Purpose: r.Purpose, Origin: r.Origin, ConnectIP: r.ConnectIP, PathPrefix: r.PathPrefix,
			PathScopeRef: r.PathScopeRef, Methods: append([]string{}, r.Methods...), TLSPinned: r.TLSSPKISHA256 != ""})
	}
	if p.Auth != nil {
		for _, a := range p.Auth.Accounts {
			view.Accounts = append(view.Accounts, a.Label)
		}
	}
	return view
}

type e2FlowAttempt struct {
	AttemptID         string    `json:"attempt_id"`
	Index             uint64    `json:"index"`
	Sequence          uint64    `json:"sequence"`
	Stage             string    `json:"stage"`
	Method            string    `json:"method"`
	RuleRef           string    `json:"rule_ref"`
	PathPattern       string    `json:"path_pattern"`
	Decision          string    `json:"decision"`
	Reason            string    `json:"reason"`
	ForwardAttempted  bool      `json:"forward_attempted"`
	RequestWritten    bool      `json:"request_written"`
	ResponseReceived  bool      `json:"response_received"`
	ResponseComplete  bool      `json:"response_complete"`
	StatusCode        int       `json:"status_code"`
	ResponseTruncated bool      `json:"response_truncated"`
	At                time.Time `json:"at"`
}

type e2FlowCall struct {
	CallRef     string          `json:"call_ref"`
	IntentID    int64           `json:"intent_id"`
	Closed      bool            `json:"closed"`
	ClosedIndex uint64          `json:"closed_index,omitempty"`
	Attempts    []e2FlowAttempt `json:"attempts"`
}

type e2FlowCounts struct {
	Attempts         int `json:"attempts"`
	Allowed          int `json:"allowed"`
	Denied           int `json:"denied"`
	Unsupported      int `json:"unsupported"`
	ForwardAttempted int `json:"forward_attempted"`
	Responses        int `json:"responses"`
	Unknown          int `json:"unknown"`
	Transient        int `json:"transient"`
	Withheld         int `json:"withheld"`
	Calls            int `json:"calls"`
}

type e2FlowView struct {
	Format       string       `json:"format"`
	Journal      string       `json:"journal"`
	LastIndex    uint64       `json:"last_index"`
	More         bool         `json:"more"`
	OmittedCalls int          `json:"omitted_calls"`
	Counts       e2FlowCounts `json:"counts"`
	Calls        []e2FlowCall `json:"calls"`
}

// e2FlowEvents는 결속 디렉터리의 감사 기록을 실행 감사 키로 끝까지 검증하고 사건을 돌려준다. 키가 pin과 다르거나
// 기록이 틀리면 ok가 거짓이다(사건 없음). 기록이 아직 없으면(실행 전) errRunNotBound다.
func e2FlowEvents(r *http.Request, b *runBinding) ([]e2proxy.Event, bool, error) {
	p := b.policy
	if filepath.Clean(p.StateDir) != filepath.Join(b.dir, "state") || filepath.Base(p.AuditKey.File) != p.AuditKey.File {
		return nil, false, nil
	}
	journal, err := b.root.Open("state/events.jsonl")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, errRunNotBound
	}
	if err != nil {
		return nil, false, errPreparation
	}
	defer journal.Close()
	key, err := readRunFile(b.root, p.AuditKey.File, maxRunAuditKey)
	if err != nil {
		return nil, false, nil
	}
	defer clear(key)
	sum := sha256.Sum256(key)
	if hex.EncodeToString(sum[:]) != p.AuditKey.SHA256 {
		return nil, false, nil
	}
	events, _, err := e2proxy.VerifyJournalEvents(r.Context(), journal, key)
	if err != nil {
		return nil, false, nil
	}
	return events, true, nil
}

// e2FlowOf는 검증된 사건을 호출 단위로 묶는다(계약 §4 커서 규칙). after가 없으면 최근 호출 e2FlowTailCalls개를 그
// 호출의 모든 사건으로, after가 있으면 그보다 큰 번호의 사건만 오래된 것부터 e2FlowPageEvents개까지 준다.
func e2FlowOf(events []e2proxy.Event, after *uint64) e2FlowView {
	view := e2FlowView{Format: e2FlowViewFormat, Journal: "verified", Calls: []e2FlowCall{}}
	attempts, calls := map[string]bool{}, map[string]bool{}
	for _, e := range events {
		if e.CallRef != "" {
			calls[e.CallRef] = true
		}
		if e.Stage != "result" || e.AttemptID == "" || attempts[e.AttemptID] {
			continue
		}
		attempts[e.AttemptID] = true
		view.Counts.Attempts++
		switch e.Decision {
		case "allowed":
			view.Counts.Allowed++
		case "denied":
			view.Counts.Denied++
		case "unsupported":
			view.Counts.Unsupported++
		}
		if e.ForwardAttempted {
			view.Counts.ForwardAttempted++
		}
		if e.ResponseReceived {
			view.Counts.Responses++
		}
		switch e.Reason {
		case e2proxy.Unknown:
			view.Counts.Unknown++
		case e2proxy.TransportFailed:
			view.Counts.Transient++
		case e2proxy.Withheld:
			view.Counts.Withheld++
		}
	}
	view.Counts.Calls = len(calls)
	selected := events
	if after != nil {
		view.LastIndex = *after
		start := sort.Search(len(events), func(i int) bool { return events[i].Index > *after })
		selected = events[start:]
		if len(selected) > e2FlowPageEvents {
			selected, view.More = selected[:e2FlowPageEvents], true
		}
	}
	grouped := map[string]*e2FlowCall{}
	order := []string{}
	for _, e := range selected {
		view.LastIndex = max(view.LastIndex, e.Index)
		if e.CallRef == "" {
			// 실행 통제 사건(control)은 호출에 묶이지 않는다. 커서는 지나간다.
			continue
		}
		c := grouped[e.CallRef]
		if c == nil {
			c = &e2FlowCall{CallRef: e.CallRef, IntentID: e.IntentID, Attempts: []e2FlowAttempt{}}
			grouped[e.CallRef] = c
			order = append(order, e.CallRef)
		}
		switch {
		case e.Stage == "call_closed":
			c.Closed, c.ClosedIndex = true, e.Index
		case e.AttemptID != "":
			row := e2FlowAttempt{AttemptID: e.AttemptID, Index: e.Index, Sequence: e.Sequence, Stage: e.Stage, Method: e.Method, RuleRef: e.RuleRef,
				Decision: e.Decision, Reason: string(e.Reason), ForwardAttempted: e.ForwardAttempted, RequestWritten: e.RequestWritten,
				ResponseReceived: e.ResponseReceived, ResponseComplete: e.ResponseComplete, StatusCode: e.StatusCode, ResponseTruncated: e.ResponseTruncated, At: e.At}
			replaced := false
			for i := range c.Attempts {
				if c.Attempts[i].AttemptID == row.AttemptID {
					c.Attempts[i], replaced = row, true
				}
			}
			if !replaced {
				c.Attempts = append(c.Attempts, row)
			}
		}
	}
	if after == nil && len(order) > e2FlowTailCalls {
		view.OmittedCalls = len(order) - e2FlowTailCalls
		order = order[len(order)-e2FlowTailCalls:]
	}
	for _, ref := range order {
		view.Calls = append(view.Calls, *grouped[ref])
	}
	return view
}

// serveLiveData는 작업 결속을 확인한 task의 실데이터 읽기 API다. 실행 결속 전이면 404, 정책 파일이 승인 해시와
// 다르면 409다.
func serveLiveData(w http.ResponseWriter, r *http.Request, runsDir, jobID, view string) {
	if r.Method != http.MethodGet {
		http.Error(w, "읽기만 받습니다", http.StatusMethodNotAllowed)
		return
	}
	var after *uint64
	if raw, ok := r.URL.Query()["after"]; ok {
		n, err := strconv.ParseUint(strings.Join(raw, ""), 10, 64)
		if view != "e2-flow" || len(raw) != 1 || err != nil || strconv.FormatUint(n, 10) != raw[0] {
			http.Error(w, "조회 조건이 맞지 않습니다", http.StatusBadRequest)
			return
		}
		after = &n
	}
	b, err := openRunBinding(runsDir, jobID)
	if errors.Is(err, errRunNotBound) {
		http.Error(w, "실행이 아직 결속되지 않았습니다", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "실행 정책이 승인과 다릅니다", http.StatusConflict)
		return
	}
	defer b.Close()
	var out any
	if view == "control-policy" {
		out = controlPolicyOf(b)
	} else {
		events, ok, err := e2FlowEvents(r, b)
		switch {
		case errors.Is(err, errRunNotBound):
			http.Error(w, "전송 통제 기록이 아직 없습니다", http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, "전송 통제 기록을 읽지 못했습니다", http.StatusServiceUnavailable)
			return
		case !ok:
			out = e2FlowView{Format: e2FlowViewFormat, Journal: "invalid", Calls: []e2FlowCall{}}
		default:
			out = e2FlowOf(events, after)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("[first-diagnosis] 작업대 실데이터 응답 쓰기 실패: 화면=%s", view)
	}
}

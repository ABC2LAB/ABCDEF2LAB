package loginattempt

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Schema는 기록 이름이다(§3).
const Schema = "jindo.login-attempt/v1"

// 단계·주체(§3 stage·actor).
const (
	StageExploration  = "exploration"
	StageDiagnosis    = "diagnosis"
	StageConfirmation = "confirmation"
	ActorModel        = "model"
	ActorProgram      = "program"
)

// 전송 결과·판정(§3 transport·outcome).
const (
	TransportSent      = "sent"
	TransportNotSent   = "not_sent"
	TransportUnknown   = "result_unknown"
	OutcomeSucceeded   = "succeeded"
	OutcomeUnconfirmed = "unconfirmed"
	OutcomeFailed      = "failed"
	OutcomeNotSent     = "not_sent"
	OutcomeUnknown     = "result_unknown"
)

// 세션 자리(§3 session_source).
const (
	SourceSetCookie = "set_cookie"
	SourceJSONField = "json_field"
	SourceHeader    = "header"
	SourceContinued = "continued"
	SourceNone      = "none"
)

// 뒤 요청 신호(§4 followup)와 대조 출처.
const (
	FollowupContrast    = "authenticated_contrast"
	FollowupNoLoginForm = "no_login_form"
	FollowupLoginForm   = "login_form"
	FollowupNone        = "none"
	ContrastObserved    = "observed"
	ContrastProbe       = "probe"
)

// 보내기 전 거부 사유다. 그 요청만 실패하고 회차는 계속한다.
const (
	RefusalExhausted = "login_attempts_exhausted"
	RefusalPending   = "login_verification_pending"
)

// 화면 계정 상태(§4 account_auth). not_attempted·unconfirmed는 모델 로그인에서 새로 생긴 값이다.
const (
	AuthNotAttempted  = "not_attempted"
	AuthAuthenticated = "authenticated"
	AuthNotSent       = "not_sent"
	AuthLoginFailed   = "login_failed"
	AuthUnconfirmed   = "unconfirmed"
	AuthResultUnknown = "result_unknown"
)

// 탐색 쓰기 예외의 계정별 상태(§2-2).
const (
	StateLoggedOut = "logged_out"
	StateVerifying = "verifying"
	StateLoggedIn  = "logged_in"
)

const (
	maxSessionNames     = 16
	maxOpenedPerAttempt = 64
	maxAnonURLs         = 4096
	maxRecentBodies     = 32
	// MaxObservedBodyBytes는 경계가 판정기에 넘기는 응답 본문 앞부분 상한이다(로그인 화면·토큰 칸 판정용).
	MaxObservedBodyBytes = 64 << 10
	maxRecentBodyBytes   = 64 << 10
	maxSentAsPrefix      = 16
)

var ErrConfig = errors.New("loginattempt: invalid config")

// Row는 시도 한 행이다(§3). 비밀번호·세션 값·응답 본문은 넣지 않는다.
type Row struct {
	Schema               string   `json:"schema"`
	AttemptRef           string   `json:"attempt_ref"`
	RunRef               string   `json:"run_ref"`
	Stage                string   `json:"stage"`
	Actor                string   `json:"actor"`
	TransportRef         string   `json:"transport_ref"`
	At                   string   `json:"at"`
	AccountLabel         string   `json:"account_label"`
	Identifier           string   `json:"identifier"`
	PasswordRegistered   bool     `json:"password_registered"`
	Method               string   `json:"method"`
	Path                 string   `json:"path"`
	QueryKeys            []string `json:"query_keys"`
	Format               string   `json:"format"`
	WriteException       bool     `json:"write_exception"`
	IdentifierField      string   `json:"identifier_field"`
	PasswordField        string   `json:"password_field"`
	OtherFields          []string `json:"other_fields"`
	FieldsFromResponse   []string `json:"fields_from_response"`
	Status               int      `json:"status"`
	Transport            string   `json:"transport"`
	Refusal              string   `json:"refusal"`
	RedirectPath         string   `json:"redirect_path"`
	LoginFormReturned    bool     `json:"login_form_returned"`
	SessionSource        string   `json:"session_source"`
	SessionNames         []string `json:"session_names"`
	SessionSeenAnonymous []string `json:"session_seen_anonymous"`
	SessionSentAs        string   `json:"session_sent_as"`
	Followup             string   `json:"followup"`
	ContrastSource       string   `json:"contrast_source"`
	ContrastProbes       int      `json:"contrast_probes"`
	SessionEnded         bool     `json:"session_ended"`
	Outcome              string   `json:"outcome"`
	ConsecutiveFailures  int      `json:"consecutive_failures"`
}

// Config는 회차·단계 하나의 판정기 설정이다.
type Config struct {
	RunRef string
	Stage  string
	Actor  string
	// Origin은 승인 origin이다. 리다이렉트가 같은 origin인지 가리는 데만 쓴다.
	Origin   *url.URL
	Accounts []Account
	// MaxConsecutiveFailures는 아이디별 연속 실패 상한이다(§5, 기본 3은 사용자 결정 2026-11-26).
	MaxConsecutiveFailures int
	// MaxContrastProbes는 시도마다 보내는 프로그램 대조 요청 URL 수 상한이다(§4, 기본 3).
	MaxContrastProbes int
	// TokenMinBytes는 응답 JSON·헤더 값을 세션 토큰 후보로 볼 최소 길이다(계약 밖 해석, 설정값).
	TokenMinBytes int
	// Exception은 이 단계에서 §2-2 탐색 쓰기 예외를 쓰는지다(탐색 단계만).
	Exception bool
	Now       func() time.Time
	// Sink는 행이 바뀔 때마다 받는다. 판정기 잠금 안에서 부르므로 판정기를 다시 부르지 않는다.
	Sink func(Row)
	// OnSucceeded는 등록 계정 라벨의 시도가 성공한 순간 그 세션을 실어 보낼 헤더(Cookie 또는 토큰 헤더)로 받는다. 값은
	// 회차 메모리에만 둔다(세션 저장소·확정 단계 계정 결속). 판정기 잠금 안에서 부른다.
	OnSucceeded func(label string, session http.Header)
}

type observation struct {
	key    string
	path   string
	status int
	loc    string
	seq    int
	probe  bool
}

type attempt struct {
	row         Row
	order       int
	cand        Candidate
	exception   bool
	priorState  string
	done        bool
	seeded      bool
	cookies     map[string]string
	tokens      []token
	opened      []observation
	contrastKey string
	successSeq  int
	probed      map[string]bool
	last        []byte
}

// Probe는 프로그램 대조 요청 하나다(§4). 모델이 이 세션으로 이미 GET한 정확한 URL을 세션 없이 다시 연다.
type Probe struct {
	Path     string
	RawQuery string
	attempt  *attempt
}

func (p Probe) URLKey() string { return urlKey(p.Path, p.RawQuery) }

// Plan은 보내기 전 판정이다. Refusal이 있으면 보내지 않는다(행은 이미 남겼다).
type Plan struct {
	Login     bool
	Exception bool
	Refusal   string
	attempt   *attempt
}

// Tracker는 회차·단계 하나의 로그인 시도 판정기다. 세션 값과 질의 값은 이 메모리에만 있고 회차가 끝나면 버린다.
type Tracker struct {
	mu             sync.Mutex
	cfg            Config
	attempts       []*attempt
	failures       map[string]int
	state          map[string]string
	loginPaths     map[string]bool
	anon           map[string][]observation
	anonCookies    map[string]bool
	sessionHeaders map[string]bool
	recent         [][]byte
	pending        []Probe
	obsSeq         int
	n              int
}

func New(cfg Config) (*Tracker, error) {
	if cfg.RunRef == "" || cfg.Stage == "" || cfg.MaxConsecutiveFailures <= 0 || cfg.MaxContrastProbes < 0 || cfg.TokenMinBytes <= 0 || cfg.Origin == nil {
		return nil, ErrConfig
	}
	if cfg.Actor == "" {
		cfg.Actor = ActorModel
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Tracker{cfg: cfg, failures: map[string]int{}, state: map[string]string{}, loginPaths: map[string]bool{},
		anon: map[string][]observation{}, anonCookies: map[string]bool{}, sessionHeaders: map[string]bool{}}, nil
}

func urlKey(p, rawQuery string) string {
	if rawQuery == "" {
		return p
	}
	return p + "?" + rawQuery
}

func queryKeys(rawQuery string) []string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil && len(values) == 0 {
		return []string{}
	}
	out := []string{}
	for k := range values {
		out = appendName(out, k)
	}
	sort.Strings(out)
	return out
}

// Prepare는 보내기 전에 요청을 판정한다. writeAllowed는 회차 쓰기 승인 범위가 이 요청을 이미 허용하는지다.
// 로그인 시도가 아니면 빈 Plan이다. 로그인 시도인데 Refusal이 있으면 보내지 않는다.
func (t *Tracker) Prepare(req Request, writeAllowed bool) Plan {
	t.mu.Lock()
	defer t.mu.Unlock()
	cand, ok := Classify(req.Method, req.ContentType, req.Body, t.cfg.Accounts, t.fromResponseLocked)
	if !ok {
		return Plan{}
	}
	t.n++
	a := &attempt{order: t.n, cand: cand, probed: map[string]bool{}}
	a.row = Row{Schema: Schema, AttemptRef: t.cfg.Stage + "-" + itoa(t.n), RunRef: t.cfg.RunRef, Stage: t.cfg.Stage, Actor: t.cfg.Actor, TransportRef: req.TransportRef,
		At: t.cfg.Now().UTC().Format(time.RFC3339Nano), AccountLabel: cand.AccountLabel, Identifier: cand.Identifier,
		PasswordRegistered: cand.PasswordRegistered, Method: req.Method, Path: cleanPath(req.Path), QueryKeys: queryKeys(req.RawQuery),
		Format: cand.Format, IdentifierField: cand.IdentifierField, PasswordField: cand.PasswordField,
		OtherFields: nonNil(cand.OtherFields), FieldsFromResponse: nonNil(cand.FieldsFromResponse),
		SessionNames: []string{}, SessionSeenAnonymous: []string{}, Followup: FollowupNone}
	t.attempts = append(t.attempts, a)
	if t.failures[cand.Identifier] >= t.cfg.MaxConsecutiveFailures {
		t.refuseLocked(a, RefusalExhausted)
		return Plan{Login: true, Refusal: RefusalExhausted}
	}
	plan := Plan{Login: true, attempt: a}
	if t.cfg.Exception && !writeAllowed && cand.Pair {
		switch st := t.stateLocked(cand.AccountLabel); st {
		case StateVerifying:
			t.refuseLocked(a, RefusalPending)
			return Plan{Login: true, Refusal: RefusalPending}
		case StateLoggedOut:
			a.exception, a.priorState = true, st
			a.row.WriteException = true
			t.state[cand.AccountLabel] = StateVerifying
			plan.Exception = true
		}
	}
	return plan
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (t *Tracker) stateLocked(label string) string {
	if st, ok := t.state[label]; ok {
		return st
	}
	return StateLoggedOut
}

func (t *Tracker) refuseLocked(a *attempt, reason string) {
	a.row.Transport, a.row.Outcome, a.row.Refusal = TransportNotSent, OutcomeNotSent, reason
	a.row.ConsecutiveFailures = t.failures[a.cand.Identifier]
	a.done = true
	t.emitLocked(a)
}

// NotSent는 로그인 시도가 다른 경계 규칙(범위·메서드·예산 등)으로 보내지지 않았을 때 부른다. transportRef는 그 거부를 남긴
// 경계·E② 기록 참조다(비면 Prepare 때 값을 둔다). 예외로 바꾼 상태는 되돌린다.
func (t *Tracker) NotSent(p Plan, transportRef, reason string) {
	if p.attempt == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	a := p.attempt
	if transportRef != "" {
		a.row.TransportRef = transportRef
	}
	if a.exception {
		t.state[a.cand.AccountLabel] = a.priorState
	}
	t.refuseLocked(a, reason)
}

// Unknown은 보냈는지·응답을 모를 때 부른다. 자동으로 다시 보내지 않는다. 예외 상태는 꺼짐(로그인됨) 쪽으로 둔다.
func (t *Tracker) Unknown(p Plan, transportRef string) {
	if p.attempt == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	a := p.attempt
	a.row.TransportRef, a.row.Transport, a.row.Outcome = transportRef, TransportUnknown, OutcomeUnknown
	a.row.ConsecutiveFailures = t.failures[a.cand.Identifier]
	a.done = true
	if a.exception {
		t.state[a.cand.AccountLabel] = StateLoggedIn
	}
	t.emitLocked(a)
}

// Complete는 로그인 시도의 응답을 받은 뒤 부른다(§4 로그인 응답의 실패 신호·세션 자리).
func (t *Tracker) Complete(p Plan, transportRef string, req Request, resp Response) {
	if p.attempt == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	a := p.attempt
	now := t.cfg.Now()
	a.done = true
	a.row.TransportRef, a.row.Transport, a.row.Status = transportRef, TransportSent, resp.Status
	loginPath := cleanPath(req.Path)
	t.loginPaths[loginPath] = true
	a.row.RedirectPath = redirectPath(t.cfg.Origin, loginPath, resp)
	a.row.LoginFormReturned = hasPasswordInput(resp) || a.row.RedirectPath != "" && a.row.RedirectPath == loginPath
	cookies := newCookies(resp, now)
	tokens := append(jsonTokens(resp.Body, t.cfg.TokenMinBytes), headerTokens(resp.Header, t.cfg.TokenMinBytes)...)
	// 응답이 되돌려 준 아이디·비밀번호는 세션이 아니다.
	kept := tokens[:0]
	for _, tk := range tokens {
		if tk.value != a.cand.password && tk.value != a.cand.Identifier {
			kept = append(kept, tk)
		}
	}
	tokens = kept
	switch {
	case len(cookies) > 0:
		a.row.SessionSource = SourceSetCookie
		a.cookies = cookies
		a.row.SessionNames = sortedKeys(cookies)
		for _, name := range a.row.SessionNames {
			if t.anonCookies[name] {
				a.row.SessionSeenAnonymous = append(a.row.SessionSeenAnonymous, name)
			}
		}
	case len(tokens) > 0:
		a.row.SessionSource = tokens[0].source
		a.row.SessionNames = tokenNames(tokens, tokens[0].source)
	default:
		if continued := requestCookies(req.Header); len(continued) > 0 {
			a.row.SessionSource = SourceContinued
			a.cookies = continued
			a.row.SessionNames = sortedKeys(continued)
		} else {
			a.row.SessionSource = SourceNone
		}
	}
	a.tokens = tokens
	t.rememberLocked(resp.Body)
	failed := resp.Status >= 400 || a.row.SessionSource == SourceNone || a.row.LoginFormReturned
	if failed {
		t.setOutcomeLocked(a, OutcomeFailed)
	} else {
		t.setOutcomeLocked(a, OutcomeUnconfirmed)
		// 실패가 분명하지 않으면 대조 전이라도 예외는 끈다(§2-2, 팀 합의 2026-11-26).
		if t.cfg.Exception && a.cand.AccountLabel != "" {
			t.state[a.cand.AccountLabel] = StateLoggedIn
		}
	}
	t.reevaluateLocked()
	t.emitLocked(a)
}

func tokenNames(tokens []token, source string) []string {
	out := []string{}
	for _, tk := range tokens {
		if tk.source == source {
			out = appendName(out, tk.name)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = appendName(out, k)
	}
	sort.Strings(out)
	return out
}

func (t *Tracker) setOutcomeLocked(a *attempt, outcome string) {
	if a.row.Outcome == outcome {
		return
	}
	a.row.Outcome = outcome
	switch outcome {
	case OutcomeFailed:
		t.failures[a.cand.Identifier]++
		if t.cfg.Exception && a.cand.AccountLabel != "" {
			t.state[a.cand.AccountLabel] = StateLoggedOut
		}
	case OutcomeSucceeded:
		t.failures[a.cand.Identifier] = 0
		if t.cfg.OnSucceeded != nil && a.cand.AccountLabel != "" && !a.seeded {
			if h := sessionHeader(a); len(h) > 0 {
				t.cfg.OnSucceeded(a.cand.AccountLabel, h)
			}
		}
	}
	a.row.ConsecutiveFailures = t.failures[a.cand.Identifier]
}

// sessionHeader는 성공한 시도의 세션을 뒤 요청이 실어 보낸 방식 그대로 헤더로 만든다. 쿠키는 이름 순으로 붙인다.
func sessionHeader(a *attempt) http.Header {
	h := http.Header{}
	if a.row.SessionSentAs == "cookie" && len(a.cookies) > 0 {
		parts := make([]string, 0, len(a.cookies))
		for _, name := range sortedKeys(a.cookies) {
			parts = append(parts, name+"="+a.cookies[name])
		}
		h.Set("Cookie", strings.Join(parts, "; "))
		return h
	}
	name, prefix, ok := strings.Cut(a.row.SessionSentAs, ":")
	if !ok || name == "" {
		return nil
	}
	for _, tk := range a.tokens {
		if tk.source == a.row.SessionSource && (len(a.row.SessionNames) == 0 || a.row.SessionNames[0] == tk.name) {
			h.Set(name, prefix+tk.value)
			return h
		}
	}
	return nil
}

// AccountOf는 요청 헤더가 실은 세션이 성공한 등록 계정 로그인의 것이면 그 라벨을 준다. 둘 이상의 계정 세션이 맞거나
// 성공하지 않은 세션이면 빈 값이다(모르면 귀속하지 않는다).
func (t *Tracker) AccountOf(h http.Header) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	sent := requestCookies(h)
	label := ""
	for _, a := range t.attempts {
		if a.seeded || a.row.Outcome != OutcomeSucceeded || a.cand.AccountLabel == "" {
			continue
		}
		carried := false
		for name, value := range a.cookies {
			carried = carried || sent[name] == value
		}
		for _, tk := range a.tokens {
			for _, values := range h {
				for _, v := range values {
					carried = carried || strings.Contains(v, tk.value)
				}
			}
		}
		if !carried {
			continue
		}
		if label != "" && label != a.cand.AccountLabel {
			return ""
		}
		label = a.cand.AccountLabel
	}
	return label
}

func (t *Tracker) rememberLocked(body []byte) {
	if len(body) == 0 {
		return
	}
	if len(body) > maxRecentBodyBytes {
		body = body[:maxRecentBodyBytes]
	}
	t.recent = append(t.recent, bytes.Clone(body))
	if len(t.recent) > maxRecentBodies {
		t.recent = t.recent[len(t.recent)-maxRecentBodies:]
	}
}

func (t *Tracker) fromResponseLocked(value string) bool {
	for _, b := range t.recent {
		if bytes.Contains(b, []byte(value)) {
			return true
		}
	}
	return false
}

// anonymousLocked는 요청이 실제로 인증을 싣지 않았는지 본다(§4 세션 없는 응답).
func (t *Tracker) anonymousLocked(h http.Header) bool {
	if h.Get("Cookie") != "" || h.Get("Authorization") != "" {
		return false
	}
	for name := range t.sessionHeaders {
		if h.Get(name) != "" {
			return false
		}
	}
	return true
}

// carrierLocked는 요청이 실은 세션의 시도(가장 늦은 것)와 실어 보낸 방식을 찾는다.
func (t *Tracker) carrierLocked(h http.Header) (*attempt, string, *token) {
	sent := requestCookies(h)
	for i := len(t.attempts) - 1; i >= 0; i-- {
		a := t.attempts[i]
		if !a.done || a.seeded || a.row.Transport != TransportSent {
			continue
		}
		for name, value := range a.cookies {
			if sent[name] == value {
				return a, "cookie", nil
			}
		}
		for k := range a.tokens {
			tk := &a.tokens[k]
			for name, values := range h {
				for _, v := range values {
					if idx := strings.Index(v, tk.value); idx >= 0 {
						prefix := v[:idx]
						if len(prefix) > maxSentAsPrefix {
							prefix = prefix[:maxSentAsPrefix]
						}
						return a, http.CanonicalHeaderKey(name) + ":" + prefix, tk
					}
				}
			}
		}
	}
	return nil, "", nil
}

// Observe는 로그인 시도가 아닌 요청을 보내고 받은 응답마다 부른다(정적 크롤·모델 요청 모두).
func (t *Tracker) Observe(req Request, resp Response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observeLocked(req, resp, false)
}

func (t *Tracker) observeLocked(req Request, resp Response, probe bool) {
	t.rememberLocked(resp.Body)
	t.obsSeq++
	p := cleanPath(req.Path)
	o := observation{key: urlKey(p, req.RawQuery), path: p, status: resp.Status, loc: redirectPath(t.cfg.Origin, p, resp), seq: t.obsSeq, probe: probe}
	// 실린 세션 후보를 먼저 본다. 사용자 지정 헤더(X-Auth-Token 등)로 실은 세션은 그 헤더 이름을 아직 모르는 첫 요청에서도
	// 무인증으로 분류되면 안 된다(#969 동찬 리뷰 P2).
	a, sentAs, tk := t.carrierLocked(req.Header)
	if a == nil && t.anonymousLocked(req.Header) {
		for name := range newCookies(resp, t.cfg.Now()) {
			t.anonCookies[name] = true
		}
		if hasPasswordInput(resp) {
			t.loginPaths[p] = true
		}
		if req.Method == "GET" && (len(t.anon) < maxAnonURLs || t.anon[o.key] != nil) {
			t.anon[o.key] = append(t.anon[o.key], o)
		}
		t.reevaluateLocked()
		return
	}
	if a == nil {
		return
	}
	if a.row.SessionSentAs == "" {
		a.row.SessionSentAs = sentAs
	}
	if tk != nil {
		t.sessionHeaders[strings.SplitN(sentAs, ":", 2)[0]] = true
		if a.row.SessionSource != tk.source || len(a.row.SessionNames) != 1 || a.row.SessionNames[0] != tk.name {
			a.row.SessionSource, a.row.SessionNames = tk.source, []string{tk.name}
		}
	}
	if req.Method == "GET" && a.row.Outcome == OutcomeSucceeded && !a.row.SessionEnded && o.key == a.contrastKey && t.blocked(o) {
		// 대조 URL이 이 세션으로 다시 막히면 세션 끊김이다(§2-2·§4). 다른 URL의 막힘은 보지 않는다.
		a.row.SessionEnded = true
		if t.cfg.Exception && a.cand.AccountLabel != "" {
			t.state[a.cand.AccountLabel] = StateLoggedOut
		}
	}
	if req.Method == "GET" && len(a.opened) < maxOpenedPerAttempt {
		a.opened = append(a.opened, o)
	}
	t.reevaluateLocked()
	t.emitLocked(a)
}

func (t *Tracker) blocked(o observation) bool {
	return o.status == http.StatusUnauthorized || o.status == http.StatusForbidden || o.loc != "" && t.loginPaths[o.loc]
}

func (t *Tracker) open(o observation) bool {
	return o.status > 0 && o.status < 400 && !(o.loc != "" && t.loginPaths[o.loc])
}

// reevaluateLocked는 지금까지의 관찰로 아직 대조 전인 시도의 뒤 요청 신호와 성공 시도의 세션 끊김을 다시 본다.
// 세션 없는 응답과 세션 응답의 순서를 따지지 않으므로(§4) 관찰마다 처음부터 본다. 시도·관찰 수는 상한이 있다.
func (t *Tracker) reevaluateLocked() {
	for _, a := range t.attempts {
		if !a.done || a.seeded {
			continue
		}
		switch a.row.Outcome {
		case OutcomeUnconfirmed:
			t.followupLocked(a)
		case OutcomeSucceeded:
			if a.row.SessionEnded {
				continue
			}
			for _, o := range a.opened {
				if o.seq > a.successSeq && o.key == a.contrastKey && t.blocked(o) {
					a.row.SessionEnded = true
					if t.cfg.Exception && a.cand.AccountLabel != "" {
						t.state[a.cand.AccountLabel] = StateLoggedOut
					}
					t.emitLocked(a)
					break
				}
			}
		}
	}
}

func (t *Tracker) followupLocked(a *attempt) {
	opened := append([]observation(nil), a.opened...)
	sort.Slice(opened, func(i, j int) bool { return opened[i].seq < opened[j].seq })
	followup := FollowupNone
	for _, o := range opened {
		if o.loc != "" && t.loginPaths[o.loc] {
			a.row.Followup = FollowupLoginForm
			t.setOutcomeLocked(a, OutcomeFailed)
			t.emitLocked(a)
			return
		}
		if !t.open(o) {
			continue
		}
		if src, ok := t.contrastLocked(o.key); ok {
			a.row.Followup, a.row.ContrastSource, a.contrastKey, a.successSeq = FollowupContrast, src, o.key, t.obsSeq
			t.setOutcomeLocked(a, OutcomeSucceeded)
			t.emitLocked(a)
			return
		}
		followup = FollowupNoLoginForm
		if len(t.anon[o.key]) == 0 && !a.probed[o.key] && len(a.probed) < t.cfg.MaxContrastProbes {
			a.probed[o.key] = true
			path, rawQuery, _ := strings.Cut(o.key, "?")
			t.pending = append(t.pending, Probe{Path: path, RawQuery: rawQuery, attempt: a})
		}
	}
	if a.row.Followup != followup {
		a.row.Followup = followup
		t.emitLocked(a)
	}
}

func (t *Tracker) contrastLocked(key string) (string, bool) {
	src, ok := "", false
	for _, o := range t.anon[key] {
		if !t.blocked(o) {
			continue
		}
		ok = true
		if !o.probe {
			return ContrastObserved, true
		}
		src = ContrastProbe
	}
	return src, ok
}

// NextProbe는 보낼 프로그램 대조 요청 하나를 꺼낸다. 이미 세션 없는 응답이 생긴 URL은 건너뛴다.
func (t *Tracker) NextProbe() (Probe, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.pending) > 0 {
		p := t.pending[0]
		t.pending = t.pending[1:]
		if len(t.anon[p.URLKey()]) > 0 || p.attempt.row.Outcome != OutcomeUnconfirmed {
			continue
		}
		p.attempt.row.ContrastProbes++
		t.emitLocked(p.attempt)
		return p, true
	}
	return Probe{}, false
}

// ProbeDone은 대조 요청의 응답을 받으면 부른다. 경계가 막아 보내지 못했으면 resp를 nil로 준다.
func (t *Tracker) ProbeDone(p Probe, resp *Response) {
	if resp == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observeLocked(Request{Method: "GET", Path: p.Path, RawQuery: p.RawQuery, Header: http.Header{}}, *resp, true)
}

func (t *Tracker) emitLocked(a *attempt) {
	if t.cfg.Sink == nil {
		return
	}
	b, err := json.Marshal(a.row)
	if err != nil || bytes.Equal(b, a.last) {
		return
	}
	a.last = b
	t.cfg.Sink(a.row)
}

// ExceptionState는 탐색 쓰기 예외의 계정 상태다(§2-2).
func (t *Tracker) ExceptionState(label string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stateLocked(label)
}

// AccountAuth는 등록 계정 라벨마다 화면 계정 상태를 정한다(§4 표, 위에서 처음 맞는 행). 아직 응답을 기다리는 시도는 보지 않는다.
func (t *Tracker) AccountAuth() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]string{}
	for _, acc := range t.cfg.Accounts {
		if acc.Label == "" {
			continue
		}
		var records, sent []*attempt
		succeeded := false
		for _, a := range t.attempts {
			if a.row.AccountLabel != acc.Label || a.row.Outcome == "" {
				continue
			}
			records = append(records, a)
			if a.row.Outcome != OutcomeNotSent {
				sent = append(sent, a)
				succeeded = succeeded || a.row.Outcome == OutcomeSucceeded
			}
		}
		switch {
		case len(records) == 0:
			out[acc.Label] = AuthNotAttempted
		case succeeded:
			out[acc.Label] = AuthAuthenticated
		case len(sent) == 0:
			out[acc.Label] = AuthNotSent
		default:
			last := sent[0]
			for _, a := range sent {
				if a.order > last.order {
					last = a
				}
			}
			switch last.row.Outcome {
			case OutcomeFailed:
				out[acc.Label] = AuthLoginFailed
			case OutcomeUnconfirmed:
				out[acc.Label] = AuthUnconfirmed
			default:
				out[acc.Label] = AuthResultUnknown
			}
		}
	}
	return out
}

// Seed는 앞 단계(탐색)의 기록 행으로 연속 실패 수와 계정 상태를 잇는다(§5 회차당). 세션 값은 없으므로 뒤 요청 판정에는
// 쓰지 않는다. 같은 attempt_ref의 마지막 행이 그 시도의 지금 값이고, 시도 순서는 처음 나온 순서다.
func (t *Tracker) Seed(rows []Row) {
	t.mu.Lock()
	defer t.mu.Unlock()
	byRef := map[string]*attempt{}
	var order []*attempt
	for _, r := range rows {
		if r.Schema != Schema || r.AttemptRef == "" {
			continue
		}
		a, ok := byRef[r.AttemptRef]
		if !ok {
			a = &attempt{seeded: true, done: true}
			byRef[r.AttemptRef] = a
			order = append(order, a)
		}
		a.row = r
		a.cand.Identifier, a.cand.AccountLabel = r.Identifier, r.AccountLabel
	}
	for _, a := range order {
		t.n++
		a.order = t.n
		switch a.row.Outcome {
		case OutcomeFailed:
			t.failures[a.cand.Identifier]++
		case OutcomeSucceeded:
			t.failures[a.cand.Identifier] = 0
		}
		t.attempts = append(t.attempts, a)
	}
}

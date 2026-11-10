package diagnosisprep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var reference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validText(s string, limit int) bool {
	if s == "" || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validTask(id string) bool {
	n, e := strconv.ParseInt(id, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

// CanonicalInput은 문자열만 검사한다. 설치 승인·DNS·연결 권위를 만들지 않는다.
func CanonicalInput(input Input) (Input, error) {
	raw := input.TargetURL
	if len(raw) == 0 || len(raw) > 2048 || !reference.MatchString(input.ProfileID) || (input.DisplayName != "" && !validText(input.DisplayName, 160)) {
		return Input{}, ErrProtocol
	}
	for _, r := range raw {
		if r > 127 || r <= 32 || r == 127 {
			return Input{}, ErrProtocol
		}
	}
	if strings.ContainsAny(raw, "\\%?#") {
		return Input{}, ErrProtocol
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return Input{}, ErrProtocol
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || strings.Contains(host, "%") {
		return Input{}, ErrProtocol
	}
	bracketed := strings.HasPrefix(u.Host, "[")
	if addr, err := netip.ParseAddr(host); err == nil {
		if bracketed != addr.Is6() {
			return Input{}, ErrProtocol
		}
		if addr.Is4In6() || addr.Zone() != "" || addr.String() != host {
			return Input{}, ErrProtocol
		}
	} else {
		if bracketed {
			return Input{}, ErrProtocol
		}
		if strings.Contains(host, ":") || len(host) > 253 {
			return Input{}, ErrProtocol
		}
		numeric := true
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return Input{}, ErrProtocol
			}
			for _, c := range label {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
					return Input{}, ErrProtocol
				}
				if c < '0' || c > '9' {
					numeric = false
				}
			}
		}
		// 브라우저별 정수·축약 IPv4 해석을 받아들이지 않는다.
		if numeric {
			return Input{}, ErrProtocol
		}
		last := host[strings.LastIndex(host, ".")+1:]
		if _, e := strconv.ParseUint(last, 10, 64); e == nil || strings.HasPrefix(last, "0x") {
			return Input{}, ErrProtocol
		}
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return Input{}, ErrProtocol
	}
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return Input{}, ErrProtocol
		}
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
		return Input{}, ErrProtocol
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return Input{}, ErrProtocol
		}
	}
	input.TargetURL = u.Scheme + "://" + authority + path
	return input, nil
}

func ValidateRequest(r Request) error {
	if (r.ExternalTransferConfirmed && r.Command != Start) || r.Version != Version || !reference.MatchString(r.RequestID) || !validText(r.Principal, 256) {
		return ErrProtocol
	}
	noInput := r.Input == nil
	switch r.Command {
	case Catalog:
		if r.JobID != "" || r.ExpectedRevision != 0 || r.IdempotencyKey != "" || !noInput || r.TaskID != "" {
			return ErrProtocol
		}
	case Create:
		if r.JobID != "" || r.ExpectedRevision != 0 || !reference.MatchString(r.IdempotencyKey) || noInput || r.TaskID != "" {
			return ErrProtocol
		}
		if _, err := CanonicalInput(*r.Input); err != nil {
			return err
		}
	case Lookup:
		if r.JobID != "" || r.ExpectedRevision != 0 || !reference.MatchString(r.IdempotencyKey) || !noInput || r.TaskID != "" {
			return ErrProtocol
		}
	case Validate:
		if !reference.MatchString(r.JobID) || r.ExpectedRevision == 0 || r.IdempotencyKey != "" || !noInput || r.TaskID != "" {
			return ErrProtocol
		}
	case Start:
		if !reference.MatchString(r.JobID) || r.ExpectedRevision == 0 || !reference.MatchString(r.IdempotencyKey) || !noInput || r.TaskID != "" {
			return ErrProtocol
		}
	case Status:
		if !reference.MatchString(r.JobID) || r.ExpectedRevision != 0 || r.IdempotencyKey != "" || !noInput || r.TaskID != "" {
			return ErrProtocol
		}
	case Stop:
		if !reference.MatchString(r.JobID) || r.ExpectedRevision == 0 || r.IdempotencyKey != "" || !noInput || r.TaskID != "" {
			return ErrProtocol
		}
	case AuthorizeTask:
		if r.JobID != "" || r.ExpectedRevision != 0 || r.IdempotencyKey != "" || !noInput || !validTask(r.TaskID) {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func validBudget(b Budget) bool {
	return b.Requests >= 0 && b.ModelRequests >= 0 && b.InputTokens >= 0 && b.OutputTokens >= 0 && b.CostUSDMicros >= 0 && b.DurationSeconds >= 0
}
func validModel(m *ModelInfo) bool {
	if m == nil {
		return true
	}
	return m.Mode == "openrouter" && m.Model == "qwen/qwen3.5-35b-a3b" && validText(m.Provider, 128) && m.Provider != "" && m.ExternalTransfer && m.InputPerCall == 262144 && m.OutputPerCall > 0 && m.OutputPerCall <= m.InputPerCall
}
func validScope(s *Scope) bool {
	if s == nil {
		return true
	}
	if !validModel(s.Model) || !reference.MatchString(s.TargetRef) || !reference.MatchString(s.ScopeRef) || !reference.MatchString(s.AttestationRef) || !validText(s.ApprovalMode, 64) || !validText(s.QueryPolicy, 64) || !validText(s.BodyPolicy, 64) || !validBudget(s.Budget) || s.MaxBodyBytes < 0 || s.ExpiresAt.IsZero() || len(s.PathPrefixes) == 0 || len(s.PathPrefixes) > 64 || len(s.Methods) == 0 || len(s.Methods) > 8 {
		return false
	}
	input, err := CanonicalInput(Input{TargetURL: s.Origin, ProfileID: "scope"})
	if err != nil || input.TargetURL != s.Origin+"/" {
		return false
	}
	for _, p := range s.PathPrefixes {
		if len(p) == 0 || p[0] != '/' {
			return false
		}
		c, e := CanonicalInput(Input{TargetURL: s.Origin + p, ProfileID: "scope"})
		if e != nil || c.TargetURL != s.Origin+p {
			return false
		}
	}
	for _, m := range s.Methods {
		if m != "GET" && m != "HEAD" {
			return false
		}
	}
	return true
}
func validUsage(u *ModelUsage) bool {
	return u == nil || (u.Requests >= 0 && u.InputTokens >= 0 && u.OutputTokens >= 0 && u.ReservedInputTokens >= 0 && u.ReservedOutputTokens >= 0 && u.CostUSDMicros >= 0 && u.ReservedCostUSDMicros >= 0)
}
func validPhase(p Phase) bool {
	switch p {
	case DraftPhase, ScopeCheck, TaskCreation, Planning, Authority, Gateway, ExecutionPhase, Closure, Report:
		return true
	}
	return false
}
func terminal(s State) bool { return s == Completed || s == Cancelled || s == Failed }
func active(s State) bool   { return s == Preparing || s == Running || s == Stopping }
func validJob(j Job) bool {
	if !reference.MatchString(j.ID) || j.Revision == 0 || !digestPattern.MatchString(j.SourceDigest) || !validPhase(j.Phase) || !validScope(j.Scope) || !validUsage(j.ModelUsage) || (j.ReasonCode != "" && !reasonPattern.MatchString(j.ReasonCode)) || (j.TaskID != "" && !validTask(j.TaskID)) {
		return false
	}
	in, e := CanonicalInput(j.Input)
	if e != nil || in != j.Input {
		return false
	}
	switch j.AuthorityState {
	case AuthorityNotIssued, AuthorityIssued, AuthorityConsumed, AuthorityUnknown:
	default:
		return false
	}
	if j.Started && (!j.PreparationStarted || j.TaskID == "" || (j.AuthorityState != AuthorityConsumed && j.AuthorityState != AuthorityUnknown)) {
		return false
	}
	if (j.DrainComplete || j.ResultReady) && !j.PreparationStarted {
		return false
	}
	if j.ResultReady && j.TaskID == "" {
		return false
	}
	switch j.State {
	case AwaitingApproval, Ready:
		return !j.PreparationStarted && !j.Started && !j.DrainComplete && !j.ResultReady && j.TaskID == "" && j.AuthorityState == AuthorityNotIssued
	case Preparing:
		return j.PreparationStarted && !j.Started
	case Running:
		return j.PreparationStarted && j.Started && !j.DrainComplete && !j.ResultReady
	case Stopping:
		return j.PreparationStarted && !j.ResultReady
	case Completed:
		return j.PreparationStarted && j.Started && j.DrainComplete && j.ResultReady
	case Cancelled, Failed:
		return j.PreparationStarted && j.DrainComplete && (!j.Started || j.ResultReady)
	case Unknown:
		return j.PreparationStarted
	default:
		return false
	}
}
func validError(code string) bool {
	switch code {
	case "invalid_request", "not_found", "forbidden", "revision_conflict", "idempotency_conflict", "approval_changed", "approval_required", "busy", "budget_exhausted", "capacity", "unavailable", "unknown", "service_closed":
		return true
	}
	return false
}
func ValidateResponse(r Request, response Response) error {
	if ValidateRequest(r) != nil || response.Version != Version || response.RequestID != r.RequestID {
		return ErrProtocol
	}
	if response.Error != "" {
		if !validError(response.Error) || response.Job != nil || response.Authorized != nil || len(response.Profiles) != 0 {
			return ErrProtocol
		}
		return nil
	}
	switch r.Command {
	case Catalog:
		if response.Job != nil || response.Authorized != nil || len(response.Profiles) > 256 {
			return ErrProtocol
		}
		seen := map[string]bool{}
		for _, p := range response.Profiles {
			if !validModel(p.Model) || !reference.MatchString(p.ID) || seen[p.ID] || !reference.MatchString(p.ModelProfileRef) || !validText(p.Label, 160) || !validText(p.ModelLabel, 160) {
				return ErrProtocol
			}
			seen[p.ID] = true
		}
	case AuthorizeTask:
		if response.Authorized == nil || len(response.Profiles) != 0 {
			return ErrProtocol
		}
		if *response.Authorized {
			if response.Job == nil || !validJob(*response.Job) || response.Job.TaskID != r.TaskID {
				return ErrProtocol
			}
		} else if response.Job != nil {
			return ErrProtocol
		}
	default:
		if response.Job == nil || !validJob(*response.Job) || response.Authorized != nil || len(response.Profiles) != 0 {
			return ErrProtocol
		}
		if r.JobID != "" && response.Job.ID != r.JobID {
			return ErrProtocol
		}
	}
	return nil
}

var rawType = reflect.TypeFor[json.RawMessage]()
var timeType = reflect.TypeFor[time.Time]()

func strictDecode(raw []byte, dst any) error {
	if len(raw) == 0 || !utf8.Valid(raw) || !closedShape(raw, reflect.TypeOf(dst).Elem()) {
		return ErrProtocol
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil {
		return ErrProtocol
	}
	if dec.Decode(new(any)) != io.EOF {
		return ErrProtocol
	}
	return nil
}
func closedShape(raw []byte, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if t == rawType {
		return uniqueJSON(raw)
	}
	if t == timeType {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		d := json.NewDecoder(bytes.NewReader(raw))
		token, e := d.Token()
		if e != nil || token != json.Delim('{') {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			if name != "-" {
				fields[name] = f.Type
			}
		}
		seen := map[string]bool{}
		for d.More() {
			token, e = d.Token()
			key, ok := token.(string)
			ft, known := fields[key]
			if e != nil || !ok || !known || seen[key] {
				return false
			}
			seen[key] = true
			var value json.RawMessage
			if d.Decode(&value) != nil || !closedShape(value, ft) {
				return false
			}
		}
		if _, e = d.Token(); e != nil {
			return false
		}
		_, e = d.Token()
		return e == io.EOF
	case reflect.Slice:
		var parts []json.RawMessage
		if json.Unmarshal(raw, &parts) != nil {
			return false
		}
		for _, p := range parts {
			if !closedShape(p, t.Elem()) {
				return false
			}
		}
	}
	return true
}
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() bool
	walk = func() bool {
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token != nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !walk() {
					return false
				}
			}
		case '[':
			for d.More() {
				if !walk() {
					return false
				}
			}
		default:
			return false
		}
		_, err = d.Token()
		return err == nil
	}
	if !walk() {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// SourceDigest는 정본 입력과 보호 설치 digest를 같은 기록 문법으로 결속한다.
// 호출자는 CanonicalInput과 실제 설치 자료 검증을 먼저 수행해야 한다.
func SourceDigest(input Input, installation string) string {
	raw, _ := json.Marshal(struct {
		Input        Input
		Installation string
	}{input, installation})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

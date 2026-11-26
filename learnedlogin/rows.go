package learnedlogin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/internal/privateinput"
)

// AttemptsFile은 회차 상태 디렉터리의 로그인 시도 기록 파일 이름이다(§6, 0600, 덧붙이기).
const AttemptsFile = "login-attempts.jsonl"

// 기록 파일 상한이다. 한 줄은 칸 이름·상한 있는 목록뿐이라 64KiB에 닿지 않는다(판정기와 같은 상한). 파일 전체가 상한을
// 넘으면 일부만 읽어 최신 값을 잘못 고를 수 있으므로 통째로 쓰지 않는다(학습 없음).
const (
	maxRowBytes   = 64 << 10
	maxFileBytes  = 16 << 20
	maxIdentifier = 256
)

// Row는 로그인 시도 기록 한 행이다(§3 칸 전부). 비밀번호·세션 값·응답 본문은 없다. 같은 attempt_ref의 마지막 행이 그
// 시도의 지금 값이고, 시도 순서는 처음 나온 순서다(§3·§4).
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

// ErrRecord는 기록 파일이 있는데 보호 파일이 아니거나(권한·소유자·링크) 읽을 수 없다는 뜻이다. 학습하지 않는다.
var ErrRecord = errors.New("learnedlogin: attempt record unavailable")

// ReadRows는 회차 상태 디렉터리의 기록을 읽는다. 파일이 없으면 빈 목록이다. 보호 파일 검사(0600·실행 uid·링크 아님·
// 읽는 동안 바뀌지 않음)를 통과한 파일만 읽는다. 손상된 줄이 있으면 최신 상태를 알 수 없으므로 전체 기록을 거부한다.
func ReadRows(stateDir string) ([]Row, int, error) {
	path := stateDir + string(os.PathSeparator) + AttemptsFile
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	raw, err := privateinput.ReadFile(path, maxFileBytes)
	if err != nil {
		return nil, 0, ErrRecord
	}
	defer clear(raw)
	return ParseRows(raw)
}

// ParseRows는 JSON Lines 기록을 읽는다. 손상되거나 상한을 넘는 줄이 있으면 전체를 거부한다.
func ParseRows(raw []byte) ([]Row, int, error) {
	var rows []Row
	skipped := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 4096), maxRowBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row Row
		if !utf8.Valid(line) || json.Unmarshal(line, &row) != nil || !unambiguousRowKeys(line) ||
			row.Schema != RowSchema || !validRef(row.AttemptRef) {
			skipped++
			continue
		}
		rows = append(rows, row)
	}
	if scanner.Err() != nil {
		return nil, 0, ErrRecord
	}
	if skipped > 0 {
		return nil, skipped, ErrRecord
	}
	return rows, skipped, nil
}

// unambiguousRowKeys는 JSON 해석 시 뒤의 값이 앞의 값을 덮는 행을 거부한다. 구조체 디코더가 같은 칸으로 보는
// 대소문자 변형도 중복이다. §3 행의 키는 모두 ASCII이고 중첩 객체는 없다.
func unambiguousRowKeys(line []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(line))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	var value json.RawMessage
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || len(key) != utf8.RuneCountInString(key) {
			return false
		}
		key = strings.ToLower(key)
		if seen[key] || dec.Decode(&value) != nil {
			return false
		}
		seen[key] = true
	}
	return true
}

// attempt는 attempt_ref 하나의 행들이다.
type attempt struct {
	first   Row
	current Row
	order   int
	broken  bool
}

// fixedFieldsEqual은 한 시도 안에서 바뀌면 안 되는 칸(누가·어디로·무엇을 보냈는지)이 같은지 본다. 판정기는 같은 시도의
// 결과·뒤 요청 칸만 덧붙이기로 고친다(§3). 다르면 같은 참조에 다른 시도가 섞인 기록이라 학습하지 않는다.
func fixedFieldsEqual(a, b Row) bool {
	return a.RunRef == b.RunRef && a.Stage == b.Stage && a.Actor == b.Actor && a.At == b.At && a.AccountLabel == b.AccountLabel &&
		a.Identifier == b.Identifier && a.PasswordRegistered == b.PasswordRegistered && a.Method == b.Method && a.Path == b.Path &&
		slices.Equal(a.QueryKeys, b.QueryKeys) && a.Format == b.Format && a.WriteException == b.WriteException &&
		a.IdentifierField == b.IdentifierField && a.PasswordField == b.PasswordField && slices.Equal(a.OtherFields, b.OtherFields) &&
		slices.Equal(a.FieldsFromResponse, b.FieldsFromResponse) && (a.TransportRef == "" || b.TransportRef == a.TransportRef)
}

// attempts는 행을 시도로 묶는다. 순서는 attempt_ref가 처음 나온 순서이고, 지금 값은 마지막 행이다.
func attempts(rows []Row) []*attempt {
	byRef := map[string]*attempt{}
	var out []*attempt
	for _, r := range rows {
		a, ok := byRef[r.AttemptRef]
		if !ok {
			a = &attempt{first: r, current: r, order: len(out)}
			byRef[r.AttemptRef] = a
			out = append(out, a)
			continue
		}
		if !fixedFieldsEqual(a.current, r) {
			a.broken = true
		}
		a.current = r
	}
	return out
}

// Selection은 계정 하나의 이 회차 학습 결과다. Reason이 비어 있으면 Learned가 쓸 수 있는 방법이다.
type Selection struct {
	Learned Learned
	Reason  Reason
}

// Select는 이 회차(runRef)의 기록에서 결속된 계정 라벨마다 §6의 "account_label이 있고 outcome: succeeded인 마지막 시도"를
// 고른다. 적격 시도만 본다: 같은 회차, 모델이 탐색·진단 단계에서 보낸(sent) 등록 계정의 등록 비밀번호 로그인이고, 성공 근거가
// 인증 대조(authenticated_contrast)이며, 실패 신호(400 이상·로그인 화면)가 없다. 마지막 성공 시도의 방법이 재현할 수 없는
// 모양이면 그 사유를 남기고 더 앞의 성공으로 되돌아가지 않는다(가장 최근 성공이 지금 대상의 로그인 방식이다). 고른 적격
// 시도가 없는 라벨은 결과에 없다. 칸이 바뀐 시도(같은 참조에 다른 값)가 있는 라벨은 어느 시도도 학습하지 않고 ReasonRecordInvalid다.
func Select(rows []Row, runRef string, labels []string) map[string]Selection {
	bound := map[string]bool{}
	for _, l := range labels {
		bound[l] = true
	}
	out := map[string]Selection{}
	if runRef == "" {
		return out
	}
	latest := map[string]*attempt{}
	broken := map[string]bool{}
	brokenRefs := map[string]bool{}
	for _, a := range attempts(rows) {
		r := a.current
		if a.broken {
			brokenRefs[r.AttemptRef] = true
			continue
		}
		if r.RunRef != runRef || !bound[r.AccountLabel] {
			continue
		}
		if !eligible(r) {
			continue
		}
		if prev := latest[r.AccountLabel]; prev == nil || a.order > prev.order {
			latest[r.AccountLabel] = a
		}
	}
	for _, r := range rows {
		if brokenRefs[r.AttemptRef] && r.RunRef == runRef && bound[r.AccountLabel] {
			broken[r.AccountLabel] = true
		}
	}
	for label, a := range latest {
		r := a.current
		at, err := time.Parse(time.RFC3339Nano, r.At)
		m := Method{Method: r.Method, Path: r.Path, Format: r.Format, IdentifierField: r.IdentifierField, PasswordField: r.PasswordField,
			OtherFields: r.OtherFields, FieldsFromResponse: r.FieldsFromResponse, SessionSource: r.SessionSource, SessionNames: r.SessionNames,
			SessionSentAs: r.SessionSentAs}.Clone()
		sel := Selection{Learned: Learned{Method: m, Source: Source{RunRef: r.RunRef, AttemptRef: r.AttemptRef, At: at.UTC()}}}
		switch {
		case err != nil || at.IsZero():
			sel = Selection{Reason: ReasonRecordInvalid}
		case len(r.QueryKeys) > 0:
			// 질의 값은 기록에 없다(§3 query_keys는 이름뿐). 질의가 있던 로그인은 다시 만들 수 없다.
			sel = Selection{Reason: ReasonInputUnavailable}
		default:
			if reason := m.Validate(); reason != "" {
				sel = Selection{Reason: reason}
			}
		}
		out[label] = sel
	}
	// 칸이 바뀐 시도가 하나라도 있는 라벨은 어느 시도도 믿지 않는다. 그 시도를 건너뛰고 더 앞의 성공을 고르면 최신 값을
	// 잘못 고를 수 있다.
	for label := range broken {
		out[label] = Selection{Reason: ReasonRecordInvalid}
	}
	return out
}

// eligible은 학습할 수 있는 성공 시도인지다(위 Select 설명).
func eligible(r Row) bool {
	return r.Actor == ActorModel && (r.Stage == StageExploration || r.Stage == StageDiagnosis) && r.Transport == TransportSent &&
		r.Outcome == OutcomeSucceeded && r.Followup == FollowupContrast && r.PasswordRegistered && validRef(r.TransportRef) &&
		r.Status >= 100 && r.Status < 400 && !r.LoginFormReturned && r.SessionSource != "" && r.SessionSource != SourceNone
}

// FailureCounts는 회차 기록으로 아이디별 연속 실패 수를 다시 센다(§5: 탐색·진단·확정 단계를 합쳐 회차당). failed면 1을
// 더하고 succeeded면 0으로 돌린다. 다른 결과는 세지도 되돌리지도 않는다. 판정기의 Seed와 같은 규칙이다.
func FailureCounts(rows []Row, runRef string) map[string]int {
	out := map[string]int{}
	for _, a := range attempts(rows) {
		r := a.current
		if r.RunRef != runRef || len(r.Identifier) > maxIdentifier {
			continue
		}
		switch r.Outcome {
		case OutcomeFailed:
			out[r.Identifier]++
		case OutcomeSucceeded:
			out[r.Identifier] = 0
		}
	}
	return out
}

// NextAttemptNumber는 이 회차 기록에 다음으로 매길 시도 번호다(판정기와 같이 단계 이름-번호, 기존 시도 수 + 1).
func NextAttemptNumber(rows []Row) int { return len(attempts(rows)) + 1 }

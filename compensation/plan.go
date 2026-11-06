// Package compensation은 승인된 쓰기 기록에서 지워도 되는 새 자원을 고른다(docs/contracts/compensation-planner.md §3·§4).
//
// 순수 함수만 둔다. 네트워크·DB·파일을 쓰지 않고 승인도 발급하지 않는다. 실제 전송과 재검사는 E②의 보상 신뢰 경로가 맡는다.
// 잘못 지우면 원래 자원이 사라지므로 근거가 모자라면 지우지 않는 쪽으로 정한다.
package compensation

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Outcome은 E② 신뢰 결과 5상태다.
type Outcome string

const (
	OutcomePolicyDenied     Outcome = "policy_denied"
	OutcomeNotSent          Outcome = "not_sent"
	OutcomeSendAttempted    Outcome = "send_attempted"
	OutcomeResponseReceived Outcome = "response_received"
	OutcomeUnknown          Outcome = "outcome_unknown"
)

// Record는 승인된 쓰기 시도 하나다. RawTarget·Location·BodyID는 서버 내부 전용이라 공유 기록에 내보내지 않는다.
type Record struct {
	AttemptID  string
	Sequence   uint64
	SessionRef string
	Method     string
	Origin     string
	RawTarget  string
	Outcome    Outcome
	Status     int
	Location   string
	BodyID     string
}

// Class는 §3 분류다. ClassNone은 대상이 바뀌지 않아 칸에도 넣지 않는 기록이다.
type Class string

const (
	ClassNone         Class = "none"
	ClassRecordOnly   Class = "record_only"
	ClassCreateStrong Class = "create_strong"
	ClassCreateWeak   Class = "create_weak"
)

// Rules는 앱별 설정이다. 코드에 경로나 테이블을 넣지 않는다.
type Rules struct {
	// WeakPaths는 본문 id로 만든 생성 신호를 믿어도 되는 POST 경로(쿼리 없음)다.
	WeakPaths []string
	// TableByPath는 POST 경로 → 상태 차이의 테이블 이름이다.
	TableByPath map[string]string
}

// Classification은 분류와 지울 경로다. DeletePath는 생성 분류에서만 채운다.
type Classification struct {
	Class      Class
	PostPath   string
	DeletePath string
	ResourceID string
}

var (
	safeBodyID  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	positiveInt = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

// Classify는 §3 표를 위에서부터 적용한다. 응답 신호만 본다. 지워도 되는지는 Confirm이 상태 차이로 다시 거른다.
func Classify(record Record, rules Rules) Classification {
	if record.Outcome == OutcomePolicyDenied || record.Outcome == OutcomeNotSent {
		return Classification{Class: ClassNone}
	}
	if record.Method != "POST" {
		return Classification{Class: ClassRecordOnly}
	}
	postPath, ok := targetPath(record.RawTarget, record.Origin)
	if !ok {
		return Classification{Class: ClassRecordOnly}
	}
	// 생성 신호가 없어도 PostPath를 남긴다. 어느 테이블에 id를 못 정한 쓰기가 있었는지 놓침 판정이 알아야 한다.
	// 결과를 모르는 쓰기는 지우지 않는다. 모르는 상태값도 여기로 온다.
	if record.Outcome != OutcomeResponseReceived || record.Status < 200 || record.Status > 299 {
		return Classification{Class: ClassRecordOnly, PostPath: postPath}
	}
	if record.Status == 201 {
		if path, ok := sameOriginPath(record.Location, record.Origin, postPath); ok {
			return Classification{Class: ClassCreateStrong, PostPath: postPath, DeletePath: path, ResourceID: lastSegment(path)}
		}
	}
	if safeBodyID.MatchString(record.BodyID) && contains(rules.WeakPaths, postPath) {
		return Classification{Class: ClassCreateWeak, PostPath: postPath,
			DeletePath: strings.TrimRight(postPath, "/") + "/" + record.BodyID, ResourceID: record.BodyID}
	}
	return Classification{Class: ClassRecordOnly, PostPath: postPath}
}

// targetPath는 요청 대상의 경로 부분만 돌려준다. 쿼리·fragment는 버린다. 절대 형식이면 origin이 같아야 한다.
func targetPath(rawTarget, origin string) (string, bool) {
	head, _, _ := strings.Cut(rawTarget, "#")
	parsed, err := url.ParseRequestURI(head)
	if err != nil {
		return "", false
	}
	if parsed.IsAbs() {
		base, err := url.Parse(origin)
		if err != nil || parsed.Scheme != base.Scheme || parsed.Host != base.Host || parsed.User != nil {
			return "", false
		}
	}
	path := parsed.EscapedPath()
	if !safePath(path) {
		return "", false
	}
	return path, true
}

// sameOriginPath는 Location을 요청 기준으로 풀어 같은 origin일 때 경로만 돌려준다.
func sameOriginPath(location, origin, postPath string) (string, bool) {
	if location == "" {
		return "", false
	}
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", false
	}
	parsed, err := url.Parse(location)
	if err != nil || hasDotSegment(parsed.EscapedPath()) {
		return "", false
	}
	resolved := base.ResolveReference(&url.URL{Path: postPath}).ResolveReference(parsed)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host || resolved.User != nil {
		return "", false
	}
	path := resolved.EscapedPath()
	if !safePath(path) || lastSegment(path) == "" {
		return "", false
	}
	// 상태 차이는 POST 경로의 테이블만 본다. 다른 컬렉션의 Location을 지우면 같은 번호의 다른 자원이 사라질 수 있다.
	if path != strings.TrimRight(postPath, "/")+"/"+lastSegment(path) {
		return "", false
	}
	return path, true
}

// safePath는 경로 이동과 인코딩된 구분자가 없는 절대 경로만 받는다. 지울 대상이 다른 자원으로 바뀌면 안 된다.
func safePath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.Contains(path, "%") && !strings.Contains(path, "\\") &&
		!strings.Contains(path, "//") && !hasDotSegment(path)
}

// hasDotSegment는 정규화로 다른 자원을 가리키게 되는 경로를 찾는다. 풀기 전에 봐야 한다. 풀고 나면 점 구간이 사라진다.
func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func lastSegment(path string) string {
	return path[strings.LastIndexByte(path, '/')+1:]
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// TableDiff는 pre → 보상 직전 상태 차이의 한 테이블이다. Added가 nil이면 정수 키가 없거나 스키마가 바뀌어 잴 수 없는 것이고,
// 부재 증거가 아니다. 빈 슬라이스는 재서 추가가 없다는 뜻이다.
type TableDiff struct {
	Added   []int64
	Changed []int64
}

// StateDiff는 pre → 보상 직전 차이다. Measured가 false면 상태 차이를 못 잰 회차다.
type StateDiff struct {
	Measured bool
	Tables   map[string]TableDiff
}

// Action은 후보 하나에 대한 결론이다.
type Action string

const (
	ActionDelete     Action = "delete"
	ActionRecordOnly Action = "record_only"
	ActionUndecided  Action = "undecidable"
)

// Reason은 공유 기록에 남겨도 되는 고정 사유다. 경로·id 원문을 담지 않는다.
type Reason string

const (
	ReasonAddedSinceStart Reason = "added_since_pre"
	ReasonExistingChanged Reason = "existing_resource_changed"
	ReasonNoSignal        Reason = "no_create_signal"
	ReasonNoTable         Reason = "no_table_for_path"
	ReasonNotInteger      Reason = "resource_id_not_integer"
	ReasonAddedUnknown    Reason = "added_ids_unavailable"
	ReasonNotInAdded      Reason = "not_in_added_ids"
	ReasonDiffUnavailable Reason = "state_diff_unavailable"
)

// Step은 계획의 한 줄이다. 삭제가 아니면 DeletePath를 비운다.
type Step struct {
	AttemptID  string
	Sequence   uint64
	SessionRef string
	Class      Class
	Action     Action
	Reason     Reason
	Origin     string
	DeletePath string
	Table      string
	ID         int64
}

// ErrInvalidRecords는 시도 ID가 비었거나 시도 ID·순번이 겹치는 기록이다. 어느 시도인지 모르면 계획을 만들지 않는다.
var ErrInvalidRecords = errors.New("compensation: invalid write records")

// Build는 기록 전체를 sequence 역순 계획으로 만든다. ClassNone 기록은 넣지 않는다.
func Build(records []Record, rules Rules, diff StateDiff) ([]Step, error) {
	attempts := make(map[string]bool, len(records))
	sequences := make(map[uint64]bool, len(records))
	steps := make([]Step, 0, len(records))
	for _, record := range records {
		if record.AttemptID == "" || attempts[record.AttemptID] || sequences[record.Sequence] {
			return nil, ErrInvalidRecords
		}
		attempts[record.AttemptID], sequences[record.Sequence] = true, true
		classification := Classify(record, rules)
		if classification.Class == ClassNone {
			continue
		}
		steps = append(steps, confirm(record, classification, rules, diff))
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Sequence > steps[j].Sequence })
	return steps, nil
}

// confirm은 §4 "지우기 전 근거"다. 2xx 응답·사전 GET·1회 승인은 신규 생성의 근거가 아니다. 기존 자원을 갱신하고 그 id를
// 돌려주는 POST를 그대로 지우면 원래 자원이 사라지므로, pre 뒤에 새로 생겼다는 상태 차이가 있어야 지운다.
func confirm(record Record, classification Classification, rules Rules, diff StateDiff) Step {
	step := Step{AttemptID: record.AttemptID, Sequence: record.Sequence, SessionRef: record.SessionRef, Class: classification.Class,
		Action: ActionUndecided, Origin: record.Origin}
	table := rules.TableByPath[classification.PostPath]
	step.Table = table
	if classification.Class == ClassRecordOnly {
		step.Action, step.Reason = ActionRecordOnly, ReasonNoSignal
		return step
	}
	if !diff.Measured {
		step.Reason = ReasonDiffUnavailable
		return step
	}
	if table == "" {
		step.Reason = ReasonNoTable
		return step
	}
	if !positiveInt.MatchString(classification.ResourceID) {
		step.Reason = ReasonNotInteger
		return step
	}
	id, err := strconv.ParseInt(classification.ResourceID, 10, 64)
	if err != nil {
		step.Reason = ReasonNotInteger
		return step
	}
	step.ID = id
	tableDiff, measured := diff.Tables[table]
	switch {
	case measured && containsID(tableDiff.Changed, id):
		step.Action, step.Reason = ActionRecordOnly, ReasonExistingChanged
	case !measured || tableDiff.Added == nil:
		step.Reason = ReasonAddedUnknown
	case containsID(tableDiff.Added, id):
		step.Action, step.Reason, step.DeletePath = ActionDelete, ReasonAddedSinceStart, classification.DeletePath
	default:
		step.Reason = ReasonNotInAdded
	}
	return step
}

func containsID(values []int64, value int64) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// SharedStep은 러너에 넘기는 비민감 투영이다. 경로·Location·본문 id 원문은 없다. ID는 상태 차이에 이미 있는 정수 키이고 못 정했으면 null이다.
type SharedStep struct {
	AttemptID string `json:"attempt_id"`
	Sequence  uint64 `json:"sequence"`
	Class     Class  `json:"class"`
	Action    Action `json:"action"`
	Reason    Reason `json:"reason"`
	Table     string `json:"table"`
	ID        *int64 `json:"id"`
}

// Shared는 공유 기록용 투영을 만든다.
func (s Step) Shared() SharedStep {
	shared := SharedStep{AttemptID: s.AttemptID, Sequence: s.Sequence, Class: s.Class, Action: s.Action, Reason: s.Reason, Table: s.Table}
	if s.ID > 0 {
		id := s.ID
		shared.ID = &id
	}
	return shared
}

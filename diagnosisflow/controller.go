package diagnosisflow

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/pathscope"
)

type Controller struct {
	explorationReader ExplorationReader
	mu                sync.Mutex
	binding           Binding
	store             Store
	record            Record
	changed           chan struct{}
	poisoned          bool
	// observationFailures는 요청 관찰 저장이 연달아 실패한 수다. 성공하면 0으로 돌아간다(C13).
	observationFailures int
	claimed             bool
	attempts            map[string]bool
	// uncertainWrites는 결과 불명으로 끝난 상태 변경 가능 요청(쓰기 메서드, attested_read가 아닌 끝점)의 "메서드 경로"다. 대상이 받았을 수 있어 이 회차에서는 같은
	// 메서드·경로를 다시 보내지 않는다(다른 명령·다른 intent여도). 상태를 확인하는 장치가 없어 회차 끝까지 유지한다
	// (CLAUDE.md "결과 불명 쓰기는 자동으로 다시 보내지 않는다", 계약 run-outcome-states 열린 항목 3, #525 리뷰).
	uncertainWrites map[string]bool
	// floorCache는 끝점 ID별 압축 바닥 크기(받을 때 잰 값)다. 끝점 바이트 절단 검사가 받을 때마다 목록 전체를 다시
	// 직렬화하지 않게 한다. 받은 뒤 늘어나는 몫은 endpointGrowthReserve가 덮는다. 저장 기록에는 남기지 않는다.
	floorCache map[string]int
	origin     string
	nativePath string
	// 문서별 <base href> 해석 결과. 초기 탐색 안에서만 쓰므로 저장하지 않는다.
	documentBases map[string]documentBase
}

type documentBase struct {
	path  string
	known bool
}

func New(ctx context.Context, b Binding, store Store) (*Controller, error) {
	u, err := url.Parse(b.TargetURL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !ValidMode(b.Mode) || !b.Limits.Valid() || b.Current == nil || b.Current() != nil || store == nil || b.JobID == "" || b.TaskID == "" || b.SourceDigest == "" || b.Owner == "" || len(b.PathPrefixes) == 0 || len(b.ReadonlyPrefixes) == 0 {
		return nil, ErrUnavailable
	}
	for _, expression := range append(append([]string{}, b.PathPrefixes...), b.ReadonlyPrefixes...) {
		if _, ok := pathscope.Parse(expression); !ok {
			return nil, ErrDenied
		}
	}
	c := &Controller{binding: b, store: store, changed: make(chan struct{}), attempts: map[string]bool{}, uncertainWrites: map[string]bool{}, floorCache: map[string]int{}, origin: u.Scheme + "://" + u.Host}
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
	return pathscope.Allows(path, false, prefix)
}
func has(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// 위험 동작 단어(C23). 경로 조각을 단어 단위로 나눠 통째로 맞춘다. 부분 문자열로 맞추면 payments-history 같은
// 정상 읽기 경로까지 막는다.
var (
	// destructiveWords는 메서드와 상관없이 막는다. GET으로도 상태가 바뀌는 경로(GET /logout, GET /delete/1)가 흔하다.
	// signout·logoff는 logout과 같은 동작이다. 시험 계정 세션이 탐색 중 끊기면 그 뒤 탐색·진단이 모두 비인증이 된다.
	destructiveWords = []string{"delete", "remove", "reset", "logout", "signout", "logoff", "destroy", "truncate", "shutdown"}
	// transactionWords는 쓰기 메서드에서만 막는다. 결제·주문 내역을 읽는 GET·HEAD는 진단 범위다.
	transactionWords = []string{"checkout", "payment", "payments", "purchase", "purchases", "transfer", "transfers"}
	// accountClosureWords도 쓰기 메서드에서만 막는다. 계정을 닫거나 지우는 쓰기(탈퇴·해지)다. 모델이 등록 계정으로 직접
	// 로그인하게 되면서(2026-11-26) 아이디·비밀번호를 다시 넣어 확인하는 탈퇴 요청이 로그인과 모양이 같아진 것을 진단 쓰기
	// 정책에서 보조로 막는다(팀 합의, docs/contracts/model-login-attempts.md §2-2 남는 위험). 탐색 경계의 위험 단어
	// (DestructiveWords, 부분 문자열 맞춤)에는 넣지 않는다: /closet·/disclose 같은 탐색 GET을 막게 된다.
	accountClosureWords = []string{"close", "deactivate", "withdraw", "unregister", "terminate", "erase"}
)

// riskTokens는 경로 조각·query 이름·값을 영문자·숫자 단어로 나눈다. camelCase 경계와 글자·숫자 경계(reset2fa)도 나눈다.
func riskTokens(s string) []string {
	var out []string
	var b strings.Builder
	var prevLower, prevDigit bool
	flush := func() {
		if b.Len() > 0 {
			out = append(out, strings.ToLower(b.String()))
			b.Reset()
		}
	}
	for _, r := range s {
		isLetter, isDigit := unicode.IsLetter(r), unicode.IsDigit(r)
		if !isLetter && !isDigit {
			flush()
			prevLower = false
			continue
		}
		if isLetter && unicode.IsUpper(r) && prevLower || b.Len() > 0 && isDigit != prevDigit {
			flush()
		}
		b.WriteRune(r)
		prevLower = isLetter && unicode.IsLower(r)
		prevDigit = isDigit
	}
	flush()
	return out
}

// anyToken은 단어 하나나 이웃 두 단어를 붙인 것이 목록에 있는지 본다. logOut·shutDown·checkOut·log-out·sign_out처럼
// 합성어를 camelCase나 구분자로 나눠 쓴 경로는 나누면 log/out이 돼 빠지므로 붙여서도 본다(#487 리뷰 P1). 앞뒤 일치
// (deleteall·deleted)까지 넓히면 정상 조회 경로를 많이 막아 탐색을 깎으므로 넣지 않는다.
// DestructiveWords는 메서드와 상관없이 막는 파괴 동작 단어의 복사본이다. 탐색 경계의 대상 위험 단어로도 쓴다.
func DestructiveWords() []string { return append([]string(nil), destructiveWords...) }

func anyToken(s string, words []string) bool {
	tokens := riskTokens(s)
	for i, token := range tokens {
		if has(words, token) || i+1 < len(tokens) && has(words, token+tokens[i+1]) {
			return true
		}
	}
	return false
}

// actionKeys는 값이 실행할 동작을 고르는 query 이름이다. 값의 위험 단어는 이 이름일 때만 본다. 검색어·정렬 같은
// 값(?q=delete, ?sort=remove_date)까지 보면 정상 조회를 막아 탐색을 깎는다(#487 리뷰 P3).
var actionKeys = []string{"action", "act", "cmd", "command", "op", "operation", "do", "method", "mode", "task", "func", "function", "event"}

// looseQuery는 query를 &와 ;를 모두 구분자로 보고 나눈다. Go의 url.ParseQuery는 ;가 든 조각을 버리지만 대상 서버
// (Rack 2.x, 옛 Python parse_qs)는 ;를 구분자로 써서 ?x=1;action=delete를 받는다(#487 리뷰 P2).
func looseQuery(raw string) url.Values {
	out := url.Values{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
		key, value, _ := strings.Cut(part, "=")
		if k, err := url.QueryUnescape(key); err == nil {
			key = k
		}
		if v, err := url.QueryUnescape(value); err == nil {
			value = v
		}
		out[key] = append(out[key], value)
	}
	return out
}

// autoSelectable은 자동 모드가 진단할 끝점이다. 수동 모드에서 사람이 고를 수 있는 끝점(controller의 선택 검사)과 같은
// 범위에서, 대상이 쓰기를 승인했으면 폼·쓰기·추정 끝점도 고른다(C21). 스크립트 파일과 관찰이 깨진 끝점은 고르지 않는다.
func autoSelectable(e *Endpoint, reversibleWrites bool) bool {
	if !e.InScope || e.Change == "blocked" || e.Kind == "script" || e.ObservationError != "" && e.ObservationError != "body_incomplete" {
		return false
	}
	return e.Change == "attested_read" || reversibleWrites
}

// autoSelectableLocked는 자동 모드가 진단 시작 때 고르는 끝점이다. autoSelectable에 더해, 추정 출처(API 정찰·번들
// 추측)라 possible인 끝점도 그 경로의 GET·HEAD가 운영자의 읽기 전용 승인으로 확인한 읽기이면 고른다. 추정 표시는
// 크롤이 자동 방문하지 않게 하는 표시일 뿐 부작용 근거가 아니다(#602). 자동 모드는 이미 승인 범위 안이면 보내므로
// 보낼 수 있는 요청은 늘지 않고, 모델이 받는 출발점 목록만 늘어난다(VAmPI 실제 회차 24b: 정찰 끝점 31개가 고르지
// 않아 모델 지시에 들어가지 않음). 폼·스크립트·쓰기 메서드·관찰 오류 끝점은 고르지 않는다.
func (c *Controller) autoSelectableLocked(e *Endpoint) bool {
	if autoSelectable(e, c.binding.ReversibleWrites) {
		return true
	}
	if !e.InScope || e.Change != "possible" || sideEffectEvidence(*e) || e.Kind == "script" || e.ObservationError != "" && e.ObservationError != "body_incomplete" {
		return false
	}
	in, change := c.endpointScope(e.Method, e.Path, "request", "diagnosis_request")
	return in && change == "attested_read"
}

// sideEffectPossible은 끝점이 상태를 바꿀 수 있는지다. 운영자가 경로 단위로 비변경을 확인한 읽기(attested_read)만 아니다.
func sideEffectPossible(e *Endpoint) bool { return e == nil || e.Change != "attested_read" }

func writeMethod(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}

// riskyRequest는 위험 동작 요청이다. 파괴 동작 단어는 경로·query 어디에 있든 모든 메서드에서, 거래 단어는
// 쓰기 메서드의 경로에서 막는다. query는 이름과 값 모두 본다(?action=delete).
func riskyRequest(method, path string, query url.Values) bool {
	for _, segment := range strings.Split(path, "/") {
		if anyToken(segment, destructiveWords) || writeMethod(method) && (anyToken(segment, transactionWords) || anyToken(segment, accountClosureWords)) {
			return true
		}
	}
	for key, values := range query {
		if anyToken(key, destructiveWords) {
			return true
		}
		if !anyToken(key, actionKeys) {
			continue
		}
		for _, v := range values {
			if anyToken(v, destructiveWords) {
				return true
			}
		}
	}
	return false
}

// RiskyRequest는 진단 쓰기 정책의 위험 동작 단어 검사를 질의 없는 요청 하나에 적용한다. 확정 단계 학습 로그인 재사용(모델
// 로그인 계약 §6-2)이 모델 단계와 같은 단어 목록으로 로그인·로그인 화면 요청을 거른다(탈퇴·해지 같은 쓰기를 로그인으로
// 다시 보내지 않게).
func RiskyRequest(method, path string) bool { return riskyRequest(method, path, nil) }

// safePath는 화면에 싣는 경로 · 요청 줄이다. 대상 경로와 응답 글은 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기: 제품 기록 ·
// 화면은 내부 담당자만 본다). 경로 틀 · 크기 상한은 공개 투영(endpoint 바이트 상한)이 따로 맡는다.
func safePath(path string) string { return path }

// 객체 ID로 볼 조각의 종류다. 허용 판정에 쓰므로 경로 가림 규칙(pathmask, 애매하면 값으로 봄)보다 좁게 잡는다. 긴 이름·
// 특수문자·대소문자와 숫자가 섞인 조각은 이름이다. 그런 조각까지 값으로 보면 크롤에서 보지 못한 형제 끝점
// (/orders/123:cancel, /orders/cancel-all-pending-orders-for-this-user)이 같은 끝점으로 열린다(#501 리뷰).
var (
	numberSegment = regexp.MustCompile(`^[0-9]{1,20}$`)
	uuidSegment   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexSegment    = regexp.MustCompile(`^[0-9a-fA-F]{16,64}$`)
	emailSegment  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}@[A-Za-z0-9-]{1,63}(\.[A-Za-z0-9-]{1,63})+$`)
)

// idSegmentKind는 조각이 객체 ID면 그 종류 자리 표시를, 아니면 빈 문자열을 돌려준다. 숫자는 16진 문자만 쓴 것이라
// 16자 이상이어도 숫자로 본다(가장 흔한 순번 ID).
func idSegmentKind(segment string) string {
	switch {
	case numberSegment.MatchString(segment):
		return "{number}"
	case uuidSegment.MatchString(segment):
		return "{uuid}"
	case hexSegment.MatchString(segment):
		return "{hex}"
	case emailSegment.MatchString(segment):
		return "{email}"
	}
	return ""
}

// pathTemplate은 객체 ID 조각을 종류 자리 표시로 바꾼 경로와 바꾼 조각 수다. 같은 자리의 조각이 같은 종류일 때만
// 템플릿이 같다(숫자 자리에 이름·UUID를 넣으면 다른 템플릿).
func pathTemplate(path string) (string, int) {
	segments := strings.Split(path, "/")
	values := 0
	for i, segment := range segments {
		if kind := idSegmentKind(segment); kind != "" {
			segments[i] = kind
			values++
		}
	}
	return strings.Join(segments, "/"), values
}

// requestEndpointLocked는 요청이 쓰는 끝점이다. 진단 단계에서는 선택된 끝점과 값 조각만 다른 요청(/x/1을 선택했으면
// /x/2)도 그 끝점으로 본다(C18). 크롤에서 본 객체 하나만 보낼 수 있으면 다른 계정·객체로 바꿔 보는 인가 진단이
// 불가능하기 때문이다. 요청 경로 자체도 그 끝점과 같은 범위·변경 분류여야 한다(승인 접두·제외 경로·위험 단어·읽기
// 전용 접두를 요청 경로로 다시 본다). 같은 경로가 따로 발견됐지만 선택되지 않았으면 선택한 끝점 쪽을 쓴다.
func (c *Controller) requestEndpointLocked(method, path string) *Endpoint {
	exact := c.endpointLocked(method, path)
	if exact != nil && exact.Selected || c.record.Snapshot.Stage != "diagnosing" {
		return exact
	}
	template, values := pathTemplate(path)
	if values == 0 {
		return exact
	}
	for i := range c.record.Snapshot.Endpoints {
		e := &c.record.Snapshot.Endpoints[i]
		if !e.Selected || e.Method != method || !e.InScope {
			continue
		}
		if other, _ := pathTemplate(e.Path); other != template {
			continue
		}
		in, change := c.endpointScope(method, path, e.Kind, e.Source)
		// 같은 경로를 따로 발견했으면 그 근거(폼·추정 경로라 possible, 막힘)가 상한이다. 선택한 형제의 근거로 더 넓게
		// (사전 승인 읽기로) 바꾸지 않는다(#501 리뷰 P1).
		if in && change == e.Change && (exact == nil || exact.InScope && exact.Change == e.Change) {
			return e
		}
	}
	return exact
}

func (c *Controller) endpointLocked(method, path string) *Endpoint {
	for i := range c.record.Snapshot.Endpoints {
		v := &c.record.Snapshot.Endpoints[i]
		if v.Method == method && v.Path == path {
			return v
		}
	}
	return nil
}
func (c *Controller) allowedPath(method, path string) bool {
	if riskyRequest(normalizedMethod(method), path, nil) {
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
	if !allowed {
		return false
	}
	// path는 정규 표기(%가 없는 경로를 다시 인코딩한 것)라 단어 경계가 달라지고(%3Cdelete는 "3"·"cdelete"로 나뉘어
	// delete를 놓침) 비ASCII 제외 경로와 표기가 다르다(%C3%BCber ↔ über). 크롤·발견 분류도 대상이 처리하는 풀린 경로로
	// 위험 동작·제외 경로·범위를 다시 본다(#626 진단 단계와 같은 함수). 크롤 요청은 E②를 거치지만 E②는 위험 단어·
	// 제외 경로를 보지 않아 여기가 유일한 방어선이다(#627, 재현: 크롤이 %3Cdelete·%C3%BCber를 방문했음).
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return false
	}
	return decoded == path || c.decodedPathAllowed(normalizedMethod(method), decoded, false)
}
func (c *Controller) allowedURLPath(path string, hasQuery bool) bool {
	for _, expression := range c.binding.PathPrefixes {
		if pathscope.Allows(path, hasQuery, expression) {
			return true
		}
	}
	return false
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
			old.Kind = kind
			if old.DiscoveryConfidence != "observed" {
				old.Source = source
				c.setFromLocked(old, from)
			}
			old.InScope, old.Change = c.endpointScope(method, path, kind, old.Source)
		} else if inferredSource(old.Source) && !inferredSource(source) {
			// 추정으로 먼저 봤어도 같은 호출을 문자 그대로 확인하면 후보를 올린다.
			old.Source = source
			c.setFromLocked(old, from)
			old.InScope, old.Change = c.endpointScope(method, path, kind, source)
		} else if old.Source == "bundle_string" && kind == "script" {
			// 글자로만 봤던 스크립트를 근거 있는 형태로 다시 보면 방문 순서를 올린다.
			old.Source = source
		}
		return old
	}
	in, change := c.endpointScope(method, path, kind, source)
	e := Endpoint{ID: endpointID(c.origin, method, path), Method: method, Path: path, Kind: kind, Source: source, From: from, InScope: in, Change: change}
	// 개수 상한(설치 max_endpoints ≤ 256)과 공개 투영 바이트 중 먼저 닿는 쪽에서 자른다. 범위 안 발견은 범위 밖·미방문·
	// 미선택 끝점 자리를 대신 쓸 수 있다(바꿔도 바이트 안에 들 때만).
	full := len(c.record.Snapshot.Endpoints) >= c.binding.Limits.MaxEndpoints
	floor, fits := 0, false
	if !full {
		floor, fits = c.endpointFitsLocked(-1, e)
	}
	if !fits {
		c.record.Snapshot.Counts.Omitted++
		if in {
			// 지금처럼 첫 자리 하나만 본다(받을 때마다 바이트를 재는 횟수를 일정하게).
			for i, old := range c.record.Snapshot.Endpoints {
				if !old.InScope && !old.Selected && !old.Visited {
					if floor, fits = c.endpointFitsLocked(i, e); !fits {
						return nil
					}
					delete(c.floorCache, old.ID)
					c.floorCache[e.ID] = floor
					c.record.Snapshot.Endpoints[i] = e
					return &c.record.Snapshot.Endpoints[i]
				}
			}
		}
		return nil
	}
	c.floorCache[e.ID] = floor
	c.record.Snapshot.Endpoints = append(c.record.Snapshot.Endpoints, e)
	return &c.record.Snapshot.Endpoints[len(c.record.Snapshot.Endpoints)-1]
}

// setFromLocked는 끝점의 발견 위치(From)를 바꾼다. From은 압축에서 지우지 않는 칸(크롤 문서 기준)이라 바꾼 끝점의
// 바닥 크기를 다시 잰다. 문자열 길이가 아니라 직렬화·경로 가림 기준이다(`&`는 JSON에서 `\u0026` 6바이트가 되고, 가림은
// 같은 길이 경로를 `[REDACTED]`로 늘릴 수 있음, #639 리뷰). 바닥 크기가 기억한 값보다 커지면 공개 투영 바이트 안일 때만
// 바꾸고, 넘으면 이전 값을 둔다. 바꾸면 기억한 바닥 크기도 새 값으로 둔다.
func (c *Controller) setFromLocked(e *Endpoint, from string) {
	if from == e.From {
		return
	}
	updated := *e
	updated.From = from
	floor := endpointFloorBytes(updated)
	for i := range c.record.Snapshot.Endpoints {
		if &c.record.Snapshot.Endpoints[i] != e {
			continue
		}
		if cached, ok := c.floorCache[e.ID]; !ok || floor > cached {
			if _, fits := c.endpointFitsLocked(i, updated); !fits {
				return
			}
		}
		c.floorCache[e.ID] = floor
		break
	}
	e.From = from
}

// endpointFitsLocked는 끝점 목록에 e를 더하거나(i < 0) i번째를 e로 바꿨을 때, 공개 투영을 끝까지 덜어 낸 바닥
// 크기에 끝점마다 늘 수 있는 몫과 목록 밖 칸이 늘 수 있는 몫을 더해도 MaxProjectionBytes 안인지와 e의 바닥 크기다.
// 이 안이면 뒤에 관찰이 붙어도 compactProjection이 투영을 상한 안으로 줄일 수 있다(목록이 화면·IPC·저장 기록에 늘
// 들어감). 다른 끝점은 받을 때 잰 바닥 크기(floorCache)를 더한다(JSON 배열은 원소 직렬화 + 쉼표).
func (c *Controller) endpointFitsLocked(i int, e Endpoint) (int, bool) {
	if c.floorCache == nil {
		c.floorCache = map[string]int{}
	}
	floor := endpointFloorBytes(e)
	eps := c.record.Snapshot.Endpoints
	n := len(eps)
	total := snapshotBaseFloorBytes(c.record.Snapshot) + floor
	for j := range eps {
		if j == i {
			continue
		}
		cached, ok := c.floorCache[eps[j].ID]
		if !ok {
			cached = endpointFloorBytes(eps[j])
			c.floorCache[eps[j].ID] = cached
		}
		total += cached
	}
	if i < 0 {
		n++
	}
	total += max(n-1, 0) + endpointGrowthReserve*n + snapshotGrowthReserve
	return floor, total <= MaxProjectionBytes
}

// 번들에서 경로를 문자 그대로 확인하지 못한 후보다. 탐색 에이전트가 번들·문서에서 찾아 보고한 것
// (api_recon_static)도 실제로 오간 요청이 아니므로 추정이다(탐색 실행기 결정안 §2-3).
func inferredSource(source string) bool {
	return source == "script_guess" || source == "script_prefix" || source == SourceAPIReconStatic || source == SourceAPIReconRuntime
}

func (c *Controller) endpointScope(method, path, kind, source string) (bool, string) {
	// 접두사는 앱이 실제로 보내는 주소가 아니므로 전송 후보로 두지 않는다.
	if !has(c.binding.Methods, method) || !c.allowedPath(method, path) || source == "script_prefix" {
		return false, "blocked"
	}
	// 폼과 추정 경로는 실제 요청이 다를 수 있어 자동 방문·선택하지 않는다.
	if (method == "GET" || method == "HEAD") && kind != "form" && !inferredSource(source) && c.readPath(path) {
		return true, "attested_read"
	}
	return true, "possible"
}

// maxObservationProjectionFailures는 요청 관찰 저장이 연달아 실패해도 진단을 이어 가는 횟수다.
// 관찰 투영은 화면·보고용이고 E② 감사 기록은 따로 남는다. 저장소 일시 오류 한 번으로 그 뒤 정상
// 요청을 모두 막지 않되, 이 횟수를 넘기면 기록 없이 계속 보내지 않도록 막는다(C13).
const maxObservationProjectionFailures = 3

// saveLocked는 선택·모드·단계처럼 허용 판단에 쓰는 상태를 저장한다. 실패하면 메모리 상태와 저장 상태가
// 어긋났을 수 있으므로 이후 허용을 모두 막는다.
func (c *Controller) saveLocked(ctx context.Context, create bool, kind, method, path string, status int) error {
	if c.poisoned {
		return ErrProjection
	}
	if _, err := c.writeLocked(ctx, create, kind, method, path, status, eventRef{}); err != nil {
		c.poisonLocked()
		return ErrProjection
	}
	c.observationFailures = 0
	c.notifyLocked()
	return nil
}

// saveObservationLocked는 이미 끝난 요청의 관찰(방문·상태·건수)만 저장한다. 허용 상태는 바뀌지 않으므로
// 일시 오류는 판(revision)을 되돌리고 다음 저장에서 같은 기록 전체를 다시 쓴다. 판 충돌은 다른 기록과
// 어긋났다는 뜻이고(커밋 결과를 모르는 실패 뒤에도 생긴다), 연달은 실패는 상한까지만 넘긴다. 둘 다 이후
// 허용을 막는다(C13).
func (c *Controller) saveObservationLocked(ctx context.Context, method, path string, status int, ref eventRef) error {
	if c.poisoned {
		return ErrProjection
	}
	old, err := c.writeLocked(ctx, false, "request", method, path, status, ref)
	if err == nil {
		c.observationFailures = 0
		c.notifyLocked()
		return nil
	}
	c.observationFailures++
	if errors.Is(err, ErrConflict) || c.observationFailures >= maxObservationProjectionFailures {
		c.poisonLocked()
		return ErrProjection
	}
	c.record.Snapshot.Revision = old
	// 고정 문구만 남긴다. 저장소 오류 원문에는 대상 경로·연결 정보가 들어 있을 수 있다.
	log.Print("[diagnosisflow] observation_projection_deferred")
	return ErrProjection
}

// writeLocked는 판을 올려 기록 전체를 저장하고 이전 판을 돌려준다. 실패 처리는 호출자가 정한다.
func (c *Controller) writeLocked(ctx context.Context, create bool, kind, method, path string, status int, ref eventRef) (uint64, error) {
	old := c.record.Snapshot.Revision
	c.record.Snapshot.Revision++
	c.record.Snapshot.UpdatedAt = time.Now().UTC()
	if kind != "" {
		c.record.Snapshot.Events = append(c.record.Snapshot.Events, Event{At: c.record.Snapshot.UpdatedAt, Kind: kind, Method: method, Path: safePath(path), Status: status, CallRef: ref.call, AttemptID: ref.attempt})
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
	if create {
		return old, c.store.Create(ctx, clone(c.record))
	}
	return old, c.store.Save(ctx, old, clone(c.record))
}

func (c *Controller) poisonLocked() {
	c.poisoned = true
	c.record.Snapshot.Stage = "failed"
	c.record.Snapshot.Failure = "workflow_projection_failed"
	c.record.Snapshot.Paused = false
	c.notifyLocked()
}

func (c *Controller) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *Controller) Snapshot() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return compactProjection(Project(clone(c.record).Snapshot)), nil
}
func (c *Controller) Wait(ctx context.Context) error {
	return c.wait(ctx, false)
}

func (c *Controller) wait(ctx context.Context, requestBoundary bool) error {
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
			if requestBoundary {
				return errors.Join(e2proxy.ErrWorkflowPaused, ctx.Err())
			}
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
	rows, omitted := c.modelEndpointsLocked(nil)
	return c.instructionsLocked(rows, omitted)
}

// ModelInput은 모델에 줄 지시 문장과 고른 끝점 데이터를 같은 행 목록으로 만든다. redact는 그 실행의 출력 가림이고, 데이터
// 상한(MaxSelectedDataBytes)은 가린 뒤 바이트로 잰다: 가림은 값을 [REDACTED]로 바꿔 길이를 늘릴 수 있다. 지시 문장의
// "빠진 수"도 이 행 목록에서 나온다. redact가 nil이면 가림 없이 대상 데이터를 주지 않도록 데이터를 비운다.
func (c *Controller) ModelInput(redact func([]byte) []byte) (instructions, data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if redact == nil {
		rows, omitted := c.modelEndpointsLocked(nil)
		return c.instructionsLocked(rows, omitted), ""
	}
	rows, omitted := c.modelEndpointsLocked(redact)
	raw, _ := json.Marshal(rows)
	// 상한은 실제로 돌려줄 바이트에 건다. 가림은 호출마다 다를 수 있어(원격 가림 실패 등) 행을 고를 때 잰 값이 반환값과 같다고
	// 보지 않는다. 가린 결과가 비었거나 상한을 넘으면 데이터를 주지 않는다(#685 이동찬 리뷰).
	out := redact(raw)
	if len(out) == 0 || len(out) > MaxSelectedDataBytes {
		return c.instructionsLocked(rows, omitted), ""
	}
	return c.instructionsLocked(rows, omitted), string(out)
}

func (c *Controller) instructionsLocked(rows []map[string]any, omitted int) string {
	var selected []string
	for _, row := range rows {
		selected = append(selected, row["endpoint_ref"].(string))
	}
	data, _ := json.Marshal(selected)
	scope := "Only selected endpoints may be tested after initial discovery. Endpoint references below are opaque program-owned IDs. Do not expand scope for newly found endpoints. A selected endpoint also covers the same method on a path that differs only in object ID segments of the same kind (number, UUID, long hex or email), so other object IDs may be requested there."
	if c.binding.Mode == Auto {
		scope = "In automatic mode you may test any path and method within the approved target scope, including endpoints that discovery did not find; the selected endpoints below are discovered starting points, not a limit. Requests outside the approved origin, paths or methods, excluded paths and destructive actions are blocked by the transfer control. Endpoint references below are opaque program-owned IDs."
	}
	more := ""
	if omitted > 0 {
		more = " " + strconv.Itoa(omitted) + " more selected endpoints were left out of this list for length; the same scope rules apply to them."
	}
	return "\nWorkbench workflow mode: " + c.record.Snapshot.Mode + ". Workbench workflow phase: " + c.record.Snapshot.Stage + ". " + scope + " Selected endpoint references (JSON): " + string(data) + more
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
		c.recordModelOmittedLocked()
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

// requestPathLocked는 허용 판단에 쓸 요청 경로다. path는 끝점 대조·결과 불명 기억 키에 쓰는 정규 표기, decoded는 한 번
// 푼 경로다. 진단 단계(Worker 진단 요청)만 인코딩한 경로 조각을 받는다(diagnosisPath). 그 밖의 단계(크롤)는 지금처럼
// requestEndpoint만 받고 두 값이 같다.
func (c *Controller) requestPathLocked(v e2proxy.RequestView) (path, decoded string, ok bool) {
	if c.record.Snapshot.Stage != "diagnosing" {
		path, ok = c.requestEndpoint(v)
		return path, path, ok
	}
	u, err := url.Parse(v.URL)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != c.origin {
		return "", "", false
	}
	return diagnosisPath(u)
}

// decodedPathAllowed는 풀린 경로가 승인 범위 안이고 제외 경로·위험 동작이 아닌지다. 진단 단계에서 정규 표기 검사에 더해
// 본다. 대상이 처리하는 것은 풀린 경로이고, 다시 인코딩한 표기는 단어 경계가 달라진다(%3Cdelete는 "3"·"cdelete"로
// 나뉘어 delete를 놓친다). 범위는 E②처럼 풀린 경로로 맞춘다(pathscope).
func (c *Controller) decodedPathAllowed(method, decoded string, hasQuery bool) bool {
	if riskyRequest(method, decoded, nil) {
		return false
	}
	for _, p := range c.binding.Excluded {
		if excludedWithin(decoded, p) {
			return false
		}
	}
	return c.allowedURLPath(decoded, hasQuery)
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
	} else if err := c.wait(ctx, true); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 단계에 따라 받는 경로 표기가 다르므로 단계를 읽는 잠금 안에서 경로를 정한다.
	path, decoded, ok := c.requestPathLocked(v)
	if !ok {
		return ErrDenied
	}
	// 결과 불명으로 끝난 부작용 가능 요청과 같은 메서드·경로다. 기록은 부작용 가능 요청만 남기므로 메서드로 거르지 않는다.
	if c.uncertainWrites[normalizedMethod(v.Method)+" "+path] {
		return ErrDenied
	}
	e := c.requestEndpointLocked(v.Method, path)
	if v.BodyBytes > 65536 || e != nil && (!e.InScope || e.Change == "blocked") || !c.allowedPath(v.Method, path) {
		return ErrDenied
	}
	// 경로가 범위 안이어도 query에 파괴 동작이 실리면 막는다(위험 query 검증 유지, C23).
	u, err := url.Parse(v.URL)
	if err != nil || !c.allowedURLPath(path, u.RawQuery != "" || u.ForceQuery) || riskyRequest(normalizedMethod(v.Method), path, looseQuery(u.RawQuery)) {
		return ErrDenied
	}
	// 인코딩한 경로 조각(진단 단계만)은 풀린 경로로도 범위·제외·위험 단어를 본다. 둘 중 하나라도 걸리면 막는다.
	if decoded != path && !c.decodedPathAllowed(normalizedMethod(v.Method), decoded, u.RawQuery != "" || u.ForceQuery) {
		return ErrDenied
	}
	s := c.record.Snapshot
	if s.Stage == "crawling" {
		if e == nil || v.Method != "GET" || v.BodyPresent || e.Change != "attested_read" {
			return ErrDenied
		}
		return nil
	}
	if s.Stage != "diagnosing" {
		return ErrDenied
	}
	change, ok := c.diagnosisChangeLocked(v.Method, path)
	if !ok {
		return ErrDenied
	}
	// 대상에 승인한 쓰기(되돌릴 수 있는 쓰기 승인)가 있으면 건별 확인 없이 보낸다(C21, 2026-11-13 결정: 자동 모드는
	// 승인 없이 돈다). 범위·메서드·위험 요청·예산 검사는 그대로다.
	if change != "attested_read" && !c.binding.ReversibleWrites {
		return ErrDenied
	}
	return nil
}

// uncertainSideEffectLocked는 결과 불명으로 끝난 요청이 상태를 바꿨을 수 있다고 볼지다.
func (c *Controller) uncertainSideEffectLocked(method, path string) bool {
	if c.record.Snapshot.Stage == "diagnosing" {
		if change, ok := c.diagnosisChangeLocked(method, path); ok {
			return change != "attested_read"
		}
	}
	return sideEffectPossible(c.requestEndpointLocked(method, path))
}

// diagnosisChangeLocked는 진단 단계에서 보낼 수 있는 요청의 변경 분류(attested_read·possible)다. ok=false면 보낼 수 없다.
// Before(허용)와 After(결과 불명 요청 기억)가 같은 분류를 쓴다(#557 이동찬 리뷰: 허용 때 확인한 읽기로 본 경로를 결과
// 불명 뒤 부작용 가능으로 기억해 회차 끝까지 막지 않게).
func (c *Controller) diagnosisChangeLocked(method, path string) (string, bool) {
	e := c.requestEndpointLocked(method, path)
	// 자동 모드에서 추정 출처라 고른 끝점(autoSelectableLocked)의 possible은 부작용 근거가 아니므로, 고르지 않은 경로와
	// 같은 아래 자동 모드 판정을 따른다(고른 끝점이라고 더 막히지 않게). 수동 모드는 고른 끝점의 분류 그대로다.
	if e != nil && e.Selected && !(c.binding.Mode == Auto && e.Change == "possible" && !sideEffectEvidence(*e)) {
		return e.Change, true
	}
	if c.binding.Mode != Auto {
		return "", false
	}
	// 자동 모드는 고르지 않은 끝점·크롤이 찾지 못한 경로도 대상 승인 범위(origin·경로 접두·메서드) 안이면 보낸다
	// (2026-11-13 사용자 결정: 원본처럼 승인 범위 안이면 진단한다. 선택 제한은 운영자가 고르는 수동 모드만). 제외 경로·
	// 위험 요청·결과 불명 재전송 금지·예산·감사는 그대로다. 읽기 확인 경로 밖이거나 쓰기 메서드면 부작용 가능으로 본다.
	in, change := c.endpointScope(normalizedMethod(method), path, "request", "diagnosis_request")
	if !in {
		return "", false
	}
	// 같은 경로를 따로 발견했으면 그 근거(폼 등 부작용 가능)가 상한이다. 더 넓게(확인한 읽기로) 바꾸지 않는다.
	// 다만 추정 출처(번들 추측·API 정찰)의 possible은 "자동 방문·선택하지 않는다"는 표시일 뿐 부작용 근거가 아니다.
	// 승인 접두 안 GET·HEAD의 비변경 근거는 운영자의 읽기 전용 승인이므로 그대로 둔다. 쓰기 메서드의 판정은 위
	// endpointScope가 메서드로 정하고 여기서 바꾸지 않는다(VAmPI 실제 회차 24b: 정찰로 찾은 GET 23건이 거부됨).
	if e != nil && e.Change != "attested_read" && sideEffectEvidence(*e) {
		change = e.Change
	}
	return change, true
}

// sideEffectEvidence는 발견한 끝점이 부작용 가능의 실제 근거인지다. 폼은 실제 요청이 다를 수 있어 근거이고, 추정
// 출처가 아닌 발견의 possible도 근거다. 추정 출처의 possible은 근거가 아니다.
func sideEffectEvidence(e Endpoint) bool {
	return e.Kind == "form" || !inferredSource(e.Source)
}

func (c *Controller) PreapprovedRead(v e2proxy.RequestView) bool {
	if v.BodyPresent || v.Method != "GET" && v.Method != "HEAD" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	path, _, ok := c.requestPathLocked(v)
	if !ok {
		return false
	}
	e := c.requestEndpointLocked(v.Method, path)
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
	// Before와 같은 정규 표기로 기억한다. 인코딩만 다른 같은 경로를 결과 불명 뒤 다시 보내지 못하게 한다.
	path, _, ok := c.requestPathLocked(v)
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
		// 쓰기 메서드만이 아니라 부작용 가능 끝점(GET 폼·추정 경로 등 attested_read가 아닌 것)도 기억한다. GET이라고 상태
		// 변경이 없다고 보지 않는다(CLAUDE.md 규칙 6, #525 이동찬 리뷰). 진단 단계는 허용 때와 같은 분류를 쓰고, 분류할 수
		// 없으면(그 밖의 단계·끝점 없음) 부작용 가능으로 본다.
		if method := normalizedMethod(v.Method); ok && (writeMethod(method) || c.uncertainSideEffectLocked(v.Method, path)) {
			c.uncertainWrites[method+" "+path] = true
		}
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
			if r.Reason == e2proxy.Withheld {
				e.Response += " · response withheld"
			} else if !r.ResponseComplete {
				e.Response += " · response incomplete"
			}
		}
	}
	// 요청을 보낸 줄에만 E② 호출·시도 식별자를 붙인다(보내지 않은 시도는 상세로 열 원문 기록이 없다).
	var ref eventRef
	if r.RequestWritten {
		ref = eventRef{call: r.CallRef, attempt: r.AttemptID}
	}
	return c.saveObservationLocked(ctx, normalizedMethod(v.Method), path, boundedStatus(r.StatusCode), ref)
}

// eventRef는 흐름 Event에 붙이는 E② 호출·시도 식별자다(요청을 보낸 관찰에만).
type eventRef struct{ call, attempt string }

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
	// 제외 판정은 E②와 같은 함수다(#627: E②도 제외 경로를 집행). 판정이 갈리면 차등 시험이 잡는다.
	return pathscope.Excluded(path, pattern)
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

// NoDeepRuns는 진단 Worker가 한 번도 시작하지 않았고 흐름이 깨지지 않았는지 본다. 계획이 정상으로 끝났는데 의도가
// 하나도 없던 실행은 DeepReady가 될 수 없으므로, 마감 판정이 그 경우를 실패와 구분하는 데 쓴다.
func (c *Controller) NoDeepRuns() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.poisoned && c.record.Snapshot.Failure == "" && c.record.Snapshot.Stage != "failed" && c.record.Snapshot.DeepRuns == 0
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

// SelectedData는 가림 없는 고른 끝점 데이터다(시험·투영 대조용). 모델에는 ModelInput으로 가린 데이터만 준다.
func (c *Controller) SelectedData() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, _ := c.modelEndpointsLocked(nil)
	raw, _ := json.Marshal(rows)
	return string(raw)
}

// MaxSelectedDataBytes는 모델 지시(Planner 목표·Worker 입력)에 넣는 고른 끝점 데이터의 바이트 상한이다. 공개 끝점 목록이
// 이미 공개 투영 바이트(MaxProjectionBytes)로 잘려 들어오고, 모델 행은 공개 행보다 칸이 적으므로 같은 값이면 고른 끝점이
// 전부 들어간다(공식 측정 프로토콜 고정 초안 §5, 2026-11-17 결정). 승인 범위 안 끝점을 모델이 모른 채 도는 일을 없애려는
// 것이다. 공개 상한을 거치지 않은 목록이 생기면 뒤쪽(추정) 행부터 덜고 빠진 수를 남기는 장치는 그대로 둔다.
const MaxSelectedDataBytes = MaxProjectionBytes

// recordModelOmittedLocked는 선택이 정해질 때 모델 목록에서 빠지는 고른 끝점 수를 스냅숏에 남긴다.
func (c *Controller) recordModelOmittedLocked() {
	_, omitted := c.modelEndpointsLocked(nil)
	c.record.Snapshot.Counts.ModelOmitted = uint64(omitted)
}

// modelEndpointsLocked는 모델에게 줄 고른 끝점 행과 길이 때문에 뺀 수다. 관찰한 끝점을 앞에, 추정 출처 끝점을 뒤에
// 둔다. 상한을 넘으면 뒤쪽 행의 이름 목록(보조 정보)부터 덜고, 그래도 넘으면 뒤쪽 행을 뺀다. 빠진 끝점도 허용 판정은
// 그대로다(모델이 모를 뿐이다).
// redact가 있으면 가린 뒤 바이트로 상한을 잰다(모델에 실제로 나가는 크기).
func (c *Controller) modelEndpointsLocked(redact func([]byte) []byte) ([]map[string]any, int) {
	var observed, inferred []map[string]any
	for _, e := range c.record.Snapshot.Endpoints {
		if !e.Selected {
			continue
		}
		row := map[string]any{"endpoint_ref": e.ID, "method": e.Method, "path": e.Path}
		// 보고된 요청 모양은 이름뿐이고 대상에서 온 데이터다(값 없음).
		if len(e.QueryKeys) > 0 {
			row["query_keys"] = e.QueryKeys
		}
		if len(e.BodyKeys) > 0 {
			row["body_keys"] = e.BodyKeys
		}
		if e.ContentType != "" {
			row["content_type"] = e.ContentType
		}
		if inferredSource(e.Source) {
			inferred = append(inferred, row)
		} else {
			observed = append(observed, row)
		}
	}
	rows := append(observed, inferred...)
	fits := func() bool {
		raw, _ := json.Marshal(rows)
		if redact != nil {
			raw = redact(raw)
		}
		return len(raw) <= MaxSelectedDataBytes
	}
	for i := len(rows) - 1; i >= 0 && !fits(); i-- {
		delete(rows[i], "query_keys")
		delete(rows[i], "body_keys")
		delete(rows[i], "content_type")
	}
	omitted := 0
	for len(rows) > 0 && !fits() {
		rows = rows[:len(rows)-1]
		omitted++
	}
	return rows, omitted
}

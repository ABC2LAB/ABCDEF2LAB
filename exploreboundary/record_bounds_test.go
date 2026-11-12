package exploreboundary

import (
	"encoding/json"
	"math"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

// 탐색 쪽이 고른 값으로 만든 가장 큰 행도 maxRecordBytes 안이다(#488 리뷰 P1). 상한을 바꾸면 이 시험이 다시 잰다.
func TestBoundRecordFitsRowLimit(t *testing.T) {
	huge := strings.Repeat("가", 30000)
	worst := boundRecord(Record{SessionRef: strings.Repeat("s", 128), Seq: math.MaxUint64, At: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		Decision: "forwarded", Reason: strings.Repeat("r", 32), Ingress: DirectIngress, Dest: DestExternal, Method: huge, Status: 999, Bytes: math.MaxInt64,
		PathPattern: huge, ExternalHost: huge, FetchDest: huge, FetchMode: huge, HasAuthHeader: true})
	raw, err := json.Marshal(worst)
	if err != nil || len(raw) > maxRecordBytes {
		t.Fatalf("worst bounded row is %d bytes (limit %d): %v", len(raw), maxRecordBytes, err)
	}
	if worst.Method != "INVALID" || worst.FetchDest != recordInvalid || worst.FetchMode != recordInvalid || worst.ExternalHost != recordInvalid ||
		len(worst.PathPattern) > maxRecordPatternBytes || !strings.HasSuffix(worst.PathPattern, "...truncated") {
		t.Fatalf("bounded row: method=%q dest=%q mode=%q host=%q pattern=%d", worst.Method, worst.FetchDest, worst.FetchMode, worst.ExternalHost, len(worst.PathPattern))
	}
	// 정상 값은 그대로다(대소문자만 Fetch 표준 소문자로).
	ok := boundRecord(Record{Method: "GET", FetchDest: "Script", FetchMode: "no-cors", ExternalHost: "cdn.example", PathPattern: "/app/{id}"})
	if ok.Method != "GET" || ok.FetchDest != "script" || ok.FetchMode != "no-cors" || ok.ExternalHost != "cdn.example" || ok.PathPattern != "/app/{id}" {
		t.Fatalf("ordinary row changed: %+v", ok)
	}
}

// 가장 큰 행으로 꽉 찬 한 쪽도 제어 소켓 한 줄 안이다. 행 수를 묶지 않고 쪽으로 나눠 읽는다.
func TestFullRecordsPageFitsControlLine(t *testing.T) {
	huge := strings.Repeat("가", 30000)
	row := boundRecord(Record{SessionRef: strings.Repeat("s", 128), Seq: math.MaxUint64, At: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		Decision: "forwarded", Reason: strings.Repeat("r", 32), Ingress: DirectIngress, Dest: DestExternal, Method: huge, Status: 999, Bytes: math.MaxInt64,
		PathPattern: huge, ExternalHost: huge, FetchDest: huge, FetchMode: huge, HasAuthHeader: true})
	page := make([]Record, recordsPerPage)
	for i := range page {
		page[i] = row
	}
	raw, err := json.Marshal(ControlResponse{OK: true, Records: page, Omitted: math.MaxUint64, Next: math.MaxUint64})
	if err != nil || len(raw)+1 > maxControlLine {
		t.Fatalf("full page is %d bytes (line limit %d): %v", len(raw), maxControlLine, err)
	}
}

// 쪽 나누기: offset부터 limit 행, 남으면 next, 끝이면 0. 세션 요청 수 상한은 기록 때문에 줄지 않는다.
func TestRecordsPage(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	s.mu.Lock()
	for i := 0; i < 7; i++ {
		s.records = append(s.records, Record{Seq: uint64(i + 1)})
	}
	s.omitted = 2
	s.mu.Unlock()
	var got []uint64
	offset, pages := uint64(0), 0
	for {
		rows, omitted, next := s.RecordsPage(offset, 3)
		pages++
		if omitted != 2 || len(rows) > 3 {
			t.Fatalf("page rows=%d omitted=%d", len(rows), omitted)
		}
		for _, r := range rows {
			got = append(got, r.Seq)
		}
		if next == 0 {
			break
		}
		if next != offset+uint64(len(rows)) || pages > 10 {
			t.Fatalf("next=%d after offset=%d rows=%d", next, offset, len(rows))
		}
		offset = next
	}
	if pages != 3 || len(got) != 7 || got[0] != 1 || got[6] != 7 {
		t.Fatalf("paged rows=%v pages=%d", got, pages)
	}
	if rows, _, next := s.RecordsPage(99, 3); len(rows) != 0 || next != 0 {
		t.Fatalf("offset past the end: rows=%d next=%d", len(rows), next)
	}
	if rows, _, next := s.RecordsPage(0, 0); len(rows) != 0 || next != 0 {
		t.Fatalf("zero limit: rows=%d next=%d", len(rows), next)
	}
	// 큰 요청 수 상한도 세션을 연다(기록 때문에 탐색량을 줄이지 않음).
	clock := &fakeClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	target := Target{Ref: "target-1", Revision: 2, Host: fixtureHost, Port: fixturePort, PathPrefixes: []string{"/app"}, Methods: []string{"GET"},
		ConnectIP: fixtureConnect, SPKISHA256: fixtureSPKI, ExpiresAt: clock.now.Add(time.Hour)}
	b := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	b.Limits.MaxRequests = uint64(maxControlLine)
	policy := Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: fixtureDenied, Now: clock.Now}
	if _, err := Open(t.Context(), b, fixtureDigest, &fakeSource{target: target}, policy); err != nil {
		t.Fatalf("large request budget refused: %v", err)
	}
}

// 판정도 정규화한 값을 본다. 대문자 NAVIGATE로 외부 이동 차단을 피하지 못한다.
func TestFetchModeNormalizedBeforeDecision(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, func(_ *Target, p *Policy) {
		p.Resolver = fakeResolver{"cdn.example": {netip.MustParseAddr("93.184.216.35")}}
	})
	d, ticket := s.Admit(t.Context(), Request{Ingress: BrowserIngress, Method: "GET", Scheme: "https", Host: "cdn.example", Port: fixturePort, EscapedPath: "/", FetchMode: "NAVIGATE"})
	if ticket != nil {
		ticket.Done(Result{Status: 200})
	}
	if d.Forward || d.Reason != ReasonExternalNavigation {
		t.Fatalf("upper-case navigate bypassed the external navigation rule: %+v", d)
	}
}

// 경로 표시 값(설치가 넣는 PathPattern)과 외부 호스트 이름도 기록 직전에 길이를 묶는다. 입구 정규화는 이 둘을 줄이지 않는다.
func TestRecordedPatternAndHostAreBounded(t *testing.T) {
	long := strings.Repeat("p", 10*maxRecordPatternBytes)
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(_ *Target, p *Policy) {
		p.PathPattern = func(string) string { return long }
	})
	s.Admit(t.Context(), approved("POST", "/app/a")) // 읽기만 승인이라 막히고 경로 패턴이 기록된다.
	s.Refuse(Request{Ingress: BrowserIngress, Method: "GET", Scheme: "https", Host: strings.Repeat("h", 4*maxRecordHostBytes), Port: fixturePort}, ReasonOutOfScopeOrigin)
	s.mu.Lock()
	s.recordLocked(Record{Decision: "denied", Dest: DestExternal, ExternalHost: strings.Repeat("h", 4*maxRecordHostBytes)})
	s.mu.Unlock()
	records, _ := s.Records()
	if len(records) != 3 {
		t.Fatalf("records=%d", len(records))
	}
	if len(records[0].PathPattern) > maxRecordPatternBytes || records[2].ExternalHost != recordInvalid {
		t.Fatalf("unbounded row: pattern=%d host=%q", len(records[0].PathPattern), records[2].ExternalHost)
	}
}

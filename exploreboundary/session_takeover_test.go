package exploreboundary

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

// 제외 경로의 조각 하나 전체 "*"는 제품 대상 등록·진단 흐름과 같이 아무 조각 하나와 맞고, 그 아래 경로까지 덮는다
// (#482 리뷰 R1). 등록 검사가 "*"를 받으므로 글자 그대로 비교하면 제외가 조용히 빠진다.
func TestExcludedPathWildcardSegment(t *testing.T) {
	for path, forward := range map[string]bool{
		"/app/v1/admin":       false,
		"/app/v2/admin/users": false,
		"/app/v1/public":      true,
		"/app/admin":          true, // "*"는 빈 조각과 맞지 않는다.
	} {
		t.Run(path, func(t *testing.T) {
			s, _, _ := fixtureSession(t, exploration.ApprovedWrites, exploration.FullLocal, func(tg *Target, _ *Policy) {
				tg.ExcludedPrefixes = []string{"/app/*/admin"}
			})
			d, ticket := s.Admit(t.Context(), approved("POST", path))
			if ticket != nil {
				ticket.Done(Result{Status: 200})
			}
			if d.Forward != forward || !forward && d.Reason != ReasonOutOfScopePath {
				t.Fatalf("forward=%v reason=%q, want forward=%v", d.Forward, d.Reason, forward)
			}
		})
	}
}

// 승인 대상 서버를 다른 이름·IP 리터럴로 부르면 외부 자원으로 보내지 않는다(#482 리뷰 R3). 외부로 보내면 승인 경로·
// 위험 경로·제외 경로·SPKI 고정과 대상 요청 수를 모두 건너뛴다. 여러 주소 중 하나만 승인 IP여도 막는다.
func TestApprovedServerNotReachableAsExternal(t *testing.T) {
	public := netip.MustParseAddr("93.184.216.34")
	other := netip.MustParseAddr("93.184.216.35")
	for name, host := range map[string]string{"alias": "alias.example", "ip-literal": "93.184.216.34", "mapped-literal": "[::ffff:93.184.216.34]", "mixed": "mixed.example"} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, func(tg *Target, p *Policy) {
				tg.ConnectIP = public
				p.Resolver = fakeResolver{"alias.example": {public}, "mixed.example": {other, public}}
			})
			req := Request{Ingress: BrowserIngress, Method: "GET", Scheme: "https", Host: host, Port: fixturePort, EscapedPath: "/app/admin/reset", FetchMode: "cors"}
			d, ticket := s.Admit(t.Context(), req)
			if ticket != nil {
				ticket.Done(Result{Status: 200})
			}
			if d.Forward || d.Reason != ReasonOutOfScopeOrigin || s.Status().ExternalRequests != 0 {
				t.Fatalf("approved server reached as external: %+v status=%+v", d, s.Status())
			}
		})
	}
	// 대조: 승인 IP가 아닌 외부 주소는 지금처럼 외부 자원으로 보낸다.
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, func(tg *Target, p *Policy) {
		tg.ConnectIP = public
		p.Resolver = fakeResolver{"cdn.example": {other}}
	})
	d, ticket := s.Admit(t.Context(), Request{Ingress: BrowserIngress, Method: "GET", Scheme: "https", Host: "cdn.example", Port: fixturePort, EscapedPath: "/lib.js", FetchMode: "no-cors"})
	if !d.Forward || ticket == nil || d.Dest != DestExternal {
		t.Fatalf("ordinary external resource was refused: %+v", d)
	}
	ticket.Done(Result{Status: 200})
}

type ctxSource struct{ fakeSource }

func (c *ctxSource) Current(ctx context.Context, ref string, rev uint64) (Target, error) {
	if err := ctx.Err(); err != nil {
		return Target{}, err
	}
	return c.fakeSource.Current(ctx, ref, rev)
}

// 요청 하나의 문맥이 판정 중에 끝나면(브라우저의 XHR 중단 등) 그 요청만 request_cancelled로 막고 세션은 닫지 않는다
// (#482 리뷰 R4). 저장소 장애로 보고 세션 전체를 boundary_unavailable로 닫으면 안 된다.
func TestCancelledRequestKeepsSession(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	target := Target{Ref: "target-1", Revision: 2, Host: fixtureHost, Port: fixturePort, PathPrefixes: []string{"/app"}, Methods: []string{"GET"},
		ConnectIP: fixtureConnect, SPKISHA256: fixtureSPKI, ExpiresAt: clock.now.Add(time.Hour)}
	policy := Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: fixtureDenied, Now: clock.Now}
	src := &ctxSource{fakeSource{target: target}}
	s, err := Open(t.Context(), fixtureBinding(exploration.ReadOnly, exploration.FullLocal), fixtureDigest, src, policy)
	if err != nil {
		t.Fatal(err)
	}
	aborted, cancel := context.WithCancel(t.Context())
	cancel()
	d, ticket := s.Admit(aborted, approved("GET", "/app/a"))
	if d.Forward || ticket != nil || d.Reason != ReasonRequestCancelled {
		t.Fatalf("cancelled request: %+v", d)
	}
	if st := s.Status(); !st.Active {
		t.Fatalf("one cancelled request closed the session: %+v", st)
	}
	records, _ := s.Records()
	if len(records) != 1 || records[0].Reason != ReasonRequestCancelled {
		t.Fatalf("cancelled request record: %+v", records)
	}
	// 같은 세션의 다음 요청은 그대로 나간다.
	d, ticket = s.Admit(t.Context(), approved("GET", "/app/a"))
	if !d.Forward || ticket == nil {
		t.Fatalf("session refused the next request: %+v", d)
	}
	ticket.Done(Result{Status: 200})
}

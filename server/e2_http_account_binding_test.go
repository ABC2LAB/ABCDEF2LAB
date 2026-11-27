package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

// 계정 결속 참조(E③↔E② v3 계약 §2.2·§4). 시험 값은 모두 합성이다.
var accountBindingPattern = regexp.MustCompile(`^acct-[0-9a-f]{32}$`)

func bindingStore(t *testing.T, sessions E2HTTPSessions) *e2HTTPSessionStore {
	t.Helper()
	return newE2HTTPSessionStore(sessions, "run-ab", nil)
}

func TestActualSentSessionMatchesOnlyOneProtectedAccount(t *testing.T) {
	store := bindingStore(t, E2HTTPSessions{
		"tester-a": {"Authorization": {"Bearer synthetic-a"}},
		"tester-b": {"Authorization": {"Bearer synthetic-b"}},
	})
	if label, ok := store.matchSentAccount(http.Header{"Authorization": {"Bearer synthetic-a"}}, ""); !ok || label != "tester-a" {
		t.Fatal("sent credential did not bind the actual account", label, ok)
	}
	for _, sent := range []http.Header{
		{"Authorization": {"Bearer synthetic-other"}},
		{"Authorization": {"Bearer synthetic-a"}, "Cookie": {"extra=synthetic"}},
		{"Cookie": {"sid=synthetic-a"}},
	} {
		if label, ok := store.matchSentAccount(sent, ""); ok || label != "" {
			t.Fatal("unmatched or extra credential acquired account identity", label)
		}
	}
	store = bindingStore(t, E2HTTPSessions{
		"tester-a": {"X-Auth-Token": {"synthetic-a"}},
		"tester-b": {"X-Auth-Token": {"synthetic-b"}},
	})
	if label, ok := store.matchSentAccount(http.Header{"X-Auth-Token": {"synthetic-b"}}, "X-Auth-Token"); !ok || label != "tester-b" {
		t.Fatal("custom credential header did not bind", label, ok)
	}
}

// C14 인증에 성공해 세션이 있는 계정만 참조를 받는다. 실행이 다르거나 세션이 없는 라벨은 참조가 없다.
func TestAccountBindingIssuedOnlyForAuthenticatedSessions(t *testing.T) {
	s := bindingStore(t, E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=synthetic-a"}}, "tester-empty": http.Header{}})
	a, ok := s.AccountBindingRef("run-ab", "tester-a")
	if !ok || !accountBindingPattern.MatchString(a) {
		t.Fatalf("인증된 계정 참조: %q %v", a, ok)
	}
	for _, c := range []struct{ run, label string }{{"run-ab", "tester-b"}, {"run-ab", "tester-empty"}, {"run-other", "tester-a"}, {"", "tester-a"}} {
		if ref, ok := s.AccountBindingRef(c.run, c.label); ok || ref != "" {
			t.Fatalf("%+v: 참조가 있음 %q", c, ref)
		}
	}
	header, err := s.BoundSession("run-ab", "tester-a", a)
	if err != nil || header.Get("Cookie") != "sid=synthetic-a" {
		t.Fatalf("지금 결속으로 세션: %v", err)
	}
	// 돌려준 사본을 고쳐도 저장소 세션은 그대로다.
	header.Set("Cookie", "changed")
	if again, _ := s.BoundSession("run-ab", "tester-a", a); again.Get("Cookie") != "sid=synthetic-a" {
		t.Fatal("세션 사본이 저장소를 바꿈")
	}
	// 실행을 모르는 저장소는 참조를 발급하지 않는다(세션은 Worker용으로 그대로 있음).
	unbound := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=x"}}}, "", nil)
	if _, ok := unbound.AccountBindingRef("", "tester-a"); ok || unbound.snapshot()["tester-a"].Get("Cookie") != "sid=x" {
		t.Fatal("실행 없는 저장소")
	}
}

// 다시 로그인으로 세션이 바뀌면 참조도 바뀌고, 옛 참조로는 새 세션도 옛 세션도 얻을 수 없다. 다시 로그인이 실패하면
// 세션과 참조가 그대로다. 다른 라벨·다른 실행의 참조로도 세션을 얻을 수 없다.
func TestAccountBindingOldRefCannotReachAnotherSession(t *testing.T) {
	s := bindingStore(t, E2HTTPSessions{
		"tester-a": http.Header{"Cookie": {"sid=synthetic-a1"}},
		"tester-b": http.Header{"Cookie": {"sid=synthetic-b1"}},
	})
	oldA, _ := s.AccountBindingRef("run-ab", "tester-a")
	refB, _ := s.AccountBindingRef("run-ab", "tester-b")
	if oldA == refB {
		t.Fatal("라벨마다 다른 참조여야 함")
	}
	if _, err := s.BoundSession("run-ab", "tester-b", oldA); !errors.Is(err, ErrE2HTTPAccountBinding) {
		t.Fatal("다른 라벨의 참조로 세션을 얻음")
	}
	if _, err := s.BoundSession("run-other", "tester-a", oldA); !errors.Is(err, ErrE2HTTPAccountBinding) {
		t.Fatal("다른 실행으로 세션을 얻음")
	}
	if _, err := s.BoundSession("run-ab", "tester-a", ""); !errors.Is(err, ErrE2HTTPAccountBinding) {
		t.Fatal("빈 참조로 세션을 얻음")
	}

	// 다시 로그인 실패: 세션·참조 그대로
	if _, result := s.refresh(t.Context(), "tester-a", func() (http.Header, string) { return nil, E2HTTPAuthLoginFailed }); result != E2HTTPAuthLoginFailed {
		t.Fatal(result)
	}
	if ref, _ := s.AccountBindingRef("run-ab", "tester-a"); ref != oldA {
		t.Fatal("실패한 다시 로그인이 참조를 바꿈")
	}

	// 다시 로그인 성공: 새 세션·새 참조, 옛 참조는 거부
	if _, result := s.refresh(t.Context(), "tester-a", func() (http.Header, string) {
		return http.Header{"Cookie": {"sid=synthetic-a2"}}, E2HTTPAuthAuthenticated
	}); result != E2HTTPAuthAuthenticated {
		t.Fatal(result)
	}
	newA, ok := s.AccountBindingRef("run-ab", "tester-a")
	if !ok || newA == oldA || !accountBindingPattern.MatchString(newA) {
		t.Fatalf("다시 로그인 뒤 참조: %q (옛 %q)", newA, oldA)
	}
	if header, err := s.BoundSession("run-ab", "tester-a", oldA); !errors.Is(err, ErrE2HTTPAccountBinding) || header != nil {
		t.Fatal("옛 참조로 새 세션을 얻음")
	}
	if header, err := s.BoundSession("run-ab", "tester-a", newA); err != nil || header.Get("Cookie") != "sid=synthetic-a2" {
		t.Fatal("새 참조로 새 세션", err)
	}
	// 다른 라벨의 결속은 그대로다.
	if ref, _ := s.AccountBindingRef("run-ab", "tester-b"); ref != refB {
		t.Fatal("다른 라벨 참조가 바뀜")
	}
	// 처음 인증 준비에 없던 라벨도 다시 로그인에 성공하면 그때 발급한다.
	if _, ok := s.AccountBindingRef("run-ab", "tester-c"); ok {
		t.Fatal("인증 전 참조")
	}
	s.refresh(t.Context(), "tester-c", func() (http.Header, string) {
		return http.Header{"Cookie": {"sid=synthetic-c1"}}, E2HTTPAuthAuthenticated
	})
	if ref, ok := s.AccountBindingRef("run-ab", "tester-c"); !ok || ref == oldA || ref == newA || ref == refB {
		t.Fatal("새로 인증한 라벨의 참조", ref)
	}
}

// 참조는 난수에서만 나온다: 같은 난수면 세션·자격 값이 달라도 같은 참조이고, 참조에 실행·라벨·세션 값의 어떤 조각도 없다.
// 그래서 자격·토큰이 참조에 섞이지 않고 참조에서 추정되지도 않는다. 난수를 읽지 못하면 발급하지 않고, 다시 로그인 때
// 읽지 못하면 옛 참조도 지운다(옛 참조가 새 세션을 가리키지 않게).
func TestAccountBindingCarriesNoCredential(t *testing.T) {
	seed := bytes.Repeat([]byte{0x5a}, 64)
	secret := "Bearer synthetic-token-0123456789"
	a := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Authorization": {secret}}}, "run-ab", bytes.NewReader(seed))
	b := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=other-session-value"}}}, "run-other", bytes.NewReader(seed))
	refA, _ := a.AccountBindingRef("run-ab", "tester-a")
	refB, _ := b.AccountBindingRef("run-other", "tester-a")
	if refA == "" || refA != refB || refA != "acct-"+strings.Repeat("5a", 16) {
		t.Fatalf("참조가 난수에서만 나와야 함: %q %q", refA, refB)
	}
	for _, part := range []string{"synthetic", "token", "Bearer", "sid", "tester", "run-ab", "0123456789"} {
		if strings.Contains(refA, part) {
			t.Fatalf("참조에 %q", part)
		}
	}
	// 난수 실패: 발급 안 함
	failing := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=x"}}}, "run-ab", iotest.ErrReader(errors.New("no entropy")))
	if _, ok := failing.AccountBindingRef("run-ab", "tester-a"); ok {
		t.Fatal("난수 없이 발급")
	}
	// 다시 로그인 때 난수 실패: 옛 참조를 지우고 새 세션에는 참조가 없다
	short := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=one"}}}, "run-ab", bytes.NewReader(seed[:16]))
	old, ok := short.AccountBindingRef("run-ab", "tester-a")
	if !ok {
		t.Fatal("처음 발급")
	}
	short.refresh(t.Context(), "tester-a", func() (http.Header, string) {
		return http.Header{"Cookie": {"sid=two"}}, E2HTTPAuthAuthenticated
	})
	if _, ok := short.AccountBindingRef("run-ab", "tester-a"); ok {
		t.Fatal("난수 없이 다시 발급")
	}
	if _, err := short.BoundSession("run-ab", "tester-a", old); !errors.Is(err, ErrE2HTTPAccountBinding) {
		t.Fatal("옛 참조가 새 세션을 가리킴")
	}
}

// 다시 로그인과 조회가 동시에 일어나도, 세션을 받은 참조는 그 순간의 지금 결속이다(옛 참조 + 새 세션 조합이 없다).
func TestAccountBindingConcurrentRefresh(t *testing.T) {
	s := bindingStore(t, E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=0"}}})
	seen := sync.Map{} // 참조 → 그 참조로 받은 세션 값
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				value := "sid=" + string(rune('a'+i)) + string(rune('a'+j%26))
				s.refresh(t.Context(), "tester-a", func() (http.Header, string) {
					return http.Header{"Cookie": {value}}, E2HTTPAuthAuthenticated
				})
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				ref, ok := s.AccountBindingRef("run-ab", "tester-a")
				if !ok {
					continue
				}
				header, err := s.BoundSession("run-ab", "tester-a", ref)
				if err != nil {
					continue // 그 사이 다시 로그인으로 바뀜 — 옛 참조는 거부된다
				}
				if prev, loaded := seen.LoadOrStore(ref, header.Get("Cookie")); loaded && prev != header.Get("Cookie") {
					t.Errorf("한 참조가 두 세션을 가리킴: %v / %v", prev, header.Get("Cookie"))
				}
			}
		}()
	}
	wg.Wait()
}

func TestLearnedReloginBindsOnlyInstalledSession(t *testing.T) {
	const label = "tester-a"
	s := bindingStore(t, E2HTTPSessions{label: {"Cookie": {"sid=old"}}})
	oldRef, _ := s.AccountBindingRef(s.runRef, label)
	var session http.Header
	s.learned = func(context.Context, E2HTTPLearnedLoginRequest) (E2HTTPLearnedLoginResult, http.Header) {
		return E2HTTPLearnedLoginResult{Result: E2HTTPAuthAuthenticated}, session
	}
	req := E2HTTPLearnedLoginRequest{Label: label}
	if got := s.LearnedRelogin(t.Context(), req); got.BindingRef != "" {
		t.Fatal("새 세션 없이 기존 세션을 학습 재로그인 결과에 결속함")
	}
	session = http.Header{"Cookie": {"sid=learned"}}
	got := s.LearnedRelogin(t.Context(), req)
	bound, err := s.BoundSession(s.runRef, label, got.BindingRef)
	if err != nil || got.BindingRef == oldRef || bound.Get("Cookie") != session.Get("Cookie") {
		t.Fatal("설치한 학습 재로그인 세션의 참조를 받지 못함")
	}
	s.replace(label, http.Header{"Cookie": {"sid=replaced"}})
	if _, err := s.BoundSession(s.runRef, label, got.BindingRef); !errors.Is(err, ErrE2HTTPAccountBinding) {
		t.Fatal("학습 재로그인 참조가 후속 세션을 가리킴")
	}
}

// 다시 로그인 경로가 없는 저장소(시험 계정 실행이 아님)는 확정 단계 다시 로그인을 보내지 않는다.
func TestAccountReloginWithoutPathIsNotSent(t *testing.T) {
	s := bindingStore(t, E2HTTPSessions{"tester-a": http.Header{"Cookie": {"sid=synthetic-a"}}})
	if got := s.Relogin(t.Context(), "tester-a"); got != E2HTTPAuthNotSent {
		t.Fatalf("다시 로그인 경로 없이 결과 %q", got)
	}
	var nilStore *e2HTTPSessionStore
	if got := nilStore.Relogin(t.Context(), "tester-a"); got != E2HTTPAuthNotSent {
		t.Fatalf("nil 저장소 결과 %q", got)
	}
	called := ""
	s.relogin = func(_ context.Context, label string) (http.Header, string) {
		called = label
		return http.Header{"Cookie": {"sid=synthetic-new"}}, E2HTTPAuthLoginFailed
	}
	if got := s.Relogin(t.Context(), "tester-a"); got != E2HTTPAuthLoginFailed || called != "tester-a" {
		t.Fatalf("다시 로그인 결과 %q label %q", got, called)
	}
}

// 보낸 다시 로그인만 확정 몫으로 센다.
func TestE2HTTPAuthSent(t *testing.T) {
	for result, want := range map[string]bool{
		E2HTTPAuthAuthenticated: true, E2HTTPAuthLoginFailed: true, E2HTTPAuthSessionNotFound: true, E2HTTPAuthResultUnknown: true,
		E2HTTPAuthNotSent: false, E2HTTPRefreshLimit: false, E2HTTPRefreshCooldown: false, "": false, "other": false,
	} {
		if E2HTTPAuthSent(result) != want {
			t.Fatalf("%q: %v", result, !want)
		}
	}
}

//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/server"
)

// e3HookFixture는 선언(소유 전용 note·공유 post)을 둔 대상과, 그 선언을 동결하고 확정 몫(종류 1 = 요청 5·50초)을 적은
// 회차 디렉터리(runs/<liveJob>)다.
type e3HookFixture struct {
	runs   string
	r      *productTargetRegistry
	rec    productTargetRecord
	target diagnosisprep.ProductTarget
	frozen productFrozenPolicy
	refs   productTargetRefs
}

func newE3HookFixture(t *testing.T, declare bool, policy func(*server.E2HTTPFilePolicy)) e3HookFixture {
	t.Helper()
	return newE3HookFixtureWith(t, declare, policy, nil)
}

// newE3HookFixtureWith는 선언 입력을 바꿀 수 있는 newE3HookFixture다.
func newE3HookFixtureWith(t *testing.T, declare bool, policy func(*server.E2HTTPFilePolicy), input func(*diagnosisprep.DeclarationInput)) e3HookFixture {
	t.Helper()
	r, _, target, _ := declarationTestTarget(t)
	var decl *e3contract.Declaration
	if declare {
		in := declarationTestInput()
		if input != nil {
			input(&in)
		}
		v, err := r.putDeclaration("ARTEX", target.ID, target.Revision, in)
		if err != nil {
			t.Fatal(err)
		}
		decl = v.Declaration
	}
	rec, err := r.lookup("ARTEX", target.Origin+"/app", "")
	if err != nil {
		t.Fatal(err)
	}
	runs, _ := liveRunFixtureWith(t, nil, func(p *server.E2HTTPFilePolicy) {
		share := server.ComputeE3ConfirmationShare(1, p.MaxRequests, p.ValidUntil.Sub(p.ValidFrom), server.E3ConfirmationRequestTimeoutDefault)
		requests, timeoutMS := share.Requests, share.Timeout.Milliseconds()
		p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = &requests, &timeoutMS
		if policy != nil {
			policy(p)
		}
	})
	f := e3HookFixture{runs: runs, r: r, rec: rec, target: target}
	// 실행 시작처럼 세대·인스턴스 참조를 발급해 동결한다(url_preparation_linux.go).
	if f.refs, err = r.issueTargetRefs(target.ID, liveJob, rand.Reader); err != nil {
		t.Fatal(err)
	}
	if raw, err := f.refs.marshal(); err != nil || os.WriteFile(filepath.Join(runs, liveJob, productTargetRefsFile), raw, 0600) != nil {
		t.Fatal("세대·인스턴스 참조 쓰기", err)
	}
	if decl != nil {
		now := time.Now().UTC()
		frozen, frozenPolicy, err := freezeDeclaration(*decl, liveJob, now.Add(-time.Minute), now.Add(time.Hour), target.ApprovedAt, target.ExpiresAt)
		if err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string][]byte{productFrozenDeclarationFile: frozen, productFrozenPolicyFile: frozenPolicy} {
			if os.WriteFile(filepath.Join(runs, liveJob, name), raw, 0600) != nil {
				t.Fatal("동결 사본 쓰기")
			}
		}
		var ok bool
		if f.frozen, ok, err = readFrozenPolicy(runs, liveJob); err != nil || !ok {
			t.Fatal("동결 정책 출처", err)
		}
	}
	return f
}

func e3HookAccounts() map[string]string {
	return map[string]string{"tester-b": server.E2HTTPAuthAuthenticated, "tester-a": server.E2HTTPAuthAuthenticated, "tester-c": "login_failed"}
}

// confirmation은 가짜 E²(e3fake)를 넣은 확정 단계다. 표식 난수는 seed로 고정해 시험이 같은 표식을 다시 만들 수 있게 한다.
func (f e3HookFixture) confirmation(fake *e3fake.Transport, accounts map[string]string, seed []byte) *productE3Confirmation {
	c := newProductE3Confirmation(f.runs, liveJob, f.rec, f.r, func() map[string]string { return accounts })
	if fake != nil {
		c.deps.Transport, c.deps.Journal = fake, fake
	}
	if seed != nil {
		c.deps.Random = bytes.NewReader(seed)
	}
	return c
}

func e3HookFake() *e3fake.Transport {
	fake := e3fake.New()
	// 유효창(대상 승인·실행 기간 겹침)이 지금 기준이라 관찰 시각도 지금으로 둔다.
	fake.Clock = func() time.Time { return time.Now().UTC() }
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleOwner}] = e3fake.Behavior{SubjectID: "user-a"}
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleCross}] = e3fake.Behavior{SubjectID: "user-b"}
	return fake
}

func readE3Result(t *testing.T, runs string) (e3contract.RunResult, []byte) {
	t.Helper()
	path := filepath.Join(runs, liveJob, productE3ConfirmationFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("확정 결과 파일·권한: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var res e3contract.RunResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal("확정 결과 JSON", err)
	}
	return res, raw
}

func e3Seed() []byte {
	seed := make([]byte, e3contract.MarkerLength)
	for i := range seed {
		seed[i] = byte(i * 7)
	}
	return seed
}

// deadlineTransport는 확정 요청이 받은 문맥의 시한을 남긴다(확정 단계 시간 몫 = Run 문맥 시한, 계약 §12).
type deadlineTransport struct {
	*e3fake.Transport
	deadline time.Time
	set      bool
}

func (d *deadlineTransport) Identity(ctx context.Context, req e3contract.IdentityRequest) (e3contract.Observation, error) {
	if !d.set {
		d.deadline, d.set = ctx.Deadline()
	}
	return d.Transport.Identity(ctx, req)
}

// 선언을 동결하고 몫이 든 회차: 확정 단계 결과가 회차 디렉터리에 0600으로 남고, 생성 쓰기가 대상 쓰기 기록에 남아 다음
// 회차의 리셋 게이트가 막는다. 결과 파일에는 표식이 어느 모양으로도 없다.
func TestProductE3ConfirmationCompletedRecordsResultAndWrites(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	fake := e3HookFake()
	timed := &deadlineTransport{Transport: fake}
	c := f.confirmation(fake, e3HookAccounts(), e3Seed())
	c.deps.Transport = timed
	started, begins := time.Now(), 0
	if err := c.run(t.Context(), "9", func() { begins++ }); err != nil || begins != 1 {
		t.Fatal("확정 단계 훅", err, begins)
	}
	res, raw := readE3Result(t, f.runs)
	if res.Version != e3contract.RunResultVersion || res.Status != e3contract.StatusCompleted || res.Reason != "" || len(res.Verdicts) != 1 ||
		res.Verdicts[0].ResourceType != "note" || res.Verdicts[0].Value != "confirmed" || res.Requests != 5 || len(res.Skipped) != 1 {
		t.Fatalf("확정 단계 결과: %s", raw)
	}
	want := e3contract.RunBinding{JobRef: liveJob, RunRef: "run-live", TaskRef: "9", E2PolicyRef: "policy-live",
		GenerationRef: f.refs.GenerationRef, InstanceRef: f.refs.InstanceRef, Policy: f.frozen.Policy}
	if !f.refs.GenerationKnown || !f.refs.InstanceKnown {
		t.Fatalf("승인된 시험 대상의 참조를 모름: %+v", f.refs)
	}
	if res.Binding == nil || *res.Binding != want {
		t.Fatalf("실행 결속: %+v", res.Binding)
	}
	if len(res.Writes) != 1 || res.Writes[0].Outcome != e3contract.WriteCreated || !urlDigestValid(res.Writes[0].ObjectIDDigest) {
		t.Fatalf("확정 쓰기: %+v", res.Writes)
	}
	// 인증된 시험 계정 둘을 이름순으로 A·B에 둔다(로그인 실패 계정은 빠진다). 요청마다 확정 요청 시간 상한을 넘긴다.
	owner, cross := "", ""
	for _, call := range fake.Calls() {
		if call.Timeout != server.E3ConfirmationRequestTimeoutDefault {
			t.Fatalf("확정 요청 시간 상한: %v", call.Timeout)
		}
		if call.Key.Purpose == e3contract.PurposeIdentity && call.Key.Role == e3contract.RoleOwner {
			owner = call.Account
		}
		if call.Key.Purpose == e3contract.PurposeIdentity && call.Key.Role == e3contract.RoleCross {
			cross = call.Account
		}
	}
	if owner != "tester-a" || cross != "tester-b" {
		t.Fatalf("계정 배정: A=%s B=%s", owner, cross)
	}
	// 시간 몫(요청 고정 7 + 종류 1×4 = 11 × 10초)이 확정 단계 문맥의 시한이다.
	if share := time.Duration(server.E3ConfirmationFixedRequests+server.E3ConfirmationRequestsPerType) * server.E3ConfirmationRequestTimeoutDefault; !timed.set || timed.deadline.Before(started.Add(share)) || timed.deadline.After(time.Now().Add(share)) {
		t.Fatalf("확정 단계 시한: set=%v deadline=%v", timed.set, timed.deadline)
	}
	marker, err := e3contract.NewMarker(bytes.NewReader(e3Seed()))
	if err != nil {
		t.Fatal(err)
	}
	sinks := fake.Sinks()
	sinks["result"] = raw
	if leaks := e3contract.LeakingSinks(marker, sinks); len(leaks) != 0 {
		t.Fatalf("표식이 남음: %v", leaks)
	}
	// 양성 대조: 같은 찾기 함수가 결과에 표식이 들어가면 찾는다.
	if leaks := e3contract.LeakingSinks(marker, map[string][]byte{"result": append(bytes.Clone(raw), marker.Reveal()...)}); len(leaks) != 1 {
		t.Fatal("표식 찾기 대조 실패")
	}
	state, err := f.r.resetConfirmation("ARTEX", f.target.ID)
	if err != nil || !state.ResetRequired || state.LastWritesJob != liveJob || state.LastWritesObjects != 1 {
		t.Fatalf("대상 쓰기 기록: %+v %v", state, err)
	}
	if obj := f.r.resets[f.target.ID].writesObjects[0]; obj.Type != "note" || obj.IDUnknown || obj.IDDigest != res.Writes[0].ObjectIDDigest {
		t.Fatalf("쓰기 객체: %+v", obj)
	}
	if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("리셋 확인 전 다음 회차: %q", refusal)
	}
	// ID를 아는 객체도 잔존 점검(E² 쪽)이 없으면 리셋 확인만으로 다음 회차를 시작한다(사용자 결정 2026-11-20, 계약 §12).
	if _, err := f.r.confirmReset("ARTEX", f.target.ID, true, state.LastWritesRef); err != nil {
		t.Fatal(err)
	}
	if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != "" {
		t.Fatalf("ID 아는 객체·리셋 확인함: %q", refusal)
	}
	// 같은 회차의 결과 파일은 한 번만 쓴다(덮어쓰지 않음).
	if err := f.confirmation(e3HookFake(), e3HookAccounts(), nil).run(t.Context(), "9", nil); err == nil {
		t.Fatal("같은 회차 결과를 다시 씀")
	}
	if again, _ := readE3Result(t, f.runs); again.Status != e3contract.StatusCompleted {
		t.Fatal("첫 결과가 바뀜")
	}
}

// 생성 요청이 시간 상한으로 끊기면 객체가 생겼을 수 있다: ID 모름 쓰기로 남고, 리셋 확인 전까지 다음 회차를 막는다. ID
// 모름 객체만 있으면 리셋 확인만으로 다음 회차가 열린다(계약 §12).
func TestProductE3ConfirmationCreateTimeoutRecordsUnknownWrite(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	fake := e3HookFake()
	fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeCreate, Role: e3contract.RoleOwner, Type: "note"}] = e3fake.Behavior{TimedOut: true}
	if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
		t.Fatal(err)
	}
	res, raw := readE3Result(t, f.runs)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 1 || len(res.Verdicts[0].ProductReasons) != 1 ||
		res.Verdicts[0].ProductReasons[0] != e3contract.ReasonRequestTimeout || len(res.Writes) != 1 || res.Writes[0].Outcome != e3contract.WriteUnknown {
		t.Fatalf("생성 시간 초과 결과: %s", raw)
	}
	st := f.r.resets[f.target.ID]
	if st == nil || st.writesJob != liveJob || len(st.writesObjects) != 1 || !st.writesObjects[0].IDUnknown || st.writesObjects[0].IDDigest != "" {
		t.Fatalf("ID 모름 쓰기 기록: %+v", st)
	}
	if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != e3contract.RefuseResetConfirmationMissing {
		t.Fatalf("리셋 확인 전: %q", refusal)
	}
	if _, err := f.r.confirmReset("ARTEX", f.target.ID, true, st.writesRef); err != nil {
		t.Fatal(err)
	}
	if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != "" {
		t.Fatalf("ID 모름 객체만 있고 리셋 확인함: %q", refusal)
	}
}

// 진위 실패로 판정을 버려도 대상에는 이미 쓰기가 있었다. 쓰기 기록은 상태와 관계없이 남는다(계약 §7.2).
func TestProductE3ConfirmationIntegrityFailureStillRecordsWrites(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	fake := e3HookFake()
	fake.Tamper = func(c e3fake.Call, o *e3contract.Observation) {
		if c.Key.Purpose == e3contract.PurposeRead && c.Key.Role == e3contract.RoleOwner {
			o.StatusCode = 201
		}
	}
	if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
		t.Fatal(err)
	}
	res, raw := readE3Result(t, f.runs)
	if res.Status != e3contract.StatusIntegrityFailed || res.Reason != string(e3contract.IntegrityJournalEventMismatch) || len(res.Verdicts) != 0 ||
		len(res.Writes) != 1 || res.Writes[0].Outcome != e3contract.WriteCreated {
		t.Fatalf("진위 실패 결과: %s", raw)
	}
	st := f.r.resets[f.target.ID]
	if st == nil || st.writesJob != liveJob || len(st.writesObjects) != 1 || st.writesObjects[0].IDDigest != res.Writes[0].ObjectIDDigest {
		t.Fatalf("진위 실패 뒤 쓰기 기록: %+v", st)
	}
}

// 중지로 끊긴 확정 단계도 그때까지의 쓰기를 남긴다.
func TestProductE3ConfirmationInterruptedStillRecordsWrites(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	fake := e3HookFake()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// 생성 관찰을 돌려준 뒤 회차를 멈춘다. 다음 요청(A 조회) 전에 끊긴다.
	fake.Tamper = func(c e3fake.Call, _ *e3contract.Observation) {
		if c.Key.Purpose == e3contract.PurposeCreate {
			cancel()
		}
	}
	if err := f.confirmation(fake, e3HookAccounts(), nil).run(ctx, "9", nil); err != nil {
		t.Fatal(err)
	}
	res, raw := readE3Result(t, f.runs)
	if res.Status != e3contract.StatusInterrupted || len(res.Writes) != 1 || res.Writes[0].Outcome != e3contract.WriteCreated {
		t.Fatalf("중지 결과: %s", raw)
	}
	if st := f.r.resets[f.target.ID]; st == nil || st.writesJob != liveJob || len(st.writesObjects) != 1 {
		t.Fatalf("중지 뒤 쓰기 기록: %+v", st)
	}
}

// 제품 의존(E② Transport·Journal 없음)은 선언이 있든 없든 not_wired로 끝나고 요청·쓰기가 없다. 가짜 E²를 넣은 연결에서
// 선언이 없으면 declaration_missing이다(계약 §7: not_wired를 먼저 판정).
func TestProductE3ConfirmationDisabledResults(t *testing.T) {
	for name, tc := range map[string]struct {
		declare, fake bool
		reason        e3contract.DisabledReason
	}{
		"product-deps-with-declaration":    {true, false, e3contract.DisabledNotWired},
		"product-deps-without-declaration": {false, false, e3contract.DisabledNotWired},
		"fake-deps-without-declaration":    {false, true, e3contract.DisabledNoDeclaration},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, tc.declare, nil)
			var fake *e3fake.Transport
			if tc.fake {
				fake = e3HookFake()
			}
			begins := 0
			if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", func() { begins++ }); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			// 꺼진 확정 단계는 감독자에 시작 신호를 보내지 않는다(엔진 정리 상태·단계 기록 없음, Go 조건 2).
			if begins != 0 || res.Status != e3contract.StatusDisabled || res.Reason != string(tc.reason) || res.Requests != 0 || len(res.Writes) != 0 || len(res.Verdicts) != 0 {
				t.Fatalf("꺼진 확정 단계: %s", raw)
			}
			if fake != nil && len(fake.Calls()) != 0 {
				t.Fatal("꺼진 확정 단계가 요청을 보냄")
			}
			if st, err := f.r.resetConfirmation("ARTEX", f.target.ID); err != nil || st.ResetRequired || st.LastWritesJob != "" {
				t.Fatalf("꺼진 확정 단계가 쓰기를 남김: %+v %v", st, err)
			}
		})
	}
}

// 회차 기록에서 읽지 못한 칸은 지어내지 않는다. 확정 단계가 그 이유로 꺼지고 요청은 0건이다.
func TestProductE3ConfirmationInputFailsClosed(t *testing.T) {
	share := func(requests uint64, timeoutMS int64) func(*server.E2HTTPFilePolicy) {
		return func(p *server.E2HTTPFilePolicy) {
			p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = &requests, &timeoutMS
		}
	}
	for name, tc := range map[string]struct {
		policy   func(*server.E2HTTPFilePolicy)
		change   func(t *testing.T, f *e3HookFixture, c *productE3Confirmation)
		task     string
		accounts map[string]string
		status   e3contract.ConfirmationStatus
		reason   string
		requests int
	}{
		"frozen-policy-broken": {change: func(t *testing.T, f *e3HookFixture, _ *productE3Confirmation) {
			if os.WriteFile(filepath.Join(f.runs, liveJob, productFrozenPolicyFile), []byte(`{"format":"x"}`), 0600) != nil {
				t.Fatal("write")
			}
		}, reason: string(e3contract.DisabledBindingInvalid)},
		"run-policy-missing": {change: func(t *testing.T, f *e3HookFixture, _ *productE3Confirmation) {
			if os.Remove(filepath.Join(f.runs, liveJob, "policy.json")) != nil {
				t.Fatal("remove")
			}
		}, reason: string(e3contract.DisabledBindingInvalid)},
		"task-ref-missing": {task: "-", reason: string(e3contract.DisabledBindingInvalid)},
		"share-fields-absent": {policy: func(p *server.E2HTTPFilePolicy) {
			p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = nil, nil
		}, reason: string(e3contract.DisabledShareUnavailable)},
		"share-not-type-shaped":  {policy: share(6, 60000), reason: string(e3contract.DisabledShareUnavailable)},
		"share-identity-only":    {policy: share(2, 20000), reason: string(e3contract.DisabledShareUnavailable)},
		"share-time-not-matched": {policy: share(5, 40000), reason: string(e3contract.DisabledShareUnavailable)},
		"write-not-reversible": {change: func(_ *testing.T, _ *e3HookFixture, c *productE3Confirmation) { c.target.ReversibleWrites = false },
			reason: string(e3contract.DisabledWriteNotConsented)},
		"write-consent-missing": {change: func(_ *testing.T, _ *e3HookFixture, c *productE3Confirmation) { c.target.WriteConsent = "" },
			reason: string(e3contract.DisabledWriteNotConsented)},
		"one-account": {accounts: map[string]string{"tester-a": server.E2HTTPAuthAuthenticated, "tester-b": "result_unknown"},
			reason: string(e3contract.DisabledAccountsUnverified)},
		"no-account-reader": {change: func(_ *testing.T, _ *e3HookFixture, c *productE3Confirmation) { c.accounts = nil },
			reason: string(e3contract.DisabledAccountsUnverified)},
		// 몫이 0이면(종류가 몫에 들지 않음) 요청 없이 종류마다 UNDECIDABLE(예산)이다.
		"share-zero": {policy: share(0, 0), status: e3contract.StatusCompleted},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, true, tc.policy)
			accounts := tc.accounts
			if accounts == nil {
				accounts = e3HookAccounts()
			}
			fake := e3HookFake()
			c := f.confirmation(fake, accounts, nil)
			if tc.change != nil {
				tc.change(t, &f, c)
			}
			task := "9"
			if tc.task != "" {
				task = tc.task
			}
			begins := 0
			if err := c.run(t.Context(), task, func() { begins++ }); err != nil || begins != 0 {
				t.Fatal(err, begins)
			}
			res, raw := readE3Result(t, f.runs)
			status := tc.status
			if status == "" {
				status = e3contract.StatusDisabled
			}
			if res.Status != status || res.Reason != tc.reason || res.Requests != tc.requests || len(fake.Calls()) != 0 || len(res.Writes) != 0 {
				t.Fatalf("결과: %s", raw)
			}
			if status == e3contract.StatusCompleted && (len(res.Verdicts) != 1 || res.Verdicts[0].Value != "undecidable" ||
				len(res.Verdicts[0].ProductReasons) != 1 || res.Verdicts[0].ProductReasons[0] != e3contract.ReasonConfirmationBudgetExhaust) {
				t.Fatalf("몫 0 결과: %s", raw)
			}
		})
	}
}

// 쓰기 기록 갱신: created(모양이 맞는 digest)만 ID를 채우고, not_sent는 거두고, 결과 불명·모양이 틀린 digest는 미리 남긴
// ID 모름 그대로 둔다. 다른 회차의 결속으로는 남기지 않는다.
func TestProductWriteRecorderSettle(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for name, c := range map[string]struct {
		write e3contract.ConfirmationWrite
		want  productConfirmationObject
	}{
		"created":    {e3contract.ConfirmationWrite{ResourceType: "note", Outcome: e3contract.WriteCreated, ObjectIDDigest: digest}, productConfirmationObject{Type: "note", IDDigest: digest}},
		"not_sent":   {e3contract.ConfirmationWrite{ResourceType: "note", Outcome: e3contract.WriteNotSent}, productConfirmationObject{Type: "note", IDUnknown: true, NotSent: true}},
		"unknown":    {e3contract.ConfirmationWrite{ResourceType: "note", Outcome: e3contract.WriteUnknown}, productConfirmationObject{Type: "note", IDUnknown: true}},
		"bad_digest": {e3contract.ConfirmationWrite{ResourceType: "note", Outcome: e3contract.WriteCreated, ObjectIDDigest: "not-a-digest"}, productConfirmationObject{Type: "note", IDUnknown: true}},
		"other":      {e3contract.ConfirmationWrite{ResourceType: "note", Outcome: "other", ObjectIDDigest: digest}, productConfirmationObject{Type: "note", IDUnknown: true}},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, target, _ := declarationTestTarget(t)
			w := productWriteRecorder{registry: r, targetID: target.ID, jobID: liveJob}
			binding := e3contract.RunBinding{JobRef: liveJob}
			if err := w.Reserve(t.Context(), binding, "note", ""); err != nil {
				t.Fatal(err)
			}
			if err := w.Settle(t.Context(), binding, c.write); err != nil {
				t.Fatal(err)
			}
			if got := r.resets[target.ID].writesObjects; len(got) != 1 || got[0] != c.want {
				t.Fatalf("쓰기 기록: %+v", got)
			}
		})
	}
	r, _, target, _ := declarationTestTarget(t)
	w := productWriteRecorder{registry: r, targetID: target.ID, jobID: liveJob}
	other := e3contract.RunBinding{JobRef: "job-other"}
	if w.Reserve(t.Context(), other, "note", "") == nil || w.Settle(t.Context(), other, e3contract.ConfirmationWrite{ResourceType: "note", Outcome: e3contract.WriteNotSent}) == nil {
		t.Fatal("다른 회차 결속으로 쓰기 기록을 남김")
	}
	if r.resets[target.ID] != nil {
		t.Fatalf("다른 회차 결속이 상태를 바꿈: %+v", r.resets[target.ID])
	}
}

// 회차 시작 리셋 게이트(결정 §6.1): 선언이 있는 대상만, 지난 쓰기 뒤 리셋 확인이 없으면 회차를 거절한다. 거절이면 대상
// 묶음을 남기지 않는다.
func TestProductRunStartResetGate(t *testing.T) {
	r, _, target, _ := declarationTestTarget(t)
	rec, err := r.lookup("ARTEX", target.Origin+"/app", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &productRuntime{targets: r}
	runs := t.TempDir()
	attach := func(job string) string {
		t.Helper()
		release, reason := p.attachRun(t.Context(), rec, job, runs, func() {})
		p.activeMu.Lock()
		_, attached := p.active[rec.Target.ID][job]
		p.activeMu.Unlock()
		raw, readErr := os.ReadFile(filepath.Join(runs, job, productE3ConfirmationFile))
		if reason != "" {
			if release != nil || attached {
				t.Fatal("거절한 회차가 대상에 묶임")
			}
			// E③ 시작 거부는 확정 단계 결과 파일에 start_refused와 그 사유를 남긴다(대상 변경 거절은 E③ 사유가 아니라 남기지 않음).
			var res e3contract.RunResult
			if e3contract.StartRefusal(reason).Valid() {
				if readErr != nil || json.Unmarshal(raw, &res) != nil || res.Status != e3contract.StatusStartRefused || res.Reason != reason || res.Requests != 0 {
					t.Fatalf("거절 결과 파일(%s): %s %v", reason, raw, readErr)
				}
			} else if readErr == nil {
				t.Fatalf("E③ 사유가 아닌 거절에 결과 파일: %s", reason)
			}
			return reason
		}
		if readErr == nil {
			t.Fatal("받은 회차에 거절 결과 파일")
		}
		if release == nil || !attached {
			t.Fatal("받은 회차가 묶이지 않음")
		}
		release()
		return ""
	}
	// 선언이 없으면 확정 단계가 꺼진 회차라 지난 쓰기가 있어도 게이트가 없다.
	if err := r.recordConfirmationWrites(target.ID, "job-old", []productConfirmationObject{{Type: "note", IDUnknown: true}}); err != nil {
		t.Fatal(err)
	}
	if reason := attach("job-1"); reason != "" {
		t.Fatalf("선언 없는 대상에 게이트: %q", reason)
	}
	if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err != nil {
		t.Fatal(err)
	}
	if reason := attach("job-2"); reason != string(e3contract.RefuseResetConfirmationMissing) {
		t.Fatalf("리셋 확인 전: %q", reason)
	}
	state, err := r.resetConfirmation("ARTEX", target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.confirmReset("ARTEX", target.ID, true, state.LastWritesRef); err != nil {
		t.Fatal(err)
	}
	if reason := attach("job-3"); reason != "" {
		t.Fatalf("ID 모름 객체만 있고 리셋 확인함: %q", reason)
	}
	// ID를 아는 객체가 남아도 리셋 확인 전에는 시작하지 않고, 확인 뒤에는 잔존 점검 없이 시작한다(사용자 결정 2026-11-20).
	if err := r.recordConfirmationWrites(target.ID, "job-3", []productConfirmationObject{{Type: "note", IDDigest: strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	if reason := attach("job-4"); reason != string(e3contract.RefuseResetConfirmationMissing) {
		t.Fatalf("ID 아는 객체·리셋 확인 전: %q", reason)
	}
	state, _ = r.resetConfirmation("ARTEX", target.ID)
	if _, err := r.confirmReset("ARTEX", target.ID, true, state.LastWritesRef); err != nil {
		t.Fatal(err)
	}
	if reason := attach("job-4b"); reason != "" {
		t.Fatalf("ID 아는 객체·리셋 확인함: %q", reason)
	}
	// 대상이 바뀌었으면(철회) 게이트 전에 거절한다.
	if _, err := r.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if reason := attach("job-5"); reason != "target_approval_changed" {
		t.Fatalf("철회된 대상: %q", reason)
	}
}

// 쓰기 기록이 없는 대상(지금 모든 대상)은 선언이 있든 없든 지금처럼 시작한다. 게이트는 지난 확정 쓰기가 있을 때만 막는다.
func TestProductRunStartGateOpenWithoutWrites(t *testing.T) {
	for _, declare := range []bool{false, true} {
		r, _, target, _ := declarationTestTarget(t)
		if declare {
			if _, err := r.putDeclaration("ARTEX", target.ID, target.Revision, declarationTestInput()); err != nil {
				t.Fatal(err)
			}
		}
		rec, err := r.lookup("ARTEX", target.Origin+"/app", "")
		if err != nil {
			t.Fatal(err)
		}
		if refusal := r.e3RunStartRefusal(t.Context(), rec, "job-1"); refusal != "" {
			t.Fatalf("declare=%v 쓰기 없는 대상 게이트: %q", declare, refusal)
		}
		p := &productRuntime{targets: r}
		release, reason := p.attachRun(t.Context(), rec, "job-1", t.TempDir(), func() {})
		if reason != "" || release == nil {
			t.Fatalf("declare=%v 쓰기 없는 대상 시작 거절: %q", declare, reason)
		}
		release()
	}
}

func TestWriteRunPrivateFileStaysInRun(t *testing.T) {
	runs, _ := liveRunFixture(t, nil)
	// 회차 디렉터리 밖(상위·다른 회차)과 회차 안 하위 디렉터리(state/) 모두 받지 않는다.
	for _, bad := range [][2]string{{"..", "x.json"}, {liveJob, "../x.json"}, {"missing-job", "x.json"}, {liveJob, "state/x.json"}, {liveJob + "/state", "x.json"}} {
		if writeRunPrivateFile(runs, bad[0], bad[1], []byte("{}")) == nil {
			t.Fatalf("회차 밖 쓰기 허용: %v", bad)
		}
	}
	if err := writeRunPrivateFile(runs, liveJob, "x.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if writeRunPrivateFile(runs, liveJob, "x.json", []byte("{}")) == nil {
		t.Fatal("있는 파일을 덮어씀")
	}
}

// 발급 정책의 확정 몫에서 몫에 든 종류 수를 되읽는다(withE3ConfirmationShare의 역). 모양이 틀리면 몫을 받지 못한 것이다.
func TestProductE3AdmittedReadsShare(t *testing.T) {
	for k := 1; k <= 4; k++ {
		share := server.ComputeE3ConfirmationShare(k, 400, time.Hour, server.E3ConfirmationRequestTimeoutDefault)
		requests, timeoutMS := share.Requests, share.Timeout.Milliseconds()
		admitted, timeout := productE3Admitted(server.E2HTTPFileLimits{E3ConfirmationRequests: &requests, E3ConfirmationTimeoutMS: &timeoutMS})
		if share.Admitted != k || admitted != k || timeout != share.Timeout {
			t.Fatalf("종류 %d: 몫 %+v → %d %v", k, share, admitted, timeout)
		}
	}
	limits := func(requests uint64, timeoutMS int64) server.E2HTTPFileLimits {
		return server.E2HTTPFileLimits{E3ConfirmationRequests: &requests, E3ConfirmationTimeoutMS: &timeoutMS}
	}
	zero := uint64(0)
	for name, tc := range map[string]struct {
		limits server.E2HTTPFileLimits
		want   int
	}{
		"absent":           {server.E2HTTPFileLimits{}, -1},
		"requests-only":    {server.E2HTTPFileLimits{E3ConfirmationRequests: &zero}, -1},
		"zero":             {limits(0, 0), 0},
		"time-without-req": {limits(0, 1000), -1},
		"identity-only":    {limits(2, 20000), -1},
		"not-type-shaped":  {limits(6, 60000), -1},
		"time-not-matched": {limits(5, 40000), -1},
	} {
		if got, timeout := productE3Admitted(tc.limits); got != tc.want || timeout != 0 {
			t.Fatalf("%s: %d %v", name, got, timeout)
		}
	}
}

// 감독자가 모델 단계 시간 상한에서 빼는 확정 몫(timeShare)은 확정 단계가 실제로 받는 몫(input)과 같은 발급 정책 값이다.
// 몫을 받지 못한 정책은 0, 정책을 읽지 못하면 오류(실행을 시작하지 않음)다.
func TestProductE3ConfirmationTimeShareMatchesInput(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	c := f.confirmation(e3HookFake(), e3HookAccounts(), e3Seed())
	got, err := c.timeShare()
	_, given := c.input("41")
	if err != nil || got <= 0 || got != given {
		t.Fatalf("time share %v (err %v), confirmation gets %v", got, err, given)
	}
	// 실제 발급(productE3PolicyRules)은 확정 단계를 켤 때 쓰기 동의·리셋 가능 칸을 싣는다. 그 정책도 읽는다(이 칸을 정책
	// 문서 검증이 거부해 선언 있는 제품 회차가 시작부터 gateway_unavailable이었다, 합성 A/B 회차 11/20).
	consented := newE3HookFixture(t, true, func(p *server.E2HTTPFilePolicy) { p.E3WriteConsent, p.E3Resettable = true, true })
	if got, err := consented.confirmation(e3HookFake(), e3HookAccounts(), e3Seed()).timeShare(); err != nil || got <= 0 {
		t.Fatalf("쓰기 동의 칸이 있는 발급 정책: %v %v", got, err)
	}
	unshared := newE3HookFixture(t, true, func(p *server.E2HTTPFilePolicy) {
		p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = nil, nil
	})
	if got, err := unshared.confirmation(e3HookFake(), e3HookAccounts(), e3Seed()).timeShare(); err != nil || got != 0 {
		t.Fatalf("policy without share: %v %v", got, err)
	}
	if os.Remove(filepath.Join(f.runs, liveJob, "policy.json")) != nil {
		t.Fatal("remove")
	}
	if _, err := c.timeShare(); err == nil {
		t.Fatal("unreadable run policy gave a time share")
	}
}

// 인증 준비가 성공한 시험 계정만, 이름순 앞 둘을 A·B로 둔다(지도 순서와 관계없이 같다).
// 소유(A)·교차(B)는 선언한 라벨 그대로이고 인증 결과만 붙인다. 이름순으로 고르지 않는다(사용자 결정 2026-11-19).
func TestProductE3RolesFromDeclaration(t *testing.T) {
	read := func() map[string]string {
		return map[string]string{"admin": server.E2HTTPAuthAuthenticated, "zeta": server.E2HTTPAuthAuthenticated, "mu": "login_failed"}
	}
	decl := e3contract.Declaration{OwnerAccountLabel: "zeta", CrossAccountLabel: "mu"}
	owner, cross := productE3Roles(decl, read)
	// 이름순이면 admin이 A가 됐을 것이다. 선언한 zeta가 A이고, 인증에 실패한 mu는 B지만 인증되지 않았다.
	if owner != (e3contract.Account{Label: "zeta", Authenticated: true}) || cross != (e3contract.Account{Label: "mu"}) {
		t.Fatalf("A=%+v B=%+v", owner, cross)
	}
	if owner, cross := productE3Roles(e3contract.Declaration{OwnerAccountLabel: "zeta", CrossAccountLabel: "absent"}, read); !owner.Authenticated || cross.Authenticated {
		t.Fatalf("인증 결과 없는 계정: %+v %+v", owner, cross)
	}
	if owner, cross := productE3Roles(e3contract.Declaration{}, read); owner != (e3contract.Account{}) || cross != (e3contract.Account{}) {
		t.Fatal("역할 없는 선언에서 계정을 지어냄")
	}
	if owner, _ := productE3Roles(decl, nil); owner != (e3contract.Account{Label: "zeta"}) {
		t.Fatalf("인증 결과를 읽을 수 없으면 인증되지 않은 것: %+v", owner)
	}
}

type e3AuthRuntimeFixture struct{ initial, current map[string]string }

func (f e3AuthRuntimeFixture) AccountAuth() map[string]string      { return f.initial }
func (f e3AuthRuntimeFixture) AccountAuthForE3() map[string]string { return f.current }

func TestProductE3RolesUseCurrentSessionBinding(t *testing.T) {
	runtime := e3AuthRuntimeFixture{
		initial: map[string]string{"tester-a": server.E2HTTPAuthAuthenticated, "tester-b": server.E2HTTPAuthSessionNotFound},
		current: map[string]string{"tester-a": server.E2HTTPAuthAuthenticated, "tester-b": server.E2HTTPAuthAuthenticated},
	}
	decl := e3contract.Declaration{OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b"}
	owner, cross := productE3Roles(decl, func() map[string]string { return launchAccountAuthForE3(runtime) })
	if !owner.Authenticated || !cross.Authenticated || launchAccountAuth(runtime)["tester-b"] != server.E2HTTPAuthSessionNotFound {
		t.Fatal("E3 gate ignored current binding or changed historical preparation result")
	}
}

// 쓰기 기록을 남기지 못하면 훅이 오류를 돌려준다(감독자가 단계·상태를 남긴다). 결과 파일은 그래도 쓴다.
// failingWrites는 쓰기 기록의 Reserve·Settle 중 하나를 실패시키고 나머지는 실제 기록에 넘긴다.
type failingWrites struct {
	e3contract.WriteRecorder
	reserve, settle bool
}

func (f failingWrites) Reserve(ctx context.Context, b e3contract.RunBinding, typ, objectID string) error {
	if f.reserve {
		return errors.New("synthetic reserve failure")
	}
	return f.WriteRecorder.Reserve(ctx, b, typ, objectID)
}

func (f failingWrites) Settle(ctx context.Context, b e3contract.RunBinding, w e3contract.ConfirmationWrite) error {
	if f.settle {
		return errors.New("synthetic settle failure")
	}
	return f.WriteRecorder.Settle(ctx, b, w)
}

// 기록하고 나서 보내기(사용자 결정 2026-11-19): 생성 요청이 나갈 때 대상 선언 저널에는 이미 그 종류의 ID 모름 쓰기가 있다.
func TestProductE3ConfirmationRecordsBeforeCreate(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	fake := e3HookFake()
	var atCreate []productConfirmationObject
	fake.Tamper = func(c e3fake.Call, _ *e3contract.Observation) {
		if c.Key.Purpose == e3contract.PurposeCreate {
			if st := f.r.resets[f.target.ID]; st != nil {
				atCreate = append([]productConfirmationObject(nil), st.writesObjects...)
			}
		}
	}
	if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
		t.Fatal(err)
	}
	if len(atCreate) != 1 || atCreate[0] != (productConfirmationObject{Type: "note", IDUnknown: true}) {
		t.Fatalf("생성 요청 때의 쓰기 기록: %+v", atCreate)
	}
	if obj := f.r.resets[f.target.ID].writesObjects; len(obj) != 1 || obj[0].IDUnknown || !urlDigestValid(obj[0].IDDigest) {
		t.Fatalf("생성 뒤 갱신: %+v", obj)
	}
}

// 쓰기 기록을 남기지 못하면 생성 요청을 보내지 않고 UNDECIDABLE이다. 보낸 뒤 갱신을 못 하면 먼저 남긴 기록(ID 모름)이 다음
// 회차를 리셋 확인 전까지 막는다.
func TestProductE3ConfirmationWriteRecordFailures(t *testing.T) {
	t.Run("reserve", func(t *testing.T) {
		f := newE3HookFixture(t, true, nil)
		fake := e3HookFake()
		c := f.confirmation(fake, e3HookAccounts(), nil)
		c.deps.Writes = failingWrites{WriteRecorder: c.deps.Writes, reserve: true}
		if err := c.run(t.Context(), "9", nil); err != nil {
			t.Fatal(err)
		}
		res, raw := readE3Result(t, f.runs)
		if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 1 || res.Verdicts[0].Value != "undecidable" ||
			len(res.Verdicts[0].ProductReasons) != 1 || res.Verdicts[0].ProductReasons[0] != e3contract.ReasonWriteRecordUnavailable || len(res.Writes) != 0 {
			t.Fatalf("기록 없이 생성: %s", raw)
		}
		for _, call := range fake.Calls() {
			if call.Key.Purpose == e3contract.PurposeCreate {
				t.Fatal("쓰기 기록 없이 생성 요청을 보냄")
			}
		}
		if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != "" {
			t.Fatalf("보내지 않았는데 다음 회차를 막음: %q", refusal)
		}
	})
	t.Run("settle", func(t *testing.T) {
		f := newE3HookFixture(t, true, nil)
		c := f.confirmation(e3HookFake(), e3HookAccounts(), nil)
		c.deps.Writes = failingWrites{WriteRecorder: c.deps.Writes, settle: true}
		if err := c.run(t.Context(), "9", nil); err != nil {
			t.Fatal(err)
		}
		if res, raw := readE3Result(t, f.runs); res.Status != e3contract.StatusCompleted || len(res.Writes) != 1 || res.Writes[0].Outcome != e3contract.WriteCreated {
			t.Fatalf("결과: %s", raw)
		}
		// 갱신을 못 했으니 기록은 ID 모름이다. 리셋 확인 전까지 다음 회차를 막고, 확인 뒤에는 연다(ID 모름만, 계약 §12).
		st := f.r.resets[f.target.ID]
		if st == nil || len(st.writesObjects) != 1 || st.writesObjects[0] != (productConfirmationObject{Type: "note", IDUnknown: true}) {
			t.Fatalf("먼저 남긴 기록: %+v", st)
		}
		if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != e3contract.RefuseResetConfirmationMissing {
			t.Fatalf("갱신 실패 뒤 다음 회차: %q", refusal)
		}
		if _, err := f.r.confirmReset("ARTEX", f.target.ID, true, st.writesRef); err != nil {
			t.Fatal(err)
		}
		if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != "" {
			t.Fatalf("리셋 확인 뒤: %q", refusal)
		}
	})
	// 연결 뒤 실패·결과 불명은 거두지 않는다: 기록이 ID 모름으로 남아 리셋 확인 전까지 다음 회차를 막는다.
	for name, be := range map[string]e3fake.Behavior{
		"transport_error_kept":  {Err: errors.New("synthetic connection reset after connect")},
		"sent_incomplete_kept":  {Incomplete: true},
		"invalid_not_sent_kept": {NotSent: "transport_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, true, nil)
			fake := e3HookFake()
			fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeCreate, Role: e3contract.RoleOwner, Type: "note"}] = be
			if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
				t.Fatal(err)
			}
			st := f.r.resets[f.target.ID]
			if st == nil || len(st.writesObjects) != 1 || st.writesObjects[0] != (productConfirmationObject{Type: "note", IDUnknown: true}) {
				t.Fatalf("결과 불명 생성의 기록: %+v", st)
			}
			if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != e3contract.RefuseResetConfirmationMissing {
				t.Fatalf("결과 불명 생성 뒤 다음 회차: %q", refusal)
			}
		})
	}
	t.Run("not_sent_withdraws", func(t *testing.T) {
		// 보내지 않은 것이 확실한 생성(E² 거부)은 미리 남긴 기록을 거둬 다음 회차를 막지 않는다.
		f := newE3HookFixture(t, true, nil)
		fake := e3HookFake()
		fake.Behaviors[e3fake.Key{Purpose: e3contract.PurposeCreate, Role: e3contract.RoleOwner, Type: "note"}] = e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}
		if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
			t.Fatal(err)
		}
		if v, _ := f.r.resetConfirmation("ARTEX", f.target.ID); v.ResetRequired || v.LastWritesObjects != 0 {
			t.Fatalf("거둔 기록이 남음: %+v", v)
		}
		if refusal := f.r.e3RunStartRefusal(t.Context(), f.rec, "job-next"); refusal != "" {
			t.Fatalf("보내지 않았는데 다음 회차를 막음: %q", refusal)
		}
	})
}

// 확정 단계 계정은 선언한 역할 그대로다(계약 §4). 역할이 없거나 같은 권한 등급 확인이 없거나 선언한 계정이 인증되지 않았으면
// 요청 없이 꺼진다.
func TestProductE3ConfirmationUsesDeclaredRoles(t *testing.T) {
	// 이름순이면 tester-a가 A가 된다. 선언을 뒤집으면 tester-b가 소유·생성, tester-a가 교차 조회다.
	f := newE3HookFixtureWith(t, true, nil, func(in *diagnosisprep.DeclarationInput) {
		in.OwnerAccountLabel, in.CrossAccountLabel = "tester-b", "tester-a"
	})
	fake := e3HookFake()
	if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
		t.Fatal(err)
	}
	if res, raw := readE3Result(t, f.runs); res.Status != e3contract.StatusCompleted || res.Requests == 0 {
		t.Fatalf("선언한 역할로 확정: %s", raw)
	}
	for _, call := range fake.Calls() {
		want := "tester-b"
		if call.Key.Role == e3contract.RoleCross {
			want = "tester-a"
		}
		if call.Account != want {
			t.Fatalf("%s %s 계정 %s(기대 %s)", call.Key.Purpose, call.Key.Role, call.Account, want)
		}
	}
	for name, c := range map[string]struct {
		input    func(*diagnosisprep.DeclarationInput)
		accounts map[string]string
		reason   e3contract.DisabledReason
	}{
		"undeclared": {func(in *diagnosisprep.DeclarationInput) {
			in.OwnerAccountLabel, in.CrossAccountLabel, in.AccountsSamePrivilege = "", "", false
		}, nil, e3contract.DisabledAccountRolesUndeclared},
		"privilege_unconfirmed": {func(in *diagnosisprep.DeclarationInput) { in.AccountsSamePrivilege = false }, nil, e3contract.DisabledPrivilegeUnconfirmed},
		// 선언한 교차 계정(tester-c)이 인증에 실패했다. 인증된 다른 계정(tester-b)으로 대신하지 않는다.
		"cross_not_authenticated": {func(in *diagnosisprep.DeclarationInput) { in.CrossAccountLabel = "tester-c" }, nil, e3contract.DisabledAccountsUnverified},
		"owner_no_result":         {func(in *diagnosisprep.DeclarationInput) { in.OwnerAccountLabel = "tester-z" }, nil, e3contract.DisabledAccountsUnverified},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixtureWith(t, true, nil, c.input)
			fake := e3HookFake()
			accounts := c.accounts
			if accounts == nil {
				accounts = e3HookAccounts()
			}
			begins := 0
			if err := f.confirmation(fake, accounts, nil).run(t.Context(), "9", func() { begins++ }); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != e3contract.StatusDisabled || res.Reason != string(c.reason) || res.Requests != 0 || len(fake.Calls()) != 0 || begins != 0 {
				t.Fatalf("꺼진 확정 단계: %s", raw)
			}
			if st := f.r.resets[f.target.ID]; st != nil {
				t.Fatalf("꺼진 확정 단계가 쓰기를 남김: %+v", st)
			}
		})
	}
}

// 중지 마감(감독자가 확정 단계를 부르지 못함)에서도 결과 파일에 확정 단계 상태가 남는다(#738 이동찬 리뷰). 요청은 0건이고,
// 비활성 근거가 있으면 그 사유, 돌 수 있었으면 interrupted다. 파일이 없을 때 비활성으로 추정하지 않게 하려는 것이다.
func TestProductE3ConfirmationStoppedRecordsResult(t *testing.T) {
	for name, tc := range map[string]struct {
		declare, fake bool
		status        e3contract.ConfirmationStatus
		reason        string
	}{
		"product_deps_not_wired":    {true, false, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"no_declaration":            {false, true, e3contract.StatusDisabled, string(e3contract.DisabledNoDeclaration)},
		"would_run_stopped_earlier": {true, true, e3contract.StatusInterrupted, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, tc.declare, nil)
			var fake *e3fake.Transport
			if tc.fake {
				fake = e3HookFake()
			}
			c := f.confirmation(fake, e3HookAccounts(), nil)
			if err := c.stopped(t.Context(), "9"); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != tc.status || res.Reason != tc.reason || res.Requests != 0 || len(res.Verdicts) != 0 || len(res.Writes) != 0 {
				t.Fatalf("중지 기록: %s", raw)
			}
			if fake != nil && len(fake.Calls()) != 0 {
				t.Fatal("중지 기록이 요청을 보냄")
			}
			if st := f.r.resets[f.target.ID]; st != nil {
				t.Fatalf("중지 기록이 쓰기를 남김: %+v", st)
			}
			// 결과 파일은 회차마다 한 번이다(확정 단계 결과든 중지 기록이든 덮어쓰지 않음).
			if err := c.stopped(t.Context(), "9"); err == nil {
				t.Fatal("중지 기록이 결과 파일을 덮어씀")
			}
		})
	}
}

// 실행 시작 때 발급한 세대·인스턴스 참조가 확정 단계 시작 때 지금 대상과 다르면(대상 리셋 확인·주소·TLS 키 변경) 요청
// 없이 target_changed로 멈춘다. 대상이 바뀌었는데 같은 참조로 확정하는 경우가 없다(v3 계약 §6, 사용자 지시 2026-11-19).
func TestProductE3ConfirmationTargetChangedRefuses(t *testing.T) {
	cases := map[string]struct {
		change func(*testing.T, e3HookFixture)
		reason string
	}{
		"selected_ip": {func(t *testing.T, f e3HookFixture) {
			f.r.mu.Lock()
			rec := f.r.records[f.target.ID]
			rec.SelectedIP = "192.0.2.77"
			f.r.records[f.target.ID] = rec
			f.r.mu.Unlock()
		}, string(e3contract.RefuseTargetChanged)},
		"tls_key": {func(t *testing.T, f e3HookFixture) {
			f.r.mu.Lock()
			rec := f.r.records[f.target.ID]
			rec.Target.TLSSPKISHA256 = strings.Repeat("d", 64)
			f.r.records[f.target.ID] = rec
			f.r.mu.Unlock()
		}, string(e3contract.RefuseTargetChanged)},
		// 다른 회차가 쓰기를 남기고 운영자가 리셋을 확인했다(대상 상태가 실행 시작 때와 다름).
		"reset_confirmed": {func(t *testing.T, f e3HookFixture) {
			if err := f.r.recordConfirmationWrites(f.target.ID, "job-other", []productConfirmationObject{{Type: "note", IDUnknown: true}}); err != nil {
				t.Fatal(err)
			}
			st, err := f.r.resetConfirmation("ARTEX", f.target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.r.confirmReset("ARTEX", f.target.ID, true, st.LastWritesRef); err != nil {
				t.Fatal(err)
			}
		}, string(e3contract.RefuseTargetChanged)},
		// 대상을 확인할 수 없으면(기록 없음) 대조 전에 리셋 상태를 모른다.
		"target_gone": {func(t *testing.T, f e3HookFixture) {
			f.r.mu.Lock()
			delete(f.r.records, f.target.ID)
			f.r.mu.Unlock()
		}, string(e3contract.RefuseResetStateUnavailable)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, true, nil)
			c.change(t, f)
			fake := e3HookFake()
			begins := 0
			if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", func() { begins++ }); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != e3contract.StatusStartRefused || res.Reason != c.reason || res.Requests != 0 || len(fake.Calls()) != 0 ||
				len(res.Verdicts) != 0 || len(res.Writes) != 0 {
				t.Fatalf("대상이 바뀐 회차: %s calls=%d begins=%d", raw, len(fake.Calls()), begins)
			}
		})
	}
	// 동결한 참조 파일이 없거나 깨졌으면 결속이 비어 요청 없이 꺼진다.
	f := newE3HookFixture(t, true, nil)
	if os.Remove(filepath.Join(f.runs, liveJob, productTargetRefsFile)) != nil {
		t.Fatal("참조 파일 지우기")
	}
	fake := e3HookFake()
	if err := f.confirmation(fake, e3HookAccounts(), nil).run(t.Context(), "9", nil); err != nil {
		t.Fatal(err)
	}
	if res, raw := readE3Result(t, f.runs); res.Status != e3contract.StatusDisabled || res.Reason != string(e3contract.DisabledBindingInvalid) ||
		len(fake.Calls()) != 0 {
		t.Fatalf("참조 없는 회차: %s", raw)
	}
}

// 제품 생성자는 Deps.Random을 두지 않는다(crypto/rand). id_field 선언도 그 기본 난수원으로 ID를 만들어 생성·조회에 싣고
// 확정까지 간다(#766 이동찬 리뷰: ID 생성에 nil을 넘겨 생성 요청 0건으로 끝나던 것).
func TestProductE3ConfirmationIDFieldDefaultRandom(t *testing.T) {
	f := newE3HookFixtureWith(t, true, nil, func(in *diagnosisprep.DeclarationInput) {
		for i := range in.Types {
			if in.Types[i].Type == "note" {
				in.Types[i].Create.IDPath, in.Types[i].Create.IDField = "", "title_id"
			}
		}
	})
	fake := e3HookFake()
	c := f.confirmation(fake, e3HookAccounts(), nil)
	if c.deps.Random != nil {
		t.Fatal("제품 생성자가 난수원을 정함 — 이 시험은 기본 난수원 경로를 본다")
	}
	if err := c.run(t.Context(), "9", nil); err != nil {
		t.Fatal(err)
	}
	res, raw := readE3Result(t, f.runs)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 1 || res.Verdicts[0].Value != "confirmed" {
		t.Fatalf("id_field 확정 결과: %s", raw)
	}
	created := ""
	for _, call := range fake.Calls() {
		switch call.Key.Purpose {
		case e3contract.PurposeCreate:
			if created != "" || !strings.HasPrefix(call.ObjectID, e3contract.ObjectIDPrefix) {
				t.Fatalf("생성 요청: %+v", call)
			}
			created = call.ObjectID
		case e3contract.PurposeRead:
			if call.ObjectID != created {
				t.Fatalf("조회가 생성한 ID를 싣지 않음: %+v", call)
			}
		}
	}
	if created == "" || len(res.Writes) != 1 || res.Writes[0].ObjectID != created || res.Writes[0].Outcome != e3contract.WriteCreated {
		t.Fatalf("생성 ID·쓰기: %q %+v", created, res.Writes)
	}
}

// 회차 시작 리셋 게이트가 거절한 회차는 회차 디렉터리를 만들기 전이다. 그래도 확정 단계 결과 파일에 start_refused와 사유를
// 남겨 화면·집계가 거절 사유를 본다(S12 제품 직접 실행, 2026-11-20). 이미 있는 디렉터리에도 쓰고, 결과 파일은 덮어쓰지 않으며,
// 정해진 거부 사유가 아니면 쓰지 않는다.
func TestRecordRunStartRefusal(t *testing.T) {
	runs := t.TempDir()
	if err := recordRunStartRefusal(runs, "prep-refused", e3contract.RefuseResetConfirmationMissing); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(runs, "prep-refused"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("회차 디렉터리: %v %v", info, err)
	}
	path := filepath.Join(runs, "prep-refused", productE3ConfirmationFile)
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("결과 파일 권한: %v %v", st, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if json.Unmarshal(raw, &res) != nil || res["version"] != e3contract.RunResultVersion || res["status"] != string(e3contract.StatusStartRefused) ||
		res["reason"] != string(e3contract.RefuseResetConfirmationMissing) || res["requests"] != float64(0) {
		t.Fatalf("결과: %s", raw)
	}
	for _, k := range []string{"verdicts", "candidates", "writes", "skipped"} {
		if list, ok := res[k].([]any); !ok || len(list) != 0 {
			t.Fatalf("%s: %s", k, raw)
		}
	}
	if _, ok := res["binding"]; ok {
		t.Fatalf("결속 없는 거절에 결속: %s", raw)
	}
	// 이미 결과가 있으면 덮어쓰지 않는다.
	if recordRunStartRefusal(runs, "prep-refused", e3contract.RefuseTargetChanged) == nil {
		t.Fatal("결과 파일을 덮어씀")
	}
	// 디렉터리가 이미 있으면 그 안에 쓴다.
	if err := os.Mkdir(filepath.Join(runs, "prep-existing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := recordRunStartRefusal(runs, "prep-existing", e3contract.RefuseResetStateUnavailable); err != nil {
		t.Fatal(err)
	}
	// 정해진 사유가 아니거나 회차 참조가 아니면 쓰지 않는다.
	for name, tc := range map[string]struct {
		job     string
		refusal e3contract.StartRefusal
	}{"unknown_reason": {"prep-other", "target_approval_changed"}, "bad_job": {"../escape", e3contract.RefuseResetConfirmationMissing}} {
		if recordRunStartRefusal(runs, tc.job, tc.refusal) == nil {
			t.Fatalf("%s: 받음", name)
		}
	}
	if _, err := os.Stat(filepath.Join(runs, "prep-other")); !os.IsNotExist(err) {
		t.Fatal("거절한 입력으로 디렉터리를 만듦")
	}
}

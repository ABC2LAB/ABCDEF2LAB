package e3contract_test

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
)

// 확정 단계를 돌리지 못하고 실행이 끝나면(중지·취소·실패 마감) RunStopped가 요청 없이 상태만 정한다(#738 이동찬 리뷰).
// 비활성 근거가 있으면 Run과 같은 결과이고, 돌 수 있었으면 interrupted(사유 없음)다. 어느 쪽이든 대상 요청·쓰기 기록·시작
// 신호가 없다.
func TestRunStoppedRecordsWithoutRequests(t *testing.T) {
	cases := map[string]struct {
		edit   func(*fixture, *e3contract.Deps)
		status e3contract.ConfirmationStatus
		reason string
	}{
		"would_run":      {nil, e3contract.StatusInterrupted, ""},
		"not_wired":      {func(_ *fixture, d *e3contract.Deps) { d.Transport = nil }, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"no_declaration": {func(f *fixture, _ *e3contract.Deps) { f.in.Binding.Policy = e3contract.PolicySource{} }, e3contract.StatusDisabled, string(e3contract.DisabledNoDeclaration)},
		"no_accounts":    {func(f *fixture, _ *e3contract.Deps) { f.in.Cross.Authenticated = false }, e3contract.StatusDisabled, string(e3contract.DisabledAccountsUnverified)},
		"share_missing":  {func(f *fixture, _ *e3contract.Deps) { f.in.AdmittedTypes = -1 }, e3contract.StatusDisabled, string(e3contract.DisabledShareUnavailable)},
		// 비활성인지 확인할 수 없는 경우(동결본을 못 읽음)는 비활성으로 추정하지 않고 시작 거절로 구분된다.
		"policy_unreadable": {func(_ *fixture, d *e3contract.Deps) {
			d.Declarations = brokenDeclarations{}
		}, e3contract.StatusStartRefused, string(e3contract.RefusePolicySourceUnavailable)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			deps := f.deps()
			if c.edit != nil {
				c.edit(f, &deps)
			}
			started := 0
			deps.Started = func() { started++ }
			res := e3contract.RunStopped(context.Background(), deps, f.in)
			noConfirmed(t, res)
			if res.Status != c.status || res.Reason != c.reason || res.Requests != 0 || len(res.Verdicts) != 0 || len(res.Writes) != 0 ||
				len(f.fake.Calls()) != 0 || len(f.writes.Events()) != 0 || started != 0 {
				t.Fatalf("%+v calls=%d writes=%d started=%d", res, len(f.fake.Calls()), len(f.writes.Events()), started)
			}
		})
	}
}

type brokenDeclarations struct{}

func (brokenDeclarations) ReadFrozen(context.Context, e3contract.PolicySource) (e3contract.Declaration, error) {
	return e3contract.Declaration{}, e3contract.ErrPolicyUnavailable
}

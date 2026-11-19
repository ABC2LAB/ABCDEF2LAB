package e3contract_test

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
)

// startOrderTransport는 표식 등록(첫 E² 호출) 때 Started가 이미 불렸는지 남긴다.
type startOrderTransport struct {
	e3contract.Transport
	started           *int
	startedAtRegister int
}

func (s *startOrderTransport) RegisterMarker(ctx context.Context, b e3contract.RunBinding, m e3contract.Marker) error {
	s.startedAtRegister = *s.started
	return s.Transport.RegisterMarker(ctx, b, m)
}

// Started는 확정 단계가 실제로 대상 요청을 시작하기 직전에만 한 번 불린다. 꺼짐·시작 거절·몫에 든 종류 없음·확정할 종류
// 없음이면 부르지 않는다. 감독자는 이 신호로만 엔진 정리 상태·단계 기록을 남기므로, 꺼진 회차의 감독자 흐름이 E③가 없을
// 때와 같다(Go 조건 2).
func TestRunStartedOnlyWhenRequestsStart(t *testing.T) {
	cases := map[string]func(*fixture, *e3contract.Deps){
		"not_wired":        func(_ *fixture, d *e3contract.Deps) { d.Transport = nil },
		"no_declaration":   func(f *fixture, _ *e3contract.Deps) { f.in.Binding.Policy = e3contract.PolicySource{} },
		"accounts_missing": func(f *fixture, _ *e3contract.Deps) { f.in.Cross = e3contract.Account{} },
		"policy_unavailable": func(_ *fixture, d *e3contract.Deps) {
			d.Declarations = e3fake.Declarations{Err: e3contract.ErrPolicyUnavailable}
		},
		"no_confirmable_type": func(f *fixture, _ *e3contract.Deps) {
			decl := declaration()
			for i := range decl.Types {
				decl.Types[i] = e3contract.TypeDeclaration{Type: decl.Types[i].Type, Sharing: e3contract.SharingShared}
			}
			src, err := e3contract.Freeze(decl, frozenRef)
			if err != nil {
				t.Fatal(err)
			}
			f.decls.Stored[frozenRef], f.in.Binding.Policy = decl, src
		},
		"share_zero": func(f *fixture, _ *e3contract.Deps) { f.in.AdmittedTypes = 0 },
		"reset_refused": func(_ *fixture, d *e3contract.Deps) {
			d.Reset = e3fake.Reset{State: e3contract.ResetState{PreviousWrites: true, Residue: e3contract.ResidueAbsent}}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			deps := f.deps()
			edit(f, &deps)
			started := 0
			deps.Started = func() { started++ }
			res := e3contract.Run(context.Background(), deps, f.in)
			if started != 0 || len(f.fake.Calls()) != 0 || res.Requests != 0 {
				t.Fatalf("started=%d calls=%d result=%+v", started, len(f.fake.Calls()), res)
			}
		})
	}
	t.Run("runs", func(t *testing.T) {
		f := newFixture(t)
		deps := f.deps()
		started := 0
		order := &startOrderTransport{Transport: f.fake, started: &started}
		deps.Transport = order
		deps.Started = func() { started++ }
		res := e3contract.Run(context.Background(), deps, f.in)
		if res.Status != e3contract.StatusCompleted || started != 1 || order.startedAtRegister != 1 || len(f.fake.Calls()) == 0 {
			t.Fatalf("started=%d at_register=%d calls=%d result=%+v", started, order.startedAtRegister, len(f.fake.Calls()), res)
		}
	})
}

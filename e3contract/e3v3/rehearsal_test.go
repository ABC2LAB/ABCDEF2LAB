package e3v3_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/e3contract/e3v3/v3fake"
	"github.com/Autumn-27/artex/oracle"
)

// E③ 실제 검증 사전 실행(Go 조건 1 준비). 대상별 선언 입력과 시나리오 목록은 데이터 파일이다(대상 경로·칸 이름을 코드에
// 넣지 않음). 이 시험은 각 시나리오를 가짜 v3 E²와 어댑터로 돌려 목록의 기대 상태·사유와 맞는지 본다. E3_REHEARSAL_OUT을
// 주면 시나리오마다 <out>/<id>/e3-confirmation.json을 써서 집계 스크립트(scripts/eval/e3_vampi_tally.py)로 넘길 수 있다.
const rehearsalDir = "../../scripts/eval/e3_vampi"

type rehearsalManifest struct {
	Version     string              `json:"version"`
	Declaration string              `json:"declaration"`
	Scenarios   []rehearsalScenario `json:"scenarios"`
}

type rehearsalScenario struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Fake   bool   `json:"fake"`
	Expect struct {
		Status       string `json:"status"`
		Reason       string `json:"reason"`
		RequestsZero bool   `json:"requests_zero"`
		Verdicts     map[string]struct {
			Value   string   `json:"value"`
			Reasons []string `json:"reasons"`
		} `json:"verdicts"`
	} `json:"expect"`
}

type rehearsalDeclarationInput struct {
	Identity              e3contract.IdentitySpec      `json:"identity"`
	Types                 []e3contract.TypeDeclaration `json:"types"`
	OwnerAccountLabel     string                       `json:"owner_account_label"`
	CrossAccountLabel     string                       `json:"cross_account_label"`
	AccountsSamePrivilege bool                         `json:"accounts_same_privilege"`
}

func loadRehearsal(t *testing.T) (rehearsalManifest, e3contract.Declaration) {
	t.Helper()
	var m rehearsalManifest
	raw, err := os.ReadFile(filepath.Join(rehearsalDir, "scenarios.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Version != "e3-acceptance-scenarios/v1" {
		t.Fatalf("시나리오 목록: %v", err)
	}
	var in rehearsalDeclarationInput
	dec := json.NewDecoder(bytes.NewReader(mustRead(t, filepath.Join(rehearsalDir, m.Declaration))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("선언 입력: %v", err)
	}
	d := e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: "target-1", TargetApprovalRevision: "approval-3",
		DeclaredBy: "operator-1", DeclaredAt: t0, Identity: in.Identity, Types: in.Types,
		OwnerAccountLabel: in.OwnerAccountLabel, CrossAccountLabel: in.CrossAccountLabel, AccountsSamePrivilege: in.AccountsSamePrivilege}
	if err := d.Validate(); err != nil {
		t.Fatalf("선언이 계약 검사를 통과하지 못함: %v", err)
	}
	return m, d
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// rehearsalWorld는 선언 하나로 만든 가짜 v3 세계다. setup이 대상 동작·입력·리셋·저널 손잡이를 바꾼다.
type rehearsalWorld struct {
	decl     e3contract.Declaration
	typ      string
	target   *e3fake.Transport
	e2       *v3fake.E2
	in       e3contract.Input
	reset    e3contract.ResetState
	decls    e3fake.Declarations
	frozen   *e3contract.Declaration
	writes   *e3fake.Writes
	transErr error
}

func newRehearsalWorld(t *testing.T, d e3contract.Declaration) *rehearsalWorld {
	t.Helper()
	src, err := e3contract.Freeze(d, frozenRef)
	if err != nil {
		t.Fatal(err)
	}
	target := e3fake.New()
	target.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleOwner}] = e3fake.Behavior{SubjectID: "subject-a"}
	target.Behaviors[e3fake.Key{Purpose: e3contract.PurposeIdentity, Role: e3contract.RoleCross}] = e3fake.Behavior{SubjectID: "subject-b"}
	w := &rehearsalWorld{decl: d, typ: d.Types[0].Type, target: target,
		e2:    v3fake.New(target, []byte("run-audit-key-for-tests-only-32b")),
		decls: e3fake.Declarations{Stored: map[string]e3contract.Declaration{frozenRef: d}},
		reset: e3contract.ResetState{Residue: e3contract.ResidueAbsent},
		in: e3contract.Input{
			Binding: e3contract.RunBinding{JobRef: "job-1", RunRef: "run-1", TaskRef: "task-1", E2PolicyRef: "e2-policy-1",
				GenerationRef: "gen-1", InstanceRef: "inst-1", Policy: src},
			TargetRef: "target-1", WriteConsent: true,
			Owner:           e3contract.Account{Label: d.OwnerAccountLabel, Authenticated: true},
			Cross:           e3contract.Account{Label: d.CrossAccountLabel, Authenticated: true},
			PolicyValidFrom: t0.Add(-time.Hour), PolicyValidUntil: t0.Add(24 * time.Hour),
			AdmittedTypes: 8, RequestTimeout: 10 * time.Second,
		}}
	w.writes = &e3fake.Writes{Transport: target}
	return w
}

func (w *rehearsalWorld) key(p e3contract.Purpose, r e3contract.Role) e3fake.Key {
	k := e3fake.Key{Purpose: p, Role: r}
	if p != e3contract.PurposeIdentity {
		k.Type = w.typ
	}
	return k
}

func (w *rehearsalWorld) run() e3contract.RunResult {
	a, _ := e3v3.New(w.e2)
	deps := e3contract.Deps{Transport: a, Journal: a, Declarations: w.decls, Reset: e3fake.Reset{State: w.reset}, Writes: w.writes,
		Random: bytes.NewReader(seed()), Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler)}
	return e3contract.Run(context.Background(), deps, w.in)
}

// rehearsalSetups는 시나리오 이름별 가짜 설정이다. 대상 값은 선언에서 읽고, 여기서는 상황만 만든다.
var rehearsalSetups = map[string]func(*rehearsalWorld){
	"confirmed": func(*rehearsalWorld) {},
	// 고쳐진 대상(교차 조회가 차단): 확정하지 않는다.
	"fixed_target": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeRead, e3contract.RoleCross)] = e3fake.Behavior{Status: 404, Normal: oracle.StateNo}
	},
	"declaration_missing": func(w *rehearsalWorld) { w.in.Binding.Policy = e3contract.PolicySource{} },
	"account_unverified":  func(w *rehearsalWorld) { w.in.Cross.Authenticated = false },
	// 두 라벨이 대상의 같은 사용자로 로그인한다.
	"identity_same_subject": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeIdentity, e3contract.RoleCross)] = e3fake.Behavior{SubjectID: "subject-a"}
	},
	// 신원 응답에서 사용자 식별자를 찾지 못한다.
	"identity_unverified": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeIdentity, e3contract.RoleCross)] = e3fake.Behavior{}
	},
	"marker_unsupported": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeRead, e3contract.RoleCross)] = e3fake.Behavior{MarkerStatus: e3contract.MarkerUnsupported}
	},
	// 실행 시작 때 동결한 선언과 다른 선언이 정책 출처 자리에 있다.
	"policy_mismatch": func(w *rehearsalWorld) {
		other := w.decl
		other.Types = append([]e3contract.TypeDeclaration(nil), w.decl.Types...)
		other.Types[0].OwnerOnlyFields = append([]string{"other_field"}, other.Types[0].OwnerOnlyFields...)
		w.decls.Stored[frozenRef] = other
	},
	// 실행 시작 뒤 대상 세대·인스턴스가 바뀌었다(리셋 게이트가 알림).
	"generation_mismatch": func(w *rehearsalWorld) { w.reset.TargetChanged = true },
	"approval_denied": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeCreate, e3contract.RoleOwner)] = e3fake.Behavior{NotSent: e3contract.NotSentApprovalDenied}
	},
	"budget_exhausted": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeRead, e3contract.RoleCross)] = e3fake.Behavior{NotSent: e3contract.NotSentBudgetExhausted}
	},
	// 지난 회차가 쓰기를 남겼고 운영자 리셋 확인이 없다.
	"reset_missing": func(w *rehearsalWorld) {
		w.reset = e3contract.ResetState{PreviousWrites: true, Residue: e3contract.ResidueAbsent}
	},
	"partial_response": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeRead, e3contract.RoleCross)] = e3fake.Behavior{MarkerStatus: e3contract.MarkerPartial}
	},
	// 교차 조회를 보냈지만 응답을 다 받지 못했다(다시 보내지 않음).
	"result_unknown": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeRead, e3contract.RoleCross)] = e3fake.Behavior{Incomplete: true}
	},
	// E² 기록의 실행 결속이 이 회차와 다르다(다른 인스턴스 참조).
	"run_binding_mismatch": func(w *rehearsalWorld) {
		w.e2.Recorded = func(e *e3v3.Event) { e.E3.InstanceRef = "inst-other" }
	},
	// 지난 회차가 쓰기를 남겼고 리셋 확인은 있지만 지난 표식이 아직 보인다.
	"previous_markers_present": func(w *rehearsalWorld) {
		w.reset = e3contract.ResetState{PreviousWrites: true, ResetConfirmed: true, Residue: e3contract.ResiduePresent}
	},
	// 요청 본문 칸 ID로 만든 객체가 이미 있다(같은 ID 충돌 등). 대상이 생성을 거부한다.
	"id_collision": func(w *rehearsalWorld) {
		w.target.Behaviors[w.key(e3contract.PurposeCreate, e3contract.RoleOwner)] = e3fake.Behavior{Status: 400, Normal: oracle.StateNo}
	},
	// E²가 돌려준 관찰과 봉인된 저널 이벤트가 다르다.
	"journal_mismatch": func(w *rehearsalWorld) {
		w.e2.Returned = func(e *e3v3.Event) {
			if e.Program == e3contract.PurposeRead && e.Role == e3contract.RoleCross {
				e.Marker.Presence, e.Marker.Method = e3contract.MarkerPresent, e3contract.MatchExact
				e.BodyDigestSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			}
		}
	},
}

func TestAcceptanceRehearsal(t *testing.T) {
	m, d := loadRehearsal(t)
	out := os.Getenv("E3_REHEARSAL_OUT")
	seen := map[string]bool{}
	for _, sc := range m.Scenarios {
		if seen[sc.ID] {
			t.Fatalf("시나리오 ID 중복: %s", sc.ID)
		}
		seen[sc.ID] = true
		if !sc.Fake {
			continue
		}
		setup, ok := rehearsalSetups[sc.Name]
		if !ok {
			t.Fatalf("%s: 가짜 설정 없음 %q", sc.ID, sc.Name)
		}
		t.Run(sc.ID+"-"+sc.Name, func(t *testing.T) {
			w := newRehearsalWorld(t, d)
			setup(w)
			res := w.run()
			raw, _ := json.Marshal(res)
			if out != "" {
				dir := filepath.Join(out, sc.ID)
				if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(filepath.Join(dir, "e3-confirmation.json"), raw, 0o600) != nil {
					t.Fatal("결과 쓰기")
				}
			}
			if string(res.Status) != sc.Expect.Status || res.Reason != sc.Expect.Reason {
				t.Fatalf("상태·사유 %s/%s, 기대 %s/%s: %s", res.Status, res.Reason, sc.Expect.Status, sc.Expect.Reason, raw)
			}
			if sc.Expect.RequestsZero && (res.Requests != 0 || len(w.target.Calls()) != 0) {
				t.Fatalf("요청이 나감: %s", raw)
			}
			for typ, want := range sc.Expect.Verdicts {
				var got *e3contract.Verdict
				for i := range res.Verdicts {
					if res.Verdicts[i].ResourceType == typ {
						got = &res.Verdicts[i]
					}
				}
				if got == nil || string(got.Value) != want.Value {
					t.Fatalf("%s 판정 %v, 기대 %s: %s", typ, got, want.Value, raw)
				}
				for _, r := range want.Reasons {
					if !slices.ContainsFunc(got.ProductReasons, func(x e3contract.ProductReason) bool { return string(x) == r }) &&
						!slices.ContainsFunc(got.OracleReasons, func(x oracle.JudgeReason) bool { return string(x) == r }) {
						t.Fatalf("%s 사유 %v·%v에 %s 없음", typ, got.ProductReasons, got.OracleReasons, r)
					}
				}
			}
			// 요청 본문 칸 ID 선언이면 생성·조회가 모두 같은 ID를 싣고, 조회 경로에 그 ID가 들어간다. 생성은 종류마다 한 번이다.
			if d.Types[0].Create.IDField != "" {
				var id string
				creates := 0
				for _, c := range w.target.Calls() {
					if c.Key.Purpose == e3contract.PurposeIdentity {
						continue
					}
					if c.ObjectID == "" || id != "" && c.ObjectID != id {
						t.Fatalf("요청의 ID가 없거나 다름: %+v", c)
					}
					id = c.ObjectID
					if c.Key.Purpose == e3contract.PurposeCreate {
						creates++
					}
					if c.Key.Purpose == e3contract.PurposeRead && !strings.Contains(c.Path, id) {
						t.Fatalf("조회 경로에 ID 없음: %s", c.Path)
					}
				}
				if creates > 1 {
					t.Fatalf("생성 %d번(다시 보냄)", creates)
				}
			}
			// 거짓 CONFIRMED 없음: 기대하지 않은 확정 판정·확정 후보가 없다.
			for _, v := range res.Verdicts {
				if v.Value == oracle.Confirmed && sc.Expect.Verdicts[v.ResourceType].Value != string(oracle.Confirmed) {
					t.Fatalf("거짓 CONFIRMED: %s", raw)
				}
			}
			for _, c := range res.Candidates {
				if c.Level == oracle.Confirmed && len(sc.Expect.Verdicts) == 0 {
					t.Fatalf("확정 후보가 판정 없이: %s", raw)
				}
			}
		})
	}
}

//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// policySourceBase는 기본 준비 서비스의 상태 조회만 흉내 낸다(회차 소유 확인).
type policySourceBase struct {
	job   *diagnosisprep.Job
	error string
	err   error
	calls []diagnosisprep.Request
}

func (b *policySourceBase) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	b.calls = append(b.calls, r)
	if b.err != nil {
		return diagnosisprep.Response{}, b.err
	}
	return diagnosisprep.Response{Version: r.Version, RequestID: r.RequestID, Job: b.job, Error: b.error}, nil
}

func policySourceRuns(t *testing.T) string {
	t.Helper()
	runs := t.TempDir()
	if os.Chmod(runs, 0o700) != nil || os.Mkdir(filepath.Join(runs, liveJob), 0o700) != nil {
		t.Fatal("회차 디렉터리")
	}
	return runs
}

// frozenForPolicySource는 종류 둘(소유자 전용 note·공유 post)을 가진 동결본 바이트다. mutate로 모양을 바꿔 쓴다.
func frozenForPolicySource(t *testing.T, mutate func(*e3contract.Declaration)) []byte {
	t.Helper()
	in := declarationTestInput()
	d := e3contract.Declaration{Identity: in.Identity, Types: []e3contract.TypeDeclaration{in.Types[1], in.Types[0]}}
	if mutate != nil {
		mutate(&d)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// frozenTypesSummary는 frozenForPolicySource 기본 동결본의 종류 요약이다.
var frozenTypesSummary = []diagnosisprep.ProductDeclaredType{{Type: "note", Sharing: "owner_only", Confirmable: true}, {Type: "post", Sharing: "shared"}}

// 동결본 바이트의 SHA-256과 종류 요약을 돌려주고, 동결본이 없으면 present=false다. 읽기 규칙(권한·링크)은 확정 단계 읽기와 같다.
func TestReadPolicySource(t *testing.T) {
	runs := policySourceRuns(t)
	if got, err := readPolicySource(runs, liveJob); err != nil || got.Present || got.SHA256 != "" {
		t.Fatalf("동결본 없음: %+v %v", got, err)
	}
	if got, err := readPolicySource(runs, "job-without-dir"); err != nil || got.Present {
		t.Fatalf("회차 디렉터리 없음: %+v %v", got, err)
	}
	raw := frozenForPolicySource(t, nil)
	path := filepath.Join(runs, liveJob, productFrozenDeclarationFile)
	if os.WriteFile(path, raw, 0o600) != nil {
		t.Fatal("동결본 쓰기")
	}
	sum := sha256.Sum256(raw)
	if got, err := readPolicySource(runs, liveJob); err != nil || !got.Present || got.SHA256 != hex.EncodeToString(sum[:]) ||
		got.OwnerAccountLabel != "" || got.CrossAccountLabel != "" || !reflect.DeepEqual(got.Types, frozenTypesSummary) {
		t.Fatalf("동결본 digest·종류 요약(라벨 없는 선언): %+v %v", got, err)
	}
	if os.Chmod(path, 0o644) != nil {
		t.Fatal("권한 바꾸기")
	}
	if _, err := readPolicySource(runs, liveJob); err == nil {
		t.Fatal("다른 사용자가 읽을 수 있는 동결본을 받음")
	}
	if os.Remove(path) != nil || os.Symlink(filepath.Join(runs, "elsewhere"), path) != nil {
		t.Fatal("링크 만들기")
	}
	if _, err := readPolicySource(runs, liveJob); err == nil {
		t.Fatal("링크 동결본을 받음")
	}
	for _, job := range []string{"", ".", "..", "a/b", "../" + liveJob} {
		if _, err := readPolicySource(runs, job); err == nil {
			t.Fatalf("회차 참조 %q를 받음", job)
		}
	}
	if _, err := readPolicySource("", liveJob); err == nil {
		t.Fatal("회차 디렉터리 설정 없이 받음")
	}
}

// 종류 요약은 종류 이름·공유 여부·확정 가능 여부만 싣는다. 경로·본문 틀·표식 칸·ID 칸은 IPC 값에 나가지 않는다.
func TestReadPolicySourceTypesCarryNoRequestShape(t *testing.T) {
	runs := policySourceRuns(t)
	if os.WriteFile(filepath.Join(runs, liveJob, productFrozenDeclarationFile), frozenForPolicySource(t, nil), 0o600) != nil {
		t.Fatal("동결본 쓰기")
	}
	got, err := readPolicySource(runs, liveJob)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"/app/notes", "/app/me", `{\"title\"`, "title", "marker", "body", "zeta", "id_path", "path"} {
		if strings.Contains(string(wire), leak) {
			t.Fatalf("IPC 값에 선언 내용 %q가 나감: %s", leak, wire)
		}
	}
}

// 요약을 만들 수 없는 동결본은 일부만 싣지 않고 읽지 못한 것으로 다룬다(present=true면 종류가 늘 1개 이상·계약 모양).
func TestReadPolicySourceRejectsUnsummarizableFrozen(t *testing.T) {
	for name, raw := range map[string][]byte{
		"not_declaration": []byte(`{"frozen":"declaration"}`),
		"broken_json":     []byte(`{"types":`),
		"trailing":        append(frozenForPolicySource(t, nil), []byte(`{}`)...),
		"no_types":        frozenForPolicySource(t, func(d *e3contract.Declaration) { d.Types = nil }),
		"unsorted":        frozenForPolicySource(t, func(d *e3contract.Declaration) { d.Types[0], d.Types[1] = d.Types[1], d.Types[0] }),
		"duplicate":       frozenForPolicySource(t, func(d *e3contract.Declaration) { d.Types[1] = d.Types[0] }),
		"bad_type_name":   frozenForPolicySource(t, func(d *e3contract.Declaration) { d.Types[0].Type = "Note" }),
		"bad_sharing":     frozenForPolicySource(t, func(d *e3contract.Declaration) { d.Types[1].Sharing = "private" }),
		// 소유자 전용인데 표식 칸이 소유자 전용 필드가 아니면 확정 불가다. 공유 선언으로 보이지 않게 거부한다.
		"owner_only_unconfirmable": frozenForPolicySource(t, func(d *e3contract.Declaration) { d.Types[0].Create.MarkerField = "title" }),
		"over_max": frozenForPolicySource(t, func(d *e3contract.Declaration) {
			d.Types = nil
			for i := 0; i <= e3contract.MaxDeclarationTypes; i++ {
				d.Types = append(d.Types, e3contract.TypeDeclaration{Type: "t" + string(rune('a'+i)), Sharing: e3contract.SharingShared})
			}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			runs := policySourceRuns(t)
			if os.WriteFile(filepath.Join(runs, liveJob, productFrozenDeclarationFile), raw, 0o600) != nil {
				t.Fatal("동결본 쓰기")
			}
			if got, err := readPolicySource(runs, liveJob); err == nil {
				t.Fatalf("요약할 수 없는 동결본을 받음: %+v", got)
			}
			endpoint := productPreparationEndpoint{base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}, product: &productRuntime{config: urlPreparationInstallation{RunsDir: runs}}}
			request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "verdict-policy-source", Principal: "ARTEX",
				Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "policy_source", JobID: liveJob}}
			out, err := endpoint.Call(t.Context(), request)
			if err != nil || diagnosisprep.ValidateResponse(request, out) != nil || out.Product == nil || out.Product.Reason != "policy_source_unavailable" || out.Product.PolicySource != nil {
				t.Fatalf("IPC 응답: %+v %v", out, err)
			}
		})
	}
}

// IPC policy_source: 회차 소유는 기본 준비 서비스의 상태 조회(Principal·Job)로 확인하고, 응답은 계약 검사를 통과한다.
func TestProductPolicySourceEndpoint(t *testing.T) {
	runs := policySourceRuns(t)
	raw := frozenForPolicySource(t, nil)
	if os.WriteFile(filepath.Join(runs, liveJob, productFrozenDeclarationFile), raw, 0o600) != nil {
		t.Fatal("동결본 쓰기")
	}
	sum := sha256.Sum256(raw)
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "verdict-policy-source", Principal: "ARTEX",
		Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "policy_source", JobID: liveJob}}
	for name, tc := range map[string]struct {
		base   *policySourceBase
		runs   string
		reason string
		want   *diagnosisprep.ProductPolicySource
	}{
		"owned":     {base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}, runs: runs, want: &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(sum[:]), Types: frozenTypesSummary}},
		"not_owned": {base: &policySourceBase{error: "not_found"}, runs: runs, reason: "not_found"},
		"forbidden": {base: &policySourceBase{error: "forbidden"}, runs: runs, reason: "not_found"},
		// 오류와 함께 회차가 실려 와도 오류가 이긴다.
		"error_with_job": {base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}, error: "forbidden"}, runs: runs, reason: "not_found"},
		"other_job":      {base: &policySourceBase{job: &diagnosisprep.Job{ID: "job-other"}}, runs: runs, reason: "not_found"},
		"no_job":         {base: &policySourceBase{}, runs: runs, reason: "not_found"},
		"base_down":      {base: &policySourceBase{err: errors.New("closed")}, runs: runs, reason: "policy_source_unavailable"},
		"runs_missing":   {base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}, reason: "policy_source_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := productPreparationEndpoint{base: tc.base, product: &productRuntime{config: urlPreparationInstallation{RunsDir: tc.runs}}}
			out, err := endpoint.Call(t.Context(), request)
			if err != nil || diagnosisprep.ValidateResponse(request, out) != nil || out.Error != "" || out.Product == nil {
				t.Fatalf("응답: %+v %v", out, err)
			}
			if out.Product.Reason != tc.reason || (tc.want == nil) != (out.Product.PolicySource == nil) ||
				tc.want != nil && !reflect.DeepEqual(*out.Product.PolicySource, *tc.want) {
				t.Fatalf("policy_source: reason=%q source=%+v", out.Product.Reason, out.Product.PolicySource)
			}
			if len(tc.base.calls) != 1 || tc.base.calls[0].Command != diagnosisprep.Status || tc.base.calls[0].Principal != "ARTEX" || tc.base.calls[0].JobID != liveJob {
				t.Fatalf("회차 소유 확인: %+v", tc.base.calls)
			}
		})
	}
}

// 계정 라벨은 그 회차의 동결본에서만 읽는다. 대상의 현재 선언을 다른 라벨로 바꿔도 결과가 같다(현재 등록으로 대신하지 않음).
func TestReadPolicySourceLabelsFromFrozenOnly(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	before, err := readPolicySource(f.runs, liveJob)
	if err != nil || !before.Present || before.OwnerAccountLabel != "tester-a" || before.CrossAccountLabel != "tester-b" || len(before.Types) == 0 {
		t.Fatalf("동결본 라벨·종류: %+v %v", before, err)
	}
	// 현재 선언은 라벨도 종류도 다르게 바꾼다(공유 종류 하나 더).
	in := declarationTestInput()
	in.OwnerAccountLabel, in.CrossAccountLabel = "tester-c", "tester-d"
	in.Types = append(in.Types, e3contract.TypeDeclaration{Type: "album", Sharing: e3contract.SharingShared})
	current, err := f.r.lookup("ARTEX", f.target.Origin+"/app", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.putDeclaration("ARTEX", current.Target.ID, current.Target.Revision, in); err != nil {
		t.Fatal(err)
	}
	after, err := readPolicySource(f.runs, liveJob)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("현재 선언을 바꾸자 결과가 바뀜: %+v → %+v %v", before, after, err)
	}
}

// 동결본 라벨이 계약 모양이 아니면(대문자·같은 라벨·하나만) 라벨 없이 digest·종류 요약만 준다. 라벨을 실으면 IPC 응답 검사가 응답
// 전체를 거부하므로, 판정 상세의 동결본 대조까지 막히지 않게 하려는 것이다.
func TestReadPolicySourceRejectsBadFrozenLabels(t *testing.T) {
	for name, labels := range map[string][2]string{"upper": {"Tester A", "tester-b"}, "same": {"tester-a", "tester-a"}, "one": {"tester-a", ""}} {
		t.Run(name, func(t *testing.T) {
			runs := policySourceRuns(t)
			raw := frozenForPolicySource(t, func(d *e3contract.Declaration) { d.OwnerAccountLabel, d.CrossAccountLabel = labels[0], labels[1] })
			if os.WriteFile(filepath.Join(runs, liveJob, productFrozenDeclarationFile), raw, 0o600) != nil {
				t.Fatal("동결본 쓰기")
			}
			got, err := readPolicySource(runs, liveJob)
			sum := sha256.Sum256(raw)
			if err != nil || !got.Present || got.SHA256 != hex.EncodeToString(sum[:]) || got.OwnerAccountLabel != "" || got.CrossAccountLabel != "" ||
				!reflect.DeepEqual(got.Types, frozenTypesSummary) || !diagnosisprep.ValidProductPolicySourceValue(*got) {
				t.Fatalf("라벨 거르기: %+v %v", got, err)
			}
		})
	}
}

// 확정 단계 결과 파일은 상태·사유만 읽는다. 없으면 present=false, 있는데 읽지 못하거나 계약 밖이면 unreadable이다(없음과 섞지
// 않는다).
func TestReadConfirmationState(t *testing.T) {
	runs := policySourceRuns(t)
	path := filepath.Join(runs, liveJob, productE3ConfirmationFile)
	if got := readConfirmationState(runs, liveJob); !reflect.DeepEqual(*got, diagnosisprep.ProductConfirmationState{}) {
		t.Fatalf("파일 없음: %+v", got)
	}
	write := func(raw string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(path)
		if os.WriteFile(path, []byte(raw), mode) != nil || os.Chmod(path, mode) != nil {
			t.Fatal("결과 파일 쓰기")
		}
	}
	version := e3contract.RunResultVersion
	unreadable := diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable}
	for name, tc := range map[string]struct {
		raw  string
		mode os.FileMode
		want diagnosisprep.ProductConfirmationState
	}{
		"disabled":  {`{"version":"` + version + `","status":"disabled","reason":"declaration_missing","verdicts":[]}`, 0o600, diagnosisprep.ProductConfirmationState{Present: true, Status: "disabled", Reason: "declaration_missing"}},
		"completed": {`{"version":"` + version + `","status":"completed","verdicts":[{"resource_type":"note"}]}`, 0o600, diagnosisprep.ProductConfirmationState{Present: true, Status: "completed"}},
		// 건너뛴 종류는 결과 파일 그대로(종류·사유 코드)다. 빈 배열이면 비우고, 모양이 계약 밖이면 전체가 unreadable이다.
		"skipped": {`{"version":"` + version + `","status":"completed","verdicts":[],"skipped":[{"resource_type":"post","reason":"not_owner_only"},{"resource_type":"album","reason":"not_owner_only"}]}`, 0o600,
			diagnosisprep.ProductConfirmationState{Present: true, Status: "completed", Skipped: []diagnosisprep.ProductSkippedType{{ResourceType: "post", Reason: "not_owner_only"}, {ResourceType: "album", Reason: "not_owner_only"}}}},
		"skipped_empty":      {`{"version":"` + version + `","status":"disabled","reason":"declaration_missing","skipped":[]}`, 0o600, diagnosisprep.ProductConfirmationState{Present: true, Status: "disabled", Reason: "declaration_missing"}},
		"skipped_duplicate":  {`{"version":"` + version + `","status":"completed","skipped":[{"resource_type":"post","reason":"not_owner_only"},{"resource_type":"post","reason":"not_owner_only"}]}`, 0o600, unreadable},
		"skipped_bad_type":   {`{"version":"` + version + `","status":"completed","skipped":[{"resource_type":"Post","reason":"not_owner_only"}]}`, 0o600, unreadable},
		"skipped_bad_reason": {`{"version":"` + version + `","status":"completed","skipped":[{"resource_type":"post","reason":"Not Owner"}]}`, 0o600, unreadable},
		"skipped_not_array":  {`{"version":"` + version + `","status":"completed","skipped":"post"}`, 0o600, unreadable},
		"skipped_over_max":   {`{"version":"` + version + `","status":"completed","skipped":[` + strings.TrimSuffix(strings.Repeat(`{"resource_type":"post","reason":"not_owner_only"},`, 17), ",") + `]}`, 0o600, unreadable},
		"broken_json":        {`{"version":`, 0o600, unreadable},
		"wrong_version":      {`{"version":"other/v9","status":"completed"}`, 0o600, unreadable},
		"unknown_status":     {`{"version":"` + version + `","status":"maybe"}`, 0o600, unreadable},
		"status_literal":     {`{"version":"` + version + `","status":"unreadable"}`, 0o600, unreadable},
		"bad_reason":         {`{"version":"` + version + `","status":"disabled","reason":"Not Wired"}`, 0o600, unreadable},
		"world_readable":     {`{"version":"` + version + `","status":"completed"}`, 0o644, unreadable},
	} {
		t.Run(name, func(t *testing.T) {
			write(tc.raw, tc.mode)
			if got := readConfirmationState(runs, liveJob); !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("%+v, want %+v", got, tc.want)
			}
		})
	}
	_ = os.Remove(path)
	if os.Symlink(filepath.Join(runs, "elsewhere"), path) != nil {
		t.Fatal("링크 만들기")
	}
	if got := readConfirmationState(runs, liveJob); !reflect.DeepEqual(*got, unreadable) {
		t.Fatalf("링크 결과 파일: %+v", got)
	}
}

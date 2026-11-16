//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func runList(t *testing.T, endpoint Endpoint, principal string, input RunListRequest) Response {
	t.Helper()
	r := request(ListRuns)
	r.Principal = principal
	r.Runs = &input
	return call(t, endpoint, r)
}
func TestRunListOwnedPaginationSnapshotAndNoExecution(t *testing.T) {
	service, backend, _ := newFixture(t)
	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		job := create(t, service, fmt.Sprintf("secret-create-key-%d", i))
		ids[job.ID] = true
	}
	other := request(Create)
	other.Principal = "other"
	other.IdempotencyKey = "other-private-key"
	other.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local", DisplayName: "다른 주체"}
	if response := call(t, service, other); response.Error != "" {
		t.Fatal(response.Error)
	}
	inspectBefore := backend.inspectCalls.Load()
	page := runList(t, service, "owner", RunListRequest{Limit: 2})
	if page.Error != "" || !page.RunPage.More || len(page.RunPage.Runs) != 2 {
		t.Fatal("첫 목록", page.Error)
	}
	firstCursor := page.RunPage.NextCursor
	seen := map[string]bool{}
	for {
		for _, run := range page.RunPage.Runs {
			if !ids[run.ID] || seen[run.ID] {
				t.Fatal("소유권·페이지 중복")
			}
			seen[run.ID] = true
		}
		if !page.RunPage.More {
			break
		}
		page = runList(t, service, "owner", RunListRequest{Cursor: page.RunPage.NextCursor})
		if page.Error != "" {
			t.Fatal(page.Error)
		}
	}
	if len(seen) != 5 || backend.calls.Load() != 0 || backend.inspectCalls.Load() != inspectBefore {
		t.Fatal("읽기에서 실행·준비 또는 누락 발생")
	}
	private, _ := json.Marshal(page)
	for _, value := range []string{"secret-create-key", "other-private-key", "https://example.test", "source_digest", "budget_ref", "scope"} {
		if strings.Contains(string(private), value) {
			t.Fatal("목록에 보호 입력 노출")
		}
	}
	if got := runList(t, service, "other", RunListRequest{Cursor: firstCursor}); got.Error != "snapshot_changed" {
		t.Fatal("다른 주체 cursor 재사용", got.Error)
	}
	create(t, service, "created-between-pages")
	if got := runList(t, service, "owner", RunListRequest{Cursor: firstCursor}); got.Error != "snapshot_changed" {
		t.Fatal("변경된 집합의 페이지를 혼합", got.Error)
	}
}
func TestRunListRestartUnknownAndReportFacts(t *testing.T) {
	service, backend, root := newFixture(t)
	completed := create(t, service, "complete")
	if response := start(t, service, completed, "start-complete"); response.Error != "" {
		t.Fatal(response.Error)
	}
	awaitState(t, service, completed.ID, Completed)
	awaitExecutionReturned(t, service, completed.ID)
	// 두 번째 실행은 반환했지만 마감 근거를 게시하지 않았다. 목록에서 완료로 승격하면 안 된다.
	backend.run = func(context.Context, Execution, Publish) error { return nil }
	unknown := create(t, service, "unknown")
	if response := start(t, service, unknown, "start-unknown"); response.Error != "" {
		t.Fatal(response.Error)
	}
	awaitState(t, service, unknown.ID, Unknown)
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(t.Context())
	before := backend.calls.Load()
	page := runList(t, restarted, "owner", RunListRequest{})
	if page.Error != "" {
		t.Fatal(page.Error)
	}
	foundComplete, foundUnknown := false, false
	for _, run := range page.RunPage.Runs {
		switch run.ID {
		case completed.ID:
			foundComplete = run.State == Completed && run.ReportAvailable && run.TaskID == "42"
		case unknown.ID:
			foundUnknown = run.State == Unknown && run.ActionRequired && !run.ReportAvailable
		}
	}
	if !foundComplete || !foundUnknown || backend.calls.Load() != before {
		t.Fatal("재시작 목록이 상태·보고서를 잃거나 재실행")
	}
}
func TestRunListBoundsAndIPCValidation(t *testing.T) {
	service, _, _ := newFixture(t)
	for i := 0; i < 55; i++ {
		r := request(Create)
		r.IdempotencyKey = fmt.Sprintf("key-%d", i)
		r.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local", DisplayName: strings.Repeat("a", 160)}
		if response := call(t, service, r); response.Error != "" {
			t.Fatal(response.Error)
		}
	}
	page := runList(t, service, "owner", RunListRequest{Limit: MaxRunPageSize})
	raw, _ := json.Marshal(page.RunPage)
	if page.Error != "" || len(page.RunPage.Runs) > MaxRunPageSize || len(raw) > MaxRunPageBytes || !page.RunPage.More {
		t.Fatal("목록 개수·바이트 상한")
	}
	for _, input := range []RunListRequest{{Limit: -1}, {Limit: 51}, {Cursor: "not-a-cursor"}, {Cursor: strings.Repeat("a", 513)}} {
		r := request(ListRuns)
		r.Runs = &input
		if ValidateRequest(r) == nil {
			t.Fatal("잘못된 목록 입력 수용")
		}
	}
	r := request(ListRuns)
	r.Runs = &RunListRequest{}
	r.IdempotencyKey = "secret-key"
	if ValidateRequest(r) == nil {
		t.Fatal("멱등 키를 조회 권위로 사용")
	}
	r = request(Status)
	r.JobID = "job"
	r.Runs = &RunListRequest{}
	if ValidateRequest(r) == nil {
		t.Fatal("다른 명령에 목록 입력 혼입")
	}
	for _, mutate := range []func(*RunPage){func(p *RunPage) { p.Runs = append(p.Runs, p.Runs[0]) }, func(p *RunPage) { p.Runs[0].ApprovalStatus = "current" }, func(p *RunPage) { p.Runs[0].State = "unknown-new-state" }, func(p *RunPage) { p.Runs[0].Revision = 0 }} {
		response := runList(t, service, "owner", RunListRequest{Limit: 2})
		request := request(ListRuns)
		request.Runs = &RunListRequest{Limit: 2}
		mutate(response.RunPage)
		if ValidateResponse(request, response) == nil {
			t.Fatal("유효하지 않은 서비스 projection 수용")
		}
	}
}
func TestRunListAcrossProtectedUnixTransport(t *testing.T) {
	service, backend, root := newFixture(t)
	created := create(t, service, "private-create-key")
	socket, err := ListenUnix(t.Context(), root+"/runs.sock", service)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close(t.Context())
	client, err := NewUnixClient(root + "/runs.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	page := runList(t, client, "owner", RunListRequest{})
	if page.Error != "" || len(page.RunPage.Runs) != 1 || page.RunPage.Runs[0].ID != created.ID || backend.calls.Load() != 0 {
		t.Fatal("보호 IPC 목록 왕복 실패", page.Error)
	}
}

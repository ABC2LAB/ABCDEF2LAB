//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/report"
	"golang.org/x/crypto/bcrypt"
)

type followupFixture struct {
	jobs   map[string]*diagnosisprep.Job
	target diagnosisprep.ProductTarget
}

func (f *followupFixture) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	out := diagnosisprep.Response{Version: r.Version, RequestID: r.RequestID}
	switch {
	case r.Principal != "ARTEX":
		return out, diagnosisprep.ErrProtocol
	case r.Command == diagnosisprep.AuthorizeTask:
		out.Job = f.jobs[r.TaskID]
		ok := out.Job != nil
		out.Authorized = &ok
	case r.Command == diagnosisprep.ProductControl && r.Product.Operation == "target" && r.Product.TargetID == f.target.ID:
		out.Product = &diagnosisprep.ProductResponse{Target: &f.target}
	default:
		return out, diagnosisprep.ErrProtocol
	}
	return out, nil
}

func TestRemediationFollowupProduct(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	source := *stored.TaskID
	other := func() int64 {
		task, err := s.m.CreateTask("합성 보고서", "저장 기록만 사용", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		id, ok := verdictDetailID(task.ID)
		if !ok {
			t.Fatal(task.ID)
		}
		t.Cleanup(func() { s.m.pg.DeleteTask(id) })
		return id
	}
	current, general := other(), other()
	base, _, _ := reportFixtureService(t, source)
	proof, err := base.Call(t.Context(), diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "fixture", Command: diagnosisprep.AuthorizeTask, Principal: "ARTEX", TaskID: fmt.Sprint(source)})
	if err != nil || proof.Job == nil {
		t.Fatal(err)
	}
	endpoint := &followupFixture{jobs: map[string]*diagnosisprep.Job{}}
	for _, id := range []int64{source, current, general} {
		job := *proof.Job
		scope := *job.Scope
		scope.ScopeRef = "scope-" + scope.TargetRef
		scope.PathPrefixes = []string{"/safe", "/other"}
		scope.Methods = []string{"GET", "HEAD"}
		job.TaskID = fmt.Sprint(id)
		job.ID = "job-" + job.TaskID
		job.Scope = &scope
		job.Input.WorkflowMode = "ai_auto_v1"
		job.Input.ExclusionsReviewed = true
		job.Input.ExcludedPaths = "/logout"
		endpoint.jobs[job.TaskID] = &job
		endpoint.target = diagnosisprep.ProductTarget{ID: scope.TargetRef, Origin: scope.Origin, State: "approved", Revision: 1, PathPrefixes: scope.PathPrefixes, Methods: []string{"GET", "HEAD"}, Requests: scope.Budget.Requests, ExpiresAt: scope.ExpiresAt}
	}
	// 임시 로그인 비밀번호가 같은 DB를 쓰는 다른 시험에 남지 않게 한다.
	preserveRevisionWireAuth(t, s.m.pg)
	hash, err := bcrypt.GenerateFromPassword([]byte("followup-fixture"), bcrypt.MinCost)
	if err != nil || s.m.pg.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("인증 fixture")
	}
	keys, data := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if err := os.WriteFile(filepath.Join(keys, jwtKeyFilename), []byte("synthetic-remediation-followup-key"), 0600); err != nil {
		t.Fatal(err)
	}
	product, err := NewProductDiagnosisHandler(t.Context(), s.m.pg.DB, keys, data, endpoint, "http://127.0.0.1:1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	product.ServeHTTP(login, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"ABC2LAB","password":"followup-fixture"}`)))
	var session struct{ Token string }
	if json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatalf("제품 로그인 %d", login.Code)
	}
	url := fmt.Sprintf("/api/tasks/%d/remediation-followup", source)
	get := fmt.Sprintf("?current_task=%d", current)
	both := get + fmt.Sprintf("&general_task=%d", general)
	request := func(t *testing.T, method, suffix, token string, edit *followupEdit, want int) followupDraft {
		t.Helper()
		body := ""
		if edit != nil {
			b, e := json.Marshal(edit)
			if e != nil {
				t.Fatal(e)
			}
			body = string(b)
		}
		r := httptest.NewRequest(method, url+suffix, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		product.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d, want %d, %s", method, suffix, w.Code, want, w.Body.String())
		}
		var p followupDraft
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	call := func(method, suffix, token string, edit *followupEdit, want int) followupDraft {
		return request(t, method, suffix, token, edit, want)
	}
	o := e2proxy.HTTPObservation{Sequence: 1, AttemptRef: "test", Method: "GET", Endpoint: "/safe", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}
	add := func(task int64, run string, obs ...e2proxy.HTTPObservation) string {
		return fmt.Sprintf("candidate-%d", diagnosticServerRunCandidate(t, s, task, run, "completed", "XSS", obs...))
	}
	action := func(task int64, obs ...e2proxy.HTTPObservation) string {
		e := add(task, "run-job-"+fmt.Sprint(task), obs...)
		in, err := db.LoadDiagnosticRemediationSource(t.Context(), s.m.pg.DB, task, e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.m.pg.SaveDiagnosticRemediation(t.Context(), task, e, in.SourceDigest, nil); err != nil {
			t.Fatal(err)
		}
		return e
	}
	a, b := action(source, o), action(source)
	foreign := action(current)
	token := session.Token
	p := call("GET", "", "", nil, 401)
	p = call("GET", "", token, nil, 200)
	if p.Source != fmt.Sprint(source) || len(p.Items) != 2 || p.Exclusions == nil || *p.Exclusions != "/logout" || p.Scope.State != "review_required" || p.General.ResultState != "not_linked" {
		t.Fatalf("원래 보고서 초안 %+v", p)
	}
	call("GET", "?entry="+foreign, token, nil, 404)
	p = call("GET", "?entry="+b, token, nil, 200)
	if len(p.Items) != 1 || p.Items[0].Entry != b || p.Items[0].Result != "inconclusive" {
		t.Fatalf("일부 선택 %+v", p.Items)
	}
	p = call("GET", both, token, nil, 200)
	if p.General.ResultState != "available" || len(p.General.Findings) != 0 {
		t.Fatal("완료 후 0건")
	}
	proofEntry := add(current, "run-job-"+fmt.Sprint(current), o)
	c := add(general, "run-job-"+fmt.Sprint(general))
	add(current, "another-run", o)
	p = call("GET", both+"&entry="+a, token, nil, 200)
	if p.Items[0].Entry != a || len(p.Items[0].Criteria) == 0 || p.Items[0].Digest == "" || p.Items[0].Result != "inconclusive" || p.Current.ResultState != "partial" || len(p.General.Findings) != 1 || p.General.Findings[0].Entry != c {
		t.Fatal("근거 없이 확정되거나 회차가 섞임", p)
	}
	findStep := func(t *testing.T, p followupDraft, entry string) followupEvidence {
		t.Helper()
		for _, x := range p.Current.Findings {
			if x.Entry == entry && len(x.Steps) == 1 {
				return x.Steps[0]
			}
		}
		t.Fatal("저장된 감사 근거 없음", entry)
		return followupEvidence{}
	}
	step := findStep(t, p, proofEntry)
	original := p.Items[0]
	if !step.Reviewable || len(original.OriginalRequests) != 1 ||
		original.OriginalRequests[0] != (followupRequest{Method: "GET", Path: "/safe", VulnClass: "xss"}) {
		t.Fatal("원래 조치의 비교 근거 누락", original.OriginalRequests)
	}
	rev := int64(0)
	edit := followupEdit{Entry: a, Record: original.Record, OriginalDigest: original.Digest, Outcome: "fixed", Comparable: true, CriteriaMet: true, ExpectedRevision: &rev, EvidenceEntry: proofEntry, StepID: step.ID, BindingDigest: step.Digest}
	bad := edit
	bad.CriteriaMet = false
	call("PUT", get, token, &bad, 400)
	bad = edit
	bad.BindingDigest = strings.Repeat("0", 64)
	call("PUT", get, token, &bad, 409)

	for _, tc := range []struct {
		name, path, method, class string
		reviewable                bool
	}{
		{"경로 불일치", "/other", "GET", "XSS", true},
		{"메서드 불일치", "/safe", "HEAD", "XSS", true},
		{"유형 불일치", "/safe", "GET", "IDOR", true},
		{"가린 경로", "/safe/{resource}", "GET", "XSS", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := o
			observation.Endpoint, observation.Method = tc.path, tc.method
			id := diagnosticServerRunCandidate(t, s, current, "run-job-"+fmt.Sprint(current), "completed", tc.class, observation)
			t.Cleanup(func() {
				if _, err := s.m.pg.Exec("DELETE FROM exploration_nodes WHERE id=$1", id); err != nil {
					t.Error(err)
				}
			})
			entry := fmt.Sprintf("candidate-%d", id)
			candidate := findStep(t, request(t, "GET", get, token, nil, 200), entry)
			if candidate.Reviewable != tc.reviewable {
				t.Fatal("현재 회차 안에서의 관찰 적격 상태", candidate)
			}
			bad := edit
			bad.EvidenceEntry, bad.StepID, bad.BindingDigest = entry, candidate.ID, candidate.Digest
			request(t, "PUT", get, token, &bad, 409)
			bad.Outcome, bad.CriteriaMet = "reproduced", false
			request(t, "PUT", get, token, &bad, 409)
		})
	}
	withoutOriginal := call("GET", get+"&entry="+b, token, nil, 200).Items[0]
	if len(withoutOriginal.OriginalRequests) != 0 {
		t.Fatal("관찰 없는 원래 조치에 비교 근거가 생김")
	}
	bad = edit
	bad.Entry, bad.Record, bad.OriginalDigest = withoutOriginal.Entry, withoutOriginal.Record, withoutOriginal.Digest
	call("PUT", get, token, &bad, 409)

	incomplete := o
	incomplete.AuditComplete = false
	incompleteEntry := add(current, "run-job-"+fmt.Sprint(current), incomplete)
	p = call("GET", get, token, nil, 200)
	for _, f := range p.Current.Findings {
		if f.Entry == incompleteEntry && len(f.Steps) != 0 {
			t.Fatal("감사 미완료 근거 선택 가능")
		}
	}
	endpoint.target.PathPrefixes = []string{"/other"}
	p = call("GET", get, token, nil, 200)
	if p.Scope.State != "changed" {
		t.Fatal("승인 범위 변경 누락")
	}
	call("PUT", get, token, &edit, 409)
	endpoint.target.PathPrefixes = []string{"/safe", "/other"}
	currentJob := endpoint.jobs[fmt.Sprint(current)]
	currentJob.Input.ExcludedPaths = "/safe"
	call("PUT", get, token, &edit, 409)
	currentJob.Input.ExcludedPaths = "/logout"
	call("PUT", get, token, &edit, 200)
	p = call("PUT", get, token, &edit, 200)
	lookup := func(p followupDraft, id string) followupItem {
		t.Helper()
		for _, item := range p.Items {
			if item.Entry == id {
				return item
			}
		}
		t.Fatal(id)
		return followupItem{}
	}
	got := lookup(p, a)
	if got.Result != "fixed" || got.Review == nil || got.Review.Revision != 1 || got.Review.Reviewer != "ARTEX" || lookup(p, b).Result != "inconclusive" {
		t.Fatal("원래 조치별 판정", p.Items)
	}
	// 현재 승인이 만료되면 저장된 판본을 보존한 채 이번 결과를 확인 불가로 표시한다.
	expires := endpoint.target.ExpiresAt
	endpoint.target.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	p = call("GET", get, token, nil, 200)
	expired := lookup(p, a)
	if p.Scope.State != "expired" || expired.Result != "inconclusive" || expired.Reason != "review_evidence_changed" ||
		expired.Review == nil || expired.Review.Revision != 1 || expired.Review.EvidenceEntry != "" {
		t.Fatal("승인 만료 뒤 기존 판정이 현재 결과에 남음", expired)
	}
	call("PUT", get, token, &edit, 409)
	endpoint.target.ExpiresAt = expires
	p = call("GET", both+"&entry="+b, token, nil, 200)
	refs := map[followupReference]int{}
	for _, ref := range p.Additional {
		refs[ref]++
	}
	if refs[followupReference{fmt.Sprint(general), c}] != 1 || refs[followupReference{fmt.Sprint(current), proofEntry}] != 0 {
		t.Fatal("추가 관찰 분리 실패", refs)
	}
	p = call("GET", get+"&general_task="+fmt.Sprint(current), token, nil, 200)
	if len(p.Additional) != 2 || p.Additional[0].Entry != foreign || p.Additional[1].Entry != incompleteEntry {
		t.Fatal("중복 회차", p.Additional)
	}
	bad = edit
	bad.Outcome = "reproduced"
	bad.CriteriaMet = false
	call("PUT", get, token, &bad, 409)
	bad.ExpectedRevision = new(int64)
	*bad.ExpectedRevision = 1
	p = call("PUT", get, token, &bad, 200)
	if lookup(p, a).Review.Revision != 2 || lookup(p, a).Result != "reproduced" {
		t.Fatal("판정 정정")
	}
	second := lookup(p, b)
	unchecked := followupEdit{Entry: b, Record: second.Record, OriginalDigest: second.Digest, Outcome: "inconclusive", Reason: "conditions_changed", ExpectedRevision: &rev}
	p = call("PUT", get, token, &unchecked, 200)
	if lookup(p, b).Reason != "conditions_changed" {
		t.Fatal("확인 불가 사유 누락")
	}
	// 관찰이 사라지거나 원래 조치의 근거가 삭제돼도 조치 자체와 확인 불가 사유를 남긴다.
	proofID, _ := strings.CutPrefix(proofEntry, "candidate-")
	if _, err := s.m.pg.Exec("DELETE FROM exploration_nodes WHERE id=$1", proofID); err != nil {
		t.Fatal(err)
	}
	originalID, _ := strings.CutPrefix(b, "candidate-")
	if _, err := s.m.pg.Exec("DELETE FROM exploration_nodes WHERE id=$1", originalID); err != nil {
		t.Fatal(err)
	}
	p = call("GET", get, token, nil, 200)
	if lookup(p, a).Result != "inconclusive" || lookup(p, a).Reason != "review_evidence_changed" || lookup(p, b).Result != "inconclusive" || lookup(p, b).Digest != "" {
		t.Fatal("사라진 근거의 판정", p.Items)
	}
	job := endpoint.jobs[fmt.Sprint(general)]
	job.Scope.TargetRef = "another-target"
	call("GET", both, token, nil, 409)
	delete(endpoint.jobs, fmt.Sprint(general))
	call("GET", both, token, nil, 403)
	delete(endpoint.jobs, fmt.Sprint(current))
	call("PUT", get, token, &edit, 403)
}

func TestRemediationFollowupIncompleteResults(t *testing.T) {
	for _, tc := range []struct {
		state diagnosisprep.State
		want  string
	}{{diagnosisprep.Running, "pending"}, {diagnosisprep.Cancelled, "unavailable"}, {diagnosisprep.Failed, "unavailable"}} {
		j := &diagnosisprep.Job{State: tc.state, TaskID: "22"}
		if got := followupObservations(j, report.DiagnosticReport{}, followupScope{}); got.ResultState != tc.want || got.Ready {
			t.Fatal(got)
		}
	}
	for _, q := range []string{"?entry=", "?entry=candidate-1&entry=candidate-1", "?current_task=1", "?general_task=03", "?unknown=1", "?entry=candidate-1%ZZ"} {
		if _, ok := parseFollowup(httptest.NewRequest("GET", "/api/tasks/1/remediation-followup"+q, nil)); ok {
			t.Fatal(q)
		}
	}
}

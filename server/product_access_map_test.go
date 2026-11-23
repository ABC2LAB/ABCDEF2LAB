package server

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
)

// 제품 접근통제 지도 시험(docs/contracts/product-access-map.md §4). 셀 상태는 §2.3 표의 행마다 하나, 지도를 만들 수 없는
// 경우는 §2.4 표 전부다. 변조는 소유 시험 연결의 transaction 안에서만 재현하고 반드시 롤백한다.

func productMapTypes() []diagnosisprep.ProductDeclaredType {
	return []diagnosisprep.ProductDeclaredType{
		{Type: "document", Sharing: "owner_only", Confirmable: true},
		{Type: "note", Sharing: "owner_only", Confirmable: true},
		{Type: "post", Sharing: "shared"},
	}
}

func productMapSource() *diagnosisprep.ProductPolicySource {
	s := matchedProductSource()
	s.Types = productMapTypes()
	return s
}

func productMapCompleted() *diagnosisprep.ProductConfirmationState {
	return &diagnosisprep.ProductConfirmationState{Present: true, Status: "completed",
		Skipped: []diagnosisprep.ProductSkippedType{{ResourceType: "post", Reason: "not_owner_only"}}}
}

func strPtr(s string) *string { return &s }

func productMapItem(value oracle.VerdictValue, policy, identity, data oracle.CheckState) productVerdictListItem {
	v := string(value)
	return productVerdictListItem{VerdictID: strPtr("901"), RecordStatus: productVerdictVerified, ResourceType: strPtr("note"), OracleValue: &v,
		Checks:            &productVerdictListChecks{Policy: string(policy), Identity: string(identity), Data: string(data)},
		OracleReasonCodes: []string{string(oracle.ReasonPolicyDenyResolved)}, ProductReasonCodes: []string{}, CandidateRefs: []string{"candidate-1"},
		ObservedAt: strPtr("2026-11-18T09:00:00.000000Z"), EvidenceSeal: &productEvidenceSeal{Status: productEvidenceSealUnconfirmed}}
}

func cellError(c productAccessMapCell) string {
	if c.ProjectionError == nil {
		return ""
	}
	return *c.ProjectionError
}

// §2.3 B 열 × 시험 대상 종류: 위에서부터 처음 맞는 줄. 판정 칸은 확정·의심·판단불가일 때만 싣는다.
func TestProductAccessMapCrossCell(t *testing.T) {
	yes, no := oracle.StateYes, oracle.StateNo
	for name, tc := range map[string]struct {
		items         []productVerdictListItem
		hidden        int
		status        string
		state, err    string
		verdictFields bool
	}{
		"ambiguous":              {items: []productVerdictListItem{productMapItem(oracle.Confirmed, yes, yes, yes), productMapItem(oracle.Confirmed, yes, yes, yes)}, status: "completed", state: "unknown", err: "selection_ambiguous"},
		"confirmed":              {items: []productVerdictListItem{productMapItem(oracle.Confirmed, yes, yes, yes)}, status: "completed", state: "confirmed", verdictFields: true},
		"confirmed_check_no":     {items: []productVerdictListItem{productMapItem(oracle.Confirmed, yes, no, yes)}, status: "completed", state: "unknown", err: "record_invalid"},
		"confirmed_policy_no":    {items: []productVerdictListItem{productMapItem(oracle.Confirmed, no, yes, yes)}, status: "completed", state: "unknown", err: "record_invalid"},
		"confirmed_data_unknown": {items: []productVerdictListItem{productMapItem(oracle.Confirmed, yes, yes, oracle.StateUnknown)}, status: "completed", state: "unknown", err: "record_invalid"},
		"suspected":              {items: []productVerdictListItem{productMapItem(oracle.Suspected, yes, yes, oracle.StateUnknown)}, status: "completed", state: "suspected", verdictFields: true},
		"undecidable":            {items: []productVerdictListItem{productMapItem(oracle.Undecidable, yes, oracle.StateUnknown, oracle.StateUnknown)}, status: "completed", state: "unknown", verdictFields: true},
		"allowed":                {items: []productVerdictListItem{productMapItem(oracle.Allowed, yes, yes, yes)}, status: "completed", state: "unknown", err: "record_invalid"},
		"hidden":                 {hidden: 1, status: "completed", state: "unknown", err: "record_hidden"},
		"completed_missing":      {status: "completed", state: "unknown", err: "record_missing_after_attempt"},
		"interrupted":            {status: "interrupted", state: "unknown", err: "observation_unavailable"},
		"not_started":            {status: "", state: "unknown", err: "observation_unavailable"},
		"start_refused":          {status: "start_refused", state: "unknown", err: "observation_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			c := productMapCrossCell("tester-b", "note", tc.items, tc.hidden, tc.status)
			if c.State != tc.state || cellError(c) != tc.err || c.Applicable == nil || !*c.Applicable || c.NABasis != nil || c.Principal != "tester-b" {
				t.Fatalf("칸: %+v (오류 %q)", c, cellError(c))
			}
			if (c.VerdictID != nil) != tc.verdictFields || (c.OracleValue != nil) != tc.verdictFields || (c.Checks != nil) != tc.verdictFields ||
				(c.EvidenceSeal != nil) != tc.verdictFields || (c.RecordStatus != nil) != tc.verdictFields {
				t.Fatalf("판정 칸 유무: %+v", c)
			}
			if !tc.verdictFields && (len(c.OracleReasonCodes) != 0 || len(c.CandidateRefs) != 0 || c.OracleReasonCodes == nil) {
				t.Fatalf("판정 없는 칸의 배열: %+v", c)
			}
			// 판단불가 판정과 관측 누락을 합치지 않는다.
			if name == "undecidable" && (c.OracleValue == nil || *c.OracleValue != "undecidable" || c.ProjectionError != nil) {
				t.Fatalf("판단불가: %+v", c)
			}
		})
	}
}

// §2.3 B 열 × 공유 종류와 A 열.
func TestProductAccessMapSharedAndOwnerCells(t *testing.T) {
	completed := productMapSkipCheck{completed: true, any: true, reasons: map[string]string{"post": "not_owner_only"}}
	for name, tc := range map[string]struct {
		items []productVerdictListItem
		check productMapSkipCheck
		state string
		err   string
		hasNA bool
	}{
		"declared_shared":       {check: completed, state: "na", hasNA: true},
		"not_completed_no_skip": {check: productMapSkipCheck{}, state: "na", hasNA: true},
		"verdict_on_shared":     {items: []productVerdictListItem{productMapItem(oracle.Confirmed, oracle.StateYes, oracle.StateYes, oracle.StateYes)}, check: completed, state: "unknown", err: "record_invalid"},
		"completed_skip_absent": {check: productMapSkipCheck{completed: true, reasons: map[string]string{}}, state: "unknown", err: "na_evidence_mismatch"},
		"other_reason":          {check: productMapSkipCheck{any: true, reasons: map[string]string{"post": "something_else"}}, state: "unknown", err: "na_evidence_mismatch"},
		"skip_listed_elsewhere": {check: productMapSkipCheck{any: true, reasons: map[string]string{"other": "not_owner_only"}}, state: "unknown", err: "na_evidence_mismatch"},
	} {
		t.Run(name, func(t *testing.T) {
			c := productMapSharedCell("tester-b", "post", tc.items, tc.check)
			if c.State != tc.state || cellError(c) != tc.err || c.Applicable == nil || *c.Applicable || (c.NABasis != nil) != tc.hasNA || c.VerdictID != nil || c.OracleValue != nil {
				t.Fatalf("칸: %+v (오류 %q)", c, cellError(c))
			}
			if tc.hasNA && *c.NABasis != "declared_shared" {
				t.Fatalf("근거: %s", *c.NABasis)
			}
		})
	}
	owner := productMapOwnerCell("tester-a", "note")
	if owner.State != "owner_control" || owner.Applicable != nil || owner.VerdictID != nil || owner.OracleValue != nil || owner.Checks != nil ||
		owner.ProjectionError != nil || owner.NABasis != nil || owner.OracleReasonCodes == nil {
		t.Fatalf("A 열: %+v", owner)
	}
}

// §2.4 지도를 만들 수 없을 때와 결과·동결본 대조 실패. 판정 행이 없어도 축과 칸은 만든다(빠진 칸 없음).
func TestProductAccessMapAssemble(t *testing.T) {
	ctx := func(source *diagnosisprep.ProductPolicySource, conf *diagnosisprep.ProductConfirmationState) productVerdictContext {
		return productListContext(source, conf)
	}
	noRoles := productMapSource()
	noRoles.OwnerAccountLabel, noRoles.CrossAccountLabel = "", ""
	ownerOnlyNotConfirmable := productMapSource()
	ownerOnlyNotConfirmable.Types[1].Confirmable = false
	sharedConfirmable := productMapSource()
	sharedConfirmable.Types[2].Confirmable = true
	skip := func(s ...diagnosisprep.ProductSkippedType) *diagnosisprep.ProductConfirmationState {
		return &diagnosisprep.ProductConfirmationState{Present: true, Status: "completed", Skipped: s}
	}
	for name, tc := range map[string]struct {
		got    productVerdictContext
		status int
		code   string
	}{
		"source_unavailable":         {got: ctx(nil, nil), status: 503, code: productAccessMapUnavailable},
		"no_declaration":             {got: ctx(&diagnosisprep.ProductPolicySource{}, nil), status: 409, code: productAccessMapNoDeclaration},
		"roles_undeclared":           {got: ctx(noRoles, nil), status: 409, code: productAccessMapRolesUndeclared},
		"owner_only_not_confirmable": {got: ctx(ownerOnlyNotConfirmable, nil), status: 503, code: productAccessMapUnavailable},
		"shared_confirmable":         {got: ctx(sharedConfirmable, nil), status: 503, code: productAccessMapUnavailable},
		"confirmation_shape":         {got: ctx(productMapSource(), &diagnosisprep.ProductConfirmationState{Present: true, Status: "bogus"}), status: 503, code: productAccessMapUnavailable},
		"skipped_undeclared":         {got: ctx(productMapSource(), skip(diagnosisprep.ProductSkippedType{ResourceType: "image", Reason: "not_owner_only"})), status: 422, code: productAccessMapIntegrityFailed},
		"skipped_confirmable":        {got: ctx(productMapSource(), skip(diagnosisprep.ProductSkippedType{ResourceType: "note", Reason: "not_owner_only"})), status: 422, code: productAccessMapIntegrityFailed},
		"skipped_duplicate": {got: ctx(productMapSource(), skip(diagnosisprep.ProductSkippedType{ResourceType: "post", Reason: "not_owner_only"},
			diagnosisprep.ProductSkippedType{ResourceType: "post", Reason: "not_owner_only"})), status: 503, code: productAccessMapUnavailable}, // IPC 모양 검사가 먼저 거부
		"ok": {got: ctx(productMapSource(), productMapCompleted()), status: 200},
	} {
		t.Run(name, func(t *testing.T) {
			env, status, code := assembleProductAccessMap(42, 7, nil, tc.got, nil)
			if status != tc.status || code != tc.code {
				t.Fatalf("상태 %d %q", status, code)
			}
			if status != http.StatusOK {
				if env.Cells != nil || env.Principals != nil {
					t.Fatalf("실패인데 격자를 만듦: %+v", env)
				}
				return
			}
			want := []struct{ principal, resource, state, err string }{
				{"tester-a", "document", "owner_control", ""}, {"tester-b", "document", "unknown", "record_missing_after_attempt"},
				{"tester-a", "note", "owner_control", ""}, {"tester-b", "note", "unknown", "record_missing_after_attempt"},
				{"tester-a", "post", "owner_control", ""}, {"tester-b", "post", "na", ""},
			}
			if len(env.Cells) != len(want) || env.Format != productAccessMapFormat || env.SourceKind != "product" || env.Axis != "horizontal" ||
				env.TaskID != "42" || env.PolicySource.Ref != "declaration-"+productListJob || env.HiddenRecords != 0 ||
				len(env.Principals) != 2 || env.Principals[0] != (productAccessMapPrincipal{"tester-a", "owner"}) || env.Principals[1] != (productAccessMapPrincipal{"tester-b", "cross"}) ||
				len(env.Resources) != 3 || env.Resources[2] != (productAccessMapResource{"post", "shared", false}) ||
				env.Confirmation == nil || env.Confirmation.Status != "completed" || env.Confirmation.Reason != nil {
				t.Fatalf("응답: %+v", env)
			}
			for i, w := range want {
				c := env.Cells[i]
				if c.Principal != w.principal || c.ResourceType != w.resource || c.State != w.state || cellError(c) != w.err {
					t.Fatalf("칸 %d: %+v (오류 %q)", i, c, cellError(c))
				}
			}
		})
	}
	// 결과 파일이 없으면 confirmation은 null이고, 시험 대상 칸은 관측 누락이다(진행 중·확정 단계 전).
	env, status, _ := assembleProductAccessMap(42, 7, nil, productListContext(productMapSource(), &diagnosisprep.ProductConfirmationState{}), nil)
	if status != http.StatusOK || env.Confirmation != nil || cellError(env.Cells[1]) != "observation_unavailable" || env.Cells[5].State != "na" {
		t.Fatalf("확정 단계 전: %d %+v", status, env)
	}
}

// productMapInTx는 한 transaction에서 변조를 적용하고 지도를 읽은 뒤 롤백한다.
func productMapInTx(t *testing.T, task int64, pgDB *sql.DB, got productVerdictContext, forge func(*sql.Tx)) (productAccessMapEnvelope, int) {
	t.Helper()
	tx, err := pgDB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if forge != nil {
		forge(tx)
	}
	exploration, rows, status, _ := loadProductVerdictRows(t.Context(), tx, task)
	if status != http.StatusOK {
		t.Fatalf("행 읽기 %d", status)
	}
	seals, err := loadProductSealChecks(t.Context(), tx, task, productVerdictRunRefs(rows))
	if err != nil {
		t.Fatal(err)
	}
	env, status, _ := assembleProductAccessMap(task, exploration, rows, got, seals)
	return env, status
}

func findProductMapCell(t *testing.T, env productAccessMapEnvelope, principal, resource string) productAccessMapCell {
	t.Helper()
	for _, c := range env.Cells {
		if c.Principal == principal && c.ResourceType == resource {
			return c
		}
	}
	t.Fatalf("칸 없음 %s × %s", principal, resource)
	return productAccessMapCell{}
}

// 저장된 판정으로 칸을 채운다. 진위 실패 행은 종류를 믿을 수 없어 세기만 하고, 그 뒤 판정 없는 시험 대상 칸은 "없음"이 아니라
// record_hidden이다. 동결본에 없는 종류의 판정과 공유 종류의 판정은 칸을 만들지 않는다. 같은 작업의 평가 판정은 섞이지 않는다.
func TestProductAccessMapStoredVerdicts(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	f := diagnosticE3FixtureFor(t, pg, oracle.Confirmed, true)
	if _, err := pg.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil {
		t.Fatal(err)
	}
	task := f.taskID
	doc := storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	got := productListContext(productMapSource(), productMapCompleted())

	env, status := productMapInTx(t, task, pg.DB, got, nil)
	if status != http.StatusOK || env.HiddenRecords != 0 {
		t.Fatalf("지도: %d %+v", status, env)
	}
	c := findProductMapCell(t, env, "tester-b", "document")
	if c.State != "confirmed" || c.VerdictID == nil || *c.VerdictID != strconv.FormatInt(doc.VerdictID, 10) || c.RecordStatus == nil || *c.RecordStatus != "verified" ||
		c.Checks == nil || c.Checks.Data != "yes" || len(c.CandidateRefs) != 1 || c.EvidenceSeal == nil {
		t.Fatalf("확정 칸: %+v", c)
	}
	if n := findProductMapCell(t, env, "tester-b", "note"); cellError(n) != "record_missing_after_attempt" {
		t.Fatalf("판정 없는 칸: %+v", n)
	}
	if p := findProductMapCell(t, env, "tester-b", "post"); p.State != "na" {
		t.Fatalf("공유 칸: %+v", p)
	}
	raw, _ := json.Marshal(env)
	for _, leak := range []string{"resource_ref", strings.Repeat("b", 64), "synthetic-model", "product-run-list", "journal-synthetic", "generation-synthetic"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("응답에 %s가 나감: %s", leak, raw)
		}
	}

	// 진위 실패(다시 서명한 위조): 그 행은 숨기고, 판정 없는 시험 대상 칸은 숨긴 행일 수 있다.
	env, status = productMapInTx(t, task, pg.DB, got, func(tx *sql.Tx) {
		forgeProductRow(t, tx, doc.IssuanceID, func(p map[string]any) { p["case_ref"] = "e3-product:forged" }, nil)
	})
	if status != http.StatusOK || env.HiddenRecords != 1 || strings.Contains(productMapJSON(t, env), "forged") {
		t.Fatalf("위조: %d %+v", status, env)
	}
	for _, resource := range []string{"document", "note"} {
		if c := findProductMapCell(t, env, "tester-b", resource); cellError(c) != "record_hidden" || c.VerdictID != nil {
			t.Fatalf("숨김 뒤 %s: %+v", resource, c)
		}
	}

	// 동결본에 없는 종류의 판정(진위는 통과)은 칸을 만들지 않고 숨김으로 센다.
	storeProductVerdictKind(t, pg, task, "image", "target-revision-1", oracle.Confirmed)
	env, status = productMapInTx(t, task, pg.DB, got, nil)
	if status != http.StatusOK || env.HiddenRecords != 1 || len(env.Cells) != 6 || strings.Contains(productMapJSON(t, env), "image") {
		t.Fatalf("선언 밖 종류: %d %+v", status, env)
	}

	// 공유 종류에 판정이 있으면 모순이다.
	storeProductVerdictKind(t, pg, task, "post", "target-revision-1", oracle.Undecidable)
	env, _ = productMapInTx(t, task, pg.DB, got, nil)
	if c := findProductMapCell(t, env, "tester-b", "post"); cellError(c) != "record_invalid" || c.NABasis != nil {
		t.Fatalf("공유 종류 판정: %+v", c)
	}

	// 같은 회차·종류의 두 번째 판정은 저장이 거부한다(product_verdict_save_conflict). 그래서 selection_ambiguous는
	// TestProductAccessMapCrossCell에서 투영 함수로만 본다.
}

func productMapJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 지도 읽기는 쓰기를 하지 않는다: 읽기 전용 transaction 안에서만 돈다.
func TestProductAccessMapReadOnly(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	endpoint := &verdictDetailTestEndpoint{source: productMapSource(), confirmation: productMapCompleted()}
	reader := productPolicySourceReader{endpoint: endpoint, principal: "ARTEX", jobID: productListJob}
	var before, after int
	count := func() int {
		var n int
		if err := pg.QueryRow(`SELECT (SELECT count(*) FROM e3_verdict_issuances)+(SELECT count(*) FROM exploration_nodes)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before = count()
	env, status, _ := readProductAccessMap(t.Context(), pg.DB, task, productListJob, reader)
	after = count()
	if status != http.StatusOK || before != after || len(endpoint.calls) != 1 || findProductMapCell(t, env, "tester-b", "document").State != "confirmed" {
		t.Fatalf("읽기: %d %d→%d IPC %d", status, before, after, len(endpoint.calls))
	}
}

func TestProductAccessMapRequestShape(t *testing.T) {
	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/api/tasks/42/product-access-map", http.StatusOK},
		{http.MethodGet, "/api/tasks/42/product-access-map?x=1", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/42/product-access-map?", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/042/product-access-map", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/0/product-access-map", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/42/product-access-map/1", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/42/x/product-access-map", http.StatusBadRequest},
		{http.MethodPost, "/api/tasks/42/product-access-map", http.StatusMethodNotAllowed},
		{http.MethodHead, "/api/tasks/42/product-access-map", http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		if !ProductAccessMapPath(r) {
			t.Fatalf("%s %s: 지도 경로로 분기하지 않음", tc.method, tc.target)
		}
		if _, status := productAccessMapRequest(r); status != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.target, status)
		}
	}
	// 평가 지도 경로는 이 처리기로 오지 않고, 평가 지도 분기도 제품 경로를 받지 않는다.
	if ProductAccessMapPath(httptest.NewRequest(http.MethodGet, "/api/tasks/42/access-map", nil)) {
		t.Fatal("평가 지도 경로를 제품 지도로 봄")
	}
	if _, ok := accessMapRequestTask(httptest.NewRequest(http.MethodGet, "/api/tasks/42/product-access-map", nil)); ok {
		t.Fatal("평가 지도 분기가 제품 지도 경로를 받음")
	}
}

// 인증 → 작업 소유(AuthorizeTask) → 저장 판정 읽기 → IPC 한 번 순서다. AuthorizeTask 실패는 작업이 있든 없든 같은 403이다.
func TestProductAccessMapRoute(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	taskRef := strconv.FormatInt(task, 10)
	path := "/api/tasks/" + taskRef + "/product-access-map"
	d := productListDeclaration()
	job := &diagnosisprep.Job{ID: productListJob, TaskID: taskRef}
	for name, tc := range map[string]struct {
		method, target string
		authErr        error
		owner          string
		job            *diagnosisprep.Job
		jobErr         error
		noEndpoint     bool
		ipcErr         error
		ipcReason      string
		source         *diagnosisprep.ProductPolicySource
		status         int
		code           string
		calls          int
	}{
		"ok":               {owner: "ARTEX", job: job, status: 200, calls: 1},
		"unauthenticated":  {authErr: errors.New("인증이 필요합니다"), status: 401, code: verdictDetailAuthRequired},
		"auth_unavailable": {authErr: errManagementAuthenticationUnavailable, status: 503, code: productAccessMapUnavailable},
		"not_owned":        {owner: "ARTEX", status: 403, code: productAccessMapForbidden},
		"job_task_differs": {owner: "ARTEX", job: &diagnosisprep.Job{ID: productListJob, TaskID: "999"}, status: 409, code: productAccessMapBindingUnavailable},
		"job_without_id":   {owner: "ARTEX", job: &diagnosisprep.Job{TaskID: taskRef}, status: 409, code: productAccessMapBindingUnavailable},
		"job_read_error":   {owner: "ARTEX", jobErr: errors.New("prep down"), status: 503, code: productAccessMapUnavailable},
		"not_configured":   {owner: "ARTEX", noEndpoint: true, status: 503, code: productAccessMapUnavailable},
		"ipc_error":        {owner: "ARTEX", job: job, ipcErr: errors.New("closed"), status: 503, code: productAccessMapUnavailable, calls: 1},
		"ipc_not_found":    {owner: "ARTEX", job: job, ipcReason: "not_found", status: 503, code: productAccessMapUnavailable, calls: 1},
		"no_declaration":   {owner: "ARTEX", job: job, source: &diagnosisprep.ProductPolicySource{}, status: 409, code: productAccessMapNoDeclaration, calls: 1},
		"roles_undeclared": {owner: "ARTEX", job: job, source: &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(d[:]), Types: productMapTypes()},
			status: 409, code: productAccessMapRolesUndeclared, calls: 1},
		"query": {owner: "ARTEX", target: path + "?task=1", status: 400, code: productAccessMapInvalidRequest},
		"post":  {method: http.MethodPost, owner: "ARTEX", status: 405, code: productAccessMapInvalidRequest},
	} {
		t.Run(name, func(t *testing.T) {
			source := productMapSource()
			if tc.source != nil {
				source = tc.source
			}
			endpoint := &verdictDetailTestEndpoint{source: source, confirmation: productMapCompleted(), reason: tc.ipcReason, err: tc.ipcErr}
			if tc.ipcReason != "" {
				endpoint.source, endpoint.confirmation = nil, nil
			}
			bridge := &diagnosisManagementBridge{
				authenticate:      func(*http.Request) (string, error) { return tc.owner, tc.authErr },
				readPool:          pg.DB,
				candidateJob:      func(context.Context, string, string) (*diagnosisprep.Job, error) { return tc.job, tc.jobErr },
				candidateEndpoint: endpoint,
			}
			if tc.noEndpoint {
				bridge.candidateEndpoint = nil
			}
			method, target := http.MethodGet, path
			if tc.method != "" {
				method = tc.method
			}
			if tc.target != "" {
				target = tc.target
			}
			w := httptest.NewRecorder()
			bridge.serveProductAccessMap(w, httptest.NewRequest(method, target, nil))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || len(endpoint.calls) != tc.calls {
				t.Fatalf("상태 %d·IPC %d: %s", w.Code, len(endpoint.calls), w.Body.String())
			}
			if tc.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodGet {
				t.Fatal("405에 Allow가 없음")
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.code != "" && (body["error"] != tc.code || len(body) != 1) {
				t.Fatalf("오류 코드: %v", body)
			}
			if tc.status == http.StatusOK {
				cells, _ := body["cells"].([]any)
				if body["format"] != productAccessMapFormat || body["source_kind"] != "product" || len(cells) != 6 {
					t.Fatalf("응답: %s", w.Body.String())
				}
			}
		})
	}
}

// 판정 행이 상한(선언 종류 상한 16)을 넘으면 잘라 그리지 않고 응답 전체가 422다(§2.4). IPC는 부르지 않는다.
func TestProductAccessMapRowCap(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	for i := 0; i <= maxProductVerdictListItems; i++ {
		storeProductVerdictKind(t, pg, task, "kind_"+strconv.Itoa(i), "target-revision-1", oracle.Undecidable)
	}
	endpoint := &verdictDetailTestEndpoint{source: productMapSource(), confirmation: productMapCompleted()}
	reader := productPolicySourceReader{endpoint: endpoint, principal: "ARTEX", jobID: productListJob}
	env, status, code := readProductAccessMap(t.Context(), pg.DB, task, productListJob, reader)
	if status != http.StatusUnprocessableEntity || code != productAccessMapIntegrityFailed || env.Cells != nil || len(endpoint.calls) != 0 {
		t.Fatalf("행 상한: %d %q IPC %d", status, code, len(endpoint.calls))
	}
}

package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/report"
)

// 보고서의 확정 단계 판정 구역(#875 발견 1, 2026-11-23 사용자 결정): 중계가 진위 확인을 거친 목록을 내부 헤더로 넘기고,
// 보고서 쪽은 저장 판정과 다시 대조한다.

func intPtr(n int) *int { return &n }

func reportWireOf(t *testing.T, value string) reportProductVerdictsWire {
	t.Helper()
	wire, ok := decodeReportProductVerdicts([]string{value})
	if !ok {
		t.Fatal("헤더를 다시 읽지 못함")
	}
	return wire
}

func encodeWire(t *testing.T, wire reportProductVerdictsWire) string {
	t.Helper()
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// 진위 확인을 통과한 행만 값을 싣고, 원본 미확인·진위 실패 행은 값 없이 싣는다. 크기 상한을 넘으면 뒤 행부터 빼고 전체 수는 남긴다.
func TestEncodeReportProductVerdicts(t *testing.T) {
	observed := "2026-11-23T10:38:07.722583Z"
	verified := productVerdictListItem{VerdictID: strPtr("1"), RecordStatus: productVerdictVerified, ResourceType: strPtr("book"), OracleValue: strPtr("confirmed"),
		OracleReasonCodes: []string{"policy_deny_resolved"}, ProductReasonCodes: []string{}, EvidenceCalls: intPtr(5), ObservedAt: &observed,
		EvidenceSeal: &productEvidenceSeal{Status: "confirmed"}}
	missing := verified
	missing.VerdictID, missing.RecordStatus = strPtr("2"), verdictDetailPolicySourceMissing
	failed := productVerdictListItem{RecordStatus: verdictDetailRecordIntegrityFailed}
	wire := reportWireOf(t, encodeReportProductVerdicts(productVerdictListEnvelope{Items: []productVerdictListItem{verified, missing, failed}}))
	if wire.State != reportProductVerdictsOK || wire.Total != 3 || len(wire.Items) != 3 {
		t.Fatalf("wire %+v", wire)
	}
	if w := wire.Items[0]; !w.Verified || w.ResourceType != "book" || w.OracleValue != "confirmed" || w.Seal != "confirmed" || w.EvidenceCalls != 5 || len(w.ReasonCodes) != 1 {
		t.Fatalf("verified row %+v", w)
	}
	for _, w := range wire.Items[1:] {
		if w.Verified || w.ResourceType != "" || w.OracleValue != "" || w.ObservedAt != "" {
			t.Fatalf("unverified row carries values %+v", w)
		}
	}
	var many []productVerdictListItem
	for i := 0; i < report.MaxDiagnosticProductVerdicts; i++ {
		item := verified
		item.VerdictID = strPtr(strings.Repeat("9", 1) + string(rune('0'+i%10)) + strings.Repeat("1", i/10))
		item.ResourceType = strPtr(strings.Repeat("가", 120))
		item.OracleReasonCodes = []string{strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60), strings.Repeat("d", 60)}
		many = append(many, item)
	}
	value := encodeReportProductVerdicts(productVerdictListEnvelope{Items: many})
	big := reportWireOf(t, value)
	if len(value) > maxReportProductVerdictsHeaderBytes || big.Total != report.MaxDiagnosticProductVerdicts || len(big.Items) >= big.Total || len(big.Items) == 0 {
		t.Fatalf("truncation: bytes %d total %d items %d", len(value), big.Total, len(big.Items))
	}
}

// 헤더 모양이 어긋나면 받지 않는다.
func TestDecodeReportProductVerdictsRejects(t *testing.T) {
	ok := encodeWire(t, reportProductVerdictsWire{State: reportProductVerdictsOK, Total: 1, Items: []reportProductVerdictWire{{VerdictID: "1"}}})
	cases := map[string][]string{
		"두 개":        {ok, ok},
		"base64 아님":  {"%%%"},
		"모르는 상태":     {encodeWire(t, reportProductVerdictsWire{State: "maybe", Items: []reportProductVerdictWire{}})},
		"읽지 못함인데 행":  {encodeWire(t, reportProductVerdictsWire{State: reportProductVerdictsUnavailable, Total: 1, Items: []reportProductVerdictWire{{VerdictID: "1"}}})},
		"전체 수 < 행 수": {encodeWire(t, reportProductVerdictsWire{State: reportProductVerdictsOK, Total: 0, Items: []reportProductVerdictWire{{VerdictID: "1"}}})},
		"전체 수 상한 초과": {encodeWire(t, reportProductVerdictsWire{State: reportProductVerdictsOK, Total: report.MaxDiagnosticProductVerdicts + 1, Items: []reportProductVerdictWire{}})},
		"모르는 칸":      {base64.RawURLEncoding.EncodeToString([]byte(`{"state":"ok","total":0,"items":[],"extra":1}`))},
		"헤더 크기 초과":   {strings.Repeat("A", maxReportProductVerdictsHeaderBytes+1)},
	}
	for name, values := range cases {
		if _, ok := decodeReportProductVerdicts(values); ok {
			t.Fatalf("%s: 받음", name)
		}
	}
	if _, good := decodeReportProductVerdicts([]string{ok}); !good {
		t.Fatal("정상 헤더를 받지 않음")
	}
}

// 저장 판정과 번호·값·자원 종류가 같을 때만 확인된 판정이다. 다르면 확인 실패(확정에 안 셈)다.
func TestCrossCheckReportProductVerdicts(t *testing.T) {
	at := "2026-11-23T10:38:07Z"
	row := func(id, value, kind string) reportProductVerdictWire {
		return reportProductVerdictWire{VerdictID: id, Verified: true, ResourceType: kind, OracleValue: value, ReasonCodes: []string{"policy_deny_resolved"}, EvidenceCalls: 5, Seal: "confirmed", ObservedAt: at}
	}
	stored := map[string]storedProductVerdict{"1": {oracleValue: "confirmed", resourceType: "book"}, "2": {oracleValue: "undecidable", resourceType: "book"}}
	bad := row("6", "confirmed", "book")
	bad.ObservedAt = "어제"
	wire := reportProductVerdictsWire{State: reportProductVerdictsOK, Total: 7, Items: []reportProductVerdictWire{
		row("1", "confirmed", "book"), row("2", "confirmed", "book"), row("1", "confirmed", "users"), row("9", "confirmed", "book"), {VerdictID: "3"}, bad}}
	got := crossCheckReportProductVerdicts(wire, stored)
	if !got.Provided || got.Unavailable || got.Total != 7 || len(got.Items) != 6 {
		t.Fatalf("got %+v", got)
	}
	want := []bool{true, false, false, false, false, false}
	for i, e := range got.Items {
		if e.Verified != want[i] {
			t.Fatalf("row %d verified=%v", i, e.Verified)
		}
		if !e.Verified && (e.ResourceType != "" || e.OracleValue != "" || !e.ObservedAt.IsZero() || e.Seal != "") {
			t.Fatalf("row %d keeps values %+v", i, e)
		}
	}
	if !got.Items[0].ObservedAt.Equal(time.Date(2026, 11, 23, 10, 38, 7, 0, time.UTC)) {
		t.Fatal("관찰 시각")
	}
}

// 헤더가 없으면 구역이 없고, 읽지 못함·깨진 헤더는 '읽지 못함'이다(저장소를 열지 않는다).
func TestReportProductVerdictsFromRequestWithoutStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/report/diagnostic?task=1", nil)
	if got := reportProductVerdictsFromRequest(t.Context(), nil, req, 1); got.Provided {
		t.Fatal("헤더 없이 구역")
	}
	req.Header.Set(reportProductVerdictsHeader, reportProductVerdictsUnavailableHeader())
	if got := reportProductVerdictsFromRequest(t.Context(), nil, req, 1); !got.Provided || !got.Unavailable {
		t.Fatalf("읽지 못함: %+v", got)
	}
	req.Header.Set(reportProductVerdictsHeader, "%%%")
	if got := reportProductVerdictsFromRequest(t.Context(), nil, req, 1); !got.Provided || !got.Unavailable {
		t.Fatalf("깨진 헤더: %+v", got)
	}
}

// 저장 판정에서 판정 목록 → 헤더 → 보고서 생성까지: 확정 수에 세고 구역을 그린다. 헤더 값을 바꾸면 확인 실패로 남는다.
func TestReportProductVerdictsEndToEnd(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	got := productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{})
	env, status, _ := readProductVerdictList(t.Context(), pg.DB, task, productListJob, fixedProductContext{got})
	if status != http.StatusOK || len(env.Items) != 1 || env.Items[0].RecordStatus != productVerdictVerified {
		t.Fatalf("판정 목록: %d %+v", status, env.Items)
	}
	value := encodeReportProductVerdicts(env)
	req := httptest.NewRequest(http.MethodGet, "/api/report/diagnostic?task=1", nil)
	req.Header.Set(reportProductVerdictsHeader, value)
	product := reportProductVerdictsFromRequest(t.Context(), pg, req, task)
	if !product.Provided || product.Unavailable || len(product.Items) != 1 || !product.Items[0].Verified || product.Items[0].ResourceType != "document" || product.Items[0].OracleValue != "confirmed" {
		t.Fatalf("구역 입력: %+v", product)
	}
	data, err := loadDiagnosticReport(t.Context(), pg, task, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	base, err := report.PrepareDiagnostic(data)
	if err != nil {
		t.Fatal(err)
	}
	data.Product = product
	prepared, err := report.PrepareDiagnostic(data)
	if err != nil {
		t.Fatal(err)
	}
	sealed := product.Items[0].Seal == "confirmed"
	if sealed && prepared.Confirmed != base.Confirmed+1 || !sealed && (prepared.Confirmed != base.Confirmed || prepared.Product.Unsealed != 1) {
		t.Fatalf("확정 수: 봉인 %v %d→%d", sealed, base.Confirmed, prepared.Confirmed)
	}
	body, err := report.DiagnosticHTML(data)
	if err != nil || !strings.Contains(string(body), "<h3>확정 단계 판정</h3>") || !strings.Contains(string(body), "verdict-"+*env.Items[0].VerdictID) {
		t.Fatalf("보고서: %v", err)
	}
	// 보고서 처리기도 헤더를 읽어 구역을 그린다(내부용 HTML).
	handlerReq := httptest.NewRequest(http.MethodGet, "/api/report/diagnostic?task="+strconv.FormatInt(task, 10)+"&audience=internal", nil)
	handlerReq.Header.Set(reportProductVerdictsHeader, value)
	rec := httptest.NewRecorder()
	s.diagnosticReport(rec, handlerReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<h3>확정 단계 판정</h3>") || !strings.Contains(rec.Body.String(), "verdict-"+*env.Items[0].VerdictID) {
		t.Fatalf("보고서 처리기: %d", rec.Code)
	}
	// 헤더의 판정 값을 바꾸면 저장 판정과 달라 확인 실패다.
	wire := reportWireOf(t, value)
	wire.Items[0].OracleValue = "suspected"
	req.Header.Set(reportProductVerdictsHeader, encodeWire(t, wire))
	if tampered := reportProductVerdictsFromRequest(t.Context(), pg, req, task); len(tampered.Items) != 1 || tampered.Items[0].Verified {
		t.Fatalf("바뀐 헤더를 받음: %+v", tampered)
	}
}

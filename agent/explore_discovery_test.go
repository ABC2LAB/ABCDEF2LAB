package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
)

type recordingDiscoveryReporter struct {
	calls [][]diagnosisflow.Discovery
	n     int
	err   error
}

func (r *recordingDiscoveryReporter) ReportStatic(_ context.Context, items []diagnosisflow.Discovery) (int, error) {
	r.calls = append(r.calls, items)
	return r.n, r.err
}

// 보고 도구는 입력을 그대로 프로그램 API에 넘기고, 저장 수만 돌려준다.
func TestExploreDiscoveryToolPassesNamesOnly(t *testing.T) {
	reporter := &recordingDiscoveryReporter{n: 1}
	tool := newExploreDiscoveryTool(reporter)
	raw := `{"discoveries":[{"method":"POST","path":"/read/orders","query_keys":["page"],"body_keys":["q"],"content_type":"application/json"},{"method":"GET","path":"/other"}]}`
	result, err := tool.Call(context.Background(), json.RawMessage(raw), nil)
	if err != nil || result.IsError || len(reporter.calls) != 1 || !strings.Contains(result.Flatten(), "Stored 1 of 2") {
		t.Fatalf("result=%q calls=%d err=%v", result.Flatten(), len(reporter.calls), err)
	}
	got := reporter.calls[0]
	if len(got) != 2 || got[0].Method != "POST" || got[0].Path != "/read/orders" || got[0].QueryKeys[0] != "page" || got[0].BodyKeys[0] != "q" || got[0].ContentType != "application/json" || got[1].Path != "/other" {
		t.Fatalf("passed discoveries: %+v", got)
	}
}

// 계약 밖 입력(값 칸·모르는 칸·뒤에 붙은 값·빈 목록·크기 초과)은 저장 API를 부르지 않고 거부한다.
func TestExploreDiscoveryToolRejectsInputOutsideContract(t *testing.T) {
	big := `{"discoveries":[{"method":"GET","path":"/` + strings.Repeat("a", exploreDiscoveryInputLimit) + `"}]}`
	for _, raw := range []string{
		`{"discoveries":[{"method":"GET","path":"/read/x","query":{"id":"1"}}]}`,
		`{"discoveries":[{"method":"GET","path":"/read/x"}],"note":"x"}`,
		`{"discoveries":[{"method":"GET","path":"/read/x"}]} {}`,
		`{"discoveries":[]}`,
		`{}`,
		`[]`,
		big,
	} {
		reporter := &recordingDiscoveryReporter{n: 1}
		result, err := newExploreDiscoveryTool(reporter).Call(context.Background(), json.RawMessage(raw), nil)
		if err != nil || !result.IsError || len(reporter.calls) != 0 || !strings.Contains(result.Flatten(), "discovery_report_input_invalid") {
			t.Fatalf("%.60s: result=%q calls=%d", raw, result.Flatten(), len(reporter.calls))
		}
	}
}

// 저장 API의 거부·장애는 고정 문구로 돌려준다(원문 오류를 모델에 넘기지 않는다).
func TestExploreDiscoveryToolMapsReporterErrors(t *testing.T) {
	raw := json.RawMessage(`{"discoveries":[{"method":"GET","path":"/read/x"}]}`)
	for want, err := range map[string]error{
		"discovery_report_rejected":    diagnosisflow.ErrDenied,
		"discovery_report_unavailable": errors.New("synthetic store failure /secret/path"),
	} {
		result, callErr := newExploreDiscoveryTool(&recordingDiscoveryReporter{err: err}).Call(context.Background(), raw, nil)
		if callErr != nil || !result.IsError || !strings.Contains(result.Flatten(), want) || strings.Contains(result.Flatten(), "secret") {
			t.Fatalf("%s: %q", want, result.Flatten())
		}
	}
	result, _ := newExploreDiscoveryTool(nil).Call(context.Background(), raw, nil)
	if !result.IsError || !strings.Contains(result.Flatten(), "discovery_report_unavailable") {
		t.Fatalf("nil reporter: %q", result.Flatten())
	}
}

// 실제 diagnosisflow 저장 경로: 범위 안은 추정 발견으로 남고 범위 밖은 버려진다.
func TestExploreDiscoveryToolStoresThroughWorkflow(t *testing.T) {
	flow, err := diagnosisflow.New(t.Context(), diagnosisflow.Binding{JobID: "owned-explore", TaskID: "7", RunRef: "run-owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: diagnosisflow.Manual, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "POST"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 8, MaxResponseBytes: 4096}, Current: func() error { return nil }}, discoveryCommandStore{})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"discoveries":[{"method":"POST","path":"/read/orders","body_keys":["q"]},{"method":"GET","path":"/admin"}]}`)
	result, err := newExploreDiscoveryTool(flow).Call(context.Background(), raw, nil)
	if err != nil || result.IsError || !strings.Contains(result.Flatten(), "Stored 1 of 2") {
		t.Fatalf("result=%q err=%v", result.Flatten(), err)
	}
	s, err := flow.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range s.Endpoints {
		if e.Path == "/admin" {
			t.Fatal("out-of-scope discovery stored")
		}
		found = found || e.Method == "POST" && e.Path == "/read/orders" && e.Source == diagnosisflow.SourceAPIReconStatic && len(e.BodyKeys) == 1
	}
	if !found || s.Counts.DiscoveriesDropped != 1 {
		t.Fatalf("workflow snapshot: %+v", s)
	}
}

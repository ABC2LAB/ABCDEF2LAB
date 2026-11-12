package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// fakeInsertAssets는 넘겨받은 자산을 남기고, 원본처럼 넘긴 목록 기준 index로 결과를 돌려준다. failIndex는 원본이
// 그 index를 오류로 돌려주는 경우다.
func fakeInsertAssets(forwarded *[]map[string]any, failIndex int) actool.CoreTool {
	return actool.Build(actool.Spec{Name: "insert_assets", Description: "original", Schema: map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var in struct {
				Assets []map[string]any `json:"assets"`
			}
			if json.Unmarshal(input, &in) != nil {
				return actool.Errorf("bad input"), nil
			}
			*forwarded = append(*forwarded, in.Assets...)
			results, errs := []map[string]any{}, []map[string]any{}
			for i := range in.Assets {
				if i == failIndex {
					errs = append(errs, map[string]any{"index": i, "error": "store rejected"})
					continue
				}
				results = append(results, map[string]any{"index": i, "id": 100 + i, "type": in.Assets[i]["type"]})
			}
			return jsonResult(map[string]any{"results": results, "errors": errs})
		}})
}

func TestE2HTTPInsertAssetsKeepsOnlyApprovedTarget(t *testing.T) {
	var forwarded []map[string]any
	tool := newE2HTTPInsertAssetsTool(fakeInsertAssets(&forwarded, 1), []string{"https://juice.test:3000"}, redactSecret)
	if tool == nil || tool.Name() != "insert_assets" {
		t.Fatal("승인 origin이 있는 실행에 insert_assets를 붙이지 않음")
	}
	assets := []map[string]any{
		{"type": "endpoint", "url": "https://juice.test:3000/api/users", "method": "GET"},                           // 0 승인
		{"type": "endpoint", "url": "https://juice.test:3001/api/users", "method": "GET"},                           // 1 다른 포트
		{"type": "service", "url": "http://juice.test:3000/"},                                                       // 2 다른 scheme
		{"type": "subdomain", "domain": "JUICE.test"},                                                               // 3 승인(대소문자)
		{"type": "subdomain", "domain": "other.test"},                                                               // 4 다른 호스트
		{"type": "app", "app_name": "juice"},                                                                        // 5 대상 호스트가 아님
		{"type": "endpoint", "url": "https://juice.test:3000/a", "method": "GET", "service_ip": "10.0.0.1"},         // 6 승인 밖 IP
		{"type": "ip", "ip": "203.0.113.9"},                                                                         // 7 승인 밖 IP
		{"type": "endpoint", "url": "https://user:pw@juice.test:3000/a", "method": "GET"},                           // 8 사용자 정보
		{"type": "service", "url": "https://juice.test:3000/", "auth": []map[string]any{{"token": "SECRET-token"}}}, // 9 승인, 가림
		{"type": "subdomain", "domain": "juice.test", "record_value": []string{"evil.test"}},                        // 10 승인 밖 레코드
		{"type": "endpoint"}, // 11 URL 없음
		{"type": "service", "url": "ftp://juice.test:3000/"}, // 12 다른 scheme
	}
	raw, _ := json.Marshal(map[string]any{"assets": assets})
	out, err := tool.Call(context.Background(), raw, nil)
	if err != nil || out.IsError {
		t.Fatalf("call: %v %q", err, out.Flatten())
	}
	if len(forwarded) != 3 || forwarded[0]["url"] != "https://juice.test:3000/api/users" || forwarded[1]["domain"] != "JUICE.test" {
		t.Fatalf("원본에 넘긴 자산이 승인 대상만이 아님: %+v", forwarded)
	}
	if encoded, _ := json.Marshal(forwarded); bytes.Contains(encoded, []byte("SECRET")) {
		t.Fatal("자격 값이 가려지지 않고 원본에 넘어감")
	}
	var result struct {
		Results []struct {
			Index int   `json:"index"`
			ID    int64 `json:"id"`
		} `json:"results"`
		Errors []struct {
			Index int    `json:"index"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if json.Unmarshal([]byte(out.Flatten()), &result) != nil {
		t.Fatalf("result: %q", out.Flatten())
	}
	// 원본 결과 index(넘긴 목록 기준)는 모델이 보낸 목록 기준으로 되돌린다: 넘긴 0→0, 2→9, 원본 오류 1→3.
	if len(result.Results) != 2 || result.Results[0].Index != 0 || result.Results[1].Index != 9 {
		t.Fatalf("results index: %+v", result.Results)
	}
	errors := map[int]string{}
	for _, e := range result.Errors {
		errors[e.Index] = e.Error
	}
	if errors[3] != "store rejected" {
		t.Fatalf("원본 오류 index를 되돌리지 않음: %+v", result.Errors)
	}
	for _, i := range []int{1, 2, 4, 5, 6, 7, 8, 10, 11, 12} {
		if errors[i] != e2HTTPAssetOutsideTarget {
			t.Fatalf("index %d를 승인 밖으로 돌려주지 않음: %+v", i, result.Errors)
		}
	}
	if len(errors) != 11 {
		t.Fatalf("errors: %+v", result.Errors)
	}
	// 모두 승인 밖이면 원본을 부르지 않는다.
	forwarded = nil
	raw, _ = json.Marshal(map[string]any{"assets": []map[string]any{{"type": "subdomain", "domain": "other.test"}}})
	if out, err := tool.Call(context.Background(), raw, nil); err != nil || out.IsError || forwarded != nil || !strings.Contains(out.Flatten(), e2HTTPAssetOutsideTarget) {
		t.Fatalf("승인 밖만 있는데 원본을 부름: %q", out.Flatten())
	}
	for _, bad := range []string{`{}`, `{"assets":[]}`, `not json`} {
		if out, _ := tool.Call(context.Background(), json.RawMessage(bad), nil); !out.IsError {
			t.Fatalf("잘못된 입력 %s를 받음", bad)
		}
	}
}

// 승인 origin이 IP면 그 IP 자산은 받는다. 승인 origin을 모르거나 해석할 수 없으면 도구를 붙이지 않는다.
func TestE2HTTPInsertAssetsBinding(t *testing.T) {
	var forwarded []map[string]any
	tool := newE2HTTPInsertAssetsTool(fakeInsertAssets(&forwarded, -1), []string{"http://192.0.2.5:8080"}, redactSecret)
	raw, _ := json.Marshal(map[string]any{"assets": []map[string]any{{"type": "ip", "ip": "192.0.2.5"}, {"type": "endpoint", "url": "http://192.0.2.5:8080/x", "method": "GET"}}})
	if out, err := tool.Call(context.Background(), raw, nil); err != nil || out.IsError || len(forwarded) != 2 {
		t.Fatalf("IP origin 자산을 받지 않음: %q", out.Flatten())
	}
	base := fakeInsertAssets(&forwarded, -1)
	for name, tc := range map[string]struct {
		base    actool.CoreTool
		origins []string
		redact  func([]byte) []byte
	}{
		"no-origins":    {base, nil, redactSecret},
		"bad-origin":    {base, []string{"https://juice.test:3000", "file:///etc"}, redactSecret},
		"userinfo":      {base, []string{"https://u:p@juice.test"}, redactSecret},
		"wrong-tool":    {e2WorkerFixtureTool("record_fact", nil), []string{"https://juice.test"}, redactSecret},
		"nil-base":      {nil, []string{"https://juice.test"}, redactSecret},
		"nil-redaction": {base, []string{"https://juice.test"}, nil},
	} {
		if newE2HTTPInsertAssetsTool(tc.base, tc.origins, tc.redact) != nil {
			t.Fatalf("%s: insert_assets를 붙임", name)
		}
	}
}

// 진단 Worker 프로필은 insert_assets를 bound 판으로만 채우고, 원본 WorkerTools 순서(record_fact 다음)에 둔다.
func TestDiagnosisWorkerProfilePlacesBoundInsertAssets(t *testing.T) {
	ts := NewToolSet(nil, "")
	var forwarded []map[string]any
	wrapped := newE2HTTPInsertAssetsTool(fakeInsertAssets(&forwarded, -1), []string{"https://juice.test"}, redactSecret)
	bash, candidate := e2WorkerFixtureTool("Bash", nil), e2WorkerFixtureTool("submit_diagnostic_candidate", nil)
	worker, err := diagnosisWorkerProfile.selectTools(ts.WorkerTools(), map[string]actool.CoreTool{"Bash": bash, "submit_diagnostic_candidate": candidate, "insert_assets": wrapped}, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := profileToolNames(worker)
	if !strings.Contains(names, "record_fact,insert_assets,list_assets") {
		t.Fatalf("worker tools=%s", names)
	}
	for _, tool := range worker {
		if tool.Name() == "insert_assets" && tool != wrapped {
			t.Fatal("원본 insert_assets가 bound 판 대신 들어감")
		}
	}
	// bound 판이 없으면 원본 insert_assets는 들어가지 않는다.
	worker, err = diagnosisWorkerProfile.selectTools(ts.WorkerTools(), map[string]actool.CoreTool{"Bash": bash, "submit_diagnostic_candidate": candidate}, nil)
	if err != nil || strings.Contains(profileToolNames(worker), "insert_assets") {
		t.Fatalf("bound 판 없이 insert_assets가 들어감: %s", profileToolNames(worker))
	}
}

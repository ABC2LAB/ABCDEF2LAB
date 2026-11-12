package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// 진단 Worker의 insert_assets(C24, 루프 복원 결정안 §9-5). 원본 도구의 동작(자산 upsert·작업 연결·자동 범위)은
// 그대로 두고, 프로그램이 받는 자산을 E② 실행의 승인 대상 origin 안으로 제한한다. 승인 밖 자산이 쌓이면 Planner가
// 그쪽 intent를 만들고 거부 상한에 닿을 수 있어서다. 자동 보강(DNS·HTTP)은 진단 ToolSet에 연결하지 않는다.

const e2HTTPAssetOutsideTarget = "asset is outside the approved target of this diagnosis run; it was not recorded"

type e2HTTPApprovedOrigin struct{ scheme, host, port string }

// e2HTTPApprovedOrigins는 신뢰한 실행 정책의 origin을 비교용으로 바꾼다. 하나라도 해석할 수 없으면 nil이다.
func e2HTTPApprovedOrigins(origins []string) []e2HTTPApprovedOrigin {
	out := make([]e2HTTPApprovedOrigin, 0, len(origins))
	for _, raw := range origins {
		o, ok := e2HTTPOriginOf(raw)
		if !ok {
			return nil
		}
		out = append(out, o)
	}
	return out
}

func e2HTTPOriginOf(raw string) (e2HTTPApprovedOrigin, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return e2HTTPApprovedOrigin{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch {
	case scheme == "http" && port == "":
		port = "80"
	case scheme == "https" && port == "":
		port = "443"
	case scheme != "http" && scheme != "https":
		return e2HTTPApprovedOrigin{}, false
	}
	return e2HTTPApprovedOrigin{scheme: scheme, host: strings.ToLower(u.Hostname()), port: port}, true
}

// e2HTTPAssetApproved는 자산 하나가 승인 대상 안인지 본다. 자산에 적힌 호스트성 값(도메인·IP·URL·서비스 IP·
// 바인딩 도메인·DNS 레코드)이 모두 승인 origin의 호스트여야 하고, URL은 scheme·호스트·포트까지 같아야 한다.
func e2HTTPAssetApproved(item assetInputItem, origins []e2HTTPApprovedOrigin) bool {
	hostApproved := func(value string) bool {
		value = strings.ToLower(strings.TrimSpace(value))
		if ip := net.ParseIP(value); ip != nil {
			value = ip.String()
		}
		for _, o := range origins {
			host := o.host
			if ip := net.ParseIP(host); ip != nil {
				host = ip.String()
			}
			if value == host {
				return true
			}
		}
		return false
	}
	urlApproved := func(raw string) bool {
		o, ok := e2HTTPOriginOf(raw)
		if !ok {
			return false
		}
		for _, approved := range origins {
			if o == approved {
				return true
			}
		}
		return false
	}
	switch item.Type {
	case "service", "endpoint":
		if item.URL == "" || !urlApproved(item.URL) {
			return false
		}
	case "subdomain", "root_domain":
		if item.Domain == "" {
			return false
		}
	case "ip":
		if item.IP == "" {
			return false
		}
	default:
		// app 등 대상 호스트가 아닌 자산은 진단 실행에서 기록하지 않는다.
		return false
	}
	if item.CompanyID != nil {
		return false
	}
	values := append([]string{item.Domain, item.IP, item.ServiceIP}, item.BoundDomains...)
	values = append(values, item.RecordValue...)
	for _, value := range values {
		if value != "" && !hostApproved(value) {
			return false
		}
	}
	return true
}

type e2HTTPInsertAssetsTool struct {
	actool.CoreTool
	origins []e2HTTPApprovedOrigin
	redact  func([]byte) []byte
}

// newE2HTTPInsertAssetsTool은 원본 insert_assets를 승인 대상 origin 안의 자산만 넘기게 감싼다. 승인 origin을 모르는
// 실행(원격 평가 실행 등)에는 붙이지 않는다(nil).
func newE2HTTPInsertAssetsTool(base actool.CoreTool, origins []string, redact func([]byte) []byte) actool.CoreTool {
	approved := e2HTTPApprovedOrigins(origins)
	if base == nil || isNilCoreTool(base) || base.Name() != "insert_assets" || len(approved) == 0 || redact == nil {
		return nil
	}
	return &e2HTTPInsertAssetsTool{CoreTool: base, origins: approved, redact: redact}
}

func (t *e2HTTPInsertAssetsTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	var in struct {
		Assets []json.RawMessage `json:"assets"`
	}
	sanitized := json.RawMessage(t.redact(input))
	if json.Unmarshal(sanitized, &in) != nil || len(in.Assets) == 0 {
		return actool.Errorf("insert_assets_input_invalid: pass a non-empty assets array"), nil
	}
	type rejected struct {
		Index int    `json:"index"`
		Error string `json:"error"`
	}
	var errs []rejected
	var accepted []json.RawMessage
	var positions []int
	for i, raw := range in.Assets {
		var item assetInputItem
		if json.Unmarshal(raw, &item) != nil || !e2HTTPAssetApproved(item, t.origins) {
			errs = append(errs, rejected{Index: i, Error: e2HTTPAssetOutsideTarget})
			continue
		}
		accepted = append(accepted, raw)
		positions = append(positions, i)
	}
	out := map[string]any{"results": []any{}, "errors": errs}
	if len(accepted) > 0 {
		forwarded, err := json.Marshal(map[string]any{"assets": accepted})
		if err != nil {
			return actool.Errorf("insert_assets_input_invalid"), nil
		}
		result, callErr := t.CoreTool.Call(ctx, forwarded, tc)
		if callErr != nil || result.IsError {
			return result, callErr
		}
		// 원본 결과의 index는 넘긴 목록 기준이라 모델이 보낸 목록 기준으로 되돌린다.
		var inner struct {
			Results []map[string]any `json:"results"`
			Errors  []map[string]any `json:"errors"`
		}
		if json.Unmarshal([]byte(result.Flatten()), &inner) != nil {
			return actool.Errorf("insert_assets_result_invalid"), nil
		}
		remap := func(rows []map[string]any) []any {
			mapped := make([]any, 0, len(rows))
			for _, row := range rows {
				if index, ok := row["index"].(float64); ok && int(index) >= 0 && int(index) < len(positions) && float64(int(index)) == index {
					row["index"] = positions[int(index)]
				}
				mapped = append(mapped, row)
			}
			return mapped
		}
		out["results"] = remap(inner.Results)
		combined := remap(inner.Errors)
		for _, e := range errs {
			combined = append(combined, e)
		}
		out["errors"] = combined
	}
	return jsonResult(out)
}

package diagnosisapi

import (
	"strings"
	"testing"
)

func TestURLRunTimingCannotBeSuppliedByBrowser(t *testing.T) {
	for _, field := range []string{"started_at", "finished_at"} {
		for _, nested := range []bool{false, true} {
			endpoint := &urlTestEndpoint{}
			addition := `"` + field + `":"2026-11-17T01:02:03Z",`
			body := "{" + addition + urlCreateBody[1:]
			if nested {
				body = strings.Replace(urlCreateBody, `"input":{`, `"input":{`+addition, 1)
			}
			response := urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts", body, "timing-create")
			if response.Code != 400 || len(endpoint.calls) != 0 {
				t.Fatal("브라우저가 확인 시각을 지정했습니다", field, nested, response.Code)
			}
		}
	}
}

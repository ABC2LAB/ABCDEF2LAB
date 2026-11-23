package diagnosisapi

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 시작 확인 진술은 화면이 보낸 그대로 준비 서비스 입력까지 가야 한다(거기서 저널에 남는다). 진술이 없는 요청은 전과
// 같이 받고, 거짓 값은 정본 형식(생략)이 아니라 받지 않는다.
func TestURLAPIForwardsStartConfirmationStatements(t *testing.T) {
	const fields = `"exclusions_reviewed":true,"backup_confirmed":true,`
	body := strings.Replace(urlCreateBody, `"input":{`, `"input":{`+fields, 1)
	endpoint := &urlTestEndpoint{}
	response := urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts", body, "create-1")
	if response.Code != 201 || len(endpoint.calls) != 1 {
		t.Fatalf("status=%d calls=%d", response.Code, len(endpoint.calls))
	}
	want := diagnosisprep.Input{TargetURL: "https://owned.example/", ProfileID: "owned-read", ExclusionsReviewed: true, BackupConfirmed: true}
	if got := endpoint.calls[0].Input; got == nil || *got != want {
		t.Fatalf("forwarded input: %+v", got)
	}

	endpoint = &urlTestEndpoint{}
	response = urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts", urlCreateBody, "create-1")
	if response.Code != 201 || len(endpoint.calls) != 1 || endpoint.calls[0].Input.ExclusionsReviewed || endpoint.calls[0].Input.BackupConfirmed {
		t.Fatalf("draft without statements: status=%d calls=%d", response.Code, len(endpoint.calls))
	}

	for _, field := range []string{`"exclusions_reviewed":false,`, `"backup_confirmed":false,`, `"backup_confirmed":"true",`} {
		endpoint = &urlTestEndpoint{}
		response = urlTestRequest(urlTestHandler(t, endpoint), "POST", URLBasePath+"/drafts", strings.Replace(urlCreateBody, `"input":{`, `"input":{`+field, 1), "create-1")
		if response.Code != 400 || len(endpoint.calls) != 0 {
			t.Fatalf("%s: status=%d calls=%d", field, response.Code, len(endpoint.calls))
		}
	}
}

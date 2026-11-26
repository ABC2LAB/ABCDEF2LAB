package diagnosisprep

import (
	"strings"
	"testing"
	"time"
)

// 설치 단위 외부 전송 동의(사용자 결정 2026-11-26)의 제품 제어 요청 · 응답 모양.
func TestProductExternalConsentRequestValidation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	ok := []ProductRequest{
		{Operation: "external_consent"},
		{Operation: "external_consent_record", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true, StatementSHA256: digest}},
	}
	for _, r := range ok {
		if !validProductRequest(&r) {
			t.Errorf("valid %s rejected", r.Operation)
		}
	}
	bad := map[string]ProductRequest{
		"read with input":         {Operation: "external_consent", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true}},
		"record without input":    {Operation: "external_consent_record"},
		"record without consent":  {Operation: "external_consent_record", ExternalConsent: &ExternalConsentRequest{StatementSHA256: digest}},
		"record without digest":   {Operation: "external_consent_record", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true}},
		"record upper digest":     {Operation: "external_consent_record", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true, StatementSHA256: strings.ToUpper(digest)}},
		"record short digest":     {Operation: "external_consent_record", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true, StatementSHA256: digest[:63]}},
		"record with target":      {Operation: "external_consent_record", TargetID: "target-abc", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true}},
		"read with profile":       {Operation: "external_consent", ProfileID: "product-model-openrouter"},
		"read with job":           {Operation: "external_consent", JobID: "job-1"},
		"unknown consent op":      {Operation: "external_consent_revoke"},
		"consent on accounts op":  {Operation: "accounts", TargetID: "target-abc", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true}},
		"consent on credentials":  {Operation: "credentials", ProfileID: "product-model-openrouter", ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true}},
		"record with account req": {Operation: "external_consent_record", Account: &AccountRequest{Label: "tester-a"}, ExternalConsent: &ExternalConsentRequest{StatementConfirmed: true}},
	}
	for name, r := range bad {
		if validProductRequest(&r) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProductExternalConsentResponseValidation(t *testing.T) {
	now := time.Now()
	request := func(op string) Request {
		p := &ProductRequest{Operation: op}
		if op == "external_consent_record" {
			p.ExternalConsent = &ExternalConsentRequest{StatementConfirmed: true, StatementSHA256: strings.Repeat("a", 64)}
		}
		return Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl, Product: p}
	}
	respond := func(op string, p *ProductResponse) error {
		return ValidateResponse(request(op), Response{Version: Version, RequestID: "product-control", Product: p})
	}
	good := map[string]ProductExternalConsent{
		"not required": {},
		"required":     {Required: true, Statement: "문장"},
		"install":      {Required: true, Statement: "문장", Consented: true, Source: "install", ConsentedBy: "ARTEX", ConsentedAt: &now},
		"admin":        {Required: true, Statement: "문장", Consented: true, Source: "admin", ConsentedBy: "ARTEX", ConsentedAt: &now},
	}
	for name, c := range good {
		for _, op := range []string{"external_consent", "external_consent_record"} {
			if err := respond(op, &ProductResponse{ExternalConsent: &c}); err != nil {
				t.Errorf("%s/%s rejected: %v", op, name, err)
			}
		}
	}
	zero := time.Time{}
	bad := map[string]ProductExternalConsent{
		"not required with statement": {Statement: "문장"},
		"not required but consented":  {Consented: true, Source: "admin", ConsentedBy: "ARTEX", ConsentedAt: &now},
		"required without statement":  {Required: true},
		"open with source":            {Required: true, Statement: "문장", Source: "admin"},
		"open with time":              {Required: true, Statement: "문장", ConsentedAt: &now},
		"consented unknown source":    {Required: true, Statement: "문장", Consented: true, Source: "runner", ConsentedBy: "ARTEX", ConsentedAt: &now},
		"consented without actor":     {Required: true, Statement: "문장", Consented: true, Source: "admin", ConsentedAt: &now},
		"consented without time":      {Required: true, Statement: "문장", Consented: true, Source: "admin", ConsentedBy: "ARTEX"},
		"consented zero time":         {Required: true, Statement: "문장", Consented: true, Source: "admin", ConsentedBy: "ARTEX", ConsentedAt: &zero},
		"control char statement":      {Required: true, Statement: "문장\x00"},
	}
	for name, c := range bad {
		if respond("external_consent", &ProductResponse{ExternalConsent: &c}) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// 동의 조회 결과는 다른 조작에 실리지 않고, 동의 조작은 사유 없이 결과를 비우지 않는다.
	ok := ProductExternalConsent{}
	if respond("targets", &ProductResponse{ExternalConsent: &ok}) == nil {
		t.Error("consent on other operation accepted")
	}
	if respond("external_consent", &ProductResponse{}) == nil {
		t.Error("empty consent result accepted")
	}
	if err := respond("external_consent_record", &ProductResponse{Reason: "external_transfer_not_installed"}); err != nil {
		t.Error("consent reason rejected:", err)
	}
	if respond("external_consent_record", &ProductResponse{Reason: "statement_required", ExternalConsent: &ok}) == nil {
		t.Error("consent with reason accepted")
	}
}

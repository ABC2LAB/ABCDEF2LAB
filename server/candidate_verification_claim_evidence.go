package server

import (
	"slices"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/responseevidence"
)

// 후보 주장별 근거 요구다. 최종 판정 게이트(v2)와 최초 분석 입력이 같이 쓴다. 취약점 판정기가 아니라, 주장이 어떤 종류의
// 관찰 사실을 필요로 하는지와 그 사실이 실제 관찰에 있는지만 정한다. 사실의 보안상 의미는 모델이 해석한다.

// 주장 범주다. 후보의 vulnclass·verification_claim(프로그램이 저장한 분류 필드)으로만 정하고 이름·요약 문장은 읽지 않는다.
const (
	claimSecurityHeader  = "security_header"
	claimCookieAttribute = "cookie_attribute"
	claimErrorDisclosure = "error_disclosure"
	claimDataExposure    = "data_exposure"
	claimAuthContext     = "auth_context"
	claimBusinessRule    = "business_rule"
	claimInputHandling   = "input_handling"
	claimUnknown         = "unknown"
)

// 관찰 사실이다. 관찰 하나에서 나오는 사실과 문맥 전체에서 나오는 사실(업무 진술·입력 대비)이 있다.
const (
	// 관찰 하나의 사실
	factResponseHeaders  = "response_headers"      // 보안 헤더 구조를 읽었다.
	factIssuedCookie     = "issued_cookie"         // Set-Cookie가 실제로 왔다.
	factSpecificError    = "specific_error_signal" // 고정 오류 신호 중 구체적인 것(stack trace·SQL 오류·디버그 화면·서버 경로·예외 이름).
	factDisclosureHeader = "disclosure_header"     // Server·X-Powered-By 헤더가 왔다.
	factStructured       = "structured_content"    // 2xx 응답의 본문 구조(필드·폼·텍스트)를 읽었다.
	factGenericError     = "generic_error"         // 4xx·5xx인데 구체적인 오류 신호가 없다.
	// 문맥 사실
	factBusinessRule = "business_rule_statement" // 운영자가 업무 질문에 답했다.
	factInputEffect  = "input_contrast"          // 입력이 다른 정상 대조와 재현 사이에 상태·오류 모양 말고도 구조 차이가 있다.
)

// 관찰 하나에서 나오는 사실 집합이다. 문맥 사실은 여기 없다.
var claimObservationFacts = map[string]bool{factResponseHeaders: true, factIssuedCookie: true, factSpecificError: true,
	factDisclosureHeader: true, factStructured: true}

// candidateClaimRequirement는 주장 범주와 확정 권고에 필요한 사실이다. AnyOf 중 하나가 채워지면 된다. 관찰 사실은 원본과 모든
// 재현에 있어야 하고, 문맥 사실은 한 번 있으면 된다.
type candidateClaimRequirement struct {
	Category string     `json:"claim_category"`
	AnyOf    [][]string `json:"confirm_needs_any_of"`
	// NormalControl은 정상 대조가 필요한지다: required(대조 없이는 입력 효과를 볼 수 없음)·useful·not_needed.
	NormalControl string `json:"normal_control"`
}

var claimRequirements = map[string]candidateClaimRequirement{
	claimSecurityHeader:  {Category: claimSecurityHeader, AnyOf: [][]string{{factResponseHeaders}}, NormalControl: "not_needed"},
	claimCookieAttribute: {Category: claimCookieAttribute, AnyOf: [][]string{{factIssuedCookie}}, NormalControl: "not_needed"},
	claimErrorDisclosure: {Category: claimErrorDisclosure, AnyOf: [][]string{{factSpecificError}}, NormalControl: "useful"},
	claimDataExposure: {Category: claimDataExposure, AnyOf: [][]string{{factSpecificError}, {factDisclosureHeader},
		{factStructured, factBusinessRule}}, NormalControl: "useful"},
	claimAuthContext:   {Category: claimAuthContext, AnyOf: [][]string{{factStructured}}, NormalControl: "useful"},
	claimBusinessRule:  {Category: claimBusinessRule, AnyOf: [][]string{{factStructured, factBusinessRule}}, NormalControl: "useful"},
	claimInputHandling: {Category: claimInputHandling, AnyOf: [][]string{{factSpecificError}, {factInputEffect}}, NormalControl: "required"},
	claimUnknown:       {Category: claimUnknown, AnyOf: [][]string{{factSpecificError}, {factStructured}}, NormalControl: "useful"},
}

// 분류 낱말이다. 앞에서부터 처음 맞는 범주를 쓴다. 대상 앱의 이름·경로가 아니라 취약 유형 어휘만 둔다.
var claimClassWords = []struct {
	category string
	words    []string
}{
	{claimCookieAttribute, []string{"cookie"}},
	{claimInputHandling, []string{"sql", "sqli", "injection", "xss", "cross_site_scripting", "traversal", "lfi", "rfi", "ssrf", "ssti",
		"xxe", "open_redirect", "rce", "command", "deserialization", "template", "server_side_request_forgery"}},
	{claimErrorDisclosure, []string{"error", "stack", "exception", "debug", "verbose", "trace"}},
	{claimSecurityHeader, []string{"header", "headers", "clickjacking", "cors", "hsts", "csp", "frame"}},
	{claimDataExposure, []string{"exposure", "disclosure", "leak", "sensitive", "pii", "information", "banner"}},
	{claimBusinessRule, []string{"business", "logic", "access_control", "privilege", "authorization", "mass_assignment", "workflow"}},
	{claimAuthContext, []string{"auth", "authentication", "session", "login", "credential", "jwt"}},
}

// candidateClaimCategory는 후보의 주장 범주다. 고정 수동 판정 주장은 그 사실의 범주를 쓴다.
func candidateClaimCategory(candidate db.DiagnosticCandidate) string {
	switch candidate.VerificationClaim {
	case "html_nosniff_missing", "server_version_banner":
		return claimSecurityHeader
	}
	class := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(strings.TrimSpace(candidate.VulnClass)))
	tokens := strings.FieldsFunc(class, func(r rune) bool { return r == '_' })
	joined := "_" + strings.Join(tokens, "_") + "_"
	for _, group := range claimClassWords {
		for _, word := range group.words {
			// 낱말 경계로만 맞춘다(예: "authorization"이 "auth"로, "sqli"가 "sql"로 잘못 맞지 않게).
			if strings.Contains(joined, "_"+word+"_") {
				return group.category
			}
		}
	}
	return claimUnknown
}

func candidateClaimRequirementOf(candidate db.DiagnosticCandidate) candidateClaimRequirement {
	return claimRequirements[candidateClaimCategory(candidate)]
}

// 구체적인 오류 신호다. server_error_text는 "Internal Server Error" 같은 일반 문구라 오류가 났다는 것 말고는 알려 주지 않는다.
var specificErrorSignals = map[string]bool{responseevidence.SignalStackTrace: true, responseevidence.SignalStackTraceField: true,
	responseevidence.SignalSQLError: true, responseevidence.SignalDebugPage: true, responseevidence.SignalServerPath: true,
	responseevidence.SignalExceptionName: true}

// observationFacts는 근거 하나에서 관찰 사실을 뽑는다(정렬). 근거가 검증을 통과하지 못하면 사실이 없다.
func observationFacts(ev responseevidence.Evidence) []string {
	if responseevidence.Validate(ev) != nil {
		return nil
	}
	var facts []string
	if ev.Headers != nil && (ev.State.Structured() || ev.State == responseevidence.StateUnsupportedMedia) {
		facts = append(facts, factResponseHeaders)
		if ev.Headers.Cookies != nil && ev.Headers.Cookies.Count > 0 {
			facts = append(facts, factIssuedCookie)
		}
		if slices.Contains(ev.Headers.Present, "Server") || slices.Contains(ev.Headers.Present, "X-Powered-By") {
			facts = append(facts, factDisclosureHeader)
		}
	}
	if !ev.State.Structured() {
		slices.Sort(facts)
		return facts
	}
	specific := false
	for _, signal := range ev.Signals {
		specific = specific || specificErrorSignals[signal]
	}
	if specific {
		facts = append(facts, factSpecificError)
	}
	if ev.StatusCode >= 200 && ev.StatusCode < 300 && !ev.Empty && (ev.JSON != nil || ev.HTML != nil || ev.Text != nil) &&
		!(ev.JSON != nil && ev.JSON.ErrorShape) {
		facts = append(facts, factStructured)
	}
	if ev.StatusCode >= 400 && !specific {
		facts = append(facts, factGenericError)
	}
	slices.Sort(facts)
	return facts
}

// 입력 효과로 보지 않는 차이다. 상태·오류 모양·빈 응답·일반 오류 신호만 다르면 "요청이 실패했다"는 것 말고는 알 수 없다.
var genericDifferenceKinds = map[string]bool{responseevidence.DiffStatus: true, responseevidence.DiffEmpty: true,
	responseevidence.DiffSignal: true, responseevidence.DiffErrorShape: true, responseevidence.DiffMedia: true,
	responseevidence.DiffHeader: true, responseevidence.DiffHeaderValue: true}

// inputContrastObserved는 입력이 다른 정상 대조와 재현의 비교에 입력 효과로 볼 수 있는 구조 차이가 있는지다. 대조 쪽 입력이 같으면
// (같은 Query digest) 입력 효과가 아니다. 한쪽이 실패 응답(2xx 아님·오류 모양)이면 차이는 "실패했다"는 것뿐이라 세지 않는다.
// 정규화된 값 변화는 Compare가 이미 뺐다.
func inputContrastObserved(c responseevidence.Comparison, reproduction, control responseevidence.Evidence, sameInput bool) bool {
	if sameInput || c.Relation == responseevidence.RelationIncomparable || !successfulBody(reproduction) || !successfulBody(control) {
		return false
	}
	for _, d := range c.Differences {
		if !genericDifferenceKinds[d.Kind] {
			return true
		}
	}
	return false
}

func successfulBody(ev responseevidence.Evidence) bool {
	return ev.StatusCode >= 200 && ev.StatusCode < 300 && !(ev.JSON != nil && ev.JSON.ErrorShape)
}

// claimFactsSatisfied는 AnyOf 중 채워진 첫 묶음을 돌려준다. have는 원본·모든 재현에 공통인 관찰 사실과 문맥 사실의 합이다.
func claimFactsSatisfied(req candidateClaimRequirement, have map[string]bool) ([]string, bool) {
	for _, set := range req.AnyOf {
		ok := true
		for _, fact := range set {
			ok = ok && have[fact]
		}
		if ok {
			return set, true
		}
	}
	return nil, false
}

// claimMissingFacts는 가장 적게 모자란 묶음에서 빠진 사실을 돌려준다(분석 입력과 차단 사유에 쓴다). 모자란 수가 같으면 이미 채운
// 사실이 많은 묶음, 그것도 같으면 앞 묶음을 쓴다.
func claimMissingFacts(req candidateClaimRequirement, have map[string]bool) []string {
	var best []string
	bestHave := -1
	for _, set := range req.AnyOf {
		var missing []string
		for _, fact := range set {
			if !have[fact] {
				missing = append(missing, fact)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		got := len(set) - len(missing)
		if bestHave < 0 || len(missing) < len(best) || len(missing) == len(best) && got > bestHave {
			best, bestHave = missing, got
		}
	}
	return best
}

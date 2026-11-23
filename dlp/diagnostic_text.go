package dlp

import "regexp"

// 모델이 요약이나 재현 설명에 마크다운으로 인용한 자격값도 가린다.
// 따옴표 없는 일반 설명은 자격값과 구별할 수 없어 이 규칙에 넣지 않는다.
var quotedCredential = regexp.MustCompile("(?i)(?:\\b(?:password|passwd|pwd|credentials?|token|api[ _-]?key|client[ _-]?secret|(?:set[ -])?cookie|session[ _-]?id)\\b|비밀번호|패스워드|토큰|쿠키)(?:은|는|이|가)?(?:\\*{1,2})?[ \\t]+(?:(?:is|was|are)[ \\t]+)?(?:`[^`\\r\\n]+`|\"(?:\\\\.|[^\"\\\\\\r\\n])*\"|'(?:\\\\.|[^'\\\\\\r\\n])*')")

// DiagnosticText는 등록 자격정보 치환 뒤에도 남을 수 있는 알려진 비밀 형식을 보호한다.
// 공유 export의 닫힌 스키마/전체 마스킹을 대체하지 않으며 임의의 비밀을 판별한다고 주장하지 않는다.
var diagnosticSecrets = []*regexp.Regexp{
	// 여러 쿠키/인증 매개변수와 접힌 헤더는 구분자 뒤에도 비밀이 남을 수 있다.
	regexp.MustCompile(`(?im)\b(proxy-authorization|authorization|set-cookie|cookie)\s*:\s*[^\r\n]*(?:\r?\n[ \t]+[^\r\n]*)*`),
	// 따옴표 값은 세미콜론·콤마를 포함할 수 있으므로 전체 문자열을 먼저 제거한다.
	regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|set-cookie|password|passwd|pwd|token|api[_-]?key|client[_-]?secret|secret|session[_-]?id)\s*["']?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`),

	regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|set-cookie|password|passwd|pwd|token|api[_-]?key|client[_-]?secret|secret|session[_-]?id)\s*["']?\s*[:=]\s*[^\r\n,;}]*`),
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b`),
	regexp.MustCompile(`(?i)https?://[^\s<>"']+`),
	regexp.MustCompile(`[a-zA-Z0-9.!#$%&'*+/=?^_{}|~-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
	regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`),
}

func DiagnosticText(s string) string {
	s = quotedCredential.ReplaceAllString(s, Redacted)
	for _, re := range diagnosticSecrets {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

// credentialSecrets는 DiagnosticText에서 자격 형태만 남긴 것이다. 주소·이메일은 가리지 않는다. 탐색 도구 결과처럼 모델이
// 주소를 읽어야 다음 페이지로 갈 수 있는 출력에 쓴다.
// 따옴표 없는 값은 공백·구분자에서 끊어 같은 줄의 다른 내용(주소 등)을 남긴다.
var credentialSecrets = []*regexp.Regexp{diagnosticSecrets[0], diagnosticSecrets[1],
	regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|set-cookie|password|passwd|pwd|token|api[_-]?key|client[_-]?secret|secret|session[_-]?id)\s*["']?\s*[:=]\s*[^\s,;}&"'<>]+`),
	diagnosticSecrets[3], diagnosticSecrets[4], diagnosticSecrets[7]}

// CredentialText는 알려진 자격 형식(인증·쿠키 헤더 줄, 비밀번호·토큰 키=값, Bearer·Basic 값, JWT, 개인키)만 가린다.
// 임의의 비밀을 판별한다고 주장하지 않는다.
func CredentialText(s string) string {
	s = quotedCredential.ReplaceAllString(s, Redacted)
	for _, re := range credentialSecrets {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

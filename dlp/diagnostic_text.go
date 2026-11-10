package dlp

import "regexp"

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
	for _, re := range diagnosticSecrets {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

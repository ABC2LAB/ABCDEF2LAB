package report

import (
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/dlp"
)

// 보고서 글에서 대상이 노출한 자격 값(비밀번호·토큰)을 가리고 그 수를 센다(#875 발견 2, 사용자 결정 2026-11-23: 내부용도
// 대상에서 새어 나온 비밀번호·토큰을 가리고 보고서에는 "노출됨 n건"만 남긴다). 원문은 저장 근거(권한 있는 원본)에만 있다.
//
// 기존 자격 꼴 가림(dlp.DiagnosticText: 헤더 줄·password=·Bearer·JWT 등)에 더해, 모델이 요약에 쓰는 따옴표 없는 계정 목록
// 꼴을 가린다. 같은 문장에 자격 낱말(비밀번호·패스워드·password·credential·자격·평문 등)이 있을 때만 그 문장의 쌍을 본다.
//   - 이름/값 목록:   alice/s3cret, bob/hunter2 … 평문으로 노출
//   - 이름(값) 목록:  alice(s3cret), bob(hunter2, admin=true)
//   - 이름:값 목록:   alice:s3cret
// 경로(/users/v1 등)는 앞이 '/'인 조각이라 쌍으로 보지 않는다. 임의의 비밀을 판별한다고 주장하지 않는다.

var (
	credentialSentenceWord = regexp.MustCompile(`(?i)비밀번호|패스워드|암호|자격|평문|password|passwd|passphrase|credential|secret|token|토큰`)
	// 문장 경계: 줄바꿈, 마침표·물음표·느낌표 뒤 공백.
	credentialSentenceEnd = regexp.MustCompile(`\n|[.!?。](?:\s|$)`)
	slashPair             = regexp.MustCompile(`(^|[^/\w.@-])([A-Za-z0-9_.@-]{1,64})/([^\s,;()/\[\]]{1,128})`)
	parenPair             = regexp.MustCompile(`(^|[^\w.@-])([A-Za-z0-9_.@-]{1,64})\(([^\s,;()\[\]]{1,128})([,)\s])`)
	colonPair             = regexp.MustCompile(`(^|[^\w.@:/-])([A-Za-z][A-Za-z0-9_.@-]{0,63}):([^\s,;()/\[\]:]{1,128})`)
	credentialKeyName     = regexp.MustCompile(`(?i)^(password|passwd|pwd|token|secret|cookie|authorization|session[_-]?id|api[_-]?key|https?|ftp|mailto)$`)
)

// diagnosticTextCounted는 DiagnosticText와 쌍 꼴 가림을 하고, 자격 값으로 새로 가린 자리 수를 exposed에 더한다. 주소·이메일
// 가림(DiagnosticText의 나머지 규칙)은 자격 값이 아니라 세지 않는다. 이미 저장 기록에서 가린 자리([REDACTED])도 세지 않는다.
func diagnosticTextCounted(s string, exposed *int) string {
	before := strings.Count(s, dlp.Redacted)
	credential := strings.Count(dlp.CredentialText(s), dlp.Redacted) - before
	masked := DiagnosticText(s)
	out := maskCredentialPairs(masked)
	pairs := strings.Count(out, dlp.Redacted) - strings.Count(masked, dlp.Redacted)
	if exposed != nil {
		*exposed += max(credential, 0) + max(pairs, 0)
	}
	return out
}

// maskCredentialPairs는 자격 낱말이 있는 문장에서 이름/값·이름(값)·이름:값 쌍의 값만 [REDACTED]로 바꾼다.
func maskCredentialPairs(s string) string {
	if !credentialSentenceWord.MatchString(s) {
		return s
	}
	var out strings.Builder
	start := 0
	for _, loc := range credentialSentenceEnd.FindAllStringIndex(s, -1) {
		out.WriteString(maskSentencePairs(s[start:loc[1]]))
		start = loc[1]
	}
	out.WriteString(maskSentencePairs(s[start:]))
	return out.String()
}

func maskSentencePairs(sentence string) string {
	if !credentialSentenceWord.MatchString(sentence) {
		return sentence
	}
	sentence = slashPair.ReplaceAllStringFunc(sentence, func(m string) string {
		g := slashPair.FindStringSubmatch(m)
		if g[3] == dlp.Redacted {
			return m
		}
		return g[1] + g[2] + "/" + dlp.Redacted
	})
	sentence = parenPair.ReplaceAllStringFunc(sentence, func(m string) string {
		g := parenPair.FindStringSubmatch(m)
		if g[3] == dlp.Redacted || strings.Contains(g[3], "=") {
			return m
		}
		return g[1] + g[2] + "(" + dlp.Redacted + g[4]
	})
	return colonPair.ReplaceAllStringFunc(sentence, func(m string) string {
		g := colonPair.FindStringSubmatch(m)
		// password:값 같은 키 꼴은 DiagnosticText가 이미 가렸다. 주소 꼴(https:)도 쌍이 아니다.
		if credentialKeyName.MatchString(g[2]) || g[3] == dlp.Redacted {
			return m
		}
		return g[1] + g[2] + ":" + dlp.Redacted
	})
}

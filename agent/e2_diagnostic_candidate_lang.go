package agent

import (
	"context"
	"regexp"
	"time"
	"unicode"
)

// 한국어 화면 칸 언어 섞임 검사(2026-11-24 사용자 결정). 모델(Qwen)이 한국어 설명·조치·근거를 쓰다가 같은 뜻의 중국어
// 낱말이나 일본어 문장을 섞은 적이 있다(2026-11-23 화면 VAmPI 보고서 3군데). 진단 후보를 저장할 때 모델이 쓴 설명·조치·근거
// 문장에 한자·가나가 있으면 한 번만 한국어로 다시 쓰게 하고, 그래도 섞이면 '다른 언어 섞임' 표시를 붙여 저장한다(회차는
// 막지 않는다). 모델이 다시 내지 않고 Worker가 끝나면 붙잡아 둔 첫 제출을 표시를 붙여 저장한다(후보를 잃지 않는다).
// 대상 응답 인용(따옴표 안)·코드·URL·경로 안의 글자는 보지 않는다 — 그것은 모델이 쓴 문장이 아니다.

var (
	langFencedCode = regexp.MustCompile("(?s)```.*?```")
	langInlineCode = regexp.MustCompile("`[^`\n]*`")
	langQuoted     = regexp.MustCompile(`"[^"\n]*"|“[^”\n]*”|‘[^’\n]*’|「[^」\n]*」|『[^』\n]*』`)
	langURL        = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)
	langPath       = regexp.MustCompile(`(^|\s)/\S*`)
)

// candidateLanguageMixedError는 모델에게 돌려주는 다시 쓰기 요청이다(모델이 읽는 글이라 영어).
const candidateLanguageMixedError = "diagnostic_candidate_language_mixed: summary, remediation and severity_rationale must be Korean written in Hangul only. " +
	"Rewrite them without Chinese characters (Hanzi/Hanja/Kanji) or Japanese kana, keeping quoted target responses, code and URLs unchanged, and submit the same call_ref again."

// mixedForeignScript는 모델이 쓴 한국어 문장에 한자·가나가 섞였는지다.
func mixedForeignScript(text string) bool {
	for _, re := range []*regexp.Regexp{langFencedCode, langInlineCode, langQuoted, langURL, langPath} {
		text = re.ReplaceAllString(text, " ")
	}
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}

// candidateLanguageMixed는 진단 후보에서 모델이 쓴 설명·조치·근거 가운데 섞인 칸이 있는지다(제목·유형은 보지 않는다).
func candidateLanguageMixed(summary, remediation, rationale string) bool {
	return mixedForeignScript(summary) || mixedForeignScript(remediation) || mixedForeignScript(rationale)
}

// heldCandidateFlushTimeout은 Worker가 끝난 뒤 붙잡아 둔 후보를 저장하는 시간 상한이다(실행 문맥이 끝났어도 저장한다).
const heldCandidateFlushTimeout = 10 * time.Second

// flushHeldCandidates는 다시 쓰라고 했지만 다시 내지 않은 진단 후보를 '다른 언어 섞임' 표시를 붙여 저장한다.
func (s *e2HTTPToolState) flushHeldCandidates(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	flush := s.candidateFlush
	s.mu.Unlock()
	if flush == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), heldCandidateFlushTimeout)
	defer cancel()
	flush(ctx)
}

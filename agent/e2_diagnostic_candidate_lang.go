package agent

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Autumn-27/artex/db"
)

// 한국어 화면 칸 언어 섞임 검사(2026-11-24 사용자 결정, 같은 날 후속). 모델(Qwen)이 한국어 이름·설명·조치·근거를 쓰다가
// 같은 뜻의 중국어 낱말이나 일본어 문장을 섞은 적이 있다(2026-11-23 화면 VAmPI 보고서 3군데, 11/24 회차 2 `状态下`류 1건).
// 진단 후보를 저장할 때 모델이 쓴 이름·설명·조치·근거에 한자·가나가 있으면, 어느 칸에 어떤 조각이 섞였는지 짚어 그 부분만
// 한국어로 다시 쓰게 한다(최대 db.MaxDiagnosticLanguageRewrites번). 그래도 섞이면 '다른 언어 섞임' 표시를 붙여 저장한다
// (회차는 막지 않는다). 모델이 다시 내지 않고 Worker가 끝나면 붙잡아 둔 마지막 제출을 표시를 붙여 저장한다(후보를 잃지 않는다).
// 대상 응답 인용(따옴표 안)·코드·URL·경로 안의 글자는 보지 않는다 — 그것은 모델이 쓴 문장이 아니다. 그래서 다시 쓰기
// 요청은 대상 응답에서 온 글이면 번역하지 말고 따옴표로 감싸라고 한다(중국어·일본어 대상의 근거가 번역돼 바뀌지 않게).

var (
	langFencedCode = regexp.MustCompile("(?s)```.*?```")
	langInlineCode = regexp.MustCompile("`[^`\n]*`")
	langQuoted     = regexp.MustCompile(`"[^"\n]*"|“[^”\n]*”|‘[^’\n]*’|「[^」\n]*」|『[^』\n]*』`)
	langURL        = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)
	langPath       = regexp.MustCompile(`(^|\s)/\S*`)
)

// 다시 쓰기 요청에 싣는 섞인 조각의 상한이다. 요청이 길어지지 않게 하고, 조각은 모델이 쓴 글(가린 뒤)에서만 뽑는다.
const (
	languageFragmentsPerField = 3
	languageFragmentsTotal    = 6
	languageFragmentRunes     = 16
)

// candidateLanguageMixedError는 모델에게 돌려주는 다시 쓰기 요청의 고정 부분이다(모델이 읽는 글이라 영어). 섞인 칸·조각은
// candidateLanguageRewriteRequest가 붙인다.
const candidateLanguageMixedError = "diagnostic_candidate_language_mixed: name, summary, remediation and severity_rationale must be Korean written in Hangul only."

const candidateLanguageRewriteInstruction = "Rewrite only those parts in Korean (Hangul) and submit the same candidate again with the same call_ref, related_call_refs and vulnclass. " +
	"If a fragment is text copied from the target response (a message, field value or page text in Chinese or Japanese), do not translate it: " +
	"keep it exactly as observed and wrap it in double quotes. Keep code and URLs unchanged."

// candidateLanguageField는 섞임이 있는 칸 하나와 그 칸에서 뽑은 조각이다(칸 이름은 도구 입력 이름).
type candidateLanguageField struct {
	Field     string
	Fragments []string
}

// candidateLanguageKey는 저장소가 같은 후보로 보는 기준(실행·호출·유형·이름·증거 digest, db.RecordDiagnosticCandidate)이다.
// 저장이 끝난 후보의 다시 쓰기 횟수를 기억할 때 쓴다.
func candidateLanguageKey(c db.DiagnosticCandidate) string {
	return strings.Join([]string{c.RunRef, c.CallRef, c.VulnClass, c.Name, c.EvidenceDigest}, "\x00")
}

// candidateLanguagePendingKey는 다시 쓰기를 기다리는 동안의 후보 식별자다(2026-11-24 사용자 결정). 이름도 다시 쓰게
// 하므로 이름은 넣지 않는다 — 같은 호출·유형·증거 digest면 이름이 달라도 고친 판으로 보고, 붙잡아 둔 제출을 버린다(후보가
// 둘로 갈리지 않게). 유형이나 증거(related_call_refs)가 다르면 다른 후보다.
func candidateLanguagePendingKey(c db.DiagnosticCandidate) string {
	return strings.Join([]string{c.RunRef, c.CallRef, c.VulnClass, c.EvidenceDigest}, "\x00")
}

// languageModelText는 모델이 쓴 문장만 남긴다(인용·코드·URL·경로를 지운다).
func languageModelText(text string) string {
	for _, re := range []*regexp.Regexp{langFencedCode, langInlineCode, langQuoted, langURL, langPath} {
		text = re.ReplaceAllString(text, " ")
	}
	return text
}

// katakanaProlongedMark는 가타카나 장음 부호(U+30FC)다. 유니코드 문자 체계는 공통(Common)이지만 일본어 낱말 안에 들어가므로
// 조각을 끊지 않게 같이 본다.
const katakanaProlongedMark = '\u30fc'

func foreignScriptRune(r rune) bool {
	return r == katakanaProlongedMark || unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// mixedForeignScript는 모델이 쓴 한국어 문장에 한자·가나가 섞였는지다.
func mixedForeignScript(text string) bool {
	return strings.IndexFunc(languageModelText(text), foreignScriptRune) >= 0
}

// foreignScriptFragments는 모델이 쓴 문장에서 한자·가나가 이어진 조각을 차례대로 최대 limit개 뽑는다(같은 조각은 한 번,
// 긴 조각은 languageFragmentRunes 글자에서 자른다).
func foreignScriptFragments(text string, limit int) []string {
	var out []string
	seen := map[string]bool{}
	var run []rune
	take := func() {
		if len(run) == 0 || len(out) >= limit {
			run = run[:0]
			return
		}
		fragment := run
		if len(fragment) > languageFragmentRunes {
			fragment = fragment[:languageFragmentRunes]
		}
		s := string(fragment)
		if len(run) > languageFragmentRunes {
			s += "..."
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
		run = run[:0]
	}
	for _, r := range languageModelText(text) {
		if foreignScriptRune(r) {
			run = append(run, r)
			continue
		}
		take()
	}
	take()
	return out
}

// candidateLanguageFindings는 진단 후보에서 모델이 쓴 이름·설명·조치·근거 가운데 섞인 칸과 조각이다(유형은 보지 않는다).
// 조각은 칸마다 languageFragmentsPerField개, 전체 languageFragmentsTotal개까지 싣는다. 칸은 상한과 상관없이 모두 남긴다.
func candidateLanguageFindings(c db.DiagnosticCandidate) []candidateLanguageField {
	var out []candidateLanguageField
	left := languageFragmentsTotal
	for _, f := range []struct{ name, text string }{
		{"name", c.Name}, {"summary", c.Summary}, {"remediation", c.Remediation}, {"severity_rationale", c.SeverityRationale},
	} {
		if !mixedForeignScript(f.text) {
			continue
		}
		fragments := foreignScriptFragments(f.text, min(languageFragmentsPerField, left))
		left -= len(fragments)
		out = append(out, candidateLanguageField{Field: f.name, Fragments: fragments})
	}
	return out
}

// candidateLanguageRewriteRequest는 섞인 칸·조각을 짚은 다시 쓰기 요청이다. 조각은 모델이 낸 글을 그대로 돌려준다.
func candidateLanguageRewriteRequest(findings []candidateLanguageField, attempt int) string {
	var b strings.Builder
	b.WriteString(candidateLanguageMixedError)
	b.WriteString(" Mixed fragments:")
	for i, f := range findings {
		if i > 0 {
			b.WriteString(";")
		}
		b.WriteString(" ")
		b.WriteString(f.Field)
		if len(f.Fragments) == 0 {
			b.WriteString(" (more fragments omitted)")
			continue
		}
		b.WriteString(":")
		for j, fragment := range f.Fragments {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString(" ")
			b.WriteString(strconv.Quote(fragment))
		}
	}
	b.WriteString(". ")
	b.WriteString(candidateLanguageRewriteInstruction)
	b.WriteString(" Rewrite request " + strconv.Itoa(attempt) + " of " + strconv.Itoa(db.MaxDiagnosticLanguageRewrites) + ".")
	return b.String()
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

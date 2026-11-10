package main

import (
	"net/url"
	"regexp"
	"strings"
)

// 브라우저 수용시험의 보존 시험 전용 기록이다(docs/evaluation/browser-acceptance.md §6).
// 공식 회차에서는 켜지 않는다. 경로는 기록 전에 가리고 원문은 어떤 파일에도 남기지 않는다.

const (
	enginePathLogName = "engine-paths.jsonl"
	// 가림 규칙을 바꾸면 이 값을 올려서 다른 규칙의 패턴끼리 비교하지 않게 한다.
	pathMaskProfile  = "abc2lab.path-mask/v1"
	maskedSegment    = "{id}"
	maskedLongPath   = "/{too_long}"
	maxPathSegments  = 32
	maxSegmentLength = 32
)

var (
	safeSegment    = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	digitSegment   = regexp.MustCompile(`^[0-9]+$`)
	hexSegment     = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	encodedSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

// 시도 ID·전송 단계는 같은 seq의 engine-requests.jsonl 행에 있다. 여기에 복사하지 않는다.
type pathRow struct {
	Seq            uint64 `json:"seq"`
	Method         string `json:"method"`
	PathPattern    string `json:"path_pattern"`
	MaskedSegments int    `json:"masked_segments"`
	PathProfileRef string `json:"path_profile_ref"`
}

// 일반 엔드포인트 이름(authentication-details)은 남기고, 숫자나 대소문자가 섞인 긴 조각만 값으로 본다.
func looksEncoded(segment string) bool {
	return encodedSegment.MatchString(segment) &&
		(strings.ContainsAny(segment, "0123456789") || strings.ToLower(segment) != segment && strings.ToUpper(segment) != segment)
}

// 안전 문자 밖(이메일의 @, base64의 +·= 등)이나 32자를 넘는 조각(UUID 포함)은 전부 가린다.
func maskSegment(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	segment, err := url.PathUnescape(raw)
	if err != nil || len(segment) > maxSegmentLength || !safeSegment.MatchString(segment) ||
		digitSegment.MatchString(segment) || hexSegment.MatchString(segment) || looksEncoded(segment) {
		return maskedSegment, true
	}
	return segment, false
}

func maskPath(escaped string) (string, int) {
	segments := strings.Split(escaped, "/")
	// 빈 조각(맨 앞, 끝 `/`)은 값이 아니므로 세지 않는다.
	values := 0
	for _, segment := range segments {
		if segment != "" {
			values++
		}
	}
	if values > maxPathSegments {
		return maskedLongPath, values
	}
	masked := 0
	for i, raw := range segments {
		segment, hit := maskSegment(raw)
		segments[i] = segment
		if hit {
			masked++
		}
	}
	return strings.Join(segments, "/"), masked
}

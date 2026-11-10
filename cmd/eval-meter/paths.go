package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
)

// 브라우저 수용시험의 보존 시험 전용 기록이다(docs/evaluation/browser-acceptance.md §6).
// 공식 회차에서는 켜지 않는다. 경로 조각의 원문은 어떤 파일에도 남기지 않는다.
// 값처럼 보이는 조각은 `{id}`로 가리고, 나머지 조각도 짧은 계정명·토큰과 글자로 구분할 수 없으므로
// 회차 묶음이 함께 쓰는 비밀 키의 HMAC으로 바꾼다. 같은 키를 쓴 회차끼리는 같은 조각이 같은 값이 되어
// 보존율을 셀 수 있다. 키는 근거 디렉터리 밖에 두고 기록하지 않는다.

const (
	enginePathLogName = "engine-paths.jsonl"
	// 가림 규칙을 바꾸면 이 값을 올려서 다른 규칙의 패턴끼리 비교하지 않게 한다.
	pathMaskProfile    = "abc2lab.path-mask/v2"
	maskedSegment      = "{id}"
	maskedLongPath     = "/{too_long}"
	maxPathSegments    = 32
	maxSegmentLength   = 32
	pathKeyBytes       = 32
	pseudonymHexLength = 16
	// 키 참조는 이 고정 문자열의 HMAC이다. `/`가 들어 있어 경로 조각의 가명과 겹치지 않는다.
	pathKeyRefLabel = "abc2lab.path-key-ref/v1"
)

// 정적 자원과 API를 나눠 셀 수 있게 확장자만 남긴다(scripts/eval/preservation.py의 STATIC_SUFFIXES와 같다).
var staticSuffixes = []string{".js", ".mjs", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp",
	".woff", ".woff2", ".ttf", ".otf"}

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
	// 집계할 때 회차 묶음이 같은 키를 썼는지, 집계자가 가진 키가 그 키인지 확인하는 값이다. 키 값은 남기지 않는다.
	PathKeyRef string `json:"path_key_ref"`
}

// 일반 엔드포인트 이름(authentication-details)은 남기고, 숫자나 대소문자가 섞인 긴 조각만 값으로 본다.
func looksEncoded(segment string) bool {
	return encodedSegment.MatchString(segment) &&
		(strings.ContainsAny(segment, "0123456789") || strings.ToLower(segment) != segment && strings.ToUpper(segment) != segment)
}

// 목록의 확장자끼리는 서로의 끝이 되지 않아서 처음 맞는 것이 유일하다.
func staticSuffix(segment string) string {
	lower := strings.ToLower(segment)
	for _, candidate := range staticSuffixes {
		if strings.HasSuffix(lower, candidate) {
			return candidate
		}
	}
	return ""
}

func pathKeyRef(key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pathKeyRefLabel)) // hash.Hash의 Write는 오류를 돌려주지 않는다.
	return hex.EncodeToString(mac.Sum(nil))[:pseudonymHexLength]
}

func pseudonym(segment string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(segment)) // hash.Hash의 Write는 오류를 돌려주지 않는다.
	return "{h:" + hex.EncodeToString(mac.Sum(nil))[:pseudonymHexLength] + "}" + staticSuffix(segment)
}

// 안전 문자 밖(이메일의 @, base64의 +·= 등)이나 32자를 넘는 조각(UUID 포함)은 `{id}`로 가린다.
// 가린 조각에도 정적 자원 확장자는 남겨서 해시가 긴 번들 이름(main.<hex32>.js)이 API로 세이지 않게 한다.
// 가린 조각만 masked로 센다. HMAC으로 바꾼 조각은 값이 아니라 이름일 수도 있어서 세지 않는다.
func maskSegment(raw string, key []byte) (string, bool) {
	if raw == "" {
		return "", false
	}
	segment, err := url.PathUnescape(raw)
	if err != nil {
		return maskedSegment + staticSuffix(raw), true
	}
	if len(segment) > maxSegmentLength || !safeSegment.MatchString(segment) ||
		digitSegment.MatchString(segment) || hexSegment.MatchString(segment) || looksEncoded(segment) {
		return maskedSegment + staticSuffix(segment), true
	}
	return pseudonym(segment, key), false
}

func maskPath(escaped string, key []byte) (string, int) {
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
		segment, hit := maskSegment(raw, key)
		segments[i] = segment
		if hit {
			masked++
		}
	}
	return strings.Join(segments, "/"), masked
}

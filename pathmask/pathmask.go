// Package pathmask는 평가용 경로 가림 규칙(abc2lab.path-mask/v2)이다. 계측기(cmd/eval-meter)의 수용시험 경로 기록과
// E② 거부 행의 평가 전용 경로 키가 같은 규칙·같은 키를 써야 두 기록의 패턴을 맞춰 볼 수 있어서 한곳에 둔다.
// Python 판은 scripts/eval/path_mask.py이고, 두 판은 testdata/path-mask-v2.json 벡터로 같은 값을 내는지 시험한다.
//
// 값처럼 보이는 조각은 `{id}`로 가리고, 나머지 조각도 짧은 계정명·토큰과 글자로 구분할 수 없으므로
// 회차 묶음이 함께 쓰는 비밀 키의 HMAC으로 바꾼다. 같은 키를 쓴 기록끼리는 같은 조각이 같은 값이 된다.
// 경로 조각 원문은 결과에 남지 않는다. 키는 근거 디렉터리 밖에 두고 기록하지 않는다.
package pathmask

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
)

const (
	// 가림 규칙을 바꾸면 이 값을 올려서 다른 규칙의 패턴끼리 비교하지 않게 한다.
	Profile = "abc2lab.path-mask/v2"
	// MaskedSegment는 값으로 본 조각의 자리 표시다.
	MaskedSegment = "{id}"
	// LongPath는 MaxSegments를 넘는 경로 전체를 대신한다.
	LongPath = "/{too_long}"
	// MaxSegments는 조각별로 가리는 경로의 최대 조각 수다.
	MaxSegments = 32
	// KeyBytes는 가림 키 길이다.
	KeyBytes           = 32
	maxSegmentLength   = 32
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

// KeyRef는 기록마다 남기는 키 참조다. 집계할 때 기록 묶음이 같은 키를 썼는지 확인한다. 키 값은 드러나지 않는다.
func KeyRef(key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pathKeyRefLabel)) // hash.Hash의 Write는 오류를 돌려주지 않는다.
	return hex.EncodeToString(mac.Sum(nil))[:pseudonymHexLength]
}

// Pseudonym은 값으로 보지 않은 조각의 가명이다.
func Pseudonym(segment string, key []byte) string {
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
		return MaskedSegment + staticSuffix(raw), true
	}
	if len(segment) > maxSegmentLength || !safeSegment.MatchString(segment) ||
		digitSegment.MatchString(segment) || hexSegment.MatchString(segment) || looksEncoded(segment) {
		return MaskedSegment + staticSuffix(segment), true
	}
	return Pseudonym(segment, key), false
}

// Mask는 percent 인코딩이 남은 경로(url.URL.EscapedPath())를 패턴과 가린 조각 수로 바꾼다.
func Mask(escaped string, key []byte) (string, int) {
	segments := strings.Split(escaped, "/")
	// 빈 조각(맨 앞, 끝 `/`)은 값이 아니므로 세지 않는다.
	values := 0
	for _, segment := range segments {
		if segment != "" {
			values++
		}
	}
	if values > MaxSegments {
		return LongPath, values
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

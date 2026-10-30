// Package dlp projects host-classified JSON into minimal data suitable for a
// chosen output boundary. It does not authorize network access or infer that
// arbitrary text is free of sensitive information.
package dlp

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrPolicy  = errors.New("dlp: invalid host policy")
	ErrBlocked = errors.New("dlp: projection blocked")
	ErrLimit   = errors.New("dlp: projection limit exceeded")
	ErrWrite   = errors.New("dlp: output write failed")
)

const (
	Redacted = "[REDACTED]"
	// Schema bounds constrain trusted configuration independently of input limits.
	maxSchemaDepth    = 64
	maxFieldNameBytes = 64
)

// Limits are mandatory per-document limits, including within redacted values.
// Depth counts JSON values, starting at one for the root object.
type Limits struct {
	MaxInputBytes   int
	MaxOutputBytes  int
	MaxDepth        int
	MaxObjectFields int
	MaxArrayItems   int
}

type kind uint8

const (
	invalidKind kind = iota
	redactKind
	boolKind
	integerKind
	enumKind
	objectKind
	arrayKind
)

// Rule is an opaque host-owned rule. Its zero value is invalid. Never derive
// enum values or rule classifications from a model response or target payload.
type Rule struct{ spec *rule }

type rule struct {
	kind   kind
	min    int64
	max    int64
	values []string
	fields map[string]Rule
	item   Rule
}

// Redact replaces an entire JSON value, regardless of its type or nesting.
func Redact() Rule { return Rule{&rule{kind: redactKind}} }

// Bool permits only a JSON boolean.
func Bool() Rule { return Rule{&rule{kind: boolKind}} }

// Integer permits only an integer literal within the inclusive host bounds.
func Integer(min, max int64) Rule { return Rule{&rule{kind: integerKind, min: min, max: max}} }

// Enum permits only the exact strings supplied by the host as fixed constants.
func Enum(values ...string) Rule {
	return Rule{&rule{kind: enumKind, values: append([]string(nil), values...)}}
}

// Object permits registered fields only. Missing fields remain absent; data
// completeness and vulnerability verdicts are the caller's responsibility.
func Object(fields map[string]Rule) Rule {
	return Rule{&rule{kind: objectKind, fields: copyFields(fields)}}
}

// Array applies item to every element, subject to the document's array limit.
func Array(item Rule) Rule { return Rule{&rule{kind: arrayKind, item: item}} }

// Policy is an immutable snapshot that can be shared by concurrent calls. Its
// zero value, and a nil *Policy, block every projection.
type Policy struct {
	limits Limits
	root   Rule
}

func New(limits Limits, fields map[string]Rule) (*Policy, error) {
	if limits.MaxInputBytes <= 0 || limits.MaxOutputBytes <= 0 || limits.MaxDepth <= 0 ||
		limits.MaxObjectFields <= 0 || limits.MaxArrayItems <= 0 {
		return nil, ErrPolicy
	}
	root, err := snapshot(Object(fields), 1)
	if err != nil {
		return nil, err
	}
	return &Policy{limits: limits, root: root}, nil
}

func snapshot(in Rule, depth int) (Rule, error) {
	if in.spec == nil || depth > maxSchemaDepth {
		return Rule{}, ErrPolicy
	}
	s := *in.spec
	switch s.kind {
	case redactKind, boolKind:
	case integerKind:
		if s.min > s.max {
			return Rule{}, ErrPolicy
		}
	case enumKind:
		if len(s.values) == 0 {
			return Rule{}, ErrPolicy
		}
		seen := make(map[string]bool, len(s.values))
		for _, value := range s.values {
			if !utf8.ValidString(value) || strings.ContainsRune(value, utf8.RuneError) || strings.IndexFunc(value, unicode.IsControl) >= 0 || seen[value] {
				return Rule{}, ErrPolicy
			}
			seen[value] = true
		}
		s.values = append([]string(nil), s.values...)
	case objectKind:
		s.fields = make(map[string]Rule, len(in.spec.fields))
		seen := make(map[string]bool, len(in.spec.fields))
		for name, child := range in.spec.fields {
			key := normalizedName(name)
			if !validName(name) || seen[key] || child.spec == nil || (sensitiveName(key) && child.spec.kind != redactKind) {
				return Rule{}, ErrPolicy
			}
			seen[key] = true
			copied, err := snapshot(child, depth+1)
			if err != nil {
				return Rule{}, err
			}
			s.fields[name] = copied
		}
	case arrayKind:
		item, err := snapshot(s.item, depth+1)
		if err != nil {
			return Rule{}, err
		}
		s.item = item
	default:
		return Rule{}, ErrPolicy
	}
	return Rule{&s}, nil
}

func copyFields(fields map[string]Rule) map[string]Rule {
	copy := make(map[string]Rule, len(fields))
	for name, value := range fields {
		copy[name] = value
	}
	return copy
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > maxFieldNameBytes {
		return false
	}
	for i, c := range []byte(name) {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '_' || c == '-')) {
			return false
		}
	}
	return true
}

func normalizedName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
}

// This denylist is a backstop to the closed host schema, not a claim to detect
// every spelling of personal data. Unregistered names are blocked separately.
func sensitiveName(name string) bool {
	switch name {
	case "authorization", "proxyauthorization", "cookie", "setcookie",
		"token", "accesstoken", "refreshtoken", "apikey", "secret", "clientsecret",
		"password", "passwd", "pwd", "credential", "credentials",
		"email", "phone", "phonenumber", "address", "name", "username",
		"account", "accountid", "userid", "card", "cardnumber", "pan", "ssn", "rrn",
		"residentregistrationnumber", "body", "requestbody", "responsebody",
		"rawrequest", "rawresponse", "text", "thinking", "content", "messages", "system", "error":
		return true
	default:
		return false
	}
}

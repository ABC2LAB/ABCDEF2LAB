package dlp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Autumn-27/artex/dlp"
)

const (
	redactionFuzzPayloadLimit = 128
	redactionFuzzInputLimit   = 32 << 10
	redactionFuzzDepthLimit   = 16
	redactionFuzzWidthLimit   = 8
	redactionFuzzPaths        = 3
	redactionFuzzExpected     = `{"body":"[REDACTED]","headers":{"Authorization":"[REDACTED]"},"items":[{"body":"[REDACTED]","ok":true}],"status":200}`
)

func redactionFuzzPolicy(t *testing.T) *dlp.Policy {
	t.Helper()
	p, err := dlp.New(dlp.Limits{
		MaxInputBytes: redactionFuzzInputLimit, MaxOutputBytes: 1024,
		MaxDepth: redactionFuzzDepthLimit, MaxObjectFields: redactionFuzzWidthLimit,
		MaxArrayItems: redactionFuzzWidthLimit,
	}, map[string]dlp.Rule{
		"status": dlp.Integer(100, 599), "body": dlp.Redact(),
		"headers": dlp.Object(map[string]dlp.Rule{"Authorization": dlp.Redact()}),
		"items":   dlp.Array(dlp.Object(map[string]dlp.Rule{"ok": dlp.Bool(), "body": dlp.Redact()})),
	})
	if err != nil {
		t.Fatal("invalid host fixture policy")
	}
	return p
}

func redactionFuzzSecret(payload []byte) string {
	if len(payload) > redactionFuzzPayloadLimit {
		payload = payload[:redactionFuzzPayloadLimit]
	}
	// Distinct from public metadata and the replacement itself, even when the
	// fuzz payload is empty, invalid UTF-8, or literally "[REDACTED]".
	return "redaction-private-" + hex.EncodeToString(payload) + "-end\x00\n\t\"\\<>&π🧪"
}

func redactionFuzzJSON(t testing.TB, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot encode synthetic fixture")
	}
	return encoded
}

func redactionFuzzValue(t *testing.T, secret string, shape, depth uint8) []byte {
	t.Helper()
	var value any
	switch shape % 8 {
	case 0:
		value = nil
	case 1:
		value = false
	case 2:
		value = json.Number("-12.5")
	case 3:
		value = secret
	case 4:
		value = map[string]any{secret: secret, "private": true}
	case 5:
		value = []any{secret, nil, true, json.Number("1.25")}
	case 6:
		value = map[string]any{"left": []any{secret, secret}}
	case 7:
		value = []any{map[string]any{secret: secret}, ""}
	}
	for i := uint8(0); i < depth%6; i++ {
		if i%2 == 0 {
			value = map[string]any{"nested": value, secret: secret}
		} else {
			value = []any{value, secret}
		}
	}
	return redactionFuzzJSON(t, value)
}

func redactionFuzzDocument(value []byte, path uint8) []byte {
	values := [redactionFuzzPaths]string{`"private-control"`, `"private-control"`, `"private-control"`}
	values[path%redactionFuzzPaths] = string(value)
	return []byte(`{"status":200,"body":` + values[0] + `,"headers":{"Authorization":` + values[1] + `},"items":[{"ok":true,"body":` + values[2] + `}]}`)
}

func redactionFuzzProject(t *testing.T, p *dlp.Policy, input []byte) (dlp.SafeJSON, error) {
	t.Helper()
	original := bytes.Clone(input)
	safe, err := p.Project(context.Background(), input)
	if !bytes.Equal(input, original) {
		t.Fatal("projection mutated input")
	}
	if err != nil {
		if len(safe.Bytes()) != 0 {
			t.Fatal("failed projection exposed partial data")
		}
		if err.Error() != dlp.ErrBlocked.Error() && err.Error() != dlp.ErrLimit.Error() {
			t.Fatal("projection error exposed non-contract diagnostics")
		}
	}
	return safe, err
}

func redactionFuzzExpectSafe(t *testing.T, p *dlp.Policy, input []byte) {
	t.Helper()
	safe, err := redactionFuzzProject(t, p, input)
	if err != nil || string(safe.Bytes()) != redactionFuzzExpected {
		t.Fatal("valid subtree did not preserve metadata and redact every sensitive value")
	}
	again, err := redactionFuzzProject(t, p, safe.Bytes())
	if err != nil || string(again.Bytes()) != redactionFuzzExpected {
		t.Fatal("safe output changed when projected again")
	}
	copy := safe.Bytes()
	copy[0] = '!'
	if string(safe.Bytes()) != redactionFuzzExpected {
		t.Fatal("safe output shared its mutable backing bytes")
	}
}

// Escape UTF-16 units explicitly so values containing quotes, control bytes,
// non-ASCII text and supplementary characters get a distinct valid JSON spelling.
func redactionFuzzEscapedString(value string) []byte {
	const digits = "0123456789abcdef"
	out := []byte{'"'}
	for _, unit := range utf16.Encode([]rune(value)) {
		out = append(out, '\\', 'u', digits[unit>>12], digits[(unit>>8)&15], digits[(unit>>4)&15], digits[unit&15])
	}
	return append(out, '"')
}

func FuzzRedactionSubtreeReplacement(f *testing.F) {
	for shape := uint8(0); shape < 8; shape++ {
		f.Add([]byte("fixture"), shape, uint8(0))
		f.Add([]byte("\xff[REDACTED]\x00"), shape, uint8(5))
	}
	f.Fuzz(func(t *testing.T, payload []byte, shape, depth uint8) {
		p := redactionFuzzPolicy(t)
		secret := redactionFuzzSecret(payload)
		literal := redactionFuzzJSON(t, secret)
		redactionFuzzExpectSafe(t, p, redactionFuzzDocument(literal, 0))
		value := redactionFuzzValue(t, secret, shape, depth)
		escaped := redactionFuzzEscapedString(secret)
		var decoded string
		if json.Unmarshal(escaped, &decoded) != nil || decoded != secret {
			t.Fatal("escaped fixture changed its semantic value")
		}
		for path := uint8(0); path < redactionFuzzPaths; path++ {
			input := redactionFuzzDocument(value, path)
			redactionFuzzExpectSafe(t, p, input)
			alternate := bytes.ReplaceAll(input, literal, escaped)
			alternate = bytes.ReplaceAll(alternate, []byte(`"body":`), []byte(`"bo\u0064y":`))
			var pretty bytes.Buffer
			if json.Indent(&pretty, alternate, "", "  ") != nil {
				t.Fatal("alternate fixture is not JSON")
			}
			alternate = append([]byte(" \r\n"), pretty.Bytes()...)
			alternate = append(alternate, '\n', '\t')
			var left, right any
			if json.Unmarshal(input, &left) != nil || json.Unmarshal(alternate, &right) != nil || !reflect.DeepEqual(left, right) {
				t.Fatal("alternate fixture changed the input document")
			}
			redactionFuzzExpectSafe(t, p, alternate)
		}
	})
}

// Redaction must validate discarded data, not stop parsing when a secret field
// is found. Corruption and resource limits still block the entire document.
func FuzzRedactionRejectsInvalidSubtrees(f *testing.F) {
	const flaws = 10
	for path := uint8(0); path < redactionFuzzPaths; path++ {
		for flaw := uint8(0); flaw < flaws; flaw++ {
			f.Add([]byte("fixture"), path*flaws+flaw)
		}
	}
	f.Add([]byte("\xff[REDACTED]"), ^uint8(0))
	f.Fuzz(func(t *testing.T, payload []byte, control uint8) {
		p := redactionFuzzPolicy(t)
		literal := redactionFuzzJSON(t, redactionFuzzSecret(payload))
		path, flaw := (control/flaws)%redactionFuzzPaths, control%flaws
		redactionFuzzExpectSafe(t, p, redactionFuzzDocument(literal, path))
		value := literal
		wantErr := dlp.ErrBlocked
		switch flaw {
		case 0:
			value = []byte(`{"x":` + string(literal) + `,"x":false}`)
		case 1:
			value = []byte(`{"x":` + string(literal) + `,"\u0078":false}`)
		case 2:
			value = []byte(`[` + string(literal) + `,]`)
		case 3:
			value = []byte(`{` + string(literal) + `:}`)
		case 4:
			value = []byte(`{` + string(literal) + `:"\ud800"}`)
		case 5:
			value = []byte(`{` + string(literal) + ":\"\xff\"}")
		case 6:
			// The scalar starts at depth 2/3/4 for body/header/item paths.
			// Accept exactly MaxDepth before rejecting the next nesting level.
			nesting := redactionFuzzDepthLimit - []int{2, 3, 4}[path]
			boundary := []byte(strings.Repeat("[", nesting) + string(literal) + strings.Repeat("]", nesting))
			redactionFuzzExpectSafe(t, p, redactionFuzzDocument(boundary, path))
			value = append(append([]byte{'['}, boundary...), ']')
			wantErr = dlp.ErrLimit
		case 7:
			items := make([]json.RawMessage, redactionFuzzWidthLimit+1)
			for i := range items {
				items[i] = literal
			}
			value = redactionFuzzJSON(t, items)
			wantErr = dlp.ErrLimit
		case 8:
			fields := make(map[string]json.RawMessage)
			for i := 0; i <= redactionFuzzWidthLimit; i++ {
				fields["key"+strconv.Itoa(i)] = literal
			}
			value = redactionFuzzJSON(t, fields)
			wantErr = dlp.ErrLimit
		}
		input := redactionFuzzDocument(value, path)
		if flaw == 9 {
			input = append(input, []byte(" {}")...)
		}
		if flaw != 2 && flaw != 3 && flaw != 5 && flaw != 9 && !json.Valid(input) {
			t.Fatal("fixture unexpectedly broke JSON syntax before its intended guard")
		}
		safe, err := redactionFuzzProject(t, p, input)
		if !errors.Is(err, wantErr) {
			t.Fatalf("invalid subtree %d at path %d did not return its contract error", flaw, path)
		}
		var sink bytes.Buffer
		if n, err := safe.Write(context.Background(), &sink); n != 0 || !errors.Is(err, dlp.ErrBlocked) || sink.Len() != 0 {
			t.Fatal("failed projection remained writable")
		}
	})
}

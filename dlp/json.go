package dlp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// SafeJSON contains a completed projection. Only Project creates a writable
// result. Bytes returns a copy; failed projections return the empty zero value.
type SafeJSON struct{ body []byte }

func (s SafeJSON) Bytes() []byte { return bytes.Clone(s.body) }

// Write calls the writer once, after checking cancellation and result validity.
// It never retries a partial write. Cancellation after Write starts cannot undo
// bytes already accepted by the writer; the owning runtime must handle that.
func (s SafeJSON) Write(ctx context.Context, w io.Writer) (int, error) {
	if len(s.body) == 0 {
		return 0, ErrBlocked
	}
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if w == nil {
		return 0, ErrWrite
	}
	n, err := w.Write(s.Bytes())
	if n < 0 || n > len(s.body) {
		return 0, ErrWrite
	}
	if err == nil && n != len(s.body) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return n, &hiddenError{public: ErrWrite, cause: err}
	}
	return n, nil
}

// Project strictly parses one JSON object. It validates the whole document,
// including redacted subtrees, before exposing any output to a caller.
func (p *Policy) Project(ctx context.Context, input []byte) (SafeJSON, error) {
	if p == nil || p.root.spec == nil {
		return SafeJSON{}, ErrPolicy
	}
	if err := contextError(ctx); err != nil {
		return SafeJSON{}, err
	}
	if len(input) > p.limits.MaxInputBytes {
		return SafeJSON{}, ErrLimit
	}
	if !utf8.Valid(input) {
		return SafeJSON{}, ErrBlocked
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	parser := parser{ctx: ctx, decoder: d, limits: p.limits}
	value, err := parser.read(p.root, 1)
	if err != nil {
		return SafeJSON{}, err
	}
	if _, err := d.Token(); err != io.EOF {
		return SafeJSON{}, ErrBlocked
	}
	if err := contextError(ctx); err != nil {
		return SafeJSON{}, err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return SafeJSON{}, ErrBlocked
	}
	if len(body) > p.limits.MaxOutputBytes {
		return SafeJSON{}, ErrLimit
	}
	if err := contextError(ctx); err != nil {
		return SafeJSON{}, err
	}
	return SafeJSON{body: body}, nil
}

type parser struct {
	ctx     context.Context
	decoder *json.Decoder
	limits  Limits
}

func (p *parser) token() (json.Token, error) {
	if err := contextError(p.ctx); err != nil {
		return nil, err
	}
	token, err := p.decoder.Token()
	if err != nil {
		// JSON errors can quote raw bytes and offsets, so never wrap them.
		return nil, ErrBlocked
	}
	if text, ok := token.(string); ok && strings.ContainsRune(text, utf8.RuneError) {
		// encoding/json replaces invalid UTF-16 escapes instead of rejecting them.
		return nil, ErrBlocked
	}
	return token, nil
}

func (p *parser) read(rule Rule, depth int) (any, error) {
	if depth > p.limits.MaxDepth {
		return nil, ErrLimit
	}
	token, err := p.token()
	if err != nil {
		return nil, err
	}
	s := rule.spec
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			if s.kind == objectKind || s.kind == redactKind {
				return p.object(rule, depth)
			}
		case '[':
			if s.kind == arrayKind || s.kind == redactKind {
				return p.array(rule, depth)
			}
		}
		return nil, ErrBlocked
	}
	if s.kind == redactKind {
		return Redacted, nil
	}
	switch s.kind {
	case boolKind:
		if value, ok := token.(bool); ok {
			return value, nil
		}
	case integerKind:
		if number, ok := token.(json.Number); ok {
			value, err := number.Int64()
			if err == nil && value >= s.min && value <= s.max {
				return value, nil
			}
		}
	case enumKind:
		if value, ok := token.(string); ok {
			for _, allowed := range s.values {
				if value == allowed {
					return value, nil
				}
			}
		}
	}
	return nil, ErrBlocked
}

func (p *parser) object(rule Rule, depth int) (any, error) {
	redact := rule.spec.kind == redactKind
	seen := make(map[string]bool)
	var result map[string]any
	if !redact {
		result = make(map[string]any)
	}
	for p.decoder.More() {
		if len(seen) >= p.limits.MaxObjectFields {
			return nil, ErrLimit
		}
		token, err := p.token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return nil, ErrBlocked
		}
		seen[name] = true
		child := rule
		if !redact {
			child, ok = rule.spec.fields[name]
			if !ok {
				return nil, ErrBlocked
			}
		}
		value, err := p.read(child, depth+1)
		if err != nil {
			return nil, err
		}
		if !redact {
			result[name] = value
		}
	}
	if token, err := p.token(); err != nil {
		return nil, err
	} else if token != json.Delim('}') {
		return nil, ErrBlocked
	}
	if redact {
		return Redacted, nil
	}
	return result, nil
}

func (p *parser) array(rule Rule, depth int) (any, error) {
	redact := rule.spec.kind == redactKind
	result := make([]any, 0)
	for count := 0; p.decoder.More(); count++ {
		if count >= p.limits.MaxArrayItems {
			return nil, ErrLimit
		}
		child := rule
		if !redact {
			child = rule.spec.item
		}
		value, err := p.read(child, depth+1)
		if err != nil {
			return nil, err
		}
		if !redact {
			result = append(result, value)
		}
	}
	if token, err := p.token(); err != nil {
		return nil, err
	} else if token != json.Delim(']') {
		return nil, ErrBlocked
	}
	if redact {
		return Redacted, nil
	}
	return result, nil
}

// hiddenError keeps errors.Is/As usable without exposing an underlying error's
// text through ordinary logging. Callers must not log the unwrapped cause.
type hiddenError struct {
	public error
	cause  error
}

func (e *hiddenError) Error() string        { return e.public.Error() }
func (e *hiddenError) Unwrap() error        { return e.cause }
func (e *hiddenError) Is(target error) bool { return target == e.public }

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrBlocked
	}
	if err := ctx.Err(); err != nil {
		return &hiddenError{public: ErrBlocked, cause: err}
	}
	return nil
}

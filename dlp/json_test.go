package dlp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/dlp"
)

func limits() dlp.Limits {
	return dlp.Limits{MaxInputBytes: 8192, MaxOutputBytes: 8192, MaxDepth: 8, MaxObjectFields: 32, MaxArrayItems: 16}
}

func fields() map[string]dlp.Rule {
	return map[string]dlp.Rule{
		"status": dlp.Integer(100, 599), "sent": dlp.Bool(),
		"decision": dlp.Enum("allow", "block"),
		"headers":  dlp.Object(map[string]dlp.Rule{"Authorization": dlp.Redact(), "Cookie": dlp.Redact()}),
		"body":     dlp.Redact(), "email": dlp.Redact(),
		"attempts": dlp.Array(dlp.Object(map[string]dlp.Rule{"status": dlp.Integer(100, 599), "error": dlp.Redact()})),
	}
}

func policy(t *testing.T) *dlp.Policy {
	t.Helper()
	p, err := dlp.New(limits(), fields())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectionPreservesMetadataAndRedactsWholeSensitiveValues(t *testing.T) {
	input := []byte(`{"status":200,"sent":true,"decision":"allow","headers":{"Authorization":"Bearer fixture-secret","Cookie":"session=fixture-cookie"},"email":"fixture@example.invalid","body":{"nested":["fixture-response",{"password":"fixture-password"}]},"attempts":[{"status":503,"error":"fixture-error-secret"}]}`)
	original := bytes.Clone(input)
	safe, err := policy(t).Project(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"attempts":[{"error":"[REDACTED]","status":503}],"body":"[REDACTED]","decision":"allow","email":"[REDACTED]","headers":{"Authorization":"[REDACTED]","Cookie":"[REDACTED]"},"sent":true,"status":200}`
	if string(safe.Bytes()) != want {
		t.Fatal("unexpected projected metadata")
	}
	if !bytes.Equal(input, original) {
		t.Fatal("projection mutated source evidence")
	}
	copy := safe.Bytes()
	copy[0] = '!'
	if string(safe.Bytes()) != want {
		t.Fatal("result exposes its mutable backing storage")
	}
}

func TestBlockedInputNeverReachesWriter(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"fixture-secret-key":"fixture-secret"}`,
		"nested unknown":  `{"headers":{"X-Unknown":"fixture-secret"}}`,
		"metadata secret": `{"decision":"fixture-secret"}`,
		"status string":   `{"status":"fixture-secret"}`,
		"status float":    `{"status":200.0}`,
		"status exponent": `{"status":2e2}`,
		"status low":      `{"status":99}`, "status high": `{"status":600}`,
		"status overflow": `{"status":9223372036854775808}`,
		"status null":     `{"status":null}`, "sent number": `{"sent":1}`,
		"root array": `[]`, "root string": `"fixture-secret"`, "root null": `null`,
		"object mismatch":                 `{"headers":"fixture-secret"}`,
		"array mismatch":                  `{"attempts":{}}`,
		"array item mismatch":             `{"attempts":[false]}`,
		"duplicate":                       `{"status":200,"status":201}`,
		"escaped duplicate":               `{"status":200,"\u0073tatus":201}`,
		"redacted duplicate":              `{"body":{"x":1,"x":2}}`,
		"duplicate inside redacted array": `{"body":[{"x":1,"\u0078":2}]}`,
		"trailing":                        `{"status":200} {"body":"fixture-secret"}`,
		"trailing junk":                   `{"status":200}fixture-secret`,
		"malformed":                       `{"body":"fixture-secret"`,
		"malformed redacted array":        `{"body":[1,]}`,
		"malformed redacted object":       `{"body":{"a":}}`,
		"unexpected case":                 `{"Status":200}`,
		"empty":                           ``, "invalid UTF8": "{\"body\":\"\xff\"}",
		"lone surrogate":               `{"body":"\ud800"}`,
		"lone low surrogate":           `{"body":"\udc00"}`,
		"invalid nested surrogate key": `{"body":{"\ud800":1}}`,
	}
	p := policy(t)
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			safe, err := p.Project(context.Background(), []byte(input))
			if !errors.Is(err, dlp.ErrBlocked) {
				t.Fatalf("want blocked, got %v", err)
			}
			if len(safe.Bytes()) != 0 || strings.Contains(fmt.Sprintf("%v %+v", err, err), "fixture-secret") {
				t.Fatal("blocked result or error exposes input")
			}
			w := &countWriter{}
			if _, err := safe.Write(context.Background(), w); err == nil || w.calls != 0 {
				t.Fatal("blocked result reached writer")
			}
		})
	}
}

func TestPolicyRejectsInvalidAndSensitiveOverrideRules(t *testing.T) {
	cases := map[string]map[string]dlp.Rule{
		"zero rule":           {"status": {}},
		"bad integer range":   {"status": dlp.Integer(200, 100)},
		"empty enum":          {"decision": dlp.Enum()},
		"duplicate enum":      {"decision": dlp.Enum("allow", "allow")},
		"invalid UTF8 enum":   {"decision": dlp.Enum("\xff")},
		"control enum":        {"decision": dlp.Enum("allow\n")},
		"zero array item":     {"items": dlp.Array(dlp.Rule{})},
		"empty field":         {"": dlp.Redact()},
		"numeric field":       {"1field": dlp.Redact()},
		"unsafe field":        {"fixture@example.invalid": dlp.Redact()},
		"unicode field":       {"pаssword": dlp.Redact()},
		"case collision":      {"status": dlp.Bool(), "Status": dlp.Bool()},
		"separator collision": {"status_code": dlp.Bool(), "status-code": dlp.Bool()},
		"password override":   {"password": dlp.Enum("fixture-secret")},
		"nested override":     {"nested": dlp.Object(map[string]dlp.Rule{"Authorization": dlp.Enum("fixture-secret")})},
		"array override":      {"items": dlp.Array(dlp.Object(map[string]dlp.Rule{"email": dlp.Enum("fixture-secret")}))},
		"body override":       {"body": dlp.Object(map[string]dlp.Rule{})},
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := dlp.New(limits(), rules)
			if p != nil || !errors.Is(err, dlp.ErrPolicy) || strings.Contains(fmt.Sprint(err), "fixture-secret") {
				t.Fatal("invalid policy was accepted or disclosed configuration")
			}
		})
	}
	for _, key := range []string{"Authorization", "proxy_authorization", "COOKIE", "Set-Cookie", "api_key", "accessToken", "refresh_token", "Password", "pass_word", "email", "phone_number", "account_id", "username", "card_number", "ssn", "raw_request", "raw-response", "body", "messages", "error"} {
		t.Run(key, func(t *testing.T) {
			if _, err := dlp.New(limits(), map[string]dlp.Rule{key: dlp.Bool()}); !errors.Is(err, dlp.ErrPolicy) {
				t.Fatal("sensitive field can be promoted to metadata")
			}
		})
	}
}

func TestPolicySnapshotsHostConfiguration(t *testing.T) {
	values := []string{"allow"}
	nested := map[string]dlp.Rule{"decision": dlp.Enum(values...)}
	object := dlp.Object(nested)
	values[0] = "fixture-secret"
	nested["decision"] = dlp.Enum("fixture-secret")
	root := map[string]dlp.Rule{"meta": object}
	l := limits()
	p, err := dlp.New(l, root)
	if err != nil {
		t.Fatal(err)
	}
	root["meta"] = dlp.Redact()
	l.MaxInputBytes = 1
	got, err := p.Project(context.Background(), []byte(`{"meta":{"decision":"allow"}}`))
	if err != nil || string(got.Bytes()) != `{"meta":{"decision":"allow"}}` {
		t.Fatal("policy changed with caller-owned configuration")
	}
	if _, err := p.Project(context.Background(), []byte(`{"meta":{"decision":"fixture-secret"}}`)); !errors.Is(err, dlp.ErrBlocked) {
		t.Fatal("mutated enum became trusted")
	}
}

func TestLimitsApplyInsideRedactedSubtrees(t *testing.T) {
	cases := []struct {
		name   string
		change func(*dlp.Limits)
		input  string
	}{
		{"input", func(l *dlp.Limits) { l.MaxInputBytes = 2 }, `{"body":"fixture-secret"}`},
		{"output", func(l *dlp.Limits) { l.MaxOutputBytes = 12 }, `{"body":null}`},
		{"depth", func(l *dlp.Limits) { l.MaxDepth = 2 }, `{"body":["fixture-secret"]}`},
		{"object fields", func(l *dlp.Limits) { l.MaxObjectFields = 1 }, `{"body":{"a":1,"b":2}}`},
		{"array items", func(l *dlp.Limits) { l.MaxArrayItems = 1 }, `{"body":[1,2]}`},
		{"allowed array items", func(l *dlp.Limits) { l.MaxArrayItems = 1 }, `{"attempts":[{"status":200},{"status":201}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := limits()
			tc.change(&l)
			p, err := dlp.New(l, fields())
			// Schema depth is independent of incoming document depth.
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Project(context.Background(), []byte(tc.input))
			if !errors.Is(err, dlp.ErrLimit) || len(got.Bytes()) != 0 {
				t.Fatalf("limit did not block output: %v", err)
			}
		})
	}
}

func TestRequiredPositiveLimits(t *testing.T) {
	for _, change := range []func(*dlp.Limits){
		func(l *dlp.Limits) { l.MaxInputBytes = 0 }, func(l *dlp.Limits) { l.MaxOutputBytes = 0 },
		func(l *dlp.Limits) { l.MaxDepth = 0 }, func(l *dlp.Limits) { l.MaxObjectFields = 0 },
		func(l *dlp.Limits) { l.MaxArrayItems = -1 },
	} {
		l := limits()
		change(&l)
		if p, err := dlp.New(l, fields()); p != nil || !errors.Is(err, dlp.ErrPolicy) {
			t.Fatal("missing limit did not invalidate policy")
		}
	}
}

func TestZeroPolicyAndCanceledContextsBlock(t *testing.T) {
	for _, p := range []*dlp.Policy{nil, {}} {
		if got, err := p.Project(context.Background(), []byte(`{}`)); !errors.Is(err, dlp.ErrPolicy) || len(got.Bytes()) != 0 {
			t.Fatal("zero policy permits output")
		}
	}
	p := policy(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := p.Project(ctx, []byte(`{}`)); !errors.Is(err, context.Canceled) || len(got.Bytes()) != 0 {
		t.Fatal("canceled projection produced output")
	}
	if _, err := p.Project(nil, []byte(`{}`)); !errors.Is(err, dlp.ErrBlocked) {
		t.Fatal("missing context permits output")
	}
	safe, err := p.Project(context.Background(), []byte(`{"status":200}`))
	if err != nil {
		t.Fatal(err)
	}
	w := &countWriter{}
	if _, err := safe.Write(ctx, w); !errors.Is(err, context.Canceled) || w.calls != 0 {
		t.Fatal("canceled result reached writer")
	}
	if _, err := safe.Write(nil, w); !errors.Is(err, dlp.ErrBlocked) || w.calls != 0 {
		t.Fatal("missing write context permits output")
	}
}

type countWriter struct {
	calls int
	data  []byte
	short bool
	err   error
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.calls++
	w.data = bytes.Clone(p)
	if w.short {
		return len(p) - 1, w.err
	}
	return len(p), w.err
}

func TestWriterReceivesOnlyCompleteProjection(t *testing.T) {
	safe, err := policy(t).Project(context.Background(), []byte(`{"status":200,"body":"fixture-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	w := &countWriter{}
	n, err := safe.Write(context.Background(), w)
	if err != nil || n != len(w.data) || w.calls != 1 || !json.Valid(w.data) || bytes.Contains(w.data, []byte("fixture-secret")) {
		t.Fatal("writer did not receive one safe document")
	}
	if _, err := safe.Write(context.Background(), nil); !errors.Is(err, dlp.ErrWrite) {
		t.Fatal("nil writer was accepted")
	}
	for _, tc := range []struct {
		short bool
		cause error
	}{
		{true, nil}, {true, errors.New("fixture-secret-writer-error")}, {false, errors.New("fixture-secret-writer-error")},
	} {
		w := &countWriter{short: tc.short, err: tc.cause}
		_, err := safe.Write(context.Background(), w)
		cause := tc.cause
		if cause == nil {
			cause = io.ErrShortWrite
		}
		if !errors.Is(err, dlp.ErrWrite) || !errors.Is(err, cause) || w.calls != 1 || strings.Contains(fmt.Sprintf("%v %+v", err, err), "fixture-secret") {
			t.Fatal("writer failure was swallowed, retried, or disclosed")
		}
	}
}

func TestRedactAcceptsEveryJSONValueWithoutLeakingIt(t *testing.T) {
	p := policy(t)
	for _, value := range []string{`null`, `false`, `123`, `1.25`, `"fixture-secret"`, `[]`, `{}`, `["fixture-secret",{"x":"fixture-secret"}]`, `"\ud83d\ude00"`} {
		t.Run(value, func(t *testing.T) {
			safe, err := p.Project(context.Background(), []byte(`{"body":`+value+`}`))
			if err != nil || string(safe.Bytes()) != `{"body":"[REDACTED]"}` {
				t.Fatal("sensitive JSON value was not fully redacted")
			}
		})
	}
}

func TestPolicyCanBeSharedAcrossConcurrentCalls(t *testing.T) {
	p := policy(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			safe, err := p.Project(context.Background(), []byte(`{"status":200,"body":"fixture-secret"}`))
			if err != nil || string(safe.Bytes()) != `{"body":"[REDACTED]","status":200}` {
				t.Error("shared policy yielded an unsafe result")
			}
		}()
	}
	wg.Wait()
}

package dlp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/dlp"
)

func TestAllLimitsPermitExactBoundary(t *testing.T) {
	input := []byte(`{"body":[1]}`)
	want := `{"body":"[REDACTED]"}`
	p, err := dlp.New(dlp.Limits{
		MaxInputBytes: len(input), MaxOutputBytes: len(want), MaxDepth: 3,
		MaxObjectFields: 1, MaxArrayItems: 1,
	}, map[string]dlp.Rule{"body": dlp.Redact()})
	if err != nil {
		t.Fatal(err)
	}
	safe, err := p.Project(context.Background(), input)
	if err != nil || string(safe.Bytes()) != want {
		t.Fatal("exactly bounded document was blocked")
	}
}

func TestPolicySchemaIsBoundedIndependentlyOfInput(t *testing.T) {
	rule := dlp.Bool()
	for i := 0; i < 100; i++ {
		rule = dlp.Object(map[string]dlp.Rule{"nested": rule})
	}
	if p, err := dlp.New(limits(), map[string]dlp.Rule{"nested": rule}); p != nil || !errors.Is(err, dlp.ErrPolicy) {
		t.Fatal("unbounded host schema was accepted")
	}
}

type cancelDuringParse struct {
	context.Context
	checks int
}

func (ctx *cancelDuringParse) Err() error {
	ctx.checks++
	if ctx.checks > 2 {
		return context.Canceled
	}
	return nil
}

func TestCancellationDuringParsingAndExpiredDeadline(t *testing.T) {
	p := policy(t)
	ctx := &cancelDuringParse{Context: context.Background()}
	if safe, err := p.Project(ctx, []byte(`{"body":{"nested":[1,2,3]}}`)); !errors.Is(err, context.Canceled) || len(safe.Bytes()) != 0 {
		t.Fatal("cancellation during parsing produced output")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	if safe, err := p.Project(expired, []byte(`{}`)); !errors.Is(err, context.DeadlineExceeded) || len(safe.Bytes()) != 0 {
		t.Fatal("expired deadline produced output")
	}
}

func FuzzProjectionNeverFallsBackToInput(f *testing.F) {
	p, err := dlp.New(limits(), fields())
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{
		`{}`, `{"status":200,"sent":false,"decision":"block"}`,
		`{"body":{"password":"fixture-secret"}}`, `{"body":[{"x":1,"x":2}]}`,
		`{"decision":"fixture-secret"}`, `{"status":200} {}`, "{\"body\":\"\xff\"}",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		safe, err := p.Project(context.Background(), input)
		if err != nil {
			if len(safe.Bytes()) != 0 {
				t.Fatal("failed projection returned data")
			}
			return
		}
		if !json.Valid(safe.Bytes()) || len(safe.Bytes()) > limits().MaxOutputBytes || strings.Contains(string(safe.Bytes()), "fixture-secret") {
			t.Fatal("projection is invalid, oversized, or contains a sensitive fixture")
		}
		again, err := p.Project(context.Background(), safe.Bytes())
		if err != nil || string(again.Bytes()) != string(safe.Bytes()) {
			t.Fatal("projection is not stable when applied again")
		}
	})
}

package notify

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestSetLanguageResolverAllowsReentrantReplacement(t *testing.T) {
	if os.Getenv("ARTEX_REENTRANT_NOTIFY_CHILD") == "1" {
		replacement := func() string { return "en" }
		SetLanguageResolver(func() string {
			previous := SetLanguageResolver(replacement)
			if previous == nil {
				t.Error("reentrant replacement lost the active resolver")
			}
			return "en"
		})
		if got := SeverityLabel("high"); got != "🟠 High" {
			t.Fatalf("SeverityLabel(high) = %q, want English label", got)
		}
		if got := SeverityLabel("high"); got != "🟠 High" {
			t.Fatalf("SeverityLabel(high) after replacement = %q, want English label", got)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSetLanguageResolverAllowsReentrantReplacement$", "-test.v")
	cmd.Env = append(os.Environ(), "ARTEX_REENTRANT_NOTIFY_CHILD=1")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("reentrant notify resolver child timed out: %v", ctx.Err())
		}
		t.Fatalf("reentrant notify resolver child failed: %v", err)
	}
	return
}

func TestLanguageResolverConcurrentReplaceAndRead(t *testing.T) {
	old := SetLanguageResolver(func() string { return "ko" })
	defer SetLanguageResolver(old)

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			SetLanguageResolver(func() string { return "en" })
			SetLanguageResolver(func() string { return "ko" })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations*2; i++ {
			if got := SeverityLabel("high"); got != "🟠 High" && got != "🟠 높음" {
				t.Errorf("SeverityLabel(high) = %q, want English or Korean label", got)
				return
			}
		}
	}()
	wg.Wait()
}

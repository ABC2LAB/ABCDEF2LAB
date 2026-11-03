package config

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestSetCurrentLanguageAllowsReentrantReplacement(t *testing.T) {
	if os.Getenv("ARTEX_REENTRANT_LANGUAGE_CHILD") == "1" {
		replacement := func() string { return "en" }
		SetCurrentLanguage(func() string {
			previous := SetCurrentLanguage(replacement)
			if previous == nil {
				t.Error("reentrant replacement lost the active resolver")
			}
			return "en"
		})
		if got := ActiveLanguage(); got != "en" {
			t.Fatalf("ActiveLanguage() = %q, want en", got)
		}
		if got := ActiveLanguage(); got != "en" {
			t.Fatalf("ActiveLanguage() after replacement = %q, want en", got)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSetCurrentLanguageAllowsReentrantReplacement$", "-test.v")
	cmd.Env = append(os.Environ(), "ARTEX_REENTRANT_LANGUAGE_CHILD=1")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("reentrant resolver child timed out: %v", ctx.Err())
		}
		t.Fatalf("reentrant resolver child failed: %v", err)
	}
}

func TestCurrentLanguageConcurrentReplaceAndRead(t *testing.T) {
	old := SetCurrentLanguage(func() string { return "ko" })
	defer SetCurrentLanguage(old)

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			SetCurrentLanguage(func() string { return "en" })
			SetCurrentLanguage(func() string { return "ko" })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations*2; i++ {
			if got := ActiveLanguage(); got != "en" && got != "ko" {
				t.Errorf("ActiveLanguage() = %q, want en or ko", got)
				return
			}
		}
	}()
	wg.Wait()
}

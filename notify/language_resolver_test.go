package notify

import (
	"sync"
	"testing"
)

func TestSetLanguageResolverAllowsReentrantReplacement(t *testing.T) {
	old := SetLanguageResolver(nil)
	defer SetLanguageResolver(old)

	replacement := func() string { return "en" }
	called := false
	SetLanguageResolver(func() string {
		called = true
		previous := SetLanguageResolver(replacement)
		if previous == nil {
			t.Error("reentrant replacement lost the active resolver")
		}
		return "en"
	})

	if got := SeverityLabel("high"); got != "🟠 High" {
		t.Fatalf("SeverityLabel(high) = %q, want English label", got)
	}
	if !called {
		t.Fatal("resolver was not called")
	}
	if got := SeverityLabel("high"); got != "🟠 High" {
		t.Fatalf("SeverityLabel(high) after replacement = %q, want English label", got)
	}
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

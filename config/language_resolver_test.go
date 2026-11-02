package config

import (
	"sync"
	"testing"
)

func TestSetCurrentLanguageAllowsReentrantReplacement(t *testing.T) {
	old := SetCurrentLanguage(nil)
	defer SetCurrentLanguage(old)

	replacement := func() string { return "en" }
	called := false
	SetCurrentLanguage(func() string {
		called = true
		previous := SetCurrentLanguage(replacement)
		if previous == nil {
			t.Error("reentrant replacement lost the active resolver")
		}
		return "en"
	})

	if got := ActiveLanguage(); got != "en" {
		t.Fatalf("ActiveLanguage() = %q, want en", got)
	}
	if !called {
		t.Fatal("resolver was not called")
	}
	if got := ActiveLanguage(); got != "en" {
		t.Fatalf("ActiveLanguage() after replacement = %q, want en", got)
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

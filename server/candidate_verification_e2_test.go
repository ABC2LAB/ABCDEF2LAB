package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestSelectedCandidateGETRejectsUnboundCommand(t *testing.T) {
	for _, value := range []string{
		"https://example.test/a?redirect=/private",
		"https://example.test/a;id=1",
		"https://example.test/a$(touch /tmp/unsafe)",
		"https://example.test/a`id`",
		"https://example.test/a/{resource}",
		"https://example.test/a%2fb",
		"https://example.test/a/../b",
		"https://example.test/a b",
	} {
		if candidateFixedGETURL.MatchString(value) {
			t.Errorf("unsafe fixed command URL accepted: %q", value)
		}
	}
	if !candidateFixedGETURL.MatchString("https://example.test/documents") || !candidateFixedGETURL.MatchString("https://example.test/") {
		t.Fatal("safe exact GET path rejected")
	}
	if _, err := (*E2HTTPRuntime)(nil).runSelectedCandidateGET(context.Background(), &Task{}, &db.Node{Kind: db.KindIntent}, "https://example.test/"); err == nil {
		t.Fatal("unbound E2 runtime accepted candidate execution")
	}
}

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestProductPasswordHashUsesStdinAndDoesNotExposeSecret(t *testing.T) {
	secret := "synthetic-reset-only-password"
	var out, diagnostic bytes.Buffer
	if code := runProductPasswordHash(t.Context(), nil, strings.NewReader(secret), &out, &diagnostic); code != 0 || diagnostic.Len() != 0 {
		t.Fatal("hash failed", code)
	}
	if strings.Contains(out.String(), secret) || bcrypt.CompareHashAndPassword([]byte(strings.TrimSpace(out.String())), []byte(secret)) != nil {
		t.Fatal("invalid or exposed secret")
	}
}
func TestProductPasswordHashRejectsInvalidInputWithoutOutput(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name, password string
		args           []string
		ctx            context.Context
	}{
		{"short", "short", nil, t.Context()}, {"bcrypt_limit", strings.Repeat("x", 73), nil, t.Context()}, {"newline", strings.Repeat("x", 12) + "\n", nil, t.Context()}, {"argv", strings.Repeat("x", 12), []string{"unexpected"}, t.Context()}, {"cancelled", strings.Repeat("x", 12), nil, cancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if runProductPasswordHash(tc.ctx, tc.args, strings.NewReader(tc.password), &out, &diagnostic) == 0 || out.Len() != 0 || diagnostic.String() != "product_password_hash_unavailable\n" {
				t.Fatal("invalid input accepted or exposed")
			}
		})
	}
}

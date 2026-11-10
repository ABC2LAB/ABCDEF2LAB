package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// The reset script supplies the secret through stdin, never argv or container env.
// This command has no database or installation side effects.
func runProductPasswordHash(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func() int { fmt.Fprintln(stderr, "product_password_hash_unavailable"); return 1 }
	if len(args) != 0 || ctx == nil || ctx.Err() != nil || stdin == nil {
		return fail()
	}
	password, err := io.ReadAll(io.LimitReader(stdin, 73))
	defer clear(password)
	if err != nil || len(password) < 12 || len(password) > 72 || strings.ContainsAny(string(password), "\x00\r\n") || ctx.Err() != nil {
		return fail()
	}
	hash, err := bcrypt.GenerateFromPassword(password, 12)
	defer clear(hash)
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	if _, err = fmt.Fprintln(stdout, string(hash)); err != nil {
		return fail()
	}
	return 0
}

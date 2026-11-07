//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func localBindingFixture(t *testing.T) ControlledReadLocalBinding {
	t.Helper()
	base := t.TempDir()
	pins := map[string]ControlledReadLocalDirectory{}
	for _, name := range []string{"state", "state/journals", "descriptors", "keys", "inputs"} {
		path := filepath.Join(base, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		pin, err := ControlledReadLocalDirectoryIdentity(path)
		if err != nil {
			t.Fatal(err)
		}
		pins[name] = pin
	}
	key := bytes.Repeat([]byte{0x63}, auditlog.KeyBytes)
	keyFile := filepath.Join(pins["keys"].Path, "audit.key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(key)
	return ControlledReadLocalBinding{Version: 1, StoreRef: "store-test", EpochRef: "epoch-test", Generation: 1, PublisherRef: "publisher-test", ExpiresAt: time.Now().UTC().Add(10 * time.Minute), State: pins["state"], Journals: pins["state/journals"], Descriptors: pins["descriptors"], Keys: pins["keys"], Key: ControlledReadLocalKey{Namespace: "keys-test", PolicyRef: "key-policy", KeyRef: "key-test", SecretVersionRef: "version-test", Identity: "opaque-identity", Revision: 2, File: keyFile, SHA256: hex.EncodeToString(digest[:])}, Execution: ControlledReadLocalExecution{TaskID: 41, IntentID: 73, CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}, PolicySHA256: []string{strings.Repeat("3", 64)}, Files: []ControlledReadLocalFile{{Path: filepath.Join(pins["inputs"].Path, "metadata.json"), SHA256: strings.Repeat("4", 64)}}}
}
func TestControlledLocalBindingRejectsUnapprovedRepresentationsAndScopes(t *testing.T) {
	base := localBindingFixture(t)
	now := time.Now()
	raw, _ := json.Marshal(base)
	if _, err := decodeControlledReadLocalBinding(raw, now); err != nil {
		t.Fatal("valid installed-format fixture rejected")
	}
	cases := map[string]func(*ControlledReadLocalBinding){
		"version":               func(b *ControlledReadLocalBinding) { b.Version = 2 },
		"expired":               func(b *ControlledReadLocalBinding) { b.ExpiresAt = now.Add(-time.Second) },
		"unbounded":             func(b *ControlledReadLocalBinding) { b.ExpiresAt = now.Add(16 * time.Minute) },
		"placeholder task":      func(b *ControlledReadLocalBinding) { b.Execution.TaskID = 0 },
		"placeholder intent":    func(b *ControlledReadLocalBinding) { b.Execution.IntentID = 0 },
		"key namespace overlap": func(b *ControlledReadLocalBinding) { b.Keys = b.State },
		"unpinned key":          func(b *ControlledReadLocalBinding) { b.Key.SHA256 = "" },
		"secret identity url":   func(b *ControlledReadLocalBinding) { b.Key.Identity = "https://secret.invalid" },
		"duplicate policy":      func(b *ControlledReadLocalBinding) { b.PolicySHA256 = append(b.PolicySHA256, b.PolicySHA256[0]) },
		"duplicate input":       func(b *ControlledReadLocalBinding) { b.Files = append(b.Files, b.Files[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := base
			b.PolicySHA256 = append([]string(nil), base.PolicySHA256...)
			b.Files = append([]ControlledReadLocalFile(nil), base.Files...)
			mutate(&b)
			data, _ := json.Marshal(b)
			if _, err := decodeControlledReadLocalBinding(data, now); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	for _, data := range [][]byte{append(append([]byte(nil), raw...), '\n'), append([]byte(" "), raw...), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"root_trusted":true`), 1)} {
		if _, err := decodeControlledReadLocalBinding(data, now); err == nil {
			t.Fatal("noncanonical binding accepted")
		}
	}
}
func TestControlledLocalPhysicalExcludesSecondWriterAndPermanentReplay(t *testing.T) {
	b := localBindingFixture(t)
	digest := sha256.Sum256([]byte("installer anchor fixture"))
	p, err := openControlledLocalPhysical(b, digest)
	if err != nil {
		t.Fatal(err)
	}
	if checkControlledLocalPhysical(p, b) != nil {
		t.Fatal("actual held root failed")
	}
	if second, e := openControlledLocalPhysical(b, digest); e == nil {
		_ = closeControlledLocalPhysical(second)
		t.Fatal("second writer accepted")
	}
	if err = closeControlledLocalPhysical(p); err != nil {
		t.Fatal(err)
	}
	if replay, e := openControlledLocalPhysical(b, digest); e == nil {
		_ = closeControlledLocalPhysical(replay)
		t.Fatal("consumed startup authority replayed")
	}
}
func TestControlledLocalPhysicalDetectsNamespaceReplacement(t *testing.T) {
	for _, replace := range []string{"state", "parent", "marker", "special-mode"} {
		t.Run(replace, func(t *testing.T) {
			b := localBindingFixture(t)
			p, err := openControlledLocalPhysical(b, sha256.Sum256([]byte(replace)))
			if err != nil {
				t.Fatal(err)
			}
			defer closeControlledLocalPhysical(p)
			switch replace {
			case "state":
				if os.Rename(b.State.Path, b.State.Path+".old") != nil || os.Mkdir(b.State.Path, 0700) != nil {
					t.Fatal("replace state")
				}
			case "parent":
				parent := filepath.Dir(b.State.Path)
				if os.Rename(parent, parent+".old") != nil || os.Symlink(parent+".old", parent) != nil {
					t.Fatal("replace parent")
				}
				defer os.Remove(parent)
				defer os.RemoveAll(parent + ".old")
			case "special-mode":
				if os.Chmod(b.State.Path, os.ModeSetgid|0700) != nil {
					t.Fatal("set special mode")
				}
			case "marker":
				if os.Remove(filepath.Join(b.Keys.Path, p.writer.Name())) != nil {
					t.Fatal("remove marker")
				}
			}
			if checkControlledLocalPhysical(p, b) == nil {
				t.Fatal("namespace continuity falsely retained")
			}
		})
	}
}

type localTestSupervisor struct {
	authorize func(context.Context, transmit.Scope, int64) error
	quiescent func(context.Context, int64) error
}

func (s localTestSupervisor) AuthorizeExecution(ctx context.Context, scope transmit.Scope, intent int64) error {
	if s.authorize == nil {
		return errors.New("missing test authority")
	}
	return s.authorize(ctx, scope, intent)
}
func (s localTestSupervisor) CheckTaskQuiescent(ctx context.Context, id int64) error {
	if s.quiescent == nil {
		return errors.New("missing test quiescence")
	}
	return s.quiescent(ctx, id)
}

// 아래 fixture는 실제 FD와 수명만 시험한다. 설치/DB 신뢰 수용은 별도 PostgreSQL 시험이다.
func localHostLifetimeFixture(t *testing.T, supervisor ControlledReadLocalSupervisor) *ControlledReadLocalHost {
	t.Helper()
	b := localBindingFixture(t)
	p, err := openControlledLocalPhysical(b, sha256.Sum256([]byte("lifetime fixture")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &ControlledReadLocalHost{ctx: ctx, cancel: cancel, binding: b, physical: p, supervisor: supervisor, key: bytes.Repeat([]byte{0x19}, 32), drained: make(chan struct{}), cleanupDone: make(chan struct{})}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("fixture retained resources: %v", err)
		}
	})
	return h
}
func localFixtureScope() transmit.Scope {
	return transmit.Scope{TaskRef: "41", RunRef: "e2-03abc_9", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
}
func TestControlledLocalAuthorityExactScopeInputAndOneSources(t *testing.T) {
	h := localHostLifetimeFixture(t, localTestSupervisor{authorize: func(context.Context, transmit.Scope, int64) error { return nil }})
	ctx := context.Background()
	s := localFixtureScope()
	if h.AuthorizeExecution(ctx, s, 73) != nil {
		t.Fatal("actual generated-style run ref rejected")
	}
	for _, bad := range []transmit.Scope{{TaskRef: "42", RunRef: s.RunRef, CaseRef: s.CaseRef, ResetRef: s.ResetRef, PolicyRef: s.PolicyRef}, {TaskRef: s.TaskRef, RunRef: "other-run", CaseRef: s.CaseRef, ResetRef: s.ResetRef, PolicyRef: s.PolicyRef}, {TaskRef: s.TaskRef, RunRef: "../escape", CaseRef: s.CaseRef, ResetRef: s.ResetRef, PolicyRef: s.PolicyRef}} {
		if h.AuthorizeExecution(ctx, bad, 73) == nil {
			t.Fatal("scope drift accepted")
		}
	}
	var policy, input [32]byte
	copy(policy[:], bytes.Repeat([]byte{0x33}, 32))
	copy(input[:], bytes.Repeat([]byte{0x44}, 32))
	if h.AuthorizePolicy(ctx, policy) != nil || h.AuthorizeInput(ctx, h.binding.Files[0].Path, input) != nil {
		t.Fatal("installed pins rejected")
	}
	if h.AuthorizePolicy(ctx, input) == nil || h.AuthorizeInput(ctx, h.binding.Files[0].Path+".other", input) == nil {
		t.Fatal("unapproved bytes/path accepted")
	}
	sources, err := h.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Sources(); err == nil {
		t.Fatal("sources issued twice")
	}
	closeCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err = h.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("live source root released on timeout")
	}
	if _, err = sources.Root.Anchor().Stat(); err != nil {
		t.Fatal("pending root descriptor closed")
	}
	if bytes.Equal(h.key, make([]byte, 32)) {
		t.Fatal("pending borrowed key cleared")
	}
	sources.Root.Release()
	sources.Root.Release()
	finish, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if h.Close(finish) != nil {
		t.Fatal("released root failed to close")
	}
	if !bytes.Equal(h.key, make([]byte, 32)) {
		t.Fatal("key copy retained after final close")
	}
	if h.AuthorizeExecution(ctx, s, 73) == nil {
		t.Fatal("revoked execution reused")
	}
}
func TestControlledLocalPendingAuthorizationRetainsResources(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	h := localHostLifetimeFixture(t, localTestSupervisor{authorize: func(ctx context.Context, _ transmit.Scope, _ int64) error { close(entered); <-resume; return ctx.Err() }})
	result := make(chan error, 1)
	go func() { result <- h.AuthorizeExecution(context.Background(), localFixtureScope(), 73) }()
	<-entered
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := h.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pending authority callback not retained")
	}
	if _, err := h.Anchor().Stat(); err != nil {
		t.Fatal("pending callback root closed")
	}
	close(resume)
	if <-result == nil {
		t.Fatal("late authorization survived revocation")
	}
	finish, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if h.Close(finish) != nil {
		t.Fatal("pending ticket did not release")
	}
}

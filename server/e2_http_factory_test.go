package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/transmit"
)

type e2HTTPPolicyAuthorityFixture struct {
	ctx    context.Context
	digest [32]byte
	reject bool
	calls  int
}

func (a *e2HTTPPolicyAuthorityFixture) Context() context.Context        { return a.ctx }
func (a *e2HTTPPolicyAuthorityFixture) CheckRoot(context.Context) error { return nil }
func (a *e2HTTPPolicyAuthorityFixture) AuthorizePolicy(_ context.Context, digest [32]byte) error {
	a.calls++
	a.digest = digest
	if a.reject {
		return errors.New("private authority rejection")
	}
	return nil
}

type e2HTTPSupervisorFixture struct{}

func (*e2HTTPSupervisorFixture) AuthorizeExecution(context.Context, transmit.Scope, int64) error {
	return nil
}

func TestE2HTTPFileFactoryRequiresExplicitAuthorityAndSupervisor(t *testing.T) {
	var nilAuthority *e2HTTPPolicyAuthorityFixture
	var nilSupervisor *e2HTTPSupervisorFixture
	for _, options := range []E2HTTPFileOptions{{}, {Authority: nilAuthority, Supervisor: &e2HTTPSupervisorFixture{}}, {Authority: &e2HTTPPolicyAuthorityFixture{ctx: context.Background()}, Supervisor: nilSupervisor}} {
		if _, err := LoadE2HTTPRuntimeOptions(context.Background(), options); !errors.Is(err, ErrE2HTTPHost) {
			t.Fatal("missing or typed-nil host provider accepted")
		}
	}
}

func TestE2HTTPFileFactoryAuthorizesActualPolicyBytesBeforeParsing(t *testing.T) {
	raw := []byte("not a valid policy; private fixture marker")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, raw, 0600); err != nil {
		t.Fatal(err)
	}
	authority := &e2HTTPPolicyAuthorityFixture{ctx: context.Background(), reject: true}
	_, err := LoadE2HTTPRuntimeOptions(context.Background(), E2HTTPFileOptions{PolicyFile: policy, Authority: authority, Supervisor: &e2HTTPSupervisorFixture{}})
	if !errors.Is(err, ErrE2HTTPHost) || strings.Contains(err.Error(), "private") || authority.calls != 1 || authority.digest != sha256.Sum256(raw) {
		t.Fatal("policy authority was bypassed or source content disclosed")
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("rejected policy allocated an execution journal")
	}
}

func TestE2HTTPAcquiredWithholdsPrivateMaterialFromFormatting(t *testing.T) {
	acquired := E2HTTPAcquired{AuditKey: []byte("private-exporter-audit-key"), JournalDirectory: "private-host-directory", PolicyConfigSHA256: "private-config-digest"}
	data, err := json.Marshal(acquired)
	if err != nil || string(data) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v", acquired, acquired), "private-") {
		t.Fatal("privileged acquired input was serialized")
	}
}

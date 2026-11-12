package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// accountTestTarget은 DNS 증명·TLS 확인을 가짜로 둔 레지스트리에 대상 하나를 등록한다(approve면 승인까지).
func accountTestTarget(t *testing.T, approve bool) (*productTargetRegistry, string, diagnosisprep.ProductTarget) {
	t.Helper()
	r, root := targetTestRegistry(t)
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(24 * time.Hour), nil
	}
	target, err := r.register("ARTEX", targetTestSpec("a.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if approve {
		txt[target.ChallengeName] = []string{target.ChallengeValue}
		if target, err = r.approve(t.Context(), "ARTEX", target.ID); err != nil {
			t.Fatal(err)
		}
	}
	return r, root, target
}

func accountByLabel(t *testing.T, r *productTargetRegistry, id, label string) diagnosisprep.ProductAccount {
	t.Helper()
	list, err := r.listAccounts("ARTEX", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Label == label {
			return a
		}
	}
	t.Fatalf("account %s missing", label)
	return diagnosisprep.ProductAccount{}
}

// 계정은 real로 시작하고, 승인된 대상의 승인자가 진술에 동의해야 test가 된다. 대상 판이 바뀌면(철회) real로 돌아가고
// 사유는 target_reapproved, 표시를 떼면 manual_unmark다. 다시 열어도(저널 재생) 같은 상태다.
func TestProductAccountClassificationLifecycle(t *testing.T) {
	r, root, pending := accountTestTarget(t, false)
	if _, err := r.registerAccount("ARTEX", pending.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if a := accountByLabel(t, r, pending.ID, "tester-a"); a.Classification != "real" || a.Reason != "" || a.RegisteredBy != "ARTEX" {
		t.Fatalf("new account %+v", a)
	}
	if _, err := r.markTestAccount("ARTEX", pending.ID, "tester-a", true); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("pending target allowed a test mark: %v", err)
	}
	txt := map[string][]string{pending.ChallengeName: {pending.ChallengeValue}}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	approved, err := r.approve(t.Context(), "ARTEX", pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", false); err == nil || err.Error() != "statement_required" {
		t.Fatalf("mark without the statement: %v", err)
	}
	if _, err := r.markTestAccount("other", approved.ID, "tester-a", true); err == nil || err.Error() != "target_not_found" {
		t.Fatalf("another principal marked: %v", err)
	}
	if _, err := r.markTestAccount("ARTEX", approved.ID, "missing", true); err == nil || err.Error() != "account_not_found" {
		t.Fatalf("missing account marked: %v", err)
	}
	marked, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", true)
	if err != nil || marked.Classification != "test" || marked.MarkedRevision != approved.Revision || marked.ClassifiedBy != "ARTEX" || marked.ClassifiedAt.IsZero() {
		t.Fatalf("mark: %+v %v", marked, err)
	}
	// 같은 판에 다시 표시해도 새 기록을 만들지 않는다.
	before := accountJournalLines(t, root)
	if again, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", true); err != nil || again.Classification != "test" || accountJournalLines(t, root) != before {
		t.Fatalf("repeated mark: %+v %v", again, err)
	}
	if _, err := r.revoke("ARTEX", approved.ID); err != nil {
		t.Fatal(err)
	}
	if a := accountByLabel(t, r, approved.ID, "tester-a"); a.Classification != "real" || a.Reason != "target_reapproved" || a.MarkedRevision != approved.Revision {
		t.Fatalf("revoked target kept the test mark: %+v", a)
	}
	if _, err := r.markTestAccount("ARTEX", approved.ID, "tester-a", true); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("revoked target allowed a new mark: %v", err)
	}
	if _, err := r.registerAccount("ARTEX", approved.ID, "tester-b"); err == nil || err.Error() != "target_not_approved" {
		t.Fatalf("revoked target accepted a new account: %v", err)
	}
	unmarked, err := r.unmarkAccount("ARTEX", approved.ID, "tester-a")
	if err != nil || unmarked.Classification != "real" || unmarked.Reason != "manual_unmark" {
		t.Fatalf("unmark: %+v %v", unmarked, err)
	}
	// 다시 열면 저널 재생으로 같은 상태다.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if a := accountByLabel(t, reopened, approved.ID, "tester-a"); a.Classification != "real" || a.Reason != "manual_unmark" {
		t.Fatalf("replayed account %+v", a)
	}
}

// 표시를 뗀 계정은 real(manual_unmark)이고, 같은 승인 판에서 다시 표시할 수 있다.
func TestProductAccountUnmarkThenRemark(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if a, err := r.unmarkAccount("ARTEX", target.ID, "tester-a"); err != nil || a.Classification != "real" || a.Reason != "" {
		t.Fatalf("unmark of an unmarked account: %+v %v", a, err)
	}
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
		t.Fatal(err)
	}
	if a, err := r.unmarkAccount("ARTEX", target.ID, "tester-a"); err != nil || a.Classification != "real" || a.Reason != "manual_unmark" {
		t.Fatalf("unmark: %+v %v", a, err)
	}
	if a, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil || a.Classification != "test" {
		t.Fatalf("remark: %+v %v", a, err)
	}
}

func TestProductAccountRegistrationLimits(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	for _, label := range []string{"", "Upper", "user@example.test", strings.Repeat("a", 33), "-lead"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err == nil || err.Error() != "invalid_account_label" {
			t.Errorf("label %q accepted: %v", label, err)
		}
	}
	if _, err := r.registerAccount("ARTEX", "target-missing", "tester-a"); err == nil || err.Error() != "target_not_found" {
		t.Fatalf("missing target: %v", err)
	}
	for i := 0; i < diagnosisprep.MaxTargetAccounts; i++ {
		if _, err := r.registerAccount("ARTEX", target.ID, "tester-"+string(rune('a'+i))); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err == nil || err.Error() != "account_exists" {
		t.Fatalf("duplicate label: %v", err)
	}
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-z"); err == nil || err.Error() != "account_capacity" {
		t.Fatalf("over capacity: %v", err)
	}
	if list, err := r.listAccounts("ARTEX", target.ID); err != nil || len(list) != diagnosisprep.MaxTargetAccounts || list[0].Label != "tester-a" {
		t.Fatalf("list: %d %v", len(list), err)
	}
}

// 저널 재생은 진술 해시가 다르거나 없는 대상·판을 가리키는 줄을 받지 않는다(설치 정지).
func TestProductAccountJournalReplayRejectsForgedEvents(t *testing.T) {
	for name, forge := range map[string]func(productAccountEvent) productAccountEvent{
		"statement": func(e productAccountEvent) productAccountEvent { e.StatementSHA256 = strings.Repeat("0", 64); return e },
		"revision":  func(e productAccountEvent) productAccountEvent { e.TargetRevision = 99; return e },
		"actor":     func(e productAccountEvent) productAccountEvent { e.Actor = "other"; return e },
		"target":    func(e productAccountEvent) productAccountEvent { e.TargetID = "target-forged"; return e },
	} {
		t.Run(name, func(t *testing.T) {
			r, root, target := accountTestTarget(t, true)
			if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
				t.Fatal(err)
			}
			e := forge(productAccountEvent{TargetID: target.ID, Label: "tester-a", Action: accountMarkTest, Actor: "ARTEX", At: time.Now().UTC(), TargetRevision: target.Revision, StatementSHA256: productAccountStatementSHA256()})
			raw, _ := json.Marshal(e)
			if err := r.accountJournal.Append(raw); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil }); err == nil {
				reopened.Close()
				t.Fatal("forged account event replayed")
			}
		})
	}
}

func accountJournalLines(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	size := int64(0)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	return int(size)
}

// 표시는 그 표시를 한 대상 판에만 묶인다. 다른(이전) 판에 남은 표시는 지금 승인 판에서 test로 읽지 않는다.
func TestProductAccountMarkFromAnotherRevisionReadsReal(t *testing.T) {
	r, root, target := accountTestTarget(t, true)
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	e := productAccountEvent{TargetID: target.ID, Label: "tester-a", Action: accountMarkTest, Actor: "ARTEX", At: time.Now().UTC(), TargetRevision: target.Revision - 1, StatementSHA256: productAccountStatementSHA256()}
	raw, _ := json.Marshal(e)
	if err := r.accountJournal.Append(raw); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if a := accountByLabel(t, reopened, target.ID, "tester-a"); a.Classification != "real" || a.Reason != "target_reapproved" {
		t.Fatalf("mark from revision %d read as %+v on revision %d", e.TargetRevision, a, target.Revision)
	}
}

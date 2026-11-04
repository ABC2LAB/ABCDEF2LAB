package budget_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit/budget"
)

var testNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func grant(limit uint64) budget.Grant {
	return budget.Grant{
		Key:         budget.Key{TaskRef: "task-1", BudgetRef: "budget-1"},
		Binding:     budget.Binding{PolicyRef: "policy-1", ResetRef: "reset-1"},
		MaxRequests: limit,
		ValidFrom:   testNow.Add(-time.Minute),
		ValidUntil:  testNow.Add(time.Hour),
	}
}

func newLedger(t *testing.T, maxRequests uint64) *budget.Ledger {
	t.Helper()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 8, MaxRequestsPerApproval: maxRequests})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return ledger
}

func assertIs(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func TestRegisterSameGrantIsIdempotentWithoutReset(t *testing.T) {
	ledger := newLedger(t, 2)
	g := grant(2)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "run-1", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Register(g, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	remaining, err := ledger.Remaining(g.Key, g.Binding, testNow.Add(time.Second))
	if err != nil || remaining != 1 {
		t.Fatalf("Remaining() = %d, %v; want 1, nil", remaining, err)
	}
}

func TestRegisterChangedGrantConflictsWithoutReset(t *testing.T) {
	ledger := newLedger(t, 4)
	g := grant(2)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "before-conflict", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]budget.Grant{
		"binding": func() budget.Grant { x := g; x.Binding.PolicyRef = "policy-2"; return x }(),
		"reset":   func() budget.Grant { x := g; x.Binding.ResetRef = "reset-2"; return x }(),
		"window":  func() budget.Grant { x := g; x.ValidFrom = x.ValidFrom.Add(time.Second); return x }(),
		"limit":   func() budget.Grant { x := g; x.MaxRequests = 3; return x }(),
	} {
		t.Run(name, func(t *testing.T) {
			assertIs(t, ledger.Register(changed, testNow), budget.ErrConflict)
			remaining, err := ledger.Remaining(g.Key, g.Binding, testNow)
			if err != nil || remaining != 1 {
				t.Fatalf("Remaining() = %d, %v; want 1, nil", remaining, err)
			}
		})
	}
}

func TestConcurrentUniqueAttemptsConsumeAtMostLimit(t *testing.T) {
	const limit = 17
	const workers = 41
	ledger := newLedger(t, limit)
	g := grant(limit)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: fmt.Sprintf("run-%d", i), Sequence: 1}, testNow)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var success, exhausted int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, budget.ErrExhausted):
			exhausted++
		default:
			t.Errorf("Reserve() error = %v", err)
		}
	}
	if success != limit || exhausted != workers-limit {
		t.Fatalf("success=%d exhausted=%d, want %d/%d", success, exhausted, limit, workers-limit)
	}
	remaining, err := ledger.Remaining(g.Key, g.Binding, testNow)
	if err != nil || remaining != 0 {
		t.Fatalf("Remaining() = %d, %v; want 0, nil", remaining, err)
	}
}

func TestConcurrentDuplicateAttemptHasExactlyOneSuccess(t *testing.T) {
	const workers = 31
	ledger := newLedger(t, 1)
	g := grant(1)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "same-run", Sequence: 7}, testNow)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var success, duplicate int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, budget.ErrDuplicateAttempt):
			duplicate++
		default:
			t.Errorf("Reserve() error = %v, want duplicate", err)
		}
	}
	if success != 1 || duplicate != workers-1 {
		t.Fatalf("success=%d duplicate=%d, want 1/%d", success, duplicate, workers-1)
	}
}

func TestRegistrationRaceDoesNotResetSuccessfulReservations(t *testing.T) {
	const limit = 12
	ledger := newLedger(t, limit)
	g := grant(limit)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "before-race", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := ledger.Register(g, testNow); err != nil {
				t.Errorf("Register() error = %v", err)
			}
		}()
	}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: fmt.Sprintf("race-%d", i), Sequence: 1}, testNow); err != nil {
				t.Errorf("Reserve() error = %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	remaining, err := ledger.Remaining(g.Key, g.Binding, testNow)
	if err != nil || remaining != limit-7 {
		t.Fatalf("Remaining() = %d, %v; want %d, nil", remaining, err, limit-7)
	}
}

func TestFullCapacityRegistrationPreservesExhaustion(t *testing.T) {
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 1})
	if err != nil {
		t.Fatal(err)
	}
	g := grant(1)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "full", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Register(g, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	remaining, err := ledger.Remaining(g.Key, g.Binding, testNow.Add(time.Second))
	if err != nil || remaining != 0 {
		t.Fatalf("Remaining() = %d, %v; want 0, nil", remaining, err)
	}
}

func TestRegistrationCapacityRejectsNewKeyButKeepsExistingKey(t *testing.T) {
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 2})
	if err != nil {
		t.Fatal(err)
	}
	first := grant(1)
	if err := ledger.Register(first, testNow); err != nil {
		t.Fatal(err)
	}
	second := grant(1)
	second.Key.BudgetRef = "second-budget"
	assertIs(t, ledger.Register(second, testNow), budget.ErrCapacity)
	if err := ledger.Register(first, testNow); err != nil {
		t.Fatalf("idempotent registration after capacity rejection = %v", err)
	}
}

func TestExpiredEntryDoesNotReviveWhenTimeMovesBack(t *testing.T) {
	ledger := newLedger(t, 2)
	g := grant(2)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	expiredAt := g.ValidUntil
	_, err := ledger.Remaining(g.Key, g.Binding, expiredAt)
	assertIs(t, err, budget.ErrExpired)
	_, err = ledger.Remaining(g.Key, g.Binding, testNow)
	assertIs(t, err, budget.ErrExpired)
}

func TestApprovalLifecycleKeepsExpiredKeysAndRequiresFreshRegistration(t *testing.T) {
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 2})
	if err != nil {
		t.Fatal(err)
	}
	g := grant(2)
	future := g
	future.Key.BudgetRef = "future-lifecycle"
	future.ValidFrom = testNow.Add(time.Hour)
	future.ValidUntil = testNow.Add(2 * time.Hour)
	if err := ledger.Register(future, testNow); err != nil {
		t.Fatal(err)
	}
	expiredFreshLedger := newLedger(t, 2)
	expiredFresh := grant(1)
	expiredFresh.Key.BudgetRef = "already-expired"
	expiredFresh.ValidFrom = testNow.Add(-2 * time.Hour)
	expiredFresh.ValidUntil = testNow.Add(-time.Hour)
	if err := expiredFreshLedger.Register(expiredFresh, testNow); !errors.Is(err, budget.ErrExpired) {
		t.Fatalf("fresh expired registration = %v, want ErrExpired", err)
	}
	if remaining, err := ledger.Remaining(future.Key, future.Binding, testNow); remaining != 0 || !errors.Is(err, budget.ErrNotYetValid) {
		t.Fatalf("future Remaining() = %d, %v; want 0, ErrNotYetValid", remaining, err)
	}
	if err := ledger.Reserve(future.Key, future.Binding, budget.Attempt{RunRef: "future-valid", Sequence: 1}, future.ValidFrom); err != nil {
		t.Fatalf("Reserve() at future ValidFrom = %v", err)
	}

	// The first key cannot be registered while capacity is occupied, and its
	// entry remains known after expiry instead of being silently evicted.
	if err := ledger.Register(g, testNow); !errors.Is(err, budget.ErrCapacity) {
		t.Fatalf("expired-key registration before expiry = %v, want ErrCapacity", err)
	}
	if _, err := ledger.Remaining(future.Key, future.Binding, future.ValidUntil); !errors.Is(err, budget.ErrExpired) {
		t.Fatalf("future expiry observation = %v, want ErrExpired", err)
	}
	if _, err := ledger.Remaining(future.Key, future.Binding, testNow); !errors.Is(err, budget.ErrExpired) {
		t.Fatalf("future time rollback = %v, want ErrExpired", err)
	}
	if err := ledger.Register(future, testNow); !errors.Is(err, budget.ErrExpired) {
		t.Fatalf("expired same-grant registration = %v, want ErrExpired", err)
	}

	changed := future
	changed.ValidFrom = testNow.Add(3 * time.Hour)
	changed.ValidUntil = testNow.Add(4 * time.Hour)
	if err := ledger.Register(changed, testNow); !errors.Is(err, budget.ErrConflict) {
		t.Fatalf("expired key changed-window registration = %v, want ErrConflict", err)
	}
	newKey := g
	newKey.Key.BudgetRef = "new-after-expiry"
	if err := ledger.Register(newKey, testNow); !errors.Is(err, budget.ErrCapacity) {
		t.Fatalf("new key after expiry = %v, want ErrCapacity", err)
	}
	newLedger := newLedger(t, 2)
	if _, err := newLedger.Remaining(future.Key, future.Binding, testNow); !errors.Is(err, budget.ErrUnknownApproval) {
		t.Fatalf("new Ledger old key lookup = %v, want ErrUnknownApproval", err)
	}
}

func TestDistinctStructuredKeysDoNotCollideAtDelimiter(t *testing.T) {
	ledger := newLedger(t, 2)
	left := grant(1)
	left.Key = budget.Key{TaskRef: "a:b", BudgetRef: "c"}
	right := grant(1)
	right.Key = budget.Key{TaskRef: "a", BudgetRef: "b:c"}
	if err := ledger.Register(left, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Register(right, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(left.Key, left.Binding, budget.Attempt{RunRef: "left", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(right.Key, right.Binding, budget.Attempt{RunRef: "right", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
}

func TestEquivalentInstantsInDifferentTimeZonesAreIdempotent(t *testing.T) {
	ledger := newLedger(t, 2)
	g := grant(2)
	zone := time.FixedZone("KST", 9*60*60)
	localNow := testNow.In(zone)
	localGrant := g
	localGrant.ValidFrom = g.ValidFrom.In(zone)
	localGrant.ValidUntil = g.ValidUntil.In(zone)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Register(localGrant, localNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "zone", Sequence: 1}, localNow); err != nil {
		t.Fatal(err)
	}
}

func TestValidityBoundariesAndRejectedAttemptsDoNotConsume(t *testing.T) {
	ledger := newLedger(t, 3)
	g := grant(3)
	future := g
	future.Key.BudgetRef = "future-budget"
	future.ValidFrom = testNow.Add(time.Hour)
	future.ValidUntil = testNow.Add(2 * time.Hour)
	if err := ledger.Register(future, testNow); err != nil {
		t.Fatal(err)
	}
	assertIs(t, ledger.Reserve(future.Key, future.Binding, budget.Attempt{RunRef: "future", Sequence: 1}, testNow), budget.ErrNotYetValid)
	if err := ledger.Register(g, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "from-boundary", Sequence: 1}, g.ValidFrom); err != nil {
		t.Fatalf("Reserve() at ValidFrom = %v, want nil", err)
	}
	if err := ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "accepted", Sequence: 1}, testNow); err != nil {
		t.Fatal(err)
	}
	assertIs(t, ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "accepted", Sequence: 1}, testNow), budget.ErrDuplicateAttempt)
	assertIs(t, ledger.Reserve(g.Key, budget.Binding{PolicyRef: "wrong", ResetRef: "reset-1"}, budget.Attempt{RunRef: "binding", Sequence: 1}, testNow), budget.ErrBindingMismatch)
	remaining, err := ledger.Remaining(g.Key, g.Binding, testNow)
	if err != nil || remaining != 1 {
		t.Fatalf("Remaining() = %d, %v; want 1, nil", remaining, err)
	}
}

func TestUnknownAndExpiredErrorsDoNotExposeInput(t *testing.T) {
	ledger := newLedger(t, 2)
	secret := "secret-task-value"
	key := budget.Key{TaskRef: secret, BudgetRef: "missing-budget"}
	err := ledger.Reserve(key, budget.Binding{PolicyRef: "secret-policy", ResetRef: "secret-reset"}, budget.Attempt{RunRef: "secret-run", Sequence: 1}, testNow)
	assertIs(t, err, budget.ErrUnknownApproval)
	for _, value := range []string{secret, "missing-budget", "secret-policy", "secret-reset", "secret-run"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error %q exposes input %q", err, value)
		}
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []budget.Limits{
		{},
		{MaxApprovals: 0, MaxRequestsPerApproval: 1},
		{MaxApprovals: -1, MaxRequestsPerApproval: 1},
		{MaxApprovals: 1, MaxRequestsPerApproval: 0},
		{MaxApprovals: 1, MaxRequestsPerApproval: uint64(math.MaxInt) + 1},
	}
	for _, limits := range tests {
		if ledger, err := budget.New(limits); ledger != nil || !errors.Is(err, budget.ErrConfig) {
			t.Errorf("New(%+v) = (%v, %v), want nil, ErrConfig", limits, ledger, err)
		}
	}
}

func TestInvalidReferencesAndAttemptsAreRejected(t *testing.T) {
	ledger := newLedger(t, 2)
	cases := []budget.Grant{
		func() budget.Grant { g := grant(1); g.Key.TaskRef = ""; return g }(),
		func() budget.Grant { g := grant(1); g.Key.TaskRef = "https://target"; return g }(),
		func() budget.Grant { g := grant(1); g.Key.TaskRef = strings.Repeat("a", 129); return g }(),
		func() budget.Grant { g := grant(1); g.MaxRequests = 0; return g }(),
		func() budget.Grant { g := grant(1); g.ValidUntil = g.ValidFrom; return g }(),
	}
	for i, g := range cases {
		if err := ledger.Register(g, testNow); !errors.Is(err, budget.ErrInvalid) {
			t.Errorf("case %d Register() error = %v, want ErrInvalid", i, err)
		}
	}
	valid := grant(2)
	if err := ledger.Register(valid, testNow); err != nil {
		t.Fatal(err)
	}
	for i, attempt := range []budget.Attempt{{}, {RunRef: "run", Sequence: 0}} {
		if err := ledger.Reserve(valid.Key, valid.Binding, attempt, testNow); !errors.Is(err, budget.ErrInvalid) {
			t.Errorf("attempt %d Reserve() error = %v, want ErrInvalid", i, err)
		}
	}
}

func TestInvalidTimeAndReferenceBoundariesDoNotConsume(t *testing.T) {
	ledger := newLedger(t, 2)
	valid := grant(2)
	if err := ledger.Register(valid, testNow); err != nil {
		t.Fatal(err)
	}
	tooLarge := strings.Repeat("a", 129)
	tests := []struct {
		name string
		call func() error
	}{
		{"zero-now", func() error {
			return ledger.Reserve(valid.Key, valid.Binding, budget.Attempt{RunRef: "zero-now", Sequence: 1}, time.Time{})
		}},
		{"year-zero-now", func() error {
			return ledger.Reserve(valid.Key, valid.Binding, budget.Attempt{RunRef: "year-zero-now", Sequence: 1}, time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC))
		}},
		{"year-10000-now", func() error {
			return ledger.Reserve(valid.Key, valid.Binding, budget.Attempt{RunRef: "year-10000-now", Sequence: 1}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		}},
		{"unicode-task", func() error {
			key := valid.Key
			key.TaskRef = "t-한"
			return ledger.Reserve(key, valid.Binding, budget.Attempt{RunRef: "unicode-task", Sequence: 1}, testNow)
		}},
		{"first-character-dash", func() error {
			key := valid.Key
			key.TaskRef = "-task"
			return ledger.Reserve(key, valid.Binding, budget.Attempt{RunRef: "first-character-dash", Sequence: 1}, testNow)
		}},
		{"overlong-task", func() error {
			key := valid.Key
			key.TaskRef = tooLarge
			return ledger.Reserve(key, valid.Binding, budget.Attempt{RunRef: "overlong-task", Sequence: 1}, testNow)
		}},
		{"over-limit-grant", func() error {
			changed := valid
			changed.Key.BudgetRef = "over-limit"
			changed.MaxRequests = 3
			return ledger.Register(changed, testNow)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, budget.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
	remaining, err := ledger.Remaining(valid.Key, valid.Binding, testNow)
	if err != nil || remaining != valid.MaxRequests {
		t.Fatalf("Remaining() = %d, %v; want %d, nil", remaining, err, valid.MaxRequests)
	}
	if remaining, err := ledger.Remaining(valid.Key, budget.Binding{PolicyRef: "wrong", ResetRef: "reset-1"}, testNow); remaining != 0 || !errors.Is(err, budget.ErrBindingMismatch) {
		t.Fatalf("invalid binding Remaining() = %d, %v; want 0, ErrBindingMismatch", remaining, err)
	}
}

func TestZeroLedgerRejectsOperations(t *testing.T) {
	var ledger budget.Ledger
	g := grant(1)
	assertIs(t, ledger.Register(g, testNow), budget.ErrConfig)
	assertIs(t, ledger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "run", Sequence: 1}, testNow), budget.ErrConfig)
	remaining, err := ledger.Remaining(g.Key, g.Binding, testNow)
	if remaining != 0 {
		t.Fatalf("Remaining() = %d, want 0", remaining)
	}
	assertIs(t, err, budget.ErrConfig)

	var nilLedger *budget.Ledger
	assertIs(t, nilLedger.Register(g, testNow), budget.ErrConfig)
	assertIs(t, nilLedger.Reserve(g.Key, g.Binding, budget.Attempt{RunRef: "run", Sequence: 1}, testNow), budget.ErrConfig)
	remaining, err = nilLedger.Remaining(g.Key, g.Binding, testNow)
	if remaining != 0 {
		t.Fatalf("nil Remaining() = %d, want 0", remaining)
	}
	assertIs(t, err, budget.ErrConfig)
}

package db

import "testing"

func TestCompanyScopeMutationLockDoesNotReuseInfrastructureLocks(t *testing.T) {
	reserved := map[string]int64{
		"schema migration":         7337741001,
		"cross-package test suite": 7337741002,
		"finding evidence":         findingEvidenceLockKey,
		"budget authority":         budgetAuthorityLockKey,
	}
	seen := make(map[int64]string)
	for name, key := range reserved {
		if previous, exists := seen[key]; exists {
			t.Fatalf("%s and %s reuse advisory lock key %d", previous, name, key)
		}
		seen[key] = name
		if companyScopeMutationLock == key {
			t.Fatalf("company scope mutation lock reuses the %s advisory lock key %d", name, key)
		}
	}
}

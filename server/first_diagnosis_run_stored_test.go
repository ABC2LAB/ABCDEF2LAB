package server

import (
	"context"
	"testing"
)

func TestPersistRunOutcomeStoredDoesNotInventResults(t *testing.T) {
	for _, tc := range []struct {
		name       string
		supervisor *FirstDiagnosisSupervisor
		wantErr    bool
	}{
		{"nil", nil, true},
		{"not_started", &FirstDiagnosisSupervisor{}, false},
		{"registered", &FirstDiagnosisSupervisor{runStarted: true, registered: true}, false},
		{"missing_store", &FirstDiagnosisSupervisor{runStarted: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := tc.supervisor.PersistRunOutcomeStored(context.Background(), nil)
			if stored || (err != nil) != tc.wantErr {
				t.Fatalf("stored=%v err=%v", stored, err)
			}
			if oldErr := tc.supervisor.PersistRunOutcome(context.Background(), nil); (oldErr != nil) != tc.wantErr {
				t.Fatal("legacy caller behavior changed")
			}
		})
	}
}

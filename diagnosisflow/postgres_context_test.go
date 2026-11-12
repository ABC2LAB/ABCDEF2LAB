package diagnosisflow

import (
	"context"
	"testing"
	"time"
)

func TestWorkflowWriteIgnoresCallerCancellationButKeepsDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	caller, cancel := context.WithDeadline(t.Context(), deadline)
	cancel()
	call, closeCall := workflowWriteContext(caller)
	defer closeCall()
	if call.Err() != nil {
		t.Fatal("caller cancellation prevented protected write")
	}
	actual, ok := call.Deadline()
	if !ok || !actual.Equal(deadline) {
		t.Fatal("protected write extended caller deadline")
	}
}
func TestWorkflowWriteCannotReviveExpiredDeadline(t *testing.T) {
	caller, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	call, closeCall := workflowWriteContext(caller)
	defer closeCall()
	select {
	case <-call.Done():
	default:
		t.Fatal("expired cleanup deadline was removed")
	}
}
func TestWorkflowWriteStillHasMaximumDuration(t *testing.T) {
	caller, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	before := time.Now()
	call, closeCall := workflowWriteContext(caller)
	defer closeCall()
	actual, ok := call.Deadline()
	if !ok || actual.Before(before) || actual.After(before.Add(maxWorkflowWriteDuration+100*time.Millisecond)) {
		t.Fatal("protected write lost its independent maximum duration")
	}
}

package main

import (
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func TestModelWriteObservationsKeepAttemptsAndOutcomesDistinct(t *testing.T) {
	event := func(id, stage, method string) e2proxy.Event {
		return e2proxy.Event{Stage: stage, Receipt: e2proxy.Receipt{AttemptID: id, IntentID: 1, Method: method}}
	}
	completed := event("completed", "result", "POST")
	completed.ForwardAttempted, completed.RequestWritten, completed.ResponseReceived, completed.StatusCode = true, true, true, 201
	withheld := event("withheld", "result", "PUT")
	withheld.ForwardAttempted, withheld.RequestWritten, withheld.ResponseReceived, withheld.StatusCode = true, true, true, 204
	withheld.Reason = e2proxy.Withheld
	forbidden := event("forbidden", "result", "POST")
	forbidden.ForwardAttempted, forbidden.RequestWritten, forbidden.ResponseReceived, forbidden.StatusCode = true, true, true, 403
	serverError := event("server-error", "result", "PATCH")
	serverError.ForwardAttempted, serverError.RequestWritten, serverError.ResponseReceived, serverError.StatusCode = true, true, true, 500
	unknown := event("unknown", "unknown", "DELETE")
	denied := event("denied", "result", "DELETE")
	denied.NotSent = true
	failed := event("failed", "result", "PATCH")
	failed.Reason, failed.ForwardAttempted = e2proxy.TransportFailed, true
	program := completed
	program.AttemptID, program.Program = "program", e2proxy.ProgramAuthPrep
	read := completed
	read.AttemptID, read.Method = "read", "GET"
	events := []e2proxy.Event{
		event("completed", "intent", "POST"), completed, withheld, forbidden, serverError, unknown,
		event("denied", "intent", "DELETE"), denied, failed,
		event("pending", "intent", "PATCH"), program, read,
	}
	if got := summarizeE2ModelWrites(events); got != (e2ModelWrites{Responded2xx: 2, Unverified: 4}) {
		t.Fatalf("write observations: %+v", got)
	}
}

package main

import "github.com/Autumn-27/artex/e2proxy"

// summarizeE2ModelWrites projects verified product journal events. An HTTP 2xx
// is a response, not proof of an object; an unresolved send warrants inspection.
func summarizeE2ModelWrites(events []e2proxy.Event) e2ModelWrites {
	writes := make(map[string]bool)
	for _, e := range events {
		if e.Program != "" || e.IntentID <= 0 || e.AttemptID == "" {
			continue
		}
		switch e.Method {
		case "POST", "PUT", "PATCH", "DELETE":
		default:
			continue
		}
		switch e.Stage {
		case "intent":
			writes[e.AttemptID] = false
		case "result", "unknown":
			if e.NotSent || !e.RequestWritten && !e.ResponseReceived && e.Reason == e2proxy.TransportFailed {
				delete(writes, e.AttemptID)
				continue
			}
			if e.ForwardAttempted || e.RequestWritten || e.ResponseReceived || e.Stage == "unknown" {
				writes[e.AttemptID] = e.ResponseReceived && e.StatusCode >= 200 && e.StatusCode < 300
			}
		}
	}
	var out e2ModelWrites
	for _, responded := range writes {
		if responded {
			out.Responded2xx++
		} else {
			out.Unverified++
		}
	}
	return out
}

package main

import "github.com/Autumn-27/artex/e2proxy"

// summarizeE2ModelWrites는 검증된 제품 저널의 모델 쓰기 시도를 집계한다. 2xx도 객체 생성의 증명은 아니며,
// 결과를 확인할 수 없는 송신은 운영자가 확인하도록 따로 센다.
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

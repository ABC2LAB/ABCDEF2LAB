package main

import "testing"

func TestCandidateLLMRuntimeSecondsNeverExceedsInstalledCaps(t *testing.T) {
	for _, tc := range []struct {
		plan      int
		installed int64
		workerMS  int64
		want      int
	}{
		{90, 180, 120000, 90},
		{90, 30, 120000, 30},
		{90, 180, 20000, 20},
		{90, 180, 500, 0},
		{90, 0, 120000, 0},
	} {
		if got := candidateLLMRuntimeSeconds(tc.plan, tc.installed, tc.workerMS); got != tc.want {
			t.Fatalf("runtime seconds=%d want=%d for %+v", got, tc.want, tc)
		}
	}
}

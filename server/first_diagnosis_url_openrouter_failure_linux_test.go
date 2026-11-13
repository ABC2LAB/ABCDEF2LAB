//go:build linux

package server

import (
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"os"
	"testing"
	"time"
)

func TestURLOpenRouterFailuresStopFurtherCallsOwnedPostgres(t *testing.T) {
	if os.Getenv("ARTEX_URL_DIAGNOSIS_TEST") != "1" {
		t.Skip("NOT_RUN: owned URL process opt-in required")
	}
	for _, failure := range []string{"usage", "model", "provider", "cost", "http401"} {
		t.Run(failure, func(t *testing.T) {
			f := newURLProcessFixtureMode(t, nil, true)
			f.externalFailure = failure
			service := f.startPreparation()
			api := f.startAPI()
			client := f.api()
			job := client.job("POST", diagnosisapi.URLBasePath+"/drafts", "failure-create", map[string]any{"version": diagnosisapi.URLVersion, "input": diagnosisprep.Input{TargetURL: f.target + "/allowed", ProfileID: "owned-url"}})
			if job.State != diagnosisprep.Ready {
				t.Fatal("synthetic external scope not ready")
			}
			revision := job.Revision
			client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/start", "failure-start", urlStartBody(revision, true))
			var final diagnosisprep.Job
			for deadline := time.Now().Add(15 * time.Second); ; {
				final = client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/status", "", nil)
				if final.State == diagnosisprep.Unknown {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("unknown not preserved: state=%s reason=%s calls=%d", final.State, final.ReasonCode, f.modelCalls.Load())
				}
				time.Sleep(20 * time.Millisecond)
			}
			usage := final.ModelUsage
			if final.Started || usage == nil || usage.UsageKnown || usage.Requests != 1 || !urlRequestSizedReservation(usage.ReservedCostUSDMicros, 1) || usage.CostUSDMicros != usage.ReservedCostUSDMicros || f.modelCalls.Load() != 1 || f.allowed.Load() != 0 || f.denied.Load() != 0 {
				t.Fatal("unverified usage caused another model or target request")
			}
			// 반복 Start는 영수증만 조회하며, 청구 결과가 불명인 호출을 재시도하지 않는다.
			client.job("POST", diagnosisapi.URLBasePath+"/drafts/"+job.ID+"/start", "failure-start", urlStartBody(revision, true))
			time.Sleep(100 * time.Millisecond)
			if f.modelCalls.Load() != 1 {
				t.Fatal("failed completion replayed")
			}
			api.stop()
			service.stop()
			t.Logf("URL_OPENROUTER_FAILURE case=%s calls=1 additional_calls=0 target_sends=0 reservation_preserved=true unknown=true", failure)
		})
	}
}

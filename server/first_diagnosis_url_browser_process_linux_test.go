//go:build linux

package server

import (
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"os"
	"testing"
	"time"
)

// 실제 브라우저 수용은 공통 서버 fixture 위에 UI 상호작용만 추가한다.
func TestFirstDiagnosisURLBrowserIndependentProcessesOwnedPostgres(t *testing.T) {
	runURLProcessAcceptance(t, true)
}

func TestFirstDiagnosisURLOpenRouterBrowserIndependentProcessesOwnedPostgres(t *testing.T) {
	t.Setenv("ARTEX_URL_OPENROUTER_BROWSER", "1")
	runURLProcessAcceptanceMode(t, true, true)
}

func TestFirstDiagnosisURLOpenRouterBrowserStopOwnedPostgres(t *testing.T) {
	if os.Getenv("ARTEX_URL_DIAGNOSIS_TEST") != "1" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE") == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT") == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT") == "" {
		t.Skip("NOT_RUN: owned browser dependencies required")
	}
	t.Setenv("ARTEX_URL_OPENROUTER_BROWSER", "1")
	f := newURLProcessFixtureMode(t, nil, true)
	f.startPreparation()
	f.startAPI()
	client := f.api()
	ui := startFirstDiagnosisBrowserFixture(t, f.ctx, "http://"+f.apiAddr, f.target+"/allowed")
	raw, err := ui.result("url-draft")
	if err != nil {
		t.Fatal(err)
	}
	var draft struct {
		ID string `json:"draftID"`
	}
	if json.Unmarshal(raw, &draft) != nil || draft.ID == "" {
		t.Fatal("visible draft binding missing")
	}
	ui.run("url-start")
	select {
	case <-f.plannerEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("Planner not reached")
	}
	ui.run("url-stop")
	job := client.job("GET", diagnosisapi.URLBasePath+"/drafts/"+draft.ID+"/status", "", nil)
	if job.State != diagnosisprep.Unknown || job.Started || !job.DrainComplete || job.ModelUsage == nil || job.ModelUsage.UsageKnown || job.ModelUsage.Requests != 3 || !urlRequestSizedReservation(job.ModelUsage.ReservedCostUSDMicros, 3) || f.modelCalls.Load() != 3 || f.allowed.Load() != 0 || f.denied.Load() != 0 {
		t.Fatalf("browser stop facts mismatch state=%s started=%t drain=%t result=%t usage=%+v calls=%d allowed=%d denied=%d", job.State, job.Started, job.DrainComplete, job.ResultReady, job.ModelUsage, f.modelCalls.Load(), f.allowed.Load(), f.denied.Load())
	}
	t.Log("URL_OPENROUTER_BROWSER_STOP calls=3 additional_calls=0 target_sends=0 unknown=true reservation_preserved=true")
}

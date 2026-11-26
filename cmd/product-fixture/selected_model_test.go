package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func selectedFixtureResponse(t *testing.T, h http.Handler, names []string, messages []any) (string, string) {
	t.Helper()
	tools := []any{}
	for _, name := range names {
		tools = append(tools, map[string]any{"function": map[string]any{"name": name}})
	}
	request, _ := json.Marshal(map[string]any{"model": "owned-synthetic", "tools": tools, "messages": messages})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(request)))
	if rec.Code != http.StatusOK {
		t.Fatalf("selected fixture status: %d", rec.Code)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Choices) != 1 {
		t.Fatal("selected fixture response invalid")
	}
	message := out.Choices[0].Message
	if len(message.ToolCalls) == 1 {
		return message.ToolCalls[0].Function.Name, message.ToolCalls[0].Function.Arguments
	}
	return "", message.Content
}

func TestSelectedFixtureCompletesOnlyObservedFreshGoal(t *testing.T) {
	h := selectedModel("https://juice.test:9443")
	base := []any{map[string]any{"role": "user", "content": "Inspect the approved HTTPS target https://juice.test:9443"}}
	name, _ := selectedFixtureResponse(t, h, []string{"set_goals"}, base)
	if name != "set_goals" {
		t.Fatal("goal not created")
	}
	name, _ = selectedFixtureResponse(t, h, []string{"add_intent", "prove_goal"}, base)
	if name != "add_intent" {
		t.Fatal("intent not created")
	}
	name, _ = selectedFixtureResponse(t, h, []string{"Bash"}, base)
	if name != "Bash" {
		t.Fatal("owned read not requested")
	}
	observed := e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true,
		Call: e2proxy.CallResult{CallRef: "owned-call", Counts: e2proxy.Counts{Responses: 1}}}}
	encoded, _ := json.Marshal(observed)
	withResult := append(append([]any{}, base...), map[string]any{"role": "tool", "content": string(encoded)})
	name, _ = selectedFixtureResponse(t, h, []string{"Bash"}, withResult)
	if name != "submit_diagnostic_candidate" {
		t.Fatal("candidate not submitted after observed read")
	}
	name, _ = selectedFixtureResponse(t, h, []string{"Bash"}, base)
	if name != "Bash" {
		t.Fatal("out-of-scope denial probe not requested")
	}
	graph := map[string]any{
		"goals":               []any{map[string]any{"id": 12, "state": "open", "text": "Observe approved products at https://juice.test:9443 and save a manual-review candidate."}},
		"recent_done_intents": []any{map[string]any{"id": 13, "state": "done", "summary": "Observe https://juice.test:9443/rest/products/search through the enforced Bash boundary, preserve a manual-review candidate, and verify denial of /outside-approved.", "yields": []int{14}}},
		"recent_facts":        []any{map[string]any{"id": 14, "from_intent": 13, "summary": "Synthetic approved read observed"}},
	}
	graphJSON, _ := json.Marshal(graph)
	withGraph := append(append([]any{}, withResult...), map[string]any{"role": "system", "content": fixtureGraphOverviewMarker + string(graphJSON)})
	name, args := selectedFixtureResponse(t, h, []string{"add_intent", "prove_goal"}, withGraph)
	if name != "prove_goal" {
		t.Fatal("observed goal not completed")
	}
	var proof struct {
		GoalID     int `json:"goal_id"`
		EvidenceID int `json:"evidence_id"`
	}
	if json.Unmarshal([]byte(args), &proof) != nil || proof.GoalID != 12 || proof.EvidenceID != 14 {
		t.Fatal("goal proof did not use the current graph nodes")
	}
	graph["recent_facts"] = []any{}
	graphJSON, _ = json.Marshal(graph)
	messageJSON, _ := json.Marshal(fixtureGraphOverviewMarker + string(graphJSON))
	if _, _, ok := selectedGoalProof([]fixtureMessage{{Role: "system", Content: messageJSON}}, "https://juice.test:9443"); ok {
		t.Fatal("missing observed fact became a goal proof")
	}
}

func TestSelectedFixtureUsesOwnedSyntheticTargetWithoutJuiceShop(t *testing.T) {
	h := selectedModel("https://synthetic.test:9443")
	messages := []any{map[string]any{"role": "user", "content": "Inspect the approved HTTPS target https://synthetic.test:9443"}}
	name, _ := selectedFixtureResponse(t, h, []string{"set_goals"}, messages)
	if name != "set_goals" {
		t.Fatal("owned standalone target was not recognized")
	}
}

func TestSelectedFixtureAnalysisAndFinalAreInsufficient(t *testing.T) {
	h := selectedModel("https://juice.test:9443")
	for _, version := range []string{"selected/analysis-v1", "selected/final-v1"} {
		input, _ := json.Marshal(map[string]any{"candidate_id": 23})
		messages := []any{map[string]any{"role": "system", "content": "Return " + version},
			map[string]any{"role": "user", "content": string(input)}}
		name, body := selectedFixtureResponse(t, h, nil, messages)
		if name != "" {
			t.Fatal("selected fixture requested a tool")
		}
		var output map[string]any
		if json.Unmarshal([]byte(body), &output) != nil || output["version"] != version || output["candidate_id"] != float64(23) {
			t.Fatal("selected fixture returned the wrong candidate or stage")
		}
		if version == "selected/final-v1" && output["verdict"] != "insufficient" {
			t.Fatal("uncertain public listing was confirmed")
		}
	}
}

func TestSelectedNosniffFixtureRequiresIndependentMatchingHTML(t *testing.T) {
	observation := func(call, attempt, digest string) selectedFixtureObservation {
		var row selectedFixtureObservation
		row.Source.CallRef, row.Source.Observation.AttemptRef, row.Source.Digest = call, attempt, digest
		row.Source.Observation.Method = "GET"
		row.Source.Observation.Endpoint = "/rest/products/search"
		row.Source.Observation.StatusCode = http.StatusOK
		row.Source.Observation.ExactFunction = true
		row.Source.Observation.HTMLSecurity = "nosniff_missing"
		return row
	}
	input := map[string]any{"candidate_id": 23, "original_observation": observation("original", "attempt-1", "digest-1"),
		"followup_observations": []any{observation("followup", "attempt-2", "digest-2")}}
	invoke := func() map[string]any {
		raw, _ := json.Marshal(input)
		content, _ := json.Marshal(string(raw))
		result := selectedNosniffVerdict(23, []fixtureMessage{{Role: "user", Content: content}})
		return result.(map[string]any)
	}
	if result := invoke(); result["verdict"] != "confirmed" || len(result["sources"].([]any)) != 2 {
		t.Fatal("independent comparable observations were not confirmed")
	}
	input["followup_observations"] = []any{observation("original", "attempt-1", "digest-1")}
	if invoke()["verdict"] != "insufficient" {
		t.Fatal("duplicate original observation was confirmed")
	}
	bad := observation("followup", "attempt-2", "digest-2")
	bad.Source.Observation.HTMLSecurity = "nosniff_present"
	input["followup_observations"] = []any{bad}
	if invoke()["verdict"] != "insufficient" {
		t.Fatal("normal header response was confirmed")
	}
}

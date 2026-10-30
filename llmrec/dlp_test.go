package llmrec

import (
	"context"
	"errors"
	"io"
	"iter"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
	"github.com/google/uuid"
)

const dlpFixtureSecret = "fixture-llm-secret-7f6c"

type dlpCompleteProvider struct {
	got llm.CompletionRequest
	msg llm.Message
	err error
}

func (p *dlpCompleteProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

func (p *dlpCompleteProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.got = req
	if c := CaptureFrom(ctx); c != nil {
		c.SetRequest(`{"secret":"` + dlpFixtureSecret + `"}`)
		body := c.TeeResponse(200, io.NopCloser(strings.NewReader(`data: `+dlpFixtureSecret+"\n")))
		_, _ = io.ReadAll(body)
		_ = body.Close()
	}
	return p.msg, "end_turn", llm.Usage{InputTokens: 17, OutputTokens: 9}, p.err
}

func TestRecorderDLPDoesNotAlterProviderRequestOrResponse(t *testing.T) {
	provider := &dlpCompleteProvider{msg: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: dlpFixtureSecret}, llm.TextBlock(dlpFixtureSecret)},
	}}
	recorder := Wrap(provider, nil, "fixture-model", "fixture-profile", "", "", func() bool { return true })
	temperature := 0.25
	req := llm.CompletionRequest{
		System:    []string{dlpFixtureSecret},
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(dlpFixtureSecret)}}},
		Tools:     []llm.ToolSchema{{Name: "fixture-tool", Description: dlpFixtureSecret, InputSchema: map[string]any{"secret": dlpFixtureSecret}}},
		MaxTokens: 256, Temperature: &temperature,
	}
	got, stop, usage, err := recorder.Complete(context.Background(), req)
	if err != nil || stop != "end_turn" || usage.InputTokens != 17 || usage.OutputTokens != 9 {
		t.Fatalf("completion changed: stop=%q usage=%+v err=%v", stop, usage, err)
	}
	if provider.got.Messages[0].Content[0].Text != dlpFixtureSecret || provider.got.Tools[0].InputSchema["secret"] != dlpFixtureSecret {
		t.Fatal("DLP mutated provider request")
	}
	if got.Text() != dlpFixtureSecret || got.Content[0].Thinking != dlpFixtureSecret {
		t.Fatal("DLP mutated provider response")
	}
}

func TestRecorderDLPErrorIsReturnedButNeverUsedAsRecordContent(t *testing.T) {
	wantErr := errors.New("provider error: " + dlpFixtureSecret)
	provider := &dlpCompleteProvider{msg: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(dlpFixtureSecret)}}, err: wantErr}
	recorder := Wrap(provider, nil, "fixture-model", "fixture-profile", "", "", func() bool { return true })
	_, _, _, err := recorder.Complete(context.Background(), llm.CompletionRequest{})
	if !errors.Is(err, wantErr) || err.Error() != wantErr.Error() {
		t.Fatal("provider error was changed for caller")
	}
}

func TestRecorderDLPKeepsCapturePrimitiveVerbatimOnlyBeforePersistence(t *testing.T) {
	ctx, capture := NewCapture(transcript.WithSessionID(context.Background(), "exp1-worker-i1"))
	capture.SetRequest(dlpFixtureSecret)
	body := capture.TeeResponse(200, io.NopCloser(strings.NewReader(dlpFixtureSecret)))
	if got, _ := io.ReadAll(body); string(got) != dlpFixtureSecret {
		t.Fatal("capture changed wire bytes")
	}
	if capture.RawRequest() != dlpFixtureSecret || capture.RawResponse() != dlpFixtureSecret {
		t.Fatal("capture primitive must retain bytes until recorder persistence")
	}
	_ = ctx
}

func TestRecorderDLPDatabaseRowsContainNoFixtureSecret(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	profile := "dlp-test-" + uuid.NewString()
	session := "exp991-dlp-" + uuid.NewString()
	defer func() {
		_, _ = pg.Exec(`DELETE FROM llm_records WHERE profile_name=$1`, profile)
		_, _ = pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile)
	}()
	provider := &dlpCompleteProvider{msg: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: dlpFixtureSecret}, llm.TextBlock(dlpFixtureSecret),
	}}}
	recorder := Wrap(provider, pg, "fixture-model", profile, "", "", func() bool { return true })
	ctx := transcript.WithSessionID(context.Background(), session)
	_, _, _, err = recorder.Complete(ctx, llm.CompletionRequest{System: []string{dlpFixtureSecret}, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(dlpFixtureSecret)}}}})
	if err != nil {
		t.Fatal(err)
	}
	var rowCount int
	if err := pg.QueryRow(`SELECT count(*) FROM llm_records WHERE profile_name=$1`, profile).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("llm_records rows=%d want 1", rowCount)
	}
	var joined string
	if err := pg.QueryRow(`SELECT coalesce(error,'') || coalesce(request_body,'') || coalesce(response_body,'') || coalesce(raw_request,'') || coalesce(raw_response,'') FROM llm_records WHERE profile_name=$1`, profile).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined, dlpFixtureSecret) {
		t.Fatal("fixture secret persisted in llm_records")
	}
	var usageCount int
	if err := pg.QueryRow(`SELECT count(*) FROM llm_usage WHERE profile_name=$1`, profile).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if usageCount != 1 {
		t.Fatalf("llm_usage rows=%d want 1", usageCount)
	}
}

func TestRecorderDLPPolicyFailureStoresUsageButNoBodyRow(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	profile := "dlp-fail-" + uuid.NewString()
	session := "exp992-dlp-" + uuid.NewString()
	defer func() {
		if _, cleanupErr := pg.Exec(`DELETE FROM llm_records WHERE profile_name=$1`, profile); cleanupErr != nil {
			t.Error("cleanup llm_records failed")
		}
		if _, cleanupErr := pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile); cleanupErr != nil {
			t.Error("cleanup llm_usage failed")
		}
	}()
	provider := &dlpCompleteProvider{msg: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(dlpFixtureSecret)}}}
	recorder := Wrap(provider, pg, "fixture-model", profile, "", "", func() bool { return true })
	recorder.recordPolicy = &dlp.Policy{}
	_, _, _, err = recorder.Complete(transcript.WithSessionID(context.Background(), session), llm.CompletionRequest{System: []string{dlpFixtureSecret}})
	if err != nil {
		t.Fatal("provider call failed before policy failure")
	}
	var records, usage int
	if err := pg.QueryRow(`SELECT count(*) FROM llm_records WHERE profile_name=$1`, profile).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err := pg.QueryRow(`SELECT count(*) FROM llm_usage WHERE profile_name=$1`, profile).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if records != 0 || usage != 1 {
		t.Fatalf("policy failure rows=%d usage=%d", records, usage)
	}
}

package llmrec

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestCaptureMetadataContainsOnlyStatusAndByteCounts(t *testing.T) {
	var missing *Capture
	if len(missing.Metadata()) != 0 {
		t.Fatal("nil capture has attempts")
	}
	_, c := NewCapture(context.Background())
	if len(c.Metadata()) != 0 {
		t.Fatal("empty capture has attempts")
	}
	const secret = "synthetic-private-wire-value"
	for _, tc := range []struct {
		status int
		body   string
	}{{429, secret}, {200, "본문"}, {204, ""}} {
		body := c.TeeResponse(tc.status, io.NopCloser(strings.NewReader(tc.body)))
		if _, err := io.Copy(io.Discard, body); err != nil {
			t.Fatal(err)
		}
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	meta := c.Metadata()
	if len(meta) != 3 || meta[0].Status != 429 || meta[0].BodyBytes != len(secret) || meta[1].Status != 200 || meta[1].BodyBytes != len("본문") || meta[2].Status != 204 || meta[2].BodyBytes != 0 {
		t.Fatal("wire metadata was changed or measured in runes")
	}
	encoded, err := json.Marshal(meta)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "본문") {
		t.Fatal("metadata exposes wire content")
	}
	meta[0].Status = 999
	if c.Metadata()[0].Status != 429 {
		t.Fatal("metadata aliases capture storage")
	}
	if !strings.Contains(c.RawResponse(), secret) {
		t.Fatal("metadata projection changed transient wire capture")
	}
}

func TestCaptureMetadataCanSnapshotWhileWireIsRead(t *testing.T) {
	_, c := NewCapture(context.Background())
	const size = 64 << 10
	body := c.TeeResponse(200, io.NopCloser(strings.NewReader(strings.Repeat("x", size))))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := io.Copy(io.Discard, body); err != nil {
			t.Error("wire read failed")
		}
		if err := body.Close(); err != nil {
			t.Error("wire close failed")
		}
	}()
	for i := 0; i < 32; i++ {
		meta := c.Metadata()
		if len(meta) != 1 || meta[0].Status != 200 || meta[0].BodyBytes < 0 || meta[0].BodyBytes > size {
			t.Fatal("inconsistent metadata snapshot")
		}
	}
	wg.Wait()
	if c.Metadata()[0].BodyBytes != size {
		t.Fatal("final snapshot lost wire size")
	}
}

func TestMetadataCaptureDoesNotRetainWireBodies(t *testing.T) {
	ctx, c := NewMetadataCapture(context.Background())
	if CaptureFrom(ctx) != c {
		t.Fatal("metadata capture not retrievable from context")
	}
	c.SetRequest("synthetic-private-request")
	body := c.TeeResponse(200, io.NopCloser(strings.NewReader("synthetic-private-response")))
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if c.RawRequest() != "" || c.RawResponse() != "" {
		t.Fatal("metadata capture retained raw wire data")
	}
	if attempts := c.Attempts(); len(attempts) != 1 || attempts[0].Status != 200 || attempts[0].Body != "" {
		t.Fatalf("metadata capture exposed attempt body: %+v", attempts)
	}
	if got := c.Metadata(); len(got) != 1 || got[0].BodyBytes != len("synthetic-private-response") {
		t.Fatalf("metadata capture lost byte count: %+v", got)
	}
}

func TestMetadataCaptureTracksEmptyRequestPresence(t *testing.T) {
	_, c := NewMetadataCapture(context.Background())
	c.SetRequest("")
	if request, response := c.recordingPresence(); request || response {
		t.Fatalf("presence = request:%v response:%v", request, response)
	}
}

func TestMetadataCaptureLargeRetriesNeverAllocateBodyStorage(t *testing.T) {
	_, c := NewMetadataCapture(context.Background())
	c.SetRequest(strings.Repeat("private-request", 1<<16))
	var expected []AttemptMetadata
	for _, body := range []string{"합성-비밀", strings.Repeat("x", 1<<20), ""} {
		reader := c.TeeResponse(200, io.NopCloser(strings.NewReader(body)))
		if n, err := io.Copy(io.Discard, reader); err != nil || n != int64(len(body)) {
			t.Fatal("metadata capture altered the response stream")
		}
		_ = reader.Close()
		expected = append(expected, AttemptMetadata{Status: 200, BodyBytes: len(body)})
	}
	// An unread retry still has a response attempt, but zero observed bytes.
	unread := c.TeeResponse(429, io.NopCloser(strings.NewReader("unread-secret")))
	_ = unread.Close()
	expected = append(expected, AttemptMetadata{Status: 429})
	if !reflect.DeepEqual(c.Metadata(), expected) {
		t.Fatal("metadata capture lost retry byte counts or statuses")
	}
	// Check storage itself: empty getters alone could hide retained bodies.
	if c.request != "" {
		t.Fatal("metadata capture allocated a request copy")
	}
	for _, a := range c.attempts {
		if a.body.Len() != 0 {
			t.Fatal("metadata capture allocated response storage")
		}
	}
	if c.RawResponse() != "" {
		t.Fatal("metadata retries exposed raw framing")
	}
}

func TestMetadataByteCounterSaturates(t *testing.T) {
	maximum := int(^uint(0) >> 1)
	if saturatingAdd(maximum-1, 2) != maximum || saturatingAdd(maximum, 1) != maximum || saturatingAdd(3, 4) != 7 {
		t.Fatal("metadata byte count overflowed or changed ordinary sums")
	}
}

type metadataCaptureProvider struct {
	streamMetadataOnly   bool
	completeMetadataOnly bool
}

func (p *metadataCaptureProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		capture := CaptureFrom(ctx)
		p.streamMetadataOnly = capture != nil && capture.metadataOnly
		_ = yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil)
	}
}

func (p *metadataCaptureProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	capture := CaptureFrom(ctx)
	p.completeMetadataOnly = capture != nil && capture.metadataOnly
	return llm.Message{}, "stop", llm.Usage{}, nil
}

func TestRecorderUsesMetadataCaptureForStreamAndComplete(t *testing.T) {
	provider := &metadataCaptureProvider{}
	recorder := Wrap(provider, nil, "", "", "", "", nil)
	seq := recorder.Stream(context.Background(), llm.CompletionRequest{})
	seq(func(llm.StreamEvent, error) bool { return true })
	if !provider.streamMetadataOnly {
		t.Fatal("Stream did not bind metadata-only capture")
	}
	if _, _, _, err := recorder.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if !provider.completeMetadataOnly {
		t.Fatal("Complete did not bind metadata-only capture")
	}
}

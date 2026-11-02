package llmrec

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
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

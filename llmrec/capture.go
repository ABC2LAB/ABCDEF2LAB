package llmrec

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

type captureContextKey struct{}

// Capture tracks one logical LLM call. NewCapture keeps untouched wire bodies
// for an explicitly raw diagnostic path; NewMetadataCapture keeps only status,
// presence and byte counts for Recorder/connection diagnostics. The HTTP
// transport that norma dials through finds either mode on the context.
//
// This exists because everything the Recorder itself sees is already normalized:
// llm.CompletionRequest is re-serialized rather than the body buildBody() sent,
// and the response arrives as decoded StreamEvents, not the SSE frames. For
// debugging a live provider, the bytes on the wire are the only ground truth.
//
// norma's doStream retries the request-establishment phase, so one Stream can
// issue several HTTP attempts. Each attempt is kept: the discarded ones (see
// norma/llm/retry.go, which closes non-final bodies unread) are exactly what
// makes rate-limit and gateway failures diagnosable.
//
// The transport writes from norma's stream-reading goroutine while the Recorder
// snapshots at stream end, so all state is mutex-guarded.
type Capture struct {
	mu           sync.Mutex
	metadataOnly bool
	request      string
	requestSeen  bool
	attempts     []*attempt
}

type attempt struct {
	status    int
	body      strings.Builder
	bodyBytes int
}

// NewCapture returns a context carrying a fresh Capture, plus the Capture itself.
func NewCapture(ctx context.Context) (context.Context, *Capture) {
	c := &Capture{}
	return context.WithValue(ctx, captureContextKey{}, c), c
}

// NewMetadataCapture records only attempt status, body presence and byte
// counts. It is used at persistence/diagnostics boundaries where copying
// provider bodies would create an unnecessary transient secret retention path.
func NewMetadataCapture(ctx context.Context) (context.Context, *Capture) {
	c := &Capture{metadataOnly: true}
	return context.WithValue(ctx, captureContextKey{}, c), c
}

// CaptureFrom returns the Capture attached to ctx, or nil when raw capture is
// off. Callers must tolerate nil — recording is a toggle, and non-recorded
// providers dial through the same transport.
func CaptureFrom(ctx context.Context) *Capture {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(captureContextKey{}).(*Capture)
	return c
}

// SetRequest records request presence and, in raw mode, stores the outgoing
// request body. Retries re-send identical bytes, so only the first raw body is kept.
func (c *Capture) SetRequest(body string) {
	if c == nil || body == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestSeen = true
	if c.metadataOnly {
		return
	}
	if c.request == "" {
		c.request = body
	}
}

// MarkRequest records that the transport attempted a request without retaining
// or reconstructing its body. It is used by metadata-only diagnostics.
func (c *Capture) MarkRequest() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.requestSeen = true
	c.mu.Unlock()
}

// SetRequestSnapshot supplies a lazy raw request snapshot. Metadata-only
// captures return before invoking snapshot, so the provider transport never
// asks net/http for a second body copy on the persistence path.
func (c *Capture) SetRequestSnapshot(snapshot func() string) {
	if c == nil || snapshot == nil {
		return
	}
	c.mu.Lock()
	metadataOnly, alreadySet := c.metadataOnly, c.request != ""
	c.mu.Unlock()
	if metadataOnly || alreadySet {
		return
	}
	c.SetRequest(snapshot())
}

// TeeResponse opens a new attempt and wraps rc so everything read from it is
// mirrored into that attempt. It tees rather than reads because a successful
// response is an SSE stream that must keep streaming to the caller.
func (c *Capture) TeeResponse(status int, rc io.ReadCloser) io.ReadCloser {
	if c == nil || rc == nil {
		return rc
	}
	a := &attempt{status: status}
	c.mu.Lock()
	c.attempts = append(c.attempts, a)
	c.mu.Unlock()
	return &teeBody{rc: rc, c: c, a: a}
}

// RawRequest returns the request body as sent, or "" if nothing was captured.
func (c *Capture) RawRequest() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request
}

// RawResponse returns the response bytes as received. A single attempt yields
// the untouched original (copy-pasteable straight into a replay); multiple
// attempts are concatenated behind per-attempt header lines so a retry sequence
// stays readable.
func (c *Capture) RawResponse() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.metadataOnly {
		return ""
	}
	switch len(c.attempts) {
	case 0:
		return ""
	case 1:
		return c.attempts[0].body.String()
	}
	var b strings.Builder
	for i, a := range c.attempts {
		fmt.Fprintf(&b, "===== attempt %d/%d — HTTP %d =====\n", i+1, len(c.attempts), a.status)
		body := a.body.String()
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Attempt is one HTTP round trip's status code and response bytes.
type Attempt struct {
	Status int
	Body   string
}

// Attempts returns every round trip in order. Unlike RawResponse — which drops
// the header line for a lone attempt so the bytes stay replayable — this always
// carries the status code, for callers that must report "HTTP 401 + body".
func (c *Capture) Attempts() []Attempt {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Attempt, 0, len(c.attempts))
	for _, a := range c.attempts {
		out = append(out, Attempt{Status: a.status, Body: a.body.String()})
	}
	return out
}

// teeBody mirrors reads into a Capture attempt, guarded by the Capture's mutex
// so a snapshot taken mid-stream never races the writer.
type teeBody struct {
	rc io.ReadCloser
	c  *Capture
	a  *attempt
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.c.mu.Lock()
		t.a.bodyBytes = saturatingAdd(t.a.bodyBytes, n)
		if !t.c.metadataOnly {
			t.a.body.Write(p[:n])
		}
		t.c.mu.Unlock()
	}
	return n, err
}

func (t *teeBody) Close() error { return t.rc.Close() }

func saturatingAdd(current, added int) int {
	if added > 0 && current > int(^uint(0)>>1)-added {
		return int(^uint(0) >> 1)
	}
	return current + added
}

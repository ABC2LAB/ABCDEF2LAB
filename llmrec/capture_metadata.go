package llmrec

// AttemptMetadata describes a wire attempt without copying its body. Status is
// the observed HTTP status, before any provider-specific quota normalization.
type AttemptMetadata struct {
	Status    int
	BodyBytes int
}

// Metadata returns independent status/size snapshots. Raw bodies remain private
// to Capture's existing wire API and never enter this diagnostics projection.
func (c *Capture) Metadata() []AttemptMetadata {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AttemptMetadata, len(c.attempts))
	for i, a := range c.attempts {
		out[i] = AttemptMetadata{Status: a.status, BodyBytes: a.body.Len()}
	}
	return out
}

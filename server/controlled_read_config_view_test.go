package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
)

func TestConfigHandleCopiesAndBindsMetadataOnly(t *testing.T) {
	r, e, cfg := realInputsTestFixture()
	secret := "synthetic-config-view-secret"
	life := context.WithValue(t.Context(), struct{}{}, secret)
	cfg.Sessions[0].Headers = http.Header{"Authorization": {secret}}
	cfg.Resources[0].URL = "https://fixture.invalid/" + secret
	h, close, err := newControlledReadConfigHandle(life, r.TaskID, r.IntentID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	// Neither original registration mutation nor returned slice mutation can
	// alter the private frozen projection; no transmitter is instantiated here.
	cfg.Sessions[0].Account.Handle = "changed"
	cfg.Resources[0].Accounts[0] = "changed"
	v, err := h.View(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(v)
	if err != nil || strings.Contains(string(encoded), secret) {
		t.Fatal("view leaked secret")
	}
	for _, value := range []any{h, *h} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, value), secret) {
				t.Fatal("handle formatting leaked context")
			}
		}
	}
	encoded, err = json.Marshal(h)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("handle serialized state")
	}
	v.Sessions[0].Account.Handle = "mutated-view"
	v.Resources[0].Accounts[0] = "mutated-view"
	kg := &realInputsTestKG{}
	source := e3record.SourceReader(func(context.Context, evidencebind.Context) (e3record.Source, error) {
		t.Fatal("early Source call")
		return e3record.Source{}, nil
	})
	plan, err := h.Bind(t.Context(), r, e, kg, source)
	if err != nil || plan.Expected != e || kg.calls != 0 {
		t.Fatal("frozen binding changed")
	}
	// The sanitized bridge has no send-capable references and is never returned.
	if cfg.Sessions[0].Headers.Get("Authorization") != secret {
		t.Fatal("source headers modified")
	}
}

func TestConfigHandleRejectsOtherRequestAndZero(t *testing.T) {
	r, _, cfg := realInputsTestFixture()
	h, close, err := newControlledReadConfigHandle(t.Context(), r.TaskID, r.IntentID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	if _, err = h.View(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"task", "intent", "run", "reset", "resource", "account"} {
		t.Run(field, func(t *testing.T) {
			other := r
			switch field {
			case "task":
				other.TaskID++
			case "intent":
				other.IntentID++
			case "run":
				other.Scope.RunRef = "other"
			case "reset":
				other.Scope.ResetRef = "other"
			case "resource":
				other.ResourceRef = "other"
			case "account":
				other.AccountRef = "other"
			}
			v, err := h.View(t.Context(), other)
			if err == nil || len(v.Sessions) != 0 {
				t.Fatal("other request accepted")
			}
		})
	}
	for _, h := range []*ControlledReadConfigHandle{nil, {}} {
		if _, err := h.View(t.Context(), r); err == nil {
			t.Fatal("unminted handle accepted")
		}
	}
	var decoded ControlledReadConfigHandle
	if json.Unmarshal([]byte(`{}`), &decoded) != nil {
		t.Fatal("decode")
	}
	if _, err := decoded.View(t.Context(), r); err == nil {
		t.Fatal("JSON manufactured authority")
	}
}

func TestConfigHandleLifecycleAndExpiry(t *testing.T) {
	for _, mode := range []string{"cancel-life", "cancel-query", "close", "expire"} {
		t.Run(mode, func(t *testing.T) {
			r, _, cfg := realInputsTestFixture()
			life, cancel := context.WithCancel(t.Context())
			defer cancel()
			h, close, err := newControlledReadConfigHandle(life, r.TaskID, r.IntentID, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			query := t.Context()
			switch mode {
			case "cancel-life":
				cancel()
			case "cancel-query":
				c, stop := context.WithCancel(query)
				stop()
				query = c
			case "close":
				close()
				close()
			case "expire":
				h.state.mu.Lock()
				h.state.until = time.Now().Add(-time.Second)
				h.state.mu.Unlock()
			}
			if _, err := h.View(query, r); err == nil {
				t.Fatal("expired/cancelled/closed query accepted")
			}
		})
	}
	r, _, cfg := realInputsTestFixture()
	cfg.Sessions[0].ExpiresAt = time.Now().Add(-time.Second)
	if _, close, err := newControlledReadConfigHandle(t.Context(), r.TaskID, r.IntentID, cfg); err == nil || close != nil {
		t.Fatal("expired registration minted")
	}
}

// A future pointer field for this handle preserves the request's comparability.
var _ = ControlledReadVerificationRequest{} == ControlledReadVerificationRequest{}

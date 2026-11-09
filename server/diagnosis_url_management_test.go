package server

import (
	"context"
	"testing"
)

func TestURLDiagnosisManagementRejectsMissingHost(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, err := NewURLDiagnosisManagementHandler(ctx, nil, "", "", nil); err == nil {
			t.Fatal("missing host accepted")
		}
	}
}

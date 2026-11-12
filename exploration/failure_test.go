package exploration

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestFailureReportsOnlyFixedStageAndClassification(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	failure := ReportFailure("fixture-private-marker", errors.New("fixture-private-marker"))
	if failure.Stage != "operation" || failure.Kind != "operation_failed" || !errors.Is(failure, ErrUnavailable) || strings.Contains(output.String()+failure.Error(), "fixture-private-marker") {
		t.Fatal("unsafe diagnostic error exposed raw values")
	}
	for _, item := range []struct {
		err  error
		kind string
	}{{context.DeadlineExceeded, "deadline"}, {context.Canceled, "cancelled"}, {ErrBinding, "binding_invalid"}, {ErrClosed, "session_closed"}, {ErrToolResult, "tool_error"}} {
		if got := ReportFailure("browser_close", item.err); got.Kind != item.kind {
			t.Fatal("failure classification lost")
		}
	}
}

package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/taskmode"
)

func TestTaskMarkerReaderRejectsAmbiguousJSONFields(t *testing.T) {
	cases := []struct{ name, body string }{
		{"controlled_case_alias", `{"task_id":"7","controlled":false,"Controlled":true}`},
		{"task_case_alias", `{"Task_ID":"8","task_id":"7","controlled":true}`},
		{"intent_case_alias", `{"task_id":"7","Intent_ID":9,"intent_id":0,"controlled":true}`},
		{"single_case_variant", `{"TASK_ID":"7","controlled":true}`},
		{"null_optional_intent", `{"task_id":"7","intent_id":null,"controlled":true}`},
		{"exact_duplicate", `{"task_id":"7","controlled":false,"controlled":true}`},
		{"escaped_duplicate", `{"task_id":"7","controlled":false,"\u0063ontrolled":true}`},
		{"unknown_field", `{"task_id":"7","controlled":true,"extra":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			writeTaskMarker(t, options.RootPath, "7", 0, tc.body)
			// A malformed reserved marker must block both its own task and an
			// otherwise clear task; a partial inventory is never authority.
			for _, taskID := range []int64{7, 8} {
				got, err := readTaskModeMarkers(context.Background(), options, taskID, defaultTaskMarkerOps())
				if err == nil || got != (taskmode.Marker{}) {
					t.Fatalf("task %d accepted malformed marker: %#v %v", taskID, got, err)
				}
			}
		})
	}
}

func TestTaskMarkerReaderJSONCompatibility(t *testing.T) {
	for _, body := range []string{
		`{"task_id":"7","controlled":true}`,
		`{"controlled":true,"intent_id":0,"\u0074ask_id":"7"}`,
	} {
		t.Run(body, func(t *testing.T) {
			options, _ := markerReaderFixture(t)
			writeTaskMarker(t, options.RootPath, "7", 0, body)
			got, err := readFixture(t, options, 7)
			if err != nil || got.TaskID != 7 || got.State != taskmode.MarkerPresent {
				t.Fatalf("canonical marker refused: %#v %v", got, err)
			}
		})
	}
}

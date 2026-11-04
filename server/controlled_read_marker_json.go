package server

import (
	"bytes"
	"encoding/json"
	"io"
)

// Decode marker keys directly: struct decoding treats case aliases as the same
// field and accepts null for scalar fields. Neither is a valid marker binding.
func decodeTaskModeMarker(data []byte) (markerFile, error) {
	invalid := func() (markerFile, error) { return markerFile{}, errTaskMarkerReaderRead }
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return invalid()
	}
	var marker markerFile
	seen := make(map[string]bool, 3)
	for d.More() {
		token, err := d.Token()
		key, isString := token.(string)
		if err != nil || !isString || seen[key] {
			return invalid()
		}
		seen[key] = true
		value, err := d.Token()
		if err != nil {
			return invalid()
		}
		switch key {
		case "task_id":
			task, ok := value.(string)
			if !ok {
				return invalid()
			}
			marker.TaskID = task
		case "intent_id":
			number, ok := value.(json.Number)
			if !ok {
				return invalid()
			}
			intent, err := number.Int64()
			if err != nil || intent < 0 {
				return invalid()
			}
			marker.IntentID = intent
		case "controlled":
			controlled, ok := value.(bool)
			if !ok {
				return invalid()
			}
			marker.Controlled = controlled
		default:
			return invalid()
		}
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') || !seen["task_id"] || !seen["controlled"] {
		return invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid()
	}
	return marker, nil
}

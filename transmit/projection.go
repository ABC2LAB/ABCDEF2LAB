package transmit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/idor"
)

const maxJSONDepth = 32

func project(body []byte, contentType, resource string, rule *JSONViewRule, request *http.Request, proposal idor.AccessRequest) idor.ResponseView {
	view := idor.ResponseView{ResourceRef: resource}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if rule == nil || !utf8.Valid(body) || err != nil || mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return view
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := strictValue(decoder, 0)
	if err != nil {
		return view
	}
	if _, err := decoder.Token(); err != io.EOF {
		return view
	}
	status, ok := atPath(value, rule.StatusPath).(string)
	if !ok {
		return view
	}
	for _, errorValue := range rule.ErrorValues {
		if status == errorValue {
			view.TemplateClassified, view.ErrorTemplate = true, true
			return view
		}
	}
	data, ok := atPath(value, rule.DataPath).(map[string]any)
	if !ok || len(data) == 0 || status != rule.NormalValue {
		return view
	}
	// The registered fixture shape requires a string field, not just a status
	// discriminator. No unregistered or partially matched value leaves this scope.
	shapeMatches := false
	matched := make(map[string]bool)
	var markers []string
	for _, marker := range rule.Markers {
		field, isString := data[marker.Field].(string)
		if !isString || field == "" || marker.Trivial {
			continue
		}
		shapeMatches = true
		if field == marker.Value && !echoed(marker.Value, request, proposal) && !matched[marker.Value] {
			matched[marker.Value] = true
			markers = append(markers, marker.Value)
		}
	}
	if shapeMatches {
		view.Normal, view.TemplateClassified = true, true
		view.Body = strings.Join(markers, "\n")
	}
	return view
}

// The existing verifier enumerates logical inputs only. Suppress a marker that
// could have been reflected from the actual fixed URI, session headers or input
// references before its incomplete input list reaches the comparator.
func echoed(marker string, req *http.Request, proposal idor.AccessRequest) bool {
	sources := []string{
		req.Method, req.URL.String(), req.URL.Path, req.URL.Host, req.URL.RawQuery,
		proposal.ResourceRef, proposal.As.Label, proposal.As.Handle, "Connection: close",
	}
	if decoded, err := url.QueryUnescape(req.URL.RawQuery); err == nil {
		sources = append(sources, decoded)
	}
	sources = append(sources, proposal.Inputs...)
	for key, values := range req.Header {
		sources = append(sources, key)
		sources = append(sources, values...)
	}
	for _, source := range sources {
		if strings.Contains(source, marker) {
			return true
		}
	}
	return false
}

func atPath(value any, path []string) any {
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

// encoding/json normally accepts duplicate keys (last wins). Such ambiguous
// payloads cannot establish evidence, including escaped aliases of the same key.
func strictValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("transmit: JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("transmit: JSON key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("transmit: duplicate JSON key")
			}
			value, err := strictValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, errors.New("transmit: JSON object")
		}
		return object, nil
	case '[':
		var array []any
		for decoder.More() {
			value, err := strictValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, errors.New("transmit: JSON array")
		}
		return array, nil
	default:
		return nil, errors.New("transmit: JSON delimiter")
	}
}

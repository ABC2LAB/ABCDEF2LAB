package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
)

// The engine (baseline and ours alike) sends no sampling parameters, so the
// gateway pins the declared ones on every api request, the same way it pins the
// provider. An engine body that carries one of them is refused rather than
// overwritten: two values for one parameter would mix conditions.

const (
	// The flag holds seven small numbers; anything longer is not a sampling object.
	maxSamplingFlagBytes = 512
	maxTemperature       = 2
	maxTopK              = 1000
	maxPenalty           = 2
)

// samplingKeys are refused in engine bodies. frequency_penalty is not pinned
// but changes sampling too, so an engine-chosen value is refused as well.
var samplingKeys = map[string]bool{
	"temperature": true, "top_p": true, "top_k": true, "min_p": true,
	"presence_penalty": true, "repetition_penalty": true, "seed": true, "frequency_penalty": true,
}

// samplingParams is what the gateway applied; the verified marker records it so
// the runner compares applied values, not the declaration.
type samplingParams struct {
	Temperature       float64 `json:"temperature"`
	TopP              float64 `json:"top_p"`
	TopK              int64   `json:"top_k"`
	MinP              float64 `json:"min_p"`
	PresencePenalty   float64 `json:"presence_penalty"`
	RepetitionPenalty float64 `json:"repetition_penalty"`
	Seed              int64   `json:"seed"`
}

// parseSampling accepts exactly the seven keys with values inside the ranges
// providers accept; a missing key would silently fall back to a provider default.
func parseSampling(raw string) (*samplingParams, bool) {
	if raw == "" || len(raw) > maxSamplingFlagBytes {
		return nil, false
	}
	// Raw values, so a quoted number or null is refused instead of being read as a number.
	var fields struct {
		Temperature       json.RawMessage `json:"temperature"`
		TopP              json.RawMessage `json:"top_p"`
		TopK              json.RawMessage `json:"top_k"`
		MinP              json.RawMessage `json:"min_p"`
		PresencePenalty   json.RawMessage `json:"presence_penalty"`
		RepetitionPenalty json.RawMessage `json:"repetition_penalty"`
		Seed              json.RawMessage `json:"seed"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fields); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	var p samplingParams
	ok := true
	literal := func(value json.RawMessage) (string, bool) {
		text := string(value)
		if text == "" || text[0] == '"' || text == "null" || text == "true" || text == "false" {
			return "", false
		}
		return text, true
	}
	number := func(value json.RawMessage, low, high float64, lowOpen bool) float64 {
		text, present := literal(value)
		if !present {
			ok = false
			return 0
		}
		v, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v > high || v < low || (lowOpen && v == low) {
			ok = false
		}
		return v
	}
	integer := func(value json.RawMessage, low, high int64) int64 {
		text, present := literal(value)
		if !present {
			ok = false
			return 0
		}
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil || v < low || v > high {
			ok = false
		}
		return v
	}
	p.Temperature = number(fields.Temperature, 0, maxTemperature, false)
	p.TopP = number(fields.TopP, 0, 1, true)
	p.TopK = integer(fields.TopK, 1, maxTopK)
	p.MinP = number(fields.MinP, 0, 1, false)
	p.PresencePenalty = number(fields.PresencePenalty, -maxPenalty, maxPenalty, false)
	p.RepetitionPenalty = number(fields.RepetitionPenalty, 0, maxPenalty, true)
	p.Seed = integer(fields.Seed, 0, math.MaxInt64)
	if !ok {
		return nil, false
	}
	return &p, true
}

// fields renders the parameters as JSON object members without the braces.
func (p *samplingParams) fields() []byte {
	encoded, _ := json.Marshal(p)
	return encoded[1 : len(encoded)-1]
}

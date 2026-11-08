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
// gateway pins the declared ones on every request. An engine body that carries
// one of them is refused rather than overwritten: two values for one parameter
// would mix conditions. api and local pin different sets, because each set is
// what that upstream actually applies. A local server's OpenAI path ignores
// top_k, min_p and repetition_penalty without an error, so pinning them would
// record values that were never used (profiles/evidence/ollama-openai-sampling).

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

// samplingPin renders parameters as JSON object members without the braces.
type samplingPin interface {
	fields() []byte
}

// localSamplingParams is what a local server's OpenAI path applies.
type localSamplingParams struct {
	Temperature      float64 `json:"temperature"`
	TopP             float64 `json:"top_p"`
	PresencePenalty  float64 `json:"presence_penalty"`
	FrequencyPenalty float64 `json:"frequency_penalty"`
	Seed             int64   `json:"seed"`
}

// parseLocalSampling accepts exactly the five local keys, for the same reason
// as parseSampling.
func parseLocalSampling(raw string) (*localSamplingParams, bool) {
	var fields struct {
		Temperature      json.RawMessage `json:"temperature"`
		TopP             json.RawMessage `json:"top_p"`
		PresencePenalty  json.RawMessage `json:"presence_penalty"`
		FrequencyPenalty json.RawMessage `json:"frequency_penalty"`
		Seed             json.RawMessage `json:"seed"`
	}
	if !decodeSamplingFlag(raw, &fields) {
		return nil, false
	}
	r := samplingReader{ok: true}
	p := localSamplingParams{
		Temperature:      r.number(fields.Temperature, 0, maxTemperature, false),
		TopP:             r.number(fields.TopP, 0, 1, true),
		PresencePenalty:  r.number(fields.PresencePenalty, -maxPenalty, maxPenalty, false),
		FrequencyPenalty: r.number(fields.FrequencyPenalty, -maxPenalty, maxPenalty, false),
		Seed:             r.integer(fields.Seed, 0, math.MaxInt64),
	}
	if !r.ok {
		return nil, false
	}
	return &p, true
}

func (p *localSamplingParams) fields() []byte {
	return memberBytes(p)
}

// parseSampling accepts exactly the seven keys with values inside the ranges
// providers accept; a missing key would silently fall back to a provider default.
func parseSampling(raw string) (*samplingParams, bool) {
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
	if !decodeSamplingFlag(raw, &fields) {
		return nil, false
	}
	r := samplingReader{ok: true}
	p := samplingParams{
		Temperature:       r.number(fields.Temperature, 0, maxTemperature, false),
		TopP:              r.number(fields.TopP, 0, 1, true),
		TopK:              r.integer(fields.TopK, 1, maxTopK),
		MinP:              r.number(fields.MinP, 0, 1, false),
		PresencePenalty:   r.number(fields.PresencePenalty, -maxPenalty, maxPenalty, false),
		RepetitionPenalty: r.number(fields.RepetitionPenalty, 0, maxPenalty, true),
		Seed:              r.integer(fields.Seed, 0, math.MaxInt64),
	}
	if !r.ok {
		return nil, false
	}
	return &p, true
}

// decodeSamplingFlag reads one object with no unknown key and nothing after it.
func decodeSamplingFlag(raw string, fields any) bool {
	if raw == "" || len(raw) > maxSamplingFlagBytes {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(fields); err != nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// samplingReader checks raw values and remembers the first failure.
type samplingReader struct{ ok bool }

func (r *samplingReader) literal(value json.RawMessage) (string, bool) {
	text := string(value)
	if text == "" || text[0] == '"' || text == "null" || text == "true" || text == "false" {
		r.ok = false
		return "", false
	}
	return text, true
}

func (r *samplingReader) number(value json.RawMessage, low, high float64, lowOpen bool) float64 {
	text, present := r.literal(value)
	if !present {
		return 0
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v > high || v < low || (lowOpen && v == low) {
		r.ok = false
	}
	return v
}

func (r *samplingReader) integer(value json.RawMessage, low, high int64) int64 {
	text, present := r.literal(value)
	if !present {
		return 0
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil || v < low || v > high {
		r.ok = false
	}
	return v
}

// memberBytes renders a struct as JSON object members without the braces.
func memberBytes(v any) []byte {
	encoded, _ := json.Marshal(v)
	return encoded[1 : len(encoded)-1]
}

func (p *samplingParams) fields() []byte {
	return memberBytes(p)
}

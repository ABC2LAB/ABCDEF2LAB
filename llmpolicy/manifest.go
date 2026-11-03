package llmpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxProfileManifestBytes bounds the full JSON document, including whitespace.
// File loaders must also bound the read before calling the parser.
const MaxProfileManifestBytes = 1 << 20

var ErrInvalidProfileManifest = errors.New("llmpolicy: invalid profile manifest")

// ParseProfileManifest compiles host-supplied operator policy bytes. It performs
// no I/O and authenticates neither the bytes' source nor an existing provider.
// A failure never returns a partial registry or selects an alternative policy.
func ParseProfileManifest(data []byte) (*ProfileResolver, error) {
	if len(data) == 0 || len(data) > MaxProfileManifestBytes || !validManifestEncoding(data) {
		return nil, ErrInvalidProfileManifest
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var pins []ProfilePin
	valid := manifestObject(d, 2, func(key string) bool {
		switch key {
		case "version":
			token, err := d.Token()
			return err == nil && token == json.Number("1")
		case "profiles":
			if !manifestDelimiter(d, '[') {
				return false
			}
			for d.More() {
				if len(pins) >= maxProfileBindings {
					return false
				}
				pin, ok := manifestProfilePin(d)
				if !ok {
					return false
				}
				pins = append(pins, pin)
			}
			return manifestDelimiter(d, ']')
		default:
			return false
		}
	})
	if !valid {
		return nil, ErrInvalidProfileManifest
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalidProfileManifest
	}
	resolver, err := NewProfileResolver(pins)
	if err != nil {
		return nil, ErrInvalidProfileManifest
	}
	return resolver, nil
}

func manifestProfilePin(d *json.Decoder) (ProfilePin, bool) {
	var pin ProfilePin
	valid := manifestObject(d, 5, func(key string) bool {
		var ok bool
		switch key {
		case "profile_id":
			token, err := d.Token()
			number, isNumber := token.(json.Number)
			if err != nil || !isNumber {
				return false
			}
			id, err := strconv.ParseInt(string(number), 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != string(number) {
				return false
			}
			pin.Target.ID = id
			return true
		case "format":
			pin.Target.Format, ok = manifestString(d, maxProfileFormatBytes)
		case "model":
			pin.Target.Model, ok = manifestString(d, maxProfileModelBytes)
		case "base_url":
			pin.Target.BaseURL, ok = manifestString(d, maxURLBytes)
		case "addresses":
			if !manifestDelimiter(d, '[') {
				return false
			}
			for d.More() {
				if len(pin.Addresses) >= maxAddresses {
					return false
				}
				token, err := d.Token()
				raw, isString := token.(string)
				if err != nil || !isString {
					return false
				}
				address, err := netip.ParseAddr(raw)
				if err != nil {
					return false
				}
				pin.Addresses = append(pin.Addresses, address)
			}
			return manifestDelimiter(d, ']')
		default:
			return false
		}
		return ok
	})
	return pin, valid
}

// Decode keys explicitly: struct decoding would also accept case variants, and
// maps would discard duplicates. Count plus the field visitor requires every key.
func manifestObject(d *json.Decoder, required int, field func(string) bool) bool {
	if !manifestDelimiter(d, '{') {
		return false
	}
	seen := make(map[string]bool, required)
	for d.More() {
		token, err := d.Token()
		key, isString := token.(string)
		if err != nil || !isString || seen[key] || !field(key) {
			return false
		}
		seen[key] = true
	}
	return manifestDelimiter(d, '}') && len(seen) == required
}

func manifestDelimiter(d *json.Decoder, expected json.Delim) bool {
	token, err := d.Token()
	return err == nil && token == expected
}

func manifestString(d *json.Decoder, limit int) (string, bool) {
	token, err := d.Token()
	value, ok := token.(string)
	return value, err == nil && ok && len(value) > 0 && len(value) <= limit
}

func validManifestEncoding(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	// encoding/json repairs invalid surrogate escapes. Reject them before token
	// decoding so exact profile bindings never depend on replacement characters.
	// JSON grammar remains the decoder's job; non-Unicode escapes are skipped.
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(data) {
			return false
		}
		unit, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		if utf16.IsSurrogate(rune(unit)) {
			if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
				return false
			}
			next, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
			if err != nil || utf16.DecodeRune(rune(unit), rune(next)) == utf8.RuneError {
				return false
			}
			i += 6
		}
		i += 5
	}
	return true
}

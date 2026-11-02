package auditlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

const (
	verifyFuzzKeyRef = "fuzz-key-1"
	verifyFuzzID     = "00112233445566778899aabbccddeeff"
	verifyFuzzMax    = 4 * MaxRecordBytes
)

func verifyFuzzScope() transmit.Scope {
	return transmit.Scope{RunRef: "fuzz-run", TaskRef: "fuzz-task", CaseRef: "fuzz-case", ResetRef: "fuzz-reset", PolicyRef: "fuzz-policy"}
}
func verifyFuzzKey() []byte { return bytes.Repeat([]byte{0x31}, KeyBytes) }

// The fixture is entirely in memory and deterministic across fuzz workers.
// The trusted checkpoint is independent of every fuzz argument.
func verifyFuzzFixture(t testing.TB) ([]byte, []byte, Checkpoint) {
	t.Helper()
	key, scope := verifyFuzzKey(), verifyFuzzScope()
	created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	h := header{Version: Version, JournalID: verifyFuzzID, Scope: scope, KeyRef: verifyFuzzKeyRef, CreatedAt: created}
	hmac, err := sign(key, "header", h)
	if err != nil {
		t.Fatal(err)
	}
	headerLine, err := encodeLine(headerLine{Header: h, MAC: hmac})
	if err != nil {
		t.Fatal(err)
	}
	intent := transmit.Event{Stage: "intent", Receipt: transmit.Receipt{Scope: scope, Attempt: 1, AccountRef: "owner", SessionRef: "session-1", ResourceRef: "doc:one", ApprovalRef: "approval-1", Method: http.MethodGet, At: created.Add(time.Second)}}
	first := eventPayload{Sequence: 1, Previous: hmac, Event: intent}
	firstMAC, err := sign(key, "event", first)
	if err != nil {
		t.Fatal(err)
	}
	firstLine, err := encodeLine(eventLine{Record: first, MAC: firstMAC})
	if err != nil {
		t.Fatal(err)
	}
	result := transmit.Event{Stage: "result", Receipt: transmit.Receipt{Scope: scope, Attempt: 1, AccountRef: "owner", SessionRef: "session-1", ResourceRef: "doc:one", ApprovalRef: "approval-1", Method: http.MethodGet, Reason: transmit.Stopped, At: created.Add(2 * time.Second)}}
	second := eventPayload{Sequence: 2, Previous: firstMAC, Event: result}
	secondMAC, err := sign(key, "event", second)
	if err != nil {
		t.Fatal(err)
	}
	secondLine, err := encodeLine(eventLine{Record: second, MAC: secondMAC})
	if err != nil {
		t.Fatal(err)
	}
	wire := append(append(headerLine, firstLine...), secondLine...)
	expected := Checkpoint{Version: Version, JournalID: verifyFuzzID, Scope: scope, KeyRef: verifyFuzzKeyRef, Sequence: 2, MAC: secondMAC}
	summary, err := Verify(context.Background(), bytes.NewReader(wire), key, expected)
	if err != nil || summary.Intents != 1 || summary.Results != 1 || summary.Checkpoint != expected {
		t.Fatalf("deterministic fixture invalid: summary=%+v err=%v", summary, err)
	}
	return wire, key, expected
}

func verifyFuzzFlip(data []byte) []byte {
	clone := append([]byte(nil), data...)
	for i := len(clone) - 1; i >= 0; i-- {
		if clone[i] >= '0' && clone[i] <= '9' {
			clone[i] = 'a'
			return clone
		}
	}
	return append(clone, 'x')
}

func verifyFuzzLine(length int) []byte { return append(bytes.Repeat([]byte{'x'}, length), '\n') }

func verifyFuzzDuplicateCanonical(data []byte) []byte {
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		return data
	}
	line := data[:lineEnd]
	marker := []byte{'"', 'm', 'a', 'c', '"', ':', '"'}
	field := bytes.Index(line, marker)
	if field < 0 {
		return data
	}
	start := field + len(marker)
	end := bytes.IndexByte(line[start:], '"')
	if end < 0 {
		return data
	}
	value := line[start : start+end]
	close := bytes.LastIndexByte(line, '}')
	if close < 0 {
		return data
	}
	duplicate := append([]byte{',', '"', 'm', 'a', 'c', '"', ':', '"'}, value...)
	duplicate = append(duplicate, '"')
	line = append(append(append([]byte{}, line[:close]...), duplicate...), line[close:]...)
	return append(append(append([]byte{}, line...), '\n'), data[lineEnd+1:]...)
}

func verifyFuzzEscapedCanonical(data []byte) []byte {
	return bytes.Replace(data, []byte(`"mac":`), []byte(`"\u006dac":`), 1)
}

func verifyFuzzCompleteTrailingEvent(data []byte) []byte {
	first := bytes.IndexByte(data, '\n') + 1
	second := bytes.IndexByte(data[first:], '\n')
	if first <= 0 || second < 0 {
		return data
	}
	second += first + 1
	return append(append([]byte(nil), data...), data[first:second]...)
}

func verifyFuzzPrefixes(data []byte) (headerOnly, headerIntent []byte) {
	first := bytes.IndexByte(data, '\n') + 1
	second := bytes.IndexByte(data[first:], '\n')
	if first <= 0 || second < 0 {
		return nil, nil
	}
	second += first + 1
	return append([]byte(nil), data[:first]...), append([]byte(nil), data[:second]...)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func verifyFuzzCheck(t *testing.T, input, valid, key []byte, expected Checkpoint) {
	t.Helper()
	if len(input) > verifyFuzzMax {
		return
	}
	original, keyCopy := append([]byte(nil), input...), append([]byte(nil), key...)
	one, errOne := Verify(context.Background(), bytes.NewReader(input), key, expected)
	two, errTwo := Verify(context.Background(), bytes.NewReader(input), key, expected)
	if !bytes.Equal(input, original) || !bytes.Equal(key, keyCopy) {
		t.Fatal("Verify mutated caller input or key")
	}
	if (errOne == nil) != (errTwo == nil) || one != two || errorText(errOne) != errorText(errTwo) {
		t.Fatalf("non-deterministic Verify result: first=%+v/%v second=%+v/%v", one, errOne, two, errTwo)
	}
	if errOne != nil {
		if one != (Summary{}) {
			t.Fatalf("failed verification returned partial summary: %+v, err=%v", one, errOne)
		}
		if bytes.Equal(input, valid) {
			t.Fatal("trusted canonical fixture was rejected")
		}
		return
	}
	if !bytes.Equal(input, valid) {
		t.Fatal("non-identical wire accepted despite canonical-v1 contract")
	}
	if one.Checkpoint != expected || one.Intents != 1 || one.Results != 1 {
		t.Fatalf("accepted summary = %+v", one)
	}
}

func FuzzVerifyTrustedJournalContract(f *testing.F) {
	valid, key, expected := verifyFuzzFixture(f)
	headerOnly, headerIntent := verifyFuzzPrefixes(valid)
	f.Add(valid)
	f.Add(headerOnly)
	f.Add(headerIntent)
	f.Add(verifyFuzzFlip(valid))
	f.Add(verifyFuzzDuplicateCanonical(valid))
	f.Add(verifyFuzzEscapedCanonical(valid))
	f.Add(verifyFuzzCompleteTrailingEvent(valid))
	f.Add(verifyFuzzLine(MaxRecordBytes - 1))
	f.Add(verifyFuzzLine(MaxRecordBytes))
	f.Add(verifyFuzzLine(MaxRecordBytes + 1))
	f.Add(append(append([]byte(nil), valid...), []byte("\xff")...))
	f.Fuzz(func(t *testing.T, input []byte) { verifyFuzzCheck(t, input, valid, key, expected) })
}

func FuzzVerifyRejectsUntrustedConfiguration(f *testing.F) {
	valid, key, expected := verifyFuzzFixture(f)
	f.Add(uint8(0), []byte("scope"))
	f.Add(uint8(1), []byte("key"))
	f.Add(uint8(2), []byte("journal"))
	f.Add(uint8(3), []byte("sequence"))
	f.Add(uint8(4), []byte("mac"))
	f.Add(uint8(5), []byte("wrong-key"))
	f.Add(uint8(6), []byte("cancel"))
	f.Fuzz(func(t *testing.T, selector uint8, replacement []byte) {
		if len(replacement) > 96 {
			return
		}
		cp := expected
		candidateKey := key
		value := string(replacement)
		if value == "" {
			value = "replacement"
		}
		switch selector % 7 {
		case 0:
			if value == cp.Scope.RunRef {
				value += "-x"
			}
			cp.Scope.RunRef = value
		case 1:
			if value == cp.KeyRef {
				value += "-x"
			}
			cp.KeyRef = value
		case 2:
			digest := sha256.Sum256([]byte(value))
			cp.JournalID = hex.EncodeToString(digest[:16])
		case 3:
			cp.Sequence = uint64(len(replacement) + 3)
		case 4:
			last := "0"
			if cp.MAC[len(cp.MAC)-1] == '0' {
				last = "1"
			}
			cp.MAC = cp.MAC[:len(cp.MAC)-1] + last
		case 5:
			candidateKey = append([]byte(nil), key...)
			candidateKey[0] ^= byte(len(replacement) + 1)
		case 6:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			out, err := Verify(ctx, bytes.NewReader(valid), key, expected)
			if err == nil || out != (Summary{}) {
				t.Fatalf("canceled context accepted: %+v err=%v", out, err)
			}
			return
		}
		out, err := Verify(context.Background(), bytes.NewReader(valid), candidateKey, cp)
		if err == nil || out != (Summary{}) {
			t.Fatalf("checkpoint/config variant accepted: %+v err=%v", out, err)
		}
	})
}

type verifyFuzzReader struct {
	data                 []byte
	offset, chunk, fault int
}

func (r *verifyFuzzReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset == r.fault {
		return 0, errors.New("synthetic reader fault")
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	count := r.chunk
	if count > len(p) {
		count = len(p)
	}
	if remaining := len(r.data) - r.offset; count > remaining {
		count = remaining
	}
	if r.fault > r.offset && r.fault < r.offset+count {
		count = r.fault - r.offset
	}
	copy(p[:count], r.data[r.offset:r.offset+count])
	r.offset += count
	return count, nil
}

func FuzzVerifyStreamingReaderContract(f *testing.F) {
	valid, key, expected := verifyFuzzFixture(f)
	headerEnd := bytes.IndexByte(valid, '\n') + 1
	intentEnd := bytes.IndexByte(valid[headerEnd:], '\n') + headerEnd + 1
	f.Add([]byte{1, 1, byte(headerEnd), byte(headerEnd >> 8)})
	f.Add([]byte{1, 1, byte(intentEnd), byte(intentEnd >> 8)})
	f.Add([]byte{1, 0, 0, 0})
	f.Add([]byte{7, 1, 3, 0})
	f.Add([]byte{31, 1, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, controls []byte) {
		if len(controls) < 4 {
			return
		}
		chunk := int(controls[0]%31) + 1
		fault := int(uint16(controls[2])|uint16(controls[3])<<8) % (len(valid) + 1)
		faultEnabled := controls[1]&1 != 0
		if !faultEnabled {
			fault = len(valid) + 1
		}
		out, err := Verify(context.Background(), &verifyFuzzReader{data: valid, chunk: chunk, fault: fault}, key, expected)
		if faultEnabled {
			if err == nil || out != (Summary{}) {
				t.Fatalf("faulting reader accepted/partial: %+v err=%v", out, err)
			}
			return
		}
		if err != nil || out.Checkpoint != expected || out.Intents != 1 || out.Results != 1 {
			t.Fatalf("chunked reader failed: %+v err=%v", out, err)
		}
	})
}

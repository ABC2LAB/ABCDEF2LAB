package reportcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/reproduction"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

type sourceFunc func(context.Context) (Frame, error)

func (f sourceFunc) CurrentFrame(c context.Context) (Frame, error) { return f(c) }

type sinkFunc func(context.Context, Asset) error

func (f sinkFunc) SaveDiagnosticCapture(c context.Context, a Asset) error { return f(c, a) }
func captureFixture() Binding {
	s := reproduction.FromStored(1, "candidate-1", 1, strings.Repeat("a", 64), remediation.Context{Observations: []remediation.ContextObservation{{Source: "observed", RunID: "run-a", CallID: "call-a", StepID: "attempt-1", EvidenceRef: "proof-a", ObservedAt: "2026-11-11T00:00:00Z", Observation: "Actual response"}}})
	return Binding{TaskID: 1, EntryID: "candidate-1", Step: s[0]}
}
func testPNG() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			im.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}
func TestCaptureRequiresApprovalAndMasksPixelsBeforePersistence(t *testing.T) {
	calls := 0
	var got Asset
	src := sourceFunc(func(context.Context) (Frame, error) {
		calls++
		return Frame{PNG: testPNG(), CapturedAt: time.Now()}, nil
	})
	sink := sinkFunc(func(_ context.Context, a Asset) error { got = a; return nil })
	if e := Collect(t.Context(), captureFixture(), Policy{}, src, sink); e != nil || calls != 0 {
		t.Fatal("no approval called source")
	}
	_, c, _, valid := got.Snapshot()
	if !valid || c.Status != "not_supported" || len(c.PNG) != 0 {
		t.Fatal("missing capture misreported")
	}
	if e := Collect(t.Context(), captureFixture(), Policy{ApprovedNonsensitiveRegions: []image.Rectangle{image.Rect(0, 0, 1, 1)}}, src, sink); e != nil || calls != 1 {
		t.Fatal("approved capture failed", e)
	}
	_, c, mask, valid := got.Snapshot()
	if !valid || mask != "approved_regions/v1" || Verify(c) != nil {
		t.Fatal("capture lacks integrity")
	}
	image, e := png.Decode(bytes.NewReader(c.PNG))
	if e != nil {
		t.Fatal(e)
	}
	r, _, _, _ := image.At(0, 0).RGBA()
	outside, _, _, _ := image.At(3, 3).RGBA()
	if r == 0 || outside != 0 {
		t.Fatal("unapproved pixels persisted")
	}
	c.PNG[0] ^= 1
	if Verify(c) == nil {
		t.Fatal("tampered capture accepted")
	}
	_, again, _, _ := got.Snapshot()
	if Verify(again) != nil {
		t.Fatal("capture mutable through snapshot")
	}
}
func TestCaptureRejectsMalformedOrUnboundedSource(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("<svg><script>bad</script></svg>"), bytes.Repeat([]byte("x"), MaxPNGBytes+1)} {
		if _, _, _, e := EncodeSafePNG(raw, []image.Rectangle{image.Rect(0, 0, 1, 1)}); e == nil {
			t.Fatal("invalid image accepted")
		}
	}
	if _, _, _, e := EncodeSafePNG(testPNG(), []image.Rectangle{image.Rect(0, 0, 8, 8)}); e == nil {
		t.Fatal("out of bounds public region")
	}
	if _, _, _, valid := (Asset{}).Snapshot(); valid {
		t.Fatal("forged capture considered trusted")
	}
}

func TestVerifiedImageRejectsTrailingSecretAndMetadata(t *testing.T) {
	var asset Asset
	if e := Collect(t.Context(), captureFixture(), Policy{ApprovedNonsensitiveRegions: []image.Rectangle{image.Rect(0, 0, 1, 1)}}, sourceFunc(func(context.Context) (Frame, error) { return Frame{PNG: testPNG(), CapturedAt: time.Now()}, nil }), sinkFunc(func(_ context.Context, a Asset) error { asset = a; return nil })); e != nil {
		t.Fatal(e)
	}
	_, c, _, _ := asset.Snapshot()
	c.PNG = append(c.PNG, []byte("private-trailing-metadata")...)
	sum := sha256.Sum256(c.PNG)
	c.Digest = hex.EncodeToString(sum[:])
	if Verify(c) == nil {
		t.Fatal("trailing secret exported with valid hash")
	}
}

func TestCaptureSourceFailurePersistsStatusWithoutPixels(t *testing.T) {
	var stored Asset
	err := Collect(t.Context(), captureFixture(), Policy{ApprovedNonsensitiveRegions: []image.Rectangle{image.Rect(0, 0, 1, 1)}}, sourceFunc(func(context.Context) (Frame, error) { return Frame{}, errors.New("private source error") }), sinkFunc(func(_ context.Context, a Asset) error { stored = a; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	_, c, mask, valid := stored.Snapshot()
	if !valid || c.Status != "failed" || len(c.PNG) != 0 || c.Ref != "" || mask != "" {
		t.Fatal("failed source retained pixels or error text")
	}
}

func TestVerifiedImageRejectsRealTextChunkWithValidCRC(t *testing.T) {
	raw := testPNG()
	data := []byte("Comment\x00synthetic-private-marker")
	chunk := make([]byte, 12+len(data))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(data)))
	copy(chunk[4:8], "tEXt")
	copy(chunk[8:], data)
	binary.BigEndian.PutUint32(chunk[8+len(data):], crc32.ChecksumIEEE(chunk[4:8+len(data)]))
	withText := append(append(append([]byte{}, raw[:len(raw)-12]...), chunk...), raw[len(raw)-12:]...)
	if _, err := png.Decode(bytes.NewReader(withText)); err != nil {
		t.Fatal("metadata fixture is not valid PNG", err)
	}
	sum := sha256.Sum256(withText)
	c := reproduction.Capture{Status: "captured", PNG: withText, Digest: hex.EncodeToString(sum[:]), Width: 4, Height: 4, CapturedAt: time.Now()}
	if Verify(c) == nil {
		t.Fatal("valid PNG with private tEXt chunk accepted")
	}
}

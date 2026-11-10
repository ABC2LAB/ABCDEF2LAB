// Package reportcapture은 인가된 실행기의 현재 화면만 받는다. 브라우저 탐색·송신 권한을 발급하지 않는다.
package reportcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"github.com/Autumn-27/artex/reproduction"
	"image"
	"image/draw"
	"image/png"
	"time"
)

const Version = "diagnostic-capture/v1"
const MaxPNGBytes = 1 << 20

var ErrCapture = errors.New("diagnostic_capture_invalid")

type Binding struct {
	TaskID  int64
	EntryID string
	Step    reproduction.Step
}
type Frame struct {
	PNG        []byte
	CapturedAt time.Time
}

// Source 구현은 현재 화면만 읽어야 하며 탐색·로그인·재현을 실행하면 안 된다.
// 실제 E② Browser 실행기가 연결되기 전 제품에는 Source 구현을 설치하지 않는다.
type Source interface {
	CurrentFrame(context.Context) (Frame, error)
}
type Asset struct {
	binding    Binding
	capture    reproduction.Capture
	maskPolicy string
	valid      bool
}

func (a Asset) String() string   { return "reportcapture.Asset[protected]" }
func (a Asset) GoString() string { return a.String() }
func (a Asset) Snapshot() (Binding, reproduction.Capture, string, bool) {
	c := a.capture
	c.PNG = append([]byte(nil), c.PNG...)
	return a.binding, c, a.maskPolicy, a.valid
}

type Sink interface {
	SaveDiagnosticCapture(context.Context, Asset) error
}
type Policy struct{ ApprovedNonsensitiveRegions []image.Rectangle }

func EncodeSafePNG(raw []byte, regions []image.Rectangle) ([]byte, int, int, error) {
	if len(raw) == 0 || len(raw) > MaxPNGBytes || len(regions) < 1 || len(regions) > 16 {
		return nil, 0, 0, ErrCapture
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 8_000_000 {
		return nil, 0, 0, ErrCapture
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, ErrCapture
	}
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), image.Black, image.Point{}, draw.Src)
	for _, r := range regions {
		if r.Empty() || !r.In(src.Bounds()) {
			return nil, 0, 0, ErrCapture
		}
		draw.Draw(dst, r, src, r.Min, draw.Src)
	}
	var out bytes.Buffer
	if png.Encode(&out, dst) != nil || out.Len() > MaxPNGBytes {
		return nil, 0, 0, ErrCapture
	}
	return out.Bytes(), cfg.Width, cfg.Height, nil
}
func Verify(c reproduction.Capture) error {
	if c.Status != "captured" || c.CapturedAt.IsZero() || c.Width < 1 || c.Height < 1 || len(c.PNG) == 0 || len(c.PNG) > MaxPNGBytes {
		return ErrCapture
	}
	h := sha256.Sum256(c.PNG)
	if hex.EncodeToString(h[:]) != c.Digest {
		return ErrCapture
	}
	// 재인코딩한 정적 PNG만 허용한다. 메타데이터·애니메이션·후행 자료는 내보내지 않는다.
	if len(c.PNG) < 8 || !bytes.Equal(c.PNG[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return ErrCapture
	}
	end := false
	for offset := 8; offset < len(c.PNG); {
		if end || len(c.PNG)-offset < 12 {
			return ErrCapture
		}
		n := uint64(binary.BigEndian.Uint32(c.PNG[offset : offset+4]))
		if n+12 > uint64(len(c.PNG)-offset) {
			return ErrCapture
		}
		kind := string(c.PNG[offset+4 : offset+8])
		if kind != "IHDR" && kind != "IDAT" && kind != "IEND" {
			return ErrCapture
		}
		end = kind == "IEND"
		offset += int(n) + 12
	}
	if !end {
		return ErrCapture
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(c.PNG))
	if e != nil || cfg.Width != c.Width || cfg.Height != c.Height || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 8_000_000 {
		return ErrCapture
	}
	if _, e = png.Decode(bytes.NewReader(c.PNG)); e != nil {
		return ErrCapture
	}
	return nil
}

// Collect는 명시적 비민감 영역 승인 없이는 Source를 호출하지 않는다. 보고서 조회에서는 호출하지 않는다.
func Collect(ctx context.Context, b Binding, p Policy, source Source, sink Sink) error {
	if ctx == nil || ctx.Err() != nil || sink == nil || b.TaskID <= 0 || b.EntryID == "" || reproduction.Validate(b.Step, b.Step.EvidenceVersion, b.Step.EvidenceDigest) != nil {
		return ErrCapture
	}
	if source == nil || len(p.ApprovedNonsensitiveRegions) == 0 {
		return sink.SaveDiagnosticCapture(ctx, Asset{binding: b, valid: true, capture: reproduction.Capture{Status: "not_supported"}})
	}
	if len(p.ApprovedNonsensitiveRegions) > 16 {
		return ErrCapture
	}
	for _, region := range p.ApprovedNonsensitiveRegions {
		if region.Empty() || !region.In(image.Rect(0, 0, 4096, 4096)) {
			return ErrCapture
		}
	}
	frame, e := source.CurrentFrame(ctx)
	if e != nil {
		return sink.SaveDiagnosticCapture(ctx, Asset{binding: b, valid: true, capture: reproduction.Capture{Status: "failed"}})
	}
	if frame.CapturedAt.IsZero() || frame.CapturedAt.After(time.Now().Add(time.Second)) {
		return ErrCapture
	}
	raw, width, height, e := EncodeSafePNG(frame.PNG, p.ApprovedNonsensitiveRegions)
	if e != nil {
		return ErrCapture
	}
	sum := sha256.Sum256(raw)
	return sink.SaveDiagnosticCapture(ctx, Asset{binding: b, valid: true, maskPolicy: "approved_regions/v1", capture: reproduction.Capture{Status: "captured", Ref: "shot-" + b.Step.BindingDigest[:24], Digest: hex.EncodeToString(sum[:]), CapturedAt: frame.CapturedAt, Width: width, Height: height, PNG: raw}})
}

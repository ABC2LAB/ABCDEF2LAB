package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/reproduction"
)

func TestImageBudgetOmitsPicturesAndPreservesTextAndSharedPrivacy(t *testing.T) {
	in := diagnosticFixture()
	in.Entries = in.Entries[:1]
	e := &in.Entries[0]
	e.Run = "run-a"
	e.Digest = strings.Repeat("a", 64)
	context := remediation.Context{}
	for i := 0; i < 32; i++ {
		context.Observations = append(context.Observations, remediation.ContextObservation{Source: "observed", Method: "GET", RunID: e.Run, StepID: fmt.Sprintf("actual-%d", i), EvidenceRef: fmt.Sprintf("proof-%d", i), ObservedAt: "2026-11-11T00:00:00Z"})
	}
	e.Steps = reproduction.FromStored(in.TaskID, e.ID, e.EvidenceVersion, e.Digest, context)
	im := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	seed := uint32(1)
	for i := range im.Pix {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		im.Pix[i] = byte(seed)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(out.Bytes())
	for i := range e.Steps {
		step := &e.Steps[i]
		step.Capture = reproduction.Capture{Status: "captured", Ref: "shot-" + step.BindingDigest[:24], PNG: out.Bytes(), Digest: hex.EncodeToString(sum[:]), CapturedAt: time.Now(), Width: 256, Height: 256}
	}
	prepared, err := PrepareDiagnostic(in)
	if err != nil {
		t.Fatal(err)
	}
	omitted, captured := 0, 0
	for _, step := range prepared.Entries[0].Steps {
		switch step.Capture.Status {
		case "capacity_omitted":
			omitted++
			if len(step.Capture.PNG) != 0 {
				t.Fatal("omitted pixels retained")
			}
		case "captured":
			captured++
		}
	}
	if omitted == 0 || captured == 0 {
		t.Fatal("image budget not enforced independently")
	}
	html, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal("text report blocked by image budget", err)
	}
	if !strings.Contains(string(html), e.Evidence) || !strings.Contains(string(html), "용량 초과로 생략") {
		t.Fatal("text or omission reason missing")
	}
	if _, err := DiagnosticDOCX(in); err != nil {
		t.Fatal(err)
	}
	in.Shared = true
	shared, err := PrepareDiagnostic(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range shared.Entries[0].Steps {
		if step.Capture.Status != "shared_omitted" || len(step.Capture.PNG) != 0 || step.Capture.Digest != "" {
			t.Fatal("private capture survived shared projection")
		}
	}
	if bytes.Contains(html, []byte("private-trailing-metadata")) {
		t.Fatal("unexpected metadata")
	}
}

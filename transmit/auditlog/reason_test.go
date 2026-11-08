package auditlog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

// transmitReasons reads every typed Reason constant from the transmit package
// source. validEvent's allow-list is written by hand, so a reason added to the
// transmitter must fail here in CI instead of poisoning the recorder — and so
// blocking every transmission — at run time.
func transmitReasons(t *testing.T) []transmit.Reason {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var reasons []transmit.Reason
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "Reason" {
					continue
				}
				for _, v := range value.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("Reason constant in %s is not a string literal", name)
					}
					text, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					reasons = append(reasons, transmit.Reason(text))
				}
			}
		}
	}
	return reasons
}

func TestEveryTransmitReasonIsRecordable(t *testing.T) {
	reasons := transmitReasons(t)
	// Guard against a vacuous pass if the transmit source layout changes.
	found := false
	for _, r := range reasons {
		found = found || r == transmit.Completed
	}
	if !found || len(reasons) < 2 {
		t.Fatalf("parsed %d transmit reasons without Completed; did the source layout change?", len(reasons))
	}

	scope := transmit.Scope{RunRef: "run", TaskRef: "task", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}
	now := time.Now()
	for _, reason := range reasons {
		receipt := transmit.Receipt{Scope: scope, Attempt: 1, At: now, Method: "GET", Reason: reason}
		if reason == transmit.Completed {
			// A completed result is an admitted request whose response was received.
			receipt.AccountRef, receipt.SessionRef, receipt.ResourceRef, receipt.ApprovalRef = "acct", "sess", "res", "appr"
			receipt.Admitted, receipt.AdmittedAt = true, now
			receipt.RequestWritten, receipt.ResponseReceived, receipt.StatusCode = true, true, 200
		}
		if !validEvent(transmit.Event{Stage: "result", Receipt: receipt}, scope) {
			t.Errorf("transmit reason %q is missing from the audit allow-list (validEvent)", reason)
		}
	}
}

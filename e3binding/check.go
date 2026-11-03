package e3binding

import (
	"strings"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
)

const maxRefBytes = 128

// Check compares an independently selected target with host-owned records. All
// syntax is checked before associations, with the documented fixed precedence.
// A consistent decision proves neither provenance nor authority to save a result.
func Check(target Target, input Input) Decision {
	if !validRef(target.TaskRef) || target.FindingID <= 0 || target.RetestID <= 0 || target.OriginalEvidenceRevision == 0 {
		return refused(InvalidTarget)
	}
	finding, retest, execution := input.Finding, input.Retest, input.Execution
	if finding.ID <= 0 || finding.OriginalEvidenceRevision == 0 ||
		!validRef(finding.TaskRef) || !validRef(finding.ResourceRef) || !validRef(finding.SourceRunRef) {
		return refused(InvalidFinding)
	}
	if retest.ID <= 0 || retest.FindingID <= 0 || retest.OriginalEvidenceRevision == 0 || !validContext(retest.Expected) {
		return refused(InvalidRetest)
	}
	if execution.RetestID <= 0 || execution.FindingID <= 0 || execution.OriginalEvidenceRevision == 0 || !validContext(execution.Context) {
		return refused(InvalidExecution)
	}
	if finding.ID != target.FindingID || finding.TaskRef != target.TaskRef || finding.OriginalEvidenceRevision != target.OriginalEvidenceRevision {
		return refused(FindingMismatch)
	}
	if retest.ID != target.RetestID || retest.FindingID != target.FindingID || retest.OriginalEvidenceRevision != target.OriginalEvidenceRevision ||
		retest.Expected.Scope.TaskRef != target.TaskRef || retest.Expected.ResourceRef != finding.ResourceRef {
		return refused(RetestMismatch)
	}
	if finding.SourceRunRef == retest.Expected.Scope.RunRef {
		return refused(SourceExecutionReused)
	}
	if execution.RetestID != target.RetestID || execution.FindingID != target.FindingID || execution.OriginalEvidenceRevision != target.OriginalEvidenceRevision {
		return refused(ExecutionMismatch)
	}
	if execution.Context != retest.Expected {
		return refused(ContextMismatch)
	}
	return Decision{consistent: true, reason: Consistent}
}

func validContext(ctx evidencebind.Context) bool {
	// Match the existing Context input grammar; receipt/fact validation remains
	// evidencebind.Check's responsibility and is not duplicated in this seam.
	for _, ref := range []string{ctx.Scope.RunRef, ctx.Scope.TaskRef, ctx.Scope.CaseRef, ctx.Scope.ResetRef, ctx.Scope.PolicyRef,
		ctx.ResourceRef, ctx.ReadApprovalRef, ctx.KGPolicyVersion, ctx.Owner.Label, ctx.Owner.Handle, ctx.Evaluated.Label, ctx.Evaluated.Handle} {
		if !validRef(ref) {
			return false
		}
	}
	if ctx.IntentID <= 0 || ctx.Owner.Label == ctx.Evaluated.Label || ctx.Owner.Handle == ctx.Evaluated.Handle {
		return false
	}
	for _, status := range []oracle.SubjectStatus{ctx.Owner.Status, ctx.Evaluated.Status} {
		switch status {
		case oracle.SubjectVerified, oracle.SubjectUnknown, oracle.SubjectExpired, oracle.SubjectMismatch:
		default:
			return false
		}
	}
	return true
}

func validRef(ref string) bool {
	if len(ref) == 0 || len(ref) > maxRefBytes || strings.Contains(ref, "://") {
		return false
	}
	for index := range len(ref) {
		c := ref[index]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if index > 0 && (c == '_' || c == '.' || c == ':' || c == '/' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func refused(reason Reason) Decision { return Decision{reason: reason} }

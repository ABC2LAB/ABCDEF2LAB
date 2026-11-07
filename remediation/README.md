# Inert remediation proposal builder

This package is a deterministic, local renderer. It does not read source code,
query documents, call a model, send requests, apply patches, or deploy.

## Caller boundary

Call Build only after resolving the selected task's finding/candidate/verdict
and the **current** evidence and report snapshot under authorization. Status is
the evidence-derived outcome, never the finding's workflow/triage status.
A candidate cannot be passed as confirmed. The builder does not independently
verify the caller's evidence or provenance assertions.

ReportVersion is sha256:<actual saved report or source digest>, not a reused
evidence revision. SourceDigest must hash all relevant input facts, including
raw evidence, report text, remediation changes, observation metadata, and
technology provenance. EvidenceDigest binds the same selected evidence revision.
Do not mix versions or entries. Evidence revision zero represents an existing unversioned legacy record and
is allowed only for nonconfirmed outcomes. It is explicitly labeled as lacking
a structured evidence revision and still bound to source/report digests. Never
invent revision one or treat zero as confirmed, even with a positive count.

Technologies must come from authorized structured inputs. Source and Confidence
are enums, not free text, and are not proof of trust. In particular, model text
cannot claim user_provided or confirmed: the caller must determine those labels.
Do not infer frameworks from languages. Recognized names remain marked with
their supplied confidence; only observed/confirmed Java/Python from observed or
user-provided sources select conditional language examples. Unknown versions
remain unknown. No external documentation references are generated.

Only safe categorical metadata and digests enter the copyable prompt. Raw
evidence, URLs, actual file paths, snippets, response text, names, and secrets
are deliberately not accepted. The summary states the evidence count, outcome,
type, and missing protected details. This is **not a complete reproduction
transcript**. The report must render the actual evidence and reproduction
details separately under its existing disclosure policy; an external coding
AI may need an authorized nonsensitive copy of those details.

## Persistence and presentation

Persist the complete Bundle as one immutable artifact tied to the source
snapshot. Use IsStale against current Input before displaying or copying it.
It compares the complete deterministic bundle, including actions and prompt,
so modified saved content also fails closed. Updating GeneratorVersion
invalidates old proposals. When stale, suppress copy or regenerate and persist
a new artifact; never silently present stale text as current.

All detail, aggregate, copy and export routes should consume the same saved
bundle. Render strings with ordinary escaped text, never trusted HTML. Apply
the existing shared-export policy; this package does not authorize sharing.
Store/render order: evidence, actions, AI prompt.

Build rejects invalid identifiers/digests/counts/provenance kinds. It replaces
unknown categorical values with unknown rather than echoing arbitrary input.
No regex is claimed to discover every possible secret. Safety comes from
excluding free text, not trying to recognize all malicious instructions.

## Verification

go test -race ./remediation
go vet ./remediation

Synthetic tests cover language-specific conditional guidance, missing
frameworks, mismatch instructions, configuration/code families, nonconfirmed
outcomes, hostile fields, changed versions and modified stored prompts.
These are builder tests, not database/UI/export integration or actual target
remediation tests.

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
Do not infer frameworks from languages. Supplied names remain marked with
their supplied confidence and component role. Unknown versions
remain unknown. No external documentation references are generated.

Context adds a bounded, explicitly nonsensitive summary of the affected
component, endpoint route template, prerequisites, observation, expected normal
behavior and prohibited behavior. The caller supplies selected structured facts
or an explicitly user-reviewed summary, never raw Bash/model output, responses,
logs or customer records. NormalizeContext redacts credential-like fields,
URLs, controls and recognized instruction-like text by replacing the entire
field with an explicit unknown marker. Endpoint query/fragment/encoding and
authority syntax are rejected; actual customer identifiers must already be
replaced with route placeholders by the caller. Arbitrary secret detection is
not claimed. Caller credential/disclosure policy remains mandatory.

Context and technology metadata are JSON-quoted in a separate untrusted-data
section, never concatenated into the trusted work steps. Unknown details remain
explicit, not invented. Source=user_provided must be set by the authenticated
caller; it is not evidence of observation. Component and ComponentRole bind
technology assertions to the affected service; the prompt instructs the coding
AI to trace that actual service and never mistake frontend technology for its
backend. Source/evidence/report/technology/context changes invalidate a bundle.

Technology names are bounded labels, not a predefined language chooser. Elixir,
Phoenix and other actual supplied technologies are preserved with provenance.
No Java/Python-specific examples are selected. The coding AI must locate the
actual request path, verify implementation and versions, then apply a minimal
project-native fix within that session's permissions and report regression
results. No customer filenames, functions or architecture are invented.

This is **not a complete reproduction transcript**. Missing protected details
must be obtained through an authorized nonsensitive source; a missing relevant
code path or inconsistent evidence requires reporting the gap, not a guessed
patch.

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
No regex is claimed to discover every possible secret. Safety depends on excluding raw evidence, accepting only reviewed bounded
nonsensitive summaries as untrusted data, and enforcing the recipient
instruction boundary; regex filtering alone is not an injection defense.

## Verification

go test -race ./remediation
go vet ./remediation

Synthetic tests cover project-specific component context, open technology
labels, missing frameworks, mismatch instructions, configuration/code families, nonconfirmed
outcomes, hostile fields, changed versions and modified stored prompts.
These are builder tests, not database/UI/export integration or actual target
remediation tests.

# Inert remediation task prompt builder

This package is a deterministic, local renderer. It does not read source code,
query documents, call a model, send requests, apply patches, or deploy.

The **remediation-v5** prompt defaults to an actual customer-repository repair:
verify the repository, component/deployment relation and cause; apply the minimal
permitted code/configuration change; add and run normal/denial regressions;
review the diff, address review findings, rerun tests and report. A short plan is
progress communication, not a mandatory approval stop. The recipient's current
session permissions and explicit review-only/before-edit-approval restrictions
still govern. Missing cause/evidence, repository access, mutation permission or
deployment mapping pauses only the affected change. Independent safe authorized
checks can continue. Operational reproduction, operational DB changes, remote
push, PRs and deployment require their own session authorization. The report
neither grants those permissions nor changes product E②/approval/audit controls.
Report generation/copy does not execute the customer project.

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
their supplied confidence and component role. EvidenceRef is a bounded internal
reference, not a URL or proof; ObservedAt accepts RFC3339 timestamps normalized
to UTC. Conflicting rows survive instead of selecting the latest/user value.
Semver prerelease/build labels and unfamiliar framework names such as .NET and
@nestjs/core remain data. Unknown versions remain unknown. No external
documentation references are generated.

Context adds a bounded, explicitly nonsensitive summary of the affected
component, endpoint route template, prerequisites, observation, expected normal
behavior and prohibited behavior. The caller supplies selected structured facts
or an explicitly user-reviewed summary, never raw Bash/model output, responses,
logs or customer records. NormalizeContext redacts credential-like fields,
URLs, controls and recognized instruction-like text by replacing the entire
field with an explicit unknown marker. Security terminology alone is not an
instruction: descriptions of ignored validation, disabled authorization, eval,
exfiltration, jailbreak or prompt disclosure are preserved. Recognized commands
and instruction pivots remain quarantined. This is defense in depth, not a proof
that every dangerous string is recognized. Endpoint query/fragment/encoding and
authority syntax are rejected; actual customer identifiers must already be
replaced with route placeholders by the caller. Arbitrary secret detection is
not claimed. Caller credential/disclosure policy remains mandatory.

Context and technology metadata are JSON-quoted in a separate untrusted-data
section, never concatenated into the trusted work steps. Unknown details remain
explicit, not invented. Source=user_provided must be set by the authenticated
caller; it is not evidence of observation. Context.Observations retains the
original per-step Component/ComponentRole, Endpoint, Prerequisites, Observation,
ExpectedBehavior and ProhibitedBehavior with its own Source, EvidenceRef,
ObservedAt, RunID, CallID, StepID, DependsOn and Truncated marker. Top-level
context fields may contain a user supplement but must not replace those original
rows. The caller must reject user-supplied claims of observed provenance and
bind every row to the selected task/finding/run/call before Build; the renderer
cannot authenticate IDs. Keep explicit step order and dependencies, never join
runs by similar time/URL. Empty DependsOn means unknown dependency, not an
invented link. At most 32 observations and 16 dependencies per row are rendered;
any truncation is explicitly marked. ObservationsTruncated also indicates that
some automatic technology rows were omitted at the collection limit, or that
source output was truncated; it is not a claim of a complete observation/stack
inventory. Normalization deep-copies these slices. Build accepts at most 64
combined technology rows so that up to 32 automatic rows and 32 user supplement
rows can coexist without overwriting either source. The HTTP user-input limit
remains 32; increasing the aggregate renderer limit does not expand that API
limit.
MissingContextFields names absent component, endpoint, prerequisites,
observation, expected behavior and prohibited behavior. Providing a value is
not evidence of truth or sufficient cause, and a sparse result is explicitly a
context-incomplete work request rather than a completed customized diagnosis. Component and ComponentRole bind
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

Unsupported vulnerability classes retain their safe original VulnClass label
and context. Only remedy selection falls back to generic cause verification;
a missing specialist template does not mean the reported type is unknown.
Actual validator support is separate from report/prompt coverage.

## Persistence and presentation

Persist the complete Bundle as one immutable artifact tied to the source
snapshot. Use IsStale against current Input before displaying or copying it.
It compares the complete deterministic bundle, including actions and prompt,
so modified saved content also fails closed. Updating GeneratorVersion
invalidates old proposals. Schema 4 and remediation-v5 provide English work instructions and require the recipient to report in Korean. Korean UI/remediation advice is unchanged. Earlier bundles become stale and require explicit regeneration; they are never silently translated. Diagnostic context/technology JSON remains untrusted data and may preserve the caller's original language. Input,
technology and complete-bundle digests include the new context/provenance fields;
JSON encoding errors fail generation rather than hashing/displaying empty data.
When stale, suppress copy or regenerate and persist
a new artifact; never silently present stale text as current.

All detail, aggregate, copy and export routes should consume the same saved
bundle. Render strings with ordinary escaped text, never trusted HTML. Apply
the existing shared-export policy; this package does not authorize sharing.
Store/render order: evidence, actions, AI prompt.

Build rejects invalid identifiers/digests/counts/provenance kinds. It replaces
invalid categorical provenance/role values with unknown; unfamiliar safe
vulnerability and technology labels are preserved as untrusted metadata.
No regex is claimed to discover every possible secret. Safety depends on excluding raw evidence, accepting only reviewed bounded
nonsensitive summaries as untrusted data, and enforcing the recipient
instruction boundary; regex filtering alone is not an injection defense.

## Verification

go test -race ./remediation
go vet ./remediation
go test -run TestExecutionPromptScenarios -v ./remediation

Synthetic tests cover project-specific component context, open technology
labels, missing frameworks, mismatch instructions, configuration/code families, nonconfirmed
outcomes, hostile fields, changed versions and modified stored prompts.
The four complete verbose outputs cover normal repair, unconfirmed candidate,
missing permission and deployment mismatch for semantic review. Additional tests
cover observed/inferred/user disagreement without overwrite, preserved step
order/dependencies, truncation, timestamp/reference normalization, new fields in
staleness, unfamiliar classes/technology labels, benign security descriptions,
missing context and explicit JSON encoding failures.
These are builder tests, not database/UI/export integration or actual target
remediation tests.

`asset-*` 근거의 inferred 기술 `observed_at`은 자산 last_seen이며, 누적 기술 항목 각각의 관측 시각·현재성 증거가 아니다. 저장소의 실제 구현과 재대조해야 한다.

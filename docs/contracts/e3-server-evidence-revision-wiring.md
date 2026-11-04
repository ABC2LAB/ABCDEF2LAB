# E③ server SQL revision connection

Status: development integration; real host/source and deployment are not enabled.

## Exact dependency basis

- #158 `e6af2335fbd0412fccf03df589f95d754c26c41b` (includes #156 `684451300875251325eb507db30c4d3b45ec4dbc` and #157 `9c079d92e5728411774005cf4760124a55068bb4`).
- Normal merge of #151 `edbfc8015921dd0a8bbf71ee6a6b741e84f35c11`, including #142 `a086b338496a3c2e306856ba294239a8815e408d`, #143 `1dfc4a0fbf9f47db07eacf8df420fcbd9982e9ad` and #149 `8cc0d5588fb6e8b6313434006224cccd8968bab2`.
- Integrate their dependencies first, both #158 and #151 before this connection. No prerequisite head is modified. Inherited cumulative changes are separate from this unit's server glue/tests/document.

## Enabled API and order

`NewWithRevisionedDurableControlledReadHost` accepts trusted Go assembly only: the existing durable options plus mandatory actual `evidencerevision.ScopeProvider` and `DescriptorRootProvider`. The server constructs `evidencerevision.New` on its own manager's PostgreSQL DB and `NewDescriptorFiles`; it accepts neither an external issuer callback nor an arbitrary Store. Configuration completes in the existing bootstrap before restored/background admission. Existing main, local-only and explicitly durable constructors retain their existing behavior.

The caller must separately apply the revision migration before use. No implicit DDL, root directory creation, key/epoch/identity/approval provisioning or fallback is added. Nil/typed-nil providers fail configuration; failed host callbacks fail the operation.

1. The private collector receives the real owner/cross `SendDetailed` observations once. `Capture` retains only the validated opaque metadata from that exact `c.input` and canary-field names. Model JSON cannot provide this collector state.
2. An operation ref is fixed from the full already-authorized scope and intent. It identifies metadata for this run; it never creates or changes a RunRef or restarts execution.
3. Existing E② drain, durable Seal ACK, protected sidecar and successful completion claim precede the finalizer. No Source A is cached during collection or before the claim.
4. Fresh Source A supplies the actual exploration and mode-version facts used by `Describe`. Those facts are checked by the real SQL producer; the source is not a replacement for actual caller/marker/root authorization.
5. `DescriptorFiles.Put` must ACK protected durable metadata before `Issue` starts. Missing storage, denied roots or uncertain file outcomes prevent issuance. The descriptor is data, not authority.
6. SQL `Issue` uses the mandatory actual host scope. Its receipt must match operation, full scope, task/intent/exploration/mode, both request/binding and evidence digests, and a positive bounded revision. A nonzero Source A revision must equal that value.
7. Fresh Source B independently re-reads audit/key/KG/identity/fixture truth after all descriptor/issuance I/O. Its context and exploration/mode must match the actual receipt. A nonzero B revision must also match; otherwise the engine attaches the actual SQL value. Between B and Prepare's reader return there is no additional external I/O. Existing Prepare validation and final DB marker/mode gates remain active.

Source guarantees are for the read instant. Comparing A/B facts does not create a lease, authenticate accounts or extend authority through finding commit. The actual host must maintain its existing source/read lifetime and restore-continuity obligations. HTTP drain proof still covers only that transmitter; no proof of legacy children or whole-task quiescence is fabricated.

## Outcomes and recovery

Revision issuance is observation metadata, not a finding or completed run. Any revision error, conflict or unknown commit yields no finding/done. The collector retains the first revision-stage failure and exact operation; it does not retry with a new operation or RunRef. The fixed server diagnostic `revision_failed reason=… operation_ref=…` distinguishes revision uncertainty from the existing finding-storage outcome. Raw SQL, source contents, paths and credential errors are not emitted.

An actual SQL commit can succeed while its ACK is lost. Its protected descriptor and SQL row may therefore survive a failed engine run. An operator with fresh actual file-reader and ReadReceipt authority can explicitly `Load` and `Reconcile` the same operation. This does not resume execution, create a new revision or grant current storage permission. Operator CLI/service orchestration is outside this unit. Descriptor loss has no promised reconstruction path from raw observations; raw policy/headers/body are not persisted here.

External Source/provider/file callbacks and descriptor/hash work run outside collector/runtime/work/coordinator SQL locks. The collector marks finalization in progress, releases its mutex, and then performs callbacks/I/O. Existing SQL issuance takes only its own short DB transaction locks; final finding storage retains its independent mode checks. Callbacks must obey the existing no-reentry/drain rules of their host.

## Verification and limits

Tests use the owned PostgreSQL database, protected temporary directories/anchors, real loopback GETs and a fake finite LLM with explicit synthetic host/KG/account metadata. They cover actual sequence values above one, binding persistence, descriptor-before-Issue refusal, current source/receipt mismatch, key/epoch changes while issuance runs, scope cancellation, pre-claim cancellation and late pause. A test-only SQL driver commits the real revision transaction and then loses only its ACK; explicit descriptor Load/Reconcile must recover its historical metadata with finding count zero. Missing schema remains a failure; no automatic migration or alternate issuer exists.

These are synthetic non-eval runs (`EvalRunID` empty). They do not verify real Juice Shop, real approval/account/KG provenance, real OS identities/authentication, production root/epoch/key providers or operational deployment. Those actual inputs and team decisions remain required.

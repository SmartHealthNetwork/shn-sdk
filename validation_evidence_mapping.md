# Validator adapter mapping, version 1

This table pins the SDK response interpretation, tested in the focused
`validation_*_test.go` suites. It does not assert that an endpoint has loaded
any particular package or terminology. Profile requests use the existing explicit
profile parameter/context. Gateway lane qualification remains a separate gate.

| Wire evidence | Safe issue code | Profile evidence |
| --- | --- | --- |
| R4 `issue.code`: `invalid`, `structure`, `required`, `value`, `invariant`, `too-long`, `duplicate`, `business-rule`, `code-invalid`, `informational` | Exact listed code | Completed; error/fatal invalid, warning/information valid |
| `not-supported`, `not-found`, `incomplete` | Exact listed code | Unavailable; category does not identify a supported completed profile check |
| `processing`, `exception`, `timeout`, `throttled`, `security`, `login`, `forbidden`, `expired`, `too-costly`, `transient`, `lock-error`, `no-store`, `conflict` | Exact listed code | Unavailable with execution error |
| Unknown code or missing CLI type | `unmapped` | Unavailable; never copy an arbitrary code |
| CLI wrapper `issues[].type` | Same mapping, case-insensitive, plus enum spellings `NOTSUPPORTED`, `NOTFOUND`, `BUSINESSRULE`, `CODEINVALID` | Same interpretation |
| CLI wrapper explicitly empty `issues: []` | No issues | Completed profile execution |
| Unknown/missing severity; malformed, missing, null or wrong-shape issue collection | No content verdict | Unavailable with execution error |

The pinned lane corpus also provides these exact Java-core message IDs. For an
OperationOutcome, exactly one `details.coding` entry must have system
`http://hl7.org/fhir/java-core-messageId`; the wrapper exposes the same ID as
`messageId`. These refine only the `processing` category:

| Exact message ID | Safe code | Evidence |
| --- | --- | --- |
| `Extension_EXT_Type` | `extension-type` | Content invalid, only with error/fatal severity |
| `Reference_REF_BadTargetType` | `reference-target-type` | Content invalid, only with error/fatal severity |
| `Validation_VAL_Profile_Unknown` | `profile-unsupported` | Profile unavailable |
| `SLICING_CANNOT_BE_EVALUATED` | `slicing-unavailable` | Profile unavailable with execution error |
| Unknown ID, wrong coding system, multiple/conflicting IDs or conflicting category/severity | `unmapped` | Profile unavailable |

These four identities are pinned by the existing lane qualification corpus and
explicit-profile backport. They do not prove terminology coverage. Unrecognized
structured detail codes never fall back to treating generic processing as content.

The following additional identities are qualified **only for OperationOutcome**,
with exactly one `details.coding` entry in the same Java-core system:

| Exact message ID | Safe code | Evidence |
| --- | --- | --- |
| `http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6` | `narrative-advisory` | Completed profile execution, only with `warning` severity and `processing` category; preserve the warning |
| `Terminology_TX_NoValid_2_CC` | `extensible-binding-advisory` | Completed extensible CodeableConcept binding execution, only with `warning` severity and `processing` category; preserve the warning |
| `Terminology_TX_Code_ValueSet_Ext` with exactly `expression: ["Goal.description"]` | `goal-description-text-advisory` | Completed extensible binding check on a text-only Goal description, only with `warning` severity and `processing` category; preserve the warning. It does not establish clinical suitability or terminology coverage. |
| `All_observations_should_have_a_performer` | `observation-performer-advisory` | Completed profile execution, only with `warning` severity and `processing` category; preserve the warning |
| `All_observations_should_have_an_effectiveDateTime_or_an_effectivePeriod` | `observation-effective-time-advisory` | Completed profile execution, only with `warning` severity and `processing` category; preserve the warning |

The CLI wrapper counterpart remains `unmapped` / unavailable. Other canonical IDs,
categories, severities, coding systems and duplicate or conflicting codings remain
unavailable. The response's message-ID extension is ignored; neither it nor
English diagnostics qualifies a response.

The Goal row is grounded in a real HTTP 200 explicit-profile check of the
synthetic `providerdata-uc04-goal-goal-mbrpduc04-pt.json` source against US Core
Goal 7.0.0. Exact request bytes are included in the SDK at
`testdata/validation/hapi-goal-text-request.json` so the captured check runs
standalone. The request SHA256 is
`cf692c003ad6bc3f103a01170fce1d4ac9e167ea28c069a9b3a4f1fa9c573e3e`;
the complete response in `testdata/validation/hapi-goal-text-warning.json`
is 1,927 bytes, SHA256
`e38b11451d2ebdc0a69ed658bfbb4c3d313af7b569b7b26201a1bb3487c1517d`.
Removing only `Goal.description` produced the real minimum-cardinality error
in `hapi-goal-minus-description.json` (SHA256
`290abe944e7fdcde34e3a771b8bb17c95bb6795f2256219b8bf1e2c39b4b8db6`);
the changed request bytes are retained beside it. The adapter keeps that
malformed neighbor unavailable and therefore refused; it does not recast an
unrecognized HAPI processing error as content invalid. Outcome edits to the
message ID, coding system, severity, category and expression are synthetic
negative controls and remain unsupported. The expression is used only inside
classification, never exposed in public `ValidationIssue` output.

Mixed outcomes retain the usual rules:
an unknown profile makes support unavailable, a mapped content error makes content
invalid, and an execution failure makes execution unavailable. Terminology remains
unavailable in every case.

The byte-preserved synthetic fixture
`testdata/validation/hapi-extensible-binding-warning.json` is the complete
2,105-byte HTTP 200 response to the first provider Patient validation in the
pinned lane (SHA256
`d04aee8348346db38a0fe28216a22970dff084886e6ab9778aba34b95be4b642`).
It contains exactly the qualified extensible-binding advisory and the separately
qualified `dom-6` advisory. The unchanged 376-byte request (SHA256
`8d7990f3f1ad858ae26489df7ff0da11cbdb115c15d175ff75ba3f37bdf322a0`)
asserted the known `urn:shn:member` identifier with the pinned v2-0203 `MB`
coding. This response interpretation does not replace that participant source
assertion or establish that another IdentifierType code is suitable.

The exact Linux/ARM64 HAPI v8.10.0-1 WAR identified below carries HL7 Java core
6.9.4.1. Its `WEB-INF/lib/org.hl7.fhir.validation-6.9.4.1.jar` has SHA256
`8919955c661f4d79ca15616039b5ebad885365f3aea01d38556671256837a79f`;
the runtime `InstanceValidator.class` has SHA256
`e980fa6a72863cb4d9c03d39017395fb1031e66f69f77dcb44d2a916c02feff9`.
The immutable upstream 6.9.4.1 source commit is
[`7519d87dbb261bdd829203ad75c7bb4ed4ed4416`](https://github.com/hapifhir/org.hl7.fhir.core/commit/7519d87dbb261bdd829203ad75c7bb4ed4ed4416);
its [InstanceValidator source](https://github.com/hapifhir/org.hl7.fhir.core/blob/7519d87dbb261bdd829203ad75c7bb4ed4ed4416/org.hl7.fhir.validation/src/main/java/org/hl7/fhir/validation/instance/InstanceValidator.java#L1738)
has SHA256
`d139d6945a66a0b1f3f31e67bd63761497e59dfedf2cbff88e9ba2d7938b35d3`.
That branch distinguishes required binding, no-service, infrastructure,
unsupported-code-system and server-error paths before emitting
`Terminology_TX_NoValid_2_CC` for a negative extensible CodeableConcept binding.
The exact ID alone does not prove clinical suitability, complete ValueSet or code
system coverage, or successful terminology service execution. Those limitations
are why terminology remains `terminology-support-unproven` / `unavailable`.

The byte-preserved synthetic fixture `testdata/validation/hapi-dom6-warning.json`
(SHA256 `f1d5187bafe37f28125f7b3248cf84bfadfb6d14608de9da57cbe5f12d98385e`)
is a complete 1029-byte HTTP 200 response to a US Core Patient warm-up request,
with no explicit profile query. Its request declares the US Core Patient profile
in `meta.profile`; the request SHA256 is
`b6bc12b2062e17eb195413dba9c61ac20b2d80dbbd2dc1c97d98764a8375cc69`.
This captures a single execution, not certification of every loaded profile.

The byte-preserved synthetic fixture
`testdata/validation/hapi-observation-first-response.json` is the complete
4,452-byte HTTP 200 OperationOutcome for the first provider Observation in the
same pinned runtime (SHA256
`1f5d8adbc2f080168aef828a75163c537e2c20ded52e48fea4cea24d28d6b6f2`).
The unchanged 592-byte request has SHA256
`beb701ffc7308bceb9ae8c2a5131ba91cd56d5f261e2d6091a0edc3e622b36f7`.
The response includes an unqualified unknown-local-CodeSystem message, the
extensible-binding advisory, `dom-6`, and the exact performer advisory. The
complete response therefore remains profile unavailable, and the source
Observation was not accepted or stored. Isolated performer and mixed-error
test cases derived from this fixture are synthetic controls, not later server
replies.

The qualified performer message comes from the pinned HL7 Java core 6.9.4.1
[`ObservationValidator.java` at commit `7519d87dbb261bdd829203ad75c7bb4ed4ed4416`](https://github.com/hapifhir/org.hl7.fhir.core/blob/7519d87dbb261bdd829203ad75c7bb4ed4ed4416/org.hl7.fhir.validation/src/main/java/org/hl7/fhir/validation/instance/type/ObservationValidator.java#L20)
(SHA256 `f4ea619d7358650eb40cd519a9fd5bd3da84cb10b7a637a597a4efd0868a0985`),
corroborated by the actual WAR's `ObservationValidator.class` (SHA256
`1db567d3b3832f27f61df804079d451b8aedd68ced7af3684085fdecfcaa304c`).
It checks whether an Observation has any performer. The same engine's best-practice
helper can emit an error, warning, hint or no advice, so only the observed
`warning`/`processing` wire shape is qualified here. The R4 Observation and US
Core 6.1.0 clinical-result definitions both give performer cardinality `0..*`:
the party responsible for asserting the observed value. This presence advice
establishes neither a valid or truthful performer nor permission to invent one.
Suitable LOINC selection and the participant's clinical source remain separate
qualifications.

The byte-preserved `testdata/validation/hapi-observation-effective-request.json`
is the complete 480-byte request (SHA256
`6441e7ee60146f5673437165d2d29bd066c2e42fdef33a3dfd433aa4f6deb0e4`)
from a local, instrumented POST to
`/fhir/provider/Observation/$validate` with the US Core 6.1.0 clinical-result
profile. Its complete HTTP 200 OperationOutcome is
`testdata/validation/hapi-observation-effective-response.json`, 3,837 bytes
(SHA256 `e2a2e08203b03e219554b2e0f4c04e5ca4784b8cef4aad7e76d42f3b3ee79832`).
These are exact captured entity bytes, including all four issues in order:
extensible binding, narrative, performer and effective-time advice. All are
`warning`/`processing` with one Java-core details coding. The instrumented
transport preserved request and response entities while changing HTTP framing;
the original reply was gzip encoded and the fixture is its exact decoded JSON
entity. The request has no effective[x] or performer. At capture time the SDK
refused this Observation before PUT because the effective-time ID was unmapped;
the post-mapping completed profile result is a tested interpretation of that
same retained response, not a successful full source seed or clinical approval.
The isolated and modified test outcomes are explicitly synthetic controls.

The exact effective-time ID is supported by the pinned HL7 Java core 6.9.4.1
[`ObservationValidator.java` at immutable commit `7519d87d`](https://github.com/hapifhir/org.hl7.fhir.core/blob/7519d87dbb261bdd829203ad75c7bb4ed4ed4416/org.hl7.fhir.validation/src/main/java/org/hl7/fhir/validation/instance/type/ObservationValidator.java#L23)
(source SHA256 `f4ea619d7358650eb40cd519a9fd5bd3da84cb10b7a637a597a4efd0868a0985`,
runtime class SHA256 `1db567d3b3832f27f61df804079d451b8aedd68ced7af3684085fdecfcaa304c`).
That Observation-specific class has exactly three advice branches: missing
subject, performer and effective time. Only the latter two exact IDs have
qualified warning shapes here; the subject neighbor remains unqualified.
Despite the message ID's wording, the effective-time presence predicate accepts
**any** of `effectiveDateTime`, `effectivePeriod`, `effectiveTiming` or
`effectiveInstant`. The shared
[`bpCheck` helper](https://github.com/hapifhir/org.hl7.fhir.core/blob/7519d87dbb261bdd829203ad75c7bb4ed4ed4416/org.hl7.fhir.validation/src/main/java/org/hl7/fhir/validation/BaseValidator.java#L1717)
can emit stronger or weaker severities, so only the observed wire `warning` is
qualified. A warning of this kind establishes completion of that best-practice
check only; it does not prove the source lacked a relevant time or that supplied
time is valid.

R4 Observation defines effective[x] as 0..1 with those four types. Both
[US Core 6.1.0 clinical-result](https://hl7.org/fhir/us/core/STU6.1/StructureDefinition-us-core-observation-clinical-result.html)
and [screening-assessment](https://hl7.org/fhir/us/core/STU6.1/StructureDefinition-us-core-observation-screening-assessment.html)
keep minimum 0 while marking effective[x] Must Support; their `us-core-1`
constraint requires at least day precision **when a dateTime is supplied**.
These distinctions do not waive independent profile constraints, source/support
obligations or clinical review. A participant must preserve a genuine relevant
source time when available, never invent a date or performer to silence advice,
and independently review code suitability (including suitable LOINC) and other
structural findings. Terminology evidence remains unavailable.

Qualification independently inspected the exact Linux/ARM64 HAPI v8.10.0-1 runtime
WAR (SHA256 `12068828f38ca5c0c05c6ffa1b8c7c0a827c1cef0be6f6dae391f789e930316f`),
based on `hapiproject/hapi@sha256:1be4d7ffe7a35a9fb46151851e5a20b25c5016f16c8ef8b59b0c807ad06a40c1`
with the explicit-profile backport. Within it,
`WEB-INF/lib/hapi-fhir-validation-resources-r4-8.10.0.jar`
(SHA256 `4834b1e98b3c4648b5b6fd5675bc999cb82028d8c3762e9390d28bd324a58280`)
contains `org/hl7/fhir/r4/model/profile/profiles-resources.xml`
(SHA256 `f3005bd427a60a3552be16d59e66b4546d0e9d58decd97b87f5943fbef94a1b7`).
Its DomainResource definition identifies `dom-6` as `severity=warning`,
`elementdefinition-bestpractice=true`, expression ``text.`div`.exists()``.
The independently installed US Core 6.1.0
`package/StructureDefinition-us-core-patient.json`
(SHA256 `fbbe357a47e988dfa348e75fc70103a3bb8090ac58ae88122ae630b1d902d8e5`)
inherits the same constraint and names DomainResource as its source. These pinned
definitions corroborate advisory narrative content independently of diagnostic prose;
they do not grant terminology support or qualify a different validator adapter.

R4 OperationOutcome must have at least one issue, with a recognized lowercase
severity and a nonempty code. The wrapper requires at least one outcome with an
explicit issues array. Wrapper `level`/`message` and legacy `severity`/`details`
share the decoder. The legacy wrapper shape still needs a recognized `type` to
establish support. Additional unrelated fields are ignored. Diagnostic prose is
never interpreted as a support signal; it is retained only in the legacy
error/fatal compatibility view.

HTTP 2xx permits the classifications above. HTTP **400 and 422 only** may carry
an invalid content verdict, and only when every issue maps to a content category
and at least one has error/fatal severity. Warning-only, unsupported and execution
outcomes at either status are unavailable with an error. All other non-2xx statuses,
including parseable OperationOutcome replies at 401, 429 and 500, are execution
errors. Complete bounded reading and JSON decoding are required; truncated,
oversized or malformed responses never become verdicts.

**Terminology support gap:** neither configured real adapter has a documented
response field proving execution over all requested code systems. Consequently
both always return `terminology-support-unproven` / `unavailable`, including
warning-free responses, code-invalid content failures and explicit empty wrapper
issues. Neither profile validity nor diagnostic prose proves terminology coverage.
A real terminology checker with pinned coverage evidence is required before a
strict terminology rule can pass. A generic unsupported diagnostic is ambiguous
about scope, so profile evidence is unavailable too. No adapter emits
`not_applicable`; applicability must be proved by the consuming rule.

`Result` remains source-compatible and intentionally lossy: error/fatal diagnostics
only; profile execution/support unavailable returns an error. `ValidationResponseError`
provides private bounded response bytes via a defensive copy while its string
contains only status, byte count and hash. Never forward `RawResponse()` to observer
events, public status, logs or participant errors. Synthetic fakes require explicit
`Evidence` configuration and do not demonstrate real adapter support.

`ExecutionAttempted` is independent provenance: it is false for unconfigured
implementations, local preprocessing failures and cancellation before dispatch.
The HTTP adapters set it when attempting the checker request, preserving it for
transport errors and bounded decoder failures. A local checker can also set it
when it actually executes. Wrappers must preserve the bit, including on errors.
An attempt does not prove a verdict, support or terminology coverage; the states
above still govern that interpretation. Older/custom evidence implementations
that leave it false keep their verdict contract but cannot establish recent
checker availability. Gateway availability records only proven attempts within
its finite observation window, never a liveness guarantee.

Public consumers must release the SDK addition before releasing gateway code
that uses it; then release the Kit. Workspace builds do not prove published pins.

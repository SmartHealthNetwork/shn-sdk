# Smart Gateway Da Vinci IG Compatibility Matrix

<!-- GENERATED FILE — do not edit by hand. Regenerated from the gateway's declared-expectations grid with each SDK release. -->

This matrix states, element by element, what the Smart Gateway does to a payload when it
adapts between adjacent Da Vinci IG versions. Every row is backed by a test obligation that
runs the real transform chain.

Four honest qualifications about how strong that backing is:

- **The Carried rows are live-validated, on injected fixtures.** No pinned payload in the
  validation corpus carries these elements, because this gateway's own builders never produce
  them — that is a property of the corpus, not a gap in the checking. Each carry is exercised
  on every validation run against the real IG lanes: the carried output is validated at the
  target line, and the restored output at the source line, from fixtures derived by injecting
  a declared element into a pinned reference payload. One element is exercised on the restore lane only
  (`Claim.extension:transmissionIdentifiers`), because the downcast bundle it rides in carries
  unrelated content the 2.1 line rejects.
- **A manifest class can outrun its rows.** The per-contract `Manifest class` tables state
  what the engine declares a step is for. Where a declared `carry` step has no `Carried` row
  left, nothing travels in the wrapper for a conformant payload and no loss is reported; the
  class is marked ⚠ there and the tracked gap is named beneath the table. Read those as
  intent, not as current behavior.
- **The `pa.crd` rows are identity by construction.** The engine registers no transform for
  those steps, so their obligation confirms the payload is untouched rather than exercising
  translation logic, and CRD payloads are not part of the live validation corpus.
- **"Validated on a lane" varies in strength.** Payloads carrying a `meta.profile` are
  checked against that profile; payloads without one get structural FHIR R4 validation only.

**Topology.** Translation is an *egress* adaptation with tolerant ingress: a gateway adapts
what it sends to the line its peer speaks. An inbound request on a non-native line is not
silently translated — it answers with a legible 422 (FR-G52). So this
matrix documents outbound translation only.

**How to read the effects.**

| Effect | Meaning |
|---|---|
| Identity | Byte-identical pass-through, pinned by test. |
| Translated | The element is rewritten in place. |
| Carried | The element has no slot on the target line, so it travels inside a `shn-carried-content` extension and is restored on the way back. Restoration is value- and literal-identical: numeric literals are carried exactly as written (`0.50` returns as `0.50`). Restoration also depends on the intermediate system preserving an extension it does not recognise. A peer that filters to profile, or round-trips through a model that drops unknown extensions, loses the content silently — and the upcast then yields a payload missing the element with no loss report saying so. Preserving unrecognised extensions is therefore a **contract obligation on the peer** (`PARTICIPANT_PROTOCOL` §8.7, FR-G53); its verification — a round-trip probe against the peer — is scheduled for the first peer that makes a downcast reachable. |
| Synthesized | The target line requires an element the source cannot supply, so it is minted deterministically from the exchange identity. |
| ⚠ Refuses | There is no honest source for a required element, so the exchange refuses with a typed 422 rather than fabricating one. |
| ⚠ Known gap | A real, tracked gap. The test suite asserts the gap still exists, so it turns red the moment it closes. |

## pa.crd

**Manifest class** below describes each adjacent step as a whole — it is the engine's
worst case across both directions and both sub-cases, not a verdict on any one direction.
`full` = lossless mapping · `carry` = some content travels in `shn-carried-content` ·
`gated` = at least one direction or sub-case has no honest source and refuses.

| Adjacent step | Manifest class |
|---|---|
| 2.0 ↔ 2.1 | `full` |
| 2.1 ↔ 2.2 | `full` |

Per direction, and per sub-case where the contract distinguishes them:

| Step | Sub-case | Effects |
|---|---|---|
| 2.0 → 2.1 | — | Identity |
| 2.1 → 2.0 | — | Identity |
| 2.1 → 2.2 | — | Identity |
| 2.2 → 2.1 | — | Identity |

### pa.crd: 2.0 → 2.1

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | Identity by construction: the engine registers no transform for pa.crd, so the payload is passed through untouched. This is pinned on a CRD response fixture, and holds for the coverage-information sub-extensions SHN itself builds — it is not a survey of every CRD payload shape. |

### pa.crd: 2.1 → 2.0

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | Identity by construction: the engine registers no transform for pa.crd, so the payload is passed through untouched. This is pinned on a CRD response fixture, and holds for the coverage-information sub-extensions SHN itself builds — it is not a survey of every CRD payload shape. |

### pa.crd: 2.1 → 2.2

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | Identity by construction: the engine registers no transform for pa.crd, so the payload is passed through untouched. This is pinned on a CRD response fixture, and holds for the coverage-information sub-extensions SHN itself builds — it is not a survey of every CRD payload shape. |

### pa.crd: 2.2 → 2.1

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | Identity by construction: the engine registers no transform for pa.crd, so the payload is passed through untouched. This is pinned on a CRD response fixture, and holds for the coverage-information sub-extensions SHN itself builds — it is not a survey of every CRD payload shape. |

## pa.dtr

**Manifest class** below describes each adjacent step as a whole — it is the engine's
worst case across both directions and both sub-cases, not a verdict on any one direction.
`full` = lossless mapping · `carry` = some content travels in `shn-carried-content` ·
`gated` = at least one direction or sub-case has no honest source and refuses.

| Adjacent step | Manifest class |
|---|---|
| 2.0 ↔ 2.1 | `full` |
| 2.1 ↔ 2.2 | `carry` |

Per direction, and per sub-case where the contract distinguishes them:

| Step | Sub-case | Effects |
|---|---|---|
| 2.0 → 2.1 | — | Identity |
| 2.1 → 2.0 | — | Identity |
| 2.1 → 2.2 | — | ⚠ Refuses · Translated |
| 2.2 → 2.1 | — | ⚠ Refuses · Carried · Translated |

### pa.dtr: 2.0 → 2.1

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | Identity by construction across 2.0/2.1: the engine registers no transform for this step, pinned byte-identical on every payload in the frozen DTR corpus. Payload shapes outside that corpus are untested rather than guaranteed. |

### pa.dtr: 2.1 → 2.0

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | Identity by construction across 2.0/2.1: the engine registers no transform for this step, pinned byte-identical on every payload in the frozen DTR corpus. Payload shapes outside that corpus are untested rather than guaranteed. |

### pa.dtr: 2.1 → 2.2

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| `QuestionnaireResponse.extension:qr-coverage` | Translated | single Coverage-referencing qr-context entry and no qr-coverage entry already present (QR-content species) | The Coverage-referencing qr-context entry is relocated in place to 2.2's qr-coverage slice. |
| `QuestionnaireResponse.extension:qr-context` | Translated | single Coverage-referencing qr-context entry and no qr-coverage entry already present (QR-content species) | The relocated entry LEAVES 2.1's qr-context slice: the slice loses its Coverage-referencing member (the non-Coverage members stay put). |
| `QuestionnaireResponse.extension:intendedUse` | Translated | — | intendedUse coding.system remaps to the 2.2 CodeSystem (def-driven; foreign systems untouched). |
| `QuestionnaireResponse.item.answer.extension:information-origin.extension:source` | Translated | auto-sourced answers only; manual/override origin codes are line-invariant | The information-origin source code for auto-populated answers remaps to the 2.2 code. |
| `QuestionnaireResponse.extension:qr-coverage` | ⚠ Refuses | A 2.1 context with two or more Coverage references is not translated to 2.2: the exchange answers with a typed 422 rather than choosing one, and sends nothing. Send a single-coverage context, or submit on the 2.1 line. | A genuinely multi-coverage 2.1 context cannot honestly satisfy 2.2's single qr-coverage; the exchange refuses with a typed 422. |
| `QuestionnaireResponse.extension:qr-context` | Translated | a single Coverage-referencing qr-context entry equal, apart from its url, to an existing qr-coverage entry naming exactly the same Coverage reference | When the source already carries a qr-coverage entry for the same Coverage, the repeated Coverage qr-context entry is removed; the qr-coverage entry stays where it is and nothing else changes. |
| `QuestionnaireResponse.extension:qr-context` | ⚠ Refuses | A 2.1 context whose Coverage qr-context repeats the qr-coverage reference but differs from it in other content is not translated to 2.2: removing the repeat could lose content, so the exchange is refused with the error text and nothing is sent. Send the Coverage once, as a qr-context entry (2.1's slice for it), or submit on the 2.1 line. | A repeated Coverage entry is removed only when nothing would be lost. |
| `QuestionnaireResponse.extension:qr-coverage` | ⚠ Refuses | A 2.1 context whose qr-coverage and qr-context entries name different Coverages, or name one Coverage in different ways, is not translated to 2.2: the exchange is refused with the error text rather than choosing, and nothing is sent. Send a single Coverage, referenced the same way everywhere, or submit on the 2.1 line. | Two different Coverages are refused the same way whichever of the two extensions each arrives in; a Coverage in qr-context must be a relative Coverage/<id> reference. |
| `QuestionnaireResponse.extension:qr-coverage` | ⚠ Refuses | A 2.1 context that already names one Coverage in more than one qr-coverage entry is not translated to 2.2: the exchange is refused with the error text and nothing is sent. Send one qr-coverage entry per Coverage. | The translation keeps one qr-coverage entry per Coverage; a repeated qr-coverage entry is refused rather than passed on. |
| `QuestionnaireResponse.extension:qr-coverage` | ⚠ Refuses | A 2.1 context with no Coverage reference is not translated to 2.2: the exchange answers with a typed 422 and sends nothing. Add the Coverage reference at the source as a qr-context entry (2.1's slice for it; a qr-coverage entry alone is not enough), or submit on the 2.1 line. | A context with no coverage reference cannot mint 2.2's required qr-coverage; refused, never fabricated. |
| `Bundle.entry:questionnaireResponse` | ⚠ Refuses | A 2.1 questionnaire package with no QuestionnaireResponse entry is not translated to 2.2: the exchange answers with a typed 422 and sends nothing. Include the QuestionnaireResponse entry, or submit on the 2.1 line. | A 2.1 package with no QuestionnaireResponse entry cannot honestly satisfy 2.2's qr-required package shape; refused. |

### pa.dtr: 2.2 → 2.1

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| `QuestionnaireResponse.extension:qr-context` | Translated | no qr-context entry already naming a Coverage beside qr-coverage, and no Coverage named by more than one qr-coverage entry | qr-coverage entries relocate back to 2.1's qr-context slice (min=2 unbounded max tolerates them). |
| `QuestionnaireResponse.extension:qr-coverage` | Translated | — | The relocated entry LEAVES 2.2's qr-coverage slice, which 2.1 does not define. |
| `QuestionnaireResponse.extension:qr-coverage` | Translated | a single qr-context entry naming exactly the qr-coverage entry's Coverage reference (a relative Coverage/<id>, or the identical reference in any form) and equal to it apart from its url | When the source names its Coverage in both qr-coverage and qr-context, the repeated qr-context entry is removed and the qr-coverage entry relocates to qr-context in its own position, so 2.1 carries the Coverage once. |
| `QuestionnaireResponse.extension:qr-context` | ⚠ Refuses | A 2.2 QuestionnaireResponse whose Coverage qr-context repeats the qr-coverage reference but differs from it in other content is not translated to 2.1: removing the repeat could lose content, so the exchange is refused with the error text and nothing is sent. Send the Coverage once, in qr-coverage, or submit on the 2.2 line. | A repeated Coverage entry is removed only when nothing would be lost. |
| `QuestionnaireResponse.extension:qr-context` | ⚠ Refuses | A 2.2 QuestionnaireResponse whose qr-context names a Coverage other than exactly its qr-coverage Coverage is not translated to 2.1, where both would become indistinguishable qr-context entries: the exchange is refused with the error text rather than choosing, and nothing is sent. Name the Coverage only in qr-coverage, or submit on the 2.2 line. | At 2.1 a Coverage in qr-context cannot say whether it was the QR's coverage or only context, so a second Coverage beside qr-coverage is refused rather than merged. |
| `QuestionnaireResponse.extension:qr-context` | ⚠ Refuses | A 2.2 QuestionnaireResponse that names one Coverage in more than one qr-coverage entry is not translated to 2.1: the exchange is refused with the error text and nothing is sent. Send one qr-coverage entry per Coverage. | The translation keeps one Coverage qr-context entry per Coverage; a repeated entry is refused rather than passed on. |
| `QuestionnaireResponse.extension:intendedUse` | Translated | — | intendedUse coding.system remaps to the 2.1 CodeSystem. |
| `QuestionnaireResponse.item.answer.extension:information-origin.extension:source` | Translated | auto-sourced answers only; manual/override origin codes are line-invariant | The information-origin source code for auto-populated answers remaps to the 2.1 code. |
| `QuestionnaireResponse.item.answer.value.extension:itemWeight` | Carried | — | 2.2's itemWeight has no 2.1 definition at all, so it travels inside shn-carried-content and is restored on the way back. |
| `Questionnaire.text` | ⚠ Refuses | A standard Questionnaire without resource narrative cannot be translated from 2.2 to an older line. The exchange returns a typed 422 and sends nothing. Use the native 2.2 line or obtain a source-authored package that meets the target profile; narrative alone does not establish target conformance. | The older standard profile requires narrative. The transform refuses this missing prerequisite without authoring foreign content; the composed 2.2 to 2.0 span preserves the refusal. |

## pa.pas

**Manifest class** below describes each adjacent step as a whole — it is the engine's
worst case across both directions and both sub-cases, not a verdict on any one direction.
`full` = lossless mapping · `carry` = some content travels in `shn-carried-content` ·
`gated` = at least one direction or sub-case has no honest source and refuses.

| Adjacent step | Manifest class |
|---|---|
| 2.0 ↔ 2.1 | `gated` |
| 2.1 ↔ 2.2 | `carry` |

Per direction, and per sub-case where the contract distinguishes them:

| Step | Sub-case | Effects |
|---|---|---|
| 2.0 → 2.1 | request | ⚠ Refuses |
| 2.0 → 2.1 | response | ⚠ Known gap · Synthesized |
| 2.1 → 2.0 | response | ⚠ Known gap · Identity |
| 2.1 → 2.2 | request | ⚠ Known gap |
| 2.1 → 2.2 | response | Synthesized · Translated |
| 2.2 → 2.1 | request | ⚠ Known gap · Carried |
| 2.2 → 2.1 | response | Carried · Translated |

### pa.pas: 2.0 → 2.1

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| `Claim.item.extension:certificationType` | ⚠ Refuses | Request-direction 2.0 submits are not translated to 2.1 at all: the exchange answers every 2.0 submit with a typed 422 naming this element among the four PAS 2.1 makes mandatory, and sends nothing. The element is optional (MustSupport) at 2.0.1 and mandatory at 2.1.0; a 2.0 submit that already carries it is still not translated today. | PAS 2.1 makes mandatory four elements that are optional at 2.0.1; the exchange refuses rather than fabricates while their per-item resolution from the participant's own system is built. |
| `Claim.item.extension:requestType` | ⚠ Refuses | Request-direction 2.0 submits are not translated to 2.1 at all: the exchange answers every 2.0 submit with a typed 422 naming this element among the four PAS 2.1 makes mandatory, and sends nothing. The element is optional (MustSupport) at 2.0.1 and mandatory at 2.1.0; a 2.0 submit that already carries it is still not translated today. | PAS 2.1 makes mandatory four elements that are optional at 2.0.1; the exchange refuses rather than fabricates while their per-item resolution from the participant's own system is built. |
| `Claim.item.location[x]` | ⚠ Refuses | Request-direction 2.0 submits are not translated to 2.1 at all: the exchange answers every 2.0 submit with a typed 422 naming this element among the four PAS 2.1 makes mandatory, and sends nothing. The element is optional (MustSupport) at 2.0.1 and mandatory at 2.1.0; a 2.0 submit that already carries it is still not translated today. | PAS 2.1 makes mandatory four elements that are optional at 2.0.1; the exchange refuses rather than fabricates while their per-item resolution from the participant's own system is built. |
| `Claim.related.relationship` | ⚠ Refuses | Request-direction 2.0 submits are not translated to 2.1 at all: the exchange answers every 2.0 submit with a typed 422 naming this element among the four PAS 2.1 makes mandatory, and sends nothing. The element is optional (MustSupport) at 2.0.1 and mandatory at 2.1.0; a 2.0 submit that already carries it is still not translated today. | PAS 2.1 makes mandatory four elements that are optional at 2.0.1; the exchange refuses rather than fabricates while their per-item resolution from the participant's own system is built. |
| `ClaimResponse.request` | Synthesized | — | 2.1 requires ClaimResponse.request; it is synthesized deterministically from the exchange's correlation identity. |
| `Task.input:QuestionnairesNeeded` | ⚠ Known gap | A pended response whose Task asks for a questionnaire is relayed between PAS 2.0 and 2.1 or 2.2 with the questionnaire request in its source form (a 2.0 identifier, or a 2.1/2.2 context string), which the target line does not define; the gateway does not translate the requested items. Ask a payer on your own line, or read the request in its source form. | PAS 2.0.1 names a needed questionnaire by identifier (questionnaires-needed); 2.1.0 gives a context string (questionnaire-context). The step keeps the 2.0 Task as written, its declared profile included. |

### pa.pas: 2.1 → 2.0

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| *(whole payload)* | Identity | — | PAS 2.1 responses pass to 2.0 unchanged: 2.0 tolerates every 2.1-mandatory element as optional. |
| `Task.input:QuestionnaireContext` | ⚠ Known gap | A pended response whose Task asks for a questionnaire is relayed between PAS 2.0 and 2.1 or 2.2 with the questionnaire request in its source form (a 2.0 identifier, or a 2.1/2.2 context string), which the target line does not define; the gateway does not translate the requested items. Ask a payer on your own line, or read the request in its source form. | PAS 2.1.0 gives a questionnaire context string (questionnaire-context), which 2.0.1 does not define. The step keeps the 2.1 Task as written, its declared profile included. |

### pa.pas: 2.1 → 2.2

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| `Bundle.identifier` | Synthesized | pended responses only — synthesis is only-if-absent on a Bundle top, and the approved/denied reference responses are bare ClaimResponses with no Bundle top at any line | PAS 2.2 mandates Bundle.identifier; minted deterministically from exchange identity (urn:shn:pas:bundle). |
| `ClaimResponse.outcome` | Translated | pended responses only (Bundle carries a Task entry) | Pended outcome code remaps queued->complete (PAS 2.2's pended-outcome vocabulary). |
| `Task.meta.profile` | Translated | pended responses whose Task declares a versioned profile-task | The pended Task's declared profile-task version is re-stated for the target line. |
| `Task.input.extension` | Translated | pended responses: each need's line-number extension | The pended Task's line numbers move between extension-paLineNumber (2.1) and extension-serviceLineNumber (2.2), value unchanged; a line number below 1 has no 2.2 form and is refused. The requested attachment and questionnaire values are not translated between the lines' forms and value sets. |
| `QuestionnaireResponse.extension:qr-context` | ⚠ Known gap | Request-direction PAS submits that embed DTR content are relayed with the embedded QuestionnaireResponse in its 2.1 shape; the gateway does not yet rewrite embedded DTR content. Send a submit whose embedded DTR content already matches the peer's line. | A bridged PAS submit embeds DTR content the PAS module deliberately never touches: the embedded QuestionnaireResponse keeps its 2.1 qr-context shape instead of relocating to 2.2's qr-coverage. Request-direction submits are unbridged until the composed transform ships. |

### pa.pas: 2.2 → 2.1

| Element | Effect | Condition / support boundary | Note |
|---|---|---|---|
| `ClaimResponse.outcome` | Translated | pended responses only (Bundle carries a Task entry) | Pended outcome code remaps complete->queued. |
| `Task.meta.profile` | Translated | pended responses whose Task declares a versioned profile-task | The pended Task's declared profile-task version is re-stated for the target line. |
| `Task.input.extension` | Translated | pended responses: each need's line-number extension | The pended Task's line numbers move between extension-paLineNumber (2.1) and extension-serviceLineNumber (2.2), value unchanged; a line number below 1 has no 2.2 form and is refused. The requested attachment and questionnaire values are not translated between the lines' forms and value sets. |
| `Claim.extension:transmissionIdentifiers` | Carried | — | 2.2-only Claim extension with no 2.1 slot: carried in shn-carried-content and restored value-identical on upcast. |
| `ClaimResponse.extension:claimResponseReviewer` | Carried | — | 2.2-only ClaimResponse extension with no 2.1 slot: carried in shn-carried-content and restored value-identical on upcast. |
| `ClaimResponse.extension:transmissionIdentifiers` | Carried | — | 2.2-only ClaimResponse extension with no 2.1 slot: carried in shn-carried-content and restored value-identical on upcast. |
| `QuestionnaireResponse.extension:qr-coverage` | ⚠ Known gap | Downcast PAS submits that embed DTR content keep the embedded QuestionnaireResponse in its 2.2 shape, which the 2.1 line does not define; the gateway does not yet rewrite embedded DTR content. Send a submit whose embedded DTR content already matches the peer's line. | Downcast submits carry an un-downcast embedded DTR shape: the embedded QuestionnaireResponse keeps 2.2's qr-coverage slice, which 2.1 does not define. Unbridged until the composed transform ships. |

## pa.pdex

PDex runs on a **single native line** — no cross-version steps exist or are needed, so it has
no rows in this matrix and none in the compatibility manifest.

## Composed spans

The 2.0 and 2.2 lines are not adjacent, so a payload crossing between them walks two steps.
Composed spans are covered in their own right rather than inferred from their legs: each
span's end state is a pinned artifact that the live validation lanes validate at the target
line. The suite also pins that a composed chain equals sequential application of its steps.

| Span | Coverage |
|---|---|
| 2.0 → 2.2 | Pinned end-state artifacts, validated at the 2.2 line. |
| 2.2 → 2.0 | Pinned end-state artifacts, validated at the 2.0 line. |

Read that coverage precisely, in three respects.

**What the lanes check.** The PAS artifacts carry a `meta.profile` and are validated against
it. The 2.0 lane loads the DTR 2.0.1, CRD 2.0.1, PDex 2.1.0 and SDC 3.0.0 packages
beside PAS 2.0.1 (since 2026-09), so a DTR artifact targeting 2.0 is profile-validated
there by the profile-conformance gate. The authored downcast control additionally receives
explicit standard Questionnaire and package profile checks on all three lines.
Other unprofiled DTR artifacts receive structural validation in the general sweep.

**How many distinct payloads.** The down-direction span's artifacts are byte-duplicates of
the corresponding adjacent 2.2 → 2.1 outputs, because the second leg (2.1 → 2.0) is a
byte-passthrough identity step. The down span therefore adds acceptance evidence at the 2.0
lane rather than new payload shapes — worth knowing before reading the artifact count as a
count of distinct scenarios.

**What QR validation did not cover.** Validating a QuestionnaireResponse warns that the
referenced Questionnaire canonical could not be resolved, so item- and answer-level conformance
to the Questionnaire's own definition is unchecked. Structural and profile conformance are.

Two package shapes **refuse** rather than producing an artifact. The `pa.dtr` 2.0 → 2.2
source package has no `QuestionnaireResponse` entry, so the second leg's QR-required
gate fires. The captured standard Questionnaire at 2.2 has no resource narrative,
so its downcasts to 2.1 and 2.0 refuse the older standard profile's requirement.
The native source remains unchanged. An independently authored, explicitly profiled
package supplies the successful down-span control. Both refusal paths are asserted
with no partial output or reports escaping composition. Narrative presence alone
does not certify the captured resource against the other target constraints.

## Known gaps

Tracked, deliberate, and asserted: each gap below has a test that proves it still exists, so
it fails loudly the moment the gap closes.

| Contract | Step | Element | Support boundary | Note |
|---|---|---|---|---|
| pa.pas | 2.0 → 2.1 | `Task.input:QuestionnairesNeeded` | ⚠ A pended response whose Task asks for a questionnaire is relayed between PAS 2.0 and 2.1 or 2.2 with the questionnaire request in its source form (a 2.0 identifier, or a 2.1/2.2 context string), which the target line does not define; the gateway does not translate the requested items. Ask a payer on your own line, or read the request in its source form. | PAS 2.0.1 names a needed questionnaire by identifier (questionnaires-needed); 2.1.0 gives a context string (questionnaire-context). The step keeps the 2.0 Task as written, its declared profile included. |
| pa.pas | 2.1 → 2.0 | `Task.input:QuestionnaireContext` | ⚠ A pended response whose Task asks for a questionnaire is relayed between PAS 2.0 and 2.1 or 2.2 with the questionnaire request in its source form (a 2.0 identifier, or a 2.1/2.2 context string), which the target line does not define; the gateway does not translate the requested items. Ask a payer on your own line, or read the request in its source form. | PAS 2.1.0 gives a questionnaire context string (questionnaire-context), which 2.0.1 does not define. The step keeps the 2.1 Task as written, its declared profile included. |
| pa.pas | 2.1 → 2.2 | `QuestionnaireResponse.extension:qr-context` | ⚠ Request-direction PAS submits that embed DTR content are relayed with the embedded QuestionnaireResponse in its 2.1 shape; the gateway does not yet rewrite embedded DTR content. Send a submit whose embedded DTR content already matches the peer's line. | A bridged PAS submit embeds DTR content the PAS module deliberately never touches: the embedded QuestionnaireResponse keeps its 2.1 qr-context shape instead of relocating to 2.2's qr-coverage. Request-direction submits are unbridged until the composed transform ships. |
| pa.pas | 2.2 → 2.1 | `QuestionnaireResponse.extension:qr-coverage` | ⚠ Downcast PAS submits that embed DTR content keep the embedded QuestionnaireResponse in its 2.2 shape, which the 2.1 line does not define; the gateway does not yet rewrite embedded DTR content. Send a submit whose embedded DTR content already matches the peer's line. | Downcast submits carry an un-downcast embedded DTR shape: the embedded QuestionnaireResponse keeps 2.2's qr-coverage slice, which 2.1 does not define. Unbridged until the composed transform ships. |

---

Every cross-version row above is backed by an executed obligation in the gateway's test suite;
live conformance gates also validate the pinned artifacts at their target line. This file is
generated from the same declared-expectations grid on every release; nothing in it is
written by hand.

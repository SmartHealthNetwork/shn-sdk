# SDK wire-vectors

Canonical participant wire fixtures for the Smart Health Network, consumed by
`vectors_test.go`. These are the **public `shn-sdk` repo's standalone hermetic
contract**: the network keeps its own cross-module parity suite privately; this
vector test, importing **only `shnsdk` + stdlib + `golang.org/x/crypto`**, proves the
SDK parses, verifies, and (where deterministic) reproduces the exact bytes the
network exchanges.

## Regenerate

The vectors are MINTED upstream by the network's own vector generator (the canonical
byte source) and CONSUMED by the SDK alone. They are committed fixtures — consumers of
this repo never regenerate them; new vectors arrive with SDK releases.

Regeneration is **deterministic** — the committed vectors do not churn (all keys and
the otherwise-random fields are derived from fixed seeds; see below).

## Fixed clock

Every vector is minted under a single fixed clock: **`2026-06-04T00:00:00Z`**.

## Vectors

| File | Kind | What the SDK does |
|---|---|---|
| `cer.json` | **REPRODUCE** | `shnsdk.BuildEligibilityRequestExact("MBR-COVERED","1234567890",shnsdk.CMSPayerIdentity,clock)` rebuilds it byte-for-byte. |
| `order-select.json` | **REPRODUCE** | `shnsdk.BuildOrderSelectRequest(sr, coverage, "Patient/MBR-COVERED")` rebuilds it byte-for-byte from the SR + Coverage input vectors below (the MBR-COVERED CRD inputs for the prior-auth flow). |
| `order-select-sr.json` | INPUT | The lumbar-MRI `ServiceRequest` (CPT 72148) fed into `BuildOrderSelectRequest`. The SDK can't build it (fhirmap is internal to the network), so it ships as an input. |
| `order-select-coverage.json` | INPUT | The member `Coverage` fed into `BuildOrderSelectRequest`. |
| `crd-cards-pa.json` | CONSUME | `shnsdk.ParseCRDResponse` → one order (`ServiceRequest/sr-MBR-COVERED`) whose coverage information says covered, `auth-needed`, questionnaire `http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri` — the PA-required CRD answer for 72148, built by `shnsdk.BuildCRDResponse` (coverage information as an update system action, no card). The deprecated `shnsdk.ParseCards` reads the same projection. |
| `questionnaire.json` | INPUT | The payer's demo lumbar-MRI DTR `Questionnaire` fed into `shnsdk.FillQuestionnaire`. The SDK can't build the payer fixture (it is internal to the network), so it ships as an input. |
| `questionnaireresponse.json` | **REPRODUCE** | `shnsdk.FillQuestionnaire(questionnaire, cc, qc)` rebuilds it byte-for-byte from `questionnaire.json` + the MBR-COVERED `ClinicalContext`/`QRContext` (`authored` injected from the fixed clock). |
| `claim-bundle.json` | **REPRODUCE** (historical) | Minted by the now-removed `BuildClaimBundle(qr, sr, "Patient/MBR-COVERED", "Coverage/MBR-COVERED", "golden-corr", clock)`, deleted with the minimized PA shape (`04ac7d81`). The current PAS submit builder is `shnsdk.BuildConformantClaimBundleExact(shnsdk.ConformantClaimInputs{QR: qr, SR: sr, PatientRef: "Patient/MBR-COVERED", CoverageRef: "Coverage/MBR-COVERED", MemberID: "MBR-COVERED", Corr: "golden-corr", Created: clock})` — it builds a structurally different conformant shape and does not reproduce this fixture (see the per-line orphan note below). |
| `claimresponse-approved.json` | CONSUME | `shnsdk.ParseClaimResponse` → `PriorAuthResult{Outcome:"approved", PreAuthRef:"PA-0123456789ab", ValidUntil:"2026-09-02"}`. |
| `diagnosticreport-uc04.json` | **INPUT** | A participant's own supplemental report for MBR-UC04, as its system holds it. The SDK builds no DiagnosticReport: `ResumePriorAuth` takes it as `SupplementalReport.Report`, checks it names the authorization's patient (`Patient/MBR-UC04`) and carries it (`TestVectorDiagnosticReportInput`). Also an input to `claimupdate-bundle-uc04.json`. |
| `provenance-uc04.json` | **REPRODUCE** | `shnsdk.BuildProvenance("DiagnosticReport/dr-uc04-operative","Organization/provider",clock)` rebuilds it byte-for-byte under `vecClock`. Also an input to `claimupdate-bundle-uc04.json`. |
| `servicerequest-uc04.json` | INPUT | The pended→amend lumbar-MRI `ServiceRequest` (CPT 72148, patient `MBR-UC04`) fed into the now-removed `BuildClaimUpdateBundle` (current sibling: `shnsdk.BuildConformantClaimUpdateBundleExact`). |
| `questionnaireresponse-uc04.json` | INPUT | Pended→amend filled QR (prior-surgery context: `PriorSurgery=true`, 6 weeks conservative therapy) fed into the now-removed `BuildClaimUpdateBundle` (current sibling: `shnsdk.BuildConformantClaimUpdateBundleExact`). |
| `claimupdate-bundle-uc04.json` | **REPRODUCE** (historical) | Minted by the now-removed `BuildClaimUpdateBundle(qr, sr, dr, prov, "Patient/MBR-UC04", "Coverage/MBR-UC04", "golden-corr-update", "golden-corr", clock)`, deleted with the minimized PA shape. The current PAS update builder is `shnsdk.BuildConformantClaimUpdateBundleExact(shnsdk.ConformantClaimUpdateInputs{QR: qr, SR: sr, PatientRef: "Patient/MBR-UC04", CoverageRef: "Coverage/MBR-UC04", MemberID: "MBR-UC04", Provenance: prov, DiagnosticReport: dr, Corr: "golden-corr-update", OriginalCorr: "golden-corr", Created: clock})` — it builds a structurally different conformant shape and does not reproduce this fixture (see the per-line orphan note below). |
| `claimresponse-pended.json` | CONSUME | `shnsdk.ParsePendedResponse` → `(pended=true, needed=[{Code:"operative-diagnostic-report"}])`. Payer response requesting supplemental data (pended outcome). |
| `claimresponse-denied-uc08.json` | CONSUME | `shnsdk.ParseClaimResponse` → `PriorAuthResult{Outcome:"denied", Denial:{ReasonCode:"A3", Rationale:"…"}}`. Denied prior-auth with reviewAction A3 (Not Certified). |
| `crr-covered.json` | CONSUME | `shnsdk.ParseEligibilityResponse` → `(true, "")`. |
| `crr-notcovered.json` | CONSUME | `shnsdk.ParseEligibilityResponse` → `(false, "coverage-terminated")`. |
| `token.json` | CONSUME / VERIFY | `shnsdk.VerifyBound` accepts it (signed by `authz_pub.b64`) bound to `payer-coverage` / `eligibility-response` / `vec-corr-1` / `payer` / `pci:deadbeef…` / a fixed 64-hex `payloadHash` (AI-2). The SDK never mints tokens. |
| `envelope.json` | CONSUME | `shnsdk.Open` (with `recipient_enc_{pub,priv}.b64`) recovers the known plaintext; cleartext `Metadata` matches. |
| `assertion.b64` | CONSUME / VERIFY | `base64(json(assertion))` header value; the SDK decodes, field-asserts, and `ed25519.Verify` over the canonical signing payload (signer = `assertion_signer_pub.b64`). |

### Audit record vectors (`audit/`)

Each file is `{"record", "signable", "signature"}`, and a refused vector also carries `refused`: an audit record's content, its
canonical signable bytes (`shnsdk.SignableContent`) as hex, and a base64 ed25519
signature over those bytes under `audit/audit_signer_pub.b64`. An implementation in
another language checks its canonical form against `signable`
(`TestVectorAuditRecordSignableContent`).

| File | Record |
|---|---|
| `audit/v1-exchange.json` | version 1: an exchange-leg record with neither `recordId` nor `involvement` |
| `audit/v1-exchange-recordid.json` | version 1: the same, with a `recordId` |
| `audit/v1-exchange-involvement.json` | version 1: the Hub's `answered` record of the payer's answer on that leg, paired under the payer's own binding of another patient (`involvement` `payer-held`) |
| `audit/v2-exchange.json` | version 2: an exchange-leg record with its `correlationId`, under the prefixed transaction type `pa.pas-claim-update` |
| `audit/v2-exchange-resend.json` | version 2: a resent leg's request record, under its own correlation id, naming `v2-exchange`'s leg in `resends` |
| `audit/v2-authz-decision.json` | version 2: an Authorization Framework denial with the leg's `correlationId` |
| `audit/v2-conformance-finding.json` | version 2: a gateway's `conformance-finding` record with its `finding` |
| `audit/v2-probe.json` | version 2: an `audit-probe` record |
| `audit/v2-exchange-involvement.json` | version 2: the Hub's `answered` record of the payer's answer on `v2-exchange`'s leg, paired with the neutral `involvement` `responder-held` |
| `audit/v2-module-details-authored.json` | version 2: a provider gateway's `module-details` record for a `pa.pas-claim` request its builder authored |
| `audit/v2-module-details-bridged.json` | version 2: a `module-details` record for another `pa.pas-claim` request, which its builder authored and a line bridge then translated from 2.1 to 2.2 (edit `E-09`), with its loss; it keeps the builder |
| `audit/v2-gateway-start.json` | version 2: a gateway's `gateway-start` record naming its build, wire versions and modules |
| `audit/v2-refused-builder-on-other-edit.json` | **refused**: a provider gateway's `module-details` record for a `pa.dtr-questionnaire-fetch` answer it edited under `E-08`, naming a builder, although `E-08` edits the payer's answer, which no builder authored. Its signature verifies, and the Audit Plane refuses it `400` under the rule in its `refused` member |

A vector with a `refused` member is signed correctly and must still be refused: a
builder is allowed on an `edited` body only when every edit is an IG-line bridge's
translation of the whole body (`shnsdk.BuilderKeepingEdits()`, today `E-09`).

The version 1 vectors are minted through the Audit Plane's own canonicalisation.
Version 1 vectors never change. A version 2
record sets `"version": 2`, and its members follow `involvement` in the order
`version`, `correlationId`, `resends`, `finding`, `module`, `gateway`. Every member of
`finding`, `module` and `gateway` is always present, and an empty list is `[]`.

The canonical form is Go's `encoding/json` encoding of the record's members in
the order above, with no insignificant whitespace: the ten members from
`timestamp` to `payloadBundleHash` are always present; `recordId` and every
later member are left out when empty or zero; and strings use Go's escaping,
which writes `<`, `>`, `&`, U+2028 and U+2029 as `\u` escapes.

### Companion key files

| File | Contents |
|---|---|
| `authz_pub.b64` | Authorization Framework Ed25519 verifying key (verifies `token.json`). |
| `recipient_enc_pub.b64` / `recipient_enc_priv.b64` | Recipient X25519 keypair that opens `envelope.json`. |
| `assertion_signer_pub.b64` | Holder Ed25519 key that signed `assertion.b64`. |
| `audit/audit_signer_pub.b64` | Ed25519 key that signed the audit record vectors. |

## SYNTHETIC TEST-ONLY keys

Every key here is **synthetic and test-only**, derived deterministically by the
network's own vector generator from fixed seeds (`ed25519.NewKeyFromSeed(seed)`, a fixed
curve25519 scalar, and a seeded keystream for the sealed-box ephemeral key). They are
**never** production secrets — no production key material appears in the repository, and
none of these keys protects a real participant or patient. The private keys (`recipient_enc_priv.b64`) are committed ON PURPOSE: a CONSUME
vector is only verifiable if the test can open/verify it, and these protect nothing.

## Per-line vectors (2.1/2.2)

`2.1/` and `2.2/` are ADDITIVE subdirectories carrying native-line vectors — the
flat files above are the PAS/DTR/CRD **2.0** line and are never touched by the
per-line generator. Seven of them changed in shn-sdk v0.63.0: `cer.json`,
`claim-bundle.json`, `claimupdate-bundle-uc04.json`, `crr-covered.json`,
`crr-notcovered.json`, `order-select-coverage.json` and `order-select.json`. Only the vectors that are genuinely
line-bearing (built via the `…AtLine` builders) **and** actually read by an
sdk test get a per-line copy; a vector with no PAS/DTR/CRD line dependency
(`order-select*`, `crd-cards-pa.json`, `questionnaire.json`, `crr-*.json`,
`token.json`, `envelope.json`, `assertion.b64`, …) is never duplicated. Every
row below is covered by exactly one `sdk/vectors_test.go` `TestVector*AtLine*`
test, run against both `2.1/` and `2.2/`:

| File | Line-gated by | Covering test |
|---|---|---|
| `<line>/questionnaireresponse.json` | DTR `FillQuestionnaireAtLine` — origin-code + qr-coverage/qr-context shape (2.2 delta). | `TestVectorFillQuestionnaireAtLineReproduce` |
| `<line>/claimresponse-approved.json` | PAS `BuildClaimResponseAtLine` — documented "interface symmetry" (no structural delta at ANY line); still emitted per line for a complete, predictable per-line file set. | `TestVectorClaimResponseAtLineConsume` |
| `<line>/claimresponse-pended.json` | PAS `BuildPendedResponseAtLine` — response `Bundle.identifier` becomes mandatory at 2.2. | `TestVectorPendedAtLineConsume` |
| `<line>/claimresponse-denied-uc08.json` | PAS `BuildDeniedResponseAtLine` — same "interface symmetry" note as approved. | `TestVectorDeniedAtLineConsume` |

Minted by the SAME golden-corpus generator of the network that produces every other
vector in this directory, under the SAME fixed `vecClock`. `sdk/vectors_test.go`'s `TestVector*AtLine*`
tests extend the existing per-file read idiom — there is no directory-walking
vector loader in the SDK module (by design: the SDK never imports `internal/`,
and a walker would need to invent its own per-line file-class taxonomy that the
network's own conformance completeness guard already owns).

**No per-line copy of `claim-bundle.json`, `claimupdate-bundle-uc04.json`, or
`questionnaireresponse-uc04.json`:** their flat counterparts above are
pre-existing orphans — read by no sdk test since the minimized PA shape they
served was deleted (`04ac7d81`) — and are retained only as fixtures, so
`writePerLineVectors` deliberately excludes those three names rather than
minting a second, equally-unread copy per line.

### Why some vectors are CONSUME, not REPRODUCE

- The **token** is byte-stable (Ed25519 signatures are deterministic), but the SDK only
  *verifies* tokens — it never mints them (only the Authorization Framework mints them).
- The **envelope** uses a NaCl anonymous sealed box (an ephemeral sender key); the
  ciphertext is not something the SDK can reproduce. The generator seeds the ephemeral
  key only so the committed file is stable.
- The **assertion** carries a `jti`; the generator pins it to a fixed value only for
  vector stability. A real SDK assertion (`Identity.Assertion`) stamps a fresh random `jti`.

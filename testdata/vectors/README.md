# SDK wire-vectors

Canonical participant wire fixtures for the SHN substrate, consumed by
`vectors_test.go`. These are the **public `shn-sdk` repo's standalone hermetic
contract**: the substrate keeps its own cross-module parity suite privately; this
vector test, importing **only `shnsdk` + stdlib + `golang.org/x/crypto`**, proves the
SDK parses, verifies, and (where deterministic) reproduces the exact bytes the
substrate exchanges.

## Regenerate

The vectors are MINTED upstream by the substrate's vector generator (the canonical
byte source) and CONSUMED by the SDK alone. They are committed fixtures — consumers of
this repo never regenerate them; new vectors arrive with SDK releases.

Regeneration is **deterministic** — the committed vectors do not churn (all keys and
the otherwise-random fields are derived from fixed seeds; see below).

## Fixed clock

Every vector is minted under a single fixed clock: **`2026-06-04T00:00:00Z`**.

## Vectors

| File | Kind | What the SDK does |
|---|---|---|
| `cer.json` | **REPRODUCE** | `shnsdk.BuildEligibilityRequest("MBR-COVERED","1234567890",clock)` rebuilds it byte-for-byte. |
| `order-select.json` | **REPRODUCE** | `shnsdk.BuildOrderSelectRequest(sr, coverage, "Patient/MBR-COVERED")` rebuilds it byte-for-byte from the SR + Coverage input vectors below (the MBR-COVERED CRD inputs for the prior-auth flow). |
| `order-select-sr.json` | INPUT | The lumbar-MRI `ServiceRequest` (CPT 72148) fed into `BuildOrderSelectRequest`. The SDK can't build it (fhirmap is substrate-only), so it ships as an input. |
| `order-select-coverage.json` | INPUT | The member `Coverage` fed into `BuildOrderSelectRequest`. |
| `crd-cards-pa.json` | CONSUME | `shnsdk.ParseCards` → `(true, "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri")` — the PA-required cards response for 72148. |
| `questionnaire.json` | INPUT | The payer's sandbox lumbar-MRI DTR `Questionnaire` fed into `shnsdk.FillQuestionnaire`. The SDK can't build the payer fixture (it's substrate-only), so it ships as an input. |
| `questionnaireresponse.json` | **REPRODUCE** | `shnsdk.FillQuestionnaire(questionnaire, cc, qc)` rebuilds it byte-for-byte from `questionnaire.json` + the MBR-COVERED `ClinicalContext`/`QRContext` (`authored` injected from the fixed clock). |
| `claim-bundle.json` | **REPRODUCE** | `shnsdk.BuildClaimBundle(qr, sr, "Patient/MBR-COVERED", "Coverage/MBR-COVERED", "golden-corr", clock)` rebuilds it byte-for-byte from `questionnaireresponse.json` + `order-select-sr.json` (the PAS submit Bundle: Claim+QR+SR; the injected clock drives the bundle id/timestamp). |
| `claimresponse-approved.json` | CONSUME | `shnsdk.ParseClaimResponse` → `PriorAuthResult{Outcome:"approved", PreAuthRef:"PA-0123456789ab", ValidUntil:"2026-09-02"}`. |
| `diagnosticreport-uc04.json` | **REPRODUCE** | `shnsdk.BuildDiagnosticReport("dr-uc04-operative","Patient/MBR-UC04","72148","MRI lumbar spine w/o contrast")` rebuilds it byte-for-byte (no clock dependency — the effectiveDate is fixed). Also an input to `claimupdate-bundle-uc04.json`. |
| `provenance-uc04.json` | **REPRODUCE** | `shnsdk.BuildProvenance("DiagnosticReport/dr-uc04-operative","Organization/provider",clock)` rebuilds it byte-for-byte under `vecClock`. Also an input to `claimupdate-bundle-uc04.json`. |
| `servicerequest-uc04.json` | INPUT | The pended→amend lumbar-MRI `ServiceRequest` (CPT 72148, patient `MBR-UC04`) fed into `BuildClaimUpdateBundle`. |
| `questionnaireresponse-uc04.json` | INPUT | Pended→amend filled QR (prior-surgery context: `PriorSurgery=true`, 6 weeks conservative therapy) fed into `BuildClaimUpdateBundle`. |
| `claimupdate-bundle-uc04.json` | **REPRODUCE** | `shnsdk.BuildClaimUpdateBundle(qr, sr, dr, prov, "Patient/MBR-UC04", "Coverage/MBR-UC04", "golden-corr-update", "golden-corr", clock)` rebuilds it byte-for-byte from the four input vectors above. |
| `claimresponse-pended.json` | CONSUME | `shnsdk.ParsePendedResponse` → `(pended=true, needed=[{Code:"operative-diagnostic-report"}])`. Payer response requesting supplemental data (pended outcome). |
| `claimresponse-denied-uc08.json` | CONSUME | `shnsdk.ParseClaimResponse` → `PriorAuthResult{Outcome:"denied", Denial:{ReasonCode:"A3", Rationale:"…"}}`. Denied prior-auth with reviewAction A3 (Not Certified). |
| `crr-covered.json` | CONSUME | `shnsdk.ParseEligibilityResponse` → `(true, "")`. |
| `crr-notcovered.json` | CONSUME | `shnsdk.ParseEligibilityResponse` → `(false, "coverage-terminated")`. |
| `token.json` | CONSUME / VERIFY | `shnsdk.VerifyBound` accepts it (signed by `authz_pub.b64`) bound to `payer-coverage` / `eligibility-response` / `vec-corr-1` / `payer` / `pci:deadbeef…` / a fixed 64-hex `payloadHash` (AI-2). The SDK never mints tokens. |
| `envelope.json` | CONSUME | `shnsdk.Open` (with `recipient_enc_{pub,priv}.b64`) recovers the known plaintext; cleartext `Metadata` matches. |
| `assertion.b64` | CONSUME / VERIFY | `base64(json(assertion))` header value; the SDK decodes, field-asserts, and `ed25519.Verify` over the canonical signing payload (signer = `assertion_signer_pub.b64`). |

### Companion key files

| File | Contents |
|---|---|
| `authz_pub.b64` | Authorization Framework Ed25519 verifying key (verifies `token.json`). |
| `recipient_enc_pub.b64` / `recipient_enc_priv.b64` | Recipient X25519 keypair that opens `envelope.json`. |
| `assertion_signer_pub.b64` | Holder Ed25519 key that signed `assertion.b64`. |

## SYNTHETIC TEST-ONLY keys (AI-5)

Every key here is **synthetic and test-only**, derived deterministically by the
substrate's vector generator from fixed seeds (`ed25519.NewKeyFromSeed(seed)`, a fixed
curve25519 scalar, and a seeded keystream for the sealed-box ephemeral key). They are
**never** production secrets — no literal production key material appears in the repo
(AI-5). The private keys (`recipient_enc_priv.b64`) are committed ON PURPOSE: a CONSUME
vector is only verifiable if the test can open/verify it, and these protect nothing.

### Why some vectors are CONSUME, not REPRODUCE

- The **token** is byte-stable (Ed25519 signatures are deterministic), but the SDK only
  *verifies* tokens — it never mints them (minting is a substrate-private capability).
- The **envelope** uses a NaCl anonymous sealed box (an ephemeral sender key); the
  ciphertext is not something the SDK can reproduce. The generator seeds the ephemeral
  key only so the committed file is stable.
- The **assertion** carries a `jti`; the generator pins it to a fixed value only for
  vector stability. A real SDK assertion (`Identity.Assertion`) stamps a fresh random `jti`.

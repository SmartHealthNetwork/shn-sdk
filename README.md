# shn-sdk — SHN participant SDK (Go)

A dependency-light Go SDK for participating in the **SHN substrate** — the secure
router for exchanging healthcare data between participants. It implements the
participant wire protocol — holder identity, per-operation authorization, sealed
envelopes, and FHIR payloads — so a Go participant integrates without running the
Smart Gateway binary. The first workflow it carries end-to-end is Da Vinci prior
authorization (CRD+DTR+PAS, PDex).

Depends only on the Go standard library, `golang.org/x/crypto`, and
`github.com/samply/golang-fhir-models`. **It never imports substrate-internal code.**

> Preview substrate — not for production deployment. The canonical, language-neutral
> wire spec is [docs/PARTICIPANT_PROTOCOL.md](docs/PARTICIPANT_PROTOCOL.md) in this repo.

## Install

```
go get github.com/SmartHealthNetwork/shn-sdk
```

The CLI:

```
go install github.com/SmartHealthNetwork/shn-sdk/cmd/shn@latest
```

## Sandbox

The preview substrate is live at `shn-preview.org`. The public surfaces:

| Service | Base URL |
|---|---|
| Hub (`POST /route`) | `https://hub.shn-preview.org` |
| Authorization Framework (`POST /authorize`) | `https://authz.shn-preview.org` |
| Registrar (`POST /register`) | `https://registrar.shn-preview.org` |
| FHIR / Patient Access (`GET /metadata`) | `https://fhir.shn-preview.org` |
| Accounts / self-serve client registration | `https://accounts.shn-preview.org` |

`accounts.shn-preview.org` is the **Accounts service** — the self-serve
developer-onboarding control plane for client registration, Cognito-gated (browser
login, token cached at `~/.shn/credentials`). It also serves the machine-readable
discovery descriptor at `GET /discovery` (live endpoints, sandbox responders, seeded
personas).

**New here? Start with the getting-started guide:**
[docs/SANDBOX.md](docs/SANDBOX.md) — the Install → Discover → Register → Build →
Run → Validate path with the exact `shn` commands.

You also need the payer's holder id + X25519 public key and the Authorization
Framework's Ed25519 verifying key — published in the substrate's holder feed /
manifest. **Keys are generated client-side (proof-of-possession): your private keys
never leave your process.**

## Quickstart (CLI)

> The holder id is passed as `--id` at `keygen` and as `--name` at `register`/`eligibility`
> (same value, `ext-provider` below). `-out ./keys` points every command at the same key dir.

```sh
# 1. Generate keys + a public manifest snippet (private keys stay local, 0600).
shn keygen --id ext-provider --role provider \
  --base-url https://ext-provider.example.com -out ./keys

# 2. Register (proof-of-possession). Admission is Trust-admin-gated: an operator
#    supplies the admin assertion out of band (self-serve registration is mediated
#    by the portal).
shn register --role provider --name ext-provider \
  --base-url https://ext-provider.example.com \
  --registrar https://registrar.shn-preview.org \
  --admin-assertion "$ADMIN_ASSERTION" -out ./keys

# 3. Run a coverage-eligibility round-trip through the Hub.
shn eligibility --name ext-provider \
  --member MBR-COVERED --dob 1975-04-02 --family Johansson \
  --hub https://hub.shn-preview.org --authz https://authz.shn-preview.org \
  --payer-id payer --payer-enc "$PAYER_ENC_PUB" --authz-pub "$AUTHZ_PUB" -out ./keys
# → covered: true

# 4. Run a prior-authorization (CRD→DTR→PAS) through the Hub. Payer +
#    endpoints are resolved from the sandbox discovery descriptor; the order +
#    clinical context are the fixed sandbox values.
shn priorauth --member MBR-COVERED \
  --discovery https://accounts.shn-preview.org --id ext-provider -keys ./keys
# → outcome=approved preAuthRef=PA-… validUntil=…
```

## Register a sandbox client (self-serve)

The Accounts service (`accounts.shn-preview.org`) provides Cognito-gated self-serve
client registration. Keys are generated client-side; your private keys never leave
your process. The Cognito browser login happens once and the token is cached at
`~/.shn/credentials`.

```sh
# 1. Log in (opens browser for Cognito sign-in; token cached at ~/.shn/credentials).
shn login --accounts https://accounts.shn-preview.org

# 2. Register a client (keys generated locally; public keys sent to the Accounts service).
shn register --accounts https://accounts.shn-preview.org \
  --role provider --name acme --base-url https://acme.example -out ./keys

# 3. List your registered clients.
shn clients --accounts https://accounts.shn-preview.org

# 4. Revoke a client by id (shown in step 3).
shn revoke acme-7f3a --accounts https://accounts.shn-preview.org
```

The `--accounts` flag routes `register`/`clients`/`revoke` through the
Accounts service (Cognito-gated, self-serve) rather than the direct Trust-admin
`POST /register` path. (`rotate` is a holder-self RFC 7592 path you run directly
against the registrar — `shn rotate <id> --registrar <url>` — it never goes through
the Accounts service.) The two paths are complementary: the Accounts service is for
sandbox developers; the direct `POST /register` is the operator/Trust path (see
[docs/PARTICIPANT_PROTOCOL.md](docs/PARTICIPANT_PROTOCOL.md) §2.3).

## Self-validate (`shn doctor`)

One command answers "am I wired up + do my eligibility AND prior-auth round-trips
conform". It fetches the sandbox discovery descriptor and runs eligibility against the
seeded covered/not-covered personas, then — once eligibility passes — runs a
prior-authorization (CRD→DTR→PAS) for the persona that advertises an expected PA outcome, all using
your OWN registered identity — no FHIR validator needed (the substrate validates
server-side). Eligibility is checked first; the PA leg only runs once eligibility
conforms.

```sh
shn doctor --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys
# ✓ sandbox discovery reachable …
# ✓ wire protocol "1.1.0" supported
# ✓ your client "acme-7f3a" is registered
# ✓ MBR-COVERED: covered=true (expected "covered")
# ✓ MBR-NOTCOVERED: covered=false (expected "not-covered")
# ✓ priorauth MBR-COVERED: approved
# PASS
```

Checks run **attribution-ordered** — sandbox-health first (not your fault), then
the wire-version check (before any eligibility leg), then your registration and
outcomes — with a **stable exit code per phase** so a script can tell whose problem
a failure is:

| Code | Phase | Meaning |
|---|---|---|
| 0 | — | all checks passed |
| 10 | sandbox health | discovery/authz/registrar/payer unreachable or missing |
| 20 | wire version | the sandbox speaks a wire version this CLI doesn't — upgrade |
| 30 | your registration | your client isn't in `/holders` (run `shn register`, or it was revoked) |
| 40 | outcome | an eligibility run returned the wrong coverage, or a prior-auth run returned the wrong outcome |

Use `--persona <memberId>` to run a single seeded persona.

## Quickstart (Go)

```go
id, _ := shnsdk.GenerateIdentity("ext-provider") // ed25519 + X25519 keypairs
covered, reason, err := id.RunEligibility(ctx, http.DefaultClient,
    shnsdk.Endpoints{
        HubURL:   "https://hub.shn-preview.org",
        AuthzURL: "https://authz.shn-preview.org",
    },
    shnsdk.Payer{ID: "payer", EncPub: payerEncPub, AuthzPub: authzPub},
    "1234567890", // ordering NPI
    "MBR-COVERED", "1975-04-02", "Johansson",
)
// RunEligibility: resolve PCI → build CoverageEligibilityRequest → authorize the
// leg → seal+route the envelope → verify the bound response token → open → parse.
```

## Public API

| Symbol | Purpose |
|---|---|
| `GenerateIdentity(holderID)` | Fresh Ed25519 (signing) + X25519 (encryption) keypairs. |
| `ResolvePCI(memberID, birthDate, familyName)` | Demo patient-correlation identifier (opaque; treat as Trust-assigned). |
| `Identity.Assertion(audience, now, ttl)` | Signed holder assertion (the `X-Holder-Assertion` header value). |
| `Identity.Authorize(ctx, client, authzURL, req)` | Obtain a per-operation, scope-bound `Token`. |
| `Identity.Registration(role, baseURL)` | Build a proof-of-possession `RegistrationRequest`. |
| `Identity.RunEligibility(ctx, client, endpoints, payer, npi, member, dob, family)` | End-to-end coverage-eligibility round-trip. |
| `Seal(meta, payload, recipientEncPub)` / `Open(env, encPub, encPriv)` | NaCl anonymous-sealed-box envelope crypto (payload-blind routing). |
| `EncodeEnvelope` / `DecodeEnvelope` | Envelope wire (JSON) codec. |
| `BuildEligibilityRequest` / `ParseEligibilityResponse` | FHIR `CoverageEligibilityRequest` / `…Response` helpers. |
| `BuildConformantOrderSelectRequest(srJSON, covJSON, patientID)` / `ParseCards` | CRD order-select request (a CDS Hooks `order-select` request whose `context.draftOrders` is a FHIR collection Bundle); `ParseCards` → a `CardCoverage` (`Covered`, `PANeeded`, `Questionnaires[]`, `SatisfiedPaID`) with `.PARequired()` / `.NeedsDTR()` helpers — the projection of Da Vinci CRD coverage-information. |
| `BuildQuestionnaireFetch` / `ExtractQuestionnaireFromPackage` / `ParseQuestionnaireURL` | DTR questionnaire-fetch request; the response is a Da Vinci `$questionnaire-package` collection Bundle (Questionnaire + dependent Libraries/ValueSets) — `ExtractQuestionnaireFromPackage` returns the bare `Questionnaire`, then `ParseQuestionnaireURL` reads its canonical url. (`BuildQuestionnairePackage` is the responder-side wrapper.) |
| `FillQuestionnaire(questionnaireJSON, cc, qc)` | Fill the sandbox prior-auth DTR questionnaire into a conformant `QuestionnaireResponse` (LOCAL answers + information-origin attribution). Sandbox-targeted: FAILS LOUDLY on an unrecognized questionnaire (never a half-filled QR). |
| `FillQuestionnaireFromAnswers(questionnaireJSON, answers, author, qc)` | Fill ANY DTR questionnaire into a conformant `QuestionnaireResponse` from a caller-supplied `map[string]Answer` (keyed by `linkId`; an `Answer` carries a typed value or an `AnswerCoding{System,Code,Display}`), with information-origin attribution — for manually/attestation-sourced answers when the questionnaire isn't the built-in sandbox one. |
| `BuildConformantClaimBundle(ConformantClaimInputs{QR, SR, PatientRef, CoverageRef, Corr, Created})` / `ParseClaimResponse` | PAS preauthorization submit Bundle (the conformant Da Vinci lean shape — Claim + Patient + Coverage + payor Organization + ServiceRequest + QuestionnaireResponse; `Created` drives the deterministic bundle id/timestamp) + the `ClaimResponse` parser → `PriorAuthResult` (`Outcome:"approved"` + `PreAuthRef`/`ValidUntil`). Denied (the X12 review-action code `A2` "Not Certified" that real Da Vinci PAS payers emit; the legacy `A3` is also accepted) and pended responses parse to their own outcomes; an ambiguous response returns an error, never a wrong `Outcome`. The amended re-POST sibling is `BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{…})`. |
| `VerifyBound(tok, authzPub, now, frame, op, corr, holder, subject, payloadHash)` | Verify a token is bound to exactly this leg, INCLUDING `payloadHash = sha256hex(ciphertext)` (STRICT, AI-2) — the SDK verifies, never mints. Seal-then-authorize: seal the payload first, then authorize against its ciphertext. |

## Conformance

`vectors_test.go` verifies the SDK against canonical wire-vectors in
`testdata/vectors/` (a sealed envelope, a holder assertion, an authorize token, and
CER/CRR fixtures). It imports only `shnsdk` + stdlib + `golang.org/x/crypto`, so it is
the SDK's standalone hermetic contract. See `testdata/vectors/README.md`.

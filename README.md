# shn-sdk — Go SDK & CLI for Smart Health Network participants

[![Go Reference](https://pkg.go.dev/badge/github.com/SmartHealthNetwork/shn-sdk.svg)](https://pkg.go.dev/github.com/SmartHealthNetwork/shn-sdk)
[![License](https://img.shields.io/github/license/SmartHealthNetwork/shn-sdk)](LICENSE)

A dependency-light Go SDK and `shn` CLI for participating in the **Smart Health
Network** — the secure router for exchanging healthcare data between participants. It
implements the participant wire protocol — holder identity, per-operation
authorization, sealed envelopes, and FHIR payloads — so a Go participant integrates
without running the Smart Gateway binary. The first workflow it carries end-to-end is
Da Vinci prior authorization (CRD+DTR+PAS, PDex).

Depends only on the Go standard library, `golang.org/x/crypto`, and
`github.com/samply/golang-fhir-models`. **It never imports SHN-internal code.**

> **Sandbox preview — synthetic data only. Never send production PHI.** Not for
> production deployment.

## What you can test today

- Register a sandbox participant client (self-serve, invite-gated).
- Run eligibility checks against synthetic covered / not-covered members.
- Run a full CRD → DTR → PAS prior-authorization flow.
- Exercise the approved, pended → amended, and denied scenarios.
- Validate your setup end-to-end with `shn doctor`.

The sandbox uses synthetic data only — never send production PHI.

## What this is not

This is not a production connection to the Smart Health Network. It is a public
**sandbox** SDK and CLI for exercising the participant wire protocol — holder identity,
per-operation authorization, sealed-envelope routing, FHIR payloads, and the
prior-authorization scenarios — against **synthetic data only**.

It helps you test technical readiness for CMS-0057-style prior-authorization workflows.
It does not by itself certify regulatory compliance: each participant remains
responsible for its own compliance, data quality, policies, endpoints, and operational
readiness.

## How an exchange flows

```
  Provider / Partner           Smart Health Hub              Payer / Responder
  ──────────────────           ────────────────              ─────────────────
  build FHIR payload
  seal + authorize   ────────▶  route sealed envelope ─────▶  open + validate
                                verify authz + audit;         adjudicate
                                cannot read payload
  open + verify      ◀────────  route sealed response ◀─────  seal + authorize
  bound response
```

The Hub routes sealed envelopes and verifies each leg's authorization metadata. It
holds no decryption key and cannot read the payload.

## Documentation map

- [`docs/SANDBOX.md`](docs/SANDBOX.md) — start here: install → register → run → validate, with the exact `shn` commands.
- [`docs/PARTICIPANT_PROTOCOL.md`](docs/PARTICIPANT_PROTOCOL.md) — the language-neutral wire protocol for direct integration.
- [`docs/TECHNICAL_ARCHITECTURE.md`](docs/TECHNICAL_ARCHITECTURE.md) — the system architecture and security model.
- [`testdata/vectors/README.md`](testdata/vectors/README.md) — canonical wire vectors (the SDK's hermetic conformance contract).

## Install

```
go get github.com/SmartHealthNetwork/shn-sdk
```

The CLI:

```
go install github.com/SmartHealthNetwork/shn-sdk/cmd/shn@latest
```

## Sandbox

The public sandbox is live at `shn-preview.org` (synthetic data only). The public surfaces:

| Service | Base URL |
|---|---|
| Hub (`POST /route`) | `https://hub.shn-preview.org` |
| Authorization Framework (`POST /authorize`) | `https://authz.shn-preview.org` |
| Registrar (`POST /register`) | `https://registrar.shn-preview.org` |
| FHIR / Patient Access (`GET /metadata`) | `https://fhir.shn-preview.org` |
| Developer accounts / sandbox client registration | `https://accounts.shn-preview.org` |

`accounts.shn-preview.org` is the **Accounts service** — the self-serve
developer-onboarding control plane for client registration, Cognito-gated (browser
login, token cached at `~/.shn/credentials`). It also serves the machine-readable
discovery descriptor at `GET /discovery` (live endpoints, sandbox responders, seeded
personas).

**New here? Start with the getting-started guide:**
[docs/SANDBOX.md](docs/SANDBOX.md) — the Install → Discover → Register → Build →
Run → Validate path with the exact `shn` commands.

You also need the payer's holder id + X25519 public key and the Authorization
Framework's Ed25519 verifying key — published in the network's holder feed /
manifest. **Keys are generated client-side (proof-of-possession): your private keys
never leave your process.**

## Quickstart: register a sandbox client

Use this path to test the public sandbox. Registration is invite-gated and goes
through the Accounts service; keys are generated locally and your private keys never
leave your machine. The browser sign-in happens once and the token is cached at
`~/.shn/credentials`.

```sh
# 1. Log in (opens a browser for sign-in; token cached at ~/.shn/credentials).
shn login --accounts https://accounts.shn-preview.org

# 2. Register a client (keys generated locally; only public keys are sent to the
#    Accounts service). The holder id is server-assigned, e.g. acme-7f3a.
shn register --accounts https://accounts.shn-preview.org \
  --role provider --name acme --base-url https://acme.example -out ./keys

# 3. Validate your setup end-to-end (eligibility + prior-auth round-trips).
shn doctor --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys

# 4. Run a prior-authorization (CRD→DTR→PAS) yourself. Payer + endpoints are resolved
#    from the sandbox discovery descriptor; the order + clinical context are the fixed
#    sandbox values.
shn priorauth --member MBR-COVERED \
  --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys
# → outcome=approved preAuthRef=PA-… validUntil=…
```

Manage your clients with `shn clients --accounts <url>` (list) and
`shn revoke <id> --accounts <url>` (revoke). To re-key an existing holder, `rotate` is
a holder-self RFC 7592 path you run directly against the registrar
(`shn rotate <id> --registrar <url>`) — it never goes through the Accounts service.

Drive a gateway's prior-authorization scenarios end-to-end with
`shn send-test --gateway <gateway-url>` — it fires all eight use cases at a
running gateway's `/scenario` routes and tabulates pass/fail (`--json` for
machine-readable output). Handy when you're evaluating a gateway you've stood up
from the [`shn-gateway`](https://github.com/SmartHealthNetwork/shn-gateway)
evaluation bundles.

## Operator registration

For operator-managed deployments (not self-serve sandbox onboarding), registration is
gated by an operator-admin credential supplied out of band. Keys are still generated
locally; your private keys never leave your machine.

```sh
# 1. Generate keys + a public manifest snippet (private keys stay local, 0600).
shn keygen --id ext-provider --role provider \
  --base-url https://ext-provider.example.com -out ./keys

# 2. Register directly against the registrar with the operator-admin assertion.
shn register --role provider --name ext-provider \
  --base-url https://ext-provider.example.com \
  --registrar https://registrar.shn-preview.org \
  --admin-assertion "$ADMIN_ASSERTION" -out ./keys
```

Most sandbox developers should use the self-serve Accounts path above. The direct
`POST /register` path is for operator-managed and non-self-serve environments (see
[docs/PARTICIPANT_PROTOCOL.md](docs/PARTICIPANT_PROTOCOL.md) §2.3).

## Self-validate (`shn doctor`)

One command answers "am I wired up + do my eligibility AND prior-auth round-trips
conform". It fetches the sandbox discovery descriptor and runs eligibility against the
seeded covered/not-covered personas, then — once eligibility passes — runs a
prior-authorization (CRD→DTR→PAS) for the persona that advertises an expected PA outcome, all using
your OWN registered identity — no FHIR validator needed (the network validates
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

## Package map

- `accounts/` — developer-account sign-in (loopback PKCE) + Accounts API client —
  shared by the `shn` CLI and the SHN Kit.

## Public API

Most sandbox users drive everything through the `shn` CLI (above). Use the Go API
directly if you are building a native integration or test harness.

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

**Also exported** (responder + participation helpers; see godoc and `docs/SANDBOX.md` §3c):
`NewResponder` / `ResponderConfig` (the payer-side inbound responder handling all five
transaction types), `ParsePayerIdentifier` (coverage-derived payer routing), `FetchHolders`
/ `NewFeedEncResolver` (registry-feed holder + encryption-key resolution),
`FetchHubTransportKey`, and `WriteBundle` / `LoadBundle` (registration-bundle I/O).

## Accounts package (`shn-sdk/accounts`)

Developer-account sign-in (loopback PKCE) and client management — the same flow the
`shn login` / `register` / `clients` / `revoke` CLI and the SHN Kit's first-run sign-in
drive. Import `github.com/SmartHealthNetwork/shn-sdk/accounts` to build your own sign-in
flow instead of shelling out to the CLI.

| Symbol | Purpose |
|---|---|
| `FetchCLIConfig(ctx, hc, accountsURL)` → `CLIConfig` | Fetch the Accounts service's OIDC issuer + public client id (`GET {accounts}/cli-config`). |
| `FetchOIDC(ctx, hc, issuer)` → `OIDC` | Fetch the issuer's OIDC discovery document (authorize + token endpoints). |
| `StartPKCE(hc, cfg, oidc, ports, now)` → `*PKCEFlow` | Start a loopback-redirect PKCE authorization-code flow on one of `ports`. |
| `PKCEFlow.AuthorizeURL()` | The browser URL to open for sign-in. |
| `PKCEFlow.Wait(ctx)` → `Tokens` | Block until the browser redirect completes; returns the id / access / refresh tokens. |
| `PKCEFlow.Close()` | Tear down the loopback listener (also unblocks `Wait`). |
| `Refresh(ctx, hc, tokenEndpoint, clientID, refreshToken, now)` → `Tokens` | Refresh an expired session without re-authenticating. |
| `EmailFromIDToken(idToken)` | The signed-in developer's email from the id token (display only). |
| `NewClient(baseURL, token)` → `*Client` | Accounts API client, authenticated with a session bearer (the id token). |
| `Client.Create(ctx, name, role, encPub, signPub, baseURL)` → id | Register a client; returns the server-assigned holder id. |
| `Client.SubmitPoP(ctx, id, reg)` | Submit the proof-of-possession for a pending registration. |
| `Client.List(ctx)` → `[]ClientRow` | List the developer's registered clients. |
| `Client.Revoke(ctx, id)` | Revoke a client by id. |

## Conformance

`vectors_test.go` verifies the SDK against canonical wire-vectors in
`testdata/vectors/` (a sealed envelope, a holder assertion, an authorize token, and
CER/CRR fixtures). It imports only `shnsdk` + stdlib + `golang.org/x/crypto`, so it is
the SDK's standalone hermetic contract. See `testdata/vectors/README.md`.

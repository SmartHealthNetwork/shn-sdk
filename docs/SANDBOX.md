# SHN Sandbox — Getting Started

**Audience:** developers building a participant (provider/payer/facility) against the
SHN prior-authorization substrate. This is the **Discover → Register → Build → Run →
Validate** path, with the exact `shn` commands per step.

> **Prefer a browser?** The developer portal at `https://developers.shn-preview.org`
> shows the live sandbox descriptor, your registered clients, and the exact commands.
> Registration (key generation) and runs stay the `shn` CLI — the portal is the
> Discover + manage surface.

> **Synthetic data only (FR-39).** The sandbox is seeded with deterministic synthetic
> personas (Linda Johansson et al.). **Never send production PHI.** Every persona,
> member id, and DOB below is fabricated test data.

The public sandbox is `shn-preview.org`. Substitute your own apex if you run a private
deployment (the discovery descriptor is the source of truth for the live URLs).

## 0. Install the CLI

**macOS / Linux** — one command. The installer detects your platform, downloads the
prebuilt `shn` binary from the developer portal, and verifies its published SHA-256
before installing to `~/.local/bin`:

```sh
curl -fsSL https://developers.shn-preview.org/install.sh | sh
```

**Windows** — download
[`shn_windows_amd64.exe`](https://developers.shn-preview.org/downloads/shn_windows_amd64.exe)
and put it on your `PATH`.

**Go toolchain** — the SDK is public at
[github.com/SmartHealthNetwork/shn-sdk](https://github.com/SmartHealthNetwork/shn-sdk)
(Apache-2.0):

```sh
go install github.com/SmartHealthNetwork/shn-sdk/cmd/shn@latest
```

Private deployment? The binaries are served by your own portal host — substitute it:
`curl -fsSL <portal>/install.sh | SHN_BASE_URL=<portal> sh`.

---

## 1. Discover

The sandbox publishes a machine-readable discovery descriptor — the FR-37 conformance
surface. It is **sufficient to drive the eligibility loop**: it lists the live
endpoints, the sandbox responders (the payer you exchange with), and the seeded
personas with their expected eligibility outcomes. No keys are embedded; you resolve
the payer's encryption key from the registrar feed and the Authorization Framework's
verifying key from `authzPublicKeyURL`.

```sh
curl https://accounts.shn-preview.org/discovery
```

```json
{
  "sandbox": true,
  "syntheticDataOnly": true,
  "wireProtocolVersion": "1.1.0",
  "endpoints": {
    "hub": "https://hub.shn-preview.org",
    "authz": "https://authz.shn-preview.org",
    "registrar": "https://registrar.shn-preview.org",
    "patientAccess": "https://fhir.shn-preview.org"
  },
  "authzPublicKeyURL": "https://authz.shn-preview.org/pubkey",
  "sandboxResponders": [{ "role": "payer", "holderId": "payer" }],
  "sandboxPersonas": [
    { "memberId": "MBR-COVERED",    "dob": "1975-04-02", "family": "Johansson", "expectedEligibility": "covered" },
    { "memberId": "MBR-NOTCOVERED", "dob": "1980-09-15", "family": "Reyes",     "expectedEligibility": "not-covered" }
  ],
  "docs": "https://github.com/SmartHealthNetwork/shn-sdk/blob/main/docs/SANDBOX.md"
}
```

See `docs/PARTICIPANT_PROTOCOL.md` §discovery for the full field contract (how to
resolve the payer `encPub` from `/holders` and the authz pub from `/pubkey`).

---

## 2. Register

Self-serve client registration goes through the **Accounts service** (Cognito-gated).
Log in once (the token caches at `~/.shn/credentials`), then register. Keys are
generated client-side — your private keys never leave your process.

```sh
# Log in (opens a browser for Cognito sign-in; token cached at ~/.shn/credentials).
shn login --accounts https://accounts.shn-preview.org

# Register a client. The holder id is server-assigned and printed on success;
# keys are written to -out.
shn register --accounts https://accounts.shn-preview.org \
  --role provider --name acme --base-url https://acme.example -out ./keys
# → Registered acme-7f3a. Keys in ./keys.
```

List or revoke your clients:

```sh
shn clients --accounts https://accounts.shn-preview.org
shn revoke acme-7f3a --accounts https://accounts.shn-preview.org
```

---

## 3. Build / Run

Run a coverage-eligibility round-trip (UC-01) for a seeded persona, originating from
your registered identity. Resolve the payer encryption key (from the registrar
`/holders` feed) and the Authorization Framework verifying key (from `/pubkey`), then:

```sh
shn eligibility --name acme-7f3a \
  --member MBR-COVERED --dob 1975-04-02 --family Johansson \
  --hub https://hub.shn-preview.org --authz https://authz.shn-preview.org \
  --payer-id payer --payer-enc "$PAYER_ENC_PUB" --authz-pub "$AUTHZ_PUB" -out ./keys
# → covered: true
```

The not-covered persona returns the other branch:

```sh
shn eligibility --name acme-7f3a \
  --member MBR-NOTCOVERED --dob 1980-09-15 --family Reyes \
  --hub https://hub.shn-preview.org --authz https://authz.shn-preview.org \
  --payer-id payer --payer-enc "$PAYER_ENC_PUB" --authz-pub "$AUTHZ_PUB" -out ./keys
# → covered: false
```

---

## 3a. Prior-authorization (UC-03)

Once eligibility conforms, run a **prior-authorization** — the full Da Vinci
CRD→DTR→PAS leg sequence — for the covered persona. `shn priorauth` resolves the
sandbox surface from the discovery descriptor (same path as `shn doctor`), drives the
fixed UC-03 order (a lumbar-spine MRI without contrast, CPT 72148 / ICD-10-CM M51.16),
and prints the payer's outcome:

```sh
shn priorauth --member MBR-COVERED --discovery https://accounts.shn-preview.org \
  --id acme-7f3a -keys ./keys
# → outcome=approved preAuthRef=… validUntil=…
```

**Why approved — the answers that drive the outcome.** The CRD card requires PA for
this order, DTR fetches the lumbar-MRI questionnaire, and the SDK fills it from the
UC-03 clinical context (`shnsdk.SandboxUC03Context()`). The load-bearing answers the
sandbox payer adjudicates on are:

| DTR answer | UC-03 value | Why it matters |
|---|---|---|
| Conservative therapy (weeks) | **6** (≥6 required) | Documents the payer's conservative-therapy threshold was met |
| Prior imaging | **true** | Prior imaging is on file |
| Neuro deficit | **false** | No red-flag neuro deficit forcing a different pathway |

These mirror the substrate's MBR-COVERED clinical fixture, so the payer adjudicates the
claim as **approved** with a `PreAuthRef`. **Change an answer and the adjudication
changes** — e.g. drop the conservative-therapy weeks below the threshold and the same
order no longer clears the payer's criteria. The answers are dev-visible inputs
(`SandboxUC03Context`), never hardcoded inside the round-trip, precisely so an
integrator can see *which* clinical facts carry the decision.

> **Outcome vocabulary:** `approved` | `no-pa-required` | `pended` | `denied`.
> See `docs/PARTICIPANT_PROTOCOL.md` §7a.2 and §7b.

---

## 3b. Per-scenario sandbox contexts — answers drive the outcome (FR-35)

The sandbox seeds **three synthetic personas** for the prior-auth scenarios. The
**answers (the clinical context), not the member id, drive the payer's adjudication**.
This is FR-35's approve-vs-deny-variant design: the same member identifier with a
different clinical context would produce a different outcome; it is the answer values
the sandbox payer evaluates.

| Member | Context constructor | Expected outcome |
|---|---|---|
| `MBR-COVERED` | `SandboxUC03Context()` | `approved` |
| `MBR-UC04` | `SandboxUC04Context()` | `pended` on exchange-1, then `approved` after the ClaimUpdate (UC-04) |
| `MBR-UC08` | `SandboxUC08Context()` | `denied` |

Use `SandboxContextFor(memberID)` to resolve the right context for a given member
without hard-coding the pairing:

```go
cc, ok := shnsdk.SandboxContextFor("MBR-UC04")
// cc == SandboxUC04Context(), ok == true
```

**The concrete answers that separate approved, pended, and denied:**

| Answer | UC-03 (approved) | UC-04 (pended→approved) | UC-08 (denied) |
|---|---|---|---|
| Conservative therapy (weeks) | **6** (≥6) | **6** (≥6) | **4** (< 6) |
| Prior imaging | true | true | true |
| Neuro deficit | false | false | false |
| Prior surgery | false | **true** | false |

`SandboxUC08Context()` sets `ConservativeTherapyWeeks: 4` — one answer below the
payer's 6-week threshold — and the same order that approves for UC-03 is denied.
`SandboxUC04Context()` adds `PriorSurgery: true` to the UC-03 context, which the
payer pends awaiting an operative DiagnosticReport.

**Change one answer and watch the outcome change.** A dev can pass a modified
`ClinicalContext` directly to `RunPriorAuth` — the clinical answers are dev-visible
inputs (`PriorAuthRequest.Clinical`), never hidden inside the round-trip. For example,
passing an `MBR-UC08` member with the UC-03 approved context would adjudicate
*approved*: it is the `ConservativeTherapyWeeks: 4` answer that drives the denial,
not the member id. This is the first thing a real integrator does to build confidence
in the adjudication logic.

### 3b-i. UC-04 pended → amend flow (CLI)

Run with `MBR-UC04` to exercise the pended→amend path. The CLI prints the outcome
and writes a serializable resume handle:

```sh
shn priorauth --member MBR-UC04 --discovery https://accounts.shn-preview.org \
  --id acme-7f3a -keys ./keys
# → outcome=pended needed=operative-diagnostic-report resume=shn-resume.json
# → resume with: shn priorauth resume --resume shn-resume.json --sandbox-supplemental --discovery <url> --id <id> -keys <dir>
```

The resume handle (`shn-resume.json` by default; override with `--resume-out`) is a
JSON file that carries the original submit correlation id, the subject PCI, and the
QR/SR from exchange-1 — everything the ClaimUpdate needs. It JSON round-trips across
process restarts, modelling the real pend→amend gap (hours to days).

Resume with the sandbox supplemental (the operative DiagnosticReport + Provenance
that matches what the payer pended on):

```sh
shn priorauth resume --resume shn-resume.json --sandbox-supplemental \
  --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys
# → outcome=approved preAuthRef=… validUntil=…
```

`--sandbox-supplemental` supplies `SandboxUC04Report()` — the pre-built operative
report fixture. To supply your own supplemental:

```sh
shn priorauth resume --resume shn-resume.json \
  --report-id dr-my-operative --report-cpt 72148 \
  --report-display "MRI lumbar spine w/o contrast" \
  --provenance-agent "Organization/acme-7f3a" \
  --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys
```

The `--provenance-agent` flag is **required** for a non-sandbox supplemental
(FR-32: supplemental data must carry provenance attribution; the SDK rejects it
before sealing if the agent is absent).

### 3b-ii. UC-08 denied flow (CLI)

Run with `MBR-UC08` to exercise the denied path:

```sh
shn priorauth --member MBR-UC08 --discovery https://accounts.shn-preview.org \
  --id acme-7f3a -keys ./keys
# → outcome=denied reasonCode=A3 rationale="…"
# → appeal: …
```

`reasonCode=A3` is the PAS X12 reviewActionCode for "Not Certified" (denied). The
rationale is the payer's `ClaimResponse.disposition`; the appeal line (if present)
is the first `processNote`. There is no `preAuthRef` on a denied response.

---

## 4. Validate

`shn doctor` is the one-command self-validate: it fetches the discovery descriptor and
runs eligibility against **every** seeded persona using your own registered identity,
asserting the expected coverage outcome — **and** runs the UC-03 prior-authorization
for the persona that advertises an expected PA outcome, asserting it. A green `doctor`
means **both** eligibility AND prior-auth conform. It needs no FHIR validator — the
substrate validates server-side.

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

Checks are **attribution-ordered** with a **stable exit code per phase**, so a script
can tell whose problem a failure is:

| Code | Phase | Meaning |
|---|---|---|
| 0 | — | all checks passed |
| 10 | sandbox health | discovery/authz/registrar/payer unreachable or missing (not your fault) |
| 20 | wire version | the sandbox speaks a wire version this CLI doesn't — upgrade your SDK/CLI |
| 30 | your registration | your client isn't in `/holders` (run `shn register`, or it was revoked) |
| 40 | outcome | an eligibility run returned the wrong coverage |

Run a single persona with `--persona MBR-COVERED`.

---

## Next steps

- **Wire spec:** `docs/PARTICIPANT_PROTOCOL.md` — the language-neutral Option-B
  contract (identity, assertions, authorize, envelopes, the full UC-01 worked example,
  and §discovery).
- **Go SDK:** [github.com/SmartHealthNetwork/shn-sdk](https://github.com/SmartHealthNetwork/shn-sdk)
  (public, Apache-2.0) — the importable participant surface
  (`shnsdk.Identity.RunEligibility`, envelope crypto, FHIR helpers). See its README.
- **Integration options:** run the SHN Smart Gateway binary, or implement the wire
  contract natively — `docs/PARTICIPANT_PROTOCOL.md` §6 covers both surfaces.

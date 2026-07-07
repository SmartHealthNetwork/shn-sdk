# SHN Sandbox — Getting Started

**Audience:** developers building a participant (provider/payer/facility) against the
SHN substrate — the secure exchange router for healthcare data. This is the
**Discover → Register → Build → Run → Validate** path, with the exact `shn` commands
per step. The sandbox's first end-to-end workflow is prior authorization.

> **Prefer a browser?** The developer portal at `https://developers.shn-preview.org`
> shows the live sandbox descriptor, your registered clients, and the exact commands.
> Registration (key generation) and runs stay the `shn` CLI — the portal is the
> Discover + manage surface.

> **Just want to see it run first?** The **SHN Kit** desktop app (Mac/Windows,
> [Releases](https://github.com/SmartHealthNetwork/shn-kit)) runs the full eight-scenario
> Prior Authorization suite locally with no Docker and no CLI — a zero-setup way to watch
> the exchange end-to-end before you build against it here.

> **First time here? Request access.** The sandbox is invite-gated: submit the
> **Request access** form at `https://developers.shn-preview.org` (no account
> needed). When approved you'll receive an invite email with a temporary
> password — sign in at the portal, set your password, and continue below.

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
  "hubTransportKeyURL": "https://hub.shn-preview.org/transport-key",
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
  --role provider --name acme --base-url https://your-org.example.com -out ./keys
# → Registered acme-7f3a. Keys in ./keys.
```

> `--base-url` must be an **https URL that publicly resolves** (the registrar
> rejects private, loopback, link-local, and unresolvable addresses with
> `400 "invalid baseURL: …"`). If you only *originate* requests (CRD/DTR/PAS via
> the CLI or SDK), the Hub never dials your baseURL — use any https URL you
> control, e.g. your organization's website. Responders must use the real
> endpoint where their gateway listens.

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

## 3a. Prior-authorization (the CRD → DTR → PAS chain)

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

## 3c. Run a payer responder (eligibility + full PA chain)

If you are building a **payer** integration that receives eligibility queries and prior-authorization requests, you can stand up a responder endpoint using `shnsdk.Responder`. The responder serves `POST /substrate/inbound` with the same pipeline as the substrate's own gateway: `X-Hub-Assertion` verification first (before the body is read), then authz token verification, decryption, adjudication, and a sealed-and-authorized response — all in one call to `responder.Handler()`, minus the runtime FHIR $validate the operator-run gateways perform at their own edges (your response shape is parity-pinned against the substrate builder).

**Register a payer-role client:**

```sh
shn register --accounts https://accounts.shn-preview.org \
  --role payer --name acme-payer --base-url https://your-endpoint.example.com -out ./keys
```

> `--base-url` must be a **publicly resolvable https URL** (the registrar rejects
> private, loopback, link-local, and unresolvable addresses with
> `400 "invalid baseURL: …"`). The Hub will POST `{baseURL}/substrate/inbound` — the
> endpoint **must not redirect** on that path.

**Go example (~50 lines). Every symbol references a real public SDK export:**

```go
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	"golang.org/x/crypto/curve25519"
)

type myAdjudicator struct{}

func (myAdjudicator) Eligibility(memberID string) (bool, string) {
	return true, "" // your coverage logic; (false, "reason") to deny
}

// OrderSelect decides whether a CPT needs prior auth and which questionnaire applies.
func (myAdjudicator) OrderSelect(cpt string) (bool, string) {
	if cpt == "72148" { // lumbar-spine MRI — the sandbox UC-03 order
		return true, shnsdk.QuestionnaireCanonicalLumbarMRI
	}
	return false, ""
}

// Questionnaire returns the FHIR Questionnaire for a canonical you advertise.
func (myAdjudicator) Questionnaire(canonical string) ([]byte, bool) {
	if canonical == shnsdk.QuestionnaireCanonicalLumbarMRI {
		return shnsdk.SandboxLumbarQuestionnaire(), true // serve your own in production
	}
	return nil, false
}

// PriorAuth adjudicates a PAS submission (and ClaimUpdate re-adjudication).
func (myAdjudicator) PriorAuth(qrJSON []byte, hasDiagnosticReport bool) (shnsdk.PASDecision, error) {
	return shnsdk.SandboxAdjudicate(qrJSON, hasDiagnosticReport, time.Now(), nil) // replace with your policy
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Discovery: GET /discovery, unmarshal into shnsdk.Discovery.
	//    (shnsdk.Discovery is a public type; the fetch is plain HTTP.)
	resp, err := client.Get("https://accounts.shn-preview.org/discovery")
	if err != nil { log.Fatal(err) }
	body, _ := io.ReadAll(resp.Body); resp.Body.Close()
	var disc shnsdk.Discovery
	if err := json.Unmarshal(body, &disc); err != nil { log.Fatal(err) }

	// 2. Hub transport key — FetchHubTransportKey IS a public SDK function.
	hubKey, err := shnsdk.FetchHubTransportKey(ctx, client, disc.HubTransportKeyURL)
	if err != nil { log.Fatal(err) }

	// 3. Authz public key: GET {authzPublicKeyURL} → {"pubkey": "<base64 ed25519>"}.
	//    (No public FetchAuthzKey helper; fetch inline.)
	resp2, err := client.Get(disc.AuthzPublicKeyURL)
	if err != nil { log.Fatal(err) }
	body2, _ := io.ReadAll(resp2.Body); resp2.Body.Close()
	var authzKeyResp struct { Pubkey string `json:"pubkey"` }
	if err := json.Unmarshal(body2, &authzKeyResp); err != nil { log.Fatal(err) }
	authzPubRaw, err := base64.StdEncoding.DecodeString(authzKeyResp.Pubkey)
	if err != nil { log.Fatal(err) }
	authzPub := ed25519.PublicKey(authzPubRaw)

	// 4. Load identity from keys written by `shn register -out ./keys`.
	//    Files: sign.key (base64 ed25519 private key), enc.key (base64 X25519 private key),
	//    manifest.json ({"id": "<holderID>", ...}).
	//    (No public LoadIdentity helper; read the files directly.)
	keysDir := "./keys"
	signB64, _ := os.ReadFile(filepath.Join(keysDir, "sign.key"))
	signPrivRaw, err := base64.StdEncoding.DecodeString(string(signB64))
	if err != nil { log.Fatal(err) }
	encB64, _ := os.ReadFile(filepath.Join(keysDir, "enc.key"))
	encPrivRaw, err := base64.StdEncoding.DecodeString(string(encB64))
	if err != nil { log.Fatal(err) }
	manifestB, _ := os.ReadFile(filepath.Join(keysDir, "manifest.json"))
	var man struct { ID string `json:"id"` }
	_ = json.Unmarshal(manifestB, &man)

	signPriv := ed25519.PrivateKey(signPrivRaw)
	var encPriv, encPub [32]byte
	copy(encPriv[:], encPrivRaw)
	curve25519.ScalarBaseMult(&encPub, &encPriv)
	// Derive the public key from the private scalar — must match what shn register stored.
	id := shnsdk.Identity{
		HolderID: man.ID,
		SignPub:  signPriv.Public().(ed25519.PublicKey),
		SignPriv: signPriv,
		EncPub:   &encPub,
		EncPriv:  &encPriv,
	}

	// 5. Wire up the responder. NewFeedEncResolver and NewResponder are public SDK exports.
	responder, err := shnsdk.NewResponder(shnsdk.ResponderConfig{
		Identity: id,
		AuthzURL: disc.Endpoints.Authz,
		// AuthzPub must be the verifying key of the SAME service AuthzURL points
		// at (both come from one discovery descriptor here — keep it that way).
		AuthzPub:        authzPub,
		HubTransportPub: hubKey,
		ResolveEnc:      shnsdk.NewFeedEncResolver(client, disc.Endpoints.Registrar),
		Adjudicator:     myAdjudicator{},
	})
	if err != nil { log.Fatal(err) }

	log.Fatal(http.ListenAndServe(":8443", responder.Handler()))
}
```

The example listens on plain HTTP — in deployment, terminate TLS in front of it (reverse proxy or load balancer): your registered `--base-url` must be **https**, and the Hub connects to that https endpoint.

> **Note on key loading.** There is no public `LoadIdentity` helper in the SDK today —
> the `loadIdentity` function lives in the `shn` CLI (package-private). The example
> above shows the exact file layout `shn register -out ./keys` produces: `sign.key`
> (std-base64 ed25519 private key, 64 bytes), `enc.key` (std-base64 X25519 private key,
> 32 bytes), and `manifest.json` (`{"id":"<holderID>","role":"payer","encPub":"...","signPub":"...","baseURL":"..."}`).
> You derive `encPub` from `encPriv` via `curve25519.ScalarBaseMult`, or read it directly
> from `manifest.json "encPub"`. A public `LoadIdentity` is on the tracked list.

**Supported operations today:** all five transaction types — `coverage-eligibility`,
`crd-order-select`, `dtr-questionnaire-fetch`, `pas-claim`, `pas-claim-update` — handled
by the four-method `Adjudicator` interface above. The pended-claim ledger is per-process:
if your deployment needs durable pends across restarts or replicas, front the responder
with your own store keyed on the `preAuthRef` the adjudicator returns.

The Hub verifies that your baseURL endpoint serves `POST /substrate/inbound` and sends
an `X-Hub-Assertion` header on every forward (see `PARTICIPANT_PROTOCOL.md` §6.2a).
`shnsdk.Responder` verifies this assertion for you — signature, issuer pin, audience,
expiry, and jti-once — before the body is read.

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
| 40 | outcome | an eligibility run returned the wrong coverage, or a prior-auth run returned the wrong outcome |

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
- **Payer responder:** if you receive eligibility queries or prior-authorization requests,
  see §3c above for the `shnsdk.Responder` quickstart (full PA chain available:
  eligibility + CRD/DTR/PAS prior-auth).

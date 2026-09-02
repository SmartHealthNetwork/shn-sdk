# SHN Preview Environment — Getting Started

The preview environment is a full deployment of the network — the same shape production
will have, on its own domain — and serves as the quality gate for it.

**Audience:** developers at providers, payers, facilities, HIEs, EHR/RCM vendors, and
integration partners building a participant against the Smart Health Network — the
secure exchange router for healthcare data. This is the
**Discover → Register → Build → Run → Validate** path, with the exact `shn` commands
per step. The preview environment's first end-to-end workflow is prior authorization.

> **Prefer a browser?** The developer portal at `https://developers.shn-preview.org`
> shows the live discovery descriptor, your registered clients, and the exact commands.
> Registration (key generation) and runs stay the `shn` CLI — the portal is the
> Discover + manage surface.

> **Just want to see it run first?** The **SHN Kit** desktop app (Mac/Windows,
> [Releases](https://github.com/SmartHealthNetwork/shn-kit)) runs the full eight-scenario
> Prior Authorization suite locally with no Docker and no CLI — a zero-setup way to watch
> the exchange end-to-end before you build against it here.

> **First time here? Request access.** The preview environment is invite-gated: submit
> the **Request access** form at `https://developers.shn-preview.org` (no account
> needed). When approved you'll receive an invite email with a temporary
> password — sign in at the portal, set your password, and continue below.

> **Preview environment — synthetic data only.** The preview environment is seeded
> with deterministic test personas (Linda Johansson et al.). **Never send production
> PHI.** Every persona, member id, and DOB below is fabricated test data.

The public preview environment is `shn-preview.org`. Substitute your own apex if you run
a private deployment (the discovery descriptor is the source of truth for the live URLs).

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

The preview environment publishes a machine-readable discovery descriptor — the
conformance surface. It is **sufficient to drive the eligibility loop**: it lists the
live endpoints, the test responder (the payer you exchange with), and the seeded
personas with their expected outcomes. No keys are embedded; you resolve the payer's
encryption key from the registrar feed and the Authorization Framework's verifying key
from `authzPublicKeyURL`.

```sh
curl https://accounts.shn-preview.org/discovery
```

```json
{
  "demo": true,
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
  "demoResponders": [{ "role": "payer", "holderId": "conformance-payer" }],
  "demoPersonas": [
    { "memberId": "MBR-D-UC01",    "dob": "1972-03-14", "family": "Larsen",             "expectedEligibility": "covered" },
    { "memberId": "MBR-D-UC01-NC", "dob": "1972-03-14", "family": "Larsen-Terminated",  "expectedEligibility": "not-covered" },
    { "memberId": "MBR-D-UC04",    "dob": "1958-12-19", "family": "Okereke",            "expectedEligibility": "covered",
      "expectedPriorAuth": "pended", "expectedAfterAmend": "approved",
      "order": { "system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "G0151", "display": "Services of a qualified physical therapist in the home health setting, each 15 minutes", "diagnosis": "I63.9" } },
    { "memberId": "MBR-D-UC08",    "dob": "1968-07-30", "family": "Delacroix",          "expectedEligibility": "covered",
      "expectedPriorAuth": "denied",
      "order": { "system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "J3490", "display": "Unclassified drugs - investigational gene therapy agent", "diagnosis": "D57.1" } }
  ],
  "docs": "https://github.com/SmartHealthNetwork/shn-sdk/blob/main/docs/PREVIEW.md"
}
```

Every persona also carries a `payerId` (the seeded member's Coverage payor identity),
omitted above for brevity — see `docs/PARTICIPANT_PROTOCOL.md` §1a for the full field
contract (how to resolve the payer's `encPub` from `/holders` by matching `payerId`,
and the authz pub from `/pubkey`).

**`conformance-payer` answers for every demo persona above (payer identifier `00001`).**
Every adjudication these personas return — eligibility, CRD cards, DTR questionnaires,
PAS verdicts — comes from the real Da Vinci reference payer, not a built-in stand-in;
`conformance-payer` forwards natively to it. It is not the network's *only* payer
holder — a second `role=payer` holder, `cambia-payer` (identifier `00200`), answers for
a separate external-payer conformance lane with its own personas — but for every
`MBR-D-*`/`MBR-*` demo persona this guide drives, `conformance-payer` is the one
counterparty, and there is no built-in adjudication path behind it.

---

## 2. Register

Self-serve client registration goes through the **Accounts service** (Cognito-gated).
Log in once (the token caches at `~/.shn/credentials`), then register. Keys are
generated client-side — your private keys never leave your process.

```sh
# Log in (opens a browser for Cognito sign-in; token cached at ~/.shn/credentials).
shn login --accounts https://accounts.shn-preview.org
# No browser on this machine (SSH/CI)? Add --no-browser — the CLI prints a URL to
# open anywhere and you paste back the code it shows.

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
  --member MBR-D-UC01 --dob 1972-03-14 --family Larsen \
  --hub https://hub.shn-preview.org --authz https://authz.shn-preview.org \
  --payer-id conformance-payer --payer-enc "$PAYER_ENC_PUB" --authz-pub "$AUTHZ_PUB" -out ./keys
# → covered: true
```

The not-covered persona returns the other branch (a terminated Coverage seeded for the
same holder — a reason-bearing negative, not an absence):

```sh
shn eligibility --name acme-7f3a \
  --member MBR-D-UC01-NC --dob 1972-03-14 --family Larsen-Terminated \
  --hub https://hub.shn-preview.org --authz https://authz.shn-preview.org \
  --payer-id conformance-payer --payer-enc "$PAYER_ENC_PUB" --authz-pub "$AUTHZ_PUB" -out ./keys
# → covered: false
```

---

## 3a. Prior-authorization (the CRD → DTR → PAS chain)

Once eligibility conforms, run a **prior-authorization** — the full Da Vinci
CRD→DTR→PAS leg sequence — for `MBR-D-UC04`. `shn priorauth` resolves the discovery
surface and the payer (same path as `shn doctor`), drives the persona's own advertised
order (a HCPCS `G0151` home-health physical-therapy visit, ICD-10-CM `I63.9`), and
prints the outcome:

```sh
shn priorauth --member MBR-D-UC04 --discovery https://accounts.shn-preview.org \
  --id acme-7f3a -keys ./keys
# → outcome=pended needed=<whatever the live payer's Task names> resume=shn-resume.json
# → resume with: shn priorauth resume --resume shn-resume.json --report-id <id> --report-cpt <cpt> --discovery <url> --id <id> -keys <dir>
```

**Why pended — how the reference payer actually decides.** The demo persona roster
rides four HCPCS families — `E0250`, `L8000`, `G0151`, `J3490` — and the reference
payer's verdict is a **per-family determination**, pinned against the live Da Vinci
reference payer (`internal/brpayermirror`'s hermetic mirror reproduces the exact same
shapes). It is **not** a function of what your DTR questionnaire answers say: `G0151`
(home-health PT) is a PA-required family the payer pends on the initial submit and
resolves on a ClaimUpdate amendment. The CRD and DTR legs are still genuine Da Vinci
wire traffic — the CRD card really does say PA is required, and the DTR leg really
does fetch the payer's own questionnaire — but because the SDK doesn't know how to
auto-fill a real payer's questionnaire, it submits an honest **zero-answer
`QuestionnaireResponse` shell** naming exactly what the payer advertised
(`status: "in-progress"`, no answers) rather than inventing clinical content. A
production integration puts a clinician — or an operated SDC `$populate` engine — behind
that fill step; the demo path proves the wire mechanics, not a canned answer set.

**`needed=` is whatever the live payer's own pended Task names — not a fixed string.**
On this preview environment the payer gateway native-forwards and relays the reference
payer's pended Bundle **verbatim**; its `Task.input` names what it actually wants, coded
`payer-url` and `questionnaires-needed` (a re-query URL and the still-outstanding
questionnaire canonical), which is what your run prints. `pend-resolution-timer` is a
synthetic label that exists only in the hermetic in-process mirror local/CI runs use
(`internal/brpayermirror`) — it is not something the live reference payer ever puts on
the wire.

> **Outcome vocabulary:** `approved` | `no-pa-required` | `pended` | `denied`.
> See `docs/PARTICIPANT_PROTOCOL.md` §7a.2 and §7b.

Resume with the SDK's shipped supplemental evidence (a `DiagnosticReport` +
`Provenance`). The amendment `ResumePriorAuth` (what `shn priorauth resume` calls) builds
is a conformant Da Vinci Claim Update: the prior Claim rides along in-bundle and
`Claim.related[0].claim` resolves to it, and every Claim item carries the Da Vinci PAS
`infoChanged` extension — the one marker a real PAS payer re-evaluates an amendment on.
Both lanes behave identically here: the hermetic in-process mirror
(`internal/brpayermirror`, the `make up`/local-dev lane) and the live reference payer
(native-forward — what this preview environment's `conformance-payer` runs) re-evaluate
ONLY an `infoChanged`-marked amendment of a prior authorization they actually stored, and
both refuse an amendment whose prior they never saw submitted.

Resolution is **not evidence-driven**: the payer re-pends the amendment (still A4) and
its own pend-resolution **timer** is what later flips the claim to approved, independent
of the supplemental report's specific content — the SDK client re-queries the pend until
the timer resolves it. `Provenance` is required regardless because FR-32 (SHN's own rule)
says supplemental data must carry attribution — it is not a payer verdict input:

```sh
shn priorauth resume --resume shn-resume.json \
  --report-id dr-uc04-operative --report-cpt 72148 \
  --report-display "MRI lumbar spine w/o contrast" \
  --provenance-agent "Organization/acme-7f3a" \
  --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys
# → outcome=approved preAuthRef=AUTH-1234 validUntil=…
```

**Proven scope.** The `outcome=approved` line above is proven on both lanes: hermetically
against the in-process mirror, and live against the real Da Vinci reference payer — the
release gate that pairs the SDK with the real reference implementations drives this same
client path (`shnsdk.RunPriorAuth` → `PriorAuthResult.Resume` → `shnsdk.ResumePriorAuth`)
as a registered participant and asserts the pend really resolves to approved.

`--provenance-agent` is **required** on every resume (supplemental data must carry
provenance attribution; the SDK rejects it before sealing if the agent is absent —
FR-32). `preAuthRef` is the reference payer's own authorization number,
`AUTH-` followed by four digits — never the retired `PA-<hex>` shape.

---

## 3b. The demo persona roster

The network seeds **ten demo personas** — one per scenario (`MBR-D-UC01`…`MBR-D-UC08`),
plus a not-covered/no-consent twin each for UC-01 and UC-05 (`MBR-D-UC01-NC`,
`MBR-D-UC05-NC`). Only four are advertised on the discovery descriptor — the ones a bare
`shn` CLI run can drive end to end, because `shn priorauth`/`shn doctor` resolve a
persona's payer and order from the descriptor itself:

| Member | DOB / family | Eligibility | Order (family) | Prior-auth outcome |
|---|---|---|---|---|
| `MBR-D-UC01` | 1972-03-14 / Larsen | covered | — | n/a (eligibility only) |
| `MBR-D-UC01-NC` | 1972-03-14 / Larsen-Terminated | not-covered | — | n/a (eligibility only) |
| `MBR-D-UC04` | 1958-12-19 / Okereke | covered | `G0151` | pended → **approved** on amend |
| `MBR-D-UC08` | 1968-07-30 / Delacroix | covered | `J3490` | **denied** |

The remaining **six** personas exercise the other two HCPCS families plus the CDex and
patient-authored legs — **not** "the other two families" split evenly across them: only
UC-02 rides `E0250` and only UC-03 rides `L8000`; UC-05, UC-05-NC, UC-06 and UC-07 all
ride `G0151`, same as UC-04:

| Member | DOB / family | Order (family) | What it exercises |
|---|---|---|---|
| `MBR-D-UC02` | 1965-06-11 / Fontaine | `E0250` | no PA required (order-select terminal) |
| `MBR-D-UC03` | 1979-09-02 / Whitfield | `L8000` | approved outright |
| `MBR-D-UC05` | 1963-02-27 / Marchetti | `G0151` | CDex federated query (consent granted) |
| `MBR-D-UC05-NC` | 1963-02-27 / Marchetti-Noconsent | `G0151` | CDex federated query (no consent → denied) |
| `MBR-D-UC06` | 1970-05-08 / Adeyemi | `G0151` | clinician manual-entry + amendment |
| `MBR-D-UC07` | 1985-10-23 / Kowalczyk | `G0151` | patient-authored attestation |

None of these six are reachable through `shn priorauth --member` today because they
aren't in `demoPersonas`. The **SHN Kit** desktop app (see the callout at the top)
drives all eight scenarios against the same reference payer with no CLI needed — though
its rows drive a separate, older persona set (`MBR-COVERED` et al., still live and
seeded), not these `MBR-D-*` ids. A Go participant can still reach any `MBR-D-*` persona
directly with `Identity.RunPriorAuth`, supplying the member/DOB/family/order by hand
instead of resolving them from the descriptor.

### 3b-i. UC-08 denied flow (CLI)

Run with `MBR-D-UC08`:

```sh
shn priorauth --member MBR-D-UC08 --discovery https://accounts.shn-preview.org \
  --id acme-7f3a -keys ./keys
# → outcome=denied reasonCode=A2 rationale="…"
# → appeal: …
```

`J3490` is an excluded-service family: the CRD card itself comes back **not covered**,
so (with `ProceedOnNotCovered` set, which the CLI always does) the flow submits the PAS
claim straight through for the payer's **formal** determination — there is no DTR leg
here at all. `reasonCode` is the PAS X12 review-action code for the denial. X12 306
defines `A3` as "Not Certified" — the conformant denial code, which is what a
locally-run stack (`make up`, holder `conformance-payer` served by the in-process
`cmd/payermirror`) returns on this leg. **This cloud environment's** behavior differs:
since UC-08 is a PA-spine leg native-forwarded to the real reference payer, and that RI
denies with reviewActionCode `A2` (display "Not Certified" — a code/display
self-contradiction in that RI, not a different conformant code), this preview
environment returns `A2` on this leg. The SDK's parser accepts both `A3` and this
observed `A2` shape as a denial — it never emits `A2` itself. The rationale is the
payer's own `ClaimResponse.disposition`; the appeal line, if present, is the first
`processNote`. There is no `preAuthRef` on a denied response.

---

## 3c. Run a payer responder (eligibility + full PA chain)

If you are building a **payer** integration that receives eligibility queries and prior-authorization requests, you can stand up a responder endpoint using `shnsdk.Responder`. The responder serves `POST /substrate/inbound` with the same pipeline the network's own gateways use: `X-Hub-Assertion` verification first (before the body is read), then authz token verification, decryption, adjudication, and a sealed-and-authorized response — all in one call to `responder.Handler()`, minus the runtime FHIR $validate the operator-run gateways perform at their own edges (your response shape is parity-pinned against the network's own builders).

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

// myQuestionnaireCanonical/myQuestionnaireJSON are YOUR OWN content — shn-sdk ships no
// questionnaire fixture of its own for a responder to serve; every payer on the network
// advertises and serves its own. In production, load this from your own content store.
const myQuestionnaireCanonical = "https://your-payer.example.com/fhir/Questionnaire/breast-prosthesis-pa"

var myQuestionnaireJSON = []byte(`{"resourceType":"Questionnaire","status":"active",` +
	`"url":"` + myQuestionnaireCanonical + `","item":[` +
	`{"linkId":"medical-necessity","type":"text","text":"Medical necessity statement"}]}`)

func (myAdjudicator) Eligibility(memberID string) (bool, string) {
	return true, "" // your coverage logic; (false, "reason") to deny
}

// OrderSelect decides whether an order needs prior auth and which questionnaire applies.
// code is opaque here — CPT or HCPCS, whichever system the draft order's ServiceRequest
// used.
func (myAdjudicator) OrderSelect(code string) (bool, string) {
	if code == "L8000" { // breast prosthesis, mastectomy bra — an ADVERTISED HCPCS family
		return true, myQuestionnaireCanonical
	}
	return false, ""
}

// Questionnaire returns the FHIR Questionnaire for a canonical you advertise.
func (myAdjudicator) Questionnaire(canonical string) ([]byte, bool) {
	if canonical == myQuestionnaireCanonical {
		return myQuestionnaireJSON, true
	}
	return nil, false
}

// PriorAuth adjudicates a PAS submission (and ClaimUpdate re-adjudication).
// This is a placeholder — replace it with your own utilization-review policy.
func (myAdjudicator) PriorAuth(qrJSON []byte, hasDiagnosticReport bool) (shnsdk.PASDecision, error) {
	return shnsdk.PASDecision{Outcome: shnsdk.PASApproved, PreAuthRef: "AUTH-0001", ValidUntil: "2027-01-01"}, nil
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
asserting the expected coverage outcome — **and** runs prior-authorization for every
persona that advertises an expected PA outcome (resuming a pend where one is
advertised), asserting each. A green `doctor` means **both** eligibility AND
prior-auth conform, against the real reference payer. It needs no FHIR validator — the
network validates server-side.

```sh
shn doctor --discovery https://accounts.shn-preview.org --id acme-7f3a -keys ./keys
# ✓ network discovery reachable (…)
# ✓ wire protocol "1.1.0" supported
# ✓ authz verifying key fetched
# ✓ registrar /holders feed fetched (N holders)
# ✓ test counterparties resolve in the directory (1 payer(s))
# ✓ your client "acme-7f3a" is registered
# ✓ MBR-D-UC01: covered=true (expected "covered")
# ✓ MBR-D-UC01-NC: covered=false (expected "not-covered")
# ✓ MBR-D-UC04: covered=true (expected "covered")
# ✓ MBR-D-UC08: covered=true (expected "covered")
# ✓ priorauth MBR-D-UC04: pended
# ✓ priorauth MBR-D-UC04: after amend approved
# ✓ priorauth MBR-D-UC08: denied
# PASS
```

Checks are **attribution-ordered** with a **stable exit code per phase**, so a script
can tell whose problem a failure is:

| Code | Phase | Meaning |
|---|---|---|
| 0 | — | all checks passed |
| 10 | network health | discovery/authz/registrar/payer unreachable or missing (not your fault) |
| 20 | wire version | the network speaks a wire version this CLI doesn't — upgrade your SDK/CLI |
| 30 | your registration | your client isn't in `/holders` (run `shn register`, or it was revoked) |
| 40 | outcome | an eligibility run returned the wrong coverage, or a prior-auth run returned the wrong outcome |

Run a single persona with `--persona MBR-D-UC01`.

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

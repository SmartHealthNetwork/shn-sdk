# Participant Protocol — Option-B Substrate Contract

**Audience:** Partner engineers building a native integration with the SHN substrate
without running the Smart Gateway binary. Every claim in this document is verified
against the substrate source code. Field names and endpoint paths are exact.

**Scope:** Preview substrate. This document specifies the **general participant wire contract**
(identity, per-operation authorization, sealed envelopes, payload-blind routing); the worked flows
are the first workflow delivered on it, Prior Authorization (Da Vinci CRD+DTR+PAS, PDex). Do not
use this in a production deployment.

---

## 1. Overview and posture

The substrate consists of four cooperating components:

| Component | Canonical name | Role |
|---|---|---|
| Routing node | Hub | Payload-blind envelope router |
| Per-operation authority service | Authorization Framework | Mints and verifies scope-bound tokens |
| Participant integration point | Smart Gateway | Holder-side FHIR mapping + envelope handling |
| Signed canonical log | Audit Plane | Append-only audit chain |

**The Hub is payload-blind.** It reads only cleartext `Metadata`; it holds no
X25519 private key and cannot decrypt any `ciphertext`. This property is
structural (enforced by the Hub's construction). Every routed leg
is audited before it is forwarded.

**Two participation modes:**

- **Option A** — run the SHN Smart Gateway binary. The gateway handles the
  envelope, FHIR mapping, validation, and authority flow on your behalf.
- **Option B (this document)** — implement the substrate protocol directly. You
  manage keys, assertions, tokens, and envelopes yourself. The public `shn-sdk`
  (the `shnsdk` package + `shn` CLI) is the reference implementation of this path
  (eligibility round-trip, no Smart Gateway dependency).

This document is the Option-B contract.

> **Go participants: use the SDK.** The supported Option-B path for Go is the public
> participant SDK, **`github.com/SmartHealthNetwork/shn-sdk`** (`shnsdk`). It implements
> this protocol standalone (stdlib + `golang.org/x/crypto` only) and ships the `shn`
> CLI (keygen → register → eligibility). This document remains the **canonical wire spec** —
> authoritative for non-Go participants and the exact byte/field contract the SDK is verified against.

---

## 1a. Discovery

A participant's **first call** is the sandbox discovery descriptor: a machine-readable
(FR-37) document listing the live endpoints, the sandbox responder(s) you exchange
with, and the seeded personas. It is **sufficient to drive the eligibility loop** — no
out-of-band URL list or key file required. `shn doctor` consumes it; so does a live
discovery probe in the deploy pipeline.

```
GET {accounts}/discovery
```

Returns (the Accounts service, `accounts.<apex>`):

```json
{
  "sandbox": true,
  "syntheticDataOnly": true,
  "wireProtocolVersion": "1.1.0",
  "igVersions": { "uscore": "6.1.0", "crd": "2.0.1", "dtr": "2.0.1", "pas": "2.0.1", "pdex": "2.1.0" },
  "endpoints": {
    "hub": "https://hub.<apex>",
    "authz": "https://authz.<apex>",
    "registrar": "https://registrar.<apex>",
    "patientAccess": "https://fhir.<apex>"
  },
  "authzPublicKeyURL": "https://authz.<apex>/pubkey",
  "hubTransportKeyURL": "https://hub.<apex>/transport-key",
  "sandboxResponders": [{ "role": "payer", "holderId": "payer" }],
  "operations": [ { "frame": "provider-tpo", "operation": "eligibility-inquiry", "transactionType": "coverage-eligibility" }, … ],
  "sandboxPersonas": [
    { "memberId": "MBR-COVERED",    "dob": "1975-04-02", "family": "Johansson", "expectedEligibility": "covered" },
    { "memberId": "MBR-NOTCOVERED", "dob": "1980-09-15", "family": "Reyes",     "expectedEligibility": "not-covered" }
  ],
  "docs": "https://github.com/SmartHealthNetwork/shn-sdk/blob/main/docs/SANDBOX.md"
}
```

### Fields

| Field | Meaning |
|---|---|
| `sandbox` | Always `true` for the preview substrate. |
| `syntheticDataOnly` | Always `true` — **synthetic personas only, never production PHI** (FR-39). |
| `wireProtocolVersion` | The wire-protocol version the sandbox speaks (see below). A consumer rejects a descriptor whose version it does not support **before** running any leg. |
| `igVersions` | Pinned IG versions the substrate validates against (server-side gate). |
| `endpoints.{hub,authz,registrar,patientAccess}` | The live participant-facing base URLs. `hub` is where you originate a leg (`POST /route`); `authz` mints/serves tokens; `registrar` serves the holder feed; `patientAccess` is the FHIR/Patient-Access surface (`GET /metadata`). |
| `authzPublicKeyURL` | Where to fetch the Authorization Framework Ed25519 verifying key (`{authz}/pubkey`). |
| `hubTransportKeyURL` | Where to fetch the Hub's Ed25519 transport verifying key (`{hub}/transport-key` → `{"pubkey": "<base64 ed25519>"}`). Responders use this key to verify `X-Hub-Assertion` on every inbound forward (§6.2a). |
| `sandboxResponders[]` | The responders you may exchange with (`role` + `holderId`). For UC-01 there is one: the payer. |
| `operations[]` | The advertised `(frame, operation, transactionType)` triples the substrate authorizes. |
| `sandboxPersonas[]` | The seeded synthetic patients and their `expectedEligibility` (`"covered"` \| `"not-covered"`) — the inputs + expected outcomes `shn doctor` asserts. |
| `docs` | Getting-started URL (`docs/SANDBOX.md`). |

**No keys are embedded** in the descriptor (so it cannot drift from the live keys).
You resolve the two keys a UC-01 leg needs from the live endpoints:

- **Payer encryption key** (the X25519 `encPub` you seal the envelope to): from the
  registrar feed — `GET {registrar}/holders`, the row whose `id` matches the responder's
  `holderId`, field `encPub` (std-base64, 32 bytes). See §2.2 for the feed shape.
- **Authorization Framework verifying key** (the Ed25519 `authzPub` you check the bound
  response token against): from `GET {authzPublicKeyURL}` → `{"pubkey": "<base64 ed25519>"}`.
  Same value as `authzPub` in `manifest.json` (§4.5).

### `wireProtocolVersion`

The version string gates wire compatibility. **`1.1.0`** is the current value: it is the
**payloadHash wire** — the per-leg authorize token binds `payloadHash = sha256hex(ciphertext)`,
verified STRICTLY in `VerifyBound` (AI-2; see §4.4). A consumer whose SDK speaks a
different version should refuse to proceed and prompt an upgrade rather than send a leg
the sandbox may reject (`shn doctor` exits code `20` here).

---

## 2. Identity and registration

### 2.1 Holder record

Every substrate participant is a **holder**. A holder record
carries exactly these fields:

```go
type Holder struct {
    ID      string            // stable participant identifier, e.g. "acme-payer"
    Role    string            // "provider" | "payer" | "facility" | "phg"
    EncPub  *[32]byte         // X25519 public key — envelope encryption target
    SignPub ed25519.PublicKey  // Ed25519 public key — assertion verification
    BaseURL string            // where the Hub delivers inbound envelopes
}
```

A holder originates envelopes under its `ID` and `SignPub` and receives envelopes
at `BaseURL + /substrate/inbound` (see §6).

### 2.2 Static admission (current)

Admission is static. The operator produces a
**provisioning bundle**: a public `manifest.json` + per-process secret key files.

`manifest.json` shape (all keys base64-standard-encoded):

```json
{
  "holders": [
    {
      "id": "acme-payer",
      "role": "payer",
      "encPub": "<base64 X25519 32-byte public key>",
      "signPub": "<base64 Ed25519 32-byte public key>",
      "baseURL": "https://acme-payer.example.com"
    }
  ],
  "authzPub": "<base64 Ed25519 public key — Authorization Framework signer>",
  "auditSignPub": "<base64 Ed25519 public key — Audit Plane signer>",
  "auditCheckpointPub": "<base64 Ed25519 public key — Audit Plane checkpoint head-attestation signer>",
  "adminPub": "<base64 Ed25519 public key — Trust admin (gates POST /register, /revoke)>",
  "registrarPub": "<base64 Ed25519 public key — Registrar lifecycle-audit signer>"
}
```

`manifest.json` is **public**. It is the substrate's trust root for the session:
every participant reads it at startup to populate their registry and to learn the
Authorization Framework's verifying key (`authzPub`).

The secrets directory (`secrets/`) holds private key files, mode `0600`:

| File | Contents |
|---|---|
| `<holderID>.enc` | Base64 X25519 private key (32 bytes raw) |
| `<holderID>.sign` | Base64 Ed25519 private key (64 bytes raw) |
| `authz.sign` | Authorization Framework Ed25519 private key |
| `audit.sign` | Audit Plane Ed25519 private key (signs audit records) |
| `audit-checkpoint.sign` | Audit Plane Ed25519 private key (signs head checkpoints; head-attestation only, cannot forge record content — see §2.5.1) |
| `admin.sign` | Trust admin Ed25519 private key (held by the operator/console; gates registration + revoke) |
| `registrar.sign` | Registrar Ed25519 private key (signs holder lifecycle audit records; held by the registrar) |

These are **never** distributed beyond the process that needs them.

### 2.3 Dynamic registration (FR-38)

Dynamic registration is delivered. New holders can be admitted at runtime without
a Hub or Authorization Framework restart.

**Trust-operated Registrar** exposes two
endpoints:

```
POST   /register        — Trust-admin-gated holder admission
GET    /holders         — dynamic holder feed polled by Hub + Authorization Framework
POST   /revoke          — Trust-admin-gated holder revocation (§2.4)
DELETE /register/{id}   — holder-initiated clean exit / deregistration (§2.4)
PUT    /register/{id}   — holder-initiated key-rotation (§2.4)
```

**`POST /register`** — admit a new holder.

Header: `X-Holder-Assertion: base64(json(assertion))` — a standard holder assertion
(§3) signed by the **Trust admin key** (`adminPub` in `manifest.json`), with
`audience` set to `"registrar"`.

Body (JSON, all keys base64-standard-encoded):

```json
{
  "id":      "external-payer",
  "role":    "payer",
  "encPub":  "<base64 X25519 32-byte public key>",
  "signPub": "<base64 Ed25519 32-byte public key>",
  "baseURL": "https://external-payer.example.com",
  "pop":     "<base64 Ed25519 signature — registration proof-of-possession>"
}
```

| Field | Notes |
|---|---|
| `id` | Stable participant identifier; must be unique. Must not contain ASCII control characters (< 0x20) |
| `role` | `"provider"` \| `"payer"` \| `"facility"` \| `"phg"` |
| `encPub` | Base64 X25519 public key (32 bytes raw) — envelope encryption target |
| `signPub` | Base64 Ed25519 public key (32 bytes raw) — assertion verification |
| `baseURL` | Where the Hub delivers inbound envelopes. Must be a publicly resolvable https URL — no userinfo, no ASCII control characters (< 0x20) — and must not redirect at /substrate/inbound (the Hub refuses redirects). Originator-only clients are never dialed but the URL must still validate. |
| `pop` | Base64 Ed25519 **proof-of-possession** signature over the canonical registration payload, made with the private key for the `signPub` being registered (see below) |

**Proof-of-possession (`pop`).** In addition to the Trust admin gate, the
participant must prove control of the `signPub` private key it is registering. This
binds `id` ↔ key from moment zero, independently of who holds the admin credential.
The PoP is an Ed25519 signature over a **canonical signing payload**: the five
fields below, in this exact order, joined by single `\n` (newline, `0x0A`) bytes:

```
id
role
encPub
signPub
baseURL
```

i.e. the byte string `id + "\n" + role + "\n" + encPub + "\n" + signPub + "\n" + baseURL`,
where each field is its exact wire value (the base64 strings for `encPub`/`signPub`,
not the decoded bytes). Sign that byte string with the **`signPub` private key**, then
base64-standard-encode the 64-byte signature into `pop`. Because `role` is
enum-constrained, `encPub`/`signPub` are validated base64, and `id`/`baseURL` are
rejected if they contain control characters, no two distinct registration bodies can
share one signing payload.

Rejection cases:

| Condition | Status |
|---|---|
| Missing or invalid Trust admin credential | 401 (`"missing or invalid Trust admin credential"`) |
| `id` or `baseURL` absent, or `role` not in the allowed set | 400 |
| `id` or `baseURL` contains a control character | 400 (`"id/baseURL must not contain control characters"`) |
| `baseURL` is not an acceptable public https URL (scheme, userinfo, private/unresolvable address) | 400 — body `{"error":"invalid baseURL: <reason>"}`; the `"invalid baseURL: "` prefix is the stable contract, the reason tail may evolve |
| `encPub` / `signPub` malformed (not valid base64 / wrong length) | 400 (`"malformed encPub/signPub"`) |
| `pop` absent or malformed (not valid base64 / empty) | 400 (`"missing registration proof-of-possession"`) |
| `pop` does not verify against the submitted `signPub` | 401 (`"registration proof-of-possession failed"`) |
| `id` is a founding holder from the manifest | 409 (`"founding holder, manifest-authoritative"`) |
| `id` already dynamically registered | 409 (`"id already registered"`) |

A 201 response means the holder is registered and will be visible to Hub and
Authorization Framework on their next poll.

**`GET /holders`** — returns all dynamically registered holders as a JSON array of
the same shape as the `POST /register` body. The Hub and Authorization Framework
poll this endpoint on a ~3-second interval to pick up new admissions.

**Registry merge rule:** `registry = manifest base ∪ dynamic`. Dynamic holders are
appended; the **manifest base is immutable at runtime** (founding holders are never
overwritten or deleted by the poller). Dynamic holders can be **removed** via the
lifecycle endpoints in §2.4; on the next poll the Hub and Authorization Framework
converge to the registrar feed and drop the holder. A dynamic holder can also
**rotate its keys in place** via `PUT /register/{id}` (§2.4); the new keys converge
to the registry on the same poll cycle.

**What a partner does today:**

1. Obtain the Trust admin credential (out-of-band from the Trust operator — today a
   shared `adminPub`/signing key from the provisioning bundle; OAuth 2.1-style self-serve
   client registration is delivered via the Accounts service — see §2.3a below).
2. Generate X25519 and Ed25519 key pairs for your holder.
3. `POST /register` with your public keys, role, and gateway `baseURL` (public https — see the baseURL requirements above).
4. Within ~3 seconds (one poll cycle), Hub and Authorization Framework will route to
   your `baseURL` and accept assertions signed by your `signPub`.

The `adminPub` field is present in every provisioning bundle;
regenerate the bundle if it is absent.

### 2.3a Self-serve registration via the Accounts service (sandbox)

Sandbox developers can register clients without obtaining a Trust admin credential
out of band. The **Accounts service** (`accounts.shn-preview.org`) is a Cognito-gated
developer-onboarding control plane that wraps `POST /register` on your behalf.

Use the `shn` CLI with the `--accounts` flag:

```sh
shn login --accounts https://accounts.shn-preview.org   # browser Cognito login, token cached
shn register --accounts https://accounts.shn-preview.org \
  --role provider --name acme --base-url https://acme.example -out ./keys
shn clients --accounts https://accounts.shn-preview.org  # list your clients
shn revoke <id> --accounts https://accounts.shn-preview.org
```

The `--accounts` path is the **Cognito-gated self-serve** path for sandbox
onboarding. The admin-gated direct `POST /register` (§2.3) remains the canonical
operator/Trust path and the authoritative wire spec for all participants — the
Accounts service is an additive convenience layer over it.

> **Note:** The full self-serve round-trip (login → register → list →
> revoke) is interactive (browser Cognito login) and is verified operator-side
> after each deploy.

### 2.4 Credential lifecycle — revoke and deregister

A dynamically-registered holder can be removed two ways: the Trust operator can
**revoke** it, or the holder can **deregister itself** for a clean exit. Both are
restricted to dynamic holders — a founding holder from the manifest cannot be
removed at runtime (409).

**`POST /revoke`** — Trust-operated revocation.

Header: `X-Holder-Assertion: base64(json(assertion))` — a holder assertion (§3)
signed by the **Trust admin key** (`adminPub`), `audience` = `"registrar"`. Same
gate as `POST /register`.

Body (JSON):

```json
{ "id": "external-payer" }
```

Rejection cases:

| Condition | Status |
|---|---|
| Missing or invalid Trust admin credential | 401 (`"missing or invalid Trust admin credential"`) |
| `id` absent | 400 (`"id required"`) |
| `id` is a founding holder from the manifest | 409 (`"founding holder, manifest-authoritative"`) |
| No such (dynamic) holder | 404 (`"no such holder"`) |
| Success | 204 (No Content) |

**`DELETE /register/{id}`** — holder-initiated clean exit (RFC 7592 client
configuration DELETE).

Authenticated by the holder's **own** key, not the admin key: the
`X-Holder-Assertion` header must be a holder assertion (§3) signed by the
registered `signPub` of `{id}`, with `holderId == {id}` and `audience ==
"registrar"`. A holder may only deregister itself.

```
DELETE /register/external-payer
X-Holder-Assertion: base64(json(assertion))   // holderId="external-payer", audience="registrar", signed by external-payer's signPub
```

Rejection cases:

| Condition | Status |
|---|---|
| `{id}` is a founding holder from the manifest | 409 (`"founding holder, manifest-authoritative"`) |
| No such (dynamic) holder | 404 (`"no such holder"`) |
| Assertion missing, not signed by `{id}`'s `signPub`, or `holderId != {id}` | 403 (`"a holder may only deregister itself"`) |
| Success | 204 (No Content) |

Note: `{id}` existence is checked before authentication, so an unknown id returns
404 rather than 403. This is intentional and matches RFC 7592 semantics — the
`/holders` feed is already public, so the 404 leaks nothing new.

**`PUT /register/{id}`** — key-rotation (RFC 7592 client-configuration update).

A dynamically-registered holder can re-key itself in place, rotating **both** its
`encPub` and `signPub` without a deregister/re-register cycle. Like deregister, this
is authenticated by the holder's **current** key and is restricted to dynamic holders.

Auth: the `X-Holder-Assertion` header must be a holder assertion (§3) signed by the
holder's **current** registered `signPub`, with `holderId == {id}` and `audience ==
"registrar"`. The `jti` is consumed one-time-use, like the other holder-self
operations (§3.4). The current key authenticates the request; the new key proves
itself via the body PoP below.

Body: a full holder record (the same `holderDTO` shape as `POST /register`, §2.3),
carrying the **new** `encPub`/`signPub` and a fresh `pop`:

```
PUT /register/external-payer
X-Holder-Assertion: base64(json(assertion))   // holderId="external-payer", audience="registrar", signed by external-payer's CURRENT signPub
```

```json
{
  "id":      "external-payer",
  "role":    "payer",
  "encPub":  "<base64 X25519 32-byte public key — NEW>",
  "signPub": "<base64 Ed25519 32-byte public key — NEW>",
  "baseURL": "https://external-payer.example.com",
  "pop":     "<base64 Ed25519 signature over the canonical payload, by the NEW signPub private key>"
}
```

The `pop` is the same canonical proof-of-possession as registration (§2.3): an
Ed25519 signature over `id + "\n" + role + "\n" + encPub + "\n" + signPub + "\n" +
baseURL`, but here signed with the **new** `signPub`'s private key (proving the
rotator controls the key it is rotating to). **Re-key only:** `role` and `baseURL`
in the body MUST equal the existing record — rotation rotates the two keys and
nothing else.

Rejection cases (checks are ordered):

| Condition | Status |
|---|---|
| `{id}` is a founding holder from the manifest | 409 (`"founding holder, manifest-authoritative"`) |
| No such (dynamic) holder | 404 (`"no such holder"`) |
| Assertion missing, not signed by `{id}`'s **current** `signPub`, or `holderId != {id}` | 403 (`"a holder may only rotate itself"`) |
| Body is not valid JSON | 400 (`"bad request body"`) |
| `id` in body does not match `{id}` in the path | 400 (`"id in body must match path"`) |
| `id` or `baseURL` contains a control character | 400 (`"id/baseURL must not contain control characters"`) |
| `role` or `baseURL` differs from the existing record | 400 (`"rotation changes keys only; role/baseURL must match"`) |
| New `encPub` / `signPub` malformed (not valid base64 / wrong length) | 400 (`"malformed encPub/signPub"`) |
| `pop` absent or malformed (not valid base64 / empty) | 400 (`"missing registration proof-of-possession"`) |
| `pop` does not verify against the **new** `signPub` | 401 (`"registration proof-of-possession failed"`) |
| Registrar store read/write unavailable (list or update) | 502 (`"store error"`) |
| Lifecycle audit append failed (keys rolled back, fail-closed) | 502 (`"lifecycle audit failed"`) |
| Success | 200 (OK) |

**Operational note — propagation window.** A 200 only updates the registrar feed.
The new keys propagate to the Hub and the Authorization Framework on their next
converge poll (~one ~3-second interval, §2.3). Rotate during a quiet moment, and on
the holder side:

- **Keep the old `encPub` private key loaded for ~one poll interval** so you can
  still decrypt envelopes a counterparty addressed to the now-stale `encPub` before
  it observed the new one. The payload-blind substrate never holds your enc private
  key, so this overlap is entirely a holder-operational responsibility.
- **Tolerate/retry transient assertion-auth blips** until the new `signPub`
  propagates — an in-flight leg may be briefly rejected mid-convergence.

**Propagation.** Revocation and deregistration are **not** push events. The
removed holder disappears from `GET /holders`, and on their next converge-to-feed
poll (~3-second cycle) the Hub and Authorization Framework drop it from their
registries. After convergence, the holder's next leg fails authority: the Hub
cannot resolve its `baseURL`, and the Authorization Framework will not mint or
verify tokens for an unknown holder. There is no standing token to revoke
separately (§4 tokens are per-leg, per-operation — see the concept mapping in
§3.5).

### 2.5 Auditing

Every lifecycle transition — `registered`, `revoked`, `deregistered`, `rotated` — is signed
by the registrar with its own signing key (public key = the manifest `registrarPub`,
which the Audit Plane trusts as a signer; distinct from the Audit Plane's own
`auditSignPub`) and appended to the canonical audit chain (AI-6). A transition
that cannot be recorded does **not** stand: a failed audit append rolls the change
back and returns 502 (fail-closed). Lifecycle audit records carry no patient
subject (AI-5) — they are fabric events.

#### 2.5.1 Signed checkpoints (tail-truncation detection) — 2026-06-10

The hash-chain + per-record signature prove no record was reordered or
content-tampered, but say nothing about *how many* records there should be — a
store or relay could drop the tail (or roll back to an older state) and the chain
that remains still verifies. The signed **checkpoint** closes that gap by pinning
the head.

- **`audit-checkpoint` trust key.** A dedicated head-attestation key, public half
  published in the manifest as `auditCheckpointPub` (distinct from the record
  signer's `auditSignPub`). It is held **only** by the Audit Plane. It attests the
  chain *head*, not record content — the audit task still cannot forge a record
  (records are Hub-signed); it gains only the power to attest "the chain is this
  long, ending here."
- **`Checkpoint` artifact** `(seq, headHash, generation, timestamp, signatures[])` — a
  signed high-water mark over the chain head, tagged with the store's **chain-generation
  id** (an opaque generation id — a UUID on the Postgres path — minted once per chain
  lifetime in `audit_chain_meta`, rotated only by a
  reset; `omitempty` so legacy checkpoints with no generation still verify byte-identically).
  Its `signatures` slot is the **same additive
  FROST/external-witness seam as `Record.signatures`**: `Signatures[0]` is today's
  mandatory single `audit-checkpoint` signature, and the slot is **excluded from the
  signed content**, so cosigners may append entries later without changing what
  `Signatures[0]` attests.
- **`/verify` head assertion — reset-aware since 2026-07-02 (DEF-G14).** Beyond the
  chain-integrity and per-record signature checks, `/verify` asserts the chain head
  matches the latest persisted signed checkpoint **within the same chain generation**
  (detecting tail-truncation / rollback, including infrastructure-level backup-restore)
  and reports `generation`, `anchoredSeq`, `headSeq`, and `anchor lag`
  (`headSeq − anchoredSeq`, `0` when fully anchored) on **both** the green and red
  response. `/verify` goes **red** (`ok:false`, HTTP 500) only if a **same-generation**
  head fails to match the checkpoint, the checkpoint's signature fails to verify, **or
  the anchor is unreachable** — the strongest check is never skipped. A
  **validly-signed** checkpoint whose generation differs from the store's own
  re-anchors synchronously instead of going red — this is what lets a legitimate chain
  reset (e.g. a demo/smoke reset) re-anchor without manual S3 surgery; a signature
  failure is always red, never treated as a reset.
- **Anchor.** The durable home for the checkpoint. In cloud it is a **versioned S3
  object** (`AUDIT_CHECKPOINT_S3_BUCKET` / `AUDIT_CHECKPOINT_S3_KEY` env; key defaults
  to `audit/checkpoint.json`); local/dev uses an **in-memory anchor** (restart-gap
  coverage is cloud-only, since stale restores are a cloud phenomenon). The S3
  **version history is the audit-of-the-audit / external-witness seam**, and — since
  2026-07-02 — also the forensic trail for chain-generation transitions: each
  generation's final anchor deliberately lingers (never deleted) so an
  out-of-band-DDL-as-reset can be correlated after the fact.
- **Stated residual.** Records newer than the last persisted checkpoint are
  truncatable-undetected within the flush/lag window — inherent to checkpointing; the
  window is the tuning knob (and is reported as `anchor lag`). **Threat boundary:**
  this detects truncation by the **store or relay**. Truncation by a **fully
  compromised Audit Plane** (which holds the `audit-checkpoint` key and so could
  re-sign a shorter chain) is **DEF-5's** job — the checkpoint is the seam DEF-5
  plugs into (FROST multi-party / external witness over the same `signatures[]` slot).
  A DDL-capable truncation that masquerades as a legitimate reset by minting a fresh
  chain generation — an **unauthenticated generation declaration** — is **DEF-G14**;
  mitigated by the versioned per-generation anchors above plus a live
  `/verify` probe (steady-state and post-reset) run in the deploy pipeline.

---

## 3. Holder assertion

Before calling either the Authorization Framework or the Hub, a holder must prove
its identity by presenting a signed **holder assertion**. This is transport
authentication — distinct from per-operation authority (§4).

### 3.1 Assertion fields

```go
type Assertion struct {
    HolderID string    `json:"holderId"`
    Audience string    `json:"audience"`
    IssuedAt time.Time `json:"issuedAt"`
    Expiry   time.Time `json:"expiry"`
    JTI      string    `json:"jti"`
    Sig      []byte    `json:"sig"`
}
```

`Sig` is an Ed25519 signature over the JSON encoding of the struct with `sig` set
to `null`. Do **not** include the signature field in the signing payload. The
`jti` **is** part of the signing payload (it is set before signing — see §3.5).

**`jti` — unique per-assertion id (REQUIRED).** Every assertion carries a `jti`: a
unique identifier, stamped before signing so the signature covers it. The substrate
helper (`holderauth.Issue`) generates a random 16-byte `jti` (base64url, unpadded);
an Option-B participant minting assertions by hand must do the same. An assertion
**without** a `jti` is rejected (`"holderauth: missing jti"`). This is the SMART
`private_key_jwt` `jti` claim.

Verifier-enforced bounds:

| Bound | Value |
|---|---|
| Maximum assertion lifetime (`Expiry − IssuedAt`) | 1 hour |
| Maximum future clock skew on `issuedAt` | 5 minutes |

**Time-source prerequisite:** holders and the Hub/Authorization Framework must
share a disciplined time source (NTP). The skew window absorbs only small drift;
an unsynchronised clock will cause assertion rejection.

### 3.2 Wire encoding

Encode the assertion as JSON, then base64-standard-encode it:

```
X-Holder-Assertion: base64(json(assertion))
```

### 3.3 Audience values

| Target | `audience` value |
|---|---|
| Authorization Framework (`POST /authorize`) | `"authz"` |
| Hub (`POST /route`) | `"hub"` |
| Registrar (`POST /register`, `POST /revoke`) | `"registrar"` (Trust admin key) |
| Registrar (`DELETE /register/{id}`, `PUT /register/{id}`) | `"registrar"` (the holder's **own** current `signPub`) |

The verifier rejects an assertion whose `audience` does not match what it expects.

### 3.4 One-time-use

Verifiers that **consume** an assertion to take a privileged action enforce
one-time-use on its `jti`: a `jti` already seen within the assertion window
(`MaxAssertionTTL`, 1 hour) is rejected as a replay. So a captured assertion cannot
be replayed to drive a second action within its lifetime. The consuming verifiers
are:

- the **Authorization Framework** `POST /authorize` (an assertion mints one token,
  not many), and
- the **Registrar** `POST /register`, `POST /revoke`, `DELETE /register/{id}`,
  `PUT /register/{id}` (each gated action consumes its assertion once).

The Hub's `POST /route` verifies the assertion for transport identity but does not
single-use it here; replay protection on routing is the per-correlation guard (§5.1).
Generate a fresh `jti` per assertion regardless of target.

### 3.5 Construction sequence

1. Set `holderId` to your registered `Holder.ID`.
2. Set `audience` to the appropriate value for the target (see §3.3).
3. Set `issuedAt` to current UTC time; `expiry` to `issuedAt + TTL` (≤ 1 hour).
4. Set `jti` to a fresh unique value (e.g. 16 random bytes, base64url-unpadded).
5. Marshal the assertion with `sig: null` (and `jti` populated), sign with your
   Ed25519 private key, set `sig` to the resulting 64-byte signature.
6. Marshal the complete assertion (including `jti` and `sig`) to JSON.
7. Base64-standard-encode and send as `X-Holder-Assertion`.

Example assertion (pre-base64):

```json
{
  "holderId": "my-provider",
  "audience": "authz",
  "issuedAt": "2026-06-06T13:55:00Z",
  "expiry":   "2026-06-06T14:55:00Z",
  "jti":      "Yk3pQ1f8r2N5vXzA7bQwLg",
  "sig":      "<base64 Ed25519 signature>"
}
```

---

## 3a. SHN ↔ OAuth/SMART concept mapping

The substrate's credentialing contract maps onto familiar OAuth 2.0 / SMART
building blocks. The shapes are recognizable; the trust posture is deliberately
narrower (no standing bearer token).

| SHN substrate construct | OAuth / SMART analogue |
|---|---|
| Holder registration (`POST /register`, §2.3) | OAuth 2.0 Dynamic Client Registration (RFC 7591) — the registration body is the client-metadata shape |
| Registration proof-of-possession (`pop`, §2.3) | A software-statement-style self-attestation of key control — but a bare Ed25519 PoP over the canonical payload, **not** a UDAP X.509 software statement |
| Holder assertion (§3) | SMART asymmetric ("private_key_jwt") client authentication (RFC 7521 / RFC 7523): a short-lived, key-signed assertion with `aud`, `exp`, and a one-time-use `jti` |
| Per-operation `authz` token (§4) | **NOT** an OAuth bearer access token. It is a **sender-constrained, per-operation** grant — a strict **superset** of SMART system scopes: bound to one operation, one frame, one correlation, one subject PCI, and one holder (OWD-6 / AI-11: no standing blanket capability that can be lifted or replayed across operations) |
| Lifecycle — deregister (`DELETE /register/{id}`, §2.4) | RFC 7592 client-configuration DELETE |
| Lifecycle — key-rotation (`PUT /register/{id}`, §2.4) | RFC 7592 client-configuration UPDATE (re-key) |
| Lifecycle — revoke (`POST /revoke`, §2.4) | Trust-operated client revocation (no self-serve OAuth token revocation; see §9) |

Because authority is minted per leg and never standing, there is no long-lived
bearer credential to steal, revoke, or introspect — removing the holder's
registration (§2.4) stops all future per-operation authority at the source.

---

## 4. Authority flow

Every substrate operation requires a scope-bound **authorization token** minted by
the Authorization Framework. Tokens are per-leg: one token authorizes one envelope
in one direction for one correlation.

### 4.1 Request — `POST {authz}/authorize`

**Header:** `X-Holder-Assertion: <assertion for audience "authz">`

**Body (JSON):**

```json
{
  "frame":         "provider-tpo",
  "operation":     "eligibility-inquiry",
  "subjectPCI":    "pci:a1b2c3d4e5f6...",
  "correlationId": "8f3d...",
  "custodian":     "",
  "payloadHash":   "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
}
```

| Field | Required | Notes |
|---|---|---|
| `frame` | Yes | Authority frame; see §4.3 |
| `operation` | Yes | Operation string; see §4.3 |
| `subjectPCI` | Yes | Must start with `"pci:"` — the Trust-issued patient identifier (AI-5) |
| `correlationId` | Yes | Must be non-empty; binds the minted token to one leg |
| `custodian` | For `federated-query-submit` only | The facility holder ID; used to resolve patient consent at the Global Person Consent service |
| `payloadHash` | For every **envelope** op | `sha256hex` (64 lowercase hex) of the envelope **ciphertext**. **Seal the payload FIRST, then authorize** against the ciphertext (seal-then-authorize) so the minted token binds THIS payload (AI-2). Absent for the one non-envelope op, `patient-access-read` (a REST bearer read) |

**Rejection cases:**

- `subjectPCI` absent or not prefixed `"pci:"` → 400
- `correlationId` absent → 400
- `payloadHash` absent/malformed on an envelope op, or PRESENT on `patient-access-read` → policy denies → 403
- Policy denies (wrong role, no consent for `federated-query-submit`) → 403
  `{"error":"forbidden"}`

### 4.2 Response (200 OK)

```json
{
  "token": {
    "operation":     "eligibility-inquiry",
    "scope":         "eligibility-scope",
    "subject":       "pci:a1b2c3d4e5f6...",
    "frame":         "provider-tpo",
    "correlationId": "8f3d...",
    "holder":        "my-provider",
    "consentRef":    "",
    "payloadHash":   "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "expiry":        "2026-06-06T14:00:00Z",
    "signature":     "<base64 Ed25519 signature>"
  }
}
```

The `token` object is the `Token` struct:

```go
type Token struct {
    Operation     string    `json:"operation"`
    Scope         string    `json:"scope"`
    Subject       string    `json:"subject"`
    Frame         string    `json:"frame"`
    CorrelationID string    `json:"correlationId"`
    Holder        string    `json:"holder"`
    ConsentRef    string    `json:"consentRef,omitempty"`
    PayloadHash   string    `json:"payloadHash"` // sha256hex(ciphertext); empty only for patient-access-read
    Expiry        time.Time `json:"expiry"`
    Signature     []byte    `json:"signature"`
}
```

`Holder` is stamped by the Authorization Framework from the verified assertion —
never a client-supplied field. The Hub asserts `token.Holder == envelope.Sender`
so a holder cannot route an envelope using another holder's token (H1).

### 4.3 Frame and operation reference

The Hub validates that the token's `operation` matches the expected value for the
envelope's `transactionType`. Invalid combinations are rejected before any audit
record is written.

| `transactionType` (envelope) | Request `operation` | Response `operation` |
|---|---|---|
| `coverage-eligibility` | `eligibility-inquiry` | `eligibility-response` |
| `crd-order-select` | `crd-order-select` | `crd-cards` |
| `dtr-questionnaire-fetch` | `dtr-questionnaire-fetch` | `dtr-questionnaire` |
| `pas-claim` | `pas-submit` | `pas-response` |
| `pas-claim-update` | `pas-update-submit` | `pas-update-response` |
| `federated-query` | `federated-query-submit` | `federated-query-response` |
| `patient-dtr` | `patient-dtr-request` | `patient-dtr-response` |

Standard authority frames:

| Exchange | Request frame | Response frame |
|---|---|---|
| Provider → payer (eligibility, CRD, DTR, PAS) | `provider-tpo` | `payer-coverage` |
| Provider → facility (federated query) | `provider-tpo` | `facility-disclosure` |

### 4.4 Per-leg token verification — `VerifyBound`

Every receiver (and the Hub on behalf of all parties) verifies a token using
`VerifyBound`:

```go
func VerifyBound(
    t Token,
    pub ed25519.PublicKey,  // Authorization Framework verifying key (from manifest.json "authzPub")
    now time.Time,
    wantFrame         string,
    wantOp            string,
    wantCorrelationID string,
    wantHolder        string,
    wantSubject       string,
    wantPayloadHash   string,  // sha256hex(the ciphertext you received) — STRICT (AI-2)
) error
```

Pass an empty string to skip a particular binding check — **except
`wantPayloadHash`, which is STRICT**: every envelope leg binds a payload, so the
receiver recomputes `sha256hex` over the ciphertext it received and asserts it
equals `token.payloadHash`; an empty want or an empty token hash is REJECTED. This
makes the substrate payload-blind AND payload-AUTHENTICATED (AI-2): a payload
swapped in flight is cryptographically detected at the authorization check. On the
**response leg**, also pass the **request** token's `subject` as `wantSubject`. A
validly-signed token cannot be lifted into a different envelope, operation,
correlation, patient, **or payload**.

The ONE non-envelope op, `patient-access-read` (a REST bearer read that carries no
sealed payload), is verified with `VerifyBoundNoPayload` instead — it asserts the
token carries NO `payloadHash`, so an envelope-bound token can never be replayed
onto the bearer-read path.

### 4.5 Authorization Framework public key endpoint

```
GET {authz}/pubkey
```

Returns `{"pubkey": "<base64 Ed25519 public key>"}`. This is the same value as
`authzPub` in `manifest.json`. Load it from the manifest at startup; call this
endpoint only if you need to refresh it without a manifest reload.

---

## 5. Envelope

Each substrate hop is one **envelope**: cleartext routing
metadata plus an opaque, encrypted payload.

### 5.1 Metadata fields

```go
type Metadata struct {
    Sender          string `json:"sender"`
    Recipient       string `json:"recipient"`
    TransactionType string `json:"transactionType"`
    AuthorityFrame  string `json:"authorityFrame"`
    ConsentRef      string `json:"consentRef,omitempty"`
    AuthzToken      string `json:"authzToken"`
    Timestamp       string `json:"timestamp"`
    CorrelationID   string `json:"correlationId"`
}
```

| Field | Notes |
|---|---|
| `sender` | Your holder ID |
| `recipient` | The counterpart holder ID |
| `transactionType` | See §4.3 |
| `authorityFrame` | Must be non-empty; must match the token's `frame` |
| `consentRef` | Non-empty only for `federated-query-submit`; copied from the minted token's `consentRef` |
| `authzToken` | JSON-marshalled `authz.Token` as a **string** (not a nested object) |
| `timestamp` | RFC 3339 UTC — must be within ±5 minutes of the Hub clock |
| `correlationId` | Must be non-empty; must match the token's `correlationId` |

**Patient identifiers never appear in Metadata** (AI-5). The subject PCI lives
only inside the token (and therefore only in the sealed ciphertext from the
sender's perspective; the Hub reads `authzToken` from the metadata as a string but
verifies `token.Subject` via `VerifyBound`).

The Hub enforces:

- `authorityFrame` must be non-empty (400 otherwise).
- `correlationId` must be non-empty (400 otherwise).
- `timestamp` must parse as RFC 3339 and be within ±5 minutes of Hub clock (400 otherwise).
- The correlation ID must not have been seen in the last 2 hours (409 replay rejection).

### 5.2 Payload encryption

The payload is encrypted with an **X25519 anonymous sealed box** to the recipient's
`EncPub` key (from `manifest.json`). The sender needs only the recipient's public
key; the Hub never has any decryption key.

```go
// Seal: encrypt payload to recipientPub.
func Seal(meta Metadata, payload []byte, recipientPub *[32]byte) (Envelope, error)

// Open: decrypt using the recipient's own key pair.
func Open(e Envelope, recipientPub, recipientPriv *[32]byte) ([]byte, error)
```

`Seal` uses `golang.org/x/crypto/nacl/box.SealAnonymous` with `crypto/rand`.
`Open` uses `box.OpenAnonymous`. A caller lacking the private key cannot open the
box; this is the structural basis of payload-blind routing (AI-2).

### 5.3 Wire encoding

An `Envelope` serialises to JSON. The `ciphertext` field is a `[]byte` and is
base64-standard-encoded by `encoding/json`. The full wire structure is:

```json
{
  "metadata": {
    "sender":          "my-provider",
    "recipient":       "acme-payer",
    "transactionType": "coverage-eligibility",
    "authorityFrame":  "provider-tpo",
    "consentRef":      "",
    "authzToken":      "{\"operation\":\"eligibility-inquiry\",...}",
    "timestamp":       "2026-06-06T13:55:00Z",
    "correlationId":   "8f3d..."
  },
  "ciphertext": "<base64>"
}
```

Maximum body size accepted by Hub and Gateway endpoints: **8 MiB**.

---

## 6. Surfaces

### 6.1 Hub — originate a leg

```
POST {hub}/route
Content-Type: application/json
X-Holder-Assertion: <base64(json(assertion))>

<JSON-encoded Envelope>
```

The Hub:

1. Verifies the assertion (audience `"hub"`, `HolderID` == `envelope.metadata.sender`).
2. Verifies the authz token (`VerifyBound` with request `operation` and `frame`).
3. Rejects stale/future timestamps and replayed correlation IDs.
4. Appends a `"routed"` audit record to the Audit Plane (mandatory; 502 on failure).
5. Forwards the **same envelope bytes** to `recipient.BaseURL + /substrate/inbound`.
6. Verifies the response envelope's authz token (response `operation`, same
   `correlationId`, `sender == original recipient`, `subject == request token subject`).
7. Appends an `"answered"` audit record.
8. Returns the verified response envelope as the HTTP response body (200 OK).

**The Hub returns the response envelope synchronously** in the HTTP response body.
The originator reads the response from the `POST /route` reply — there is no
polling or callback.

On error the Hub returns a JSON error body `{"error": "<message>"}` with an
appropriate 4xx/5xx status.

### 6.2 Holder inbound surface — receive a routed leg

A holder that acts as a **responder** (payer, facility, PHG) must serve:

```
POST {holder}/substrate/inbound
Content-Type: application/json

<JSON-encoded Envelope>
```

The endpoint must:

1. Decode the envelope.
2. Verify the authz token using `VerifyBound` with the expected response `frame`,
   the response `operation` (see §4.3), and the known `correlationId` for this
   exchange.
3. Decrypt the ciphertext with the holder's own `EncPub`/`EncPriv` key pair.
4. Process the FHIR payload (validate, apply business logic).
5. Build the response payload, seal it to the **original sender's** `EncPub`.
6. Construct a response `Metadata` with:
   - `sender` set to **your** holder ID
   - `recipient` set to the request envelope's `sender`
   - `transactionType` set to the **same** `transactionType` as the request
   - `authorityFrame` set to the response frame (e.g. `"payer-coverage"`)
   - `authzToken` set to a JSON-encoded token minted for the **response** operation
   - `correlationId` set to the **same** `correlationId` as the request
   - `timestamp` set to current UTC in RFC 3339
7. Return the response envelope as the HTTP response body (200 OK, `Content-Type: application/json`).

The Hub verifies this response envelope before writing the `"answered"` audit
record and returning it to the originator. A mis-constructed response causes the
Hub to return 502 to the originator.

**Originator-only participants** (those that only send, never receive) do not need
to serve this endpoint. A responder/inbound surface is available today via the public SDK (`shnsdk.Responder` — see `SANDBOX.md` §3c); originator-only participants do not need to serve this endpoint.

### 6.2a Inbound transport authentication (`X-Hub-Assertion`)

Every Hub forward to `/substrate/inbound` carries an **`X-Hub-Assertion`** header.
It has the same assertion shape as `X-Holder-Assertion` (§3) but is signed by the
Hub's own transport key — it authenticates the **channel** (the caller is the Hub),
not the envelope authority.

**Header shape:**

```
X-Hub-Assertion: base64(json(assertion))
```

where the assertion JSON is identical to the `Assertion` struct in §3.1, with:

| Field | Value |
|---|---|
| `holderId` | `"hub"` (issuer pin — MUST equal this string) |
| `audience` | Your holder ID (the `recipient` in the envelope `Metadata`) |
| `issuedAt` / `expiry` | UTC timestamps; TTL is 2 minutes |
| `jti` | A single-use, per-forward unique identifier |
| `sig` | Ed25519 signature by the Hub's transport key |

**Verification key.** Fetch from the discovery descriptor's `hubTransportKeyURL`:

```
GET {hubTransportKeyURL}     →     {"pubkey": "<base64 ed25519>"}
```

e.g. `GET https://hub.<apex>/transport-key`. Load this key at startup; do not
re-fetch per request.

**A conformant responder MUST, BEFORE processing the envelope:**

1. Decode and parse the `X-Hub-Assertion` header.
2. Assert `holderId == "hub"` (issuer pin — cheap fast-fail before crypto).
3. Verify the Ed25519 signature against the Hub transport key.
4. Assert `audience` equals **your** holder ID.
5. Assert `expiry` is in the future and `issuedAt` is not in the future beyond
   the 5-minute clock-skew allowance (same bounds as §3.1).
6. Assert the `jti` has not been seen before (one-time-use). Retain seen `jti`
   values for at least the maximum assertion lifetime (1 hour —
   `MaxAssertionTTL`), not merely the 2-minute TTL: the substrate's own guard
   retains for the full hour.

> **Go participants:** `shnsdk.Responder` implements this full verification pipeline —
> steps 1–6 above, plus authz token `VerifyBound`, decryption, adjudication, and the
> sealed-and-authorized response — in one call to `NewResponder` + `Handler()`. See
> `docs/SANDBOX.md` §3c for the quickstart.

On any failure, reject the request with:

```
403 {"error":"missing or invalid hub assertion"}
```

This is the stable error string — do not vary it.

**Rejection table (inbound):**

| Condition | Status | Body |
|---|---|---|
| `X-Hub-Assertion` header absent | 403 | `{"error":"missing or invalid hub assertion"}` |
| Signature invalid or key mismatch | 403 | `{"error":"missing or invalid hub assertion"}` |
| `holderId != "hub"` | 403 | `{"error":"missing or invalid hub assertion"}` |
| `audience` does not match this holder's ID | 403 | `{"error":"missing or invalid hub assertion"}` |
| Assertion expired or future-dated beyond skew | 403 | `{"error":"missing or invalid hub assertion"}` |
| `jti` already seen (replay) | 403 | `{"error":"missing or invalid hub assertion"}` |

**Channel vs. authority.** The transport assertion authenticates the **channel**
(the caller is the Hub); the bound `authzToken` inside the envelope remains the
**authority** check (§4.4, AI-11). Both are required; neither substitutes for the
other. Verify the hub assertion first, then proceed to `VerifyBound`.

**Compatibility note.** A responder built before this header existed simply receives
one extra header — ignoring it is safe for continuity, but verifying it is
**required for conformance** from this protocol version on. The header has no off
state: the Hub sends it on every forward unconditionally.

---

## 7. Worked example — eligibility round-trip

This example mirrors the canonical round-trip + authorize sequence the Smart
Gateway implements.

### Actors and frames

| Actor | Holder ID | Role | Frame |
|---|---|---|---|
| Provider gateway | `my-provider` | `provider` | Request: `provider-tpo` |
| Payer gateway | `acme-payer` | `payer` | Response: `payer-coverage` |

`transactionType`: `coverage-eligibility`
Request operation: `eligibility-inquiry`
Response operation: `eligibility-response`

### Step-by-step

**Step 1 — Resolve the patient PCI (AI-5)**

```
pci = ResolvePCI(memberID, birthDate, familyName)
     → "pci:a1b2c3d4e5f6a1b2c3d4"
```

Note: today the PCI is derived deterministically via `shnsdk.ResolvePCI`
(SHA-256 over `lowercase(memberID|birthDate|familyName)`, first 16 bytes,
`"pci:"` prefix). This is a demo scheme only. External participants must treat the
PCI as an opaque, Trust-assigned identifier and must not re-derive it from demographics.

**Step 2 — Generate a correlation ID**

A cryptographically random 32-hex-character string (16 bytes of `crypto/rand`,
hex-encoded). The correlation ID is minted **before** calling `/authorize` so the
token is bound to the exact envelope it will ride in.

```
correlationId = "8f3d9a1c4b7e2d5f..."
```

**Step 3 — Seal the request payload FIRST (seal-then-authorize)**

Look up `acme-payer` in your local registry copy (loaded from `manifest.json`) to
get its `EncPub`. Seal the payload **before** authorizing so the token can bind
THIS exact ciphertext (AI-2). Build the metadata without `authzToken` for now (you
will set it after the token is minted in Step 4).

```
meta = Metadata{
  sender:          "my-provider",
  recipient:       "acme-payer",
  transactionType: "coverage-eligibility",
  authorityFrame:  "provider-tpo",
  consentRef:      "",             // empty — not a federated query
  authzToken:      "",             // filled in after Step 4
  timestamp:       "2026-06-06T13:55:01Z",
  correlationId:   "8f3d9a1c4b7e2d5f...",
}

envelope    = Seal(meta, cerJSON, acmePayer.EncPub)
payloadHash = sha256hex(envelope.ciphertext)
            → "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
```

Where `cerJSON` is a valid `CoverageEligibilityRequest` FHIR resource (conformant
to US Core; see §8). The `payloadHash` value above is illustrative (it stands in
for the `sha256hex` of the real, randomized sealed-box ciphertext, which differs
every run); the same value reappears in the `/authorize` request and the minted
token below.

**Step 4 — Obtain an authorization token bound to that ciphertext**

```
POST {authz}/authorize
X-Holder-Assertion: base64(json({
  "holderId": "my-provider",
  "audience": "authz",
  "issuedAt": "2026-06-06T13:55:00Z",
  "expiry":   "2026-06-06T14:55:00Z",
  "jti":      "<fresh unique id>",
  "sig":      "<ed25519 sig>"
}))
Content-Type: application/json

{
  "frame":         "provider-tpo",
  "operation":     "eligibility-inquiry",
  "subjectPCI":    "pci:a1b2c3d4e5f6a1b2c3d4",
  "correlationId": "8f3d9a1c4b7e2d5f...",
  "payloadHash":   "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
}
```

Response (200):

```json
{
  "token": {
    "operation":     "eligibility-inquiry",
    "scope":         "eligibility-scope",
    "subject":       "pci:a1b2c3d4e5f6a1b2c3d4",
    "frame":         "provider-tpo",
    "correlationId": "8f3d9a1c4b7e2d5f...",
    "holder":        "my-provider",
    "consentRef":    "",
    "payloadHash":   "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "expiry":        "2026-06-06T14:55:00Z",
    "signature":     "<base64>"
  }
}
```

The minted token's `payloadHash` equals the one you sent in the `/authorize` body,
which is `sha256hex` of the ciphertext sealed in Step 3 — the token now binds THIS
payload.

**Step 5a — Stamp the token into the sealed envelope**

Set `meta.authzToken = json(token)` on the envelope you sealed in Step 3 (the
ciphertext does not change — the token rides in the cleartext metadata). The
envelope is now ready to route.

**Step 5b — Route through the Hub**

```
POST {hub}/route
X-Holder-Assertion: base64(json({
  "holderId": "my-provider",
  "audience": "hub",
  "issuedAt": "2026-06-06T13:55:01Z",
  "expiry":   "2026-06-06T14:55:01Z",
  "jti":      "<fresh unique id>",
  "sig":      "<ed25519 sig>"
}))
Content-Type: application/json

<JSON-encoded envelope>
```

**Step 6 — Receive and verify the response envelope (synchronous)**

The Hub returns the payer's response envelope directly in the HTTP body (200 OK).
The response leg seals a *new* ciphertext (the `CoverageEligibilityResponse`), so
the payer sealed-then-authorized it the same way: its token's `payloadHash` is the
`sha256hex` of the response ciphertext, which you recompute and check strictly per
§4.4.

Verify the response token using `VerifyBound`:

```
VerifyBound(
  respToken,
  authzPub,          // from manifest.json
  now,
  wantFrame:         "payer-coverage",
  wantOp:            "eligibility-response",
  wantCorrelationID: "8f3d9a1c4b7e2d5f...",   // same as request
  wantHolder:        respEnv.metadata.sender,   // must be "acme-payer"
  wantSubject:       "pci:a1b2c3d4e5f6a1b2c3d4", // same pci as request (response leg)
  wantPayloadHash:   sha256hex(respEnv.ciphertext), // STRICT — see §4.4
)
```

`wantPayloadHash` is the `sha256hex` you compute over the response ciphertext you
received; `VerifyBound` rejects the leg unless it equals `respToken.payloadHash`
(an empty want or empty token hash is rejected). For the illustrative response
ciphertext here this would be a distinct 64-hex value, e.g.
`b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9`.

Also assert:
- `respEnv.metadata.sender == "acme-payer"` (the holder we routed to)
- `respEnv.metadata.recipient == "my-provider"`
- `respEnv.metadata.transactionType == "coverage-eligibility"` (same as request)
- `respEnv.metadata.correlationId == "8f3d9a1c4b7e2d5f..."`

**Step 7 — Decrypt and parse the response payload**

```
payload = Open(respEnv, myProviderEncPub, myProviderEncPriv)
```

Validate the decrypted `CoverageEligibilityResponse` against US Core (§8), then
parse the `covered`/`not-covered` disposition.

---

## 7a. Prior-authorization — CRD → DTR → PAS

Prior-authorization is **three substrate legs in sequence**, each an independent
originate round-trip exactly like the eligibility leg in §7 (resolve → seal →
authorize-bound → route → verify-bound → open). Authority is evaluated **per leg**: a
fresh per-leg correlation id, a fresh `payloadHash`-bound token (AI-2), per-operation
(no standing capability — OWD-6 / AI-11). The frame is always `provider-tpo` on the
request leg and `payer-coverage` on the response leg.

### 7a.1 The leg sequence

| Leg | `transactionType` | Request `operation` | Response `operation` | What it does |
|---|---|---|---|---|
| **CRD** | `crd-order-select` | `crd-order-select` | `crd-cards` | Provider proposes the order (ServiceRequest + Coverage); payer returns CDS cards. If **no PA is required** the round-trip is **terminal here** — DTR/PAS never run. |
| **DTR** | `dtr-questionnaire-fetch` | `dtr-questionnaire-fetch` | `dtr-questionnaire` | Provider fetches the questionnaire the CRD card advertised (by canonical URL); the response is a Da Vinci `$questionnaire-package` collection Bundle (the questionnaire plus its dependent Libraries/ValueSets) — extract the Questionnaire, then fill it **locally** from its own clinical data. |
| **PAS** | `pas-claim` | `pas-submit` | `pas-response` | Provider submits the Claim bundle (the filled QuestionnaireResponse + ServiceRequest); payer adjudicates and returns a ClaimResponse. |

Two guards a conformant client MUST honour:

- **No-PA short-circuit.** If the CRD cards say PA is not required, return
  `no-pa-required` and stop. Do not run DTR/PAS.
- **Canonical-substitution guard.** The questionnaire fetched in the DTR leg MUST be
  the exact canonical the CRD card advertised; reject the exchange if the payer
  returns a different questionnaire.

Profiles per leg are in §8.2; the operation/frame rows are the CRD/DTR/PAS entries in
§4.3.

### 7a.2 Outcome vocabulary (`PriorAuthResult.Outcome`)

| Outcome | Meaning | Status |
|---|---|---|
| `approved` | Claim adjudicated approved; a non-empty `PreAuthRef` (and `validUntil`) is returned | Implemented |
| `no-pa-required` | The CRD leg determined no PA is needed (terminal at leg 1) | Implemented |
| `pended` | Adjudication pending (needs review); resume via ClaimUpdate (§7b) | Implemented |
| `denied` | Claim denied (explicit A3 review action; carries the denial rationale) | Implemented |

A client parsing a ClaimResponse that carries none of the explicit outcome signals
gets an error rather than a silent mis-parse — the parser never infers an outcome
from an ambiguous shape.

### 7a.3 One call vs. the manual leg-by-leg path

The Go SDK runs the whole sequence in one call:

```go
res, err := id.RunPriorAuth(ctx, httpClient, endpoints, payer, shnsdk.PriorAuthRequest{
    Member: "MBR-COVERED", DOB: "1975-04-02", Family: "Johansson",
    Clinical:         shnsdk.SandboxUC03Context(), // the answers that drive the outcome
    ProcedureCPT:     "72148",                     // SandboxUC03Order()
    ProcedureDisplay: "MRI lumbar spine w/o contrast",
    DiagnosisICD10:   "M51.16",
})
// res.Outcome == "approved", res.PreAuthRef != ""
```

A non-Go participant (or a Go participant that needs to inspect/modify an intermediate
resource) drives the same three legs **manually** using the exported SDK builders/
parsers as the escape hatch — each `→` below is one originate round-trip (§6.1, §7),
the build/parse calls bracket the leg:

```
# Leg inputs (built once from the dev-visible order):
srJSON  = BuildServiceRequest(cpt, display, icd10, "Patient/MBR-COVERED")
covJSON = BuildCoverage("Patient/MBR-COVERED", "Coverage/MBR-COVERED")

# LEG 1 — CRD
crdReq            = BuildConformantOrderSelectRequest(srJSON, covJSON, "Patient/MBR-COVERED")
crdResp           ← route(crd-order-select / crd-order-select → crd-cards, crdReq)
cov               = ParseCards(crdResp)          # cov: CardCoverage. !cov.PARequired() ⇒ no-pa STOP; cov.Covered=="not-covered" ⇒ STOP
canon             = cov.Questionnaires[0]        # DTR canonical (present when cov.NeedsDTR())

# LEG 2 — DTR
dtrReq   = BuildQuestionnaireFetch(canon)
dtrResp  ← route(dtr-questionnaire-fetch / dtr-questionnaire-fetch → dtr-questionnaire, dtrReq)
qJSON    = ExtractQuestionnaireFromPackage(dtrResp)  # DTR-fetch returns a $questionnaire-package Bundle
url      = ParseQuestionnaireURL(qJSON)          # MUST equal canon (canonical-substitution guard)
qrJSON   = FillQuestionnaire(qJSON, clinical, qrContext)     # fill LOCALLY from your data

# LEG 3 — PAS
bundle   = BuildConformantClaimBundle(ConformantClaimInputs{QR: qrJSON, SR: srJSON, PatientRef: "Patient/MBR-COVERED", CoverageRef: "Coverage/MBR-COVERED", Corr: corrID, Created: now})
pasResp  ← route(pas-claim / pas-submit → pas-response, bundle)
result   = ParseClaimResponse(pasResp)           # → {Outcome, PreAuthRef, ValidUntil}
```

Each `route(...)` is the §7 originate sequence with that leg's `transactionType` /
request `operation` / response `operation`; the `payloadHash`-bound token is minted per
leg. The clinical answers that drive the outcome are dev-visible inputs to
`FillQuestionnaire` (the conservative-therapy weeks, prior-imaging, neuro-deficit flags
— see `docs/SANDBOX.md` §3a), never conjured inside the round-trip.

---

## 7b. Prior-authorization — pended → amend, and denied

UC-04 and UC-08 extend the §7a CRD→DTR→PAS three-leg sequence with new outcomes
on the PAS leg and, for UC-04, a second exchange. Authority is evaluated per leg
throughout (fresh correlation + `payloadHash`-bound token each leg, AI-2/AI-11).

### 7b.1 UC-04: exchange-1 PAS submit returns pended

The initial PAS submit (§7a, leg 3) is identical for UC-04, but the payer's response
is a **Bundle** instead of a bare `ClaimResponse`. The Bundle shape is the pended
signal: it contains a `ClaimResponse` with `outcome=queued` and a `Task` enumerating
the supplemental items needed for adjudication (FR-20).

**Detect pended with `ParsePendedResponse`:**

```go
pended, needed, err := shnsdk.ParsePendedResponse(pasRespBytes)
// pended==true ⇒ Bundle received; needed carries the Task.input items
// pended==false ⇒ bare ClaimResponse; continue to ParseClaimResponse
```

`ParsePendedResponse` inspects the response `resourceType`: a `"Bundle"` is pended;
anything else is passed through to `ParseClaimResponse`. The `needed` slice is typed
(`[]NeededItem{Code, Display}`) — `Code` is the `Task.input` value (e.g.
`"operative-diagnostic-report"`) and `Display` is the `Task.input.type.text`.

### 7b.2 UC-04: exchange-2 ClaimUpdate (amend)

Exchange-2 is a second PAS leg using the `pas-claim-update` transaction type. The
provider sends the supplemental evidence, then the payer adjudicates and returns the
final `ClaimResponse`.

Wire contract:

| Field | Value |
|---|---|
| `transactionType` | `pas-claim-update` |
| Request `operation` | `pas-update-submit` |
| Response `operation` | `pas-update-response` |
| Response frame | `payer-coverage` |

The update Bundle payload carries (FR-21 + FR-32):

- `Claim` with `related[]` referencing the **original submit correlation identifier**
  (this binds the amendment to the pended claim; the payer rejects an update whose
  `related[]` does not match an open pended claim).
- The **unchanged** `QuestionnaireResponse` and `ServiceRequest` from exchange-1.
- An operative **`DiagnosticReport`** (US Core Note profile) — the new clinical
  evidence.
- A **`Provenance`** attributing the DiagnosticReport to its source (FR-32 — the
  payer **rejects** supplemental data without Provenance; `ResumePriorAuth` validates
  that `supp.ProvenanceAgent` is non-empty before calling any builder, so you meet
  FR-32 as a named precondition rather than a cryptic three-legs-deep payer rejection).

**One-call path** (`ResumePriorAuth`):

```go
// resume is the PriorAuthResume written by RunPriorAuth when outcome=="pended".
// supp carries the supplemental DiagnosticReport facts + the required ProvenanceAgent.
supp := shnsdk.SupplementalReport{
    ReportID:        "dr-uc04-operative",
    CPT:             "72148",
    Display:         "MRI lumbar spine w/o contrast",
    ProvenanceAgent: "Organization/acme-7f3a",  // FR-32 required
}
res, err := id.ResumePriorAuth(ctx, c, ep, payer, resume, supp)
// res.Outcome == "approved", res.PreAuthRef != ""
```

`ResumePriorAuth` validates `supp.ProvenanceAgent` before touching the wire — an
absent agent returns an error immediately rather than a cryptic payer rejection.

**Manual leg-by-leg path:**

For a non-Go participant or a Go participant that needs to inspect intermediates:

```
# Build the supplemental FHIR resources:
drJSON   = BuildDiagnosticReport(reportID, patientRef, cptCode, display)
provJSON = BuildProvenance("DiagnosticReport/"+reportID, provenanceAgent, now)

# Build the update bundle (Claim.related[] → originalCorrelationID, FR-21):
updateBundle = BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{
    QR: qrJSON, SR: srJSON, DiagnosticReport: drJSON, Provenance: provJSON,
    PatientRef: patientRef, CoverageRef: coverageRef,
    Corr: updateCorrID, OriginalCorr: originalCorrID, Created: now})

# Route as pas-claim-update (single originate round-trip, §7):
updResp ← route(pas-claim-update / pas-update-submit → pas-update-response, updateBundle)

# Parse the update response:
result = ParseClaimResponse(updResp)   # → {Outcome:"approved", PreAuthRef, ValidUntil}
```

`originalCorrID` is the correlation identifier from exchange-1 — the value the
envelope carried when the submit leg was routed (the payer's ledger key for the
pended claim). The `PriorAuthResume` struct persists it as `OriginalCorrelationID`.

### 7b.3 UC-08: denied PAS response

A denial is a **bare `ClaimResponse`** (not a Bundle), `outcome=complete`, with the
Da Vinci PAS reviewActionCode extension carrying `"A3"` (X12 306 "Not Certified").
There is **no** `preAuthRef`; the rationale is in `ClaimResponse.disposition`; the
appeal window is in `ClaimResponse.processNote[].text`.

**Parse with `ParseClaimResponse`:**

```go
result, err := shnsdk.ParseClaimResponse(claimRespBytes)
// result.Outcome == "denied"
// result.Denial.ReasonCode == "A3"
// result.Denial.Rationale == "…" (ClaimResponse.disposition)
// result.Denial.AppealNote == []string{"…"} (ClaimResponse.processNote[].text)
```

`ParseClaimResponse` navigates
`item[].adjudication[].extension[reviewAction].extension[reviewActionCode]` for the
A3 code. It fails loud on an ambiguous shape — an outcome that is neither
`approved` (non-empty `preAuthRef` + `outcome=complete`) nor `denied` (reviewActionCode
A3) returns an error rather than a silent mis-parse.

**Response shape summary:**

| Outcome | Response shape | Key field |
|---|---|---|
| `approved` | Bare `ClaimResponse` | `outcome=complete` + non-empty `preAuthRef` |
| `pended` | `Bundle` (ClaimResponse + Task) | Task.input enumerates needed items |
| `denied` | Bare `ClaimResponse` | `outcome=complete` + reviewActionCode `A3`; no `preAuthRef` |

Use `ParsePendedResponse` first (Bundle check), then `ParseClaimResponse` on
the non-Bundle case — this is the dispatch `parsePASOutcome` implements internally
and `ResumePriorAuth` / `RunPriorAuth` call for you.

---

## 8. FHIR conformance obligations

### 8.1 Two-gate posture

All FHIR resources exchanged through the substrate must conform to their
applicable IG profiles. The substrate enforces a **two-gate** posture:

1. **Runtime US Core validation** — every resource is validated against base R4 +
   US Core profiles at the gateway on egress (before sealing) and on ingress
   (after decrypting). Egress validation is load-bearing: an invalid resource
   must never enter the substrate.

2. **Da Vinci gap-report contract** — Da Vinci CRD/DTR/PAS-specific profile gaps
   are tracked in the substrate's conformance gap report (maintained upstream).

### 8.2 Profiles by transaction type

| Transaction type | Profiles |
|---|---|
| `coverage-eligibility` | `CoverageEligibilityRequest` / `CoverageEligibilityResponse` (US Core) |
| `crd-order-select` | Da Vinci CRD `CDSHooksRequest` / `CDSHooksResponse` |
| `dtr-questionnaire-fetch` | Da Vinci DTR `Questionnaire` |
| `pas-claim` / `pas-claim-update` | Da Vinci PAS `Claim` bundle / `ClaimResponse` bundle |
| `federated-query` | Da Vinci CDex `cdex-task-data-request` `Task` (request) / completed CDex `Task` whose `output` contains a US-Core searchset `Bundle` (`DiagnosticReport`/`DocumentReference`, `Provenance`) (response) — CDex + HRex + US Core |
| `patient-dtr` | Da Vinci DTR `QuestionnaireResponse` |

### 8.3 Terminology

Codes (LOINC, SNOMED-CT, ICD-10-CM, CPT) must be validated against the
substrate's curated value sets or a terminology service. Do not synthesise or
hallucinate codes; the FHIR validation gate — not the implementation — certifies
conformance.

### 8.4 CapabilityStatements

The payer gateway publishes a `CapabilityStatement` at `GET /metadata` for the
CMS-0057 Patient Access API (PDex PA EOB). Participants implementing that surface
must conform to it.

---

## 9. Status and roadmap

### Changelog

- **2026-06-24 — `federated-query` is now Da Vinci CDex (Task-Based Approach).** UC-05's federated
  external retrieval moved from a bespoke FHIR `Parameters` query to **Da Vinci CDex** (Clinical Data
  Exchange). The **request** is a `Task` conforming to `cdex-task-data-request`
  (`http://hl7.org/fhir/us/davinci-cdex/StructureDefinition/cdex-task-data-request`): `status=requested`,
  `intent=order`, `code=data-request-query` (cdex-temp), `for=Patient/<member>`, `authoredOn`, `requester`
  (the data-consumer), `owner` (the data-source facility), EXACTLY ONE hrex-temp `data-query` input — a
  `valueString` FHIR RESTful query `<Type>?patient=Patient/<m>&date=ge<start>&date=le<end>` — and a
  `purpose-of-use` input (cdex-temp, `valueCodeableConcept` TREAT). The **response** is the **same Task
  transitioned** to `status=completed`: it RETAINS the request `input` and adds an `output` → a `contained`
  US-Core searchset `Bundle` (the `DiagnosticReport` + `DocumentReference` + a source `Provenance`),
  FHIR-validated against CDex. Per CDex invariant **cdex-9** a Task Data Request carries EXACTLY ONE
  data-query, so the two document types UC-05 names federate as **two CDex legs — one Task per named type**.
  New exports `BuildCDexTaskDataRequest(patientRef, resourceType, start, end, CDexTaskMeta)`,
  `ParseCDexTaskDataRequest(taskJSON)` (the narrowness validator), `BuildCDexQueryResult(requestTaskJSON,
  searchsetBundle)` (the completed-Task wrapper), `ExtractCDexEvidence(taskJSON)`, and
  `CDexTaskMeta{AuthoredOn, Requester, Owner}`; the shared `BuildRecordsBundle` / `AllowedTypes` US-Core
  searchset assembler is unchanged. The bespoke `BuildQuery` / `ParseQuery` / `ExtractOperativeEvidence`
  are **REMOVED** (breaking). The substrate is unchanged: the `federated-query` op names, the
  `consentRef`/`custodian` consent gate (§4–§5), payload-blind routing, and non-aggregation are the same —
  only the leg CONTENT became CDex; the purpose-of-use in the Task is partner-asserted and NOT load-bearing
  for authorization (the substrate re-checks consent).
- **2026-06-18 — DTR-fetch returns a `$questionnaire-package`.** The `dtr-questionnaire-fetch`
  response is now a Da Vinci `$questionnaire-package` collection Bundle (the Questionnaire plus its
  dependent Libraries/ValueSets), not a bare Questionnaire — so the questionnaire's CQL/value-set
  dependencies survive the wire. New exports `BuildQuestionnairePackage` /
  `ExtractQuestionnaireFromPackage`; `Responder` wraps the Questionnaire into a package and
  `RunPriorAuth` extracts it before the canonical-substitution check + auto-fill. A manual
  leg-by-leg client MUST call `ExtractQuestionnaireFromPackage(dtrResp)` before
  `ParseQuestionnaireURL`/`FillQuestionnaire`. The sandbox lumbar-MRI questionnaire is also
  CQL-backed (a `cqf-library` extension + a per-item SDC `initialExpression`), so an operated SDC
  `Questionnaire/$populate` CQL engine can populate it; `FillQuestionnaire` ignores those extensions
  and fills by `linkId`, so the managed `QuestionnaireResponse` is byte-unchanged. `SandboxAdjudicate`
  accepts `valueInteger` **or** `valueDecimal` for `conservative-therapy-weeks` (a `$populate` engine
  emits a CQL numeric as `valueDecimal`).
- **2026-06-13 — PA-chain responder available.** `Adjudicator` grows three new methods —
  `OrderSelect(cpt string) (paRequired bool, questionnaireCanonical string)`,
  `Questionnaire(canonical string) (questionnaireJSON []byte, ok bool)`, and
  `PriorAuth(qrJSON []byte, hasDiagnosticReport bool) (PASDecision, error)` — served by
  `shnsdk.Responder` across four transaction types: `crd-order-select`,
  `dtr-questionnaire-fetch`, `pas-claim`, `pas-claim-update`. Sandbox helpers:
  `SandboxLumbarQuestionnaire()`, `SandboxAdjudicate(...)`, `QuestionnaireCanonicalLumbarMRI`,
  `SandboxUC03Context()`, `SandboxUC03Order()`. The pended-claim ledger is per-process;
  deployments needing durable pends across replicas front it with their own store. See
  `docs/SANDBOX.md` §3c for the updated quickstart.
- **2026-06-12 — Payer responder (eligibility) delivered.** `shnsdk.Responder` is now
  available in the public SDK (`github.com/SmartHealthNetwork/shn-sdk`). It implements
  the full inbound pipeline — `X-Hub-Assertion` verification (§6.2a) first, then authz
  token `VerifyBound`, decryption, `Adjudicator.Eligibility`, and a sealed-and-authorized
  response — for the `coverage-eligibility` transaction type. See `docs/SANDBOX.md` §3c
  for the quickstart.
- **2026-06-12 — Inbound transport authentication (`X-Hub-Assertion`).** The Hub
  now signs every forward to `/substrate/inbound` with an `X-Hub-Assertion` header
  (same assertion shape as `X-Holder-Assertion`; `holderId == "hub"`; 2-minute TTL;
  single-use `jti`). Responders MUST verify it — signature, issuer pin, audience,
  expiry, jti-once — before processing the envelope (§6.2a); failure → 403
  `{"error":"missing or invalid hub assertion"}` (stable string). The verification
  key is `hubTransportKeyURL` from the discovery descriptor → Hub `GET /transport-key`
  → `{"pubkey": "<base64 ed25519>"}`. This header is mandatory with no off state;
  a responder built before this version simply receives an extra header (safe to
  ignore for continuity, required to verify for conformance). The discovery descriptor
  now carries `hubTransportKeyURL`; see §1a field table.
- **2026-06-10 — Prior-authorization (UC-03, CRD→DTR→PAS).** Added §7a: the three-leg
  prior-auth sequence (frames/operations/transaction-types, the no-PA short-circuit and
  canonical-substitution guards), the `PriorAuthResult` outcome vocabulary
  (`approved`/`no-pa-required` initially; `pended`/`denied` added since — §7a.2), and the
  manual leg-by-leg path via the exported SDK builders
  (`BuildConformantOrderSelectRequest`→`ParseCards`→`BuildQuestionnaireFetch`→`ParseQuestionnaireURL`→`FillQuestionnaire`→`BuildConformantClaimBundle`→`ParseClaimResponse`)
  as the escape hatch beyond the one-call `shnsdk.Identity.RunPriorAuth`. `shn priorauth`
  runs it; `shn doctor` now also validates it. See `docs/SANDBOX.md` §3a.
- **2026-06-10 — Discovery descriptor (FR-37).** Added §1a: `GET {accounts}/discovery`
  serves a machine-readable descriptor (endpoints, sandbox responders, seeded personas,
  `wireProtocolVersion`, `sandbox`/`syntheticDataOnly`). It is sufficient to drive the
  loop — keys are resolved live (payer `encPub` from `/holders`, authz pub from
  `/pubkey`). `shn doctor` and a live deploy probe consume it. See
  `docs/SANDBOX.md` for the getting-started path.
- **2026-06-09 — BREAKING: authz token wire format changed (`payloadHash`).** The
  token now carries a signed `payloadHash` field — `sha256hex` (64 lowercase hex) of
  the envelope **ciphertext**. It is **REQUIRED on every envelope-borne operation**
  and **MUST be ABSENT on `patient-access-read`** (the one REST bearer read). Senders
  must **seal-then-authorize**: seal the payload first, then call `/authorize` against
  `sha256hex(ciphertext)` so the minted token binds THIS payload (AI-2). Recipients
  (and the Hub per leg) **strictly verify** `sha256hex(received ciphertext) ==
  token.payloadHash` — an empty want or an empty token hash is **rejected**. This is
  breaking: a participant minting or verifying tokens the old way (no `payloadHash`)
  is now rejected. Contract: §4.1 (request), §4.2 (token struct), §4.4 (`VerifyBound`
  / `VerifyBoundNoPayload`).

### What is implemented today (preview substrate)

- **Static admission** — holders provisioned via an operator manifest bundle.
- **Dynamic admission** (FR-38) — Trust-admin-gated `POST /register` (with
  participant proof-of-possession) via the Trust-operated Registrar;
  Hub and Authorization Framework poll `GET /holders` (~3-second interval);
  `registry = manifest ∪ dynamic`, no restart required. See §2.3.
- **Credential lifecycle** — Trust-operated `POST /revoke`, holder-initiated
  `DELETE /register/{id}` (RFC 7592), and holder-initiated `PUT /register/{id}`
  key-rotation (RFC 7592 re-key); removal/rotation converges to the registry on the
  next poll. Every transition is registrar-signed to the audit chain (fail-closed).
  See §2.4–§2.5.
- **Assertion one-time-use** — assertions carry a required `jti`; the Authorization
  Framework and Registrar enforce single-use within the assertion window. See §3.4.
- **Bundle-minted keys** — X25519 and Ed25519 keys generated at bootstrap time,
  written to `manifest.json` (public) and `secrets/` (private); the bundle now
  includes `adminPub` (Trust admin key for `POST /register`).
- **Originator path** — a participant can call `POST {authz}/authorize` and
  `POST {hub}/route` to originate substrate legs.
- **Reference inbound receiver** — the Hub forwards to `POST {holder}/substrate/inbound`;
  the Smart Gateway implements this surface. An Option-B participant implementing it
  natively must follow the protocol in §6.2.
- **Reference Option-B participant** — a reference implementation runs the full
  eligibility round-trip (both the covered and not-covered branches) and the
  prior-auth round-trips (approved, pended, and denied) against the live substrate
  by delegating to the public SDK (`shnsdk.RunEligibility` / `shnsdk.RunPriorAuth`),
  without importing any Smart Gateway internals on the originate path. Each run's
  `AuditEvent` is verified. The deploy pipeline runs all of these against the public
  sandbox on every substrate deploy.

### What's coming next

| Feature | Notes |
|---|---|
| **Push-notify on admission** | Hub + authz poll today (~3-second cycle); push-notify is the tracked fast-follow |
| **Per-role CapabilityStatements** (FR-37) | Machine-readable capability declarations for provider and Hub roles |
| **Trust-issued PCI** | Today: deterministic hash (demo only); goal: unguessable Trust-minted PCI |
| **Distributed replay cache** | Today: single-Hub in-process guard; goal: shared cache for horizontal scale |
| **Audit reader access control** | Today: audit chain is open; goal: role-gated reads |

### Deferred credentialing features (additive, not yet built)

Each of these is an additive extension of the §2–§3 contract; none is load-bearing
today and none changes the shapes above when added.

- **Overlapping dual-key rotation window** — zero-gap rotation in which the registry
  accepts a `prevSignPub` (with a `validUntil`) alongside the new key during the
  propagation interval, so no in-flight leg is ever rejected mid-convergence. Today's
  `PUT /register/{id}` (§2.4) does an atomic swap and relies on the holder-side
  propagation-window discipline instead.
- **Full SMART Patient Access edge** — `.well-known/smart-configuration`, SMART App
  Launch / Backend Services, Inferno conformance.
- **UDAP PKI / X.509 trust chains** — replace the bare-key PoP with a UDAP software
  statement and certificate-based trust.
- **`jwks_url` live key resolution** — resolve holder keys from a published JWKS
  endpoint rather than the manifest / registration body.
- **mTLS / DPoP transport binding** — channel-level sender constraint on top of the
  application-layer assertions.
- **Open self-service registration** — registration without a Trust-provisioned
  admin credential.
- **OAuth token revocation (RFC 7009) / introspection (RFC 7662)** — not applicable
  as-is: there is no standing bearer token to revoke or introspect (§4 authority is
  per-leg, per-operation). Revoking the **registration** (§2.4) stops all future
  per-operation authority.

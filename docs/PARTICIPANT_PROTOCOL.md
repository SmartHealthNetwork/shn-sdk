# Participant Wire Protocol — Direct Integration Contract

**Audience:** Partner engineers building a native integration with the Smart Health
Network without running the Smart Gateway binary. The details here match the current
public SDK and network behavior; field names and endpoint paths are exact.

**Scope:** Preview environment — synthetic data only. This document specifies the **general participant wire contract**
(identity, per-operation authorization, sealed envelopes, payload-blind routing); the worked flows
are the first workflow delivered on it, Prior Authorization (Da Vinci CRD+DTR+PAS, PDex). Not for
production deployment.

---

## 1. Overview and posture

The Smart Health Network consists of four cooperating components:

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

**Two integration paths:**

- **Integration path A** — run the SHN Smart Gateway binary. The gateway handles the
  envelope, FHIR mapping, validation, and authority flow on your behalf.
- **Integration path B (this document)** — implement the participant wire protocol
  directly. You manage keys, assertions, tokens, and envelopes yourself. The public
  `shn-sdk` (the `shnsdk` package + `shn` CLI) is the reference implementation of this
  path (eligibility round-trip, no Smart Gateway dependency).

This document specifies integration path B.

> **Go participants: use the SDK.** The supported direct-integration path for Go is the public
> participant SDK, **`github.com/SmartHealthNetwork/shn-sdk`** (`shnsdk`). It implements
> this protocol standalone (stdlib + `golang.org/x/crypto` only) and ships the `shn`
> CLI (keygen → register → eligibility). This document remains the **canonical wire spec** —
> authoritative for non-Go participants and the exact byte/field contract the SDK is verified against.

---

## 1a. Discovery

A participant's **first call** is the discovery descriptor: a machine-readable
document listing the live endpoints, the test responder(s) you exchange
with, and the seeded personas. It is **sufficient to drive the eligibility loop** — no
out-of-band URL list or key file required. `shn doctor` consumes it; so does a live
discovery probe in the deploy pipeline.

```
GET {accounts}/discovery
```

Returns (the Accounts service, `accounts.<apex>`):

```json
{
  "demo": true,
  "syntheticDataOnly": true,
  "wireProtocolVersion": "1.1.0",
  "igVersions": { "uscore": "6.1.0", "crd": "2.0.1", "dtr": "2.0.1", "pas": "2.0.1", "pdex": "2.1.0" },
  "igVersionsByLine": { "2.0": { "uscore": "6.1.0", "crd": "2.0.1", "dtr": "2.0.1", "pas": "2.0.1", "pdex": "2.1.0" }, "2.1": { "uscore": "7.0.0", "crd": "2.1.0", "dtr": "2.1.0", "pas": "2.1.0", "pdex": "2.1.0" }, "2.2": { "uscore": "7.0.0", "crd": "2.2.1", "dtr": "2.2.0", "pas": "2.2.1", "pdex": "2.1.0" } },
  "bridgedContractVersions": ["pa.crd@2.0", "pa.crd@2.1", "pa.crd@2.2", "pa.dtr@2.0", "pa.dtr@2.1", "pa.dtr@2.2", "pa.pas@2.0", "pa.pas@2.1", "pa.pas@2.2", "pa.pdex@2.1"],
  "hubAccepts": ["involved"],
  "endpoints": {
    "hub": "https://hub.<apex>",
    "authz": "https://authz.<apex>",
    "registrar": "https://registrar.<apex>",
    "patientAccess": "https://fhir.<apex>"
  },
  "authzPublicKeyURL": "https://authz.<apex>/pubkey",
  "hubTransportKeyURL": "https://hub.<apex>/transport-key",
  "demoResponders": [{ "role": "payer", "holderId": "conformance-payer" }],
  "operations": [ { "frame": "provider-tpo", "operation": "eligibility-inquiry", "transactionType": "coverage-eligibility" }, … ],
  "demoPersonas": [
    { "memberId": "MBR-D-UC01",    "dob": "1972-03-14", "family": "Larsen",            "expectedEligibility": "covered",     "payerId": { "system": "urn:oid:2.16.840.1.113883.6.300", "value": "00001" } },
    { "memberId": "MBR-D-UC01-NC", "dob": "1972-03-14", "family": "Larsen-Terminated", "expectedEligibility": "not-covered", "payerId": { "system": "urn:oid:2.16.840.1.113883.6.300", "value": "00001" } }
  ],
  "docs": "https://github.com/SmartHealthNetwork/shn-sdk/blob/main/docs/PREVIEW.md"
}
```

Two of the four advertised personas (`MBR-D-UC04`, `MBR-D-UC08`) also carry
`expectedPriorAuth`, `expectedAfterAmend`, and `order` — see §7a/§7b and
`docs/PREVIEW.md` §1 for the full four-persona descriptor.

### Fields

| Field | Meaning |
|---|---|
| `demo` | Always `true` on the preview network's descriptor. |
| `syntheticDataOnly` | Always `true` — **synthetic personas only, never production PHI**. |
| `wireProtocolVersion` | The wire-protocol version the network speaks (see below). A consumer rejects a descriptor whose version it does not support **before** running any leg. |
| `igVersions` | Pinned IG versions the network validates against (server-side gate). |
| `contractVersions` | Legacy — no longer populated in the network descriptor: participant declarations are participant truth, carried per-participant in the registrar feed (§2.3) and the directory (§3). Field retained for wire compatibility. |
| `igVersionsByLine` | Per-line IG pin sets: each contract line (`"2.0"`, `"2.1"`, `"2.2"`) maps to the IG versions that line validates against — same keys and composition as `igVersions`, which remains the 2.0-line snapshot. Additive field. |
| `bridgedContractVersions` | Contract lines the network's gateways can build or bridge (version-matched routing and translation, §8.6) — the network's contract capability surface. Additive field. |
| `hubAccepts` | Optional envelope fields the network's Hub reads. `involved` means the Hub records a leg under every patient its `involved` list names (§5.1): send `involved` only when it is listed. Additive field. |
| `endpoints.{hub,authz,registrar,patientAccess}` | The live participant-facing base URLs. `hub` is where you originate a leg (`POST /route`); `authz` mints/serves tokens; `registrar` serves the holder feed; `patientAccess` is the FHIR/Patient-Access surface (`GET /metadata`). |
| `authzPublicKeyURL` | Where to fetch the Authorization Framework Ed25519 verifying key (`{authz}/pubkey`). |
| `hubTransportKeyURL` | Where to fetch the Hub's Ed25519 transport verifying key (`{hub}/transport-key` → `{"pubkey": "<base64 ed25519>"}`). Responders use this key to verify `X-Hub-Assertion` on every inbound forward (§6.2a). |
| `demoResponders[]` | Responder-hint fallback (`role` + `holderId`) for a consumer whose descriptor parser predates `demoPersonas[].payerId`. Every persona in the current descriptor carries a `payerId`, so a current consumer resolves the test counterparty from the directory (`demoPersonas[].payerId` → holder-attested `payerIds`, §3) and never needs this field; it stays populated for that older path. Do not build new consumers against it. |
| `operations[]` | The advertised `(frame, operation, transactionType)` triples the network authorizes. |
| `demoPersonas[]` | The seeded synthetic patients and their `expectedEligibility` (`"covered"` \| `"not-covered"`) — the inputs + expected outcomes `shn doctor` asserts. Each persona also carries `payerId` — the seeded member's Coverage payor identity; resolve your test counterparty by matching it against holder-attested `payerIds` in `/holders` (§3). A persona that also carries `expectedPriorAuth` additionally names its own prior-auth `order` (system/code/display/diagnosis) — the order a payer verdict is a function of. |
| `docs` | Getting-started URL (`docs/PREVIEW.md`). |

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
verified STRICTLY in `VerifyBound` (see §4.4). A consumer whose SDK speaks a
different version should refuse to proceed and prompt an upgrade rather than send a leg
the network may reject (`shn doctor` exits code `20` here).

### Participant directory

```
GET {accounts}/participants
```

Requires a developer bearer token (`Authorization: Bearer <id_token>`, the same
credential as `GET {accounts}/clients`). A deliberately **narrow** projection
of the registrar's `/holders` feed (§2.3): every registered participant's id,
role, declared contract versions, and — for payers — operator-attested payer
IDs — nothing else (no keys, no base URLs). Sorted by `id`.

```json
[
  { "id": "acme-payer", "role": "payer", "contractVersions": ["pa.pas@2.0", "pa.pdex@2.1"], "payerIds": [{ "system": "urn:oid:2.16.840.1.113883.6.300", "value": "00078" }] },
  { "id": "acme-provider", "role": "provider" }
]
```

`contractVersions` is omitted for a participant that never declared any
(§2.3's field is optional). `payerIds` is present only on payer rows whose
payer identities are **operator-attested**; omitted otherwise. A payer identity
is never self-asserted at registration: on the self-serve path (§2.3a) the
applicant *claims* it on the access request (`POST {accounts}/access-requests`),
the operator *vouches* it at approval, and client registration (`shn register
--accounts`, wire `POST {accounts}/clients/{id}/pop`) refuses any declared id the
operator did not vouch (`403`, `"payer-id <system>|<value> not authorized for
this org"`) before forwarding the registration to the registrar; on the direct
Trust-admin path the operator attests it in the `POST /register` body itself
(§2.3). FR-G42. Additive field. A feed error surfaces as `502` rather than a
guess — this endpoint never invents a directory. FR-G47.

### Participant directory summary

```
GET {accounts}/participants/summary
```

**Public — no bearer token required.** An anonymous aggregate over the same
registrar `/holders` feed: total participant count and a per-role breakdown,
nothing else (no ids, keys, base URLs, or contract versions).

```json
{ "total": 3, "byRole": { "payer": 2, "provider": 1 } }
```

A feed error surfaces as `502`, same rule as the authenticated directory. Both
`/participants` and `/participants/summary` share ONE per-IP rate budget (120
requests/hour); tripping the cap returns `429`.

---

## 2. Identity and registration

### 2.1 Holder record

Every network participant is a **holder**. A holder record
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

`manifest.json` is **public**. It is the network's trust root for the session:
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

### 2.3 Dynamic registration

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
  "messageFrames": ["v1"],
  "contractVersions": ["pa.crd@2.0", "pa.dtr@2.0", "pa.pas@2.0", "pa.pdex@2.1"],
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
| `messageFrames` | **Optional** JSON array of message-frame versions this holder can decode (today: `["v1"]` — see §6.3). **Self-declared** — the codec-capable SDK/gateway build stamps it automatically; you do not hand-set it. Omitted ⇒ legacy (no framing). It is **outside** the PoP signing payload (below), so advertising it never changes your `pop`. |
| `contractVersions` | **Optional** JSON array of self-declared exchange-contract version tokens, one per contract line this build can exchange, shape `<contract>@<line>` (e.g. `"pa.pas@2.0"`). Grammar: `^[a-z0-9]+(\.[a-z0-9]+)*@[0-9]+(\.[0-9]+)*$`; at most 16 tokens; each 3–48 bytes. The registrar admission-validates shape only — the grammar is deliberately **open**, so declaring a line the network does not yet speak registers fine; tokens are **self-asserted capability, not admission-verified identity** (contrast the operator-vouched `payerIds`, FR-G42). Today's network-native set is `pa.crd@2.0`, `pa.dtr@2.0`, `pa.pas@2.0`, `pa.pdex@2.1` (§8.6's bridgedContractVersions is the network's capability surface). It is **outside** the PoP signing payload, so advertising it never changes your `pop`, and this field is purely **additive** — it did not require a `wireProtocolVersion` bump. Version-aware routing and translation consume these in later slices; today they are declaration + surfacing. |
| `payerIds` | **Optional, `role=payer` only.** JSON array of `{ "system", "value" }` payer identifiers this holder is routed to for (§1a personas carry the matching `payerId`). **Operator-attested, never self-asserted** (FR-G42): on this admin-gated path the Trust operator attests them in the body; on the self-serve path (§2.3a) they must have been vouched at access-request approval before `/pop` forwards them here. Outside the PoP signing payload. Globally unique — a `(system, value)` already bound to another holder is refused (409 below). Preserved across key rotation (§2.4); republished verbatim on `/holders` and projected into the participant directory (§1a). Identities acquired **after** admission are attested through `PUT /register/{id}/payer-ids` (§2.4) — same authority, same rules — so a payer never re-onboards to become routable on a new one. |
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
| `payerIds` present on a non-`payer` role | 400 (`"payerIds are only valid for role=payer"`) |
| `id` already dynamically registered, or a `payerIds` entry already bound to another holder | 409 (`"id or payer-id already registered"`) |

A 201 response means the holder is registered and will be visible to Hub and
Authorization Framework on their next poll.

**`GET /holders`** — returns all dynamically registered holders as a JSON array of
the same shape as the `POST /register` body. The Hub and Authorization Framework
poll this endpoint on a ~3-second interval to pick up new admissions. Each row
republishes the holder's `messageFrames` and `contractVersions` **verbatim** — the
feed is the same self-declared value the holder registered or last rotated, not
re-derived or re-validated beyond the admission-time shape check — and, for payers,
the operator-attested `payerIds`.

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

### 2.3a Self-serve registration via the Accounts service (preview environment)

Developers can register clients against the preview environment without obtaining a
Trust admin credential out of band. The **Accounts service** (`accounts.shn-preview.org`) is a Cognito-gated
developer-onboarding control plane that wraps `POST /register` on your behalf.

Use the `shn` CLI with the `--accounts` flag:

```sh
shn login --accounts https://accounts.shn-preview.org   # browser Cognito login, token cached
# No browser on this machine (SSH/CI)? Add --no-browser — the CLI prints a URL to
# open anywhere and you paste back the code it shows.
shn register --accounts https://accounts.shn-preview.org \
  --role provider --name acme --base-url https://acme.example -out ./keys
shn clients --accounts https://accounts.shn-preview.org  # list your clients
shn revoke <id> --accounts https://accounts.shn-preview.org
```

The `--accounts` path is the **Cognito-gated self-serve** path for preview-environment
onboarding. The admin-gated direct `POST /register` (§2.3) remains the canonical
operator/Trust path and the authoritative wire spec for all participants — the
Accounts service is an additive convenience layer over it.

> **Note:** The full self-serve round-trip (login → register → list →
> revoke) is interactive (browser Cognito login) and is verified operator-side
> after each deploy.

**Payer identities acquired after onboarding.** A payer client re-declares the
identities it is routed for with `PUT /clients/{id}/payer-ids` on the Accounts
service, authenticated as the client's owner:

```
PUT /clients/<client id>/payer-ids
Authorization: Bearer <developer token>

{ "payerIds": [ { "system": "…", "value": "…" } ] }
```

The set REPLACES the client's declared identities (an explicit `[]` withdraws them
all; an absent field is a `400`). Every identity in it must be one an operator
vouched for your organization — the same check `/pop` applies at registration
(FR-G42), so an unvouched one is refused
`403 payer-id <system>|<value> not authorized for this org` and nothing is
forwarded. Ask the operator to vouch it first. Accepted requests are forwarded to
the registrar's `PUT /register/{id}/payer-ids` (§2.4) under the Accounts service's
own admin credential; the registrar's refusals — notably `409` for an identity
another holder already holds (AI-G12) — come back verbatim, and your client's
declared set is left as it was. The client must be `active` and `role=payer`.

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
  "messageFrames": ["v1"],
  "contractVersions": ["pa.crd@2.0", "pa.dtr@2.0", "pa.pas@2.0", "pa.pdex@2.1"],
  "pop":     "<base64 Ed25519 signature over the canonical payload, by the NEW signPub private key>"
}
```

The `pop` is the same canonical proof-of-possession as registration (§2.3): an
Ed25519 signature over `id + "\n" + role + "\n" + encPub + "\n" + signPub + "\n" +
baseURL`, but here signed with the **new** `signPub`'s private key (proving the
rotator controls the key it is rotating to). **Re-key only:** `role` and `baseURL`
in the body MUST equal the existing record.

**Rotation refreshes `messageFrames` from the submitted body** — unlike the
operator-attested fields, the capability declaration is a property of the *current*
build, so the registrar re-reads it from every rotate. **Include `messageFrames` on
every rotate**, or your advertised capability silently resets to legacy (no framing);
a codec-capable SDK/gateway build re-stamps it for you, so a library-driven rotate
carries it automatically — hand-built rotate bodies must not drop it.

**Rotation refreshes `contractVersions` the same way** — same rule as
`messageFrames`: it is a self-declared property of the *current* build, not an
operator-attested field, so the registrar re-reads it from every rotate rather than
carrying the prior value forward. **Include `contractVersions` on every rotate**
you submit by hand, or your advertised set silently clears. The registrar
admission-validates shape only (grammar `^[a-z0-9]+(\.[a-z0-9]+)*@[0-9]+(\.[0-9]+)*$`,
≤16 tokens, each 3–48 bytes) — same as at registration (§2.3) — and the tokens
remain outside the PoP signing payload, so rotating them never changes your `pop`.
This is additive: no `wireProtocolVersion` bump was needed to add it. Version-aware
routing and translation consume these in later slices; today they are declaration +
surfacing.

**Rotation refreshes `requestFrames` the same way** — it is the current build's
self-declared request-frame set (§6.3), re-read from every rotate. A library-driven
rotate (`shn rotate`, `Identity.Registration`) carries the build's set, which is how a
holder registered with an earlier SDK comes to declare `"v1op"` (pass
`shn rotate --request-frames v1` while the Smart Gateway serving your base URL is older
than v0.44.0); a hand-built rotate body that omits `requestFrames` clears it, and
requests to you are then sent bare.

**Rotation NEVER changes `payerIds`** — they are operator-attested, not
self-declared (§2.3, FR-G42), so a rotate body may omit them (every library-driven
rotate does) or repeat the set the registrar already holds, and the attested set is
carried forward untouched either way. A body carrying a *different* set is refused
`403` rather than silently dropped; the route that changes them is
`PUT /register/{id}/payer-ids` below.

**Re-declaring without new keys.** A `PUT /register/{id}` whose `encPub` and
`signPub` equal the registered keys is accepted the same way (the `pop` is signed
with the current key): it refreshes only the self-declared lists above. It is
audited as `redeclared` rather than `rotated` (§2.5).

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
| `payerIds` present and different from the attested set | 403 (`"payerIds are operator-attested; a holder cannot change its own payer identities"`) |
| New `encPub` / `signPub` malformed (not valid base64 / wrong length) | 400 (`"malformed encPub/signPub"`) |
| `pop` absent or malformed (not valid base64 / empty) | 400 (`"missing registration proof-of-possession"`) |
| `pop` does not verify against the **new** `signPub` | 401 (`"registration proof-of-possession failed"`) |
| Registrar store read/write unavailable (list or update) | 502 (`"store error"`) |
| Lifecycle audit append failed (keys rolled back, fail-closed) | 502 (`"lifecycle audit failed"`) |
| Success | 200 (OK) |

**`PUT /register/{id}/payer-ids`** — operator-attested payer-identity update.

A payer acquires identities after it is admitted: an EHR assigns it a payer id, it
merges, it opens a line of business. This route REPLACES the holder's attested set
without re-admission, under the same authority and the same rules as `POST /register`
carried at admission — it is admission's attestation applied later, never a
self-declaration.

Auth: the `X-Holder-Assertion` header must be a **Trust admin** assertion, exactly as
for `POST /register` (§2.3). A holder's own assertion is refused `401`: a holder
declares its capabilities, never the identities the network routes to it (FR-G42).
Participants reach this through their own front door instead — the accounts service's
`PUT /clients/{id}/payer-ids` (§2.3a), which checks the identities against the ones an
operator vouched for that org and then makes this call server-side. Hosted tenants get
it from the control plane: change the tenant's `payerIds` and its reconcile loop
publishes the difference.

```
PUT /register/external-payer/payer-ids
X-Holder-Assertion: base64(json(assertion))   // Trust admin, audience="registrar"
```

```json
{ "payerIds": [ { "system": "urn:oid:2.16.840.1.113883.6.300", "value": "00078" },
                { "system": "http://ehr.example.org/payer-id", "value": "204" } ] }
```

The set is a REPLACE, not a union: an identity the body omits is withdrawn, and an
explicit `[]` withdraws all of them. An **absent** `payerIds` field is a malformed
request, not "leave them alone" — re-declaring capabilities is `PUT /register/{id}`.
A withdrawn `(system, value)` is immediately free for the holder that actually holds
it. Success is 200, and `GET /holders` (and the operator payer-id view) carries the
new set at once; the Hub and Authorization Framework see it on their next poll.

Rejection cases (checks are ordered):

| Condition | Status |
|---|---|
| Missing or invalid Trust admin credential (including a holder's own assertion) | 401 (`"missing or invalid Trust admin credential"`) |
| `{id}` is a founding holder from the manifest | 409 (`"founding holder, manifest-authoritative"`) |
| Body is not valid JSON | 400 (`"bad request body"`) |
| `payerIds` absent | 400 (`"payerIds required (an empty array withdraws every identity)"`) |
| More than 16 entries | 400 (`"too many payerIds"`) |
| An entry missing `system` or `value` | 400 (`"payerIds entries require both system and value"`) |
| An entry's `system`/`value` contains whitespace or `\|` (FHIR's reserved token delimiter — the same rule the self-serve path applies, §2.3a) | 400 (`"payerId system/value must not contain whitespace or '\|'"`) |
| The same entry twice | 400 (`"payerIds entries must be distinct"`) |
| No such (dynamic) holder | 404 (`"no such holder"`) |
| The holder's role is not `payer` | 400 (`"payerIds are only valid for role=payer"`) |
| A `(system, value)` is already bound to ANOTHER holder — ambiguity is refused, never resolved (AI-G12) | 409 (`"payer-id already registered to another holder"`) |
| Registrar store read/write unavailable | 502 (`"store error"`) |
| Lifecycle audit append failed (the set is rolled back, fail-closed) | 502 (`"lifecycle audit failed"`) |
| Success | 200 (OK) |

The transition is audited as `payer-ids-attested` (§2.5), with the admin key's label
in `scope` — so the record says which authority attested the change.

**Operational note — propagation window.** A 200 only updates the registrar feed.
The new keys propagate to the Hub and the Authorization Framework on their next
converge poll (~one ~3-second interval, §2.3). Rotate during a quiet moment, and on
the holder side:

- **Keep the old `encPub` private key loaded for ~one poll interval** so you can
  still decrypt envelopes a counterparty addressed to the now-stale `encPub` before
  it observed the new one. The payload-blind network never holds your enc private
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

Every lifecycle transition — `registered`, `revoked`, `deregistered`, `rotated` (new keys),
`redeclared` (a `PUT /register/{id}` that kept both keys, §2.4), `payer-ids-attested`
(a `PUT /register/{id}/payer-ids`, §2.4) — is signed
by the registrar with its own signing key (public key = the manifest `registrarPub`,
which the Audit Plane trusts as a signer; distinct from the Audit Plane's own
`auditSignPub`) and appended to the canonical audit chain. A transition
that cannot be recorded does **not** stand: a failed audit append rolls the change
back and returns 502 (fail-closed). Lifecycle audit records carry no patient
subject — they are fabric events.

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
- **`/verify` head assertion — reset-aware since 2026-07-02.** Beyond the
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
  re-sign a shorter chain) is not addressed by this checkpoint mechanism — it is the seam a future control
plugs into (FROST multi-party / external witness over the same `signatures[]` slot).
  A DDL-capable truncation that masquerades as a legitimate reset by minting a fresh
  chain generation — an **unauthenticated generation declaration** — is mitigated by the versioned per-generation anchors above plus a live
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
unique identifier, stamped before signing so the signature covers it. The network's own assertion-issuing logic generates a random 16-byte `jti` (base64url, unpadded);
an direct-integration participant minting assertions by hand must do the same. An assertion
**without** a `jti` is rejected (`"holderauth: missing jti"`). This is the SMART
`private_key_jwt` `jti` claim.

**`bh` — body-binding hash (OPTIONAL).** An assertion may additionally carry `bh`:
hex(sha256(request body)), stamped before signing so the signature covers it. A
body-bound verifier recomputes the hash from the body it received and rejects on
mismatch — a captured assertion cannot be replayed against a different body. An
assertion without body binding **omits the field entirely**; an omitted `bh`
contributes nothing to the signing payload, so the field-set above signs
byte-identically whether or not an integration ever uses body binding, and a
verifier that is not body-bound does not inspect it.

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
single-use it here; replay protection on routing is the per-envelope guard (§5.1).
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

The network's credentialing contract maps onto familiar OAuth 2.0 / SMART
building blocks. The shapes are recognizable; the trust posture is deliberately
narrower (no standing bearer token).

| SHN network construct | OAuth / SMART analogue |
|---|---|
| Holder registration (`POST /register`, §2.3) | OAuth 2.0 Dynamic Client Registration (RFC 7591) — the registration body is the client-metadata shape |
| Registration proof-of-possession (`pop`, §2.3) | A software-statement-style self-attestation of key control — but a bare Ed25519 PoP over the canonical payload, **not** a UDAP X.509 software statement |
| Holder assertion (§3) | SMART asymmetric ("private_key_jwt") client authentication (RFC 7521 / RFC 7523): a short-lived, key-signed assertion with `aud`, `exp`, and a one-time-use `jti` |
| Per-operation `authz` token (§4) | **NOT** an OAuth bearer access token. It is a **sender-constrained, per-operation** grant — a strict **superset** of SMART system scopes: bound to one operation, one frame, one correlation, one subject PCI, and one holder (no standing blanket capability that can be lifted or replayed across operations) |
| Lifecycle — deregister (`DELETE /register/{id}`, §2.4) | RFC 7592 client-configuration DELETE |
| Lifecycle — key-rotation (`PUT /register/{id}`, §2.4) | RFC 7592 client-configuration UPDATE (re-key) |
| Lifecycle — revoke (`POST /revoke`, §2.4) | Trust-operated client revocation (no self-serve OAuth token revocation; see §9) |

Because authority is minted per leg and never standing, there is no long-lived
bearer credential to steal, revoke, or introspect — removing the holder's
registration (§2.4) stops all future per-operation authority at the source.

---

## 4. Authority flow

Every network operation requires a scope-bound **authorization token** minted by
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
| `subjectPCI` | Yes | Must start with `"pci:"` — the network's opaque patient identifier |
| `correlationId` | Yes | Must be non-empty; binds the minted token to one leg |
| `custodian` | For `federated-query-submit` only | The facility holder ID; used to resolve patient consent at the Global Person Consent service |
| `payloadHash` | For every **envelope** op | `sha256hex` (64 lowercase hex) of the envelope **ciphertext**. **Seal the payload FIRST, then authorize** against the ciphertext (seal-then-authorize) so the minted token binds THIS payload. Absent for the one non-envelope op, `patient-access-read` (a REST bearer read) |
| `involvement` | Only for an involved patient's token | Asks for a token for **another patient this leg involves**, carried in the envelope's `involved` (§5.1): `request-named` (another patient your request names), or, on a payer's answer, `payer-held` / `payer-derived` (your own binding of the member the request names, when it differs from the leg token's patient, from a member your records hold or one they do not). Every other field is the leg's own, with `subjectPCI` that patient. The decision is recorded with it, so a refused one still appears in that patient's record. Absent for the leg's own token |

**Rejection cases:**

- `subjectPCI` absent or not prefixed `"pci:"` → 400
- `correlationId` absent → 400
- `involvement` present and not one of the three values above → 400
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
    Involvement   string    `json:"involvement,omitempty"` // only on a token for another patient the leg involves (§5.1)
    Signature     []byte    `json:"signature"`
}
```

`Involvement` is set only on a token minted for another patient a leg involves
(§5.1); such a token travels only to the Hub, never as a leg's own token, and
`VerifyBound` and `VerifyBoundNoPayload` refuse it when a recipient verifies a
leg. The Hub refuses a leg whose own token carries one (§5.1).

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
| `crd-order-dispatch` | `crd-order-dispatch` | `crd-dispatch-cards` |
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
    wantPayloadHash   string,  // sha256hex(the ciphertext you received) — STRICT
) error
```

Pass an empty string to skip a particular binding check — **except
`wantPayloadHash`, which is STRICT**: every envelope leg binds a payload, so the
receiver recomputes `sha256hex` over the ciphertext it received and asserts it
equals `token.payloadHash`; an empty want or an empty token hash is REJECTED. This
makes the network payload-blind AND payload-AUTHENTICATED: a payload
swapped in flight is cryptographically detected at the authorization check. On the
**response leg**, also pass the **request** token's `subject` as `wantSubject`. A
validly-signed token cannot be lifted into a different envelope, operation,
correlation, patient, **or payload**. Both verifiers also refuse a token carrying
`involvement`: it is minted for another patient a leg involves and is never a
leg's own token (§4.2).

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

Each network hop is one **envelope**: cleartext routing
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
    Involved        string `json:"involved,omitempty"`
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
| `involved` | Optional. The other patients this leg involves, as a JSON-marshalled list (a **string**, like `authzToken`) of `{"token": <JSON-marshalled token>, "involvement": <value>}`, one per patient, each token minted for this leg with that patient as subject and carrying, signed, the `involvement` it was requested with (§4.1); the entry's `involvement` must match it. The Hub records the exchange under each of these patients as well as the leg token's. Omit it when the leg involves no other patient. Recipients ignore it |

**Patient identifiers never appear in Metadata**. The subject PCI lives
only inside the token (and therefore only in the sealed ciphertext from the
sender's perspective; the Hub reads `authzToken` from the metadata as a string but
verifies `token.Subject` via `VerifyBound`). An involved patient likewise appears
only as the subject of its own token in `involved`.

**When to send `involved`.** Only to a network whose discovery descriptor lists
`involved` in `hubAccepts` (§1a); otherwise send none. A requester names each
other patient its request names (`request-named`). A payer, on its answer, names its own binding of the
member the request names when it differs from the leg token's patient
(`payer-held`, or `payer-derived` for a member its records do not hold). This is
recording only: an involved patient whose token `/authorize` refuses or fails to
mint, or whose minted token does not carry the `involvement` requested, is left
out of `involved` (a refused decision is itself recorded), and the leg is sent as
usual.

**What is recorded, and who sees it.** The Hub records the leg under the leg
token's patient and again under each involved patient, marked with how that
patient is involved. On a request that is each `routed`, `unreachable` or
`failed` record; on the answer, the `answered` record, under the patients the
request named and the payer's own binding (a patient named on both is recorded
once). A request refused before the Hub can read `involved` (a payload that does
not match its token) is recorded under the leg token's patient only. The Hub
removes `involved` from the envelope before forwarding the request and before
returning the answer, so neither gateway receives the other patients' identifiers
from it. Each holder's own view of its audit records includes the records
addressed to it, so the recipient of a request, or of an answer, sees the paired
records of that leg.

The Hub enforces:

- `authorityFrame` must be non-empty (400 otherwise).
- `correlationId` must be non-empty (400 otherwise).
- `timestamp` must parse as RFC 3339 and be within ±5 minutes of Hub clock (400 otherwise).
- `involved`, when present, must decode as a list of at most 16 entries, each with
  one of the involvement values its leg allows (`request-named` on a request;
  `payer-held` or `payer-derived` on an answer) and a token that verifies exactly
  as `authzToken` does — signature, expiry, frame, operation, `correlationId`,
  holder (the sender) and payload hash — minted with that same `involvement`,
  whose subject is a `pci:` identifier different from the leg token's and from
  every other entry's.
  - On a request, a list that fails this is refused (400) before anything is
    recorded or forwarded.
  - On an answer, an entry that fails it (or every entry, if the list itself
    cannot be read, including an `involved` that is not a JSON string, or is too
    long) is dropped and nothing is recorded from it; the answer is still
    delivered and recorded, since `involved` is recording input only. The Hub
    counts the drops: each entry, or one for a list it cannot read.
  - A request whose `involved` is not a JSON string is refused (400) like any
    list that cannot be read.
  - If the Hub cannot write a paired record for a request, it answers 502 and
    does not forward the request; the records already written for a request it
    was about to forward are closed `failed`. An answer is still delivered,
    with its own `answered` record, and the missing paired record is counted.
  - These are rules on the envelope's shape and authority, the same at every
    conformance level.
- The leg's own `authzToken` must carry no `involvement` (§4.2): a token minted
  for another patient never stands in for the leg's. This is an authorization
  failure, checked before `involved` is read and whether or not a list is
  present. On a request it is refused (403 `authz token verification failed`,
  marked `X-SHN-Delivered: no`) and nothing is recorded or forwarded; on an
  answer the Hub answers 502 (`response leg authorization failed`, marked
  `X-SHN-Delivered: yes`: the recipient answered), the answer is not
  delivered, and no `answered` record is written.
- The envelope must not have been routed in the last 2 hours (409 replay rejection).
  The Hub keys this on the pair the token binds — the `correlationId` and the
  ciphertext's hash — so exactly a byte-identical envelope is refused. It is
  recorded once the envelope passes verification — before recipient lookup,
  auditing, or forwarding — and is never released after that; a failed attempt
  spends that envelope, not its `correlationId`. See §6.1a for the retry rule.

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
box; this is the structural basis of payload-blind routing.

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
3. Rejects stale/future timestamps and replayed envelopes (the same `correlationId` and ciphertext).
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
appropriate 4xx/5xx status, and the header `X-SHN-Delivered` saying whether the
recipient's gateway received the request: `no` when the Hub refused before
forwarding, or the recipient's gateway was not reached or refused the request
at its edge (`4xx`); `yes` when the recipient answered and the Hub could not
read, decode, verify or audit the answer; `unknown` when the recipient answered
`5xx`, the connection failed after the request was sent, or it did not answer
before the forward gave up, so it may have received the request. An error
without the header did not come from the Hub (a load balancer in front of it,
say); a Smart Gateway reads a `5xx` of that kind as `unknown`. A failed forward
names the recipient's status, never its body: `forward to recipient failed: the
recipient answered 503`, `… the recipient refused it (403)`, `… the recipient
could not be reached`, `… the connection failed after the request was sent`,
`… the recipient's answer could not be read`, `… the recipient did not answer
in time`. An error response never releases the envelope — retry with a freshly
sealed one (§6.1a).

### 6.1a Retries — a freshly sealed envelope per attempt

The Hub records each envelope once it passes verification (§5.1) — before
recipient lookup, auditing, or forwarding — and never releases it, whether the
exchange then succeeds or fails. The record is the pair the request token binds:
the `correlationId` and the ciphertext's hash. Within the 2-hour replay window
(§5.1) a second byte-identical envelope is rejected
`409 {"error":"replay detected"}` — **including your own resend of the same bytes
after an error**. In particular, a `502` from a failed exchange — recipient
unreachable, a forward or audit-append failure, or a mis-constructed response
envelope — does not free that envelope for reuse. (A request refused at
verification itself — a 400/401/403, or the 502 payload-hash mismatch — is
rejected before it is recorded; the rule below is correct in either case, so you
never need to distinguish.)

To retry after any non-2xx from `POST /route`, build the leg again:

1. Keep the `correlationId`, or mint a **fresh random** one (§5.1). Both are
   accepted: the Hub never spends an id, only an envelope. The recipient keys
   its own records on the envelope's `correlationId` (a payer gateway files a
   PAS submit's authorization under it), so keep it to land a retried
   submission on the same authorization; a fresh one is a new submission.
2. **Re-seal** the payload (§5.2). Sealed boxes are non-deterministic, so the
   ciphertext — and with it the payload hash — changes on every seal.
3. Request a **fresh token** bound to the `correlationId` and the new payload
   hash (§4.1). Tokens are per-leg by design; there is nothing to reuse.
4. `POST /route` the new envelope.

A fresh `correlationId` over the original ciphertext, with a fresh token bound to
it, is also a new envelope and is routed.

SDK callers get this for free: `RunEligibility`, `RunPriorAuth`, and the other
originate helpers seal afresh (and mint a fresh `correlationId`) on every call,
so calling the helper again is a new, separate submission.

**Why the guard does not roll back on failure.** The payload-blind Hub cannot
distinguish a legitimate byte-identical resend from an attacker replaying a
captured envelope + assertion — they are the same bytes, and rejecting them is
the point of the guard (§5.1). Nor can the Hub know whether a failed forward
actually reached the recipient (a timeout can fire after delivery). Never
releasing an envelope keeps each one to at most one routed attempt — `routed` +
`answered`, or `routed` + `failed`, never two `routed` records for the same
bytes.

**A retry is a new exchange.** It gets its own audit trail and its own
processing at the recipient, which may see the same `correlationId` more than
once. Linkage that must survive a retry rides in the payload, not the envelope:
the prior-authorization amend leg, for example, references its pended claim via
`Claim.related` → the original submit's `urn:shn:correlation` (§7b.2), which is
independent of the amend leg's own envelope `correlationId`. A payer gateway
keys a submit's authorization on the envelope `correlationId` — which an SHN
gateway's ingress sets to the Claim's own correlation — so a retry under the
same `correlationId` lands on the same authorization rather than filing a
second one. If a failed
attempt may nonetheless have reached the recipient (a timeout, or a `502`
returned after forwarding), reconcile through the payload's FHIR business
identifiers.

**At an SHN gateway's Da Vinci ingress.** A participant's own system may send an
`X-Correlation-Id` header: it is the call's **trace** value, not the leg's
envelope `correlationId`. Each call's leg is sent under a freshly minted
`correlationId` — or, on a PAS submit or update, the Claim's own identity: its
`urn:shn:correlation`, or else the caller's trace value when that value is one of
the Claim's own identifiers (so an amend naming it in `related` binds) — so a
caller that reuses a trace value, or retries, is never refused for it and never
lands on another request's payer-side record. Every answer carries `X-Correlation-Id` (the caller's value,
the Claim's correlation when it names one, else the leg's id) and
`X-SHN-Leg-Id` (the id the leg was sent under, which the gateways' leg log lines
carry).

### 6.1b Leg outcomes — what an originator sees

Every origination leg your gateway attempts ends in exactly one of five
outcomes. They are the vocabulary of the gateway's leg metric
(`LegOutcome{outcome, role}`, emitted behind the gateway's `METRICS_SERVICE`
opt-in — the `LegMetric` seam in the gateway's `STABILITY.md`) and of what your
caller gets back from the console route that started the exchange. A leg is
`routed` when it is attempted, then one of the four terminal outcomes follows.

| Outcome | Meaning | What the caller sees |
|---|---|---|
| `routed` | The leg was attempted: recipient resolved, contract line selected, seal → authorize (§4.1) → `POST {hub}/route` under way. Always followed by exactly one terminal outcome. | — |
| `answered` | The counterpart answered and the response envelope verified end-to-end (§6.1 steps 6–8, `VerifyBound` §4.4). A frame-carried **non-2xx answer** is `answered`, not a failure: an application answer (§6.3 — an adjudication denial, a `422` validation reject, a partner payer's real `400`), and since shn-gateway v0.54.0 the counterpart gateway's own failure (§8, "Mechanical vs. application status" — its system unreachable, a record it could not read). The counterpart reports it, so it is not counted in your `LegError`; the counterpart's operators see it, and to the Hub, which cannot see inside the frame, the leg was answered. | The application response; a non-2xx answer is relayed **verbatim** with the recipient's own status, `Content-Type` and body. |
| `denied` | The **Authorization Framework refused the request leg** — `403` from `POST {authz}/authorize` (§4.1: wrong role for the frame, or no consent on `federated-query-submit`). A policy decision, not an error; excluded from the operators' `LegError` alarm. | A `403` whose `error` carries `authorization denied` (a `502` before shn-gateway v0.54.0), from a route with no legitimate denied branch; a flow that has one treats it as a business outcome instead (the UC-05 federated query leaves the prior authorization pended with `consentDenied: true` rather than failing). |
| `unreachable` | The **Hub leg did not complete**: the recipient was not reached, or, for a leg that timed out after your gateway sent it, may have been (the `504` says so): your gateway could not reach `POST {hub}/route`; or the Hub refused the leg — its own verification refusal (§6.1, any `400`/`401`/`403`/`409`, including `401 "unknown sender"` inside the registrar-poll window after you register), or the `502` it returns when the recipient is unknown, cannot be reached, or refuses the forward at its edge, on a payload-hash mismatch, or on an audit-append failure before the forward (each marked `X-SHN-Delivered: no`). A recipient that does not frame its answers is refused at its edge in the Hub's eyes, so its `4xx` application answer reads this way too. Also the leg that produced **no answer within your gateway's wait** (the HTTP client timeout it posts to `POST {hub}/route` with — 30 seconds in the published Smart Gateway; the whole Hub → counterpart gateway → counterpart system path shares that budget). | The Hub not reached: a `502` whose `error` is `hub routing failed`. The Hub's refusal, with `error` reading `hub refused the exchange: <the Hub's reason>`: a `409 … replay detected` keeps the Hub's status; every other Hub `4xx` concerns your gateway's standing with the Hub (its registration, its token, its clock), not the caller's request, and is a `502` — e.g. `502 … stale or future timestamp`, `502 … unknown sender`; a Hub `5xx` keeps its status, e.g. `502 … unknown recipient`. A recipient gateway that refused the forward at its edge: `502 the recipient's gateway refused the exchange (403)`. Before shn-gateway v0.54.0, every one of these was `502 hub routing failed`. The timed-out leg: a `504` whose `error` reads `no answer on the hub leg within 30s (hub leg timeout)` (the number is the client's own timeout, which your gateway applies as its own deadline on the leg; `hub leg timed out` with no number when your own request deadline ended the wait first; either adds `the recipient may have received this request: check its outcome before resending` when the request had already been sent to the Hub; a connection or TLS handshake that gives up before the Hub is reached is not called a timeout and stays the `502`). Retry with a freshly sealed envelope (§6.1a). |
| `failed` | Anything else, on **your gateway's** side of the leg: the Authorization Framework unreachable or erroring (non-403), a seal/encode failure. Also a leg **the recipient's gateway was reached for** whose answer was lost: the Hub marks its `502` with `X-SHN-Delivered` (`yes`: the recipient answered and the Hub could not read, decode, verify or audit the answer; `unknown`: the recipient answered `5xx`, the connection failed after the request was sent, or it did not answer before the forward gave up), a `5xx` with no marker (not from the Hub), a connection to the Hub that failed after your gateway sent the request, or the Hub returned `200` and the answer could not be read or fails your gateway's own verification (not an envelope, `VerifyBound`, correlation match, decrypt, frame decode). Counted in `LegError` with `unreachable`. | A `502` whose `error` names the reason — `authorization failed`, …. A leg the recipient was reached for reads `the recipient received this request and answered, but its answer was lost on the way back (…)` or `… its answer could not be accepted (…)`, or `the recipient may have received this request (…)`: the recipient may have acted on it, so check the request's outcome (for PAS, `$inquire`) before resending. |

Two things are **not** leg outcomes:

- A **refusal before the leg exists** — no registered payer for the member's
  Coverage payor identifier, or no shared contract line and no bridge (§8.6) —
  is a legible `422` and never emits `routed`; nothing reached the Hub.
- The Hub's **audit-record outcome** (§6.1 steps 4 and 7, §6.1a) is the Hub's
  own view of the same exchange and reuses some of these words with the Hub's
  meaning: a leg your gateway reports as `unreachable` is, on the canonical
  chain, either a request record the Hub wrote before failing (`denied` for a
  payload-hash mismatch, `unreachable` for an unknown recipient, `failed` for a
  forward that did not complete after `routed`), a `routed` record with no
  terminal record when the Hub refused the recipient's response, or no record
  at all when the Hub refused the envelope at verification.

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
5. Build the response payload — your application's real answer, whether that is a
   success body or a non-2xx application answer (an adjudication denial, a 422
   validation reject, and so on).
6. Construct a response `Metadata` with:
   - `sender` set to **your** holder ID
   - `recipient` set to the request envelope's `sender`
   - `transactionType` set to the **same** `transactionType` as the request
   - `authorityFrame` set to the response frame (e.g. `"payer-coverage"`)
   - `authzToken` set to a JSON-encoded token minted for the **response** operation
   - `correlationId` set to the **same** `correlationId` as the request
   - `timestamp` set to current UTC in RFC 3339
7. Look up whether the **requester** (the request envelope's `sender`) advertises
   message-frame support in the registry (§6.3). This decides, per exchange, whether
   step 8 or step 9 applies — there is no configuration flag.
8. **If the requester is frame-capable:** encode the response payload as a
   **message frame v1** (§6.3) — carrying its real status (2xx or not) and an
   allowlisted `Content-Type` — then seal the frame bytes to the **original
   sender's** `EncPub`. Both success and application-failure answers travel this way
   and are returned **200 to the Hub**; the Hub's own response code stops meaning
   anything about the application outcome.
9. **If the requester is not frame-capable (legacy):** seal the bare response
   payload to the **original sender's** `EncPub`, unchanged from the pre-message-frame
   contract — implicit `200` on success; a non-2xx application answer is not carried
   in the envelope at all and instead surfaces to the Hub as a genuine non-2xx, which
   the Hub relays to the requester as its generic `"hub routing failed"` failure.
10. Return the response envelope as the HTTP response body — `200 OK,
    Content-Type: application/json` for a frame-capable exchange (step 8); the
    payload's own status for a legacy exchange (step 9).

The Hub verifies this response envelope before writing the `"answered"` audit
record and returning it to the originator. A mis-constructed response causes the
Hub to return 502 to the originator.

**Originator-only participants** (those that only send, never receive) do not need
to serve this endpoint. A responder/inbound surface is available today via the public SDK (`shnsdk.Responder` — see `PREVIEW.md` §3c); originator-only participants do not need to serve this endpoint.

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
   `MaxAssertionTTL`), not merely the 2-minute TTL: the network's own guard
   retains for the full hour.

> **Go participants:** `shnsdk.Responder` implements this full verification pipeline —
> steps 1–6 above, plus authz token `VerifyBound`, decryption, adjudication, and the
> sealed-and-authorized response — in one call to `NewResponder` + `Handler()`. See
> `docs/PREVIEW.md` §3c for the quickstart.

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
**authority** check (§4.4). Both are required; neither substitutes for the
other. Verify the hub assertion first, then proceed to `VerifyBound`.

**Compatibility note.** A responder built before this header existed simply receives
one extra header — ignoring it is safe for continuity, but verifying it is
**required for conformance** from this protocol version on. The header has no off
state: the Hub sends it on every forward unconditionally.

### 6.3 Message frame v1

A **message frame** is how a frame-capable responder carries its real application
answer — status, an allowlisted header, and body — as the sealed payload of a
response leg (§6.2 steps 7–10). It is unrelated to the `authorityFrame` field on
`Metadata` (§5.1, §4.3); this section always says "message frame" in full to keep
the two apart.

**Negotiation.** Message framing is decided **per exchange from the registry**,
never from a per-message flag or a sniff of the bytes:

- A **responder** frames its answer iff the **requester's** registry entry
  advertises `"v1"` in its `messageFrames` capability list.
- An **originator** decodes any payload bearing the frame magic; the recipient's
  advertised `"v1"` governs only expectation and observability (the stale-feed
  downgrade log below), not the decode decision. *(Hardened at final review:
  decoding on the magic byte — rather than on the advertised capability — also
  closes the inverse stale window, where a responder correctly frames to a
  v1-advertising requester while the originator's view of the recipient is still
  pre-upgrade, e.g. during re-registration or a rolling deploy; the same magic-byte
  collision argument makes this safe.)*
- The capability is **self-declared by the library, not hand-configured**: an
  SDK-based (or gateway) participant on a codec-capable build stamps `"v1"` into
  its own registry entry automatically at registration, and again on key
  rotation. An older, already-registered participant simply has no `"v1"` entry
  and stays on the legacy contract until it re-registers from an upgraded build.
  There is nothing for an operator to turn on.

**Stale-feed fallback.** Registry propagation is eventually consistent — an
originator can briefly still see a responder's pre-upgrade entry. If the
originator expects a frame (per the rule above) but the decrypted payload does
not begin with the frame's magic byte, it treats the payload as bare legacy
instead of rejecting it, and logs the downgrade as an observable event. This is
safe, not a sniff: the magic byte (`0x00`) can never be the first byte of any
bare payload this protocol carries (FHIR JSON, and every other text format in
use, both start with a printable byte), so a bare payload can never be
misidentified as a frame, or vice versa.

**Wire layout.** The frame is the entire sealed response payload (i.e., it is
what `Open` in §5.2 decrypts to, in place of the bare FHIR bytes):

```
byte 0        magic       0x00
byte 1        version     0x01
bytes 2..5    header-len  big-endian uint32 (H), capped at 64 KiB
bytes 6..6+H  header      JSON, schema below
rest          body        raw bytes — no additional encoding
```

**Header schema.** Every current transaction type on this protocol is HTTP-family:

```json
{
  "status": 200,
  "headers": {
    "Content-Type": "application/fhir+json",
    "contractVersion": "pa.pas@2.0"
  }
}
```

- `status` — the application's real HTTP status, `100`–`599`. Both 2xx and
  non-2xx answers are framed identically; there is no separate error shape.
- `headers` — an **allowlist**, widened 2026-08-11 (multi-version-contracts
  design §4, routing) to `Content-Type` and `contractVersion`, and widened
  again for framed DTR operations to `operation` (request frames only, below).
  No other header (hop-by-hop, cookie, or otherwise) is ever carried inside a
  frame.
- `Content-Type` on a **request** frame — the media type of the body. An SHN
  gateway frames the body's own media type (`application/json` for a CDS Hooks
  request, `application/fhir+json` for a FHIR one) and, on a framed DTR
  operation, the FHIR JSON media type the participant's own system declared
  (`application/fhir+json` or `application/json`, with its parameters such as
  `fhirVersion`). PAS submit, update and inquiry requests are not framed, so
  they carry no declared media type. A payer gateway sends its own system each
  request with that leg's media type as `Content-Type` and `Accept` —
  `application/json` for CDS Hooks, `application/fhir+json` for the FHIR
  operations, or the declared FHIR media type a DTR request frame carried — as
  a direct client would. On a CDS Hooks leg the frame's `Content-Type` is not
  used: earlier releases stamp `application/fhir+json` on every request frame.
- `contractVersion` — the full `<contract>@<line>` token (e.g. `pa.pas@2.0`) of
  the exchange-contract line the response body was **built at** — content-
  descriptive, like `Content-Type`, not a negotiation echo. Present only on
  contract-mapped legs (§8.6); a version-neutral leg's frame omits it, same as
  every other legacy answer. **Stamped** on every framed **success** (2xx)
  answer by SHN gateways since **v0.37.0**, and — published-SDK stamp parity —
  by an SDK-based `Responder` that opts in
  (`ResponderConfig.StampContractVersion`) as of **v0.38.0** of this library.
  **Verified** against the contract line the leg actually routed to (§8.6) by
  SHN gateways since **v0.37.0**, and — the same v0.38.0 parity — by the
  published SDK's own originators (`RunEligibility`, `RunPriorAuth`; the
  package-private `unframeAnswer`'s `expectedToken` check, verbatim-mirroring
  the gateway's verify) as of **v0.38.0**; disagreement is rejected before the
  body reaches any parser, either way. An **absent** stamp is always tolerated
  (a pre-version responder, or a responder build that does not opt into
  stamping), exactly like an absent frame is tolerated today. A non-2xx frame
  is never stamped: its body is relayed verbatim and never parsed as contract
  content.

**Decoding is strict.** A decoder rejects (rather than silently degrading) on: an
unknown version byte, a header length that overruns the payload or the 64 KiB
cap, a non-JSON or malformed header, or an out-of-range `status`. Each of these
is a distinct, typed decode failure. A header field outside the allowlist is
**not** a reject: the reference decoder silently drops it and returns success
(SHN gateways only ever emit the allowlisted headers, `Content-Type`, on
contract-mapped legs `contractVersion` (§8.6), and on a framed DTR operation
`operation`, so this rarely fires in practice). Because an unknown header is
dropped rather than refused, a receiver built before a header was allowlisted
never sees it: a new header that changes how the body is read is therefore
sent only to a receiver that declares the matching capability (see
`v1op` below).

**Mechanical vs. application status — the rule that replaced
`RESPONDER_RELAY_ERRORS`.** A responder returns a non-2xx status **to the Hub**
only for a genuine exchange-machinery failure at its own edge — a bad hop
assertion, an envelope that fails to decode, a token that fails verification, a
replay, an unknown `transactionType`, or a failure building the response leg
itself (seal/authorize/encode). Everything the application produced — an
adjudication denial, a partner payer's real `400`, a `422` validation reject —
is an application **answer**, not a machinery failure, and (for a frame-capable
exchange) travels inside the frame with **200 to the Hub**. So is any `4xx` the
responding gateway itself writes about the request once the leg is authenticated
— a member it does not hold when it requires known members (`400 unknown member`), a request it cannot read, no
order to decide on, an eligibility subject that does not match the token (`403`; a payer that
declares its own eligibility endpoint carries such a request instead), a consent it
cannot confirm, an ingress validation failure at enforcement `strict` (`422
ingress validation failed`) — those are its verdict, not its machinery, and
travel the same way, as does any `4xx` it writes about its own participant's
answer after that system answered (an answer that repeats a member name, and at
`strict` a PAS response whose patient linkage is inconsistent or that names
another patient, a questionnaire package carrying a subject, or an answer that
fails validation). Since shn-gateway v0.54.0 so does its own failure once the leg
is authenticated (`5xx`): its participant's system that could not be reached
(`502 the payer's system could not be reached`) or that received the request and
gave no answer it could carry (`502 the payer's system received this request but
gave no answer this gateway could carry; it may have acted on it: check its
outcome before resending`), a system-of-record read that failed (`502`, or `503`
while that system is unavailable), a validator it cannot reach. (A record it
cannot write after its payer answered withholds nothing: the answer is relayed,
§7b.2.) The requester reads that gateway's status and message. On a PAS
submit or update, any refusal the gateway makes after its payer's system answered
(a failure of its own, or its refusal of the payer's answer) adds `the payer's
system received and answered this request: check its outcome before resending`. Only the pre-handler checks
above and a failure building the response leg itself stay bare, and the Hub
reports those as a failed forward that names the recipient's status (§6.1).

**Legacy peers see no change.** An exchange where either side is not
frame-capable is byte-identical to the protocol's original, pre-message-frame
contract: bare FHIR payload on the wire, implicit `200` on success, and a
non-2xx application answer reaching the requester as the Hub's failed forward,
which names the recipient's status but never its body (§6.1). There is no environment variable
governing this — a prior, now-removed release-specific mechanism
(`RESPONDER_RELAY_ERRORS`, a JSON wrapper) covered the same problem for a single
release before message-frame negotiation replaced it; see the gateway's
`docs/CONFIGURATION.md`.

**Request frames (`requestFrames`).** The same v1
codec above also frames the **request** leg of a contract-mapped exchange
(`pa.crd` / `pa.dtr` / `pa.pas`, §8.6): an originator wraps the request payload
in a v1 frame carrying the `contractVersion` it built the request at (the line
it is routing the leg to, §8.6) — same wire layout and header schema as the
response direction, with the frame's `status` field inert on a request (a
`200` filler; no receiver reads it). This is a capability distinct from
`messageFrames` — a **separate** registry list, so the request and response
directions negotiate, and can be rolled out, independently:

- An originator frames a request iff the **recipient's** registry entry
  advertises `"v1"` in its `requestFrames` capability list — a peer that never
  declares it keeps receiving a byte-identical bare request, the same
  one-recipient-at-a-time gate `messageFrames` uses for the response direction.
- **Receiver obligation.** A holder that declares `requestFrames` MUST accept
  **both** a framed and a bare inbound request — declaring the capability
  commits only to being *able* to decode a frame when one arrives, never to
  requiring one. The reference decoder is the same `shnsdk.DecodeHTTPFrame`
  §6.3 already documents, applied to the request payload (keyed on the frame
  magic byte, exactly like the response direction) before the request is
  dispatched to application logic. This SDK's `Responder` self-declares
  `requestFrames` automatically at registration (`SupportedRequestFrames` —
  on by default for every SDK-library registrant, the `messageFrames`
  precedent) and decodes accordingly; a partner implementing its own receiver
  from scratch must do the same — call `DecodeHTTPFrame` (or an equivalent
  decoder) on any inbound payload beginning with the frame magic, and fall
  through to bare parsing otherwise.
- A framed request carrying **no** `contractVersion` claim is treated exactly
  like a bare request (the frames-without-versions case — absence is always
  tolerated, the same precedent as the response direction).
- `coverage-eligibility` is version-neutral (no contract-version token exists
  for it, §8.6) and is therefore never framed, regardless of what either side
  declares.

**Framed DTR operations (`operation`, capability `v1op`).** A
`dtr-questionnaire-fetch` request frame may name the Da Vinci DTR operation
whose own input is the frame body:

| `operation` | Body |
|---|---|
| `questionnaire-package` | the `$questionnaire-package` input `Parameters` (profile `dtr-qpackage-input-parameters`), exactly as the requester built or received it: every `coverage`, `order`, `questionnaire` canonical (a `\|version` kept), `context` and other parameter |
| `next-question` | the SDC `$next-question` input: a `Parameters` with one `questionnaire-response` parameter, or a bare `QuestionnaireResponse` |

- **Capability.** A holder that accepts framed DTR operations declares the
  token `"v1op"` in its `requestFrames` list (it satisfies the registrar's
  `^[a-z0-9]{1,16}$` token rule). `"v1op"` is separate from `"v1"`: a holder
  that declares only `"v1"` accepts request frames but not the `operation`
  header. Declaring `"v1op"` also declares that the holder accepts request
  frames for `dtr-questionnaire-fetch`: a requester frames the operation to a
  `"v1op"` peer whether or not that peer also lists `"v1"`. The other
  transaction types are framed only to a peer that lists `"v1"`.
- **Refuse unless declared.** A receiver that does not know the `operation`
  header drops it silently (above) and would read the body as the older
  questionnaire request. A requester therefore sends a framed DTR operation
  **only** to a recipient whose registry entry declares `"v1op"`, and
  otherwise refuses before sending:
  `payer gateway does not support framed DTR operations (upgrade required)`
  (this SDK: `ErrFramedDTRUnsupported`). The frame also carries
  `contractVersion` as usual.
- **Receiver obligation.** A holder that declares `"v1op"` dispatches a
  `dtr-questionnaire-fetch` request on its `operation` header. The Smart
  Gateway, as a payer, refuses a `dtr-questionnaire-fetch` request that names
  no operation — the older questionnaire request (a JSON object with
  `canonical` and optional `coverage`), framed without `operation` or bare —
  with `400 questionnaire request names no operation: …`, before its payer's
  system sees it: such a request is not the requester's own operation input,
  and the package request the gateway used to rebuild from it carried only
  the canonical and a coverage. `shnsdk.Responder` still answers the older
  request. The `operation` header on any other transaction type is refused
  with `400`. The Smart Gateway, as a payer, identifies the patient a DTR request
  names by its own system before its payer's system sees the request, and
  does not compare that patient with the one the leg's token names. At
  enforcement `strict` every patient the request names (each coverage
  beneficiary, order subject or patient, Patient resource and other patient
  reference) must be that one patient: a second patient is refused with
  `403`, and a non-Patient reference, a Patient with no id or a package
  request with no coverage with `400`. Below `strict` these are carried as
  sent (recorded as a finding at `observe`). At every level, a request the
  payer cannot read, a coverage parameter that is not a JSON object (or, on a
  payer that maps its payer identity, not a Coverage) and a `next-question`
  round naming no patient subject are refused with `400`; a
  request left naming no patient at all is refused with `strict`'s refusal;
  and, when the gateway requires known members, so is a member it does not
  hold (`400 unknown member`).
- **This SDK.** `BuildQuestionnairePackageParameters(line, …)` builds the
  `questionnaire-package` input with every resource embedded as your own
  bytes; `RunPriorAuth` sends it as a framed operation when the payer
  declares `"v1op"` (carrying the payer's updated order and its
  `coverage-assertion-id` as `context`) and the older request otherwise. The
  SDK `Responder` serves both operations and the older request; `next-question`
  is served when your `Adjudicator` implements `NextQuestionAdjudicator`, and
  refused with `422` otherwise. `SupportedRequestFrames()` lists `"v1"` and
  `"v1op"`, so every registration this SDK builds declares both. An older
  payer gateway does not serve framed DTR operations, so never declare
  `"v1op"` by hand for a payer that cannot.
- **Who declares it.** A holder's `requestFrames` states what the Smart
  Gateway (or receiver) serving its base URL accepts, so who declares `"v1op"`
  depends on how the holder was registered:
  - **Reference participants provisioned with the network** declare `"v1"`
    and `"v1op"`; their Smart Gateways accept framed DTR operations.
  - **Hosted gateways** declare what their pinned Smart Gateway release
    serves: `"v1"` and `"v1op"` from v0.44.0, `"v1"` only from v0.34.0, and
    no request frames on an earlier release or an image whose release cannot
    be told from its tag. Changing the release re-declares the frames: a
    withdrawal is published before the older release is rolled out, and the
    rollout waits until the network has confirmed it; an addition is
    published once the newer release is running.
  - **Self-registered participants** declare exactly what their registration
    (or last rotation) declared; the network never adds a capability for
    them. A registration built with an SDK earlier than v0.50.1 declares only
    `"v1"`, so requesters do not send it framed DTR operations; to receive
    them, re-declare with `shn rotate` (`PUT /register/{id}`, §2.4) from SDK
    v0.50.1 or later, once the Smart Gateway serving your base URL accepts
    them.
  - **The `shn` CLI** (`shn register`, `shn rotate`) declares every capability
    its build supports unless you pass `--request-frames`. Declare `"v1op"`
    only when the Smart Gateway serving your base URL is v0.44.0 or newer;
    otherwise pass `--request-frames v1`, and rotate after you upgrade the
    gateway. `shn rotate` issues new keys, so the gateway must then load the
    new key directory. The option accepts only tokens the build supports;
    `v1op` without `v1` is accepted with a warning, since requesters then
    frame only DTR operations to you and send your other requests bare.

The Smart Gateway additionally *honors* a well-formed claim it can both
natively build and validate for (native ∩ laned);
this SDK's published `Responder` does not do per-line content negotiation, so
it decodes and tolerates the claim without acting on it.

**Ingress refusal — a claim the receiver cannot honor is a legible `422`, never
a silent downgrade.** A Smart Gateway that decodes a request-frame
`contractVersion` claim answers at that line only when it can *both* build and
validate there; otherwise it refuses the leg before any application logic runs,
naming what it does speak. Three distinct refusals, each a `422`:

- **Unknown / unbuildable line.** The claimed token is not in the receiver's
  native set for that leg's contract — e.g. a peer claims `pa.pas@3.0`:
  `request declares contract version pa.pas@3.0, which this gateway cannot build for leg pas-claim (it speaks pa.pas@2.0,pa.pas@2.1,pa.pas@2.2)`.
- **Native but unlaned.** The receiver *can* build the line but has no
  `$validate` lane configured for it, so it cannot certify its own answer —
  it refuses rather than answer unvalidated (FR-36):
  `request declares contract version pa.pas@2.2 but this gateway has no FHIR validator lane for line 2.2 — refusing to answer at an unvalidatable line (FR-36/FR-G29)`.
  One `$validate` server hosts exactly one version of a given IG, so a line
  without its own lane genuinely cannot be validated on another line's.
- **Claim on a version-neutral leg.** `coverage-eligibility` carries no
  contract-version token (§8.6); a frame that claims one on that leg is
  malformed, not tolerated.

A **bare** request (or a framed one carrying no claim) is never refused: the
receiver symmetrically recomputes the line the originator would have selected —
the sender's declared set × the receiver's declared set, highest common line,
falling back to the receiver's own canonical line for a silent sender — so a
pre-request-frame or version-neutral sender is answered exactly as before. A
recomputation that would *refuse* degrades to the receiver's canonical line
instead: the originator already made the routing decision, and refusing an
in-flight leg on the receiving side would break the declared-set change window
described in the gateway's `docs/CONFIGURATION.md`.

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

**Step 1 — Identify the patient**

Obtain the patient's network identifier (the `subjectPCI` of Step 4) from
`shnsdk.ResolvePCI`, passing the member's identifier and demographics as your system of
record holds them; the Smart Gateway does this through its system-of-record connector. Treat
the result as an opaque string: carry it as the SDK returns it and as tokens name it, and do
not parse it, reimplement how it is produced, or depend on its format beyond the `pci:`
prefix. How it is produced is internal to the network and may change. For a member its
system of record does not hold, the Smart Gateway identifies the patient from the member
id and the Patient the request carries; that derivation is internal to the gateway, and a
participant building its own gateway has no SDK call for it. From shn-gateway v0.55.0 the
derived identifier is in a namespace of its own that never equals a held member's
identifier; earlier releases derive it the way a held member's identifier is made, so it
can equal one.

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
THIS exact ciphertext. Build the metadata without `authzToken` for now (you
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

Prior-authorization is **three network legs in sequence**, each an independent
originate round-trip exactly like the eligibility leg in §7 (resolve → seal →
authorize-bound → route → verify-bound → open). Authority is evaluated **per leg**: a
fresh per-leg correlation id, a fresh `payloadHash`-bound token, per-operation
(no standing capability). The frame is always `provider-tpo` on the
request leg and `payer-coverage` on the response leg.

### 7a.1 The leg sequence

| Leg | `transactionType` | Request `operation` | Response `operation` | What it does |
|---|---|---|---|---|
| **CRD** | `crd-order-select` | `crd-order-select` | `crd-cards` | Provider proposes the order (ServiceRequest + Coverage); payer returns a CDS Hooks response. Da Vinci CRD carries the coverage information as an `update` system action on the order (cards are for text a person reads). If **no PA is required** the round-trip is **terminal here** — DTR/PAS never run. |
| **DTR** | `dtr-questionnaire-fetch` | `dtr-questionnaire-fetch` | `dtr-questionnaire` | Provider fetches the questionnaire the CRD coverage information advertised (by canonical URL); the response is a Da Vinci `$questionnaire-package` collection Bundle (the questionnaire plus its dependent Libraries/ValueSets) — extract the Questionnaire, then fill it **locally** from its own clinical data. |
| **PAS** | `pas-claim` | `pas-submit` | `pas-response` | Provider submits the Claim bundle (the filled QuestionnaireResponse + ServiceRequest); payer adjudicates and returns a ClaimResponse. |

Two guards a conformant client MUST honour:

- **No-PA short-circuit.** If the CRD coverage information says PA is not required, return
  `no-pa-required` and stop. Do not run DTR/PAS.
- **Canonical-substitution guard.** The questionnaire fetched in the DTR leg MUST be
  the exact canonical the CRD coverage information advertised; reject the exchange if the payer
  returns a different questionnaire.

Profiles per leg are in §8.2; the operation/frame rows are the CRD/DTR/PAS entries in
§4.3.

### 7a.2 Outcome vocabulary (`PriorAuthResult.Outcome`)

| Outcome | Meaning | Status |
|---|---|---|
| `approved` | Claim adjudicated approved; a non-empty `PreAuthRef` (and `validUntil`) is returned | Implemented |
| `no-pa-required` | The CRD leg determined no PA is needed (terminal at leg 1) | Implemented |
| `pended` | Adjudication pending (needs review); resume via ClaimUpdate (§7b) | Implemented |
| `denied` | Claim denied (a reviewActionCode `A3` "Not Certified" — the code this network's own PAS producer emits; the parser also accepts the reference payer's observed `A2` denial shape; carries the denial rationale) | Implemented |

A client parsing a ClaimResponse that carries none of the explicit outcome signals
gets an error rather than a silent mis-parse — the parser never infers an outcome
from an ambiguous shape.

An `approved` result may also carry `Partial:true` (+ `Disposition`, the payer's own
disposition/display text): X12 306's `A2` means "Certified – partial", so a
reviewActionCode `A2` that arrives WITH an authorization number parses as an approval,
not a denial — `Partial` distinguishes it from a full `A1` certification. `A2` without a
number still parses as `denied` (the observed reference-payer shape above); `A3` is
always an unconditional denial. No producer on this network emits `A2` with a number
today, so a partner should not expect to observe `Partial:true` from this network's
current deployments — the field exists for spec-conformant callers/payers that do.

### 7a.3 One call vs. the manual leg-by-leg path

The Go SDK runs the whole sequence in one call. This drives `MBR-D-UC04` (a `G0151`
home-health PT order) — the reference payer pends this family on first submit, so this
call returns `pended`, not `approved`; §7b resumes it:

```go
res, err := id.RunPriorAuth(ctx, httpClient, endpoints, payer, shnsdk.PriorAuthRequest{
    Member: "MBR-D-UC04", DOB: "1958-12-19", Family: "Okereke",
    ProcedureSystem:  "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets",
    ProcedureCPT:     "G0151",
    ProcedureDisplay: "Services of a qualified physical therapist in the home health setting, each 15 minutes",
    DiagnosisICD10:   "I63.9",
    ProceedOnNotCovered: true,
})
// res.Outcome == "pended", res.Resume != nil (resume via ResumePriorAuth, §7b, to reach "approved")
```

To have the coverage check carry your own records, set `Patient` (your Patient resource for the
member, whose id is the member id) and `Coverage` (your Coverage search result: a searchset
Bundle with the member's Coverage and the payor Organization it names) together, and `NPI` (the
ordering practitioner, sent as `userId` `Practitioner/<NPI>`; required with records). The check is
then built with `BuildCRDRequest` and names no FHIR server. `Hook` chooses its hook:
`order-sign` (the default: the order is signed and goes on to prior authorization) or
`order-select` (the order is still being chosen; the request selects it). Without `Patient` and
`Coverage` the older order-select request is sent, and `Hook` must be empty.

Note there is no `Clinical` field set. The reference payer's verdict for this family is
a function of the order's HCPCS code, not of the DTR answers, and `RunPriorAuth` only
auto-fills the one worked-example questionnaire it ships fixture logic for
(`SupportedQuestionnaireCanonical`, the CPT-coded lumbar-MRI questionnaire from an
earlier worked example — never something a real payer's own questionnaire canonical
matches). For every other canonical — including every questionnaire the reference payer
itself advertises — `RunPriorAuth` submits an honest **zero-answer
`QuestionnaireResponse` shell** (`status: "in-progress"`, no `item[].answer`) naming
exactly the canonical fetched, rather than inventing clinical content. A production
integration puts real fill logic — a clinician or an operated SDC `$populate` engine —
behind that step (see `docs/PREVIEW.md` §3a).

A non-Go participant (or a Go participant that needs to inspect/modify an intermediate
resource) drives the same three legs **manually** using the exported SDK builders/
parsers as the escape hatch — each `→` below is one originate round-trip (§6.1, §7),
the build/parse calls bracket the leg:

```
# Leg inputs (built once from the dev-visible order):
srJSON  = BuildServiceRequestCoded(system, code, display, icd10, "Patient/MBR-D-UC04")
covJSON = BuildCoverage("Patient/MBR-D-UC04", "MBR-D-UC04")   # 2nd arg = the BARE member id
                                                                # (the urn:shn:coverage MB identifier
                                                                #  value); a "Coverage/…" reference is
                                                                #  refused

# LEG 1 — CRD
crdReq            = BuildConformantOrderSelectRequest(srJSON, covJSON, "Patient/MBR-D-UC04")   # deprecated: see BuildCRDRequest below
crdResp           ← route(crd-order-select / crd-order-select → crd-cards, crdReq)
obs               = ParseCRDResponse(crdResp)    # every order the payer returned, every coverage-information value, exactly as sent
cov, ok           = obs.Primary()                # cov: CardCoverage. !ok ⇒ no coverage information; !cov.PARequired() ⇒ no-pa STOP; cov.Covered=="not-covered" ⇒ STOP
canon             = cov.Questionnaires[0]        # DTR canonical (present when cov.NeedsDTR())

# LEG 2 — DTR
dtrReq   = BuildQuestionnairePackageParameters("2.0", {Coverages: [covJSON], Orders: [obs.Orders[0].Order],
                                                      Questionnaires: [cov.Questionnaires[0]],   # |version kept
                                                      Context: <the payer's coverage-assertion-id>})
dtrResp  ← route(dtr-questionnaire-fetch / dtr-questionnaire-fetch → dtr-questionnaire,
                 frame(operation=questionnaire-package, dtrReq.Body))   # ONLY if the payer declares requestFrames "v1op" (§6.3)
         # a payer without "v1op": dtrReq = BuildQuestionnaireFetch(canon) (the deprecated older request), sent with no operation header
         # — a Smart Gateway payer refuses it (400, "names no operation"); every routable gateway payer declares "v1op"
qJSON    = ExtractQuestionnaireFromPackage(dtrResp)  # DTR-fetch returns a $questionnaire-package Bundle
url      = ParseQuestionnaireURL(qJSON)          # MUST equal canon (canonical-substitution guard)
qrJSON   = FillQuestionnaire(qJSON, clinical, qrContext)     # ONLY valid when url == SupportedQuestionnaireCanonical;
                                                              # otherwise BuildQuestionnaireResponseShell(qJSON, qrContext)
                                                              # — an honest zero-answer shell, never invented content

# LEG 3 — PAS
bundle   = BuildConformantClaimBundle(ConformantClaimInputs{QR: qrJSON, SR: srJSON, PatientRef: "Patient/MBR-D-UC04", CoverageRef: "Coverage/MBR-D-UC04", MemberID: "MBR-D-UC04", Corr: corrID, Created: now})
pasResp  ← route(pas-claim / pas-submit → pas-response, bundle)
result   = ParseClaimResponse(pasResp)           # → {Outcome, PreAuthRef, ValidUntil}
```

Each `route(...)` is the §7 originate sequence with that leg's `transactionType` /
request `operation` / response `operation`; the `payloadHash`-bound token is minted per
leg. `PreAuthRef` on an approved outcome is the reference payer's own authorization
number, shape `AUTH-NNNN`.

**CRD request and response builders.** `BuildCRDRequest(CRDRequestInputs{…})` builds
the CRD request from your own records: the hook your workflow fires (`order-select`,
`order-sign` or `order-dispatch`) and that hook's context, your `Patient` (required),
and your Coverage search result for the patient (a `searchset` Bundle, or `null` when
you hold none). Every resource travels as your exact bytes, and the request names no
`fhirServer` and no `fhirAuthorization`. CRD 2.2.1's request model marks both `1..1`;
the network omits them by design: the payer answers from the context and prefetch, and
the network never hands a payer a route into the provider's system. For an order type
whose patient element is `patient` (`NutritionOrder`, `VisionPrescription`) that element
names your `Patient`. It replaces the deprecated
`BuildConformantOrderSelectRequest` / `BuildConformantOrderDispatchRequest`, which still
send an id-only Patient. A payer participant answers with `BuildCRDResponse(line, …)`:
the order returned in an `update` system action carrying the coverage-information
extension (with the payer's own `coverage-assertion-id`), and `cards: []` unless it
supplies cards for a person to read, each with a `source.label` and `source.topic`.
`CheckCDSHooksResponse(body, line)` certifies any CDS Hooks response against the CDS
Hooks 2.0 response rules and the CRD card rules for the line, and lists every
violation; `CDSHooksRules()` is its rule table with the specification text each rule
enforces. Payer gateways relay the requested hook and a Coverage search result as sent
from gateway v0.44.0; until your payer's gateway runs it, keep the deprecated request
builder.

### 7a.4 What the network changes in a CRD or DTR message

From Smart Gateway v0.44.0, a CDS Hooks or `$questionnaire-package` message reaches the other
participant as its author sent it. The payer's answer comes back byte for byte, and the
hook is never changed. The only changes are the edits below. Each is made on the
message's own bytes; everything else in the message is unchanged. By default a provider's
gateway makes only the first of them to a request its EHR sends: the prefetch, Coverage and
Patient edits are the provider's opt-in (`ENRICH_NATIVE_REQUESTS=true` on its gateway), and
without it the request is carried as the EHR sent it. A Coverage a request leaves out is
read from the provider's system of record either way, only to choose the payer.

| Edit | Made by | What changes |
|---|---|---|
| Callback removed | the provider's gateway, on a CDS Hooks request from the EHR | `fhirServer` and `fhirAuthorization` are removed. The payer never gets a route or a credential into the provider's systems. |
| Prefetch obtained (opt-in) | the provider's gateway, on a CDS Hooks request from the EHR | An advertised prefetch key the EHR left out is added from the provider's own system of record: the Patient as read, a search as a `searchset` of the records exactly as returned (`urn:uuid:` entry addresses, no server links), `null` for no match. Nothing is made up. |
| Coverage obtained (opt-in) | the provider's gateway, on a `$questionnaire-package` request from the EHR | One `coverage` parameter is appended from the provider's system of record, only when the request carries none. |
| Patient obtained (opt-in) | the provider's gateway, on a `$questionnaire-package` request from the EHR | One `referenced` parameter holding the provider's own Patient record is appended, only when the request carries no Patient for the bound patient and the provider's system of record holds the patient under the id the request names. |
| Payer identity mapping | the payer's gateway, on the request to its payer (only when configured) | Only the payer identifier strings of each Coverage, and of a PAS Claim's insurer when it names the payer. |

- **Signatures.** A signature inside the message (`Bundle.signature`, `Provenance.signature`, a
  `Signature` element) travels untouched. An edit that would change signed content is refused
  with `422 signed content cannot be edited`.
- **No transport signatures.** HTTP-level signatures (signed header fields, a detached JWS)
  are not carried. Each Smart Gateway terminates HTTP, and a message frame (§6.3) carries only
  `Content-Type`, `contractVersion` and `operation`. A participant that needs an end-to-end
  signature signs inside the payload.
- **Hooks and services.** The payer's gateway sends each request to the CDS service the
  payer's own `/cds-services` listing offers for the request's hook. A hook the payer does not
  offer is refused before anything is sent, with `422` and the hooks it does offer:
  `{"error":"payer offers no CDS service for hook order-select","offered":[…]}`.
- **Answers.** The payer's gateway checks a CDS Hooks answer against the CDS Hooks response
  rules; at `strict` it refuses (`502`) one that breaks them, and below `strict` it relays it
  as sent (recorded as a finding at `observe`). It never repairs one. The provider's gateway
  returns the answer body exactly. The success media type is still the gateway's own:
  `application/json` for CDS Hooks, `application/fhir+json` for a questionnaire package.
- **Member id limitation.** `context.patientId`, and the patient references a request binds
  (each order's subject, each Coverage's beneficiary), must use the patient's network member
  id. A provider's gateway that has opted in obtains prefetch only when its system of record
  names the patient by that id. When the system names the patient differently, a request
  that leaves out `coverage` is refused (`422`); one that leaves out `patient` is refused the
  same way at `strict` and sent without it below `strict`; history keys are left out. Without
  the opt-in nothing is added, and the Coverage read to choose the payer is found under the
  system's own Patient id.
- **Gateway-originated requests.** A request that a provider's gateway builds for its own
  workflow names the patient by the member id. It changes the system of record's `Patient.id`
  and the patient reference on each carried record's patient path, and nothing else; the
  change is checked byte for byte.
- **Framed questionnaire requests.** A provider's gateway sends a `$questionnaire-package`
  request only as a framed operation (§6.3). A payer whose registration does not declare
  `"v1op"` therefore receives no questionnaire requests from a v0.44.0 provider gateway, which
  answers its EHR `502 payer gateway does not support framed DTR operations (upgrade
  required)`. A self-registered payer that answers with this SDK's `Responder` must rebuild on
  SDK v0.50.1 or later, and re-declare its frames with `shn rotate` from that build
  (`--request-frames` to choose them) to receive framed questionnaire requests.

---

## 7b. Prior-authorization — pended → amend, and denied

UC-04 and UC-08 extend the §7a CRD→DTR→PAS three-leg sequence with new outcomes
on the PAS leg and, for UC-04, a second exchange. Authority is evaluated per leg
throughout (fresh correlation + `payloadHash`-bound token each leg).

### 7b.1 UC-04: exchange-1 PAS submit returns pended

The initial PAS submit (§7a, leg 3) returns a PAS response **Bundle** for
approval, denial or pending adjudication. Select its single `ClaimResponse` and
inspect the decision: `outcome=queued` or an A4 review-action code means pending.
A retained Task does not make a completed decision pending.

**Detect pended with `ParsePendedResponse`:**

```go
pended, needed, err := shnsdk.ParsePendedResponse(pasRespBytes)
// Check err first: malformed or ambiguous responses are rejected.
// pended==true ⇒ pending decision; needed carries requested Task.input items
// pended==false ⇒ continue to ParseClaimResponse with the same response bytes
```

Both parsers accept a Bundle containing exactly one ClaimResponse, or a bare
ClaimResponse returned by a payer polling read. Missing or multiple responses and
malformed entries return errors. `ParseClaimResponse` rejects a pending decision.

**Read what the payer asks for with `ParsePendedResponseDetail`.** It returns each
PAS pended-response Task exactly as the payer sent it (`PendedTask.Raw`) with the
facts it carries (identifiers, status, code, requester, owner, payer URL) and its
needs grouped by request line (`PendedItem`): `AttachmentCodes` (an
`attachments-needed` CodeableConcept), `QuestionnaireIDs` (a `questionnaires-needed`
Identifier, PAS 2.0.1) and `QuestionnaireContexts` (a `questionnaire-context` string,
PAS 2.1.0 and 2.2.1), with the line number from `extension-paLineNumber` (2.0.1,
2.1.0) or `extension-serviceLineNumber` (2.2.1). An input in any other form is
reported in `PendedTask.Nonconformant` with its exact value, never dropped and
never converted. The reference payer sends its questionnaire as a
`valueCanonical` and its payer URL as a `valueString`; both are reported there. A
Task coded from another code system (for example a CDex data request) is kept
aside in `OtherTasks`.

The `needed` slice `ParsePendedResponse` returns flattens the same needs
(`[]NeededItem{Code, Display}`): each attachment code, questionnaire identifier
value and questionnaire context, then each nonconformant value that has text
(the payer URL excluded). `Display` is the coding's display, else the input
type's display or text. No questionnaire content or clinical answer is inferred.
A hermetic/local mirror names the questionnaire its own seeded adjudication asks
for, as a questionnaire identifier whose value is that questionnaire's canonical.

**Item trace numbers.** The SDK's PAS submit and update builders write one
`Claim.item.extension:itemTraceNumber` per item (system `urn:shn:pas:item-trace`,
value `<correlation>.<item sequence>`), which PAS allows at 2.0.1, 2.1.0 and 2.2.1.
A payer echoes it on the matching `ClaimResponse.item`, so a later answer or an
inquiry is matched by request line. `shnsdk.Responder` echoes it too.

### 7b.1a Pended responses a payer builds (the PAS Task)

A pended PAS response carries a Task for each kind of information the payer asks
for. `shnsdk.BuildPendedTasks(line, …)` builds it and
`shnsdk.BuildPendedClaimResponseAtLine(line, …)` builds the whole pended response
(the `ClaimResponse` with one A4 item per request line asked about, then the
Tasks). The facts below are read from the published PAS packages
(`StructureDefinition-profile-task`, `CodeSystem-PASTempCodes`,
`ValueSet-PASTaskCodes`) and the IG's example Task
(`AdditionalInformationTaskExample`).

| Element | 2.0.1 | 2.1.0 | 2.2.1 | Who supplies it |
|---|---|---|---|---|
| `meta.profile` | `…/profile-task\|2.0.1` | `\|2.1.0` | `\|2.2.1` | the builder |
| `identifier` 1..* | required | required | required | the payer (`PendedTaskInputs.Identifier`, system and value) |
| `status` 1..1 (HRex task status: requested, accepted, rejected, in-progress, failed, completed, on-hold) | required | required | required | the payer |
| `intent` | `order` | `order` | `order` | the builder |
| `code` 1..1 (PASTaskCodes) | `attachment-request-code` or `attachment-request-questionnaire` | same | same | the builder, from the kind of need; both kinds give two Tasks |
| `for` 1..1 | required | required | required | the request's `Claim.patient` |
| `requester`, `owner` (identifier only) | **1..1** each ("Payer ID") | 0..1 ("Provider ID") | 0..1 ("Provider ID") | the payer |
| `reasonCode` 1..1 | PASTempCodes `priorAuthorization` | same | same | the builder |
| `reasonReference` 1..1 | Reference(PAS Claim) | same | same | the request Claim (its `fullUrl`) |
| `input:PayerURL` 1..1 | `payer-url`, `valueUrl` | same | same | the payer (absolute http(s) URL) |
| attachments | `attachments-needed`, `valueCodeableConcept` (LOINC class ATTACH or X12 755) | LOINC | LOINC (`valid-hl7-attachment-requests`) | the payer |
| questionnaires | `questionnaires-needed`, `valueIdentifier` | `questionnaire-context`, `valueString` | `questionnaire-context`, `valueString` | the payer |
| line number (1..1 on each need) | `extension-paLineNumber`, `valueInteger` | `extension-paLineNumber`, `valueInteger` | `extension-serviceLineNumber`, `valuePositiveInt` | the item sequence |

Profile invariants, enforced by the builder: a Task coded
`attachment-request-code` has an `attachments-needed` input; a Task coded
`attachment-request-questionnaire` has a `questionnaires-needed` input at 2.0.1
and a `questionnaire-context` input at 2.1.0 and 2.2.1.

`requester` and `owner`: PAS 2.0.1 describes both as "Payer ID"; 2.1.0 and 2.2.1
describe both as "Provider ID - only send the identifier"; the IG's example Task
sends the same NPI identifier as both, at all three lines. The builder therefore
never derives them: the payer supplies both.

A missing or malformed fact is a builder error; nothing is invented. The Task a
payer sent is never rebuilt by the network or the SDK. The CDex data-request Task
(`BuildCDexTaskDataRequest`) is a separate contract with its own profile and codes.

**`shnsdk.Responder`.** A pended `PASDecision` carries `PendedItems`,
`TaskIdentifier`, `TaskStatus`, `TaskRequester`, `TaskOwner` and `PayerURL` (the
Responder answers at PAS 2.0, where requester and owner are required).
`PayerURL` may be empty when the Responder was built with
`ResponderConfig.PublicBaseURL`, which is then used. A pended decision missing a
fact is refused with `500`, naming it. `PASDecision.NeededItems` is deprecated: a
bare string does not say whether an item is an attachment or a questionnaire, so
a pended decision that sets only `NeededItems` is refused with `500`.
`BuildPendedResponse` / `BuildPendedResponseAtLine` are deprecated and return
`ErrPendedResponseNeedsTaskFacts`.

### 7b.2 UC-04: exchange-2 ClaimUpdate (amend)

Exchange-2 is a second PAS leg using the `pas-claim-update` transaction type. The
provider sends the supplemental evidence, then the payer adjudicates and returns the
final PAS response Bundle containing the `ClaimResponse` and its referenced resources.

Wire contract:

| Field | Value |
|---|---|
| `transactionType` | `pas-claim-update` |
| Request `operation` | `pas-update-submit` |
| Response `operation` | `pas-update-response` |
| Response frame | `payer-coverage` |

**The payer's system decides; the payer gateway's pend ledger records and never
gates.** A Smart Gateway serving a payer forwards every amendment to its payer's system
and relays the answer, whatever its own ledger holds for the authorization (no pend
recorded there, a decision already recorded, or another amendment still with the payer).
The answer checks at the participant's chosen conformance level still apply. The ledger
records the answer only onto the authorization it already holds for the same requester;
anything else is relayed and not recorded. A record the gateway cannot write after the
payer answered does not withhold the answer: it is relayed, and the gateway reports the
failure to its operator. The payer's own `409` (for example a version conflict while
it resolves the same claim) is relayed as its answer; whether to resend is the
requester's decision.

The update Bundle payload carries :

- `Claim` with `related[]` referencing the **original submit correlation identifier**
  (this binds the amendment to the pended claim; the payer's system decides what an
  update whose `related[]` names no authorization it holds means, and the reference
  payer refuses one whose prior it never stored).
- The **unchanged** `QuestionnaireResponse` and `ServiceRequest` from exchange-1.
- An operative **`DiagnosticReport`** (US Core Note profile) — the new clinical
  evidence.
- A **`Provenance`** attributing the DiagnosticReport to its source (the
  payer **rejects** supplemental data without Provenance; `ResumePriorAuth` validates
  that `supp.ProvenanceAgent` has a recognized holder/NPI system and nonblank value before calling any builder, so you meet
  that requirement as a named precondition rather than a cryptic three-legs-deep payer rejection).

**One-call path** (`ResumePriorAuth`):

```go
// resume is the PriorAuthResume written by RunPriorAuth when outcome=="pended".
// supp carries the supplemental DiagnosticReport facts + the required ProvenanceAgent.
// SupplementalReport is a plain struct — build it yourself; shn-sdk ships no fixture
// constructor for it. Its own CPT/display need not match the order's HCPCS code — G0151
// is used here only because it is this worked example's own advertised family. The
// G0151 family's resolution itself is NOT evidence-driven — the payer re-pends and its
// own pend-resolution timer is what later flips the same claim to a decision,
// independent of this report's specific content. Provenance is required because FR-32
// (SHN's own rule) says supplemental data must carry attribution — not because either
// payer's verdict reads it.
supp := shnsdk.SupplementalReport{ReportID: "dr-uc04-operative", CPT: "G0151", Display: "Home health services"}
supp.ProvenanceAgent = shnsdk.ProvenanceIdentifier{System: "http://smarthealth.network/ids/holder", Value: "acme-7f3a"} // required
res, err := id.ResumePriorAuth(ctx, c, ep, payer, resume, supp)
// res.Outcome == "pended" against the reference payer: it accepted the amendment and is
// still holding the request. res.Resume carries the continuation.

// Ask for the decision, with your own records for the member, the coverage, the
// requesting provider and the payer.
var records shnsdk.PASInquiryRecords
got, err := id.Inquire(ctx, c, ep, payer, *res.Resume, records)
// got.Outcome is the payer's own latest word: "pended" while pended, "approved" with
// got.PreAuthRef (shape AUTH-NNNN) once it has decided.
_ = got.PreAuthRef

// Or let the client ask for you, for a bounded time you state:
//   id.ResumePriorAuthWith(ctx, c, ep, payer, resume, supp,
//       shnsdk.WithWait(20*time.Second), shnsdk.WithInquiryRecords(records))
```

`ResumePriorAuth` validates `supp.ProvenanceAgent` before touching the wire — an
absent agent returns an error immediately rather than a cryptic payer rejection.

**What an amendment does, and what decides — measured against the reference payer with no
gateway in between.** The amendment CARRIES evidence. It does not decide. The reference
payer resolves a pended request by its own internal timer and by nothing else, and an
amendment that lands while the request is pended is answered `200` with a fresh pend (`A4`)
and that timer re-armed. `ResumePriorAuth`'s bundle carries a `Provenance` entry, no Da Vinci
PAS `infoChanged` item extension, and `Claim.related[0].claim` keyed by `identifier` (never
`reference`); none of those choices changes the payer's verdict.

**How the decision reaches you.** By asking. `Identity.Inquire` (or `POST /Claim/$inquire`
through your gateway's ingress) is built from the request you sent and the answer you
received, plus your own records, and reports the payer's own latest word — pended while
pended, decided once decided. Your gateway never holds a leg open waiting for a better
answer, and it never re-queries on your behalf unasked.

If you would rather your own call waited a little, `RunPriorAuthWith` / `ResumePriorAuthWith`
take `WithWait(d)` (with `WithInquiryRecords`), and the originator routes take
`?wait=<seconds>`. Both make a small, bounded number of inquiries and then stop; reaching
that bound is not an error — the result is "pended, and here is the continuation". The
default is no wait, because how long you are willing to wait is yours to state.

**What this bounded wait is not.** Prior Authorization names **Subscription** as the way a
requester learns a decision made later, and states it as a `SHALL`; an inquiry is the
permitted manual status check, and at the 2.2.1 line the guide says it `SHOULD NOT` be used
while waiting for final results. This network offers no notification path yet, so the
bounded wait stands one in. For a payer that decides in seconds it is convenient; for one
that decides in hours or days, keep the continuation and inquire when you are ready.

**Proven scope.** `Identity.ResumePriorAuth` is proven live against the reference payer: the
amendment is accepted and answered, and the payer's answer reaches you as the payer wrote it.
The determination that follows is proven through `Identity.Inquire` on the same handle
(`test/tworilive/sdkresume_test.go`). An earlier version of this section said the resume
itself resolved the pend on the mirror and got `422 "amendment still insufficient"` against a
live payer. Both halves were wrong: the mirror's resolution came from the same timer the real
payer uses, and that `422` was minted by our own gateway's deleted poll gate — the reference
payer never sends it.

**Manual leg-by-leg path:**

For a non-Go participant or a Go participant that needs to inspect intermediates:

```
# Build the supplemental FHIR resources:
drJSON   = BuildDiagnosticReport(reportID, patientRef, cptCode, display)
provJSON = BuildProvenanceWithIdentifier("DiagnosticReport/"+reportID, provenanceAgent, now)

# Build the update bundle (Claim.related[] → originalCorrelationID):
updateBundle = BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{
    QR: qrJSON, SR: srJSON, DiagnosticReport: drJSON, Provenance: provJSON,
    PatientRef: patientRef, CoverageRef: coverageRef, MemberID: memberID,
    Corr: updateCorrID, OriginalCorr: originalCorrID, Created: now})

# Route as pas-claim-update (single originate round-trip, §7):
updResp ← route(pas-claim-update / pas-update-submit → pas-update-response, updateBundle)

# Parse the update response:
result = ParseClaimResponse(updResp)   # → {Outcome:"approved", PreAuthRef, ValidUntil}
```

`originalCorrID` is the correlation identifier from exchange-1 — the value the
envelope carried when the submit leg was routed (the payer's ledger key for the
pended claim). The `PriorAuthResume` struct persists it as `OriginalCorrelationID`.

The payer answers an amendment with its own decision: approved, denied, or pended
again (with the Task of what it still needs). `ResumePriorAuth` returns that
decision; a pended result carries a usable `Resume`. `shnsdk.Responder` answers an
amendment with its `Adjudicator`'s decision likewise: an approval or a denial
decides the claim (a later amendment of it is refused with `409`), and a re-pend
keeps it pended for a later amendment.

### 7b.2a Following up a pended decision: `$inquire`

A pended decision is followed up with a Da Vinci PAS inquiry (`Claim/$inquire`),
transaction type `pas-claim-inquire`, request operation `pas-inquire`, response
operation `pas-inquire-response` (frames `provider-tpo` → `payer-coverage`, contract
`pa.pas`). **The inquiry leg is served by Smart Gateway v0.45.0 and later**; an older
payer gateway refuses it. (Framed DTR operations, §6.3, need Smart Gateway v0.44.0
and later.)

- **The request.** `shnsdk.BuildPASInquiryBundle(line, …)` builds the inquiry: a
  collection `Bundle` (identifier and timestamp; no `entry.request`, `response` or
  `search`) whose first entry is the inquiry `Claim` (status, type,
  `use=preauthorization`, patient, created, insurer, provider — an Organization or
  PractitionerRole — priority, insurance, and the items asked about with their trace
  numbers), followed by your Patient, Coverage, provider and insurer records, each
  embedded as your exact bytes. The payer matches on the member identifier plus the
  provider identifier, so the Patient must carry the member id as an identifier with
  a system (typed `MB` at PAS 2.1.0, the one line that slices it). The inquiry `Claim.identifier` is the inquiry's own trace number (required
  from 2.1.0). Authorization and administration reference numbers are item
  extensions at every line: PAS 2.2.1 also declares `Claim`-level slices for them,
  but both extension definitions allow only item contexts, which validators
  enforce.
- **The answer.** A `Bundle` of `ClaimResponse`s at 2.0.1 and 2.1.0; a `Parameters`
  with `return` Bundles at 2.2.1. Your gateway relays it to you exactly as the payer
  wrote it, with the payer's own media type, in either shape.
- **Through the Da Vinci ingress.** A system connected to a gateway's Da Vinci
  ingress sends the inquiry to `POST /Claim/$inquire` on its own gateway, the same
  way it sends a submission to `POST /Claim/$submit`. The request is carried to the
  network as sent, bound to the one member every patient reference in it names — a
  Bundle naming two members is refused — and routed by the Coverage it carries; a
  Coverage naming no payer the gateway can resolve is refused rather than defaulted.
  No SHN state is involved: the inquiry names the authorization, so your system is
  the only thing that has to remember it.
- **Who certifies it.** Neither gateway profile-validates your inquiry or the
  payer's answer; both travel as written. The payer's own system certifies the
  inquiry it receives, so a request that does not meet the prior-authorization
  profile for its line comes back as that payer's refusal, not the network's. This
  matters for one cardinality in particular: PAS 2.0.1 requires the inquiry Claim
  to name at least one item (`Claim.item` 1..\*) and 2.1.0 and 2.2.1 do not
  (0..\*), so an inquiry by authorization number alone is carried at every line and
  answered — or refused — by the payer.
- **The continuation handle.** A pended `PriorAuthResult.Resume` carries
  `Continuation` (`PriorAuthContinuation`): the PAS line, the payer holder, the
  submitted Claim's identifiers, type and priority, the member id, the provider NPI,
  the items (sequence, product code, service date, trace number, and the numbers the
  payer gave), and the payer's `ClaimResponse` identifiers and authorization
  reference. It holds no clinical content. `NewPriorAuthContinuation` records it
  from a request and its answer.
- **`Identity.Inquire(ctx, client, endpoints, payer, resume, records)`** builds the
  inquiry from the handle and your `PASInquiryRecords`, sends it to the payer the
  handle names, matches the answer to your request by item trace number (or the
  payer's identifiers), and returns the payer's decision. No match or more than one is
  an error (`ErrInquiryNoMatch`, `ErrInquiryAmbiguous`); a handle without
  continuation facts is refused (`ErrNoContinuation`).
- **Waiting.** `RunPriorAuthWith` / `ResumePriorAuthWith` accept `WithWait(d)` and
  `WithInquiryRecords(records)`. The default is no wait (`RunPriorAuth` and
  `ResumePriorAuth` never wait). With a wait, the first inquiry runs 2 s after the
  pend and later ones back off (4 s, then 5 s steps); at most 6 inquiries are sent
  (`MaxPriorAuthInquiries`) and the wait is capped at 30 s (`MaxPriorAuthWait`). The
  cap is the schedule's own reach rather than a round number: the sixth and last
  inquiry falls due at 26 s, so a longer bound would only hold your call open with no
  inquiry left to make. Reaching the bound returns the pended decision, not an error;
  cancellation follows the context. A decision you expect hours from now is not a
  longer wait — it is a continuation you keep and use when you are ready.
- **Keeping the handle.** The continuation is the durable result of a pend: it is what
  you hold between the payer's pend and the payer's decision, and it is yours to
  persist. A participant driving the SDK keeps `PriorAuthResult.Resume` (and with it
  the `PriorAuthContinuation`) in its own system; it holds no clinical content, so it
  can be stored beside your own record of the request. If instead you use a gateway's
  own originator routes, that gateway keeps the continuation for you: it survives a
  restart when the gateway is configured with a database, the answer says which
  (`continuationDurable`), and an id a gateway minted and could not keep is refused
  with `410` — never reported as an id that never existed. See CONFIGURATION.md in the
  gateway module.
- **Payers.** `shnsdk.Responder` answers an inquiry when its `Adjudicator` also
  implements `InquiryAdjudicator` (`Inquire(PASInquiry) (PASDecision, error)`),
  with that decision as a PAS response; otherwise it refuses with `501`. An inquiry
  does not change the Responder's pended-claim state.
- **Standards note.** Prior Authorization's mechanism for learning a decision made
  later is **Subscription**, stated as a `SHALL`, and **this network does not offer
  it**: no gateway on this path subscribes, notifies, or accepts a subscription, and
  no partner integration should be built expecting one. PAS 2.2.1's guidance on the
  inquire operation (`spec-9`) accordingly says a client `SHOULD NOT` use it to wait
  for final results, while PAS 2.0.1 says the client can use it to query for the
  final result. What this network offers is the inquiry: the manual status check, made
  when you choose to make it, plus an opt-in bounded wait that stands in for the
  notification path — never a claim that the bounded wait is what the guide
  prescribes.
- **Updating an authorization the payer has already decided.** Not supported here. An
  amendment is for a request the payer is still holding: `shnsdk.Responder` refuses an
  update to a decided claim with `409` and its reason, and a payer that behaves
  otherwise is stating its own policy — the reference payer, for one, accepts such an
  amendment, pends the authorization again and later issues a **different**
  authorization number that replaces the earlier one, so a requester that amends a
  decided authorization must re-read the number rather than assume the one it holds.
  To change a decided authorization, submit a new request.

### 7b.3 UC-08: denied PAS response

`MBR-D-UC08`'s order (`J3490`, an excluded-service family) already comes back
**not covered** on the CRD card — with `ProceedOnNotCovered` set, the flow submits the
PAS claim straight through for the payer's formal determination, with **no DTR leg** at
all (a not-covered card carries no questionnaire).

A denial is a **bare `ClaimResponse`** (not a Bundle), `outcome=complete`, with the
Da Vinci PAS reviewActionCode extension carrying a code. X12 306 defines `A3` as "Not
Certified" — the conformant denial code, and the one this network's own PAS producer
emits, including the hermetic mirror (`cmd/payermirror`, what `make up` boots). A
`role=payer` holder that native-forwards to the real reference payer (the live preview
network's `conformance-payer`) instead returns `"A2"` on this leg with display "Not
Certified" — a code/display self-contradiction in that reference implementation, not a
different conformant code; this network only ever PARSES it, never emits it. The parser
accepts both `A3` and this observed `A2` shape as a denial signal. There is **no**
`preAuthRef`. The rationale is in `ClaimResponse.disposition` when the payer gave one,
and any notes the payer supplies (an appeal window, a review instruction) are in
`ClaimResponse.processNote[].text`. A payer that gives neither sends neither: the network
adds no rationale or note of its own.

**Parse with `ParseClaimResponse`:**

```go
result, err := shnsdk.ParseClaimResponse(claimRespBytes)
// result.Outcome == "denied"
// result.Denial.ReasonCode == "A2"
// result.Denial.Rationale == "…" (ClaimResponse.disposition, else the review-action display)
// result.Denial.AppealNote == the payer's own notes, if any (ClaimResponse.processNote[].text)
```

`ParseClaimResponse` navigates
`item[].adjudication[].extension[reviewAction].extension[reviewActionCode]` for the
A3/A2 code. It fails loud on an ambiguous shape — an outcome that is neither
`approved` (non-empty `preAuthRef` + `outcome=complete`) nor `denied` (reviewActionCode
A3, or the observed reference-payer A2 denial shape) returns an error rather than a
silent mis-parse.

**Response shape summary:**

| Outcome | Response shape | Key field |
|---|---|---|
| `approved` | PAS response `Bundle` | Completed ClaimResponse with an explicit authorization number |
| `pended` | PAS response `Bundle` | ClaimResponse `outcome=queued` or reviewActionCode `A4`; Task inputs describe requested items |
| `denied` | PAS response `Bundle` | `outcome=complete` + reviewActionCode `A3` (or the observed reference-payer `A2` denial shape); no `preAuthRef` |

Use `ParsePendedResponse` first (decision check), then `ParseClaimResponse` on
the same bytes when the decision is not pending — this is the dispatch `parsePASOutcome` implements internally
and `ResumePriorAuth` / `RunPriorAuth` call for you.

---

## 8. FHIR conformance obligations

### 8.1 Two-gate posture

All FHIR resources exchanged through the network must conform to their
applicable IG profiles. The network enforces a **two-gate** posture:

1. **Runtime validation** — at the enforcement levels that run checks, a gateway
   validates FHIR resources on egress (before sealing) and on ingress (after
   decrypting) with its FHIR `$validate` endpoint. What each check covers:

   - A resource is checked against the profiles it declares in its own
     `meta.profile`, and otherwise against its base FHIR R4 definition. A
     resource that declares no profile is checked for base R4 shape only, with
     no Da Vinci or US Core profile applied.
   - A few resources a gateway builds are checked against a named profile,
     whatever they declare: the DTR QuestionnaireResponse it sends, against the
     DTR QuestionnaireResponse profile of the leg's IG line; a
     QuestionnaireResponse it populates, against the base R4
     QuestionnaireResponse; and a PAS inquiry it builds, against the PAS inquiry
     request-bundle profile of the line.
   - Of a CDS Hooks request, the draft order is validated, and on `order-select`
     and `order-sign` the Coverage too; a CDS Hooks answer is checked against
     the CDS Hooks response rules.
   - A PAS request carried from the participant's own system (the `$submit`
     ingress) is not `$validate`d, by either gateway; only the content checks
     and the network rules below apply to it. A PAS submit or update a gateway
     builds from its participant's records is validated: the Bundle for base R4
     shape, and its QuestionnaireResponse attachments against the DTR profile.
   - From shn-gateway v0.55.0, a payer's answer to a leg a provider's gateway sends — the DTR
     `$questionnaire-package` and `$next-question` answers and the PAS
     ClaimResponse answers — is validated by the provider's gateway on ingress,
     at that gateway's level, against the IG lines the gateway supports: first
     the line the leg was sent at (the payer's declared line, or the gateway's
     own line when the payer declared none), then the lines the answer's
     versioned `meta.profile` names, then the lines its structural markers
     point to, then 2.2, 2.1 and 2.0. The answer is valid if it conforms to any
     of them and is refused only when it conforms to none; the finding records
     the line the leg was sent at and each line tried with its verdict, and a
     finding that the answer was valid only on another line is not a defect. If
     no line is valid and the line the leg was sent at, or a line the answer
     itself names, could not be checked (its validator was unavailable, or the
     gateway has none for it), the check is unavailable rather than a refusal
     for structure: recorded at `observe` and `structural`, refused at `strict`.
     Each line beyond the one the leg was sent at gets at most 2 seconds; one
     that does not answer in time is recorded as unavailable for that line. An
     answer that names a supported line the gateway has no validator for is
     recorded rather than refused at `structural`, because that line could not
     be checked. When the gateway has no validator for the line the leg was sent
     at, the first line it actually checks gets the same 2-second bound. At `strict`, an answer whose check cannot run (the validator
     is unavailable) is refused like any other such check. The exception is
     SHN's reference payers, identified by the payer identity the member's
     Coverage names (`urn:oid:2.16.840.1.113883.6.300` `00001`, `00300` and
     `00301`, and SHN's two bridging-demo payers under `urn:shn:demo-payer`,
     which front `00001`), answering a provider gateway that relays their bytes
     (`ORIGINATION_PROFILE` `provider-data` or `demo`): their answers are not
     validated, because their packages do not yet conform (the 2.0 reference
     payer's DTR package fails DTR 2.0.1). Judging a payer's confirmed line
     alone, more strictly, may be introduced later. Before shn-gateway v0.55.0,
     a provider gateway on the `provider-data` or `demo` lane validates no
     payer's answers.
   - Of coverage eligibility, both gateways validate the request and the answer:
     the answer the payer's gateway builds from its participant's records or,
     when the payer declares its own endpoint (`PAYER_ELIGIBILITY_URL`), the
     payer system's answer.

   A level applies only to what these checks cover: `strict` refuses a defect a
   check finds, and `structural` refuses broken structure a check finds. Neither
   checks a resource or a profile the list above leaves out.

   Whether those checks run, and what an invalid result *does*, is each gateway's
   own configured choice (`CONFORMANCE_ENFORCEMENT`), not something the network
   imposes. At `none` no payload conformance check runs and no finding is
   recorded; at `observe` — the default when the value is unset — every check
   runs, each defect is recorded as a conformance finding and the message is
   carried as sent, apart from the gateway's registered edits (§7a.4); at `structural`
   every check runs, a message whose structure is broken is refused and every
   other defect is recorded as at `observe`; at `strict`
   the message is refused and the refusal names the rule and the issues behind it.

   At `structural`, broken structure is: a missing required element, an element the
   resource does not define, a value of the wrong JSON type, a value that cannot
   be read (a malformed date, or a code outside a core FHIR code list such as
   `Claim.status`), a CDS Hooks answer that cannot be read or lacks a required
   member or has one of the wrong type, and a request or answer the gateway
   cannot read. A FHIR validation issue is read by the validator's own message id,
   and only two kinds of FHIR issue are recorded rather than refused: invariants,
   and a code outside its code list (a code the bound value set or code system
   does not contain, or a code system the validator cannot check, licensed ones
   included), as the validator's recognized code-list issues report it. Every
   other FHIR profile issue — cardinality, fixed and pattern values, slicing,
   extensions, lengths, any other terminology issue, or an issue the gateway
   cannot classify — and every fatal issue refuses at `structural`. Outside FHIR validation
   these are recorded, not refused: CDS Hooks summary length, topic and selection
   behavior, another patient in one message, and the content business rules
   (QuestionnaireResponse attestation, amendment provenance, the EOB decision rules,
   an unresolvable `Claim.insurer`, prefetch the participant's system cannot
   supply). A validator that cannot run is
   recorded at `structural`, not refused. On a PAS 2.1 or 2.2 request Bundle that
   declares the PAS request-bundle profile, the Claim entry may match either of
   two candidate profiles; when only a deeper rule fails the Claim's own profile,
   the validator's reports that the Claim matched neither candidate, and the other
   candidate's own requirements, are recorded as consequences of that deeper
   failure, not refused.
   Your obligation above is unchanged at every level: a gateway that relays your
   non-conformant resource has not accepted it, and the peer you sent it to may be
   configured to refuse it.

   Some checks refuse at every level, because none of them is a statement about
   a peer's conformance: authentication, authority (including a token
   presented with a request other than the one it was issued for), consent, the
   patient binding (each gateway identifies the member the request names by its
   own system), routing, replay, a repeated member name in any body, the contract line
   stamped on an answer's frame, and the check of a payload that gateway itself
   translated between IG lines. An answer the
   gateway cannot read is a conformance defect: refused at `structural` and `strict`,
   relayed as sent below them. An answer about another patient is refused at
   `strict` and relayed as sent below it.

2. **Da Vinci gap-report contract** — Da Vinci CRD/DTR/PAS-specific profile gaps
   are tracked in the network's conformance gap report (maintained upstream).

### 8.2 Profiles by transaction type

| Transaction type | Profiles |
|---|---|
| `coverage-eligibility` | `CoverageEligibilityRequest` / `CoverageEligibilityResponse` (US Core) |
| `crd-order-select` | Da Vinci CRD `CDSHooksRequest` / `CDSHooksResponse` |
| `dtr-questionnaire-fetch` | Da Vinci DTR `Questionnaire` |
| `pas-claim` / `pas-claim-update` | Da Vinci PAS `Claim` bundle / `ClaimResponse` bundle |
| `federated-query` | Da Vinci CDex `cdex-task-data-request` `Task` (request) / completed CDex `Task` whose `output` contains a US-Core searchset `Bundle` (`DiagnosticReport`/`DocumentReference` records, the facility's identity-binding `Patient`, one `Provenance` per record) (response) — CDex + HRex + US Core |
| `patient-dtr` | Da Vinci DTR `QuestionnaireResponse` |

### 8.3 Terminology

Codes (LOINC, SNOMED-CT, ICD-10-CM, CPT) must be validated against the
network's curated value sets or a terminology service. Do not synthesise or
hallucinate codes; the FHIR validation gate — not the implementation — certifies
conformance.

### 8.4 CapabilityStatements

Every SHN role publishes a `CapabilityStatement` at `GET /metadata` (FR-G45).
Participants implementing any of these surfaces must conform to the declared
IG canonicals and profiles.

- **Payer `/metadata`** — the CMS-0057 Patient Access API (PDex PA EOB)
  statement. `implementationGuide` carries the versioned PDex canonical
  matching the payer's declared `pa.pdex` contract line:
  `http://hl7.org/fhir/us/davinci-pdex/ImplementationGuide/hl7.fhir.us.davinci-pdex|2.1.0`.
  `rest[0].resource` declares `ExplanationOfBenefit` (`read`, `search-type`)
  against the `pdex-priorauthorization` profile.
- **Provider ingress `GET /metadata`** — the Da Vinci ingress statement for
  foreign EHR/CDS clients calling into the Smart Gateway's ingress edge.
  `implementationGuide` lists the versioned CRD/DTR/PAS canonicals this build
  speaks natively (matching its `pa.crd@2.0` / `pa.dtr@2.0` / `pa.pas@2.0`
  contract lines):
  ```
  http://hl7.org/fhir/us/davinci-crd/ImplementationGuide/hl7.fhir.us.davinci-crd|2.0.1
  http://hl7.org/fhir/us/davinci-dtr/ImplementationGuide/hl7.fhir.us.davinci-dtr|2.0.1
  http://hl7.org/fhir/us/davinci-pas/ImplementationGuide/hl7.fhir.us.davinci-pas|2.0.1
  ```
  `rest[0].resource` declares the two FHIR-REST operations the ingress edge
  actually serves: `Claim.$submit` (PAS) and `Questionnaire.$questionnaire-package`
  (DTR). **CRD is CDS Hooks, not FHIR REST** — it is named in
  `implementationGuide` and `implementation.description` only; its own
  discovery document is served separately at `/cds-services`. Version-specific
  endpoint codes for all three lines are published at
  `/.well-known/davinci-configuration` (§8.5).
- **Hub `GET /metadata`** — deliberately **IG-free and resource-free**: the
  Hub is the payload-blind routing plane (OWD-2), so its statement declares
  no `implementationGuide`, no profiles, and no `rest[0].resource` entries —
  only that a JSON service exists here and where its real surface is
  documented. The Hub's two routes (`POST /route`, `GET /transport-key`) are
  network wire contracts, not FHIR REST, and are described in
  `implementation.description` / `rest[0].documentation` instead.

### 8.5 Da Vinci well-known configuration (`GET /.well-known/davinci-configuration`)

The provider gateway's Da Vinci ingress edge (§8.4's provider-ingress
statement) also serves the HRex 1.2.0
[`.well-known/davinci-configuration`](http://hl7.org/fhir/us/davinci-hrex/StructureDefinition/hrex-wellknown)
document — plain TLS, no auth (HRex requires the document be readable without
mTLS; it names public base URLs only). It is absent (404) when the ingress
edge is disabled.

```
GET {provider-ingress}/.well-known/davinci-configuration
```

```json
{
  "identifier": { "system": "urn:shn:participant-id", "value": "acme-provider" },
  "endpoints": {
    "davinci_crd_hook_endpoint#2.0": "https://acme-provider.example.com/fhir",
    "davinci_dtr_qpackage_endpoint#2.0": "https://acme-provider.example.com/fhir",
    "davinci_pas_submission_endpoint#2.0": "https://acme-provider.example.com/fhir"
  }
}
```

- **`identifier`** (REQUIRED, 1..1) — namespaces the holder id under
  `urn:shn:participant-id`. SHN has no FHIR `NamingSystem` for participant ids
  yet, so a URN is used; additive to replace with a resolvable canonical
  later.
- **`endpoints`** — a `<code>#<major.minor>` → base-URL map (HRex endpoint
  values are operation **base** URLs; callers append the actual operation
  path, e.g. `/Claim/$submit`). Each code line derives from the ingress
  edge's own declared `contractVersions` (§2.3) — a build that natively
  speaks a contract earns the matching HRex code with zero new code once the
  contract-to-code mapping exists. Today's mapping:

  | Contract | HRex code |
  |---|---|
  | `pa.crd` | `davinci_crd_hook_endpoint` |
  | `pa.dtr` | `davinci_dtr_qpackage_endpoint` |
  | `pa.pas` | `davinci_pas_submission_endpoint` |

  `pa.pdex` has **no** HRex code here: the patient-access endpoint belongs to
  the payer edge, which has no config-pinned self base URL yet. Additive: a
  future deployment adds that config field and reuses this same builder with
  the `davinci_pdex_patient_endpoint#2.1` code.

### 8.6 Version-matched routing

At origination, the Smart Gateway selects — for every contract-mapped leg
(the `pa.crd` / `pa.dtr` / `pa.pas` families named in §8.4) — the highest
contract line **both** the originating build and the recipient share,
deterministically. Selection happens entirely at the originating gateway
edge; the Hub stays version-blind — it never inspects `contractVersion`,
which lives inside the seal (§6.3).

- **A silent recipient** (no `contractVersions` declared at all — the default
  for a holder that has never registered the field, §2.3) is not treated as
  incompatible: the leg routes at the originating gateway's own highest
  **declared** line for that contract (its `contractVersions` declaration — the
  build's default declaration when none is set — never the highest line the
  build could natively produce). Silence is not disagreement.
- **A non-empty declaration is exhaustive.** Once a recipient declares *any*
  contract-version tokens, that declaration is read as its complete
  capability across every contract, including ones it never mentions. A
  recipient that declares tokens but shares no line with this build for the
  leg's contract — or omits the contract entirely from a non-empty
  declaration — fails closed. This is deliberately **stricter** than the
  operator connectivity-check drift rule (FR-G46), which treats silence on
  one contract as "not drift": routing compares two parties' capabilities to
  decide whether a leg can run at all, drift compares two descriptions of the
  *same* endpoint after the fact.
- **Refusal is a legible `422`**, never the Hub's generic `502`/`"hub routing
  failed"`. It names the failing contract, the leg, and both parties'
  declared tokens; a duplicate declared token (admission validates shape only
  and tolerates duplicates by design — the same `messageFrames` precedent)
  collapses to one entry. Verbatim example — this build speaks `pa.pas@2.0`
  only, and `acme-payer` has declared `pa.pas@3.0` only:

  ```json
  {"error":"no shared contract line for pa.pas (leg pas-claim): this gateway speaks pa.pas@2.0; recipient \"acme-payer\" declares pa.pas@3.0 — no bridge available"}
  ```

- **A pended exchange pins its line at origination.** A PAS submit that
  returns `pended` (§7b) selects its contract-version line once, at the leg
  that ran to `PENDED`, and every resume leg (the ClaimUpdate amend, §7b.2)
  reuses that exact line — it is never re-selected. The pin lives beside the
  already-pinned `recipient` in the provider's in-memory pend state, not the
  durable exchange store — the store is metadata-only by its own invariant and
  gates nothing, so a routing decision cannot live there.
- **The frame stamp verifies the routed line**, not the reverse — see §6.3: a
  responder's framed 2xx answer carries the line it actually built at; the
  originator rejects a stamp that disagrees with the line it routed the leg
  to, and tolerates an absent one.
- **The foreign Da Vinci peer** (native-forward payer mode, `PAYER_DAVINCI_*`)
  is filtered by the same rule, sourced from the operator's declared
  `PAYER_DAVINCI_CONTRACT_VERSIONS` instead of the registry: a leg whose
  contract shares no line with the operator's declaration is refused
  **before** anything is sent to the partner — zero bytes leave the gateway.
  Leaving `PAYER_DAVINCI_CONTRACT_VERSIONS` unset leaves native-forward legs
  unfiltered (today's default). See the gateway's `docs/CONFIGURATION.md`.

**Native capability vs. the declared set — two separate axes (2026-08-11).**
"What a build *can* produce" and "what a deployment *advertises* it speaks" are
deliberately different sets, and only the second one routes:

- **Native set.** The Smart Gateway and this SDK build every PA contract at
  **three** lines — `pa.crd@{2.0,2.1,2.2}`, `pa.dtr@{2.0,2.1,2.2}`,
  `pa.pas@{2.0,2.1,2.2}` — plus `pa.pdex@2.1`. Each line's payloads are built
  against that line's own IG package pins and validated against a `$validate`
  lane loaded with *that line's* packages (one validator per line — a single
  server cannot host two versions of the same IG). `shnsdk.NativeContractVersions()`
  is the machine-readable list.
- **Declared set.** What a deployment publishes — in its registration /
  rotation `contractVersions`, its `CapabilityStatement`s' versioned
  `implementationGuide` canonicals, its `.well-known/davinci-configuration`, and
  the `/holders` feed peers select against (§1a, §2.3, §8.4, §8.5). It defaults
  to the canonical `2.0` line (`pa.crd@2.0`, `pa.dtr@2.0`, `pa.pas@2.0`,
  `pa.pdex@2.1`) and is operator-configurable to any **subset of the native
  set**. Declaring a line the build cannot produce is a configuration error that
  fails at boot, not a routing outcome; so is declaring a line with no
  `$validate` lane configured for it. All four surfaces read one accessor, so a
  deployment cannot declare one set locally and a different one to its peers.
- **Selection routes on the declared sets** — the highest common line between
  the *originator's* declared set and the *recipient's* declared set, exactly as
  the rules above describe. Native capability beyond the declared set is
  invisible to selection.
- **Honoring an inbound claim is wider than the declared set.** A receiver
  honors a request-frame `contractVersion` claim (§6.3) whenever it can both
  natively build and validate at that line — *native ∩ laned*, which is a
  superset of its declared set. That is what makes a declared-set change benign:
  a peer still holding the receiver's previous, smaller declaration keeps routing
  legs at the old line and they still complete.
- **A declared set may grow, never swap or shrink.** Adding a line is safe in
  either order (the sender's stale view simply keeps selecting the older line
  until the feed converges). Removing one can strand a pended exchange that
  pinned the removed line at origination and must resume on it verbatim — a
  breaking operation that requires draining pends first. See the gateway's
  `docs/CONFIGURATION.md` for the opt-in procedure and the change window.

**Cross-version translation, and what a carry-extension partner may see on the
wire (2026-08-12).** When no shared declared line and no native-reach line
exist for a leg, routing tries one more thing before refusing: a **transform
chain** — per-adjacent-step modules (`pa.pas`/`pa.dtr`: `2.0↔2.1`, `2.1↔2.2`,
composed for longer hops) that adapt this build's own bytes to the
recipient's declared line. Native reach always wins over a chain when both
are available — a chain is the bridge of last resort, for a line this build
does not natively speak (see the spec's "native-reach-first" rationale
below). A partner on the other end of a chained leg may therefore see two
things a same-line exchange never carries:

- A **`shn-carried-content` extension**, wherever a downcast has no honest
  slot in the target line for an element the source line carried (e.g. a 2.2
  `authorizationNumber` downcast to 2.0/2.1). It holds the original element,
  byte-faithful, plus its source line, so a later upcast restores it exactly.
  This is the network's no-silent-loss policy: an element with nowhere to go
  is **carried**, never dropped, and a Provenance resource with an attached
  `shn-loss-report` extension travels alongside the transformed payload
  (payload-internal only, in the leg's observer stream, or both, depending on
  target-profile tolerance — never an envelope/`Metadata` field, so the Hub
  stays version-blind).
- A payload built at a line **older or newer** than what this build's own
  declared set advertises — the chain's target is the *peer's* declared
  line, not this build's.

**Gated-overlay semantics.** A partner that is known — by config, today; by
probe or refusal history in a future slice — to reject unknown extensions
gets a `gated` overlay entry. A gated peer never receives a lossy chained
leg (one whose worst step is `carry` or `gated`); the leg refuses at
selection instead, legibly, rather than strip the carried content to look
clean. A chain whose every step is `full` (lossless both ways) still reaches
a gated peer normally — the overlay gates *lossy* legs, not translation
itself.

**Refusal grammar, verbatim.** A no-bridge outcome is the same legible `422`
family as the no-shared-line case above, with a parenthetical naming the
specific missing ingredient. This build speaks `pa.pas@2.0` only, no
transform chain reaches a peer that declares only `pa.pas@2.3`:

```json
{"error":"no shared contract line for pa.pas (leg pas-claim): this gateway speaks pa.pas@2.0; recipient \"acme-payer\" declares pa.pas@2.3 — no bridge available (no transform chain bridges to line 2.3)"}
```

The same base grammar carries one of three other parenthetical phrases
depending on which reachability ingredient is missing: `"no configured
validator lane for line …"` (a chain exists but the target line has no
`$validate` lane), `"chain to line … refused for this peer (gated overlay:
chain contains a lossy step)"` (a chain exists but every candidate is too
lossy for a gated peer), or, on a pended exchange's resume leg specifically,
`"no bridge to the pinned line … remains available"` (the pin's original
bridge stopped existing between origination and resume). Every phrase names
a bare **line**, never a repeated `contract@line` token, so it cannot
duplicate a token already named earlier in the same message.

**Ingress stays tolerant, not translating.** A non-native inbound line is
still a legible 422 (§8.6's earlier "native capability vs. declared set"
rules) until its own transform module pair ships — chained translation runs
egress-only this generation. See the gateway's `docs/CONFIGURATION.md` for
the operator-facing lane and gated-overlay configuration.

### 8.7 Extension preservation (carry survivability)

When two parties share no native line for a contract, the originating gateway
may bridge them with a transform chain (§8.6). A downcast that has no
target-line slot for an element **carries** it, byte-faithfully, inside a
registered extension —
`http://smarthealth.network/fhir/StructureDefinition/shn-carried-content` — so
a later upcast can restore it instead of the element being dropped.

That restoration depends on you. **A participant that receives a payload MUST
preserve every extension it does not recognise, and MUST return it unmodified
on the corresponding response.** This is ordinary FHIR extension etiquette — a
receiver ignores extensions it does not understand; it does not strip them —
stated explicitly because the round trip closes only if the intermediate
system honours it. A peer that filters to profile, or that round-trips the
payload through a model that drops unknown extensions, loses the carried
content, and the gateway **cannot detect the loss**: on upcast the only
evidence that anything was carried is the carry wrapper itself, so a stripped
wrapper and a payload that never carried anything are byte-identical. No
in-band signal can fix this (a count or manifest extension is dropped by the
same filter), and the exchange is stateless by design, so nothing out-of-band
remembers what was sent.

Today the obligation is latent: every published line is native on both sides,
so no downcast leaves a gateway. It becomes load-bearing with the first peer on
a line the gateway does not build natively; at that point the partner
self-conformance harness gains a round-trip probe — a payload with a carried
element that must come back intact — as the onboarding check for this
property. Until then, build to the rule: preserve what you do not recognise.

---

## 9. Status and roadmap

### Changelog

- **2026-09-25 — An exchange is recorded under every patient it involves (§4.1, §5.1).**
  An envelope may carry `involved`: the other patients the leg involves, each as
  its own token for the leg, requested from `/authorize` with an `involvement`.
  The Hub records the exchange under each of them as well as the leg token's
  patient, so an exchange about several patients reaches each patient's record.
  It is recording only; a leg without `involved` is routed and recorded as
  before, and recipients ignore it. The Hub refuses a request whose `involved`
  it cannot read or verify (400); on an answer it drops such entries and
  delivers the answer. Send `involved` only when the discovery descriptor's new
  `hubAccepts` lists it.

- **2026-09-25 — From shn-gateway v0.55.0, a provider's gateway validates a payer's answers (§8.1).**
  The DTR `$questionnaire-package` and `$next-question` answers and the PAS
  ClaimResponse answers a provider's gateway receives are validated at its
  conformance level for every payer, on every origination lane; before, a
  gateway on the `provider-data` or `demo` lane validated no payer's answers.
  Each answer is judged against the IG lines the gateway supports, starting at
  the line the leg was sent at, and refused only when it conforms to none; the
  conformance finding records that line (`declaredLine`) and each line tried
  (`lines`). At `strict` a check that cannot run refuses a partner payer's
  answer. Only the answers of SHN's reference payers (payer identities `00001`,
  `00300` and `00301`, and the two bridging-demo payers that front `00001`) on
  those two lanes are left unvalidated.
- **2026-09-25 — Conformance enforcement adds `structural` (§8.1).**
  `CONFORMANCE_ENFORCEMENT` is `none`, `observe`, `structural` or `strict`. `structural`
  runs every check, refuses a message whose structure is broken, with the status
  and body `strict` uses, and records every other defect as `observe` does; a
  validator that cannot run is recorded. The default when the value is unset is
  still `observe`. A gateway now sends a `Parameters` resource to its validator
  inside the operation's `resource` parameter, so the validator reads it; a
  validator that answers that it was given no resource is treated as unavailable
  at every level. Ships in shn-gateway v0.54.0.
- **2026-09-25 — Native requests carried as sent unless the provider opts in (§7a.4).**
  From Smart Gateway v0.54.0, a provider's gateway adds nothing to a CDS Hooks or
  `$questionnaire-package` request its EHR sends, apart from removing the callback. The
  prefetch, Coverage and Patient edits run only when the provider's gateway sets
  `ENRICH_NATIVE_REQUESTS=true`. A Coverage a request leaves out is still read from the
  provider's system of record to choose the payer, and is not added. Send every prefetch
  value the payer should see, or opt in.
- **2026-09-24 — Members and the payer's patient.** On the CRD, DTR, PAS and
  inquiry legs, a member a system of record does not hold is carried unless the
  gateway sets `REQUIRE_KNOWN_MEMBERS=true`; eligibility, federated query and
  patient-authored DTR still refuse it (`400 unknown member`). On the CRD, DTR,
  PAS and inquiry legs a payer gateway identifies the patient the request names
  by its own system and no longer compares it with the token's patient; the
  receiving gateway on eligibility, federated query and patient-authored DTR
  keeps that comparison. Everything a payer gateway keeps about an exchange (the
  pended authorization, the decision ExplanationOfBenefit, the correlation and
  an inquiry's decision) is filed under its own identification of the member,
  never under the token's patient, and it emits `subject.binding-differs` when
  the two differ.
- **2026-09-24 — The requester sees what happened behind the Hub (§6.1,
  §6.1b outcomes).** A Smart Gateway no longer reports every failure behind the
  Hub as `502 hub routing failed`. The Hub's own refusal reaches the caller with
  the Hub's status and reason (`hub refused the exchange: …`); an Authorization
  Framework denial is a `403`; a leg the recipient was reached for, whose answer
  was lost, says so and that the recipient may have acted on the request. The
  Hub marks errors after the forward with `X-SHN-Delivered` and names the
  recipient's own status on a failed forward; every Hub error carries the
  marker, `no` when nothing was forwarded. `hub routing failed` now means only
  that your gateway could not reach the Hub. A Hub `4xx` other than `409` is a
  `502` to the caller. A responding gateway's own
  failure after the leg is authenticated (its participant's system unreachable
  or silent, a system-of-record read that failed, a validator it cannot reach)
  is framed as its answer with `200` to the Hub, like its `4xx` refusals (§8,
  "Mechanical vs. application status"), so the requester reads that gateway's
  status and message; it is an `answered` leg. On a PAS submit or update, any
  refusal the payer gateway makes after its payer's system answered says so, so
  the requester checks before resending. Ships in shn-gateway v0.54.0.
- **2026-09-24 — A payer gateway's pend ledger records and never gates (§7b.2).** A
  Smart Gateway serving a payer sends every PAS amendment to its payer's system and
  relays the answer, whatever its own ledger holds for the authorization. Earlier it
  refused an amendment itself with `409` when it held no pend for the prior claim, had
  already recorded a decision, or had another amendment in progress. A payer's own
  `409` (a version conflict while it resolves the same claim) is relayed as its answer,
  where the gateway used to re-send the amendment once. A record the gateway cannot
  write after the payer answered no longer withholds the answer. A re-pend never reopens
  an amendment still with the payer. Ships in shn-gateway v0.54.0.
- **2026-09-24 — The Hub spends an envelope, never a `correlationId` (§5.1,
  §6.1a).** The Hub's replay guard now keys the pair its token binds — the
  `correlationId` and the ciphertext's hash — so only a byte-identical envelope
  is refused (`409`). A retry sealed afresh is routed under the same
  `correlationId` or a new one; before this change any reuse of a
  `correlationId` within two hours was refused. At an SHN gateway's Da Vinci
  ingress the caller's `X-Correlation-Id` is a trace value only: each call's leg
  is sent under a freshly minted id (or the PAS Claim's `urn:shn:correlation`),
  and every answer also carries `X-SHN-Leg-Id`. A payer gateway keys a PAS
  submit's authorization on the Claim's correlation, as before.
- **2026-09-24 — Conformance enforcement has three levels (§8.1).**
  `CONFORMANCE_ENFORCEMENT` is `none`, `observe` or `strict`. `none` now runs no
  payload conformance check and records no finding; `observe`, the new level and
  now the default when the value is unset, runs every check, records each defect
  as a finding and carries the message as sent, apart from the gateway's
  registered edits (§7a.4); `strict`'s conformance refusals are unchanged. An
  answer a gateway cannot read is now refused only at `strict`. Authentication,
  authority, consent, the patient binding, routing, replay, a repeated member
  name, the contract line stamped on an answer's frame and the check of a payload
  a gateway translated between IG lines refuse at every level.
- **2026-09-21 — The Smart Gateway names a Hub leg that timed out (§6.1b outcome
  table).** An originating gateway whose Hub leg produced no answer within its
  HTTP client's timeout (30 seconds in the published gateway) collapsed the
  timeout into the generic `502 hub routing failed`, indistinguishable from a
  Hub that could not be reached, so the only way to learn the cause was the
  Hub's own log. The requester now reads `504` with `error` set to `no answer
  on the hub leg within 30s (hub leg timeout)`, the number taken from the
  client's own timeout, applied by the gateway as its own deadline on the leg
  (`hub leg timed out`, no number, when the caller's own request deadline
  ended the wait first; a connection that gives up before the Hub is reached
  is not called a timeout and stays the `502`), and the
  gateway logs one line with the leg, the counterpart and the correlation id.
  The leg outcome stays `unreachable`; every other Hub-leg transport fault is
  still `502 hub routing failed`. Ships in shn-gateway v0.52.0.
- **2026-09-21 — `RunPriorAuth` is made under the payer the caller's Coverage
  names.** The prior-authorization loop read the payer identity for its
  questionnaire request and its claim from a constant (the CMS test identity):
  the DTR leg sent a Coverage built under that identity in place of the
  caller's, and the claim's insurer check compared against it, so a payer that
  maps payer identity at its edge refused the questionnaire request of every
  caller whose Coverage names another payer, after the CRD leg had passed with
  the caller's own record. The loop now reads the payer identity from the
  caller's Coverage search result (`ParseCoveragePayer`), sends that result's
  first Coverage on the DTR leg (id-stamped as before) together with the payor
  Organization it names as a `referenced` parameter, and checks the claim's
  insurer against that identity. `QuestionnairePackageInputs` gains
  `Referenced` (records the payer needs to resolve references in the coverages
  or orders, embedded exactly). A payor reference the payer cannot resolve
  from those records (an absolute reference, say) is refused before any leg,
  naming the reference. `shn doctor` and `shn priorauth` supply the caller's
  records already and take the fix as is. Ships in sdk v0.53.0.
- **2026-09-20 — The Smart Gateway no longer answers the older questionnaire
  request (§6.3 receiver obligation).** A payer gateway refuses a
  `dtr-questionnaire-fetch` request that names no operation with `400
  questionnaire request names no operation: …`, framed as its answer, before
  its payer's system sees it; it no longer rebuilds a `$questionnaire-package`
  request from the `canonical`/`coverage`/`order` envelope (a rebuild carried
  only those and lost every other parameter, including `referenced` and
  `context`). A `questionnaire-package` or `next-question` operation's own
  input is sent to the payer's system exactly, as before. Every routable
  gateway payer declares `"v1op"`. A requester that reads the payer's
  `requestFrames` from the `/holders` feed sends the framed operation:
  `RunPriorAuth` does, given `Payer.RequestFrames`; `shn doctor`, `shn
  priorauth` and the sample participant do from sdk v0.52.0, while the
  published `shn` CLI before it (sdk v0.51.1) did not read the payer's
  `requestFrames` and sent the older request. This refusal therefore ships in
  the gateway release that follows sdk v0.52.0. `shnsdk.Responder` still
  answers the older request.
- **2026-09-20 — The `shn` CLI and the sample participant carry the payer's
  declared request frames (§6.3).** `shn doctor` and `shn priorauth` build
  their view of a payer from its `/holders` row and now carry that row's
  `messageFrames` and `requestFrames`, so their questionnaire request is sent
  as the framed `questionnaire-package` operation to a payer declaring
  `"v1op"`; the published CLI before this (sdk v0.51.1) did not read the
  payer's `requestFrames` and sent the older questionnaire request. Callers of
  `RunPriorAuth` pass `Payer.RequestFrames` from the payer's `/holders` row
  the same way. The sample participant reads the row when given the feed URL
  and refuses a prior-authorization run, before anything is sent, against a
  payer that does not declare `"v1op"`. A payer gateway that refuses the older
  questionnaire request arrives with the gateway release that follows sdk
  v0.52.0; every routable gateway payer already declares `"v1op"`.
- **2026-09-20 — A recipient gateway's refusal of its own participant's answer
  travels framed (§8, "Mechanical vs. application status").** After the payer's
  system has answered, a `4xx` its gateway writes about that answer — a PAS
  response whose patient linkage is inconsistent or that names another patient
  (`403`), a questionnaire package carrying a subject (`403`), an answer that
  repeats a member name (`403`), an answer that fails validation at `strict`
  (`422`) — is framed as the gateway's answer with `200` to the Hub, so the
  requester reads that status and reason instead of `502 hub routing failed`.
  A `5xx` about the answer and a response-leg build failure stay bare, as before.
- **2026-09-17 — SDK: CRD card builders retired, card and evidence readers corrected,
  decision EOBs state the payer's own reason.** `BuildCards` / `BuildCardsAtLine` now
  build nothing and return `ErrBuildCardsReplaced`: a CRD answer returns the requested
  order with its coverage information, and a `CardCoverage` names no order, Coverage,
  assertion date or `coverage-assertion-id`; build answers with `BuildCRDResponse`.
  `ParseCards` and `ParseCRDResponse` read a card's `extension` object as coverage only
  when it has the shape earlier SDK card builders wrote (a `covered` value and only
  `covered`, `paNeeded`, `questionnaires`, `satisfiedPaId`); any other card extension
  object is the card author's own, so the coverage the answer states elsewhere is read
  (earlier, a payer's own card extension object hid it). `ExtractCDexEvidence` returns
  the records' last `DiagnosticReport` with the `Provenance` whose target names that
  report (`DiagnosticReport/<id>` or the report entry's `fullUrl`), and refuses records
  with no such `Provenance`; earlier it returned the last `Provenance` whatever it
  attributed. `PADecisionEOBParams` gains `ReviewAction` (`PASReviewAction`: the payer's
  X12 306 decision code and X12 886 reasons) and `DenialReasons` (the payer's CARC or
  RARC codes). With either set (an empty `DenialReasons` included), a denied EOB carries
  a `denialreason` adjudication only for each code the payer supplied, and carries the
  decision as the PDex `reviewAction` extension on the amount adjudication (`submitted`,
  0 USD, as approvals use): the payer's review action, or `A3` Not Certified when none
  is given. A review action that contradicts the decision (`A1` or `A6` on a denial, `A3`
  on an approval) is refused; `A2` is carried on either. With neither set, a denied EOB keeps the fixed CARC 50 `denialreason`
  (deprecated, removed in a later release). `PriorAuthResult` gains `ProcessNotes` (the
  payer's notes with their types, on approvals and denials), `ReviewAction` and
  `DenialReasons`; new constants `X12ReviewDecisionSystem`,
  `X12ReviewDecisionReasonSystem`, `CARCSystem`, `RARCSystem`.

- **2026-09-17 — What the network changes in a CRD or DTR message (§7a.4).** New
  section listing the four edits a Smart Gateway (v0.44.0) makes to a CDS Hooks or
  `$questionnaire-package` message, and nothing else: callback removed, prefetch obtained,
  coverage obtained, payer identity mapping. Also: signatures inside the payload travel
  untouched and transport signatures are not carried; the payer's service is chosen by
  hook and a hook it does not offer is refused with the offered hooks; answer bodies are
  relayed exactly; the member id limitation and the patient naming of gateway-originated
  requests; and the framed questionnaire requirement for self-registered payers.

- **2026-09-17 — Framed DTR operations are declared (§6.3).**
  `SupportedRequestFrames()` now returns `["v1","v1op"]`, so every registration
  built with this SDK declares that it accepts framed DTR operations; the
  network declares it for its reference participants, and for hosted gateways
  whose pinned release serves them (v0.44.0 or later). `RunPriorAuth` therefore
  sends a framed `questionnaire-package` operation to a payer whose registry
  entry lists `"v1op"` (pass the entry's `requestFrames` in
  `Payer.RequestFrames`); a `Payer` without it still receives the older
  questionnaire request. A self-registered payer keeps the frames it declared:
  one built on an earlier SDK must rebuild on this version (v0.50.1) and run
  `shn rotate` to receive framed DTR operations. `shn register` and `shn rotate`
  take `--request-frames` (for example `--request-frames v1` for a Smart Gateway
  older than v0.44.0); unsupported tokens are refused. A `PUT /register/{id}`
  that keeps both keys (a re-declaration) is audited as `redeclared` instead of
  `rotated` (§2.4, §2.5).

- **2026-09-16 — PAS: profiled pended Task, inquiry and continuation (§7b).** New:
  `BuildPendedTasks` and `BuildPendedClaimResponseAtLine` (the PAS `profile-task` at
  each line, from the payer's facts only), `ParsePendedResponseDetail` (needs per
  request line; values in a form the line does not define are reported, never
  converted), `BuildPASInquiryBundle`, `PriorAuthContinuation` /
  `NewPriorAuthContinuation`, `PriorAuthResume.Continuation`, `Identity.Inquire`,
  `RunPriorAuthWith` / `ResumePriorAuthWith` with `WithWait` and
  `WithInquiryRecords`, `InquiryAdjudicator`, and `ResponderConfig.PublicBaseURL`.
  The submit and update builders write one item trace number per item, and
  `shnsdk.Responder` echoes it on its answers. Behavior changes:
  `shnsdk.Responder` answers an amendment its `Adjudicator` pends again or denies
  with that decision (it used to answer `422 amendment still insufficient`); a
  pended decision must carry `PendedItems` and the Task facts (`500` otherwise);
  `PASDecision.NeededItems` alone is refused; `BuildPendedResponse` /
  `BuildPendedResponseAtLine` return `ErrPendedResponseNeedsTaskFacts`. The
  Responder frames each answer with its own media type (`application/json` for a
  CDS Hooks answer, `application/fhir+json` for FHIR); an `Adjudicator` error that is
  an `*AppAnswerError` is relayed with its status, exact body and media type (no
  `Content-Type` when it names none). `PASDecision.PendedItems`, `TaskIdentifier`,
  `TaskStatus`, `TaskRequester`, `TaskOwner` and `PayerURL` are new; `NeededItems` is
  deprecated. The inquiry leg needs Smart Gateway v0.45.0 or later; framed DTR
  operations need Smart Gateway v0.44.0 or later.

- **2026-09-16 — CRD: coverage information as a system action; request and response
  builders; response certifier (§7a).** `shnsdk.Responder` now answers a CRD request
  the way Da Vinci CRD defines it: `cards: []` and one `update` system action returning
  the requested order (its own bytes plus the coverage-information extension: covered,
  `pa-needed`, and for a prior authorization with a questionnaire, `doc-needed`
  `clinical` and the questionnaire), with a fresh `coverage-assertion-id`. An
  `Adjudicator` that also implements `CoverageAssertionRecorder` receives each
  assertion after its answer is sent. New: `BuildCRDResponse`, `ParseCRDResponse`
  (coverage information from system actions, from card suggestions, and from the card
  extension object earlier SDK versions wrote; every value exactly as sent, unknown
  sub-extensions included), `BuildCRDRequest`, `CheckCDSHooksResponse` /
  `CDSHooksRules`, and `ParseCoveragePayer`. `ParsePayerIdentifier` and
  `ParseCoverageBeneficiary` also accept a Bundle of Coverages when every Coverage names
  the same payer (respectively beneficiary). `RunPriorAuth` reads the CRD answer with
  `ParseCRDResponse`, so it understands both answer shapes. Deprecated:
  `BuildCards` / `BuildCardsAtLine` (output unchanged in this release; a later release
  emits the `BuildCRDResponse` shape), `ParseCards` (now a wrapper that keeps its
  earlier result for the card extension object and otherwise returns the first
  coverage information), `BuildConformantOrderSelectRequest` and
  `BuildConformantOrderDispatchRequest` (output unchanged; replaced by
  `BuildCRDRequest`). A requester on an earlier SDK that reads only the card extension
  object cannot read a current `shnsdk.Responder`'s CRD answer. `BuildCRDRequest` omits
  `fhirServer` and `fhirAuthorization`, which CRD 2.2.1's request model marks `1..1`,
  by design: the payer answers from context and prefetch, and the network never hands
  a payer a route into the provider's system. `BuildCRDResponse` enforces `crd-ci-q3`
  in both directions and the `withpa` rule of `crd-ci-q4` as CRD 2.2.1 states it at
  every line, writes `contact` as a `ContactPoint` at 2.0/2.1 and a `ContactDetail` at
  2.2 (refusing the other shape), and requires a card `uuid` at 2.2.
- **2026-09-16 — DTR: framed operations (`operation` header, `v1op`) and the package
  request builder (§6.3).** The frame header allowlist gains `operation`
  (`questionnaire-package`, `next-question`) for `dtr-questionnaire-fetch` request
  frames, declared by the `requestFrames` token `"v1op"`; a requester sends a framed
  DTR operation only to a recipient that declares it (`ErrFramedDTRUnsupported`
  otherwise). New: `BuildQuestionnairePackageParameters`, `SupportsRequestFrameV1Op`,
  `NextQuestionAdjudicator`. `RunPriorAuth` sends the framed operation to a payer that
  declares `"v1op"` and the older request otherwise; `shnsdk.Responder` serves both.
  Behavior change: a `nextQuestion` round sent in the older questionnaire request is
  now answered by your `NextQuestionAdjudicator`, or refused with `422` when the
  `Adjudicator` does not implement it; it is no longer answered with a questionnaire
  package. Declaring `"v1op"` implies accepting `dtr-questionnaire-fetch` request
  frames (a requester frames the operation to a `"v1op"` peer without `"v1"`).
  `SupportedRequestFrames()` is unchanged (`["v1"]`). Deprecated:
  `BuildQuestionnaireFetch` / `BuildQuestionnaireFetchWithCoverage` (output unchanged).
- **2026-09-16 — Denials carry only the payer's own reason and notes (§7b.3).** A
  `shnsdk.Responder` denial now carries `ClaimResponse.disposition` only when the
  adjudicator's `PASDecision.DenyReason` is set (exactly as given), and
  `ClaimResponse.processNote` only for the adjudicator's own
  `PASDecision.ProcessNotes`. The SDK no longer supplies a default rationale or a fixed
  appeal note. `shnsdk.BuildDeniedResponse` no longer emits the fixed appeal note;
  `shnsdk.BuildDeniedResponseWithNotesAtLine` carries supplied notes. A decision that
  sets `ProcessNotes` with an outcome other than denied is refused (`500`).
- **2026-09-17 — A facility's records reach the requester exactly as its system holds them.**
  A facility gateway answers a CDex data request with **every** record of each requested
  type whose date falls in the requested range, in the order its FHIR server returned
  them (earlier releases sent the first record of each type). Each record is carried as
  the server's own bytes: its `subject` is no longer rewritten to the network member id,
  and nothing is re-encoded. The facility's own `Patient` record is **not** sent (minimum
  necessary): the records `Bundle` (id `results`) carries only an identity binding the
  facility gateway writes, a `Patient` with the facility's Patient `id` and the
  `urn:shn:member` identifier, plus one gateway `Provenance` (with an id) per record
  (`search.mode` `match` for records, `include` for the others). Entries are identified as
  `urn:shn:fedquery:<n>`, never by the facility's server URLs. The facility's search is
  narrowed to the requested dates (`date=ge…&date=le…`, widened by one day on each side), and the facility still selects
  records by their own dates (`DiagnosticReport` by `effectiveDateTime`, else
  `effectivePeriod` end, else start; `DocumentReference` by `date`). More records than one
  search's bounds (10 pages, 200 entries, 4 MiB) is a `422`
  (`records exceed the per-answer bound for <type>`), never a partial answer. Before
  anything is sent, the member must have a `Patient` in the facility's system (`404` when
  there is none), and every record must be about that member (a record about anyone else,
  or the same record twice, is a `502`). The requester checks the same on receipt: a
  record binds to the member through its patient reference, directly or through the
  identity binding, and an answer naming anyone else is refused before use
  (`federated response refused: …`). **Requester-side
  change:** a provider that forwards a facility report as prior-authorization evidence
  (the `pas-claim-update` `ClaimUpdate`, its own message) sends a copy of the report
  whose `subject` names the Claim's patient, re-encoded; the facility's bytes are only
  those in the facility's answer. The evidence is the `DiagnosticReport` with the latest
  date (as above), compared as instants: a year, month or date alone is the start of that
  period in UTC, and a date-time keeps its own offset. A report without a readable date
  comes before dated ones, and of equal instants the later entry wins. The evidence also
  includes the `Provenance` that targets it.
- **2026-09-16 — CDex fulfillment keeps the request Task and the records exactly.**
  `shnsdk.BuildCDexQueryResult` now extends the payer's request `Task` instead of
  rebuilding it. The Task keeps its own bytes apart from three changes: `status` becomes
  `completed`, the records searchset `Bundle` is appended to `contained`, and one
  `data-query` `output` referencing it is appended to `output` (each array is created when
  absent). The facility's records `Bundle` is embedded as its own bytes, so decimal and
  large-integer lexemes, unknown elements and member order survive. A contained id is
  written into it only when it has none, or when its id is already used in the Task
  (`results`, else `results-<n>`); a records `Bundle` with an empty `id` is refused. A
  request Task that carries a `Signature`, or that a
  signed `Provenance` targets, is refused with `ErrCDexSignedContent`; so is a signed
  records `Bundle` that would need an id. A Task whose status is not `requested`,
  `received`, `accepted` or `in-progress` is refused with `ErrCDexTaskStatus`.
  `shnsdk.BuildCDexFulfillment` (new) returns the same Task plus the spans it copied
  unchanged. `shnsdk.ExtractCDexEvidence` now follows the Task's last `data-query`
  output to the contained `Bundle` it references, instead of taking the first contained
  `Bundle`, and refuses a reference that more than one contained resource answers. Both
  functions refuse a document that repeats a member name at any depth.
- **2026-09-02 — Leg outcomes (§6.1b) and operator-attested `payerIds` (§1a, §2.3).**
  Documentation only — no `wireProtocolVersion` bump, no wire change. §6.1b defines
  the five outcomes an origination leg can end in (`routed`, `answered`, `denied`,
  `unreachable`, `failed`), what the originator's caller sees for each, and which
  refusals are not leg outcomes at all. §1a and §2.3 now state how a payer row's
  `payerIds` come to exist — operator-attested, never self-asserted (FR-G42) —
  with the registration field and its rejections (`400` on a non-payer role,
  `409` on a duplicate payer-id); the earlier text called them "declared claims
  at registration" and left who attests them unstated.
- **2026-08-20 — Extension preservation obligation (§8.7).** Documentation only — no
  `wireProtocolVersion` bump, no wire change. States the peer obligation the carry
  mechanism (§8.6) has always depended on: preserve extensions you do not recognise
  and return them unmodified, `shn-carried-content` specifically. Latent until the
  first non-native line is published; recorded now so participants build to it.
- **2026-08-14 — Persona `payerId` + directory-resolved test counterparties (`demoPersonas[].payerId`, §1a;
  the descriptor's demo fields carried a different prefix at the time, renamed since — §1a).**
  Additive, no `wireProtocolVersion` bump. Each advertised `demoPersonas[]` entry now
  also carries `payerId` — the seeded member's Coverage payor identity (fixture truth). As
  of sdk v0.41.0, the `shn` CLI (`shn doctor`, `shn priorauth`) and cloudsmoke resolve their
  test counterparty by matching a persona's `payerId` against holder-attested `payerIds` in
  the registrar `/holders` feed (§3), instead of the legacy `demoResponders[]` hint.
  `demoResponders[]` is still populated for older consumers that have not migrated; see
  its field-table entry above for the deprecation note.
- **2026-09-13 — Silent-recipient rule stated exactly (§8.6).** A silent recipient is answered at the originator's highest declared line, not its highest native line; the code has always done this (`selectContractToken` over the declared set) — the prose is corrected to match.
- **2026-08-12 — Tri-line native builders + request frames (`requestFrames`, §6.3, §8.6).**
  Two additive changes, no `wireProtocolVersion` bump, no new frame version.
  (1) **Tri-line native.** The Smart Gateway and this SDK now build every PA
  contract at three lines (`pa.crd`/`pa.dtr`/`pa.pas` at `@2.0`, `@2.1`, `@2.2`),
  each validated against its own line's `$validate` lane. What a deployment
  *declares* stays a configurable subset of that native capability and still
  defaults to the canonical `2.0` line, so **nothing about an existing
  deployment's wire behavior changes** until an operator opts a line in — the
  `2.0` payload bytes are frozen and fenced by regression tests. §8.6 gains the
  native-vs-declared distinction and the grow-only rule for declared sets.
  (2) **Request frames.** A new, independently negotiated `requestFrames`
  registry capability lets the **request** leg of a contract-mapped exchange
  carry the line it was built at, in the same v1 frame the response direction
  already uses (§6.3). Receivers that declare it MUST accept both framed and
  bare requests; a peer that does not declare it keeps receiving byte-identical
  bare requests. A claim the receiver cannot both build and validate for is
  refused with a legible `422` (§6.3, ingress refusal) rather than silently
  answered at another line. `coverage-eligibility` is version-neutral and is
  never framed. Also in this release: an SDK-based `Responder` can opt into
  stamping `contractVersion` on its framed success answers
  (`ResponderConfig.StampContractVersion`), and the SDK's own originators verify
  the stamp — the published-library parity for what SHN gateways have done since
  v0.37.0. Absence is tolerated in every direction, as before.
- **2026-08-11 — Version-matched routing + frame-header allowlist widened (`contractVersion`, §6.3, §8.6).**
  The message-frame header allowlist (§6.3) widens from `Content-Type` alone to
  `{Content-Type, contractVersion}`. `contractVersion` carries the full
  `<contract>@<line>` token of the exchange-contract line the response body was
  **built at**; it is stamped only by SHN gateways on every framed **success**
  (2xx) answer for a contract-mapped leg (§8.6), and verified only by originators
  on **≥v0.37.0** of this library against the line the leg actually routed to —
  disagreement is rejected before the body reaches any parser. An **absent**
  stamp is always tolerated, exactly like an absent frame: a pre-version
  responder, or one on an older published build that predates the stamp, is
  never treated as an error. This is additive to message frame v1 (§6.3) and
  rides the same version-matched routing this slice adds (§8.6) — no new frame
  version, no `wireProtocolVersion` bump.
- **2026-08-11 — Contract-version declaration + surfacing (`contractVersions`, §1a, §2.3, §2.4).**
  Registration and rotation MAY carry a self-declared `contractVersions` array of
  `<contract>@<line>` tokens (grammar `^[a-z0-9]+(\.[a-z0-9]+)*@[0-9]+(\.[0-9]+)*$`, ≤16
  tokens, each 3–48 bytes), outside the PoP payload, and the `/holders` feed republishes
  them verbatim. The discovery descriptor now also advertises the network's own
  native set (`pa.crd@2.0`, `pa.dtr@2.0`, `pa.pas@2.0`, `pa.pdex@2.1`, §1a). This is
  **additive** — no `wireProtocolVersion` bump — and tokens are self-asserted
  capability, not admission-verified identity (contrast the operator-vouched
  `payerIds`). Nothing in this slice branches behavior on the declared tokens:
  version-aware routing and translation consume these in later slices; today they are
  declaration + surfacing.
- **2026-07-17 — Message frame v1: negotiated, sealed application answers (§6.2, §6.3).** A
  frame-capable responder now carries its real application status (success or not) and body
  inside the sealed response leg, versus the Hub's implicit `200`-on-bare-payload / generic
  `"hub routing failed"` collapse. Negotiated per exchange from each holder's registry-advertised
  `messageFrames` capability (self-declared automatically by a codec-capable build at
  registration/rotation) — there is no flag and no sniffing. A pair where either side is not
  frame-capable stays byte-identical to the original, pre-message-frame contract. This
  **replaces** the interim, single-release `RESPONDER_RELAY_ERRORS` JSON-wrapper mechanism
  (removed): that wrapper's `{__shnStatus,__shnBody}` shape, its flag, and the response sniff it
  depended on are gone.
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
  are **REMOVED** (breaking). The network is unchanged: the `federated-query` op names, the
  `consentRef`/`custodian` consent gate (§4–§5), payload-blind routing, and non-aggregation are the same —
  only the leg CONTENT became CDex; the purpose-of-use in the Task is partner-asserted and NOT load-bearing
  for authorization (the network re-checks consent).
- **2026-06-18 — DTR-fetch returns a `$questionnaire-package`.** The `dtr-questionnaire-fetch`
  response is now a Da Vinci `$questionnaire-package` collection Bundle (the Questionnaire plus its
  dependent Libraries/ValueSets), not a bare Questionnaire — so the questionnaire's CQL/value-set
  dependencies survive the wire. New exports `BuildQuestionnairePackage` /
  `ExtractQuestionnaireFromPackage`; `Responder` wraps the Questionnaire into a package and
  `RunPriorAuth` extracts it before the canonical-substitution check + auto-fill. A manual
  leg-by-leg client MUST call `ExtractQuestionnaireFromPackage(dtrResp)` before
  `ParseQuestionnaireURL`/`FillQuestionnaire`. The worked-example lumbar-MRI questionnaire is also
  CQL-backed (a `cqf-library` extension + a per-item SDC `initialExpression`), so an operated SDC
  `Questionnaire/$populate` CQL engine can populate it; `FillQuestionnaire` ignores those extensions
  and fills by `linkId`, so the managed `QuestionnaireResponse` is byte-unchanged. (The demo-fill
  helper this shipped with — renamed since to `DemoLumbarContext`/`FillQuestionnaire`, §7a.3 —
  accepted `valueInteger` **or** `valueDecimal` for `conservative-therapy-weeks`, a `$populate`
  engine emitting a CQL numeric as `valueDecimal`.)
- **2026-06-13 — PA-chain responder available.** `Adjudicator` grows three new methods —
  `OrderSelect(cpt string) (paRequired bool, questionnaireCanonical string)`,
  `Questionnaire(canonical string) (questionnaireJSON []byte, ok bool)`, and
  `PriorAuth(qrJSON []byte, hasDiagnosticReport bool) (PASDecision, error)` — served by
  `shnsdk.Responder` across four transaction types: `crd-order-select`,
  `dtr-questionnaire-fetch`, `pas-claim`, `pas-claim-update`. Demo/worked-example helpers
  (renamed since to `Demo*`, then two of the three — `DemoLumbarQuestionnaire()` and
  `QuestionnaireCanonicalLumbarMRI` — retired outright rather than shipped on a later
  breaking release; `DemoLumbarContext()` still ships. See §3c/§7b.2 for a self-contained
  worked example that does not depend on any of them): the pended-claim ledger is per-process;
  deployments needing durable pends across replicas front it with their own store. See
  `docs/PREVIEW.md` §3c for the updated quickstart.
- **2026-06-12 — Payer responder (eligibility) delivered.** `shnsdk.Responder` is now
  available in the public SDK (`github.com/SmartHealthNetwork/shn-sdk`). It implements
  the full inbound pipeline — `X-Hub-Assertion` verification (§6.2a) first, then authz
  token `VerifyBound`, decryption, `Adjudicator.Eligibility`, and a sealed-and-authorized
  response — for the `coverage-eligibility` transaction type. See `docs/PREVIEW.md` §3c
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
  runs it; `shn doctor` now also validates it. See `docs/PREVIEW.md` §3a.
- **2026-06-10 — Discovery descriptor.** Added §1a: `GET {accounts}/discovery`
  serves a machine-readable descriptor (endpoints, demo responders, seeded personas,
  `wireProtocolVersion`, `demo`/`syntheticDataOnly` — the demo-prefixed field names were
  renamed since, §1a). It is sufficient to drive the
  loop — keys are resolved live (payer `encPub` from `/holders`, authz pub from
  `/pubkey`). `shn doctor` and a live deploy probe consume it. See
  `docs/PREVIEW.md` for the getting-started path.
- **2026-06-09 — BREAKING: authz token wire format changed (`payloadHash`).** The
  token now carries a signed `payloadHash` field — `sha256hex` (64 lowercase hex) of
  the envelope **ciphertext**. It is **REQUIRED on every envelope-borne operation**
  and **MUST be ABSENT on `patient-access-read`** (the one REST bearer read). Senders
  must **seal-then-authorize**: seal the payload first, then call `/authorize` against
  `sha256hex(ciphertext)` so the minted token binds THIS payload. Recipients
  (and the Hub per leg) **strictly verify** `sha256hex(received ciphertext) ==
  token.payloadHash` — an empty want or an empty token hash is **rejected**. This is
  breaking: a participant minting or verifying tokens the old way (no `payloadHash`)
  is now rejected. Contract: §4.1 (request), §4.2 (token struct), §4.4 (`VerifyBound`
  / `VerifyBoundNoPayload`).

### What is implemented today (preview network)

- **Static admission** — holders provisioned via an operator manifest bundle.
- **Dynamic admission** — Trust-admin-gated `POST /register` (with
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
  `POST {hub}/route` to originate network legs.
- **Reference inbound receiver** — the Hub forwards to `POST {holder}/substrate/inbound`;
  the Smart Gateway implements this surface. An direct-integration participant implementing it
  natively must follow the protocol in §6.2.
- **Reference direct-integration participant** — a reference implementation runs the full
  eligibility round-trip (both the covered and not-covered branches) and the
  prior-auth round-trips (approved, pended, and denied) against the live network
  by delegating to the public SDK (`shnsdk.RunEligibility` / `shnsdk.RunPriorAuth`),
  without importing any Smart Gateway internals on the originate path. Each run's
  `AuditEvent` is verified. The deploy pipeline runs all of these against the public
  preview environment on every network deploy.

### What's coming next

| Feature | Notes |
|---|---|
| **Push-notify on admission** | Hub + authz poll today (~3-second cycle); push-notify is the tracked fast-follow |
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

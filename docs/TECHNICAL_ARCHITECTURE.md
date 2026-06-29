# Smart Health Network — Technical Architecture

The Smart Health Network (SHN) is a **federated health-data exchange substrate**. It lets
independent healthcare organizations — providers, payers, and facilities — exchange FHIR
clinical and administrative data **without any central party ever holding, reading, or
accumulating that data**.

The system is built around five architectural commitments, each enforced **structurally** —
by what components can and cannot hold — rather than by policy promises:

1. **Non-aggregation.** No component persists a cross-organization clinical record, and no
   component maintains a patient→organization index. Data stays with the organization that
   holds it and moves only for a specific, authorized operation.
2. **Payload-blind routing.** Messages cross the network as encrypted envelopes. The router
   (the Hub) verifies *who* is sending *what kind* of operation to *whom*, but structurally
   cannot read the contents — it is constructed without a decryption key.
3. **Per-operation authority.** There is no standing access. Every individual message leg
   carries a freshly minted, cryptographically signed authorization token bound to that exact
   exchange — the operation, the legal frame, the patient, the correlation ID, and a hash of
   the encrypted payload itself.
4. **Tamper-evident audit.** Every leg is recorded — metadata only, never payload content — in
   an append-only, hash-chained, signature-verified audit log, with a signed checkpoint
   anchored externally so even tail truncation is detectable.
5. **A patient-fiduciary surface.** Patients see what happened with their data and interact
   with decisions about their care through an SHN-operated gateway — governed by the fiduciary
   and patient-access standards of the Smart Health Data Trust (SHDT) — that holds nothing
   itself and reads through to canonical sources.

Everything else in this document is an elaboration of these five ideas.

---

## Core concepts and vocabulary

| Term | Meaning |
|---|---|
| **Holder** | An organization that holds clinical/administrative data and participates in the network: a *provider*, a *payer*, a *facility*, or the *PHG* (the SHN-operated patient surface). |
| **Smart Gateway** | The holder-side service that terminates the substrate. It is the only thing a holder's internal systems touch. It does all sealing, validation, token acquisition, and verification. |
| **Hub** | The central, payload-blind message router. |
| **Authorization Framework** | The central token-issuing service. Policy-evaluates every requested operation (default-deny) and mints signed, scope-bound tokens. Verification of those tokens is decentralized — every party checks them locally. |
| **Frame** | The legal basis under which an operation occurs. Five frames are defined: `provider-tpo` (provider treatment/payment/operations), `payer-coverage` (payer coverage decisions), `facility-disclosure` (a facility disclosing records in answer to a federated query), `patient-access` (the patient reading their own data), and `patient-authorship` (the patient authoring data). Patient consent for a federated query is modeled as a *gating conjunct* on the provider's `provider-tpo` query — confirmed against the Consent service at token issuance — not as a frame of its own; and the patient-reading and patient-authoring frames are deliberately kept distinct and never collapsed into one "patient access" concept. |
| **Leg** | One direction of one exchange (request or response). The unit of authorization and of audit: every leg gets its own token and its own audit record. |
| **PCI** | The substrate-level patient identifier — a derived, opaque `pci:`-prefixed value. Member IDs, MRNs, and other real-world identifiers cross the network **only inside sealed payloads**; all routing metadata and all audit records carry the PCI instead. |
| **Envelope** | The wire unit: cleartext routing metadata (sender, recipient, transaction type, frame, correlation ID, authorization token, timestamp) plus an opaque ciphertext sealed to the recipient's public key. |
| **Registry** | The directory of admitted holders: ID, role, encryption public key, signing public key, and base URL. Sourced from a provisioning manifest plus dynamic runtime admissions. |
| **SHN** | The operator of every shared service — the Hub, the Authorization Framework, the Consent service, the Registrar, the Accounts service, the Audit Plane, and the PHG (patient surface). Its routing core, the Hub, is the conduit: it holds no decryption keys and no re-identifiable patient provenance. |
| **Smart Health Data Trust (SHDT)** | The patient-data policy and fiduciary body. It sets the consent and patient-access standards the patient-facing services run under, holds the patient fiduciary duty, and carries oversight and enforcement authority. It is not an operator — the services themselves are run by SHN. |

---

## The one mental model: the authorized sealed leg

Every interaction on the network — an eligibility check, a coverage-requirements lookup, a
prior-auth submission, a federated record query, a patient attestation — is composed from the
same unit. Understanding this unit means understanding the system.

```
 sender gateway                      Hub (payload-blind)                 recipient gateway
 ──────────────                      ───────────────────                 ─────────────────
 1. validate the FHIR payload
    (egress, fail-closed)
 2. SEAL the payload to the
    recipient's public key
 3. obtain a TOKEN from the
    Authorization Framework, bound to
    (holder, frame, operation, patient
    PCI, correlationID, hash of the
    ciphertext)
 4. send envelope + signed
    holder assertion        ───────▶ 5. verify the sender's assertion
                                     6. verify the token's bindings
                                     7. replay + timestamp guards
                                     8. forward the ciphertext
                                        (cannot decrypt)      ─────────▶ 9. verify the token, open the
                                                                            envelope, validate (ingress),
                                                                            check the payload is about the
                                                                            patient the token names,
                                                                            do the work
                                     11. verify the RESPONSE   ◀───────  10. seal the response back, with
                                         token + sender                      a fresh response-leg token
                                     12. append two SIGNED audit            bound to the SAME correlation
                                         records ("routed",                 ID and patient
                                         "answered") — mandatory,
                                         fail-closed
 13. verify the response token
     against the original correlation
     ID and sender; open; validate
```

Key properties of every leg:

- **Seal-then-authorize.** The payload is encrypted first; the token then binds to the SHA-256
  of the ciphertext. Swapping the payload under an existing token — or lifting a token into a
  different envelope — fails verification at the Hub and at the recipient.
- **Tokens are single-exchange.** Bound to frame + operation + correlation ID + holder +
  patient subject + payload hash, with a short expiry. The response leg's token must carry the
  *same* correlation ID and the *same* patient subject as the request, so a response about the
  wrong patient, or grafted from another exchange, is rejected.
- **Authority is asymmetric by role.** Policy is default-deny and gated by holder role: a
  provider cannot mint a payer-frame token, and so on. Roles come from the registry, not from
  anything the caller claims.
- **Verification is everywhere; issuance is central.** Tokens are signed by the Authorization
  Framework; the Hub, the recipient, and the original sender each independently verify them
  against the published verification key.
- **Audit is fail-closed.** The Hub appends a signed audit record *before* forwarding a request
  and after relaying the response. If the audit append fails, the message does not flow.
- **Validation is load-bearing.** Every FHIR resource is validated against a real FHIR
  validator on egress *and* ingress at the gateways. A validator outage means rejection, never
  pass-through.
- **Replay is bounded.** Correlation IDs are one-time-use within a window at the Hub; holder
  assertions carry one-time JTIs; envelope timestamps must fall within a small clock-skew
  window.

Exchanges are synchronous request/response today, but each leg is an independently authorized
and audited envelope tied together only by the correlation ID — so asynchronous delivery is a
transport change, not a redesign.

---

## Components

Each component runs as an independent service. The shared services are operated by SHN; the
gateways run per holder, at the holder's own boundary.

### Smart Gateway

The holder-side termination point, run once per holder with a configured role:

- **Provider** — originates workflows: eligibility checks, coverage-requirements lookups,
  questionnaire completion, prior-auth submissions and amendments, federated queries. Also
  exposes the two-phase pending-attestation API the patient surface drives. It additionally
  accepts **native Da Vinci interactions** from the holder's own conformant systems — a CDS
  Hooks `order-select` call (CRD), the DTR `$questionnaire-package` operation, and the PAS
  `$submit` operation — and maps each onto the corresponding substrate leg, so a
  standards-conformant provider system can drive the network through its existing Da Vinci
  client without bespoke integration.
- **Payer** — receives substrate envelopes at its inbound endpoint and dispatches on
  transaction type: eligibility decisions, coverage-requirements cards, questionnaire serving,
  prior-auth adjudication. Its decisioning is **pluggable**: the payer answers from a built-in
  adjudicator, or — symmetrically to the provider's native ingress — **delegates the CRD, DTR,
  and PAS legs outward to the holder's own Da Vinci-conformant payer system** (its
  coverage-requirements rules engine, `$questionnaire-package` service, and PAS adjudication
  endpoint) over authenticated SMART Backend Services, so a payer can keep its existing Da
  Vinci stack as the source of truth for decisions. Additionally serves a conventional RESTful
  **Patient Access API** (a published CapabilityStatement plus `ExplanationOfBenefit`
  read/search) gated by per-operation patient-access tokens.
- **Facility** — responds to consent-gated federated record queries, re-checking consent
  itself before disclosing anything (a deliberate second gate beyond the central one).
- **PHG gateway** — the sealed responder for patient-authored content: it builds and
  signature-attests patient answers, so authorship evidence is constructed at the gateway
  boundary, not by the patient app.

The gateway owns all cryptography and conformance work for its holder: envelope
sealing/opening, token acquisition and verification, holder assertions, per-message FHIR
validation, and the patient-binding check (the decrypted payload's patient must resolve to the
PCI named in the token).

### The holder data boundary

Gateways never talk to clinical systems directly. Between the substrate-facing engine and a
holder's backend sit a few narrow, well-defined **seams**:

- A **read seam** (the holder's system of record): resolve a patient, report whether coverage
  is in force, fetch clinical context for local questionnaire auto-fill, and fetch supplemental
  reports or facility records for a federated query.
- A **write seam** (the holder's store): persist and retrieve authorization numbers, track
  in-flight claim state (the pended-claim ledger, with an atomic test-and-set so concurrent or
  replayed updates can't double-process), and record the `ExplanationOfBenefit` documents the
  Patient Access API serves.
- A **decisioning seam** (payer side): the holder's coverage and medical-necessity policy,
  supplied either as an injected adjudicator or by delegating outward to a real Da Vinci payer
  system (above).

A holder binds these seams to its actual backend through **connectors**. Reference connectors
ship for a US Core FHIR R4 system of record and a relational store, alongside a scaffold
template for non-FHIR backends (e.g. HL7v2, X12, SOAP, or a SQL database) that a holder clones and
fills in against the same method signatures. Nothing about the network's operation requires
the backend to change how it stores data, and it never faces the network directly. The gateway
is deliberately **self-contained** — it depends only on the published wire contract and these
seams, never on any SHN-operated service's internals — so any participant can lift and run
it. Conformance obligations therefore sit at a single, well-defined point per organization: its
gateway, which validates every FHIR payload at the edge and enforces per-operation
authorization before anything crosses the network.

### Hub

The payload-blind router. It verifies the sender's signed assertion against the registry,
verifies both legs' tokens (including the payload-hash binding and the response-leg subject and
correlation constraints), enforces replay and timestamp guards, signs and appends both audit
records, and relays the opaque ciphertext. It is constructed without any decryption key —
blindness is structural, not behavioral.

A deliberate, documented boundary: receiving gateways trust the **Hub** to have authenticated
the original sender; the Hub is the trust boundary between holders.

### Authorization Framework

The token mint. Callers authenticate with a signed holder assertion; the service evaluates a
policy-as-code engine (default-deny, role→frame→operation gated, minimum-necessary scope per
operation — an eligibility check cannot carry a chart, an order-time coverage lookup carries
only the draft order and coverage) and mints a signed token bound to the full exchange tuple:

```
{
  operation        // pinned per transaction type, request vs. response
  scope            // minimum-necessary, policy-derived
  subject          // the patient, as a PCI — never a member ID
  frame            // the legal basis
  correlationId    // one-time exchange identifier
  holder           // the authenticated requester
  consentRef       // present only on the consent-gated federated query
  payloadHash      // sha256 of the sealed ciphertext
  expiry           // short-lived
  signature        // Ed25519, over all of the above
}
```

For the consent-gated federated query it first consults the Consent service and stamps the
consent reference into the token — fail-closed if consent cannot be confirmed. Every decision,
including denials and policy errors, is signed and appended to the audit chain before the
caller hears the answer.

### Consent service

The SHN-operated Global Person Consent store, run under the consent standards set by the SHDT.
It answers a precise four-facet question — may
*recipient* receive data held by *custodian* about *patient (PCI)* for *purpose*? — returning a
permit decision and a FHIR Consent reference. It gates federated-query token issuance
centrally, and facilities re-check it locally before disclosure. Returned records carry a
Provenance whose policy cites the Consent, so disclosures stay attributable to their legal
basis.

### Audit Plane

The system's memory, designed so it can be trusted more than any single operator:

- **Append-only hash chain.** Each record links to the previous via hash; records hold
  metadata only — sender, recipient, transaction type, frame, scope, outcome, consent
  reference, the patient's PCI, and the hash of the (still-encrypted) payload. Never content,
  never member IDs.
- **Authenticated writes.** The append endpoint only accepts records signed by an authorized
  substrate key (Hub, Authorization Framework, Registrar); the signature covers the record
  content.
- **Durable and immutable.** Persisted in a store whose schema forbids update, delete, and
  truncate.
- **Truncation-evident.** A dedicated checkpoint key periodically signs a high-water mark
  (sequence + head hash) to an external anchor (versioned object storage), so cutting the tail
  of the chain is detectable, not just rewriting it. The checkpoint key attests only to the
  chain head — it cannot forge record content.
- **Two views from one chain.** An auditor view over the full chain, and a per-patient
  projection filtered by PCI — the patient's "where my data went" feed derives from the same
  canonical records, not a parallel store. Records also render as FHIR `AuditEvent`s.
- **Self-verification.** A verification endpoint recomputes the chain, re-checks every
  signature, and asserts the head against the latest anchored checkpoint.

The audit plane also pushes best-effort notification signals to the PHG when patient-affecting
records land — a freshness hint only; patient reads always go back to the canonical chain.

### PHG — the patient surface

The SHN-operated Personal Health Gateway, presented to patients as their **Smart Health
account** and run under the patient-fiduciary and patient-access standards set by the SHDT. Its
defining property is that it is **non-custodial**: it persists no patient data
and holds no clinical state. Every screen reads through, at request time, to a canonical
source:

- **Activity / transparency** — the patient projection of the audit chain.
- **Prior-authorization decisions** — approved and denied determinations read from the payer's
  Patient Access API. For each read, the PHG mints a fresh, subject-bound, consume-once
  patient-access token; denial cards render the payer's machine-readable reason and appeal
  rights in plain language.
- **Questionnaire / attestation** — when a prior authorization is pending on
  patient-reported information, the patient completes and attests it in-app. The PHG acts as a
  stateless proxy to the provider's two-phase API; resume tokens stay server-side, and the
  signed patient-authored answer is constructed at the PHG gateway responder over a sealed
  `patient-authorship` leg.

The patient's identity (and therefore which PCI they may read) is server-determined from the
authenticated identity; the client cannot select an arbitrary subject.

### Registrar and participant admission

Participation is governed by the registry. The base registry is established at provisioning;
beyond that, an SHN-operated **Registrar** admits new holders at runtime through an
admin-gated registration carrying the holder's keys, role, base URL, and a
proof-of-possession signature. The Hub and the Authorization Framework poll the registrar's
feed and merge it onto the provisioning base (`registry = base ∪ dynamic`, add-only; founding
holders are immutable) — no restarts, live within seconds. The registrar also handles the
credential lifecycle — revocation, self-service deregistration, and key rotation — and signs
every lifecycle event into the audit chain.

### Accounts service

The self-service front door for external developers. It authenticates developers (OIDC),
manages their sandbox client registrations (an ownership and presentation read-model — the
registrar feed remains the runtime source of truth for trust), proxies admissions to the
registrar using a dedicated admin key, and serves an unauthenticated **discovery document**
advertising the live endpoints, wire-protocol version, FHIR profile versions, and seeded
sandbox details a participant needs to integrate without out-of-band coordination.

---

## Cryptographic identity and key custody

Security follows key placement; the map of who holds which key *is* the threat model.

| Component | Holds | Deliberately does NOT hold |
|---|---|---|
| **Each holder / gateway** | Its X25519 decryption key + Ed25519 signing key | Any other holder's keys |
| **Hub** | An audit-record signing key | **Any decryption key** — it cannot read what it routes |
| **Authorization Framework** | The token-signing key (the verification key is published to all parties) | Clinical payloads — policy evaluates metadata only |
| **Audit Plane** | The checkpoint-signing key (head attestation only) | Record-signing keys — it can only *accept* signed records, not author them |
| **Registrar / SHN admin** | Lifecycle-signing and admission-gating keys | Holder private keys — registrants prove possession of their own |

Supporting mechanisms:

- **Envelopes** are sealed with NaCl sealed boxes (X25519): encrypted to the recipient's
  public key, openable only with the recipient's private key.
- **Holder assertions** — short-lived, audience-scoped, single-use (one-time JTI) Ed25519
  assertions — authenticate every call a holder makes to the Hub, the Authorization
  Framework, and the Registrar.
- **One provisioning root.** A single provisioning step generates every holder keypair, every
  service key, and the registry manifest, so identity and trust roots are consistent across
  every deployment of the same network. In cloud deployments, secret material is split per
  service with least-privilege access — each service can read only its own keys, and the
  Hub's secret material structurally contains no decryption key.
- **Rotation and revocation** are first-class registrar operations, audited like everything
  else.

---

## Trust and security model

| Question | Mechanism |
|---|---|
| Who is the sender? | A signed, audience-scoped, expiring **holder assertion** (Ed25519), verified against the registry's signing key for that holder; one-time JTIs prevent assertion replay. |
| May they do this? | Default-deny policy in the Authorization Framework: role→frame→operation gated, minimum-necessary scope minted per operation. |
| Is this token for *this* exchange? | Strict binding verification — frame, operation, correlation ID, holder, patient subject, and ciphertext hash must all match — checked independently by the Hub, the recipient, and the sender (response leg). |
| Is this a replay? | Hub-side seen-correlation-ID cache with TTL, plus a tight envelope-timestamp window. |
| Is the payload about the right patient? | The recipient resolves the decrypted payload's patient to a PCI and requires it to equal the token's subject. |
| Can anyone read it in transit? | Payloads are sealed to the recipient's X25519 key; only the recipient can open them. The Hub has no key. |
| Did it really happen / was history edited? | Hub-signed, hash-chained, append-only audit with externally anchored signed checkpoints; the whole chain is independently re-verifiable. |
| Is the data well-formed? | Real FHIR `$validate` at every gateway crossing, egress and ingress, fail-closed; terminology validated against curated value sets. |
| Is patient identity protected? | Member IDs and demographics cross only inside sealed payloads; routing and audit use the derived PCI. |

---

## What flows across the network

The substrate is workload-agnostic — any FHIR exchange can ride the authorized sealed leg —
and the implemented domain is **prior authorization**, end to end:

1. **Coverage eligibility.** A provider sends a `CoverageEligibilityRequest`; the payer
   answers covered or not-covered.
2. **Coverage requirements discovery (CRD).** At order time, the provider sends a CDS Hooks
   `order-select` carrying only the draft order — a procedure identified by CPT or HCPCS
   Level II — and coverage (minimum necessary). The payer's rules engine answers with a card:
   either "no prior authorization required" or "PA required" plus a canonical reference to the
   documentation questionnaire.
3. **Documentation (DTR).** The provider fetches the payer's DTR `$questionnaire-package` — the
   `Questionnaire` together with the dependent libraries and value sets needed to render and
   auto-fill it — and completes it **locally** from its own clinical context, so patient data
   used for auto-fill never crosses the network. Every answer carries origin attribution (auto-filled vs. hand-entered, with a
   source or author reference); clinician-entered answers carry an attestation extension.
4. **Prior-auth submission and adjudication (PAS).** The provider submits a Bundle (Claim +
   QuestionnaireResponse + ServiceRequest); the payer adjudicates to one of three outcomes:
   - **Approved** — a `ClaimResponse` with an authorization number and validity period. The
     number is stored holder-side only; the network keeps no copy.
   - **Pended** — a `ClaimResponse` plus a `Task` naming exactly what's missing (an operative
     report, a clinician-attested item, a patient-reported item). The provider amends —
     attaching the document or attested answer plus a `Provenance` — and submits a
     claim-update on the same rails for re-adjudication. The payer's pended-claim ledger uses
     an atomic test-and-set so concurrent or replayed updates can't double-process.
   - **Denied** — a `ClaimResponse` carrying the machine-readable review action and denial
     code, appeal rights in process notes, and a corresponding `ExplanationOfBenefit` recorded
     payer-side for the patient to read.
5. **Federated query.** When required evidence lives at a *third* organization, the provider
   issues a narrow, consent-gated query routed through the Hub to the facility. It rides as a
   Da Vinci CDex data-request `Task` (named patient, named resource type, bounded scope — no
   bulk access); because each Task carries exactly one data-query, distinct resource types
   travel as distinct consent-gated legs rather than one broad pull. Consent is checked
   twice (centrally at token issuance, locally at the facility — and narrowness is enforced
   post-decrypt at the facility, as a rejection rather than a filter), and the returned record
   carries a Provenance citing the consent. The evidence then amends the pended claim. This is
   the non-aggregation property in action: data is fetched at need, under consent, leg by leg
   — never pre-pooled.
6. **Patient attestation.** When adjudication needs patient-reported information, the patient
   answers in their Smart Health account; the signed, patient-attributed answer travels back
   over a sealed `patient-authorship` leg and completes the claim.
7. **Patient access.** Patients read their determinations (and appeal information) through the
   PHG from the payer's Patient Access API, each read individually tokenized and audited.

---

## FHIR conformance

- **Runtime gate.** Every message is validated per-leg via FHIR `$validate` against a real,
  IG-enabled validation service (FHIR R4 + US Core; key resources pin their `meta.profile`).
  Fail-closed, in both directions, in the message path — not as an offline afterthought.
- **Profile conformance.** Separately from the runtime gate, the prior-auth surface (PAS
  request/update Bundles, Claim, ClaimResponse, DTR QuestionnaireResponses, the PDex
  ExplanationOfBenefit, and the Da Vinci CDex data-request Tasks that carry federated queries)
  is conformance-tested as a dedicated **pre-deployment gate** that drives profile-directed
  `$validate` against an IG-loaded validator, with a documented allowlist for licensed
  terminology an offline validator cannot expand. The pinned profile versions — US Core 6.1.0,
  Da Vinci CRD/DTR/PAS 2.0.1, and PDex 2.1.0 — are advertised in the discovery document, so a
  participant validates against exactly what the network does.
- **Published surface.** The payer publishes a Patient Access `CapabilityStatement`,
  conformance-tested against what is actually served.
- **Terminology.** LOINC, ICD-10-CM, CPT, HCPCS Level II, and X12 codes come from curated value
  sets and are validator-checked — never free-generated. Procedures carry their native coding
  system end to end: a HCPCS-coded order produces a HCPCS-coded determination and
  patient-readable `ExplanationOfBenefit`, never silently rewritten to CPT.
- **Delegated conformance.** In the optional native-delegation mode — where a holder's own Da
  Vinci system answers a leg — that system is the conformance authority for the resources it
  authors. The substrate still applies its full security fence to every such leg (sealing,
  per-operation authority, patient-binding, and tamper-evident audit), rather than re-validating
  the delegated system's payloads against its own profiles.

---

## Deployment shape

Every component is an independent service, and the same composition runs across environments
from **one provisioning root** — a single generator for every holder keypair, service signing
key, and the registry manifest — so deployments cannot drift apart in identity or trust.

In a cloud deployment, the topology is fully infrastructure-as-code: each service in its own
container with its own least-privilege identity, per-service secret isolation (each task can
read only its own key material; the Hub's secrets contain no decryption key), TLS at the
public edge, and OIDC in front of operator- and patient-facing surfaces. Deployment is
**holder-operated at the edge**: each participating organization terminates the substrate at
its own Smart Gateway, on its own infrastructure, behind its own boundary.

---

## External participation

Organizations join the network in one of two ways:

- **Option A — run the Smart Gateway.** Deploy the gateway at your boundary with your role
  and keys; it handles sealing, validation, tokens, and routing, and you implement the holder
  data interface against your own systems. The common, conformant case is **configuration
  only, no code**: run one published bundle — the gateway image together with its co-located
  IG-loaded FHIR validator — point it at a single discovery anchor, mount a registration bundle
  carrying your role and keys, and you are on the network. A provider running the gateway can
  originate workflows by pointing an existing Da Vinci-conformant client at its native ingress
  (CDS Hooks, DTR, PAS), and a payer can let its own Da Vinci endpoints answer the CRD/DTR/PAS
  legs — the gateway translates those interactions onto authorized sealed legs. A participant
  whose backend is not yet FHIR-conformant binds a connector rather than forking the gateway.
- **Option B — speak the protocol natively.** Implement the published wire contract in any
  stack: generate an X25519 + Ed25519 keypair, get admitted (self-service via the accounts
  service, or admin-gated via the registrar), then issue holder assertions, obtain
  per-operation tokens, seal envelopes, and route through the Hub. The gateway and a
  dependency-light client SDK are published as versioned artifacts under a documented stability
  contract, alongside a reference participant implementation that demonstrates the full flow.

Discovery is machine-readable: the accounts service's discovery document advertises live
endpoint URLs, the wire-protocol version, and FHIR profile versions, so a participant can
configure itself against a running network without out-of-band coordination. Admission is
live within seconds, requires proof of key possession, and every lifecycle event — admission,
rotation, revocation, deregistration — is signed into the audit chain.

The same contracts serve operator-run reference holders and external participants alike —
there is no separate "demo" path. This is demonstrated on a hosted preview network: two
independent, third-party Da Vinci reference implementations — an external provider and an
external payer, each running its own systems and holding its own keys — exchange a prior
authorization **with one another** across the network, request and decision, on exactly the
contracts above, with neither seeing the other's keys or infrastructure.

---

## How correctness is enforced

The architecture's properties are not documentation claims; they are executable:

- **Invariant tests** assert the structural properties live — the Hub holds no key, nothing
  aggregates, member IDs never appear in metadata or audit, every leg is tokenized and audited
  — and fail loudly if violated.
- **An adversarial mutation suite** pairs every guard with a rejection test: take a valid
  exchange, mutate exactly one thing (a token field, a payload byte, a sender, a subject), and
  assert the substrate rejects it.
- **Scenario conformance tests** drive each workflow end-to-end — holder ↔ Hub ↔ holder, never
  point-to-point — asserting the exact FHIR resources exchanged, both branches where a
  workflow branches, an audit record per leg, and Provenance where required.
- **Live conformance gates** validate the exchanged resources against real, IG-loaded FHIR
  validators, and a deployment smoke gate boots the fully separated service topology and runs
  every workflow through it.
- **Cross-organization interoperability gates** run independent, third-party FHIR reference
  implementations — an external provider and an external payer — against the live network,
  proving the published contracts carry real participants, not only the operator's reference
  holders.

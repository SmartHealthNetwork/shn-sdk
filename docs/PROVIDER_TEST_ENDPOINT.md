# Provider Test Endpoint — hosted Da Vinci prior-authorization test lane

**Audience:** external partners building a Da Vinci prior-authorization client — EHR
vendors, provider organizations, and the implementers working on their behalf. Client
registration is self-service; exchanging patient messages also requires the identity and
routing integration described below.

**What it is.** A hosted, publicly reachable Da Vinci prior-authorization test endpoint —
CRD (coverage requirements discovery), DTR (documentation templates and rules), and PAS
(prior-authorization submission) — at:

```
https://pa-test.shn-preview.org
```

Register a client and get a token using §2–3. Native exchange requires either verified
signed exchange context from an authorized integration, or authoritative subject linkage
and resolvable routing hints in the request. Registration and bearer tokens do not supply
signed exchange context or authoritative patient linkage. A bare patient id or patient
demographics cannot establish that linkage. Without the required context or linkage and
routing, the gateway refuses the exchange with `400 context_missing`.

**Current integration gap.** The production FHIR connector does not yet supply authoritative
network patient linkage. Test-door registration does not close this gap, and the resolver
used by synthetic test fixtures is not production support. The examples below describe
request shapes and payer answers; end-to-end execution requires a working exchange-context
or authoritative-linkage integration. This prerequisite describes the repository's current
native path; it is not a claim that a hosted deployment has completed that integration.

With exchange context established, requests can target either of two Da Vinci reference
payers, one on the **2.2** line and one on the **2.0** line. The unsigned addressing path
uses the `Coverage.payor` identifier to resolve the payer; signed context must identify the
intended recipient.

**Terms of use.**

- **Test lane only.** Every payer reachable from this endpoint is a test environment.
  Nothing here ever reaches a production payer.
- **Synthetic data only.** Send only synthetic test data — no real patient information,
  ever. The published test members below are the ones the endpoint holds records for.
  Native carriage of an external member still requires authoritative exchange identity
  (§8); possession of a patient id is insufficient. Every patient you use must be synthetic.
- **Application traffic evidence.** After owner activation, default capture includes raw
  request and response bodies, headers, and authentication material, including synthetic
  client secrets, bearer tokens and assertions. Evidence is accessible to ordinary staff
  in the operator console, with indefinite retention. Use synthetic credentials only.
  Collection is nonblocking and best effort: outages, capacity suspension, partial bodies
  and gaps are possible; traffic rejected before the application edge is not captured.
  This evidence is stored separately from routine stdout logs; raw bodies and tokens
  are not added to stdout. As of 2026-09-19, cloud capture remains OFF pending measured
  capacity acceptance and owner activation. Local two-reference-participant acceptance
  enables the same collectors with synthetic data.
- **Availability.** This endpoint remains available for integration testing, with no
  scheduled teardown date. It is a test service, not a production endpoint.
- **No verification, no SLA.** Registration is self-service and unauthenticated beyond
  possession of a key or secret — there is no identity check, no uptime guarantee, and no
  support contract. Treat it as a scratch environment.

---

## 1. What the endpoint offers

### 1.1 Routes

| Path | Bearer | What it is |
|---|---|---|
| `GET /.well-known/smart-configuration` | no | SMART discovery: token endpoint, auth methods, signing algorithms |
| `POST /register` | no | Self-service client registration (§2) |
| `POST /oauth/token` | no | Token endpoint, `client_credentials` only (§3) |
| `GET /cds-services` | no | CDS Hooks discovery — the services and prefetch templates below |
| `POST /cds-services/{id}` | yes | CRD — invoke one of the services in §1.2 |
| `POST /Questionnaire/$questionnaire-package` | yes | DTR — fetch a questionnaire package |
| `POST /Claim/$submit` | yes | PAS — submit a prior-authorization request |
| `POST /Claim/$inquire` | yes | PAS — ask the payer for the decision it holds on a submission (§1.6) |
| `GET /healthz` | no | Liveness |

**Not offered.** Subscription, `Questionnaire/$next-question` and `Questionnaire/$populate`
are not exposed here. A payer's later decision on a pended request is not pushed to you
(there is no notification path); you ask for it with `Claim/$inquire` (§1.6).

**Two aliases, so you can test as you are configured today.** A `POST` to a path that
is not in the table but ends in one of its operations — `/$submit` without `Claim/`, or
`/fhir/Claim/$submit` — is served as that operation (`POST /Claim/$submit`). A
`POST /cds-services/{id}` with an id this endpoint does not advertise, whose body `hook`
is one an advertised service carries — `order-sign-crd` with `"hook":"order-sign"` — is
served by that service (`shn-order-sign`). An aliased answer is the payer's answer
unchanged, plus two headers naming the canonical form:
`Link: </Claim/$submit>; rel="canonical"` (or `</cds-services/shn-order-sign>`) and a
`Warning: 299` that says the same in words. The canonical forms are the table above and
the ids in §1.2; use them in the configuration you take to production, because no other
endpoint on the network serves the aliases. Any other path, and any id whose hook no
advertised service carries, is answered `404` with the table or the advertised ids in the
body.

### 1.2 Hooks and CDS services

Two services are advertised, one per hook the payers behind this endpoint carry.
`GET /cds-services` needs no bearer, so you can read this before you register:

| Service id | Hook |
|---|---|
| `shn-order-sign` | `order-sign` |
| `shn-order-select` | `order-select` |

Post your request to the service whose hook matches the `hook` in your body. **The hook is
never changed on the way**, and each request goes to the payer's own service for that hook.
Three things worth knowing before you test:

- A payer that offers no service for your hook is refused before it is called, with `422`
  and the list of hooks it does offer.
- A payer's `order-select` service answers only the order families it covers. Both
  reference payers below adjudicate the `order-sign` examples in §5 and §6; the same
  orders sent to `shn-order-select` — with the `context.selections` array that hook
  requires — come back as a well-formed empty answer (`{"cards":[],"systemActions":[]}`),
  which is the payer's answer, not an error.
- **`order-dispatch` is not offered here.** Neither reference payer behind this endpoint
  carries the `order-dispatch` leg, so the endpoint does not advertise it: `/cds-services`
  lists `shn-order-sign` and `shn-order-select` only, and a request to
  `/cds-services/shn-order-dispatch` answers `404` naming the service ids that are offered —
  before routing, never as a generic `502`. Build against `order-sign` (§5, §6) or
  `order-select`. The listing is what this endpoint's payers carry; when a payer here
  carries `order-dispatch`, it is advertised again.

`appointment-book`, `encounter-start` and `encounter-discharge` have no service here.

### 1.3 Prefetch

All three services advertise the same six prefetch keys:

| Key | Template |
|---|---|
| `patient` | `Patient/{{context.patientId}}` |
| `coverage` | `Coverage?patient=Patient/{{context.patientId}}&_include=Coverage:payor` |
| `serviceHistory` | `ServiceRequest?patient=Patient/{{context.patientId}}` |
| `deviceHistory` | `DeviceRequest?patient=Patient/{{context.patientId}}&_include=DeviceRequest:performer` |
| `medicationHistory` | `MedicationRequest?patient=Patient/{{context.patientId}}` |
| `questionnaireResponses` | `QuestionnaireResponse?patient=Patient/{{context.patientId}}` |

What the endpoint does with your request:

- It **removes `fhirServer` and `fhirAuthorization`** — the payer never gets a route into
  your systems, and the endpoint never calls back into them.
- **Omitted prefetch keys remain absent.** Native carriage does not read local records or
  assemble missing source data. Omitting a key does not request a local source-assembly
  action, and does not by itself cause a missing-record `422`.
- Once the exchange context and routing prerequisites above are met, everything else in
  your request is carried to the payer as you sent it.

At `strict`, the gateway also checks the required CDS Hooks context shapes for
its advertised `order-select`, `order-sign`, and `order-dispatch` services. Each
requires a correctly cased string `context.patientId`; `order-select` also
requires string `userId`, string-array `selections`, and a `draftOrders` Bundle;
`order-sign` requires string `userId` and a `draftOrders` Bundle;
`order-dispatch` requires string-array `dispatchedOrders` and string `performer`.
A supported malformed context is refused as `cds.request.context`. At `none`,
`observe`, and `basic`, that deeper rule does not stop delivery. It does not
compare the supplied patient id with the authenticated subject or look up a
patient in the gateway's local records. The current source rule-set version is
`participant-conformance/4`; deployed release availability may differ.

**Send the prefetch data the payer needs yourself**, as the examples below do for `patient`
and `coverage`. Supplied prefetch does not establish authoritative patient linkage. On the
unsigned addressing path, absent or unresolvable patient/routing hints yield
`context_missing`; this is a context refusal, not a local source-record lookup failure.

### 1.4 What comes back

The payer's answer body is relayed to you as the payer sent it. In particular:

- **A CRD coverage decision arrives as a `systemActions` update on your order, not as a
  card.** The updated order carries the CRD IG's own coverage-information extension,
  `http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information`, whose
  sub-extensions include `coverage-assertion-id`, `covered`, `pa-needed`, `doc-needed` and
  the `questionnaire` canonical. That is what a client drives its DTR or PAS follow-up
  from. Both reference payers below answer with an empty `cards` array and one
  `systemActions` update.
- **DTR and PAS answers are the payer's own bytes**, relayed as received.
- HTTP-level signatures are not carried. CRD responses are served as
  `application/json`, DTR and PAS responses as `application/fhir+json`.

### 1.5 The reference payers and their routes

| Route | `Coverage.payor` identifier | Da Vinci line | Test member | Example order |
|---|---|---|---|---|
| `00301` | `urn:oid:2.16.840.1.113883.6.300\|00301` | **2.2** | `MBR-COVERED` | HCPCS `G0151` |
| `00300` | `urn:oid:2.16.840.1.113883.6.300\|00300` | **2.0** | `MBR-COVERED` | HCPCS `L8000`; `E1390` to pend |

Start with **route `00301`** (§5). It is the current Da Vinci reference payer, it is on the
2.2 line, and it is the route that demonstrates the whole loop: a CRD coverage answer, a
questionnaire package, a PAS submission that **pends with a `CommunicationRequest`**, and a
DTR relaunch keyed off that pend. Route `00300` (§6) is the 2.0-line route: the same
reference payer software, run as a hosted participant on the network and declaring the 2.0
line. Its PAS submission for the worked example is approved outright; an
oxygen-concentrator order (§6.4) **pends with a `Task`**, the 2.0-line pended shape.

Both routes are hosted participants, and a hosted participant takes gateway fixes at
published gateway releases: a fix announced for route `00301` or `00300` names the release
that carries it, and the route answers the old way until that release is rolled.

**Route `00001` is still available.** `urn:oid:2.16.840.1.113883.6.300|00001` reaches the
platform's own instance of the same 2.0-line reference payer. It answers every call in §6
identically, takes gateway fixes at every deploy rather than at a release, and writes its
internal server address (`http://localhost:8081/fhir/…`) into the `fullUrl`s of its PAS
answers. It is not the route to configure; a client already pointed at it can stay until it
next changes configuration.

The endpoint declares both the 2.0 and the 2.2 lines for CRD, DTR and PAS, and pairs with
each payer on a line they share. A request that cannot be carried to the payer's line
unchanged is refused rather than silently rewritten (§8).

### 1.6 Inquiry

`POST /Claim/$inquire` is answered on both routes. Send a PAS inquiry request Bundle
(`profile-pas-inquiry-request-bundle`) whose Coverage names the route's payer identifier
as in §1.5, whose Patient carries the member identifier you submitted with (typed `MB`),
and whose Claim names the requesting provider you submitted with: both reference payers
match an inquiry on the member identifier and the provider's NPI, and a query for a
submission they cannot match is answered with an empty result, not an error.

What comes back is the payer's own answer, relayed as sent. Measured on 2026-09-19 with the
Inferno PAS test kits, both routes answer `HTTP 200` and a `Parameters` resource whose
`responseBundle` parameter carries the PAS inquiry response Bundle — a `ClaimResponse` with
the decision the payer holds (on route `00301`, a pended decision also carries the
`CommunicationRequest` from §5.3). That is the PAS 2.2 inquiry answer, and it passes the
2.2.1 kit's inquiry test on route `00301`. The 2.0-line payer behind route `00001` answers
in the same shape, which the PAS 2.0.1 kit rejects (`expected Bundle, but received
Parameters`): the 2.0.1 operation returns the response Bundle itself. A client on the 2.0
line should read the Bundle out of the `Parameters` until that answer is carried at the
2.0 line; this document will change when it is.

---

## 2. Register a client

Two authentication methods are supported. Pick whichever fits your stack.

### Option A — `private_key_jwt` (recommended: SMART Backend Services style)

Generate a P-384 EC keypair:

```bash
openssl ecparam -name secp384r1 -genkey -noout -out client-key.pem
openssl ec -in client-key.pem -pubout -out client-pub.pem
```

Register the **public** key (never send the private key anywhere):

```bash
jq -n --arg pem "$(cat client-pub.pem)" \
  '{client_name: "Acme EHR test", auth_method: "private_key_jwt", alg: "ES384", public_key_pem: $pem}' \
  | curl -s https://pa-test.shn-preview.org/register \
      -H 'Content-Type: application/json' -d @-
```

Response:

```json
{"client_id": "3f9c...b21", "token_endpoint": "https://pa-test.shn-preview.org/oauth/token"}
```

(RSA keys of at least 2048 bits work too — register with `"alg": "RS384"` and an RSA PEM.)

### Option B — `client_secret` (shared-secret client_credentials)

```bash
curl -s https://pa-test.shn-preview.org/register \
  -H 'Content-Type: application/json' \
  -d '{"client_name": "Acme EHR test", "auth_method": "client_secret"}'
```

Response — **the `client_secret` is returned exactly once, at registration.** It is never
shown again and only its hash is stored; if you lose it, register a new client.

```json
{
  "client_id": "9a71...44c",
  "token_endpoint": "https://pa-test.shn-preview.org/oauth/token",
  "client_secret": "kR3v...9pQ"
}
```

Save both `client_id` and `client_secret` immediately.

---

## 3. Get a token

Discovery: `GET /.well-known/smart-configuration` advertises the token endpoint, the three
supported client authentication methods and the signing algorithms:

```bash
curl -s https://pa-test.shn-preview.org/.well-known/smart-configuration
```

```json
{
  "grant_types_supported": ["client_credentials"],
  "scopes_supported": ["system/Davinci.write"],
  "token_endpoint": "https://pa-test.shn-preview.org/oauth/token",
  "token_endpoint_auth_methods_supported": ["private_key_jwt", "client_secret_basic", "client_secret_post"],
  "token_endpoint_auth_signing_alg_values_supported": ["ES384", "RS384"]
}
```

### `private_key_jwt` — sign and exchange a client assertion

Build a signed `client_assertion` (any JWT library with ES384 support works; this uses
Python's `PyJWT` for illustration — `pip install pyjwt cryptography`):

```bash
CLIENT_ID=3f9c...b21   # from registration

ASSERTION=$(python3 - "$CLIENT_ID" <<'EOF'
import sys, time, uuid, jwt
client_id = sys.argv[1]
with open("client-key.pem") as f:
    private_key = f.read()
now = int(time.time())
claims = {
    "iss": client_id, "sub": client_id,
    "aud": "https://pa-test.shn-preview.org/oauth/token",
    "exp": now + 120, "iat": now, "jti": str(uuid.uuid4()),
}
print(jwt.encode(claims, private_key, algorithm="ES384"))
EOF
)

curl -s https://pa-test.shn-preview.org/oauth/token \
  -d grant_type=client_credentials \
  -d client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer \
  -d client_assertion="$ASSERTION"
```

Two rules the endpoint enforces on the assertion, both answered with
`401 invalid_client`:

- **`jti` is one-time.** Replaying an assertion is refused with `missing or replayed jti`.
  Mint a fresh `jti` per token request — a library that reuses one will fail on the second
  call.
- **The assertion's lifetime must be at most 5 minutes** (`exp - iat`), and `aud` must be
  the token endpoint URL exactly.

### `client_secret` — `client_secret_post`

```bash
curl -s https://pa-test.shn-preview.org/oauth/token \
  -d grant_type=client_credentials \
  -d client_id="$CLIENT_ID" \
  -d client_secret="$CLIENT_SECRET"
```

### `client_secret` — `client_secret_basic`

```bash
curl -s https://pa-test.shn-preview.org/oauth/token \
  -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials
```

All three return:

```json
{"access_token": "eyJhbGciOi...", "token_type": "bearer", "expires_in": 300, "scope": "system/Davinci.write"}
```

Tokens are short-lived (5 minutes) — fetch a fresh one per test run rather than caching
across sessions. The endpoint is redeployed routinely; a token stays valid for its five
minutes across a redeploy, and so does your registration. If a call returns `401`, the
token has expired or was never issued here: request a new one and retry.

```bash
TOKEN=<access_token from above>
```

---

## 4. The examples below

Both worked examples use the published test member `MBR-COVERED` ("Linda Johansson") and
differ only in the payer identifier and the ordered code. Each shows the sequence CRD,
then DTR, then PAS. Running it end to end depends on the identity/context integration
prerequisite and current gap stated above; registration and a bearer token alone do not
make the sequence runnable. The PAS request bundle is in
[Appendix A](#appendix-a--the-pas-request-bundle) so the examples stay readable.

---

## 5. Worked example — route `00301` (2.2 line)

The current Da Vinci reference payer, declaring the 2.2 line of CRD, DTR and PAS. Test
member **`MBR-COVERED`**, HCPCS **`G0151`** (physical therapy in the home health setting),
payer identifier `urn:oid:2.16.840.1.113883.6.300|00301`, hook `order-sign`.

The payer's answers are its own, relayed as received, with one correction applied on the
payer's side: its PAS response Bundle carries the resources its references name. The
published reference-payer code leaves them out, and a receiver that checks reference
closure has to refuse such a Bundle; this has been reported to its maintainers. Verdicts,
codes and every other element are the payer's own.

### 5.1 CRD

```bash
cat > crd-00301.json <<'EOF'
{
  "hook": "order-sign",
  "hookInstance": "quickstart-00301-order-sign",
  "fhirServer": "https://provider.example/fhir",
  "context": {
    "userId": "Practitioner/p1",
    "patientId": "MBR-COVERED",
    "selections": [],
    "draftOrders": {
      "resourceType": "Bundle",
      "type": "collection",
      "entry": [
        {
          "fullUrl": "urn:uuid:sr1",
          "resource": {
            "resourceType": "ServiceRequest",
            "id": "sr1",
            "status": "draft",
            "intent": "order",
            "subject": {"reference": "Patient/MBR-COVERED"},
            "insurance": [{"reference": "Coverage/c1"}],
            "code": {"coding": [{"system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "G0151", "display": "Services performed by a qualified physical therapist in the home health setting"}]}
          }
        }
      ]
    }
  },
  "prefetch": {
    "patient": {"resourceType": "Patient", "id": "MBR-COVERED"},
    "coverage": {
      "resourceType": "Coverage",
      "id": "c1",
      "status": "active",
      "beneficiary": {"reference": "Patient/MBR-COVERED"},
      "payor": [{"reference": "#cms-payer"}],
      "contained": [{"resourceType": "Organization", "id": "cms-payer", "name": "Centers for Medicare and Medicaid Services", "identifier": [{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00301"}]}]
    }
  }
}
EOF

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-00301.json | tee crd-00301-response.json
```

Expect `HTTP 200`, an empty `"cards"` array and one `systemActions` update on the order
carrying the coverage-information extension:

```json
{"cards":[],"systemActions":[{"type":"update","resource":{"resourceType":"ServiceRequest","id":"sr1","extension":[{"url":"http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information","extension":[
  {"url":"coverage","valueReference":{"reference":"Coverage/c1"}},
  {"url":"covered","valueCode":"conditional"},
  {"url":"pa-needed","valueCode":"auth-needed"},
  {"url":"doc-needed","valueCode":"clinical"},
  {"url":"doc-purpose","valueCode":"withpa"},
  {"url":"info-needed","valueCode":"OTH"},
  {"url":"questionnaire","valueCanonical":"http://example.org/fhir/Questionnaire/HomeHealthAssessment"},
  {"url":"reason","valueCodeableConcept":{"text":"Prior authorization required for home health services"}},
  {"url":"date","valueDate":"…"},
  {"url":"coverage-assertion-id","valueString":"home-health-…-c1"}]}],"…":"…"}}]}
```

`doc-needed` is `clinical`, so a client follows up with DTR; the `questionnaire` canonical
is what §5.2 fetches, and the `coverage-assertion-id` is what ties the follow-up back to
this decision.

### 5.2 DTR

```bash
cat > dtr-00301.json <<'EOF'
{
  "resourceType": "Parameters",
  "meta": {"profile": ["http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/dtr-qpackage-input-parameters"]},
  "parameter": [
    {
      "name": "coverage",
      "resource": {
        "resourceType": "Coverage",
        "id": "coverage-1",
        "status": "active",
        "beneficiary": {"reference": "Patient/MBR-COVERED"},
        "payor": [{"reference": "#payor-org"}],
        "contained": [{"resourceType": "Organization", "id": "payor-org", "active": true, "name": "Centers for Medicare and Medicaid Services", "identifier": [{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00301"}]}]
      }
    },
    {
      "name": "questionnaire",
      "valueCanonical": "http://example.org/fhir/Questionnaire/HomeHealthAssessment"
    }
  ]
}
EOF

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @dtr-00301.json
```

Expect `HTTP 200` and a `Parameters` resource with two parameters:

- `packagebundle` — a Bundle carrying the `HomeHealthAssessment` `Questionnaire`, its three
  prepopulation `Library` resources, a `ValueSet` and a `QuestionnaireResponse`;
- `outcome` — an `OperationOutcome` carrying one warning.

Use the `questionnaire` canonical exactly as the payer stated it in §5.1. The payer resolves
it exactly, so a `|version` its questionnaire does not carry is answered with a
`Questionnaire not found` outcome and no package.

**Why the warning, and how to clear it.** The payer skips demographic prepopulation because
it matched no member in its own records: the Coverage above carries no `subscriberId` and no
`beneficiary.identifier`, and the payer will not use the sender's `Patient/…` reference as a
lookup key. The outcome says so:

```
Patient demographic pre-population skipped: no Patient in this payer's records matched
Coverage.beneficiary.identifier or Coverage.subscriberId … Supply Coverage.subscriberId or
Coverage.beneficiary.identifier matching a payer member identifier.
```

You still receive the real `Questionnaire` and its Libraries. This is the payer's own
member-matching rule, reported to you unchanged.

You may also send the order from §5.1's answer (an `order` parameter) and its
`coverage-assertion-id` (a `context` parameter). Your `Parameters` reach the payer exactly
as you sent them.

### 5.3 PAS — a pend with a `CommunicationRequest`

Submit the bundle from [Appendix A](#appendix-a--the-pas-request-bundle) (a `G0151` `Claim`
bundle for `MBR-COVERED`, payer `00301`):

**The member identity the payer matches on is `Patient.identifier`** — for `MBR-COVERED`,
`http://example.org/MIN|12345678901`, as the appendix carries it. Your own identifiers may
sit beside it on the same Patient; the Patient's `id`, its demographics and
`Coverage.subscriberId` are not what the payer matches on, and `subscriberId` alone does
not match. A Patient carrying only your own identifier (an MRN under your system) is a
member the payer has never seen: it creates a Patient of its own for it, links the claim
to a Coverage whose beneficiary is still its stored member, and its answer names two
members — which the payer's gateway refuses as inconsistent patient linkage, and you see
as a `502` (§8.3). Two shapes answer without an error and are still not the exchange you
meant: a request without `Coverage.beneficiary`, or whose references point at an external
base, is answered `200` with `A3` "Not Required" and no `CommunicationRequest` instead of
the pend; and a `Claim.patient` given as an identifier only (`type` and `identifier`, no
`reference`) makes the payer answer `500`.

```bash
curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @pas-00301.json | tee pas-00301-response.json
```

Expect `HTTP 200` and a PAS response `Bundle` (`collection`) of ten entries — a
`ClaimResponse`, a `CommunicationRequest`, and the Patient, Claim, Coverage, ServiceRequest,
Organizations, PractitionerRole and Practitioner they reference — with every reference
resolving inside the Bundle:

- **`ClaimResponse`** — `outcome` `complete`; on the item, a review action of
  `A4` "Pending" (X12 `https://codesystem.x12.org/005010/306`), an
  `extension-administrationReferenceNumber`, and item trace numbers including the payer's
  own (`urn:trnorg:PASPAYER|AUTH-TRN…`); `communicationRequest` names the resource below.
- **`CommunicationRequest`** — `status` `active`, `identifier` the same trace number,
  `category` X12 755 code `15` ("Justification for Admissions"), and
  `payload[0].contentString` **`102089-0`**, the LOINC marker that tells you a DTR
  questionnaire is what the payer wants.

The trace number is new on every submission. Both resources name the payer's own member
record (`Patient/SubscriberExample`) rather than the `Patient/MBR-COVERED` you sent; that is
the payer's identity mapping, relayed as sent.

**Relaunch DTR from the pend.** Send `$questionnaire-package` with the Coverage from §5.2 and
the trace number as a `context` parameter, and **no `questionnaire`**:

```bash
TRN=$(jq -r '.entry[] | select(.resource.resourceType == "CommunicationRequest") | .resource.identifier[0].value' pas-00301-response.json)

jq --arg trn "$TRN" '.parameter = [.parameter[0], {"name": "context", "valueString": $trn}]' \
  dtr-00301.json > dtr-00301-pend.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @dtr-00301-pend.json
```

Expect `HTTP 200` and the same `HomeHealthAssessment` package. The payer resolved the trace
number to the pended claim: the `QuestionnaireResponse`'s `qr-context` extension names the
order **as the payer stored it from your submission**, and `intendedUse` is `withpa`.

The pend's later resolution is not pushed to you; ask for it with `Claim/$inquire` (§1.6).

---

## 6. Worked example — route `00300` (2.0 line)

The hosted conformance payer on the 2.0 line, the same Da Vinci reference payer software as
§5 at its 2.0 line. Test member **`MBR-COVERED`**, HCPCS **`L8000`**
(breast prosthesis, mastectomy bra), payer identifier
`urn:oid:2.16.840.1.113883.6.300|00300`, hook `order-sign`. This payer adjudicates coverage
against its own PlanDefinition and will not return a decision for an arbitrary CPT code.

### 6.1 CRD

```bash
cat > crd-00300.json <<'EOF'
{
  "hook": "order-sign",
  "hookInstance": "quickstart-00300-order-sign",
  "fhirServer": "https://provider.example/fhir",
  "context": {
    "userId": "Practitioner/p1",
    "patientId": "MBR-COVERED",
    "draftOrders": {
      "resourceType": "Bundle",
      "type": "collection",
      "entry": [
        {
          "fullUrl": "urn:uuid:sr1",
          "resource": {
            "resourceType": "ServiceRequest",
            "id": "sr1",
            "status": "draft",
            "intent": "order",
            "subject": {"reference": "Patient/MBR-COVERED"},
            "insurance": [{"reference": "Coverage/c1"}],
            "code": {"coding": [{"system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "L8000", "display": "Breast prosthesis, mastectomy bra"}]}
          }
        }
      ]
    }
  },
  "prefetch": {
    "patient": {"resourceType": "Patient", "id": "MBR-COVERED"},
    "coverage": {
      "resourceType": "Coverage",
      "id": "c1",
      "status": "active",
      "beneficiary": {"reference": "Patient/MBR-COVERED"},
      "payor": [{"reference": "#cms-payer"}],
      "contained": [{"resourceType": "Organization", "id": "cms-payer", "name": "Centers for Medicare and Medicaid Services", "identifier": [{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00300"}]}]
    }
  }
}
EOF

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-00300.json
```

Expect `HTTP 200`, an empty `"cards"` array and one `systemActions` update:

```json
{"cards":[],"systemActions":[{"type":"update","resource":{"resourceType":"ServiceRequest","id":"sr1","extension":[{"url":"http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information","extension":[
  {"url":"coverage","valueReference":{"reference":"Coverage/c1"}},
  {"url":"covered","valueCode":"covered"},
  {"url":"pa-needed","valueCode":"auth-needed"},
  {"url":"doc-needed","valueCode":"no-doc"},
  {"url":"questionnaire","valueCanonical":"http://example.org/fhir/Questionnaire/PriorAuthRequired"},
  {"url":"date","valueDate":"…"},
  {"url":"coverage-assertion-id","valueString":"prior-auth-…-c1"}]}],"…":"…"}}]}
```

**Known limitation on this route — the `doc-needed` code.** This payer answers `no-doc`
while also naming a `questionnaire` canonical. `no-doc` is not in the CRD 2.0 value set for
`doc-needed` (`clinical`, `admin`, `both`, `conditional`), and it contradicts the
questionnaire it supplies. It is the payer's own answer and it is relayed as sent rather
than corrected. If your client keys its DTR follow-up off `doc-needed`, this route will tell
it to skip DTR — use route `00301` (§5) to exercise the CRD-to-DTR handoff.

### 6.2 DTR

```bash
cat > dtr-00300.json <<'EOF'
{
  "resourceType": "Parameters",
  "meta": {"profile": ["http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/dtr-qpackage-input-parameters"]},
  "parameter": [
    {
      "name": "coverage",
      "resource": {
        "resourceType": "Coverage",
        "id": "coverage-1",
        "status": "active",
        "beneficiary": {"reference": "Patient/MBR-COVERED"},
        "payor": [{"reference": "#cms-payer"}],
        "contained": [{"resourceType": "Organization", "id": "cms-payer", "active": true, "name": "Centers for Medicare and Medicaid Services", "identifier": [{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00300"}]}]
      }
    },
    {
      "name": "questionnaire",
      "valueCanonical": "http://example.org/fhir/Questionnaire/PriorAuthRequired"
    }
  ]
}
EOF

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @dtr-00300.json
```

Expect `HTTP 200` and a `Parameters` resource whose `packagebundle` carries the real
`PriorAuthRequired` `Questionnaire` and a `QuestionnaireResponse`.

**Known limitations on this route.** The payer looks the member up in its own records by the
Coverage `subscriberId` or `beneficiary.identifier`; the Coverage in the example above
carries neither, and today the roster does not hold `MBR-COVERED` for this operation even
when they are supplied. The package therefore comes back with a `QuestionnaireResponse` whose
id names `__unresolved-member` rather than the test member, and the `outcome` parameter
carries the same demographic-prepopulation warning quoted in §5.2. Separately, the static
`PriorAuthRequired` form this payer publishes carries **no prepopulation logic** (no Library,
no prepopulation extensions or expressions), so no member-specific answers are prepopulated
on this route regardless of member resolution. You still receive the real `Questionnaire`.
This document will change when either limitation does. The example-URL `questionnaire`
canonical above is the payer's actual published value — preserve it, as an invented
canonical would request a different questionnaire.

### 6.3 PAS — an approval

Submit the bundle from [Appendix A](#appendix-a--the-pas-request-bundle), with the three
substitutions the appendix names for this route. The member-identity rule and the two
warnings in §5.3 hold here unchanged: the payer matches `Patient.identifier`
`http://example.org/MIN|12345678901`, not the Patient id or `subscriberId`.

```bash
curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @pas-00300.json
```

Expect `HTTP 200` and a PAS response `Bundle` (`collection`) of ten entries containing
exactly one `ClaimResponse` plus the resources it references, among them the payer's own
`Organization/example`. For this request the `ClaimResponse` has `outcome` `complete` and
a review action of `A1` "Certified in total".

**The `fullUrl`s are the payer's own.** Six of the ten entries carry a `fullUrl` under this
payer's own public host on the preview environment (`https://….shn-preview.org/fhir/…`),
exactly as the payer sent them; the rest keep the `http://example.org/fhir/…` addresses from your request.
None of them is meant to be fetched — every reference in the Bundle resolves to an entry
inside it, so resolve by `fullUrl` within the Bundle, not over the network.

**Where the authorization number is.** It is **not** in `ClaimResponse.preAuthRef`, which
this payer leaves absent. It is on the item's adjudication, in the `number` sub-extension of
`extension-reviewAction`, alongside the review-action code:

```json
{"url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction",
 "extension": [
   {"url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",
    "valueCodeableConcept": {"coding": [{"system": "https://codesystem.x12.org/005010/306", "code": "A1", "display": "Certified in total"}]}},
   {"url": "number", "valueString": "AUTH-…"}]}
```

The item also carries `extension-itemPreAuthPeriod` (the authorization's validity window)
and `extension-itemPreAuthIssueDate`. As on route `00301`, the `ClaimResponse` names the
payer's own member record (`Patient/SubscriberExample`), not the patient id you sent.

Approval and denial both come back as a Bundle; inspect the decision inside it. A pending
decision uses review-action code `A4` — see §5.3 for the 2.2-line pend (a
`CommunicationRequest`) and §6.4 for the 2.0-line pend (a `Task`).

### 6.4 A pend on this route — `E1390` with a `Task`

This payer pends an oxygen-concentrator order. Take the §6.3 request and change both
`"code": "L8000"` codings to `"code": "E1390"` (on `Claim.item.productOrService` and on the
`ServiceRequest`), with a Bundle identifier of your own, and submit it the same way. Expect
`HTTP 200` and a ten-entry Bundle whose `ClaimResponse` is `outcome` `queued` with review
action `A4` "Pending", plus a `Task` (`status` `requested`, code
`attachment-request-questionnaire`): the PAS 2.0 pended model, in which the payer asks for
documentation through a `Task` rather than the `CommunicationRequest` of §5.3. The `G0151`
home-health order of §5, sent to this route, is answered `A3` "Not certified" — a denial,
and this payer's decision for that order.

---

## 7. Published test members

| Member ID | Persona | Payer route | `Coverage.payor` identifier | Expect |
|---|---|---|---|---|
| `MBR-COVERED` | Linda Johansson | `00301` (2.2 line) | `urn:oid:2.16.840.1.113883.6.300\|00301` | CRD coverage answer, questionnaire package, and a PAS pend with a `102089-0` `CommunicationRequest` you can relaunch DTR from (§5) |
| `MBR-COVERED` | Linda Johansson | `00300` (2.0 line) | `urn:oid:2.16.840.1.113883.6.300\|00300` | CRD coverage answer, questionnaire package and a PAS approval, plus a PAS pend with a `Task` for `E1390` (§6.4); the DTR package names an unresolved member, and `doc-needed` is off-value-set (§6) |

These are the published test members, and the only ones the endpoint holds records for. In
a PAS request both payers match the member on `Patient.identifier`
(`http://example.org/MIN|12345678901` for `MBR-COVERED`, §5.3), not on the Patient id. A
member id the endpoint does not hold requires authenticated exchange context or explicit
authoritative identity linkage (§8.1). The endpoint does not derive external identity
from the Patient's demographics. A payer's answer for a member it does not hold remains
the payer's own answer. On route `00300` the DTR limitation in §6.2 applies even to the
published member.

---

## 8. Fail-closed behavior and limits

Nothing here is silently dropped, translated or invented. Every refusal is explicit.

### 8.1 Routing and membership

- **Unresolvable unsigned routing → `400 context_missing`.** Without verified signed
  context, the gateway needs authoritative subject linkage and request hints that resolve
  a registered recipient. An unknown payer identifier or missing routing hints cannot
  establish that context. The routes in §1.5 are the ones documented here with test members.
- **External members require authoritative exchange identity.** SHN_ACCEPT_UNKNOWN_MEMBERS is a deprecated compatibility no-op.
  Both `0` and `1` are ignored with a startup warning. Native exchange accepts a verified
  signed context or explicit authoritative subject linkage; absent linkage is
  `context_missing`. Patient demographics never supply missing network identity.
  At `none` and `observe`, native carriage does not require a local roster entry or compare
  payload patients. Explicit local source-data assembly and local business actions retain
  their independent source and authority checks. This compatibility setting requests no
  Patient insertion and authorizes no record disclosure.
- **Local source actions have separate requirements.** Explicit local source-assembly
  actions retain their own source and authority requirements. A local workflow that
  explicitly builds a request from its participant's records must refuse when required
  source facts are missing or disclosure is unauthorized. Native posting to the routes in
  §1.1 does not invoke that assembly by leaving out prefetch. Omitted keys stay absent (§1.3);
  the payer may itself require data to process the request.

### 8.2 Hooks and services

- **Unknown service id → served by its hook, else `404`.** An id this endpoint does not
  advertise is served by the advertised service for the `hook` in your body, with the
  canonical id named in the answer's `Link` and `Warning` headers (§1.1). When no
  advertised service carries that hook, the `404` lists the advertised ids with the hook
  each carries. If you see it, change the service id in your CDS Hooks client
  configuration to one of them; the hook stays as it is.
- **Hook and service disagree → `400`.** Sending `"hook": "order-sign"` to
  `shn-order-select` is refused; the error names both.
- **The payer offers no service for your hook → `422`**, with the hooks it does offer. A hook
  no payer here carries at all, `order-dispatch`, is not advertised and is refused earlier,
  with `404` and the service ids that are offered (§1.2).

### 8.3 `502` — refused, never altered

A `502` from this endpoint means a message could not be carried faithfully, so it was not
carried at all. The cases:

- the request cannot be carried to the payer's IG line without changing it;
- the payer's CRD answer is not a valid CDS Hooks response;
- the payer does not accept the framed DTR operation;
- the payer's PAS decision cannot be stated as sent — for example a decision that denies
  while also naming an authorization number, or decision detail outside the code systems
  the decision record binds;
- the payer's PAS response Bundle names a resource it does not carry. The body says which:
  `{"error":"engine: invalid native PAS response Bundle: PAS response graph: entry 6
  (ServiceRequest/1810) /subject references Patient \"https://…/Patient/MBR-COVERED\",
  which is no entry of the Bundle"}` — the entry that made the reference, the element,
  the reference as the payer wrote it, and why it does not resolve. The answer is refused
  whole, never completed or trimmed on the way through; the payer's own record is the one
  to correct. A resource the payer places under a URN fullUrl (`urn:uuid` or `urn:oid`)
  needs no `id`, and a `Type/id` reference inside it (which FHIR gives no base to resolve
  against) is read as the one entry whose address ends in that `Type/id`; two such entries,
  or none, is a refusal that says so.

A `502` also covers a request that could not be routed at all — no payer behind this
endpoint carries the leg the request needs; the body is `{"error":"hub routing failed"}`.
No advertised service reaches that case today (§1.2).

A `504` is the one exchange failure the endpoint names rather than leaving generic: the
leg to the payer produced no answer within the endpoint's gateway's wait (30 seconds; the
Hub, the payer's gateway and the payer's own system share that budget). The body is
`{"error":"no answer on the hub leg within 30s (hub leg timeout)"}` — the number is the
gateway's own leg deadline — carried as an `OperationOutcome` with issue code `timeout` on
the FHIR operation routes. The endpoint relays its gateway's `504` and body as they are.

A `502` is a failed exchange, not a payer verdict. Retrying an identical request will
produce the same result. A `504` is the same kind of failure with its cause named; a
retry may succeed once the far side is answering within the budget again.

**A refusal the payer's gateway itself produces is not a `502`.** A member the payer does
not hold, a request with no order to decide on, a validation failure: these come back with
the payer's own status and error text — for example
`400 {"error":"no order (ServiceRequest or DeviceRequest) in draftOrders"}` for an
`order-sign` request whose `draftOrders` is empty. `502 hub routing failed` means the
exchange machinery failed, not that the payer disagreed.

### 8.4 Rate and size limits

| Limit | Value | On exceeding |
|---|---|---|
| Registration, per source IP | 5 per hour | `429` |
| Requests per client | 60 per minute | `429` |
| Concurrent requests in flight | 8 | `503` |
| Request body | 5 MiB | `400` |

A body over the cap is refused before any of it is forwarded, with
`{"error":"request body exceeds 5 MiB"}`.

If you are scripting repeated registration/token/CRD/DTR/PAS runs in a loop, register once
and reuse the client — a normal integration test needs a handful of registrations at most.

### 8.5 When something fails, quote `X-Correlation-Id`

Every answer from this endpoint carries an `X-Correlation-Id` header: the id the exchange
is recorded under on our side. If a call does not do what you expect, send us that value
and the time of the call, and we can find the request, the legs it ran and the answer the
payer gave without you sending the body again. It is on refusals as much as on successes,
including the endpoint's own `4xx` and `5xx` answers.

You can also send your own. An `X-Correlation-Id` request header of up to 64 characters
(letters, digits, `.`, `_` and `-`) is used as the id of the exchange and comes back on the
answer, so the id your integration test already tracks is the one we find. A header outside
that shape is ignored and an id is assigned instead.

---

## 9. What acceptance here does and does not mean

A `200` and a payer decision prove the exchange reached the payer and the payer accepted the
request. That is **not** IG-profile certification, of the endpoint or of the payloads in this
document, and a copy of these payloads will inherit their known findings.

The route `00300` payloads in §6 double as the automated payer-acceptance checks run against
this endpoint; the captured synthetic fixture is accepted by the conformance payer, and it is
**not certified against the full** `hl7.org/fhir/us/davinci-pas` profiles. The Inferno DTR and PAS test kits were run
against this endpoint on 2026-09-03, with a PAS follow-up on 2026-09-10 (run records
available on request). What is known about each:

- **PAS request bundle** against `profile-pas-request-bundle` 2.0.1: the identifier systems
  on the Bundle, Claim, Patient and Coverage are `http://example.org/…` placeholders, which
  the profile rejects, and the Patient and Coverage `MIN` systems also cause target-profile
  failures; `Coverage.relationship` carries a `subscriber-relationship` code where the
  profile requires an X12 slice.
- **DTR request** against `dtr-qpackage-input-parameters` 2.0.1: the Coverage carries no
  member identifier or `subscriberId` (`us-core-15`) and no `relationship` (a constraint the
  kit evaluated against CRD 2.2.1, which its DTR 2.0.1 package pulls in); the questionnaire
  canonical is the payer's example URL (§6.2).
- **CRD request**: no CRD test kit has been run against this endpoint; treat the CRD payload
  as payer-accepted only.
- **The `00301` payloads in §5** have not been run against the Inferno 2.2.1 kits. Payer
  acceptance on that route likewise **does not certify** them.

A few further points if you copy these payloads:

- The PAS bundle's `Patient.link` self-reference (subscriber is the beneficiary) is not
  required by PAS, and it crashes the PAS kit's reference walker (a kit defect, reported
  upstream) — drop it in your own copy.
- Independently of the bytes, structural validation of a PAS request against the 2.0
  profiles needs the definition of the R5 cross-version `Claim.encounter` extension, which
  HL7 publishes in its cross-version package (`hl7.fhir.uv.xver-r5.r4`), not in the PAS
  package. A validator you run without it cannot evaluate the slicing on the Claim's
  extensions; that is a limit of the validator's package set, not a defect in the payload.
- A validator without licensed CMS HCPCS terminology content reports error-level terminology
  findings on the HCPCS codings (`L8000`, `G0151`). The codes are valid, so this is a
  terminology-availability limit of the validator you run, not a payload to correct.

---

## Appendix A — the PAS request bundle

One bundle serves both routes. As written it is the **route `00301`** request (§5.3): a
`G0151` order for `MBR-COVERED` against payer `00301`. Save it as `pas-00301.json`.

For the **route `00300`** request (§6.3), save a copy as `pas-00300.json` with three
substitutions (for the pend in §6.4, use `"code": "E1390"` in substitution 1 instead):

1. both `"code": "G0151"` codings → `"code": "L8000"` (on `Claim.item.productOrService` and
   on the `ServiceRequest`);
2. the Coverage's `payor[0].identifier.value`, `"00301"` → `"00300"` (this is what routes the
   request; a blind find-and-replace on `00301` also rewrites the Bundle `identifier.value`
   below, which substitution 3 replaces anyway);
3. the Bundle `identifier.value` → any value of your own, so your submissions are
   distinguishable.

```json
{
  "entry": [
    {
      "fullUrl": "http://example.org/fhir/Claim/prior-auth-required-claim",
      "resource": {
        "careTeam": [
          {
            "extension": [
              {
                "url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-careTeamClaimScope",
                "valueBoolean": true
              }
            ],
            "provider": {
              "reference": "PractitionerRole/ReferralPractitionerRoleExample"
            },
            "sequence": 1
          }
        ],
        "created": "2026-06-19T21:54:38+00:00",
        "extension": [
          {
            "extension": [
              {
                "url": "applicationSenderCode",
                "valueString": "8189991234"
              },
              {
                "url": "applicationReceiverCode",
                "valueString": "1234567893"
              }
            ],
            "url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-TransmissionIdentifiers"
          }
        ],
        "id": "prior-auth-required-claim",
        "identifier": [
          {
            "system": "http://example.org/PATIENT_EVENT_TRACE_NUMBER",
            "value": "277b6b27-0b0f-4927-ae4d-f0cb242a3a9c"
          }
        ],
        "insurance": [
          {
            "coverage": {
              "reference": "Coverage/InsuranceExample"
            },
            "focal": true,
            "sequence": 1
          }
        ],
        "insurer": {
          "reference": "Organization/InsurerExample"
        },
        "item": [
          {
            "careTeamSequence": [
              1
            ],
            "category": {
              "coding": [
                {
                  "code": "3",
                  "display": "Consultation",
                  "system": "https://codesystem.x12.org/005010/1365"
                }
              ]
            },
            "extension": [
              {
                "url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-certificationType",
                "valueCodeableConcept": {
                  "coding": [
                    {
                      "code": "I",
                      "display": "Initial",
                      "system": "https://codesystem.x12.org/005010/1322"
                    }
                  ]
                }
              },
              {
                "url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-serviceItemRequestType",
                "valueCodeableConcept": {
                  "coding": [
                    {
                      "code": "HS",
                      "display": "Health Services Review",
                      "system": "https://codesystem.x12.org/005010/1525"
                    }
                  ]
                }
              },
              {
                "url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-itemTraceNumber",
                "valueIdentifier": {
                  "system": "http://example.org/ITEM_TRACE_NUMBER",
                  "value": "prior-auth-required-trace"
                }
              },
              {
                "url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-requestedService",
                "valueReference": {
                  "reference": "ServiceRequest/prior-auth-required-service-request"
                }
              }
            ],
            "locationCodeableConcept": {
              "coding": [
                {
                  "code": "11",
                  "display": "Office",
                  "system": "https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set"
                }
              ]
            },
            "productOrService": {
              "coding": [
                {
                  "code": "G0151",
                  "system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
                }
              ]
            },
            "sequence": 1
          }
        ],
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim"
          ]
        },
        "patient": {
          "reference": "Patient/MBR-COVERED"
        },
        "priority": {
          "coding": [
            {
              "code": "normal",
              "display": "Normal",
              "system": "http://terminology.hl7.org/CodeSystem/processpriority"
            }
          ]
        },
        "provider": {
          "reference": "Organization/UMOExample"
        },
        "resourceType": "Claim",
        "status": "active",
        "type": {
          "coding": [
            {
              "code": "professional",
              "display": "Professional",
              "system": "http://terminology.hl7.org/CodeSystem/claim-type"
            }
          ]
        },
        "use": "preauthorization"
      }
    },
    {
      "fullUrl": "http://example.org/fhir/Patient/MBR-COVERED",
      "resource": {
        "communication": [
          {
            "language": {
              "coding": [
                {
                  "code": "en",
                  "display": "English",
                  "system": "urn:ietf:bcp:47"
                }
              ]
            }
          }
        ],
        "gender": "male",
        "id": "MBR-COVERED",
        "identifier": [
          {
            "system": "http://example.org/MIN",
            "value": "12345678901"
          },
          {
            "system": "http://example.org/MIN",
            "type": {
              "coding": [
                {
                  "code": "MB",
                  "display": "Member Number",
                  "system": "http://terminology.hl7.org/CodeSystem/v2-0203"
                },
                {
                  "code": "MR",
                  "display": "Medical record number",
                  "system": "http://terminology.hl7.org/CodeSystem/v2-0203"
                }
              ]
            },
            "value": "12345678901"
          }
        ],
        "link": [
          {
            "other": {
              "reference": "Patient/MBR-COVERED"
            },
            "type": "seealso"
          }
        ],
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-beneficiary",
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-subscriber"
          ]
        },
        "name": [
          {
            "family": "SMITH",
            "given": [
              "JOE"
            ]
          }
        ],
        "resourceType": "Patient",
        "telecom": [
          {
            "system": "phone",
            "value": "555-0100"
          }
        ]
      }
    },
    {
      "fullUrl": "http://example.org/fhir/Organization/InsurerExample",
      "resource": {
        "active": true,
        "id": "InsurerExample",
        "identifier": [
          {
            "system": "http://hl7.org/fhir/sid/us-npi",
            "value": "1234567893"
          }
        ],
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-insurer"
          ]
        },
        "name": "MARYLAND CAPITAL INSURANCE COMPANY",
        "resourceType": "Organization",
        "type": [
          {
            "coding": [
              {
                "code": "PR",
                "system": "https://codesystem.x12.org/005010/98"
              }
            ]
          }
        ]
      }
    },
    {
      "fullUrl": "http://example.org/fhir/Organization/UMOExample",
      "resource": {
        "active": true,
        "id": "UMOExample",
        "identifier": [
          {
            "system": "http://hl7.org/fhir/sid/us-npi",
            "value": "8189991234"
          }
        ],
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-requestor"
          ]
        },
        "name": "DR. JOE SMITH CORPORATION",
        "resourceType": "Organization",
        "type": [
          {
            "coding": [
              {
                "code": "X3",
                "system": "https://codesystem.x12.org/005010/98"
              }
            ]
          }
        ]
      }
    },
    {
      "fullUrl": "http://example.org/fhir/Coverage/InsuranceExample",
      "resource": {
        "beneficiary": {
          "reference": "Patient/MBR-COVERED"
        },
        "class": [
          {
            "type": {
              "coding": [
                {
                  "code": "group",
                  "display": "Group",
                  "system": "http://terminology.hl7.org/CodeSystem/coverage-class"
                }
              ]
            },
            "value": "GRP-001"
          },
          {
            "type": {
              "coding": [
                {
                  "code": "plan",
                  "display": "Plan",
                  "system": "http://terminology.hl7.org/CodeSystem/coverage-class"
                }
              ]
            },
            "value": "PLAN-001"
          }
        ],
        "id": "InsuranceExample",
        "identifier": [
          {
            "system": "http://example.org/MIN",
            "type": {
              "coding": [
                {
                  "code": "MB",
                  "display": "Member Number",
                  "system": "http://terminology.hl7.org/CodeSystem/v2-0203"
                },
                {
                  "code": "MR",
                  "display": "Medical record number",
                  "system": "http://terminology.hl7.org/CodeSystem/v2-0203"
                }
              ]
            },
            "value": "1122334455"
          }
        ],
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-coverage"
          ]
        },
        "payor": [
          {
            "identifier": {
              "system": "urn:oid:2.16.840.1.113883.6.300",
              "value": "00301"
            },
            "reference": "Organization/InsurerExample"
          }
        ],
        "period": {
          "start": "2026-06-19T21:54:38+00:00"
        },
        "relationship": {
          "coding": [
            {
              "code": "self",
              "display": "Self",
              "system": "http://terminology.hl7.org/CodeSystem/subscriber-relationship"
            }
          ]
        },
        "resourceType": "Coverage",
        "status": "active",
        "subscriber": {
          "reference": "Patient/MBR-COVERED"
        },
        "subscriberId": "1122334455"
      }
    },
    {
      "fullUrl": "http://example.org/fhir/PractitionerRole/ReferralPractitionerRoleExample",
      "resource": {
        "id": "ReferralPractitionerRoleExample",
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-practitionerrole"
          ]
        },
        "practitioner": {
          "reference": "Practitioner/ReferralPractitionerExample"
        },
        "resourceType": "PractitionerRole",
        "telecom": [
          {
            "system": "phone",
            "value": "4029993456"
          }
        ]
      }
    },
    {
      "fullUrl": "http://example.org/fhir/Practitioner/ReferralPractitionerExample",
      "resource": {
        "id": "ReferralPractitionerExample",
        "identifier": [
          {
            "system": "http://hl7.org/fhir/sid/us-npi",
            "value": "1234567893"
          }
        ],
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-practitioner"
          ]
        },
        "name": [
          {
            "family": "WATSON",
            "given": [
              "SUSAN"
            ]
          }
        ],
        "resourceType": "Practitioner"
      }
    },
    {
      "fullUrl": "http://example.org/fhir/ServiceRequest/prior-auth-required-service-request",
      "resource": {
        "code": {
          "coding": [
            {
              "code": "G0151",
              "system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
            }
          ]
        },
        "id": "prior-auth-required-service-request",
        "intent": "order",
        "meta": {
          "profile": [
            "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-servicerequest"
          ]
        },
        "resourceType": "ServiceRequest",
        "status": "active",
        "subject": {
          "reference": "Patient/MBR-COVERED"
        }
      }
    }
  ],
  "identifier": {
    "system": "http://example.org/SUBMITTER_TRANSACTION_IDENTIFIER",
    "value": "door-check-00301-pas-submit"
  },
  "meta": {
    "profile": [
      "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-pas-request-bundle"
    ]
  },
  "resourceType": "Bundle",
  "timestamp": "2026-06-19T21:54:38.534+00:00",
  "type": "collection"
}
```

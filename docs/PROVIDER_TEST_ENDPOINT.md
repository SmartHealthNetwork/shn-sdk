# Provider Test Endpoint — hosted Da Vinci prior-authorization test lane

**Audience:** external partners building a Da Vinci prior-authorization client — EHR
vendors, provider organizations, and the implementers working on their behalf. Everything
here is self-service: you register your own client and need nothing configured on the SHN
side.

**What it is.** A hosted, publicly reachable Da Vinci prior-authorization test endpoint —
CRD (coverage requirements discovery), DTR (documentation templates and rules), and PAS
(prior-authorization submission) — at:

```
https://pa-test.shn-preview.org
```

Register a client, get a token, and send real Da Vinci requests. Behind the endpoint your
request is routed to a payer by the `Coverage.payor` identifier you send. Two Da Vinci
reference payers sit behind it, one on the **2.2** line and one on the **2.0** line, so you
can run the identical request pattern against two IG lines just by changing the Coverage,
without deploying anything of your own.

**Terms of use.**

- **Test lane only.** Every payer reachable from this endpoint is a test environment.
  Nothing here ever reaches a production payer.
- **Synthetic data only.** Send only synthetic test data — no real patient information,
  ever. The published test members below are the ones the endpoint holds records for; a
  member it does not hold is carried under the id you send (§8), so any patient you use
  must be synthetic too. Use credentials made only for this endpoint.
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

All three services advertise the same six prefetch keys. From shn-gateway v0.60.0 the
templates are the Da Vinci reference payer's order-sign templates: the patient named by its
bare id, a status filter on the coverage and each history, and no `_include`, which CDS
Hooks does not list among the query features a client supports:

```json
"prefetch": {
  "patient": "Patient/{{context.patientId}}",
  "coverage": "Coverage?patient={{context.patientId}}&status=active",
  "serviceHistory": "ServiceRequest?patient={{context.patientId}}&status=active,completed",
  "deviceHistory": "DeviceRequest?patient={{context.patientId}}&status=active,on-hold,completed",
  "medicationHistory": "MedicationRequest?patient={{context.patientId}}&status=active,completed",
  "questionnaireResponses": "QuestionnaireResponse?patient={{context.patientId}}&status=completed"
}
```

Before shn-gateway v0.60.0 they were:

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
  your systems. From shn-gateway v0.60.0 the endpoint may first use them to read your
  Coverage (and at most one payor Organization) to choose the payer (next bullets and
  §8.1); it never calls back into your systems otherwise. From shn-gateway v0.61.0 the
  Coverage it read that way, with its payor Organization, is carried to the payer as
  `prefetch.coverage`, since the payer cannot read it once `fhirServer` is removed.
- It **adds nothing of its own**. A prefetch key you leave out is left out of what the payer
  receives; from shn-gateway v0.61.0 the one exception is the coverage it read through your
  `fhirServer` (§8.1), carried exactly as your server returned it.
- From shn-gateway v0.60.0, a `coverage` you send that names its payor only as
  `Organization/<id>` (what the template returns when your server does not add the
  Organization) is routed by that Organization, read where that id is yours, only to choose
  the payer; your `coverage` is sent to the payer byte for byte. When your request names a
  `fhirServer`, the Organization is read once through it with your `fhirAuthorization` token,
  never from the endpoint's own records (a different server, whose ids are not yours). When it
  names none, the endpoint's own records are read, only when they name the patient by
  `context.patientId` (a published test member) and hold that Organization. Otherwise the
  request is refused `422 no payer identifier on member coverage: Coverage.payor is a
  reference to an Organization the gateway could not read; send the payor Organization with
  the coverage, or a payor identifier`. A payor reference written another way is not read: an
  absolute one (other than on your `fhirServer`) is refused `422 no payer identifier on member
  coverage: Coverage.payor is a reference to an Organization the gateway could not resolve;
  send the payor Organization as a Bundle entry whose fullUrl is that reference, or a payor
  identifier` (an entry with that `fullUrl` of a Bundle your request carries, your `coverage`
  Bundle or another prefetch value, routes it), and a
  versioned one, or one with a fragment, a leading slash or a dot segment, `422 no payer
  identifier on member coverage: Coverage.payor is a reference to an Organization the gateway
  does not read; send a payor identifier with the coverage`. The read's requirements are §8.1's, and so are
  its refusals, with the reason after `no payer identifier on member coverage: ` instead of
  `no coverage to route by: ` (for example `412 no payer identifier on member coverage:
  fhirServer refused the fhirAuthorization token`).
- If you leave out `coverage`, the endpoint looks the member's coverage up in its own
  synthetic records only to choose the payer; that coverage is not sent to the payer. From
  shn-gateway v0.60.0 the payer is chosen from the member's active coverage when it has one,
  so a cancelled coverage naming another payer is ignored; a member with no active coverage
  is still sent to the payer its coverage names (when its coverages name one payer), which
  answers that it does not cover the member. For a member those records do not hold, it reads
  the coverage instead through the `fhirServer` your request names (your own FHIR server),
  with your `fhirAuthorization` token, to choose the payer, and chooses the same way (§8.1);
  before shn-gateway v0.61.0 that coverage was never sent, and from shn-gateway v0.61.0 it is
  carried as `prefetch.coverage` (§8.1). A request it finds no coverage to route by for is
  refused `412` (§8.1).
- A request routed by its own synthetic records (or, before shn-gateway v0.61.0, through your
  `fhirServer`) reaches the payer **with no Coverage**, and gets whatever the
  payer's own system answers for it. From shn-gateway v0.60.0 the payer's gateway carries it
  as sent; earlier releases of a payer gateway that maps its payer identity refused it `400
  payer backend identity mapping: inbound Coverage carries no resolvable payor identifier`.
  A payer's system may not decide on a request without the member's Coverage, or may refuse
  it.
- Everything else in your request is carried to the payer as you sent it.

So **send every prefetch value you want the payer to see**, and above all `coverage`: a
Coverage whose payor names the payer (its identifier, inline or on a payor Organization in
the same prefetch) is what the payer decides on. The examples below carry all six
keys: the member's `patient` and `coverage`, and the four history keys as empty searchsets.

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

**Test data on these two routes is cleared on a schedule.** The shared reference payers
behind routes `00300` and `00301` start again from their published test data weekly, on
Sundays from 07:00 UTC, and nightly from 07:00 UTC (03:00 US Eastern) from 2026-10-07 through
2026-10-14, the event week, and occasionally at other times: a submission made before a
clearing is not found by an inquiry (§1.6) after it, so submit again before you inquire.

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

On route `00301` an inquiry takes longer the more claims the payer holds, since its last
clearing, for the same member and requesting provider. Submissions that name your own
provider organization, with its own NPI, as the requester keep your inquiries independent
of everyone else's claims; submissions that send the example provider of Appendix A share
it with every participant who does the same.

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

Every registration names who is testing, so SHN staff can tell whose calls they are
looking at, help when something fails, and report how the testing went. These fields are
required:

| Field | What to send |
|---|---|
| `client_name` | A name for this client, e.g. `"Acme EHR test"` |
| `organization` | Your organization |
| `contact_name` | The person SHN staff should contact about this client |
| `contact_email` | That person's email address, as a plain address (`name@example.org`) |
| `system_under_test` | The product or system you are testing, e.g. `"Acme EHR 24.1"` |
| `participant_type` | One of `ehr_vendor`, `provider_organization`, `intermediary`, `other` |

Each is plain text of at most 256 characters (320 for the email), with surrounding whitespace
removed and no control characters such as line breaks. A registration missing any of them
is refused with `400`, and the answer names every missing field, for example
`missing required registration fields: contact_email, system_under_test`. SHN staff can
correct the details you registered, and can group several of your clients under one
organization; you don't need to register again to fix a typo.

Two authentication methods are supported. Pick whichever fits your stack.

### Option A — `private_key_jwt` (recommended: SMART Backend Services style)

Generate a P-384 EC keypair:

```bash
openssl ecparam -name secp384r1 -genkey -noout -out client-key.pem
openssl ec -in client-key.pem -pubout -out client-pub.pem
```

Register the **public** key (never send the private key anywhere):

```bash
jq -n --arg pem "$(cat client-pub.pem)" '{
    client_name: "Acme EHR test", auth_method: "private_key_jwt",
    alg: "ES384", public_key_pem: $pem,
    organization: "Acme Health IT", contact_name: "Pat Example",
    contact_email: "pat@example.org", system_under_test: "Acme EHR 24.1",
    participant_type: "ehr_vendor"
  }' | curl -s https://pa-test.shn-preview.org/register \
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
  -d '{"client_name": "Acme EHR test", "auth_method": "client_secret",
       "organization": "Acme Health IT", "contact_name": "Pat Example",
       "contact_email": "pat@example.org", "system_under_test": "Acme EHR 24.1",
       "participant_type": "ehr_vendor"}'
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
differ only in the payer identifier and the ordered code. Each is a complete, runnable
sequence: CRD, then DTR, then PAS. The PAS request bundle is in
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
    },
    "serviceHistory": {"resourceType": "Bundle", "type": "searchset", "total": 0},
    "deviceHistory": {"resourceType": "Bundle", "type": "searchset", "total": 0},
    "medicationHistory": {"resourceType": "Bundle", "type": "searchset", "total": 0},
    "questionnaireResponses": {"resourceType": "Bundle", "type": "searchset", "total": 0}
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
      "name": "referenced",
      "resource": {"resourceType": "Patient", "id": "MBR-COVERED", "name": [{"family": "Johansson", "given": ["Linda"]}], "birthDate": "1975-04-02"}
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
    },
    "serviceHistory": {"resourceType": "Bundle", "type": "searchset", "total": 0},
    "deviceHistory": {"resourceType": "Bundle", "type": "searchset", "total": 0},
    "medicationHistory": {"resourceType": "Bundle", "type": "searchset", "total": 0},
    "questionnaireResponses": {"resourceType": "Bundle", "type": "searchset", "total": 0}
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
      "name": "referenced",
      "resource": {"resourceType": "Patient", "id": "MBR-COVERED", "name": [{"family": "Johansson", "given": ["Linda"]}], "birthDate": "1975-04-02"}
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

These are the published test members, and the only ones the endpoint holds records for. In a
PAS request both payers match the member on `Patient.identifier`
(`http://example.org/MIN|12345678901` for `MBR-COVERED`, §5.3), not on the Patient id. A
member id the endpoint does not hold — a patient from your own test environment, for example
— is still carried, bound by the id you send (§8.1), but you must then supply `coverage`
yourself (and `patient`, for the payer to receive one), and what the payer answers for a
member it does not hold is the payer's own answer. On route `00300` the DTR limitation in
§6.2 applies even to the published member.

---

## 8. Fail-closed behavior and limits

Nothing here is silently dropped, translated or invented. Every refusal is explicit.

### 8.1 Routing and membership

- **Unknown payer identifier → `422`.** If `Coverage.payor[0].identifier.value` names no
  payer registered on the network, the request is rejected with `422 Unprocessable Entity`
  (`no registered payer for identifier …`) rather than silently going nowhere. The routes
  in §1.5 are the ones documented here with test members.
- **No payer identifier → `422`.** From shn-gateway v0.60.0, the endpoint routes a PAS
  Bundle by its first Coverage's `payor`: an identifier on the payor itself, a contained
  Organization, or an Organization that is an entry of the Bundle. An absolute reference
  (a URL, or a `urn:uuid`) names the entry whose `fullUrl` equals it, and a relative
  `Organization/<id>` the entry with that type and id, whatever its `fullUrl`; a
  reference several entries answer is refused unless they all name the same payer
  identifier. The endpoint never looks the Organization up on your server. When none
  of that yields an identifier with both a system and a value (a NAIC code or payer id),
  the request is refused with `422` (`no payer identifier on member coverage: …`), and the
  text after the colon says which part failed: a reference that matches no entry, or
  several that do not name one payer; a reference to something that is not an
  Organization; or an Organization with no such identifier.
- **Unknown member → carried with the id you send.** The endpoint first resolves the member
  against its own synthetic roster. A CRD request whose `context.patientId`, a PAS bundle
  whose `Claim.patient`, or a DTR request whose Coverage beneficiary (or, if the Coverage
  names none, the order's subject) names a member it does not hold is carried with the
  member id and the Patient your request carries for it, so you can drive the hook from a
  patient in your own test environment. Send the same Patient, unchanged, on every leg of
  one exchange. Three consequences: the endpoint holds no records for such a member, so
  leave out `coverage` prefetch and the request is refused (next bullet) unless, from
  shn-gateway v0.60.0, the endpoint can read it through your `fhirServer`, while any
  other key you leave out is left out of what the payer receives; the payer handles the
  member as it would directly; and the
  payer resolves the member on its own, so its answer for a member it does not hold is the
  payer's own — the DTR prepopulation warning in §5.2 is the visible case. A request whose
  payload names a second patient is refused (`403 Forbidden`) only when the gateway runs
  `strict`; this endpoint runs below strict, so such a request is carried under the patient
  it is bound to.
- **A coverage the endpoint cannot find → `412` (CDS Hooks) or `422` (DTR).** If you leave
  out `coverage` prefetch and the endpoint's own records hold no matching coverage, there is
  no payer to send the request to, so it is refused rather than routed on a blank or invented
  value. From shn-gateway v0.60.0 a CDS Hooks request is answered CDS Hooks' `412` (the
  service could not obtain the data it needs; `422` before): `412 no coverage in request or
  system of record` for a member the endpoint holds, or a `null` coverage prefetch, and, for a
  member it does not hold, `412 no coverage to route by: send prefetch.coverage or fhirServer
  (this gateway's system of record names no patient for this member)` when your request names
  no `fhirServer`. On `$questionnaire-package` the answer stays `422 no coverage to route by:
  send the coverage parameter …`. The endpoint adds nothing to your request, so any other key
  you leave out is simply left out.
- **Coverage read through your `fhirServer`.** From shn-gateway v0.60.0, a CDS Hooks request
  for a member the endpoint does not hold that carries no `coverage` prefetch but names
  `fhirServer` has its coverage searched there, to choose the payer (and, from shn-gateway
  v0.61.0, to carry it to the payer; below):
  `GET {fhirServer}/Coverage?patient={context.patientId}` (one page, no status filter, no
  `_include`), with `Authorization: Bearer <access_token>` when you send `fhirAuthorization`
  (its `token_type` must be `Bearer`). It routes on the Coverages that are `active` when any
  is, and otherwise on the others when they name one payer. When a Coverage names its payor only as
  `Organization/<id>` on your server and the answer does not resolve it, the endpoint reads
  `GET {fhirServer}/Organization/<id>` once more with the same token: at most two reads, within
  4 seconds in all. Before shn-gateway v0.61.0 nothing either read returned was sent to the
  payer, which received your request with no Coverage and answered whatever its own system
  answers for one (§1.3). From shn-gateway v0.61.0 the endpoint adds what it routed by as
  `prefetch.coverage`: a `searchset` it writes, with each Coverage it chose and the payor
  Organization it chose the payer by (returned by your search, or read), exactly as your server
  returned them (so any reference they hold, an absolute one on your server included, is
  carried as written), under `urn:uuid:` entry addresses, and none of your server's links, entry
  addresses or messages (from shn-gateway v0.61.0 too, a payor Organization your Coverage
  names by an absolute reference on your `fhirServer` base has that reference, already in your
  Coverage, as its `fullUrl`, so the payer resolves it); your request is otherwise carried as
  you sent it. `fhirServer` and
  `fhirAuthorization` are still removed. The endpoint is a gateway SHN runs, so it reads in the
  gateway's `public` mode; for the reads to route your request, your FHIR server must be
  `https` on port 443 at a public address, answer without redirecting and in at most 512 KiB
  an answer, allow a Coverage search and an Organization read with the token, and return the
  patient's Coverage whose payer is registered on the network: a `payor.identifier`,
  or an Organization (in the answer, or read by its reference) carrying the payer's
  identifier. Sending `coverage` in `prefetch`, with a `payor.identifier` or with the payor
  Organization, is still preferred, and always works. (A Smart
  Gateway you run yourself makes the same read by default, of a server in your own network or
  on the internet, and `CDS_FHIR_SERVER_READ=off` turns it off.) A read that cannot route is
  refused `412 no coverage to route by: <reason>`; the ones you are most likely to see are
  `fhirServer must use port 443` and `fhirServer's address is not public` (a server not
  reachable on the public internet on 443), `fhirServer refused the fhirAuthorization token`,
  `fhirServer holds no Coverage for the patient`, `fhirServer holds no Organization for
  the coverage's payor`, `fhirServer did not answer in time` and `fhirServer's TLS could not be
  verified`. A Coverage about another patient (`fhirServer returned another patient's
  coverage`) or an Organization other than the one asked for (`fhirServer's answer is not the
  payor Organization`) is `502`. A payor reference to another server, or an Organization with
  no payer identifier, is refused `422 no payer identifier on member coverage`. The read of
  the payor of a `coverage` you sent (§1.3) gives the same reasons and statuses after `no
  payer identifier on member coverage: ` instead of `no coverage to route by: `. The gateway's
  CONFIGURATION.md ("Reading the coverage through `fhirServer`") lists every refusal.

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

A `502` whose body is `{"error":"hub routing failed"}` means the endpoint's gateway
could not reach the Hub at all. When the Hub refuses the exchange, the endpoint answers
with the Hub's reason — for example `502 {"error":"hub refused the exchange: unknown
recipient"}` (a Hub refusal is about the endpoint's gateway, not your request) — and
an authorization or consent denial is a `403 {"error":"authorization denied"}`. From
shn-gateway v0.60.0, when the network's authorization service does not answer the
endpoint's gateway, the answer is a `503 {"error":"the authorization service could not be
reached, so this leg was not sent to the payer"}` (an `OperationOutcome` with issue code
`transient` on the FHIR operation routes): nothing reached the payer, and resending is
safe. When the
payer's gateway was reached but its answer was lost on the way back, the `502` says so —
`the recipient received this request and answered, but its answer was lost …` or `the
recipient may have received this request …` — because the payer may have acted on it:
check the outcome (for PAS, `$inquire`) before resending.

A `504` is an exchange failure the endpoint names rather than leaving generic. When the
leg to the payer produced no answer within the endpoint's gateway's wait (30 seconds; the
Hub, the payer's gateway and the payer's own system share that budget), the body is
`{"error":"no answer on the hub leg within 30s (hub leg timeout)"}` — the number is the
gateway's own leg deadline — carried as an `OperationOutcome` with issue code `timeout` on
the FHIR operation routes. From shn-gateway v0.59.0, the `504` may instead carry the
payer's gateway's own body, as JSON on every route. When the payer's own system did not
answer within its gateway's deadline for it (25 seconds unless the payer set another), the
body is `{"error":"the payer's system received this request but did not answer in time; it
may have acted on it: check its outcome before resending"}`, or `{"error":"the payer's
system could not be reached in time"}` when the request was not sent. When the payer's
gateway spent its deadline on its own work before it could ask its system, the body is
`{"error":"the payer's gateway ran out of time before it could send this request to the
payer's system; the payer's system did not receive it"}`. The endpoint relays its
gateway's `504` and body as they are.

A `502` is a failed exchange, not a payer verdict. Its message says whether the payer
may have received the request: when it says so, check the outcome (for PAS, `$inquire`)
before resending; when it does not (`hub routing failed`, `the payer's system could not be
reached`), nothing reached the payer, and a retry may succeed once the far side is
reachable again. A `504` is a failure with its cause named, and says so too when the
request had already been sent; from shn-gateway v0.59.0, one saying the payer's system did
not receive it, or could not be reached in time, means nothing reached the payer, and a
retry is safe. You may retry under the same `X-Correlation-Id`: it is your trace value, and
reusing it is never refused (§8.5).

**A refusal the payer's gateway itself produces is not a `502`.** A member the payer does
not hold, a request with no order to decide on, a validation failure: these come back with
the payer's own status and error text — for example
`400 {"error":"no order (ServiceRequest or DeviceRequest) in draftOrders"}` for an
`order-sign` request whose `draftOrders` is empty. So do the payer gateway's own
failures: `502 {"error":"the payer's system could not be reached"}` when the payer's
own system never got the request, a `502` saying the payer's system may have acted on it
when it got the request and gave no usable answer, from shn-gateway v0.59.0 a `504` when
the payer's system did not answer within its gateway's deadline for it (saying the same
when it got the request), `502` or `503` when it could not read the payer's records. On a
PAS submission, any refusal the payer gateway makes after the payer's system answered says
so too: check the outcome with `$inquire` before resending. `502 hub routing failed` means only that the endpoint's gateway
could not reach the Hub.

### 8.4 Rate and size limits

| Limit | Value | On exceeding |
|---|---|---|
| Registration, per source IP | 50 per hour | `429` |
| Requests per client | 60 per minute | `429` |
| Concurrent requests in flight, across every caller of the endpoint | 8 | `503` |
| Request body | 5 MiB | `400` |
| Calls from one network address straight to a hosted payer gateway or a reference payer, not through this endpoint | 2,000 per 5 minutes (about 6.7 a second) | `403` |

A body over the cap is refused before any of it is forwarded, with
`{"error":"request body exceeds 5 MiB"}`.

The per-IP registration limit allows a room of testers sharing one network address. SHN
may raise these limits for an event; the values above are the standing ones.

If you are scripting repeated registration/token/CRD/DTR/PAS runs in a loop, register once
and reuse the client — a normal integration test needs a handful of registrations at most.
A harness that needs more than 60 requests a minute registers a few more clients and spreads
its calls across them: the request limit is per client, not per participant or per network
address, and the per-IP registration limit leaves room for that. More clients raise only the
per-minute ceiling: the 8 requests in flight are shared by every caller of the endpoint, and
registered clients count against the endpoint's total of 500, which they keep, so register
the few a harness needs and reuse them.

Calls you make through this endpoint never count against the last limit in the table: the
network's own traffic to the payers is exempt from it. It applies only to calls one network
address sends straight to a hosted payer gateway or a reference payer, and lifts by itself
once that address's rate falls back under it. Blocked calls still count toward that rate, so
a harness that retries through the block keeps itself blocked: back off instead. Its `403` is
the load balancer's plain response, not one of this endpoint's JSON errors.

### 8.5 When something fails, quote `X-Correlation-Id`

Every answer to a CRD, DTR or PAS call carries an `X-Correlation-Id` header: the id the
exchange is recorded under on our side. If a call does not do what you expect, send us
that value and the time of the call, and we can find the call and the legs it ran. It is
on refusals as much as on successes, including the endpoint's own `4xx` and `5xx` answers.
Registration, token and SMART discovery (`/.well-known/smart-configuration`) answers carry
none, and neither does the redirect a path with a doubled or dotted segment gets
(`//Claim/$submit`) or an error the load balancer answers when the endpoint itself is
unavailable: for those, send us the time and your client id. Whichever you send, send it
to your SHN contact.

You can also send your own. An `X-Correlation-Id` request header of up to 64 characters
(letters, digits, `.`, `_` and `-`) is your call's trace value: it comes back on the answer,
and our log records it beside the exchange, so the id your integration test already tracks
is the one we find. A header outside that shape, or sent more than once, is ignored and
an id is assigned instead.
Reusing a trace value, or retrying under it, is never refused: each call's exchange runs
under its own id, which the answer to every exchange that ran also carries as
`X-SHN-Leg-Id`.

A PAS submission is different in one way. If its Claim names its own correlation
(`Claim.identifier` with system `urn:shn:correlation`), that value is the payer's key for
the authorization, and the answer's `X-Correlation-Id` reports it. So is an
`X-Correlation-Id` you send that equals one of the Claim's own identifiers: an amendment
that names that identifier in `Claim.related` finds the authorization. A resend of the same
submission lands on the same authorization. A different patient's submission under a
Claim correlation another patient's authorization already holds is refused with `409`
before the payer is asked, so give each authorization its own Claim correlation.

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

# Provider Test Endpoint — a hosted Da Vinci prior-authorization test endpoint

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

- **Test environment only.** Every payer reachable from this endpoint is a test environment.
  Nothing here ever reaches a production payer.
- **Synthetic data only.** Send only synthetic test data — no real patient information,
  ever. The endpoint holds records for the published test members below and for other
  synthetic members of its own; a member it does not hold is carried under the id you send
  (§8), so any patient you use must be synthetic too. Use credentials made only for this endpoint.
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

**Not offered on this endpoint:**

- PAS subscriptions and notifications: a payer's later decision is not pushed to you; ask for
  it with `Claim/$inquire` (§1.6);
- DTR `Questionnaire/$next-question` (the next questions of an adaptive questionnaire) and
  `Questionnaire/$populate`;
- CDex `$submit-attachment`;
- the CDS Hooks `order-dispatch`, `appointment-book`, `encounter-start` and
  `encounter-discharge` hooks.

**Two aliases, so you can test as you are configured today.** A `POST` to a path that
is not in the table but ends in one of its operations — `/$submit` without `Claim/`, or
`/fhir/Claim/$submit` — is served as that operation (`POST /Claim/$submit`). A
`POST /cds-services/{id}` with an id this endpoint does not advertise, whose body `hook`
is one an advertised service carries — `order-sign-crd` with `"hook":"order-sign"` — is
served by that service (`shn-order-sign`). An aliased answer is the payer's answer
unchanged, plus two headers naming the canonical form:
`Link: </Claim/$submit>; rel="canonical"` (or `</cds-services/shn-order-sign>`) and a
`Warning: 299` that says the same in words. The canonical forms are the table above and
the ids in §1.2; use them in the configuration you take to production, since the aliases
are served only here. Any other path, and any id whose hook no
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
- A request with `"hook":"order-dispatch"`, to `/cds-services/shn-order-dispatch` or any id
  not advertised, answers `404` naming the service ids that are offered. Build against
  `order-sign` (§5, §6) or `order-select`.

### 1.3 Prefetch

Both services advertise the same six prefetch keys, the Da Vinci reference payer's
order-sign templates: the patient named by its bare id, a status filter on the coverage and
each history, and no `_include`:

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

What the endpoint does with your request:

- It **removes `fhirServer` and `fhirAuthorization`** — the payer never gets a route into
  your systems. The endpoint may first use them to read your Coverage (and at most one payor
  Organization) to choose the payer (next bullets and §8.1); it never calls back into your
  systems otherwise. The Coverage it read that way, with its payor Organization, is carried to
  the payer as `prefetch.coverage`, since the payer cannot read it once `fhirServer` is
  removed.
- It **adds nothing of its own**. A prefetch key you leave out is left out of what the payer
  receives; the one exception is the coverage it read through your `fhirServer` (§8.1),
  carried exactly as your server returned it.
- A `coverage` you send that names its payor only as
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
  Bundle or another prefetch value, routes it), a `urn:uuid:` or `urn:oid:` one that no Bundle
  entry's `fullUrl` matches `422 no payer identifier on member coverage: Coverage.payor is a urn
  reference no Bundle entry's fullUrl matches; send the payor Organization as a Bundle entry
  whose fullUrl is that reference, or a payor identifier`, and a
  versioned one, or one with a fragment, a leading slash or a dot segment, `422 no payer
  identifier on member coverage: Coverage.payor is a reference to an Organization the gateway
  does not read; send a payor identifier with the coverage`. The read's requirements are §8.1's, and so are
  its refusals, with the reason after `no payer identifier on member coverage: ` instead of
  `no coverage to route by: ` (for example `412 no payer identifier on member coverage:
  fhirServer refused the fhirAuthorization token`).
- If you leave out `coverage`, the endpoint looks the member's coverage up in its own
  synthetic records only to choose the payer; that coverage is not sent to the payer. The
  payer is chosen from the member's active coverage when it has one,
  so a cancelled coverage naming another payer is ignored; a member with no active coverage
  is still sent to the payer its coverage names (when its coverages name one payer), which
  answers that it does not cover the member. For a member those records do not hold, it reads
  the coverage instead through the `fhirServer` your request names (your own FHIR server),
  with your `fhirAuthorization` token, to choose the payer, and chooses the same way (§8.1);
  that coverage is carried as `prefetch.coverage` (§8.1). A request it finds no coverage to route by for is
  refused `412` (§8.1). For `MBR-COVERED`, the endpoint's own records name the payer behind
  route `00001`, which is not the route to configure (§1.5): to reach `00301` or `00300`,
  send the Coverage.
- A request routed by the endpoint's own synthetic records reaches the payer **with no
  Coverage**, and gets whatever the payer's own system answers for it. A payer's system may
  not decide on a request without the member's Coverage, or may refuse it.
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
- HTTP-level signatures are not carried. A CRD response carries the media type the payer's
  system stated, as sent: route `00301` answers
  `text/json;charset=UTF-8`. When the payer states none, or states
  `application/fhir+json`, it is served as `application/json`. So read a CRD response
  as JSON whatever its media type says. DTR and PAS responses are served as
  `application/fhir+json`.
- **A PAS review action's code and display can disagree.** The X12 code set
  (`https://codesystem.x12.org/005010/306`) defines `A1` "Certified in total", `A2` "Certified –
  partial", `A3` "Not Certified" and `A4` "Pended". The route `00301` reference payer answers an
  order its CRD answer called not-covered with `A2` "Not Certified", and a covered order its CRD
  answer did not mark `auth-needed` (no `pa-needed`, or `pa-needed` `conditional`) with `A3` "Not
  Required". The displays say what the payer means, a denial and no authorization required;
  read as X12 defines them, the codes say partial certification (`A2`) and not certified (`A3`).
  The answers are carried as sent, so read the code and the display together, not the code
  alone.

### 1.5 The reference payers and their routes

| Route | `Coverage.payor` identifier | Da Vinci line | Test member | Example order |
|---|---|---|---|---|
| `00301` | `urn:oid:2.16.840.1.113883.6.300\|00301` | **2.2** | `MBR-COVERED` | HCPCS `G0151` |
| `00300` | `urn:oid:2.16.840.1.113883.6.300\|00300` | **2.0** | `MBR-COVERED` | HCPCS `L8000`; `E1390` to pend |

Start with **route `00301`** (§5). It is the Da Vinci reference payer on the 2.2 line, and it is the route that demonstrates the whole loop: a CRD coverage answer, a
questionnaire package, a PAS submission that **pends with a `CommunicationRequest`**, and a
DTR relaunch keyed off that pend. Route `00300` (§6) is the 2.0-line route: the same
reference payer software, on the 2.0 line. Its PAS submission for the worked example is approved outright; an
oxygen-concentrator order (§6.4) **pends with a `Task`**, the 2.0-line pended shape.

**Test data on these two routes is cleared on a schedule.** The shared reference payers
behind routes `00300` and `00301` start again from their published test data weekly, on
Sundays from 07:00 UTC, nightly from 07:00 UTC (03:00 US Eastern) from 2026-10-07 through
2026-10-14, and occasionally at other times: a submission made before a clearing is not found
by an inquiry (§1.6) after it, so submit again before you inquire.

**Route `00001`.** `urn:oid:2.16.840.1.113883.6.300|00001` also reaches another instance
of the same 2.0-line reference payer. It answers every call in §6 identically, and writes
`http://localhost:8081/fhir/…` addresses into the `fullUrl`s of its PAS answers. Configure
`00300` instead.

The endpoint declares both the 2.0 and the 2.2 lines for CRD, DTR and PAS, and pairs with
each payer on a line they share. A request that cannot be carried to the payer's line
unchanged is refused rather than silently rewritten (§8). A payer that shares no line with
the endpoint, with no bridge to its line, is refused `422 no shared contract line for
<transaction> (leg <leg>): this gateway speaks <lines>; recipient "<payer>" declares <lines> —
no bridge available`, sometimes followed by a reason in parentheses. Send your SHN contact the
`X-Correlation-Id` (§8.5). When it reads `declares (contract not declared)`, the payer takes
the transaction on no line, and it is the payer's to fix.

### 1.6 Inquiry

`POST /Claim/$inquire` is answered on both routes. Send a PAS inquiry request Bundle
(`profile-pas-inquiry-request-bundle`) whose Coverage names the route's payer identifier
as in §1.5, whose Patient carries the member identifier you submitted with (typed `MB`),
and whose Claim names the requesting provider you submitted with: both reference payers
match an inquiry on the member identifier and the provider's NPI (route `00301` also matches
the order), and a query for a submission they cannot match is answered with an empty result,
not an error.

Name the patient in the inquiry's Claim as a `Patient/<id>` reference, relative or absolute.
A `urn:uuid:` or contained `#id` reference, which `$submit` accepts, is refused here with
`400 PAS inquiry Bundle missing Claim.patient`, and a Claim that names the patient only by an
identifier with `400 PAS inquiry Claim missing patient`.

On route `00301` an inquiry takes longer the more claims the payer holds, since its last
clearing, for the same member and requesting provider. Submissions that name your own
provider organization, with its own NPI, as the requester keep your inquiries independent
of everyone else's claims; submissions that send the example provider of Appendix A share
it with every participant who does the same.

What comes back is the payer's own answer, relayed as sent. Both routes answer `HTTP 200`
and a `Parameters` resource whose `responseBundle` parameter carries the PAS inquiry response
Bundle — a `ClaimResponse` with the decision the payer holds (on route `00301`, a pended
decision also carries the `CommunicationRequest` from §5.3). That is the PAS 2.2 inquiry
answer. The 2.0-line reference payer answers in the same shape, while the PAS 2.0.1 operation
returns the response Bundle itself, so a client on the 2.0 line reads the Bundle out of the
`Parameters`.

An inquiry can return more than the submission you are asking about: the payer returns every
submission it holds that matches, since its last clearing (§1.5), for the same member and
requesting provider (and, on route `00301`, the same order), another participant's included.
A pend that asked for documentation stays pended: its later answer is not pushed, and an
inquiry keeps returning the pend. CDex `$submit-attachment`, which would carry documentation
for it, is not offered here (§1.1). A new submission that carries the documentation is decided
on its own and leaves the pend as it was (§10.4).

---

## 2. Register a client

Every registration names who is testing, so we can tell whose calls we are looking at, help
when something fails, and report how the testing went. These fields are
required:

| Field | What to send |
|---|---|
| `client_name` | A name for this client, e.g. `"Acme EHR test"` |
| `organization` | Your organization |
| `contact_name` | The person we should contact about this client |
| `contact_email` | That person's email address, as a plain address (`name@example.org`) |
| `system_under_test` | The product or system you are testing, e.g. `"Acme EHR 24.1"` |
| `participant_type` | One of `ehr_vendor`, `provider_organization`, `intermediary`, `other` |

Each is plain text of at most 256 characters (320 for the email), with surrounding whitespace
removed and no control characters such as line breaks. A registration missing any of them
is refused with `400`, and the answer names every missing field, for example
`missing required registration fields: contact_email, system_under_test`. We can correct the
details you registered, and can group several of your clients under one organization; you
don't need to register again to fix a typo.

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
- **The assertion's `exp` must be at most 5 minutes from now**, and `aud` must be the token
  endpoint URL exactly.

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

A request with no usable credential, such as a `client_secret` request whose secret or client
id is empty or missing (an unset `$CLIENT_SECRET` or `$CLIENT_ID`, or `-u "$CLIENT_ID:"`), is
answered
`400` `{"error":"invalid_request","error_description":"client_id and client_secret, or client_assertion, required"}`.
A wrong secret is answered
`401` `{"error":"invalid_client","error_description":"client authentication failed"}`.

Tokens are short-lived (5 minutes) — fetch a fresh one per test run rather than caching
across sessions. The endpoint is redeployed routinely; a token stays valid for its five
minutes across a redeploy, and so does your registration. A plain-text `401 valid bearer
required …` means the request carried no `Authorization: Bearer` header, or a token that has
expired or was not issued here: send the header with a new token and retry. A JSON
`401 {"error":"ingress authentication required"}` is on our side, not your token: retry, and
if it repeats, send your SHN contact the `X-Correlation-Id`.

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

The Da Vinci reference payer on the 2.2 line, declaring CRD, DTR and PAS at 2.2. Test
member **`MBR-COVERED`**, HCPCS **`G0151`** (physical therapy in the home health setting),
payer identifier `urn:oid:2.16.840.1.113883.6.300|00301`, hook `order-sign`.

The payer's answers are its own, relayed as received.

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

The package does not carry the `FHIRHelpers` Library its libraries depend on. You receive
it as the payer sent it.

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
member the payer has never seen: it creates a Patient of its own for it and links the claim
to a Coverage whose beneficiary is still its stored member. Its answer is `HTTP 200`, and
that one answer names two members. It is carried as the payer sent it, so do not take the
`200` for a decision about your patient; carry the appendix's member identifier beside
your own. A payer may choose to have such an answer refused instead: you then see
its `403`, whose text begins `PAS response has inconsistent patient linkage:` and names the
entry and the patient it is about. A request
without `Coverage.beneficiary`, or whose references point at an external base, also answers
without an error and is still not the exchange you meant: `200` with `A3` "Not Required"
(code and display disagree, §1.4) and no `CommunicationRequest` instead of the pend. A
`Claim.patient` given as an identifier only (`type` and `identifier`, no `reference`) is
refused before it reaches the payer: a `400` `OperationOutcome` (issue code `invalid`)
whose diagnostics read `PAS bundle missing Claim.patient`.

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

The pend's later resolution is not pushed to you; ask for its current state with
`Claim/$inquire` (§1.6). A pend that asks for documentation stays pended: CDex
`$submit-attachment`, which would carry the documentation, is not offered here (§1.1).

---

## 6. Worked example — route `00300` (2.0 line)

The 2.0-line reference payer, the same Da Vinci reference payer software as §5 at its 2.0
line. Test member **`MBR-COVERED`**, HCPCS **`L8000`**
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

**The `doc-needed` code on this route.** This payer answers `no-doc`
while also naming a `questionnaire` canonical. `no-doc` is not in the CRD 2.0 value set for
`doc-needed` (`clinical`, `admin`, `both`, `conditional`), and it contradicts the
questionnaire it supplies. It is the payer's own answer and it is relayed as sent rather
than corrected. If your client keys its DTR follow-up off `doc-needed`, this route will tell
it to skip DTR; use route `00301` (§5) to exercise the CRD-to-DTR handoff.

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

**What this route's package carries.** The payer looks the member up in its own records by the
Coverage `subscriberId` or `beneficiary.identifier`; the Coverage in the example above
carries neither, and the payer's records do not hold `MBR-COVERED` for this operation even
when they are supplied. The package therefore comes back with a `QuestionnaireResponse` whose
id names `__unresolved-member` rather than the test member, and the `outcome` parameter
carries the same demographic-prepopulation warning quoted in §5.2. Separately, the static
`PriorAuthRequired` form this payer publishes carries **no prepopulation logic** (no Library,
no prepopulation extensions or expressions), so no member-specific answers are prepopulated
on this route regardless of member resolution. You still receive the real `Questionnaire`.
The example-URL `questionnaire`
canonical above is the payer's actual published value — preserve it, as an invented
canonical would request a different questionnaire.

### 6.3 PAS — an approval

Submit the bundle from [Appendix A](#appendix-a--the-pas-request-bundle), with the three
substitutions the appendix names for this route. The member-identity rule and the
other shapes in §5.3 hold here unchanged: the payer matches `Patient.identifier`
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
payer's own public host (`https://….shn-preview.org/fhir/…`),
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

These are the published test members. The endpoint holds records for other synthetic members
of its own too, so use your own patient ids and send `coverage`. In a
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
- **No payer identifier → `422`.** The endpoint routes a PAS Bundle by its first
  Coverage's `payor`: an identifier on the payor itself, a contained
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
  leave out `coverage` prefetch and the request is refused (next bullet) unless the endpoint
  can read it through your `fhirServer`, while any other key you leave out is left out of
  what the payer receives; the payer handles the member as it would directly; and the payer
  resolves the member on its own, so its answer for a member it does not hold is the payer's
  own — the DTR prepopulation warning in §5.2 is the visible case. A request whose payload
  names a second patient is carried under the patient it is bound to.
- **A coverage the endpoint cannot find → `412` (CDS Hooks) or `422` (DTR).** If you leave
  out `coverage` prefetch and the endpoint's own records hold no matching coverage, there is
  no payer to send the request to, so it is refused rather than routed on a blank or invented
  value. A CDS Hooks request is answered CDS Hooks' `412` (the service could not obtain the
  data it needs): `412 no coverage in request or system of record` for a member the endpoint holds, or a `null` coverage prefetch, and, for a
  member it does not hold, `412 no coverage to route by: send prefetch.coverage or fhirServer
  (this gateway's system of record names no patient for this member)` when your request names
  no `fhirServer`. On `$questionnaire-package` the answer stays `422 no coverage to route by:
  send the coverage parameter …`. The endpoint adds nothing to your request, so any other key
  you leave out is simply left out.
- **Coverage read through your `fhirServer`.** A CDS Hooks request for a member the
  endpoint does not hold that carries no `coverage` prefetch but names `fhirServer` has its
  coverage searched there, to choose the payer and to carry it to the payer (below):
  `GET {fhirServer}/Coverage?patient={context.patientId}` (one page, no status filter, no
  `_include`), with `Authorization: Bearer <access_token>` when you send `fhirAuthorization`
  (its `token_type` must be `Bearer`, in any letter case). It routes on the Coverages that are `active` when any
  is, and otherwise on the others when they name one payer. When a Coverage names its payor only as
  `Organization/<id>` on your server and the answer does not resolve it, the endpoint reads
  `GET {fhirServer}/Organization/<id>` once more with the same token: at most two reads, within
  4 seconds in all. The endpoint adds what it routed by as `prefetch.coverage`: a `searchset` it writes, with each Coverage it chose and the payor
  Organization it chose the payer by (returned by your search, or read), exactly as your server
  returned them (so any reference they hold, an absolute one on your server included, is
  carried as written), under `urn:uuid:` entry addresses, and none of your server's links, entry
  addresses or messages (a payor Organization your Coverage names by an absolute reference
  on your `fhirServer` base has that reference, already in your Coverage, as its `fullUrl`, so
  the payer resolves it); your request is otherwise carried as you sent it. `fhirServer` and
  `fhirAuthorization` are still removed. For the reads to route your request, your FHIR
  server must be
  `https` on port 443 at a public address, answer without redirecting and in at most 512 KiB
  an answer, allow a Coverage search and an Organization read with the token, and return the
  patient's Coverage whose payer is registered on the network: a `payor.identifier`,
  or an Organization (in the answer, or read by its reference) carrying the payer's
  identifier. Sending `coverage` in `prefetch`, with a `payor.identifier` or with the payor
  Organization, is still preferred, and always works. A read that cannot route is
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
  payer identifier on member coverage: ` instead of `no coverage to route by: `. The reasons above
  are the ones you are most likely to see, not every one.

### 8.2 Hooks and services

- **Unknown service id → served by its hook, else `404`.** An id this endpoint does not
  advertise is served by the advertised service for the `hook` in your body, with the
  canonical id named in the answer's `Link` and `Warning` headers (§1.1). When no
  advertised service carries that hook, the `404` lists the advertised ids with the hook
  each carries. If you see it, change the service id in your CDS Hooks client
  configuration to one of them; the hook stays as it is.
- **An ordering hook sent to the other ordering service → carried.** Sending
  `"hook": "order-sign"` to `shn-order-select` (or `order-select` to `shn-order-sign`) is
  carried, and the payer answers it with its service for the hook in your body: on route
  `00301` the same coverage decision as at `shn-order-sign`, with no `Link` or `Warning`
  header. Post to the service for your hook all the same. A hook outside that pair is
  refused, for example `400 hook order-dispatch is not carried on crd-order-select`.
- **The payer offers no service for your hook → `422`**, with the hooks it does offer. A hook
  no payer here carries at all, `order-dispatch`, has no advertised service: posted to a
  service id that is not advertised, it is refused earlier, with `404` and the service ids
  that are offered (§1.2).

### 8.3 `502` — refused, never altered

A `502` from this endpoint means a message could not be carried faithfully, so it was not
carried at all. These cases are always refused:

- the request cannot be carried to the payer's IG line without changing it;
- the payer does not accept the framed DTR operation;
- the payer's answer repeats a JSON member name, so it could be read two ways:
  `{"error":"payer CRD response is not a valid CDS Hooks response: response.json"}` on CRD,
  and on PAS `{"error":"engine: invalid native PAS response Bundle: invalid or incomplete PAS
  response graph; the payer's system received and answered this request: check its outcome
  before resending"}` (the payer's system has answered and may have acted, so check the
  outcome with `$inquire`), and on `$inquire` `{"error":"engine: invalid prior-authorization
  inquiry answer"}`.

The cases below are carried as the payer sent them, unless the payer has chosen to have them
refused; you then see a `502`:

- the payer's CRD answer cannot be read, lacks a member CDS Hooks requires, or breaks another
  CDS Hooks or CRD card rule (for example a summary that is too long);
- the payer's PAS decision cannot be stated as sent — for example a decision that denies
  while also naming an authorization number, or decision detail outside the code systems the
  decision record binds;
- the payer's PAS response Bundle names a resource it does not carry. The body says which:
  `{"error":"engine: invalid native PAS response Bundle: PAS response graph: entry 6
  (ServiceRequest/1810) /subject references Patient \"https://…/Patient/MBR-COVERED\",
  which is no entry of the Bundle; the payer's system received and answered this request:
  check its outcome before resending"}` — the entry that made the reference, the element,
  the reference as the payer wrote it, and why it does not resolve. The payer's system has
  answered and may have acted, so check the outcome with `$inquire` before resending. The
  answer is refused whole, never completed or trimmed on the way through; the payer's own
  record is the one to correct. A resource the payer places under a URN fullUrl
  (`urn:uuid` or `urn:oid`) needs no `id`, and a `Type/id` reference inside it (which FHIR
  gives no base to resolve against) is read as the one entry whose address ends in that
  `Type/id`; two such entries, or none, is a refusal that says so.

A `502` whose body is `{"error":"hub routing failed"}` means the endpoint could not reach the
network's routing service at all. When the network refuses the exchange, the endpoint answers with the
reason — for example `502 {"error":"hub refused the exchange: unknown recipient"}`, which is
about the endpoint, not your request — and an authorization or consent denial is a
`403 {"error":"authorization denied"}`. When the network's authorization service does not
answer, the answer is a `503 {"error":"the authorization service could not be reached, so
this leg was not sent to the payer"}` (an `OperationOutcome` with issue code
`transient` on the FHIR operation routes): nothing reached the payer, and resending is
safe. When the
payer's gateway was reached but its answer was lost on the way back, the `502` says so —
`the recipient received this request and answered, but its answer was lost …` or `the
recipient may have received this request …` — because the payer may have acted on it:
check the outcome (for PAS, `$inquire`) before resending.

A `504` is an exchange failure the endpoint names rather than leaving generic. When the
leg to the payer produced no answer within the 30 seconds the exchange allows it (shared by
everything on the payer's side, the payer's own system included), the body is
`{"error":"no answer on the hub leg within 30s (hub leg timeout)"}`, carried as an
`OperationOutcome` with issue code `timeout` on the FHIR operation routes. The `504` may
instead carry the payer side's own body, as JSON on every route. When the payer's own system did not
answer within its gateway's deadline for it (25 seconds unless the payer set another), the
body is `{"error":"the payer's system received this request but did not answer in time; it
may have acted on it: check its outcome before resending"}`, or `{"error":"the payer's
system could not be reached in time"}` when the request was not sent. When the payer's
gateway spent its deadline on its own work before it could ask its system, the body is
`{"error":"the payer's gateway ran out of time before it could send this request to the
payer's system; the payer's system did not receive it"}`. These `504`s and bodies are relayed
as they are.

A `502` is a failed exchange, not a payer verdict, and a `504` a failure with its cause
named. Most texts for a failure after the payer received the request say so: `may have
received`, `received this request`, or `received and answered`, as does the endpoint's own
failure, `500 {"error":"the provider test endpoint failed on this call; the payer may have
received the request: check its outcome before resending"}`. A few do not, yet the payer
may still have acted:

- `502 response contract version mismatch …`, which comes after the payer answered;
- `502 engine: invalid native PAS response Bundle: …` without its `the payer's system
  received and answered this request` suffix;
- the endpoint's own plain-text `502 upstream unavailable`, `504 upstream timeout` and
  `504 upstream timeout at the per-call limit`;
- a load balancer's HTML `504`.

After one of these, or a text that says the payer may have received the request, check a PAS
submission's outcome with `$inquire` before resending. An `$inquire` sent within seconds of
the failure can miss a submission the payer's system is still processing, so wait a minute
and inquire again before you resend. Any other failure from the network or the endpoint
means nothing reached the payer, and a resend is safe. You may retry under the same `X-Correlation-Id`: it is your trace value, and
reusing it is never refused (§8.5).

**A refusal the payer side itself produces is not a `502`.** A member the payer does
not hold, a request with no order to decide on, a validation failure: these come back with
the payer's own status and error text — for example
`400 {"error":"no order (ServiceRequest or DeviceRequest) in draftOrders"}` for an
`order-sign` request whose `draftOrders` is empty. So do the payer gateway's own
failures: `502 {"error":"the payer's system could not be reached"}` when the payer's
own system never got the request, a `502` saying the payer's system may have acted on it
when it got the request and gave no usable answer, a `504` when the payer's system did not
answer within its gateway's deadline for it (saying the same
when it got the request), `502` or `503` when it could not read the payer's records. On a
PAS submission, any refusal the payer gateway makes after the payer's system answered says
so too: check the outcome with `$inquire` before resending. `502 hub routing failed` means
only that the endpoint could not reach the network's routing service.

### 8.4 Rate and size limits

| Limit | Value | On exceeding |
|---|---|---|
| Registration, per source IP | 50 per hour | `429` |
| Requests per client | 60 per minute | `429` |
| Concurrent requests in flight, across every caller of the endpoint | 8 | `503` |
| Request body | 5 MiB | `400` |
| One forwarded call (CRD, DTR, PAS), from its arrival | 90 seconds | `408`, `504`, or the connection is closed |
| Calls from one network address straight to a payer's host on the network, not through this endpoint | 2,000 per 5 minutes (about 6.7 a second) | `403` |

A body over the cap is refused before any of it is forwarded, with
`{"error":"request body exceeds 5 MiB"}`.

A forwarded call still running 90 seconds after it arrived is cut. If its request had not
fully arrived, it is answered `408` with
`{"error":"the request did not complete within the provider test endpoint's 90-second limit for one call: send it again"}`,
and nothing of it was forwarded. The endpoint itself waits at most 60 seconds for the
exchange's answer (its own `504 upstream timeout`), so a slow exchange is answered `504`, at the
latest when the 90 seconds run out. If
the answer was already being sent, the connection is closed. Registration and token
requests are cut at the same limit; a body that does not arrive in time is answered as an
unreadable one (`400`), and a registration that cannot be completed within the limit is
answered `503` with `registration unavailable: try again shortly`: send it again.

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
address sends straight to a payer's host on the network, and lifts by itself
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

The route `00300` payloads in §6 are accepted by the conformance payer behind route
`00300`, and they are
**not certified against the full** `hl7.org/fhir/us/davinci-pas` profiles. What the Inferno DTR
and PAS test kits find in these payloads:

- **PAS request bundle** against `profile-pas-request-bundle` 2.0.1: the identifier systems
  on the Bundle, Claim, Patient and Coverage are `http://example.org/…` placeholders, which
  the profile rejects, and the Patient and Coverage `MIN` systems also cause target-profile
  failures; `Coverage.relationship` carries a `subscriber-relationship` code where the
  profile requires an X12 slice.
- **DTR request** against `dtr-qpackage-input-parameters` 2.0.1: the Coverage carries no
  member identifier or `subscriberId` (`us-core-15`) and no `relationship` (a constraint the
  kit evaluated against CRD 2.2.1, which its DTR 2.0.1 package pulls in); the questionnaire
  canonical is the payer's example URL (§6.2).
- **CRD request**: no CRD test kit certifies this payload; treat it as payer-accepted only.
- **The `00301` payloads in §5** are not certified by the Inferno 2.2.1 kits. Payer
  acceptance on that route likewise **does not certify** them.

A few further points if you copy these payloads:

- The PAS bundle's `Patient.link` self-reference (subscriber is the beneficiary) is not
  required by PAS, and some validators cannot process it — drop it in your own copy.
- Independently of the bytes, structural validation of a PAS request against the 2.0
  profiles needs the definition of the R5 cross-version `Claim.encounter` extension, which
  HL7 publishes in its cross-version package (`hl7.fhir.uv.xver-r5.r4`), not in the PAS
  package. A validator you run without it cannot evaluate the slicing on the Claim's
  extensions; that is a limit of the validator's package set, not a defect in the payload.
- A validator without licensed CMS HCPCS terminology content reports error-level terminology
  findings on the HCPCS codings (`L8000`, `G0151`). The codes are valid, so this is a
  terminology-availability limit of the validator you run, not a payload to correct.

---

## 10. The ePA test-case set

These requests run a set of electronic prior-authorization test cases through this
endpoint. Each case states what it tests, the request, and the expected answer. §10.1 to §10.3
are the endpoint's part of the set: routing, a broken request bundle, discovery and prefetch. §10.4 onward are the clinical cases, whose
decisions are the route `00301` payer's own answers, carried as sent.

The clinical cases are shown on this guide's member, `MBR-COVERED`, with the order code and
reason of each case. A copy of the test cases you receive elsewhere may use other synthetic
members. The payer's decisions here depend on the order code, the reason (TC-08) and the
documentation sent. If you change the member in a PAS request, keep the member identifier the
payer matches on: a PAS request whose Patient carries only your own identifiers is answered
`200` with an answer that names two members, not a decision on yours (§5.3).

Each request is derived from a file you already have: `crd-00301.json` (§5.1),
`dtr-00301.json` (§5.2) or `pas-00301.json` (Appendix A). Get a token first (§3):

```bash
TOKEN=$(curl -s https://pa-test.shn-preview.org/oauth/token \
  -u "$CLIENT_ID:$CLIENT_SECRET" -d grant_type=client_credentials | jq -r .access_token)
```

The endpoint has no error code system of its own: a refusal is an HTTP status and a body.
§8 describes the refusals and the texts you are most likely to see. A refused request is
not held for later delivery or retried by the endpoint: correct it and send it again. After a failure that says the payer may
have received it, or one of the others §8.3 lists, check the outcome first (for PAS,
`$inquire`; §8.3).

### 10.1 TC-03 — routing by `Coverage.payor`

The same order goes to the payer its Coverage names, and a Coverage that names no payer on
the network is refused before any payer is called.

**Routes.** `crd-00301.json` as it is:

```bash
curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-00301.json
```

Expected answer: `HTTP 200`, the route `00301` payer's coverage answer from §5.1
(`covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`).

**A payer identifier no payer has registered.** The same request naming payer `00999`:

```bash
jq '.prefetch.coverage.contained[0].identifier[0].value = "00999"' \
  crd-00301.json > crd-unregistered.json

curl -s -w '\nHTTP %{http_code}\n' https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-unregistered.json
```

Expected answer:

```
{"error":"no registered payer for identifier urn:oid:2.16.840.1.113883.6.300|00999"}
HTTP 422
```

The identifier is matched exactly, system and value. A payer's name or label used as the
identifier value, a misspelled value, or a value under another identifier system is refused
the same way; the endpoint does not guess which payer was meant. A payor with no identifier
at all is refused with `no payer identifier on member coverage` instead (§1.3, §8.1).

**Coverages that name two payers.** A `coverage` prefetch holding two active Coverages,
one naming `00301` and one naming `00001`:

```bash
jq '.prefetch.coverage as $c | .prefetch.coverage = {
      resourceType: "Bundle", type: "searchset", total: 2,
      entry: [
        {fullUrl: "urn:uuid:3b1f0c52-7e1a-4d2b-9a44-0c1e5f6a7b01", resource: $c},
        {fullUrl: "urn:uuid:3b1f0c52-7e1a-4d2b-9a44-0c1e5f6a7b02",
         resource: ($c | .id = "c2" | .contained[0].identifier[0].value = "00001")}
      ]}' crd-00301.json > crd-two-payers.json

curl -s -w '\nHTTP %{http_code}\n' https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-two-payers.json
```

Expected answer:

```
{"error":"ambiguous coverage for routing: the coverage names more than one payer"}
HTTP 422
```

The Coverages in the request have to agree on one payer. A single Coverage is routed by its
first `payor` (§8.1).

### 10.2 TC-05 — a PAS bundle with a broken internal reference

A Claim whose requested service names a ServiceRequest the Bundle does not carry. The
endpoint carries the request as sent; the payer decides on what it receives.

```bash
jq '.entry |= map(select(.resource.resourceType != "ServiceRequest"))
    | .entry[0].resource.identifier[0].value = "5d0c8e7a-1f3b-4c62-9e15-7a2b4c6d8e91"' \
  pas-00301.json > pas-broken-reference.json

curl -s -w '\nHTTP %{http_code}\n' "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @pas-broken-reference.json
```

The Claim keeps its `extension-requestedService` reference to
`ServiceRequest/prior-auth-required-service-request`.

Expected answer: `HTTP 200` and the route `00301` payer's PAS response Bundle: a
`ClaimResponse` with `outcome` `complete` and an item review action of `A3` "Not Required"
(X12 `https://codesystem.x12.org/005010/306`; code and display disagree, §1.4), no `CommunicationRequest`, and the Patient,
Claim, Coverage, Organizations, PractitionerRole and Practitioner it references. That is
the payer's answer to a request with no order in it, relayed as sent.

### 10.3 TC-10 — discovery and prefetch

**Discovery needs no token:**

```bash
curl -s https://pa-test.shn-preview.org/cds-services
```

Expected answer: `HTTP 200` and two services, `shn-order-sign` (`order-sign`) and
`shn-order-select` (`order-select`), each with the six prefetch keys in §1.3. These are the
endpoint's own service ids; each request goes to the payer's service for its hook (§1.2).
Other hooks, `appointment-book` among them, are not offered here.

**A CRD request names its service.** Post to `/cds-services/shn-order-sign` (or
`shn-order-select`). A `POST` to `/cds-services` itself is answered `404` with
`{"error":"unmapped path", …}` and the list of operations the endpoint serves.

**Full prefetch** is §5.1's request (§10.1, "Routes").

**Prefetch without `coverage`, for a patient of your own.** Leave out `coverage` and
`fhirServer`:

```bash
jq '.hookInstance = "subset-prefetch"
    | del(.prefetch.coverage) | del(.fhirServer)
    | .context.patientId = "EPA-SUBSET-1"
    | .prefetch.patient.id = "EPA-SUBSET-1"
    | .context.draftOrders.entry[0].resource.subject.reference = "Patient/EPA-SUBSET-1"' \
  crd-00301.json > crd-subset.json

curl -s -w '\nHTTP %{http_code}\n' https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-subset.json
```

Expected answer:

```
{"error":"no coverage to route by: send prefetch.coverage or fhirServer (this gateway's system of record names no patient for this member)"}
HTTP 412
```

Nothing is filled in for a prefetch key you leave out, except that a Coverage the endpoint
reads through your `fhirServer` is carried to the payer as `prefetch.coverage` (§1.3). To
route without `coverage`, name a `fhirServer` the endpoint can read the Coverage from
(§8.1).

**Correlation.** Every answer in this section, discovery and the `422`, `412` and `404`
refusals included, carries an `X-Correlation-Id` header; send your own to trace a run (§8.5).

### 10.4 TC-01 — total knee arthroplasty (CPT `27447`)

The full loop: a coverage answer that asks for documentation, the questionnaire package, a pend without the documentation, and an approval with it. Order `27447`, reason `M17.11`.

**CRD.**

```bash
jq '.hookInstance = "tc01-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "27447", "display": "Total knee arthroplasty"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "M17.11", "display": "Unilateral primary osteoarthritis, right knee"}]}]' \
  crd-00301.json > tc01-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc01-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the order carrying the coverage-information extension: `covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, `info-needed` `OTH`, and `questionnaire` `http://example.org/fhir/Questionnaire/TotalKneeArthroplasty`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/TotalKneeArthroplasty"' \
  dtr-00301.json > tc01-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc01-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `TotalKneeArthroplasty` Questionnaire and a QuestionnaireResponse, and `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "27447", "display": "Total knee arthroplasty"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "27447", "display": "Total knee arthroplasty"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "M17.11", "display": "Unilateral primary osteoarthritis, right knee"}]}]
   | .entry[0].resource.identifier[0].value = "tc01-claim"
   | .identifier.value = "tc01-pas-submit"' \
  pas-00301.json > tc01-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc01-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

**PAS with the completed questionnaire.**

```bash
cat > tc01-qr.json <<'EOF'
{
  "resourceType": "QuestionnaireResponse",
  "id": "tc01-qr",
  "status": "completed",
  "questionnaire": "http://example.org/fhir/Questionnaire/TotalKneeArthroplasty",
  "subject": {
    "reference": "Patient/MBR-COVERED"
  },
  "authored": "2026-10-06T12:00:00Z",
  "item": [
    {
      "linkId": "1",
      "text": "Total Knee Arthroplasty Documentation",
      "item": [
        {
          "linkId": "1.1",
          "text": "Radiographic evidence of advanced knee osteoarthritis",
          "answer": [
            {
              "valueBoolean": true
            }
          ]
        },
        {
          "linkId": "1.2",
          "text": "Weeks of conservative therapy completed",
          "answer": [
            {
              "valueInteger": 12
            }
          ]
        },
        {
          "linkId": "1.3",
          "text": "Functional limitation interferes with daily activities",
          "answer": [
            {
              "valueBoolean": true
            }
          ]
        }
      ]
    }
  ]
}
EOF

jq --slurpfile qr tc01-qr.json \
  '.entry += [{fullUrl: "http://example.org/fhir/QuestionnaireResponse/tc01-qr", resource: $qr[0]}]
   | .entry[0].resource.supportingInfo = [{sequence: 1,
       category: {coding: [{system: "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes", code: "additionalInformation"}]},
       valueReference: {reference: "QuestionnaireResponse/tc01-qr"}}]
   | .entry[0].resource.identifier[0].value = "tc01-claim-with-documentation"
   | .identifier.value = "tc01-pas-submit-with-documentation"' \
  tc01-pas.json > tc01-pas-doc.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc01-pas-doc.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete`, item review action `A1` "Certified in total" and the payer's authorization number, and no `CommunicationRequest`.

The second submission is the first with the completed QuestionnaireResponse added as a Bundle entry and named in `Claim.supportingInfo`, under its own `Claim.identifier` and Bundle identifier. It is a separate request: it does not resolve the first one's pend.

### 10.5 TC-02 — lumbar fusion (CPT `22633`), a pend and an inquiry

The same pattern as TC-01, then `Claim/$inquire` built from the first submission (the two profile changes are what make it an inquiry, §1.6). Order `22633`, reason `M43.16`.

**CRD.**

```bash
jq '.hookInstance = "tc02-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "22633", "display": "Arthrodesis, lumbar, posterior interbody"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "M43.16", "display": "Spondylolisthesis, lumbar region"}]}]' \
  crd-00301.json > tc02-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc02-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the order carrying the coverage-information extension: `covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, `info-needed` `OTH`, and `questionnaire` `http://example.org/fhir/Questionnaire/LumbarSpinalFusion`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/LumbarSpinalFusion"' \
  dtr-00301.json > tc02-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc02-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `LumbarSpinalFusion` Questionnaire and a QuestionnaireResponse, and `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "22633", "display": "Arthrodesis, lumbar, posterior interbody"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "22633", "display": "Arthrodesis, lumbar, posterior interbody"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "M43.16", "display": "Spondylolisthesis, lumbar region"}]}]
   | .entry[0].resource.identifier[0].value = "tc02-claim"
   | .identifier.value = "tc02-pas-submit"' \
  pas-00301.json > tc02-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc02-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

**PAS with the completed questionnaire.**

```bash
cat > tc02-qr.json <<'EOF'
{
  "resourceType": "QuestionnaireResponse",
  "id": "tc02-qr",
  "status": "completed",
  "questionnaire": "http://example.org/fhir/Questionnaire/LumbarSpinalFusion",
  "subject": {
    "reference": "Patient/MBR-COVERED"
  },
  "authored": "2026-10-06T12:00:00Z",
  "item": [
    {
      "linkId": "1",
      "text": "Lumbar Spinal Fusion Documentation",
      "item": [
        {
          "linkId": "1.1",
          "text": "Weeks of conservative therapy completed",
          "answer": [
            {
              "valueInteger": 8
            }
          ]
        },
        {
          "linkId": "1.2",
          "text": "Imaging confirms instability or spondylolisthesis",
          "answer": [
            {
              "valueBoolean": true
            }
          ]
        }
      ]
    }
  ]
}
EOF

jq --slurpfile qr tc02-qr.json \
  '.entry += [{fullUrl: "http://example.org/fhir/QuestionnaireResponse/tc02-qr", resource: $qr[0]}]
   | .entry[0].resource.supportingInfo = [{sequence: 1,
       category: {coding: [{system: "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes", code: "additionalInformation"}]},
       valueReference: {reference: "QuestionnaireResponse/tc02-qr"}}]
   | .entry[0].resource.identifier[0].value = "tc02-claim-with-documentation"
   | .identifier.value = "tc02-pas-submit-with-documentation"' \
  tc02-pas.json > tc02-pas-doc.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc02-pas-doc.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete`, item review action `A1` "Certified in total" and the payer's authorization number, and no `CommunicationRequest`.

**Inquiry.**

```bash
jq '.meta.profile = ["http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-pas-inquiry-request-bundle"]
   | .entry[0].resource.meta.profile = ["http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim-inquiry"]' \
  tc02-pas.json > tc02-inquire.json

curl -s "https://pa-test.shn-preview.org/Claim/\$inquire" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc02-inquire.json
```

Expected answer: `HTTP 200` and a `Parameters` resource with two `responseBundle`s, one for each submission above: the pend (`A4` "Pending") and the documented submission (`A1` "Certified in total"). You may see more: the payer also returns earlier submissions of this order for this member and requesting provider since its last clearing (§1.5), such as another participant's run of this case. TC-01's submissions in §10.4, for the same member and provider but another order, are not returned.

The second submission is the first with the completed QuestionnaireResponse added as a Bundle entry and named in `Claim.supportingInfo`, under its own `Claim.identifier` and Bundle identifier. It is a separate request: it does not resolve the first one's pend. The pend does not change on its own: a pend that asks for documentation stays pended, since CDex `$submit-attachment` is not offered here (§1.1).

### 10.6 TC-04 — tonsillectomy (CPT `42836`), and an order the payer does not cover

Order `42836`, reason `J35.01`. The code is for a child under 12; the payer's answer depends on the code, not on the member's age, so the guide's adult member gets the same answers.

**CRD.**

```bash
jq '.hookInstance = "tc04-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "42836", "display": "Adenoidectomy and tonsillectomy, younger than age 12"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "J35.01", "display": "Chronic tonsillitis"}]}]' \
  crd-00301.json > tc04-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc04-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the order carrying the coverage-information extension: `covered` `covered`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, and `questionnaire` `http://example.org/fhir/Questionnaire/Tonsillectomy`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/Tonsillectomy"' \
  dtr-00301.json > tc04-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc04-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `Tonsillectomy` Questionnaire and a QuestionnaireResponse, and `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "42836", "display": "Adenoidectomy and tonsillectomy, younger than age 12"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "42836", "display": "Adenoidectomy and tonsillectomy, younger than age 12"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "J35.01", "display": "Chronic tonsillitis"}]}]
   | .entry[0].resource.identifier[0].value = "tc04-claim"
   | .identifier.value = "tc04-pas-submit"' \
  pas-00301.json > tc04-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc04-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

**PAS with the completed questionnaire.**

```bash
cat > tc04-qr.json <<'EOF'
{
  "resourceType": "QuestionnaireResponse",
  "id": "tc04-qr",
  "status": "completed",
  "questionnaire": "http://example.org/fhir/Questionnaire/Tonsillectomy",
  "subject": {
    "reference": "Patient/MBR-COVERED"
  },
  "authored": "2026-10-06T12:00:00Z",
  "item": [
    {
      "linkId": "1",
      "text": "Tonsillectomy Documentation",
      "item": [
        {
          "linkId": "1.1",
          "text": "Throat infections in the past 12 months",
          "answer": [
            {
              "valueInteger": 7
            }
          ]
        },
        {
          "linkId": "1.2",
          "text": "Obstructive sleep-disordered breathing documented",
          "answer": [
            {
              "valueBoolean": true
            }
          ]
        }
      ]
    }
  ]
}
EOF

jq --slurpfile qr tc04-qr.json \
  '.entry += [{fullUrl: "http://example.org/fhir/QuestionnaireResponse/tc04-qr", resource: $qr[0]}]
   | .entry[0].resource.supportingInfo = [{sequence: 1,
       category: {coding: [{system: "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes", code: "additionalInformation"}]},
       valueReference: {reference: "QuestionnaireResponse/tc04-qr"}}]
   | .entry[0].resource.identifier[0].value = "tc04-claim-with-documentation"
   | .identifier.value = "tc04-pas-submit-with-documentation"' \
  tc04-pas.json > tc04-pas-doc.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc04-pas-doc.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete`, item review action `A1` "Certified in total" and the payer's authorization number, and no `CommunicationRequest`.

The second submission is the first with the completed QuestionnaireResponse added as a Bundle entry and named in `Claim.supportingInfo`, under its own `Claim.identifier` and Bundle identifier. It is a separate request: it does not resolve the first one's pend.

**An order the payer does not cover** (CPT `42999`, unlisted procedure). The payer has a rule for this code, and the rule answers not-covered. An order the payer has no rule for at all is answered `covered` `conditional` with `info-needed` `detail-code`, the payer's default, not `not-covered`.

**CRD.**

```bash
jq '.hookInstance = "tc04-offmap-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "42999", "display": "Unlisted procedure, pharynx, adenoids, or tonsils"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "J35.01", "display": "Chronic tonsillitis"}]}]' \
  crd-00301.json > tc04-offmap-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc04-offmap-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update whose coverage-information says `covered` `not-covered`, with no `pa-needed`. The not-covered answer is the payer's, carried as sent.

**PAS.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "42999", "display": "Unlisted procedure, pharynx, adenoids, or tonsils"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "42999", "display": "Unlisted procedure, pharynx, adenoids, or tonsils"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "J35.01", "display": "Chronic tonsillitis"}]}]
   | .entry[0].resource.identifier[0].value = "tc04-offmap-claim"
   | .identifier.value = "tc04-offmap-pas-submit"' \
  pas-00301.json > tc04-offmap-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc04-offmap-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A2` "Not Certified" (the payer's denial of an order its CRD answer called not-covered; code and display disagree, §1.4), and no `CommunicationRequest`.

### 10.7 TC-06 — cataract, standard (CPT `66984`) and complex (CPT `66989`)

**Standard** (reason `H25.11`); the PAS request is submitted although CRD asked for no prior authorization:

**CRD.**

```bash
jq '.hookInstance = "tc06-standard-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "66984", "display": "Extracapsular cataract removal with insertion of intraocular lens"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H25.11", "display": "Age-related nuclear cataract, right eye"}]}]' \
  crd-00301.json > tc06-standard-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc06-standard-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update whose coverage-information says `covered` `covered`, with no `pa-needed`: no prior authorization is asked for.

**PAS.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "66984", "display": "Extracapsular cataract removal with insertion of intraocular lens"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "66984", "display": "Extracapsular cataract removal with insertion of intraocular lens"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H25.11", "display": "Age-related nuclear cataract, right eye"}]}]
   | .entry[0].resource.identifier[0].value = "tc06-standard-claim"
   | .identifier.value = "tc06-standard-pas-submit"' \
  pas-00301.json > tc06-standard-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc06-standard-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A3` "Not Required" (the payer's statement that no authorization is required; code and display disagree, §1.4), and no `CommunicationRequest`.

**Complex** (reason `H40.1131`):

**CRD.**

```bash
jq '.hookInstance = "tc06-complex-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "66989", "display": "Extracapsular cataract removal, complex, with intraocular lens and drainage device"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H40.1131", "display": "Primary open-angle glaucoma, bilateral, mild stage"}]}]' \
  crd-00301.json > tc06-complex-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc06-complex-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the order carrying the coverage-information extension: `covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, `info-needed` `OTH`, and `questionnaire` `http://example.org/fhir/Questionnaire/CataractComplex`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/CataractComplex"' \
  dtr-00301.json > tc06-complex-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc06-complex-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `CataractComplex` Questionnaire and a QuestionnaireResponse, and `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "66989", "display": "Extracapsular cataract removal, complex, with intraocular lens and drainage device"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "66989", "display": "Extracapsular cataract removal, complex, with intraocular lens and drainage device"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H40.1131", "display": "Primary open-angle glaucoma, bilateral, mild stage"}]}]
   | .entry[0].resource.identifier[0].value = "tc06-complex-claim"
   | .identifier.value = "tc06-complex-pas-submit"' \
  pas-00301.json > tc06-complex-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc06-complex-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

**PAS with the completed questionnaire.**

```bash
cat > tc06-complex-qr.json <<'EOF'
{
  "resourceType": "QuestionnaireResponse",
  "id": "tc06-complex-qr",
  "status": "completed",
  "questionnaire": "http://example.org/fhir/Questionnaire/CataractComplex",
  "subject": {
    "reference": "Patient/MBR-COVERED"
  },
  "authored": "2026-10-06T12:00:00Z",
  "item": [
    {
      "linkId": "1",
      "text": "Complex Cataract Documentation",
      "item": [
        {
          "linkId": "1.1",
          "text": "Complexity indication documented",
          "answer": [
            {
              "valueBoolean": true
            }
          ]
        },
        {
          "linkId": "1.2",
          "text": "Best-corrected visual acuity, affected eye",
          "answer": [
            {
              "valueString": "20/80"
            }
          ]
        }
      ]
    }
  ]
}
EOF

jq --slurpfile qr tc06-complex-qr.json \
  '.entry += [{fullUrl: "http://example.org/fhir/QuestionnaireResponse/tc06-complex-qr", resource: $qr[0]}]
   | .entry[0].resource.supportingInfo = [{sequence: 1,
       category: {coding: [{system: "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes", code: "additionalInformation"}]},
       valueReference: {reference: "QuestionnaireResponse/tc06-complex-qr"}}]
   | .entry[0].resource.identifier[0].value = "tc06-complex-claim-with-documentation"
   | .identifier.value = "tc06-complex-pas-submit-with-documentation"' \
  tc06-complex-pas.json > tc06-complex-pas-doc.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc06-complex-pas-doc.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete`, item review action `A1` "Certified in total" and the payer's authorization number, and no `CommunicationRequest`.

The second submission is the first with the completed QuestionnaireResponse added as a Bundle entry and named in `Claim.supportingInfo`, under its own `Claim.identifier` and Bundle identifier. It is a separate request: it does not resolve the first one's pend.

### 10.8 TC-07 — cochlear implant (CPT `69930`) with a device order (HCPCS `L8614`)

One CRD request carrying the procedure order and a DeviceRequest for the device. Reason `H90.3`.

**CRD.**

```bash
jq --argjson dr '{"resourceType": "DeviceRequest", "id": "dr1", "status": "draft", "intent": "order", "subject": {"reference": "Patient/MBR-COVERED"}, "codeCodeableConcept": {"coding": [{"system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "L8614", "display": "Cochlear device, includes all internal and external components"}]}, "insurance": [{"reference": "Coverage/c1"}]}' '.hookInstance = "tc07-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "69930", "display": "Cochlear device implantation"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H90.3", "display": "Sensorineural hearing loss, bilateral"}]}]
   | .context.draftOrders.entry += [{fullUrl: "urn:uuid:dr1", resource: $dr}]' \
  crd-00301.json > tc07-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc07-crd.json
```

Expected answer: `HTTP 200`, no cards, and two `systemActions` updates, one per order (the ServiceRequest and the DeviceRequest), each carrying the coverage-information extension: `covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, `info-needed` `OTH`, and `questionnaire` `http://example.org/fhir/Questionnaire/CochlearImplantation`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/CochlearImplantation"' \
  dtr-00301.json > tc07-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc07-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `CochlearImplantation` Questionnaire and a QuestionnaireResponse, and `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "69930", "display": "Cochlear device implantation"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "69930", "display": "Cochlear device implantation"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H90.3", "display": "Sensorineural hearing loss, bilateral"}]}]
   | .entry[0].resource.identifier[0].value = "tc07-claim"
   | .identifier.value = "tc07-pas-submit"' \
  pas-00301.json > tc07-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc07-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

**PAS with the completed questionnaire.**

```bash
cat > tc07-qr.json <<'EOF'
{
  "resourceType": "QuestionnaireResponse",
  "id": "tc07-qr",
  "status": "completed",
  "questionnaire": "http://example.org/fhir/Questionnaire/CochlearImplantation",
  "subject": {
    "reference": "Patient/MBR-COVERED"
  },
  "authored": "2026-10-06T12:00:00Z",
  "item": [
    {
      "linkId": "1",
      "text": "Cochlear Implant Audiometric Documentation",
      "item": [
        {
          "linkId": "1.1",
          "text": "Pure-tone average, better ear (dB)",
          "answer": [
            {
              "valueInteger": 78
            }
          ]
        },
        {
          "linkId": "1.2",
          "text": "Aided speech recognition score (%)",
          "answer": [
            {
              "valueInteger": 40
            }
          ]
        }
      ]
    }
  ]
}
EOF

jq --slurpfile qr tc07-qr.json \
  '.entry += [{fullUrl: "http://example.org/fhir/QuestionnaireResponse/tc07-qr", resource: $qr[0]}]
   | .entry[0].resource.supportingInfo = [{sequence: 1,
       category: {coding: [{system: "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes", code: "additionalInformation"}]},
       valueReference: {reference: "QuestionnaireResponse/tc07-qr"}}]
   | .entry[0].resource.identifier[0].value = "tc07-claim-with-documentation"
   | .identifier.value = "tc07-pas-submit-with-documentation"' \
  tc07-pas.json > tc07-pas-doc.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc07-pas-doc.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete`, item review action `A1` "Certified in total" and the payer's authorization number, and no `CommunicationRequest`.

The second submission is the first with the completed QuestionnaireResponse added as a Bundle entry and named in `Claim.supportingInfo`, under its own `Claim.identifier` and Bundle identifier. It is a separate request: it does not resolve the first one's pend. The PAS request asks for the procedure (`69930`) through a ServiceRequest; the device order is in the CRD request only.

### 10.9 TC-08 — blepharoplasty (CPT `15823`), functional and cosmetic

The same order with two reasons; the payer decides on the reason, so each request carries it in the order (CRD) and in the bundle's ServiceRequest (PAS).

**Functional** (reason `H02.831`):

**CRD.**

```bash
jq '.hookInstance = "tc08-functional-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "15823", "display": "Blepharoplasty, upper eyelid; with excessive skin weighting down lid"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H02.831", "display": "Dermatochalasis of right upper eyelid"}]}]' \
  crd-00301.json > tc08-functional-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc08-functional-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the order carrying the coverage-information extension: `covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, `info-needed` `OTH`, and `questionnaire` `http://example.org/fhir/Questionnaire/Blepharoplasty`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/Blepharoplasty"' \
  dtr-00301.json > tc08-functional-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc08-functional-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `Blepharoplasty` Questionnaire and a QuestionnaireResponse, and `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "15823", "display": "Blepharoplasty, upper eyelid; with excessive skin weighting down lid"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "15823", "display": "Blepharoplasty, upper eyelid; with excessive skin weighting down lid"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "H02.831", "display": "Dermatochalasis of right upper eyelid"}]}]
   | .entry[0].resource.identifier[0].value = "tc08-functional-claim"
   | .identifier.value = "tc08-functional-pas-submit"' \
  pas-00301.json > tc08-functional-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc08-functional-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

**PAS with the completed questionnaire.**

```bash
cat > tc08-functional-qr.json <<'EOF'
{
  "resourceType": "QuestionnaireResponse",
  "id": "tc08-functional-qr",
  "status": "completed",
  "questionnaire": "http://example.org/fhir/Questionnaire/Blepharoplasty",
  "subject": {
    "reference": "Patient/MBR-COVERED"
  },
  "authored": "2026-10-06T12:00:00Z",
  "item": [
    {
      "linkId": "1",
      "text": "Blepharoplasty Documentation",
      "item": [
        {
          "linkId": "1.1",
          "text": "Superior visual-field loss in degrees (taped vs untaped)",
          "answer": [
            {
              "valueInteger": 24
            }
          ]
        },
        {
          "linkId": "1.2",
          "text": "Eyelid obstructs the visual axis",
          "answer": [
            {
              "valueBoolean": true
            }
          ]
        }
      ]
    }
  ]
}
EOF

jq --slurpfile qr tc08-functional-qr.json \
  '.entry += [{fullUrl: "http://example.org/fhir/QuestionnaireResponse/tc08-functional-qr", resource: $qr[0]}]
   | .entry[0].resource.supportingInfo = [{sequence: 1,
       category: {coding: [{system: "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes", code: "additionalInformation"}]},
       valueReference: {reference: "QuestionnaireResponse/tc08-functional-qr"}}]
   | .entry[0].resource.identifier[0].value = "tc08-functional-claim-with-documentation"
   | .identifier.value = "tc08-functional-pas-submit-with-documentation"' \
  tc08-functional-pas.json > tc08-functional-pas-doc.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc08-functional-pas-doc.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete`, item review action `A1` "Certified in total" and the payer's authorization number, and no `CommunicationRequest`.

The second submission is the first with the completed QuestionnaireResponse added as a Bundle entry and named in `Claim.supportingInfo`, under its own `Claim.identifier` and Bundle identifier. It is a separate request: it does not resolve the first one's pend.

**Cosmetic** (reason `Z41.1`):

**CRD.**

```bash
jq '.hookInstance = "tc08-cosmetic-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "15823", "display": "Blepharoplasty, upper eyelid; with excessive skin weighting down lid"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "Z41.1", "display": "Encounter for cosmetic surgery"}]}]' \
  crd-00301.json > tc08-cosmetic-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc08-cosmetic-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update whose coverage-information says `covered` `not-covered`, with no `pa-needed`. The not-covered answer is the payer's, carried as sent.

**PAS.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "15823", "display": "Blepharoplasty, upper eyelid; with excessive skin weighting down lid"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "15823", "display": "Blepharoplasty, upper eyelid; with excessive skin weighting down lid"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "Z41.1", "display": "Encounter for cosmetic surgery"}]}]
   | .entry[0].resource.identifier[0].value = "tc08-cosmetic-claim"
   | .identifier.value = "tc08-cosmetic-pas-submit"' \
  pas-00301.json > tc08-cosmetic-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc08-cosmetic-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A2` "Not Certified" (the payer's denial of an order its CRD answer called not-covered; code and display disagree, §1.4), and no `CommunicationRequest`.

### 10.10 TC-09 — CPAP (HCPCS `E0601`)

A device order: the CRD request carries a DeviceRequest in place of §5.1's ServiceRequest. Reason `G47.33`.

**CRD.**

```bash
jq --argjson dr '{"resourceType": "DeviceRequest", "id": "dr1", "status": "draft", "intent": "order", "subject": {"reference": "Patient/MBR-COVERED"}, "codeCodeableConcept": {"coding": [{"system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "E0601", "display": "Continuous positive airway pressure (CPAP) device"}]}, "reasonCode": [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "G47.33", "display": "Obstructive sleep apnea"}]}], "insurance": [{"reference": "Coverage/c1"}]}' \
  '.hookInstance = "tc09-order-sign"
   | .context.draftOrders.entry = [{fullUrl: "urn:uuid:dr1", resource: $dr}]' \
  crd-00301.json > tc09-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc09-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the DeviceRequest whose coverage-information says `covered` `covered`, `pa-needed` `conditional`, `info-needed` `detail-code`.

**PAS.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "E0601", "display": "Continuous positive airway pressure (CPAP) device"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "code": "E0601", "display": "Continuous positive airway pressure (CPAP) device"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "G47.33", "display": "Obstructive sleep apnea"}]}]
   | .entry[0].resource.identifier[0].value = "tc09-claim"
   | .identifier.value = "tc09-pas-submit"' \
  pas-00301.json > tc09-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc09-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A3` "Not Required" (the payer's statement that no authorization is required; code and display disagree, §1.4), and no `CommunicationRequest`.

The PAS request asks for `E0601` through a ServiceRequest; the DeviceRequest is in the CRD request only.

### 10.11 TC-05 — bariatric surgery (CPT `43775`), an adaptive questionnaire

A coverage answer that asks for documentation through an adaptive questionnaire, and a pend without the documentation. Order `43775`, reason `E66.01`. (§10.2 is TC-05's broken-bundle check, run on the §5.3 bundle.)

**CRD.**

```bash
jq '.hookInstance = "tc05-order-sign"
   | .context.draftOrders.entry[0].resource.code.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "43775", "display": "Laparoscopy, surgical, gastric restrictive procedure; longitudinal gastrectomy (ie, sleeve gastrectomy)"}]
   | .context.draftOrders.entry[0].resource.reasonCode = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "E66.01", "display": "Morbid (severe) obesity due to excess calories"}]}]' \
  crd-00301.json > tc05-crd.json

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc05-crd.json
```

Expected answer: `HTTP 200`, no cards, and one `systemActions` update on the order carrying the coverage-information extension: `covered` `conditional`, `pa-needed` `auth-needed`, `doc-needed` `clinical`, `doc-purpose` `withpa`, `info-needed` `OTH`, and `questionnaire` `http://example.org/fhir/Questionnaire/BariatricSurgery`.

**DTR.**

```bash
jq '(.parameter[] | select(.name == "questionnaire") | .valueCanonical) = "http://example.org/fhir/Questionnaire/BariatricSurgery"' \
  dtr-00301.json > tc05-dtr.json

curl -s "https://pa-test.shn-preview.org/Questionnaire/\$questionnaire-package" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc05-dtr.json
```

Expected answer: `HTTP 200` and a `Parameters` resource: `packagebundle` carries the `BariatricSurgery` Questionnaire as an adaptive questionnaire (`dtr-questionnaire-adapt`) holding only its first group, "Eligibility" (body mass index, and months of medically supervised diet completed), and an in-progress QuestionnaireResponse (`dtr-questionnaireresponse-adapt`) that names its contained Questionnaire (`#contained-questionnaire`); `outcome` carries the payer's prepopulation warning from §5.2 (the Coverage in `dtr-00301.json` carries no `subscriberId` or `beneficiary.identifier`).

The remaining questions (comorbidities and evaluation, and a shorter-diet follow-up asked only when fewer than six months are answered) come from DTR `$next-question`, which is not offered here (§1.1), so the completed adaptive answers cannot be produced through this endpoint.

**PAS without documentation.**

```bash
jq '.entry[0].resource.item[0].productOrService.coding = [{"system": "http://www.ama-assn.org/go/cpt", "code": "43775", "display": "Laparoscopy, surgical, gastric restrictive procedure; longitudinal gastrectomy (ie, sleeve gastrectomy)"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.code.coding) = [{"system": "http://www.ama-assn.org/go/cpt", "code": "43775", "display": "Laparoscopy, surgical, gastric restrictive procedure; longitudinal gastrectomy (ie, sleeve gastrectomy)"}]
   | (.entry[] | select(.resource.resourceType == "ServiceRequest") | .resource.reasonCode) = [{"coding": [{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": "E66.01", "display": "Morbid (severe) obesity due to excess calories"}]}]
   | .entry[0].resource.identifier[0].value = "tc05-claim"
   | .identifier.value = "tc05-pas-submit"' \
  pas-00301.json > tc05-pas.json

curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @tc05-pas.json
```

Expected answer: `HTTP 200` and a PAS response Bundle: a `ClaimResponse` with `outcome` `complete` and item review action `A4` "Pending", and a `CommunicationRequest` (payload `102089-0`, as in §5.3).

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
    "value": "example-pas-submit"
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

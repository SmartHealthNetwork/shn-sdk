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
  must be synthetic too.
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
| `GET /healthz` | no | Liveness |

**Not offered.** `Claim/$inquire`, Subscription, `Questionnaire/$next-question` and
`Questionnaire/$populate` are not exposed here. This endpoint is submission-only: once a
payer pends a request, the pend's later resolution is not observable through it.

### 1.2 Hooks and CDS services

Three services are advertised, one per hook the network carries. `GET /cds-services` needs
no bearer, so you can read this before you register:

| Service id | Hook |
|---|---|
| `shn-order-sign` | `order-sign` |
| `shn-order-select` | `order-select` |
| `shn-order-dispatch` | `order-dispatch` |

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
- **`order-dispatch` is advertised here but reaches no payer today.** Neither reference
  payer runs the `order-dispatch` leg, so a well-formed `order-dispatch` request fails at
  routing with `502 {"error":"hub routing failed"}` — earlier than the `422` above, and
  without the hook list. Build against `order-sign` (§5, §6) or `order-select`.

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
- For an advertised prefetch key you leave out, it supplies the value **only from its own
  synthetic records**. Nothing is invented.
- Everything else in your request is carried to the payer as you sent it.

The published test member `MBR-COVERED` is stored in those records under a different id, so
**send `patient` and `coverage` yourself**, as the examples below do.

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

### 1.5 The two reference payers

| Route | `Coverage.payor` identifier | Da Vinci line | Test member | Example order |
|---|---|---|---|---|
| `00301` | `urn:oid:2.16.840.1.113883.6.300\|00301` | **2.2** | `MBR-COVERED` | HCPCS `G0151` |
| `00001` | `urn:oid:2.16.840.1.113883.6.300\|00001` | **2.0** | `MBR-COVERED` | HCPCS `L8000` |

Start with **route `00301`** (§5). It is the current Da Vinci reference payer, it is on the
2.2 line, and it is the route that demonstrates the whole loop: a CRD coverage answer, a
questionnaire package, a PAS submission that **pends with a `CommunicationRequest`**, and a
DTR relaunch keyed off that pend. Route `00001` (§6) is the 2.0-line route; its PAS
submission is approved outright, so it does not exercise the pended path.

The endpoint declares both the 2.0 and the 2.2 lines for CRD, DTR and PAS, and pairs with
each payer on a line they share. A request that cannot be carried to the payer's line
unchanged is refused rather than silently rewritten (§8).

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
across sessions. The endpoint is redeployed routinely, and a token issued just before a
redeploy can be rejected before its five minutes are up; if a call that worked a moment
ago returns `401`, request a new token and retry. Your registration is not affected.

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

This endpoint is submission-only, so how the pend later resolves is not observable here
(§1.1).

---

## 6. Worked example — route `00001` (2.0 line)

A Da Vinci reference payer on the 2.0 line. Test member **`MBR-COVERED`**, HCPCS **`L8000`**
(breast prosthesis, mastectomy bra), payer identifier
`urn:oid:2.16.840.1.113883.6.300|00001`, hook `order-sign`. This payer adjudicates coverage
against its own PlanDefinition and will not return a decision for an arbitrary CPT code.

### 6.1 CRD

```bash
cat > crd-00001.json <<'EOF'
{
  "hook": "order-sign",
  "hookInstance": "quickstart-00001-order-sign",
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
      "contained": [{"resourceType": "Organization", "id": "cms-payer", "name": "Centers for Medicare and Medicaid Services", "identifier": [{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00001"}]}]
    }
  }
}
EOF

curl -s https://pa-test.shn-preview.org/cds-services/shn-order-sign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @crd-00001.json
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
cat > dtr-00001.json <<'EOF'
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
        "contained": [{"resourceType": "Organization", "id": "cms-payer", "active": true, "name": "Centers for Medicare and Medicaid Services", "identifier": [{"system": "urn:oid:2.16.840.1.113883.6.300", "value": "00001"}]}]
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
  -d @dtr-00001.json
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
substitutions the appendix names for this route:

```bash
curl -s "https://pa-test.shn-preview.org/Claim/\$submit" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @pas-00001.json
```

Expect `HTTP 200` and a PAS response `Bundle` (`collection`) of nine entries containing
exactly one `ClaimResponse` plus the resources it references. For this request the
`ClaimResponse` has `outcome` `complete` and a review action of `A1` "Certified in total".

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
decision uses review-action code `A4` — see §5.3 for the route that produces one.

---

## 7. Published test members

| Member ID | Persona | Payer route | `Coverage.payor` identifier | Expect |
|---|---|---|---|---|
| `MBR-COVERED` | Linda Johansson | `00301` (2.2 line) | `urn:oid:2.16.840.1.113883.6.300\|00301` | CRD coverage answer, questionnaire package, and a PAS pend with a `102089-0` `CommunicationRequest` you can relaunch DTR from (§5) |
| `MBR-COVERED` | Linda Johansson | `00001` (2.0 line) | `urn:oid:2.16.840.1.113883.6.300\|00001` | CRD coverage answer, questionnaire package and a PAS approval; the DTR package names an unresolved member, and `doc-needed` is off-value-set (§6) |

These are the published test members, and the only ones the endpoint holds records for. A
member id the endpoint does not hold — a patient from your own test environment, for example — is still
carried, bound by the id you send (§8.1), but you must then supply `patient` and `coverage`
yourself, and what the payer answers for a member it does not hold is the payer's own
answer. On route `00001` the DTR limitation in §6.2 applies even to the published member.

---

## 8. Fail-closed behavior and limits

Nothing here is silently dropped, translated or invented. Every refusal is explicit.

### 8.1 Routing and membership

- **Unknown payer identifier → `422`.** If `Coverage.payor[0].identifier.value` names no
  payer registered on the network, the request is rejected with `422 Unprocessable Entity`
  (`no registered payer for identifier …`) rather than silently going nowhere. The routes
  in §1.5 are the ones documented here with test members.
- **Unknown member → carried, bound by the id you send.** The endpoint first resolves the
  member against its own synthetic roster. A CRD request whose `context.patientId`, a PAS
  bundle whose `Claim.patient`, or a DTR request whose Coverage beneficiary (or, if the
  Coverage names none, the order's subject) names a member it does not hold is bound by
  that member id alone and carried, so you can drive the hook from a patient in your own test
  environment. Two consequences: the endpoint holds no records for such a member, so leave
  out `patient` or `coverage` prefetch and the request is refused (`422`, next bullet),
  and a history key you leave out is left out of what the payer receives rather than
  supplied; and the payer resolves the member on its own, so its answer for a member it
  does not hold is the payer's own — the DTR prepopulation warning in §5.2 is the visible
  case. A request that mixes members is still rejected with `403 Forbidden`.
- **A prefetch key the endpoint cannot supply → `422`.** If you leave out an advertised
  prefetch key and the endpoint's own records hold no matching patient or coverage, the
  request is refused rather than sent with a blank or invented value.

### 8.2 Hooks and services

- **Unknown service id → `404`.** Post only to the three ids in §1.2.
- **Hook and service disagree → `400`.** Sending `"hook": "order-sign"` to
  `shn-order-select` is refused; the error names both.
- **The payer offers no service for your hook → `422`**, with the hooks it does offer. The
  exception here today is `order-dispatch`: no payer behind this endpoint carries that leg,
  so the request fails earlier, at routing, with `502` and no hook list (§1.2).

### 8.3 `502` — refused, never altered

A `502` from this endpoint means a message could not be carried faithfully, so it was not
carried at all. The cases:

- the request cannot be carried to the payer's IG line without changing it;
- the payer's CRD answer is not a valid CDS Hooks response;
- the payer does not accept the framed DTR operation;
- the payer's PAS decision cannot be stated as sent — for example a decision that denies
  while also naming an authorization number, or decision detail outside the code systems
  the decision record binds.

A `502` also covers a request that could not be routed at all — no payer behind this
endpoint carries the leg the request needs. Today that is `order-dispatch` (§1.2); the body
is `{"error":"hub routing failed"}`.

A `502` is a failed exchange, not a payer verdict. Retrying an identical request will
produce the same result.

### 8.4 Rate and size limits

| Limit | Value | On exceeding |
|---|---|---|
| Registration, per source IP | 5 per hour | `429` |
| Requests per client | 60 per minute | `429` |
| Concurrent requests in flight | 8 | `503` |
| Request body | 5 MiB | `400` |

If you are scripting repeated registration/token/CRD/DTR/PAS runs in a loop, register once
and reuse the client — a normal integration test needs a handful of registrations at most.

---

## 9. What acceptance here does and does not mean

A `200` and a payer decision prove the exchange reached the payer and the payer accepted the
request. That is **not** IG-profile certification, of the endpoint or of the payloads in this
document, and a copy of these payloads will inherit their known findings.

The `00001` payloads in §6 double as the automated payer-acceptance checks run against this
endpoint; the captured synthetic fixture is accepted by the conformance payer, and it is
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

For the **route `00001`** request (§6.3), save a copy as `pas-00001.json` with three
substitutions:

1. both `"code": "G0151"` codings → `"code": "L8000"` (on `Claim.item.productOrService` and
   on the `ServiceRequest`);
2. the Coverage's `payor[0].identifier.value`, `"00301"` → `"00001"` (this is what routes the
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

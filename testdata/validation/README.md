# Validator answers

`hapi-lane-*.json` are what a real HAPI FHIR validator answered, byte for byte. The server was
HAPI FHIR 8.10.0 (FHIR R4) loaded with the Da Vinci PAS 2.2 line's implementation guides, and the
answers were recorded on 2026-09-26 through an exact-bytes recording proxy. Each was served with
`Content-Type: application/fhir+json;charset=UTF-8`; the gzip content encoding is removed and
nothing else is changed. The requests were synthetic, with no participant or patient data.

| File | Request | Status |
|---|---|---|
| `hapi-lane-positive-warnings.json` | `POST /fhir/Claim/$validate?profile=http://hl7.org/fhir/StructureDefinition/Claim`, a synthetic Claim | 200 |
| `hapi-lane-profile-unknown.json` | `POST /fhir/ClaimResponse/$validate?profile=https://example.org/fhir/StructureDefinition/unavailable-profile`, a synthetic ClaimResponse, against a profile the server does not have | 200 |
| `hapi-lane-unparseable-body-400.json` | `POST /fhir/Patient/$validate` with the truncated body `{"resourceType":"Patient",` | 400 |

The server answers each `$validate` it can run with 200, whatever the verdict. It refuses a body it
cannot parse with 400, and a resource type it does not know with 404, each with an OperationOutcome
whose one error carries no message id. The SDK refuses a body that is not JSON before sending it, so
the tests serve the 400 answer for its shape: a non-2xx OperationOutcome.

`hapi-operation-outcome.json` is a HAPI validator's answer for a Questionnaire.

The tests serve these answers as recorded, or with deliberately malformed issues appended after the
server's own. They use hand-written bodies only for shapes no validator sends: malformed issue
members, other key spellings, message-id precedence, and a proxy's non-FHIR outage answer.

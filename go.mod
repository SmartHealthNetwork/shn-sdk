module github.com/SmartHealthNetwork/shn-sdk

go 1.26

require (
	github.com/samply/golang-fhir-models/fhir-models v0.3.2
	golang.org/x/crypto v0.52.0
)

require golang.org/x/sys v0.45.0 // indirect

// v0.9.0 shipped a CQL-backed sandbox questionnaire with an SDC launchContext whose CodeSystem a
// US-Core-only runtime egress validator rejects (Unknown Code System → 422 on the DTR-fetch leg).
// Superseded by v0.9.1 (launchContext dropped) the same day. Effective once this directive ships in
// a release > v0.9.0 (the next SDK publish).
retract v0.9.0

module github.com/SmartHealthNetwork/shn-sdk

go 1.26.0

require (
	github.com/samply/golang-fhir-models/fhir-models v0.3.2
	golang.org/x/crypto v0.56.0
)

require golang.org/x/sys v0.47.0 // indirect

// Withdrawn: the demo questionnaire's SDC launchContext used a CodeSystem a US Core runtime validator rejects (422 on the questionnaire fetch). Superseded by v0.9.1.
retract v0.9.0

// Withdrawn: superseded by v0.56.0.
retract [v0.54.0, v0.55.0]

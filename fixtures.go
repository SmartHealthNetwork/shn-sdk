// The provider-data seed-persona transaction Bundles in fixtures/providerdata/ are cribbed from
// the HL7 Da Vinci br-provider reference implementation (MIT-licensed,
// github.com/HL7-DaVinci/test-data / br-provider @ 43a4806, server/src/main/resources/seed-data/):
// the HomeOxygen personas (o2billy/o2jane — Patient + Coverage#cms + Observations + the order) and
// the home-health service persona (G0151/G0155/G0180 modeled as a ServiceRequest). The clinical
// codes are augmented to the values these personas exercise (E0431, G0151). Synthetic data only — no PHI.

package shnsdk

import (
	"embed"
	"fmt"
)

//go:embed fixtures/providerdata/*.json
var providerDataFS embed.FS

// ProviderDataPersonas lists the provider-data seed personas shipped for partners to load into
// their SoR (the "seed your SoR to match" bundle). Each is a self-contained transaction Bundle.
// Synthetic data only — no PHI. uc02 is the HospitalBeds persona:
// a seeded E0250 hospital-bed DeviceRequest carrying a LOAD-BEARING reasonCode (M62.81) — br-payer
// keys Documentation Required on exists(DeviceRequest.reasonCode), and HospitalBeds attaches its
// DTR questionnaire only when Documentation Required=true, so the reasonCode is what makes UC-02's
// order-select determination no-DTR (covered / no-PA). The reason is persona SELECTION (a real
// hospital-bed order carries an indication), not field-tuning. uc03 is the HomeOxygenDispatch
// analog of the homeoxygen persona on a DIFFERENT oxygen code (E1390 oxygen concentrator vs E0431):
// a seeded oxygen-concentrator DeviceRequest + the O2 clinical observations br-payer's prepop CQL
// auto-fills against (order-dispatch → A4 → timer A1). uc02-payerb is uc02's byte-identical twin
// EXCEPT its Coverage.payor names a SECOND payer identity (urn:oid:2.16.840.1.113883.6.300|00078,
// member MBR-PD-UC02-PB) — it exists solely so a partner (or the SHN provider-data FHIR tenant)
// can seed a genuinely multi-payer SoR for the coverage-derived payer-routing proof (FR-G40); it
// carries NO NPI, so the R-8 contracted-NPI honesty fence is vacuously satisfied. It is NOT driven
// by any live UC scenario (no console/scenario wiring reads MBR-PD-UC02-PB) — the hermetic
// two-payer routing proof is a separate, engine-local hermetic routing fixture and does not
// depend on this bundle at all; this one is for a REAL FHIR-SoR-backed multi-payer
// demonstration (multi-payer partner onboarding), seeded but otherwise inert today.
func ProviderDataPersonas() []string {
	return []string{"uc02", "uc02-payerb", "uc03", "uc04", "homeoxygen", "uc08", "uc06", "uc01", "uc01-nc", "uc07", "uc05", "uc05-nc"}
}

// ProviderDataBundle returns a persona's transaction Bundle bytes (load into a FHIR SoR to
// exercise the matching UC off provider data).
func ProviderDataBundle(persona string) ([]byte, error) {
	b, err := providerDataFS.ReadFile("fixtures/providerdata/" + persona + ".json")
	if err != nil {
		return nil, fmt.Errorf("provider-data persona %q: %w", persona, err)
	}
	return b, nil
}

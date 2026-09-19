// The provider-data seed-persona transaction Bundles in fixtures/providerdata/ are cribbed from
// the HL7 Da Vinci br-provider reference implementation (MIT-licensed,
// github.com/HL7-DaVinci/test-data / br-provider @ 43a4806, server/src/main/resources/seed-data/):
// the HomeOxygen personas (o2billy/o2jane — Patient + Coverage#cms + Observations + the order) and
// the home-health service persona (G0151/G0155/G0180 modeled as a ServiceRequest). The clinical
// codes are augmented to the values these personas exercise (E0431, G0151). The home-health
// attestation personas (uc04/uc06/uc07) additionally carry the clinician's functional assessment
// (a ClinicalImpression) and the plan-of-care goals (a Goal), both referenced from
// ServiceRequest.supportingInfo — a home PT referral carries them beside the order, and the
// HomeHealthAssessment's Physical Therapy group requires them (3.2 / 3.3); seeding them lets a
// gateway attest the whole delivered group from the record rather than invent a value. Synthetic
// data only — no PHI.

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
// order-select determination no-DTR (covered / no-PA). Its order is a DRAFT (an order still being
// chosen, as the order-select hook describes); every other persona's order is active (signed or
// dispatched). The reason is persona SELECTION (a real
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
// pending is the PEND persona: a seeded E0424 (stationary oxygen system) DeviceRequest —
// the one conditional-coverage family the Da Vinci reference payer pends on a first
// $submit rather than approving or denying outright. It exists so the prior-authorization
// follow-up (pend -> DTR package -> amend -> inquire) runs on a persona that genuinely
// pends, instead of on an approve-family order coaxed into pending. Unlike the oxygen
// DISPATCH personas (homeoxygen/uc03), E0424 is an order-SIGN family and advertises no CRD
// questionnaire canonical, so its order names its ordering clinician (a Practitioner with
// an NPI) rather than a DME supplier as performer; it carries the same two O2 observations
// the pend's HomeOxygen questionnaire is answered from, so every attested value comes from
// the persona's own record. Its NPI is Luhn-valid ("80840"+the first nine digits, per the CMS
// check-digit rule): us-core-practitioner enforces that as an invariant (us-core-17), so a
// made-up ten-digit string fails $validate — unlike the supplier NPIs on the Organization
// personas, which US Core does not constrain the same way.
func ProviderDataPersonas() []string {
	return []string{"uc02", "uc02-payerb", "uc03", "uc04", "homeoxygen", "uc08", "uc06", "uc01", "uc01-nc", "uc07", "uc05", "uc05-nc", "pending"}
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

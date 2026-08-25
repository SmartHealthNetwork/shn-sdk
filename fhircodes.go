// Package shnsdk — fhircodes.go is the single source of truth for the code systems and codes
// the SHN connector both seeds and searches. Keeping them here makes a seed/search mismatch a
// compile-time single-definition rather than a docker-gated-only failure. Dependency-free.
//
// All standard terminology codes (LOINC, SNOMED CT, ICD-10-CM, CPT) are verified against
// tx.fhir.org $lookup and pinned by TestStandardCodesVerified (FR-36: codes are validated,
// never hallucinated). The urn:shn:clinical-context codes are intentionally local — no canonical
// FHIR coding exists for those facts.
package shnsdk

const (
	// MemberSystem is the Patient.identifier system used to resolve a member to a Patient
	// (demo convention; realistic Coverage-driven resolution is additive).
	MemberSystem = "urn:shn:member"

	// Standard terminology systems.
	SystemICD10CM = "http://hl7.org/fhir/sid/icd-10-cm"
	SystemLOINC   = "http://loinc.org"
	SystemSNOMED  = "http://snomed.info/sct"
	SystemCPT     = "http://www.ama-assn.org/go/cpt"

	// ConditionCodeLumbar is the lumbar-disc-displacement Condition code (the personas' dx).
	ConditionCodeLumbar = "M51.16"
	// ODICode is the Oswestry Disability Index LOINC (HighDisability source).
	ODICode = "97909-6"
	// ImagingCPT is the prior-imaging DiagnosticReport code (the personas' X-ray/MRI).
	ImagingCPT = "72148"

	// Report-document LOINC codes (verified against tx.fhir.org $lookup 2026-06-13; pinned in
	// TestStandardCodesVerified). ReportImagingStudyLOINC is the supplemental MRI code (the one
	// SupplementalReport searches, to disambiguate from the prior-imaging X-ray = ImagingCPT).
	// ReportOperativeNoteLOINC is the facility operative report code (seeded truthfully;
	// FacilityRecords returns facility records by type, so it is NOT searched by code).
	ReportImagingStudyLOINC  = "18748-4" // Diagnostic imaging study
	ReportOperativeNoteLOINC = "11504-8" // Surgical operation note

	// SHN-local codes for clinical facts with NO canonical FHIR coding (documented in the
	// spec). The seed Observations carry these and fhirsor searches them.
	SystemSHNClinical            = "urn:shn:clinical-context"
	ConservativeTherapyWeeksCode = "conservative-therapy-weeks"
	NeuroDeficitCode             = "neuro-deficit"
	// PatientReportedCode signals that the PA workflow requires a patient-reported
	// functional-status attestation (FR-27). Seeded as a boolean Observation for
	// MBR-UC07 only; fhirsor reads it into ClinicalContext.PatientReported.
	PatientReportedCode = "patient-reported-required"

	// SNOMED CT procedure codes for the surgical personas — DISTINCT concepts, verified
	// against a terminology service (tx.fhir.org $lookup, 2026-06-13) and pinned by
	// TestStandardCodesVerified (FR-36: codes are validated, never hallucinated).
	ProcLaminectomySNOMED     = "387731002" // Laminectomy
	ProcMicrodiscectomySNOMED = "178625001" // Primary lumbar microdiscectomy

	// HomeOxygen-family LOINC codes (R3 — UC-03's re-key). These are not merely
	// tx.fhir.org-verified: they are the EXACT codes the real Da Vinci reference payer's own
	// HomeOxygenDispatchPrepopulation CQL library reads (internal/brpayermirror's captured
	// Questionnaire-HomeOxygen.json items 2.2/2.3 cite them by initialExpression), confirmed
	// live by test/tworilive's TestTwoRI_ProviderData_UC03 (2.2==87 off 59408-5, 2.3==53 off
	// 2703-7). Pinned by TestStandardCodesVerified.
	OxygenSaturationLOINC = "59408-5" // Oxygen saturation in Arterial blood by Pulse oximetry
	ArterialPaO2LOINC     = "2703-7"  // Oxygen [Partial pressure] in Arterial blood
)

// DemoUC03OxygenSaturationPct / DemoUC03ArterialPaO2mmHg are the SHARED demo-lane
// HomeOxygen facts for MBR-D-UC03 (R3, register §11 ruling (b)): the value
// internal/fhirseed.go SEEDS as her real Observation AND the value the hermetic operated
// $populate fixture (internal/brpayermirror's populate_loopback.go) computes for her —
// ONE fact, not two independently-maintained numbers that could silently drift apart
// (the anti-pattern this slice exists to rule out). Deliberately distinct from the live
// reference payer's real values for MBR-OX (86/54) and MBR-PD-UC03 (87/53) — see
// internal/fhirseed.go's demoClinicalSeeds comment — so no test can pass off a shared
// canned book across all three oxygen personas.
const (
	DemoUC03OxygenSaturationPct = 89
	DemoUC03ArterialPaO2mmHg    = 56
)

// ReportValueSet is the set of LOINC document/report codes the gateway treats as
// supplemental reports attachable to a PA (Flag 3). Both verified + pinned (FR-36,
// TestStandardCodesVerified).
var ReportValueSet = []string{ReportImagingStudyLOINC, ReportOperativeNoteLOINC}

// ProcedureValueSet is the bounded demo set of SNOMED procedure codes that count as
// relevant prior surgery for ClinicalContext.PriorSurgery (Flag 4) — NOT a clinical
// rules engine. Both verified + pinned (FR-36, TestStandardCodesVerified).
var ProcedureValueSet = []string{ProcLaminectomySNOMED, ProcMicrodiscectomySNOMED}

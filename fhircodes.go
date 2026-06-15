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
	// (sandbox convention; realistic Coverage-driven resolution is additive).
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
)

// ReportValueSet is the set of LOINC document/report codes the gateway treats as
// supplemental reports attachable to a PA (Flag 3). Both verified + pinned (FR-36,
// TestStandardCodesVerified).
var ReportValueSet = []string{ReportImagingStudyLOINC, ReportOperativeNoteLOINC}

// ProcedureValueSet is the bounded demo set of SNOMED procedure codes that count as
// relevant prior surgery for ClinicalContext.PriorSurgery (Flag 4) — NOT a clinical
// rules engine. Both verified + pinned (FR-36, TestStandardCodesVerified).
var ProcedureValueSet = []string{ProcLaminectomySNOMED, ProcMicrodiscectomySNOMED}

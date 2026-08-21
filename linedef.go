package shnsdk

import "strings"

// LineOf returns the line component of a contract-version token — everything
// after "@" (e.g. LineOf("pa.pas@2.1") == "2.1"). Returns "" for a token with
// no "@" (malformed per the token grammar).
func LineOf(token string) string {
	_, line, ok := strings.Cut(token, "@")
	if !ok {
		return ""
	}
	return line
}

// PASDef is the per-line PAS contract definition — the strip/keep policy and
// profile canonicals that used to live in prose comments (sdk/pas.go:294-299,
// sdk/pasresponder.go:463-465) now live here as data, per line.
type PASDef struct {
	Line                 string // "2.0" | "2.1" | "2.2"
	PackageVersion       string // "2.0.1" | "2.1.0" | "2.2.1" — mirrors tools/contracts/manifest.json (parity-pinned by test)
	ClaimResponseProfile string // set as meta.profile on built ClaimResponses
	// NOT a field (was SubmitBundleMetaProfile, added unconsumed and
	// REMOVED by the 2.2-lane conformance fix wave under the produce-iff rule): stamping
	// meta.profile on the Bundle-embedded Claim. PAS 2.1+ slices the request
	// Bundle's Claim entry against TWO candidate type profiles
	// (profile-pas-request-bundle.json's Bundle.entry:Claim.resource lists
	// {profile-claim-update, profile-claim} at 2.1.0 and 2.2.1; 2.0.1 lists
	// profile-claim-update alone), and the IG's own example bundles DO carry
	// meta.profile on that Claim. But the slicing discriminator is {type: type,
	// path: resource} — the resource TYPE, not the declared profile — and a live
	// probe against the 2.2 lane proved a declared meta.profile does NOT let the
	// validator pick a candidate: with meta.profile=[…/profile-claim] the HAPI
	// validator still reports "The entry resource did not match any of the allowed
	// profiles" AND additionally validates the declared profile a second time
	// (17 → 21 error-severity issues), which also breaks FR-36's no-profile
	// $validate gate (2 → 8). The IG's OWN example instances — which carry
	// meta.profile — fail identically on the same lane, because matching a
	// candidate requires expanding the licensed/undistributed X12 + CMS-POS value
	// sets no package ships (see test/conformance/davinci_gap_test.go's
	// pasSliceMatchConsequence). A field only a fake could ever make useful has no
	// legitimate producer, so it is not modeled. Re-add iff a validator that can
	// expand those value sets makes the declaration load-bearing.
	//
	// ClaimResponseRequestRequired: PAS 2.1+ makes ClaimResponse.request mandatory
	// (StructureDefinition-profile-claimresponse-base.json's differential sets
	// min=1 at 2.1.0 AND 2.2.1 — snapshot min=1 on profile-claimresponse itself;
	// the 2.0.1 differential leaves it min=0). The response builders populate it
	// with a business-identifier Reference to the request Claim they answer —
	// Reference.identifier = {urn:shn:correlation, correlationID}, the SAME
	// identifier every SHN-built request Claim carries (buildPASClaim /
	// internal/pas.buildClaim set Claim.identifier from that correlation id, and
	// the responder is handed that same id as its exchange correlation). NOT a
	// literal "Claim/<id>" reference: the conformant submit path stamps its own
	// stable Claim.id (convergence-claim) while the correlation identifier holds
	// across BOTH the minimized and conformant paths.
	ClaimResponseRequestRequired bool
	// PendedResponseOutcome is the ClaimResponse.outcome code the PENDED response's
	// inner ClaimResponse carries. PAS 2.0.1/2.1.0 bind ClaimResponse.outcome to
	// base R4 http://hl7.org/fhir/ValueSet/remittance-outcome|4.0.1 (required),
	// which includes "queued". PAS 2.2.1 REPLACES that binding with its own
	// ValueSet-ClaimResponseOutcome = {complete, error, partial} (required) — so
	// "queued" stops being a conformant outcome at 2.2. The IG's own pended example
	// (ClaimResponse-PractitionerRequestorPendingResponseExample) uses "complete"
	// at 2.1.0 AND 2.2.1: in Da Vinci PAS the pend is carried by the A4 reviewAction
	// / the Task, never by the outcome code — and SHN's wire convention already
	// discriminates a pend by the Bundle's Task entry (see BuildPendedResponse and
	// gateway/engine.normalizePASResponse, which checks the Task FIRST), so the
	// 2.2 code change is conformance-only and changes no routing.
	PendedResponseOutcome            string
	ResponseBundleIdentifierRequired bool // 2.2: true (PAS 2.2 makes Bundle.identifier mandatory)
	// ClaimItemLineDetailRequired: PAS 2.1+ requires (verified against the PAS 2.1.0/2.2.1
	// package differential.element for profile-claim.json + profile-claim-update.json;
	// introduced 2.1.0, UNCHANGED into 2.2.1; absent at 2.0.1 — see PAS package differential):
	//   - Claim.item.extension:certificationType  min=1 (canonical extension-certificationType)
	//   - Claim.item.extension:requestType        min=1 (canonical extension-serviceItemRequestType)
	//   - Claim.item.location[x]                  min=1 (CodeableConcept; X12278LocationType
	//     required binding, itself bound to the NON-licensed CMS place-of-service code set)
	// Gates the item-detail extensions + locationCodeableConcept the builders add at 2.1/2.2.
	ClaimItemLineDetailRequired bool
	// ClaimRelatedRelationshipRequired: PAS 2.1+ profile-claim-update requires
	// Claim.related.relationship (min=1, patternCodeableConcept
	// http://terminology.hl7.org/CodeSystem/ex-relatedclaimrelationship#prior — a standard HL7
	// terminology code, not X12-licensed) — absent at 2.0.1. Verified against the PAS
	// 2.1.0/2.2.1 package differential for profile-claim-update.json (unchanged 2.1.0→2.2.1).
	ClaimRelatedRelationshipRequired bool
	// extended as package diffs dictate — additive fields only
}

// pasLineDefs holds the per-line PAS contract definitions this build speaks
// natively (see NativeContractVersions's pa.pas@2.0/2.1/2.2 tokens).
var pasLineDefs = map[string]PASDef{
	"2.0": {
		Line:                             "2.0",
		PackageVersion:                   "2.0.1",
		ClaimResponseProfile:             pasProfileClaimResponse,
		ClaimResponseRequestRequired:     false,
		PendedResponseOutcome:            "queued",
		ResponseBundleIdentifierRequired: false,
		ClaimItemLineDetailRequired:      false,
		ClaimRelatedRelationshipRequired: false,
	},
	"2.1": {
		Line:                             "2.1",
		PackageVersion:                   "2.1.0",
		ClaimResponseProfile:             pasProfileClaimResponse,
		ClaimResponseRequestRequired:     true,
		PendedResponseOutcome:            "queued",
		ResponseBundleIdentifierRequired: false,
		ClaimItemLineDetailRequired:      true,
		ClaimRelatedRelationshipRequired: true,
	},
	"2.2": {
		Line:                             "2.2",
		PackageVersion:                   "2.2.1",
		ClaimResponseProfile:             pasProfileClaimResponse,
		ClaimResponseRequestRequired:     true,
		PendedResponseOutcome:            "complete",
		ResponseBundleIdentifierRequired: true,
		ClaimItemLineDetailRequired:      true,
		ClaimRelatedRelationshipRequired: true,
	},
}

// PASLineDef returns the per-line PAS contract definition for line ("2.0",
// "2.1", "2.2"). ok is false for a line this build does not speak natively.
func PASLineDef(line string) (PASDef, bool) {
	d, ok := pasLineDefs[line]
	return d, ok
}

// DTRDef is the per-line DTR contract definition — the strip/keep policy and
// extension/codesystem choices that used to live in prose comments (sdk/dtr.go:36,
// 255, 422, 430, 457, 594, 598 and the internal/dtr twins) now live here as data,
// per line. Derived live from packages.simplifier.net/hl7.fhir.us.davinci-dtr
// {2.0.1,2.1.0,2.2.0} — the per-line DTR package differential.
type DTRDef struct {
	Line           string // "2.0" | "2.1" | "2.2"
	PackageVersion string // mirrors tools/contracts/manifest.json (parity-pinned by test)

	// SingleCoverageConstraint: DTR 2.2 (verified against StructureDefinition-dtr-
	// questionnaireresponse.json's differential + StructureDefinition-qr-coverage.json):
	// QuestionnaireResponse gains a dedicated, REQUIRED (min=1, max=*) qr-coverage
	// extension (Reference(Coverage)) carrying the coverage reference; the pre-existing
	// qr-context slice — which at 2.0/2.1 carried BOTH the coverage and order
	// references — becomes optional (min=0, was min=2) and is used for the order
	// reference only at 2.2. false at "2.0"/"2.1" (coverage rides the generic
	// qr-context slice, as today).
	SingleCoverageConstraint bool

	// QuestionnairePackageReturnShape records what the $questionnaire-package response
	// Bundle (profile DTR-QPackageBundle) requires at this line, verified against that
	// profile's differential Bundle.entry slicing — NOT a "return becomes a Parameters
	// resource" wire-envelope change (the optional Parameters wrapper profile,
	// dtr-qpackage-output-parameters, already existed at 2.0.1 and is materially
	// unchanged since — cosmetic slice-name casing only; see the DTR package differential).
	// Values: "unconstrained" (2.0: Bundle.entry carries no DTR-profile slicing),
	// "qr-optional" (2.1: Bundle.entry:questionnaireResponse min=0 max=1), "qr-required"
	// (2.2: min=1 max=1 — the package MUST include a QuestionnaireResponse entry).
	// Gates BuildQuestionnairePackageAtLine's optional questionnaireResponse parameter:
	// an empty/nil QR is rejected only at "qr-required".
	QuestionnairePackageReturnShape string

	// QuestionnairePackageCoverageRequired gates the $questionnaire-package REQUEST
	// (the OTHER side of the wire from QuestionnairePackageReturnShape, above):
	// verified live against the pinned DTR package's StructureDefinition-dtr-qpackage-
	// input-parameters.json + OperationDefinition-questionnaire-package.json
	// (2026-08-12) — `coverage` is min=1 at EVERY line (2.0.1/2.1.0/2.2.0 all require
	// it; a real Da Vinci payer 400s "The 'coverage' parameter is required (min=1)"
	// without it regardless of line, per the long-standing FR-G28 comment on
	// buildQuestionnairePackageRequest), but 2.2.0 additionally TIGHTENS max from *
	// to 1 (min=1 max=1, i.e. exactly one) — the genuinely NEW per-line fact. true
	// only at "2.2": the request builder refuses BEFORE the wire (a legible local
	// refusal replacing what would otherwise be the partner's 400) when a caller
	// omits coverage at this line; 2.0/2.1 keep today's looser behavior (coverage is
	// attached when the caller supplies one, omitted otherwise — no local refusal,
	// as before this field existed).
	QuestionnairePackageCoverageRequired bool

	// NOT a field: an earlier draft DTRDef included a QRItemWeightExtension field
	// (the canonical http://hl7.org/fhir/StructureDefinition/itemWeight — DTR 2.2's new
	// QuestionnaireResponse.item.answer.VALUE.extension:itemWeight slice, the successor of
	// the "ordinalValue" slice present-but-never-built at 2.0.1/2.1.0 and removed at 2.2.0.
	// 2.2.0's own differential declares the slice one level up, at item.answer.extension,
	// which the extension's SD context forbids — the engine reads the SD, not the
	// differential).
	// DROPPED by owner ruling (2026-08-11): the verified package diff governs over the
	// plan's pre-verification field list — itemWeight is OPTIONAL (min=0) at 2.2, and
	// SHN has no honest per-answer weight source to stamp on it (FR-36 applies to
	// values, not just codes — same reasoning that kept "ordinalValue" unbuilt at every
	// prior line). A field only a fabricated value could ever fill has no legitimate
	// producer. Re-add as an additive field iff the package requires it AND a legitimate
	// consumer with honest data materializes (the deferred in-band CRD version-extension logic).

	// AutoOriginSourceCode is the information-origin extension's source code
	// FillQuestionnaireAtLine stamps on a CQL/EHR-auto-populated answer.
	// Verified against ValueSet-informationOrigins.json + CodeSystem-dtr-
	// informationorigin-codes.json: the 2.0.1/2.1.0 code "auto" (CodeSystem/temp) is
	// RETIRED at 2.2.0 in favor of two more specific codes under the renamed
	// CodeSystem/dtr-informationorigin-codes — "auto-client" ("Information was
	// auto-populated by the client side (e.g., EHR)") is the one that matches this
	// provider-side auto-fill (never "auto-server", the payer-side $populate case). The
	// DTR 2.2 informationOrigins binding also tightens from extensible to required, so
	// an un-migrated "auto" code would fail terminology validation at 2.2.
	AutoOriginSourceCode string

	// IntendedUseCodeSystem is the CodeSystem the QuestionnaireResponse-level
	// intendedUse extension's "withpa" code is drawn from at this line. DTR's
	// StructureDefinition-intendedUse.json REQUIRED-binds Extension.value[x] to the
	// CRD DocReason ValueSet at every line (2.1.0 → …/ValueSet/DocReason, 2.2.0 →
	// …/ValueSet/DocReason|2.2.1), and CRD MOVED the concept between lines:
	// ValueSet-DocReason at CRD 2.1.0 includes {withpa, withclaim, withorder,
	// retain-doc} from …/CodeSystem/temp, while at CRD 2.2.1 it includes the SAME
	// four codes from the renamed …/CodeSystem/coverage-information-codes — and
	// CodeSystem-temp at 2.2.1 no longer defines "withpa" at all. So the code is
	// "withpa" at every line and only the system moves at 2.2; an un-migrated
	// temp#withpa is an ERROR-severity required-binding failure on a 2.2 lane
	// (verified live: 2 errors → 0 with the system swapped).
	IntendedUseCodeSystem string
}

var dtrLineDefs = map[string]DTRDef{
	"2.0": {
		Line:                                 "2.0",
		PackageVersion:                       "2.0.1",
		SingleCoverageConstraint:             false,
		QuestionnairePackageReturnShape:      "unconstrained",
		QuestionnairePackageCoverageRequired: false,
		AutoOriginSourceCode:                 "auto",
		IntendedUseCodeSystem:                crdTempCodeSystem,
	},
	"2.1": {
		Line:                                 "2.1",
		PackageVersion:                       "2.1.0",
		SingleCoverageConstraint:             false,
		QuestionnairePackageReturnShape:      "qr-optional",
		QuestionnairePackageCoverageRequired: false,
		AutoOriginSourceCode:                 "auto",
		IntendedUseCodeSystem:                crdTempCodeSystem,
	},
	"2.2": {
		Line:                                 "2.2",
		PackageVersion:                       "2.2.0",
		SingleCoverageConstraint:             true,
		QuestionnairePackageReturnShape:      "qr-required",
		QuestionnairePackageCoverageRequired: true,
		AutoOriginSourceCode:                 "auto-client",
		IntendedUseCodeSystem:                crdCoverageInformationCodeSystem,
	},
}

// DTRLineDef returns the per-line DTR contract definition for line ("2.0",
// "2.1", "2.2"). ok is false for a line this build does not speak natively.
func DTRLineDef(line string) (DTRDef, bool) {
	d, ok := dtrLineDefs[line]
	return d, ok
}

// CRDDef is the per-line CRD contract definition. The package diff was derived
// live (packages.simplifier.net/hl7.fhir.us.davinci-crd/{2.0.1,2.1.0,2.2.1},
// StructureDefinition-ext-coverage-information.json differential) and found NO
// behavioral delta: the split covered/pa-needed/questionnaire/satisfied-pa-id
// sub-extension shape BuildCardsAtLine builds is min/max-identical across all three
// STUs — so CRDDef carries no behavior-gating field beyond PackageVersion (see
// BuildCardsAtLine's doc comment, sdk/crd.go, for the full citation).
//
// In-band CRD version extensions (NOT a field here): 2.2.1 adds two new
// extensions — CDSHookServiceRequestExtensionRequestCRDVersion (client → server,
// requested CRD version on the hook request) and CDSHookServicesExtensionCRDVersion
// (server → client, supported-version list on /cds-services discovery) — both
// verified OPTIONAL (min=0) in the package differential. Per the produce-iff ruling
// (produce iff the package REQUIRES it), neither is modeled: the package does not
// require them. Re-add as a field iff a future CRD line makes either mandatory.
type CRDDef struct {
	Line           string // "2.0" | "2.1" | "2.2"
	PackageVersion string // mirrors tools/contracts/manifest.json (parity-pinned by test)
	// extended as package diffs dictate — additive fields only
}

var crdLineDefs = map[string]CRDDef{
	"2.0": {Line: "2.0", PackageVersion: "2.0.1"},
	"2.1": {Line: "2.1", PackageVersion: "2.1.0"},
	"2.2": {Line: "2.2", PackageVersion: "2.2.1"},
}

// CRDLineDef returns the per-line CRD contract definition for line ("2.0",
// "2.1", "2.2"). ok is false for a line this build does not speak natively.
func CRDLineDef(line string) (CRDDef, bool) {
	d, ok := crdLineDefs[line]
	return d, ok
}

package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// QuestionnaireCanonicalLumbarMRI is the canonical URL for the lumbar MRI PA
// questionnaire used in CDS Hooks card extensions. Mirrors
// internal/crd.QuestionnaireCanonicalLumbarMRI byte-for-byte (parity proven by
// test/sdkparity/crd_parity_test.go).
const QuestionnaireCanonicalLumbarMRI = "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri"

// OrderSelectContext is the CDS Hooks context for an order-select hook invocation.
// Ported standalone from internal/crd.OrderSelectContext with the SAME json tags so
// the marshaled bytes are identical (test/sdkparity/crd_parity_test.go).
type OrderSelectContext struct {
	PatientID   string            `json:"patientId"`
	DraftOrders []json.RawMessage `json:"draftOrders"`
}

// OrderSelectRequest is the full CDS Hooks order-select hook request payload.
// Ported standalone from internal/crd.OrderSelectRequest; the prefetch carries only
// Coverage (FR-14 minimum-necessary), exactly as the substrate emits.
type OrderSelectRequest struct {
	Hook     string             `json:"hook"`
	Context  OrderSelectContext `json:"context"`
	Prefetch struct {
		Coverage json.RawMessage `json:"coverage"`
	} `json:"prefetch"`
}

// Canonical CardCoverage value-space constants (the Da Vinci CRD STU 2.1 split shape
// the type is frozen to). These name the wire codes so producers/consumers/normalizers
// reference one source of truth instead of bare string literals.
const (
	CoveredCovered      = "covered"     // Covered: service is covered
	CoveredNotCovered   = "not-covered" // Covered: service is not covered (originator stops)
	CoveredConditional  = "conditional" // Covered: coverage is conditional
	PANeededNoAuth      = "no-auth"     // PANeeded: no prior auth required
	PANeededAuthNeeded  = "auth-needed" // PANeeded: prior auth required
	PANeededSatisfied   = "satisfied"   // PANeeded: PA already satisfied (SatisfiedPaID set)
	PANeededPerformPA   = "performpa"   // PANeeded: provider must perform PA now
	PANeededConditional = "conditional" // PANeeded: PA requirement is conditional
)

// CardCoverage is the faithful-minimal projection of the Da Vinci CRD coverage-information
// system action (frozen to the STU 2.1 split shape). Cosmetic fields dropped.
type CardCoverage struct {
	Covered        string   `json:"covered"`                  // covered | not-covered | conditional
	PANeeded       string   `json:"paNeeded,omitempty"`       // no-auth | auth-needed | satisfied | performpa | conditional
	Questionnaires []string `json:"questionnaires,omitempty"` // 0..* canonical(Questionnaire)
	SatisfiedPaID  string   `json:"satisfiedPaId,omitempty"`  // present iff PANeeded == "satisfied"
}

// PARequired reports whether the coverage-information requires the provider to obtain
// prior authorization (auth-needed or performpa).
func (c CardCoverage) PARequired() bool {
	return c.PANeeded == PANeededAuthNeeded || c.PANeeded == PANeededPerformPA
}

// NeedsDTR reports whether the card advertises at least one DTR questionnaire to gather.
func (c CardCoverage) NeedsDTR() bool { return len(c.Questionnaires) > 0 }

// card is a single CDS Hooks suggestion card. Ported from internal/crd.Card.
type card struct {
	Summary   string       `json:"summary"`
	Indicator string       `json:"indicator"`
	Detail    string       `json:"detail,omitempty"`
	Extension CardCoverage `json:"extension"`
}

// cardsResponse is the CDS Hooks response envelope. Ported from internal/crd.CardsResponse.
type cardsResponse struct {
	Cards []card `json:"cards"`
}

// conformantCDSBundle is a FHIR collection Bundle carrying the draft orders inline
// (one entry per draft order). The conformant CRD order-select request models
// context.draftOrders as a FHIR Bundle (vs the minimized request's bare resource
// array), exactly as the payer-side conformant bind (gateway conformantCRDBind) and
// a real br-payer expect.
type conformantCDSBundle struct {
	ResourceType string                  `json:"resourceType"`
	Type         string                  `json:"type"`
	Entry        []conformantBundleEntry `json:"entry"`
}

type conformantBundleEntry struct {
	FullURL  string          `json:"fullUrl"`
	Resource json.RawMessage `json:"resource"`
}

// conformantOrderSelectContext is the conformant CDS Hooks order-select context: a
// FHIR Bundle of draft orders plus the userId/patientId/selections a real CDS client
// sends. Distinct from the minimized OrderSelectContext (bare draftOrders array).
type conformantOrderSelectContext struct {
	UserID      string              `json:"userId"`
	PatientID   string              `json:"patientId"`
	DraftOrders conformantCDSBundle `json:"draftOrders"`
	Selections  []string            `json:"selections"`
}

// conformantOrderSelectRequest is the full conformant CDS Hooks order-select request
// (the shape gateway conformantCRDBind accepts and a real br-payer adjudicates):
// hook/hookInstance/fhirServer + the conformant context + the Coverage prefetch.
type conformantOrderSelectRequest struct {
	Hook         string                       `json:"hook"`
	HookInstance string                       `json:"hookInstance"`
	FHIRServer   string                       `json:"fhirServer"`
	Context      conformantOrderSelectContext `json:"context"`
	Prefetch     struct {
		Patient  json.RawMessage `json:"patient,omitempty"`
		Coverage json.RawMessage `json:"coverage"`
	} `json:"prefetch"`
}

// Deterministic conformant-CRD demo-context constants. These are fixed (no time/random)
// so the builder reproduces the conformant golden byte-for-byte. The order ids are local to
// the request (the SR is wrapped fullUrl urn:uuid:<id> and selected by ServiceRequest/<id>).
const (
	conformantCRDHookInstance = "convergence-crd-hi-1"
	conformantCRDFHIRServer   = "https://provider.example/fhir"
	conformantCRDUserID       = "Practitioner/p1"
	conformantCRDOrderID      = "sr1"

	// Conformant Coverage + contained cms-payer Organization (CMS-0057). The system is the
	// NAIC Company Code OID (urn:oid:2.16.840.1.113883.6.300), HL7's registered namespace
	// for US insurance-company identifiers; value "00001" is the Da Vinci br-payer RI's
	// first plan id — a synthetic sandbox value, not an official NAIC-assigned code. These
	// are fixed (deterministic) and match the conformant golden + the existing conformant
	// goldens.
	conformantCoverageID    = "c1"
	conformantPayerOrgID    = "cms-payer"
	conformantPayerOrgName  = "Centers for Medicare and Medicaid Services"
	conformantPayerOrgValue = "00001"
	systemNAICCompanyCode   = "urn:oid:2.16.840.1.113883.6.300"
)

// BuildConformantOrderSelectRequest builds the CONFORMANT CRD order-select request bytes
// (the conformant target): hook "order-select", context.draftOrders a FHIR collection
// Bundle whose single entry is the ServiceRequest (id sr1, fullUrl urn:uuid:sr1),
// context.patientId the bare member, context.selections referencing the SR, and
// prefetch.coverage the (payer-bearing) Coverage. This is the converged CRD order-select
// request shape (the minimized BuildOrderSelectRequest has been removed — this is the sole CRD request shape);
// the gateway's conformantCRDBind accepts this shape and a real br-payer adjudicates it. Deterministic
// (no time/random). The SR keeps its US Core meta.profile (US Core resolves clean
// against the US-Core-only validator).
func BuildConformantOrderSelectRequest(serviceRequestJSON, coverageJSON []byte, patientID string) ([]byte, error) {
	// Inject the local order id into the order (the minimized BuildServiceRequest emits no
	// id; the conformant Bundle entry needs a stable id to wrap+select).
	srWithID, err := withResourceID(serviceRequestJSON, conformantCRDOrderID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant CRD: %w", err)
	}
	// context.selections must reference the order by its ACTUAL resourceType: br-payer's
	// order-select service matches selections[] against the draftOrders entries
	// type-sensitively (HAPI IdType.equalsIgnoreBase — "ServiceRequest/x" does NOT match a
	// DeviceRequest/x order, yielding 0 cards). A ServiceRequest order stays
	// "ServiceRequest/<id>"; a DeviceRequest order (UC-02 hospital-bed E0250) selects
	// "DeviceRequest/<id>". Fall back to "ServiceRequest" if the order has no parseable
	// resourceType so existing callers never regress. Deterministic. (UC-02)
	orderResourceType, _ := extractResourceTypeAndID(serviceRequestJSON)
	if orderResourceType == "" {
		orderResourceType = "ServiceRequest"
	}
	req := conformantOrderSelectRequest{
		Hook:         "order-select",
		HookInstance: conformantCRDHookInstance,
		FHIRServer:   conformantCRDFHIRServer,
		Context: conformantOrderSelectContext{
			UserID: conformantCRDUserID,
			// CDS Hooks context.patientId is the BARE patient id (not a Patient/ ref);
			// the gateway bind trims the prefix either way, but the conformant shape +
			// a real CDS client send it bare.
			PatientID: strings.TrimPrefix(patientID, "Patient/"),
			DraftOrders: conformantCDSBundle{
				ResourceType: "Bundle",
				Type:         "collection",
				Entry: []conformantBundleEntry{{
					FullURL:  "urn:uuid:" + conformantCRDOrderID,
					Resource: json.RawMessage(srWithID),
				}},
			},
			Selections: []string{orderResourceType + "/" + conformantCRDOrderID},
		},
	}
	// A real CDS client supplies the patient it holds; SHN is config-only with no
	// queryable fhirServer, so the patient MUST be inline (else a real Da Vinci payer
	// like br-payer tries to fetch Patient/{id} from fhirServer and 412s). br-payer
	// accepts an id-only Patient (captured). The id is the BARE member (no Patient/).
	// The fake fhirServer (conformantCRDFHIRServer) is INTENTIONALLY left as-is: the
	// inline patient makes that URL dead, and SHN has nothing real to fetch from. Do
	// NOT "fix" it into a resolvable endpoint — that re-introduces the 412.
	bareID := strings.TrimPrefix(patientID, "Patient/")
	patientJSON, err := json.Marshal(map[string]string{"resourceType": "Patient", "id": bareID})
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant CRD patient prefetch: %w", err)
	}
	req.Prefetch.Patient = json.RawMessage(patientJSON)
	req.Prefetch.Coverage = json.RawMessage(coverageJSON)
	return json.Marshal(req)
}

// withResourceID returns the FHIR resource JSON with its top-level "id" set to id,
// preserving every other field verbatim. Deterministic.
func withResourceID(resourceJSON []byte, id string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(resourceJSON, &m); err != nil {
		return nil, fmt.Errorf("inject id: %w", err)
	}
	idJSON, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	m["id"] = idJSON
	return json.Marshal(m)
}

// BuildCoverageWithPayer builds the CONFORMANT Coverage: the same us-core-coverage shape
// as BuildCoverage (status active, beneficiary, self relationship, MB-type identifier
// carrying coverageRef, US Core meta.profile — all KEPT) but additionally carries (a) a
// stable id "c1" and (b) a CONTAINED cms-payer Organization (identifier system|value taken
// from payer), with payor referencing it (#cms-payer). This is the additive conformant
// variant of BuildCoverage (which is byte-parity-locked and stays untouched); a
// production-conformant CRD (CMS-0057) names the payer Organization, and the contained Org
// $validates clean. Deterministic.
func BuildCoverageWithPayer(patientRef, coverageRef string, payer PayerIdentifier) ([]byte, error) {
	cov := fhir.Coverage{
		Id:          strPtr(conformantCoverageID),
		Meta:        &fhir.Meta{Profile: []string{profileUSCoreCoverage}},
		Status:      fhir.FinancialResourceStatusCodesActive,
		Beneficiary: fhir.Reference{Reference: strPtr(patientRef)},
		Payor:       []fhir.Reference{{Reference: strPtr("#" + conformantPayerOrgID)}},
		Relationship: &fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System: strPtr(systemSubscriberRelationship),
				Code:   strPtr("self"),
			}},
		},
		Identifier: []fhir.Identifier{{
			Type: &fhir.CodeableConcept{
				Coding: []fhir.Coding{{
					System: strPtr(systemV2Identifier),
					Code:   strPtr("MB"),
				}},
			},
			System: strPtr("urn:shn:coverage"),
			Value:  strPtr(coverageRef),
		}},
	}
	covJSON, err := json.Marshal(cov)
	if err != nil {
		return nil, err
	}
	// fhir.Coverage has no Contained field; splice the contained cms-payer Organization
	// in (the only field the typed model lacks). Deterministic re-marshal; the test
	// canonicalizes so key order is immaterial.
	org := fhir.Organization{
		Id:   strPtr(conformantPayerOrgID),
		Name: strPtr(conformantPayerOrgName),
		Identifier: []fhir.Identifier{{
			System: strPtr(payer.System),
			Value:  strPtr(payer.Value),
		}},
	}
	orgJSON, err := json.Marshal(org)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(covJSON, &m); err != nil {
		return nil, err
	}
	containedJSON, err := json.Marshal([]json.RawMessage{json.RawMessage(orgJSON)})
	if err != nil {
		return nil, err
	}
	m["contained"] = containedJSON
	return json.Marshal(m)
}

// ParseOrderSelectRequest deserializes an order-select CDS Hooks request. It errors if
// the hook field is not "order-select" or if there are no draft orders. Ported
// standalone from internal/crd.ParseOrderSelectRequest with identical error semantics
// (test/sdkparity/crd_parity_test.go).
//
// Deprecated: no production callers remain; retained for API stability and slated for
// removal at the next breaking shn-sdk major. New code should not depend on it.
func ParseOrderSelectRequest(data []byte) (OrderSelectRequest, error) {
	var req OrderSelectRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return req, err
	}
	if req.Hook != "order-select" {
		return req, fmt.Errorf("shnsdk: expected hook order-select, got %q", req.Hook)
	}
	if len(req.Context.DraftOrders) == 0 {
		return req, fmt.Errorf("shnsdk: order-select request must contain at least one draft order")
	}
	return req, nil
}

// BuildCards constructs a CDS Hooks CardsResponse JSON carrying the given coverage
// projection. The PA-required-per-CPT policy belongs to the partner's Adjudicator; only
// the card SHAPE is protocol. The summary/indicator are derived from the coverage:
// not-covered → "warning"; PA-required → "warning"; otherwise → "info". Byte parity with
// internal/crd.BuildCards for equivalent coverage is proven by
// test/sdkparity/crd_parity_test.go.
func BuildCards(cov CardCoverage) ([]byte, error) {
	c := card{Extension: cov}
	switch {
	case cov.Covered == CoveredNotCovered:
		c.Summary, c.Indicator = "Service not covered", "warning"
	case cov.PARequired():
		c.Summary, c.Indicator = "Prior authorization required", "warning"
	default:
		c.Summary, c.Indicator = "No prior authorization required", "info"
	}
	return json.Marshal(cardsResponse{Cards: []card{c}})
}

// ParseCards parses the CRD cards response, returning the first card's coverage
// projection (the substrate emits exactly one card). It errors if the response carries
// zero cards. Reimplements internal/crd.ParseCards standalone.
func ParseCards(data []byte) (CardCoverage, error) {
	var resp cardsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return CardCoverage{}, err
	}
	if len(resp.Cards) == 0 {
		return CardCoverage{}, fmt.Errorf("shnsdk: CardsResponse must contain at least one card")
	}
	return resp.Cards[0].Extension, nil
}

// StripCanonicalVersion drops a trailing |version from a FHIR canonical URL, leaving the
// bare canonical. A canonical with no version is returned unchanged.
func StripCanonicalVersion(canonical string) string {
	if i := strings.IndexByte(canonical, '|'); i >= 0 {
		return canonical[:i]
	}
	return canonical
}

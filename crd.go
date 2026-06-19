package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
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

// BuildOrderSelectRequest builds the CRD order-select request bytes. Reimplements
// internal/crd.BuildOrderSelectRequest standalone (no internal/ import); only the
// ServiceRequest and Coverage are included (FR-14 minimum-necessary), embedded
// verbatim as json.RawMessage. test/sdkparity asserts byte-identity with the substrate
// for the same inputs.
func BuildOrderSelectRequest(serviceRequestJSON, coverageJSON []byte, patientID string) ([]byte, error) {
	req := OrderSelectRequest{
		Hook: "order-select",
		Context: OrderSelectContext{
			PatientID:   patientID,
			DraftOrders: []json.RawMessage{json.RawMessage(serviceRequestJSON)},
		},
	}
	req.Prefetch.Coverage = json.RawMessage(coverageJSON)
	return json.Marshal(req)
}

// ParseOrderSelectRequest deserializes an order-select CDS Hooks request. It errors if
// the hook field is not "order-select" or if there are no draft orders. Ported
// standalone from internal/crd.ParseOrderSelectRequest with identical error semantics
// (test/sdkparity/crd_parity_test.go).
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

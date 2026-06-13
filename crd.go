package shnsdk

import (
	"encoding/json"
	"fmt"
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

// cardExtension carries SHN-specific PA verdict fields in a CDS Hooks card. Ported
// standalone from internal/crd.CardExtension (same json tags, same omitempty).
type cardExtension struct {
	SHNPARequired          bool   `json:"shnPaRequired"`
	QuestionnaireCanonical string `json:"questionnaireCanonical,omitempty"`
}

// card is a single CDS Hooks suggestion card. Ported from internal/crd.Card.
type card struct {
	Summary   string        `json:"summary"`
	Indicator string        `json:"indicator"`
	Detail    string        `json:"detail,omitempty"`
	Extension cardExtension `json:"extension"`
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

// BuildCards constructs a CDS Hooks CardsResponse JSON for the given PA verdict.
// The PA-required-per-CPT policy belongs to the partner's Adjudicator; only the card
// SHAPE is protocol. When paRequired is true, the card carries a "warning" indicator
// and the given questionnaireCanonical in the SHN extension. When false, an "info"
// card is returned. Byte parity with internal/crd.BuildCards for equivalent verdicts
// is proven by test/sdkparity/crd_parity_test.go.
func BuildCards(paRequired bool, questionnaireCanonical string) ([]byte, error) {
	var c card
	if paRequired {
		c = card{
			Summary:   "Prior authorization required",
			Indicator: "warning",
			Extension: cardExtension{
				SHNPARequired:          true,
				QuestionnaireCanonical: questionnaireCanonical,
			},
		}
	} else {
		c = card{
			Summary:   "No prior authorization required",
			Indicator: "info",
			Extension: cardExtension{
				SHNPARequired: false,
			},
		}
	}
	return json.Marshal(cardsResponse{Cards: []card{c}})
}

// ParseCards parses the CRD cards response: whether PA is required + the DTR
// questionnaire canonical to fetch. Reimplements internal/crd.ParseCards standalone,
// reading the first card's SHN extension (the substrate emits exactly one card). It
// errors if the response carries zero cards.
func ParseCards(data []byte) (paRequired bool, questionnaireCanonical string, err error) {
	var resp cardsResponse
	if err = json.Unmarshal(data, &resp); err != nil {
		return false, "", err
	}
	if len(resp.Cards) == 0 {
		return false, "", fmt.Errorf("shnsdk: CardsResponse must contain at least one card")
	}
	ext := resp.Cards[0].Extension
	return ext.SHNPARequired, ext.QuestionnaireCanonical, nil
}

package shnsdk

import (
	"encoding/json"
	"fmt"
)

// orderSelectContext is the CDS Hooks context for an order-select hook invocation.
// Ported standalone from internal/crd.OrderSelectContext with the SAME json tags so
// the marshaled bytes are identical (test/sdkparity/crd_parity_test.go).
type orderSelectContext struct {
	PatientID   string            `json:"patientId"`
	DraftOrders []json.RawMessage `json:"draftOrders"`
}

// orderSelectRequest is the full CDS Hooks order-select hook request payload.
// Ported standalone from internal/crd.OrderSelectRequest; the prefetch carries only
// Coverage (FR-14 minimum-necessary), exactly as the substrate emits.
type orderSelectRequest struct {
	Hook     string             `json:"hook"`
	Context  orderSelectContext `json:"context"`
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
	req := orderSelectRequest{
		Hook: "order-select",
		Context: orderSelectContext{
			PatientID:   patientID,
			DraftOrders: []json.RawMessage{json.RawMessage(serviceRequestJSON)},
		},
	}
	req.Prefetch.Coverage = json.RawMessage(coverageJSON)
	return json.Marshal(req)
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

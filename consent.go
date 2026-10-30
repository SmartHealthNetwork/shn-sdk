package shnsdk

// PurposeTreatment is the v3-ActReason purpose-of-use code for treatment (TREAT).
// It is the purpose a gateway presents to the Hub's consent service when
// re-confirming a TREAT permit before disclosing records (FR-32). PORTED from
// the consent service's treatment purpose — same literal.
const PurposeTreatment = "TREAT"

// ConsentCheckRequest is the POST /check body a gateway sends to the Hub's
// consent service: the four-way authority question (pci, purpose, custodian,
// recipient). Same json tags as the consent service's check request so the wire form
// is identical.
type ConsentCheckRequest struct {
	PCI       string `json:"pci"`
	Purpose   string `json:"purpose"`
	Custodian string `json:"custodian"`
	Recipient string `json:"recipient"`
}

// ConsentCheckResponse is the consent service's /check reply: whether the permit
// exists and, if so, the authenticated consent reference that anchors the
// disclosure's source Provenance (never the wire-supplied ref). consentRef is
// omitempty to match the consent service's check response.
type ConsentCheckResponse struct {
	Permit     bool   `json:"permit"`
	ConsentRef string `json:"consentRef,omitempty"`
}

package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

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

// Conformant Coverage + contained cms-payer Organization (CMS-0057). The system is the
// NAIC Company Code OID (urn:oid:2.16.840.1.113883.6.300), HL7's registered namespace for
// US insurance-company identifiers; value "00001" is the Da Vinci br-payer RI's first plan
// id — a synthetic demo value, not an official NAIC-assigned code. These are fixed
// (deterministic) and match the existing conformant goldens.
const (
	conformantCoverageID    = "c1"
	conformantPayerOrgID    = "cms-payer"
	conformantPayerOrgName  = "Centers for Medicare and Medicaid Services"
	conformantPayerOrgValue = "00001"
	systemNAICCompanyCode   = "urn:oid:2.16.840.1.113883.6.300"
)

// errCRDRequestNeedsPatient is the refusal of both deprecated CRD request
// builders: a CRD request's patient prefetch is the participant's own Patient
// record, and neither builder takes one.
var errCRDRequestNeedsPatient = errors.New("refused: a CRD request carries your own Patient record, " +
	"and this deprecated builder takes none and will not make one up; build the request with BuildCRDRequest from your own Patient")

// BuildConformantOrderSelectRequest builds nothing and returns an error.
//
// A CRD order-select request carries the participant's own Patient record as
// its patient prefetch. This builder takes a patient id, not a Patient, so
// the only request it could build would carry a Patient it made up (an
// id-only stub) beside a FHIR server, a user and an order id it also made up.
// From shn-sdk v0.59.0 it refuses instead, whatever its inputs. Earlier
// releases sent that request.
//
// Deprecated: use BuildCRDRequest, which takes the participant's own Patient,
// the hook the workflow fires, and the Coverage prefetch as the caller holds
// it, carries each exactly, and sends no fhirServer. From shn-sdk v0.59.0
// this builder returns an error naming BuildCRDRequest; a later release
// removes it.
func BuildConformantOrderSelectRequest(serviceRequestJSON, coverageJSON []byte, patientID string) ([]byte, error) {
	return nil, fmt.Errorf("shnsdk: BuildConformantOrderSelectRequest: %w", errCRDRequestNeedsPatient)
}

// withResourceID returns the FHIR resource JSON with its top-level "id" set to id,
// preserving every other field verbatim (re-marshalled, so member order may change).
// Deterministic. It is this client re-keying its own copy of its own record — the
// order and the Coverage it sends on the coverage check and the questionnaire request
// keep the id the record carries in the caller's system there, and travel under a
// request-specific id here so a responder that derives references from ids (a 2.2
// questionnaire responder's QR shell) gets one that is stable per request.
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
// carrying the bare memberID, US Core meta.profile — all KEPT) but additionally carries (a)
// a stable id "c1" and (b) a CONTAINED cms-payer Organization (identifier system|value taken
// from payer), with payor referencing it (#cms-payer). memberID is the member's BARE member
// number, never a "Coverage/<id>" reference — a reader that needs a reference to THIS
// resource uses its id ("c1"), and a prefixed memberID is refused rather than stamped
// (requireBareMemberID; the signature cannot express the change, both spellings are a
// string). This is the additive conformant variant of BuildCoverage (which is
// byte-parity-locked); a production-conformant CRD (CMS-0057) names the payer Organization,
// and the contained Org $validates clean. Deterministic.
func BuildCoverageWithPayer(patientRef, memberID string, payer PayerIdentifier) ([]byte, error) {
	if err := requireBareMemberID(memberID); err != nil {
		return nil, err
	}
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
			System: strPtr(systemSHNCoverage),
			Value:  strPtr(memberID),
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

// ErrBuildCardsReplaced is returned by the deprecated BuildCards and
// BuildCardsAtLine. A CRD answer returns the requested order with its
// coverage information (an update system action); a CardCoverage names no
// order, no Coverage, no assertion date and no coverage-assertion-id, so no
// conformant answer can be built from it. Build the answer with
// BuildCRDResponse from the order the request carried.
var ErrBuildCardsReplaced = errors.New("shnsdk: BuildCards no longer builds a CRD answer: " +
	"coverage information returns on the requested order; use BuildCRDResponse")

// BuildCards used to write the coverage projection into a card extension
// object, a shape no Da Vinci CRD line defines. It now builds nothing and
// returns ErrBuildCardsReplaced: it is BuildCardsAtLine("2.0", cov).
//
// Deprecated: use BuildCRDResponse with the order the request carried and
// the participant's own coverage assertion.
func BuildCards(cov CardCoverage) ([]byte, error) {
	return BuildCardsAtLine("2.0", cov)
}

// BuildCardsAtLine is BuildCards at a CRD line ("2.0", "2.1", "2.2"). An
// unknown line is refused as such; a known line returns
// ErrBuildCardsReplaced.
//
// Deprecated: use BuildCRDResponse (see BuildCards).
func BuildCardsAtLine(line string, cov CardCoverage) ([]byte, error) {
	if _, ok := CRDLineDef(line); !ok {
		return nil, fmt.Errorf("shnsdk: BuildCardsAtLine: unknown CRD line %q", line)
	}
	return nil, ErrBuildCardsReplaced
}

// legacyCardKeys are the members of the card extension object earlier
// releases of this SDK wrote (CardCoverage's JSON form).
var legacyCardKeys = map[string]bool{"covered": true, "paNeeded": true, "questionnaires": true, "satisfiedPaId": true}

// legacyCardCoverage reads a card's CDS Hooks extension object as the
// coverage object earlier releases of this SDK wrote. ok is false unless the
// object has that shape: a non-empty covered string, only that object's
// members, each of its type. Any other extension object belongs to the card's
// author and states no coverage.
func legacyCardCoverage(raw []byte) (CardCoverage, bool) {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil || members == nil {
		return CardCoverage{}, false
	}
	for k := range members {
		if !legacyCardKeys[k] {
			return CardCoverage{}, false
		}
	}
	var c CardCoverage
	if json.Unmarshal(raw, &c) != nil || c.Covered == "" {
		return CardCoverage{}, false
	}
	return c, true
}

// ParseCards returns the coverage projection of a CRD response. When the
// first card carries the coverage object earlier releases of this SDK wrote
// in the card's extension, that object is returned. Otherwise the first
// coverage information the response carries (ParseCRDResponse, then
// CRDObservation.Primary) is returned; a card extension object of any other
// shape is the card author's own and is not coverage. A response with no card
// and no coverage information is an error; a card without either still
// yields the empty projection, as it always did.
//
// Deprecated: use ParseCRDResponse, which returns every order and every
// coverage-information value, exactly as sent.
func ParseCards(data []byte) (CardCoverage, error) {
	var resp struct {
		Cards []struct {
			Summary   string          `json:"summary"`
			Indicator string          `json:"indicator"`
			Detail    string          `json:"detail"`
			Extension json.RawMessage `json:"extension"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return CardCoverage{}, err
	}
	if len(resp.Cards) > 0 {
		if cov, ok := legacyCardCoverage(resp.Cards[0].Extension); ok {
			return cov, nil
		}
	}
	if obs, err := ParseCRDResponse(data); err == nil {
		if cov, ok := obs.Primary(); ok {
			return cov, nil
		}
	}
	if len(resp.Cards) == 0 {
		return CardCoverage{}, fmt.Errorf("shnsdk: CardsResponse must contain at least one card")
	}
	return CardCoverage{}, nil
}

// StripCanonicalVersion drops a trailing |version from a FHIR canonical URL, leaving the
// bare canonical. A canonical with no version is returned unchanged.
func StripCanonicalVersion(canonical string) string {
	if i := strings.IndexByte(canonical, '|'); i >= 0 {
		return canonical[:i]
	}
	return canonical
}

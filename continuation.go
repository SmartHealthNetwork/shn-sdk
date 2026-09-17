package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// PriorAuthContinuation is what a requester keeps to follow up on a pended
// prior authorization with an inquiry. It holds metadata only: identifiers,
// codes and dates of the request the requester itself sent, and the
// identifiers the payer answered with. It holds no clinical content (no order,
// questionnaire answers or reports); an inquiry is built from it together with
// the requester's own records (PASInquiryRecords). It JSON round-trips as part
// of PriorAuthResume.
type PriorAuthContinuation struct {
	// Line is the PAS line the request was sent at.
	Line string `json:"line"`
	// PayerHolder is the payer's network holder id.
	PayerHolder string `json:"payerHolder"`
	// ClaimIdentifiers are the submitted Claim's identifiers.
	ClaimIdentifiers []PASIdentifier `json:"claimIdentifiers"`
	// ClaimType and Priority are the submitted Claim's type and priority.
	ClaimType PASCoding `json:"claimType"`
	Priority  PASCoding `json:"priority"`
	// MemberID is the member identifier the request was made for.
	MemberID string `json:"memberId"`
	// ProviderNPI is the requesting provider's NPI, when known.
	ProviderNPI string `json:"providerNpi,omitempty"`
	// Items are the submitted request lines, with the numbers the payer gave
	// them.
	Items []PASInquiryItem `json:"items"`
	// ClaimResponseIdentifiers and PreAuthRef are what the payer answered with.
	ClaimResponseIdentifiers []PASIdentifier `json:"claimResponseIdentifiers,omitempty"`
	PreAuthRef               string          `json:"preAuthRef,omitempty"`
}

// PASInquiryRecords are the requester's own records an inquiry embeds (see
// PASInquiryInputs): the Patient (carrying the member identifier), the
// Coverage, the requesting provider (Organization or PractitionerRole) and
// the payer Organization.
type PASInquiryRecords struct {
	Patient  []byte
	Coverage []byte
	Provider []byte
	Insurer  []byte
}

// NewPriorAuthContinuation records the continuation facts of a submitted PAS
// request and the payer's answer to it: the request's Claim identifiers,
// type, priority and items (sequence, product code, service date, trace
// number), and the answer's ClaimResponse identifiers, authorization
// reference and per-item authorization and administration reference numbers.
// Every request item must carry a trace number. The answer may be empty (no
// payer facts yet).
func NewPriorAuthContinuation(line, payerHolder, memberID, providerNPI string, request, response []byte) (PriorAuthContinuation, error) {
	if _, ok := PASLineDef(line); !ok {
		return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: unknown PAS line %q", line)
	}
	if payerHolder == "" || memberID == "" {
		return PriorAuthContinuation{}, errors.New("shnsdk: continuation: the payer holder and the member id are required")
	}
	c := PriorAuthContinuation{Line: line, PayerHolder: payerHolder, MemberID: memberID, ProviderNPI: providerNPI}
	claim, err := firstBundleResource(request, "Claim")
	if err != nil {
		return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: request: %w", err)
	}
	var cl struct {
		Identifier []PASIdentifier `json:"identifier"`
		Type       struct {
			Coding []PASCoding `json:"coding"`
		} `json:"type"`
		Priority struct {
			Coding []PASCoding `json:"coding"`
		} `json:"priority"`
		Item []struct {
			Sequence         int                          `json:"sequence"`
			Extension        []map[string]json.RawMessage `json:"extension"`
			ProductOrService struct {
				Coding []PASCoding `json:"coding"`
			} `json:"productOrService"`
			ServicedDate   string `json:"servicedDate"`
			ServicedPeriod struct {
				Start string `json:"start"`
			} `json:"servicedPeriod"`
		} `json:"item"`
	}
	if err := json.Unmarshal(claim, &cl); err != nil {
		return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: request Claim: %w", err)
	}
	c.ClaimIdentifiers = cl.Identifier
	if len(cl.Type.Coding) > 0 {
		c.ClaimType = cl.Type.Coding[0]
	}
	if len(cl.Priority.Coding) > 0 {
		c.Priority = cl.Priority.Coding[0]
	}
	if len(cl.Item) == 0 {
		return PriorAuthContinuation{}, errors.New("shnsdk: continuation: the request Claim has no items")
	}
	for _, it := range cl.Item {
		item := PASInquiryItem{Sequence: it.Sequence, ServiceDate: it.ServicedDate}
		if item.ServiceDate == "" && len(it.ServicedPeriod.Start) >= 10 {
			item.ServiceDate = it.ServicedPeriod.Start[:10]
		}
		if len(it.ProductOrService.Coding) > 0 {
			item.ProductOrService = it.ProductOrService.Coding[0]
		}
		for _, e := range it.Extension {
			var url string
			_ = json.Unmarshal(e["url"], &url)
			if url == pasExtItemTraceNumber && item.TraceNumber == (PASIdentifier{}) {
				_ = json.Unmarshal(e["valueIdentifier"], &item.TraceNumber)
			}
		}
		if !validIdentifier(item.TraceNumber) {
			return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: request item %d has no item trace number", it.Sequence)
		}
		c.Items = append(c.Items, item)
	}
	if len(response) > 0 {
		if err := c.Record(response); err != nil {
			return PriorAuthContinuation{}, err
		}
	}
	return c, nil
}

// Record adds the payer facts of an answer (a PAS response Bundle, a bare
// ClaimResponse, or an inquiry answer) to the continuation: ClaimResponse
// identifiers not yet held, the authorization reference, and per-item
// authorization and administration reference numbers for the continuation's
// items.
func (c *PriorAuthContinuation) Record(response []byte) error {
	responses, err := claimResponsesIn(response)
	if err != nil {
		return fmt.Errorf("shnsdk: continuation: answer: %w", err)
	}
	for _, raw := range responses {
		var cr struct {
			Identifier []PASIdentifier `json:"identifier"`
			PreAuthRef string          `json:"preAuthRef"`
			Item       []struct {
				ItemSequence int                          `json:"itemSequence"`
				Extension    []map[string]json.RawMessage `json:"extension"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw, &cr); err != nil {
			return fmt.Errorf("shnsdk: continuation: answer ClaimResponse: %w", err)
		}
		for _, id := range cr.Identifier {
			if !slices.Contains(c.ClaimResponseIdentifiers, id) {
				c.ClaimResponseIdentifiers = append(c.ClaimResponseIdentifiers, id)
			}
		}
		if cr.PreAuthRef != "" {
			c.PreAuthRef = cr.PreAuthRef
		}
		for _, it := range cr.Item {
			for i := range c.Items {
				if c.Items[i].Sequence != it.ItemSequence {
					continue
				}
				for _, e := range it.Extension {
					var url, v string
					_ = json.Unmarshal(e["url"], &url)
					_ = json.Unmarshal(e["valueString"], &v)
					switch {
					case v == "":
					case url == pasExtAuthorizationNumber:
						c.Items[i].AuthorizationNumber = v
					case url == pasExtAdministrationReferenceNumber:
						c.Items[i].AdministrationReferenceNumber = v
					}
				}
			}
		}
	}
	return nil
}

// InquiryInputs returns the inquiry for this continuation with the given
// inquiry id, identifiers, timestamp and the requester's records.
func (c PriorAuthContinuation) InquiryInputs(id string, identifier PASIdentifier, records PASInquiryRecords, timestamp time.Time) PASInquiryInputs {
	return PASInquiryInputs{
		ID:              id,
		Identifier:      identifier,
		ClaimIdentifier: identifier,
		Timestamp:       timestamp,
		ClaimType:       c.ClaimType,
		Priority:        c.Priority,
		MemberID:        c.MemberID,
		Patient:         records.Patient,
		Coverage:        records.Coverage,
		Provider:        records.Provider,
		Insurer:         records.Insurer,
		Items:           slices.Clone(c.Items),
	}
}

// matches reports whether a ClaimResponse is about this continuation's
// request: an item echoes one of its trace numbers, or the response carries
// one of its ClaimResponse identifiers, names one of its Claim identifiers as
// its request, or carries its authorization reference.
func (c PriorAuthContinuation) matches(claimResponse []byte) bool {
	var cr struct {
		Identifier []PASIdentifier `json:"identifier"`
		PreAuthRef string          `json:"preAuthRef"`
		Request    struct {
			Identifier *PASIdentifier `json:"identifier"`
		} `json:"request"`
		Item []struct {
			Extension []map[string]json.RawMessage `json:"extension"`
		} `json:"item"`
	}
	if json.Unmarshal(claimResponse, &cr) != nil {
		return false
	}
	for _, it := range cr.Item {
		for _, e := range it.Extension {
			var url string
			_ = json.Unmarshal(e["url"], &url)
			if url != pasExtItemTraceNumber {
				continue
			}
			var trace PASIdentifier
			if json.Unmarshal(e["valueIdentifier"], &trace) != nil {
				continue
			}
			for _, own := range c.Items {
				if validIdentifier(trace) && own.TraceNumber == trace {
					return true
				}
			}
		}
	}
	for _, id := range cr.Identifier {
		if validIdentifier(id) && slices.Contains(c.ClaimResponseIdentifiers, id) {
			return true
		}
	}
	if r := cr.Request.Identifier; r != nil && validIdentifier(*r) && slices.Contains(c.ClaimIdentifiers, *r) {
		return true
	}
	return c.PreAuthRef != "" && cr.PreAuthRef == c.PreAuthRef
}

// firstBundleResource returns the first entry resource of type rt in a
// Bundle.
func firstBundleResource(bundle []byte, rt string) (json.RawMessage, error) {
	var b struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return nil, err
	}
	if b.ResourceType != "Bundle" {
		return nil, fmt.Errorf("not a Bundle (%q)", b.ResourceType)
	}
	for _, e := range b.Entry {
		var h struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(e.Resource, &h) == nil && h.ResourceType == rt {
			return e.Resource, nil
		}
	}
	return nil, fmt.Errorf("no %s entry", rt)
}

// claimResponsesIn returns every ClaimResponse in a bare ClaimResponse, a
// Bundle, or a Parameters whose parameters hold Bundles (the PAS 2.2.1
// $inquire answer).
func claimResponsesIn(doc []byte) ([]json.RawMessage, error) {
	var head struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
		Parameter []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(doc, &head); err != nil {
		return nil, err
	}
	var out []json.RawMessage
	switch head.ResourceType {
	case "ClaimResponse":
		return []json.RawMessage{doc}, nil
	case "Bundle":
		for _, e := range head.Entry {
			var h struct {
				ResourceType string `json:"resourceType"`
			}
			if json.Unmarshal(e.Resource, &h) == nil && h.ResourceType == "ClaimResponse" {
				out = append(out, e.Resource)
			}
		}
	case "Parameters":
		for _, p := range head.Parameter {
			if len(p.Resource) == 0 {
				continue
			}
			inner, err := claimResponsesIn(p.Resource)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		}
	default:
		return nil, fmt.Errorf("unexpected %q", head.ResourceType)
	}
	return out, nil
}

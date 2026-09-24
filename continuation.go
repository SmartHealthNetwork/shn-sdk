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
	// AdministrationReferenceNumber is the administrative reference the payer
	// stated for the ANSWER AS A WHOLE — the ClaimResponse's own extension,
	// beside the per-item ones in Items.
	//
	// It is recorded and NOT sent as a narrowing fact on the inquiry, and the
	// difference is measured rather than chosen. A payer states this reference
	// while it holds the request and withdraws it once it decides: the pinned
	// reference payer drops the root reference when no item remains pended, and
	// answers an inquiry narrowed by it only while the pend stands. An inquiry
	// that went on naming it would stop matching at exactly the moment the
	// decision arrived — the one moment the requester is asking for. So the
	// continuation keeps what the payer said, and asks by the facts that outlive
	// the pend: the item trace numbers, the claim identifiers, the parties.
	AdministrationReferenceNumber string `json:"administrationReferenceNumber,omitempty"`
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
// type, priority, requesting-provider identifier and items (sequence, product
// code, service date, trace number), and the answer's ClaimResponse
// identifiers, authorization reference and per-item authorization and
// administration reference numbers. Every request item must carry a trace
// number. The answer may be empty (no payer facts yet).
//
// The provider identifier is read off the SUBMITTED BUNDLE — the Claim's own
// provider reference, resolved to the entry that rode with it — like every
// other fact here. A continuation derived a second way from the order the
// request was built from could record one party while the wire named another.
func NewPriorAuthContinuation(line, payerHolder, memberID string, request, response []byte) (PriorAuthContinuation, error) {
	if _, ok := PASLineDef(line); !ok {
		return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: unknown PAS line %q", line)
	}
	if payerHolder == "" || memberID == "" {
		return PriorAuthContinuation{}, errors.New("shnsdk: continuation: the payer holder and the member id are required")
	}
	c := PriorAuthContinuation{Line: line, PayerHolder: payerHolder, MemberID: memberID}
	claim, err := firstBundleResource(request, "Claim")
	if err != nil {
		return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: request: %w", err)
	}
	if c.ProviderNPI, err = submittedProviderNPI(request); err != nil {
		return PriorAuthContinuation{}, fmt.Errorf("shnsdk: continuation: request: %w", err)
	}
	var cl struct {
		Identifier []PASIdentifier `json:"identifier"`
		Related    []struct {
			Claim struct {
				Identifier *PASIdentifier `json:"identifier"`
			} `json:"claim"`
		} `json:"related"`
		Type struct {
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
	// A request that AMENDS an earlier one names it (Claim.related[].claim), and
	// that name is one of this authorization's own: the payer's answers go on
	// referring to the request it first stored, so a continuation that knew only
	// the amendment's identifier could not recognize a decision about the thing it
	// is a continuation OF. Read off the bytes actually sent, like every other fact
	// here — never carried in from a caller's memory of the earlier request.
	for _, rel := range cl.Related {
		if id := rel.Claim.Identifier; id != nil && validIdentifier(*id) && !slices.Contains(c.ClaimIdentifiers, *id) {
			c.ClaimIdentifiers = append(c.ClaimIdentifiers, *id)
		}
	}
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
// identifiers not yet held, the authorization reference, the answer's own
// administrative reference number, and per-item authorization and
// administration reference numbers for the continuation's items.
//
// Administrative reference numbers are read at BOTH levels because payers
// state them at both: the pinned reference payer puts one on the ClaimResponse
// itself and none on its items, so reading only the items recorded none.
func (c *PriorAuthContinuation) Record(response []byte) error {
	responses, err := claimResponsesIn(response)
	if err != nil {
		return fmt.Errorf("shnsdk: continuation: answer: %w", err)
	}
	for _, raw := range responses {
		var cr struct {
			Identifier []PASIdentifier              `json:"identifier"`
			PreAuthRef string                       `json:"preAuthRef"`
			Extension  []map[string]json.RawMessage `json:"extension"`
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
		// The answer's OWN administrative reference, which this payer states at
		// the root rather than on the items. Reading only the item-level ones
		// recorded nothing at all for every answer the pinned reference payer
		// sends, which is every answer this network has measured.
		for _, e := range cr.Extension {
			var url, v string
			_ = json.Unmarshal(e["url"], &url)
			_ = json.Unmarshal(e["valueString"], &v)
			if url == pasExtAdministrationReferenceNumber && v != "" {
				c.AdministrationReferenceNumber = v
			}
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
//
// It asks by the facts that OUTLIVE the pend — the item trace numbers the payer
// echoes on every answer it sends about this request, the authorization numbers
// once there are any, the parties and the member — and not by the administration
// reference numbers the payer stated while it was holding the request.
//
// That last part is measured, not chosen. A payer states an administration
// reference for a pend and withdraws it when it decides: the pinned reference
// payer answers an inquiry narrowed by one WHILE the pend stands and stops
// matching it once the claim resolves, because the resolved answer no longer
// states it. An inquiry that went on naming it would therefore go blind at exactly
// the moment the decision arrived — the one moment the requester is asking for.
// Record has them (Items[i].AdministrationReferenceNumber and
// AdministrationReferenceNumber), so the requester keeps what the payer said; a
// caller that wants to ask by one anyway can still set it on the item it builds.
func (c PriorAuthContinuation) InquiryInputs(id string, identifier PASIdentifier, records PASInquiryRecords, timestamp time.Time) PASInquiryInputs {
	items := slices.Clone(c.Items)
	for i := range items {
		items[i].AdministrationReferenceNumber = ""
	}
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
		Items:           items,
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

// submittedProviderNPI reads the requesting provider's NPI out of the request
// that was actually sent: the Claim's provider reference, resolved against the
// entries it rode with (by their fullUrls and by each entry's relative
// identity), then the NPI that record carries.
//
// A request whose Claim names a provider the Bundle does not resolve is a
// refusal, not an empty identifier: the payer stored a party this requester
// cannot name again, and recording nothing would hide that until an inquiry
// silently matched nothing. A resolved provider carrying NO NPI records none —
// that is a fact about the participant's record, not a failure to read it.
func submittedProviderNPI(request []byte) (string, error) {
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(request, &b); err != nil {
		return "", err
	}
	byRef := map[string]json.RawMessage{}
	ref := ""
	for _, e := range b.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Provider     struct {
				Reference string `json:"reference"`
			} `json:"provider"`
		}
		if json.Unmarshal(e.Resource, &r) != nil {
			continue
		}
		if e.FullURL != "" {
			byRef[e.FullURL] = e.Resource
		}
		if r.ResourceType != "" && r.ID != "" {
			byRef[r.ResourceType+"/"+r.ID] = e.Resource
		}
		if r.ResourceType == "Claim" && ref == "" {
			ref = r.Provider.Reference
		}
	}
	if ref == "" {
		return "", errors.New("the Claim names no requesting provider by reference")
	}
	resource, ok := byRef[ref]
	if !ok {
		return "", fmt.Errorf("the Claim names provider %q, which the request does not resolve", ref)
	}
	return PASProviderNPI(resource), nil
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

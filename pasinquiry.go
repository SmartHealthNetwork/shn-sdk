package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The Da Vinci PAS inquiry request (Claim/$inquire).
//
// Facts from the published PAS packages (2.0.1, 2.1.0, 2.2.1;
// OperationDefinition-Claim-inquiry, StructureDefinition-profile-pas-inquiry-
// request-bundle, -profile-claim-inquiry, -profile-beneficiary,
// -profile-subscriber and the itemTraceNumber, authorizationNumber and
// administrationReferenceNumber extensions):
//
//   - The request is a collection Bundle with identifier and timestamp 1..1,
//     entry.fullUrl and entry.resource 1..1, entry.request, entry.response
//     and entry.search 0..0, and exactly one Claim entry
//     (profile-claim-inquiry); at 2.2.1 the Claim is the first entry.
//   - The inquiry Claim requires status (active), type, use
//     (preauthorization), patient, created, insurer, provider (a PAS
//     Requestor Organization or PractitionerRole), priority and insurance
//     (sequence, focal = true, coverage). Claim.identifier is 0..1 at 2.0.1
//     and 1..1 at 2.1.0 and 2.2.1, where it is the inquiry's own trace number,
//     not the identifier of the authorization asked about. Claim.item is
//     1..* at 2.0.1 and 0..* later.
//   - Claim.item.extension:itemTraceNumber identifies each asked-about item.
//     authorizationNumber and administrationReferenceNumber are item
//     extensions at every line. PAS 2.2.1 also declares Claim-level slices for
//     them, but both extension definitions allow only item contexts
//     (Claim.item, ClaimResponse.item, ClaimResponse.addItem,
//     ExplanationOfBenefit.item) and validators enforce the context, so this
//     builder writes them on the item at every line.
//   - The payer matches on the member (or subscriber) identifier plus the
//     provider identifier. Patient.identifier is 1..* with system and value
//     1..1 at every line; PAS 2.1.0 adds a memberIdentifier slice typed MB
//     (http://terminology.hl7.org/CodeSystem/v2-0203), which 2.0.1 and 2.2.1
//     do not define.
//   - The answer is a Bundle (2.0.1, 2.1.0: $inquire return 1..1) or a
//     Parameters holding return Bundles (2.2.1: return 0..*), each with
//     ClaimResponse entries.

// PASInquiryRequestBundleProfile is the Da Vinci PAS inquiry request Bundle
// profile the inquiry builder's output conforms to.
const PASInquiryRequestBundleProfile = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-pas-inquiry-request-bundle"

const (
	pasExtAuthorizationNumber           = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-authorizationNumber"
	pasExtAdministrationReferenceNumber = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-administrationReferenceNumber"
	pasSystemV20203                     = "http://terminology.hl7.org/CodeSystem/v2-0203"
)

// PASInquiryItem is one request line an inquiry asks about.
type PASInquiryItem struct {
	// Sequence is the item sequence of the request being asked about.
	Sequence int `json:"sequence"`
	// ProductOrService is the requested service's code.
	ProductOrService PASCoding `json:"productOrService"`
	// ServiceDate is the requested service date (YYYY-MM-DD), when the
	// request carried one.
	ServiceDate string `json:"serviceDate,omitempty"`
	// TraceNumber is the item trace number the request carried.
	TraceNumber PASIdentifier `json:"traceNumber"`
	// AuthorizationNumber and AdministrationReferenceNumber are the numbers
	// the payer gave this item, when it gave them.
	AuthorizationNumber           string `json:"authorizationNumber,omitempty"`
	AdministrationReferenceNumber string `json:"administrationReferenceNumber,omitempty"`
}

// PASInquiryInputs are a provider participant's prior-authorization inquiry.
// Resources travel as the participant's exact bytes; every one needs an id.
type PASInquiryInputs struct {
	// ID is the inquiry Claim's id.
	ID string
	// Identifier is the inquiry's own transaction identifier
	// (Bundle.identifier). Required.
	Identifier PASIdentifier
	// ClaimIdentifier is the inquiry Claim's identifier, the inquiry's own
	// trace number. Required at PAS 2.1 and 2.2, optional at 2.0.
	ClaimIdentifier PASIdentifier
	// Timestamp is the Bundle timestamp and Claim.created. Required.
	Timestamp time.Time
	// ClaimType and Priority are the Claim.type and Claim.priority codings
	// (the ones the request being asked about carried). Required: a type
	// system and code, and a priority code.
	ClaimType PASCoding
	Priority  PASCoding
	// MemberID is the member identifier value the Patient carries. Required.
	MemberID string
	// Patient is the patient's record. It must carry an identifier with a
	// system and the value MemberID, typed MB (v2-0203) — what a payer matches
	// an inquiry on, and what the submit path already puts on the request.
	Patient []byte
	// Coverage is the patient's Coverage (its beneficiary is Patient).
	Coverage []byte
	// Provider is the requesting provider: an Organization or a
	// PractitionerRole.
	Provider []byte
	// Insurer is the payer Organization.
	Insurer []byte
	// Items are the request lines asked about (at least one).
	Items []PASInquiryItem
}

// PASInquiryRequest is an inquiry built by BuildPASInquiryBundle.
type PASInquiryRequest struct {
	// Body is the inquiry request Bundle.
	Body []byte
	// Copied lists every resource copied unchanged from the inputs.
	Copied []PASInquiryCopiedSpan
}

// PASInquiryCopiedSpan says that Body[At:At+(End-Start)] is a copy of bytes
// [Start, End) of the input named by Input ("Patient", "Coverage",
// "Provider" or "Insurer").
type PASInquiryCopiedSpan struct {
	Input      string
	Start, End int
	At         int
}

var fhirDatePattern = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)

// BuildPASInquiryBundle builds the Da Vinci PAS inquiry request Bundle at a
// PAS line ("2.0", "2.1", "2.2"): the inquiry Claim first, then the Patient,
// Coverage, Provider and Insurer entries, each embedded as the caller's own
// bytes. Each item carries its trace number and, when the payer gave them,
// its authorization and administration reference numbers (item extensions at
// every line). No entry carries a request, response or search element.
//
// Refusals (an error, never a partial inquiry): an unknown line; a missing
// required input (see PASInquiryInputs); a Patient without the member
// identifier (with a system, typed MB); a resource that is not one JSON object
// of the expected type with an id; a Coverage for another patient; a Provider
// that is not an Organization or PractitionerRole; an item without a positive
// sequence, a product code, or a trace number, or with a malformed service
// date; repeated item sequences.
func BuildPASInquiryBundle(line string, in PASInquiryInputs) (PASInquiryRequest, error) {
	out, err := buildPASInquiryBundle(line, in)
	if err != nil {
		return PASInquiryRequest{}, fmt.Errorf("shnsdk: BuildPASInquiryBundle: %w", err)
	}
	return out, nil
}

// inquiryResource is a checked input resource.
type inquiryResource struct {
	name       string
	raw        []byte
	start, end int
	fullURL    string
}

func inquiryInput(name string, raw []byte, types ...string) (inquiryResource, map[string]json.RawMessage, error) {
	s, e, d, err := pkgResource(name, raw)
	if err != nil {
		return inquiryResource{}, nil, err
	}
	rt := docString(d, d.Root(), "resourceType")
	if !slices.Contains(types, rt) {
		return inquiryResource{}, nil, fmt.Errorf("%s is a %q, not %s", name, rt, strings.Join(types, " or "))
	}
	id := docString(d, d.Root(), "id")
	if !fhirIDPattern.MatchString(id) {
		return inquiryResource{}, nil, fmt.Errorf("%s has no valid id", name)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw[s:e], &m); err != nil {
		return inquiryResource{}, nil, fmt.Errorf("%s: %w", name, err)
	}
	return inquiryResource{name: name, raw: raw, start: s, end: e, fullURL: pasBundleBaseURL + "/" + rt + "/" + id}, m, nil
}

// memberIdentifierPresent reports whether a Patient carries an identifier
// with a system and the member id (typed MB when typed is set).
func memberIdentifierPresent(patient map[string]json.RawMessage, memberID string, typed bool) bool {
	var ids []struct {
		System string `json:"system"`
		Value  string `json:"value"`
		Type   struct {
			Coding []PASCoding `json:"coding"`
		} `json:"type"`
	}
	_ = json.Unmarshal(patient["identifier"], &ids)
	for _, id := range ids {
		if id.Value != memberID || strings.TrimSpace(id.System) == "" {
			continue
		}
		if !typed {
			return true
		}
		for _, c := range id.Type.Coding {
			if c.System == pasSystemV20203 && c.Code == "MB" {
				return true
			}
		}
	}
	return false
}

func validCoding(c PASCoding) bool {
	return strings.TrimSpace(c.System) != "" && strings.TrimSpace(c.Code) != ""
}

func buildPASInquiryBundle(line string, in PASInquiryInputs) (PASInquiryRequest, error) {
	if _, ok := PASLineDef(line); !ok {
		return PASInquiryRequest{}, fmt.Errorf("unknown PAS line %q", line)
	}
	switch {
	case !fhirIDPattern.MatchString(in.ID):
		return PASInquiryRequest{}, fmt.Errorf("inquiry Claim id %q is not a FHIR id", in.ID)
	case !validIdentifier(in.Identifier):
		return PASInquiryRequest{}, errors.New("the inquiry identifier (system and value) is required")
	case line != "2.0" && !validIdentifier(in.ClaimIdentifier):
		return PASInquiryRequest{}, fmt.Errorf("the inquiry Claim identifier (system and value) is required at PAS %s", line)
	case in.ClaimIdentifier != (PASIdentifier{}) && !validIdentifier(in.ClaimIdentifier):
		return PASInquiryRequest{}, errors.New("the inquiry Claim identifier needs a system and a value")
	case in.Timestamp.IsZero():
		return PASInquiryRequest{}, errors.New("the inquiry timestamp is required")
	case !validCoding(in.ClaimType):
		return PASInquiryRequest{}, errors.New("the Claim type coding is required")
	case strings.TrimSpace(in.Priority.Code) == "":
		return PASInquiryRequest{}, errors.New("the Claim priority code is required")
	case strings.TrimSpace(in.MemberID) == "":
		return PASInquiryRequest{}, errors.New("the member identifier is required: a payer matches an inquiry on it")
	case len(in.Items) == 0:
		return PASInquiryRequest{}, errors.New("an inquiry names at least one item")
	}

	patient, pm, err := inquiryInput("Patient", in.Patient, "Patient")
	if err != nil {
		return PASInquiryRequest{}, err
	}
	// The member identifier must be typed MB at EVERY line, not only where 2.1.0
	// slices it. That is the payer's rule, measured: it refuses an inquiry whose
	// Patient carries an untyped member identifier at 2.0 as well
	// ("Patient member identifier (type=MB) is required for inquiry").
	//
	// It is also the rule the SUBMIT path already follows — the request it builds
	// identifies the member with the MB type (pasMemberPatient), and its guard
	// checkPASMemberIdentified refuses anything less. A submission and the inquiry
	// about it treating one record differently is how an authorization gets stored
	// under a member no inquiry can name, so the two paths ask the same thing of the
	// same record. This builder sends the participant's own bytes and will not
	// rewrite them, so it refuses here rather than quietly typing the identifier
	// itself: what a payer matches on has to be in the record.
	if !memberIdentifierPresent(pm, in.MemberID, true) {
		return PASInquiryRequest{}, errors.New("the Patient carries no member identifier typed MB (v2-0203) with a system and the member id, and a payer matches an inquiry on it")
	}
	coverage, cm, err := inquiryInput("Coverage", in.Coverage, "Coverage")
	if err != nil {
		return PASInquiryRequest{}, err
	}
	var beneficiary struct {
		Reference string `json:"reference"`
	}
	_ = json.Unmarshal(cm["beneficiary"], &beneficiary)
	var patientID string
	_ = json.Unmarshal(pm["id"], &patientID)
	if patientKey(beneficiary.Reference) != "Patient/"+patientID {
		return PASInquiryRequest{}, fmt.Errorf("the Coverage beneficiary %q is not the Patient", beneficiary.Reference)
	}
	// The same set the submit builder checks, from the same one place: a request
	// and the inquiry about it can never name parties of different types.
	provider, _, err := inquiryInput("Provider", in.Provider, PASProviderTypes...)
	if err != nil {
		return PASInquiryRequest{}, err
	}
	insurer, _, err := inquiryInput("Insurer", in.Insurer, "Organization")
	if err != nil {
		return PASInquiryRequest{}, err
	}

	if provider.fullURL == insurer.fullURL {
		return PASInquiryRequest{}, errors.New("the Provider and the Insurer are the same resource")
	}

	items := slices.Clone(in.Items)
	slices.SortStableFunc(items, func(a, b PASInquiryItem) int { return a.Sequence - b.Sequence })
	for i, it := range items {
		switch {
		case it.Sequence < 1:
			return PASInquiryRequest{}, fmt.Errorf("item %d: sequence %d is below 1", i, it.Sequence)
		case i > 0 && items[i-1].Sequence == it.Sequence:
			return PASInquiryRequest{}, fmt.Errorf("item sequence %d is listed twice", it.Sequence)
		case !validCoding(it.ProductOrService):
			return PASInquiryRequest{}, fmt.Errorf("item %d: the product or service coding is required", it.Sequence)
		case !validIdentifier(it.TraceNumber):
			return PASInquiryRequest{}, fmt.Errorf("item %d: the item trace number (system and value) is required", it.Sequence)
		case it.ServiceDate != "" && !fhirDatePattern.MatchString(it.ServiceDate):
			return PASInquiryRequest{}, fmt.Errorf("item %d: service date %q is not a FHIR date", it.Sequence, it.ServiceDate)
		}
	}

	var w jsonWriter
	coding := func(c PASCoding) {
		w.raw(`{"coding":[{`)
		if c.System != "" {
			w.raw(`"system":`)
			w.str(c.System)
			w.raw(`,`)
		}
		w.raw(`"code":`)
		w.str(c.Code)
		if c.Display != "" {
			w.raw(`,"display":`)
			w.str(c.Display)
		}
		w.raw(`}]}`)
	}
	identifier := func(id PASIdentifier) {
		w.raw(`{"system":`)
		w.str(id.System)
		w.raw(`,"value":`)
		w.str(id.Value)
		w.raw(`}`)
	}
	stringExt := func(url, v string) {
		w.raw(`{"url":`)
		w.str(url)
		w.raw(`,"valueString":`)
		w.str(v)
		w.raw(`}`)
	}
	ref := func(key, url string) {
		w.raw(`,"` + key + `":{"reference":`)
		w.str(url)
		w.raw(`}`)
	}

	claimURL := pasBundleBaseURL + "/Claim/" + in.ID
	w.raw(`{"resourceType":"Bundle","identifier":`)
	identifier(in.Identifier)
	w.raw(`,"type":"collection","timestamp":`)
	w.str(in.Timestamp.UTC().Format(time.RFC3339))
	w.raw(`,"entry":[{"fullUrl":`)
	w.str(claimURL)
	w.raw(`,"resource":{"resourceType":"Claim","id":`)
	w.str(in.ID)
	if validIdentifier(in.ClaimIdentifier) {
		w.raw(`,"identifier":[`)
		identifier(in.ClaimIdentifier)
		w.raw(`]`)
	}
	w.raw(`,"status":"active","type":`)
	coding(in.ClaimType)
	w.raw(`,"use":"preauthorization"`)
	ref("patient", patient.fullURL)
	w.raw(`,"created":`)
	w.str(in.Timestamp.UTC().Format(time.RFC3339))
	ref("insurer", insurer.fullURL)
	ref("provider", provider.fullURL)
	w.raw(`,"priority":`)
	coding(in.Priority)
	w.raw(`,"insurance":[{"sequence":1,"focal":true,"coverage":{"reference":`)
	w.str(coverage.fullURL)
	w.raw(`}}],"item":[`)
	for i, it := range items {
		if i > 0 {
			w.raw(",")
		}
		w.raw(`{"extension":[{"url":`)
		w.str(pasExtItemTraceNumber)
		w.raw(`,"valueIdentifier":`)
		identifier(it.TraceNumber)
		w.raw(`}`)
		if it.AuthorizationNumber != "" {
			w.raw(",")
			stringExt(pasExtAuthorizationNumber, it.AuthorizationNumber)
		}
		if it.AdministrationReferenceNumber != "" {
			w.raw(",")
			stringExt(pasExtAdministrationReferenceNumber, it.AdministrationReferenceNumber)
		}
		w.raw(fmt.Sprintf(`],"sequence":%d,"productOrService":`, it.Sequence))
		coding(it.ProductOrService)
		if it.ServiceDate != "" {
			w.raw(`,"servicedDate":`)
			w.str(it.ServiceDate)
		}
		w.raw(`}`)
	}
	w.raw(`]}}`)

	var copied []PASInquiryCopiedSpan
	for _, r := range []inquiryResource{patient, coverage, provider, insurer} {
		w.raw(`,{"fullUrl":`)
		w.str(r.fullURL)
		w.raw(`,"resource":`)
		copied = append(copied, PASInquiryCopiedSpan{Input: r.name, Start: r.start, End: r.end, At: w.b.Len()})
		w.bytes(r.raw[r.start:r.end])
		w.raw(`}`)
	}
	w.raw(`]}`)
	return PASInquiryRequest{Body: w.b.Bytes(), Copied: copied}, nil
}

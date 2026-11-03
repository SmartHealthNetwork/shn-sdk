package shnsdk

import (
	"bytes"
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
	// trace number. Required at PAS 2.1 and 2.2, optional at 2.0 (PAS 2.0.1's
	// inquiry profile allows it to be absent). Send it at 2.0 too whenever your
	// system has one: a payer may require it, and the Da Vinci 2.0 reference
	// payer refuses an inquiry without it.
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
	// CoverageParties are the participant's own Patient records of the people
	// the Coverage names as subscriber or policyHolder who are not the member
	// (a dependent's parent, the PAS IG's subscriber), read from its own
	// system: the records ConformantClaimInputs.CoverageParties gave the
	// submission. Each person a subscriber or policyHolder names by a literal
	// reference to another Patient record rides the inquiry as a Patient entry
	// of its own, carried as the record's own bytes, at the fullUrl the slot's
	// own reference resolves to (BundleReferenceURL): the inquiry carries the
	// Coverage as your own bytes, so its slot names that entry as it stands,
	// and the person is the Coverage's party exactly by CoverageEntryParties'
	// rule. A Coverage naming such a person whose record is not here, or that
	// cannot ride, is refused with ErrPASCoveragePartyNotCarried: as the
	// submission refuses it (among other reasons, the record contains other
	// resources, makes a reference, or is another version than the slot names;
	// or another element of the inquiry names it too), and, since nothing in
	// the Coverage is re-pointed, when the slot's reference resolves to no
	// entry, one person is named by references resolving to different URLs, the
	// Coverage's beneficiary does not resolve to the Patient's entry (or names
	// another version of it), or a subscriber, policyHolder or beneficiary
	// spells its reference member in another case where, read leniently, a
	// slot names a Patient other than the member. A Coverage naming no one
	// beside the member builds as before. A slot's other members are carried
	// as the record has them, as the rest of the Coverage is. Records the
	// Coverage does not name are not carried.
	// Optional: a member who is their own subscriber needs none.
	CoverageParties [][]byte
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
// "Provider" or "Insurer", or "CoverageParties[i]" for the i-th of
// PASInquiryInputs.CoverageParties).
type PASInquiryCopiedSpan struct {
	Input      string
	Start, End int
	At         int
}

var fhirDatePattern = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)

// BuildPASInquiryBundle builds the Da Vinci PAS inquiry request Bundle at a
// PAS line ("2.0", "2.1", "2.2"): the inquiry Claim first, then the Patient,
// Coverage, the Coverage's parties (PASInquiryInputs.CoverageParties),
// Provider and Insurer entries, each embedded as the caller's own bytes. Each item carries its trace number and, when the payer gave them,
// its authorization and administration reference numbers (item extensions at
// every line). No entry carries a request, response or search element.
//
// Refusals (an error, never a partial inquiry): an unknown line; a missing
// required input (see PASInquiryInputs); a Patient without the member
// identifier (with a system, typed MB); a resource that is not one JSON object
// of the expected type with an id; a Coverage for another patient; a Provider
// that is not an Organization or PractitionerRole; a person the Coverage names
// beside the member who cannot ride the inquiry (ErrPASCoveragePartyNotCarried,
// see PASInquiryInputs.CoverageParties); an item without a positive
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
	// The people the Coverage names beside the member, by the submit builder's
	// own rule, so the inquiry carries what the submission carried about the
	// same Coverage.
	var patientMeta struct {
		VersionID string `json:"versionId"`
	}
	_ = json.Unmarshal(pm["meta"], &patientMeta)
	parties, err := inquiryCoverageParties(in.Coverage[coverage.start:coverage.end], cm, coverage.fullURL, patient.fullURL, patientMeta.VersionID, in.CoverageParties)
	if err != nil {
		return PASInquiryRequest{}, err
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
	entries := []inquiryResource{patient, coverage}
	for _, p := range parties {
		entries = append(entries, p.resource)
	}
	entries = append(entries, provider, insurer)
	for _, r := range entries {
		w.raw(`,{"fullUrl":`)
		w.str(r.fullURL)
		w.raw(`,"resource":`)
		copied = append(copied, PASInquiryCopiedSpan{Input: r.name, Start: r.start, End: r.end, At: w.b.Len()})
		w.bytes(r.raw[r.start:r.end])
		w.raw(`}`)
	}
	w.raw(`]}`)
	body := w.b.Bytes()
	if len(parties) > 0 {
		// As the submission confirms its own (checkPASCoverageEntryParties):
		// each person carried beside the member is the Coverage's party
		// exactly, at the entry it rides as, and named by nothing else.
		var b map[string]any
		if err := json.Unmarshal(body, &b); err != nil {
			return PASInquiryRequest{}, fmt.Errorf("read the built inquiry: %w", err)
		}
		named := CoverageEntryParties(b)
		for _, p := range parties {
			if !named[p.resource.fullURL] {
				return PASInquiryRequest{}, fmt.Errorf("%w: the Patient %s the Coverage names beside the member is not its party alone in the built inquiry (another element names it, or it shares an entry's fullUrl)", ErrPASCoveragePartyNotCarried, p.resource.fullURL)
			}
		}
	}
	return PASInquiryRequest{Body: body, Copied: copied}, nil
}

// inquiryParty is a person the Coverage names beside the member, ready to ride
// the inquiry: the record prepared by the submission's rule, and the supplied
// input it is embedded from.
type inquiryParty struct {
	party    pasEntryParty
	resource inquiryResource
}

// inquiryCoverageParties is the submit builder's party rule (pasCoverageEntry)
// for an inquiry, which carries the Coverage as the participant's own bytes and
// so leaves its slots as they are: each subscriber or policyHolder naming, by a
// literal reference, a Patient other than the member is the Coverage's party,
// carried from the records supplied, as a Patient entry whose fullUrl is the
// one the slot's own reference resolves to from the Coverage's entry
// (BundleReferenceURL), so the Coverage names it as its party
// (CoverageEntryParties) without a byte of it changed. A slot naming the
// member, a contained party (which rides inside the Coverage), or no Patient
// at all carries no one, as before; a slot's other members are carried as the
// record has them, as the rest of the Coverage is. Every way a party cannot
// ride is ErrPASCoveragePartyNotCarried: the submission's own
// (pasCoverageSlotParty, pasCoverageSlotVersion), and, because nothing in the
// Coverage is re-pointed, a slot whose reference resolves to no entry, one
// person named by references that resolve to different entries, a
// beneficiary that does not resolve to the member's entry or names another
// version of it, and a slot or the beneficiary spelling its reference member in
// another case where, read leniently, a slot names a Patient other than the
// member.
func inquiryCoverageParties(coverage []byte, cm map[string]json.RawMessage, coverageURL, patientURL, patientVersion string, supplied [][]byte) ([]inquiryParty, error) {
	_, contained, _, err := coverageParties(coverage)
	if err != nil {
		return nil, err
	}

	var head struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(coverage, &head)
	from := map[string]any{"fullUrl": coverageURL, "resource": map[string]any{"resourceType": "Coverage", "id": head.ID}}
	beneficiaryRef := coverageReferenceOf(cm["beneficiary"])
	beneficiaryKey := patientKey(beneficiaryRef)
	var out []inquiryParty
	for _, field := range pasCoverageSubjectReferences {
		raw, ok := cm[field]
		if !ok || len(raw) == 0 {
			continue
		}
		if _, ok := coveragePartyNamed(raw, contained); ok {
			continue
		}
		ref := coverageReferenceOf(raw)
		// Whether a slot names a person beside the member is read from its
		// reference member, and the beneficiary's, by their exact names, as
		// the submission reads them. A slot spelling its reference member in
		// another case cannot be read, and neither can a beneficiary so spelled
		// when the slot names another Patient than the one it seems to: each
		// could hide a person the inquiry would leave out, so it is refused, as
		// the submission refuses such a Coverage.
		lenientBeneficiary := patientKey(anyCaseReference(cm["beneficiary"]))
		if lenientRef := anyCaseReference(raw); lenientRef != ref {
			// Read leniently, the slot names the member or no Patient: no one
			// beside the member could hide behind it, so it builds as before.
			if k := patientKey(lenientRef); k == "" || k == lenientBeneficiary {
				continue
			}
			return nil, fmt.Errorf("%w: your Coverage record's %s spells its reference member in another case, and could name a person beside the member (%s) this inquiry would leave out", ErrPASCoveragePartyNotCarried, field, patientKey(lenientRef))
		}
		slotKey := patientKey(ref)
		if slotKey == "" {
			continue
		}
		if lenient := lenientBeneficiary; lenient != beneficiaryKey && slotKey != lenient {
			return nil, fmt.Errorf("%w: your Coverage record's beneficiary spells its reference member in another case, and its %s names %s, so whether that is the member cannot be read exactly", ErrPASCoveragePartyNotCarried, field, slotKey)
		}
		if beneficiaryKey == "" || slotKey == beneficiaryKey {
			continue
		}
		notCarried := func(format string, args ...any) error {
			return fmt.Errorf("%w: your Coverage record's %s names %s, %s", ErrPASCoveragePartyNotCarried, field, slotKey, fmt.Sprintf(format, args...))
		}
		target := BundleReferenceURL(from, ref)
		if target == "" || !strings.HasSuffix(target, "/"+slotKey) ||
			!(strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "urn:")) {
			return nil, notCarried("by a reference (%q) that resolves to no entry this inquiry can carry: the inquiry carries your Coverage as its own bytes, so its reference must name the parent's entry as it stands", ref)
		}
		if BundleReferenceURL(from, beneficiaryRef) != patientURL {
			return nil, notCarried("and its beneficiary (%q) does not resolve to the member's entry in this inquiry, so the person beside the member cannot ride as the Coverage's party: the inquiry carries your Coverage as its own bytes", beneficiaryRef)
		}
		if i := strings.LastIndex(beneficiaryRef, "/_history/"); i >= 0 && beneficiaryRef[i+len("/_history/"):] != patientVersion {
			return nil, notCarried("and its beneficiary (%q) names another version of the member's record than the Patient carried, so the person beside the member cannot ride as the Coverage's party", beneficiaryRef)
		}
		at := slices.IndexFunc(out, func(p inquiryParty) bool { return "Patient/"+p.party.id == slotKey })
		if at < 0 {
			party, err := pasCoverageSlotParty(field, slotKey, supplied)
			if err != nil {
				return nil, err
			}
			i := slices.IndexFunc(supplied, func(rec []byte) bool { return bytes.Equal(rec, party.record) })
			res, _, err := inquiryInput(fmt.Sprintf("CoverageParties[%d]", i), supplied[i], "Patient")
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrPASCoveragePartyNotCarried, err)
			}
			res.fullURL = target
			out = append(out, inquiryParty{party: party, resource: res})
			at = len(out) - 1
		} else if out[at].resource.fullURL != target {
			return nil, notCarried("by a reference (%q) that resolves to another entry than the one its other slot names (%s): one record rides as one entry", ref, out[at].resource.fullURL)
		}
		if err := pasCoverageSlotVersion(field, ref, out[at].party); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// anyCaseReference is the string a Reference element's reference member holds,
// whatever case its name is spelled in, or "" when it has none: what a reader
// folding member names would take the element to name.
func anyCaseReference(raw json.RawMessage) string {
	var elem map[string]json.RawMessage
	if json.Unmarshal(raw, &elem) != nil {
		return ""
	}
	if v, ok := elem["reference"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		return s
	}
	for k, v := range elem {
		if strings.EqualFold(k, "reference") {
			var s string
			_ = json.Unmarshal(v, &s)
			return s
		}
	}
	return ""
}

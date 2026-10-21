package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SmartHealthNetwork/shn-sdk/internal/exactdecode"
)

// The input of CoverageEligibilityRequest/$submit, the base R4 operation a
// system submits coverage eligibility requests with (IHE FAIS ITI-124), read
// as the requests it submits, as a payer's Smart Gateway reads it.
//
// The operation's input is one request, or a Bundle: one request with the
// resources it references (IHE FAIS's CoverageEligibilityRequest Bundle), or a
// batch of requests, each one alone or in such a Bundle of its own. The input
// is the request or Bundle itself, or a Parameters whose one `resource`
// parameter holds it.

const resourceEligibilityRequest = "CoverageEligibilityRequest"

// maxEligibilityBatch is the most coverage eligibility requests one input
// submits: the exchange's own patient and the 16 other patients the network's
// Hub records one exchange under.
const maxEligibilityBatch = 17

// EligibilitySubmission is one coverage eligibility request an input of
// CoverageEligibilityRequest/$submit submits: the request's exact bytes, and
// the member its patient names.
type EligibilitySubmission struct {
	Request []byte
	Member  string
}

// EligibilityBatchTooLarge is an input submitting more coverage eligibility
// requests than one exchange can be recorded under. Its Error is the text a
// payer's Smart Gateway answers the input with, with status 413.
type EligibilityBatchTooLarge struct{ Requests int }

func (e *EligibilityBatchTooLarge) Error() string {
	return fmt.Sprintf("the batch carries %d coverage eligibility requests; a gateway reads at most %d in one batch, so send them in batches of at most %d", e.Requests, maxEligibilityBatch, maxEligibilityBatch)
}

// ParseEligibilitySubmission is the requests an input of
// CoverageEligibilityRequest/$submit submits, in document order, each with
// the member its patient names, read as a payer's Smart Gateway reads the
// input. The input is any of the operation's forms:
//   - a CoverageEligibilityRequest, read as ParseEligibilityRequestMemberExact
//     reads it;
//   - a Bundle carrying one request with the resources it references, whose
//     patient reference resolves among the Bundle's entries (by fullUrl, or
//     by type and id) to a Patient, named by its id or, with none, its member
//     identifier, or, as "#id", to the request's contained Patient; a
//     well-formed Patient reference no entry answers names the id it writes;
//   - a Bundle that is a batch of requests, each alone or in such a Bundle;
//   - a Parameters whose one resource parameter holds any of these.
//
// Every input is read by its exact member names, so an input that can be
// read two ways is refused with an *EligibilityRequestRefusal carrying the
// gateway's text: a repeated member name anywhere, or one of these named only
// in another case: the type, a Parameters' parameter, the name of any of its
// parameters or the resource of its resource parameter, and a request's
// patient, its reference, or the contained resources a "#id" names. A
// Parameters' other parameters are not read, however they spell any member
// but their name. A batch of more than 17 requests is refused with an
// *EligibilityBatchTooLarge before any request's patient is read. Any other
// error is an input that is none of the forms, or a request whose patient
// names no member one way only; the gateway answers it "parse member failed".
func ParseEligibilitySubmission(data []byte) ([]EligibilitySubmission, error) {
	if scan := scanMemberNames(data); scan.repeated {
		dup := scan.named
		if dup == "" {
			dup = "a member name"
		}
		return nil, &EligibilityRequestRefusal{"eligibility request repeats " + dup + ": it can be read two ways, so it is not read"}
	}
	subs, err := eligibilitySubmissions(data)
	if err != nil {
		return nil, namedInAnotherCase(err)
	}
	if len(subs) > maxEligibilityBatch {
		return nil, &EligibilityBatchTooLarge{Requests: len(subs)}
	}
	out := make([]EligibilitySubmission, len(subs))
	for i, sub := range subs {
		member, err := eligibilitySubmissionMember(sub)
		if err != nil {
			return nil, namedInAnotherCase(err)
		}
		out[i] = EligibilitySubmission{Request: sub.request, Member: member}
	}
	return out, nil
}

// errNamedInAnotherCase is a request that names its type only in another
// case, found past the strict reader's limits (exactdecode.ErrFieldNameCase
// is that reader's finding of the same).
var errNamedInAnotherCase = errors.New("a member is named only in another case")

// namedInAnotherCase is err as the gateway answers it: a member the patient
// is read through named only in another case is refused with its text, and
// any other error is returned as it is.
func namedInAnotherCase(err error) error {
	var fe *exactdecode.FieldNameError
	switch {
	case errors.As(err, &fe):
		switch fe.Field {
		case "resourceType", "parameter", "name", "resource", "patient", "reference", "contained":
			return &EligibilityRequestRefusal{"eligibility request names " + quotedBounded(fe.Member) + " for " + quotedBounded(fe.Field) + noPatientToBind}
		}
	case errors.Is(err, errNamedInAnotherCase):
		return &EligibilityRequestRefusal{"eligibility request names its type or patient only in another case" + noPatientToBind}
	}
	return err
}

// eligibilitySubmission is one request an operation input submits: its exact
// bytes, the entries of the Bundle that carries it with the resources it
// references (none for a request sent alone or alone in a batch), and whether
// it was submitted in a Bundle (the operation's Bundle forms, read from the
// start by the rules a request sent alone keeps for its own reading).
type eligibilitySubmission struct {
	request []byte
	entries []bundleEntry
	bundled bool
}

// bundleEntry is a Bundle entry as a reference resolves against it.
type bundleEntry struct {
	fullURL  string
	typ, id  string
	resource json.RawMessage
}

// eligibilitySubmissions is the requests an operation input submits, in
// document order. An error is an input that is none of the operation's forms,
// or one that cannot be read one way only. A request sent alone that the
// strict reader refuses only for its own limits is still read, by its exact
// member names, as ParseEligibilityRequestMemberExact reads it.
func eligibilitySubmissions(body []byte) ([]eligibilitySubmission, error) {
	var head struct {
		ResourceType string `json:"resourceType"`
	}
	if err := exactdecode.Decode(body, &head); err != nil {
		if exactdecode.ReadsTwoWays(err) {
			return nil, err
		}
		top := exactObject(body)
		if top == nil {
			return nil, err
		}
		if _, ok := namedOnlyInAnotherCase(top, "resourceType"); ok {
			return nil, fmt.Errorf("the request's type: %w", errNamedInAnotherCase)
		}
		if json.Unmarshal(top["resourceType"], &head.ResourceType) != nil || head.ResourceType != resourceEligibilityRequest {
			return nil, err
		}
	}
	switch head.ResourceType {
	case resourceEligibilityRequest:
		return []eligibilitySubmission{{request: body}}, nil
	case "Bundle":
		return eligibilityBundleSubmissions(body)
	case "Parameters":
	default:
		return nil, errors.New("expected a CoverageEligibilityRequest, a Bundle of them, or a Parameters holding one")
	}
	// Every parameter's name is read by its exact member name: a reader that
	// folded it could take another parameter for the resource. Only the
	// resource parameter's own members are read further; any other parameter
	// is not read, however it spells any member but its name.
	var params struct {
		Parameter []json.RawMessage `json:"parameter"`
	}
	if err := exactdecode.Decode(body, &params); err != nil {
		return nil, err
	}
	var resource json.RawMessage
	for _, raw := range params.Parameter {
		var p struct {
			Name string `json:"name"`
		}
		if err := exactdecode.Decode(raw, &p); err != nil {
			return nil, err
		}
		if p.Name != "resource" {
			continue
		}
		var value struct {
			Resource json.RawMessage `json:"resource"`
		}
		if err := exactdecode.Decode(raw, &value); err != nil {
			return nil, err
		}
		if resource != nil {
			return nil, errors.New("the Parameters holds more than one resource parameter")
		}
		if value.Resource == nil {
			return nil, errors.New("the resource parameter holds no resource")
		}
		resource = value.Resource
	}
	if resource == nil {
		return nil, errors.New("the Parameters holds no resource parameter")
	}
	var inner struct {
		ResourceType string `json:"resourceType"`
	}
	if err := exactdecode.Decode(resource, &inner); err != nil {
		return nil, err
	}
	switch inner.ResourceType {
	case resourceEligibilityRequest:
		return []eligibilitySubmission{{request: resource}}, nil
	case "Bundle":
		return eligibilityBundleSubmissions(resource)
	}
	return nil, errors.New("the resource parameter holds no CoverageEligibilityRequest or Bundle of them")
}

// eligibilityBundleEntries is a Bundle's entries read by their exact member
// names: each entry's fullUrl and resource, and that resource's resourceType
// and id. ok is false for a Bundle any of whose entries cannot be read so (a
// member named in another case, a value of another JSON type), or carries no
// resource, or one with no type (a null one among them): a reader that
// skipped or folded it would read another set of resources.
func eligibilityBundleEntries(bundle []byte) ([]bundleEntry, bool) {
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if exactdecode.Decode(bundle, &b) != nil {
		return nil, false
	}
	out := make([]bundleEntry, 0, len(b.Entry))
	for _, e := range b.Entry {
		var head struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if exactdecode.Decode(e.Resource, &head) != nil || head.ResourceType == "" {
			return nil, false
		}
		out = append(out, bundleEntry{e.FullURL, head.ResourceType, head.ID, e.Resource})
	}
	return out, true
}

// eligibilityBundleSubmissions is the requests a Bundle submits: the one
// request of a Bundle carrying it with the resources it references, or, for a
// Bundle whose entries are all requests or such Bundles, each of them in
// order (a batch). Any other Bundle is refused: it is neither form.
func eligibilityBundleSubmissions(bundle []byte) ([]eligibilitySubmission, error) {
	entries, ok := eligibilityBundleEntries(bundle)
	if !ok {
		return nil, errors.New("the Bundle's entries cannot be read one way only")
	}
	var requests, bundles int
	for _, e := range entries {
		switch e.typ {
		case resourceEligibilityRequest:
			requests++
		case "Bundle":
			bundles++
		}
	}
	others := len(entries) - requests - bundles
	switch {
	case requests == 1 && bundles == 0:
		for _, e := range entries {
			if e.typ == resourceEligibilityRequest {
				return []eligibilitySubmission{{request: e.resource, entries: entries, bundled: true}}, nil
			}
		}
	case requests+bundles > 0 && others == 0:
		out := make([]eligibilitySubmission, 0, len(entries))
		for i, e := range entries {
			if e.typ == resourceEligibilityRequest {
				out = append(out, eligibilitySubmission{request: e.resource, bundled: true})
				continue
			}
			sub, ok := oneRequest(e.resource)
			if !ok {
				return nil, fmt.Errorf("entry[%d] is a Bundle that does not carry exactly one CoverageEligibilityRequest with the resources it references", i)
			}
			sub.bundled = true
			out = append(out, sub)
		}
		return out, nil
	case requests+bundles == 0:
		return nil, errors.New("the Bundle holds no CoverageEligibilityRequest")
	}
	return nil, errors.New("the Bundle is neither one CoverageEligibilityRequest with the resources it references nor a batch of requests")
}

// oneRequest is the request of a Bundle that carries exactly one, with the
// resources it references and no Bundle among them.
func oneRequest(bundle []byte) (eligibilitySubmission, bool) {
	entries, ok := eligibilityBundleEntries(bundle)
	if !ok {
		return eligibilitySubmission{}, false
	}
	var found *bundleEntry
	for i, e := range entries {
		switch e.typ {
		case "Bundle":
			return eligibilitySubmission{}, false
		case resourceEligibilityRequest:
			if found != nil {
				return eligibilitySubmission{}, false
			}
			found = &entries[i]
		}
	}
	if found == nil {
		return eligibilitySubmission{}, false
	}
	return eligibilitySubmission{request: found.resource, entries: entries}, true
}

// eligibilitySubmissionMember is the member a submitted request's patient
// names. A request sent alone is read by ParseEligibilityRequestMemberExact.
// A request submitted in a Bundle is read only by its exact member names: its
// patient reference and its contained resources must read one way. Its
// reference names:
//   - "#id": the contained Patient with that id, by its member identifier;
//   - anything else, as a FHIR reader of the Bundle resolves it: the Patient
//     entry it resolves to among the entries of the Bundle carrying the
//     request (by fullUrl, or type and id), by that entry's id or, with none,
//     its member identifier. An entry whose id is not the id a well-formed
//     Patient reference writes is refused, as is a reference an entry of
//     another type answers;
//   - a well-formed Patient reference no entry answers: the id it writes.
//
// Any other reference, an empty one among them, names no patient, and is
// refused.
func eligibilitySubmissionMember(sub eligibilitySubmission) (string, error) {
	if !sub.bundled {
		return ParseEligibilityRequestMemberExact(sub.request)
	}
	var req struct {
		Patient struct {
			Reference string `json:"reference"`
		} `json:"patient"`
	}
	if err := exactdecode.Decode(sub.request, &req); err != nil {
		return "", fmt.Errorf("the request's patient cannot be read one way only: %w", err)
	}
	ref := req.Patient.Reference
	if strings.HasPrefix(ref, "#") {
		var c struct {
			Contained []any `json:"contained"`
		}
		if err := exactdecode.Decode(sub.request, &c); err != nil {
			return "", fmt.Errorf("the request's contained resources cannot be read one way only: %w", err)
		}
		if member, ok := patientRefMember(ref, containedObjects(c.Contained)); ok {
			return member, nil
		}
		return "", fmt.Errorf("patient reference %s names no contained Patient", quotedBounded(ref))
	}
	var matches []bundleEntry
	for _, e := range sub.entries {
		if entryAnswers(ref, e) {
			matches = append(matches, e)
		}
	}
	switch {
	case len(matches) > 1:
		return "", fmt.Errorf("patient reference %s names more than one Bundle entry", quotedBounded(ref))
	case len(matches) == 1 && matches[0].typ != "Patient":
		return "", fmt.Errorf("patient reference %s names a Bundle entry that is no Patient", quotedBounded(ref))
	case len(matches) == 0:
		if member, ok := patientRefMember(ref, nil); ok {
			return member, nil
		}
		return "", fmt.Errorf("patient reference %s names no Patient the Bundle carries", quotedBounded(ref))
	}
	var p struct {
		ID         string `json:"id"`
		Identifier []any  `json:"identifier"`
	}
	if exactdecode.Decode(matches[0].resource, &p) != nil {
		return "", fmt.Errorf("patient reference %s names a Patient that cannot be read one way only", quotedBounded(ref))
	}
	if written := patientRefShape.FindStringSubmatch(ref); written != nil && p.ID != written[1] {
		return "", fmt.Errorf("patient reference %s names a Bundle entry whose id is not the reference's", quotedBounded(ref))
	}
	if p.ID != "" {
		return p.ID, nil
	}
	if m, ok := containedPatientMember(map[string]any{"identifier": p.Identifier}); ok {
		return m, nil
	}
	return "", fmt.Errorf("patient reference %s names a Patient with no id or member identifier", quotedBounded(ref))
}

// entryAnswers is whether a Bundle entry is what ref names: an absolute
// reference (a URL, or a URN such as urn:uuid) names the entry whose fullUrl
// equals it, and a relative "[type]/[id]" names the entry whose resource has
// that type and id, whatever its fullUrl. A version-specific or otherwise
// differently spelled reference names no entry.
func entryAnswers(ref string, e bundleEntry) bool {
	return (e.fullURL != "" && e.fullURL == ref) || (e.typ != "" && e.id != "" && ref == e.typ+"/"+e.id)
}

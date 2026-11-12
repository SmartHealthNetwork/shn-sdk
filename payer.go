package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// PayerIdentifier is a Coverage.payor Organization identifier (system|value): a candidate for
// routing, which chooses among a payor's identifiers by a registry (ChooseRegisteredPayer).
type PayerIdentifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// CMSPayerIdentity is the historical hardcoded payer identity (CMS, urn:oid:…300|00001).
// Kept as a named constant so behavior-preserving call sites can pass it explicitly during the
// migration to coverage-derived payer identity.
var CMSPayerIdentity = PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00001"}

// ErrNoCoveragePayer: the Coverage value names no payer this SDK can resolve.
var ErrNoCoveragePayer = errors.New("shnsdk: no payer can be resolved from the coverage")

// ErrPayerRecordAbsent: a builder that names a payer was given none. It names
// the payer by the identifier the caller's own records give it (a Coverage's
// payor, or for a payer its own Organization), or, for an answer, by the insurer
// its request named, and never a placeholder such as Organization/payer: without
// one it refuses rather than build.
var ErrPayerRecordAbsent = errors.New("shnsdk: no payer record supplied, so nothing is built: SHN's builders name the caller's own payer and never a placeholder")

// named reports whether p names a payer: both its system and its value.
func (p PayerIdentifier) named() bool { return p.System != "" && p.Value != "" }

// reference is p as a logical FHIR Reference (Reference.identifier): a payer
// known by its identifier, not by a resource in the message.
func (p PayerIdentifier) reference() fhir.Reference {
	return fhir.Reference{Identifier: &fhir.Identifier{System: strPtr(p.System), Value: strPtr(p.Value)}}
}

// ErrAmbiguousCoveragePayer: the coverage names more than one payer: a Coverage Bundle's Coverages
// name different payers, or (ChooseRegisteredPayer) a Coverage's identifiers, or the Coverages,
// name more than one registered holder, or the Coverages name one registered payer beside none,
// or different sets of unregistered identifiers.
var ErrAmbiguousCoveragePayer = errors.New("shnsdk: the coverage names more than one payer")

// ParsePayerIdentifier extracts a payer identity from a Coverage's payor. It honors
// all three payor forms: a CONTAINED Organization (payor ref "#id"), an EXTERNAL Organization
// (payor ref "Organization/id", resolved via resolveRef), and an INLINE Coverage.payor[0].identifier.
// ok=false when no (system,value) can be found. resolveRef may be nil (contained + inline only).
//
// coverageJSON may also be a Bundle (a search result or a collection) of Coverages: it routes
// only when every Coverage entry resolves to the same payer, an external payor reference being
// looked up first among the Bundle's own entries and then through resolveRef.
// ParseCoveragePayer reports why a value does not route.
//
// The identity it returns is POSITION-BASED: the first identifier of the payor Organization,
// whatever its system. It is not a routing decision: an Organization that lists an NPI before
// the payer identifier a network registers yields the NPI. To route, use
// CoveragePayerCandidates and ChooseRegisteredPayer, which choose by what the registry resolves.
func ParsePayerIdentifier(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, bool) {
	pid, err := ParseCoveragePayer(coverageJSON, resolveRef)
	return pid, err == nil
}

// ParseCoveragePayer is ParsePayerIdentifier with the reason a value does not route:
// ErrNoCoveragePayer (no Coverage, or a Coverage whose payer cannot be resolved) or
// ErrAmbiguousCoveragePayer (a Bundle whose Coverages name different payers). It only parses,
// and, like ParsePayerIdentifier, answers by position: not for routing (see
// ChooseRegisteredPayer).
func ParseCoveragePayer(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, error) {
	entries, isBundle, err := coverageBundleEntries(coverageJSON)
	if err != nil {
		return PayerIdentifier{}, fmt.Errorf("%w: %v", ErrNoCoveragePayer, err)
	}
	if !isBundle {
		if pid, ok := parseCoveragePayor(coverageJSON, resolveRef); ok {
			return pid, nil
		}
		return PayerIdentifier{}, ErrNoCoveragePayer
	}
	inBundle := bundleResolver(entries, resolveRef)
	var found *PayerIdentifier
	for _, e := range entries {
		if e.head.ResourceType != "Coverage" {
			continue
		}
		pid, ok := parseCoveragePayor(e.Resource, inBundle)
		if !ok {
			return PayerIdentifier{}, fmt.Errorf("%w: Coverage %q", ErrNoCoveragePayer, e.head.ID)
		}
		if found != nil && *found != pid {
			return PayerIdentifier{}, ErrAmbiguousCoveragePayer
		}
		found = &pid
	}
	if found == nil {
		return PayerIdentifier{}, fmt.Errorf("%w: the Bundle holds no Coverage", ErrNoCoveragePayer)
	}
	return *found, nil
}

// ErrNoRegisteredPayer: a coverage names payer identifiers, but none of them is one the
// registry resolves (ChooseRegisteredPayer). The error ChooseRegisteredPayer returns is a
// *NoRegisteredPayerError naming them.
var ErrNoRegisteredPayer = errors.New("shnsdk: none of the coverage's payer identifiers is registered")

// NoRegisteredPayerError names the identifiers a Coverage's payor carried, in document order,
// when the registry resolves none of them. errors.Is(err, ErrNoRegisteredPayer) holds. Its
// Error text lists every identifier as given, unbounded: the identifiers come from the caller's
// own record, and a caller putting them in an answer bounds them itself.
type NoRegisteredPayerError struct {
	Candidates []PayerIdentifier
}

// Error names ErrNoRegisteredPayer and every candidate.
func (e *NoRegisteredPayerError) Error() string {
	names := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		names[i] = c.System + "|" + c.Value
	}
	return ErrNoRegisteredPayer.Error() + ": " + strings.Join(names, ", ")
}

// Is reports ErrNoRegisteredPayer.
func (e *NoRegisteredPayerError) Is(target error) bool { return target == ErrNoRegisteredPayer }

// ErrUnresolvedPayor: a payor of the coverage names no payer the caller can read: its reference
// resolves to nothing (a dangling contained or in-Bundle reference, an external one the resolver
// does not answer, or no resolver), or to a resource that is no payer Organization with an
// identifier, so the payer the coverage names is unknown (CoveragePayerCandidates). The error it
// returns is an *UnresolvedPayorError.
var ErrUnresolvedPayor = errors.New("shnsdk: a payor of the coverage names no payer that can be read, so the payer it names is unknown")

// UnresolvedPayorError names the payor that names no payer the caller can read: the id of its
// Coverage (empty when it has none), its index in Coverage.payor and its reference text.
// errors.Is(err, ErrUnresolvedPayor) and errors.Is(err, ErrNoCoveragePayer) hold.
type UnresolvedPayorError struct {
	Coverage  string
	Index     int
	Reference string
}

// Error names ErrUnresolvedPayor, the Coverage, the payor's index and its reference.
func (e *UnresolvedPayorError) Error() string {
	at := fmt.Sprintf("payor[%d] %q", e.Index, e.Reference)
	if e.Coverage != "" {
		at = fmt.Sprintf("Coverage %q %s", e.Coverage, at)
	}
	return ErrUnresolvedPayor.Error() + ": " + at
}

// Is reports ErrUnresolvedPayor and ErrNoCoveragePayer.
func (e *UnresolvedPayorError) Is(target error) bool {
	return target == ErrUnresolvedPayor || target == ErrNoCoveragePayer
}

// CoveragePayerCandidates returns, for each Coverage the value holds (one Coverage, or each
// Coverage entry of a Bundle, in order), every payer identifier its payors name: for each payor
// in document order (Coverage.payor carries no ordering meaning), its inline identifier, then
// every (system, value) identifier of the Organization it references (contained, an entry of the
// Bundle, or external through resolveRef), each once. It reads the payor forms ParseCoveragePayer
// reads, but every payor rather than payor[0], resolves a reference even beside an inline
// identifier, and keeps every identifier, so a caller can route by the one a registry knows
// rather than by position.
//
// A payor is classified by structure, never by text. Its type is its Reference.type when it
// states one, else the type of the resource its reference resolves to, else the type its literal
// reference names. A Patient or RelatedPerson payor (self-pay) names no payer and adds nothing,
// not even an inline identifier (it names the patient). Any other payor with an inline identifier
// is known by it; its reference adds the Organization's identifiers when the payor's type is
// Organization (stated, or none stated) and the reference resolves to one with an identifier.
// Any other payor that names no payer Organization with an identifier (its reference resolves to
// nothing, to an Organization with no identifier, or to another kind of resource, or its stated
// type is neither Organization nor self-pay) makes the payer unknown: an *UnresolvedPayorError.
// ErrNoCoveragePayer when the value holds no Coverage, or a Coverage names no identifier (for
// example a self-pay Coverage).
//
// In a Bundle, a reference that several entries answer picks none of them: when every one is an
// Organization, the payor names every identifier they name, in document order, each once, and the
// registry decides among them (ChooseRegisteredPayer); when not every one is an Organization, the
// reference names no payer and is not passed to resolveRef. ParseCoveragePayer still reads the
// first entry.
func CoveragePayerCandidates(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) ([][]PayerIdentifier, error) {
	return coverageCandidateLists(coverageJSON, resolveRef)
}

// ChooseRegisteredPayer routes a coverage by the registry, never by an identifier's position or
// system. For each Coverage's candidates (CoveragePayerCandidates) the identifiers resolve accepts
// must name one holder, and every Coverage must name the same payer: the same holder, or, when
// resolve accepts none of their identifiers, the same set of identifiers. It returns the holder
// and the first identifier of the first Coverage, in document order, that names it.
//
// ErrNoCoveragePayer: no candidates, or a Coverage with none. ErrAmbiguousCoveragePayer: the
// identifiers, or the Coverages, name more than one payer, including a Coverage naming a
// registered payer beside one naming none. *NoRegisteredPayerError (errors.Is ErrNoRegisteredPayer):
// every Coverage names the same identifiers and resolve accepts none of them, naming them. An
// identifier resolve accepts with an empty holder counts as unregistered; resolve must not be nil.
func ChooseRegisteredPayer(candidates [][]PayerIdentifier, resolve func(PayerIdentifier) (holder string, ok bool)) (PayerIdentifier, string, error) {
	if len(candidates) == 0 {
		return PayerIdentifier{}, "", ErrNoCoveragePayer
	}
	type pick struct {
		holder string
		pid    PayerIdentifier
	}
	picks := make([]pick, len(candidates))
	for i, cov := range candidates {
		if len(cov) == 0 {
			return PayerIdentifier{}, "", ErrNoCoveragePayer
		}
		for _, p := range cov {
			h, ok := resolve(p)
			switch {
			case !ok || h == "":
			case picks[i].holder == "":
				picks[i] = pick{h, p}
			case h != picks[i].holder:
				return PayerIdentifier{}, "", ErrAmbiguousCoveragePayer
			}
		}
	}
	if picks[0].holder == "" {
		for i := range candidates[1:] {
			if picks[i+1].holder != "" || !sameIdentifiers(candidates[i+1], candidates[0]) {
				return PayerIdentifier{}, "", ErrAmbiguousCoveragePayer
			}
		}
		return PayerIdentifier{}, "", &NoRegisteredPayerError{Candidates: slices.Clone(candidates[0])}
	}
	for _, p := range picks[1:] {
		if p.holder != picks[0].holder {
			return PayerIdentifier{}, "", ErrAmbiguousCoveragePayer
		}
	}
	return picks[0].pid, picks[0].holder, nil
}

// sameIdentifiers reports whether a and b hold the same identifiers, whatever their order and
// however often each repeats one.
func sameIdentifiers(a, b []PayerIdentifier) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			return false
		}
	}
	return true
}

// bundleResolver resolves a reference among a Bundle's entries: an entry resolves a reference
// equal to its fullUrl, or a relative reference equal to its Type/id; an absolute reference to
// another server never resolves to an entry. Anything else goes to resolveRef, when given.
func bundleResolver(entries []coverageBundleEntry, resolveRef func(ref string) ([]byte, bool)) func(ref string) ([]byte, bool) {
	return func(ref string) ([]byte, bool) {
		for _, e := range entries {
			if e.FullURL != "" && e.FullURL == ref {
				return e.Resource, true
			}
			if e.head.ResourceType != "" && e.head.ID != "" && ref == e.head.ResourceType+"/"+e.head.ID {
				return e.Resource, true
			}
		}
		if resolveRef != nil {
			return resolveRef(ref)
		}
		return nil, false
	}
}

// bundleUnionResolver resolves a payor reference among a Bundle's entries as routing reads
// them: no entry is picked over another. The entries that answer it are those bundleResolver
// matches (a fullUrl equal to it, or a Type/id equal to a relative reference). One answers as
// itself. Several that are all Organizations answer as one Organization naming every identifier
// they name, in document order, each once, so the registry decides among them as it does across
// payors. Several that are not all Organizations answer, and name no payer: the reference is not
// looked up further, and the payor is unknown unless it carries an identifier of its own. A
// reference no entry answers goes to resolveRef, when given.
//
// bundleResolver, which ParseCoveragePayer keeps, reads the first entry that answers.
func bundleUnionResolver(entries []coverageBundleEntry, resolveRef func(ref string) ([]byte, bool)) func(ref string) ([]byte, bool) {
	return func(ref string) ([]byte, bool) {
		var answers []coverageBundleEntry
		for _, e := range entries {
			// An entry with no resource member answers nothing, as the gateway reads it;
			// one whose resource is null answers as a resource that names no payer.
			if len(e.Resource) == 0 {
				continue
			}
			if (e.FullURL != "" && e.FullURL == ref) || (e.head.ResourceType != "" && e.head.ID != "" && ref == e.head.ResourceType+"/"+e.head.ID) {
				answers = append(answers, e)
			}
		}
		switch len(answers) {
		case 0:
			if resolveRef != nil {
				return resolveRef(ref)
			}
			return nil, false
		case 1:
			return answers[0].Resource, true
		}
		var ids []identifier
		for _, a := range answers {
			if a.head.ResourceType != "Organization" {
				return nil, true
			}
			for _, p := range orgIdentifiers(a.Resource, "") {
				if !slices.Contains(ids, identifier{p.System, p.Value}) {
					ids = append(ids, identifier{p.System, p.Value})
				}
			}
		}
		merged, err := json.Marshal(struct {
			ResourceType string       `json:"resourceType"`
			Identifier   []identifier `json:"identifier"`
		}{"Organization", ids})
		if err != nil {
			return nil, true
		}
		return merged, true
	}
}

// coverageCandidateLists is CoveragePayerCandidates: one non-empty list per Coverage, at least one.
func coverageCandidateLists(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) ([][]PayerIdentifier, error) {
	entries, isBundle, err := coverageBundleEntries(coverageJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoCoveragePayer, err)
	}
	if !isBundle {
		cands, unresolved := payorCandidates(coverageJSON, resolveRef)
		switch {
		case unresolved != nil:
			return nil, unresolved
		case len(cands) > 0:
			return [][]PayerIdentifier{cands}, nil
		}
		return nil, ErrNoCoveragePayer
	}
	inBundle := bundleUnionResolver(entries, resolveRef)
	var lists [][]PayerIdentifier
	for _, e := range entries {
		if e.head.ResourceType != "Coverage" {
			continue
		}
		cands, unresolved := payorCandidates(e.Resource, inBundle)
		if unresolved != nil {
			unresolved.Coverage = e.head.ID
			return nil, unresolved
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("%w: Coverage %q", ErrNoCoveragePayer, e.head.ID)
		}
		lists = append(lists, cands)
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("%w: the Bundle holds no Coverage", ErrNoCoveragePayer)
	}
	return lists, nil
}

type coverageBundleEntry struct {
	FullURL  string          `json:"fullUrl"`
	Resource json.RawMessage `json:"resource"`
	head     struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
}

// coverageBundleEntries returns the entries of a Bundle value (isBundle false for any other
// value). A Bundle that does not decode is an error.
func coverageBundleEntries(v []byte) (entries []coverageBundleEntry, isBundle bool, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if json.Unmarshal(v, &probe) != nil || probe.ResourceType != "Bundle" {
		return nil, false, nil
	}
	var b struct {
		Entry []coverageBundleEntry `json:"entry"`
	}
	if err := json.Unmarshal(v, &b); err != nil {
		return nil, true, err
	}
	for i := range b.Entry {
		_ = json.Unmarshal(b.Entry[i].Resource, &b.Entry[i].head)
	}
	return b.Entry, true, nil
}

// payorCandidates resolves every payor of one Coverage, in document order, to every identifier
// it names: each payor's inline identifier, then every identifier of the Organization it
// references (orgIdentifiers), each once. A self-pay payor adds nothing. A payor naming no payer
// Organization with an identifier, or whose stated type is neither Organization nor self-pay,
// and carrying no inline identifier, is unknown, and its error is returned.
func payorCandidates(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) ([]PayerIdentifier, *UnresolvedPayorError) {
	var cov struct {
		ID    json.RawMessage `json:"id"`
		Payor []struct {
			Reference  string          `json:"reference"`
			Type       json.RawMessage `json:"type"`
			Identifier *identifier     `json:"identifier"`
		} `json:"payor"`
		Contained []json.RawMessage `json:"contained"`
	}
	if err := json.Unmarshal(coverageJSON, &cov); err != nil {
		return nil, nil
	}
	covID := jsonString(cov.ID)
	var out []PayerIdentifier
	add := func(ids ...PayerIdentifier) {
		for _, id := range ids {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	for i, p := range cov.Payor {
		inline := p.Identifier != nil && p.Identifier.System != "" && p.Identifier.Value != ""
		// The payor's type, by structure: its Reference.type when it states one, which
		// decides; otherwise the type of the resource its reference names (below), or the
		// type its literal reference names when nothing resolves.
		declared := jsonString(p.Type)
		var ids []PayerIdentifier
		var resolvedType string
		if p.Reference != "" {
			ids, resolvedType = payorTarget(p.Reference, cov.Contained, resolveRef)
		}
		typ := declared
		if typ == "" {
			typ = resolvedType
		}
		if typ == "" {
			typ = literalReferenceType(p.Reference)
		}
		switch {
		case selfPayType(typ):
			// Self-pay: a Patient or a RelatedPerson names no payer, and its identifiers,
			// inline or not, are the patient's, never offered as a payer's.
		case len(ids) > 0 && (declared == "" || declared == "Organization"):
			if inline {
				add(PayerIdentifier{p.Identifier.System, p.Identifier.Value})
			}
			add(ids...)
		case inline:
			// Known by its inline identifier; its reference adds no payer Organization.
			add(PayerIdentifier{p.Identifier.System, p.Identifier.Value})
		default:
			return nil, &UnresolvedPayorError{Coverage: covID, Index: i, Reference: p.Reference}
		}
	}
	return out, nil
}

// payorTarget resolves a payor reference: the identifiers of the Organization it names (none
// when it names no Organization with an identifier) and the type of the resource it names ("" when
// it resolves to nothing). A contained reference ("#id") names the first contained Organization
// with that id and an identifier, else the first contained resource with that id; "#" alone names
// the first contained Organization with an identifier, as ParseCoveragePayer reads it. Any other
// reference is resolveRef's.
func payorTarget(ref string, contained []json.RawMessage, resolveRef func(ref string) ([]byte, bool)) ([]PayerIdentifier, string) {
	resourceType := func(raw []byte) string {
		var head struct {
			ResourceType string `json:"resourceType"`
		}
		_ = json.Unmarshal(raw, &head)
		return head.ResourceType
	}
	if id, ok := strings.CutPrefix(ref, "#"); ok {
		for _, raw := range contained {
			if ids := orgIdentifiers(raw, id); len(ids) > 0 {
				return ids, "Organization"
			}
		}
		for _, raw := range contained {
			var head struct {
				ID json.RawMessage `json:"id"`
			}
			if id != "" && json.Unmarshal(raw, &head) == nil && jsonString(head.ID) == id {
				return nil, resourceType(raw)
			}
		}
		return nil, ""
	}
	if resolveRef == nil {
		return nil, ""
	}
	raw, found := resolveRef(ref)
	if !found {
		return nil, ""
	}
	return orgIdentifiers(raw, ""), resourceType(raw)
}

// jsonString is a JSON string's value, or "" for anything else.
func jsonString(raw json.RawMessage) string {
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v
}

// selfPayType reports whether a resource type names a self-paying party (Coverage.payor's
// Patient and RelatedPerson targets), not a payer organization.
func selfPayType(t string) bool { return t == "Patient" || t == "RelatedPerson" }

// literalReferenceType is the resource type a literal reference names by FHIR's reference grammar
// ([url/]Type/id[/_history/version]), or "" for a contained ("#id") or URN reference.
func literalReferenceType(ref string) string {
	if ref == "" || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "urn:") {
		return ""
	}
	if i := strings.Index(ref, "/_history/"); i >= 0 {
		ref = ref[:i]
	}
	segs := strings.Split(ref, "/")
	if len(segs) < 2 {
		return ""
	}
	return segs[len(segs)-2]
}

// parseCoveragePayor resolves one Coverage's first payor.
func parseCoveragePayor(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, bool) {
	var cov struct {
		Payor []struct {
			Reference  string      `json:"reference"`
			Identifier *identifier `json:"identifier"`
		} `json:"payor"`
		Contained []json.RawMessage `json:"contained"`
	}
	if err := json.Unmarshal(coverageJSON, &cov); err != nil || len(cov.Payor) == 0 {
		return PayerIdentifier{}, false
	}
	p := cov.Payor[0]
	// Inline form.
	if p.Identifier != nil && p.Identifier.System != "" && p.Identifier.Value != "" {
		return PayerIdentifier{p.Identifier.System, p.Identifier.Value}, true
	}
	switch {
	case strings.HasPrefix(p.Reference, "#"): // contained
		id := strings.TrimPrefix(p.Reference, "#")
		for _, raw := range cov.Contained {
			if pid, ok := orgIdentifier(raw, id); ok {
				return pid, true
			}
		}
	case p.Reference != "" && resolveRef != nil: // external
		if orgJSON, found := resolveRef(p.Reference); found {
			if pid, ok := orgIdentifier(orgJSON, ""); ok {
				return pid, true
			}
		}
	}
	return PayerIdentifier{}, false
}

// ParseOrganizationIdentifier extracts the first (system,value) identifier from a
// standalone Organization resource — a payer's SELF-read of its own well-known
// Organization (e.g. Organization/payer), as opposed to ParsePayerIdentifier's
// Coverage.payor resolution. Used when a member has NO Coverage record at all: there is
// no payor to parse, so the insurer instead names the payer's own identity. ok=false when
// the resource isn't an Organization or carries no identifier. Like ParseCoveragePayer it answers by
// position, not for routing: route by OrganizationIdentifiers and a registry.
func ParseOrganizationIdentifier(orgJSON []byte) (PayerIdentifier, bool) {
	return orgIdentifier(orgJSON, "")
}

// OrganizationIdentifiers returns every (system, value) identifier of an Organization, in
// document order, each once; nil when the resource is not an Organization or carries none. A
// caller routing by an Organization (an insurer) chooses among them with ChooseRegisteredPayer.
func OrganizationIdentifiers(orgJSON []byte) []PayerIdentifier {
	return orgIdentifiers(orgJSON, "")
}

type identifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// orgIdentifier returns the first (system,value) of an Organization's identifier. When wantID is
// non-empty the Organization's id must match (contained-resolution); empty wantID accepts any.
func orgIdentifier(orgJSON []byte, wantID string) (PayerIdentifier, bool) {
	var org struct {
		ResourceType string       `json:"resourceType"`
		ID           string       `json:"id"`
		Identifier   []identifier `json:"identifier"`
	}
	if err := json.Unmarshal(orgJSON, &org); err != nil || org.ResourceType != "Organization" {
		return PayerIdentifier{}, false
	}
	if wantID != "" && org.ID != wantID {
		return PayerIdentifier{}, false
	}
	for _, id := range org.Identifier {
		if id.System != "" && id.Value != "" {
			return PayerIdentifier{id.System, id.Value}, true
		}
	}
	return PayerIdentifier{}, false
}

// orgIdentifiers returns every (system,value) of an Organization's identifier, in document order,
// each once. When wantID is non-empty the Organization's id must match.
func orgIdentifiers(orgJSON []byte, wantID string) []PayerIdentifier {
	var org struct {
		ResourceType string       `json:"resourceType"`
		ID           string       `json:"id"`
		Identifier   []identifier `json:"identifier"`
	}
	if err := json.Unmarshal(orgJSON, &org); err != nil || org.ResourceType != "Organization" {
		return nil
	}
	if wantID != "" && org.ID != wantID {
		return nil
	}
	var out []PayerIdentifier
	for _, id := range org.Identifier {
		p := PayerIdentifier{id.System, id.Value}
		if p.System != "" && p.Value != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

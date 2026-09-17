package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// CoverageInformationURL is the Da Vinci CRD coverage-information extension.
const CoverageInformationURL = "http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information"

// CRDObservationSource says where in a CDS Hooks response a coverage
// observation was found.
type CRDObservationSource string

const (
	// CRDSourceSystemAction: an update or create system action's resource.
	CRDSourceSystemAction CRDObservationSource = "systemAction"
	// CRDSourceCardSuggestion: an update or create action inside a card
	// suggestion (the shape CRD used before system actions).
	CRDSourceCardSuggestion CRDObservationSource = "cardSuggestion"
	// CRDSourceLegacyCard: the card "extension" object earlier versions of
	// this SDK wrote (see BuildCards).
	CRDSourceLegacyCard CRDObservationSource = "legacyCard"
)

// CRDObservation is everything a CRD response says about coverage, read
// without changing a byte: one entry per order that carries coverage
// information, in response order (system actions first, then cards).
type CRDObservation struct {
	Orders []CRDObservedOrder
}

// CRDObservedOrder is one order a CRD response returned with coverage
// information.
type CRDObservedOrder struct {
	Source CRDObservationSource
	// Path locates the action (or legacy card extension) in the response,
	// for example "systemActions[0]".
	Path         string
	ActionType   string // update | create ("" for a legacy card)
	ResourceType string
	ID           string
	// Order is the updated order exactly as the response carries it (nil for
	// a legacy card, which carries no order).
	Order json.RawMessage
	// Coverage holds each coverage-information extension on the order, in
	// order.
	Coverage []CoverageInformation
}

// CoverageInformationValue is one sub-extension exactly as sent.
type CoverageInformationValue struct {
	URL string
	Raw json.RawMessage
}

// CRDCoding is a FHIR Coding (system, code and optional display).
type CRDCoding struct {
	System  string `json:"system,omitempty"`
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`
}

// CoverageInformation is one coverage-information extension. Values keeps
// every sub-extension in order, exactly as sent; the typed fields are views
// over Values. Repeating sub-extensions are slices in order; for a
// sub-extension the profile allows once, the first value is used and every
// value stays in Values.
type CoverageInformation struct {
	// Raw is the whole extension exactly as sent.
	Raw    json.RawMessage
	Values []CoverageInformationValue

	Coverage            string // coverage (the Coverage reference)
	Covered             string
	PANeeded            string
	DocNeeded           []string
	DocPurpose          []string
	InfoNeeded          []string
	BillingCodes        []CRDCoding
	Reasons             []json.RawMessage // each reason CodeableConcept, exactly as sent
	Details             []json.RawMessage // each detail sub-extension, exactly as sent
	Questionnaires      []string          // canonicals, with any |version
	Responses           []string          // response references (CRD 2.0.1)
	Dependencies        []string          // dependency references
	Date                string
	ExpiryDate          string
	CoverageAssertionID string
	SatisfiedPAID       string
	Contacts            []json.RawMessage // each contact value, exactly as sent
	// Unknown are sub-extensions this SDK does not know, exactly as sent.
	Unknown []CoverageInformationValue
	// Nonconformant describes values that are not of their defined type; the
	// typed field is left empty for them.
	Nonconformant []string
}

// Primary returns the first order's first coverage information as the
// CardCoverage view that ParseCards returned. ok is false when the response
// carries no coverage information with a covered value.
func (o CRDObservation) Primary() (CardCoverage, bool) {
	for _, order := range o.Orders {
		for _, ci := range order.Coverage {
			if ci.Covered == "" {
				return CardCoverage{}, false
			}
			return CardCoverage{
				Covered:        ci.Covered,
				PANeeded:       ci.PANeeded,
				Questionnaires: ci.Questionnaires,
				SatisfiedPaID:  ci.SatisfiedPAID,
			}, true
		}
	}
	return CardCoverage{}, false
}

// ParseCRDResponse reads the coverage information a CDS Hooks response
// carries: in update and create system actions, in update and create actions
// of card suggestions, and in the card extension object earlier versions of
// this SDK wrote. The body must be one JSON object with unique member names.
// It never changes or re-encodes the response; every value it returns is a
// view over, or an exact copy of, the response's own bytes.
func ParseCRDResponse(body []byte) (CRDObservation, error) {
	d, err := splice.Scan(body, splice.DefaultLimits())
	if err != nil {
		return CRDObservation{}, fmt.Errorf("shnsdk: CRD response: %w", err)
	}
	root := d.Root()
	if d.Kind(root) != splice.KindObject {
		return CRDObservation{}, errors.New("shnsdk: CRD response is not a JSON object")
	}
	p := crdReader{d: d, src: body}
	var obs CRDObservation
	if sa, ok := d.Member(root, "systemActions"); ok && d.Kind(sa) == splice.KindArray {
		for i, a := range d.Elems(sa) {
			if o, ok := p.action(a, CRDSourceSystemAction, fmt.Sprintf("systemActions[%d]", i)); ok {
				obs.Orders = append(obs.Orders, o)
			}
		}
	}
	if cards, ok := d.Member(root, "cards"); ok && d.Kind(cards) == splice.KindArray {
		for i, card := range d.Elems(cards) {
			if d.Kind(card) != splice.KindObject {
				continue
			}
			cp := fmt.Sprintf("cards[%d]", i)
			if ext, ok := d.Member(card, "extension"); ok && d.Kind(ext) == splice.KindObject {
				obs.Orders = append(obs.Orders, p.legacyCard(ext, cp+".extension"))
			}
			sugs, ok := d.Member(card, "suggestions")
			if !ok || d.Kind(sugs) != splice.KindArray {
				continue
			}
			for j, sug := range d.Elems(sugs) {
				if d.Kind(sug) != splice.KindObject {
					continue
				}
				actions, ok := d.Member(sug, "actions")
				if !ok || d.Kind(actions) != splice.KindArray {
					continue
				}
				for k, a := range d.Elems(actions) {
					path := fmt.Sprintf("%s.suggestions[%d].actions[%d]", cp, j, k)
					if o, ok := p.action(a, CRDSourceCardSuggestion, path); ok {
						obs.Orders = append(obs.Orders, o)
					}
				}
			}
		}
	}
	return obs, nil
}

type crdReader struct {
	d   *splice.Doc
	src []byte
}

func (p crdReader) span(n splice.NodeID) json.RawMessage {
	s, e := p.d.Span(n)
	return append(json.RawMessage(nil), p.src[s:e]...)
}

func (p crdReader) str(obj splice.NodeID, key string) (string, bool) {
	v, ok := p.d.Member(obj, key)
	if !ok || p.d.Kind(v) != splice.KindString {
		return "", false
	}
	s, err := p.d.StringValue(v)
	return s, err == nil
}

func (p crdReader) action(a splice.NodeID, src CRDObservationSource, path string) (CRDObservedOrder, bool) {
	d := p.d
	if d.Kind(a) != splice.KindObject {
		return CRDObservedOrder{}, false
	}
	typ, _ := p.str(a, "type")
	if typ != "update" && typ != "create" {
		return CRDObservedOrder{}, false
	}
	res, ok := d.Member(a, "resource")
	if !ok || d.Kind(res) != splice.KindObject {
		return CRDObservedOrder{}, false
	}
	exts, ok := d.Member(res, "extension")
	if !ok || d.Kind(exts) != splice.KindArray {
		return CRDObservedOrder{}, false
	}
	var cis []CoverageInformation
	for _, e := range d.Elems(exts) {
		if d.Kind(e) != splice.KindObject {
			continue
		}
		if url, _ := p.str(e, "url"); url == CoverageInformationURL {
			cis = append(cis, p.coverageInformation(e))
		}
	}
	if len(cis) == 0 {
		return CRDObservedOrder{}, false
	}
	rt, _ := p.str(res, "resourceType")
	id, _ := p.str(res, "id")
	return CRDObservedOrder{
		Source: src, Path: path, ActionType: typ, ResourceType: rt, ID: id,
		Order: p.span(res), Coverage: cis,
	}, true
}

// valueOf returns the sub-extension's value member of the given kind.
func (p crdReader) valueOf(sub splice.NodeID, key string, kind splice.Kind) (splice.NodeID, bool) {
	v, ok := p.d.Member(sub, key)
	return v, ok && p.d.Kind(v) == kind
}

func (p crdReader) coverageInformation(e splice.NodeID) CoverageInformation {
	d := p.d
	ci := CoverageInformation{Raw: p.span(e)}
	subs, ok := d.Member(e, "extension")
	if !ok || d.Kind(subs) != splice.KindArray {
		ci.Nonconformant = append(ci.Nonconformant, "coverage-information has no sub-extension array")
		return ci
	}
	for i, sub := range d.Elems(subs) {
		if d.Kind(sub) != splice.KindObject {
			ci.Nonconformant = append(ci.Nonconformant, fmt.Sprintf("sub-extension %d is not an object", i))
			continue
		}
		url, ok := p.str(sub, "url")
		if !ok {
			ci.Nonconformant = append(ci.Nonconformant, fmt.Sprintf("sub-extension %d has no url", i))
			continue
		}
		raw := p.span(sub)
		ci.Values = append(ci.Values, CoverageInformationValue{URL: url, Raw: raw})
		bad := func(want string) {
			ci.Nonconformant = append(ci.Nonconformant, fmt.Sprintf("sub-extension %d (%s) has no %s", i, url, want))
		}
		code := func(dst *string, list *[]string) {
			v, ok := p.valueOf(sub, "valueCode", splice.KindString)
			if !ok {
				bad("valueCode")
				return
			}
			s, _ := d.StringValue(v)
			if list != nil {
				*list = append(*list, s)
			} else if *dst == "" {
				*dst = s
			}
		}
		strValue := func(key string, dst *string, list *[]string) {
			v, ok := p.valueOf(sub, key, splice.KindString)
			if !ok {
				bad(key)
				return
			}
			s, _ := d.StringValue(v)
			if list != nil {
				*list = append(*list, s)
			} else if *dst == "" {
				*dst = s
			}
		}
		reference := func(list *[]string, dst *string) {
			v, ok := p.valueOf(sub, "valueReference", splice.KindObject)
			if !ok {
				bad("valueReference")
				return
			}
			ref, _ := p.str(v, "reference")
			if list != nil {
				*list = append(*list, ref)
			} else if *dst == "" {
				*dst = ref
			}
		}
		object := func(key string, list *[]json.RawMessage) {
			v, ok := p.valueOf(sub, key, splice.KindObject)
			if !ok {
				bad(key)
				return
			}
			*list = append(*list, p.span(v))
		}
		switch url {
		case "coverage":
			reference(nil, &ci.Coverage)
		case "covered":
			code(&ci.Covered, nil)
		case "pa-needed":
			code(&ci.PANeeded, nil)
		case "doc-needed":
			code(nil, &ci.DocNeeded)
		case "doc-purpose":
			code(nil, &ci.DocPurpose)
		case "info-needed":
			code(nil, &ci.InfoNeeded)
		case "billingCode":
			v, ok := p.valueOf(sub, "valueCoding", splice.KindObject)
			if !ok {
				bad("valueCoding")
				continue
			}
			var c CRDCoding
			c.System, _ = p.str(v, "system")
			c.Code, _ = p.str(v, "code")
			c.Display, _ = p.str(v, "display")
			ci.BillingCodes = append(ci.BillingCodes, c)
		case "reason":
			object("valueCodeableConcept", &ci.Reasons)
		case "detail":
			ci.Details = append(ci.Details, raw)
		case "questionnaire":
			strValue("valueCanonical", nil, &ci.Questionnaires)
		case "response":
			reference(&ci.Responses, nil)
		case "dependency":
			reference(&ci.Dependencies, nil)
		case "date":
			strValue("valueDate", &ci.Date, nil)
		case "expiry-date":
			strValue("valueDate", &ci.ExpiryDate, nil)
		case "coverage-assertion-id":
			strValue("valueString", &ci.CoverageAssertionID, nil)
		case "satisfied-pa-id":
			strValue("valueString", &ci.SatisfiedPAID, nil)
		case "contact":
			if _, ok := d.Member(sub, "valueContactDetail"); ok {
				object("valueContactDetail", &ci.Contacts)
			} else {
				object("valueContactPoint", &ci.Contacts)
			}
		default:
			ci.Unknown = append(ci.Unknown, CoverageInformationValue{URL: url, Raw: raw})
		}
	}
	return ci
}

// legacyCard reads the card extension object written by BuildCards.
func (p crdReader) legacyCard(ext splice.NodeID, path string) CRDObservedOrder {
	var c CardCoverage
	_ = json.Unmarshal(p.span(ext), &c) // lenient, as ParseCards always read it
	ci := CoverageInformation{
		Raw:            p.span(ext),
		Covered:        c.Covered,
		PANeeded:       c.PANeeded,
		Questionnaires: c.Questionnaires,
		SatisfiedPAID:  c.SatisfiedPaID,
	}
	return CRDObservedOrder{Source: CRDSourceLegacyCard, Path: path, Coverage: []CoverageInformation{ci}}
}

// CRDResponseInputs are a payer participant's CRD answer: the orders it
// returns with coverage information and any cards for a person to read.
type CRDResponseInputs struct {
	Orders []CRDOrderCoverage
	// Cards are optional human-readable cards; each needs a source label and
	// topic. Coverage information never travels in a card.
	Cards []CRDCard
}

// CRDOrderCoverage is one order from the request and the coverage
// information the participant asserts for it.
type CRDOrderCoverage struct {
	// Order is the order resource exactly as the request carried it. The
	// answer returns these bytes with the coverage information appended to
	// the order's extension array; nothing else changes.
	Order []byte
	// Description is the system action's description (required).
	Description string
	// Coverage holds one coverage-information per Coverage the assertion is
	// made against (at least one).
	Coverage []CoverageInformationInput
}

// CoverageInformationInput is the participant's own coverage assertion. The
// builder writes exactly these values, in the profile's element order, and
// adds nothing. Raw JSON values are written byte for byte.
type CoverageInformationInput struct {
	Coverage   string // Coverage reference (required), e.g. "Coverage/c1"
	Covered    string // required: covered | not-covered | conditional (| indeterminate at 2.2)
	PANeeded   string
	DocNeeded  []string
	DocPurpose []string
	InfoNeeded []string
	// BillingCodes are Codings (system and code required).
	BillingCodes []CRDCoding
	// Reasons are CodeableConcept objects.
	Reasons []json.RawMessage
	// Details are complete detail sub-extensions ({"url":"detail",...}).
	Details        []json.RawMessage
	Questionnaires []string // canonicals, a |version kept as given
	Date           string   // required, YYYY-MM-DD
	// CoverageAssertionID is the participant's own id for this assertion
	// (required); a later DTR request can carry it back as context.
	CoverageAssertionID string
	SatisfiedPAID       string
	// Contacts are ContactPoint objects (lines 2.0, 2.1) or ContactDetail
	// objects (line 2.2).
	Contacts   []json.RawMessage
	ExpiryDate string // lines 2.1 and 2.2 only
}

// CRDCard is a human-readable card.
type CRDCard struct {
	UUID      string
	Summary   string // required, fewer than 140 characters
	Detail    string
	Indicator string // info | warning | critical
	Source    CRDCardSource
	Links     []CRDLink
}

// CRDCardSource names who produced a card: Label is the participant's
// recognizable name; Topic is the card type Coding CRD requires.
type CRDCardSource struct {
	Label string
	URL   string
	Topic CRDCoding
}

// CRDLink is a card link (Type absolute or smart; AppContext for smart
// links only).
type CRDLink struct {
	Label      string
	URL        string
	Type       string
	AppContext string
}

// crdLineCodes are the required value sets of the coverage-information codes
// per CRD line (CRD 2.0.1, 2.1.0 and 2.2.1 ValueSet coverageInfo,
// coveragePaDetail, AdditionalDocumentation, DocReason, informationNeeded).
type crdLineCodes struct {
	covered, paNeeded, docNeeded, docPurpose, infoNeeded []string
	docNeededRepeats, expiryDate                         bool
}

var crdCodesByLine = map[string]crdLineCodes{
	"2.0": {
		covered:    []string{"not-covered", "covered", "conditional"},
		paNeeded:   []string{"no-auth", "auth-needed", "satisfied", "performpa", "conditional"},
		docNeeded:  []string{"clinical", "admin", "both", "conditional"},
		docPurpose: []string{"withpa", "withclaim", "withorder", "retain-doc", "OTH"},
		infoNeeded: []string{"performer", "location", "timeframe", "contract-window", "OTH"},
	},
	"2.1": {
		covered:          []string{"not-covered", "covered", "conditional"},
		paNeeded:         []string{"no-auth", "auth-needed", "satisfied", "performpa", "conditional"},
		docNeeded:        []string{"clinical", "admin", "patient", "conditional"},
		docPurpose:       []string{"withpa", "withclaim", "withorder", "retain-doc", "OTH"},
		infoNeeded:       []string{"performer", "location", "timeframe", "contract-window", "OTH"},
		docNeededRepeats: true, expiryDate: true,
	},
	"2.2": {
		covered:          []string{"not-covered", "covered", "conditional", "indeterminate"},
		paNeeded:         []string{"no-auth", "auth-needed", "satisfied", "performpa", "conditional", "indeterminate"},
		docNeeded:        []string{"clinical", "admin", "patient", "conditional", "indeterminate"},
		docPurpose:       []string{"withpa", "withclaim", "withorder", "retain-doc", "OTH"},
		infoNeeded:       []string{"performer", "location", "timeframe", "contract-window", "detail-code", "OTH"},
		docNeededRepeats: true, expiryDate: true,
	},
}

var fhirDate = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$`)

// checkCoverageInformation applies the line's value sets, cardinalities and
// the coverage-information invariants (crd-ci-q1, q2, q3, q4, q5, q6, q8, q9)
// that do not need a terminology server.
func checkCoverageInformation(line string, c CoverageInformationInput) error {
	codes := crdCodesByLine[line]
	fail := func(format string, args ...any) error {
		return fmt.Errorf("shnsdk: BuildCRDResponse: line %s: "+format, append([]any{line}, args...)...)
	}
	if c.Coverage == "" {
		return fail("coverage reference is required")
	}
	if !slices.Contains(codes.covered, c.Covered) {
		return fail("covered %q is not in the line's value set", c.Covered)
	}
	if c.PANeeded != "" && !slices.Contains(codes.paNeeded, c.PANeeded) {
		return fail("pa-needed %q is not in the line's value set", c.PANeeded)
	}
	for field, vals := range map[string][2][]string{
		"doc-needed":  {c.DocNeeded, codes.docNeeded},
		"doc-purpose": {c.DocPurpose, codes.docPurpose},
		"info-needed": {c.InfoNeeded, codes.infoNeeded},
	} {
		for _, v := range vals[0] {
			if !slices.Contains(vals[1], v) {
				return fail("%s %q is not in the line's value set", field, v)
			}
		}
	}
	if len(c.DocNeeded) > 1 && !codes.docNeededRepeats {
		return fail("doc-needed repeats")
	}
	if !fhirDate.MatchString(c.Date) {
		return fail("date %q is not a YYYY-MM-DD date", c.Date)
	}
	if c.ExpiryDate != "" {
		if !codes.expiryDate {
			return fail("expiry-date is not defined at this line")
		}
		if !fhirDate.MatchString(c.ExpiryDate) {
			return fail("expiry-date %q is not a YYYY-MM-DD date", c.ExpiryDate)
		}
	}
	if c.CoverageAssertionID == "" {
		return fail("coverage-assertion-id is required")
	}
	for _, bc := range c.BillingCodes {
		if bc.System == "" || bc.Code == "" {
			return fail("billingCode needs a system and a code")
		}
	}
	for _, q := range c.Questionnaires {
		if q == "" {
			return fail("empty questionnaire canonical")
		}
	}
	hasReason := len(c.Reasons) > 0
	// crd-ci-q2 (2.0.1): a covered or conditional service states pa-needed;
	// (2.1.0, 2.2.1): a not-covered service states no pa-needed.
	if line == "2.0" && c.Covered != "not-covered" && c.PANeeded == "" {
		return fail("pa-needed is required unless the service is not covered")
	}
	if line != "2.0" || c.Covered == "not-covered" {
		if c.Covered == "not-covered" && c.PANeeded != "" {
			return fail("pa-needed must be absent when the service is not covered")
		}
	}
	// crd-ci-q5
	if (c.PANeeded == "satisfied") != (c.SatisfiedPAID != "") {
		return fail("satisfied-pa-id is present exactly when pa-needed is satisfied")
	}
	// crd-ci-q1
	if len(c.Questionnaires) > 0 && len(c.DocNeeded) == 0 {
		return fail("a questionnaire needs doc-needed")
	}
	// crd-ci-q3, in both directions at every line: CRD 2.0.1's expression is
	// info-needed => some value is conditional; 2.1.0 and 2.2.1 state "if and
	// only if" (their expression checks the other direction).
	conditional := c.Covered == "conditional" || c.PANeeded == "conditional" || slices.Contains(c.DocNeeded, "conditional")
	if conditional && len(c.InfoNeeded) == 0 {
		return fail("a conditional assertion needs info-needed")
	}
	if !conditional && len(c.InfoNeeded) > 0 {
		return fail("info-needed is only allowed when covered, pa-needed or doc-needed is conditional")
	}
	// crd-ci-q4, as CRD 2.2.1 states it, at every line: no withpa
	// documentation when no prior authorization is needed (pa-needed
	// satisfied or no-auth, or the service is not covered). CRD 2.0.1 and
	// 2.1.0 state only the satisfied case.
	if slices.Contains(c.DocPurpose, "withpa") && (c.PANeeded == "satisfied" || c.PANeeded == "no-auth" || c.Covered == "not-covered") {
		return fail("doc-purpose withpa is not allowed when no prior authorization is needed")
	}
	if line != "2.0" {
		// crd-ci-q6
		if slices.Contains(c.InfoNeeded, "OTH") && !hasReason {
			return fail("info-needed OTH needs a reason")
		}
	}
	if line == "2.2" {
		// crd-ci-q8
		for _, p := range c.DocPurpose {
			if p != "conditional" && !hasReason {
				return fail("doc-purpose needs a reason")
			}
		}
		// crd-ci-q9
		if (c.Covered == "indeterminate" || c.PANeeded == "indeterminate" || slices.Contains(c.DocNeeded, "indeterminate")) && !hasReason {
			return fail("an indeterminate assertion needs a reason")
		}
	}
	return nil
}

// rawJSONValue returns v's single JSON value without surrounding whitespace,
// checking its kind.
func rawJSONValue(v []byte, kind splice.Kind) ([]byte, *splice.Doc, error) {
	d, err := splice.Scan(v, splice.DefaultLimits())
	if err != nil {
		return nil, nil, err
	}
	if d.Kind(d.Root()) != kind {
		return nil, nil, fmt.Errorf("not a JSON %s", kind)
	}
	s, e := d.Span(d.Root())
	return v[s:e], d, nil
}

type jsonWriter struct{ b bytes.Buffer }

func (w *jsonWriter) raw(s string)   { w.b.WriteString(s) }
func (w *jsonWriter) str(s string)   { w.b.Write(cdexJSONString(s)) }
func (w *jsonWriter) bytes(v []byte) { w.b.Write(v) }

// member writes ,"key":"value" (or without the comma when first).
func (w *jsonWriter) member(first bool, key, value string) {
	if !first {
		w.raw(",")
	}
	w.str(key)
	w.raw(":")
	w.str(value)
}

func (w *jsonWriter) coding(c CRDCoding) {
	w.raw("{")
	first := true
	for _, kv := range [][2]string{{"system", c.System}, {"code", c.Code}, {"display", c.Display}} {
		if kv[1] == "" {
			continue
		}
		w.member(first, kv[0], kv[1])
		first = false
	}
	w.raw("}")
}

// coverageInformationJSON writes one coverage-information extension.
func coverageInformationJSON(line string, c CoverageInformationInput) ([]byte, error) {
	var w jsonWriter
	w.raw(`{"url":`)
	w.str(CoverageInformationURL)
	w.raw(`,"extension":[`)
	n := 0
	sub := func(url, valueKey string, value func()) {
		if n > 0 {
			w.raw(",")
		}
		n++
		w.raw(`{"url":`)
		w.str(url)
		w.raw(",")
		w.str(valueKey)
		w.raw(":")
		value()
		w.raw("}")
	}
	strSub := func(url, key, v string) { sub(url, key, func() { w.str(v) }) }
	sub("coverage", "valueReference", func() { w.raw(`{"reference":`); w.str(c.Coverage); w.raw("}") })
	strSub("covered", "valueCode", c.Covered)
	if c.PANeeded != "" {
		strSub("pa-needed", "valueCode", c.PANeeded)
	}
	for _, v := range c.DocNeeded {
		strSub("doc-needed", "valueCode", v)
	}
	for _, v := range c.DocPurpose {
		strSub("doc-purpose", "valueCode", v)
	}
	for _, v := range c.InfoNeeded {
		strSub("info-needed", "valueCode", v)
	}
	for _, bc := range c.BillingCodes {
		sub("billingCode", "valueCoding", func() { w.coding(bc) })
	}
	for _, r := range c.Reasons {
		v, _, err := rawJSONValue(r, splice.KindObject)
		if err != nil {
			return nil, fmt.Errorf("reason: %w", err)
		}
		sub("reason", "valueCodeableConcept", func() { w.bytes(v) })
	}
	for _, dt := range c.Details {
		v, d, err := rawJSONValue(dt, splice.KindObject)
		if err != nil {
			return nil, fmt.Errorf("detail: %w", err)
		}
		u, ok := d.Member(d.Root(), "url")
		if url, _ := d.StringValue(u); !ok || url != "detail" {
			return nil, errors.New("detail: not a detail sub-extension")
		}
		if n > 0 {
			w.raw(",")
		}
		n++
		w.bytes(v)
	}
	for _, q := range c.Questionnaires {
		strSub("questionnaire", "valueCanonical", q)
	}
	strSub("date", "valueDate", c.Date)
	strSub("coverage-assertion-id", "valueString", c.CoverageAssertionID)
	if c.SatisfiedPAID != "" {
		strSub("satisfied-pa-id", "valueString", c.SatisfiedPAID)
	}
	// contact is a ContactPoint at CRD 2.0.1 and 2.1.0 and a ContactDetail at
	// 2.2.1; a value with any other element is refused.
	key, allowed := "valueContactPoint", []string{"id", "extension", "system", "value", "use", "rank", "period"}
	if line == "2.2" {
		key, allowed = "valueContactDetail", []string{"id", "extension", "name", "telecom"}
	}
	for _, ct := range c.Contacts {
		v, d, err := rawJSONValue(ct, splice.KindObject)
		if err != nil {
			return nil, fmt.Errorf("contact: %w", err)
		}
		members := d.Members(d.Root())
		if len(members) == 0 {
			return nil, fmt.Errorf("contact: empty %s", strings.TrimPrefix(key, "value"))
		}
		for _, m := range members {
			if !slices.Contains(allowed, m.Name) {
				return nil, fmt.Errorf("contact: %q is not a %s element (line %s)", m.Name, strings.TrimPrefix(key, "value"), line)
			}
		}
		sub("contact", key, func() { w.bytes(v) })
	}
	if c.ExpiryDate != "" {
		strSub("expiry-date", "valueDate", c.ExpiryDate)
	}
	w.raw("]}")
	return w.b.Bytes(), nil
}

// orderWithCoverage returns the order's bytes with the coverage-information
// extensions appended to its extension array (created when absent).
func orderWithCoverage(order []byte, cis [][]byte) (resourceType string, out []byte, err error) {
	d, err := splice.Scan(order, splice.DefaultLimits())
	if err != nil {
		return "", nil, err
	}
	root := d.Root()
	if d.Kind(root) != splice.KindObject {
		return "", nil, errors.New("order is not a JSON object")
	}
	rtNode, ok := d.Member(root, "resourceType")
	rt, _ := d.StringValue(rtNode)
	if !ok || d.Kind(rtNode) != splice.KindString || rt == "" {
		return "", nil, errors.New("order has no resourceType")
	}
	idNode, ok := d.Member(root, "id")
	if id, _ := d.StringValue(idNode); !ok || d.Kind(idNode) != splice.KindString || id == "" {
		return "", nil, errors.New("order has no id")
	}
	var ops []splice.Op
	if ext, ok := d.Member(root, "extension"); ok {
		if d.Kind(ext) != splice.KindArray {
			return "", nil, errors.New("order extension is not an array")
		}
		for _, ci := range cis {
			ops = append(ops, splice.AppendElement(ext, ci))
		}
	} else {
		arr := append([]byte("["), bytes.Join(cis, []byte(","))...)
		ops = append(ops, splice.InsertMember(root, "extension", append(arr, ']')))
	}
	edited, _, err := d.Apply(ops...)
	if err != nil {
		return "", nil, err
	}
	s, e := d.Span(root)
	// The root value's own bytes: Apply keeps any surrounding whitespace,
	// which the answer does not carry.
	tail := len(order) - e
	return rt, edited[s : len(edited)-tail], nil
}

// BuildCRDResponse builds a payer participant's CDS Hooks answer at a CRD
// line ("2.0", "2.1", "2.2"): every order returns in an update system action
// carrying its coverage information; cards is empty unless the participant
// supplies cards for a person to read. The builder writes only the supplied
// values, refuses values the line does not define, and certifies its output
// with CheckCDSHooksResponse.
func BuildCRDResponse(line string, in CRDResponseInputs) ([]byte, error) {
	if _, ok := CRDLineDef(line); !ok {
		return nil, fmt.Errorf("shnsdk: BuildCRDResponse: unknown CRD line %q", line)
	}
	if len(in.Orders) == 0 {
		return nil, errors.New("shnsdk: BuildCRDResponse: no orders")
	}
	var w jsonWriter
	w.raw(`{"cards":[`)
	for i, c := range in.Cards {
		if i > 0 {
			w.raw(",")
		}
		if line == "2.2" && c.UUID == "" {
			// CRD 2.2.1 CRDHooksResponse: cards.uuid 1..1.
			return nil, fmt.Errorf("shnsdk: BuildCRDResponse: card %d: uuid is required at line 2.2", i)
		}
		if err := writeCRDCard(&w, c); err != nil {
			return nil, fmt.Errorf("shnsdk: BuildCRDResponse: card %d: %w", i, err)
		}
	}
	w.raw(`],"systemActions":[`)
	for i, o := range in.Orders {
		if o.Description == "" {
			return nil, fmt.Errorf("shnsdk: BuildCRDResponse: order %d: description is required", i)
		}
		if len(o.Coverage) == 0 {
			return nil, fmt.Errorf("shnsdk: BuildCRDResponse: order %d: no coverage information", i)
		}
		var cis [][]byte
		for _, c := range o.Coverage {
			if err := checkCoverageInformation(line, c); err != nil {
				return nil, err
			}
			ci, err := coverageInformationJSON(line, c)
			if err != nil {
				return nil, fmt.Errorf("shnsdk: BuildCRDResponse: order %d: %w", i, err)
			}
			cis = append(cis, ci)
		}
		_, order, err := orderWithCoverage(o.Order, cis)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: BuildCRDResponse: order %d: %w", i, err)
		}
		if i > 0 {
			w.raw(",")
		}
		w.raw(`{"type":"update","description":`)
		w.str(o.Description)
		w.raw(`,"resource":`)
		w.bytes(order)
		w.raw("}")
	}
	w.raw("]}")
	out := w.b.Bytes()
	if vs := CheckCDSHooksResponse(out, line); len(vs) != 0 {
		return nil, fmt.Errorf("shnsdk: BuildCRDResponse: output fails certification: %+v", vs)
	}
	return out, nil
}

func writeCRDCard(w *jsonWriter, c CRDCard) error {
	switch {
	case c.Summary == "" || utf8.RuneCountInString(c.Summary) >= 140:
		return errors.New("summary must be non-empty and shorter than 140 characters")
	case c.Indicator != "info" && c.Indicator != "warning" && c.Indicator != "critical":
		return fmt.Errorf("indicator %q", c.Indicator)
	case c.Source.Label == "":
		return errors.New("source label is required")
	case c.Source.Topic.Code == "" || c.Source.Topic.System == "":
		return errors.New("source topic needs a system and a code")
	}
	w.raw("{")
	first := true
	if c.UUID != "" {
		w.member(true, "uuid", c.UUID)
		first = false
	}
	w.member(first, "summary", c.Summary)
	if c.Detail != "" {
		w.member(false, "detail", c.Detail)
	}
	w.member(false, "indicator", c.Indicator)
	w.raw(`,"source":{`)
	w.member(true, "label", c.Source.Label)
	if c.Source.URL != "" {
		w.member(false, "url", c.Source.URL)
	}
	w.raw(`,"topic":`)
	w.coding(c.Source.Topic)
	w.raw("}")
	if len(c.Links) > 0 {
		w.raw(`,"links":[`)
		for i, l := range c.Links {
			if l.Label == "" || l.URL == "" || (l.Type != "absolute" && l.Type != "smart") ||
				(l.AppContext != "" && l.Type != "smart") {
				return fmt.Errorf("link %d is not a valid link", i)
			}
			if i > 0 {
				w.raw(",")
			}
			w.raw("{")
			w.member(true, "label", l.Label)
			w.member(false, "url", l.URL)
			w.member(false, "type", l.Type)
			if l.AppContext != "" {
				w.member(false, "appContext", l.AppContext)
			}
			w.raw("}")
		}
		w.raw("]")
	}
	w.raw("}")
	return nil
}

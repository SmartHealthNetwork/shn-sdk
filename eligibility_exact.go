package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// EligibilityRequestRefusal is a coverage eligibility request that
// ParseEligibilityRequestMemberExact refuses because it cannot be read one way
// only. Its Error is the text a payer answers the request with: the text a
// payer's Smart Gateway answers the same request with. It names a member, never
// a value.
type EligibilityRequestRefusal struct{ text string }

func (e *EligibilityRequestRefusal) Error() string { return e.text }

// noPatientToBind ends the refusal of a request naming a member its patient is
// read through only in another case.
const noPatientToBind = ": member names are case-sensitive, so the request names no patient to bind"

// ParseEligibilityRequestMemberExact is the member ID a CoverageEligibilityRequest's
// patient.reference names, read as a payer's Smart Gateway reads it: by exact member
// names, one way only. FHIR member names are case-sensitive, and a JSON object's
// member names are unique, so a request that breaks either can be read two ways:
// a reader that folds case or keeps the last of a repeated name binds one member,
// and an exact reader binds another or none. Such a request is refused with an
// *EligibilityRequestRefusal, in this order:
//
//   - a member name repeated in any object, exactly or equal to another but for
//     case: "eligibility request repeats the JSON member "patient" in the
//     top-level object: it can be read two ways, so it is not read";
//   - the request's resourceType, its patient, that patient's reference, or, for
//     a "#id" reference, its contained resources, named only in another case:
//     "eligibility request names "Patient" for "patient": member names are
//     case-sensitive, so the request names no patient to bind";
//   - a member value carrying a character a reader replaces, in a body that is not
//     well-formed UTF-8 or holds a \u escape of a lone surrogate: the patient
//     reference itself, or, for a "#id" reference, the contained Patient's member
//     identifier. Readers read the value two ways, so it names no patient.
//
// Any other member is not read, so naming one in another case changes nothing.
//
// The patient reference names its member in any well-formed form: a relative
// ("Patient/<id>") or absolute ("<base>/Patient/<id>") reference, either with
// "/_history/<version>", names the member by its id; a "#id" reference names the
// one contained Patient with that id, whose urn:shn:member identifier is the
// member (a contained id is local, never a member id). A "#id" reference naming
// no contained Patient, or more than one, is an error that is no refusal, as is a
// "#id" reference that is not well-formed. Any other reference is read as
// ParseEligibilityRequestMember reads it, and so is a relative one; any other
// error is that function's. ParseEligibilitySubmission reads a request sent
// alone with this reader, and a Responder answers with that.
func ParseEligibilityRequestMemberExact(data []byte) (memberID string, err error) {
	scan := scanMemberNames(data)
	if scan.repeated {
		dup := scan.named
		if dup == "" {
			dup = "a member name"
		}
		return "", &EligibilityRequestRefusal{"eligibility request repeats " + dup + ": it can be read two ways, so it is not read"}
	}
	// Within the strict reader's bounds a member named only in another case is
	// named in the refusal; past them it is refused without naming it.
	strict := scan.strict
	inAnotherCase := func(member, field string) error {
		if strict {
			return &EligibilityRequestRefusal{"eligibility request names " + quotedBounded(member) + " for " + quotedBounded(field) + noPatientToBind}
		}
		return &EligibilityRequestRefusal{"eligibility request names its type or patient only in another case" + noPatientToBind}
	}
	top := exactObject(data)
	if member, ok := namedOnlyInAnotherCase(top, "resourceType"); ok {
		return "", inAnotherCase(member, "resourceType")
	}
	literal, err := ParseEligibilityRequestMember(data)
	if err != nil {
		return "", err
	}
	if member, ok := namedOnlyInAnotherCase(top, "patient"); ok {
		return "", inAnotherCase(member, "patient")
	}
	patient := exactObject(top["patient"])
	if member, ok := namedOnlyInAnotherCase(patient, "reference"); ok {
		return "", inAnotherCase(member, "reference")
	}
	// The lenient reading matches names as strings.EqualFold does, so with
	// neither named only in another case it read this reference.
	var ref string
	_ = json.Unmarshal(patient["reference"], &ref)
	_, local := strings.CutPrefix(ref, "#")
	var contained []any
	if local {
		if member, ok := namedOnlyInAnotherCase(top, "contained"); ok {
			return "", inAnotherCase(member, "contained")
		}
		// Contained resources of another JSON type hold no contained Patient.
		_ = json.Unmarshal(top["contained"], &contained)
	}
	member, ok := patientRefMember(ref, containedObjects(contained))
	switch {
	case ok:
	case local:
		return "", errors.New("shnsdk: the patient reference names no contained Patient, or more than one")
	default:
		// A reference that is not a well-formed one keeps the literal reading:
		// a member id is an identifier value, not limited to a FHIR id's shape.
		member = literal
	}
	if !strict && strings.ContainsRune(member, utf8.RuneError) && memberValueReplaced(top, patient, ref) {
		return "", &EligibilityRequestRefusal{"eligibility request names its patient by a value carrying a replaced character (not well-formed UTF-8 or a lone surrogate escape): readers read it two ways, so the request names no patient to bind"}
	}
	return member, nil
}

// memberValueReplaced reports whether the bytes a request's member value is
// read from carry a character a reader replaces, read as the member is read,
// by exact member names: its patient reference ref's own, or, for a "#id"
// reference, the non-blank member identifier values of the contained Patient
// it names (the caller has resolved the reference, so that Patient is the one
// contained resource with that id). Anything the member is not read from
// (another field, a value of another JSON type) is not looked at.
func memberValueReplaced(top, patient map[string]json.RawMessage, ref string) bool {
	list := func(raw json.RawMessage) []json.RawMessage {
		var l []json.RawMessage
		if json.Unmarshal(raw, &l) != nil {
			return nil
		}
		return l
	}
	str := func(raw json.RawMessage) (string, bool) {
		var v string
		return v, len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &v) == nil
	}
	id, local := strings.CutPrefix(ref, "#")
	if !local {
		return replacesCharacters(patient["reference"])
	}
	for _, raw := range list(top["contained"]) {
		resource := exactObject(raw)
		if pid, _ := str(resource["id"]); pid != id {
			continue
		}
		for _, x := range list(resource["identifier"]) {
			identifier := exactObject(x)
			if system, _ := str(identifier["system"]); system != MemberSystem {
				continue
			}
			if v, ok := str(identifier["value"]); ok && v != "" && replacesCharacters(identifier["value"]) {
				return true
			}
		}
	}
	return false
}

// exactObject is raw read as a JSON object, or nil when it is not one.
func exactObject(raw []byte) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// namedOnlyInAnotherCase is the member of obj named field but for case, when
// obj has no member named field exactly. A body repeating no member name
// holds at most one such member.
func namedOnlyInAnotherCase(obj map[string]json.RawMessage, field string) (string, bool) {
	if _, ok := obj[field]; ok {
		return "", false
	}
	for name := range obj {
		if strings.EqualFold(name, field) {
			return name, true
		}
	}
	return "", false
}

// quotedBounded is v quoted, at most 64 bytes of it.
func quotedBounded(v string) string {
	const most = 64
	if len(v) > most {
		return strconv.Quote(v[:most]) + "…"
	}
	return strconv.Quote(v)
}

// memberScan is what scanMemberNames finds in a body.
type memberScan struct {
	// strict is set when the strict JSON reader reads the body: one
	// well-formed document, within its bounds, repeating no member name.
	strict bool
	// repeated is set when some object repeats a member name, exactly or
	// equal to another but for case.
	repeated bool
	// named names the first repeat and the object holding it,
	// `the JSON member "status" in entry[2].resource`, when the strict reader
	// finds it; "" otherwise.
	named string
}

// scanMemberNames reads body for a member name an object repeats. The strict
// reader decides for a body it reads, and names the first repeat. A body it
// refuses for its own limits (not well-formed UTF-8 or a lone surrogate escape,
// or past its depth or token bound) is walked token by token as encoding/json
// reads it, so a repeat is found wherever that reader would read the body; it
// is not named.
func scanMemberNames(body []byte) memberScan {
	_, err := splice.Scan(body, splice.DefaultLimits())
	var dk *splice.DuplicateKeyError
	switch {
	case err == nil:
		return memberScan{strict: true}
	case errors.As(err, &dk):
		member := fmt.Sprintf("the JSON member %q", capName(dk.Key))
		// A location this cannot read is not guessed: the member alone is named.
		switch p, ok := memberObjectPath(body, dk.Offset); {
		case !ok:
		case p == "":
			member += " in the top-level object"
		default:
			member += " in " + p
		}
		return memberScan{repeated: true, named: member}
	}
	return memberScan{repeated: repeatsFoldedName(body)}
}

// repeatsFoldedName reports whether some object of body, read token by token
// as encoding/json reads it, repeats a member name under case folding. A body
// that reader cannot read to the repeat repeats none.
func repeatsFoldedName(body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	type object struct {
		names map[string]bool
		// name is set when the next token is a member name or the object's end.
		name bool
	}
	var open []*object // nil for an array
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if n := len(open); n > 0 && open[n-1] != nil && open[n-1].name {
			in := open[n-1]
			if tok != json.Delim('}') {
				s, _ := tok.(string)
				k := foldedName(s)
				if in.names[k] {
					return true
				}
				in.names[k], in.name = true, false
				continue
			}
			open = open[:n-1]
		} else {
			switch tok {
			case json.Delim('{'):
				open = append(open, &object{names: map[string]bool{}, name: true})
				continue
			case json.Delim('['):
				open = append(open, nil)
				continue
			case json.Delim(']'):
				open = open[:len(open)-1]
			}
		}
		// A value has ended: the object holding it reads a name next.
		if n := len(open); n > 0 && open[n-1] != nil {
			open[n-1].name = true
		}
	}
}

// foldedName is name with each rune the smallest of its simple case-folding
// orbit: two names fold alike exactly when strings.EqualFold holds.
func foldedName(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		for {
			f := unicode.SimpleFold(r)
			if f <= r {
				r = f
				break
			}
			r = f
		}
		out = utf8.AppendRune(out, r)
	}
	return string(out)
}

// maxNameLen bounds each member name a refusal echoes: the names are the
// sender's, and a refusal names, it does not copy.
const maxNameLen = 64

// capName is a member name as a refusal echoes it: at most maxNameLen runes.
func capName(name string) string {
	if r := []rune(name); len(r) > maxNameLen {
		return string(r[:maxNameLen]) + "…"
	}
	return name
}

// plainName is a member name an element path shows bare.
var plainName = regexp.MustCompile(`^[A-Za-z_-][A-Za-z0-9_-]*$`)

// pathSegment is one member of an element path: `.name` for a plain name,
// `["…"]` for any other, so a name holding a dot or a bracket reads as one
// member, not as nesting.
func pathSegment(path, name string) string {
	plain := plainName.MatchString(name)
	switch {
	case plain && path == "":
		return capName(name)
	case plain:
		return path + "." + capName(name)
	}
	return fmt.Sprintf("%s[%q]", path, capName(name))
}

// memberObjectPath is the path, from the top-level value, of the object
// holding the member name that starts at offset: "entry[2].resource", or ""
// for the top-level object. ok is false when no member name starts there.
func memberObjectPath(body []byte, offset int) (string, bool) {
	type frame struct {
		obj, wantKey bool
		path, key    string
		idx          int
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	// A number beyond float64 is still JSON: read it as text.
	dec.UseNumber()
	var stack []frame
	for {
		before := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		var top *frame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		if top != nil && top.obj && top.wantKey {
			key, _ := tok.(string)
			if int64(offset) >= before && int64(offset) < dec.InputOffset() {
				return top.path, true
			}
			top.key, top.wantKey = key, false
			continue
		}
		// The path of the value this token starts.
		path := ""
		if top != nil {
			if top.obj {
				path, top.wantKey = pathSegment(top.path, top.key), true
			} else {
				path = fmt.Sprintf("%s[%d]", top.path, top.idx)
				top.idx++
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, frame{obj: true, wantKey: true, path: path})
		case json.Delim('['):
			stack = append(stack, frame{path: path})
		}
	}
}

// replacesCharacters reports whether raw, a JSON value, is one a reader
// replaces characters of: not well-formed UTF-8, or carrying a \u escape of a
// lone surrogate.
func replacesCharacters(raw []byte) bool {
	_, err := splice.Scan(raw, splice.DefaultLimits())
	return !utf8.Valid(raw) || errors.Is(err, splice.ErrInvalidUTF8) || errors.Is(err, splice.ErrLoneSurrogate)
}

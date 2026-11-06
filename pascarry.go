package shnsdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// ErrPASRecordNotCarried is a participant's record that an amendment carries as
// written — a supplemental report or the Provenance that attributes it — which
// cannot be carried exactly so: its bytes are not one JSON object (a second
// value after it, a member named twice or spelled in another case, or not
// JSON at all), they are not UTF-8 or hold a lone surrogate escape, they pass
// the build's own limits (16 MiB, nesting 64 deep, 1,000,000 JSON tokens), it
// has no id an entry's fullUrl can be built from (the build never mints one),
// or two entries of one type would share an id (FHIR bdl-7). The amendment is
// not built and nothing is sealed or sent. It is the fidelity of SHN's own
// build, never a judgement of anyone's payload: what a record says, its
// Provenance's target included, is carried as written.
var ErrPASRecordNotCarried = errors.New("a participant's record cannot be carried as written, so the request is not built: SHN carries a participant's record byte for byte or not at all (fidelity of SHN's own build)")

func pasRecordNotCarried(what, why string) error {
	return fmt.Errorf("%w: %s %s", ErrPASRecordNotCarried, what, why)
}

// pasCarriedObject returns b, a participant's record of type rt, as the bytes a
// Bundle carries: exactly one JSON object, in UTF-8, without the white space
// around it (JSON's own white space alone), naming each member once in each object whatever its case. Anything
// else is refused (ErrPASRecordNotCarried) rather than encoded again: a record
// that repeats a member name is two records, and readers that keep the first,
// the last, or match names ignoring case read different ones, so the build
// cannot say which one it carries (the rule the Coverage record is read by,
// checkCoverageRepeatedNames).
func pasCarriedObject(what, rt string, b []byte) ([]byte, error) {
	t := bytes.Trim(b, jsonWhiteSpace)
	switch {
	case len(t) == 0 || t[0] != '{':
		return nil, pasRecordNotCarried(what, "is not a JSON object")
	case !utf8.Valid(t):
		return nil, pasRecordNotCarried(what, "is not UTF-8")
	case !json.Valid(t):
		return nil, pasRecordNotCarried(what, "is not JSON, or not exactly one JSON object")
	}
	if _, err := splice.Scan(t, splice.DefaultLimits()); err != nil {
		var dk *splice.DuplicateKeyError
		if errors.As(err, &dk) {
			return nil, pasRecordNotCarried(what, fmt.Sprintf("repeats a member name %q in %s (a name appears once in an object, whatever its case): readers that keep the first, the last, or match names ignoring case read different records", dk.Key, jsonObjectPathAt(t, dk.Offset, rt)))
		}
		return nil, pasRecordNotCarried(what, "is not one strict JSON document: "+err.Error())
	}
	return t, nil
}

// jsonWhiteSpace is the white space JSON allows around a value (RFC 8259 §2):
// a record with anything else around it is not exactly one JSON object.
const jsonWhiteSpace = " \t\r\n"

// pasCarry collects the participant records a Bundle carries as written. The
// builder places each by a placeholder (hold) while it encodes its own
// content, and splices the record's bytes in at the end (encode), so no
// encoder ever touches them.
//
// A placeholder is keyed to the Bundle's own content: the Bundle is encoded
// once with a key of zeros, the key is the SHA-256 of that encoding, and the
// Bundle is encoded again with each placeholder carrying that key. The text a
// participant wrote in every other record is part of what the key digests, so
// no such text can hold a placeholder, a request carrying the zero-keyed one
// included; text inside a carried record can, but carried records are never
// searched. The build stays deterministic.
type pasCarry struct {
	records [][]byte
	holds   []json.RawMessage
}

// pasCarryKeyZero is the key the first encoding's placeholders carry.
var pasCarryKeyZero = strings.Repeat("0", 64)

// pasCarryPlaceholder is the JSON value standing in for the ith carried
// record until the splice, under key (64 hex digits): a string no builder
// writes, the same length under every key.
func pasCarryPlaceholder(key string, i int) []byte {
	return []byte(`"\u0000shn-carried-` + key + `-` + strconv.Itoa(i) + `"`)
}

// hold registers raw, already checked by pasCarriedObject, and returns the
// placeholder the builder encodes in its place. The placeholder's bytes are
// rewritten in place under the content key (encode), so the entry holding
// it must keep the returned slice, not a copy of its bytes.
func (c *pasCarry) hold(raw []byte) json.RawMessage {
	c.records = append(c.records, raw)
	ph := json.RawMessage(pasCarryPlaceholder(pasCarryKeyZero, len(c.records)-1))
	c.holds = append(c.holds, ph)
	return ph
}

// encode returns the Bundle enc encodes, with each held record spliced in at
// its placeholder: enc runs twice, the second time under the content key.
func (c *pasCarry) encode(enc func() ([]byte, error)) ([]byte, error) {
	first, err := enc()
	if err != nil || len(c.records) == 0 {
		return first, err
	}
	sum := sha256.Sum256(first)
	key := hex.EncodeToString(sum[:])
	for i, ph := range c.holds {
		copy(ph, pasCarryPlaceholder(key, i))
	}
	keyed, err := enc()
	if err != nil {
		return nil, err
	}
	return c.splice(keyed, key)
}

// splice replaces each placeholder under key in bundle with its record's
// bytes. Each placeholder must occur exactly once in what the builder
// encoded, or the build is refused rather than guess which is the record's
// place; the records are spliced last to first, so none is ever searched.
func (c *pasCarry) splice(bundle []byte, key string) ([]byte, error) {
	type spot struct{ at, i int }
	spots := make([]spot, 0, len(c.records))
	for i := range c.records {
		ph := pasCarryPlaceholder(key, i)
		if bytes.Count(bundle, ph) != 1 {
			return nil, pasRecordNotCarried("a carried record", "could not be placed exactly once in the Bundle: another part of the request holds the text the build places it by, and the build does not guess which is the record's place")
		}
		spots = append(spots, spot{bytes.Index(bundle, ph), i})
	}
	slices.SortFunc(spots, func(a, b spot) int { return b.at - a.at })
	out := slices.Clone(bundle)
	for _, s := range spots {
		ph := pasCarryPlaceholder(key, s.i)
		out = slices.Concat(out[:s.at], c.records[s.i], out[s.at+len(ph):])
	}
	return out, nil
}

// pasCarriedResource checks raw is a participant's record of type rt that a
// Bundle can carry as written (pasCarriedObject) under a fullUrl built from
// its own id, and returns its bytes and that id. A record with no id is
// refused: under any other address its relative references would not resolve.
func pasCarriedResource(what string, raw []byte, rt string) ([]byte, string, error) {
	t, err := pasCarriedObject(what, rt, raw)
	if err != nil {
		return nil, "", err
	}
	// Read by exact member name, as FHIR JSON names them: a member spelled in
	// another case is not the one the rule reads.
	var m map[string]json.RawMessage
	var resourceType, id string
	if json.Unmarshal(t, &m) != nil || json.Unmarshal(m["resourceType"], &resourceType) != nil || resourceType != rt {
		return nil, "", pasRecordNotCarried(what, "is not a "+rt)
	}
	if json.Unmarshal(m["id"], &id) != nil || !fhirIDPattern.MatchString(id) {
		return nil, "", pasRecordNotCarried(what, "has no id: its entry's fullUrl is built from its own id, so the references in it resolve in the Bundle (FHIR R4 Bundle resolution)")
	}
	return t, id, nil
}

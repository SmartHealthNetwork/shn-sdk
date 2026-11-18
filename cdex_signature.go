package shnsdk

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// The places a value may hold, as the CDex fulfillment reads signed content:
// a context of signature_gen.go's table (a resource type, a complex data
// type or a backbone element's path), or one of these.
const (
	// cdexUntyped is a place no FHIR R4 type describes.
	cdexUntyped = "\x00untyped"
	// cdexResourcePlace is a place typed Resource: its value names its own
	// type.
	cdexResourcePlace = "Resource"
	// cdexPrimitivePlace is a place typed by a primitive.
	cdexPrimitivePlace = ""
)

// r4MembersFolded is r4Members with each member's name folded (cdexFoldName),
// for a name R4 defines read in another case.
var r4MembersFolded = func() map[string]map[string]string {
	out := make(map[string]map[string]string, len(r4Members))
	for ctx, ms := range r4Members {
		f := make(map[string]string, len(ms))
		for name, to := range ms {
			f[cdexFoldName(name)] = to
		}
		out[ctx] = f
	}
	return out
}()

// cdexFoldName is name under the case folding the parser applies to member
// names (strings.EqualFold's): cdexFoldName(a) == cdexFoldName(b) exactly
// when strings.EqualFold(a, b). Each rune becomes the least of its
// simple-folding orbit.
func cdexFoldName(name string) string {
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

var (
	cdexFoldedExtension         = cdexFoldName("extension")
	cdexFoldedModifierExtension = cdexFoldName("modifierExtension")
)

// cdexMemberContext is the context of the value of member name at context c,
// or cdexUntyped where R4 defines no such member there. A member R4 defines is
// read by its name in any case.
func cdexMemberContext(c, name string) string {
	if c == cdexUntyped {
		// An extension holds Extensions wherever it is.
		if f := cdexFoldName(name); f == cdexFoldedExtension || f == cdexFoldedModifierExtension {
			return "Extension"
		}
		return cdexUntyped
	}
	if to, ok := r4Members[c][name]; ok {
		return to
	}
	if to, ok := r4MembersFolded[c][cdexFoldName(name)]; ok {
		return to
	}
	return cdexUntyped
}

// cdexSignatureElements reports, for each value of d, whether the
// fulfillment reads it as a FHIR Signature element. It reads one where the
// gateway relay's signed-content check does, and more:
//
//   - an object at a place FHIR R4 types Signature (signature_gen.go), read
//     along its path from its nearest enclosing resource, whatever members it
//     has: an extension's valueSignature, a Task input's or output's, at any
//     depth. A Bundle's or a Provenance's own signature is not one: it is that
//     resource's own (cdexOwnSignature), read with the resource;
//   - an object anywhere with the members a Signature requires (type, when or
//     its extension form _when, and who, by their names in any case), even at
//     a place R4 types otherwise, where the relay reads none. Reading it as a
//     Signature only refuses more.
//
// One pass in document order: a container's values follow it, so each
// value's context is its parent's, read through the member that holds it.
func cdexSignatureElements(d *splice.Doc) []bool {
	sig := make([]bool, d.Len())
	if d.Len() == 0 {
		return sig
	}
	ctx := make([]string, d.Len())
	own := make([]bool, d.Len())
	ctx[0] = cdexResourcePlace
	for i := 0; i < d.Len(); i++ {
		n := splice.NodeID(i)
		switch d.Kind(n) {
		case splice.KindArray:
			for _, el := range d.Elems(n) {
				ctx[el], own[el] = ctx[n], own[n]
			}
		case splice.KindObject:
			// A resource starts its own path where a resource may stand
			// (the root, a place R4 types Resource, or one no type
			// describes); anything else at a resource's place, and a
			// resource anywhere else, is untyped.
			typ, isResource := cdexStringMember(d, n, "resourceType")
			isResource = isResource && typ != ""
			switch {
			case isResource && (ctx[n] == cdexResourcePlace || ctx[n] == cdexUntyped) && r4ResourceTypes[typ]:
				ctx[n] = typ
			case isResource || ctx[n] == cdexResourcePlace || ctx[n] == cdexPrimitivePlace:
				ctx[n] = cdexUntyped
			}
			ms := d.Members(n)
			sig[n] = (ctx[n] == "Signature" && !own[n]) || cdexIsSignature(d, n)
			c := ctx[n]
			for _, m := range ms {
				ctx[m.Value] = cdexMemberContext(c, m.Name)
				own[m.Value] = (c == "Bundle" || c == "Provenance") && ctx[m.Value] == "Signature"
			}
		}
	}
	return sig
}

// cdexStringMemberAnyCase is the string value of obj's member key, its name
// read in any case (strings.EqualFold). A parse never holds two member names
// that fold alike, so at most one member answers.
func cdexStringMemberAnyCase(d *splice.Doc, obj splice.NodeID, key string) (string, bool) {
	v, ok := cdexMemberAnyCase(d, obj, key)
	if !ok || d.Kind(v) != splice.KindString {
		return "", false
	}
	s, err := d.StringValue(v)
	return s, err == nil
}

// cdexMemberAnyCase is the value of obj's member key, its name read in any
// case.
func cdexMemberAnyCase(d *splice.Doc, obj splice.NodeID, key string) (splice.NodeID, bool) {
	for _, m := range d.Members(obj) {
		if strings.EqualFold(m.Name, key) {
			return m.Value, true
		}
	}
	return 0, false
}

// Package signedcontent reads what the signatures in a FHIR R4 JSON document
// cover, as a Smart Gateway reads it before it edits a message: the one rule
// the gateway's relay and this SDK's CDex fulfillment hold content to.
//
// A signature covers a resource, and a region of the document is what it
// covers:
//   - a Signature element, found by its place as FHIR R4 types it (or, at a
//     place no R4 type describes, by its required members), covers the
//     nearest resource that holds it;
//   - a Bundle's or a Provenance's own signature covers that resource, read
//     under its member's name in any case;
//   - a signed Provenance also covers each resource its targets name: by
//     type and id, by a Bundle entry's fullUrl, by "#id" among contained
//     resources, or by "#" from a contained Provenance (the resource
//     containing it, and on while that resource is itself contained), each
//     member that says so read under its name in any case.
//
// Coverage gives those regions, RootCarrier the signature over a whole
// document, and OwnSignatureMember the member a Bundle or a Provenance holds
// its own signature in.
//
// The package follows the module's semantic versioning and makes no other
// promise: what it reads changes when the network's rule does, stated in the
// release notes.
package signedcontent

import "github.com/SmartHealthNetwork/shn-sdk/internal/splice"

// Region is a span of a document's bytes, [Start, End), that a signature
// covers, and Carrier names that signature: "Bundle.signature",
// "Provenance.signature", "Signature in <resource type>" for a Signature
// element inside an R4 resource, or "Signature" otherwise.
type Region struct {
	Start, End int
	Carrier    string
}

// Coverage returns every region the signatures in doc cover, in the order the
// reading finds them, each span with each carrier once. Any two regions nest
// or lie apart. An error is doc the reader refuses: not one JSON value, a
// member named twice in an object (in any case), text that is not UTF-8 or
// holds a lone surrogate escape, or past the reader's limits (16 MiB, nesting
// 64 deep, 1,000,000 JSON tokens).
func Coverage(doc []byte) ([]Region, error) {
	d, err := splice.Scan(doc, splice.Limits{})
	if err != nil {
		return nil, err
	}
	c := coverage(d)
	out := make([]Region, len(c.regions))
	for i, r := range c.regions {
		out[i] = Region{Start: r.start, End: r.end, Carrier: r.carrier}
	}
	return out, nil
}

// RootCarrier names the first signature, in Coverage's order, that covers
// doc's whole root value, or returns "" when none does. Its error is
// Coverage's.
func RootCarrier(doc []byte) (string, error) {
	d, err := splice.Scan(doc, splice.Limits{})
	if err != nil {
		return "", err
	}
	start, end := d.Span(d.Root())
	for _, r := range coverage(d).regions {
		if r.start <= start && end <= r.end {
			return r.carrier, nil
		}
	}
	return "", nil
}

// OwnSignatureMember reports whether the member called name, in a resource
// whose resourceType is typ, is that resource's own signature: a Bundle's
// signature or a Provenance's, the one member at which FHIR R4 types a
// Signature in either resource, read under its name in any case. It is how
// Coverage finds the signature a Bundle or a Provenance carries for itself
// (the carriers "Bundle.signature" and "Provenance.signature"), so code that
// decides elsewhere whether one of them is signed, by this rule, agrees with
// Coverage on which member is the signature. For any other typ it reports
// false.
func OwnSignatureMember(typ, name string) bool {
	return ownSignatureMember(typ, name)
}

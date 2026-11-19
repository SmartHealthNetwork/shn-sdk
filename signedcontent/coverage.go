package signedcontent

// The reading below is the gateway relay's own, copied so that one rule
// serves both: the platform's SDK parity suite holds the two copies' code
// equal until the relay calls this package instead.

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// parents maps every value to its enclosing container (-1 for the root).
func parents(d *splice.Doc) []splice.NodeID {
	p := make([]splice.NodeID, d.Len())
	if len(p) > 0 {
		p[0] = -1
	}
	for i := 0; i < d.Len(); i++ {
		n := splice.NodeID(i)
		switch d.Kind(n) {
		case splice.KindObject:
			for _, m := range d.Members(n) {
				p[m.Value] = n
			}
		case splice.KindArray:
			for _, el := range d.Elems(n) {
				p[el] = n
			}
		}
	}
	return p
}

type coveredRegion struct {
	start, end int
	carrier    string
}

// coverageMap is what a document's signatures cover: its regions in the
// order coverage found them, each span with each carrier once, indexed so an
// effect finds the regions it touches without reading the others. A region
// is a value's span, so any two either nest or lie apart: sorted by start,
// the outer first, each region's nearest enclosing one (up) makes the
// regions containing a point one chain, no longer than the document's
// nesting, and the regions starting inside a range one run.
type coverageMap struct {
	regions []coveredRegion
	sorted  []int // region indices by start, the outer first
	up      []int // for each sorted position, the nearest enclosing region's position, or -1
	// nested says every two regions nest or lie apart; when they do not,
	// hits reads every region in turn.
	nested bool
}

func newCoverageMap(regions []coveredRegion) coverageMap {
	c := coverageMap{regions: regions, sorted: make([]int, len(regions)), up: make([]int, len(regions)), nested: true}
	for i := range c.sorted {
		c.sorted[i] = i
	}
	slices.SortFunc(c.sorted, func(i, j int) int {
		a, b := regions[i], regions[j]
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(b.end, a.end), cmp.Compare(i, j))
	})
	var stack []int
	for k, i := range c.sorted {
		r := regions[i]
		for len(stack) > 0 && regions[c.sorted[stack[len(stack)-1]]].end <= r.start {
			stack = stack[:len(stack)-1]
		}
		c.up[k] = -1
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			if regions[c.sorted[top]].end < r.end {
				c.nested = false
			}
			c.up[k] = top
		}
		stack = append(stack, k)
	}
	return c
}

type resource struct {
	node      splice.NodeID
	typ, id   string
	fullURL   string
	contained bool
	// container is the value whose contained array holds a contained
	// resource (a resource, in valid R4): what its reference "#" names.
	container splice.NodeID
}

func stringMember(d *splice.Doc, obj splice.NodeID, key string) string {
	v, ok := d.Member(obj, key)
	if !ok || d.Kind(v) != splice.KindString {
		return ""
	}
	s, _ := d.StringValue(v)
	return s
}

// nameAnyCase reports whether a member's name is key in any case
// (foldName): the member a lenient reader takes as key, as memberContext
// reads a member R4 defines. The splice scanner refuses an object holding
// two names that fold alike, so an object has at most one such member.
func nameAnyCase(name, key string) bool {
	return name == key || foldName(name) == foldName(key)
}

// foldedNameAnyCase is nameAnyCase with key already folded.
func foldedNameAnyCase(name, key, folded string) bool {
	return name == key || foldName(name) == folded
}

// memberAnyCase is the value of obj's member named key in any case
// (nameAnyCase).
func memberAnyCase(d *splice.Doc, obj splice.NodeID, key string) (splice.NodeID, bool) {
	folded := foldName(key)
	for _, m := range d.Members(obj) {
		if foldedNameAnyCase(m.Name, key, folded) {
			return m.Value, true
		}
	}
	return 0, false
}

// stringMemberAnyCase is stringMember with the member named key in any case
// (nameAnyCase).
func stringMemberAnyCase(d *splice.Doc, obj splice.NodeID, key string) string {
	v, ok := memberAnyCase(d, obj, key)
	if !ok || d.Kind(v) != splice.KindString {
		return ""
	}
	s, _ := d.StringValue(v)
	return s
}

// heldUnder is, for every value, the name of the member that holds it: ""
// for the root and an array's elements.
func heldUnder(d *splice.Doc) []string {
	names := make([]string, d.Len())
	for i := 0; i < d.Len(); i++ {
		for _, m := range d.Members(splice.NodeID(i)) {
			names[m.Value] = m.Name
		}
	}
	return names
}

// coverage finds what the document's in-payload signatures cover:
//
//   - a Bundle with its own signature (holdsOwnSignature: the member named
//     in any case) covers the whole Bundle;
//   - a Provenance with its own signature covers itself and every resource in the
//     document its target references resolve to (by fullUrl, by
//     type and id, or by #id for contained resources; a contained
//     Provenance's "#" names its container, and each container up while
//     that one is contained); a target that
//     resolves to nothing in the document covers nothing here;
//   - the members that place a resource and resolve a target (a
//     resource's id, a Bundle entry's resource and fullUrl, a contained
//     array, a Provenance's target and a Reference's reference) are read
//     by their names in any case (nameAnyCase), as the signature itself
//     is; resourceType, the JSON format's fixed property and no element,
//     is read both ways, each only refusing more: an object whose
//     resourceType is named in another case is no resource to R4's types,
//     so a Signature in it, its own included, is read by its shape and
//     covers the resource enclosing it, and it is also a resource a
//     target may name, and as a Bundle or a Provenance with its own
//     signature it covers itself and its targets;
//   - any other Signature element (signatureElements: found by its
//     element's FHIR type) covers its nearest enclosing resource, or the
//     whole document when it has none. A Bundle's or a Provenance's own
//     signature is that resource's carrier (holdsOwnSignature), and not
//     also a Signature in it where the resource stands at a place a
//     resource may (signatureElements); at another place, such as a data
//     type's, it may be named both ways, which only refuses more.
func coverage(d *splice.Doc) coverageMap {
	parent := parents(d)
	held := heldUnder(d)
	// byNode are the resources, by their resourceType named exactly: what
	// R4 types (signatureElements) and what encloses a Signature. placed
	// are those and every object whose resourceType is named in another
	// case, which a lenient reader takes as a resource: what a Provenance
	// target names, and a Bundle or a Provenance with its own signature.
	byNode := map[splice.NodeID]*resource{}
	placed := map[splice.NodeID]*resource{}
	var all []*resource
	for i := 0; i < d.Len(); i++ {
		n := splice.NodeID(i)
		if d.Kind(n) != splice.KindObject {
			continue
		}
		typ, exact := stringMember(d, n, "resourceType"), true
		if typ == "" {
			typ, exact = stringMemberAnyCase(d, n, "resourceType"), false
		}
		if typ == "" {
			continue
		}
		r := &resource{node: n, typ: typ, id: stringMemberAnyCase(d, n, "id")}
		if p := parent[n]; p >= 0 {
			if nameAnyCase(held[n], "resource") {
				r.fullURL = stringMemberAnyCase(d, p, "fullUrl")
			}
			if d.Kind(p) == splice.KindArray {
				if pp := parent[p]; pp >= 0 && nameAnyCase(held[p], "contained") {
					r.contained = true
					r.container = pp
				}
			}
		}
		if exact {
			byNode[n] = r
		}
		placed[n] = r
		all = append(all, r)
	}
	// Each span with each carrier is kept once: a later copy of a region
	// is touched exactly when the first is, so it changes no hit.
	var c []coveredRegion
	seen := map[coveredRegion]bool{}
	add := func(n splice.NodeID, carrier string) {
		s, e := d.Span(n)
		if r := (coveredRegion{start: s, end: e, carrier: carrier}); !seen[r] {
			seen[r] = true
			c = append(c, r)
		}
	}
	var targets *targetIndex
	signature := signatureElements(d, byNode)
	for i := 0; i < d.Len(); i++ {
		n := splice.NodeID(i)
		if d.Kind(n) != splice.KindObject {
			continue
		}
		if r := placed[n]; r != nil && holdsOwnSignature(d, n, r.typ) {
			switch r.typ {
			case "Bundle":
				add(n, "Bundle.signature")
			case "Provenance":
				add(n, "Provenance.signature")
				if targets == nil {
					targets = newTargetIndex(all)
				}
				refs, _ := memberAnyCase(d, n, "target")
				// Every "#" target of a Provenance names the same: its
				// containers are covered once.
				hashDone := false
				for _, t := range d.Elems(refs) {
					ref := stringMemberAnyCase(d, t, "reference")
					if ref == "" {
						continue
					}
					if ref == "#" {
						// "#" names the resource that contains the
						// Provenance, read from where it sits; from a
						// Provenance not contained it names nothing.
						// Erring wide, it covers the value whose
						// contained array holds the Provenance and the
						// nearest resource enclosing that value, then the
						// same from that resource while it is itself
						// contained (R4 allows neither a holder that is no
						// resource nor nesting; each only refuses more).
						// The climb stops at the nearest resource read
						// exactly, and goes on from each resource on the
						// way, read either way, that is itself contained.
						if r.contained && !hashDone {
							hashDone = true
							seen := map[splice.NodeID]bool{}
							for work := []splice.NodeID{r.container}; len(work) > 0; {
								c := work[len(work)-1]
								work = work[:len(work)-1]
								if seen[c] {
									continue
								}
								seen[c] = true
								add(c, "Provenance.signature")
								for x := c; x >= 0; x = parent[x] {
									if pr := placed[x]; pr != nil && pr.contained {
										work = append(work, pr.container)
									}
									if byNode[x] != nil {
										add(x, "Provenance.signature")
										break
									}
								}
							}
						}
						continue
					}
					for _, i := range targets.resolve(ref) {
						add(all[i].node, "Provenance.signature")
					}
				}
			}
		}
		if signature[n] {
			enc := parent[n]
			for enc >= 0 && byNode[enc] == nil {
				enc = parent[enc]
			}
			if enc >= 0 && r4ResourceTypes[byNode[enc].typ] {
				add(enc, "Signature in "+byNode[enc].typ)
			} else if enc >= 0 {
				// A resource type that is no FHIR R4 resource type is
				// the payload's own text: the carrier does not repeat it.
				add(enc, "Signature")
			} else {
				add(d.Root(), "Signature")
			}
		}
	}
	return newCoverageMap(c)
}

// targetIndex finds the resources a Provenance target's reference may name
// (by #id, by fullUrl, or by type and id; it errs toward a match, since a
// wider match only refuses more edits) through indexes of the document's
// resources, so each target
// costs the resources it names, not every resource: by #id among contained
// resources, by fullUrl (its version dropped), and by type with the
// resource's own id or the id its fullUrl ends in.
type targetIndex struct {
	contained, fullURL map[string][]int
	typeID             map[[2]string][]int
	// done are the index entries already resolved by an earlier target:
	// every resource in one is already covered, so a later target naming it
	// adds nothing and need not read it again.
	done map[targetKey]bool
}

type targetKey struct {
	by  int // 0 contained, 1 fullUrl, 2 type and id
	key [2]string
}

func newTargetIndex(all []*resource) *targetIndex {
	ix := &targetIndex{contained: map[string][]int{}, fullURL: map[string][]int{}, typeID: map[[2]string][]int{}, done: map[targetKey]bool{}}
	for i, r := range all {
		if r.contained && r.id != "" {
			ix.contained[r.id] = append(ix.contained[r.id], i)
		}
		if r.fullURL == "" {
			if r.id != "" {
				ix.typeID[[2]string{r.typ, r.id}] = append(ix.typeID[[2]string{r.typ, r.id}], i)
			}
			continue
		}
		f := stripHistory(r.fullURL)
		ix.fullURL[f] = append(ix.fullURL[f], i)
		if r.id != "" {
			ix.typeID[[2]string{r.typ, r.id}] = append(ix.typeID[[2]string{r.typ, r.id}], i)
		}
		// A reference by type and id also names a resource with no id of
		// its own whose fullUrl ends in "/<type>/<id>".
		if slash := strings.LastIndex(f, "/"); slash >= 0 {
			if tail := f[slash+1:]; tail != "" && tail != r.id && strings.HasSuffix(f[:slash], "/"+r.typ) {
				ix.typeID[[2]string{r.typ, tail}] = append(ix.typeID[[2]string{r.typ, tail}], i)
			}
		}
	}
	return ix
}

// resolve is the resources ref may name, by their place in all, in that
// order, leaving out those an earlier target already named: exactly the
// resources the reference may name, less ones already covered.
func (ix *targetIndex) resolve(ref string) []int {
	var keys []targetKey
	if strings.HasPrefix(ref, "#") {
		if len(ref) > 1 {
			keys = append(keys, targetKey{0, [2]string{ref[1:]}})
		}
	} else {
		ref = stripHistory(ref)
		keys = append(keys, targetKey{1, [2]string{ref}})
		if segs := strings.Split(ref, "/"); len(segs) >= 2 && segs[len(segs)-1] != "" {
			keys = append(keys, targetKey{2, [2]string{segs[len(segs)-2], segs[len(segs)-1]}})
		}
	}
	var out []int
	for _, k := range keys {
		if ix.done[k] {
			continue
		}
		ix.done[k] = true
		switch k.by {
		case 0:
			out = append(out, ix.contained[k.key[0]]...)
		case 1:
			out = append(out, ix.fullURL[k.key[0]]...)
		case 2:
			out = append(out, ix.typeID[k.key]...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// untyped is the context of any place no FHIR R4 type describes: outside
// every resource, in a resource whose type is not an R4 resource type, in a
// member R4 does not define where it stands, or a value of another kind
// than its place's type (an object at a primitive's place, a resource at a
// data type's, a place for a resource holding none). Unknown is not empty:
// there a Signature is read by its required members.
const untyped = "\x00untyped"

// r4MembersFolded is r4Members by each member's name case-folded
// (foldName): a lenient reader takes a member by its name in any case
// (the splice scanner refuses an object holding two names that fold alike),
// so a Signature is found under any case of the names on its path.
var r4MembersFolded = func() map[string]map[string]string {
	out := make(map[string]map[string]string, len(r4Members))
	for ctx, ms := range r4Members {
		f := make(map[string]string, len(ms))
		for name, to := range ms {
			f[foldName(name)] = to
		}
		out[ctx] = f
	}
	return out
}()

// foldName is name under the case folding a lenient reader applies to
// member names, the splice scanner's (strings.EqualFold's): foldName(a) ==
// foldName(b) exactly when strings.EqualFold(a, b). Each rune becomes the
// least of its simple-folding orbit.
func foldName(name string) string {
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

// Folded names the walk compares a member's name with.
var (
	foldedExtension         = foldName("extension")
	foldedModifierExtension = foldName("modifierExtension")
	foldedType              = foldName("type")
	foldedWhen              = foldName("when")
	foldedWhenExt           = foldName("_when")
	foldedWho               = foldName("who")
)

// memberContext is the context of the value of member name at context c, or
// untyped where R4 defines no such member there.
func memberContext(c, name string) string {
	if c == untyped {
		// An extension holds Extensions wherever it is.
		if f := foldName(name); f == foldedExtension || f == foldedModifierExtension {
			return "Extension"
		}
		return untyped
	}
	if to, ok := r4Members[c][name]; ok {
		return to
	}
	if to, ok := r4MembersFolded[c][foldName(name)]; ok {
		return to
	}
	return untyped
}

// signatureElements reports, for each value of the document, whether it is
// a FHIR Signature element (an object), deciding by structure: the type of
// the place it holds, read from the R4 StructureDefinitions
// (signature_gen.go) along its path from its nearest enclosing resource, so
// a valueSignature in an extension is one at any depth, and an object at a
// place R4 types as anything but Signature is none, whatever its members. A
// Bundle's or a Provenance's own signature is not reported: it is that
// resource's carrier (Bundle.signature, Provenance.signature), one
// signature, not a second.
//
// Where no R4 type describes a place (untyped), an object there with the
// members a Signature requires (type, when or its extension form _when,
// and who, by their names in any case) is reported: reading it as a
// Signature only refuses an edit
// there, or makes an exempt edit disclose it, never lets a signed change
// through unflagged.
//
// One pass in document order: a container's values follow it, so each
// value's context is its parent's, read through the member that holds it.
func signatureElements(d *splice.Doc, byNode map[splice.NodeID]*resource) []bool {
	sig := make([]bool, d.Len())
	if d.Len() == 0 {
		return sig
	}
	ctx := make([]string, d.Len())
	own := make([]bool, d.Len())
	ctx[0] = resourcePlace
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
			switch r := byNode[n]; {
			case r != nil && (ctx[n] == resourcePlace || ctx[n] == untyped) && r4ResourceTypes[r.typ]:
				ctx[n] = r.typ
			case r != nil || ctx[n] == resourcePlace || ctx[n] == primitivePlace:
				ctx[n] = untyped
			}
			switch {
			case ctx[n] == "Signature":
				sig[n] = !own[n]
			}
			c := ctx[n]
			ms := d.Members(n)
			if c == untyped {
				sig[n] = shapedAsSignature(ms)
			}
			for _, m := range ms {
				v := m.Value
				ctx[v] = memberContext(c, m.Name)
				own[v] = ownSignatureMember(c, m.Name)
			}
		}
	}
	return sig
}

// ownSignatureMember reports whether member name of a resource of type typ
// (its resourceType) is that resource's own signature, Bundle.signature or
// Provenance.signature: the member at which FHIR R4 types a Signature in a
// Bundle or a Provenance, by its name in any case. It is the one rule the
// signed-content check reads a resource's own signature by: a gateway that
// decides elsewhere whether a Bundle or a Provenance is signed reads it
// through this rule too, so the two agree on which member is the signature.
//
// Within this package, signatureElements leaves such a member to its
// resource as the resource's own and coverage reads it as that resource's
// carrier, both through this rule, so a signature the one leaves to its
// resource is always the other's carrier.
func ownSignatureMember(typ, name string) bool {
	return (typ == "Bundle" || typ == "Provenance") && memberContext(typ, name) == "Signature"
}

// holdsOwnSignature reports whether object n, a resource of type typ, holds
// its own signature (ownSignatureMember).
func holdsOwnSignature(d *splice.Doc, n splice.NodeID, typ string) bool {
	if typ != "Bundle" && typ != "Provenance" {
		return false
	}
	for _, m := range d.Members(n) {
		if ownSignatureMember(typ, m.Name) {
			return true
		}
	}
	return false
}

// shapedAsSignature reports whether an object's members include those a
// Signature requires (type, when or its extension form _when, and who), by
// their names in any case.
func shapedAsSignature(ms []splice.MemberRef) bool {
	var typ, when, who bool
	for _, m := range ms {
		switch foldName(m.Name) {
		case foldedType:
			typ = true
		case foldedWhen, foldedWhenExt:
			when = true
		case foldedWho:
			who = true
		}
	}
	return typ && when && who
}

// The contexts signature_gen.go gives a place holding a resource and a
// place holding a primitive.
const (
	resourcePlace  = "Resource"
	primitivePlace = ""
)

// stripHistory drops a trailing `/_history/<version>` (one segment) from ref.
// A base whose path contains `/_history/` elsewhere is kept: it is part of the
// reference, never its version.
func stripHistory(ref string) string {
	if i := strings.LastIndex(ref, "/_history/"); i >= 0 && !strings.Contains(ref[i+len("/_history/"):], "/") {
		return ref[:i]
	}
	return ref
}

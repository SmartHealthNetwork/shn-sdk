package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// errCDexSelfCheck reports a fulfillment whose output failed the builder's
// own re-check. It is a fault in this package, never in the inputs.
var errCDexSelfCheck = errors.New("cdex: fulfillment failed its own verification")

// cdexLimited marks err with ErrCDexLimit when the reader's limits (size,
// nesting depth or tokens) stopped it, keeping the reader's own error in the
// chain; any other error is returned as it is.
func cdexLimited(err error) error {
	if errors.Is(err, splice.ErrSizeLimit) || errors.Is(err, splice.ErrDepthLimit) || errors.Is(err, splice.ErrTokenLimit) {
		return fmt.Errorf("%w: %w", ErrCDexLimit, err)
	}
	return err
}

// cdexFulfillableStatus are the request Task statuses a fulfillment may
// complete.
var cdexFulfillableStatus = map[string]bool{
	"requested":   true,
	"received":    true,
	"accepted":    true,
	"in-progress": true,
}

// cdexResultsID is the contained id a fulfillment prefers for its records
// Bundle.
const cdexResultsID = "results"

func cdexStringMember(d *splice.Doc, obj splice.NodeID, key string) (string, bool) {
	v, ok := d.Member(obj, key)
	if !ok || d.Kind(v) != splice.KindString {
		return "", false
	}
	s, err := d.StringValue(v)
	return s, err == nil
}

// cdexJSONString writes s as a JSON string token without HTML escaping.
func cdexJSONString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a Go string always encodes
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// cdexOutputElement is the data-query output that references the contained
// records Bundle.
func cdexOutputElement(id string) []byte {
	b := []byte(`{"type":{"coding":[{"system":"` + hrexTemp + `","code":"data-query"}]},"valueReference":{"reference":`)
	b = append(b, cdexJSONString("#"+id)...)
	return append(b, "}}"...)
}

// cdexRootValue returns the bytes of d's top-level value without the
// whitespace around it.
func cdexRootValue(d *splice.Doc, src []byte) []byte {
	s, e := d.Span(d.Root())
	return src[s:e]
}

// cdexIsSignature reports whether obj has the shape of a FHIR Signature
// datatype: type, when (or its extension form _when) and who, each name read
// in any case (strings.EqualFold).
func cdexIsSignature(d *splice.Doc, obj splice.NodeID) bool {
	if d.Kind(obj) != splice.KindObject {
		return false
	}
	var hasType, hasWhen, hasWho bool
	for _, m := range d.Members(obj) {
		switch {
		case strings.EqualFold(m.Name, "type"):
			hasType = true
		case strings.EqualFold(m.Name, "when"), strings.EqualFold(m.Name, "_when"):
			hasWhen = true
		case strings.EqualFold(m.Name, "who"):
			hasWho = true
		}
	}
	return hasType && hasWhen && hasWho
}

// cdexOwnSignature reports whether obj, a Bundle or a Provenance, carries its
// own signature member, `signature`, its name read in any case (`Signature`,
// `SIGNATURE`; strings.EqualFold). A parse never holds two member names that
// fold alike, so at most one member answers.
func cdexOwnSignature(d *splice.Doc, obj splice.NodeID) bool {
	for _, m := range d.Members(obj) {
		if strings.EqualFold(m.Name, "signature") {
			return true
		}
	}
	return false
}

// cdexIsResource reports whether obj is a FHIR resource (an object with a
// string resourceType).
func cdexIsResource(d *splice.Doc, obj splice.NodeID) bool {
	if d.Kind(obj) != splice.KindObject {
		return false
	}
	rt, ok := cdexStringMember(d, obj, "resourceType")
	return ok && rt != ""
}

// cdexWalk calls visit for every value of d with the nearest resource that
// holds it: a resource above it, never the value itself (-1 when there is
// none). A Signature is read with the resource that holds it, as the relay
// reads one, even when it names a resourceType of its own.
func cdexWalk(d *splice.Doc, visit func(n, enclosing splice.NodeID)) {
	var walk func(n, enclosing splice.NodeID)
	walk = func(n, enclosing splice.NodeID) {
		visit(n, enclosing)
		resource := enclosing
		if cdexIsResource(d, n) {
			resource = n
		}
		switch d.Kind(n) {
		case splice.KindObject:
			for _, m := range d.Members(n) {
				walk(m.Value, resource)
			}
		case splice.KindArray:
			for _, e := range d.Elems(n) {
				walk(e, resource)
			}
		}
	}
	walk(d.Root(), -1)
}

// cdexSignedProvenanceTargets calls match for every target reference of
// every Provenance in d that carries its own signature (cdexOwnSignature), and
// reports whether any call matched. The members that say what the signature
// covers, the Provenance's resourceType and target and each target's
// reference, are read by their names in any case, as the relay reads them.
func cdexSignedProvenanceTargets(d *splice.Doc, match func(ref string) bool) bool {
	hit := false
	for i := 0; i < d.Len() && !hit; i++ {
		n := splice.NodeID(i)
		if d.Kind(n) != splice.KindObject {
			continue
		}
		if rt, _ := cdexStringMemberAnyCase(d, n, "resourceType"); rt != "Provenance" {
			continue
		}
		if !cdexOwnSignature(d, n) {
			continue
		}
		targets, _ := cdexMemberAnyCase(d, n, "target")
		for _, t := range d.Elems(targets) {
			if ref, ok := cdexStringMemberAnyCase(d, t, "reference"); ok && match(ref) {
				hit = true
				break
			}
		}
	}
	return hit
}

// cdexStripHistory cuts ref at its first "/_history/".
func cdexStripHistory(ref string) string {
	if i := strings.Index(ref, "/_history/"); i >= 0 {
		return ref[:i]
	}
	return ref
}

// cdexStripTrailingHistory cuts a version from the end of ref ("/_history/"
// and a last segment), as the relay reads a reference.
func cdexStripTrailingHistory(ref string) string {
	if i := strings.LastIndex(ref, "/_history/"); i >= 0 && !strings.Contains(ref[i+len("/_history/"):], "/") {
		return ref[:i]
	}
	return ref
}

// cdexRefersTo reports whether ref may name the resource of type typ and id
// id: a relative or absolute literal reference, with or without a version,
// whose last two segments are typ and id once a version is cut, either from
// its end, as the relay reads it, or at its first "/_history/". It errs
// toward a match, which only refuses more.
func cdexRefersTo(ref, typ, id string) bool {
	if id == "" {
		return false
	}
	for _, r := range []string{cdexStripTrailingHistory(ref), cdexStripHistory(ref)} {
		if r == typ+"/"+id || strings.HasSuffix(r, "/"+typ+"/"+id) {
			return true
		}
	}
	return false
}

// cdexTaskSignature names the signature that covers the request Task, or
// returns "". taskID is the Task's id, its member's name read in any case.
func cdexTaskSignature(task, records *splice.Doc, taskID string) string {
	for _, s := range cdexSignatureElements(task) {
		if s {
			return "a Signature element in the request Task"
		}
	}
	inTask := func(ref string) bool { return ref == "#" || cdexRefersTo(ref, "Task", taskID) }
	if cdexSignedProvenanceTargets(task, inTask) {
		return "a signed Provenance in the request Task targets it"
	}
	if cdexSignedProvenanceTargets(records, func(ref string) bool { return cdexRefersTo(ref, "Task", taskID) }) {
		return "a signed Provenance in the records targets the request Task"
	}
	// The fulfilled Task contains the records, so a "#" target whose climb
	// reaches the records Bundle names, through it, the Task.
	if cdexHashReachesRoot(records) {
		return "a signed Provenance contained in the records targets the request Task"
	}
	return ""
}

// cdexHashReachesRoot reports whether the "#" target of a Provenance in d
// that carries its own signature names d's root, as the relay reads "#": it
// names the value whose contained array holds the Provenance, and the climb
// goes up from there to the nearest resource whose resourceType is named
// exactly, and on from that resource's container while it is itself
// contained. (A resource on the way whose resourceType is named in another
// case does not stop the climb, which so passes its container too.) The
// members that place a value, contained and the Provenance's resourceType,
// are read by their names in any case.
func cdexHashReachesRoot(d *splice.Doc) bool {
	parent := make([]splice.NodeID, d.Len())
	held := make([]string, d.Len())
	for i := range parent {
		parent[i] = -1
	}
	for i := 0; i < d.Len(); i++ {
		n := splice.NodeID(i)
		switch d.Kind(n) {
		case splice.KindObject:
			for _, m := range d.Members(n) {
				parent[m.Value], held[m.Value] = n, m.Name
			}
		case splice.KindArray:
			for _, el := range d.Elems(n) {
				parent[el] = n
			}
		}
	}
	exact := func(x splice.NodeID) bool {
		typ, _ := cdexStringMember(d, x, "resourceType")
		return d.Kind(x) == splice.KindObject && typ != ""
	}
	// containerOf is the value whose contained array holds x.
	containerOf := func(x splice.NodeID) (splice.NodeID, bool) {
		if p := parent[x]; p >= 0 && d.Kind(p) == splice.KindArray && strings.EqualFold(held[p], "contained") && parent[p] >= 0 {
			return parent[p], true
		}
		return -1, false
	}
	root := d.Root()
	for i := 0; i < d.Len(); i++ {
		n := splice.NodeID(i)
		if d.Kind(n) != splice.KindObject {
			continue
		}
		if rt, _ := cdexStringMemberAnyCase(d, n, "resourceType"); rt != "Provenance" || !cdexOwnSignature(d, n) {
			continue
		}
		c, ok := containerOf(n)
		if !ok {
			continue
		}
		hash := false
		targets, _ := cdexMemberAnyCase(d, n, "target")
		for _, t := range d.Elems(targets) {
			if ref, ok := cdexStringMemberAnyCase(d, t, "reference"); ok && ref == "#" {
				hash = true
			}
		}
		if !hash {
			continue
		}
		seen := map[splice.NodeID]bool{}
		for work := []splice.NodeID{c}; len(work) > 0; {
			c := work[len(work)-1]
			work = work[:len(work)-1]
			if seen[c] {
				continue
			}
			seen[c] = true
			for x := c; x >= 0; x = parent[x] {
				if x == root {
					return true
				}
				if exact(x) {
					if cc, ok := containerOf(x); ok {
						work = append(work, cc)
					}
					break
				}
			}
		}
	}
	return false
}

// cdexRecordsSignature names the signature that covers the records Bundle
// as a whole, or returns "".
func cdexRecordsSignature(records *splice.Doc) string {
	root := records.Root()
	if cdexOwnSignature(records, root) {
		return "Bundle.signature on the records"
	}
	sig := cdexSignatureElements(records)
	carrier := ""
	cdexWalk(records, func(n, enclosing splice.NodeID) {
		if carrier == "" && enclosing == root && sig[n] {
			carrier = "a Signature element in the records Bundle"
		}
	})
	if carrier != "" {
		return carrier
	}
	// A "#" target names the Provenance's container, and through a contained
	// container the resource holding it, which may be the records Bundle:
	// any "#" refuses, which only refuses more.
	id, _ := cdexStringMemberAnyCase(records, root, "id")
	if cdexSignedProvenanceTargets(records, func(ref string) bool { return ref == "#" || cdexRefersTo(ref, "Bundle", id) }) {
		return "a signed Provenance in the records targets the records Bundle"
	}
	return ""
}

// cdexTakenIDs are the ids a new contained resource may not use: every
// contained resource's id and every local reference in the Task.
func cdexTakenIDs(task *splice.Doc, contained splice.NodeID, hasContained bool) map[string]bool {
	taken := map[string]bool{}
	if hasContained {
		for _, c := range task.Elems(contained) {
			if id, ok := cdexStringMember(task, c, "id"); ok {
				taken[id] = true
			}
		}
	}
	for i := 0; i < task.Len(); i++ {
		if ref, ok := cdexStringMember(task, splice.NodeID(i), "reference"); ok {
			if id, local := strings.CutPrefix(ref, "#"); local && id != "" {
				taken[id] = true
			}
		}
	}
	return taken
}

func cdexFreeID(taken map[string]bool) string {
	if !taken[cdexResultsID] {
		return cdexResultsID
	}
	for n := 1; ; n++ {
		if id := cdexResultsID + "-" + strconv.Itoa(n); !taken[id] {
			return id
		}
	}
}

func buildCDexFulfillment(taskSrc, recordsSrc []byte) (CDexFulfillment, error) {
	task, err := splice.Scan(taskSrc, splice.Limits{})
	if err != nil {
		return CDexFulfillment{}, cdexLimited(fmt.Errorf("cdex: parse request task: %w", err))
	}
	troot := task.Root()
	if rt, _ := cdexStringMember(task, troot, "resourceType"); task.Kind(troot) != splice.KindObject || rt != "Task" {
		return CDexFulfillment{}, fmt.Errorf("cdex: request is not a Task (got %q)", rt)
	}
	statusNode, ok := task.Member(troot, "status")
	if !ok || task.Kind(statusNode) != splice.KindString {
		return CDexFulfillment{}, fmt.Errorf("%w: the Task has no status", ErrCDexTaskStatus)
	}
	if status, _ := task.StringValue(statusNode); !cdexFulfillableStatus[status] {
		return CDexFulfillment{}, fmt.Errorf("%w: %q", ErrCDexTaskStatus, status)
	}
	contained, hasContained := task.Member(troot, "contained")
	if hasContained && task.Kind(contained) != splice.KindArray {
		return CDexFulfillment{}, fmt.Errorf("cdex: request Task contained is not an array")
	}
	output, hasOutput := task.Member(troot, "output")
	if hasOutput && task.Kind(output) != splice.KindArray {
		return CDexFulfillment{}, fmt.Errorf("cdex: request Task output is not an array")
	}

	records, err := splice.Scan(recordsSrc, splice.Limits{})
	if err != nil {
		return CDexFulfillment{}, cdexLimited(fmt.Errorf("cdex: parse records bundle: %w", err))
	}
	rroot := records.Root()
	if rt, _ := cdexStringMember(records, rroot, "resourceType"); records.Kind(rroot) != splice.KindObject || rt != "Bundle" {
		return CDexFulfillment{}, fmt.Errorf("cdex: records are not a Bundle (got %q)", rt)
	}

	taskID, _ := cdexStringMemberAnyCase(task, troot, "id")
	if carrier := cdexTaskSignature(task, records, taskID); carrier != "" {
		return CDexFulfillment{}, fmt.Errorf("%w: %s", ErrCDexSignedContent, carrier)
	}

	// The records Bundle's contained id.
	taken := cdexTakenIDs(task, contained, hasContained)
	var resultsID string
	var idOp splice.Op
	idNode, hasID := records.Member(rroot, "id")
	switch {
	case hasID && records.Kind(idNode) != splice.KindString:
		return CDexFulfillment{}, fmt.Errorf("cdex: records Bundle id is not a string")
	case hasID:
		own, _ := records.StringValue(idNode)
		if own == "" {
			return CDexFulfillment{}, fmt.Errorf("cdex: records Bundle id is empty")
		}
		if !taken[own] {
			resultsID = own
		} else {
			resultsID = cdexFreeID(taken)
			idOp = splice.Replace(idNode, cdexJSONString(resultsID))
		}
	default:
		resultsID = cdexFreeID(taken)
		idOp = splice.InsertMember(rroot, "id", cdexJSONString(resultsID))
	}
	embedded := cdexRootValue(records, recordsSrc)
	if idOp != nil {
		if carrier := cdexRecordsSignature(records); carrier != "" {
			return CDexFulfillment{}, fmt.Errorf("%w: %s", ErrCDexSignedContent, carrier)
		}
		edited, _, err := records.Apply(idOp)
		if err != nil {
			return CDexFulfillment{}, cdexLimited(fmt.Errorf("cdex: write records bundle id: %w", err))
		}
		ed, err := splice.Scan(edited, splice.Limits{})
		if err != nil {
			return CDexFulfillment{}, fmt.Errorf("%w: %v", errCDexSelfCheck, err)
		}
		embedded = cdexRootValue(ed, edited)
	}

	outElem := cdexOutputElement(resultsID)
	ops := []splice.Op{splice.Replace(statusNode, []byte(`"completed"`))}
	if hasContained {
		ops = append(ops, splice.AppendElement(contained, embedded))
	} else {
		ops = append(ops, splice.InsertMember(troot, "contained", cdexArrayOf(embedded)))
	}
	if hasOutput {
		ops = append(ops, splice.AppendElement(output, outElem))
	} else {
		ops = append(ops, splice.InsertMember(troot, "output", cdexArrayOf(outElem)))
	}
	out, _, err := task.Apply(ops...)
	if err != nil {
		return CDexFulfillment{}, cdexLimited(fmt.Errorf("cdex: extend request task: %w", err))
	}
	f := CDexFulfillment{Task: out, ResultsID: resultsID}
	f.Copied, err = cdexVerify(task, taskSrc, records, recordsSrc, out, embedded, outElem, idOp != nil, hasID)
	if err != nil {
		return CDexFulfillment{}, err
	}
	return f, nil
}

func cdexArrayOf(v []byte) []byte {
	b := make([]byte, 0, len(v)+2)
	b = append(b, '[')
	b = append(b, v...)
	return append(b, ']')
}

// cdexVerify re-reads the fulfilled Task independently of the splice and
// confirms that it holds exactly the declared changes: every original member
// in order and unchanged except status, the existing contained and output
// elements unchanged, the appended Bundle and output, and nothing else. It
// returns the copied spans.
func cdexVerify(task *splice.Doc, taskSrc []byte, records *splice.Doc, recordsSrc, out, embedded, outElem []byte, idEdited, idReplaced bool) ([]CDexCopiedSpan, error) {
	fail := func(format string, args ...any) ([]CDexCopiedSpan, error) {
		return nil, fmt.Errorf("%w: %s", errCDexSelfCheck, fmt.Sprintf(format, args...))
	}
	od, err := splice.Scan(out, splice.Limits{})
	if err != nil {
		return fail("output does not scan: %v", err)
	}
	value := func(d *splice.Doc, src []byte, n splice.NodeID) []byte {
		s, e := d.Span(n)
		return src[s:e]
	}
	var copied []CDexCopiedSpan
	copyOf := func(fromRecords bool, sd *splice.Doc, sn splice.NodeID, on splice.NodeID) {
		s, e := sd.Span(sn)
		at, _ := od.Span(on)
		copied = append(copied, CDexCopiedSpan{FromRecords: fromRecords, Start: s, End: e, At: at})
	}
	orig := task.Members(task.Root())
	have := od.Members(od.Root())
	seen := map[string]bool{}
	i := 0
	for _, m := range orig {
		if i >= len(have) || have[i].Name != m.Name {
			return fail("member %q moved or vanished", m.Name)
		}
		seen[m.Name] = true
		hv := have[i].Value
		i++
		switch m.Name {
		case "status":
			if !bytes.Equal(value(od, out, hv), []byte(`"completed"`)) {
				return fail("status is not completed")
			}
		case "contained", "output":
			added := embedded
			if m.Name == "output" {
				added = outElem
			}
			oe, he := task.Elems(m.Value), od.Elems(hv)
			if len(he) != len(oe)+1 {
				return fail("%s has %d elements, want %d", m.Name, len(he), len(oe)+1)
			}
			for k, e := range oe {
				if !bytes.Equal(value(task, taskSrc, e), value(od, out, he[k])) {
					return fail("%s element %d changed", m.Name, k)
				}
				copyOf(false, task, e, he[k])
			}
			if !bytes.Equal(value(od, out, he[len(oe)]), added) {
				return fail("%s does not end with the added element", m.Name)
			}
		default:
			if !bytes.Equal(value(task, taskSrc, m.Value), value(od, out, hv)) {
				return fail("member %q changed", m.Name)
			}
			copyOf(false, task, m.Value, hv)
		}
	}
	for _, name := range []string{"contained", "output"} {
		if seen[name] {
			continue
		}
		if i >= len(have) || have[i].Name != name {
			return fail("%s was not added", name)
		}
		added := embedded
		if name == "output" {
			added = outElem
		}
		if !bytes.Equal(value(od, out, have[i].Value), cdexArrayOf(added)) {
			return fail("%s does not hold exactly the added element", name)
		}
		i++
	}
	if i != len(have) {
		return fail("the Task has %d members, want %d", len(have), i)
	}

	// The records Bundle inside the Task.
	containedNode, _ := od.Member(od.Root(), "contained")
	elems := od.Elems(containedNode)
	bundle := elems[len(elems)-1]
	rroot := records.Root()
	if !idEdited {
		copyOf(true, records, rroot, bundle)
		return copied, nil
	}
	rm, bm := records.Members(rroot), od.Members(bundle)
	want := len(rm)
	if !idReplaced {
		want++
	}
	if len(bm) != want {
		return fail("the records Bundle has %d members, want %d", len(bm), want)
	}
	for k, m := range rm {
		if bm[k].Name != m.Name {
			return fail("records member %q moved", m.Name)
		}
		if m.Name == "id" {
			continue
		}
		if !bytes.Equal(value(records, recordsSrc, m.Value), value(od, out, bm[k].Value)) {
			return fail("records member %q changed", m.Name)
		}
		copyOf(true, records, m.Value, bm[k].Value)
	}
	if !idReplaced && bm[len(bm)-1].Name != "id" {
		return fail("the records Bundle id was not added last")
	}
	return copied, nil
}

// cdexIsDataQuery reports whether a CodeableConcept's codings include the
// data-query code.
func cdexIsDataQuery(codings []struct{ System, Code string }) bool {
	for _, c := range codings {
		if c.System == hrexTemp && c.Code == "data-query" {
			return true
		}
	}
	return false
}

func extractCDexEvidence(taskJSON []byte) (drJSON, provJSON []byte, err error) {
	if _, err := splice.Scan(taskJSON, splice.Limits{}); err != nil {
		return nil, nil, fmt.Errorf("cdex: parse task: %w", err)
	}
	var task struct {
		Contained []json.RawMessage `json:"contained"`
		Output    []struct {
			Type struct {
				Coding []struct{ System, Code string } `json:"coding"`
			} `json:"type"`
			ValueReference *struct {
				Reference string `json:"reference"`
			} `json:"valueReference"`
		} `json:"output"`
	}
	if e := json.Unmarshal(taskJSON, &task); e != nil {
		return nil, nil, fmt.Errorf("cdex: parse task: %w", e)
	}
	last := -1
	for i, o := range task.Output {
		if cdexIsDataQuery(o.Type.Coding) {
			last = i
		}
	}
	if last < 0 {
		return nil, nil, fmt.Errorf("cdex: completed Task has no data-query output")
	}
	vr := task.Output[last].ValueReference
	if vr == nil {
		return nil, nil, fmt.Errorf("cdex: the Task's data-query output has no valueReference")
	}
	id, local := strings.CutPrefix(vr.Reference, "#")
	if !local || id == "" {
		return nil, nil, fmt.Errorf("cdex: data-query output reference %q is not a contained resource", vr.Reference)
	}
	var target json.RawMessage
	targetType := ""
	for _, c := range task.Contained {
		var head struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if json.Unmarshal(c, &head) != nil || head.ID != id {
			continue
		}
		if target != nil {
			return nil, nil, fmt.Errorf("cdex: data-query output references #%s, which more than one contained resource carries", id)
		}
		target, targetType = c, head.ResourceType
	}
	switch {
	case target == nil:
		return nil, nil, fmt.Errorf("cdex: data-query output references #%s, which the Task does not contain", id)
	case targetType != "Bundle":
		return nil, nil, fmt.Errorf("cdex: data-query output references #%s, a %s, not a Bundle", id, targetType)
	}
	return extractEvidenceFromBundle(target)
}

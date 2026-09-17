package shnsdk

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// CRDRequestInputs are a provider participant's CDS Hooks request: the hook
// its workflow fires, that hook's context, and the prefetch values it holds.
// Every resource value is the participant's own record and travels as its
// exact bytes. The request names no FHIR server and grants no
// authorization: the payer answers from the context and prefetch alone.
type CRDRequestInputs struct {
	// Hook is the workflow event: order-select, order-sign or order-dispatch.
	Hook string
	// HookInstance is a UUID the caller mints for this call.
	HookInstance string

	// UserID (order-select, order-sign; required) is the ordering user's
	// reference, for example "PractitionerRole/123".
	UserID string
	// EncounterID (order-select, order-sign; optional).
	EncounterID string
	// DraftOrders (order-select, order-sign; required) is the Bundle of
	// draft orders. Every order's subject is the Patient below.
	DraftOrders []byte
	// Selections (order-select; required) name the selected draft orders as
	// "Type/id"; each must be in DraftOrders.
	Selections []string

	// DispatchedOrders (order-dispatch; required) are the dispatched orders'
	// references.
	DispatchedOrders []string
	// Performer (order-dispatch; required) is the performer's reference.
	Performer string
	// FulfillmentTasks (order-dispatch; optional) are Task resources.
	FulfillmentTasks [][]byte

	// Patient is the participant's Patient resource (required). Its id is
	// the context's patientId, and it travels as prefetch "patient".
	Patient []byte
	// Coverage is prefetch "coverage" (required): the participant's Coverage
	// search result for the patient (a searchset Bundle), or the literal
	// null when it holds no Coverage.
	Coverage []byte
	// Prefetch holds any other prefetch values by key: each a resource, a
	// Bundle, or the literal null. A key the participant cannot satisfy is
	// left out.
	Prefetch map[string][]byte
}

var hookInstanceUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// crdRequestValue checks v is one JSON object (or null when nullable) and
// returns its bytes without surrounding whitespace.
func crdRequestValue(name string, v []byte, nullable bool) ([]byte, *splice.Doc, error) {
	if len(v) == 0 {
		return nil, nil, fmt.Errorf("%s is required", name)
	}
	d, err := splice.Scan(v, splice.DefaultLimits())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	s, e := d.Span(d.Root())
	switch k := d.Kind(d.Root()); {
	case k == splice.KindObject:
	case k == splice.KindNull && nullable:
	default:
		return nil, nil, fmt.Errorf("%s is not a JSON object", name)
	}
	return v[s:e], d, nil
}

// patientRefersTo reports whether ref names Patient/id (relative or
// absolute).
func patientRefersTo(ref, id string) bool {
	return ref == "Patient/"+id || strings.HasSuffix(ref, "/Patient/"+id)
}

// bundleEntries returns each entry's resource node of a Bundle document.
func bundleEntries(d *splice.Doc) []splice.NodeID {
	var out []splice.NodeID
	entries, ok := d.Member(d.Root(), "entry")
	if !ok || d.Kind(entries) != splice.KindArray {
		return nil
	}
	for _, e := range d.Elems(entries) {
		if d.Kind(e) != splice.KindObject {
			continue
		}
		if r, ok := d.Member(e, "resource"); ok && d.Kind(r) == splice.KindObject {
			out = append(out, r)
		}
	}
	return out
}

func docString(d *splice.Doc, obj splice.NodeID, key string) string {
	v, ok := d.Member(obj, key)
	if !ok || d.Kind(v) != splice.KindString {
		return ""
	}
	s, _ := d.StringValue(v)
	return s
}

func docRef(d *splice.Doc, obj splice.NodeID, key string) string {
	v, ok := d.Member(obj, key)
	if !ok || d.Kind(v) != splice.KindObject {
		return ""
	}
	return docString(d, v, "reference")
}

// BuildCRDRequest builds a CDS Hooks request for a CRD hook from the
// participant's own records. It refuses inputs that are incomplete for the
// hook, a Patient without an id, a Coverage value that is neither a
// searchset Bundle nor null, and any draft order or Coverage that belongs to
// another patient. It never adds a FHIR server, an authorization, or a
// resource the caller did not supply.
func BuildCRDRequest(in CRDRequestInputs) ([]byte, error) {
	out, err := buildCRDRequest(in)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: BuildCRDRequest: %w", err)
	}
	return out, nil
}

func buildCRDRequest(in CRDRequestInputs) ([]byte, error) {
	ordering := in.Hook == "order-select" || in.Hook == "order-sign"
	switch {
	case !ordering && in.Hook != "order-dispatch":
		return nil, fmt.Errorf("unsupported hook %q (order-select, order-sign, order-dispatch)", in.Hook)
	case !hookInstanceUUID.MatchString(in.HookInstance):
		return nil, fmt.Errorf("hookInstance %q is not a UUID", in.HookInstance)
	}
	patient, pd, err := crdRequestValue("patient", in.Patient, false)
	if err != nil {
		return nil, err
	}
	if docString(pd, pd.Root(), "resourceType") != "Patient" {
		return nil, errors.New("patient is not a Patient resource")
	}
	patientID := docString(pd, pd.Root(), "id")
	if patientID == "" {
		return nil, errors.New("patient has no id")
	}
	coverage, cd, err := crdRequestValue("coverage", in.Coverage, true)
	if err != nil {
		return nil, err
	}
	if cd.Kind(cd.Root()) == splice.KindObject {
		if docString(cd, cd.Root(), "resourceType") != "Bundle" || docString(cd, cd.Root(), "type") != "searchset" {
			return nil, errors.New("coverage is neither a searchset Bundle nor null")
		}
		for _, r := range bundleEntries(cd) {
			if docString(cd, r, "resourceType") == "Coverage" && !patientRefersTo(docRef(cd, r, "beneficiary"), patientID) {
				return nil, errors.New("coverage search result holds another patient's Coverage")
			}
		}
	}

	var w jsonWriter
	w.raw(`{"hook":`)
	w.str(in.Hook)
	w.raw(`,"hookInstance":`)
	w.str(in.HookInstance)
	w.raw(`,"context":{`)
	if ordering {
		if err := writeOrderingContext(&w, in, patientID); err != nil {
			return nil, err
		}
	} else {
		if err := writeDispatchContext(&w, in, patientID); err != nil {
			return nil, err
		}
	}
	w.raw(`},"prefetch":{"patient":`)
	w.bytes(patient)
	w.raw(`,"coverage":`)
	w.bytes(coverage)
	keys := make([]string, 0, len(in.Prefetch))
	for k := range in.Prefetch {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if k == "" || k == "patient" || k == "coverage" {
			return nil, fmt.Errorf("prefetch key %q is not allowed here", k)
		}
		v, _, err := crdRequestValue("prefetch "+k, in.Prefetch[k], true)
		if err != nil {
			return nil, err
		}
		w.raw(",")
		w.str(k)
		w.raw(":")
		w.bytes(v)
	}
	w.raw("}}")
	return w.b.Bytes(), nil
}

func writeOrderingContext(w *jsonWriter, in CRDRequestInputs, patientID string) error {
	switch {
	case in.UserID == "":
		return fmt.Errorf("%s needs userId", in.Hook)
	case len(in.DispatchedOrders) > 0 || in.Performer != "" || len(in.FulfillmentTasks) > 0:
		return fmt.Errorf("%s takes no dispatch context", in.Hook)
	case in.Hook == "order-select" && len(in.Selections) == 0:
		return errors.New("order-select needs selections")
	case in.Hook == "order-sign" && len(in.Selections) > 0:
		return errors.New("order-sign takes no selections")
	}
	orders, od, err := crdRequestValue("draftOrders", in.DraftOrders, false)
	if err != nil {
		return err
	}
	if docString(od, od.Root(), "resourceType") != "Bundle" {
		return errors.New("draftOrders is not a Bundle")
	}
	present := map[string]bool{}
	entries := bundleEntries(od)
	if len(entries) == 0 {
		return errors.New("draftOrders holds no order")
	}
	for _, r := range entries {
		rt, id := docString(od, r, "resourceType"), docString(od, r, "id")
		// The order's patient is its subject, or its patient element for the
		// order types that name it so (NutritionOrder, VisionPrescription);
		// every one present must be the request's patient.
		subject, patient := docRef(od, r, "subject"), docRef(od, r, "patient")
		if (subject == "" && patient == "") ||
			(subject != "" && !patientRefersTo(subject, patientID)) ||
			(patient != "" && !patientRefersTo(patient, patientID)) {
			return fmt.Errorf("draft order %s/%s is not for patient %s", rt, id, patientID)
		}
		present[rt+"/"+id] = true
	}
	for _, s := range in.Selections {
		if !present[s] {
			return fmt.Errorf("selection %q is not among the draft orders", s)
		}
	}
	w.member(true, "userId", in.UserID)
	w.member(false, "patientId", patientID)
	if in.EncounterID != "" {
		w.member(false, "encounterId", in.EncounterID)
	}
	if in.Hook == "order-select" {
		w.raw(`,"selections":[`)
		for i, s := range in.Selections {
			if i > 0 {
				w.raw(",")
			}
			w.str(s)
		}
		w.raw("]")
	}
	w.raw(`,"draftOrders":`)
	w.bytes(orders)
	return nil
}

func writeDispatchContext(w *jsonWriter, in CRDRequestInputs, patientID string) error {
	switch {
	case in.UserID != "" || in.EncounterID != "" || len(in.DraftOrders) > 0 || len(in.Selections) > 0:
		return errors.New("order-dispatch takes no ordering context")
	case len(in.DispatchedOrders) == 0:
		return errors.New("order-dispatch needs dispatchedOrders")
	case in.Performer == "":
		return errors.New("order-dispatch needs a performer")
	}
	w.member(true, "patientId", patientID)
	w.raw(`,"dispatchedOrders":[`)
	for i, o := range in.DispatchedOrders {
		if o == "" {
			return errors.New("empty dispatched order reference")
		}
		if i > 0 {
			w.raw(",")
		}
		w.str(o)
	}
	w.raw("]")
	w.member(false, "performer", in.Performer)
	if len(in.FulfillmentTasks) > 0 {
		w.raw(`,"fulfillmentTasks":[`)
		for i, t := range in.FulfillmentTasks {
			v, _, err := crdRequestValue("fulfillmentTask", t, false)
			if err != nil {
				return err
			}
			if i > 0 {
				w.raw(",")
			}
			w.bytes(v)
		}
		w.raw("]")
	}
	return nil
}

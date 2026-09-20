package shnsdk

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// QuestionnairePackageInputProfile is the Da Vinci DTR profile of the
// $questionnaire-package input Parameters (the same canonical at DTR 2.0.1,
// 2.1.0 and 2.2.0). BuildQuestionnairePackageParameters declares it in
// meta.profile.
const QuestionnairePackageInputProfile = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/dtr-qpackage-input-parameters"

// QuestionnairePackageInputs are a provider participant's
// $questionnaire-package request. Resources travel as the participant's
// exact bytes.
type QuestionnairePackageInputs struct {
	// Coverages are the patient's Coverage resources (at least one; exactly
	// one at DTR 2.2).
	Coverages [][]byte
	// Orders are the orders the questionnaires are for. After CRD, pass the
	// payer's updated order exactly as ParseCRDResponse returned it
	// (CRDObservedOrder.Order).
	Orders [][]byte
	// Questionnaires are questionnaire canonicals exactly as the payer
	// stated them, a |version included.
	Questionnaires []string
	// Context is the payer's coverage-assertion-id (or another context id the
	// payer gave) when the workflow holds one; empty when it holds none.
	Context string
	// Referenced are your own records, read from your system, that the payer
	// needs to resolve references in the coverages or orders — the payor
	// Organization a Coverage names by reference, above all: a payer that maps
	// payer identity at its edge reads the Coverage's payor and refuses one it
	// cannot resolve. Each is embedded exactly, as a referenced parameter;
	// nothing is minted here. RunPriorAuth fills it with the payor Organization
	// the caller's Coverage search result carries, and refuses a search result
	// that carries none before any leg is sent.
	Referenced [][]byte
}

// QuestionnairePackageParameters is a request built by
// BuildQuestionnairePackageParameters.
type QuestionnairePackageParameters struct {
	// Body is the $questionnaire-package input Parameters.
	Body []byte
	// Copied lists every resource copied unchanged from the inputs.
	Copied []QuestionnairePackageCopiedSpan
}

// QuestionnairePackageCopiedSpan says that Body[At:At+(End-Start)] is a copy
// of bytes [Start, End) of an input resource: Coverages[Index] when
// Parameter is "coverage", Orders[Index] when it is "order", Referenced[Index]
// when it is "referenced". The span is the whole JSON value in each.
type QuestionnairePackageCopiedSpan struct {
	Parameter  string
	Index      int
	Start, End int
	At         int
}

// dtrOrderTypes are the order resource types the input profile allows for
// the order parameter at each DTR line.
var dtrOrderTypes = map[string][]string{
	"2.0": {"Appointment", "CommunicationRequest", "DeviceRequest", "Encounter", "ImmunizationRecommendation",
		"MedicationRequest", "NutritionOrder", "ServiceRequest", "SupplyRequest", "VisionPrescription"},
	"2.1": {"Appointment", "CommunicationRequest", "DeviceRequest", "Encounter",
		"MedicationRequest", "NutritionOrder", "ServiceRequest", "SupplyRequest", "VisionPrescription"},
	"2.2": {"Appointment", "CommunicationRequest", "DeviceRequest", "Encounter",
		"MedicationRequest", "NutritionOrder", "ServiceRequest", "SupplyRequest", "VisionPrescription"},
}

// BuildQuestionnairePackageParameters builds the Da Vinci DTR
// $questionnaire-package input Parameters at a DTR line ("2.0", "2.1",
// "2.2"): one coverage parameter per Coverage, one order parameter per
// order, one questionnaire parameter per canonical, and a context parameter
// when in.Context is set, in that order. Every resource is embedded as the
// caller's own bytes (surrounding whitespace aside), and each canonical is
// written exactly as given.
//
// Refusals (an error, never a partial request):
//   - an unknown line;
//   - no Coverage, or (at 2.2, where coverage is 1..1) more than one;
//   - a value that is not one well-formed JSON object with unique member
//     names, a Coverage that is not a Coverage, an order whose type the
//     line's input profile does not allow;
//   - a Coverage without a beneficiary reference, Coverages for different
//     patients, an order whose subject (or patient) is another patient, or a
//     beneficiary, subject or patient that is not a Patient reference;
//   - an empty canonical or one containing whitespace, or a canonical or
//     context that is not valid UTF-8;
//   - no order, questionnaire or context (the profile requires one).
func BuildQuestionnairePackageParameters(line string, in QuestionnairePackageInputs) (QuestionnairePackageParameters, error) {
	out, err := buildQuestionnairePackageParameters(line, in)
	if err != nil {
		return QuestionnairePackageParameters{}, fmt.Errorf("shnsdk: BuildQuestionnairePackageParameters: %w", err)
	}
	return out, nil
}

// pkgResource checks v is one strict JSON object and returns its value span
// and document.
func pkgResource(name string, v []byte) (start, end int, d *splice.Doc, err error) {
	if len(bytes.TrimSpace(v)) == 0 {
		return 0, 0, nil, fmt.Errorf("%s is empty", name)
	}
	d, err = splice.Scan(v, splice.DefaultLimits())
	if err != nil {
		return 0, 0, nil, fmt.Errorf("%s: %w", name, err)
	}
	if d.Kind(d.Root()) != splice.KindObject {
		return 0, 0, nil, fmt.Errorf("%s is not a JSON object", name)
	}
	start, end = d.Span(d.Root())
	return start, end, d, nil
}

// patientKey returns "Patient/<id>" for a relative or absolute Patient
// reference, or "" when ref names no Patient.
func patientKey(ref string) string {
	i := strings.LastIndex(ref, "Patient/")
	if i < 0 || (i > 0 && ref[i-1] != '/') {
		return ""
	}
	id := ref[i+len("Patient/"):]
	if j := strings.Index(id, "/"); j >= 0 {
		id = id[:j] // Patient/<id>/_history/<v>
	}
	if id == "" {
		return ""
	}
	return "Patient/" + id
}

func buildQuestionnairePackageParameters(line string, in QuestionnairePackageInputs) (QuestionnairePackageParameters, error) {
	def, ok := DTRLineDef(line)
	if !ok {
		return QuestionnairePackageParameters{}, fmt.Errorf("unknown DTR line %q", line)
	}
	switch n := len(in.Coverages); {
	case def.QuestionnairePackageCoverageRequired && n != 1:
		return QuestionnairePackageParameters{}, fmt.Errorf("DTR line %s requires exactly one coverage, got %d", line, n)
	case n == 0:
		return QuestionnairePackageParameters{}, fmt.Errorf("DTR line %s requires at least one coverage", line)
	}
	if len(in.Orders) == 0 && len(in.Questionnaires) == 0 && in.Context == "" {
		return QuestionnairePackageParameters{}, errors.New("the request needs an order, questionnaire or context")
	}

	var w jsonWriter
	var copied []QuestionnairePackageCopiedSpan
	w.raw(`{"resourceType":"Parameters","meta":{"profile":[`)
	w.str(QuestionnairePackageInputProfile)
	w.raw(`]},"parameter":[`)
	first := true
	sep := func() {
		if !first {
			w.raw(",")
		}
		first = false
	}
	embed := func(param string, idx int, v []byte, s, e int) {
		sep()
		w.raw(`{"name":`)
		w.str(param)
		w.raw(`,"resource":`)
		copied = append(copied, QuestionnairePackageCopiedSpan{Parameter: param, Index: idx, Start: s, End: e, At: w.b.Len()})
		w.bytes(v[s:e])
		w.raw("}")
	}

	patient := ""
	for i, c := range in.Coverages {
		name := fmt.Sprintf("coverage %d", i)
		s, e, d, err := pkgResource(name, c)
		if err != nil {
			return QuestionnairePackageParameters{}, err
		}
		if docString(d, d.Root(), "resourceType") != "Coverage" {
			return QuestionnairePackageParameters{}, fmt.Errorf("%s is not a Coverage", name)
		}
		beneficiary := docRef(d, d.Root(), "beneficiary")
		p := patientKey(beneficiary)
		switch {
		case beneficiary == "":
			return QuestionnairePackageParameters{}, fmt.Errorf("%s has no Patient beneficiary reference", name)
		case p == "":
			return QuestionnairePackageParameters{}, fmt.Errorf("%s beneficiary %q is not a Patient reference", name, beneficiary)
		case patient != "" && p != patient:
			return QuestionnairePackageParameters{}, fmt.Errorf("%s is for another patient (%s, not %s)", name, p, patient)
		}
		patient = p
		embed("coverage", i, c, s, e)
	}
	for i, o := range in.Orders {
		name := fmt.Sprintf("order %d", i)
		s, e, d, err := pkgResource(name, o)
		if err != nil {
			return QuestionnairePackageParameters{}, err
		}
		rt := docString(d, d.Root(), "resourceType")
		if !slices.Contains(dtrOrderTypes[line], rt) {
			return QuestionnairePackageParameters{}, fmt.Errorf("%s (%q) is not an order type DTR line %s allows", name, rt, line)
		}
		for _, member := range []string{"subject", "patient"} {
			ref := docRef(d, d.Root(), member)
			switch {
			case ref == "":
			case patientKey(ref) == "":
				return QuestionnairePackageParameters{}, fmt.Errorf("%s %s %q is not a Patient reference", name, member, ref)
			case patientKey(ref) != patient:
				return QuestionnairePackageParameters{}, fmt.Errorf("%s is for another patient (%s %q, coverage is for %s)", name, member, ref, patient)
			}
		}
		embed("order", i, o, s, e)
	}
	for i, r := range in.Referenced {
		name := fmt.Sprintf("referenced %d", i)
		s, e, d, err := pkgResource(name, r)
		if err != nil {
			return QuestionnairePackageParameters{}, err
		}
		if docString(d, d.Root(), "resourceType") == "" {
			return QuestionnairePackageParameters{}, fmt.Errorf("%s is not a FHIR resource", name)
		}
		embed("referenced", i, r, s, e)
	}
	for i, q := range in.Questionnaires {
		if q == "" || !utf8.ValidString(q) || strings.ContainsFunc(q, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return QuestionnairePackageParameters{}, fmt.Errorf("questionnaire %d (%q) is not a canonical", i, q)
		}
		sep()
		w.raw(`{"name":"questionnaire","valueCanonical":`)
		w.str(q)
		w.raw("}")
	}
	if in.Context != "" {
		if !utf8.ValidString(in.Context) {
			return QuestionnairePackageParameters{}, errors.New("context is not valid UTF-8")
		}
		sep()
		w.raw(`{"name":"context","valueString":`)
		w.str(in.Context)
		w.raw("}")
	}
	w.raw("]}")

	body := w.b.Bytes()
	if _, err := splice.Scan(body, splice.DefaultLimits()); err != nil {
		return QuestionnairePackageParameters{}, fmt.Errorf("built request is not well formed: %w", err)
	}
	return QuestionnairePackageParameters{Body: body, Copied: copied}, nil
}

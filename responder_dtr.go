package shnsdk

import (
	"errors"
	"net/http"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// NextQuestionAdjudicator is an optional Adjudicator extension for payers
// that serve adaptive questionnaires (SDC $next-question). NextQuestion
// receives the requester's in-progress QuestionnaireResponse exactly as sent
// and returns the payer's answer (the QuestionnaireResponse with the next
// questions), which the Responder returns unchanged. An error is answered
// with 422, or, when it is an *AppAnswerError, with that answer's status,
// body and media type. A Responder whose Adjudicator does not implement it refuses
// next-question requests with 422.
type NextQuestionAdjudicator interface {
	NextQuestion(questionnaireResponse []byte) (answer []byte, err error)
}

// handleDTROperation serves a framed DTR operation: the body is the
// operation's own input.
func (r *Responder) handleDTROperation(body []byte, operation string) handlerResult {
	switch operation {
	case FrameOperationQuestionnairePackage:
		return r.handlePackageParameters(body)
	case FrameOperationNextQuestion:
		qr, ok := nextQuestionInput(body)
		if !ok {
			return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse next-question input failed"}
		}
		return r.nextQuestion(qr)
	default:
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "unsupported DTR operation"}
	}
}

// handlePackageParameters serves a $questionnaire-package input Parameters.
// The request must carry at least one coverage (the input profile's 1..*),
// every Coverage and order must be for one patient, and it must name exactly
// one questionnaire: this Responder serves questionnaires by canonical, one
// per request. The canonical is looked up exactly as sent, a |version
// included.
func (r *Responder) handlePackageParameters(body []byte) handlerResult {
	parseFailed := handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse questionnaire-package parameters failed"}
	notPatient := handlerResult{appStatus: http.StatusBadRequest, errMsg: "questionnaire-package request names a coverage beneficiary or order subject that is not a Patient reference"}
	d, err := splice.Scan(body, splice.DefaultLimits())
	if err != nil || d.Kind(d.Root()) != splice.KindObject || docString(d, d.Root(), "resourceType") != "Parameters" {
		return parseFailed
	}
	params, ok := d.Member(d.Root(), "parameter")
	if !ok || d.Kind(params) != splice.KindArray {
		return parseFailed
	}
	var canonicals []string
	coverages := 0
	patients := map[string]bool{}
	unreadable := false
	// subject is the id the first coverage beneficiary that reads names.
	subject := ""
	for _, p := range d.Elems(params) {
		if d.Kind(p) != splice.KindObject {
			return parseFailed
		}
		name := docString(d, p, "name")
		switch name {
		case "coverage", "order":
			res, ok := d.Member(p, "resource")
			if !ok || d.Kind(res) != splice.KindObject {
				return parseFailed
			}
			if name == "coverage" {
				ref := docRef(d, res, "beneficiary")
				if docString(d, res, "resourceType") != "Coverage" || ref == "" {
					return parseFailed
				}
				// One that only looks like a reference to a patient names a patient this cannot
				// know: not the request's patient, and not refused, as a payer's Smart Gateway at
				// none or observe carries it beside a reference that names the patient.
				beneficiary := requestPatientKey(ref)
				if beneficiary == "" && !pasUnreadablePatientRef(ref) {
					return notPatient
				}
				coverages++
				if id, _, ok := readPatientReference(ref); ok && subject == "" {
					subject = id
				}
				if beneficiary != "" {
					patients[beneficiary] = true
				} else {
					unreadable = true
				}
				continue
			}
			for _, member := range []string{"subject", "patient"} {
				if ref := docRef(d, res, member); ref != "" {
					key := requestPatientKey(ref)
					if key == "" && !pasUnreadablePatientRef(ref) {
						return notPatient
					}
					if key != "" {
						patients[key] = true
					} else {
						unreadable = true
					}
				}
			}
		case "questionnaire":
			v, ok := d.Member(p, "valueCanonical")
			if !ok || d.Kind(v) != splice.KindString {
				return parseFailed
			}
			c, _ := d.StringValue(v)
			canonicals = append(canonicals, c)
		}
	}
	// The request's patient is the one its first coverage names, else, beside a reference that
	// only looks like a patient's, the first patient it names anywhere (packageWalk), as a
	// payer's Smart Gateway reads it. A request that names none, or that the gateway's walk
	// cannot read past, is refused as the gateway refuses it at every level: with that
	// reference's refusal, ahead of the no-coverage one.
	if subject == "" && unreadable {
		w := packageWalk{d: d}
		w.parameterList(params)
		if w.broken || w.first == "" {
			return notPatient
		}
		subject = w.first
	}
	// The gateway binds that patient by its id, and refuses at every level one that does not
	// read as a Patient's id, or names no one.
	if subject != "" {
		id, _, ok := readPatientReference("Patient/" + subject)
		if !ok {
			return handlerResult{appStatus: http.StatusBadRequest, errMsg: "next-question request carries no patient subject"}
		}
		if memberNamesNoOne(id) {
			return handlerResult{appStatus: http.StatusBadRequest, errMsg: msgRequestNamesNoOne}
		}
	}
	switch {
	case coverages == 0:
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "questionnaire-package request has no coverage"}
	// No coverage or order names a patient here only when the walk found it (above).
	case len(patients) > 1:
		return handlerResult{appStatus: http.StatusForbidden, errMsg: "questionnaire-package request covers more than one patient"}
	case len(canonicals) == 0:
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "questionnaire-package request names no questionnaire"}
	case len(canonicals) > 1:
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "questionnaire-package request names more than one questionnaire"}
	}
	return r.questionnairePackage(canonicals[0])
}

// packageWalk reads the patients a questionnaire-package request names as a payer's Smart
// Gateway reads them (packagePatients): a Patient resource's id as written, and the id a
// patient-bearing reference (beneficiary, subject, patient) of any resource names by FHIR's
// reference grammar, in every parameter and part, a Bundle's entries and a nested Parameters'
// included.
type packageWalk struct {
	d *splice.Doc
	// first is the first patient named, in document order.
	first string
	// broken is a shape the gateway's walk stops at and refuses at every level: a parameter or
	// part list that is not an array, an entry of one that is not an object, a resource that
	// is not an object, a nested Parameters with no parameter list.
	broken bool
}

func (w *packageWalk) add(id string) {
	if w.first == "" {
		w.first = id
	}
}

func (w *packageWalk) parameterList(arr splice.NodeID) {
	d := w.d
	if d.Kind(arr) != splice.KindArray {
		w.broken = true
		return
	}
	for _, p := range d.Elems(arr) {
		if d.Kind(p) != splice.KindObject {
			w.broken = true
			return
		}
		if res, ok := d.Member(p, "resource"); ok {
			if w.resource(res); w.broken {
				return
			}
		}
		if parts, ok := d.Member(p, "part"); ok {
			if w.parameterList(parts); w.broken {
				return
			}
		}
	}
}

func (w *packageWalk) resource(n splice.NodeID) {
	d := w.d
	if d.Kind(n) != splice.KindObject {
		w.broken = true
		return
	}
	switch docString(d, n, "resourceType") {
	case "Patient":
		// A Patient with no id could be anyone: it names no patient.
		if id := docString(d, n, "id"); id != "" {
			w.add(id)
		}
	case "Bundle":
		if entries, ok := d.Member(n, "entry"); ok {
			for _, e := range d.Elems(entries) {
				if r, ok := d.Member(e, "resource"); ok {
					if w.resource(r); w.broken {
						return
					}
				}
			}
		}
	case "Parameters":
		params, ok := d.Member(n, "parameter")
		if !ok {
			w.broken = true
			return
		}
		if w.parameterList(params); w.broken {
			return
		}
	}
	for _, key := range []string{"beneficiary", "subject", "patient"} {
		if el, ok := d.Member(n, key); ok {
			if id, _, ok := readPatientReference(docString(d, el, "reference")); ok {
				w.add(id)
			}
		}
	}
}

// nextQuestionInput returns the QuestionnaireResponse of an SDC
// $next-question input: a bare QuestionnaireResponse, or a Parameters with
// exactly one questionnaire-response parameter. The bytes are the request's
// own.
func nextQuestionInput(body []byte) ([]byte, bool) {
	d, err := splice.Scan(body, splice.DefaultLimits())
	if err != nil || d.Kind(d.Root()) != splice.KindObject {
		return nil, false
	}
	qr := d.Root()
	switch docString(d, qr, "resourceType") {
	case "QuestionnaireResponse":
	case "Parameters":
		params, ok := d.Member(d.Root(), "parameter")
		if !ok {
			return nil, false
		}
		found := 0
		for _, p := range d.Elems(params) {
			if docString(d, p, "name") != "questionnaire-response" {
				continue
			}
			found++
			if v, ok := d.Member(p, "resource"); ok {
				qr = v
			}
		}
		if found != 1 || qr == d.Root() || docString(d, qr, "resourceType") != "QuestionnaireResponse" {
			return nil, false
		}
	default:
		return nil, false
	}
	s, e := d.Span(qr)
	return body[s:e], true
}

// nextQuestion answers an adaptive round through the Adjudicator.
func (r *Responder) nextQuestion(questionnaireResponse []byte) (out handlerResult) {
	adaptive, ok := r.cfg.Adjudicator.(NextQuestionAdjudicator)
	if !ok {
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "adaptive questionnaires are not served by this payer"}
	}
	answer, err := adaptive.NextQuestion(questionnaireResponse)
	// The Adjudicator has answered: every return from here on is its answer, or
	// this Responder refusing it (answeredBySystem).
	defer answeredBySystem(&out)
	var ae *AppAnswerError
	if errors.As(err, &ae) {
		return adjudicatorError(err)
	}
	if err != nil || len(answer) == 0 {
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "next-question failed"}
	}
	return handlerResult{payload: answer, contentType: fhirJSON}
}

// requestPatientKey returns "Patient/<id>" for a reference a request names its patient by, read
// by FHIR's reference grammar (readPatientReference): relative, or absolute under any base,
// versioned or not. It is "" for one that names no Patient so ("Patient/" inside another
// reference, a trailing path, an escape), as a payer's Smart Gateway reads the request.
func requestPatientKey(ref string) string {
	if id, _, ok := readPatientReference(ref); ok {
		return "Patient/" + id
	}
	return ""
}

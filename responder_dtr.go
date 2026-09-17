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
				beneficiary := patientKey(ref)
				if beneficiary == "" {
					return notPatient
				}
				coverages++
				patients[beneficiary] = true
				continue
			}
			for _, member := range []string{"subject", "patient"} {
				if ref := docRef(d, res, member); ref != "" {
					key := patientKey(ref)
					if key == "" {
						return notPatient
					}
					patients[key] = true
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
	switch {
	case coverages == 0:
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "questionnaire-package request has no coverage"}
	case len(patients) != 1:
		return handlerResult{appStatus: http.StatusForbidden, errMsg: "questionnaire-package request covers more than one patient"}
	case len(canonicals) == 0:
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "questionnaire-package request names no questionnaire"}
	case len(canonicals) > 1:
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "questionnaire-package request names more than one questionnaire"}
	}
	return r.questionnairePackage(canonicals[0])
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
func (r *Responder) nextQuestion(questionnaireResponse []byte) handlerResult {
	adaptive, ok := r.cfg.Adjudicator.(NextQuestionAdjudicator)
	if !ok {
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "adaptive questionnaires are not served by this payer"}
	}
	answer, err := adaptive.NextQuestion(questionnaireResponse)
	var ae *AppAnswerError
	if errors.As(err, &ae) {
		return adjudicatorError(err)
	}
	if err != nil || len(answer) == 0 {
		return handlerResult{appStatus: http.StatusUnprocessableEntity, errMsg: "next-question failed"}
	}
	return handlerResult{payload: answer, contentType: fhirJSON}
}

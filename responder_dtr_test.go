package shnsdk

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// adaptiveAdjudicator is the PA test adjudicator plus adaptive questionnaire
// support.
type adaptiveAdjudicator struct {
	paTestAdjudicator
	mu     sync.Mutex
	got    [][]byte
	answer []byte
	err    error
}

func (a *adaptiveAdjudicator) NextQuestion(questionnaireResponse []byte) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, bytes.Clone(questionnaireResponse))
	return a.answer, a.err
}

func frameDTROp(t *testing.T, op string, body []byte) []byte {
	t.Helper()
	headers := map[string]string{"Content-Type": "application/fhir+json", FrameHeaderContractVersion: ContractPADTR20}
	if op != "" {
		headers[FrameHeaderOperation] = op
	}
	f, err := EncodeHTTPFrameHeaders(200, headers, body)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func packageRequest(t *testing.T, in QuestionnairePackageInputs) []byte {
	t.Helper()
	got, err := BuildQuestionnairePackageParameters("2.0", in)
	if err != nil {
		t.Fatal(err)
	}
	return got.Body
}

const nqQR = "{\"resourceType\":\"QuestionnaireResponse\",\"status\":\"in-progress\",\"subject\":{\"reference\":\"Patient/p1\"},\"authored\":\"2026-06-12T10:00:00Z\",\"extension\":[{\"url\":\"http://example.org/x\",\"valueDecimal\":1.50}]}"

// TestSDKResponderDTR_AcceptsFramedOperation: the SDK payer responder
// serves a framed questionnaire-package operation from its Parameters, and a
// framed next-question from the SDC input (Parameters or a bare
// QuestionnaireResponse) when its Adjudicator serves adaptive questionnaires.
func TestSDKResponderDTR_AcceptsFramedOperation(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &adaptiveAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, answer: []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","x":1.50}`)}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)

	ask := func(t *testing.T, corr string, payload []byte) []byte {
		t.Helper()
		envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", corr, payload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		return h.openResponse(t, body)
	}

	t.Run("questionnaire-package", func(t *testing.T) {
		req := packageRequest(t, QuestionnairePackageInputs{
			Coverages:      [][]byte{pkgCoverage("c1", "Patient/p1")},
			Orders:         [][]byte{pkgOrder("o1", "Patient/p1")},
			Questionnaires: []string{SupportedQuestionnaireCanonical},
			Context:        "assert-1",
		})
		answer := ask(t, "dtr-op-1", frameDTROp(t, FrameOperationQuestionnairePackage, req))
		q, err := ExtractQuestionnaireFromPackage(answer)
		if err != nil {
			t.Fatalf("answer is not a package: %v %s", err, answer)
		}
		if url, _ := ParseQuestionnaireURL(q); url != SupportedQuestionnaireCanonical {
			t.Fatalf("questionnaire %q", url)
		}
		want, _ := BuildQuestionnairePackage(demoLumbarQuestionnaire())
		if !bytes.Equal(answer, want) {
			t.Fatalf("framed answer differs from the legacy answer:\n%s\n%s", answer, want)
		}
	})

	t.Run("next-question parameters", func(t *testing.T) {
		in := []byte("{\"resourceType\":\"Parameters\",\"parameter\":[{\"name\":\"questionnaire-response\",\"resource\":" + nqQR + "}]}")
		answer := ask(t, "dtr-op-2", frameDTROp(t, FrameOperationNextQuestion, in))
		if !bytes.Equal(answer, adj.answer) {
			t.Fatalf("answer = %s", answer)
		}
		if len(adj.got) != 1 || string(adj.got[0]) != nqQR {
			t.Fatalf("adjudicator received %q, want the QuestionnaireResponse exactly", adj.got)
		}
	})

	t.Run("next-question bare QuestionnaireResponse", func(t *testing.T) {
		answer := ask(t, "dtr-op-3", frameDTROp(t, FrameOperationNextQuestion, []byte(nqQR)))
		if !bytes.Equal(answer, adj.answer) {
			t.Fatalf("answer = %s", answer)
		}
		if len(adj.got) != 2 || string(adj.got[1]) != nqQR {
			t.Fatalf("adjudicator received %q", adj.got)
		}
	})
}

// TestSDKResponderDTR_AcceptsLegacyEnvelope: the older envelope keeps working
// for requesters that do not send the framed operation, framed or bare.
func TestSDKResponderDTR_AcceptsLegacyEnvelope(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &adaptiveAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, answer: []byte(`{"resourceType":"QuestionnaireResponse","status":"completed"}`)}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	want, _ := BuildQuestionnairePackage(demoLumbarQuestionnaire())
	legacy, _ := BuildQuestionnaireFetchWithCoverage(SupportedQuestionnaireCanonical, pkgCoverage("c1", "Patient/p1"))
	nullNQ := []byte(`{"canonical":"` + SupportedQuestionnaireCanonical + `","nextQuestion":null}`)
	for i, payload := range [][]byte{legacy, frameDTROp(t, "", legacy), nullNQ} {
		envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-legacy-"+string(rune('a'+i)), payload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("row %d: status %d: %s", i, resp.StatusCode, body)
		}
		if got := h.openResponse(t, body); !bytes.Equal(got, want) {
			t.Fatalf("row %d: answer %s", i, got)
		}
	}

	// The envelope's adaptive round reaches the adaptive Adjudicator instead of
	// being answered with a package.
	nq := []byte(`{"canonical":"` + SupportedQuestionnaireCanonical + `","nextQuestion":` + nqQR + `}`)
	envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-legacy-nq", nq)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy next-question: status %d: %s", resp.StatusCode, body)
	}
	if got := h.openResponse(t, body); !bytes.Equal(got, adj.answer) || len(adj.got) != 1 || string(adj.got[0]) != nqQR {
		t.Fatalf("legacy next-question: answer %s, adjudicator got %q", got, adj.got)
	}
}

// TestSDKResponderDTR_FramedOperationRefusals: each malformed or unservable
// framed operation is refused with its own status and reason.
func TestSDKResponderDTR_FramedOperationRefusals(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	plain := &paTestAdjudicator{now: h.now}
	_, plainSrv := h.makeResponderSrv(t, responderIdent, plain)
	failing := &adaptiveAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, err: errors.New("no such item")}
	_, adaptiveSrv := h.makeResponderSrv(t, responderIdent, failing)

	cov := pkgCoverage("c1", "Patient/p1")
	pkg := func(in QuestionnairePackageInputs) []byte { return packageRequest(t, in) }
	withQ := QuestionnairePackageInputs{Coverages: [][]byte{cov}, Questionnaires: []string{SupportedQuestionnaireCanonical}}
	rows := []struct {
		name       string
		adaptive   bool
		payload    []byte
		wantStatus int
		wantMsg    string
	}{
		{"unknown operation", false, frameDTROp(t, "populate", pkg(withQ)), 400, "unsupported DTR operation"},
		{"package body not JSON", false, frameDTROp(t, FrameOperationQuestionnairePackage, []byte("nope")), 400, "parse questionnaire-package parameters failed"},
		{"package body is the legacy envelope", false, frameDTROp(t, FrameOperationQuestionnairePackage, mustLegacy(t)), 400, "parse questionnaire-package parameters failed"},
		{"package duplicate member", false, frameDTROp(t, FrameOperationQuestionnairePackage, []byte(`{"resourceType":"Parameters","parameter":[],"parameter":[]}`)), 400, "parse questionnaire-package parameters failed"},
		{"package without coverage", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			[]byte(`{"resourceType":"Parameters","parameter":[{"name":"questionnaire","valueCanonical":"`+SupportedQuestionnaireCanonical+`"}]}`)), 400, "questionnaire-package request has no coverage"},
		{"package without questionnaire", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			pkg(QuestionnairePackageInputs{Coverages: [][]byte{cov}, Context: "a"})), 422, "questionnaire-package request names no questionnaire"},
		{"package with two questionnaires", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			pkg(QuestionnairePackageInputs{Coverages: [][]byte{cov}, Questionnaires: []string{SupportedQuestionnaireCanonical, SupportedQuestionnaireCanonical + "|2"}})), 422, "questionnaire-package request names more than one questionnaire"},
		{"package unknown canonical", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			pkg(QuestionnairePackageInputs{Coverages: [][]byte{cov}, Questionnaires: []string{SupportedQuestionnaireCanonical + "|9"}})), 400, "unknown questionnaire canonical"},
		{"package for two patients", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			[]byte(`{"resourceType":"Parameters","parameter":[{"name":"coverage","resource":`+string(bytes.TrimSpace(cov))+`},{"name":"order","resource":`+string(pkgOrder("o1", "Patient/p2"))+`},{"name":"questionnaire","valueCanonical":"`+SupportedQuestionnaireCanonical+`"}]}`)), 403, "questionnaire-package request covers more than one patient"},
		{"package order subject not a Patient", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			[]byte(`{"resourceType":"Parameters","parameter":[{"name":"coverage","resource":`+string(bytes.TrimSpace(cov))+`},{"name":"order","resource":`+string(pkgOrder("o1", "Group/g1"))+`},{"name":"questionnaire","valueCanonical":"`+SupportedQuestionnaireCanonical+`"}]}`)), 400, "questionnaire-package request names a coverage beneficiary or order subject that is not a Patient reference"},
		{"package beneficiary not a Patient", false, frameDTROp(t, FrameOperationQuestionnairePackage,
			[]byte(`{"resourceType":"Parameters","parameter":[{"name":"coverage","resource":`+string(bytes.TrimSpace(pkgCoverage("c1", "Group/g1")))+`},{"name":"questionnaire","valueCanonical":"`+SupportedQuestionnaireCanonical+`"}]}`)), 400, "questionnaire-package request names a coverage beneficiary or order subject that is not a Patient reference"},
		{"next-question unsupported", false, frameDTROp(t, FrameOperationNextQuestion, []byte(nqQR)), 422, "adaptive questionnaires are not served by this payer"},
		{"legacy next-question unsupported", false, []byte(`{"canonical":"x","nextQuestion":` + nqQR + `}`), 422, "adaptive questionnaires are not served by this payer"},
		{"next-question not a QuestionnaireResponse", true, frameDTROp(t, FrameOperationNextQuestion, []byte(`{"resourceType":"Questionnaire"}`)), 400, "parse next-question input failed"},
		{"next-question parameters without response", true, frameDTROp(t, FrameOperationNextQuestion, []byte(`{"resourceType":"Parameters","parameter":[{"name":"other","valueString":"x"}]}`)), 400, "parse next-question input failed"},
		{"next-question two responses", true, frameDTROp(t, FrameOperationNextQuestion, []byte(`{"resourceType":"Parameters","parameter":[{"name":"questionnaire-response","resource":`+nqQR+`},{"name":"questionnaire-response","resource":`+nqQR+`}]}`)), 400, "parse next-question input failed"},
		{"next-question adjudicator error", true, frameDTROp(t, FrameOperationNextQuestion, []byte(nqQR)), 422, "next-question failed"},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			srv := plainSrv
			if row.adaptive {
				srv = adaptiveSrv
			}
			envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-ref-"+strings.Repeat("x", i+1), row.payload)
			resp := postInbound(t, srv, envBytes, hubHdr)
			body := readBody(t, resp)
			assertError(t, resp, body, row.wantStatus, row.wantMsg)
		})
	}

	// The operation header belongs to the DTR leg only.
	req, _ := BuildConformantOrderSelectRequest([]byte(`{"resourceType":"ServiceRequest","id":"sr","subject":{"reference":"Patient/p1"}}`), cov, "Patient/p1")
	envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-op-1", frameDTROp(t, FrameOperationQuestionnairePackage, req))
	resp := postInbound(t, plainSrv, envBytes, hubHdr)
	body := readBody(t, resp)
	assertError(t, resp, body, http.StatusBadRequest, "operation header is not defined for this transaction type")
}

func mustLegacy(t *testing.T) []byte {
	t.Helper()
	b, err := BuildQuestionnaireFetch(SupportedQuestionnaireCanonical)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

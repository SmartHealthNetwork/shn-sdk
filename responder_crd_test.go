package shnsdk

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// recordingAdjudicator is the PA test adjudicator plus the optional
// coverage-assertion receiver.
type recordingAdjudicator struct {
	paTestAdjudicator
	mu  sync.Mutex
	got []CoverageAssertion
}

func (a *recordingAdjudicator) RecordCoverageAssertion(c CoverageAssertion) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, c)
}

func (a *recordingAdjudicator) recorded() []CoverageAssertion {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.got)
}

// crdDraftOrder returns the draft order a request built by
// buildConformantCRD carries.
func crdDraftOrder(t *testing.T, req []byte) []byte {
	t.Helper()
	var r struct {
		Context struct {
			DraftOrders struct {
				Entry []struct {
					Resource json.RawMessage `json:"resource"`
				} `json:"entry"`
			} `json:"draftOrders"`
		} `json:"context"`
	}
	if err := json.Unmarshal(req, &r); err != nil || len(r.Context.DraftOrders.Entry) == 0 {
		t.Fatalf("draft order: %v %s", err, req)
	}
	return r.Context.DraftOrders.Entry[0].Resource
}

// TestResponderCRD_AnswersWithCoverageSystemAction: the SDK responder answers
// a CRD request the way CRD defines it: no card, one update system action
// returning the requested order (its own bytes plus the coverage information),
// and a coverage assertion id the adjudicator receives once the answer is
// sent.
func TestResponderCRD_AnswersWithCoverageSystemAction(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &recordingAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	date := h.now.UTC().Format("2006-01-02")

	ask := func(t *testing.T, corr string, req []byte) CRDObservedOrder {
		t.Helper()
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", corr, req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		answer := h.openResponse(t, body)
		if v := CheckCDSHooksResponse(answer, "2.0"); len(v) != 0 {
			t.Fatalf("answer fails certification %+v: %s", v, answer)
		}
		if !bytes.HasPrefix(answer, []byte(`{"cards":[],"systemActions":[{"type":"update",`)) {
			t.Fatalf("answer shape: %s", answer)
		}
		obs, err := ParseCRDResponse(answer)
		if err != nil || len(obs.Orders) != 1 || len(obs.Orders[0].Coverage) != 1 {
			t.Fatalf("observation %+v %v", obs, err)
		}
		order := obs.Orders[0]
		sent := crdDraftOrder(t, req)
		if !bytes.HasPrefix(order.Order, append(bytes.TrimSuffix(sent, []byte("}")), `,"extension":[`...)) {
			t.Fatalf("the requested order is not returned as sent:\n%s\nsent\n%s", order.Order, sent)
		}
		return order
	}

	t.Run("pa-required", func(t *testing.T) {
		order := ask(t, "crd-sa-1", buildConformantCRD(t, "MBR-001", "72148"))
		ci := order.Coverage[0]
		if ci.Coverage != "Coverage/c1" || ci.Covered != "covered" || ci.PANeeded != "auth-needed" ||
			!slices.Equal(ci.DocNeeded, []string{"clinical"}) ||
			!slices.Equal(ci.Questionnaires, []string{SupportedQuestionnaireCanonical}) ||
			ci.Date != date || ci.CoverageAssertionID == "" || len(ci.Unknown) != 0 {
			t.Fatalf("coverage information %+v", ci)
		}
		rec := adj.recorded()
		want := CoverageAssertion{ID: ci.CoverageAssertionID, Order: "ServiceRequest/sr1", Coverage: "Coverage/c1",
			ProcedureCode: "72148", PARequired: true, Questionnaire: SupportedQuestionnaireCanonical}
		if len(rec) != 1 || rec[0] != want {
			t.Fatalf("recorded %+v, want %+v", rec, want)
		}
	})

	t.Run("no-pa-required", func(t *testing.T) {
		order := ask(t, "crd-sa-2", buildConformantCRD(t, "MBR-001", "99999"))
		ci := order.Coverage[0]
		if ci.PANeeded != "no-auth" || len(ci.DocNeeded) != 0 || len(ci.Questionnaires) != 0 {
			t.Fatalf("coverage information %+v", ci)
		}
		rec := adj.recorded()
		if len(rec) != 2 || rec[1].PARequired || rec[1].ID != ci.CoverageAssertionID || rec[1].ID == rec[0].ID {
			t.Fatalf("recorded %+v", rec)
		}
	})

	t.Run("order-sign with a Coverage search result", func(t *testing.T) {
		patient := []byte(`{"resourceType":"Patient","id":"MBR-001"}`)
		sr, err := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", "Patient/MBR-001")
		if err != nil {
			t.Fatal(err)
		}
		sr = []byte(strings.Replace(string(sr), `{`, `{"id":"sr-9",`, 1))
		cov, err := BuildCoverageWithPayer("Patient/MBR-001", "MBR-001", CMSPayerIdentity)
		if err != nil {
			t.Fatal(err)
		}
		req, err := BuildCRDRequest(CRDRequestInputs{
			Hook: "order-sign", HookInstance: "0b6f1a52-6a1c-4d0e-9f59-6c1e3d8f0a11", UserID: "Practitioner/p1",
			DraftOrders: []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"urn:uuid:0b6f1a52-6a1c-4d0e-9f59-6c1e3d8f0a12","resource":` + string(sr) + `}]}`),
			Patient:     patient,
			Coverage:    []byte(`{"resourceType":"Bundle","type":"searchset","entry":[{"resource":` + string(cov) + `}]}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		order := ask(t, "crd-sa-3", req)
		if order.ID != "sr-9" || order.Coverage[0].Coverage != "Coverage/c1" {
			t.Fatalf("order %+v", order)
		}
	})

	t.Run("order without id refused, nothing recorded", func(t *testing.T) {
		before := len(adj.recorded())
		req := buildConformantCRD(t, "MBR-001", "72148")
		req = bytes.Replace(req, []byte(`"id":"sr1",`), nil, 1)
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-sa-4", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "draft order has no id")
		if len(adj.recorded()) != before {
			t.Fatal("a refused request recorded an assertion")
		}
	})

	t.Run("coverage without id refused", func(t *testing.T) {
		req := buildConformantCRD(t, "MBR-001", "72148")
		req = bytes.Replace(req, []byte(`"id":"c1",`), nil, 1)
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-sa-5", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "coverage has no id")
	})
}

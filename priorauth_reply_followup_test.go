package shnsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

func replyFollowupRig(t *testing.T) (Identity, Endpoints, Payer, *paFakeSubstrate, PriorAuthResume) {
	t.Helper()
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	pend, err := BuildPendedClaimResponseAtLine("2.0", testPendedInputs("2.0", "Patient/MBR-COVERED", "corr-pend"))
	if err != nil {
		t.Fatal(err)
	}
	f := &paFakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now, paRequired: true, pasRawAnswer: pend}
	id, ep, payer, _ := newPATestRig(t, f)
	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
	if err != nil || res.Outcome != "pended" || res.Resume == nil || res.Resume.Continuation == nil {
		t.Fatalf("prepare pended submit: %+v %v", res, err)
	}
	return id, ep, payer, f, *res.Resume
}

func assertReceivedReply(t *testing.T, err error, leg string, body []byte) *PriorAuthConsumptionError {
	t.Helper()
	var ce *PriorAuthConsumptionError
	if !errors.As(err, &ce) || ce.Leg != leg || ce.Status != http.StatusOK ||
		ce.ContractVersion != ContractPAPAS20 || ce.ContentType != "application/fhir+json" || !bytes.Equal(ce.Body, body) {
		t.Fatalf("verified %s reply lost: %v", leg, err)
	}
	if strings.Contains(err.Error(), "synthetic-") {
		t.Fatal("Error disclosed response body")
	}
	return ce
}

func TestResumePriorAuth_ReceivedUpdateReplySurvivesLocalFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		badLine bool
	}{
		{"parse", []byte(`{"secret":"synthetic-update"}`), false},
		{"record", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ep, payer, f, resume := replyFollowupRig(t)
			if tc.badLine {
				var err error
				tc.body, err = BuildPendedClaimResponseAtLine("2.0", testPendedInputs("2.0", "Patient/MBR-COVERED", "corr-update"))
				if err != nil {
					t.Fatal(err)
				}
				resume.Continuation.Line = "9.9"
			}
			f.updateRawAnswer = tc.body
			f.doStamp, f.stampLeg, f.stampToken = true, "pas-claim-update", ContractPAPAS20
			res, err := id.ResumePriorAuthWith(context.Background(), http.DefaultClient, ep, payer, resume, demoSupplementalReport())
			if res.Outcome != "" {
				t.Fatalf("invented outcome: %+v", res)
			}
			assertReceivedReply(t, err, "pas-claim-update", tc.body)
		})
	}
}

func TestResumePriorAuth_RePendWithoutContinuationKeepsSelectedPASLine(t *testing.T) {
	id, ep, payer, f, resume := replyFollowupRig(t)
	resume.Continuation = nil // legacy saved handle; PASLine still pins the actual submit line
	resume.PASLine = "2.2"
	payer.ContractVersions = []string{"pa.pas@2.2"}
	// This fixture changes the recorded request line, so it must supply the
	// actual prior Claim at that line with its own item facts too.
	sub := conformantSubmitInputs(t)
	sub.Corr = resume.OriginalCorrelationID
	priorBundle, err := BuildConformantClaimBundleAtLine("2.2", sub)
	if err != nil {
		t.Fatal(err)
	}
	resume.PriorClaimJSON = claimEntryForAmendmentTest(t, priorBundle, "convergence-claim")
	pend, err := BuildPendedClaimResponseAtLine("2.2", testPendedInputs("2.2", "Patient/MBR-COVERED", "corr-update"))
	if err != nil {
		t.Fatal(err)
	}
	f.updateRawAnswer = pend
	result, err := id.ResumePriorAuthWith(context.Background(), http.DefaultClient, ep, payer, resume, demoSupplementalReport())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resume == nil || result.Resume.PASLine != "2.2" || result.Resume.Continuation == nil || result.Resume.Continuation.Line != "2.2" {
		t.Fatalf("re-pend continuation lines: resume=%q continuation=%q, want 2.2", result.Resume.PASLine, result.Resume.Continuation.Line)
	}
}

func TestInquire_ReceivedReplySurvivesLocalFailure(t *testing.T) {
	approved, err := BuildClaimResponse("AUTH-1", "2026-12-31", "Patient/MBR-COVERED", "corr-pend", testNow)
	if err != nil {
		t.Fatal(err)
	}
	pend, err := BuildPendedClaimResponseAtLine("2.0", testPendedInputs("2.0", "Patient/MBR-COVERED", "corr-pend"))
	if err != nil {
		t.Fatal(err)
	}
	malformedOther := contentBundle(`{"resourceType":"ClaimResponse","identifier":7}`)
	recordFailure := []byte(`{"resourceType":"Parameters","parameter":[{"name":"return","resource":` + string(pend) + `},{"name":"return","resource":` + string(malformedOther) + `}]}`)
	for _, tc := range []struct {
		name string
		body []byte
		want error
	}{
		{"parse", []byte(`{"secret":"synthetic-inquiry"}`), nil},
		{"no match", contentBundle(`{"resourceType":"ClaimResponse","outcome":"complete","identifier":[{"system":"urn:p","value":"unrelated"}]}`), ErrInquiryNoMatch},
		{"ambiguous", contentBundle(string(approved), string(approved)), ErrInquiryAmbiguous},
		{"record", recordFailure, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ep, payer, f, resume := replyFollowupRig(t)
			f.inquireRawAnswer = tc.body
			f.doStamp, f.stampLeg, f.stampToken = true, "pas-claim-inquire", ContractPAPAS20
			records := pairInquiryRecords()
			records.Patient = testMemberPatient("MBR-COVERED")
			records.Coverage = testMemberCoverage("MBR-COVERED")
			res, err := id.Inquire(context.Background(), http.DefaultClient, ep, payer, resume, records)
			if res.Outcome != "" {
				t.Fatalf("invented outcome: %+v", res)
			}
			assertReceivedReply(t, err, "pas-claim-inquire", tc.body)
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("lost sentinel %v: %v", tc.want, err)
			}
		})
	}
}

func TestPriorAuthFollowups_SuccessAndAuthorityControls(t *testing.T) {
	approved, err := BuildClaimResponse("AUTH-1", "2026-12-31", "Patient/MBR-COVERED", "corr-pend", testNow)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("update", func(t *testing.T) {
		id, ep, payer, f, resume := replyFollowupRig(t)
		f.updateRawAnswer = approved
		f.doStamp, f.stampLeg, f.stampToken = true, "pas-claim-update", ContractPAPAS20
		res, err := id.ResumePriorAuthWith(context.Background(), http.DefaultClient, ep, payer, resume, demoSupplementalReport())
		if err != nil || res.Outcome != "approved" {
			t.Fatalf("valid update: %+v %v", res, err)
		}
	})
	t.Run("inquiry", func(t *testing.T) {
		id, ep, payer, f, resume := replyFollowupRig(t)
		f.inquireRawAnswer = contentBundle(string(approved))
		f.doStamp, f.stampLeg, f.stampToken = true, "pas-claim-inquire", ContractPAPAS20
		records := pairInquiryRecords()
		records.Patient = testMemberPatient("MBR-COVERED")
		records.Coverage = testMemberCoverage("MBR-COVERED")
		res, err := id.Inquire(context.Background(), http.DefaultClient, ep, payer, resume, records)
		if err != nil || res.Outcome != "approved" {
			t.Fatalf("valid inquiry: %+v %v", res, err)
		}
	})
	t.Run("unverified response", func(t *testing.T) {
		id, ep, payer, f, resume := replyFollowupRig(t)
		f.inquireRawAnswer = []byte(`{"secret":"synthetic-unverified"}`)
		wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
		payer.AuthzPub = wrongPub
		records := pairInquiryRecords()
		records.Patient = testMemberPatient("MBR-COVERED")
		records.Coverage = testMemberCoverage("MBR-COVERED")
		_, err := id.Inquire(context.Background(), http.DefaultClient, ep, payer, resume, records)
		var ce *PriorAuthConsumptionError
		if err == nil || errors.As(err, &ce) || strings.Contains(err.Error(), "synthetic-unverified") {
			t.Fatalf("unverified answer exposed: %v", err)
		}
	})
	t.Run("non-2xx update", func(t *testing.T) {
		id, ep, payer, f, resume := replyFollowupRig(t)
		body := []byte(`{"secret":"synthetic-refusal"}`)
		f.frameLeg, f.frameStatus, f.frameBody = "pas-claim-update", 422, body
		_, err := id.ResumePriorAuthWith(context.Background(), http.DefaultClient, ep, payer, resume, demoSupplementalReport())
		var ae *AppAnswerError
		var ce *PriorAuthConsumptionError
		if !errors.As(err, &ae) || errors.As(err, &ce) || ae.Status != 422 || !bytes.Equal(ae.Body, body) {
			t.Fatalf("application refusal reclassified or lost: %v", err)
		}
	})
	t.Run("wait propagates inquiry reply", func(t *testing.T) {
		id, ep, payer, f, _ := replyFollowupRig(t)
		body := []byte(`{"secret":"synthetic-wait-inquiry"}`)
		f.inquireRawAnswer = body
		f.doStamp, f.stampLeg, f.stampToken = true, "pas-claim-inquire", ContractPAPAS20
		records := pairInquiryRecords()
		records.Patient = testMemberPatient("MBR-COVERED")
		records.Coverage = testMemberCoverage("MBR-COVERED")
		timer := &fakeWaitTimer{now: f.now}
		_, err := id.RunPriorAuthWith(context.Background(), http.DefaultClient, ep, payer, demoPARequest(),
			WithWait(3*time.Second), WithInquiryRecords(records), withWaitTimer(timer))
		assertReceivedReply(t, err, "pas-claim-inquire", body)
	})
}

func TestRunPriorAuth_PASBuildFailureRetainsDTRReply(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	f := &paFakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), paRequired: true,
		doStamp: true, stampLeg: "dtr-questionnaire-fetch", stampToken: ContractPADTR20}
	id, ep, payer, _ := newPATestRig(t, f)
	req := demoPARequest()
	req.Provider = []byte(`{"bad":`)
	want := f.payloadFor("dtr-questionnaire-fetch", nil)
	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, req)
	if res.Outcome != "" {
		t.Fatalf("invented outcome: %+v", res)
	}
	var ce *PriorAuthConsumptionError
	if !errors.As(err, &ce) || ce.Leg != "pas-claim" || ce.ContractVersion != ContractPADTR20 ||
		ce.ContentType != "application/fhir+json" || ce.Status != http.StatusOK || !bytes.Equal(ce.Body, want) || ce.Cause == nil {
		t.Fatalf("PAS local build lost latest DTR reply: %v", err)
	}
	if f.capturedRequestFramed["pas-claim"] {
		t.Fatal("PAS sent after local build failure")
	}
}

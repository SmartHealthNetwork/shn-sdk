package shnsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// priorauth_requestframe_test.go — the Originator's REQUEST-framing (request-frame
// contract, published-SDK parity — v0.38.0): RunPriorAuth frames each
// contract-mapped leg's REQUEST toward a requestFrames-declaring payer, with the
// SAME per-leg token contractTokenForTxType computes for both the request stamp
// AND the response unframeAnswer verify. A payer that does not declare
// requestFrames gets a BYTE-IDENTICAL bare request (Payer.RequestFrames unset —
// the zero value every pre-v0.38.0 test/caller already uses).

// TestRunPriorAuth_FramesRequestsToDeclaringPayer proves each of the three PA-chain
// legs is framed with ITS OWN contract-family token when the payer declares
// requestFrames v1.
func TestRunPriorAuth_FramesRequestsToDeclaringPayer(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	f := &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now, paRequired: true,
	}
	id, ep, payer, _ := newPATestRig(t, f)
	payer.RequestFrames = []string{RequestFrameV1}

	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
	if err != nil {
		t.Fatalf("RunPriorAuth: %v", err)
	}
	if res.Outcome != "approved" {
		t.Fatalf("Outcome = %q, want approved", res.Outcome)
	}

	wantClaim := map[string]string{
		"crd-order-select":        ContractPACRD20,
		"dtr-questionnaire-fetch": ContractPADTR20,
		"pas-claim":               ContractPAPAS20,
	}
	for txType, want := range wantClaim {
		if !f.capturedRequestFramed[txType] {
			t.Errorf("%s: request not framed, want framed (payer declares requestFrames v1)", txType)
			continue
		}
		if got := f.capturedRequestClaim[txType]; got != want {
			t.Errorf("%s: contractVersion claim = %q, want %q", txType, got, want)
		}
	}
}

// TestRunPriorAuth_BareRequestsToNonDeclaringPayer proves the BYTE-IDENTICAL
// counterpart: a payer that does not declare requestFrames (Payer.RequestFrames
// unset — every pre-v0.38.0 caller) never sees a framed request, on any leg.
func TestRunPriorAuth_BareRequestsToNonDeclaringPayer(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	f := &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now, paRequired: true,
	}
	id, ep, payer, _ := newPATestRig(t, f) // payer.RequestFrames left nil (zero value)

	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
	if err != nil {
		t.Fatalf("RunPriorAuth: %v", err)
	}
	if res.Outcome != "approved" {
		t.Fatalf("Outcome = %q, want approved", res.Outcome)
	}

	for _, txType := range []string{"crd-order-select", "dtr-questionnaire-fetch", "pas-claim"} {
		if f.capturedRequestFramed[txType] {
			t.Errorf("%s: request framed, want bare (payer does not declare requestFrames) — claim %q", txType, f.capturedRequestClaim[txType])
		}
	}
}

// TestRunEligibility_NeverFramed proves coverage-eligibility — which has no
// contract-version token (contractTokenForTxType's "" default) — is NEVER framed
// even toward a payer that declares requestFrames v1 (Payer.RequestFrames set).
func TestRunEligibility_NeverFramed(t *testing.T) {
	signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	id, _ := GenerateIdentity("ext-provider")
	id.Clock = func() time.Time { return now }

	f := &fakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", covered: true, now: now, requesterEnc: id.EncPub,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorizeHandler)
	authzSrv := httptest.NewServer(mux)
	defer authzSrv.Close()
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", f.routeHandler())
	hubSrv := httptest.NewServer(hubMux)
	defer hubSrv.Close()

	covered, _, err := id.RunEligibility(
		context.Background(), authzSrv.Client(),
		Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
		Payer{ID: "payer", EncPub: payerPub, AuthzPub: signPub, RequestFrames: []string{RequestFrameV1}},
		"9999999999", "MBR-COVERED", "1975-04-02", "Johansson",
	)
	if err != nil {
		t.Fatalf("RunEligibility: %v", err)
	}
	if !covered {
		t.Error("covered = false, want true")
	}
	if f.capturedRequestFramed {
		t.Error("coverage-eligibility request arrived framed, want bare (no contract-version token exists for it)")
	}
}

// TestUnframeAnswer_ContractTokenForTxType proves the per-leg token map itself
// (crd/dtr/pas map to distinct contracts; coverage-eligibility and any unknown
// txType are version-neutral) — the single source both the request-frame stamp
// and the response expectedToken read.
func TestContractTokenForTxType(t *testing.T) {
	cases := map[string]string{
		"crd-order-select":        ContractPACRD20,
		"dtr-questionnaire-fetch": ContractPADTR20,
		"pas-claim":               ContractPAPAS20,
		"pas-claim-update":        ContractPAPAS20,
		"coverage-eligibility":    "",
		"unknown-leg-type":        "",
	}
	for txType, want := range cases {
		if got := contractTokenForTxType(txType); got != want {
			t.Errorf("contractTokenForTxType(%q) = %q, want %q", txType, got, want)
		}
	}
}

// TestRunPriorAuth_ResponseStampVerify covers the contractVersion stamp-verify rows on the
// FULL orchestrator round trip (unlike TestUnframeAnswer_StampVerify's unit-level
// coverage): the pas-claim leg's response is framed with a matching / mismatched /
// absent contractVersion stamp.
func TestRunPriorAuth_ResponseStampVerify(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stampToken string
		wantErr    bool
	}{
		{"matching stamp accepted", ContractPAPAS20, false},
		{"mismatched stamp rejected", "pa.pas@9.9", true},
		{"absent stamp tolerated", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
			payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
			now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

			f := &paFakeSubstrate{
				signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
				payerID: "payer", now: now, paRequired: true,
				doStamp: true, stampLeg: "pas-claim", stampToken: tc.stampToken,
			}
			id, ep, payer, _ := newPATestRig(t, f)

			res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
			if tc.wantErr {
				if err == nil {
					t.Fatal("RunPriorAuth: expected a contract-version mismatch error, got nil")
				}
				if !strings.Contains(err.Error(), "contract version mismatch") {
					t.Errorf("error = %v, want it to mention a contract version mismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunPriorAuth: %v", err)
			}
			if res.Outcome != "approved" {
				t.Errorf("Outcome = %q, want approved", res.Outcome)
			}
		})
	}
}

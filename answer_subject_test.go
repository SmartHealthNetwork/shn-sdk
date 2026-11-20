package shnsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// The legs an Identity sends hold the answer token to the request's patient:
// an answer whose token names another patient, or none, is refused as a
// subject mismatch.
var answerSubjectMisfits = map[string]string{
	"another patient": "pci:ffffffffffffffffffffffffffffffff",
	"no patient":      "",
}

func TestRunEligibility_TheAnswerTokenNamesTheRequestsPatient(t *testing.T) {
	for name, subject := range answerSubjectMisfits {
		t.Run(name, func(t *testing.T) {
			signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
			payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
			now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
			id, _ := GenerateIdentity("ext-provider")
			id.Clock = func() time.Time { return now }
			f := &fakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", covered: true, now: now,
				requesterEnc: id.EncPub, answerSubject: subject, answerSubjectSet: true}
			mux := http.NewServeMux()
			mux.HandleFunc("/authorize", f.authorizeHandler)
			authzSrv := httptest.NewServer(mux)
			defer authzSrv.Close()
			hubMux := http.NewServeMux()
			hubMux.HandleFunc("/route", f.routeHandler())
			hubSrv := httptest.NewServer(hubMux)
			defer hubSrv.Close()
			_, _, err := id.RunEligibility(context.Background(), authzSrv.Client(),
				Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
				Payer{ID: "payer", Identity: CMSPayerIdentity, EncPub: payerPub, AuthzPub: signPub},
				"9999999999", "MBR-COVERED", "1975-04-02", "Johansson")
			if !errors.Is(err, ErrSubjectMismatch) {
				t.Fatalf("an answer token naming %s: %v; want ErrSubjectMismatch", name, err)
			}
		})
	}
}

func TestRunPriorAuth_TheAnswerTokenNamesTheRequestsPatient(t *testing.T) {
	for name, subject := range answerSubjectMisfits {
		t.Run(name, func(t *testing.T) {
			_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
			payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
			f := &paFakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer",
				now: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), paRequired: true, answerSubject: subject, answerSubjectSet: true}
			id, ep, payer, _ := newPATestRig(t, f)
			if _, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest()); !errors.Is(err, ErrSubjectMismatch) {
				t.Fatalf("an answer token naming %s: %v; want ErrSubjectMismatch", name, err)
			}
		})
	}
}

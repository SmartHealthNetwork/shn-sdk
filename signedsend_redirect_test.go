package shnsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// redirectProbe is a server on another host (localhost, where each request
// goes to 127.0.0.1) that fails the test if anything reaches it, and counts
// what did.
func redirectProbe(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Errorf("a redirect was followed to another host: %s %s, assertion sent %v", r.Method, r.URL.Path, r.Header.Get("X-Holder-Assertion") != "")
	}))
	t.Cleanup(probe.Close)
	return probe, &hits
}

// redirector answers every request with status, naming the same path on the
// probe's host.
func redirector(t *testing.T, probe *httptest.Server, status int) *httptest.Server {
	t.Helper()
	other := strings.Replace(probe.URL, "127.0.0.1", "localhost", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other+r.URL.Path, status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// redirectStatuses are a redirect that turns a POST or PUT into a GET (303)
// and one that re-sends it as it was (307).
var redirectStatuses = []int{http.StatusSeeOther, http.StatusTemporaryRedirect}

// A send that carries an SHN assertion follows no cross-host redirect, for
// each audience: the registrar (a holder's own declaration), the
// Authorization Framework, the Hub, and any service PostRaw reaches. The
// probe receives nothing, each send fails, and the caller's client is not
// changed. (The registrar under an admin's assertion: shn register, in
// cmd/shn.)
func TestSignedSends_FollowNoRedirect(t *testing.T) {
	id, err := GenerateIdentity("h-redirect")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range redirectStatuses {
		probe, hits := redirectProbe(t)
		srv := redirector(t, probe, status)
		client := &http.Client{}
		ctx := context.Background()

		if _, err := id.DeclareCapabilities(ctx, client, srv.URL, CapabilityDeclaration{Legs: []string{}}); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("registrar answered %d", status)) {
			t.Errorf("%d: DeclareCapabilities = %v, want the redirect as the registrar's answer", status, err)
		}
		if _, err := id.Authorize(ctx, client, srv.URL, AuthorizeRequest{Frame: "provider-tpo", Operation: "eligibility-inquiry"}); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("/authorize returned %d", status)) {
			t.Errorf("%d: Authorize = %v, want the redirect as the Framework's answer", status, err)
		}
		var se *StatusError
		if err := PostRaw(ctx, client, srv.URL+"/check", []byte(`{}`), nil, map[string]string{"X-Holder-Assertion": "a"}); !errors.As(err, &se) || se.Code != status {
			t.Errorf("%d: PostRaw = %v, want the redirect as a StatusError", status, err)
		}
		if client.CheckRedirect != nil {
			t.Errorf("%d: the caller's client was changed", status)
		}

		// The Hub: an eligibility check whose Authorization Framework answers,
		// and whose Hub redirects.
		signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
		payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
		now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
		pid, err := GenerateIdentity("ext-provider")
		if err != nil {
			t.Fatal(err)
		}
		pid.Clock = func() time.Time { return now }
		f := &fakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", covered: true, now: now, requesterEnc: pid.EncPub}
		authz := httptest.NewServer(http.HandlerFunc(f.authorizeHandler))
		if _, _, err := pid.RunEligibility(ctx, client, Endpoints{HubURL: srv.URL, AuthzURL: authz.URL},
			Payer{ID: "payer", Identity: CMSPayerIdentity, EncPub: payerPub, AuthzPub: signPub}, "9999999999", "MBR-1", "1975-04-02", "Johansson"); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("/route returned %d", status)) {
			t.Errorf("%d: RunEligibility = %v, want the Hub's redirect as a failed /route", status, err)
		}
		authz.Close()
		if n := hits.Load(); n != 0 {
			t.Errorf("%d: the probe was reached %d times", status, n)
		}
	}
}

// DeclareCapabilities signs at the identity's Clock, so a registrar on a
// fixed clock verifies its declaration.
func TestDeclareCapabilities_SignsAtTheIdentitysClock(t *testing.T) {
	id, err := GenerateIdentity("h-clock")
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	id.Clock = func() time.Time { return fixed }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := decodeAssertion(t, r.Header.Get("X-Holder-Assertion"))
		if !a.IssuedAt.Equal(fixed) || VerifyAssertion(a, "registrar", id.SignPub, fixed) != nil {
			t.Errorf("the declaration was signed at %v, want the identity's clock %v", a.IssuedAt, fixed)
		}
		_, _ = w.Write([]byte(`{"legs":[]}`))
	}))
	defer srv.Close()
	if _, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}}); err != nil {
		t.Fatal(err)
	}
}

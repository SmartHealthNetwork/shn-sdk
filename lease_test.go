package shnsdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// renewalRegistrar is a registrar stub that checks a renewal's request and
// answers it with status and body.
func renewalRegistrar(t *testing.T, id Identity, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/register/"+id.HolderID+"/lease" {
			t.Errorf("renewal sent %s %s, want PUT /register/%s/lease", r.Method, r.URL.Path, id.HolderID)
		}
		if b, _ := io.ReadAll(r.Body); len(b) != 0 {
			t.Errorf("renewal sent a body: %q", b)
		}
		raw, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Holder-Assertion"))
		var a Assertion
		if err == nil {
			err = json.Unmarshal(raw, &a)
		}
		if err != nil || a.HolderID != id.HolderID || !a.IssuedAt.Equal(id.now()) && id.Clock != nil || VerifyAssertion(a, "registrar", id.SignPub, id.now()) != nil {
			t.Errorf("renewal's assertion is not the holder's own for the registrar: %v %+v", err, a)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

// A renewal the registrar confirms is renewed: null says no lease runs, and a
// time is when the lease lapses.
func TestRenewLease_Renewed(t *testing.T) {
	id, err := GenerateIdentity("h-lease")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
		want       *time.Time
	}{
		{"no lease runs", `{"leaseExpiresAt":null}`, nil},
		{"a lease lapses", `{"leaseExpiresAt":"2026-12-27T00:00:00Z"}`, &at},
		{"a member this SDK does not read", `{"leaseExpiresAt":null,"later":1}`, nil},
		{"a lease named at another offset, read in UTC", `{"leaseExpiresAt":"2026-12-27T01:00:00+01:00"}`, &at},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := renewalRegistrar(t, id, http.StatusOK, tc.body)
			defer srv.Close()
			got, err := id.RenewLease(context.Background(), srv.Client(), srv.URL+"/")
			if err != nil || got.Outcome != RenewalRenewed || got.Status != http.StatusOK {
				t.Fatalf("got %+v, %v; want renewed", got, err)
			}
			if (got.LeaseExpiresAt == nil) != (tc.want == nil) || got.LeaseExpiresAt != nil && (!got.LeaseExpiresAt.Equal(*tc.want) || got.LeaseExpiresAt.Location() != time.UTC) {
				t.Fatalf("LeaseExpiresAt = %v, want %v", got.LeaseExpiresAt, tc.want)
			}
		})
	}
}

// refusal is the registrar's own refusal body for key.
func refusal(key string, status int) string {
	return `{"error":"refused","type":"` + registrarRefusalType + key + `","title":"Refused","status":` + strconv.Itoa(status) + `}`
}

// A renewal the registrar does not confirm is unknown, never renewed: a 404
// or 405 (a registrar that predates the renewal answers so), a 5xx, any other
// status, a 403 or 409 that is not the registrar's own refusal, or a 200
// whose answer cannot be read. The registrar's own refusal is refused.
func TestRenewLease_UnconfirmedIsUnknownAndRefusedIsRefused(t *testing.T) {
	id, err := GenerateIdentity("h-lease")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   RenewalOutcome
	}{
		{"404, a registrar that predates the renewal or holds no such holder", http.StatusNotFound, "404 page not found", RenewalUnknown},
		{"405, a registrar that predates the renewal", http.StatusMethodNotAllowed, "", RenewalUnknown},
		{"500", http.StatusInternalServerError, "", RenewalUnknown},
		{"502, the renewal not recordable", http.StatusBadGateway, "lifecycle audit failed", RenewalUnknown},
		{"503, the store unavailable", http.StatusServiceUnavailable, "store error", RenewalUnknown},
		{"504", http.StatusGatewayTimeout, "", RenewalUnknown},
		{"400, a status the renewal does not name", http.StatusBadRequest, "", RenewalUnknown},
		{"401", http.StatusUnauthorized, "", RenewalUnknown},
		{"201, a success the renewal does not name", http.StatusCreated, `{"leaseExpiresAt":null}`, RenewalUnknown},
		{"204", http.StatusNoContent, "", RenewalUnknown},
		{"200 with no answer", http.StatusOK, "", RenewalUnknown},
		{"200 naming no leaseExpiresAt", http.StatusOK, `{}`, RenewalUnknown},
		{"200 not an object", http.StatusOK, `[]`, RenewalUnknown},
		{"200 not JSON", http.StatusOK, `renewed`, RenewalUnknown},
		{"200 naming leaseExpiresAt twice", http.StatusOK, `{"leaseExpiresAt":null,"leaseExpiresAt":"2026-12-27T00:00:00Z"}`, RenewalUnknown},
		{"200 naming a number", http.StatusOK, `{"leaseExpiresAt":5}`, RenewalUnknown},
		{"200 naming a time that is not RFC 3339", http.StatusOK, `{"leaseExpiresAt":"2026-12-27"}`, RenewalUnknown},
		{"200 over the answer bound", http.StatusOK, `{"leaseExpiresAt":null,"pad":"` + strings.Repeat("x", maxLeaseAnswerBytes) + `"}`, RenewalUnknown},
		{"200 over the answer bound by trailing space alone", http.StatusOK, `{"leaseExpiresAt":null}` + strings.Repeat(" ", maxLeaseAnswerBytes), RenewalUnknown},
		{"403, the registrar's own not-self refusal", http.StatusForbidden, refusal("not-self", 403), RenewalRefused},
		{"409, the registrar's own founding-holder refusal", http.StatusConflict, refusal("founding-holder", 409), RenewalRefused},
		{"403 from something in front of the registrar", http.StatusForbidden, "<html>Request blocked</html>", RenewalUnknown},
		{"403 naming no problem type", http.StatusForbidden, `{"error":"a holder may only renew itself"}`, RenewalUnknown},
		{"403 naming another refusal", http.StatusForbidden, refusal("founding-holder", 403), RenewalUnknown},
		{"409 naming another refusal", http.StatusConflict, refusal("not-self", 409), RenewalUnknown},
		{"403 naming its type twice", http.StatusForbidden, `{"type":"x","type":"` + registrarRefusalType + `not-self"}`, RenewalUnknown},
		{"403 naming another registry's type", http.StatusForbidden, `{"type":"tag:example.com,2026:registrar/refusal/not-self"}`, RenewalUnknown},
		{"403 naming the type as Type", http.StatusForbidden, `{"Type":"` + registrarRefusalType + `not-self"}`, RenewalUnknown},
		{"403 naming type and TYPE", http.StatusForbidden, `{"type":"x","TYPE":"` + registrarRefusalType + `not-self"}`, RenewalUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := renewalRegistrar(t, id, tc.status, tc.body)
			defer srv.Close()
			got, err := id.RenewLease(context.Background(), srv.Client(), srv.URL)
			if err == nil || got.Outcome != tc.want || got.Status != tc.status || got.LeaseExpiresAt != nil {
				t.Fatalf("got %+v, %v; want %s with status %d and an error", got, err, tc.want, tc.status)
			}
		})
	}
}

// A redirect is unknown, and the renewal follows none, whatever the client
// does: the assertion goes only to the registrar's URL.
func TestRenewLease_ARedirectIsUnknownAndNotFollowed(t *testing.T) {
	id, err := GenerateIdentity("h-lease")
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the renewal followed a redirect: %s %s, assertion sent %v", r.Method, r.URL.Path, r.Header.Get("X-Holder-Assertion") != "")
		_, _ = io.WriteString(w, `{"leaseExpiresAt":null}`)
	}))
	defer elsewhere.Close()
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL+r.URL.Path, status)
		}))
		got, err := id.RenewLease(context.Background(), &http.Client{}, srv.URL)
		srv.Close()
		if err == nil || got.Outcome != RenewalUnknown || got.Status != status {
			t.Fatalf("%d: got %+v, %v; want unknown with status %d", status, got, err, status)
		}
	}
}

// The renewal is signed at the identity's Clock, so a registrar on a fixed
// clock verifies it.
func TestRenewLease_SignsAtTheIdentitysClock(t *testing.T) {
	id, err := GenerateIdentity("h-lease")
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	id.Clock = func() time.Time { return fixed }
	srv := renewalRegistrar(t, id, http.StatusOK, `{"leaseExpiresAt":null}`)
	defer srv.Close()
	if got, err := id.RenewLease(context.Background(), srv.Client(), srv.URL); err != nil || got.Outcome != RenewalRenewed {
		t.Fatalf("got %+v, %v; want renewed", got, err)
	}
}

// A 200 naming no leaseExpiresAt is unreadable as such, never as no lease.
func TestRenewLease_AnAnswerNamingNoLeaseSaysSo(t *testing.T) {
	if _, err := parseLeaseAnswer([]byte(`{"later":null}`)); err == nil || !strings.Contains(err.Error(), "names no leaseExpiresAt") {
		t.Fatalf("got %v, want it to say the answer names no leaseExpiresAt", err)
	}
}

// No answer at all is unknown, with no status.
func TestRenewLease_NoAnswerIsUnknown(t *testing.T) {
	id, err := GenerateIdentity("h-lease")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	got, err := id.RenewLease(context.Background(), nil, u)
	if err == nil || got.Outcome != RenewalUnknown || got.Status != 0 {
		t.Fatalf("got %+v, %v; want unknown with no status", got, err)
	}
}

// An identity with no signing key sends nothing: the renewal is unknown.
func TestRenewLease_NoSigningKeySendsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a renewal was sent without a signing key")
	}))
	defer srv.Close()
	got, err := Identity{HolderID: "h-lease"}.RenewLease(context.Background(), srv.Client(), srv.URL)
	if err == nil || got.Outcome != RenewalUnknown || got.Status != 0 {
		t.Fatalf("got %+v, %v; want unknown with no status", got, err)
	}
}

package shnsdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// A holder's legs list round-trips as itself: absent (nil) is omitted and
// decodes to nil, a null decodes to nil, and an empty list is sent as [] and
// decodes to an empty list, not nil. Absent and empty mean different things.
func TestLegsAbsentAndEmptyRoundTrip(t *testing.T) {
	for name, v := range map[string]any{"Holder": &Holder{}, "RegistrationRequest": &RegistrationRequest{}} {
		t.Run(name, func(t *testing.T) {
			legs := func(v any) []string {
				switch x := v.(type) {
				case *Holder:
					return x.Legs
				case *RegistrationRequest:
					return x.Legs
				}
				t.Fatalf("type %T", v)
				return nil
			}
			set := func(l []string) any {
				switch v.(type) {
				case *Holder:
					return Holder{ID: "h", Legs: l}
				default:
					return RegistrationRequest{ID: "h", Legs: l}
				}
			}
			fresh := func() any {
				switch v.(type) {
				case *Holder:
					return &Holder{}
				default:
					return &RegistrationRequest{}
				}
			}
			absent, _ := json.Marshal(set(nil))
			if strings.Contains(string(absent), `"legs"`) {
				t.Fatalf("absent legs marshalled as %s, want the key omitted", absent)
			}
			got := fresh()
			if err := json.Unmarshal(absent, got); err != nil || legs(got) != nil {
				t.Fatalf("absent decoded as %#v (%v), want nil", legs(got), err)
			}
			got = fresh()
			if err := json.Unmarshal([]byte(`{"id":"h","legs":null}`), got); err != nil || legs(got) != nil {
				t.Fatalf("null decoded as %#v (%v), want nil", legs(got), err)
			}
			empty, _ := json.Marshal(set([]string{}))
			if !strings.Contains(string(empty), `"legs":[]`) {
				t.Fatalf("empty legs marshalled as %s, want \"legs\":[]", empty)
			}
			got = fresh()
			if err := json.Unmarshal(empty, got); err != nil || legs(got) == nil || len(legs(got)) != 0 {
				t.Fatalf("empty decoded as %#v (%v), want a non-nil empty list", legs(got), err)
			}
			full, _ := json.Marshal(set([]string{"pas-claim"}))
			got = fresh()
			if err := json.Unmarshal(full, got); err != nil || !slices.Equal(legs(got), []string{"pas-claim"}) {
				t.Fatalf("a list decoded as %#v (%v)", legs(got), err)
			}
		})
	}
	// A registration built by the SDK declares no list: the system behind its
	// base URL declares its own.
	id, err := GenerateIdentity("h-legs")
	if err != nil {
		t.Fatal(err)
	}
	if reg := id.RegistrationWithDeclared("payer", "https://payer.example", nil); reg.Legs != nil {
		t.Fatalf("a registration defaulted legs to %v", reg.Legs)
	}
}

// DeclareCapabilities PUTs the sorted legs, and the attachment limit only when
// one is set, to the holder's capabilities path with a holder-self assertion
// (audience "registrar") bound to the exact body sent, and returns what the
// registrar stored. A list naming no leg, a staged name or a leg twice, a nil
// list, or a limit outside 1 to 2^53-1, is refused before anything is sent; a
// registrar that does not answer 200 with a list is an error.
func TestDeclareCapabilities(t *testing.T) {
	id, err := GenerateIdentity("h-legs")
	if err != nil {
		t.Fatal(err)
	}
	var sent []byte
	var calls int
	answer := func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPut || r.URL.Path != "/register/h-legs/capabilities" {
			t.Errorf("%s %s, want PUT /register/h-legs/capabilities", r.Method, r.URL.Path)
		}
		sent, _ = io.ReadAll(r.Body)
		a := decodeAssertion(t, r.Header.Get("X-Holder-Assertion"))
		if err := VerifyAssertion(a, "registrar", id.SignPub, time.Now()); err != nil || a.HolderID != "h-legs" {
			t.Errorf("assertion %+v: %v, want a holder-self assertion for the registrar", a, err)
		}
		sum := sha256.Sum256(sent)
		if a.BodyHash != hex.EncodeToString(sum[:]) {
			t.Errorf("assertion bh %q is not the body sent's hash", a.BodyHash)
		}
		_, _ = w.Write(sent)
	}
	srv := httptest.NewServer(http.HandlerFunc(answer))
	defer srv.Close()
	ctx := context.Background()
	stored, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL+"/", CapabilityDeclaration{Legs: []string{"pas-claim", "cdex-attachment-submit"}})
	if err != nil || !slices.Equal(stored.Legs, []string{"cdex-attachment-submit", "pas-claim"}) || stored.AttachmentMaxBytes != nil {
		t.Fatalf("stored %+v (%v), want the sorted list and no limit", stored, err)
	}
	if string(sent) != `{"legs":["cdex-attachment-submit","pas-claim"]}` {
		t.Fatalf("body %s, want no attachmentMaxBytes key when none is declared", sent)
	}
	limit := int64(10 << 20)
	stored, err = id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{"cdex-attachment-submit"}, AttachmentMaxBytes: &limit})
	if err != nil || stored.AttachmentMaxBytes == nil || *stored.AttachmentMaxBytes != limit || string(sent) != `{"legs":["cdex-attachment-submit"],"attachmentMaxBytes":10485760}` {
		t.Fatalf("a limit: stored %+v (%v), body %s", stored, err, sent)
	}
	if stored, err := id.DeclareCapabilities(ctx, nil, srv.URL, CapabilityDeclaration{Legs: []string{"pas-claim"}}); err != nil || !slices.Equal(stored.Legs, []string{"pas-claim"}) {
		t.Fatalf("a nil client: stored %+v (%v), want http.DefaultClient used", stored, err)
	}
	if stored, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}}); err != nil || stored.Legs == nil || len(stored.Legs) != 0 || string(sent) != `{"legs":[]}` {
		t.Fatalf("an empty list: stored %+v (%v), body %s, want [] sent and stored", stored, err, sent)
	}
	before := calls
	zero, negative, over := int64(0), int64(-1), int64(1<<53)
	for name, d := range map[string]CapabilityDeclaration{
		"nil legs":       {},
		"no such leg":    {Legs: []string{"no-such-leg"}},
		"a staged name":  {Legs: []string{"pa.cdex-attachment-submit"}},
		"a leg twice":    {Legs: []string{"pas-claim", "pas-claim"}},
		"an empty name":  {Legs: []string{""}},
		"a zero limit":   {Legs: []string{}, AttachmentMaxBytes: &zero},
		"a negative one": {Legs: []string{}, AttachmentMaxBytes: &negative},
		"over 2^53-1":    {Legs: []string{}, AttachmentMaxBytes: &over},
	} {
		if _, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, d); err == nil {
			t.Errorf("%s: declared", name)
		}
	}
	if calls != before {
		t.Fatalf("a refused declaration reached the registrar")
	}
	top := int64(1<<53 - 1)
	if _, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, AttachmentMaxBytes: &top}); err != nil {
		t.Fatalf("a limit of 2^53-1: %v", err)
	}
	for name, h := range map[string]http.HandlerFunc{
		"a 409": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "a newer or the same declaration is already stored", http.StatusConflict)
		},
		"a 403":       func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) },
		"no list":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
		"a null list": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"legs":null}`)) },
		"not JSON":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`ok`)) },
		"a zero limit stored": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"legs":["pas-claim"],"attachmentMaxBytes":0}`))
		},
	} {
		s := httptest.NewServer(h)
		_, err := id.DeclareCapabilities(ctx, s.Client(), s.URL, CapabilityDeclaration{Legs: []string{"pas-claim"}})
		s.Close()
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A Responder declares the legs it has a handler for, sorted, and no other:
// not the CDex submission, which it does not answer.
func TestResponderLegsAreTheLegsItAnswers(t *testing.T) {
	var r *Responder
	legs := r.Legs()
	want := make([]string, 0, len(responderReqOp))
	for tx := range responderReqOp {
		want = append(want, tx)
	}
	slices.Sort(want)
	if !slices.Equal(legs, want) || len(legs) == 0 || slices.Contains(legs, "cdex-attachment-submit") {
		t.Fatalf("Legs = %v, want %v", legs, want)
	}
}

// A leg added after shn-sdk v1.0.0 is marked with the release that brings it,
// in either name form; a leg v1.0.0 published, and a name no leg has, are not.
func TestTransactionTypeSince(t *testing.T) {
	for _, name := range []string{"cdex-attachment-submit", "pa.cdex-attachment-submit"} {
		if since, ok := TransactionTypeSince(name); !ok || since != "1.1.0" {
			t.Errorf("%s: %q %v, want 1.1.0", name, since, ok)
		}
	}
	for _, name := range []string{"pas-claim", "pa.pas-claim", "federated-query", "no-such-leg", ""} {
		if since, ok := TransactionTypeSince(name); ok || since != "" {
			t.Errorf("%s: %q %v, want unmarked", name, since, ok)
		}
	}
}

// A holder's attachment limit round-trips as itself: absent (nil) is omitted
// and decodes to nil, and a declared value is served as declared.
func TestAttachmentMaxBytesRoundTrip(t *testing.T) {
	b, _ := json.Marshal(Holder{ID: "h"})
	if strings.Contains(string(b), "attachmentMaxBytes") {
		t.Fatalf("absent limit marshalled as %s", b)
	}
	var h Holder
	if err := json.Unmarshal(b, &h); err != nil || h.AttachmentMaxBytes != nil {
		t.Fatalf("absent decoded as %v (%v), want nil", h.AttachmentMaxBytes, err)
	}
	n := int64(5 << 20)
	b, _ = json.Marshal(Holder{ID: "h", AttachmentMaxBytes: &n})
	h = Holder{}
	if err := json.Unmarshal(b, &h); err != nil || h.AttachmentMaxBytes == nil || *h.AttachmentMaxBytes != n || !strings.Contains(string(b), `"attachmentMaxBytes":5242880`) {
		t.Fatalf("a limit: %s decoded as %v (%v)", b, h.AttachmentMaxBytes, err)
	}
}

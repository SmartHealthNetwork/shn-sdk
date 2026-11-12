package shnsdk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func recordToken(t *testing.T, tok Token) string {
	t.Helper()
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("marshal token: %v", err)
	}
	return string(b)
}

// validRequesterBinding is a bound record whose tokens are well formed. Its
// signatures are not real: Validate checks structure only.
func validRequesterBinding(t *testing.T) RequesterBinding {
	t.Helper()
	exp := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	req := Token{Operation: "cdex-task-read", Frame: "payer-tpo", CorrelationID: "corr-1", Holder: "payer-1", PayloadHash: strings.Repeat("a", 64), Expiry: exp, Signature: []byte("s")}
	resp := Token{Operation: "cdex-task-read-response", Frame: "payer-tpo", Subject: "pci:prov", CorrelationID: "corr-1", Holder: "provider-1", PayloadHash: strings.Repeat("b", 64), Expiry: exp, Signature: []byte("s")}
	inv := resp
	inv.Subject, inv.Holder, inv.Involvement = "pci:payer", "payer-1", InvolvementRequesterHeld
	return RequesterBinding{
		RequestToken:  recordToken(t, req),
		ResponseToken: recordToken(t, resp),
		Binding:       RequesterBindingBound,
		Involved:      &InvolvedToken{Token: recordToken(t, inv), Involvement: InvolvementRequesterHeld},
	}
}

// A bound record and an unavailable one validate, and each crosses the wire
// and back as itself.
func TestRequesterBinding_Valid(t *testing.T) {
	bound := validRequesterBinding(t)
	derived := validRequesterBinding(t)
	var inv Token
	if err := json.Unmarshal([]byte(derived.Involved.Token), &inv); err != nil {
		t.Fatal(err)
	}
	inv.Involvement = InvolvementRequesterDerived
	derived.Involved = &InvolvedToken{Token: recordToken(t, inv), Involvement: InvolvementRequesterDerived}
	unavailable := validRequesterBinding(t)
	unavailable.Binding, unavailable.Involved = RequesterBindingUnavailable, nil
	for name, r := range map[string]RequesterBinding{"bound, held": bound, "bound, derived": derived, "unavailable": unavailable} {
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: Validate: %v", name, err)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		got, err := DecodeRequesterBinding(b)
		if err != nil {
			t.Fatalf("%s: decode %s: %v", name, b, err)
		}
		if got.RequestToken != r.RequestToken || got.ResponseToken != r.ResponseToken || got.Binding != r.Binding || (got.Involved == nil) != (r.Involved == nil) || (got.Involved != nil && *got.Involved != *r.Involved) {
			t.Fatalf("%s: round trip %+v, want %+v", name, got, r)
		}
		if name == "unavailable" && strings.Contains(string(b), `"involved"`) {
			t.Fatalf("an unavailable record carries involved: %s", b)
		}
	}
}

// Every branch of Validate refuses: the rejection rows of the record's
// structure, refused 400 at the Hub as message integrity.
func TestRequesterBinding_ValidateRefuses(t *testing.T) {
	notInvolved := func(t *testing.T) string {
		var inv Token
		if err := json.Unmarshal([]byte(validRequesterBinding(t).Involved.Token), &inv); err != nil {
			t.Fatal(err)
		}
		inv.Involvement = ""
		return recordToken(t, inv)
	}
	for _, row := range []struct {
		name   string
		mutate func(t *testing.T, r *RequesterBinding)
	}{
		{"no request token", func(t *testing.T, r *RequesterBinding) { r.RequestToken = "" }},
		{"a request token that is not JSON", func(t *testing.T, r *RequesterBinding) { r.RequestToken = "not json" }},
		{"a request token that is not an object", func(t *testing.T, r *RequesterBinding) { r.RequestToken = "null" }},
		{"a request token of the wrong shape", func(t *testing.T, r *RequesterBinding) { r.RequestToken = `{"expiry":"tomorrow"}` }},
		{"no response token", func(t *testing.T, r *RequesterBinding) { r.ResponseToken = "" }},
		{"a response token that is not an object", func(t *testing.T, r *RequesterBinding) { r.ResponseToken = `"x"` }},
		{"no binding", func(t *testing.T, r *RequesterBinding) { r.Binding = "" }},
		{"a binding outside the pair", func(t *testing.T, r *RequesterBinding) { r.Binding = "Bound" }},
		{"another binding outside the pair", func(t *testing.T, r *RequesterBinding) { r.Binding = "no-member-match" }},
		{"bound with no involved token", func(t *testing.T, r *RequesterBinding) { r.Involved = nil }},
		{"bound with a responder-side involvement", func(t *testing.T, r *RequesterBinding) { r.Involved.Involvement = InvolvementResponderHeld }},
		{"bound with no involvement", func(t *testing.T, r *RequesterBinding) { r.Involved.Involvement = "" }},
		{"bound with an empty involved token", func(t *testing.T, r *RequesterBinding) { r.Involved.Token = "" }},
		{"bound with an involved token that is not an object", func(t *testing.T, r *RequesterBinding) { r.Involved.Token = "[]" }},
		{"bound, the record's involvement not the token's", func(t *testing.T, r *RequesterBinding) {
			r.Involved.Involvement = InvolvementRequesterDerived
		}},
		{"bound, the token with no involvement", func(t *testing.T, r *RequesterBinding) { r.Involved.Token = notInvolved(t) }},
		{"unavailable with an involved token", func(t *testing.T, r *RequesterBinding) { r.Binding = RequesterBindingUnavailable }},
	} {
		r := validRequesterBinding(t)
		row.mutate(t, &r)
		if err := r.Validate(); !errors.Is(err, ErrRequesterBinding) {
			t.Fatalf("%s: Validate = %v, want ErrRequesterBinding", row.name, err)
		}
		if b, err := json.Marshal(r); err == nil {
			if _, err := DecodeRequesterBinding(b); !errors.Is(err, ErrRequesterBinding) {
				t.Fatalf("%s: DecodeRequesterBinding = %v, want ErrRequesterBinding", row.name, err)
			}
		}
	}
}

// The body is read strictly: over the cap is refused before it is read (413
// at the Hub, message integrity), and a member the type does not name, a
// second value, or a body that is not one object is refused (400).
func TestDecodeRequesterBinding_Refuses(t *testing.T) {
	good, err := json.Marshal(validRequesterBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequesterBinding(good); err != nil {
		t.Fatalf("the valid body: %v", err)
	}
	over := append([]byte(nil), good...)
	over = append(over[:len(over)-1], []byte(`,"pad":"`+strings.Repeat("x", MaxRequesterBindingBytes)+`"}`)...)
	if _, err := DecodeRequesterBinding(over); !errors.Is(err, ErrRequesterBindingTooLarge) {
		t.Fatalf("over the cap: %v, want ErrRequesterBindingTooLarge", err)
	}
	for name, body := range map[string]string{
		"an unknown member":                   strings.TrimSuffix(string(good), "}") + `,"taskId":"Task/1"}`,
		"a second value":                      string(good) + " {}",
		"trailing garbage":                    string(good) + "x",
		"an array":                            "[" + string(good) + "]",
		"empty":                               "",
		"null":                                "null",
		"a patient member":                    strings.TrimSuffix(string(good), "}") + `,"patient":"MBR-1"}`,
		"truncated":                           string(good[:len(good)/2]),
		"a string":                            `"x"`,
		"an empty object":                     "{}",
		"a duplicate member":                  strings.TrimSuffix(string(good), "}") + `,"binding":"bound"}`,
		"a member in another case":            strings.Replace(string(good), `"binding"`, `"Binding"`, 1),
		"a member in another case, beside it": strings.TrimSuffix(string(good), "}") + `,"Binding":"unavailable"}`,
		"an unknown member of involved":       strings.Replace(string(good), `"involvement":"requester-held"}`, `"involvement":"requester-held","patient":"MBR-1"}`, 1),
		"a duplicate member of involved":      strings.Replace(string(good), `"involvement":"requester-held"}`, `"involvement":"requester-held","involvement":"requester-held"}`, 1),
		"involved in another case":            strings.Replace(string(good), `"involvement":"requester-held"}`, `"Involvement":"requester-held"}`, 1),
		"involved not an object":              strings.TrimSuffix(strings.SplitN(string(good), `,"involved"`, 2)[0], "}") + `,"involved":"x"}`,
		// Invalid UTF-8 where Validate does not look, inside a token: without
		// the UTF-8 check this body decodes cleanly.
		"not UTF-8, inside a token":  strings.Replace(string(good), "cdex-task-read-response", "cdex-task-read-\xffresponse", 1),
		"not UTF-8":                  strings.Replace(string(good), `"binding":"bound"`, "\"binding\":\"b\xffound\"", 1),
		"involved given as null":     strings.Replace(strings.TrimSuffix(strings.SplitN(string(good), `,"involved"`, 2)[0], "}"), `"binding":"bound"`, `"binding":"unavailable"`, 1) + `,"involved":null}`,
		"a member of the wrong type": strings.Replace(string(good), `"binding":"bound"`, `"binding":true`, 1),
	} {
		if _, err := DecodeRequesterBinding([]byte(body)); !errors.Is(err, ErrRequesterBinding) {
			t.Fatalf("%s: %v, want ErrRequesterBinding", name, err)
		}
	}
	// The unavailable record the null row spells with "involved": null decodes
	// without it, so that row is refused for the null alone.
	unavailableBody := strings.Replace(strings.SplitN(string(good), `,"involved"`, 2)[0], `"binding":"bound"`, `"binding":"unavailable"`, 1) + "}"
	if _, err := DecodeRequesterBinding([]byte(unavailableBody)); err != nil {
		t.Fatalf("the unavailable body %s: %v", unavailableBody, err)
	}
	// Whitespace around the one object is not a second value.
	if _, err := DecodeRequesterBinding([]byte("\n " + string(good) + " \n")); err != nil {
		t.Fatalf("surrounding whitespace: %v", err)
	}
}

func TestRequesterBinding_Names(t *testing.T) {
	if HubRequesterBindingPath != "/requester-binding" || RequesterBindingBound != "bound" || RequesterBindingUnavailable != "unavailable" || MaxRequesterBindingBytes != 64<<10 {
		t.Fatalf("names changed: %q %q %q %d", HubRequesterBindingPath, RequesterBindingBound, RequesterBindingUnavailable, MaxRequesterBindingBytes)
	}
}

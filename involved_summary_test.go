package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

func summaryKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func summaryLegToken(sig string) Token {
	return Token{Operation: "pas-claim", Frame: "provider-tpo", CorrelationID: "corr-1", Holder: "provider-1", Signature: []byte(sig)}
}

func signedSummary(t *testing.T, priv ed25519.PrivateKey, side string, leg Token) InvolvedSummary {
	t.Helper()
	return SignInvolvedSummary(InvolvedSummary{
		Side: side, Leg: InvolvedSummaryLeg(leg), Listed: 2,
		Omitted: []InvolvedOmission{{Reason: "budget", Count: 1}, {Reason: "unknown-member", Count: 3}},
	}, priv)
}

func TestVerifyInvolvedSummary_EachSideVerifiesForItsOwnLeg(t *testing.T) {
	pub, priv := summaryKey(t)
	leg := summaryLegToken("leg-signature")
	for _, side := range []string{InvolvedSideRequest, InvolvedSideAnswer} {
		if err := VerifyInvolvedSummary(signedSummary(t, priv, side, leg), pub, side, leg); err != nil {
			t.Errorf("%s: %v", side, err)
		}
	}
}

// Every way a summary can fail is refused, each as ErrInvolvedSummary: a
// reader takes any of them as not knowing whether the list was complete.
func TestVerifyInvolvedSummary_RefusesEveryOtherSummary(t *testing.T) {
	pub, priv := summaryKey(t)
	_, retiredPriv := summaryKey(t)
	leg := summaryLegToken("leg-signature")
	good := signedSummary(t, priv, InvolvedSideRequest, leg)
	edit := func(f func(*InvolvedSummary)) InvolvedSummary {
		s := good
		s.Omitted = slices.Clone(good.Omitted)
		s.Sig = slices.Clone(good.Sig)
		f(&s)
		return s
	}
	for _, c := range []struct {
		name string
		s    InvolvedSummary
		pub  ed25519.PublicKey
		side string
		leg  Token
	}{
		{"a request's summary replayed as the answer's", good, pub, InvolvedSideAnswer, leg},
		{"another side, re-signed, read as this side", signedSummary(t, priv, InvolvedSideAnswer, leg), pub, InvolvedSideRequest, leg},
		{"another leg's summary", good, pub, InvolvedSideRequest, summaryLegToken("another-leg")},
		{"a leg token with no signature", SignInvolvedSummary(InvolvedSummary{Side: InvolvedSideRequest, Leg: InvolvedSummaryLeg(Token{})}, priv), pub, InvolvedSideRequest, Token{}},
		{"listed edited", edit(func(s *InvolvedSummary) { s.Listed = 3 }), pub, InvolvedSideRequest, leg},
		{"an omission removed", edit(func(s *InvolvedSummary) { s.Omitted = s.Omitted[:1] }), pub, InvolvedSideRequest, leg},
		{"a count edited", edit(func(s *InvolvedSummary) { s.Omitted[0].Count = 9 }), pub, InvolvedSideRequest, leg},
		{"the signature altered", edit(func(s *InvolvedSummary) { s.Sig[0] ^= 1 }), pub, InvolvedSideRequest, leg},
		{"no signature", edit(func(s *InvolvedSummary) { s.Sig = nil }), pub, InvolvedSideRequest, leg},
		{"signed by a key the sender no longer has registered", signedSummary(t, retiredPriv, InvolvedSideRequest, leg), pub, InvolvedSideRequest, leg},
		{"no valid verifying key", good, ed25519.PublicKey{1, 2, 3}, InvolvedSideRequest, leg},
		{"a side that is neither", signedSummary(t, priv, "both", leg), pub, "both", leg},
	} {
		if err := VerifyInvolvedSummary(c.s, c.pub, c.side, c.leg); !errors.Is(err, ErrInvolvedSummary) {
			t.Errorf("%s: %v, want ErrInvolvedSummary", c.name, err)
		}
	}
}

// The signature covers the domain prefix: a signature by the same key over the
// summary's bare JSON, which a holder key might sign as another message, does
// not verify.
func TestVerifyInvolvedSummary_TheSignatureIsOverItsOwnDomain(t *testing.T) {
	pub, priv := summaryKey(t)
	leg := summaryLegToken("leg-signature")
	s := signedSummary(t, priv, InvolvedSideRequest, leg)
	payload := involvedSummarySigningPayload(s)
	if !strings.HasPrefix(string(payload), involvedSummaryDomain) {
		t.Fatalf("signed bytes do not start with the domain: %q", payload)
	}
	s.Sig = ed25519.Sign(priv, payload[len(involvedSummaryDomain):])
	if err := VerifyInvolvedSummary(s, pub, InvolvedSideRequest, leg); !errors.Is(err, ErrInvolvedSummary) {
		t.Errorf("a signature over the bare JSON verified: %v", err)
	}
}

// A nil and an empty Omitted sign and encode alike, so a summary that left no
// one out has one form.
func TestInvolvedSummary_NoOmissionHasOneForm(t *testing.T) {
	pub, priv := summaryKey(t)
	leg := summaryLegToken("leg-signature")
	signedNil := SignInvolvedSummary(InvolvedSummary{Side: InvolvedSideRequest, Leg: InvolvedSummaryLeg(leg)}, priv)
	empty := signedNil
	empty.Omitted = []InvolvedOmission{}
	if err := VerifyInvolvedSummary(empty, pub, InvolvedSideRequest, leg); err != nil {
		t.Errorf("an empty Omitted does not verify a nil one's signature: %v", err)
	}
	a, _ := EncodeInvolvedSummary(signedNil)
	b, _ := EncodeInvolvedSummary(empty)
	if a != b || !strings.Contains(a, `"omitted":[]`) {
		t.Errorf("nil and empty encode differently: %s vs %s", a, b)
	}
}

func TestInvolvedSummary_EncodeDecodeRoundTrip(t *testing.T) {
	pub, priv := summaryKey(t)
	leg := summaryLegToken("leg-signature")
	s := signedSummary(t, priv, InvolvedSideAnswer, leg)
	enc, err := EncodeInvolvedSummary(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeInvolvedSummary(enc)
	if err != nil || got == nil {
		t.Fatalf("decode: %v %v", got, err)
	}
	if err := VerifyInvolvedSummary(*got, pub, InvolvedSideAnswer, leg); err != nil {
		t.Errorf("decoded summary does not verify: %v", err)
	}
	if got, err := DecodeInvolvedSummary(""); got != nil || err != nil {
		t.Errorf(`"" decodes as %v, %v; want no summary`, got, err)
	}
	if _, err := DecodeInvolvedSummary(`{"side":`); err == nil {
		t.Error("an undecodable summary decoded")
	}
}

// Metadata with no summary encodes exactly as before the field existed.
func TestMetadata_NoInvolvedSummaryKeepsItsBytes(t *testing.T) {
	b, err := json.Marshal(Metadata{Sender: "a", Recipient: "b", Involved: "[]"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "involvedSummary") {
		t.Errorf("an unset summary is encoded: %s", b)
	}
	b, _ = json.Marshal(Metadata{InvolvedSummary: "{}"})
	if !strings.Contains(string(b), `"involvedSummary":"{}"`) {
		t.Errorf("a set summary is not encoded under involvedSummary: %s", b)
	}
}

// A gateway's reasons are the record's, less the two only the Hub writes.
func TestInvolvedSummaryOmitReasons_AreTheRecordsLessTheHubsOwn(t *testing.T) {
	got := InvolvedSummaryOmitReasons()
	var want []string
	for _, r := range AuditInvolvedOmitReasons() {
		if r != AuditInvolvedUnrecognized && r != AuditInvolvedUnverified {
			want = append(want, r)
		}
	}
	if !slices.Equal(got, want) || !slices.IsSorted(got) || len(got) != len(AuditInvolvedOmitReasons())-2 {
		t.Errorf("InvolvedSummaryOmitReasons = %v, want %v", got, want)
	}
	got[0] = "changed"
	if InvolvedSummaryOmitReasons()[0] == "changed" {
		t.Error("InvolvedSummaryOmitReasons returns a shared list")
	}
}

// The record's complete is the sending gateway's own statement. Its doc says
// so, and never says the network verified the list complete.
func TestInvolvedComplete_IsDocumentedAsTheGatewaysStatement(t *testing.T) {
	doc := constDoc(t, "audit.go", "InvolvedComplete")
	for _, must := range []string{"stated", "own statement"} {
		if !strings.Contains(doc, must) {
			t.Errorf("InvolvedComplete's doc does not say %q:\n%s", must, doc)
		}
	}
	if strings.Contains(strings.ToLower(doc), "verif") {
		t.Errorf("InvolvedComplete's doc speaks of verification:\n%s", doc)
	}
}

// constDoc is the doc comment of the const name declared in file.
func constDoc(t *testing.T, file, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for _, n := range vs.Names {
				if n.Name == name && vs.Doc != nil {
					return vs.Doc.Text()
				}
			}
		}
	}
	t.Fatalf("no documented const %s in %s", name, file)
	return ""
}

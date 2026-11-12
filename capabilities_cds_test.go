package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// A payer's CDS service listing as its own system wrote it: an entry's members
// in the payer's order, an HTML-special character left as written, and the
// extension the CRD configuration options live in.
const payerCDSServices = `[{"hook":"order-sign","id":"order-sign-crd","title":"Coverage <requirements> & more","prefetch":{"patient":"Patient/{{context.patientId}}"},"extension":{"davinci-crd.configuration-options":[{"code":"coverage-info","type":"boolean","name":"Coverage Information","description":"Return coverage-information system actions.","default":true}]}},{"id":"order-dispatch-crd","hook":"order-dispatch"}]`

// DeclareCapabilities sends the CDS service listing it is given as the payer
// wrote it (insignificant whitespace aside: the members, their order and every
// value unchanged, HTML-special characters included), and only then. Absent
// declares none.
func TestDeclareCapabilitiesCarriesTheCDSServiceListingAsWritten(t *testing.T) {
	id, err := GenerateIdentity("h-cds")
	if err != nil {
		t.Fatal(err)
	}
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		answerWithDigest(t, w, sent)
	}))
	defer srv.Close()
	ctx := context.Background()

	// Written with whitespace, as a payer's own system may serve it.
	spaced := strings.Replace(payerCDSServices, `,"id":"order-sign-crd"`, `,  "id" : "order-sign-crd"`, 1)
	stored, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(spaced)})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if want := `{"legs":[],"cdsServices":` + payerCDSServices + `}`; string(sent) != want {
		t.Fatalf("body:\n%s\nwant:\n%s", sent, want)
	}
	if stored.CDSServicesDigest != DigestCDSServices([]byte(payerCDSServices)) || stored.CDSServices != nil {
		t.Fatalf("answered digest %q, listing %s: want the digest of the compact listing and no listing", stored.CDSServicesDigest, stored.CDSServices)
	}

	if _, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}}); err != nil || string(sent) != `{"legs":[]}` {
		t.Fatalf("no listing: %v, body %s, want the key omitted", err, sent)
	}
	// The digest is the answer's, never sent, even when the caller sets it.
	if _, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServicesDigest: DigestCDSServices([]byte(`[]`))}); err != nil || string(sent) != `{"legs":[]}` {
		t.Fatalf("a digest set by the caller: %v, body %s, want it not sent", err, sent)
	}
	if _, err := id.DeclareCapabilities(ctx, srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(`[]`)}); err != nil || string(sent) != `{"legs":[],"cdsServices":[]}` {
		t.Fatalf("an empty listing: %v, body %s", err, sent)
	}
}

// The listing is compacted, never decoded and re-encoded: number spellings
// (1.0, 1e2), \u escapes and member order come out exactly as written, so the
// body holds the listing's json.Compact form byte for byte.
func TestTheCDSServiceListingIsCompactedNeverReencoded(t *testing.T) {
	id, err := GenerateIdentity("h-cds-compact")
	if err != nil {
		t.Fatal(err)
	}
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = w.Write(sent)
	}))
	defer srv.Close()
	written := "[ {\"title\": \"caf\\u00e9 \\u003cCRD\\u003e\", \"id\": \"z-svc\", \"hook\" : \"order-sign\",\n" +
		"  \"extension\": {\"davinci-crd.configuration-options\": [{\"code\":\"max-cards\",\"type\":\"integer\",\"default\": 1e2, \"weight\": 1.0}]}} ]"
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(written)); err != nil {
		t.Fatal(err)
	}
	if _, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(written)}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if want := `{"legs":[],"cdsServices":` + compact.String() + `}`; string(sent) != want {
		t.Fatalf("body:\n%s\nwant:\n%s", sent, want)
	}
	for _, kept := range []string{`\u00e9`, `\u003cCRD\u003e`, `1e2`, `1.0`, `{"title":`} {
		if !strings.Contains(string(sent), kept) {
			t.Errorf("%s was rewritten: %s", kept, sent)
		}
	}
}

// A listing that names a member twice in one object, at any depth, cannot be
// read unambiguously (which id is the service's? which option's default?), so
// it is refused before anything is sent: a refusal on message integrity.
func TestACDSServiceListingWithADuplicateMemberIsRefused(t *testing.T) {
	id, err := GenerateIdentity("h-cds-dup")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	for name, listing := range map[string]string{
		"a duplicate id":         `[{"id":"a","id":"b","hook":"order-sign"}]`,
		"a duplicate hook":       `[{"id":"a","hook":"order-sign","hook":"order-select"}]`,
		"in the prefetch":        `[{"id":"a","hook":"order-sign","prefetch":{"patient":"Patient/{{context.patientId}}","patient":"Patient/x"}}]`,
		"deep in an option":      `[{"id":"a","hook":"order-sign","extension":{"davinci-crd.configuration-options":[{"code":"c","default":true,"default":false}]}}]`,
		"the same in two places": `[{"id":"a","hook":"order-sign","extension":{"x":1,"x":1}}]`,
	} {
		_, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(listing)})
		if err == nil || !strings.Contains(err.Error(), "twice") {
			t.Errorf("%s: err=%v, want a duplicate-member refusal", name, err)
		}
	}
	if calls != 0 {
		t.Fatalf("a refused listing reached the registrar")
	}
	// The same member name in two different objects is no duplicate.
	if _, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(`[{"id":"a","hook":"order-sign","prefetch":{"id":"x"}},{"id":"b","hook":"order-select"}]`)}); err != nil {
		t.Fatalf("one name in two objects: %v", err)
	}
}

// JSON names a member case-sensitively (RFC 8259), so the listing is read so:
// "ID" is not "id" and "HOOK" is not "hook". A listing naming both "id" and
// "ID" in one entry is unambiguous and declared, an entry's "ID" is not its
// id, and a "HOOK" does not stand in for a missing "hook".
func TestTheCDSServiceListingIsReadCaseSensitively(t *testing.T) {
	id, err := GenerateIdentity("h-cds-case")
	if err != nil {
		t.Fatal(err)
	}
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		answerWithDigest(t, w, sent)
	}))
	defer srv.Close()
	for _, listing := range []string{
		`[{"id":"a","ID":"a","hook":"order-sign"}]`,
		`[{"id":"a","hook":"order-sign"},{"id":"b","hook":"order-sign","ID":"a"}]`,
		`[{"id":"a","hook":"order-sign","HOOK":"order-select","Hook":"order-dispatch"}]`,
	} {
		if _, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(listing)}); err != nil {
			t.Errorf("%s: %v, want it declared", listing, err)
		} else if want := `{"legs":[],"cdsServices":` + listing + `}`; string(sent) != want {
			t.Errorf("sent %s, want %s", sent, want)
		}
	}
	for _, listing := range []string{
		`[{"ID":"a","hook":"order-sign"}]`,
		`[{"id":"a","HOOK":"order-sign"}]`,
	} {
		if _, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(listing)}); err == nil {
			t.Errorf("%s: declared, though it names no id or no hook", listing)
		}
	}
}

// A listing the registry would not hold is refused before anything is sent:
// not a JSON array, an entry that is not an object or names no id or hook as a
// non-empty string, an id named twice, or more than maxDeclaredCDSServicesBytes.
func TestDeclareCapabilitiesRefusesAMalformedCDSServiceListing(t *testing.T) {
	id, err := GenerateIdentity("h-cds-bad")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	big := `[{"id":"a","hook":"order-sign","title":"` + strings.Repeat("x", maxDeclaredCDSServicesBytes) + `"}]`
	for name, listing := range map[string]string{
		"not JSON":          `[{"id":`,
		"null":              `null`,
		"an object":         `{"services":[]}`,
		"a string entry":    `["order-sign-crd"]`,
		"no id":             `[{"hook":"order-sign"}]`,
		"no hook":           `[{"id":"a"}]`,
		"an empty id":       `[{"id":"","hook":"order-sign"}]`,
		"an empty hook":     `[{"id":"a","hook":""}]`,
		"a numeric id":      `[{"id":7,"hook":"order-sign"}]`,
		"an id named twice": `[{"id":"a","hook":"order-sign"},{"id":"a","hook":"order-select"}]`,
		"over the cap":      big,
		"trailing data":     `[] []`,
		"invalid UTF-8":     "[{\"id\":\"a\",\"hook\":\"order-sign\",\"title\":\"\xff\"}]",
	} {
		if _, err := id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, CapabilityDeclaration{Legs: []string{}, CDSServices: json.RawMessage(listing)}); err == nil {
			t.Errorf("%s: declared", name)
		}
	}
	if calls != 0 {
		t.Fatalf("a refused listing reached the registrar")
	}
}

// answerWithDigest answers a declaration as the registrar does: the stored
// declaration, naming the listing by its digest, never carrying it.
func answerWithDigest(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	var d CapabilityDeclaration
	if err := json.Unmarshal(body, &d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.CDSServices != nil {
		d.CDSServicesDigest = DigestCDSServices(d.CDSServices)
	}
	d.CDSServices = nil
	_ = json.NewEncoder(w).Encode(d)
}

// The registrar's answer names the listing it holds by its digest, which
// must be the digest of the listing declared, and of none when none was: a
// registrar that holds anything else is not holding the declaration. One that
// predates the listing answers no digest, and that is returned as it is.
func TestTheDeclarationAnswerNamesTheListingHeldByItsDigest(t *testing.T) {
	id, err := GenerateIdentity("h-cds-digest")
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	declare := func(listing string) (CapabilityDeclaration, error) {
		d := CapabilityDeclaration{Legs: []string{}}
		if listing != "" {
			d.CDSServices = json.RawMessage(listing)
		}
		return id.DeclareCapabilities(context.Background(), srv.Client(), srv.URL, d)
	}
	held := DigestCDSServices([]byte(payerCDSServices))
	other := DigestCDSServices([]byte(`[]`))
	for _, c := range []struct {
		name, listing, answer string
		ok                    bool
	}{
		{"the digest of the listing declared", payerCDSServices, `{"legs":[],"cdsServicesDigest":"` + held + `"}`, true},
		{"none, from a registrar that predates the listing", payerCDSServices, `{"legs":[]}`, true},
		{"none, for none declared", "", `{"legs":[]}`, true},
		{"the digest of other bytes", payerCDSServices, `{"legs":[],"cdsServicesDigest":"` + other + `"}`, false},
		{"a digest, for none declared", "", `{"legs":[],"cdsServicesDigest":"` + held + `"}`, false},
		{"the digest of no bytes, for none declared", "", `{"legs":[],"cdsServicesDigest":"` + DigestCDSServices(nil) + `"}`, false},
		{"the right digest beside a listing, which is dropped", payerCDSServices, `{"legs":[],"cdsServices":[{"id":"x","hook":"order-sign"}],"cdsServicesDigest":"` + held + `"}`, true},
		{"an answer over its bound", payerCDSServices, `{"legs":[],"cdsServicesDigest":"` + held + `","x":"` + strings.Repeat("x", maxDeclarationAnswerBytes) + `"}`, false},
		{"not a digest", payerCDSServices, `{"legs":[],"cdsServicesDigest":"` + strings.ToUpper(held) + `"}`, false},
	} {
		answer = c.answer
		stored, err := declare(c.listing)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want accepted=%v", c.name, err, c.ok)
			continue
		}
		if c.ok && stored.CDSServices != nil {
			t.Errorf("%s: the answer's listing was returned", c.name)
		}
	}
}

// A holder's row on the feed names the listing its system declared by its
// digest, and omits the key when there is none.
func TestHolderNamesTheDeclaredListingByItsDigest(t *testing.T) {
	d := DigestCDSServices([]byte(payerCDSServices))
	var h Holder
	if err := json.Unmarshal([]byte(`{"id":"p","role":"payer","cdsServicesDigest":"`+d+`"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.CDSServicesDigest != d {
		t.Fatalf("decoded %q", h.CDSServicesDigest)
	}
	out, err := json.Marshal(Holder{ID: "p", Role: "payer"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "cdsServices") {
		t.Fatalf("no listing marshalled as %s, want the key omitted", out)
	}
	if len(d) != 64 || strings.ToLower(d) != d || !validCDSServicesDigest(d) {
		t.Fatalf("digest %q is not 64 lowercase hex digits", d)
	}
}

// FetchCDSServices returns the listing the registrar serves only once it is
// verified to be the one its digest names: the same bytes, compact, a
// listing. Anything else is refused as integrity and nothing is returned,
// including bytes that match the digest under an ETag naming another one.
func TestFetchCDSServicesVerifiesTheListingAgainstItsDigest(t *testing.T) {
	digest := DigestCDSServices([]byte(payerCDSServices))
	var status int
	var body, etag string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	fetch := func(d string) (json.RawMessage, error) {
		return FetchCDSServices(context.Background(), srv.Client(), srv.URL+"/", "payer a/b", d)
	}

	status, body, etag = http.StatusOK, payerCDSServices, `"`+digest+`"`
	got, err := fetch(digest)
	if err != nil || string(got) != payerCDSServices {
		t.Fatalf("the listing its digest names: %s, %v", got, err)
	}
	if paths[0] != "/holders/payer%20a%2Fb/cds-services" {
		t.Fatalf("asked for %s", paths[0])
	}
	etag = ""
	if got, err := fetch(digest); err != nil || string(got) != payerCDSServices {
		t.Fatalf("with no ETag, the content check alone: %s, %v", got, err)
	}
	etag = `W/"` + digest + `"`
	if got, err := fetch(digest); err != nil || string(got) != payerCDSServices {
		t.Fatalf("under a weak ETag naming its digest: %s, %v", got, err)
	}
	// An ETag naming no digest, as an intermediary rewrites one, is left to
	// the content checks.
	etag = `"` + digest + `-gzip"`
	if got, err := fetch(digest); err != nil || string(got) != payerCDSServices {
		t.Fatalf("under an ETag naming no digest: %s, %v", got, err)
	}
	// A listing of exactly the cap is fetched.
	head, tail := `[{"id":"a","hook":"order-sign","title":"`, `"}]`
	atCap := head + strings.Repeat("x", maxDeclaredCDSServicesBytes-len(head)-len(tail)) + tail
	status, body, etag = http.StatusOK, atCap, ""
	if got, err := fetch(DigestCDSServices([]byte(atCap))); err != nil || len(got) != maxDeclaredCDSServicesBytes {
		t.Fatalf("a listing of exactly %d bytes: %d bytes, %v", maxDeclaredCDSServicesBytes, len(got), err)
	}

	spaced := strings.Replace(payerCDSServices, `,"id":"order-sign-crd"`, `, "id":"order-sign-crd"`, 1)
	over := `[{"id":"a","hook":"order-sign","title":"` + strings.Repeat("x", maxDeclaredCDSServicesBytes) + `"}]`
	for _, c := range []struct {
		name, body, etag, digest string
	}{
		{"other bytes", `[]`, `"` + digest + `"`, digest},
		{"its bytes under an ETag naming another digest", payerCDSServices, `"` + DigestCDSServices([]byte(`[]`)) + `"`, digest},
		{"a weak ETag naming another digest", payerCDSServices, `W/"` + DigestCDSServices([]byte(`[]`)) + `"`, digest},
		{"the listing not compact, under its own digest", spaced, "", DigestCDSServices([]byte(spaced))},
		{"not a listing, under its own digest", `{"id":"a"}`, "", DigestCDSServices([]byte(`{"id":"a"}`))},
		{"more than any listing", over, "", DigestCDSServices([]byte(over))},
	} {
		status, body, etag = http.StatusOK, c.body, c.etag
		if got, err := fetch(c.digest); !errors.Is(err, ErrCDSServicesIntegrity) || got != nil {
			t.Errorf("%s: %s, %v; want ErrCDSServicesIntegrity and nothing", c.name, got, err)
		}
	}

	status, body, etag = http.StatusNotFound, `{"error":"no CDS service listing"}`, ""
	if _, err := fetch(digest); !errors.Is(err, ErrNoCDSServices) {
		t.Fatalf("404: %v, want ErrNoCDSServices", err)
	}
	status = http.StatusBadGateway
	if _, err := fetch(digest); err == nil || errors.Is(err, ErrNoCDSServices) || errors.Is(err, ErrCDSServicesIntegrity) {
		t.Fatalf("502: %v, want a plain failure", err)
	}
	asked := len(paths)
	if _, err := fetch("not-a-digest"); err == nil || len(paths) != asked {
		t.Fatalf("a malformed digest: %v, asked %d times, want refused before asking", err, len(paths)-asked)
	}
	if _, err := FetchCDSServices(context.Background(), srv.Client(), srv.URL, "", digest); err == nil || len(paths) != asked {
		t.Fatalf("no holder id: %v, asked %d times, want refused before asking", err, len(paths)-asked)
	}
}

// countingBody counts the bytes a reader takes from a response body.
type countingBody struct {
	io.ReadCloser
	n *int
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	*b.n += n
	return n, err
}

type countingTransport struct {
	next http.RoundTripper
	n    *int
}

func (t countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err == nil {
		resp.Body = countingBody{resp.Body, t.n}
	}
	return resp, err
}

// FetchCDSServices reads no more than one byte past the largest listing,
// however much the registrar sends.
func TestFetchCDSServicesReadsNoMoreThanAListingCanBe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("[", 32<<10))
		for range 64 { // 2 MiB, far past any listing
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	var read int
	c := &http.Client{Transport: countingTransport{next: srv.Client().Transport, n: &read}}
	_, err := FetchCDSServices(context.Background(), c, srv.URL, "p", DigestCDSServices([]byte(`[]`)))
	if !errors.Is(err, ErrCDSServicesIntegrity) {
		t.Fatalf("an endless answer: %v, want ErrCDSServicesIntegrity", err)
	}
	if read > maxDeclaredCDSServicesBytes+1 {
		t.Fatalf("read %d bytes of the answer, want at most %d", read, maxDeclaredCDSServicesBytes+1)
	}
}

// The CDS service header rides a request frame, and only a holder that
// declares v1cds is sent it: this library's codec carries the header but does
// not act on it, so SupportedRequestFrames does not declare the token.
func TestTheCDSServiceFrameHeaderAndItsToken(t *testing.T) {
	framed, err := EncodeHTTPFrameHeaders(http.StatusOK, map[string]string{"Content-Type": "application/json", FrameHeaderCDSService: "order-sign-crd"}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := DecodeHTTPFrame(framed)
	if err != nil {
		t.Fatal(err)
	}
	if f.Headers[FrameHeaderCDSService] != "order-sign-crd" {
		t.Fatalf("headers %v: the service id did not survive the frame", f.Headers)
	}
	if !SupportsRequestFrameV1CDS([]string{"v1", RequestFrameV1CDS}) || SupportsRequestFrameV1CDS([]string{"v1", "v1op", "v1opsub"}) {
		t.Fatal("SupportsRequestFrameV1CDS reads the wrong token")
	}
	if slices.Contains(SupportedRequestFrames(), RequestFrameV1CDS) {
		t.Fatal("this library declares v1cds by default, though it does not act on the header")
	}
	if err := checkDeclaredRequestFrames([]string{RequestFrameV1CDS}); err != nil {
		t.Fatalf("the token breaks the registry's limits: %v", err)
	}
}

// The service id is carried exactly, so a value the frame's JSON would change
// is refused where it is made, and one header given twice under two
// spellings is refused too.
func TestTheCDSServiceFrameHeader_RefusesAValueTheFrameWouldChange(t *testing.T) {
	for _, v := range []string{"order-sign\ncrd", "order-sign\x00crd", "order-sign\x7fcrd", "order-sign\xffcrd"} {
		if _, err := EncodeHTTPFrameHeaders(http.StatusOK, map[string]string{FrameHeaderCDSService: v}, nil); !errors.Is(err, ErrFrameHeaderValue) {
			t.Fatalf("%q: %v, want ErrFrameHeaderValue", v, err)
		}
	}
	if _, err := EncodeHTTPFrameHeaders(http.StatusOK, map[string]string{FrameHeaderCDSService: "a", strings.ToUpper(FrameHeaderCDSService): "b"}, nil); !errors.Is(err, ErrFrameHeaderDuplicate) {
		t.Fatalf("given twice: %v, want ErrFrameHeaderDuplicate", err)
	}
}

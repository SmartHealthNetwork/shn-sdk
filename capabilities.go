package shnsdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// capabilitiesDeclarationTTL bounds a capability declaration's assertion: it
// is sent at once.
const capabilitiesDeclarationTTL = 2 * time.Minute

// declaredAttachmentMaxBytes is the largest attachment limit a declaration can
// state: the largest integer a JSON number carries exactly (2^53-1).
const declaredAttachmentMaxBytes = 1<<53 - 1

// CapabilityDeclaration is what a holder's own system declares to the
// registrar about itself (Identity.DeclareCapabilities), each time it starts.
// It replaces the Legs and AttachmentMaxBytes the registrar holds for the
// holder, together, and the request frames its system declared: RequestFrames
// is sent only when present, and a declaration without it leaves the holder
// no declared set. A registrar that predates the field ignores it.
type CapabilityDeclaration struct {
	// Legs are the legs the holder takes as a recipient (Holder.Legs): the
	// flat transaction types of the request legs its system answers. Always
	// present; an empty list declares that it takes none.
	Legs []string `json:"legs"`
	// AttachmentMaxBytes is the largest single inline attachment (file), in
	// bytes after base64 decoding, the holder's system accepts in one
	// documentation submission (Holder.AttachmentMaxBytes); nil declares none.
	AttachmentMaxBytes *int64 `json:"attachmentMaxBytes,omitempty"`
	// RequestFrames are the sealed request-frame versions the holder's
	// running system accepts (Holder.RequestFrames), as its full set. The
	// registrar keeps them apart from the set the holder's registration
	// carried, and serves them in its place while there is one. Absent (nil)
	// is not sent, and the registrar then holds no declared set for the
	// holder, so the set its registration carried (for a holder founded with
	// the network, the network's own record of it) is its own again: a system
	// rolled back to a release that declares its capabilities without request
	// frames drops, at its first start, a set a later release declared. An empty list is sent as [] and
	// declares that the system accepts none. A registrar that predates the
	// field ignores it. Each token matches ^[a-z0-9]{1,16}$, at most 8, each
	// named once.
	RequestFrames []string `json:"requestFrames,omitzero"`
	// CDSServices is the CDS Hooks service listing the holder's own system
	// serves (the `services` array of its `GET {CDS base}/cds-services`),
	// as that system wrote it: compacted, never decoded and encoded again, so
	// every entry's members, their order and their values (number spellings
	// and \u escapes included) are unchanged, insignificant whitespace aside.
	// Hold it as raw bytes end to end. Absent (nil) declares none; `[]`
	// declares a listing with no services. DeclareCapabilities sends only a
	// JSON array, at most maxDeclaredCDSServicesBytes once compacted, of
	// objects each naming a non-empty string `id` and `hook`, each id once, in
	// valid UTF-8, with no object naming a member twice at any depth (such a
	// listing cannot be read unambiguously). A registrar that predates the
	// field ignores it, within that registrar's own limit on the size of a
	// whole declaration body.
	CDSServices json.RawMessage `json:"cdsServices,omitzero"`
	// CDSServicesDigest is set only in the declaration DeclareCapabilities
	// returns, from the registrar's answer, and never sent: the digest
	// (DigestCDSServices) the answer names for the listing the registrar
	// holds, empty when the answer names none. DeclareCapabilities requires a
	// digest it reads to be the digest of the listing declared.
	CDSServicesDigest string `json:"cdsServicesDigest,omitempty"`
}

// maxDeclaredCDSServicesBytes is the largest CDS service listing, compacted,
// DeclareCapabilities sends.
const maxDeclaredCDSServicesBytes = 64 << 10

// maxDeclarationAnswerBytes bounds the answer DeclareCapabilities reads: it
// reads legs, a limit, request frames and a listing's digest from it, never a
// listing.
const maxDeclarationAnswerBytes = 64 << 10

// DigestCDSServices is the digest that names a CDS service listing: the
// lowercase hex SHA-256 of the listing's bytes in their compact form
// (CapabilityDeclaration.CDSServices). It is the digest this SDK reads from a
// holder's row (Holder.CDSServicesDigest) and from a declaration's answer,
// and the one FetchCDSServices verifies a fetched listing against.
func DigestCDSServices(listing []byte) string {
	sum := sha256.Sum256(listing)
	return hex.EncodeToString(sum[:])
}

// validCDSServicesDigest reports whether d is shaped as DigestCDSServices
// writes one: 64 lowercase hex digits.
func validCDSServicesDigest(d string) bool {
	if len(d) != 2*sha256.Size {
		return false
	}
	for _, c := range d {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// maxDeclaredRequestFrames is the most request-frame tokens a holder may
// declare, the registry's limit.
const maxDeclaredRequestFrames = 8

// DeclareCapabilities declares to the registrar at registrarURL what this
// holder's system takes as a recipient: the legs it answers (a gateway's
// compiled catalog, a Responder's Legs), when its system has one, the largest
// single inline attachment it accepts, and, when given, the request frames it
// accepts (RequestFrames). It replaces Legs and AttachmentMaxBytes as the
// registrar holds them for the holder, and the declared request frames: it
// sends RequestFrames only when given, and a declaration without them leaves
// the holder no declared set, so its registration's set is its own again. A
// registrar that predates that field ignores it. The registrar keeps
// a declaration only when it is newer than the one it holds, so a system
// declares its capabilities each time it starts. Legs must name each leg once
// by its flat name; an empty, non-nil list declares that the holder takes
// none. AttachmentMaxBytes, when set, is 1 to 2^53-1. The request is PUT
// {registrarURL}/register/{id}/capabilities, carrying a holder-self assertion
// (audience "registrar") bound to the exact body sent. It returns the
// declaration the registrar stored; a registrar that predates RequestFrames
// answers without it, so the returned RequestFrames is then nil whatever was
// sent. When the answer names a CDS service listing by its digest
// (CDSServicesDigest), it must be the digest of the listing sent, and none
// may be named when none was sent, or DeclareCapabilities fails: the
// registrar's answer then does not match what was declared. An answer that
// names no digest is returned with CDSServicesDigest empty. A nil c uses http.DefaultClient.
func (id Identity) DeclareCapabilities(ctx context.Context, c *http.Client, registrarURL string, d CapabilityDeclaration) (CapabilityDeclaration, error) {
	if c == nil {
		c = http.DefaultClient
	}
	if d.Legs == nil {
		return CapabilityDeclaration{}, errors.New("shnsdk: DeclareCapabilities: Legs is nil: pass an empty list to declare that the holder takes none")
	}
	if n := d.AttachmentMaxBytes; n != nil && (*n < 1 || *n > declaredAttachmentMaxBytes) {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: AttachmentMaxBytes %d is not 1 to 2^53-1", *n)
	}
	sorted := slices.Clone(d.Legs)
	slices.Sort(sorted)
	for i, leg := range sorted {
		if flat, ok := FlatTransactionType(leg); !ok || flat != leg {
			return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: %q is not a leg's flat transaction type", leg)
		}
		if i > 0 && sorted[i-1] == leg {
			return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: %q is named twice", leg)
		}
	}
	if err := checkDeclaredRequestFrames(d.RequestFrames); err != nil {
		return CapabilityDeclaration{}, err
	}
	listing, err := compactDeclaredCDSServices(d.CDSServices)
	if err != nil {
		return CapabilityDeclaration{}, err
	}
	// Not json.Marshal: it would HTML-escape the listing's strings, and the
	// listing is sent as the holder's system wrote it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(CapabilityDeclaration{Legs: sorted, AttachmentMaxBytes: d.AttachmentMaxBytes, RequestFrames: slices.Clone(d.RequestFrames), CDSServices: listing}); err != nil {
		return CapabilityDeclaration{}, err
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	hdr, err := id.AssertionForBody("registrar", time.Now(), capabilitiesDeclarationTTL, body)
	if err != nil {
		return CapabilityDeclaration{}, err
	}
	u := strings.TrimRight(registrarURL, "/") + "/register/" + url.PathEscape(id.HolderID) + "/capabilities"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return CapabilityDeclaration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Holder-Assertion", hdr)
	resp, err := c.Do(req)
	if err != nil {
		return CapabilityDeclaration{}, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxDeclarationAnswerBytes+1))
	if err != nil {
		return CapabilityDeclaration{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: registrar answered %d: %.300s", resp.StatusCode, answer)
	}
	if len(answer) > maxDeclarationAnswerBytes {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: the registrar's answer is over %d bytes", maxDeclarationAnswerBytes)
	}
	var stored CapabilityDeclaration
	if err := json.Unmarshal(answer, &stored); err != nil || stored.Legs == nil {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: the registrar's answer names no stored list: %.300s", answer)
	}
	if n := stored.AttachmentMaxBytes; n != nil && (*n < 1 || *n > declaredAttachmentMaxBytes) {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: the registrar's answer names an attachment limit of %d, not 1 to 2^53-1", *n)
	}
	// This SDK takes no listing from the answer, only a digest; a listing is
	// read with FetchCDSServices.
	stored.CDSServices = nil
	if held := stored.CDSServicesDigest; held != "" {
		switch {
		case !validCDSServicesDigest(held):
			return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: the registrar's answer names the CDS service listing by %.80q, not a digest", held)
		case listing == nil:
			return CapabilityDeclaration{}, errors.New("shnsdk: DeclareCapabilities: the registrar holds a CDS service listing, though this declaration declared none")
		case held != DigestCDSServices(listing):
			return CapabilityDeclaration{}, errors.New("shnsdk: DeclareCapabilities: the registrar does not hold the CDS service listing as declared: its digest differs")
		}
	}
	return stored, nil
}

// compactDeclaredCDSServices returns a declared CDS service listing with its
// insignificant whitespace removed, or refuses one DeclareCapabilities does
// not send: not a JSON array, more than maxDeclaredCDSServicesBytes compacted, an
// entry that is not an object naming a non-empty string id and hook, or an id
// named twice. nil passes as nil: it declares none.
func compactDeclaredCDSServices(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("shnsdk: DeclareCapabilities: the CDS service listing is not JSON: %w", err)
	}
	if !utf8.Valid(compact.Bytes()) {
		return nil, errors.New("shnsdk: DeclareCapabilities: the CDS service listing is not valid UTF-8, so it cannot be read unambiguously")
	}
	if compact.Len() > maxDeclaredCDSServicesBytes {
		return nil, fmt.Errorf("shnsdk: DeclareCapabilities: the CDS service listing is %d bytes, more than %d", compact.Len(), maxDeclaredCDSServicesBytes)
	}
	if !bytes.HasPrefix(compact.Bytes(), []byte("[")) {
		return nil, errors.New("shnsdk: DeclareCapabilities: the CDS service listing is not a JSON array")
	}
	if err := noDuplicateMembers(compact.Bytes()); err != nil {
		return nil, fmt.Errorf("shnsdk: DeclareCapabilities: the CDS service listing cannot be read unambiguously: %w", err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(compact.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("shnsdk: DeclareCapabilities: a CDS service listing entry is not an object: %w", err)
	}
	seen := map[string]bool{}
	for i, e := range entries {
		var id, hook string
		if json.Unmarshal(e["id"], &id) != nil || id == "" {
			return nil, fmt.Errorf("shnsdk: DeclareCapabilities: CDS service %d names no id", i)
		}
		if json.Unmarshal(e["hook"], &hook) != nil || hook == "" {
			return nil, fmt.Errorf("shnsdk: DeclareCapabilities: CDS service %q names no hook", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("shnsdk: DeclareCapabilities: CDS service %q is named twice", id)
		}
		seen[id] = true
	}
	return json.RawMessage(compact.Bytes()), nil
}

// noDuplicateMembers refuses JSON in which one object names a member twice,
// at any depth: which of the two values a reader takes is the reader's own
// choice, so such a document says nothing unambiguous. The JSON is read as
// tokens, never decoded into values, so nothing about it is changed.
func noDuplicateMembers(doc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	// One frame per open object or array: an object's member names so far,
	// and whether the next string token in it is a member name.
	type frame struct {
		names   map[string]bool
		wantKey bool
		object  bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				if top != nil && top.object {
					top.wantKey = true
				}
				stack = append(stack, &frame{names: map[string]bool{}, wantKey: true, object: true})
			case '[':
				if top != nil && top.object {
					top.wantKey = true
				}
				stack = append(stack, &frame{})
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		case string:
			if top != nil && top.object && top.wantKey {
				if top.names[t] {
					return fmt.Errorf("member %q is named twice in one object", t)
				}
				top.names[t] = true
				top.wantKey = false
				continue
			}
			if top != nil && top.object {
				top.wantKey = true
			}
		default:
			if top != nil && top.object {
				top.wantKey = true
			}
		}
	}
}

// checkDeclaredRequestFrames refuses a declared request-frame set the
// registry would not hold (more than maxDeclaredRequestFrames tokens, or a
// token that does not match ^[a-z0-9]{1,16}$), or one naming a token twice.
// nil passes: it declares nothing.
func checkDeclaredRequestFrames(frames []string) error {
	if len(frames) > maxDeclaredRequestFrames {
		return fmt.Errorf("shnsdk: DeclareCapabilities: %d request frames, more than %d", len(frames), maxDeclaredRequestFrames)
	}
	for i, f := range frames {
		if len(f) < 1 || len(f) > 16 || strings.IndexFunc(f, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) >= 0 {
			return fmt.Errorf("shnsdk: DeclareCapabilities: request frame %q does not match ^[a-z0-9]{1,16}$", f)
		}
		if slices.Contains(frames[:i], f) {
			return fmt.Errorf("shnsdk: DeclareCapabilities: request frame %q is named twice", f)
		}
	}
	return nil
}

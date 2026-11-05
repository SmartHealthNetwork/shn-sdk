package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
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
// holder, together. RequestFrames is sent only when present; a registrar that
// predates the field ignores it.
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
	// running system accepts (Holder.RequestFrames), as its full set. Absent
	// (nil) is not sent and says nothing about them; an empty list is sent as
	// [] and declares that the system accepts none. A registrar that predates
	// the field ignores it. Each token matches ^[a-z0-9]{1,16}$, at most 8,
	// each named once.
	RequestFrames []string `json:"requestFrames,omitzero"`
}

// maxDeclaredRequestFrames is the most request-frame tokens a holder may
// declare, the registry's limit.
const maxDeclaredRequestFrames = 8

// DeclareCapabilities declares to the registrar at registrarURL what this
// holder's system takes as a recipient: the legs it answers (a gateway's
// compiled catalog, a Responder's Legs), when its system has one, the largest
// single inline attachment it accepts, and, when given, the request frames it
// accepts (RequestFrames). It replaces Legs and AttachmentMaxBytes as the
// registrar holds them for the holder, and sends RequestFrames only when
// given; a registrar that predates that field ignores it. The registrar keeps
// a declaration only when it is newer than the one it holds, so a system
// declares its capabilities each time it starts. Legs must name each leg once
// by its flat name; an empty, non-nil list declares that the holder takes
// none. AttachmentMaxBytes, when set, is 1 to 2^53-1. The request is PUT
// {registrarURL}/register/{id}/capabilities, carrying a holder-self assertion
// (audience "registrar") bound to the exact body sent. It returns the
// declaration the registrar stored; a registrar that predates RequestFrames
// answers without it, so the returned RequestFrames is then nil whatever was
// sent. A nil c uses http.DefaultClient.
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
	body, err := json.Marshal(CapabilityDeclaration{Legs: sorted, AttachmentMaxBytes: d.AttachmentMaxBytes, RequestFrames: slices.Clone(d.RequestFrames)})
	if err != nil {
		return CapabilityDeclaration{}, err
	}
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
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return CapabilityDeclaration{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: registrar answered %d: %.300s", resp.StatusCode, answer)
	}
	var stored CapabilityDeclaration
	if err := json.Unmarshal(answer, &stored); err != nil || stored.Legs == nil {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: the registrar's answer names no stored list: %.300s", answer)
	}
	if n := stored.AttachmentMaxBytes; n != nil && (*n < 1 || *n > declaredAttachmentMaxBytes) {
		return CapabilityDeclaration{}, fmt.Errorf("shnsdk: DeclareCapabilities: the registrar's answer names an attachment limit of %d, not 1 to 2^53-1", *n)
	}
	return stored, nil
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

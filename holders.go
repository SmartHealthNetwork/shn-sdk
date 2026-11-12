package shnsdk

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Holder is one row of the public registrar /holders feed — wire-identical to
// the substrate's registration DTO (id/role/encPub/signPub/baseURL, std base64).
// For the decoded, in-memory runtime peer-cache entry (keys as usable values, no JSON tags) see RegistryEntry.
type Holder struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	EncPub  string `json:"encPub"`
	SignPub string `json:"signPub"`
	BaseURL string `json:"baseURL"`
	// PayerIDs are the holder's operator-attested payer-identity claims (role=payer only;
	// omitted otherwise). FeedPayerRouter indexes (system,value) → holder id for coverage-
	// derived routing (FR-G41). Globally unique across holders (AI-G12).
	PayerIDs []PayerIdentifier `json:"payerIds,omitempty"`
	// ProviderIDs are the organization identifiers the holder, a provider, is
	// operator-attested for. Absent means it is attested for none, and a reader
	// that knows only PayerIDs reads the row as before.
	ProviderIDs []ProviderIdentifier `json:"providerIds,omitempty"`
	// MessageFrames are the sealed message-frame versions this holder's build
	// negotiates (library-self-declared at registration; "v1" = the binary
	// status+headers+body response frame). Absent ⇒ legacy bare payloads.
	MessageFrames []string `json:"messageFrames,omitempty"`
	// ContractVersions are the exchange-contract version tokens this holder's
	// build speaks natively ("pa.pas@2.0"-style; library-self-declared at
	// registration). Surfaced for display/discovery first;
	// version-aware routing consumes it. Absent ⇒ pre-contract build.
	ContractVersions []string `json:"contractVersions,omitempty"`
	// RequestFrames are the sealed REQUEST-frame versions this holder accepts
	// ("v1"; library-self-declared at registration — the request-frame contract).
	// An originator frames a contract-mapped request ONLY toward a holder that
	// declares it. Absent ⇒ send bare (byte-identical to the pre-request-frame wire).
	RequestFrames []string `json:"requestFrames,omitempty"`
	// Legs are the legs this holder takes as a recipient: the flat transaction
	// types of the request legs it answers, declared by the holder itself
	// (Identity.DeclareCapabilities). nil (the key absent) means the holder declares no
	// list: it predates every leg TransactionTypeSince marks, and is routed
	// every other leg as before. An empty list (present, []) declares that it
	// takes none. The two are distinct, so the field is omitted only when nil.
	Legs []string `json:"legs,omitzero"`
	// AttachmentMaxBytes is the largest single inline attachment (file), in
	// bytes after base64 decoding, the holder's system accepts in one
	// documentation submission (cdex-attachment-submit): each FHIR Attachment
	// with inline data is measured on its own. It is declared by the holder itself
	// (Identity.DeclareCapabilities); nil (absent) means it declared none, and
	// a sender then applies only its own ceiling. It is the holder's statement,
	// served as declared: a sender applies the smaller of it and its ceiling.
	AttachmentMaxBytes *int64 `json:"attachmentMaxBytes,omitempty"`
	// CDSServicesDigest is the row's cdsServicesDigest: the digest
	// (DigestCDSServices) naming the CDS Hooks service listing the holder's
	// own system declared (CapabilityDeclaration.CDSServices). A reader fetches
	// the listing it names (FetchCDSServices) when the digest changes. Empty
	// (absent) means the row names no listing.
	CDSServicesDigest string `json:"cdsServicesDigest,omitempty"`
}

// ErrNoCDSServices is FetchCDSServices's answer when the registrar answers
// 404: it serves no CDS service listing for the holder. Read the holder's row
// again for the digest it names now.
var ErrNoCDSServices = errors.New("shnsdk: the registrar holds no CDS service listing for the holder")

// ErrCDSServicesIntegrity is FetchCDSServices's answer when what the
// registrar served is not the listing the digest names: other bytes, an ETag
// (strong or weak) naming another digest, more than any listing can be, or
// not a listing in its compact form. Nothing it served is returned. It can
// also mean the holder declared again after the caller read its row: read the
// row again and fetch the digest it names now.
var ErrCDSServicesIntegrity = errors.New("shnsdk: the CDS service listing served is not the one its digest names")

// FetchCDSServices fetches holderID's CDS service listing from the registrar
// at registrarURL: GET {registrarURL}/holders/{id}/cds-services. It returns
// the listing's bytes, unchanged and in compact form, only once they are
// verified against digest, the digest the caller read from the holder's row
// (Holder.CDSServicesDigest); ErrCDSServicesIntegrity otherwise, and
// ErrNoCDSServices when the registrar answers 404. A nil c uses
// http.DefaultClient.
func FetchCDSServices(ctx context.Context, c *http.Client, registrarURL, holderID, digest string) (json.RawMessage, error) {
	if !validCDSServicesDigest(digest) {
		return nil, fmt.Errorf("shnsdk: FetchCDSServices: %.80q is not a CDS service listing's digest", digest)
	}
	if holderID == "" {
		return nil, errors.New("shnsdk: FetchCDSServices: no holder id")
	}
	if c == nil {
		c = http.DefaultClient
	}
	u := strings.TrimRight(registrarURL, "/") + "/holders/" + url.PathEscape(holderID) + "/cds-services"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoCDSServices
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("shnsdk: FetchCDSServices: registrar answered %d", resp.StatusCode)
	}
	// The ETag, when it names a digest, names the listing's: one naming
	// another, strong or weak (W/), is another listing. An ETag naming no
	// digest (an intermediary's, rewritten) is left to the content checks.
	if named := strings.Trim(strings.TrimPrefix(resp.Header.Get("ETag"), "W/"), `"`); validCDSServicesDigest(named) && named != digest {
		return nil, fmt.Errorf("%w: its ETag names another digest", ErrCDSServicesIntegrity)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDeclaredCDSServicesBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDeclaredCDSServicesBytes {
		return nil, fmt.Errorf("%w: it is larger than any listing", ErrCDSServicesIntegrity)
	}
	if DigestCDSServices(body) != digest {
		return nil, fmt.Errorf("%w: its digest differs", ErrCDSServicesIntegrity)
	}
	listing, err := compactDeclaredCDSServices(body)
	if err != nil || !bytes.Equal(listing, body) {
		return nil, fmt.Errorf("%w: it is not a listing in its compact form", ErrCDSServicesIntegrity)
	}
	return listing, nil
}

// EncKey decodes the holder's X25519 public key.
func (h Holder) EncKey() (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(h.EncPub)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("holders: malformed encPub")
	}
	var k [32]byte
	copy(k[:], raw)
	return &k, nil
}

// FetchHolders fetches the COMPLETE public holder directory from the registrar
// /holders feed (founding manifest holders ∪ dynamic registrations).
func FetchHolders(ctx context.Context, c *http.Client, registrarURL string) ([]Holder, error) {
	url := strings.TrimRight(registrarURL, "/") + "/holders"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("holders: status %d", resp.StatusCode)
	}
	var hs []Holder
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxResponseBytes)).Decode(&hs); err != nil {
		return nil, err
	}
	return hs, nil
}

// NewFeedEncResolver returns a resolve-by-holder-id func backed by the live
// /holders feed. It re-fetches on every call — deliberately simple for B1 (a
// demo responder answers at human cadence); add caching when volume warrants
// (additive).
func NewFeedEncResolver(c *http.Client, registrarURL string) func(holderID string) (*[32]byte, bool) {
	return func(holderID string) (*[32]byte, bool) {
		hs, err := FetchHolders(context.Background(), c, registrarURL)
		if err != nil {
			return nil, false
		}
		for _, h := range hs {
			if h.ID == holderID {
				k, kerr := h.EncKey()
				if kerr != nil {
					return nil, false
				}
				return k, true
			}
		}
		return nil, false
	}
}

// NewFeedFrameResolver returns a ResolveFrames func backed by the live /holders
// feed. Re-fetches per call — same deliberate simplicity as NewFeedEncResolver.
// NOTE: a responder that wires BOTH this and NewFeedEncResolver makes two
// independent /holders fetches per answered leg (enc-key resolve + frame resolve).
// That is the accepted cost of the per-call-fetch simplicity; a caller that wants
// a single fetch can share one cached holder snapshot behind both resolvers when
// volume warrants it (additive — no wire or signature change).
func NewFeedFrameResolver(c *http.Client, registrarURL string) func(holderID string) []string {
	return func(holderID string) []string {
		hs, err := FetchHolders(context.Background(), c, registrarURL)
		if err != nil {
			return nil
		}
		for _, h := range hs {
			if h.ID == holderID {
				return h.MessageFrames
			}
		}
		return nil
	}
}

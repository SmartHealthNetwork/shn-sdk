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
	"strings"
	"time"

	"github.com/SmartHealthNetwork/shn-sdk/internal/signedsend"
)

// leaseRenewalTTL bounds a renewal's assertion: it is sent at once.
const leaseRenewalTTL = 2 * time.Minute

// maxLeaseAnswerBytes bounds the registrar's answer to a renewal, which is
// one member.
const maxLeaseAnswerBytes = 4 << 10

// registrarRefusalType prefixes the problem type of every refusal the
// registrar itself answers; its last segment is the refusal's key.
const registrarRefusalType = "tag:smarthealthnetwork.org,2026:registrar/refusal/"

// RenewalOutcome is what a renewal of a holder's registration established
// (Identity.RenewLease).
type RenewalOutcome string

const (
	// RenewalUnknown is a renewal the registrar did not confirm: no answer, an
	// answer this SDK cannot read, a redirect, a 404 or 405, a 5xx, a 403 or
	// 409 that is not the registrar's own refusal, or any other status it does
	// not name. A registrar that predates the renewal answers 404 or 405, so
	// neither says the holder is gone. The registration may or may not be
	// renewed: renew again later.
	RenewalUnknown RenewalOutcome = "unknown"
	// RenewalRenewed is a renewal the registrar confirmed (200).
	RenewalRenewed RenewalOutcome = "renewed"
	// RenewalRefused is a renewal the registrar itself refused, as its
	// problem type names: 403 not-self, the assertion is not the holder's own
	// by its current key (or its clock is too far off), or 409
	// founding-holder, a founding holder, whose lifecycle the network's
	// manifest owns. Renewing again with the same keys and clock is refused
	// the same way.
	RenewalRefused RenewalOutcome = "refused"
)

// LeaseRenewal is a renewal's result.
type LeaseRenewal struct {
	// Outcome is what the renewal established. Only RenewalRenewed says the
	// registrar renewed the registration.
	Outcome RenewalOutcome
	// Status is the registrar's HTTP status, or 0 when none answered.
	Status int
	// LeaseExpiresAt is when the holder's lease lapses, for a renewed
	// registration: the time it stops being listed unless the holder renews
	// before it. nil says no lease runs, so nothing lapses. It is nil for any
	// outcome but RenewalRenewed.
	LeaseExpiresAt *time.Time
}

// RenewLease renews the holder's registration: proof, by its current key,
// that it is alive. The request is PUT {registrarURL}/register/{id}/lease,
// carrying a holder-self assertion (audience "registrar", issued at the
// identity's Clock) and no body. A nil c uses http.DefaultClient. It follows
// no redirect, whatever c does: a redirect would send the assertion to
// another host, and its answer is unknown.
//
// The error is nil exactly when the outcome is RenewalRenewed. Every other
// outcome comes with an error that says why, and with the LeaseRenewal that
// classifies it, so a caller can tell a refusal, which renewing again does not
// change, from a renewal that is unknown, which it may.
func (id Identity) RenewLease(ctx context.Context, c *http.Client, registrarURL string) (LeaseRenewal, error) {
	unknown := LeaseRenewal{Outcome: RenewalUnknown}
	hdr, err := id.Assertion("registrar", id.now(), leaseRenewalTTL)
	if err != nil {
		return unknown, fmt.Errorf("shnsdk: RenewLease: %w", err)
	}
	u := strings.TrimRight(registrarURL, "/") + "/register/" + url.PathEscape(id.HolderID) + "/lease"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return unknown, fmt.Errorf("shnsdk: RenewLease: %w", err)
	}
	resp, err := signedsend.Do(c, req, map[string]string{signedsend.HolderAssertion: hdr})
	if err != nil {
		return unknown, fmt.Errorf("shnsdk: RenewLease: the renewal is unknown: %w", err)
	}
	defer resp.Body.Close()
	unknown.Status = resp.StatusCode
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxLeaseAnswerBytes+1))
	if err != nil {
		return unknown, fmt.Errorf("shnsdk: RenewLease: the renewal is unknown: reading the registrar's answer: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusForbidden && registrarRefusal(answer, "not-self"),
		resp.StatusCode == http.StatusConflict && registrarRefusal(answer, "founding-holder"):
		return LeaseRenewal{Outcome: RenewalRefused, Status: resp.StatusCode},
			fmt.Errorf("shnsdk: RenewLease: the registrar refused the renewal: %d: %.300s", resp.StatusCode, answer)
	default:
		return unknown, fmt.Errorf("shnsdk: RenewLease: the renewal is unknown: %d: %.300s", resp.StatusCode, answer)
	}
	expires, err := parseLeaseAnswer(answer)
	if err != nil {
		return unknown, fmt.Errorf("shnsdk: RenewLease: the renewal is unknown: %w", err)
	}
	return LeaseRenewal{Outcome: RenewalRenewed, Status: resp.StatusCode, LeaseExpiresAt: expires}, nil
}

// registrarRefusal reports whether answer is the registrar's own refusal with
// key: a problem whose "type" member, by that exact name, is the registrar's
// refusal type for key. A 403 or 409 from anything in front of the registrar
// names no such type. The member is read by its exact name, never by
// encoding/json's case-folded match, so "Type" or "TYPE" is not it.
func registrarRefusal(answer []byte, key string) bool {
	if len(answer) > maxLeaseAnswerBytes || noDuplicateMembers(answer) != nil {
		return false
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(answer, &members) != nil {
		return false
	}
	var typ string
	return json.Unmarshal(members["type"], &typ) == nil && typ == registrarRefusalType+key
}

// parseLeaseAnswer reads a renewal's 200 answer: an object whose
// leaseExpiresAt is null or an RFC 3339 time, returned in UTC (the registrar
// sends UTC; another offset names the same instant). A member left out, named
// twice, or of another type is not read as either: the answer is unreadable.
func parseLeaseAnswer(answer []byte) (*time.Time, error) {
	if len(answer) > maxLeaseAnswerBytes {
		return nil, fmt.Errorf("the registrar's answer is over %d bytes", maxLeaseAnswerBytes)
	}
	if err := noDuplicateMembers(answer); err != nil {
		return nil, fmt.Errorf("the registrar's answer cannot be read unambiguously: %w", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(answer, &members); err != nil {
		return nil, fmt.Errorf("the registrar's answer is not a JSON object: %.300s", answer)
	}
	raw, ok := members["leaseExpiresAt"]
	if !ok {
		return nil, errors.New("the registrar's answer names no leaseExpiresAt")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("the registrar's answer names leaseExpiresAt %.80s, not a time or null", raw)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("the registrar's answer names leaseExpiresAt %.80q, not an RFC 3339 time", s)
	}
	t = t.UTC()
	return &t, nil
}

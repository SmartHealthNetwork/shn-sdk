package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Rows for the prior Claim an amendment carries from PAS 2.1: it is
// the requester's own submitted Claim, carried as the submission sent it, byte
// for byte — never rebuilt, restated or rewritten. A record that cannot be
// carried that way refuses the amendment, on the fidelity of SHN's own edit.

// entryResources returns each entry's resource exactly as the bundle's bytes
// hold it, by fullUrl, and the fullUrls in entry order.
func entryResources(t *testing.T, bundleJSON []byte) (map[string]json.RawMessage, []string) {
	t.Helper()
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	byURL := map[string]json.RawMessage{}
	var order []string
	for _, e := range b.Entry {
		byURL[e.FullURL] = e.Resource
		order = append(order, e.FullURL)
	}
	return byURL, order
}

// submittedClaim is the first Claim entry of a submitted bundle: its fullUrl
// and its resource exactly as the bundle's bytes hold it.
func submittedClaim(t *testing.T, bundleJSON []byte) (string, json.RawMessage) {
	t.Helper()
	byURL, order := entryResources(t, bundleJSON)
	for _, u := range order {
		var head struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(byURL[u], &head) == nil && head.ResourceType == "Claim" {
			return u, byURL[u]
		}
	}
	t.Fatal("the submitted bundle carries no Claim")
	return "", nil
}

// carriedSubmission builds the submission an update continues at line, on the
// same lane, and records it as a participant does (PASPriorClaimOf).
func carriedSubmission(t *testing.T, line string, absolute bool) ([]byte, PASPriorClaim) {
	t.Helper()
	sub := conformantSubmitInputs(t)
	sub.AbsoluteRefs, sub.PayerOrgEntry = absolute, absolute
	out, err := BuildConformantClaimBundleAtLineExact(line, sub)
	if err != nil {
		t.Fatalf("build the submission at %s: %v", line, err)
	}
	p, err := PASPriorClaimOf(out)
	if err != nil {
		t.Fatal(err)
	}
	return out, p
}

func carriedUpdateInputs(t *testing.T, line string, absolute bool) ([]byte, ConformantClaimUpdateInputs) {
	t.Helper()
	sub, prior := carriedSubmission(t, line, absolute)
	in := conformantUpdateInputsFromGolden(t)
	in.AbsoluteRefs, in.PayerOrgEntry = absolute, absolute
	in.PriorClaim = prior
	return sub, in
}

// TestPASUpdate_PriorClaimIsTheSubmittedClaimAsSent: from PAS 2.1 the prior
// Claim entry's resource bytes are the submitted Claim's resource bytes as the
// submission's bundle holds them, and the update Claim's related[0].claim
// resolves to it. At PAS 2.0 none is carried. Both lanes.
func TestPASUpdate_PriorClaimIsTheSubmittedClaimAsSent(t *testing.T) {
	for _, line := range pasLines {
		for _, absolute := range []bool{false, true} {
			sub, in := carriedUpdateInputs(t, line, absolute)
			subURL, subClaim := submittedClaim(t, sub)
			out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
			if err != nil {
				t.Fatalf("%s absolute=%v: %v", line, absolute, err)
			}
			claims := bundleClaims(t, out)
			if def, _ := PASLineDef(line); !def.UpdateCarriesPriorClaim {
				if len(claims) != 1 {
					t.Errorf("%s: %d Claims, want the update alone (PAS 2.0.1's request Bundle allows one)", line, len(claims))
				}
				continue
			}
			if len(claims) != 2 {
				t.Fatalf("%s absolute=%v: %d Claims, want the update and the submitted Claim", line, absolute, len(claims))
			}
			byURL, _ := entryResources(t, out)
			got, ok := byURL[subURL]
			if !ok {
				t.Fatalf("%s absolute=%v: no entry at the submitted Claim's fullUrl %s", line, absolute, subURL)
			}
			if !bytes.Equal(got, subClaim) {
				t.Errorf("%s absolute=%v: the prior Claim is not the submitted Claim as sent\n got: %s\nwant: %s", line, absolute, got, subClaim)
			}
			var related []struct {
				Claim struct {
					Reference string `json:"reference"`
				} `json:"claim"`
			}
			_ = json.Unmarshal(claims[0]["related"], &related)
			if len(related) == 0 || (related[0].Claim.Reference != subURL && pasBundleBaseURL+"/"+related[0].Claim.Reference != subURL) {
				t.Errorf("%s absolute=%v: related = %+v, want it to resolve to %s", line, absolute, related, subURL)
			}
		}
	}
}

// TestPASUpdate_PriorClaimCarriedVerbatim: a submitted Claim whose members
// the SDK's own builders would never write (narrative, an extension, members
// in the requester's own order) is carried exactly so: nothing is re-encoded,
// reordered or dropped.
func TestPASUpdate_PriorClaimCarriedVerbatim(t *testing.T) {
	for _, line := range []string{"2.1", "2.2"} {
		_, in := carriedUpdateInputs(t, line, true)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(in.PriorClaim.Claim, &m); err != nil {
			t.Fatal(err)
		}
		// The requester's own order and spelling: resourceType last, a narrative
		// and an extension the builders never write, a number as the requester
		// spelled it.
		var b bytes.Buffer
		b.WriteString(`{"text":{"status":"generated","div":"<div xmlns=\"http://www.w3.org/1999/xhtml\">as the requester wrote it</div>"},"extension":[{"url":"http://example.org/requester-ext","valueDecimal":1.50}]`)
		for _, k := range []string{"id", "identifier", "status", "type", "use", "patient", "created", "insurer", "provider", "priority", "insurance", "item"} {
			if v, ok := m[k]; ok {
				b.WriteString(`,"` + k + `":`)
				b.Write(v)
			}
		}
		b.WriteString(`,"resourceType":"Claim"}`)
		in.PriorClaim.Claim = json.RawMessage(b.Bytes())
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		// The bundle's own encoding writes < and > in strings as \u003c and
		// \u003e; nothing else about the bytes changes.
		var want bytes.Buffer
		json.HTMLEscape(&want, b.Bytes())
		if !bytes.Contains(out, want.Bytes()) {
			t.Errorf("%s: the bundle does not carry the submitted Claim's bytes as sent:\n%s", line, out)
		}
	}
}

// TestPASUpdate_PriorClaimNotCarriableRefuses: a record that cannot be carried
// as sent refuses the amendment — nothing is built — and the refusal names its
// primitive. A record written before this release carries no Claim, so its
// amendment is refused and the request is resubmitted.
func TestPASUpdate_PriorClaimNotCarriableRefuses(t *testing.T) {
	editClaim := func(edit func(map[string]any)) func(*testing.T, *ConformantClaimUpdateInputs) {
		return func(t *testing.T, in *ConformantClaimUpdateInputs) {
			var m map[string]any
			if err := json.Unmarshal(in.PriorClaim.Claim, &m); err != nil {
				t.Fatal(err)
			}
			edit(m)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			in.PriorClaim.Claim = raw
		}
	}
	type row struct {
		mutate func(*testing.T, *ConformantClaimUpdateInputs)
		want   string
		// carried rows bite only where the line carries the prior Claim.
		carried bool
		// relative rows build on the lane whose references stay relative.
		relative bool
	}
	rows := map[string]row{
		"a record written before this release (no Claim)": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.PriorClaim.Claim = nil
		}, "resubmit", false, false},
		"Claim null": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.PriorClaim.Claim = json.RawMessage("null")
		}, "resubmit", false, false},
		"Claim not JSON": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.PriorClaim.Claim = json.RawMessage(`{"resourceType":"Claim",`)
		}, "not JSON", false, false},
		"not a Claim": {editClaim(func(m map[string]any) { m["resourceType"] = "Patient" }), "not a Claim", false, false},
		"no id":       {editClaim(func(m map[string]any) { delete(m, "id") }), "no id the amendment's related claim can reference", false, false},
		"no items":    {editClaim(func(m map[string]any) { delete(m, "item") }), "items", false, false},
		"another submission's correlation": {editClaim(func(m map[string]any) {
			m["identifier"] = []any{map[string]any{"system": "urn:shn:correlation", "value": "another-submission"}}
		}), "urn:shn:correlation", false, false},
		"no identifier":             {editClaim(func(m map[string]any) { delete(m, "identifier") }), "urn:shn:correlation", false, false},
		"the update Claim's own id": {editClaim(func(m map[string]any) { m["id"] = conformantPASClaimUpdateID }), "the amendment's own Claim id", true, false},
		"a reference outside the bundle": {editClaim(func(m map[string]any) {
			m["patient"] = map[string]any{"reference": pasBundleBaseURL + "/Patient/someone-else"}
		}), "Patient/someone-else", true, false},
		"a relative reference outside the bundle": {editClaim(func(m map[string]any) {
			m["patient"] = map[string]any{"reference": "Patient/someone-else"}
		}), "Patient/someone-else", true, true},
		"a contained reference the Claim does not contain": {editClaim(func(m map[string]any) {
			m["patient"] = map[string]any{"reference": "#not-contained"}
		}), "#not-contained", true, true},
		"a relative reference a build step would rewrite": {editClaim(func(m map[string]any) {
			// The reference-payer lane absolutizes Claim.patient (absolutizeBundleRefs).
			m["patient"] = map[string]any{"reference": "Patient/MBR-COVERED"}
		}), "as sent", true, false},
	}
	for _, line := range pasLines {
		def, _ := PASLineDef(line)
		for name, r := range rows {
			if r.carried && !def.UpdateCarriesPriorClaim {
				continue
			}
			_, in := carriedUpdateInputs(t, line, !r.relative)
			r.mutate(t, &in)
			out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
			if err == nil || out != nil {
				t.Errorf("%s %s: built %d bytes, want the amendment refused", line, name, len(out))
				continue
			}
			if !errors.Is(err, ErrPASPriorClaimNotCarried) {
				t.Errorf("%s %s: %v, want ErrPASPriorClaimNotCarried", line, name, err)
			}
			if !strings.Contains(err.Error(), "fidelity of SHN's own edit") || !strings.Contains(err.Error(), r.want) {
				t.Errorf("%s %s: %q, want it to name the primitive and %q", line, name, err, r.want)
			}
		}
	}
}

// TestPASUpdate_PriorClaimItemsAreTheCarriedClaims: the infoChanged marker is
// derived from the items of the Claim carried, not from the Deprecated Items
// field beside it.
func TestPASUpdate_PriorClaimItemsAreTheCarriedClaims(t *testing.T) {
	for _, line := range pasLines {
		_, in := carriedUpdateInputs(t, line, true)
		in.PriorClaim.Items = []json.RawMessage{json.RawMessage(`{"sequence":1}`)}
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if bytes.Contains(bundleClaims(t, out)[0]["item"], []byte(pasInfoChangedExtensionURL)) {
			t.Errorf("%s: the item is marked against the Deprecated Items field, want it read against the carried Claim (unchanged)", line)
		}
	}
}

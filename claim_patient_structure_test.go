package shnsdk

// The Responder reads a PAS request's Claim.patient by structure, as a payer's
// Smart Gateway reads it (pasPatientRefMember): a "#<id>" naming a Patient the
// Claim contains, or a urn:uuid naming a Patient entry, names that Patient's
// member, never the reference's text. A referenced Patient that holds no member
// key names no one, with a text that says what the request must carry.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// structureClaim rewrites the first Claim's patient to "#p1", a Patient the
// Claim contains carrying the member identifiers ids.
func structureClaim(t *testing.T, body []byte, ids ...string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	claim := firstClaim(doc)
	if claim == nil {
		t.Fatal("fixture: no Claim")
	}
	p := map[string]any{"resourceType": "Patient", "id": "p1", "birthDate": "1970-01-01", "name": []any{map[string]any{"family": "Structure"}}}
	var identifiers []any
	for _, v := range ids {
		identifiers = append(identifiers, map[string]any{"system": MemberSystem, "value": v})
	}
	if identifiers != nil {
		p["identifier"] = identifiers
	}
	contained, _ := claim["contained"].([]any)
	claim["contained"] = append(contained, p)
	claim["patient"] = map[string]any{"reference": "#p1"}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// structureURN gives member's Patient entry the fullUrl urn:uuid:structure and
// names it so from every reference that named it; bare removes its id and
// identifiers.
func structureURN(t *testing.T, body []byte, member string, bare bool) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range doc["entry"].([]any) {
		em := e.(map[string]any)
		if r, _ := em["resource"].(map[string]any); r["resourceType"] == "Patient" && r["id"] == member {
			em["fullUrl"] = "urn:uuid:structure"
			if bare {
				delete(r, "id")
				delete(r, "identifier")
			}
		}
	}
	renamed := 0
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["reference"].(string); ok {
				if id, _, ok := readPatientReference(ref); ok && id == member {
					v["reference"] = "urn:uuid:structure"
					renamed++
				}
			}
			for _, c := range v {
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(doc)
	if slot, _ := firstClaim(doc)["patient"].(map[string]any); renamed == 0 || slot["reference"] != "urn:uuid:structure" {
		t.Fatal("fixture: Claim.patient is not the urn")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestResponder_ClaimPatientByStructure(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	const member = "MBR-COVERED"
	for _, row := range []struct {
		name   string
		body   []byte
		status int
		says   string
	}{
		// A urn:uuid naming the member's Patient entry is answered: the
		// Adjudicator decides it.
		{"a urn:uuid Patient entry", structureURN(t, demoConformantClaim(t), member, false), 0, ""},
		// A "#<id>" naming a contained Patient is bound to its member, but this
		// Responder's answer cannot restate a contained Claim patient, so it is
		// refused before the Adjudicator is asked.
		{"a contained Patient", structureClaim(t, demoConformantClaim(t), member), http.StatusBadRequest, "incomplete PAS request graph"},
		{"a contained Patient with no member identifier", structureClaim(t, demoConformantClaim(t)), http.StatusBadRequest, msgPatientCarriesNoMember},
		{"a contained Patient with two member identifiers", structureClaim(t, demoConformantClaim(t), member, "MBR-OTHER"), http.StatusBadRequest, msgPatientCarriesNoMember},
		{"a urn:uuid Patient entry with no id and no member identifier", structureURN(t, demoConformantClaim(t), member, true), http.StatusBadRequest, msgPatientCarriesNoMember},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := &Responder{cfg: ResponderConfig{Adjudicator: twinFenceAdjudicator{}}, ledger: newPendedLedger()}
			res := r.handlePASSubmit(row.body, Token{Subject: "pci:structure"}, "corr-structure", now, "")
			if res.appStatus != row.status || res.errMsg != row.says {
				t.Fatalf("answer %d %q, want %d %q", res.appStatus, res.errMsg, row.status, row.says)
			}
			if row.status == 0 && len(res.payload) == 0 {
				t.Fatal("answered with no body")
			}
		})
	}
	// The subject consistency check reads the member, not the reference's text.
	t.Run("the binding reads the contained Patient's member", func(t *testing.T) {
		cs, ok := parseConformantClaimSubmit(structureClaim(t, demoConformantClaim(t), member))
		if !ok {
			t.Fatal("parse")
		}
		if status, msg, _ := bindConformantClaimSubject(cs); status != 0 {
			t.Fatalf("bind refused %d %s", status, msg)
		}
		if got := conformantBundleMember(cs); got != member {
			t.Fatalf("bound member %q, want %q", got, member)
		}
	})
	// The inquiry reads Claim.patient by the same reader.
	t.Run("an inquiry naming a contained Patient with no member identifier", func(t *testing.T) {
		body := structureClaim(t, inquiryRequest(t, nil))
		res := (&Responder{cfg: ResponderConfig{Adjudicator: twinFenceAdjudicator{}}, ledger: newPendedLedger()}).handlePASInquire(body, "corr-structure", now)
		if res.appStatus != http.StatusBadRequest || !strings.Contains(res.errMsg, "holds no member identifier") {
			t.Fatalf("answer %d %q, want 400 %q", res.appStatus, res.errMsg, msgPatientCarriesNoMember)
		}
	})
}

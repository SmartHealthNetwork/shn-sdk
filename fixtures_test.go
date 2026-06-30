package shnsdk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// expectedMember pins each provider-data persona to the canonical member id (Patient.id) the
// gateway's sceneMember resolves to (provider-data values). A collision or a wrong member would
// break OpenOrder routing (OpenOrder is keyed on member). UC-02/UC-03 are descoped (D-PD-2).
var expectedMember = map[string]string{
	"uc04":       "MBR-PD-UC04",
	"homeoxygen": "MBR-OX",
	"uc08":       "MBR-PD-UC08",
	"uc06":       "MBR-PD-UC06",
	"uc01":       "MBR-PD-UC01",
	"uc01-nc":    "MBR-PD-UC01-NC",
	"uc07":       "MBR-PD-UC07",
	"uc05":       "MBR-PD-UC05",
	"uc05-nc":    "MBR-PD-UC05-NC",
}

// fixtureBundle is the minimal transaction-Bundle shape the invariants assert against.
type fixtureBundle struct {
	ResourceType string `json:"resourceType"`
	Type         string `json:"type"`
	Entry        []struct {
		FullURL  string          `json:"fullUrl"`
		Resource json.RawMessage `json:"resource"`
		Request  struct {
			Method string `json:"method"`
			URL    string `json:"url"`
		} `json:"request"`
	} `json:"entry"`
}

// fixtureResource is the discriminator subset read from each entry's resource.
type fixtureResource struct {
	ResourceType string `json:"resourceType"`
	ID           string `json:"id"`
	Status       string `json:"status"`
	Identifier   []struct {
		System string `json:"system"`
		Value  string `json:"value"`
	} `json:"identifier"`
}

// TestProviderDataBundle_NoContractedNPI asserts that the contracted-NPI set
// {1112223334, 4445556667, 7778889990} never appears in any ProviderDataBundle.
// These NPIs represent contracted suppliers (carrier honesty — the VERDICT is
// conditional-coverage-determined, not supplier-NPI-determined, and the
// contracted-NPI set is never seeded, R-8).
func TestProviderDataBundle_NoContractedNPI(t *testing.T) {
	contractedNPIs := []string{"1112223334", "4445556667", "7778889990"}
	for _, persona := range ProviderDataPersonas() {
		raw, err := ProviderDataBundle(persona)
		if err != nil {
			t.Fatalf("ProviderDataBundle(%q): %v", persona, err)
		}
		for _, npi := range contractedNPIs {
			if bytes.Contains(raw, []byte(npi)) {
				t.Errorf("ProviderDataBundle(%q) contains contracted NPI %s (contracted-NPI set must never be seeded, R-8)", persona, npi)
			}
		}
	}
}

func TestProviderDataPersonas_NoDescoped(t *testing.T) {
	// The shipped set is exactly uc04 + homeoxygen + uc08 + uc06 + uc01 + uc01-nc + uc07 + uc05 + uc05-nc;
	// uc02/uc03 are descoped (D-PD-2). uc01/uc01-nc are coverage-completion eligibility
	// personas (SHN eligibility gap-fill; active vs terminated Coverage).
	// uc07 is the patient-authored twin of uc06 (same G0151/M62.81; distinct member).
	// uc05/uc05-nc are the federated-query personas (G0151 order; operative DR is facility-seeded).
	got := ProviderDataPersonas()
	want := map[string]bool{
		"uc04": true, "homeoxygen": true, "uc08": true, "uc06": true,
		"uc01": true, "uc01-nc": true, "uc07": true,
		"uc05": true, "uc05-nc": true,
	}
	if len(got) != len(want) {
		t.Fatalf("ProviderDataPersonas() = %v, want exactly %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("ProviderDataPersonas() returned unexpected persona %q (uc02/uc03 are descoped, D-PD-2)", p)
		}
	}
}

func TestProviderDataBundle(t *testing.T) {
	patientIDs := map[string]string{} // persona → Patient.id, to assert distinctness across personas

	for _, persona := range ProviderDataPersonas() {
		persona := persona
		t.Run(persona, func(t *testing.T) {
			raw, err := ProviderDataBundle(persona)
			if err != nil {
				t.Fatalf("ProviderDataBundle(%q): %v", persona, err)
			}
			if len(raw) == 0 {
				t.Fatalf("ProviderDataBundle(%q) returned empty bytes", persona)
			}

			var b fixtureBundle
			if err := json.Unmarshal(raw, &b); err != nil {
				t.Fatalf("ProviderDataBundle(%q) does not unmarshal: %v", persona, err)
			}
			if b.ResourceType != "Bundle" {
				t.Errorf("%q: resourceType = %q, want Bundle", persona, b.ResourceType)
			}
			if b.Type != "transaction" {
				t.Errorf("%q: type = %q, want transaction (partners load it as a single transaction)", persona, b.Type)
			}
			if len(b.Entry) == 0 {
				t.Fatalf("%q: bundle has no entries", persona)
			}

			var activeOrders int
			var patientID string
			var sawMemberIdentifier bool
			for i, e := range b.Entry {
				if e.FullURL == "" {
					t.Errorf("%q entry[%d]: missing fullUrl (transaction entries need a resolvable fullUrl)", persona, i)
				}
				if e.Request.Method != "PUT" {
					t.Errorf("%q entry[%d]: request.method = %q, want PUT", persona, i, e.Request.Method)
				}
				if e.Request.URL == "" {
					t.Errorf("%q entry[%d]: missing request.url", persona, i)
				}

				var r fixtureResource
				if err := json.Unmarshal(e.Resource, &r); err != nil {
					t.Fatalf("%q entry[%d]: resource does not unmarshal: %v", persona, i, err)
				}

				// One-active-order invariant: exactly one status=active order (DeviceRequest or
				// ServiceRequest). OpenOrder returns Entry[0] of DeviceRequest-then-ServiceRequest;
				// a second active order would make the routed order non-deterministic.
				// Eligibility-only personas (uc01/uc01-nc) carry no order by design — they
				// exercise CoverageInforce only, not the order-dispatch path (coverage-completion,
				// not new fidelity).
				if r.ResourceType == "DeviceRequest" || r.ResourceType == "ServiceRequest" {
					if r.Status == "active" {
						activeOrders++
					}
				}

				if r.ResourceType == "Patient" {
					patientID = r.ID
					// The member identifier (urn:shn:member|<member>) is what fhirsor.resolvePatient
					// searches on — without it OpenOrder cannot find the order.
					for _, id := range r.Identifier {
						if id.System == MemberSystem && id.Value == expectedMember[persona] {
							sawMemberIdentifier = true
						}
					}
				}
			}

			// eligibilityPersonas carry no ServiceRequest/DeviceRequest (CoverageInforce only).
			eligibilityPersonas := map[string]bool{"uc01": true, "uc01-nc": true}
			if eligibilityPersonas[persona] {
				if activeOrders != 0 {
					t.Errorf("%q: found %d active orders, want 0 (eligibility persona — no order path)", persona, activeOrders)
				}
			} else {
				if activeOrders != 1 {
					t.Errorf("%q: found %d active orders, want exactly 1 (one-active-order invariant)", persona, activeOrders)
				}
			}
			if patientID == "" {
				t.Fatalf("%q: bundle has no Patient", persona)
			}
			if want := expectedMember[persona]; patientID != want {
				t.Errorf("%q: Patient.id = %q, want %q (OpenOrder routes on member)", persona, patientID, want)
			}
			if !sawMemberIdentifier {
				t.Errorf("%q: Patient missing identifier %s|%s (fhirsor.resolvePatient searches on it)",
					persona, MemberSystem, expectedMember[persona])
			}
			patientIDs[persona] = patientID
		})
	}

	// Distinct members across personas: a collision would break OpenOrder routing
	// (it is keyed on member, matching the gateway's sceneMember provider-data values).
	seen := map[string]string{}
	for persona, id := range patientIDs {
		if other, dup := seen[id]; dup {
			t.Errorf("personas %q and %q share Patient.id %q — members must be distinct", persona, other, id)
		}
		seen[id] = persona
	}
}

// TestProviderDataBundle_UnknownPersona pins the public-API error contract: an unknown persona
// name (a typo, or a descoped UC-02/03) returns a non-nil error naming the persona, never silent
// empty bytes a caller might POST as an empty SoR seed.
func TestProviderDataBundle_UnknownPersona(t *testing.T) {
	raw, err := ProviderDataBundle("uc02") // descoped (D-PD-2); no fixture ships for it
	if err == nil {
		t.Fatalf("ProviderDataBundle(unknown persona): err = nil, want a not-found error")
	}
	if raw != nil {
		t.Errorf("ProviderDataBundle(unknown persona): bytes = %q, want nil on error", raw)
	}
	if !strings.Contains(err.Error(), "uc02") {
		t.Errorf("ProviderDataBundle(unknown persona): error %q should name the persona", err)
	}
}

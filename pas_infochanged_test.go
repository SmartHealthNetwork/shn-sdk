package shnsdk

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// fixedInfoChangedClock is the deterministic clock the InfoChanged tests inject.
var fixedInfoChangedClock = time.Unix(1700000000, 0).UTC()

// infoChangedTestSR is a minimal demo-persona ServiceRequest (the single-shot
// procedure order) the InfoChanged conformant-submit tests build a bundle around.
func infoChangedTestSR() []byte {
	return []byte(`{"resourceType":"ServiceRequest","id":"sr-x","status":"active","intent":"order","subject":{"reference":"Patient/MBR-PD-UC04"},"code":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"72148","display":"MRI lumbar spine w/o contrast"}]}}`)
}

func infoChangedTestInputs(infoChanged bool) ConformantClaimInputs {
	return ConformantClaimInputs{
		SR:          infoChangedTestSR(),
		PatientRef:  "Patient/MBR-PD-UC04",
		CoverageRef: "Coverage/MBR-PD-UC04",
		Corr:        "corr-infochanged-0001",
		Created:     fixedInfoChangedClock,
		InfoChanged: infoChanged,
	}
}

// claimItemHasInfoChanged mirrors the gateway's engine-local requestClaimHasInfoChanged
// predicate: it reports whether any Claim entry in a $submit bundle carries the PAS
// infoChanged item extension (the gateway poll discriminator). Replicated here so the SDK
// test proves the built bytes satisfy exactly what the gateway gate checks.
func claimItemHasInfoChanged(t *testing.T, bundleJSON []byte) bool {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Item         []struct {
					Extension []struct {
						URL string `json:"url"`
					} `json:"extension"`
				} `json:"item"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	for _, e := range b.Entry {
		if e.Resource.ResourceType != "Claim" {
			continue
		}
		for _, it := range e.Resource.Item {
			for _, ext := range it.Extension {
				if ext.URL == pasInfoChangedExtensionURL {
					return true
				}
			}
		}
	}
	return false
}

// TestBuildConformantClaimBundle_InfoChangedDefaultByteIdentical proves InfoChanged is
// additive: an unset InfoChanged (the existing-caller default) and an explicit
// InfoChanged:false produce byte-identical bundles for the same inputs — no existing
// caller's wire bytes move.
func TestBuildConformantClaimBundle_InfoChangedDefaultByteIdentical(t *testing.T) {
	unset := infoChangedTestInputs(false)
	unset.InfoChanged = false // explicit false
	explicitFalse := infoChangedTestInputs(false)

	a, err := BuildConformantClaimBundle(unset)
	if err != nil {
		t.Fatalf("build (unset): %v", err)
	}
	b, err := BuildConformantClaimBundle(explicitFalse)
	if err != nil {
		t.Fatalf("build (explicit false): %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("InfoChanged false != unset: bundles differ\n unset=%s\n false=%s", a, b)
	}
	// The default-false bundle MUST carry NO infoChanged extension (so composite UC-04's
	// submit, which does not set InfoChanged, stays in the payer-gw pend lane).
	if claimItemHasInfoChanged(t, a) {
		t.Fatalf("default (InfoChanged:false) bundle unexpectedly carries the infoChanged item extension:\n%s", a)
	}
}

// TestBuildConformantClaimBundle_InfoChangedTrueCarriesExtension proves InfoChanged:true
// emits the Da Vinci PAS infoChanged item extension on the Claim — the exact element the
// gateway's requestClaimHasInfoChanged poll discriminator reads. It does NOT add a
// prior-claim ref (this is a fresh submit, not an update).
func TestBuildConformantClaimBundle_InfoChangedTrueCarriesExtension(t *testing.T) {
	bundleJSON, err := BuildConformantClaimBundle(infoChangedTestInputs(true))
	if err != nil {
		t.Fatalf("build (InfoChanged:true): %v", err)
	}
	if !claimItemHasInfoChanged(t, bundleJSON) {
		t.Fatalf("InfoChanged:true bundle missing the infoChanged item extension:\n%s", bundleJSON)
	}
	// The extension value mirrors setPriorClaimReferenceAndInfoChanged: valueCode "changed".
	// And a fresh submit must NOT carry Claim.related[prior].
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
				Related      []any  `json:"related"`
				Item         []struct {
					Extension []struct {
						URL       string `json:"url"`
						ValueCode string `json:"valueCode"`
					} `json:"extension"`
				} `json:"item"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sawChanged := false
	for _, e := range b.Entry {
		if e.Resource.ResourceType != "Claim" {
			continue
		}
		if len(e.Resource.Related) != 0 {
			t.Fatalf("fresh InfoChanged submit must NOT carry Claim.related[prior]; got %d related", len(e.Resource.Related))
		}
		for _, it := range e.Resource.Item {
			for _, ext := range it.Extension {
				if ext.URL == pasInfoChangedExtensionURL {
					if ext.ValueCode != "changed" {
						t.Fatalf("infoChanged valueCode = %q, want %q (mirror setPriorClaimReferenceAndInfoChanged)", ext.ValueCode, "changed")
					}
					sawChanged = true
				}
			}
		}
	}
	if !sawChanged {
		t.Fatalf("did not observe the infoChanged extension with valueCode=changed")
	}
}

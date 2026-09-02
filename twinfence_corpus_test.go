package shnsdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The twin-fence corpus (testdata/twinfence/) is the shared conformance
// vector set for the two request fences this module carries as deliberate
// twins of the substrate gateway's engine:
//
//   - the FR-16/FR-27 attestation conformance fence (fenceAttestedItems), and
//   - the FR-32 supplemental-data / subject-bind fence on the conformant
//     pas-claim-update leg (handlePASUpdate's guard block).
//
// The vectors are MINTED upstream by the substrate's vector generator (the
// same canonical byte source as testdata/vectors/) and committed here
// byte-identically to the engine's copy; each side's own test drives ITS
// fence over the SAME inputs and asserts the SAME accept/reject verdict, so
// a tolerance added to one side fails the other until the twin learns it
// too. Where a vector pins rejectContains, the twins also share the exact
// rejection text.
//
// This test imports only shnsdk + stdlib, so it runs unchanged in the public
// standalone repo — the published module genuinely proves its fences against
// the canonical corpus rather than trusting the private parity suite.

// twinFenceVector mirrors the committed vector schema (a data contract, kept
// local so the test stays standalone).
type twinFenceVector struct {
	Name           string          `json:"name"`
	Family         string          `json:"family"`
	Description    string          `json:"description"`
	Expect         string          `json:"expect"`
	RejectContains string          `json:"rejectContains"`
	RejectStatus   int             `json:"rejectStatus"`
	TokenMember    string          `json:"tokenMember"`
	OriginalCorr   string          `json:"originalCorr"`
	Bundle         json.RawMessage `json:"bundle"`
}

// twinFenceAdjudicator approves every prior-auth decision: the corpus
// exercises the request FENCES, so an accept vector must reach the
// adjudicator and come back clean rather than being masked by a pend/deny.
type twinFenceAdjudicator struct{}

func (twinFenceAdjudicator) Eligibility(string) (bool, string)   { return true, "" }
func (twinFenceAdjudicator) OrderSelect(string) (bool, string)   { return false, "" }
func (twinFenceAdjudicator) Questionnaire(string) ([]byte, bool) { return nil, false }
func (twinFenceAdjudicator) PriorAuth([]byte, bool) (PASDecision, error) {
	return PASDecision{Outcome: PASApproved, PreAuthRef: "PA-000000000000", ValidUntil: "2026-09-02"}, nil
}

func readTwinFenceCorpus(t *testing.T) []twinFenceVector {
	t.Helper()
	dir := filepath.Join("testdata", "twinfence")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("twin-fence corpus dir %s not found (the committed vectors ship with the module): %v", dir, err)
	}
	var vecs []twinFenceVector
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read vector %s: %v", e.Name(), err)
		}
		var v twinFenceVector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("parse vector %s: %v", e.Name(), err)
		}
		if v.Name+".json" != e.Name() {
			t.Fatalf("vector file %s carries mismatched name %q", e.Name(), v.Name)
		}
		vecs = append(vecs, v)
	}
	if len(vecs) == 0 {
		t.Fatal("twin-fence corpus is empty")
	}
	return vecs
}

// TestTwinFenceCorpus drives every corpus vector into this module's own copy
// of each fence and asserts the authored verdict. An unknown family fails
// loud: a vector added for a new fence family must not silently no-op here
// while the engine enforces it.
func TestTwinFenceCorpus(t *testing.T) {
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for _, v := range readTwinFenceCorpus(t) {
		v := v
		seen[v.Family+"/"+v.Expect] = true
		t.Run(v.Name, func(t *testing.T) {
			switch v.Family {
			case "attestation":
				reason, ok := fenceAttestedItems(v.Bundle)
				switch v.Expect {
				case "accept":
					if !ok {
						t.Fatalf("attestation fence rejected an accept vector: %s", reason)
					}
				case "reject":
					if ok {
						t.Fatal("attestation fence accepted a reject vector")
					}
					if v.RejectContains != "" && !strings.Contains(reason, v.RejectContains) {
						t.Fatalf("rejection reason %q does not contain %q", reason, v.RejectContains)
					}
				default:
					t.Fatalf("unknown verdict %q", v.Expect)
				}
			case "update-bind":
				if v.OriginalCorr == "" {
					t.Fatal("update-bind vector carries no originalCorr")
				}
				r := &Responder{cfg: ResponderConfig{Adjudicator: twinFenceAdjudicator{}}, ledger: newPendedLedger()}
				const subject = "pci:twinfence-corpus"
				r.ledger.record(subject, v.OriginalCorr)
				res := r.handlePASUpdate(v.Bundle, Token{Subject: subject}, "twinfence-corr", now, "")
				switch v.Expect {
				case "accept":
					if res.appStatus != 0 {
						t.Fatalf("update fence rejected an accept vector: %d %s", res.appStatus, res.errMsg)
					}
				case "reject":
					if res.appStatus == 0 {
						t.Fatal("update fence accepted a reject vector")
					}
					if res.appStatus == 409 {
						t.Fatalf("update vector failed the ledger pend, not the fence: %s", res.errMsg)
					}
					if v.RejectStatus != 0 && res.appStatus != v.RejectStatus {
						t.Fatalf("rejection status = %d, want %d (%s)", res.appStatus, v.RejectStatus, res.errMsg)
					}
					if v.RejectContains != "" && !strings.Contains(res.errMsg, v.RejectContains) {
						t.Fatalf("rejection message %q does not contain %q", res.errMsg, v.RejectContains)
					}
				default:
					t.Fatalf("unknown verdict %q", v.Expect)
				}
			default:
				t.Fatalf("unknown vector family %q — teach this driver the new family before committing its vectors", v.Family)
			}
		})
	}
	// Non-vacuity: both fences must have been driven in both directions.
	for _, want := range []string{"attestation/accept", "attestation/reject", "update-bind/accept", "update-bind/reject"} {
		if !seen[want] {
			t.Fatalf("corpus carries no %s vector — the fence pair is no longer exercised in both directions", want)
		}
	}
}

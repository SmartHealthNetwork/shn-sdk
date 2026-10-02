package shnsdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// coveragePartyRow is one row of the shared CoverageParty table
// (testdata/coverageparty/rows.json): a Coverage, the index in its contained
// list of the resource asked about, and whether that resource is the
// Coverage's party. This package's PAS builders and PAS response check decide
// a party by CoverageParty, so the table pins the rule they apply.
type coveragePartyRow struct {
	Name      string         `json:"name"`
	Coverage  map[string]any `json:"coverage"`
	Contained int            `json:"contained"`
	Party     bool           `json:"party"`
}

func coveragePartyRows(t *testing.T) []coveragePartyRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "coverageparty", "rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []coveragePartyRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// candidate returns the contained resource row asks about.
func (r coveragePartyRow) candidate(t *testing.T) map[string]any {
	t.Helper()
	list, _ := r.Coverage["contained"].([]any)
	if r.Contained < 0 || r.Contained >= len(list) {
		t.Fatalf("%s: contained index %d out of range", r.Name, r.Contained)
	}
	cr, ok := list[r.Contained].(map[string]any)
	if !ok {
		t.Fatalf("%s: contained[%d] is not an object", r.Name, r.Contained)
	}
	return cr
}

func TestCoverageParty(t *testing.T) {
	rows := coveragePartyRows(t)
	parties, others := 0, 0
	for _, r := range rows {
		if r.Party {
			parties++
		} else {
			others++
		}
		t.Run(r.Name, func(t *testing.T) {
			if got := CoverageParty(r.Coverage, r.candidate(t)); got != r.Party {
				t.Fatalf("CoverageParty = %t, want %t", got, r.Party)
			}
		})
	}
	// The table must keep both verdicts covered.
	if parties < 5 || others < 20 {
		t.Fatalf("the table holds %d parties and %d others", parties, others)
	}
}

// coveragePartyMarkKey is a member name no decoded JSON document can carry
// (it is not UTF-8): a reader that marks the maps it visits, as the Smart
// Gateway's patient fence does, adds an int under such a key.
const coveragePartyMarkKey = "\xffmark"

// markEveryMap adds an int member under coveragePartyMarkKey to every object
// in v.
func markEveryMap(v any, n *int) {
	switch t := v.(type) {
	case map[string]any:
		for _, e := range t {
			markEveryMap(e, n)
		}
		*n++
		t[coveragePartyMarkKey] = *n
	case []any:
		for _, e := range t {
			markEveryMap(e, n)
		}
	}
}

// A member that holds no reference, here an int under a key no document
// carries, added to every map, changes no answer.
func TestCoveragePartyIgnoresMarks(t *testing.T) {
	for _, r := range coveragePartyRows(t) {
		t.Run(r.Name, func(t *testing.T) {
			n := 0
			markEveryMap(r.Coverage, &n)
			if n < 3 {
				t.Fatalf("marked %d maps", n)
			}
			if _, ok := r.candidate(t)[coveragePartyMarkKey].(int); !ok {
				t.Fatal("the candidate is not marked")
			}
			if got := CoverageParty(r.Coverage, r.candidate(t)); got != r.Party {
				t.Fatalf("marked: CoverageParty = %t, want %t", got, r.Party)
			}
		})
	}
}

// CoverageParty answers only for an element of the Coverage's own contained
// list: a copy of a party (the same members, another map) is no party, nor is
// a lookalike carrying what the element does not.
func TestCoveragePartyRequiresTheElement(t *testing.T) {
	n := 0
	for _, r := range coveragePartyRows(t) {
		if !r.Party {
			continue
		}
		n++
		t.Run(r.Name, func(t *testing.T) {
			cr := r.candidate(t)
			copied := make(map[string]any, len(cr))
			for k, v := range cr {
				copied[k] = v
			}
			if CoverageParty(r.Coverage, copied) {
				t.Fatal("a copy of the party is a party")
			}
			copied["contained"] = []any{map[string]any{"resourceType": "Patient", "id": "m", "identifier": []any{map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}}}
			if CoverageParty(r.Coverage, copied) {
				t.Fatal("a lookalike containing another member is a party")
			}
			if !CoverageParty(r.Coverage, cr) {
				t.Fatal("the element itself is not a party")
			}
		})
	}
	if n == 0 {
		t.Fatal("no party rows")
	}
}

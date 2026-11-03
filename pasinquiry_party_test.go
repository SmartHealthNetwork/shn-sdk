package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// inquiryParent is the participant's own Patient record of a dependent's
// parent, with byte-level details the inquiry must carry unchanged.
var inquiryParent = []byte(" {\"resourceType\":\"Patient\",\"id\":\"parent-1\",\"meta\":{\"versionId\":\"3\",\"profile\":[\"http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient\"]}," +
	"\"identifier\":[{\"system\":\"urn:oid:1.2.3.4.5\",\"value\":\"MRN-9\"}],\"name\":[{\"family\":\"Halvorsen\"}],\"_x\":{\"n\":9007199254740993},\"birthDate\":\"1981-08-09\"}\n")

// inquiryDependentCoverage is inquiryCoverage with its subscriber and policyHolder
// slots set as given (nil leaves a slot out).
func inquiryDependentCoverage(t *testing.T, subscriber, policyHolder any) []byte {
	t.Helper()
	var cov map[string]any
	if err := json.Unmarshal(inquiryCoverage, &cov); err != nil {
		t.Fatal(err)
	}
	if subscriber != nil {
		cov["subscriber"] = subscriber
	}
	if policyHolder != nil {
		cov["policyHolder"] = policyHolder
	}
	cov["relationship"] = map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/subscriber-relationship", "code": "child"}}}
	b, err := json.Marshal(cov)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func refTo(s string) map[string]any { return map[string]any{"reference": s} }

// A dependent's inquiry carries the parent the Coverage names as its
// subscriber and policyHolder, at every line, as a Patient entry of its own:
// the record's own bytes, copied and declared as the input it came from, and
// the Coverage's party by CoverageEntryParties. The Coverage is carried as the
// participant's own bytes, its slots naming the parent's entry. Records the
// Coverage does not name are not carried.
func TestBuildPASInquiryBundle_CarriesTheCoveragesParty(t *testing.T) {
	other := []byte(`{"resourceType":"Patient","id":"cousin","name":[{"family":"Other"}]}`)
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		t.Run(line, func(t *testing.T) {
			in := testInquiryInputs(line)
			in.Coverage = inquiryDependentCoverage(t, refTo("Patient/parent-1"), refTo("Patient/parent-1"))
			in.CoverageParties = [][]byte{other, inquiryParent}
			out, err := BuildPASInquiryBundle(line, in)
			if err != nil {
				t.Fatal(err)
			}
			var b map[string]any
			if err := json.Unmarshal(out.Body, &b); err != nil {
				t.Fatal(err)
			}
			if named := CoverageEntryParties(b); !named[pasBundleBaseURL+"/Patient/parent-1"] || len(named) != 1 {
				t.Fatalf("Coverage parties %v, want the parent's entry alone", named)
			}
			var parties int
			for _, c := range out.Copied {
				if !strings.HasPrefix(c.Input, "CoverageParties[") {
					continue
				}
				parties++
				if c.Input != "CoverageParties[1]" {
					t.Fatalf("the parent is copied from %s, want CoverageParties[1]", c.Input)
				}
				got := out.Body[c.At : c.At+(c.End-c.Start)]
				if !bytes.Equal(got, inquiryParent[c.Start:c.End]) || !bytes.Equal(got, bytes.TrimSpace(inquiryParent)) {
					t.Fatalf("the parent entry is %s, want the record's own bytes", got)
				}
			}
			if parties != 1 {
				t.Fatalf("%d party entries copied, want one", parties)
			}
			if bytes.Contains(out.Body, []byte(`"cousin"`)) {
				t.Fatal("a record the Coverage does not name rides the inquiry")
			}
			for _, c := range out.Copied {
				if c.Input == "Coverage" && !bytes.Equal(out.Body[c.At:c.At+(c.End-c.Start)], in.Coverage) {
					t.Fatal("the Coverage is not carried as the participant's own bytes")
				}
			}
		})
	}
}

// Every way the Coverage's party cannot ride the inquiry is refused as
// ErrPASCoveragePartyNotCarried, as the submission refuses it, and nothing is
// built.
func TestBuildPASInquiryBundle_RefusesAPartyItCannotCarry(t *testing.T) {
	parent := func(edit func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(inquiryParent, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		b, _ := json.Marshal(m)
		return b
	}
	for name, row := range map[string]struct {
		subscriber any
		parties    [][]byte
	}{
		"no record of the parent":     {refTo("Patient/parent-1"), nil},
		"two records with its id":     {refTo("Patient/parent-1"), [][]byte{inquiryParent, inquiryParent}},
		"a record making a reference": {refTo("Patient/parent-1"), [][]byte{parent(func(m map[string]any) { m["generalPractitioner"] = []any{refTo("Practitioner/x")} })}},
		"a record containing others": {refTo("Patient/parent-1"), [][]byte{parent(func(m map[string]any) {
			m["contained"] = []any{map[string]any{"resourceType": "Organization", "id": "o"}}
		})}},
		"another version than the slot names": {refTo("Patient/parent-1/_history/2"), [][]byte{inquiryParent}},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInquiryInputs("2.2")
			in.Coverage = inquiryDependentCoverage(t, row.subscriber, nil)
			in.CoverageParties = row.parties
			if _, err := BuildPASInquiryBundle("2.2", in); !errors.Is(err, ErrPASCoveragePartyNotCarried) {
				t.Fatalf("got %v, want ErrPASCoveragePartyNotCarried", err)
			}
		})
	}
}

// A Coverage naming no one beside the member builds as before, and its records
// supplied are not carried: a member who is their own subscriber, a parent
// contained in the Coverage (riding inside it), and a slot naming no Patient.
// A slot naming the very version supplied is carried.
func TestBuildPASInquiryBundle_PartyUnchangedCases(t *testing.T) {
	contained := func(t *testing.T) []byte {
		var cov map[string]any
		_ = json.Unmarshal(inquiryDependentCoverage(t, refTo("#p"), nil), &cov)
		cov["contained"] = []any{map[string]any{"resourceType": "Patient", "id": "p", "name": []any{map[string]any{"family": "Inside"}}}}
		b, _ := json.Marshal(cov)
		return b
	}
	for name, row := range map[string]struct {
		coverage    func(t *testing.T) []byte
		wantParties int
	}{
		"the member is the subscriber": {func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("Patient/pat-1"), refTo("Patient/pat-1"))
		}, 0},
		"a contained parent":         {contained, 0},
		"an organization subscriber": {func(t *testing.T) []byte { return inquiryDependentCoverage(t, refTo("Organization/employer"), nil) }, 0},
		"the version supplied": {func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("Patient/parent-1/_history/3"), nil)
		}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInquiryInputs("2.2")
			in.Coverage = row.coverage(t)
			in.CoverageParties = [][]byte{inquiryParent}
			out, err := BuildPASInquiryBundle("2.2", in)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, c := range out.Copied {
				if strings.HasPrefix(c.Input, "CoverageParties[") {
					n++
				}
			}
			if n != row.wantParties {
				t.Fatalf("%d party entries, want %d", n, row.wantParties)
			}
		})
	}
}

// The continuation hands the inquiry the parties its records hold.
func TestInquiryInputsCarryTheCoverageParties(t *testing.T) {
	records := PASInquiryRecords{Patient: inquiryPatient(true), Coverage: inquiryCoverage, CoverageParties: [][]byte{inquiryParent}, Provider: inquiryProvider, Insurer: inquiryInsurer}
	in := PriorAuthContinuation{}.InquiryInputs("inq", PASIdentifier{System: "s", Value: "v"}, records, testInquiryInputs("2.2").Timestamp)
	if len(in.CoverageParties) != 1 || !bytes.Equal(in.CoverageParties[0], inquiryParent) {
		t.Fatalf("CoverageParties %q, want the records' parent", in.CoverageParties)
	}
}

// Because the inquiry carries the Coverage as the participant's own bytes, the
// parent's entry takes the fullUrl the slot's own reference resolves to: a
// parent named under another base, or by its version, rides at that URL and
// is the Coverage's party there. A reference that cannot name an entry the
// inquiry carries, one person named by references resolving to two URLs, and a
// beneficiary that does not resolve to the member's entry are refused as
// ErrPASCoveragePartyNotCarried, saying why, rather than sent naming nothing.
func TestBuildPASInquiryBundle_PartyAtTheURLItsReferenceNames(t *testing.T) {
	const abs = "https://ehr.example/fhir/Patient/parent-1"
	for name, row := range map[string]struct {
		subscriber, policyHolder any
		wantURL                  string
	}{
		"an absolute reference":           {refTo(abs), nil, abs},
		"an absolute versioned reference": {refTo(abs + "/_history/3"), nil, abs},
		"a relative reference":            {refTo("Patient/parent-1"), nil, pasBundleBaseURL + "/Patient/parent-1"},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInquiryInputs("2.2")
			in.Coverage = inquiryDependentCoverage(t, row.subscriber, row.policyHolder)
			in.CoverageParties = [][]byte{inquiryParent}
			out, err := BuildPASInquiryBundle("2.2", in)
			if err != nil {
				t.Fatal(err)
			}
			var b map[string]any
			if err := json.Unmarshal(out.Body, &b); err != nil {
				t.Fatal(err)
			}
			if named := CoverageEntryParties(b); !named[row.wantURL] || len(named) != 1 {
				t.Fatalf("Coverage parties %v, want %s alone", named, row.wantURL)
			}
		})
	}
	beneficiaryAbsolute := func(t *testing.T) []byte {
		var cov map[string]any
		_ = json.Unmarshal(inquiryDependentCoverage(t, refTo("Patient/parent-1"), nil), &cov)
		cov["beneficiary"] = refTo("https://ehr.example/fhir/Patient/pat-1")
		b, _ := json.Marshal(cov)
		return b
	}
	for name, row := range map[string]struct {
		coverage func(t *testing.T) []byte
		want     string
	}{
		"a reference naming no entry": {func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("Patient/parent-1/_history/"), nil)
		}, "names Patient/parent-1"},
		"one person at two URLs": {func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("Patient/parent-1"), refTo(abs))
		}, "resolves to another entry"},
		"a beneficiary resolving to no member entry": {beneficiaryAbsolute, "does not resolve to the member's entry"},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInquiryInputs("2.2")
			in.Coverage = row.coverage(t)
			in.CoverageParties = [][]byte{inquiryParent}
			_, err := BuildPASInquiryBundle("2.2", in)
			if !errors.Is(err, ErrPASCoveragePartyNotCarried) || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("got %v, want ErrPASCoveragePartyNotCarried saying %q", err, row.want)
			}
		})
	}
}

// Each remaining way a party cannot ride is refused, saying why, never built
// with the parent left out or named at nothing: a Coverage spelling its
// beneficiary's reference member in another case (so the member it names
// cannot be read exactly), a beneficiary naming another version of the
// member's record, a slot whose reference has a scheme no entry can carry or
// a trailing slash, and a parent the member's own Patient also names, so it
// would not be the Coverage's party alone.
func TestBuildPASInquiryBundle_RefusesEachUnresolvableParty(t *testing.T) {
	withBeneficiary := func(b any) func(t *testing.T) []byte {
		return func(t *testing.T) []byte {
			var cov map[string]any
			_ = json.Unmarshal(inquiryDependentCoverage(t, refTo("Patient/parent-1"), nil), &cov)
			cov["beneficiary"] = b
			out, _ := json.Marshal(cov)
			return out
		}
	}
	linkedPatient := func(t *testing.T) []byte {
		var p map[string]any
		if err := json.Unmarshal(inquiryPatient(true), &p); err != nil {
			t.Fatal(err)
		}
		p["link"] = []any{map[string]any{"other": refTo("Patient/parent-1"), "type": "seealso"}}
		out, _ := json.Marshal(p)
		return out
	}
	for name, row := range map[string]struct {
		coverage func(t *testing.T) []byte
		patient  func(t *testing.T) []byte
		want     string
	}{
		"a beneficiary reference member in another case": {coverage: withBeneficiary(map[string]any{"Reference": "Patient/pat-1"}), want: "reference"},
		"a beneficiary naming another version":           {coverage: withBeneficiary(refTo("Patient/pat-1/_history/1")), want: "another version of the member's record"},
		"a slot under a scheme no entry carries": {coverage: func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("ftp://x.example/Patient/parent-1"), nil)
		}, want: "resolves to no entry"},
		"a slot with a trailing slash": {coverage: func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("https://ehr.example/fhir/Patient/parent-1/"), nil)
		}, want: "resolves to no entry"},
		"a parent the member's Patient also names": {coverage: func(t *testing.T) []byte {
			return inquiryDependentCoverage(t, refTo("Patient/parent-1"), nil)
		}, patient: linkedPatient, want: "not its party alone"},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInquiryInputs("2.2")
			in.Coverage = row.coverage(t)
			if row.patient != nil {
				in.Patient = row.patient(t)
			}
			in.CoverageParties = [][]byte{inquiryParent}
			_, err := BuildPASInquiryBundle("2.2", in)
			if !errors.Is(err, ErrPASCoveragePartyNotCarried) || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("got %v, want ErrPASCoveragePartyNotCarried saying %q", err, row.want)
			}
		})
	}
	// A member who is their own subscriber, named absolutely or by version,
	// builds as before: there is no party to resolve.
	for _, ref := range []string{"https://ehr.example/fhir/Patient/pat-1", "Patient/pat-1/_history/9"} {
		in := testInquiryInputs("2.2")
		var cov map[string]any
		_ = json.Unmarshal(inquiryCoverage, &cov)
		cov["beneficiary"], cov["subscriber"] = refTo(ref), refTo(ref)
		in.Coverage, _ = json.Marshal(cov)
		if _, err := BuildPASInquiryBundle("2.2", in); err != nil {
			t.Errorf("own subscriber at %s: %v", ref, err)
		}
	}
}

// A reference member spelled in another case is refused only where it could
// hide a person beside the member: a subscriber or policyHolder slot, or the
// beneficiary, that, read leniently, leaves a slot naming a Patient other than
// the member. Elsewhere (a slot naming the member or no Patient, the payor, a
// member who is their own subscriber) the inquiry builds as before.
func TestBuildPASInquiryBundle_CaseVariantReferencesOnlyWhereAPartyCouldHide(t *testing.T) {
	cov := func(t *testing.T, edit func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(inquiryCoverage, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		b, _ := json.Marshal(m)
		return b
	}
	for name, row := range map[string]struct {
		edit    func(m map[string]any)
		refused bool
	}{
		"a parent named only by a policyHolder so spelled": {func(m map[string]any) {
			m["policyHolder"] = map[string]any{"Reference": "Patient/parent-1"}
		}, true},
		"the member as subscriber, a payor so spelled": {func(m map[string]any) {
			m["subscriber"] = refTo("Patient/pat-1")
			m["payor"] = []any{map[string]any{"Reference": "Organization/payer-1"}}
		}, false},
		"the member as subscriber, the beneficiary so spelled": {func(m map[string]any) {
			m["beneficiary"] = map[string]any{"Reference": "Patient/pat-1"}
			m["subscriber"] = refTo("Patient/pat-1")
		}, false},
		"the member as subscriber, so spelled": {func(m map[string]any) {
			m["subscriber"] = map[string]any{"Reference": "Patient/pat-1"}
		}, false},
		"a slot naming no Patient, so spelled": {func(m map[string]any) {
			m["subscriber"] = map[string]any{"Reference": "RelatedPerson/rp-1"}
		}, false},
		"the member as subscriber, both so spelled": {func(m map[string]any) {
			m["beneficiary"] = map[string]any{"Reference": "Patient/pat-1"}
			m["subscriber"] = map[string]any{"Reference": "Patient/pat-1"}
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInquiryInputs("2.2")
			in.Coverage = cov(t, row.edit)
			in.CoverageParties = [][]byte{inquiryParent}
			_, err := BuildPASInquiryBundle("2.2", in)
			if refused := errors.Is(err, ErrPASCoveragePartyNotCarried); refused != row.refused || (!row.refused && err != nil) {
				t.Fatalf("got %v, want refused %v", err, row.refused)
			}
		})
	}
}

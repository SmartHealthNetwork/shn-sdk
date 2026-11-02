package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// entryParent is the participant's own Patient record of a dependent's parent:
// a record of its own, which the dependent's Coverage names by reference.
const entryParent = `{"resourceType":"Patient","id":"parent","meta":{"profile":["http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"]},"identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"name":[{"family":"Parent","given":["Pat"]}],"gender":"female","birthDate":"1981-08-09"}`

// entryParentCarried is entryParent as a request carries it: less
// meta.profile, its only meta member.
const entryParentCarried = `{"resourceType":"Patient","id":"parent","identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"name":[{"family":"Parent","given":["Pat"]}],"gender":"female","birthDate":"1981-08-09"}`

// entryCoverage is a dependent's Coverage naming the parent's own Patient
// record, by ref, in slots.
func entryCoverage(member, ref string, slots []string, extra string) []byte {
	c := `{"resourceType":"Coverage","id":"cov-` + strings.ToLower(member) + `","status":"active",` +
		`"identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},` +
		`"system":"urn:shn:coverage","value":"` + member + `"}],`
	for _, s := range slots {
		c += `"` + s + `":{"reference":"` + ref + `"},`
	}
	return []byte(c + `"beneficiary":{"reference":"Patient/` + member + `"},` +
		`"relationship":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/subscriber-relationship","code":"child"}]},` +
		`"payor":[{"reference":"Organization/org-cms-payer"}]` + extra + `}`)
}

// entryBuild builds a dependent's submission, or an amendment continuing that
// submission, at line, with coverage and the records supplied as
// CoverageParties.
func entryBuild(t *testing.T, line string, update, absolute bool, coverage []byte, parties [][]byte) ([]byte, error) {
	t.Helper()
	sub := conformantSubmitInputs(t)
	sub.AbsoluteRefs, sub.PayerOrgEntry = absolute, absolute
	sub.Coverage, sub.CoverageParties = coverage, parties
	built, err := BuildConformantClaimBundleAtLineExact(line, sub)
	if !update || err != nil {
		return built, err
	}
	prior, err := PASPriorClaimOf(built)
	if err != nil {
		t.Fatal(err)
	}
	in := conformantUpdateInputsFromGolden(t)
	in.AbsoluteRefs, in.PayerOrgEntry = absolute, absolute
	in.PriorClaim = prior
	in.Coverage, in.CoverageParties = coverage, parties
	return BuildConformantClaimUpdateBundleAtLineExact(line, in)
}

// entryAmend builds, at line, an amendment with coverage and the records
// supplied as CoverageParties, continuing the member's plain submission: the
// amendment builder alone decides it.
func entryAmend(t *testing.T, line string, coverage []byte, parties [][]byte) ([]byte, error) {
	t.Helper()
	built, err := BuildConformantClaimBundleAtLineExact(line, conformantSubmitInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := PASPriorClaimOf(built)
	if err != nil {
		t.Fatal(err)
	}
	in := conformantUpdateInputsFromGolden(t)
	in.PriorClaim = prior
	in.Coverage, in.CoverageParties = coverage, parties
	return BuildConformantClaimUpdateBundleAtLineExact(line, in)
}

// patientEntries returns the fullUrl and resource of each Patient entry of a
// built Bundle, in entry order.
func patientEntries(t *testing.T, bundle []byte) (urls []string, resources []json.RawMessage) {
	t.Helper()
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	for _, e := range b.Entry {
		if PASResourceTypeOf(e.Resource) == "Patient" {
			urls = append(urls, e.FullURL)
			resources = append(resources, e.Resource)
		}
	}
	return urls, resources
}

// A dependent's Coverage naming the parent's own Patient record is carried:
// the parent rides as a Patient entry of its own, as the participant's record
// has it less meta.profile, after the member's; each slot naming the parent
// names that entry; and CoverageEntryParties names the entry the Coverage's
// party. On the submission and the amendment, at every line, with relative and
// absolute references, whatever form the record's slot reference takes, and
// a supplied record the Coverage does not name is not carried.
func TestPASBuildersCarryTheCoverageEntryParty(t *testing.T) {
	const member = "MBR-COVERED"
	unnamed := []byte(`{"resourceType":"Patient","id":"sibling","name":[{"family":"Sibling"}]}`)
	for _, line := range pasLines {
		for _, update := range []bool{false, true} {
			for _, absolute := range []bool{false, true} {
				for setName, slots := range dependentSlotSets {
					for _, ref := range []string{"Patient/parent", "https://ehr.example/fhir/Patient/parent", "Patient/parent/_history/3"} {
						name := line + "/" + map[bool]string{false: "submit", true: "update"}[update] + "/" + map[bool]string{false: "relative", true: "absolute"}[absolute] + "/" + setName + "/" + ref
						t.Run(name, func(t *testing.T) {
							// A slot naming one version of the record names the
							// entry carried when the record is that version.
							record, carried := entryParent, entryParentCarried
							if strings.Contains(ref, "/_history/") {
								record = strings.Replace(entryParent, `"meta":{`, `"meta":{"versionId":"3",`, 1)
								carried = strings.Replace(entryParentCarried, `"id":"parent",`, `"id":"parent","meta":{"versionId":"3"},`, 1)
							}
							out, err := entryBuild(t, line, update, absolute, entryCoverage(member, ref, slots, ""), [][]byte{unnamed, []byte(record)})
							if err != nil {
								t.Fatalf("refused: %v", err)
							}
							urls, resources := patientEntries(t, out)
							parentURL := pasBundleBaseURL + "/Patient/parent"
							if len(urls) != 2 || urls[0] != pasBundleBaseURL+"/Patient/"+member || urls[1] != parentURL {
								t.Fatalf("Patient entries %v, want the member's then the parent's", urls)
							}
							if !jsonEqual(t, resources[1], []byte(carried)) {
								t.Fatalf("the parent entry %s is not the record less meta.profile", resources[1])
							}
							cov := coverageEntryOf(t, out)
							want := `{"reference":"Patient/parent"}`
							if absolute {
								want = `{"reference":"` + parentURL + `"}`
							}
							for _, s := range slots {
								if !jsonEqual(t, cov[s], []byte(want)) {
									t.Fatalf("%s %s, want %s", s, cov[s], want)
								}
							}
							var b map[string]any
							if err := json.Unmarshal(out, &b); err != nil {
								t.Fatal(err)
							}
							if parties := CoverageEntryParties(b); len(parties) != 1 || !parties[parentURL] {
								t.Fatalf("CoverageEntryParties %v, want the parent's entry alone", parties)
							}
						})
					}
				}
			}
		}
	}
}

// A member who is their own subscriber needs no record of anyone else: the
// request is byte-identical whether or not records are supplied, and carries
// one Patient.
func TestPASBuildersIgnorePartiesTheCoverageDoesNotName(t *testing.T) {
	for _, line := range pasLines {
		in := conformantSubmitInputs(t)
		plain, err := BuildConformantClaimBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		in.CoverageParties = [][]byte{[]byte(entryParent)}
		with, err := BuildConformantClaimBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		if string(plain) != string(with) {
			t.Errorf("%s: supplying a record the Coverage does not name changed the request", line)
		}
		if urls, _ := patientEntries(t, with); len(urls) != 1 {
			t.Errorf("%s: %d Patient entries, want the member's alone", line, len(urls))
		}
	}
}

// REJECTION: a Coverage naming a person other than the member whose record
// cannot ride the request is refused as ErrPASCoveragePartyNotCarried, on the
// submission and the amendment, at every line, naming why. Nothing is made up
// and nothing the Coverage asserts is dropped.
func TestPASBuildersRefuseAnEntryPartyNotCarried(t *testing.T) {
	const member = "MBR-COVERED"
	parent := func(old, new string) [][]byte {
		r := strings.Replace(entryParent, old, new, 1)
		if r == entryParent {
			t.Fatalf("mutation %q did not apply", old)
		}
		return [][]byte{[]byte(r)}
	}
	sub := entryCoverage(member, "Patient/parent", []string{"subscriber"}, "")
	for _, tc := range []struct {
		name     string
		coverage []byte
		parties  [][]byte
		want     string
	}{
		{"no record supplied", sub, nil, "no Patient record of that person was supplied"},
		{"a record of someone else", sub, [][]byte{[]byte(strings.Replace(entryParent, `"id":"parent"`, `"id":"other"`, 1))}, "no Patient record of that person was supplied"},
		{"a record that is no Patient", sub, [][]byte{[]byte(`{"resourceType":"RelatedPerson","id":"parent"}`)}, "no Patient record of that person was supplied"},
		{"two records with that id", sub, [][]byte{[]byte(entryParent), []byte(entryParent)}, "more than one of the Patient records supplied has that id"},
		{"a record containing a resource", sub, parent(`"name":`, `"contained":[{"resourceType":"Organization","id":"o"}],"name":`), "contains other resources"},
		{"a record whose identifier is not a list", sub, parent(`"identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}]`, `"identifier":{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}`), "has an identifier that is not a list"},
		{"a record with meta named in another case", sub, parent(`"meta":`, `"Meta":`), `has a member named "Meta"`},
		{"a record repeating a member name", sub, parent(`"gender":"female"`, `"gender":"female","gender":"male"`), `repeats the member name "gender"`},
		{"a record referencing an outside record", sub, parent(`"name":`, `"managingOrganization":{"reference":"Organization/clinic"},"name":`), "Patient.managingOrganization.reference -> Organization/clinic"},
		{"a record referencing under a case-variant member", sub, parent(`"name":`, `"generalPractitioner":[{"Reference":"Practitioner/dr"}],"name":`), "Patient.generalPractitioner[0].Reference -> Practitioner/dr"},
		{"a slot naming another version of the record", entryCoverage(member, "Patient/parent/_history/3", []string{"subscriber"}, ""), parent(`"meta":{`, `"meta":{"versionId":"7",`), `names version "3" of Patient/parent, and the record supplied is version "7"`},
		{"a slot naming a version of a record that states none", entryCoverage(member, "Patient/parent/_history/3", []string{"subscriber"}, ""), [][]byte{[]byte(entryParent)}, `names version "3" of Patient/parent, and the record supplied states no version`},
		{"policyHolder naming another version than subscriber", entryCoverage(member, "Patient/parent", []string{"subscriber"}, `,"policyHolder":{"reference":"Patient/parent/_history/2"}`), parent(`"meta":{`, `"meta":{"versionId":"3",`), `policyHolder names version "2" of Patient/parent, and the record supplied is version "3"`},
		{"a slot naming an empty version", entryCoverage(member, "Patient/parent/_history/", []string{"subscriber"}, ""), [][]byte{[]byte(entryParent)}, "names Patient/parent with an empty version"},
		{"a slot naming a record with no valid id", entryCoverage(member, "Patient/par_ent", []string{"subscriber"}, ""), [][]byte{[]byte(strings.Replace(entryParent, `"id":"parent"`, `"id":"par_ent"`, 1))}, "whose record has no valid id"},
		{"a slot referencing beside the person", entryCoverage(member, "Patient/parent", nil, `,"subscriber":{"reference":"Patient/parent","identifier":{"assigner":{"reference":"Organization/x"}}}`), [][]byte{[]byte(entryParent)}, "Coverage.subscriber.identifier.assigner.reference -> Organization/x"},
	} {
		for _, line := range pasLines {
			for _, update := range []bool{false, true} {
				t.Run(tc.name+"/"+line+map[bool]string{false: "/submit", true: "/update"}[update], func(t *testing.T) {
					var err error
					if update {
						_, err = entryAmend(t, line, tc.coverage, tc.parties)
					} else {
						_, err = entryBuild(t, line, false, false, tc.coverage, tc.parties)
					}
					if !errors.Is(err, ErrPASCoveragePartyNotCarried) {
						t.Fatalf("err %v, want ErrPASCoveragePartyNotCarried", err)
					}
					if !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("refusal %q does not say %q", err, tc.want)
					}
				})
			}
		}
	}
}

// REJECTION: a person the build would carry beside the member who is not the
// Coverage's party alone in the built request is refused: here the parent's
// record has the member entry's id, so it would share that entry's fullUrl.
// The beneficiary names a different record of the member than the member
// entry's id, so the slot is not re-homed to the member.
func TestPASBuildersRefuseAnEntryPartySharingTheMembersEntry(t *testing.T) {
	const member = "MBR-COVERED"
	cov := []byte(strings.Replace(string(entryCoverage(member, "Patient/"+member, []string{"subscriber"}, "")),
		`"beneficiary":{"reference":"Patient/`+member+`"}`, `"beneficiary":{"reference":"Patient/child-record"}`, 1))
	rec := []byte(strings.Replace(entryParent, `"id":"parent"`, `"id":"`+member+`"`, 1))
	for _, line := range pasLines {
		_, err := entryBuild(t, line, false, false, cov, [][]byte{rec})
		if !errors.Is(err, ErrPASCoveragePartyNotCarried) || !strings.Contains(err.Error(), "is not its party alone") {
			t.Fatalf("%s submit: err %v, want ErrPASCoveragePartyNotCarried naming the entry", line, err)
		}
		_, err = entryAmend(t, line, cov, [][]byte{rec})
		if !errors.Is(err, ErrPASCoveragePartyNotCarried) || !strings.Contains(err.Error(), "is not its party alone") {
			t.Fatalf("%s update: err %v, want ErrPASCoveragePartyNotCarried naming the entry", line, err)
		}
	}
}

// REJECTION, unchanged: a Coverage whose beneficiary names no literal Patient
// (an identifier alone) cannot say whether a slot naming a Patient record is
// the member, so that slot is refused as naming someone other than the member,
// even with the record supplied, on both builders.
func TestPASBuildersRefuseAnEntryPartyBesideAnUnnamedBeneficiary(t *testing.T) {
	const member = "MBR-COVERED"
	cov := []byte(strings.Replace(string(entryCoverage(member, "Patient/parent", []string{"subscriber"}, "")),
		`"beneficiary":{"reference":"Patient/`+member+`"}`, `"beneficiary":{"identifier":{"system":"urn:shn:coverage","value":"`+member+`"}}`, 1))
	for _, line := range pasLines {
		for name, build := range map[string]func() ([]byte, error){
			"submit": func() ([]byte, error) { return entryBuild(t, line, false, false, cov, [][]byte{[]byte(entryParent)}) },
			"update": func() ([]byte, error) { return entryAmend(t, line, cov, [][]byte{[]byte(entryParent)}) },
		} {
			if _, err := build(); err == nil || !strings.Contains(err.Error(), "subscriber names someone other than the member it covers") {
				t.Fatalf("%s %s: err %v, want the slot refused as naming someone other than the member", line, name, err)
			}
		}
	}
}

// A slot's other members ride exactly as the record writes them: a decimal's
// precision included.
func TestPASBuildersKeepTheEntryPartySlotsBytes(t *testing.T) {
	const member = "MBR-COVERED"
	cov := entryCoverage(member, "Patient/parent", nil, `,"subscriber":{"reference":"Patient/parent","extension":[{"url":"http://example.org/share","valueDecimal":1.50}],"display":"Pat Parent"}`)
	out, err := entryBuild(t, "2.2", false, false, cov, [][]byte{[]byte(entryParent)})
	if err != nil {
		t.Fatal(err)
	}
	got := string(coverageEntryOf(t, out)["subscriber"])
	for _, want := range []string{`"valueDecimal":1.50`, `"display":"Pat Parent"`, `"reference":"Patient/parent"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscriber %s does not keep %s", got, want)
		}
	}
}

// The requests the builders carry an entry party on are ones the SDK's own
// PAS check reads, as a Responder does: the parent's entry is the Coverage's
// party, never a second patient, on the submission and the amendment, at
// every line and with relative and absolute references.
func TestPASBuildersEntryPartyPassesTheMixedPatientCheck(t *testing.T) {
	const member = "MBR-COVERED"
	for _, line := range pasLines {
		for _, update := range []bool{false, true} {
			for _, absolute := range []bool{false, true} {
				out, err := entryBuild(t, line, update, absolute, entryCoverage(member, "Patient/parent", []string{"subscriber", "policyHolder"}, ""), [][]byte{[]byte(entryParent)})
				if err != nil {
					t.Fatalf("%s update=%v absolute=%v: %v", line, update, absolute, err)
				}
				if pasCarriesAnotherPatient(out, member) {
					t.Fatalf("%s update=%v absolute=%v: the check reads the parent's entry as another patient", line, update, absolute)
				}
			}
		}
	}
	// The control: the same entry no Coverage names is another patient.
	out, err := entryBuild(t, "2.2", false, false, entryCoverage(member, "Patient/parent", []string{"subscriber"}, ""), [][]byte{[]byte(entryParent)})
	if err != nil {
		t.Fatal(err)
	}
	unnamed := bytes.Replace(out, []byte(`"subscriber":{"reference":"Patient/parent"}`), []byte(`"subscriber":{"display":"a parent"}`), 1)
	if bytes.Equal(unnamed, out) || !pasCarriesAnotherPatient(unnamed, member) {
		t.Fatal("the control did not make the parent's entry another patient")
	}
}

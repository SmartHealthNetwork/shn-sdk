package shnsdk_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// blankMemberRefs are patient references that name no member: none at all,
// the bare type, or only whitespace where the id goes. Searched as a member
// identifier, a blank code matches every identifier of the member system
// (FHIR token search: "system|" names the system and any value), so a reader
// that returned one would bind whichever patient a system of record happens
// to hold alone.
var blankMemberRefs = []string{"", " ", "Patient/", "Patient/ ", "Patient/\t", "Patient/\u00a0", "Patient/ \n ", "Patient/Patient/", "Patient/Patient/ "}

func blankMemberCER(ref string) []byte {
	return []byte(`{"resourceType":"CoverageEligibilityRequest","status":"active","purpose":["validation"],` +
		`"patient":{"reference":` + jsonQuote(ref) + `},"created":"2026-01-01","insurer":{"reference":"Organization/payer"}}`)
}

func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestEligibilityReadersRefuseABlankMember: each exported reader of a coverage
// eligibility request's member refuses a request whose patient reference names
// no member, where it returned "" (or the whitespace) with no error. The
// control row proves the same request with a member still reads.
func TestEligibilityReadersRefuseABlankMember(t *testing.T) {
	readers := map[string]func([]byte) (string, error){
		"ParseEligibilityRequestMember":      shnsdk.ParseEligibilityRequestMember,
		"ParseEligibilityRequestMemberExact": shnsdk.ParseEligibilityRequestMemberExact,
		"ParseEligibilitySubmission": func(b []byte) (string, error) {
			subs, err := shnsdk.ParseEligibilitySubmission(b)
			if err != nil {
				return "", err
			}
			if len(subs) != 1 {
				t.Fatalf("ParseEligibilitySubmission: %d submissions, want 1", len(subs))
			}
			return subs[0].Member, nil
		},
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			if m, err := read(blankMemberCER("Patient/MBR-001")); err != nil || m != "MBR-001" {
				t.Fatalf("control: member %q, err %v; want MBR-001", m, err)
			}
			for _, ref := range blankMemberRefs {
				m, err := read(blankMemberCER(ref))
				if err == nil {
					t.Errorf("patient reference %q: member %q with no error; want a refusal", ref, m)
					continue
				}
				if m != "" {
					t.Errorf("patient reference %q: refused but returned member %q", ref, m)
				}
				checkNamesNoOne(t, "patient reference "+strconv.Quote(ref), err)
			}
		})
	}
}

// namesNoOne is the refusal of a request whose patient names no member: the
// text a payer's Smart Gateway answers it with, as an *EligibilityRequestRefusal
// (a Responder answers with its text), wrapping ErrBlankMember.
const namesNoOne = "eligibility request names its patient by a reference that names no one, so the request names no patient to bind"

func checkNamesNoOne(t *testing.T, what string, err error) {
	t.Helper()
	var refusal *shnsdk.EligibilityRequestRefusal
	if !errors.Is(err, shnsdk.ErrBlankMember) || !errors.As(err, &refusal) || refusal.Error() != namesNoOne || err.Error() != namesNoOne {
		t.Errorf("%s: refused as %v; want the refusal %q wrapping ErrBlankMember", what, err, namesNoOne)
	}
}

// TestEligibilitySubmissionBundleRefusesABlankMember: a request submitted in a
// Bundle whose patient reference names no member is refused as a request sent
// alone is: with the gateway's text, wrapping ErrBlankMember.
func TestEligibilitySubmissionBundleRefusesABlankMember(t *testing.T) {
	for _, ref := range blankMemberRefs {
		body := `{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"urn:uuid:1","resource":` +
			string(blankMemberCER(ref)) + `}]}`
		subs, err := shnsdk.ParseEligibilitySubmission([]byte(body))
		if err == nil {
			t.Errorf("bundled patient reference %q: %+v with no error; want a refusal", ref, subs)
			continue
		}
		checkNamesNoOne(t, "bundled patient reference "+strconv.Quote(ref), err)
		batch := `{"resourceType":"Bundle","type":"batch","entry":[{"resource":` + string(blankMemberCER(ref)) + `}]}`
		if _, err := shnsdk.ParseEligibilitySubmission([]byte(batch)); err == nil {
			t.Errorf("batched patient reference %q: no error; want a refusal", ref)
		} else {
			checkNamesNoOne(t, "batched patient reference "+strconv.Quote(ref), err)
		}
	}
	// A Bundle's request that names its patient by no reference at all is
	// no reference naming no one (unknown is not empty): it keeps its answer,
	// an error that is no refusal, as the same request sent alone does.
	for name, patient := range map[string]string{
		"no patient":                    ``,
		"a patient named by identifier": `,"patient":{"identifier":{"system":"urn:shn:member","value":"MBR-1"}}`,
	} {
		body := `{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"urn:uuid:1","resource":` +
			`{"resourceType":"CoverageEligibilityRequest","insurer":{"identifier":{"value":"P"}}` + patient + `}}]}`
		_, err := shnsdk.ParseEligibilitySubmission([]byte(body))
		var refusal *shnsdk.EligibilityRequestRefusal
		if err == nil || errors.Is(err, shnsdk.ErrBlankMember) || errors.As(err, &refusal) {
			t.Errorf("bundled request with %s: error %v; want an error that is no refusal", name, err)
		}
	}
}

// TestParseCDexTaskDataRequestRefusesABlankPatient: a federated-query Task
// whose Task.for (and the query's patient, alike) names no member is refused,
// where it read as PatientRef "Patient/" with no error.
func TestParseCDexTaskDataRequestRefusesABlankPatient(t *testing.T) {
	valid := validCDexTask(t)
	for _, ref := range []string{"Patient/", "Patient/ ", "Patient/\t", "Patient/\u00a0", "Patient/Patient/", "Patient/Patient/ "} {
		q := jsonQuote(ref)
		task := strings.ReplaceAll(string(valid), `"Patient/MBR-UC05"`, q)
		task = strings.ReplaceAll(task, "patient=Patient/MBR-UC05", "patient="+strings.Trim(q, `"`))
		if task == string(valid) {
			t.Fatal("fixture: the reference was not replaced")
		}
		got, err := shnsdk.ParseCDexTaskDataRequest([]byte(task))
		if err == nil {
			t.Errorf("Task.for %q: %+v with no error; want a refusal", ref, got)
			continue
		}
		if !errors.Is(err, shnsdk.ErrBlankMember) || err.Error() != "cdex: Task.for names no patient" {
			t.Errorf("Task.for %q: refused for another reason: %v", ref, err)
		}
	}
}

// TestResolvePCIBlankMember: a blank member id names no one, so ResolvePCI
// derives no identifier from it ("", never a hash every blank member would
// share) and ResolvePCIStrict refuses it. A member id with whitespace around
// it is not blank, and both derive what ResolvePCI always derived.
func TestResolvePCIBlankMember(t *testing.T) {
	for _, m := range []string{"", " ", "\t", "\u00a0", " \n ", "Patient/", "Patient/ "} {
		if got := shnsdk.ResolvePCI(m, "1975-04-02", "Johansson"); got != "" {
			t.Errorf("ResolvePCI(%q): %q; want \"\"", m, got)
		}
		if got, err := shnsdk.ResolvePCIStrict(m, "1975-04-02", "Johansson"); err == nil || got != "" {
			t.Errorf("ResolvePCIStrict(%q): %q, err %v; want \"\" and an error", m, got, err)
		}
	}
	for _, m := range []string{"MBR-COVERED", " MBR-COVERED "} {
		want := shnsdk.ResolvePCI(m, "1975-04-02", "Johansson")
		if !strings.HasPrefix(want, "pci:") {
			t.Fatalf("ResolvePCI(%q): %q; want a pci", m, want)
		}
		got, err := shnsdk.ResolvePCIStrict(m, "1975-04-02", "Johansson")
		if err != nil || got != want {
			t.Errorf("ResolvePCIStrict(%q): %q, err %v; want %q", m, got, err, want)
		}
	}
}

// TestEligibilityReadersRefuseABlankMemberFromItsPatient: a member read
// through the Patient a reference resolves to (a contained Patient's member
// identifier, or a Bundle entry's id) that is only whitespace names no member
// either, and the exact readers refuse it.
func TestEligibilityReadersRefuseABlankMemberFromItsPatient(t *testing.T) {
	contained := func(value string) []byte {
		return []byte(`{"resourceType":"CoverageEligibilityRequest","status":"active","purpose":["validation"],` +
			`"contained":[{"resourceType":"Patient","id":"p","identifier":[{"system":"` + shnsdk.MemberSystem + `","value":` + jsonQuote(value) + `}]}],` +
			`"patient":{"reference":"#p"},"created":"2026-01-01","insurer":{"reference":"Organization/payer"}}`)
	}
	bundled := func(id string) []byte {
		cer := `{"resourceType":"CoverageEligibilityRequest","status":"active","purpose":["validation"],` +
			`"patient":{"reference":"urn:uuid:p"},"created":"2026-01-01","insurer":{"reference":"Organization/payer"}}`
		return []byte(`{"resourceType":"Bundle","type":"collection","entry":[` +
			`{"fullUrl":"urn:uuid:c","resource":` + cer + `},` +
			`{"fullUrl":"urn:uuid:p","resource":{"resourceType":"Patient","id":` + jsonQuote(id) + `}}]}`)
	}
	submission := func(b []byte) (string, error) {
		subs, err := shnsdk.ParseEligibilitySubmission(b)
		if err != nil {
			return "", err
		}
		return subs[0].Member, nil
	}
	for _, row := range []struct {
		name    string
		read    func([]byte) (string, error)
		control []byte
		blank   func(string) []byte
	}{
		{"exact, contained", shnsdk.ParseEligibilityRequestMemberExact, contained("MBR-001"), contained},
		{"submission, contained", submission, contained("MBR-001"), contained},
		{"submission, bundle entry id", submission, bundled("MBR-001"), bundled},
	} {
		t.Run(row.name, func(t *testing.T) {
			if m, err := row.read(row.control); err != nil || m != "MBR-001" {
				t.Fatalf("control: member %q, err %v; want MBR-001", m, err)
			}
			for _, v := range []string{" ", "\t", "\u00a0", " \n ", "Patient/", "Patient/ "} {
				m, err := row.read(row.blank(v))
				if err == nil {
					t.Errorf("value %q: member %q with no error; want a refusal", v, m)
					continue
				}
				checkNamesNoOne(t, "value "+strconv.Quote(v), err)
			}
		})
	}
}

// TestExactReaderRefusesAnotherCaseBeforeABlankMember: the exact readers refuse
// a request that names its patient or reference only in another case for that
// first, as they did before a blank member was refused, even when the reference
// is also blank.
func TestExactReaderRefusesAnotherCaseBeforeABlankMember(t *testing.T) {
	const head = `{"resourceType":"CoverageEligibilityRequest","status":"active","purpose":["validation"],"created":"2026-01-01","insurer":{"reference":"Organization/payer"},`
	for _, row := range []struct{ body, want string }{
		{head + `"Patient":{"reference":"Patient/"}}`, `eligibility request names "Patient" for "patient": member names are case-sensitive, so the request names no patient to bind`},
		{head + `"patient":{"REFERENCE":""}}`, `eligibility request names "REFERENCE" for "reference": member names are case-sensitive, so the request names no patient to bind`},
	} {
		for name, read := range map[string]func([]byte) (string, error){
			"ParseEligibilityRequestMemberExact": shnsdk.ParseEligibilityRequestMemberExact,
			"ParseEligibilitySubmission": func(b []byte) (string, error) {
				_, err := shnsdk.ParseEligibilitySubmission(b)
				return "", err
			},
		} {
			if _, err := read([]byte(row.body)); err == nil || err.Error() != row.want {
				t.Errorf("%s(%s): %v; want %q", name, row.body, err, row.want)
			}
		}
	}
}

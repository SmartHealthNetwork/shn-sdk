package shnsdk_test

import (
	"errors"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestParseEligibilityRequestMemberExact pins the exact reading of a coverage
// eligibility request: each way a request can be read two ways is refused with
// the text a payer's Smart Gateway answers it with, and a request that reads one
// way only binds the member the lenient reader binds.
func TestParseEligibilityRequestMemberExact(t *testing.T) {
	const (
		cer     = `"resourceType":"CoverageEligibilityRequest"`
		patient = `"patient":{"reference":"Patient/MBR-001"}`
		repeats = "eligibility request repeats "
		twoWays = ": it can be read two ways, so it is not read"
		noBind  = ": member names are case-sensitive, so the request names no patient to bind"
		generic = "eligibility request names its type or patient only in another case" + noBind
		replace = "eligibility request names its patient by a value carrying a replaced character (not well-formed UTF-8 or a lone surrogate escape): readers read it two ways, so the request names no patient to bind"
	)
	member := func(id, value string) string {
		return `{"resourceType":"Patient","id":"` + id + `","identifier":[{"system":"urn:shn:member","value":"` + value + `"}]}`
	}
	nested := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	zeros := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("0,", n), ",") + "]" }
	rows := []struct {
		name, body string
		member     string // the member bound, when the request reads one way
		refusal    string // the refusal text, when it does not
		// resolved is set when the reference names its member in a form the
		// literal reading does not: absolute, versioned or contained.
		resolved bool
	}{
		{name: "reads one way", body: `{` + cer + `,` + patient + `}`, member: "MBR-001"},
		{name: "a member not read named in another case", body: `{` + cer + `,"Status":"active",` + patient + `}`, member: "MBR-001"},
		{name: "contained named in another case beside a literal reference", body: `{` + cer + `,"Contained":[{"resourceType":"Patient","id":"p"}],` + patient + `}`, member: "MBR-001"},
		{name: "U+FFFD sent as an escape", body: `{` + cer + `,"patient":{"reference":"Patient/MBR\ufffd"}}`, member: "MBR\ufffd"},
		{name: "U+FFFD sent as an escape past the bounds", body: `{` + cer + `,"note":"` + "\xff" + `","patient":{"reference":"Patient/MBR\ufffd"}}`, member: "MBR\ufffd"},
		{name: "U+FFFD sent as its bytes past the bounds", body: `{` + cer + `,"note":"\ud800","patient":{"reference":"Patient/MBR` + "\xef\xbf\xbd" + `"}}`, member: "MBR\ufffd"},
		{name: "U+FFFD sent as its bytes", body: `{` + cer + `,"patient":{"reference":"Patient/MBR` + "\xef\xbf\xbd" + `"}}`, member: "MBR\ufffd"},
		{name: "a replaced character in another member", body: `{` + cer + `,"text":{"div":"\ud800"},` + patient + `}`, member: "MBR-001"},
		{name: "a surrogate pair in the reference", body: `{` + cer + `,"patient":{"reference":"Patient/MBR😀"}}`, member: "MBR\U0001F600"},
		{name: "63 nested containers", body: `{` + cer + `,"x":` + nested(63) + `,` + patient + `}`, member: "MBR-001"},

		{name: "an absolute reference", body: `{` + cer + `,"patient":{"reference":"https://provider.example/fhir/Patient/MBR-001"}}`, member: "MBR-001", resolved: true},
		{name: "a versioned reference", body: `{` + cer + `,"patient":{"reference":"Patient/MBR-001/_history/2"}}`, member: "MBR-001", resolved: true},
		{name: "a versioned absolute reference", body: `{` + cer + `,"patient":{"reference":"http://provider.example:8080/fhir/Patient/MBR-001/_history/2"}}`, member: "MBR-001", resolved: true},
		{name: "a contained Patient", body: `{` + cer + `,"contained":[` + member("p", "MBR-X") + `],"patient":{"reference":"#p"}}`, member: "MBR-X", resolved: true},
		{name: "a contained Patient beside others", body: `{` + cer + `,"contained":[{"resourceType":"Organization","id":"o"},"x",` + member("p", "MBR-X") + `],"patient":{"reference":"#p"}}`, member: "MBR-X", resolved: true},
		{name: "a contained Patient with a blank member identifier", body: `{` + cer + `,"contained":[{"resourceType":"Patient","id":"p","identifier":[{"system":"urn:shn:member","value":""},{"system":"urn:mrn","value":"M"},{"system":"urn:shn:member","value":"MBR-X"}]}],"patient":{"reference":"#p"}}`, member: "MBR-X", resolved: true},
		{name: "a contained Patient with a blank member identifier after the member's", body: `{` + cer + `,"contained":[{"resourceType":"Patient","id":"p","identifier":[{"system":"urn:shn:member","value":"MBR-X"},{"system":"urn:shn:member","value":""}]}],"patient":{"reference":"#p"}}`, member: "MBR-X", resolved: true},
		{name: "a contained member identifier of any shape", body: `{` + cer + `,"contained":[` + member("p", "A&date=ge1900-01-01") + `],"patient":{"reference":"#p"}}`, member: "A&date=ge1900-01-01", resolved: true},
		{name: "a contained Patient past the bounds", body: `{` + cer + `,"note":"\ud800","contained":[` + member("p", "MBR-X") + `],"patient":{"reference":"#p"}}`, member: "MBR-X", resolved: true},
		{name: "U+FFFD sent in a contained member identifier past the bounds", body: `{` + cer + `,"note":"\ud800","contained":[` + member("p", `MBR\ufffd`) + `],"patient":{"reference":"#p"}}`, member: "MBR\ufffd", resolved: true},
		{name: "U+FFFD sent in the member identifier beside a replaced character in another contained Patient", body: `{` + cer + `,"contained":[` + member("q", `MBR\ud800`) + `,` + member("p", `MBR\ufffd`) + `],"patient":{"reference":"#p"}}`, member: "MBR\ufffd", resolved: true},
		{name: "U+FFFD sent in the member identifier beside a replaced character in another system's", body: `{` + cer + `,"contained":[{"resourceType":"Patient","id":"p","identifier":[{"system":"urn:mrn","value":"M\ud800"},{"system":"urn:shn:member","value":"MBR\ufffd"}]}],"patient":{"reference":"#p"}}`, member: "MBR\ufffd", resolved: true},
		{name: "a replaced character in another contained Patient", body: `{` + cer + `,"contained":[` + member("q", `MBR\ud800`) + `,` + member("p", "MBR-X") + `],"patient":{"reference":"#p"}}`, member: "MBR-X", resolved: true},
		{name: "a reference with a trailing slash", body: `{` + cer + `,"patient":{"reference":"Patient/MBR-001/"}}`, member: "MBR-001/"},
		{name: "a reference to an operation", body: `{` + cer + `,"patient":{"reference":"Patient/MBR-001/$everything"}}`, member: "MBR-001/$everything"},
		{name: "a bare id", body: `{` + cer + `,"patient":{"reference":"MBR-001"}}`, member: "MBR-001"},
		{name: "an id past 64 characters", body: `{` + cer + `,"patient":{"reference":"Patient/` + strings.Repeat("a", 65) + `"}}`, member: strings.Repeat("a", 65)},

		{name: "patient repeated", body: `{` + cer + `,` + patient + `,"patient":{"reference":"Patient/B"}}`, refusal: repeats + `the JSON member "patient" in the top-level object` + twoWays},
		{name: "reference repeated in another case", body: `{` + cer + `,"patient":{"reference":"Patient/A","Reference":"Patient/B"}}`, refusal: repeats + `the JSON member "Reference" in patient` + twoWays},
		{name: "a repeat in an array element", body: `{` + cer + `,"identifier":[{"value":"a"},{"value":"a","VALUE":"b"}],` + patient + `}`, refusal: repeats + `the JSON member "VALUE" in identifier[1]` + twoWays},
		{name: "a repeat under a name that is not plain", body: `{` + cer + `,"x.y":{"a":1,"a":2},` + patient + `}`, refusal: repeats + `the JSON member "a" in ["x.y"]` + twoWays},
		{name: "a long repeated name", body: `{` + cer + `,` + patient + `,"` + strings.Repeat("a", 70) + `":1,"` + strings.Repeat("A", 70) + `":2}`, refusal: repeats + `the JSON member "` + strings.Repeat("A", 64) + `…" in the top-level object` + twoWays},
		{name: "a long repeated name not ASCII", body: `{` + cer + `,` + patient + `,"` + strings.Repeat("é", 70) + `":1,"` + strings.Repeat("É", 70) + `":2}`, refusal: repeats + `the JSON member "` + strings.Repeat("É", 64) + `…" in the top-level object` + twoWays},
		{name: "a repeat under a long name", body: `{` + cer + `,` + patient + `,"` + strings.Repeat("b", 70) + `":{"a":1,"a":2}}`, refusal: repeats + `the JSON member "a" in ` + strings.Repeat("b", 64) + `…` + twoWays},
		{name: "a repeat under a long name that is not plain", body: `{` + cer + `,` + patient + `,"` + strings.Repeat("é", 70) + `":[{"a":1,"a":2}]}`, refusal: repeats + `the JSON member "a" in ["` + strings.Repeat("é", 64) + `…"][0]` + twoWays},
		{name: "a repeat of the type before the type", body: `{"ResourceType":"Bundle",` + cer + `,` + patient + `}`, refusal: repeats + `the JSON member "resourceType" in the top-level object` + twoWays},
		{name: "a repeat after a byte not UTF-8", body: `{` + cer + `,"note":"` + "\xff" + `",` + patient + `,"Patient":{}}`, refusal: repeats + "a member name" + twoWays},
		{name: "a repeat before a byte not UTF-8", body: `{` + cer + `,` + patient + `,"Patient":{},"note":"` + "\xff" + `"}`, refusal: repeats + `the JSON member "Patient" in the top-level object` + twoWays},
		{name: "a repeat after a lone surrogate", body: `{` + cer + `,"note":"\udc00",` + patient + `,"Patient":{}}`, refusal: repeats + "a member name" + twoWays},
		{name: "a repeat whose own name is not UTF-8", body: `{` + cer + `,"a` + "\xff" + `":1,"a` + "\xfe" + `":2,` + patient + `}`, refusal: repeats + "a member name" + twoWays},
		{name: "a repeat after 64 nested containers", body: `{` + cer + `,"x":` + nested(64) + `,` + patient + `,"Patient":{}}`, refusal: repeats + "a member name" + twoWays},
		{name: "a repeat after 63 nested containers", body: `{` + cer + `,"x":` + nested(63) + `,` + patient + `,"Patient":{}}`, refusal: repeats + `the JSON member "Patient" in the top-level object` + twoWays},
		{name: "a repeat before 64 nested containers", body: `{` + cer + `,` + patient + `,"Patient":{},"x":` + nested(64) + `}`, refusal: repeats + `the JSON member "Patient" in the top-level object` + twoWays},
		{name: "a repeat in a second top-level value", body: `{` + cer + `,` + patient + `} {"a":1,"a":2}`, refusal: repeats + "a member name" + twoWays},
		{name: "a repeat as the millionth token", body: `{` + cer + `,` + patient + `,"x":` + zeros(999_990) + `,"Patient":{}}`, refusal: repeats + `the JSON member "Patient" in the top-level object` + twoWays},
		{name: "a repeat past the millionth token", body: `{` + cer + `,` + patient + `,"x":` + zeros(999_991) + `,"Patient":{}}`, refusal: repeats + "a member name" + twoWays},

		{name: "the type in another case", body: `{"ResourceType":"CoverageEligibilityRequest",` + patient + `}`, refusal: `eligibility request names "ResourceType" for "resourceType"` + noBind},
		{name: "the patient in another case", body: `{` + cer + `,"Patient":{"reference":"Patient/MBR-001"}}`, refusal: `eligibility request names "Patient" for "patient"` + noBind},
		{name: "the reference in another case", body: `{` + cer + `,"patient":{"REFERENCE":"Patient/MBR-001"}}`, refusal: `eligibility request names "REFERENCE" for "reference"` + noBind},
		{name: "contained in another case beside a #id reference", body: `{` + cer + `,"Contained":[{"resourceType":"Patient","id":"p"}],"patient":{"reference":"#p"}}`, refusal: `eligibility request names "Contained" for "contained"` + noBind},
		{name: "the type in another case past the bounds", body: `{"ResourceType":"CoverageEligibilityRequest","note":"` + "\xff" + `",` + patient + `}`, refusal: generic},
		{name: "the patient in another case past the bounds", body: `{` + cer + `,"note":"\ud800","Patient":{"reference":"Patient/MBR-001"}}`, refusal: generic},
		{name: "the reference in another case past the bounds", body: `{` + cer + `,"x":` + nested(64) + `,"patient":{"Reference":"Patient/MBR-001"}}`, refusal: generic},
		{name: "contained in another case past the bounds", body: `{` + cer + `,"note":"\ud800","Contained":[],"patient":{"reference":"#p"}}`, refusal: generic},

		{name: "a lone surrogate in a contained member identifier", body: `{` + cer + `,"contained":[` + member("p", `MBR\ud800`) + `],"patient":{"reference":"#p"}}`, refusal: replace},
		{name: "a byte not UTF-8 in a contained member identifier", body: `{` + cer + `,"contained":[` + member("p", "MBR\xff") + `],"patient":{"reference":"#p"}}`, refusal: replace},
		{name: "a lone surrogate in an absolute reference", body: `{` + cer + `,"patient":{"reference":"https://provider.example/fhir/Patient/MBR\ud800"}}`, refusal: replace},
		{name: "a lone surrogate in the reference", body: `{` + cer + `,"patient":{"reference":"Patient/MBR\ud800"}}`, refusal: replace},
		{name: "a byte not UTF-8 in the reference", body: `{` + cer + `,"patient":{"reference":"Patient/MBR` + "\xff" + `"}}`, refusal: replace},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			member, err := shnsdk.ParseEligibilityRequestMemberExact([]byte(row.body))
			var refusal *shnsdk.EligibilityRequestRefusal
			switch {
			case row.refusal != "":
				if !errors.As(err, &refusal) || err.Error() != row.refusal {
					t.Fatalf("got (%q, %v), want refusal %q", member, err, row.refusal)
				}
			case err != nil:
				t.Fatalf("got error %v, want member %q", err, row.member)
			case member != row.member:
				t.Fatalf("member = %q, want %q", member, row.member)
			case row.resolved:
			default:
				lenient, err := shnsdk.ParseEligibilityRequestMember([]byte(row.body))
				if err != nil || lenient != member {
					t.Fatalf("lenient reading = (%q, %v), exact = %q: a request read one way binds as before", lenient, err, member)
				}
			}
		})
	}
}

// TestParseEligibilityRequestMemberExact_OtherErrorsAreTheLenientReaders pins
// that a request the exact reader does not refuse for reading two ways fails,
// if at all, with the lenient reader's own error, never a refusal.
func TestParseEligibilityRequestMemberExact_OtherErrorsAreTheLenientReaders(t *testing.T) {
	for _, body := range []string{
		`{"resourceType":"Bundle","type":"collection"}`,
		`{"resourceType":"CoverageEligibilityRequest"}`,
		`{"resourceType":"CoverageEligibilityRequest","patient":{"reference":"Patient/A"}`,
		`[{"resourceType":"CoverageEligibilityRequest"}]`,
	} {
		_, err := shnsdk.ParseEligibilityRequestMemberExact([]byte(body))
		_, lenient := shnsdk.ParseEligibilityRequestMember([]byte(body))
		var refusal *shnsdk.EligibilityRequestRefusal
		if err == nil || errors.As(err, &refusal) || lenient == nil || err.Error() != lenient.Error() {
			t.Errorf("%s: exact error %v, lenient error %v: want the lenient reader's error", body, err, lenient)
		}
	}
}

// TestParseEligibilityRequestMemberExact_ContainedNamingNoPatient pins that a
// "#id" reference naming no contained Patient, or more than one, names no
// member: an error that is no refusal, which a Responder answers as it
// answers a request it cannot parse, as a payer's Smart Gateway does.
func TestParseEligibilityRequestMemberExact_ContainedNamingNoPatient(t *testing.T) {
	const cer = `"resourceType":"CoverageEligibilityRequest"`
	member := func(id, value string) string {
		return `{"resourceType":"Patient","id":"` + id + `","identifier":[{"system":"urn:shn:member","value":"` + value + `"}]}`
	}
	for name, body := range map[string]string{
		"no contained resources":                        `{` + cer + `,"patient":{"reference":"#p"}}`,
		"contained resources that are no list":          `{` + cer + `,"contained":{"resourceType":"Patient","id":"p"},"patient":{"reference":"#p"}}`,
		"no contained resource with that id":            `{` + cer + `,"contained":[` + member("q", "MBR-X") + `],"patient":{"reference":"#p"}}`,
		"a contained resource that is no Patient":       `{` + cer + `,"contained":[{"resourceType":"RelatedPerson","id":"p","identifier":[{"system":"urn:shn:member","value":"MBR-X"}]}],"patient":{"reference":"#p"}}`,
		"a contained Patient with no member identifier": `{` + cer + `,"contained":[{"resourceType":"Patient","id":"p","identifier":[{"system":"urn:mrn","value":"MBR-X"}]}],"patient":{"reference":"#p"}}`,
		"a contained Patient naming two members":        `{` + cer + `,"contained":[{"resourceType":"Patient","id":"p","identifier":[{"system":"urn:shn:member","value":"MBR-X"},{"system":"urn:shn:member","value":"MBR-Z"}]}],"patient":{"reference":"#p"}}`,
		"two contained resources with one id":           `{` + cer + `,"contained":[` + member("p", "MBR-X") + `,` + member("p", "MBR-X") + `],"patient":{"reference":"#p"}}`,
		"a contained reference that is not well-formed": `{` + cer + `,"contained":[` + member("p", "MBR-X") + `],"patient":{"reference":"#p/x"}}`,
		"a lone surrogate in a contained reference":     `{` + cer + `,"contained":[` + member("p", "MBR-X") + `],"patient":{"reference":"#p\ud800"}}`,
		"a bare #": `{` + cer + `,"patient":{"reference":"#"}}`,
	} {
		_, err := shnsdk.ParseEligibilityRequestMemberExact([]byte(body))
		var refusal *shnsdk.EligibilityRequestRefusal
		if err == nil || errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want an error that is no refusal", name, err)
		}
	}
}

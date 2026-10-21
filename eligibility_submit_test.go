package shnsdk_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestParseEligibilitySubmission pins the requests each input form of
// CoverageEligibilityRequest/$submit submits, the member each one's patient
// names, and the refusals of an input that cannot be read one way only, with
// the text a payer's Smart Gateway answers it with.
func TestParseEligibilitySubmission(t *testing.T) {
	const (
		noBind  = ": member names are case-sensitive, so the request names no patient to bind"
		twoWays = ": it can be read two ways, so it is not read"
	)
	request := func(ref string) string {
		return `{"resourceType":"CoverageEligibilityRequest","patient":{"reference":"` + ref + `"},"insurer":{"identifier":{"value":"P"}}}`
	}
	req := request("Patient/MBR-001")
	params := func(resource string) string {
		return `{"resourceType":"Parameters","parameter":[{"name":"resource","resource":` + resource + `}]}`
	}
	entry := func(fullURL, resource string) string {
		return `{"fullUrl":"` + fullURL + `","resource":` + resource + `}`
	}
	bundle := func(typ string, entries ...string) string {
		return `{"resourceType":"Bundle","type":"` + typ + `","entry":[` + strings.Join(entries, ",") + `]}`
	}
	batch := func(n int) string {
		var entries []string
		for i := range n {
			entries = append(entries, entry(fmt.Sprintf("urn:uuid:r%d", i), request(fmt.Sprintf("Patient/MBR-%03d", i))))
		}
		return bundle("batch", entries...)
	}
	member := func(id, value string) string {
		return `{"resourceType":"Patient","id":"` + id + `","identifier":[{"system":"urn:shn:member","value":"` + value + `"}]}`
	}
	reads := []struct {
		name, body string
		members    []string
	}{
		{"a request", req, []string{"MBR-001"}},
		{"a Parameters holding a request", params(req), []string{"MBR-001"}},
		{"a Bundle of a request and its Patient", bundle("collection", entry("urn:uuid:r", req), entry("urn:uuid:p", `{"resourceType":"Patient","id":"MBR-001"}`)), []string{"MBR-001"}},
		{"a Bundle whose Patient is named by fullUrl", bundle("collection", entry("urn:uuid:r", request("urn:uuid:p")), entry("urn:uuid:p", `{"resourceType":"Patient","id":"MBR-002"}`)), []string{"MBR-002"}},
		{"a Bundle whose Patient has only a member identifier", bundle("collection", entry("urn:uuid:r", request("urn:uuid:p")), entry("urn:uuid:p", `{"resourceType":"Patient","identifier":[{"system":"urn:shn:member","value":"MBR-003"}]}`)), []string{"MBR-003"}},
		{"a Bundle naming a Patient it does not carry", bundle("collection", entry("urn:uuid:r", request("https://h/fhir/Patient/MBR-004/_history/1"))), []string{"MBR-004"}},
		{"a Bundle whose request names a contained Patient", bundle("collection", entry("urn:uuid:r", `{"resourceType":"CoverageEligibilityRequest","contained":[`+member("p", "MBR-005")+`],"patient":{"reference":"#p"}}`)), []string{"MBR-005"}},
		{"a Parameters holding a Bundle", params(bundle("collection", entry("urn:uuid:r", req))), []string{"MBR-001"}},
		{"a batch", batch(3), []string{"MBR-000", "MBR-001", "MBR-002"}},
		{"a batch of seventeen", batch(17), nil},
		{"a Parameters holding a resource under another name beside the request", `{"resourceType":"Parameters","parameter":[{"name":"other","resource":{"resourceType":"Patient","id":"x"}},{"name":"resource","resource":` + req + `}]}`, []string{"MBR-001"}},
		{"a Parameters whose other parameter spells its members in another case", `{"resourceType":"Parameters","parameter":[{"name":"other","Resource":{"resourceType":"Patient","id":"x"},"ValueString":"y"},{"name":"resource","resource":` + req + `}]}`, []string{"MBR-001"}},
		{"a batch of one Bundle whose request names its Patient by fullUrl", bundle("batch", entry("urn:uuid:b", bundle("collection", entry("urn:uuid:r", request("urn:uuid:p")), entry("urn:uuid:p", `{"resourceType":"Patient","id":"MBR-006"}`)))), []string{"MBR-006"}},
		{"a batch of a request and a Bundle of one", bundle("batch", entry("urn:uuid:r", req), entry("urn:uuid:b", bundle("collection", entry("urn:uuid:r2", request("Patient/MBR-002"))))), []string{"MBR-001", "MBR-002"}},
	}
	for _, row := range reads {
		t.Run(row.name, func(t *testing.T) {
			subs, err := shnsdk.ParseEligibilitySubmission([]byte(row.body))
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if row.members == nil && len(subs) == 17 {
				return
			}
			var got []string
			for _, s := range subs {
				got = append(got, s.Member)
				if len(s.Request) == 0 || !strings.Contains(string(s.Request), `"CoverageEligibilityRequest"`) {
					t.Fatalf("submission carries no request: %s", s.Request)
				}
			}
			if strings.Join(got, ",") != strings.Join(row.members, ",") {
				t.Fatalf("members %q, want %q", got, row.members)
			}
		})
	}

	refusals := []struct{ name, body, text string }{
		{"a repeat in a Bundle's request", bundle("collection", entry("urn:uuid:r", strings.TrimSuffix(req, "}")+`,"patient":{}}`)), `eligibility request repeats the JSON member "patient" in entry[0].resource` + twoWays},
		{"the Parameters' parameter in another case", strings.Replace(params(req), `"parameter"`, `"Parameter"`, 1), `eligibility request names "Parameter" for "parameter"` + noBind},
		{"the Parameters' name in another case", strings.Replace(params(req), `"name"`, `"NAME"`, 1), `eligibility request names "NAME" for "name"` + noBind},
		{"the Parameters' resource in another case", strings.Replace(params(req), `"resource":{`, `"Resource":{`, 1), `eligibility request names "Resource" for "resource"` + noBind},
		{"another parameter's name in another case", `{"resourceType":"Parameters","parameter":[{"Name":"other","valueString":"y"},{"name":"resource","resource":` + req + `}]}`, `eligibility request names "Name" for "name"` + noBind},
		{"the type in another case", strings.Replace(params(req), `"resourceType":"Parameters"`, `"ResourceType":"Parameters"`, 1), `eligibility request names "ResourceType" for "resourceType"` + noBind},
		{"a Bundle's request's patient in another case", bundle("collection", entry("urn:uuid:r", strings.Replace(req, `"patient"`, `"Patient"`, 1))), `eligibility request names "Patient" for "patient"` + noBind},
		{"a Bundle's request's reference in another case", bundle("collection", entry("urn:uuid:r", strings.Replace(req, `"reference"`, `"Reference"`, 1))), `eligibility request names "Reference" for "reference"` + noBind},
		{"a Bundle's request's contained in another case", bundle("collection", entry("urn:uuid:r", `{"resourceType":"CoverageEligibilityRequest","Contained":[`+member("p", "MBR-005")+`],"patient":{"reference":"#p"}}`)), `eligibility request names "Contained" for "contained"` + noBind},
	}
	for _, row := range refusals {
		t.Run(row.name, func(t *testing.T) {
			_, err := shnsdk.ParseEligibilitySubmission([]byte(row.body))
			var refusal *shnsdk.EligibilityRequestRefusal
			if !errors.As(err, &refusal) || err.Error() != row.text {
				t.Fatalf("got %v, want refusal %q", err, row.text)
			}
		})
	}

	t.Run("a batch of eighteen", func(t *testing.T) {
		// Refused before any request's patient is read: the eighteenth names none.
		body := strings.TrimSuffix(batch(17), "]}") + "," + entry("urn:uuid:bad", request("")) + "]}"
		_, err := shnsdk.ParseEligibilitySubmission([]byte(body))
		var tooLarge *shnsdk.EligibilityBatchTooLarge
		if !errors.As(err, &tooLarge) || tooLarge.Requests != 18 || err.Error() != "the batch carries 18 coverage eligibility requests; a gateway reads at most 17 in one batch, so send them in batches of at most 17" {
			t.Fatalf("got %v, want the batch refused as too large", err)
		}
	})

	for name, body := range map[string]string{
		"a Patient":                                                   `{"resourceType":"Patient","id":"x"}`,
		"a Parameters holding two resources":                          `{"resourceType":"Parameters","parameter":[{"name":"resource","resource":` + req + `},{"name":"resource","resource":` + req + `}]}`,
		"a Parameters holding no resource":                            `{"resourceType":"Parameters","parameter":[{"name":"other","valueString":"x"}]}`,
		"a Parameters holding a null resource":                        `{"resourceType":"Parameters","parameter":[{"name":"resource","resource":null}]}`,
		"an empty resource parameter before one holding the request":  `{"resourceType":"Parameters","parameter":[{"name":"resource","valueString":"x"},{"name":"resource","resource":` + req + `}]}`,
		"a Parameters holding a Patient":                              params(`{"resourceType":"Patient","id":"x"}`),
		"a Bundle entry's resource in another case":                   strings.Replace(bundle("collection", entry("urn:uuid:r", req)), `"resource":`, `"Resource":`, 1),
		"a Bundle entry with no resource":                             bundle("collection", `{"fullUrl":"urn:uuid:x"}`),
		"a Bundle entry with a null resource":                         bundle("collection", `{"fullUrl":"urn:uuid:x","resource":null}`),
		"a Bundle entry with no type":                                 bundle("collection", entry("urn:uuid:r", req), entry("urn:uuid:x", `{"id":"x"}`)),
		"a Bundle of two requests and a Patient":                      bundle("collection", entry("urn:uuid:r1", req), entry("urn:uuid:r2", req), entry("urn:uuid:p", `{"resourceType":"Patient","id":"MBR-001"}`)),
		"a Bundle of two requests beside a resource carrying entries": bundle("collection", entry("urn:uuid:r1", req), entry("urn:uuid:r2", req), entry("urn:uuid:x", `{"resourceType":"Basic","entry":[{"resource":`+req+`}]}`)),
		"a Bundle holding no request":                                 bundle("collection", entry("urn:uuid:p", `{"resourceType":"Patient","id":"MBR-001"}`)),
		"a batch entry that is a Bundle of two":                       bundle("batch", entry("urn:uuid:b", bundle("collection", entry("urn:uuid:r1", req), entry("urn:uuid:r2", req)))),
		"a batch entry that is a Bundle of a Bundle":                  bundle("batch", entry("urn:uuid:b", bundle("collection", entry("urn:uuid:r1", req), entry("urn:uuid:b2", bundle("collection"))))),
		"a Bundle's request naming no patient":                        bundle("collection", entry("urn:uuid:r", request(""))),
		"a Bundle's request naming an Organization":                   bundle("collection", entry("urn:uuid:r", request("urn:uuid:o")), entry("urn:uuid:o", `{"resourceType":"Organization","id":"o"}`)),
		"a Bundle's request naming two entries":                       bundle("collection", entry("urn:uuid:r", req), entry("urn:uuid:p1", `{"resourceType":"Patient","id":"MBR-001"}`), entry("urn:uuid:p2", `{"resourceType":"Patient","id":"MBR-001"}`)),
		"a Bundle's Patient whose id is not the reference's":          bundle("collection", entry("urn:uuid:r", request("https://h/fhir/Patient/MBR-001")), entry("https://h/fhir/Patient/MBR-001", `{"resourceType":"Patient","id":"MBR-002"}`)),
		"a Bundle's Patient with no id or member":                     bundle("collection", entry("urn:uuid:r", request("urn:uuid:p")), entry("urn:uuid:p", `{"resourceType":"Patient"}`)),
		"a Bundle's request naming no contained Patient":              bundle("collection", entry("urn:uuid:r", request("#p"))),
		"a Bundle's request naming a reference no reader resolves":    bundle("collection", entry("urn:uuid:r", request("Patient/MBR-001/$everything"))),
		"a Bundle past the strict reader's bounds":                    strings.TrimSuffix(bundle("collection", entry("urn:uuid:r", req)), "}") + `,"note":"\ud800"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := shnsdk.ParseEligibilitySubmission([]byte(body))
			var refusal *shnsdk.EligibilityRequestRefusal
			var tooLarge *shnsdk.EligibilityBatchTooLarge
			if err == nil || errors.As(err, &refusal) || errors.As(err, &tooLarge) {
				t.Fatalf("got %v, want an error that is no refusal", err)
			}
		})
	}
}

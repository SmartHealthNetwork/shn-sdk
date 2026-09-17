package shnsdk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// bs is a backslash, built at runtime so escape sequences in the fixtures
// below stay exactly as written.
const bs = "\x5c"

// pkgCoverage is a Coverage with number lexemes, escapes, unknown members and
// uneven whitespace that a re-encoder would change.
func pkgCoverage(id, patient string) []byte {
	return []byte(" \r\n\t{\"resourceType\" : \"Coverage\",\r\n  \"id\":\"" + id + "\",\"status\":\"active\"," +
		"\"extension\":[{\"url\":\"http://example.org/x\",\"extension\":[{\"url\":\"a\",\"valueDecimal\":1.50}," +
		"{\"url\":\"b\",\"valueDecimal\":9007199254740993},{\"url\":\"c\",\"valueDecimal\":1e2}]}]," +
		"\"subscriberId\":\"caf" + bs + "u00e9 " + bs + "/ <&>\",\"beneficiary\":{\"reference\":\"" + patient + "\"}," +
		"\"payor\":[{\"reference\":\"#org\"}],\"_unknown\":{\"x\":[1,2.0,-0.0]}}\n ")
}

func pkgOrder(id, patient string) []byte {
	return []byte("{ \"resourceType\":\"ServiceRequest\", \"id\":\"" + id + "\",\t\"status\":\"draft\",\"intent\":\"order\"," +
		"\"quantityQuantity\":{\"value\":1.50},\"subject\":{\"reference\":\"" + patient + "\"}," +
		"\"note\":[{\"text\":\"line1" + bs + "nline2 " + bs + "ud83d" + bs + "ude00\"}] }")
}

type pkgParam struct {
	Name           string          `json:"name"`
	Resource       json.RawMessage `json:"resource"`
	ValueCanonical *string         `json:"valueCanonical"`
	ValueString    *string         `json:"valueString"`
}

func decodePkg(t *testing.T, body []byte) (profile []string, params []pkgParam) {
	t.Helper()
	var p struct {
		ResourceType string `json:"resourceType"`
		Meta         struct {
			Profile []string `json:"profile"`
		} `json:"meta"`
		Parameter []pkgParam `json:"parameter"`
	}
	if !json.Valid(body) {
		t.Fatalf("output is not JSON: %s", body)
	}
	if err := json.Unmarshal(body, &p); err != nil || p.ResourceType != "Parameters" {
		t.Fatalf("output is not a Parameters resource (%v): %s", err, body)
	}
	return p.Meta.Profile, p.Parameter
}

// TestBuildQuestionnairePackageParameters_EmbedsExact: every Coverage and
// order is embedded as the caller's own bytes (only surrounding whitespace is
// left out), every copied span is reported, and each reported span is exact
// in both the input and the output.
func TestBuildQuestionnairePackageParameters_EmbedsExact(t *testing.T) {
	for _, tc := range []struct {
		line      string
		coverages [][]byte
	}{
		{"2.0", [][]byte{pkgCoverage("c1", "Patient/p1"), pkgCoverage("c2", "Patient/p1")}},
		{"2.1", [][]byte{pkgCoverage("c1", "http://ehr.example/fhir/Patient/p1"), pkgCoverage("c2", "Patient/p1")}},
		{"2.2", [][]byte{pkgCoverage("c1", "Patient/p1")}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			orders := [][]byte{pkgOrder("o1", "Patient/p1"), pkgOrder("o2", "Patient/p1")}
			in := QuestionnairePackageInputs{
				Coverages:      tc.coverages,
				Orders:         orders,
				Questionnaires: []string{"http://payer.example/Questionnaire/q|2.1"},
				Context:        "assertion-1",
			}
			got, err := BuildQuestionnairePackageParameters(tc.line, in)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			profile, params := decodePkg(t, got.Body)
			if len(profile) != 1 || profile[0] != QuestionnairePackageInputProfile {
				t.Fatalf("meta.profile = %q", profile)
			}
			wantNames := []string{}
			var wantResources [][]byte
			for _, c := range tc.coverages {
				wantNames = append(wantNames, "coverage")
				wantResources = append(wantResources, bytes.TrimSpace(c))
			}
			for _, o := range orders {
				wantNames = append(wantNames, "order")
				wantResources = append(wantResources, bytes.TrimSpace(o))
			}
			wantNames = append(wantNames, "questionnaire", "context")
			if len(params) != len(wantNames) {
				t.Fatalf("parameters = %d, want %d: %s", len(params), len(wantNames), got.Body)
			}
			for i, p := range params {
				if p.Name != wantNames[i] {
					t.Fatalf("parameter %d is %q, want %q", i, p.Name, wantNames[i])
				}
				if i < len(wantResources) && !bytes.Equal(p.Resource, wantResources[i]) {
					t.Fatalf("parameter %d resource changed:\n got %s\nwant %s", i, p.Resource, wantResources[i])
				}
			}

			if len(got.Copied) != len(wantResources) {
				t.Fatalf("copied spans = %d, want %d", len(got.Copied), len(wantResources))
			}
			seen := map[string]bool{}
			for _, s := range got.Copied {
				var src []byte
				switch s.Parameter {
				case "coverage":
					src = tc.coverages[s.Index]
				case "order":
					src = orders[s.Index]
				default:
					t.Fatalf("span names parameter %q", s.Parameter)
				}
				if s.Start < 0 || s.End > len(src) || s.Start >= s.End || s.At < 0 || s.At+(s.End-s.Start) > len(got.Body) {
					t.Fatalf("span out of range: %+v", s)
				}
				if !bytes.Equal(src[s.Start:s.End], bytes.TrimSpace(src)) {
					t.Fatalf("span %+v is not the whole input value", s)
				}
				if !bytes.Equal(got.Body[s.At:s.At+(s.End-s.Start)], src[s.Start:s.End]) {
					t.Fatalf("span %+v is not a copy in the output", s)
				}
				// The destination is exactly the parameter's resource value.
				if !json.Valid(got.Body[s.At : s.At+(s.End-s.Start)]) {
					t.Fatalf("span %+v destination is not one JSON value", s)
				}
				seen[s.Parameter+string(rune('0'+s.Index))] = true
			}
			if len(seen) != len(wantResources) {
				t.Fatalf("spans repeat an input: %+v", got.Copied)
			}
			// The inputs are not modified.
			if !bytes.Equal(orders[0], pkgOrder("o1", "Patient/p1")) {
				t.Fatal("the caller's order bytes were modified")
			}
			if !strings.Contains(string(got.Body), "9007199254740993") || !strings.Contains(string(got.Body), bs+"ud83d"+bs+"ude00") {
				t.Fatal("lexemes did not survive")
			}
		})
	}
}

// TestBuildQuestionnairePackageParameters_VersionedCanonical: questionnaire
// canonicals are written exactly as given, a |version included, in order and
// without HTML escaping.
func TestBuildQuestionnairePackageParameters_VersionedCanonical(t *testing.T) {
	canonicals := []string{
		"http://payer.example/fhir/Questionnaire/home-o2|1.0.0",
		"http://payer.example/fhir/Questionnaire/plain",
		"http://payer.example/fhir/Questionnaire/q?a=1&b=<2>|2026-01",
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		got, err := BuildQuestionnairePackageParameters(line, QuestionnairePackageInputs{
			Coverages:      [][]byte{pkgCoverage("c1", "Patient/p1")},
			Questionnaires: canonicals,
		})
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		_, params := decodePkg(t, got.Body)
		var sent []string
		for _, p := range params {
			if p.Name == "questionnaire" {
				if p.ValueCanonical == nil {
					t.Fatalf("%s: questionnaire parameter without valueCanonical", line)
				}
				sent = append(sent, *p.ValueCanonical)
			}
		}
		if strings.Join(sent, "\n") != strings.Join(canonicals, "\n") {
			t.Fatalf("%s: canonicals = %q, want %q", line, sent, canonicals)
		}
		if !bytes.Contains(got.Body, []byte(`"valueCanonical":"http://payer.example/fhir/Questionnaire/q?a=1&b=<2>|2026-01"`)) {
			t.Fatalf("%s: canonical not written literally: %s", line, got.Body)
		}
	}
}

// TestBuildQuestionnairePackageParameters_ContextWhenHeld: at every line, the
// context parameter is present exactly when the flow holds an id, carries it
// unchanged, and is absent otherwise. A context alone satisfies the input
// profile's requirement for an order, questionnaire or context.
func TestBuildQuestionnairePackageParameters_ContextWhenHeld(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		t.Run(line, func(t *testing.T) {
			base := QuestionnairePackageInputs{
				Coverages: [][]byte{pkgCoverage("c1", "Patient/p1")},
				Orders:    [][]byte{pkgOrder("o1", "Patient/p1")},
			}
			without, err := BuildQuestionnairePackageParameters(line, base)
			if err != nil {
				t.Fatal(err)
			}
			if _, params := decodePkg(t, without.Body); len(params) != 2 || bytes.Contains(without.Body, []byte(`"context"`)) {
				t.Fatalf("context written without one held: %s", without.Body)
			}

			held := base
			held.Context = "crd-assertion \"7\" é"
			with, err := BuildQuestionnairePackageParameters(line, held)
			if err != nil {
				t.Fatal(err)
			}
			_, params := decodePkg(t, with.Body)
			var ctx []string
			for _, p := range params {
				if p.Name == "context" {
					if p.ValueString == nil {
						t.Fatalf("context without valueString: %s", with.Body)
					}
					ctx = append(ctx, *p.ValueString)
				}
			}
			if len(ctx) != 1 || ctx[0] != held.Context {
				t.Fatalf("context = %q, want [%q]", ctx, held.Context)
			}
			// Adding the context changes nothing else.
			if !bytes.HasPrefix(with.Body, without.Body[:len(without.Body)-2]) {
				t.Fatalf("context changed the rest of the request:\n%s\n%s", without.Body, with.Body)
			}

			only, err := BuildQuestionnairePackageParameters(line, QuestionnairePackageInputs{
				Coverages: base.Coverages, Context: "a-1",
			})
			if err != nil {
				t.Fatalf("context-only request refused: %v", err)
			}
			if _, params := decodePkg(t, only.Body); len(params) != 2 || params[1].Name != "context" {
				t.Fatalf("context-only request: %s", only.Body)
			}
		})
	}
}

// TestBuildQuestionnairePackageParameters_Refusals: each input the operation
// or its profile does not allow is an error, never a changed request.
func TestBuildQuestionnairePackageParameters_Refusals(t *testing.T) {
	cov := pkgCoverage("c1", "Patient/p1")
	ord := pkgOrder("o1", "Patient/p1")
	q := []string{"http://payer.example/Questionnaire/q"}
	ok := func() QuestionnairePackageInputs {
		return QuestionnairePackageInputs{Coverages: [][]byte{cov}, Orders: [][]byte{ord}, Questionnaires: q}
	}
	if _, err := BuildQuestionnairePackageParameters("2.0", ok()); err != nil {
		t.Fatalf("control: %v", err)
	}
	cases := []struct {
		name string
		line string
		in   func() QuestionnairePackageInputs
		want string
	}{
		{"unknown line", "2.3", ok, "unknown DTR line"},
		{"no coverage 2.0", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Coverages = nil; return i }, "at least one coverage"},
		{"no coverage 2.2", "2.2", func() QuestionnairePackageInputs { i := ok(); i.Coverages = nil; return i }, "exactly one coverage"},
		{"two coverages 2.2", "2.2", func() QuestionnairePackageInputs {
			i := ok()
			i.Coverages = [][]byte{cov, pkgCoverage("c2", "Patient/p1")}
			return i
		}, "exactly one coverage"},
		{"empty coverage", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Coverages = [][]byte{nil}; return i }, "coverage 0"},
		{"coverage not a Coverage", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Coverages = [][]byte{ord}; return i }, "not a Coverage"},
		{"coverage array", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Coverages = [][]byte{[]byte(`[{}]`)}; return i }, "not a JSON object"},
		{"coverage duplicate member", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Coverages = [][]byte{[]byte(`{"resourceType":"Coverage","id":"a","id":"b","beneficiary":{"reference":"Patient/p1"}}`)}
			return i
		}, "coverage 0"},
		{"coverage trailing document", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Coverages = [][]byte{append(bytes.Clone(cov), []byte(`{}`)...)}
			return i
		}, "coverage 0"},
		{"coverage without beneficiary", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Coverages = [][]byte{[]byte(`{"resourceType":"Coverage","id":"a"}`)}
			return i
		}, "beneficiary"},
		{"coverages for two patients", "2.1", func() QuestionnairePackageInputs {
			i := ok()
			i.Coverages = [][]byte{cov, pkgCoverage("c2", "Patient/p2")}
			return i
		}, "another patient"},
		{"order for another patient", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Orders = [][]byte{ord, pkgOrder("o2", "Patient/p2")}
			return i
		}, "another patient"},
		{"order subject not a Patient", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Orders = [][]byte{pkgOrder("o2", "Group/g1")}
			return i
		}, `order 0 subject "Group/g1" is not a Patient reference`},
		{"beneficiary not a Patient", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Coverages = [][]byte{pkgCoverage("c2", "Group/g1")}
			return i
		}, `coverage 0 beneficiary "Group/g1" is not a Patient reference`},
		{"order not an order", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Orders = [][]byte{cov}; return i }, "not an order type"},
		{"immunization recommendation after 2.0", "2.1", func() QuestionnairePackageInputs {
			i := ok()
			i.Orders = [][]byte{[]byte(`{"resourceType":"ImmunizationRecommendation","patient":{"reference":"Patient/p1"}}`)}
			return i
		}, "not an order type"},
		{"empty order", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Orders = [][]byte{[]byte(" ")}; return i }, "order 0"},
		{"empty canonical", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Questionnaires = []string{""}; return i }, "questionnaire 0"},
		{"canonical with space", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Questionnaires = []string{"http://payer.example/Questionnaire/q |1"}
			return i
		}, "questionnaire 0"},
		{"canonical not UTF-8", "2.0", func() QuestionnairePackageInputs {
			i := ok()
			i.Questionnaires = []string{"http://payer.example/Questionnaire/\xff"}
			return i
		}, "questionnaire 0"},
		{"context not UTF-8", "2.0", func() QuestionnairePackageInputs { i := ok(); i.Context = "a\xffb"; return i }, "context is not valid UTF-8"},
		{"nothing to fetch", "2.0", func() QuestionnairePackageInputs { return QuestionnairePackageInputs{Coverages: [][]byte{cov}} }, "order, questionnaire or context"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BuildQuestionnairePackageParameters(c.line, c.in())
			if err == nil {
				t.Fatalf("accepted: %s", got.Body)
			}
			if !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "shnsdk: BuildQuestionnairePackageParameters: ") {
				t.Fatalf("error %q, want it to name %q", err, c.want)
			}
			if got.Body != nil || got.Copied != nil {
				t.Fatal("a refusal returned a request")
			}
		})
	}
	// The 2.0 line allows an ImmunizationRecommendation order.
	if _, err := BuildQuestionnairePackageParameters("2.0", QuestionnairePackageInputs{
		Coverages: [][]byte{cov},
		Orders:    [][]byte{[]byte(`{"resourceType":"ImmunizationRecommendation","patient":{"reference":"Patient/p1"}}`)},
	}); err != nil {
		t.Fatalf("2.0 ImmunizationRecommendation refused: %v", err)
	}
}

// TestBuildQuestionnaireFetch_DeprecatedOutputUnchanged pins the deprecated
// envelope builders' bytes.
func TestBuildQuestionnaireFetch_DeprecatedOutputUnchanged(t *testing.T) {
	got, err := BuildQuestionnaireFetch("http://q|1")
	if err != nil || string(got) != `{"canonical":"http://q|1"}` {
		t.Fatalf("BuildQuestionnaireFetch = %s, %v", got, err)
	}
	got, err = BuildQuestionnaireFetchWithCoverage("http://q", []byte(`{"resourceType":"Coverage","id":"c"}`))
	if err != nil || string(got) != `{"canonical":"http://q","coverage":{"resourceType":"Coverage","id":"c"}}` {
		t.Fatalf("BuildQuestionnaireFetchWithCoverage = %s, %v", got, err)
	}
}

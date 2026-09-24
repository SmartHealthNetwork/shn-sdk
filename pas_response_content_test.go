package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func contentResponse(outcome, code, number string) string {
	return fmt.Sprintf(`{"resourceType":"ClaimResponse","outcome":%q,"item":[{"adjudication":[{"extension":[{"url":%q,"extension":[{"url":%q,"valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":%q}]}},{"url":"number","valueString":%q}]}]}]}]}`, outcome, reviewActionExtURL, reviewActionCodeExtURL, code, number)
}
func contentBundle(resources ...string) []byte {
	s := `{"resourceType":"Bundle","entry":[`
	for i, r := range resources {
		if i > 0 {
			s += ","
		}
		s += `{"resource":` + r + `}`
	}
	return []byte(s + `]}`)
}
func TestPASResponseContent(t *testing.T) {
	task := `{"resourceType":"Task","input":[{"type":{"coding":[{"code":"payer-url"}]},"valueString":"https://payer.example"},{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes","code":"questionnaires-needed","display":"Questionnaires Needed"}]},"valueIdentifier":{"value":"https://payer.example/Questionnaire/oxygen"}}]}`
	for _, tc := range []struct {
		name, outcome, code, number string
		pending                     bool
		result                      string
	}{
		{"approved retained task", "complete", "A1", "AUTH", false, "approved"},
		{"denied retained task", "complete", "A3", "", false, "denied"},
		{"observed denial", "complete", "A2", "", false, "denied"},
		{"partial", "complete", "A2", "AUTH", false, "approved"},
		{"queued", "queued", "", "", true, ""},
		{"complete pended", "complete", "A4", "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := contentResponse(tc.outcome, tc.code, tc.number)
			for _, data := range [][]byte{[]byte(cr), contentBundle(cr), contentBundle(cr, task)} {
				p, items, err := ParsePendedResponse(data)
				if err != nil || p != tc.pending {
					t.Fatalf("pending=%v error=%v", p, err)
				}
				if !p && len(items) != 0 {
					t.Fatal("terminal response returned needed items")
				}
				r, err := ParseClaimResponse(data)
				if tc.pending {
					if err == nil {
						t.Fatal("pending parsed as terminal")
					}
				} else if err != nil || r.Outcome != tc.result {
					t.Fatalf("result=%+v error=%v", r, err)
				}
			}
		})
	}
	p, items, err := ParsePendedResponse(contentBundle(contentResponse("complete", "A4", ""), task))
	if err != nil || !p || len(items) != 1 || items[0].Code != "https://payer.example/Questionnaire/oxygen" || items[0].Display != "Questionnaires Needed" {
		t.Fatalf("needed=%+v pending=%v error=%v", items, p, err)
	}
}
func TestPASResponseRejectsAmbiguity(t *testing.T) {
	cr := contentResponse("complete", "A1", "AUTH")
	for name, data := range map[string][]byte{
		"empty": contentBundle(), "task only": contentBundle(`{"resourceType":"Task"}`), "two responses": contentBundle(cr, cr),
		"missing resource": []byte(`{"resourceType":"Bundle","entry":[{}]}`), "null resource": contentBundle("null"), "resource array": contentBundle("[]"), "missing type": contentBundle(`{}`), "null entry": []byte(`{"resourceType":"Bundle","entry":[null]}`), "wrong type": []byte(`{"resourceType":"Patient","outcome":"complete","preAuthRef":"AUTH"}`),
		"queued approval": []byte(contentResponse("queued", "A1", "AUTH")), "pended number": []byte(contentResponse("complete", "A4", "AUTH")),
		"ambiguous terminal": []byte(`{"resourceType":"ClaimResponse","outcome":"complete"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParsePendedResponse(data); err == nil {
				t.Error("pending parser accepted")
			}
			if _, err := ParseClaimResponse(data); err == nil {
				t.Error("terminal parser accepted")
			}
		})
	}
}

func TestPASResponseConflictingReviewActions(t *testing.T) {
	for _, codes := range [][2]string{{"A1", "A4"}, {"A2", "A4"}, {"A3", "A4"}, {"A1", "A2"}, {"A1", "A3"}, {"A2", "A3"}} {
		t.Run(codes[0]+codes[1], func(t *testing.T) {
			first := contentResponse("complete", codes[0], "")
			second := contentResponse("complete", codes[1], "")
			var a, b map[string]json.RawMessage
			if err := json.Unmarshal([]byte(first), &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(second), &b); err != nil {
				t.Fatal(err)
			}
			a["item"] = append(append(a["item"][:len(a["item"])-1], ','), b["item"][1:]...)
			data, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := ParsePendedResponse(data); err == nil {
				t.Fatal("pending accepted contradictory decisions")
			}
			if _, err := ParseClaimResponse(data); err == nil {
				t.Fatal("terminal accepted contradictory decisions")
			}
		})
	}
}

func TestPASPendingRequiresValidOutcomeAndCoding(t *testing.T) {
	for name, data := range map[string]string{
		"error outcome":         contentResponse("error", "A4", ""),
		"invented outcome":      contentResponse("invented", "A4", ""),
		"missing outcome":       contentResponse("", "A4", ""),
		"partial outcome":       contentResponse("partial", "A4", ""),
		"wrong coding system":   strings.ReplaceAll(contentResponse("complete", "A4", ""), "https://codesystem.x12.org/005010/306", "https://unrelated.example/codes"),
		"missing coding system": strings.ReplaceAll(contentResponse("complete", "A4", ""), `"system":"https://codesystem.x12.org/005010/306",`, ""),
	} {
		t.Run(name, func(t *testing.T) {
			for _, response := range [][]byte{[]byte(data), contentBundle(data)} {
				if _, _, err := ParsePendedResponse(response); err == nil {
					t.Error("pending parser accepted invalid pending content")
				}
				if _, err := ParseClaimResponse(response); err == nil {
					t.Error("terminal parser accepted invalid pending content")
				}
			}
		})
	}
}

func sequencedResponse(outcome string, codes, numbers []string, sequences []int) string {
	var response map[string]json.RawMessage
	var items []map[string]json.RawMessage
	for i, code := range codes {
		_ = json.Unmarshal([]byte(contentResponse(outcome, code, numbers[i])), &response)
		var one []map[string]json.RawMessage
		_ = json.Unmarshal(response["item"], &one)
		one[0]["itemSequence"] = json.RawMessage(fmt.Sprint(sequences[i]))
		items = append(items, one[0])
	}
	response["item"], _ = json.Marshal(items)
	data, _ := json.Marshal(response)
	return string(data)
}

func TestPASMixedItemDecisions(t *testing.T) {
	for _, terminal := range []string{"A1", "A2", "A3"} {
		for _, outcome := range []string{"complete", "queued"} {
			t.Run(outcome+terminal, func(t *testing.T) {
				number := ""
				if terminal == "A1" {
					number = "AUTH-2"
				}
				cr := sequencedResponse(outcome, []string{"A4", terminal}, []string{"", number}, []int{1, 2})
				for _, data := range [][]byte{[]byte(cr), contentBundle(cr)} {
					p, _, err := ParsePendedResponse(data)
					if err != nil || !p {
						t.Fatalf("mixed pending decision: %v %v", p, err)
					}
					if _, err := ParseClaimResponse(data); err == nil {
						t.Fatal("mixed pending accepted as terminal")
					}
				}
			})
		}
	}
	for _, seqs := range [][]int{{1, 1}, {0, 0}} {
		cr := sequencedResponse("complete", []string{"A4", "A1"}, []string{"", "AUTH"}, seqs)
		if _, _, err := ParsePendedResponse([]byte(cr)); err == nil {
			t.Fatal("same-item contradiction accepted")
		}
	}
	cr := sequencedResponse("complete", []string{"A1", "A1"}, []string{"AUTH-1", "AUTH-2"}, []int{1, 2})
	if _, err := ParseClaimResponse([]byte(cr)); err == nil {
		t.Fatal("ambiguous scalar authorization accepted")
	}
}

func TestPASMixedTerminalItemsPreserveAggregation(t *testing.T) {
	for _, tc := range []struct {
		codes   []string
		outcome string
		partial bool
	}{
		{[]string{"A1", "A2"}, "approved", true},
		{[]string{"A1", "A3"}, "denied", false},
	} {
		cr := sequencedResponse("complete", tc.codes, []string{"AUTH", ""}, []int{1, 2})
		for _, data := range [][]byte{[]byte(cr), contentBundle(cr)} {
			p, _, err := ParsePendedResponse(data)
			if err != nil || p {
				t.Fatalf("terminal decision: %v %v", p, err)
			}
			result, err := ParseClaimResponse(data)
			if err != nil || result.Outcome != tc.outcome || result.Partial != tc.partial {
				t.Fatalf("aggregate decision: %+v %v", result, err)
			}
		}
	}
}

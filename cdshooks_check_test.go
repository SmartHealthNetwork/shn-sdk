package shnsdk

import (
	"slices"
	"strings"
	"testing"
)

// cdsCard is a conformant card used as the base of the certifier rows.
const cdsCard = `{"uuid":"card-1","summary":"Prior authorization required","indicator":"warning",` +
	`"source":{"label":"Example Health Plan","topic":{"system":"http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp","code":"coverage-info"}}`

// cdsResp wraps the given card members (appended to the base card) in a
// response with one card.
func cdsResp(extraCardMembers string) string {
	return `{"cards":[` + cdsCard + extraCardMembers + `}]}`
}

const cdsUpdateAction = `{"type":"update","description":"Add coverage information","resource":{"resourceType":"ServiceRequest","id":"sr1"}}`

type certifierRow struct {
	rule     string
	line     string
	positive string // no violation at all
	negative string // exactly this rule's violation
	path     string // the negative row's reported path
}

func certifierRows() []certifierRow {
	return []certifierRow{
		{"response.json", "2.0", `{"cards":[]}`, `{"cards":[],"cards":[]}`, ""},
		{"response.object", "2.0", `{"cards":[]}`, `[]`, ""},
		{"response.cards", "2.0", `{"cards":[]}`, `{"systemActions":[]}`, "cards"},
		{"response.systemActions", "2.0", `{"cards":[],"systemActions":[` + cdsUpdateAction + `]}`, `{"cards":[],"systemActions":{}}`, "systemActions"},
		{"card.object", "2.0", cdsResp(""), `{"cards":["not a card"]}`, "cards[0]"},
		{"card.uuid", "2.0", cdsResp(""), `{"cards":[{"uuid":7,` + strings.TrimPrefix(cdsCard, `{"uuid":"card-1",`) + `}]}`, "cards[0].uuid"},
		{"card.summary", "2.0", cdsResp(""), strings.Replace(cdsResp(""), `"summary":"Prior authorization required"`, `"summary":""`, 1), "cards[0].summary"},
		{"card.summary.length", "2.0",
			strings.Replace(cdsResp(""), `Prior authorization required`, strings.Repeat("é", 139), 1),
			strings.Replace(cdsResp(""), `Prior authorization required`, strings.Repeat("é", 140), 1),
			"cards[0].summary"},
		{"card.detail", "2.0", cdsResp(`,"detail":"**markdown**"`), cdsResp(`,"detail":["x"]`), "cards[0].detail"},
		{"card.indicator", "2.0", cdsResp(""), strings.Replace(cdsResp(""), `"indicator":"warning"`, `"indicator":"hard-stop"`, 1), "cards[0].indicator"},
		{"card.source", "2.0", cdsResp(""), `{"cards":[{"summary":"s","indicator":"info"}]}`, "cards[0].source"},
		{"card.source.label", "2.0", cdsResp(""), strings.Replace(cdsResp(""), `"label":"Example Health Plan",`, ``, 1), "cards[0].source.label"},
		{"card.source.topic", "2.2", cdsResp(""), `{"cards":[{"summary":"s","indicator":"info","source":{"label":"Example Health Plan"}}]}`, "cards[0].source.topic"},
		{"card.suggestions", "2.0",
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Add coverage information","actions":[` + cdsUpdateAction + `]}]`),
			cdsResp(`,"selectionBehavior":"any","suggestions":{}`), "cards[0].suggestions"},
		{"suggestion.label", "2.0",
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order"}]`),
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"uuid":"s1"}]`), "cards[0].suggestions[0].label"},
		{"suggestion.uuid", "2.0",
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order","uuid":"s1"}]`),
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order","uuid":1}]`), "cards[0].suggestions[0].uuid"},
		{"suggestion.isRecommended", "2.0",
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order","isRecommended":true}]`),
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order","isRecommended":"yes"}]`), "cards[0].suggestions[0].isRecommended"},
		{"suggestion.actions", "2.0",
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order","actions":[]}]`),
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Keep order","actions":{}}]`), "cards[0].suggestions[0].actions"},
		{"card.selectionBehavior", "2.0",
			cdsResp(`,"selectionBehavior":"at-most-one","suggestions":[{"label":"Keep order"}]`),
			cdsResp(`,"suggestions":[{"label":"Keep order"}]`), "cards[0].selectionBehavior"},
		{"card.selectionBehavior.at-most-one", "2.0",
			cdsResp(`,"selectionBehavior":"at-most-one","suggestions":[{"label":"A","isRecommended":true},{"label":"B"}]`),
			cdsResp(`,"selectionBehavior":"at-most-one","suggestions":[{"label":"A","isRecommended":true},{"label":"B","isRecommended":true}]`),
			"cards[0].suggestions"},
		{"action.object", "2.0", `{"cards":[],"systemActions":[` + cdsUpdateAction + `]}`, `{"cards":[],"systemActions":["update"]}`, "systemActions[0]"},
		{"action.type", "2.0", `{"cards":[],"systemActions":[` + cdsUpdateAction + `]}`,
			`{"cards":[],"systemActions":[` + strings.Replace(cdsUpdateAction, `"update"`, `"upsert"`, 1) + `]}`, "systemActions[0].type"},
		{"action.description", "2.0", `{"cards":[],"systemActions":[` + cdsUpdateAction + `]}`,
			`{"cards":[],"systemActions":[{"type":"update","resource":{"resourceType":"ServiceRequest","id":"sr1"}}]}`, "systemActions[0].description"},
		{"action.resource", "2.0",
			`{"cards":[],"systemActions":[{"type":"create","description":"Create task","resource":{"resourceType":"Task"}}]}`,
			`{"cards":[],"systemActions":[{"type":"create","description":"Create task"}]}`, "systemActions[0].resource"},
		{"action.resourceId", "2.0",
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Remove order","actions":[{"type":"delete","description":"Remove order","resourceId":"ServiceRequest/sr1"}]}]`),
			cdsResp(`,"selectionBehavior":"any","suggestions":[{"label":"Remove order","actions":[{"type":"delete","description":"Remove order"}]}]`),
			"cards[0].suggestions[0].actions[0].resourceId"},
		{"card.links", "2.0",
			cdsResp(`,"links":[{"label":"Policy","url":"https://payer.example/policy","type":"absolute"}]`),
			cdsResp(`,"links":{"label":"Policy"}`), "cards[0].links"},
		{"link.label", "2.0",
			cdsResp(`,"links":[{"label":"Policy","url":"https://payer.example/policy","type":"absolute"}]`),
			cdsResp(`,"links":[{"url":"https://payer.example/policy","type":"absolute"}]`), "cards[0].links[0].label"},
		{"link.url", "2.0",
			cdsResp(`,"links":[{"label":"Policy","url":"https://payer.example/policy","type":"absolute"}]`),
			cdsResp(`,"links":[{"label":"Policy","type":"absolute"}]`), "cards[0].links[0].url"},
		{"link.type", "2.0",
			cdsResp(`,"links":[{"label":"Forms","url":"https://payer.example/launch","type":"smart"}]`),
			cdsResp(`,"links":[{"label":"Forms","url":"https://payer.example/launch","type":"relative"}]`), "cards[0].links[0].type"},
		{"link.appContext", "2.0",
			cdsResp(`,"links":[{"label":"Forms","url":"https://payer.example/launch","type":"smart","appContext":"{\"questionnaire\":\"q1\"}"}]`),
			cdsResp(`,"links":[{"label":"Policy","url":"https://payer.example/policy","type":"absolute","appContext":"x"}]`), "cards[0].links[0].appContext"},
		{"card.overrideReasons", "2.0",
			cdsResp(`,"overrideReasons":[{"system":"http://example.org/override","code":"patient-refused","display":"Patient refused"}]`),
			cdsResp(`,"overrideReasons":[{"display":"Patient refused"}]`), "cards[0].overrideReasons[0]"},
		{"overrideReason.display", "2.0",
			cdsResp(`,"overrideReasons":[{"system":"http://example.org/override","code":"patient-refused","display":"Patient refused"}]`),
			cdsResp(`,"overrideReasons":[{"system":"http://example.org/override","code":"patient-refused"}]`), "cards[0].overrideReasons[0].display"},
		{"line", "2.1", `{"cards":[]}`, `{"cards":[]}`, ""},
	}
}

// TestCDSHooksCertifierRules settles each certifier rule with one conformant
// and one non-conformant response: the conformant one yields no violation, the
// non-conformant one exactly the rule's violation at its path.
func TestCDSHooksCertifierRules(t *testing.T) {
	rows := certifierRows()
	covered := map[string]bool{}
	for _, r := range rows {
		covered[r.rule] = true
		t.Run(r.rule, func(t *testing.T) {
			if got := CheckCDSHooksResponse([]byte(r.positive), r.line); len(got) != 0 {
				t.Fatalf("conformant response reported %+v\n%s", got, r.positive)
			}
			negLine := r.line
			if r.rule == "line" {
				negLine = "9.9"
			}
			got := CheckCDSHooksResponse([]byte(r.negative), negLine)
			if len(got) != 1 || got[0].Rule != r.rule || got[0].Path != r.path {
				t.Fatalf("want exactly {%s %q}, got %+v\n%s", r.rule, r.path, got, r.negative)
			}
			wantSev := SeverityError
			if r.rule == "action.resourceId" {
				wantSev = SeverityWarning
			}
			if got[0].Severity != wantSev {
				t.Fatalf("severity %q, want %q", got[0].Severity, wantSev)
			}
		})
	}
	for _, rule := range CDSHooksRules() {
		if !covered[rule.ID] {
			t.Errorf("rule %s has no certifier row", rule.ID)
		}
		if rule.Citation == "" || rule.Description == "" {
			t.Errorf("rule %s lacks a citation or description", rule.ID)
		}
		if rule.Severity != SeverityError && rule.Severity != SeverityWarning {
			t.Errorf("rule %s has severity %q", rule.ID, rule.Severity)
		}
	}
	if len(CDSHooksRules()) != len(rows) {
		t.Errorf("%d rules, %d rows", len(CDSHooksRules()), len(rows))
	}
}

// TestCDSHooksCertifier_TopicOnlyAtCRDLines: the card topic is a CRD
// requirement, not a CDS Hooks one, so the plain CDS Hooks check ("") does not
// ask for it while every CRD line does.
func TestCDSHooksCertifier_TopicOnlyAtCRDLines(t *testing.T) {
	body := []byte(`{"cards":[{"summary":"s","indicator":"info","source":{"label":"Example Health Plan"}}]}`)
	if got := CheckCDSHooksResponse(body, ""); len(got) != 0 {
		t.Fatalf("plain CDS Hooks check: %+v", got)
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		got := CheckCDSHooksResponse(body, line)
		if len(got) != 1 || got[0].Rule != "card.source.topic" {
			t.Errorf("line %s: %+v", line, got)
		}
	}
}

// TestCDSHooksCertifier_RealPayerFixtureEnvelope: the reference payer's
// recorded answer is a well-formed CDS Hooks envelope at every line.
func TestCDSHooksCertifier_RealPayerFixtureEnvelope(t *testing.T) {
	body := readRealPayerCRDResponse(t)
	for _, line := range []string{"", "2.0", "2.1", "2.2"} {
		if got := CheckCDSHooksResponse(body, line); len(got) != 0 {
			t.Errorf("line %q: %+v", line, got)
		}
	}
}

// TestCDSHooksCertifier_ReportsEveryViolation: the certifier lists every
// problem, in document order, rather than stopping at the first.
func TestCDSHooksCertifier_ReportsEveryViolation(t *testing.T) {
	body := []byte(`{"cards":[{"summary":"","indicator":"loud","source":{}}],"systemActions":[{"type":"delete","description":"d"}]}`)
	var rules []string
	for _, v := range CheckCDSHooksResponse(body, "2.0") {
		rules = append(rules, v.Rule+"@"+v.Path)
	}
	want := []string{
		"card.summary@cards[0].summary",
		"card.indicator@cards[0].indicator",
		"card.source.label@cards[0].source.label",
		"card.source.topic@cards[0].source.topic",
		"action.resourceId@systemActions[0].resourceId",
	}
	if !slices.Equal(rules, want) {
		t.Fatalf("got %v\nwant %v", rules, want)
	}
	if !CDSHooksViolationsRefuse(CheckCDSHooksResponse(body, "2.0")) {
		t.Fatal("errors present, but the violations do not refuse")
	}
	warnOnly := CheckCDSHooksResponse([]byte(`{"cards":[],"systemActions":[{"type":"delete","description":"d"}]}`), "2.0")
	if len(warnOnly) != 1 || CDSHooksViolationsRefuse(warnOnly) {
		t.Fatalf("a warning alone must not refuse: %+v", warnOnly)
	}
}

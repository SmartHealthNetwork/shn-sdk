package shnsdk

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// Severity is how much a CDS Hooks rule violation weighs: an error breaks a
// MUST/REQUIRED rule; a warning breaks a SHOULD rule.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Violation is one broken CDS Hooks response rule: the rule id (see
// CDSHooksRules), the JSON path of the offending member (for example
// "cards[0].source.label"; "" for the whole body) and its severity.
type Violation struct {
	Rule     string
	Path     string
	Severity Severity
}

// CDSHooksRule is one row of the response certifier's rule table.
type CDSHooksRule struct {
	ID          string
	Severity    Severity
	Description string
	// Citation names the specification text the rule enforces.
	Citation string
	// CRDOnly marks a rule that applies only when a CRD line is named.
	CRDOnly bool
}

const (
	citeCDSResponse   = "CDS Hooks 2.0, CDS Service Response"
	citeCDSCard       = "CDS Hooks 2.0, Card Attributes"
	citeCDSSource     = "CDS Hooks 2.0, Source"
	citeCDSSuggestion = "CDS Hooks 2.0, Suggestion"
	citeCDSAction     = "CDS Hooks 2.0, Action"
	citeCDSLink       = "CDS Hooks 2.0, Link"
	citeCDSCoding     = "CDS Hooks 2.0, Coding"
)

var cdsHooksRules = []CDSHooksRule{
	{"response.json", SeverityError, "the body is one well-formed JSON value whose objects have unique member names", "RFC 8259 §2 and §4; " + citeCDSResponse, false},
	{"response.object", SeverityError, "the body is a JSON object", citeCDSResponse + ": the response is a JSON object", false},
	{"response.cards", SeverityError, "cards is present and is an array (it may be empty)", citeCDSResponse + ": cards REQUIRED, array of Cards, may be empty", false},
	{"response.systemActions", SeverityError, "systemActions, when present, is an array", citeCDSResponse + ": systemActions OPTIONAL, array of Actions", false},
	{"card.object", SeverityError, "each card is a JSON object", citeCDSResponse + ": array of Cards", false},
	{"card.uuid", SeverityError, "a card uuid, when present, is a string", citeCDSCard + ": uuid OPTIONAL string", false},
	{"card.summary", SeverityError, "each card has a non-empty summary string", citeCDSCard + ": summary REQUIRED string", false},
	{"card.summary.length", SeverityError, "a card summary has fewer than 140 characters", citeCDSCard + ": summary is a one-sentence, <140-character message", false},
	{"card.detail", SeverityError, "a card detail, when present, is a (markdown) string", citeCDSCard + ": detail OPTIONAL string", false},
	{"card.indicator", SeverityError, "each card has an indicator of info, warning or critical", citeCDSCard + ": indicator REQUIRED, one of info, warning, critical", false},
	{"card.source", SeverityError, "each card has a source object", citeCDSCard + ": source REQUIRED object", false},
	{"card.source.label", SeverityError, "each card source has a non-empty label", citeCDSSource + ": label REQUIRED string", false},
	{"card.source.topic", SeverityError, "at a CRD line, each card source has a topic Coding", "Da Vinci CRD 2.0.1 and 2.1.0 card requirements (source.topic SHALL be populated); CRD 2.2.1 CRDHooksResponse cards.source.topic 1..1", true},
	{"card.suggestions", SeverityError, "card suggestions, when present, are an array of objects", citeCDSCard + ": suggestions OPTIONAL array of Suggestions", false},
	{"suggestion.label", SeverityError, "each suggestion has a non-empty label", citeCDSSuggestion + ": label REQUIRED string", false},
	{"suggestion.uuid", SeverityError, "a suggestion uuid, when present, is a string", citeCDSSuggestion + ": uuid OPTIONAL string", false},
	{"suggestion.isRecommended", SeverityError, "isRecommended, when present, is a boolean", citeCDSSuggestion + ": isRecommended OPTIONAL boolean", false},
	{"suggestion.actions", SeverityError, "suggestion actions, when present, are an array", citeCDSSuggestion + ": actions OPTIONAL array of Actions", false},
	{"card.selectionBehavior", SeverityError, "a card with suggestions has a selectionBehavior of at-most-one or any", citeCDSCard + ": selectionBehavior is REQUIRED if suggestions are present; allowed values at-most-one, any", false},
	{"card.selectionBehavior.at-most-one", SeverityError, "an at-most-one card recommends at most one suggestion", citeCDSCard + " (FHIR tooling CDSHooksResponse invariant cds-resp-1)", false},
	{"action.object", SeverityError, "each action is a JSON object", citeCDSAction, false},
	{"action.type", SeverityError, "each action has a type of create, update or delete", citeCDSAction + ": type REQUIRED, one of create, update, delete", false},
	{"action.description", SeverityError, "each action has a description string", citeCDSAction + ": description REQUIRED string (the CRD 2.2.1 CRDHooksResponse tooling model marks systemActions.description 0..1; CDS Hooks 2.0 governs)", false},
	{"action.resource", SeverityError, "a create or update action carries a FHIR resource", citeCDSAction + ": for create the resource SHALL contain the new resource; for update it holds the updated resource in its entirety", false},
	{"action.resourceId", SeverityWarning, "a delete action names the resource it deletes", citeCDSAction + ": resourceId SHOULD be provided when the type is delete", false},
	{"card.links", SeverityError, "card links, when present, are an array of objects", citeCDSCard + ": links OPTIONAL array of Links", false},
	{"link.label", SeverityError, "each link has a non-empty label", citeCDSLink + ": label REQUIRED string", false},
	{"link.url", SeverityError, "each link has a non-empty url", citeCDSLink + ": url REQUIRED URL", false},
	{"link.type", SeverityError, "each link has a type of absolute or smart", citeCDSLink + ": type REQUIRED, one of absolute, smart", false},
	{"link.appContext", SeverityError, "appContext is a string and appears only on smart links", citeCDSLink + ": appContext OPTIONAL string, only valid for SMART app links", false},
	{"card.overrideReasons", SeverityError, "override reasons, when present, are Coding objects with a code and a system", citeCDSCard + ": overrideReasons OPTIONAL array of Coding; " + citeCDSCoding + ": code and system REQUIRED", false},
	{"overrideReason.display", SeverityError, "each override reason has a display", citeCDSCoding + ": display REQUIRED for override reasons provided by a CDS service", false},
	{"line", SeverityError, "the named line is empty (plain CDS Hooks) or a CRD line this SDK knows", "this SDK's CRD line table (2.0, 2.1, 2.2)", false},
}

// CDSHooksRules returns the certifier's rule table, one row per rule, each
// with the specification text it enforces.
func CDSHooksRules() []CDSHooksRule {
	return append([]CDSHooksRule(nil), cdsHooksRules...)
}

var cdsHooksRuleSeverity = func() map[string]Severity {
	m := make(map[string]Severity, len(cdsHooksRules))
	for _, r := range cdsHooksRules {
		m[r.ID] = r.Severity
	}
	return m
}()

// CDSHooksViolationsRefuse reports whether any violation is an error.
func CDSHooksViolationsRefuse(vs []Violation) bool {
	for _, v := range vs {
		if v.Severity == SeverityError {
			return true
		}
	}
	return false
}

// CheckCDSHooksResponse certifies a CDS Hooks service response against the
// CDS Hooks 2.0 response rules and, when line names a CRD line ("2.0",
// "2.1", "2.2"), the CRD card requirements for that line. line "" checks
// plain CDS Hooks. It only observes: it never changes the body, and it
// returns every violation (nil when the response conforms). Embedded FHIR
// resources are not profiled here; validate them separately.
func CheckCDSHooksResponse(body []byte, line string) []Violation {
	c := cdsChecker{crd: line != ""}
	if line != "" {
		if _, ok := CRDLineDef(line); !ok {
			c.add("line", "")
			return c.out
		}
	}
	if _, err := splice.Scan(body, splice.DefaultLimits()); err != nil {
		c.add("response.json", "")
		return c.out
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		c.add("response.json", "")
		return c.out
	}
	obj, ok := root.(map[string]any)
	if !ok {
		c.add("response.object", "")
		return c.out
	}
	cards, ok := obj["cards"].([]any)
	if !ok {
		c.add("response.cards", "cards")
	}
	for i, card := range cards {
		c.card(card, fmt.Sprintf("cards[%d]", i))
	}
	if sa, present := obj["systemActions"]; present {
		actions, ok := sa.([]any)
		if !ok {
			c.add("response.systemActions", "systemActions")
		}
		for i, a := range actions {
			c.action(a, fmt.Sprintf("systemActions[%d]", i))
		}
	}
	return c.out
}

type cdsChecker struct {
	crd bool
	out []Violation
}

func (c *cdsChecker) add(rule, path string) {
	c.out = append(c.out, Violation{Rule: rule, Path: path, Severity: cdsHooksRuleSeverity[rule]})
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

// optionalOfType reports whether m[key] is absent or satisfies ok.
func optionalOfType(m map[string]any, key string, ok func(any) bool) bool {
	v, present := m[key]
	return !present || ok(v)
}

func isString(v any) bool { _, ok := v.(string); return ok }
func isBool(v any) bool   { _, ok := v.(bool); return ok }
func isArray(v any) bool  { _, ok := v.([]any); return ok }

func (c *cdsChecker) card(v any, path string) {
	card, ok := v.(map[string]any)
	if !ok {
		c.add("card.object", path)
		return
	}
	if !optionalOfType(card, "uuid", isString) {
		c.add("card.uuid", path+".uuid")
	}
	if !nonEmptyString(card["summary"]) {
		c.add("card.summary", path+".summary")
	} else if utf8.RuneCountInString(card["summary"].(string)) >= 140 {
		c.add("card.summary.length", path+".summary")
	}
	if !optionalOfType(card, "detail", isString) {
		c.add("card.detail", path+".detail")
	}
	switch card["indicator"] {
	case "info", "warning", "critical":
	default:
		c.add("card.indicator", path+".indicator")
	}
	if source, ok := card["source"].(map[string]any); !ok {
		c.add("card.source", path+".source")
	} else {
		if !nonEmptyString(source["label"]) {
			c.add("card.source.label", path+".source.label")
		}
		if c.crd {
			topic, ok := source["topic"].(map[string]any)
			if !ok || !nonEmptyString(topic["code"]) {
				c.add("card.source.topic", path+".source.topic")
			}
		}
	}
	c.suggestions(card, path)
	c.links(card, path)
	c.overrideReasons(card, path)
}

func (c *cdsChecker) suggestions(card map[string]any, path string) {
	raw, present := card["suggestions"]
	var suggestions []any
	if present {
		var ok bool
		if suggestions, ok = raw.([]any); !ok {
			c.add("card.suggestions", path+".suggestions")
		}
	}
	recommended := 0
	for i, s := range suggestions {
		sp := fmt.Sprintf("%s.suggestions[%d]", path, i)
		sug, ok := s.(map[string]any)
		if !ok {
			c.add("card.suggestions", sp)
			continue
		}
		if !nonEmptyString(sug["label"]) {
			c.add("suggestion.label", sp+".label")
		}
		if !optionalOfType(sug, "uuid", isString) {
			c.add("suggestion.uuid", sp+".uuid")
		}
		if !optionalOfType(sug, "isRecommended", isBool) {
			c.add("suggestion.isRecommended", sp+".isRecommended")
		} else if sug["isRecommended"] == true {
			recommended++
		}
		if !optionalOfType(sug, "actions", isArray) {
			c.add("suggestion.actions", sp+".actions")
			continue
		}
		actions, _ := sug["actions"].([]any)
		for j, a := range actions {
			c.action(a, fmt.Sprintf("%s.actions[%d]", sp, j))
		}
	}
	behavior, hasBehavior := card["selectionBehavior"]
	switch {
	case !hasBehavior && len(suggestions) > 0:
		c.add("card.selectionBehavior", path+".selectionBehavior")
	case hasBehavior && behavior != "at-most-one" && behavior != "any":
		c.add("card.selectionBehavior", path+".selectionBehavior")
	case behavior == "at-most-one" && recommended > 1:
		c.add("card.selectionBehavior.at-most-one", path+".suggestions")
	}
}

func (c *cdsChecker) action(v any, path string) {
	a, ok := v.(map[string]any)
	if !ok {
		c.add("action.object", path)
		return
	}
	typ := a["type"]
	switch typ {
	case "create", "update", "delete":
	default:
		c.add("action.type", path+".type")
	}
	if !isString(a["description"]) {
		c.add("action.description", path+".description")
	}
	switch typ {
	case "create", "update":
		res, ok := a["resource"].(map[string]any)
		if !ok || !nonEmptyString(res["resourceType"]) {
			c.add("action.resource", path+".resource")
		}
	case "delete":
		if !nonEmptyString(a["resourceId"]) {
			c.add("action.resourceId", path+".resourceId")
		}
	}
}

func (c *cdsChecker) links(card map[string]any, path string) {
	raw, present := card["links"]
	if !present {
		return
	}
	links, ok := raw.([]any)
	if !ok {
		c.add("card.links", path+".links")
		return
	}
	for i, l := range links {
		lp := fmt.Sprintf("%s.links[%d]", path, i)
		link, ok := l.(map[string]any)
		if !ok {
			c.add("card.links", lp)
			continue
		}
		if !nonEmptyString(link["label"]) {
			c.add("link.label", lp+".label")
		}
		if !nonEmptyString(link["url"]) {
			c.add("link.url", lp+".url")
		}
		typ := link["type"]
		if typ != "absolute" && typ != "smart" {
			c.add("link.type", lp+".type")
		}
		if ctx, present := link["appContext"]; present && (typ != "smart" || !isString(ctx)) {
			c.add("link.appContext", lp+".appContext")
		}
	}
}

func (c *cdsChecker) overrideReasons(card map[string]any, path string) {
	raw, present := card["overrideReasons"]
	if !present {
		return
	}
	reasons, ok := raw.([]any)
	if !ok {
		c.add("card.overrideReasons", path+".overrideReasons")
		return
	}
	for i, r := range reasons {
		rp := fmt.Sprintf("%s.overrideReasons[%d]", path, i)
		coding, ok := r.(map[string]any)
		if !ok || !nonEmptyString(coding["code"]) || !nonEmptyString(coding["system"]) {
			c.add("card.overrideReasons", rp)
			continue
		}
		if !nonEmptyString(coding["display"]) {
			c.add("overrideReason.display", rp+".display")
		}
	}
}

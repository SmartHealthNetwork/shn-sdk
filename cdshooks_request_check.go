package shnsdk

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

const (
	citeCDSRequest       = "CDS Hooks 2.0, Calling a CDS Service (HTTP Request)"
	citeCDSAuthorization = "CDS Hooks 2.0, Passing the Access Token to the CDS Service"
	citeCDSPrefetch      = "CDS Hooks 2.0, Prefetch Template"
	citeCDSExtensions    = "CDS Hooks 2.0, Extensions"
	citeHookOrderSign    = "CDS Hooks order-sign 1.0 context"
	citeHookOrderSelect  = "CDS Hooks order-select 1.0 context"
	citeHookOrderDisp    = "CDS Hooks order-dispatch 1.1 context"
	citeCRDHooksRequest  = "Da Vinci CRD 2.2.1 CRDHooksRequest"
)

var cdsHooksRequestRules = []CDSHooksRule{
	{"request.json", SeverityError, "the body is one well-formed JSON value whose objects have unique member names", "RFC 8259 §2 and §4; " + citeCDSRequest, false},
	{"request.object", SeverityError, "the body is a JSON object", citeCDSRequest + ": the request is a JSON object", false},
	{"request.hook", SeverityError, "hook is a non-empty string", citeCDSRequest + ": hook REQUIRED string", false},
	{"request.hookInstance", SeverityError, "hookInstance is a UUID", citeCDSRequest + ": hookInstance REQUIRED string, a universally unique identifier (UUID) for this hook call", false},
	{"request.fhirServer", SeverityError, "fhirServer, when present, is a non-empty string", citeCDSRequest + ": fhirServer CONDITIONAL URL", false},
	{"request.fhirServer.authorization", SeverityError, "a request that carries fhirAuthorization names its fhirServer", citeCDSRequest + ": fhirServer is REQUIRED if fhirAuthorization is provided (FHIR tooling CDSHooksRequest invariant cds-r-1)", false},
	{"request.fhirAuthorization", SeverityError, "fhirAuthorization, when present, is an object", citeCDSRequest + ": fhirAuthorization OPTIONAL object", false},
	{"fhirAuthorization.access_token", SeverityError, "fhirAuthorization has a non-empty access_token", citeCDSAuthorization + ": access_token REQUIRED string", false},
	{"fhirAuthorization.token_type", SeverityError, "fhirAuthorization's token_type is Bearer", citeCDSAuthorization + ": token_type REQUIRED, fixed value Bearer", false},
	{"fhirAuthorization.expires_in", SeverityError, "fhirAuthorization's expires_in is an integer", citeCDSAuthorization + ": expires_in REQUIRED integer", false},
	{"fhirAuthorization.scope", SeverityError, "fhirAuthorization has a non-empty scope", citeCDSAuthorization + ": scope REQUIRED string", false},
	{"fhirAuthorization.subject", SeverityError, "fhirAuthorization has a non-empty subject", citeCDSAuthorization + ": subject REQUIRED string", false},
	{"fhirAuthorization.patient", SeverityError, "fhirAuthorization's patient, when present, is a non-empty string", citeCDSAuthorization + ": patient CONDITIONAL string", false},
	{"fhirAuthorization.patient.scope", SeverityWarning, "an authorization granting patient-level scopes names the patient", citeCDSAuthorization + ": patient is required when patient-level scopes are granted (FHIR tooling CDSHooksRequest invariant cds-r-2, whose text is a SHOULD; CDS Hooks 2.0 governs the severity)", false},
	{"request.context", SeverityError, "context is an object", citeCDSRequest + ": context REQUIRED object", false},
	{"request.prefetch", SeverityError, "prefetch, when present, is an object", citeCDSRequest + ": prefetch OPTIONAL object", false},
	{"prefetch.value", SeverityError, "each prefetch value is a FHIR resource or null", citeCDSPrefetch + ": each key's value is the FHIR resource its template asks for, and null when the client has no data for it (the CRD 2.2.1 model marks a prefetch value 1..1 Resource; the hook's null governs)", false},
	{"request.extension", SeverityError, "extension, when present, is an object", citeCDSExtensions + ": the extension member's value is a JSON object", false},
	{"context.userId", SeverityError, "an order-select or order-sign context has a non-empty userId", citeHookOrderSign + " and " + citeHookOrderSelect + ": userId REQUIRED string", false},
	{"context.patientId", SeverityError, "an order-select, order-sign or order-dispatch context has a non-empty patientId", citeHookOrderSign + ", " + citeHookOrderSelect + " and " + citeHookOrderDisp + ": patientId REQUIRED string", false},
	{"context.encounterId", SeverityError, "encounterId, when present, is a string", citeHookOrderSign + " and " + citeHookOrderSelect + ": encounterId OPTIONAL string", false},
	{"context.draftOrders", SeverityError, "an order-select or order-sign context has a draftOrders Bundle", citeHookOrderSign + " and " + citeHookOrderSelect + ": draftOrders REQUIRED, a Bundle of FHIR request resources", false},
	{"context.selections", SeverityError, "an order-select context has selections, a non-empty array of strings", citeHookOrderSelect + ": selections REQUIRED array, the ids of the newly selected orders (the CRD 2.2.1 CRDOrderSelectContext model marks selections 0..*; the hook governs)", false},
	{"context.selections.draftOrders", SeverityError, "each selection names a resource in draftOrders as Type/id", citeHookOrderSelect + ": selections reference FHIR resources in the draftOrders Bundle, for example MedicationRequest/103", false},
	{"context.dispatchedOrders", SeverityError, "an order-dispatch context has dispatchedOrders, an array of non-empty strings", citeHookOrderDisp + ": dispatchedOrders REQUIRED array of local references", false},
	{"context.performer", SeverityError, "an order-dispatch context has a non-empty performer", citeHookOrderDisp + ": performer REQUIRED string (the CRD 2.2.1 CRDOrderDispatchContext model marks performer 0..1; the hook governs)", false},
	{"context.fulfillmentTasks", SeverityError, "fulfillmentTasks, when present, is an array of Task resources", citeHookOrderDisp + ": fulfillmentTasks OPTIONAL array of Task", false},
	{"crd.fhirServer", SeverityError, "at CRD 2.2, the request names its fhirServer", citeCRDHooksRequest + ": fhirServer 1..1", true},
	{"crd.fhirAuthorization", SeverityError, "at CRD 2.2, the request carries fhirAuthorization", citeCRDHooksRequest + ": fhirAuthorization 1..1", true},
	{"crd.selections", SeverityError, "at CRD 2.2, each selection is Type/id", "Da Vinci CRD 2.2.1 CRDOrderSelectContext invariant ord-1 (from the FHIR tooling order-select context model CRD 2.2.1 builds on)", true},
	{"crd.dispatchedOrders", SeverityError, "at CRD 2.2, an order-dispatch context dispatches at least one order", "Da Vinci CRD 2.2.1 CRDOrderDispatchContext dispatchedOrders 1..* (from the FHIR tooling order-dispatch context model CRD 2.2.1 builds on)", true},
	{"line", SeverityError, "the named line is empty (plain CDS Hooks) or a CRD line this SDK knows", "this SDK's CRD line table (2.0, 2.1, 2.2)", false},
}

// CDSHooksRequestRules returns the request certifier's rule table, one row
// per rule, each with the specification text it enforces. A CRDOnly rule
// applies at CRD 2.2 only: CRD 2.0.1 and 2.1.0 publish no model of the
// request. CRD's optional request extension members are not checked.
func CDSHooksRequestRules() []CDSHooksRule {
	return append([]CDSHooksRule(nil), cdsHooksRequestRules...)
}

var cdsHooksRequestRuleSeverity = func() map[string]Severity {
	m := make(map[string]Severity, len(cdsHooksRequestRules))
	for _, r := range cdsHooksRequestRules {
		m[r.ID] = r.Severity
	}
	return m
}()

// crdSelection is CRD 2.2.1's ord-1: a selection is Type/id.
var crdSelection = regexp.MustCompile(`^[\d\w]+/[\d\w\-.]+$`)

// CheckCDSHooksRequest certifies a CDS Hooks service request against the
// CDS Hooks 2.0 request rules and the context of the hooks it knows
// (order-select, order-sign, order-dispatch) and, when line is "2.2", CRD
// 2.2.1's request model. line "" checks plain CDS Hooks; "2.0" and "2.1" add
// nothing to it, since CRD 2.0.1 and 2.1.0 publish no model of the request
// (2.1.0 models only an optional configuration extension), and CRD's optional
// request extension members are not checked at any line. It only
// observes: it never changes the body, and it returns every violation (nil
// when the request conforms). A request BuildCRDRequest builds names no
// fhirServer and no fhirAuthorization by design, so at "2.2" it reports
// crd.fhirServer and crd.fhirAuthorization. Embedded FHIR resources are not
// profiled here; validate them separately.
func CheckCDSHooksRequest(body []byte, line string) []Violation {
	c := cdsRequestChecker{crd: line == "2.2"}
	if line != "" {
		if _, ok := CRDLineDef(line); !ok {
			c.add("line", "")
			return c.out
		}
	}
	if _, err := splice.Scan(body, splice.DefaultLimits()); err != nil {
		c.add("request.json", "")
		return c.out
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		c.add("request.json", "")
		return c.out
	}
	obj, ok := root.(map[string]any)
	if !ok {
		c.add("request.object", "")
		return c.out
	}
	if !nonEmptyString(obj["hook"]) {
		c.add("request.hook", "hook")
	}
	if s, ok := obj["hookInstance"].(string); !ok || !hookInstanceUUID.MatchString(s) {
		c.add("request.hookInstance", "hookInstance")
	}
	_, hasServer := obj["fhirServer"]
	if hasServer && !nonEmptyString(obj["fhirServer"]) {
		c.add("request.fhirServer", "fhirServer")
	}
	authRaw, hasAuth := obj["fhirAuthorization"]
	if hasAuth && !hasServer {
		c.add("request.fhirServer.authorization", "fhirServer")
	}
	if hasAuth {
		c.authorization(authRaw)
	}
	if c.crd && !hasServer {
		c.add("crd.fhirServer", "fhirServer")
	}
	if c.crd && !hasAuth {
		c.add("crd.fhirAuthorization", "fhirAuthorization")
	}
	ctx, ok := obj["context"].(map[string]any)
	if !ok {
		c.add("request.context", "context")
	} else {
		hook, _ := obj["hook"].(string)
		c.context(hook, ctx)
	}
	if pf, present := obj["prefetch"]; present {
		c.prefetch(pf)
	}
	if !optionalOfType(obj, "extension", isObject) {
		c.add("request.extension", "extension")
	}
	return c.out
}

type cdsRequestChecker struct {
	crd bool
	out []Violation
}

func (c *cdsRequestChecker) add(rule, path string) {
	c.out = append(c.out, Violation{Rule: rule, Path: path, Severity: cdsHooksRequestRuleSeverity[rule]})
}

func isObject(v any) bool { _, ok := v.(map[string]any); return ok }

func (c *cdsRequestChecker) authorization(v any) {
	auth, ok := v.(map[string]any)
	if !ok {
		c.add("request.fhirAuthorization", "fhirAuthorization")
		return
	}
	if !nonEmptyString(auth["access_token"]) {
		c.add("fhirAuthorization.access_token", "fhirAuthorization.access_token")
	}
	if auth["token_type"] != "Bearer" {
		c.add("fhirAuthorization.token_type", "fhirAuthorization.token_type")
	}
	if n, ok := auth["expires_in"].(float64); !ok || n != math.Trunc(n) {
		c.add("fhirAuthorization.expires_in", "fhirAuthorization.expires_in")
	}
	scope, _ := auth["scope"].(string)
	if scope == "" {
		c.add("fhirAuthorization.scope", "fhirAuthorization.scope")
	}
	if !nonEmptyString(auth["subject"]) {
		c.add("fhirAuthorization.subject", "fhirAuthorization.subject")
	}
	patient, hasPatient := auth["patient"]
	switch {
	case hasPatient && !nonEmptyString(patient):
		c.add("fhirAuthorization.patient", "fhirAuthorization.patient")
	case !hasPatient && patientLevelScope(scope):
		c.add("fhirAuthorization.patient.scope", "fhirAuthorization.patient")
	}
}

// patientLevelScope reports whether a space-separated SMART scope string
// grants a patient-level scope (patient/...).
func patientLevelScope(scope string) bool {
	return slices.ContainsFunc(strings.Fields(scope), func(s string) bool { return strings.HasPrefix(s, "patient/") })
}

func (c *cdsRequestChecker) context(hook string, ctx map[string]any) {
	switch hook {
	case "order-select", "order-sign":
		if !nonEmptyString(ctx["userId"]) {
			c.add("context.userId", "context.userId")
		}
		if !nonEmptyString(ctx["patientId"]) {
			c.add("context.patientId", "context.patientId")
		}
		if !optionalOfType(ctx, "encounterId", isString) {
			c.add("context.encounterId", "context.encounterId")
		}
		drafts, ok := ctx["draftOrders"].(map[string]any)
		if !ok || drafts["resourceType"] != "Bundle" {
			c.add("context.draftOrders", "context.draftOrders")
			drafts = nil
		}
		if hook == "order-select" {
			c.selections(ctx["selections"], drafts)
		}
	case "order-dispatch":
		if !nonEmptyString(ctx["patientId"]) {
			c.add("context.patientId", "context.patientId")
		}
		orders, ok := ctx["dispatchedOrders"].([]any)
		if !ok || slices.ContainsFunc(orders, func(o any) bool { return !nonEmptyString(o) }) {
			c.add("context.dispatchedOrders", "context.dispatchedOrders")
		} else if c.crd && len(orders) == 0 {
			c.add("crd.dispatchedOrders", "context.dispatchedOrders")
		}
		if !nonEmptyString(ctx["performer"]) {
			c.add("context.performer", "context.performer")
		}
		if raw, present := ctx["fulfillmentTasks"]; present {
			tasks, ok := raw.([]any)
			if !ok || slices.ContainsFunc(tasks, func(v any) bool {
				task, ok := v.(map[string]any)
				return !ok || task["resourceType"] != "Task"
			}) {
				c.add("context.fulfillmentTasks", "context.fulfillmentTasks")
			}
		}
	}
}

func (c *cdsRequestChecker) selections(v any, drafts map[string]any) {
	sels, ok := v.([]any)
	if !ok || len(sels) == 0 || slices.ContainsFunc(sels, func(s any) bool { return !nonEmptyString(s) }) {
		c.add("context.selections", "context.selections")
		return
	}
	inDrafts := map[string]bool{}
	entries, _ := drafts["entry"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		typ, _ := res["resourceType"].(string)
		id, _ := res["id"].(string)
		if typ != "" && id != "" {
			inDrafts[typ+"/"+id] = true
		}
	}
	for i, s := range sels {
		sel := s.(string)
		path := fmt.Sprintf("context.selections[%d]", i)
		if drafts != nil && !inDrafts[sel] {
			c.add("context.selections.draftOrders", path)
		}
		if c.crd && !crdSelection.MatchString(sel) {
			c.add("crd.selections", path)
		}
	}
}

func (c *cdsRequestChecker) prefetch(v any) {
	pf, ok := v.(map[string]any)
	if !ok {
		c.add("request.prefetch", "prefetch")
		return
	}
	keys := make([]string, 0, len(pf))
	for k := range pf {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if pf[k] == nil {
			continue
		}
		res, ok := pf[k].(map[string]any)
		if !ok || !nonEmptyString(res["resourceType"]) {
			c.add("prefetch.value", "prefetch."+k)
		}
	}
}

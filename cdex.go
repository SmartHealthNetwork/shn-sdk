// Package shnsdk — cdex builds and parses the CDex Task Data Request messages: a
// cdex-task-data-request Task (named patient + resource types, NO bulk export) and a
// completed-Task response wrapping a US-Core searchset Bundle (FR-24/FR-26, AI-1). It is
// the CDex content layer that replaced the bespoke fedquery Parameters wire (BuildQuery/
// ParseQuery/ExtractOperativeEvidence have been removed).
package shnsdk

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	cdexTemp        = "http://hl7.org/fhir/us/davinci-cdex/CodeSystem/cdex-temp"
	hrexTemp        = "http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp"
	v3ActReason     = "http://terminology.hl7.org/CodeSystem/v3-ActReason"
	cdexTaskProfile = "http://hl7.org/fhir/us/davinci-cdex/StructureDefinition/cdex-task-data-request"
)

// CDexTaskMeta carries the CDex Task participants + timestamp the cdex-task-data-request profile
// requires (authoredOn/requester/owner, all min=1). These are partner-asserted descriptive content
// — NOT load-bearing for authorization (the substrate re-checks consent per AI-11/OWD-6) — but they
// are mandatory for FHIR conformance. Requester is the data-consumer (provider) holder id; Owner is
// the data-source (facility) holder id; each is emitted as a Reference.identifier (honest substrate
// identity). AuthoredOn comes from the caller's injected clock (deterministic; pass a fixed instant
// for reproducible output).
type CDexTaskMeta struct {
	AuthoredOn time.Time
	Requester  string // data-consumer holder id (provider)
	Owner      string // data-source holder id (facility)
}

const cdexHolderSystem = "http://smarthealth.network/ids/holder"

// cdexParticipantRef wraps a holder id as a Reference.identifier (no literal reference — see CDexTaskMeta).
func cdexParticipantRef(holderID string) map[string]any {
	return map[string]any{"identifier": map[string]any{"system": cdexHolderSystem, "value": holderID}}
}

// validate returns an error if any required CDexTaskMeta field is missing (fail loud at construction,
// not at $validate — a zero-value field would emit a profile-invalid Task).
func (m CDexTaskMeta) validate() error {
	if m.AuthoredOn.IsZero() {
		return fmt.Errorf("cdex: CDexTaskMeta.AuthoredOn is required")
	}
	if m.Requester == "" {
		return fmt.Errorf("cdex: CDexTaskMeta.Requester is required")
	}
	if m.Owner == "" {
		return fmt.Errorf("cdex: CDexTaskMeta.Owner is required")
	}
	return nil
}

// BuildCDexTaskDataRequest builds a CDex Task Data Request (data-request-query): exactly one
// data-query input (cdex-9: a CDex Task Data Request must have EXACTLY ONE data-query input) for
// the named resourceType (FR-24 named type + stated date range, never bulk) bound to patientRef in
// BOTH Task.for and the data-query patient=, plus a purpose-of-use input (TREAT). The CDex
// replacement for the bespoke BuildQuery (now removed). cdex-9 (one data-query per Task) is
// enforced STRUCTURALLY by the single resourceType param (multi-type is unrepresentable at compile
// time). FR-24 names two document types (DiagnosticReport + DocumentReference) ⇒ two Tasks/legs;
// callers build one CDex Task per named type.
func BuildCDexTaskDataRequest(patientRef, resourceType, start, end string, meta CDexTaskMeta) ([]byte, error) {
	if !strings.HasPrefix(patientRef, "Patient/") {
		return nil, fmt.Errorf("cdex: patientRef must be Patient/<id>, got %q", patientRef)
	}
	if err := meta.validate(); err != nil {
		return nil, err
	}
	if !AllowedTypes[resourceType] {
		return nil, fmt.Errorf("cdex: disallowed type %q (no bulk export)", resourceType)
	}
	q := fmt.Sprintf("%s?patient=%s&date=ge%s&date=le%s", resourceType, patientRef, start, end)
	inputs := []map[string]any{{
		"type":        map[string]any{"coding": []any{map[string]any{"system": hrexTemp, "code": "data-query"}}},
		"valueString": q,
	}}
	inputs = append(inputs, map[string]any{
		"type":                 map[string]any{"coding": []any{map[string]any{"system": cdexTemp, "code": "purpose-of-use"}}},
		"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": v3ActReason, "code": "TREAT"}}},
	})
	task := map[string]any{
		"resourceType": "Task",
		"meta":         map[string]any{"profile": []any{cdexTaskProfile}},
		"status":       "requested",
		"intent":       "order",
		"code":         map[string]any{"coding": []any{map[string]any{"system": cdexTemp, "code": "data-request-query"}}},
		"for":          map[string]any{"reference": patientRef},
		"authoredOn":   meta.AuthoredOn.UTC().Format(time.RFC3339),
		"requester":    cdexParticipantRef(meta.Requester),
		"owner":        cdexParticipantRef(meta.Owner),
		"input":        inputs,
	}
	return json.Marshal(task)
}

// CDexQuery is one validated narrow query parsed from a CDex Task Data Request.
type CDexQuery struct {
	ResourceType string
	PatientRef   string
	Start, End   string // FHIR date (YYYY-MM-DD), inclusive
}

// InRange reports whether date falls within this query's inclusive [Start, End] (FR-24).
func (q CDexQuery) InRange(date string) bool {
	d := dateKey(date)
	return d != "" && dateKey(q.Start) <= d && d <= dateKey(q.End)
}

// ParsedCDexRequest is the validated result.
type ParsedCDexRequest struct {
	PatientRef string
	Queries    []CDexQuery
}

// allowedQueryParams is the CLOSED allowlist of query param keys (FR-34) — only the keys the
// builder emits and the facility honors. Anything else (_count/_include/_revinclude/_type/
// category/type/code/modifiers/chained) is rejected; widen here only alongside a builder +
// facility consumer (additive).
var allowedQueryParams = map[string]bool{"patient": true, "date": true}

// ParseCDexTaskDataRequest parses AND validates a CDex Task Data Request for narrowness
// (FR-24/FR-34, AI-1): exactly one data-query input (cdex-9), allowlist resource type + param
// keys, bounded valid non-inverted date range, Task.for == data-query patient, no bulk/$op, no
// encoding evasion. Any violation is an error (the facility returns 403). Replaces the bespoke
// ParseQuery. FR-24 two named types ⇒ two Tasks/legs (one per type); this parser enforces cdex-9
// (one data-query per Task) so a Task with multiple data-query inputs is rejected.
func ParseCDexTaskDataRequest(data []byte) (ParsedCDexRequest, error) {
	var task struct {
		ResourceType string `json:"resourceType"`
		Code         struct {
			Coding []struct{ System, Code string } `json:"coding"`
		} `json:"code"`
		For   struct{ Reference string } `json:"for"`
		Input []struct {
			Type struct {
				Coding []struct{ System, Code string } `json:"coding"`
			} `json:"type"`
			ValueString string `json:"valueString"`
		} `json:"input"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return ParsedCDexRequest{}, fmt.Errorf("cdex: parse: %w", err)
	}
	if task.ResourceType != "Task" {
		return ParsedCDexRequest{}, fmt.Errorf("cdex: expected Task, got %q", task.ResourceType)
	}
	// Enforce the Task.code: only data-request-query is a valid federated query (a
	// data-request-questionnaire or other code must NOT pass the narrowness gate).
	hasQueryCode := false
	for _, c := range task.Code.Coding {
		if c.System == cdexTemp && c.Code == "data-request-query" {
			hasQueryCode = true
		}
	}
	if !hasQueryCode {
		return ParsedCDexRequest{}, fmt.Errorf("cdex: Task.code is not data-request-query")
	}
	forRef := task.For.Reference
	if !strings.HasPrefix(forRef, "Patient/") {
		return ParsedCDexRequest{}, fmt.Errorf("cdex: Task.for missing/invalid")
	}
	out := ParsedCDexRequest{PatientRef: forRef}
	for _, in := range task.Input {
		isDataQuery := false
		for _, c := range in.Type.Coding {
			if c.System == hrexTemp && c.Code == "data-query" {
				isDataQuery = true
			}
		}
		if !isDataQuery {
			continue // purpose-of-use and any other input slice
		}
		q, err := parseNarrowQuery(in.ValueString, forRef)
		if err != nil {
			return ParsedCDexRequest{}, err
		}
		out.Queries = append(out.Queries, q)
	}
	// cdex-9 (error): a CDex Task Data Request must have EXACTLY ONE data-query input.
	// This subsumes the zero-input (no bulk) and the multi-input (FR-24 multi-type ⇒
	// multi-Task/multi-leg) cases.
	if len(out.Queries) != 1 {
		return ParsedCDexRequest{}, fmt.Errorf("cdex: exactly one data-query input required (cdex-9), got %d", len(out.Queries))
	}
	return out, nil
}

// parseNarrowQuery validates ONE data-query valueString against the §3.1 allowlist.
func parseNarrowQuery(raw, forRef string) (CDexQuery, error) {
	if strings.Contains(raw, "$") {
		return CDexQuery{}, fmt.Errorf("cdex: operation not allowed in query %q", raw)
	}
	slash := strings.IndexByte(raw, '?')
	if slash < 0 {
		return CDexQuery{}, fmt.Errorf("cdex: query missing '?' %q", raw)
	}
	rtype, rest := raw[:slash], raw[slash+1:]
	if !AllowedTypes[rtype] { // exact, case-sensitive; rejects encoded/compartment/system
		return CDexQuery{}, fmt.Errorf("cdex: disallowed resource type %q (no bulk export)", rtype)
	}
	dec, err := url.QueryUnescape(rest)
	if err != nil || strings.Contains(dec, "%") { // belt-and-suspenders: no residual % after one decode
		return CDexQuery{}, fmt.Errorf("cdex: encoding evasion in query %q", raw)
	}
	var patient, start, end string
	var patientSet, startSet, endSet bool
	for _, pair := range strings.Split(dec, "&") {
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return CDexQuery{}, fmt.Errorf("cdex: malformed param %q", pair)
		}
		key, val := kv[0], kv[1]
		if strings.ContainsAny(key, ":.") || !allowedQueryParams[key] { // modifiers/chained/unknown
			return CDexQuery{}, fmt.Errorf("cdex: disallowed query param %q", key)
		}
		switch key {
		case "patient":
			if patientSet { // a duplicate patient silently dropping a conflicting value is a wrong-patient risk
				return CDexQuery{}, fmt.Errorf("cdex: duplicate patient param")
			}
			patient, patientSet = val, true
		case "date":
			switch {
			case strings.HasPrefix(val, "ge") || strings.HasPrefix(val, "gt"):
				if startSet { // a duplicate lower bound silently WIDENS the disclosure window (min-necessary)
					return CDexQuery{}, fmt.Errorf("cdex: duplicate lower date bound")
				}
				start, startSet = val[2:], true
			case strings.HasPrefix(val, "le") || strings.HasPrefix(val, "lt"):
				if endSet { // a duplicate upper bound silently WIDENS the disclosure window (min-necessary)
					return CDexQuery{}, fmt.Errorf("cdex: duplicate upper date bound")
				}
				end, endSet = val[2:], true
			default:
				return CDexQuery{}, fmt.Errorf("cdex: date needs ge/gt + le/lt bounds, got %q", val)
			}
		}
	}
	if patient != forRef {
		return CDexQuery{}, fmt.Errorf("cdex: query patient %q != Task.for %q", patient, forRef)
	}
	if start == "" || end == "" {
		return CDexQuery{}, fmt.Errorf("cdex: unbounded date range (FR-24: a stated date range is required)")
	}
	const layout = "2006-01-02"
	st, err := time.Parse(layout, start)
	if err != nil {
		return CDexQuery{}, fmt.Errorf("cdex: invalid start date %q: %w", start, err)
	}
	en, err := time.Parse(layout, end)
	if err != nil {
		return CDexQuery{}, fmt.Errorf("cdex: invalid end date %q: %w", end, err)
	}
	if st.After(en) {
		return CDexQuery{}, fmt.Errorf("cdex: inverted date range %s..%s", start, end)
	}
	return CDexQuery{ResourceType: rtype, PatientRef: forRef, Start: start, End: end}, nil
}

// BuildCDexQueryResult builds the CDex completed-Task response by TRANSITIONING the request Task:
// it echoes the request (code/for/authoredOn/requester/owner/input — all required by
// cdex-task-data-request), flips status to "completed", and adds Task.output referencing a CONTAINED
// US-Core searchset Bundle (CDex Task-Based synchronous fulfillment; cf. CDex example
// Task-cdex-task-example3 = completed + input + output + contained). The inner bundle is
// BuildRecordsBundle's output (KEPT — shared US-Core assembler). Echoing the request (vs building a
// fresh Task) keeps the response a valid cdex-task-data-request (input min=1 retained) and inherits
// the honest requester/owner/authoredOn from the request. SHN's request/response leg model sends a
// distinct completed Task (substrate-id-correlated) rather than PUTting one Task back in place — a
// documented single-Task-lifecycle simplification (the synchronous output wrapper is conformant; an
// async poll/subscribe Task-status lifecycle is an additive follow-on).
// NOTE: the inner bundle is parsed as map[string]any to set its id, so the contained resources'
// JSON key order is NOT preserved — callers must not rely on byte identity of the disclosed records.
func BuildCDexQueryResult(requestTaskJSON, searchsetBundle []byte) ([]byte, error) {
	var task map[string]any
	if err := json.Unmarshal(requestTaskJSON, &task); err != nil {
		return nil, fmt.Errorf("cdex: parse request task: %w", err)
	}
	if rt, _ := task["resourceType"].(string); rt != "Task" {
		return nil, fmt.Errorf("cdex: request is not a Task (got %q)", rt)
	}
	var inner map[string]any
	if err := json.Unmarshal(searchsetBundle, &inner); err != nil {
		return nil, fmt.Errorf("cdex: parse inner bundle: %w", err)
	}
	inner["id"] = "results"
	task["status"] = "completed"
	task["contained"] = []any{inner}
	task["output"] = []any{map[string]any{
		"type":           map[string]any{"coding": []any{map[string]any{"system": hrexTemp, "code": "data-query"}}},
		"valueReference": map[string]any{"reference": "#results"},
	}}
	return json.Marshal(task)
}

// ExtractCDexEvidence pulls the DiagnosticReport + Provenance out of a CDex completed-Task
// response (Task.contained[] searchset). Supersedes the removed ExtractOperativeEvidence.
// Returns the first contained Bundle — BuildCDexQueryResult's
// synchronous fulfillment produces exactly one.
func ExtractCDexEvidence(taskJSON []byte) (drJSON, provJSON []byte, err error) {
	var task struct {
		Contained []json.RawMessage `json:"contained"`
	}
	if e := json.Unmarshal(taskJSON, &task); e != nil {
		return nil, nil, fmt.Errorf("cdex: parse task: %w", e)
	}
	for _, c := range task.Contained {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(c, &rt) == nil && rt.ResourceType == "Bundle" {
			return extractEvidenceFromBundle(c) // shared helper in fedquery.go
		}
	}
	return nil, nil, fmt.Errorf("cdex: completed Task has no contained searchset Bundle")
}

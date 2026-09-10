package shnsdk

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// buildPASOperationResponse retains the request resources that support a locally
// adjudicated response. It performs no fetches and never supplies missing facts.
func buildPASOperationResponse(request, decision []byte, now time.Time) ([]byte, error) {
	if len(request) > pasGraphMaxBytes || len(decision) > pasGraphMaxBytes {
		return nil, pasAssemblyError()
	}
	var req map[string]any
	if err := decodePASObject(request, &req); err != nil {
		return nil, err
	}
	if req["resourceType"] != "Bundle" || req["type"] != "collection" {
		return nil, pasAssemblyError()
	}
	entries, ok := req["entry"].([]any)
	if !ok || len(entries) == 0 {
		return nil, pasAssemblyError()
	}
	var claim map[string]any
	var claimURL string
	for _, v := range entries {
		e, ok := v.(map[string]any)
		if !ok {
			return nil, pasAssemblyError()
		}
		r, ok := e["resource"].(map[string]any)
		if !ok {
			return nil, pasAssemblyError()
		}
		if r["resourceType"] == "ClaimResponse" {
			return nil, pasAssemblyError()
		}
		if r["resourceType"] == "Claim" && claim == nil {
			claim = r
			claimURL, _ = e["fullUrl"].(string)
		}
	}
	claimID, _ := claim["id"].(string)
	if claim == nil || claimURL == "" || !pasSafeResourceID(claimID) {
		return nil, pasAssemblyError()
	}
	var out map[string]any
	if err := decodePASObject(decision, &out); err != nil {
		return nil, err
	}
	var produced []any
	if out["resourceType"] == "ClaimResponse" {
		id, _ := out["id"].(string)
		if id == "" {
			id = fmt.Sprintf("response-%x", sha256.Sum256(decision))[:41]
			out["id"] = id
		}
		produced = []any{map[string]any{"fullUrl": "https://shn.example/fhir/ClaimResponse/" + id, "resource": out}}
		out = map[string]any{"resourceType": "Bundle", "type": "collection", "timestamp": now.UTC().Format(time.RFC3339)}
	} else if out["resourceType"] == "Bundle" {
		produced, _ = out["entry"].([]any)
	} else {
		return nil, pasAssemblyError()
	}
	if len(produced) == 0 {
		return nil, pasAssemblyError()
	}
	// The decision uses exactly the request's patient, insurer and Claim identity.
	// Absolutize only generated references; supplied request entries stay unchanged.
	reference := func(key string) (map[string]any, error) {
		r, ok := claim[key].(map[string]any)
		if !ok {
			return nil, pasAssemblyError()
		}
		ref, ok := r["reference"].(string)
		if !ok || ref == "" || strings.HasPrefix(ref, "#") {
			return nil, pasAssemblyError()
		}
		if !strings.Contains(ref, ":") && !strings.HasPrefix(ref, "#") {
			suffix := "/Claim/" + claimID
			if !strings.HasSuffix(claimURL, suffix) {
				return nil, pasAssemblyError()
			}
			ref = strings.TrimSuffix(claimURL, suffix) + "/" + ref
		}
		return map[string]any{"reference": ref}, nil
	}
	patient, err := reference("patient")
	if err != nil {
		return nil, err
	}
	insurer, err := reference("insurer")
	if err != nil {
		return nil, err
	}
	for _, v := range produced {
		e := v.(map[string]any)
		r := e["resource"].(map[string]any)
		switch r["resourceType"] {
		case "ClaimResponse":
			r["patient"] = patient
			r["insurer"] = insurer
			r["request"] = map[string]any{"reference": claimURL}
		case "Task":
			r["for"] = patient
		}
	}
	out["entry"] = append(produced, entries...)
	if id, ok := req["identifier"]; ok {
		out["identifier"] = id
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	g, err := readPASGraph(raw)
	if err != nil {
		return nil, err
	}
	if err = g.validate(); err != nil {
		return nil, err
	}
	patientURL := patient["reference"].(string)
	if pos := strings.Index(patientURL, "/_history/"); pos >= 0 {
		patientURL = patientURL[:pos]
	}
	target := g.byURL[patientURL]
	if target == nil || target.resource["resourceType"] != "Patient" {
		return nil, pasAssemblyError()
	}
	if !consistentPASGraphSubjects(g, patientURL) {
		return nil, pasAssemblyError()
	}
	return raw, nil
}

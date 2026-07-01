package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OrderDispatchInputs carries the provider data needed to build a conformant CDS Hooks
// order-dispatch request. All resource fields are raw FHIR JSON. The builder wraps each
// inline resource in a prefetch Bundle and emits NO fhirServer field (the non-aggregation
// floor: br-payer resolves everything from prefetch alone, zero callback).
//
// Field placement contract (verified live against br-payer a8bece4):
//   - DeviceRequest rides in prefetch["deviceHistory"] Bundle.
//   - Supplier Organization rides in prefetch["serviceHistory"] Bundle.
//   - Coverage rides in prefetch["coverage"] Bundle; its payor must reference an Organization
//     externally (not #contained) so the payer Org resolves via the coverage Bundle entry.
//   - PerformerRef must exactly match the supplier Organization's id in prefetch (br-payer
//     silently absorbs a mismatch — SHN enforces the ref-match itself).
type OrderDispatchInputs struct {
	// PatientID is the bare patient id (without the "Patient/" prefix); br-payer expects the
	// bare id for context.patientId, and the patient prefetch entry uses it as the resource id.
	PatientID string
	// PatientRef is the full patient reference ("Patient/<id>"), used inside the prefetch patient.
	PatientRef string
	// OrderRef is the full DeviceRequest reference ("DeviceRequest/<id>"), placed in
	// context.dispatchedOrders (an array of string refs, per br-payer's OrderDispatchService).
	OrderRef string
	// PerformerRef is the full Organization reference ("Organization/<id>"), placed in
	// context.performer. Must exactly match the supplier Organization's id in the
	// serviceHistory prefetch Bundle (enforced by test; br-payer won't 400 a mismatch).
	PerformerRef string
	// DeviceRequest is the raw FHIR DeviceRequest JSON. Wrapped in the deviceHistory prefetch Bundle.
	DeviceRequest []byte
	// Supplier is the raw FHIR Organization JSON for the supplying DME Organization.
	// Wrapped in the serviceHistory prefetch Bundle.
	Supplier []byte
	// Coverage is the raw FHIR Coverage JSON. Wrapped in the coverage prefetch Bundle.
	// The Coverage's payor must reference an Organization externally so the payer Org
	// resolves from that same Bundle entry (br-payer requires a resolved payor Organization
	// carrying a valid identifier; inline Coverage.payor.identifier is NOT honored).
	Coverage []byte
	// Payer is the payer Organization identifier (system|value) emitted on the external
	// payer Organization the coverage prefetch Bundle resolves payor to. Pass the identity
	// read from the patient's Coverage, or shnsdk.CMSPayerIdentity for the conformance payer.
	Payer PayerIdentifier
}

// conformantOrderDispatchContext is the CDS Hooks context for an order-dispatch hook
// invocation. dispatchedOrders is an array of string refs (bare resource references,
// NOT inline resources/maps) as required by br-payer's OrderDispatchService validator
// (inline objects are rejected; string refs are resolved from prefetch).
type conformantOrderDispatchContext struct {
	PatientID        string   `json:"patientId"`
	DispatchedOrders []string `json:"dispatchedOrders"`
	Performer        string   `json:"performer"`
}

// conformantOrderDispatchRequest is the full CDS Hooks order-dispatch request. No
// fhirServer field is emitted (non-aggregation floor; br-payer resolves everything
// from the prefetch bundles with zero callback, verified live against br-payer a8bece4).
type conformantOrderDispatchRequest struct {
	Hook         string                         `json:"hook"`
	HookInstance string                         `json:"hookInstance"`
	Context      conformantOrderDispatchContext `json:"context"`
	Prefetch     orderDispatchPrefetch          `json:"prefetch"`
}

// orderDispatchPrefetch carries the four prefetch keys accepted by br-payer's
// order-dispatch-crd service (verified from OrderDispatchService prefetch templates).
// Each resource is wrapped in a FHIR collection Bundle so br-payer's
// findInPrefetch/findInBundle resolver can locate it by id or fullUrl.
type orderDispatchPrefetch struct {
	Patient        json.RawMessage `json:"patient"`
	Coverage       json.RawMessage `json:"coverage"`
	DeviceHistory  json.RawMessage `json:"deviceHistory"`
	ServiceHistory json.RawMessage `json:"serviceHistory"`
}

// conformantOrderDispatchHookInstance is the fixed hook-instance id (deterministic,
// no time/random — mirrors conformantCRDHookInstance for the same reason).
const conformantOrderDispatchHookInstance = "convergence-order-dispatch-hi-1"

// BuildConformantOrderDispatchRequest builds a conformant CDS Hooks order-dispatch
// request that br-payer's order-dispatch-crd service accepts with zero callback.
//
// Self-containment contract (the builder's sole responsibility):
//   - DeviceRequest is placed in prefetch["deviceHistory"] as a collection Bundle entry.
//   - Supplier Organization is placed in prefetch["serviceHistory"] as a collection Bundle entry.
//   - Coverage is placed in prefetch["coverage"] as a collection Bundle entry.
//   - NO fhirServer field is emitted; br-payer resolves all resources from prefetch alone.
//   - context.performer exactly matches in.PerformerRef (caller is responsible for
//     consistency; br-payer silently absorbs a mismatch).
func BuildConformantOrderDispatchRequest(in OrderDispatchInputs) ([]byte, error) {
	// Build a minimal id-only Patient for the patient prefetch entry. br-payer accepts
	// an id-only Patient. The id is the BARE member id (no "Patient/" prefix).
	bareID := strings.TrimPrefix(in.PatientID, "Patient/")
	patientJSON, err := json.Marshal(map[string]string{"resourceType": "Patient", "id": bareID})
	if err != nil {
		return nil, fmt.Errorf("shnsdk: order-dispatch: patient prefetch: %w", err)
	}

	// Build the coverage prefetch Bundle. br-payer's OrderDispatchService requires Coverage.payor
	// to resolve to an Organization carrying a VALID identifier (an inline Coverage.payor.identifier
	// is NOT honored; a #contained payer is not resolved by the dispatch payor gate). So the bundle
	// carries TWO entries: the Coverage with an EXTERNAL payor ref, and the payer Organization it
	// points to (its identifier is the `payer` argument — e.g. the conformance payer
	// urn:oid:2.16.840.1.113883.6.300|00001). Verified live against br-payer a8bece4
	// ("Coverage ... lacks valid payer identifier" without the external payer Org).
	covBundle, err := buildCoverageBundleWithPayer(in.Coverage, in.Payer)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: order-dispatch: coverage bundle: %w", err)
	}

	// Wrap the DeviceRequest in a collection Bundle for the deviceHistory prefetch key.
	drBundle, err := wrapInCollectionBundle(in.DeviceRequest, "device-request")
	if err != nil {
		return nil, fmt.Errorf("shnsdk: order-dispatch: deviceHistory bundle: %w", err)
	}

	// Wrap the supplier Organization in a collection Bundle for the serviceHistory prefetch key.
	orgBundle, err := wrapInCollectionBundle(in.Supplier, "supplier-org")
	if err != nil {
		return nil, fmt.Errorf("shnsdk: order-dispatch: serviceHistory bundle: %w", err)
	}

	req := conformantOrderDispatchRequest{
		Hook:         "order-dispatch",
		HookInstance: conformantOrderDispatchHookInstance,
		Context: conformantOrderDispatchContext{
			// context.patientId is the BARE patient id (no "Patient/" prefix); br-payer's
			// OrderDispatchService follows the same CDS Hooks convention as order-select.
			PatientID:        strings.TrimPrefix(in.PatientID, "Patient/"),
			DispatchedOrders: []string{in.OrderRef},
			Performer:        in.PerformerRef,
		},
		Prefetch: orderDispatchPrefetch{
			Patient:        json.RawMessage(patientJSON),
			Coverage:       json.RawMessage(covBundle),
			DeviceHistory:  json.RawMessage(drBundle),
			ServiceHistory: json.RawMessage(orgBundle),
		},
	}
	return json.Marshal(req)
}

// wrapInCollectionBundle wraps a single FHIR resource JSON in a minimal FHIR collection
// Bundle with one entry. The entry fullUrl is "<ResourceType>/<id>" — the SAME form as the
// bare reference br-payer's order-dispatch context carries — because br-payer's
// ResourceResolver.findInBundle resolves a `DeviceRequest/<id>` reference by matching
// `entry.fullUrl == reference` (the by-id fallback does NOT fire for these prefetch bundles;
// a urn:shn: fullUrl leaves the order unresolvable → "dispatchedOrders ... could not be
// resolved"). Verified live against br-payer a8bece4.
func wrapInCollectionBundle(resourceJSON []byte, _ string) ([]byte, error) {
	bundle := conformantCDSBundle{
		ResourceType: "Bundle",
		Type:         "collection",
		Entry:        []conformantBundleEntry{collectionEntry(resourceJSON)},
	}
	return json.Marshal(bundle)
}

// collectionEntry builds one collection-Bundle entry whose fullUrl is "<ResourceType>/<id>"
// (the reference form br-payer resolves by). Falls back to the bare id when resourceType is
// absent (degenerate; the by-id fallback may still match).
func collectionEntry(resourceJSON []byte) conformantBundleEntry {
	rt, id := extractResourceTypeAndID(resourceJSON)
	fullURL := id
	if rt != "" && id != "" {
		fullURL = rt + "/" + id
	}
	return conformantBundleEntry{FullURL: fullURL, Resource: json.RawMessage(resourceJSON)}
}

// buildCoverageBundleWithPayer builds the coverage prefetch Bundle for order-dispatch: the
// Coverage with its payor rewritten to an EXTERNAL reference (Organization/cms-payer) PLUS the
// payer Organization (identifier from the `payer` argument) as a sibling entry. br-payer's dispatch payor gate
// requires a resolvable external payer Org with a valid identifier (a #contained payer or an
// inline identifier is rejected). Both entries carry "<Type>/<id>" fullUrls (collectionEntry).
func buildCoverageBundleWithPayer(coverageJSON []byte, payer PayerIdentifier) ([]byte, error) {
	var cov map[string]json.RawMessage
	if err := json.Unmarshal(coverageJSON, &cov); err != nil {
		return nil, err
	}
	// Rewrite payor to the external payer Organization reference (replacing the #contained ref).
	payor, err := json.Marshal([]map[string]string{{"reference": "Organization/" + conformantPayerOrgID}})
	if err != nil {
		return nil, err
	}
	cov["payor"] = payor
	// Drop any contained payer Org (now carried as a top-level bundle entry instead).
	delete(cov, "contained")
	covOut, err := json.Marshal(cov)
	if err != nil {
		return nil, err
	}
	// The external payer Organization with the payer identifier (br-payer's payor gate reads it).
	payerOrg, err := json.Marshal(map[string]any{
		"resourceType": "Organization",
		"id":           conformantPayerOrgID,
		"name":         conformantPayerOrgName,
		"identifier":   []map[string]string{{"system": payer.System, "value": payer.Value}},
	})
	if err != nil {
		return nil, err
	}
	bundle := conformantCDSBundle{
		ResourceType: "Bundle",
		Type:         "collection",
		Entry: []conformantBundleEntry{
			collectionEntry(covOut),
			collectionEntry(payerOrg),
		},
	}
	return json.Marshal(bundle)
}

// extractResourceTypeAndID reads resourceType + id from a FHIR resource JSON.
func extractResourceTypeAndID(resourceJSON []byte) (resourceType, id string) {
	var m struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(resourceJSON, &m); err != nil {
		return "", ""
	}
	return m.ResourceType, m.ID
}

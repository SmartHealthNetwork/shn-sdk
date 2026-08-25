package shnsdk

import (
	"encoding/json"
	"strings"
)

// conformant_parse.go — the payer-side parsers for the CONFORMANT PA request shapes the
// Originator (RunPriorAuth) sends: the CRD order-select request is a CDS Hooks request whose
// context.draftOrders is a FHIR collection Bundle, and the PAS $submit request is a conformant
// Da Vinci Claim Bundle (Claim + Patient + Coverage + payor Organization + ServiceRequest
// [+ QuestionnaireResponse] [+ DiagnosticReport]). The conformant request shape is the sole PA
// request-parse contract on the Responder.

// parseConformantOrderSelectSR extracts the first ServiceRequest JSON from a CONFORMANT CDS
// Hooks order-select request (context.draftOrders a FHIR collection Bundle whose entries carry
// the resources). Returns the first ServiceRequest found, or (nil, false) if none. Mirrors
// engine.extractConformantSR / firstServiceRequest's first-SR semantics.
//
// Returning the FIRST ServiceRequest only is by design: the payer-side bind below
// (parseServiceRequestSubject + Coverage beneficiary + context.patientId three-way) is the
// member fence; a multi-entry Bundle is the originator's responsibility (the substrate ingress
// rejects a rogue second SR before sealing, and a partner-run originator is the partner's edge).
func parseConformantOrderSelectSR(body []byte) ([]byte, bool) {
	var req struct {
		Context struct {
			DraftOrders struct {
				Entry []struct {
					Resource json.RawMessage `json:"resource"`
				} `json:"entry"`
			} `json:"draftOrders"`
		} `json:"context"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false
	}
	for _, e := range req.Context.DraftOrders.Entry {
		if isConformantResourceType(e.Resource, "ServiceRequest") {
			return e.Resource, true
		}
	}
	return nil, false
}

// conformantOrderSelectCoverageAndPatient extracts the Coverage prefetch JSON and the bare
// context.patientId from a CONFORMANT CDS Hooks order-select request — the two other legs of the
// three-way member fence the CRD handler runs (alongside the ServiceRequest subject). Returns
// (cov, patientID, false) if the body is unparseable or carries no Coverage prefetch.
func conformantOrderSelectCoverageAndPatient(body []byte) (covJSON []byte, patientID string, ok bool) {
	var req struct {
		Context struct {
			PatientID string `json:"patientId"`
		} `json:"context"`
		Prefetch struct {
			Coverage json.RawMessage `json:"coverage"`
		} `json:"prefetch"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", false
	}
	if len(req.Prefetch.Coverage) == 0 {
		return nil, "", false
	}
	return req.Prefetch.Coverage, req.Context.PatientID, true
}

// conformantClaimSubmit holds what the conformant $submit handler needs from a PAS Claim
// Bundle: the bound member (Claim.patient ref), the QuestionnaireResponse JSON the adjudicator
// reads, and whether a DiagnosticReport is present (the pended/supplemental branch). It is the
// subset of the conformant parse the Responder's PA decision depends on (the engine's
// conformantPASSubjects also carries srJSON/member; the SDK Responder needs the patient ref and
// the QR/DR facts).
type conformantClaimSubmit struct {
	claimPatient string // Claim.patient.reference — "Patient/<member>" or "<base>/Patient/<member>" (see pasMemberFromRef)
	srSubject    string // ServiceRequest.subject.reference (REQUIRED — intra-bundle bind)
	qrJSON       []byte // the QuestionnaireResponse resource, or nil (optional on this leg)
	qrSubject    string // QuestionnaireResponse.subject.reference, or "" if no QR
	hasDR        bool   // a DiagnosticReport entry is present (FR-20 pended branch)
	drSubject    string // DiagnosticReport.subject.reference, or "" if no DR
}

// parseConformantClaimSubmit does ONE pass over a CONFORMANT PAS Claim Bundle, indexing entries
// by resourceType (tolerating the Patient / Coverage / payor Organization / Practitioner entries
// a real Da Vinci $submit carries). It mirrors engine.parseConformantPASSubjects' one-pass index,
// surfacing the subset the Responder's PriorAuth decision needs: Claim.patient, the QR JSON, and
// whether a DiagnosticReport is present. Returns (claim, false) if the body is not a Bundle or
// yields no Claim.patient (→ 400 at the caller).
func parseConformantClaimSubmit(body []byte) (conformantClaimSubmit, bool) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.ResourceType != "Bundle" {
		return conformantClaimSubmit{}, false
	}
	subjectRef := func(resource json.RawMessage, field string) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(resource, &m); err != nil {
			return ""
		}
		var v struct {
			Reference string `json:"reference"`
		}
		_ = json.Unmarshal(m[field], &v)
		return v.Reference
	}
	var cs conformantClaimSubmit
	for _, e := range probe.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		switch rt.ResourceType {
		case "Claim":
			cs.claimPatient = subjectRef(e.Resource, "patient")
		case "ServiceRequest":
			cs.srSubject = subjectRef(e.Resource, "subject")
		case "QuestionnaireResponse":
			cs.qrJSON = e.Resource
			cs.qrSubject = subjectRef(e.Resource, "subject")
		case "DiagnosticReport":
			cs.hasDR = true
			cs.drSubject = subjectRef(e.Resource, "subject")
		}
	}
	if cs.claimPatient == "" {
		return conformantClaimSubmit{}, false
	}
	return cs, true
}

// bindConformantClaimSubject enforces intra-bundle patient consistency on a parsed conformant
// Claim Bundle (the SDK Responder's edge fence, mirroring the deleted minimized bindBundleSubject
// AND engine.parseConformantPASSubjects' three-way bind for the conformant shape): the
// ServiceRequest subject, the QuestionnaireResponse subject (when a QR is present — REQUIRED
// then, a subjectless QR could approve a Claim for a different patient), and the DiagnosticReport
// subject (when present) must all reference the SAME member as Claim.patient. Returns (0, "") on
// accept, or (HTTP status, message) to write.
//
// NOTE — divergence from the substrate gateway, by design (same rationale as handleEligibility's
// NOTE): the gateway additionally resolves Claim.patient against its patient registry and rejects
// when the derived PCI != token subject. The SDK Responder has no registry; that defense-in-depth
// layer is structurally unavailable here. ALL bundle-internal consistency checks ARE enforced.
func bindConformantClaimSubject(cs conformantClaimSubmit) (status int, msg string) {
	member := pasMemberFromRef(cs.claimPatient)
	if pasMemberFromRef(cs.srSubject) != member {
		return 403, "inconsistent patient in PAS bundle"
	}
	if cs.qrJSON != nil {
		if cs.qrSubject == "" {
			return 403, "PAS bundle QuestionnaireResponse missing subject"
		}
		if pasMemberFromRef(cs.qrSubject) != member {
			return 403, "inconsistent patient in PAS bundle"
		}
	}
	if cs.hasDR && pasMemberFromRef(cs.drSubject) != member {
		return 403, "inconsistent patient in PAS bundle"
	}
	return 0, ""
}

// pasMemberFromRef reads the bare member id out of a patient reference in EITHER spelling a
// conformant PAS bundle may legally carry: the relative "Patient/<member>", or the absolute
// "<base>/Patient/<member>" that BuildConformantClaimBundle emits under AbsoluteRefs (a real Da
// Vinci payer resolves relative references against its OWN server base, so bundle-internal ones
// must be absolute for it to find them at all).
//
// That absolutization is deliberately NOT uniform, and cannot be made so: the patient-compartment
// ANCHOR references — Coverage.beneficiary and ServiceRequest/DeviceRequest.subject — stay
// relative, because a payer's Rule CQL retrieves those resources in `context Patient` and an
// absolute anchor breaks the compartment match. So one correct bundle legitimately spells the
// SAME patient two ways, and Claim.patient is one of the absolute ones.
//
// The consistency fence's property is "the same MEMBER", not "the same string", so it has to
// compare identities rather than spellings: a raw prefix trim reads two spellings of one patient
// as two patients and refuses a valid bundle. This is NOT a relaxation of the fence. A reference
// naming a genuinely different member still mismatches in either spelling, and a non-Patient
// reference (no "Patient/" segment) is returned verbatim, so it cannot collide with a member id.
//
// Identical to the substrate gateway's own pasMemberFromRef (gateway/engine/pas_native.go), which
// already resolved this for the native lane. The two fences are twins on purpose and must decide
// the same bundle the same way.
func pasMemberFromRef(ref string) string {
	if i := strings.LastIndex(ref, "Patient/"); i >= 0 {
		return ref[i+len("Patient/"):]
	}
	return ref
}

// conformantBundleMember returns the bare member id bound by a conformantClaimSubmit, in either
// reference spelling (see pasMemberFromRef). Small helper so the Responder/ledger key on the same
// canonical member the engine binds — a ledger keyed on the raw reference would file one patient
// under two keys depending on which lane submitted.
func conformantBundleMember(cs conformantClaimSubmit) string {
	return pasMemberFromRef(cs.claimPatient)
}

// conformantUpdateFacts holds the cross-resource FR-21/FR-32 facts the CONFORMANT
// amended-re-POST (pas-claim-update) inbound gate enforces against — the fields
// parseConformantClaimSubmit does NOT surface. Mirrors engine.conformantUpdateFacts
// (gateway/engine/pas_native.go) for the conformant shape.
type conformantUpdateFacts struct {
	provenanceJSON     []byte   // the Provenance resource bytes, or nil (FR-32: REQUIRED on the update leg)
	provenanceAgents   []string // Provenance.agent[].who.reference
	provenanceTargets  []string // Provenance.target[].reference
	hasDR              bool     // a DiagnosticReport entry is present (DR-variant supplemental data)
	diagnosticReportID string   // DiagnosticReport.id
	qrID               string   // QuestionnaireResponse.id (the QR-variant supplemental data)
	relatedClaim       string   // Claim.related[0].claim.identifier.value (the amendment's distinguishing field, FR-21)
}

// parseConformantUpdateFacts does ONE pass over a CONFORMANT amended-re-POST Bundle, extracting the
// FR-21/FR-32 cross-resource facts the update gate enforces against (Claim.related[prior], the
// supplemental DiagnosticReport id, the amended QR id, and the Provenance agents/targets). It
// tolerates the full conformant entry set (Patient/Coverage/Org/Practitioner are present and
// ignored). Mirrors engine.parseConformantPASUpdateFacts. Returns (facts, false) on a malformed /
// non-Bundle body.
func parseConformantUpdateFacts(body []byte) (conformantUpdateFacts, bool) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.ResourceType != "Bundle" {
		return conformantUpdateFacts{}, false
	}
	var f conformantUpdateFacts
	for _, e := range probe.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		switch rt.ResourceType {
		case "Claim":
			var c struct {
				Related []struct {
					Claim struct {
						Identifier struct {
							Value string `json:"value"`
						} `json:"identifier"`
					} `json:"claim"`
				} `json:"related"`
			}
			_ = json.Unmarshal(e.Resource, &c)
			if len(c.Related) > 0 {
				f.relatedClaim = c.Related[0].Claim.Identifier.Value
			}
		case "QuestionnaireResponse":
			var qr struct {
				Id string `json:"id"`
			}
			_ = json.Unmarshal(e.Resource, &qr)
			f.qrID = qr.Id
		case "DiagnosticReport":
			var dr struct {
				Id string `json:"id"`
			}
			_ = json.Unmarshal(e.Resource, &dr)
			f.hasDR = true
			f.diagnosticReportID = dr.Id
		case "Provenance":
			f.provenanceJSON = e.Resource
			var prov struct {
				Target []struct {
					Reference string `json:"reference"`
				} `json:"target"`
				Agent []struct {
					Who struct {
						Reference string `json:"reference"`
					} `json:"who"`
				} `json:"agent"`
			}
			_ = json.Unmarshal(e.Resource, &prov)
			for _, tgt := range prov.Target {
				if tgt.Reference != "" {
					f.provenanceTargets = append(f.provenanceTargets, tgt.Reference)
				}
			}
			for _, a := range prov.Agent {
				if a.Who.Reference != "" {
					f.provenanceAgents = append(f.provenanceAgents, a.Who.Reference)
				}
			}
		}
	}
	return f, true
}

// isConformantResourceType reports whether the FHIR resource JSON has the given resourceType.
func isConformantResourceType(resource json.RawMessage, want string) bool {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	_ = json.Unmarshal(resource, &probe)
	return probe.ResourceType == want
}

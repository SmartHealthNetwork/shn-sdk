package shnsdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PriorAuthRequest is the dev-VISIBLE input to RunPriorAuth: the member to prior-auth
// FOR, the ordering NPI, the clinical answers that drive the DTR fill / adjudication
// outcome, AND the order being prior-authed (procedure + diagnosis). By design,
// the values that drive the outcome are visible inputs — the order details
// and the clinical context are both supplied here, never hardcoded inside RunPriorAuth.
// SandboxUC03Order + SandboxUC03Context provide the MBR-COVERED→approved values.
type PriorAuthRequest struct {
	Member string
	DOB    string
	Family string
	NPI    string
	// Clinical drives the DTR FillQuestionnaire answers (and thus the payer's outcome).
	Clinical ClinicalContext
	// The order being prior-authed (sandbox defaults via SandboxUC03Order).
	ProcedureCPT     string
	ProcedureDisplay string
	DiagnosisICD10   string
}

// SandboxUC03Context returns the ClinicalContext that drives the sandbox
// MBR-COVERED path to "approved": 6 weeks of conservative therapy (≥6), prior
// imaging present, no neuro deficit. These mirror the substrate's MBR-COVERED
// clinical fixture (internal/gateway holderdata) so the SDK fills the questionnaire
// with answers the sandbox payer adjudicates as approved.
func SandboxUC03Context() ClinicalContext {
	return ClinicalContext{
		ConditionCode:            "M51.16",
		ConditionRef:             "Condition/cond-m5116",
		ConservativeTherapyWeeks: 6,
		ConservativeTherapyRef:   "Observation/obs-pt-weeks",
		ConservativeDate:         "2026-05-20",
		NeuroDeficit:             false,
		NeuroDeficitRef:          "Observation/obs-neuro",
		PriorImaging:             true,
		PriorImagingRef:          "DiagnosticReport/dr-xray",
	}
}

// SandboxUC04Context drives the sandbox MBR-UC04 path to PENDED on exchange-1: the
// auto-approval answers PLUS PriorSurgery=true, which the payer pends awaiting an
// operative DiagnosticReport. weeks=6 means it APPROVES once that report is attached
// via the ClaimUpdate. Mirrors the substrate MBR-UC04 fixture (internal/gateway/holderdata.go).
func SandboxUC04Context() ClinicalContext {
	cc := SandboxUC03Context()
	cc.PriorSurgery = true
	cc.PriorSurgeryRef = "Procedure/proc-laminectomy"
	return cc
}

// SandboxUC08Context drives the sandbox MBR-UC08 path to DENIED: only 4 weeks of
// conservative therapy (< 6), no prior surgery, not high-disability → the payer denies.
// Mirrors the substrate MBR-UC08 fixture.
func SandboxUC08Context() ClinicalContext {
	cc := SandboxUC03Context()
	cc.ConservativeTherapyWeeks = 4
	return cc
}

// SandboxContextFor returns the sandbox ClinicalContext for a known sandbox member —
// the ONE place persona→answers is paired (so the CLI/doctor cannot mispair). The
// ANSWERS, not the member id, drive the payer's outcome (FR-35): MBR-COVERED →
// approved, MBR-UC04 → pended (then approved on amend), MBR-UC08 → denied. ok=false
// for an unknown member.
func SandboxContextFor(memberID string) (ClinicalContext, bool) {
	switch memberID {
	case "MBR-COVERED":
		return SandboxUC03Context(), true
	case "MBR-UC04":
		return SandboxUC04Context(), true
	case "MBR-UC08":
		return SandboxUC08Context(), true
	default:
		return ClinicalContext{}, false
	}
}

// SandboxUC03Order returns the sandbox prior-auth order: a lumbar-spine MRI without
// contrast (CPT 72148 / ICD-10-CM M51.16), the order that requires PA in the sandbox.
func SandboxUC03Order() (cpt, display, icd10 string) {
	return "72148", "MRI lumbar spine w/o contrast", "M51.16"
}

// RunPriorAuth runs a full prior-authorization through the substrate — the
// CRD→DTR→PAS orchestrator — and returns the outcome. It drives three sealed
// round-trips, each via runLeg (the RunEligibility sealed-leg template):
//
//	LEG 1 CRD  provider-tpo/crd-order-select        → payer-coverage/crd-cards
//	           (no-PA-required short-circuits here; DTR/PAS never run)
//	LEG 2 DTR  provider-tpo/dtr-questionnaire-fetch → payer-coverage/dtr-questionnaire
//	LEG 3 PAS  provider-tpo/pas-submit              → payer-coverage/pas-response
//
// The order (procedure/diagnosis) and the clinical answers come from req — dev-visible
// inputs, never conjured here. Every error is leg-attributed
// ("<leg>: <step>: <err>") so a caller can tell which leg + step broke. Authority is
// evaluated per leg (each round-trip mints + verifies its own token bound to its
// ciphertext, AI-2/AI-11). It uses ONLY the SDK's own primitives (no internal/).
func (id Identity) RunPriorAuth(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, req PriorAuthRequest) (PriorAuthResult, error) {
	if c == nil {
		c = http.DefaultClient
	}
	patientRef := "Patient/" + req.Member
	coverageRef := "Coverage/" + req.Member

	// Resolve the patient PCI (AI-5) — the same patient binds every leg's token.
	pci := ResolvePCI(req.Member, req.DOB, req.Family)

	// The CRD-leg inputs: the draft order (ServiceRequest) + the Coverage prefetch.
	// Built from the dev-visible order in req, not hardcoded.
	srJSON, err := BuildServiceRequest(req.ProcedureCPT, req.ProcedureDisplay, req.DiagnosisICD10, patientRef)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: build service request: %w", err)
	}
	covJSON, err := BuildCoverage(patientRef, coverageRef)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: build coverage: %w", err)
	}

	// LEG 1 — CRD order-select.
	crdReq, err := BuildOrderSelectRequest(srJSON, covJSON, patientRef)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: build order-select request: %w", err)
	}
	crdResp, err := id.runLeg(ctx, c, ep, payer, pci,
		"crd-order-select", "crd-order-select", "crd-cards", crdReq)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: %w", err)
	}
	paRequired, canonical, err := ParseCards(crdResp)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: parse cards: %w", err)
	}
	if !paRequired {
		// No PA needed for this order — terminal, short-circuit (no DTR/PAS legs).
		return PriorAuthResult{Outcome: "no-pa-required"}, nil
	}

	// LEG 2 — DTR questionnaire fetch + local auto-fill.
	dtrReq, err := BuildQuestionnaireFetch(canonical)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: build fetch request: %w", err)
	}
	dtrResp, err := id.runLeg(ctx, c, ep, payer, pci,
		"dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-questionnaire", dtrReq)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: %w", err)
	}
	fetchedURL, err := ParseQuestionnaireURL(dtrResp)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: parse questionnaire url: %w", err)
	}
	if fetchedURL != canonical {
		// Canonical-substitution guard (F5): the fetched questionnaire must be the one
		// the CRD card advertised, else the payer swapped questionnaires under us.
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: fetched questionnaire %q != advertised canonical %q", fetchedURL, canonical)
	}
	qrJSON, err := FillQuestionnaire(dtrResp, req.Clinical, QRContext{
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		OrderRef:    "ServiceRequest/sr-" + req.Member,
		Authored:    id.now(),
	})
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: fill questionnaire: %w", err)
	}

	// LEG 3 — PAS submit.
	var pasCorrRaw [16]byte
	if _, err := rand.Read(pasCorrRaw[:]); err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-submit: generate claim correlation id: %w", err)
	}
	pasCorr := hex.EncodeToString(pasCorrRaw[:])
	bundleJSON, err := BuildClaimBundle(qrJSON, srJSON, patientRef, coverageRef, pasCorr, id.now())
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-submit: build claim bundle: %w", err)
	}
	// Pass pasCorr as the envelope correlationID so the payer ledger keys the
	// pended claim on the same value the ClaimUpdate's Claim.related references
	// (pasCorr). The substrate's own originate path does the same.
	pasResp, err := id.runLegWithCorr(ctx, c, ep, payer, pci,
		"pas-claim", "pas-submit", "pas-response", pasCorr, bundleJSON)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-submit: %w", err)
	}
	result, err := parsePASOutcome(pasResp)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-submit: parse claim response: %w", err)
	}
	if result.Outcome == "pended" {
		// Fill the serializable resume handle from this leg's context: the
		// submit correlation the ClaimUpdate.related[] references, the bound subject,
		// and the submit QR/SR the update re-includes unchanged.
		result.Resume = &PriorAuthResume{
			OriginalCorrelationID: pasCorr,
			PatientRef:            patientRef,
			CoverageRef:           coverageRef,
			SubjectPCI:            pci,
			QRJSON:                json.RawMessage(qrJSON),
			SRJSON:                json.RawMessage(srJSON),
			NeededItems:           result.NeededItems,
		}
	}
	return result, nil
}

// runLeg runs ONE sealed request/response round-trip through the substrate and
// returns the decrypted response payload. It is the generalization of RunEligibility's
// sealed-leg plumbing: resolve nothing (caller passes pci) → seal the payload (AI-2:
// seal-then-authorize) → Authorize the request leg bound to sha256hex(ciphertext) →
// route through the Hub → assert envelope metadata → VerifyBound the response leg →
// Open. Every error names the STEP (so the caller can prefix the leg). The request
// frame is always "provider-tpo"; the response frame is always "payer-coverage" (the
// payer-side frame the substrate's hubsvc.responseOp pins).
//
//	txType  — envelope TransactionType (e.g. "crd-order-select", "pas-claim")
//	reqOp   — request-leg authz operation (e.g. "crd-order-select", "pas-submit")
//	respOp  — response-leg authz operation (e.g. "crd-cards", "pas-response")
func (id Identity) runLeg(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, pci, txType, reqOp, respOp string, payload []byte) ([]byte, error) {
	var corrRaw [16]byte
	if _, err := rand.Read(corrRaw[:]); err != nil {
		return nil, fmt.Errorf("generate correlation id: %w", err)
	}
	return id.runLegWithCorr(ctx, c, ep, payer, pci, txType, reqOp, respOp, hex.EncodeToString(corrRaw[:]), payload)
}

// runLegWithCorr is runLeg with a caller-supplied correlationID. Used when the
// envelope correlationID must equal the bundle's own claim correlation — specifically
// the pas-claim leg, where the payer ledger keys the pended claim on the ENVELOPE
// correlationID and the follow-up ClaimUpdate's Claim.related references it by the
// same value (pasCorr). All other legs use runLeg (fresh random correlation each leg).
func (id Identity) runLegWithCorr(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, pci, txType, reqOp, respOp, correlationID string, payload []byte) ([]byte, error) {
	now := id.now()

	// Seal FIRST so the ciphertext exists (AI-2: seal-then-authorize).
	meta := Metadata{
		Sender:          id.HolderID,
		Recipient:       payer.ID,
		TransactionType: txType,
		AuthorityFrame:  "provider-tpo",
		Timestamp:       now.UTC().Format(time.RFC3339),
		CorrelationID:   correlationID,
	}
	env, err := Seal(meta, payload, payer.EncPub)
	if err != nil {
		return nil, fmt.Errorf("seal envelope: %w", err)
	}

	// Authorize the request leg bound to THIS ciphertext.
	reqHash := sha256.Sum256(env.Ciphertext)
	tok, err := id.Authorize(ctx, c, ep.AuthzURL, AuthorizeRequest{
		Frame:         "provider-tpo",
		Operation:     reqOp,
		SubjectPCI:    pci,
		CorrelationID: correlationID,
		PayloadHash:   hex.EncodeToString(reqHash[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("authorize request leg: %w", err)
	}
	tokJSON, err := json.Marshal(tok)
	if err != nil {
		return nil, fmt.Errorf("marshal authz token: %w", err)
	}
	env.Metadata.AuthzToken = string(tokJSON)
	envBytes, err := EncodeEnvelope(env)
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}

	// Route through the Hub (carries a holder assertion for audience "hub").
	hubAssertion, err := id.Assertion("hub", now, MaxAssertionTTL)
	if err != nil {
		return nil, fmt.Errorf("build hub assertion: %w", err)
	}
	hubReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.HubURL+"/route", bytes.NewReader(envBytes))
	if err != nil {
		return nil, fmt.Errorf("build /route request: %w", err)
	}
	hubReq.Header.Set("Content-Type", "application/json")
	hubReq.Header.Set("X-Holder-Assertion", hubAssertion)

	hubResp, err := c.Do(hubReq)
	if err != nil {
		return nil, fmt.Errorf("POST /route: %w", err)
	}
	defer hubResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(hubResp.Body, MaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read /route response: %w", err)
	}
	if hubResp.StatusCode < 200 || hubResp.StatusCode >= 300 {
		return nil, fmt.Errorf("/route returned %d: %s", hubResp.StatusCode, respBody)
	}

	// Decode + verify the response envelope.
	respEnv, err := DecodeEnvelope(respBody)
	if err != nil {
		return nil, fmt.Errorf("decode response envelope: %w", err)
	}
	if respEnv.Metadata.CorrelationID != correlationID {
		return nil, fmt.Errorf("response correlationId %q != request %q", respEnv.Metadata.CorrelationID, correlationID)
	}
	if respEnv.Metadata.Recipient != id.HolderID {
		return nil, fmt.Errorf("response recipient %q != our holderID %q", respEnv.Metadata.Recipient, id.HolderID)
	}
	if respEnv.Metadata.TransactionType != txType {
		return nil, fmt.Errorf("response transactionType %q != %q", respEnv.Metadata.TransactionType, txType)
	}
	if respEnv.Metadata.Sender != payer.ID {
		return nil, fmt.Errorf("response sender %q != expected payer %q", respEnv.Metadata.Sender, payer.ID)
	}

	// Verify the response leg's token is bound to this exchange (H1): payer-coverage /
	// respOp, same correlation, holder=payer, subject=pci, payloadHash bound (AI-2).
	var respTok Token
	if err := json.Unmarshal([]byte(respEnv.Metadata.AuthzToken), &respTok); err != nil {
		return nil, fmt.Errorf("unmarshal response authz token: %w", err)
	}
	respHash := sha256.Sum256(respEnv.Ciphertext)
	if err := VerifyBound(
		respTok,
		payer.AuthzPub,
		now,
		"payer-coverage",
		respOp,
		correlationID,
		payer.ID,
		pci,
		hex.EncodeToString(respHash[:]),
	); err != nil {
		return nil, fmt.Errorf("verify response token: %w", err)
	}

	plaintext, err := Open(respEnv, id.EncPub, id.EncPriv)
	if err != nil {
		return nil, fmt.Errorf("open response envelope: %w", err)
	}
	return plaintext, nil
}

// SupplementalReport is the NEW clinical evidence a ClaimUpdate amendment attaches,
// plus its FR-32 provenance facts. ProvenanceAgent is REQUIRED: the payer REJECTS
// supplemental data without Provenance, so ResumePriorAuth validates it BEFORE
// sealing and fails with a clear error — the dev meets FR-32 as a named
// precondition, not a cryptic three-legs-deep payer rejection.
type SupplementalReport struct {
	ReportID        string // the DiagnosticReport id (e.g. "dr-uc04-operative")
	CPT             string // procedure code (e.g. "72148")
	Display         string // procedure display
	ProvenanceAgent string // FR-32 source attribution, e.g. "Organization/<holderID>" — REQUIRED
}

// SandboxUC04Report returns the operative DiagnosticReport + provenance facts that
// drive MBR-UC04's pend to "approved". Mirrors the substrate fixture
// (SupplementalReport("MBR-UC04") → fhirmap.BuildDiagnosticReport).
func SandboxUC04Report() SupplementalReport {
	return SupplementalReport{
		ReportID:        "dr-uc04-operative",
		CPT:             "72148",
		Display:         "MRI lumbar spine w/o contrast",
		ProvenanceAgent: "Organization/provider",
	}
}

// ResumePriorAuth drives the exchange-2 ClaimUpdate from a pended PA's resume
// handle: validate supp (ProvenanceAgent present → else error, no wire) → build the
// operative DiagnosticReport + Provenance → BuildClaimUpdateBundle (reusing the submit
// QR/SR unchanged, related[] → the original submit correlation, FR-21) → ONE sealed
// round-trip via runLeg under pas-claim-update / pas-update-submit / pas-update-response
// → parse the response. The outcome set is approved | pended | denied: the payer can
// release an INSUFFICIENT amendment back to PENDED (with a still-usable Resume), so
// re-resume is a valid follow-up — not an error. Errors are leg-attributed.
func (id Identity) ResumePriorAuth(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, resume PriorAuthResume, supp SupplementalReport) (PriorAuthResult, error) {
	if c == nil {
		c = http.DefaultClient
	}
	// FR-32 precondition: supplemental data MUST carry provenance attribution. Fail loud
	// BEFORE sealing anything rather than letting the payer reject it three legs deep.
	if supp.ProvenanceAgent == "" {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: SupplementalReport.ProvenanceAgent is required (FR-32: supplemental data must be attributed)")
	}
	if supp.ReportID == "" {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: SupplementalReport.ReportID is required")
	}

	drJSON, err := BuildDiagnosticReport(supp.ReportID, resume.PatientRef, supp.CPT, supp.Display)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: build diagnostic report: %w", err)
	}
	provJSON, err := BuildProvenance("DiagnosticReport/"+supp.ReportID, supp.ProvenanceAgent, id.now())
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: build provenance: %w", err)
	}

	var corrRaw [16]byte
	if _, err := rand.Read(corrRaw[:]); err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: generate update correlation id: %w", err)
	}
	updateCorr := hex.EncodeToString(corrRaw[:])

	bundleJSON, err := BuildClaimUpdateBundle(resume.QRJSON, resume.SRJSON, drJSON, provJSON,
		resume.PatientRef, resume.CoverageRef, updateCorr, resume.OriginalCorrelationID, id.now())
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: build claim update bundle: %w", err)
	}

	updResp, err := id.runLeg(ctx, c, ep, payer, resume.SubjectPCI,
		"pas-claim-update", "pas-update-submit", "pas-update-response", bundleJSON)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: %w", err)
	}
	result, err := parsePASOutcome(updResp)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: parse update response: %w", err)
	}
	if result.Outcome == "pended" {
		// Insufficient amendment — released back to PENDED. Carry a still-usable Resume
		// (same original correlation + the same QR/SR) so the dev can re-resume.
		result.Resume = &PriorAuthResume{
			OriginalCorrelationID: resume.OriginalCorrelationID,
			PatientRef:            resume.PatientRef,
			CoverageRef:           resume.CoverageRef,
			SubjectPCI:            resume.SubjectPCI,
			QRJSON:                resume.QRJSON,
			SRJSON:                resume.SRJSON,
			NeededItems:           result.NeededItems,
		}
	}
	return result, nil
}

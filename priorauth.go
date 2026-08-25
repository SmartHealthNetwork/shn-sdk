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
// FOR, the ordering NPI, the clinical answers that fill the DTR questionnaire, AND the
// order being prior-authed (procedure + diagnosis). By design, the values a payer reads
// are visible inputs — the order details and the clinical context are both supplied
// here, never hardcoded inside RunPriorAuth. DemoLumbarContext supplies a complete demo
// clinical fill; the order itself should name one of the reference payer's advertised
// families (e.g. L8000/E0431), never a code the network does not handle.
//
// A conformant payer keys its verdict on the ORDER (the requested-service code) and its
// own policy, never on this SDK's fixture answers; the clinical context exists so the
// QuestionnaireResponse this flow puts on the wire carries real, attributable content.
type PriorAuthRequest struct {
	Member string
	DOB    string
	Family string
	NPI    string
	// Clinical drives the DTR FillQuestionnaire answers.
	Clinical ClinicalContext
	// The order being prior-authed. ProcedureSystem is the code SYSTEM: "" defaults to
	// CPT (systemCPT) so every EXISTING caller that set
	// only ProcedureCPT/ProcedureDisplay/DiagnosisICD10 is unaffected. Set it to the HCPCS
	// system URI to prior-auth one of the mirrored families (E0250/L8000/G0151/J3490) — a
	// discovery descriptor's advertised persona.Order names both the system and the code.
	ProcedureSystem  string
	ProcedureCPT     string
	ProcedureDisplay string
	DiagnosisICD10   string
	// ProceedOnNotCovered keeps the flow running past a CRD card that says the service
	// is NOT covered, so the requester gets the payer's FORMAL determination (a PAS
	// denial with its rationale) rather than stopping at the advisory card. Default
	// false: a not-covered card is terminal and RunPriorAuth returns Outcome
	// "not-covered" (the originator's own stop, no PAS leg). Same semantics as the
	// substrate originator's proceedOnNotCovered.
	ProceedOnNotCovered bool
}

// DemoLumbarContext returns the complete ClinicalContext for the demo lumbar-MRI
// questionnaire: 6 weeks of conservative therapy, prior imaging present, no neuro
// deficit. It fills every leaf the demo questionnaire declares, so the QR this SDK puts
// on the wire is fully answered and every answer carries its DTR information-origin
// attribution. It is fixture data for the demo questionnaire — not a policy input.
func DemoLumbarContext() ClinicalContext {
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

// DemoLumbarContextPriorSurgery is DemoLumbarContext plus a prior lumbar surgery — the
// fill a member with an operative history produces. It is the context the pended→amend
// exercise uses, because the amend carries that surgery's operative DiagnosticReport (a
// SupplementalReport you build yourself — this package ships no fixture constructor for
// one); the PEND itself comes from the payer's own policy on the order, never from this
// field.
func DemoLumbarContextPriorSurgery() ClinicalContext {
	cc := DemoLumbarContext()
	cc.PriorSurgery = true
	cc.PriorSurgeryRef = "Procedure/proc-laminectomy"
	return cc
}

// DemoLumbarContextShortTherapy is DemoLumbarContext with only 4 weeks of conservative
// therapy — the fill a member early in their treatment course produces. Kept as a
// SECOND, materially different fill so callers can exercise the questionnaire with more
// than one answer set; it is not a verdict lever.
func DemoLumbarContextShortTherapy() ClinicalContext {
	cc := DemoLumbarContext()
	cc.ConservativeTherapyWeeks = 4
	return cc
}

// RunPriorAuth runs a full prior-authorization through the substrate — the
// CRD→DTR→PAS orchestrator — and returns the outcome. It drives three sealed
// round-trips, each via runLeg (the RunEligibility sealed-leg template):
//
//	LEG 1 CRD  provider-tpo/crd-order-select → payer-coverage/crd-cards
//	           (no-PA-required short-circuits here; DTR/PAS never run)
//	LEG 2 DTR  provider-tpo/dtr-questionnaire-fetch → payer-coverage/dtr-questionnaire
//	LEG 3 PAS  provider-tpo/pas-claim → payer-coverage/pas-response
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
	procSystem := req.ProcedureSystem
	if procSystem == "" {
		procSystem = systemCPT // backward-compat default (older callers never set this)
	}
	srJSON, err := BuildServiceRequestCoded(procSystem, req.ProcedureCPT, req.ProcedureDisplay, req.DiagnosisICD10, patientRef)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: build service request: %w", err)
	}
	// The Coverage's urn:shn:coverage MB identifier carries the BARE member id (the
	// identifier-semantics rule); coverageRef above stays the REFERENCE-shaped value the
	// QRContext/insurance roles need.
	// The CRD prefetch Coverage must NAME its payer. A real payer's coverage-requirements
	// service reads Coverage.payor to decide whose rules apply and refuses a payor it cannot
	// identify, so the generic unidentified Organization reference this leg used to send is
	// rejected outright — the whole prior-auth then fails at leg 1, before any of the PA
	// flow runs. BuildCoverageWithPayer carries the contained payer Organization with the
	// same identity every later leg of this flow already stamps.
	covJSON, err := BuildCoverageWithPayer(patientRef, req.Member, CMSPayerIdentity)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: build coverage: %w", err)
	}

	// LEG 1 — CRD order-select (the conformant crd-order-select leg; the
	// minimized crd-order-select leg/op have been removed — this is the only CRD contract).
	crdReq, err := BuildConformantOrderSelectRequest(srJSON, covJSON, patientRef)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: build order-select request: %w", err)
	}
	crdResp, err := id.runLeg(ctx, c, ep, payer, pci,
		"crd-order-select", "crd-order-select", "crd-cards", crdReq)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: %w", err)
	}
	cov, err := ParseCards(crdResp)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("crd-order-select: parse cards: %w", err)
	}
	// A NOT-COVERED verdict is its own outcome, distinct from "no PA needed" — both
	// skip the PA legs, but one says "go ahead, no authorization required" and the other
	// says "this plan does not cover this service". Collapsing them (as this flow did
	// before v0.46.0) reported a coverage refusal as a green no-PA-required.
	if cov.Covered == CoveredNotCovered {
		if !req.ProceedOnNotCovered {
			return PriorAuthResult{Outcome: "not-covered"}, nil
		}
		// Proceed to get the payer's FORMAL determination. A not-covered card carries no
		// questionnaire, so there is no DTR leg to run: submit the claim as-is and let the
		// payer's ClaimResponse carry the denial and its rationale.
		return id.submitPriorAuthClaim(ctx, c, ep, payer, pci, req, srJSON, patientRef, coverageRef, nil)
	}
	if !cov.PARequired() {
		// No PA needed for this order — terminal, short-circuit (no DTR/PAS legs).
		return PriorAuthResult{Outcome: "no-pa-required"}, nil
	}
	if !cov.NeedsDTR() {
		return PriorAuthResult{}, fmt.Errorf("shnsdk: PA-required card carried no questionnaire")
	}
	canonical := StripCanonicalVersion(cov.Questionnaires[0])

	// LEG 2 — DTR questionnaire fetch + local auto-fill. A 2.2 (qr-required) responder
	// derives the QR shell's coverage reference from Coverage.id and fails closed without a
	// STABLE, request-specific one, so the fetch leg needs its own id-stamped copy — reuse
	// withResourceID (sdk/crd.go) rather than mutate covJSON, which the CRD leg above already
	// sent as built. (covJSON does carry an id of its own: BuildCoverageWithPayer stamps the
	// builder's fixed conformant id. Stamping a member-derived one here keeps the fetch leg's
	// coverage reference distinguishable per request rather than reusing one constant.)
	dtrCovJSON, err := withResourceID(covJSON, "coverage-"+req.Member)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: stamp coverage id: %w", err)
	}
	dtrReq, err := BuildQuestionnaireFetchWithCoverage(canonical, dtrCovJSON)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: build fetch request: %w", err)
	}
	dtrResp, err := id.runLeg(ctx, c, ep, payer, pci,
		"dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-questionnaire", dtrReq)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: %w", err)
	}
	// §6.2: the DTR-fetch leg responds with a $questionnaire-package collection Bundle;
	// extract the bare Questionnaire (strict, package-only — no dual-shape tolerance)
	// before the F5 canonical check + auto-fill.
	questionnaireJSON, err := ExtractQuestionnaireFromPackage(dtrResp)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: %w", err)
	}
	fetchedURL, err := ParseQuestionnaireURL(questionnaireJSON)
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: parse questionnaire url: %w", err)
	}
	if fetchedURL != canonical {
		// Canonical-substitution guard (F5): the fetched questionnaire must be the one
		// the CRD card advertised, else the payer swapped questionnaires under us.
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: fetched questionnaire %q != advertised canonical %q", fetchedURL, canonical)
	}
	qc := QRContext{
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		OrderRef:    "ServiceRequest/sr-" + req.Member,
		Authored:    id.now(),
	}
	// Auto-fill ONLY the one questionnaire this package ships prefill logic for. A payer
	// running real Da Vinci advertises its OWN questionnaires; for those the honest answer
	// is a zero-answer shell naming what the payer advertised, never invented answers
	// (BuildQuestionnaireResponseShell). The clinician — or an operated $populate engine on
	// the requester's side — authors the content.
	var qrJSON []byte
	if fetchedURL == SupportedQuestionnaireCanonical {
		qrJSON, err = FillQuestionnaire(questionnaireJSON, req.Clinical, qc)
	} else {
		qrJSON, err = BuildQuestionnaireResponseShell(questionnaireJSON, qc)
	}
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("dtr-questionnaire-fetch: build questionnaire response: %w", err)
	}

	// LEG 3 — PAS submit.
	return id.submitPriorAuthClaim(ctx, c, ep, payer, pci, req, srJSON, patientRef, coverageRef, qrJSON)
}

// submitPriorAuthClaim is RunPriorAuth's LEG 3: build the conformant PAS $submit bundle
// and run the sealed pas-claim round-trip, returning the parsed outcome (with a resume
// handle on a pend). qrJSON may be nil — a payer whose card advertised no questionnaire
// gets a claim with no QuestionnaireResponse entry (the builder's documented
// no-questionnaire lane), which is the shape the ProceedOnNotCovered path submits to
// obtain a FORMAL determination after an advisory not-covered card.
func (id Identity) submitPriorAuthClaim(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, pci string, req PriorAuthRequest, srJSON []byte, patientRef, coverageRef string, qrJSON []byte) (PriorAuthResult, error) {
	var pasCorrRaw [16]byte
	if _, err := rand.Read(pasCorrRaw[:]); err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-submit: generate claim correlation id: %w", err)
	}
	pasCorr := hex.EncodeToString(pasCorrRaw[:])
	bundleJSON, err := BuildConformantClaimBundle(ConformantClaimInputs{
		QR:          qrJSON,
		SR:          srJSON,
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		MemberID:    req.Member,
		Corr:        pasCorr,
		Created:     id.now(),
		// The same reason ResumePriorAuth carries these: a real payer resolves
		// Claim.insurer and Coverage.payor against the bundle it was handed and refuses
		// a reference it cannot find, so the generic unresolvable payer Organization
		// this leg used to send is rejected before adjudication. These make the bundle
		// this builder's reference-payer-conformant form — payer Organization as a
		// resolvable entry, absolute references, and the Claim item stamped with the
		// ORDER's own procedure code rather than the builder's placeholder, which is
		// what a code-keyed payer decides on.
		PayerOrgEntry: true,
		AbsoluteRefs:  true,
		Payer:         CMSPayerIdentity,
	})
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-submit: build claim bundle: %w", err)
	}
	// Pass pasCorr as the envelope correlationID so the payer ledger keys the
	// pended claim on the same value the ClaimUpdate's Claim.related references
	// (pasCorr). The substrate's own originate path does the same. The conformant
	// pas-claim leg/op are the only PA-submit contract (the minimized
	// pas-claim leg + pas-submit op have been removed).
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
			MemberID:              req.Member,
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

// contractTokenForTxType returns the request-frame contract-version claim (the
// request-frame contract) — the token this SDK's request at txType is BUILT at, or "" for a version-neutral leg
// (coverage-eligibility — mirrors the gateway's paCatalog "" Contract). This SDK does
// NOT do per-line content negotiation: every Build* helper the PA-chain legs use
// targets a single fixed native line (BuildClaimResponse's "speaks PAS line 2.0"
// comment is the precedent), so the token is a static per-leg constant rather than a
// selected one. It is used BOTH to frame the request (toward a requestFrames-
// declaring payer) and as the expected stamp on that leg's response
// (unframeAnswer's stamp verify) — the same token both directions, exactly like the
// gateway's roundTripInner reads content.ProfileID for both.
func contractTokenForTxType(txType string) string {
	switch txType {
	case "crd-order-select":
		return ContractPACRD20
	case "dtr-questionnaire-fetch":
		return ContractPADTR20
	case "pas-claim", "pas-claim-update":
		return ContractPAPAS20
	default:
		return ""
	}
}

// runLegWithCorr is runLeg with a caller-supplied correlationID. Used when the
// envelope correlationID must equal the bundle's own claim correlation — specifically
// the pas-claim leg, where the payer ledger keys the pended claim on the ENVELOPE
// correlationID and the follow-up ClaimUpdate's Claim.related references it by the
// same value (pasCorr). All other legs use runLeg (fresh random correlation each leg).
func (id Identity) runLegWithCorr(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, pci, txType, reqOp, respOp, correlationID string, payload []byte) ([]byte, error) {
	now := id.now()

	// REQUEST-line claim (request-frame contract, published-SDK parity —
	// v0.38.0): frame the request IFF this leg maps to a contract AND the payer's
	// registry entry declares requestFrames v1 — a payer that never declares it
	// gets a BYTE-IDENTICAL bare request (same rule + status-200 inert filler as
	// the gateway's roundTripInner). The claim rides INSIDE the seal (AI-2's
	// Hub-payload-blindness is unaffected — this wraps `payload` before Seal).
	contractToken := contractTokenForTxType(txType)
	if contractToken != "" && SupportsRequestFrameV1(payer.RequestFrames) {
		framed, ferr := EncodeHTTPFrameHeaders(http.StatusOK, map[string]string{
			"Content-Type":             "application/fhir+json",
			FrameHeaderContractVersion: contractToken,
		}, payload)
		if ferr != nil {
			return nil, fmt.Errorf("encode request frame: %w", ferr)
		}
		payload = framed
	}

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
	// Unframe (a frame-capable payer's non-2xx APPLICATION answer surfaces as
	// *AppAnswerError, verbatim) — shared by every runLeg/runLegWithCorr caller.
	// expectedToken = contractToken: the SAME line this leg's request was built
	// (and, when capable, framed) at is what a 2xx framed answer must be stamped
	// with, or leave unstamped (tolerated) — verified by unframeAnswer.
	return unframeAnswer(plaintext, contractToken)
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

// ResumePriorAuth drives the exchange-2 ClaimUpdate from a pended PA's resume
// handle: validate supp (ProvenanceAgent present → else error, no wire) → build the
// operative DiagnosticReport + Provenance → BuildConformantClaimUpdateBundle (reusing the
// submit QR/SR unchanged, related[] → the original submit correlation, FR-21) → ONE sealed
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

	bundleJSON, err := BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{
		QR:               resume.QRJSON,
		SR:               resume.SRJSON,
		PatientRef:       resume.PatientRef,
		CoverageRef:      resume.CoverageRef,
		MemberID:         resume.MemberID,
		Provenance:       provJSON,
		DiagnosticReport: drJSON,
		Corr:             updateCorr,
		OriginalCorr:     resume.OriginalCorrelationID,
		Created:          id.now(),
		// A Da Vinci PAS amended re-POST is not merely "the submit bundle again": the
		// payer has to be able to find the authorization being amended, and to be told
		// that the amendment carries NEW information rather than restating the old.
		// Those are Claim.related[0].claim.reference resolving to the prior Claim as an
		// in-bundle entry, and the PAS infoChanged item extension. Without them a real
		// payer either refuses the update outright (the prior Claim is unresolvable) or
		// carries the prior decision forward unchanged, so a genuinely-answered
		// amendment comes back still pended — the documented resume flow would never
		// reach a determination. Setting these makes the bundle this builder's
		// reference-payer-conformant form, which also resolves the payor and the insurer
		// against real bundle entries and stamps the Claim item with the ORDER's own
		// procedure code instead of the builder's placeholder.
		PayerOrgEntry: true,
		AbsoluteRefs:  true,
		Payer:         CMSPayerIdentity,
	})
	if err != nil {
		return PriorAuthResult{}, fmt.Errorf("pas-update-submit: build claim update bundle: %w", err)
	}

	// The conformant pas-claim-update leg/op are the only PA-update contract
	// (the minimized pas-claim-update leg + pas-update-submit op have been removed).
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
			MemberID:              resume.MemberID,
			SubjectPCI:            resume.SubjectPCI,
			QRJSON:                resume.QRJSON,
			SRJSON:                resume.SRJSON,
			NeededItems:           result.NeededItems,
		}
	}
	return result, nil
}

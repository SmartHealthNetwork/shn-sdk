package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The TestVector* tests are the PUBLIC repo's standalone hermetic contract. They verify
// the SDK against canonical wire-vectors minted upstream by the substrate's vector
// generator (the substrate keeps its own cross-module parity suite privately); THIS
// test proves the SDK parses/verifies/reproduces the canonical bytes the substrate
// emits, importing ONLY shnsdk + stdlib + golang.org/x/crypto.
//
// The vectors and their seeded SYNTHETIC keys are documented in
// testdata/vectors/README.md. All keys are deterministic across generator runs so
// the committed vectors never churn.
//
// vecClock is the SINGLE fixed clock the vectors were minted under (see the vectors
// README). It is duplicated here intentionally — this test must not depend on
// anything outside the SDK module.
var vecClock = time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

func vectorsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", "vectors")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("vectors dir %s not found (the committed vectors ship with the module): %v", dir, err)
	}
	return dir
}

func readVector(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read vector %s: %v", name, err)
	}
	return b
}

func readB64Vector(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw := readVector(t, dir, name)
	b, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("base64-decode vector %s: %v", name, err)
	}
	return b
}

// TestVectorCERReproduce: the CER is byte-deterministic under the fixed clock, so the
// SDK's BuildEligibilityRequest must REPRODUCE the vector byte-for-byte.
func TestVectorCERReproduce(t *testing.T) {
	dir := vectorsDir(t)
	want := readVector(t, dir, "cer.json")
	got, err := BuildEligibilityRequest("MBR-COVERED", "1234567890", vecClock)
	if err != nil {
		t.Fatalf("BuildEligibilityRequest: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("CER not reproduced byte-for-byte:\n got=%s\nwant=%s", got, want)
	}
}

// TestVectorFillQuestionnaireReproduce: the filled DTR QuestionnaireResponse is
// byte-deterministic under the fixed clock + the MBR-COVERED cc/qc, so the SDK's
// FillQuestionnaire must REPRODUCE the QR vector byte-for-byte from the questionnaire
// INPUT vector. The questionnaire is carried as its own input vector (the SDK can't
// build the substrate's payer fixture).
func TestVectorFillQuestionnaireReproduce(t *testing.T) {
	dir := vectorsDir(t)
	questionnaire := readVector(t, dir, "questionnaire.json")
	want := readVector(t, dir, "questionnaireresponse.json")

	cc := ClinicalContext{
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
	qc := QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    vecClock,
	}
	got, err := FillQuestionnaire(questionnaire, cc, qc)
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("QR not reproduced byte-for-byte:\n got=%s\nwant=%s", got, want)
	}
}

// TestVectorFillQuestionnaireAtLineReproduce (vector corpus): the additive
// testdata/vectors/{2.1,2.2}/questionnaireresponse.json vectors must be
// REPRODUCED byte-for-byte by FillQuestionnaireAtLine from the SAME
// questionnaire.json input (there is no per-line questionnaire — DTRDef never
// gates the bare Questionnaire, only the QR/package shape).
func TestVectorFillQuestionnaireAtLineReproduce(t *testing.T) {
	dir := vectorsDir(t)
	questionnaire := readVector(t, dir, "questionnaire.json")
	cc := ClinicalContext{
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
	qc := QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    vecClock,
	}
	for _, line := range []string{"2.1", "2.2"} {
		want := readVector(t, dir, line+"/questionnaireresponse.json")
		got, err := FillQuestionnaireAtLine(line, questionnaire, cc, qc)
		if err != nil {
			t.Fatalf("FillQuestionnaireAtLine(%s): %v", line, err)
		}
		if string(got) != string(want) {
			t.Errorf("line %s: QR not reproduced byte-for-byte:\n got=%s\nwant=%s", line, got, want)
		}
	}
}

// TestVectorClaimResponseConsume: the SDK's ParseClaimResponse must CONSUME the approved
// vector → PriorAuthResult{Outcome:"approved", PreAuthRef, ValidUntil}.
func TestVectorClaimResponseConsume(t *testing.T) {
	dir := vectorsDir(t)
	res, err := ParseClaimResponse(readVector(t, dir, "claimresponse-approved.json"))
	if err != nil {
		t.Fatalf("ParseClaimResponse(claimresponse-approved): %v", err)
	}
	if res.Outcome != "approved" {
		t.Errorf("Outcome = %q, want approved", res.Outcome)
	}
	if res.PreAuthRef != "PA-0123456789ab" {
		t.Errorf("PreAuthRef = %q, want PA-0123456789ab", res.PreAuthRef)
	}
	if res.ValidUntil != "2026-09-02" {
		t.Errorf("ValidUntil = %q, want 2026-09-02", res.ValidUntil)
	}
}

// TestVectorClaimResponseAtLineConsume (vector corpus): the additive
// testdata/vectors/{2.1,2.2}/claimresponse-approved.json vectors must CONSUME
// identically to the flat vector — BuildClaimResponseAtLine is documented
// "interface symmetry" (no structural delta at any line, sdk/pasresponder.go),
// so this also proves ParseClaimResponse doesn't accidentally depend on
// anything that WOULD vary by line.
func TestVectorClaimResponseAtLineConsume(t *testing.T) {
	dir := vectorsDir(t)
	for _, line := range []string{"2.1", "2.2"} {
		res, err := ParseClaimResponse(readVector(t, dir, line+"/claimresponse-approved.json"))
		if err != nil {
			t.Fatalf("line %s: ParseClaimResponse(claimresponse-approved): %v", line, err)
		}
		if res.Outcome != "approved" {
			t.Errorf("line %s: Outcome = %q, want approved", line, res.Outcome)
		}
		if res.PreAuthRef != "PA-0123456789ab" {
			t.Errorf("line %s: PreAuthRef = %q, want PA-0123456789ab", line, res.PreAuthRef)
		}
		if res.ValidUntil != "2026-09-02" {
			t.Errorf("line %s: ValidUntil = %q, want 2026-09-02", line, res.ValidUntil)
		}
	}
}

// TestVectorCRRConsume: the SDK's ParseEligibilityResponse must CONSUME both branches
// → (true,"") for covered and (false,reason) for not-covered.
func TestVectorCRRConsume(t *testing.T) {
	dir := vectorsDir(t)

	covered, reason, err := ParseEligibilityResponse(readVector(t, dir, "crr-covered.json"))
	if err != nil {
		t.Fatalf("parse crr-covered: %v", err)
	}
	if !covered || reason != "" {
		t.Errorf("crr-covered = (%v,%q), want (true,\"\")", covered, reason)
	}

	covered, reason, err = ParseEligibilityResponse(readVector(t, dir, "crr-notcovered.json"))
	if err != nil {
		t.Fatalf("parse crr-notcovered: %v", err)
	}
	if covered || reason != "coverage-terminated" {
		t.Errorf("crr-notcovered = (%v,%q), want (false,\"coverage-terminated\")", covered, reason)
	}
}

// TestVectorTokenVerify: the token is signed by a FIXED authz key (authz_pub.b64). The
// SDK VERIFIES (never mints) it, bound to the known field values.
func TestVectorTokenVerify(t *testing.T) {
	dir := vectorsDir(t)
	var tok Token
	if err := json.Unmarshal(readVector(t, dir, "token.json"), &tok); err != nil {
		t.Fatalf("unmarshal token vector: %v", err)
	}
	authzPub := ed25519.PublicKey(readB64Vector(t, dir, "authz_pub.b64"))

	// The canonical token vector carries a fixed, well-formed payloadHash (AI-2).
	const vecPayloadHash = "abababababababababababababababababababababababababababababababab" + "ab"

	// Accept: bound to the known exchange, inside expiry.
	if err := VerifyBound(tok, authzPub, vecClock,
		"payer-coverage", "eligibility-response", "vec-corr-1", "payer",
		"pci:deadbeefdeadbeefdeadbeefdeadbeef", vecPayloadHash); err != nil {
		t.Fatalf("VerifyBound rejected the canonical token vector: %v", err)
	}

	// Reject: wrong binding (proves it genuinely binds, not a permissive stub).
	if err := VerifyBound(tok, authzPub, vecClock,
		"provider-tpo", "eligibility-response", "vec-corr-1", "payer",
		"pci:deadbeefdeadbeefdeadbeefdeadbeef", vecPayloadHash); err == nil {
		t.Error("VerifyBound should reject a wrong frame")
	}
	// Reject: wrong payloadHash (AI-2 binding).
	if err := VerifyBound(tok, authzPub, vecClock,
		"payer-coverage", "eligibility-response", "vec-corr-1", "payer",
		"pci:deadbeefdeadbeefdeadbeefdeadbeef", "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"); err == nil {
		t.Error("VerifyBound should reject a wrong payloadHash")
	}
	// Reject: expired.
	if err := VerifyBound(tok, authzPub, vecClock.Add(2*time.Hour),
		"payer-coverage", "eligibility-response", "vec-corr-1", "payer",
		"pci:deadbeefdeadbeefdeadbeefdeadbeef", vecPayloadHash); err == nil {
		t.Error("VerifyBound should reject an expired token")
	}
}

// TestVectorEnvelopeOpen: the envelope is a NaCl sealed box to a FIXED recipient enc
// keypair (recipient_enc_{pub,priv}.b64). The ephemeral sender key makes the
// ciphertext non-reproducible, so the SDK CONSUMES it: Open recovers the known
// plaintext and the cleartext Metadata matches.
func TestVectorEnvelopeOpen(t *testing.T) {
	dir := vectorsDir(t)
	env, err := DecodeEnvelope(readVector(t, dir, "envelope.json"))
	if err != nil {
		t.Fatalf("decode envelope vector: %v", err)
	}

	// Cleartext, Hub-readable Metadata: holder IDs only, never a patient id (AI-5).
	if env.Metadata.Sender != "provider" || env.Metadata.Recipient != "payer" {
		t.Errorf("metadata sender/recipient = %q/%q", env.Metadata.Sender, env.Metadata.Recipient)
	}
	if env.Metadata.TransactionType != "coverage-eligibility" || env.Metadata.CorrelationID != "vec-corr-1" {
		t.Errorf("metadata txType/corr = %q/%q", env.Metadata.TransactionType, env.Metadata.CorrelationID)
	}
	if env.Metadata.AuthorityFrame != "provider-tpo" {
		t.Errorf("metadata authorityFrame = %q", env.Metadata.AuthorityFrame)
	}

	var encPub, encPriv [32]byte
	copy(encPub[:], readB64Vector(t, dir, "recipient_enc_pub.b64"))
	copy(encPriv[:], readB64Vector(t, dir, "recipient_enc_priv.b64"))

	got, err := Open(env, &encPub, &encPriv)
	if err != nil {
		t.Fatalf("Open canonical envelope vector: %v", err)
	}
	want := `{"resourceType":"CoverageEligibilityRequest","id":"vec-envelope-payload"}`
	if string(got) != want {
		t.Errorf("envelope plaintext = %q, want %q", got, want)
	}

	// Negative: the wrong recipient key cannot open it (payload-blind basis, AI-2).
	var wrong [32]byte
	wrong[0] = 0xFF
	if _, err := Open(env, &encPub, &wrong); err == nil {
		t.Error("Open should fail with the wrong recipient private key")
	}
}

// TestVectorAssertionVerify: the assertion is base64(json(assertion)) signed by a FIXED
// holder key (assertion_signer_pub.b64). It has a random jti so it is not
// byte-reproducible; the SDK CONSUMES/VERIFIES it: decode, field-assert, and
// ed25519.Verify over the canonical signing payload. This test is package shnsdk so it
// can use the unexported assertion struct + assertionSigningPayload (the real
// signature path), not just a structural parse.
func TestVectorAssertionVerify(t *testing.T) {
	dir := vectorsDir(t)
	raw := readB64Vector(t, dir, "assertion.b64")
	signerPub := ed25519.PublicKey(readB64Vector(t, dir, "assertion_signer_pub.b64"))

	var a Assertion
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("unmarshal assertion vector: %v", err)
	}
	if a.HolderID != "provider" || a.Audience != "authz" {
		t.Errorf("assertion holderId/audience = %q/%q", a.HolderID, a.Audience)
	}
	if !a.IssuedAt.Equal(vecClock) || !a.Expiry.Equal(vecClock.Add(MaxAssertionTTL)) {
		t.Errorf("assertion issuedAt/expiry = %v/%v", a.IssuedAt, a.Expiry)
	}
	if a.JTI == "" {
		t.Error("assertion jti must be present (replay-guard, covered by the signature)")
	}
	if len(a.Sig) == 0 {
		t.Fatal("assertion sig missing")
	}

	// The decisive check: the signature verifies over the SDK's canonical signing
	// payload (Sig zeroed). If the SDK's payload diverged from the substrate's, this
	// fails — the same property test/sdkparity proves, now standalone.
	if !ed25519.Verify(signerPub, assertionSigningPayload(a), a.Sig) {
		t.Error("assertion signature did not verify over assertionSigningPayload")
	}

	// Negative: tampering with a covered field breaks the signature.
	tampered := a
	tampered.HolderID = "attacker"
	if ed25519.Verify(signerPub, assertionSigningPayload(tampered), a.Sig) {
		t.Error("signature should not verify after mutating holderId")
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// TestVectorDiagnosticReportReproduce: the operative DiagnosticReport is deterministic
// (fixed effectiveDate), so the SDK's BuildDiagnosticReport must REPRODUCE it.
func TestVectorDiagnosticReportReproduce(t *testing.T) {
	dir := vectorsDir(t)
	want := readVector(t, dir, "diagnosticreport-uc04.json")
	got, err := BuildDiagnosticReport("dr-uc04-operative", "Patient/MBR-UC04", "72148", "MRI lumbar spine w/o contrast")
	if err != nil {
		t.Fatalf("BuildDiagnosticReport: %v", err)
	}
	if !bytesEqual(want, got) {
		t.Fatalf("DiagnosticReport vector drift:\n want: %s\n got:  %s", want, got)
	}
}

// TestVectorProvenanceReproduce: the SDK's BuildProvenance reproduces the vector under vecClock.
func TestVectorProvenanceReproduce(t *testing.T) {
	dir := vectorsDir(t)
	want := readVector(t, dir, "provenance-uc04.json")
	got, err := BuildProvenance("DiagnosticReport/dr-uc04-operative", "Organization/provider", vecClock)
	if err != nil {
		t.Fatalf("BuildProvenance: %v", err)
	}
	if !bytesEqual(want, got) {
		t.Fatalf("Provenance vector drift:\n want: %s\n got:  %s", want, got)
	}
}

// TestVectorPendedConsume: the SDK parses the substrate-built PENDED Bundle → pended +
// the typed NeededItems.
func TestVectorPendedConsume(t *testing.T) {
	dir := vectorsDir(t)
	pendedJSON := readVector(t, dir, "claimresponse-pended.json")
	pended, needed, err := ParsePendedResponse(pendedJSON)
	if err != nil {
		t.Fatalf("ParsePendedResponse: %v", err)
	}
	if !pended {
		t.Fatal("ParsePendedResponse: pended=false, want true")
	}
	if len(needed) != 1 || needed[0].Code != "operative-diagnostic-report" {
		t.Fatalf("needed = %+v, want [operative-diagnostic-report]", needed)
	}
}

// TestVectorPendedAtLineConsume (vector corpus): the additive
// testdata/vectors/{2.1,2.2}/claimresponse-pended.json vectors — line 2.2 adds
// the mandatory response Bundle.identifier (PAS package differential); ParsePendedResponse
// must still CONSUME the same (pended, needed) shape at every line.
func TestVectorPendedAtLineConsume(t *testing.T) {
	dir := vectorsDir(t)
	for _, line := range []string{"2.1", "2.2"} {
		pendedJSON := readVector(t, dir, line+"/claimresponse-pended.json")
		pended, needed, err := ParsePendedResponse(pendedJSON)
		if err != nil {
			t.Fatalf("line %s: ParsePendedResponse: %v", line, err)
		}
		if !pended {
			t.Fatalf("line %s: ParsePendedResponse: pended=false, want true", line)
		}
		if len(needed) != 1 || needed[0].Code != "operative-diagnostic-report" {
			t.Fatalf("line %s: needed = %+v, want [operative-diagnostic-report]", line, needed)
		}
	}
}

// TestVectorDeniedConsume: the SDK parses the substrate-built DENIED ClaimResponse →
// Outcome denied + Denial.ReasonCode A3 + a non-empty rationale.
func TestVectorDeniedConsume(t *testing.T) {
	dir := vectorsDir(t)
	deniedJSON := readVector(t, dir, "claimresponse-denied-uc08.json")
	res, err := ParseClaimResponse(deniedJSON)
	if err != nil {
		t.Fatalf("ParseClaimResponse(denied): %v", err)
	}
	if res.Outcome != "denied" {
		t.Errorf("Outcome = %q, want denied", res.Outcome)
	}
	if res.Denial == nil || res.Denial.ReasonCode != "A3" || res.Denial.Rationale == "" {
		t.Errorf("Denial = %+v, want ReasonCode A3 + non-empty Rationale", res.Denial)
	}
}

// TestVectorDeniedAtLineConsume (vector corpus): the additive
// testdata/vectors/{2.1,2.2}/claimresponse-denied-uc08.json vectors must CONSUME
// identically to the flat vector — BuildDeniedResponseAtLine is documented
// "interface symmetry" (no structural delta at any line, sdk/pasresponder.go).
func TestVectorDeniedAtLineConsume(t *testing.T) {
	dir := vectorsDir(t)
	for _, line := range []string{"2.1", "2.2"} {
		deniedJSON := readVector(t, dir, line+"/claimresponse-denied-uc08.json")
		res, err := ParseClaimResponse(deniedJSON)
		if err != nil {
			t.Fatalf("line %s: ParseClaimResponse(denied): %v", line, err)
		}
		if res.Outcome != "denied" {
			t.Errorf("line %s: Outcome = %q, want denied", line, res.Outcome)
		}
		if res.Denial == nil || res.Denial.ReasonCode != "A3" || res.Denial.Rationale == "" {
			t.Errorf("line %s: Denial = %+v, want ReasonCode A3 + non-empty Rationale", line, res.Denial)
		}
	}
}

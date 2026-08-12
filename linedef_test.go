package shnsdk

import "testing"

// TestLineOf checks the "text after @" contract of LineOf, including the
// malformed-token (no "@") edge.
func TestLineOf(t *testing.T) {
	cases := map[string]string{
		"pa.pas@2.1":  "2.1",
		"pa.crd@2.0":  "2.0",
		"pa.pdex@2.1": "2.1",
		"malformed":   "",
	}
	for tok, want := range cases {
		if got := LineOf(tok); got != want {
			t.Errorf("LineOf(%q) = %q, want %q", tok, got, want)
		}
	}
}

// TestPASLineDefUnknownLine: a line this build does not speak returns ok=false,
// not a zero-value PASDef mistaken for a real one.
func TestPASLineDefUnknownLine(t *testing.T) {
	if _, ok := PASLineDef("9.9"); ok {
		t.Fatal("PASLineDef(\"9.9\") ok = true, want false")
	}
}

// TestPASLineDefsExist: the three PAS lines this build natively speaks all
// resolve.
func TestPASLineDefsExist(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		def, ok := PASLineDef(line)
		if !ok {
			t.Fatalf("PASLineDef(%q) ok = false, want true", line)
		}
		if def.Line != line {
			t.Errorf("PASLineDef(%q).Line = %q, want %q", line, def.Line, line)
		}
	}
}

// TestPASLineDefClaimResponseProfile: PASDef's ClaimResponseProfile is the
// same canonical the responder has always self-declared on built
// ClaimResponses (sdk/pasresponder.go's pasProfileClaimResponse) — the linedef
// carries the existing constant forward as per-line data, it does not invent a
// new value.
func TestPASLineDefClaimResponseProfile(t *testing.T) {
	def, ok := PASLineDef("2.0")
	if !ok {
		t.Fatal("PASLineDef(\"2.0\") ok = false")
	}
	if def.ClaimResponseProfile != pasProfileClaimResponse {
		t.Errorf("PASLineDef(\"2.0\").ClaimResponseProfile = %q, want %q (pasProfileClaimResponse)", def.ClaimResponseProfile, pasProfileClaimResponse)
	}
}

// TestPASLineDefResponseBundleIdentifierRequired: PAS 2.2 is the only line
// that makes Bundle.identifier mandatory on the response bundle.
func TestPASLineDefResponseBundleIdentifierRequired(t *testing.T) {
	want := map[string]bool{"2.0": false, "2.1": false, "2.2": true}
	for line, w := range want {
		def, ok := PASLineDef(line)
		if !ok {
			t.Fatalf("PASLineDef(%q) ok = false", line)
		}
		if def.ResponseBundleIdentifierRequired != w {
			t.Errorf("PASLineDef(%q).ResponseBundleIdentifierRequired = %v, want %v", line, def.ResponseBundleIdentifierRequired, w)
		}
	}
}

// TestPASLineDefClaimItemLineDetailRequired: PAS 2.1+ requires the Claim.item
// certificationType/requestType extensions + location[x] (verified against the
// PAS 2.1.0/2.2.1 package differential); 2.0.1 does not.
func TestPASLineDefClaimItemLineDetailRequired(t *testing.T) {
	want := map[string]bool{"2.0": false, "2.1": true, "2.2": true}
	for line, w := range want {
		def, ok := PASLineDef(line)
		if !ok {
			t.Fatalf("PASLineDef(%q) ok = false", line)
		}
		if def.ClaimItemLineDetailRequired != w {
			t.Errorf("PASLineDef(%q).ClaimItemLineDetailRequired = %v, want %v", line, def.ClaimItemLineDetailRequired, w)
		}
	}
}

// TestPASLineDefClaimRelatedRelationshipRequired: PAS 2.1+ requires
// Claim.related.relationship on the update Claim profile (verified against the
// PAS 2.1.0/2.2.1 package differential); 2.0.1 does not.
func TestPASLineDefClaimRelatedRelationshipRequired(t *testing.T) {
	want := map[string]bool{"2.0": false, "2.1": true, "2.2": true}
	for line, w := range want {
		def, ok := PASLineDef(line)
		if !ok {
			t.Fatalf("PASLineDef(%q) ok = false", line)
		}
		if def.ClaimRelatedRelationshipRequired != w {
			t.Errorf("PASLineDef(%q).ClaimRelatedRelationshipRequired = %v, want %v", line, def.ClaimRelatedRelationshipRequired, w)
		}
	}
}

// TestDTRLineDefUnknownAndExist mirrors the PAS coverage for the DTR
// starting-shape linedef (extended as package diffs dictate).
func TestDTRLineDefUnknownAndExist(t *testing.T) {
	if _, ok := DTRLineDef("9.9"); ok {
		t.Fatal("DTRLineDef(\"9.9\") ok = true, want false")
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		def, ok := DTRLineDef(line)
		if !ok {
			t.Fatalf("DTRLineDef(%q) ok = false, want true", line)
		}
		if def.Line != line {
			t.Errorf("DTRLineDef(%q).Line = %q, want %q", line, def.Line, line)
		}
	}
}

// TestDTRLineDefSingleCoverageConstraint: DTR 2.2 is the only line that requires the
// dedicated qr-coverage extension (verified against StructureDefinition-dtr-
// questionnaireresponse.json + StructureDefinition-qr-coverage.json — DTR package differential).
func TestDTRLineDefSingleCoverageConstraint(t *testing.T) {
	want := map[string]bool{"2.0": false, "2.1": false, "2.2": true}
	for line, w := range want {
		def, ok := DTRLineDef(line)
		if !ok {
			t.Fatalf("DTRLineDef(%q) ok = false", line)
		}
		if def.SingleCoverageConstraint != w {
			t.Errorf("DTRLineDef(%q).SingleCoverageConstraint = %v, want %v", line, def.SingleCoverageConstraint, w)
		}
	}
}

// TestDTRLineDefQuestionnairePackageReturnShape: verified against the
// DTR-QPackageBundle profile's Bundle.entry:questionnaireResponse slice across all
// three package versions (DTR package differential).
func TestDTRLineDefQuestionnairePackageReturnShape(t *testing.T) {
	want := map[string]string{"2.0": "unconstrained", "2.1": "qr-optional", "2.2": "qr-required"}
	for line, w := range want {
		def, ok := DTRLineDef(line)
		if !ok {
			t.Fatalf("DTRLineDef(%q) ok = false", line)
		}
		if def.QuestionnairePackageReturnShape != w {
			t.Errorf("DTRLineDef(%q).QuestionnairePackageReturnShape = %q, want %q", line, def.QuestionnairePackageReturnShape, w)
		}
	}
}

// TestDTRLineDefAutoOriginSourceCode: DTR 2.2 retires the "auto" informationOrigins
// code in favor of "auto-client" (verified against ValueSet-informationOrigins.json +
// CodeSystem-dtr-informationorigin-codes.json — DTR package differential).
func TestDTRLineDefAutoOriginSourceCode(t *testing.T) {
	want := map[string]string{"2.0": "auto", "2.1": "auto", "2.2": "auto-client"}
	for line, w := range want {
		def, ok := DTRLineDef(line)
		if !ok {
			t.Fatalf("DTRLineDef(%q) ok = false", line)
		}
		if def.AutoOriginSourceCode != w {
			t.Errorf("DTRLineDef(%q).AutoOriginSourceCode = %q, want %q", line, def.AutoOriginSourceCode, w)
		}
	}
}

// TestCRDLineDefUnknownAndExist mirrors the PAS coverage for the CRD
// starting-shape linedef (extended as package diffs dictate).
func TestCRDLineDefUnknownAndExist(t *testing.T) {
	if _, ok := CRDLineDef("9.9"); ok {
		t.Fatal("CRDLineDef(\"9.9\") ok = true, want false")
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		def, ok := CRDLineDef(line)
		if !ok {
			t.Fatalf("CRDLineDef(%q) ok = false, want true", line)
		}
		if def.Line != line {
			t.Errorf("CRDLineDef(%q).Line = %q, want %q", line, def.Line, line)
		}
	}
}

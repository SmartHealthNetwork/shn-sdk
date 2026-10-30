package shnsdk

import (
	"encoding/json"
	"testing"
	"time"
)

// Synthetic request facts for the SDK's own builder tests: the demo
// lumbar-MRI persona, an office service. Each code is the PAS IG's own example
// (Claim-MedicalServicesAuthorizationExample: professional, processpriority
// normal, X12 1365 "1", POS "11", X12 1525 "IN"); the builder carries them as given.
const (
	testClaimTypeJSON      = `{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"professional"}`
	testPriorityJSON       = `{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"normal"}`
	testCategoryJSON       = `{"system":"https://codesystem.x12.org/005010/1365","code":"1","display":"Medical Care"}`
	testPlaceOfServiceJSON = `{"system":"https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set","code":"11"}`
	testRequestTypeJSON    = `{"system":"https://codesystem.x12.org/005010/1525","code":"IN","display":"Initial Medical Services Reservation"}`
	testCertTypeJSON       = `{"system":"https://codesystem.x12.org/005010/1322","code":"I","display":"Initial"}`
)

func testClaimFacts() PASClaimFacts {
	return PASClaimFacts{
		ClaimType:       json.RawMessage(testClaimTypeJSON),
		Priority:        json.RawMessage(testPriorityJSON),
		ServiceCategory: json.RawMessage(testCategoryJSON),
		PlaceOfService:  json.RawMessage(testPlaceOfServiceJSON),
		RequestType:     json.RawMessage(testRequestTypeJSON),
	}
}

// testPriorClaim is the recorded submission conformantSubmitInputs builds at
// PAS 2.0, read off its bytes as a participant records it.
func testPriorClaim(t *testing.T) PASPriorClaim {
	t.Helper()
	return testPriorClaimAt(t, "2.0")
}

// testPriorClaimAt is the recorded submission conformantSubmitInputs builds at
// line.
func testPriorClaimAt(t *testing.T, line string) PASPriorClaim {
	t.Helper()
	sub, err := BuildConformantClaimBundleAtLineExact(line, conformantSubmitInputs(t))
	if err != nil {
		t.Fatalf("build the submission: %v", err)
	}
	p, err := PASPriorClaimOf(sub)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testSubmissionFor is the recorded submission an amendment built from in
// continues, at line: the submission those same parties, records and lane
// sent under in.OriginalCorr, read off its bytes as a participant records it.
func testSubmissionFor(t *testing.T, line string, in ConformantClaimUpdateInputs) PASPriorClaim {
	t.Helper()
	out, err := BuildConformantClaimBundleAtLineExact(line, ConformantClaimInputs{PASClaimFacts: in.PASClaimFacts,
		Provider: in.Provider, Coverage: in.Coverage, Insurer: in.Insurer, MemberIDSystem: in.MemberIDSystem,
		QR: in.QR, SR: in.SR, PatientRef: in.PatientRef, CoverageRef: in.CoverageRef, MemberID: in.MemberID,
		Corr: in.OriginalCorr, Created: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		ContainedInsurer: in.ContainedInsurer, AbsoluteRefs: in.AbsoluteRefs, PayerOrgEntry: in.PayerOrgEntry, Payer: in.Payer,
	})
	if err != nil {
		t.Fatalf("build the submission: %v", err)
	}
	p, err := PASPriorClaimOf(out)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testResolvedFacts resolves testClaimFacts at a line, as the builders do.
func testResolvedFacts(t *testing.T, line string) pasResolvedFacts {
	t.Helper()
	def, _ := PASLineDef(line)
	r, err := resolvePASClaimFacts(def, testClaimFacts())
	if err != nil {
		t.Fatalf("resolve facts: %v", err)
	}
	if def.ClaimItemLineDetailRequired {
		r.certificationType = pasSubmitCertificationType
	}
	return r
}

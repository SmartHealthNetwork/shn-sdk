package shnsdk_test

import (
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

const wireHCPCS = "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"

// srWithCoding builds a single-coding ServiceRequest. system/code/display must be JSON-safe string literals.
func srWithCoding(system, code, display string) []byte {
	return []byte(`{"resourceType":"ServiceRequest","subject":{"reference":"Patient/MBR-COVERED"},` +
		`"code":{"coding":[{"system":"` + system + `","code":"` + code + `","display":"` + display + `"}]}}`)
}

func TestParseServiceRequestProductCoding(t *testing.T) {
	t.Run("HCPCS order returns the HCPCS system", func(t *testing.T) {
		sys, code, display, err := shnsdk.ParseServiceRequestProductCoding(
			srWithCoding(wireHCPCS, "L8000", "Breast prosthesis, mastectomy bra"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sys != wireHCPCS || code != "L8000" || display != "Breast prosthesis, mastectomy bra" {
			t.Fatalf("got (%q,%q,%q), want (%q,L8000,Breast prosthesis, mastectomy bra)", sys, code, display, wireHCPCS)
		}
	})
	t.Run("CPT order returns the CPT system", func(t *testing.T) {
		sys, code, _, err := shnsdk.ParseServiceRequestProductCoding(
			srWithCoding("http://www.ama-assn.org/go/cpt", "72148", "MRI lumbar spine w/o contrast"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sys != "http://www.ama-assn.org/go/cpt" || code != "72148" {
			t.Fatalf("got (%q,%q), want CPT/72148", sys, code)
		}
	})
	t.Run("unrecognized system errors (honest no-coding, FR-36 allowlist)", func(t *testing.T) {
		_, _, _, err := shnsdk.ParseServiceRequestProductCoding(
			srWithCoding("http://snomed.info/sct", "123", "x"))
		if err == nil {
			t.Fatal("want error for non-{CPT,HCPCS} system, got nil")
		}
	})
	t.Run("https HCPCS does NOT match the http allowlist (scheme is load-bearing)", func(t *testing.T) {
		_, _, _, err := shnsdk.ParseServiceRequestProductCoding(
			srWithCoding("https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "E0424", "Stationary Oxygen System"))
		if err == nil {
			t.Fatal("want error: https HCPCS must not match the exact http allowlist")
		}
	})
	t.Run("coding without display returns empty display", func(t *testing.T) {
		raw := []byte(`{"resourceType":"ServiceRequest","subject":{"reference":"Patient/p"},` +
			`"code":{"coding":[{"system":"` + wireHCPCS + `","code":"L8000"}]}}`)
		sys, code, display, err := shnsdk.ParseServiceRequestProductCoding(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if display != "" {
			t.Fatalf("want display \"\", got %q", display)
		}
		_ = sys
		_ = code
	})
	t.Run("wrong resourceType errors", func(t *testing.T) {
		_, _, _, err := shnsdk.ParseServiceRequestProductCoding([]byte(`{"resourceType":"Coverage"}`))
		if err == nil {
			t.Fatal("want error for non-ServiceRequest")
		}
	})
	t.Run("missing code.coding errors", func(t *testing.T) {
		_, _, _, err := shnsdk.ParseServiceRequestProductCoding(
			[]byte(`{"resourceType":"ServiceRequest","subject":{"reference":"Patient/p"}}`))
		if err == nil {
			t.Fatal("want error for absent code.coding")
		}
	})
	t.Run("non-allowlisted coding before an allowlisted one: returns the allowlisted match", func(t *testing.T) {
		raw := []byte(`{"resourceType":"ServiceRequest","subject":{"reference":"Patient/p"},` +
			`"code":{"coding":[{"system":"http://snomed.info/sct","code":"999"},` +
			`{"system":"` + wireHCPCS + `","code":"L8000","display":"Breast prosthesis, mastectomy bra"}]}}`)
		sys, code, _, err := shnsdk.ParseServiceRequestProductCoding(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sys != wireHCPCS || code != "L8000" {
			t.Fatalf("got (%q,%q), want (%q,L8000) — first allowlisted match wins", sys, code, wireHCPCS)
		}
	})
}

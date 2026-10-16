package shnsdk

import (
	"encoding/json"
	"errors"
	"testing"
)

// testAnswerRequest is what this package's test answers restate from the
// request they answer: a professional claim, addressed to the request's own
// payer Organization entry.
var testAnswerRequest = PASAnswerRequest{
	ClaimType: json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"professional"}]}`),
	Insurer:   json.RawMessage(`{"reference":"Organization/org-cms-payer"}`),
}

// Every answer builder restates the request's own type and insurer, byte for
// byte, and refuses an answer whose request states either not at all: it
// never writes a claim type or an insurer of its own.
func TestPASAnswerBuildersRestateTheRequest(t *testing.T) {
	stated := PASAnswerRequest{
		ClaimType: json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"institutional","display":"Institutional"}],"text":"inpatient"}`),
		Insurer:   json.RawMessage(`{"reference":"Organization/payer-org","display":"Acme Health Plan"}`),
	}
	now := testNow
	build := map[string]func(PASAnswerRequest) ([]byte, error){
		"approved": func(r PASAnswerRequest) ([]byte, error) {
			return BuildClaimResponseAtLineExact("2.2", "PA-1", "2026-12-31", "Patient/p1", "corr", r, now)
		},
		"denied": func(r PASAnswerRequest) ([]byte, error) {
			return BuildDeniedResponseAtLineExact("2.2", "Patient/p1", "corr", "", r, now)
		},
		"pended": func(r PASAnswerRequest) ([]byte, error) {
			in := testPendedInputs("2.2", "Patient/p1", "corr")
			in.Request = r
			return BuildPendedClaimResponseAtLineExact("2.2", in)
		},
	}
	for name, b := range build {
		out, err := b(stated)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cr := answerClaimResponse(t, out)
		if string(cr["type"]) != string(stated.ClaimType) || string(cr["insurer"]) != string(stated.Insurer) {
			t.Errorf("%s: type %s insurer %s, want the request's own", name, cr["type"], cr["insurer"])
		}
		for absent, r := range map[string]PASAnswerRequest{
			"no type":      {Insurer: stated.Insurer},
			"no insurer":   {ClaimType: stated.ClaimType},
			"null type":    {ClaimType: json.RawMessage("null"), Insurer: stated.Insurer},
			"empty object": {ClaimType: json.RawMessage("{}"), Insurer: stated.Insurer},
		} {
			if out, err := b(r); !errors.Is(err, ErrPASAnswerSourceAbsent) || out != nil {
				t.Errorf("%s %s: err=%v built=%s, want ErrPASAnswerSourceAbsent and nothing built", name, absent, err, out)
			}
		}
	}
}

// PASAnswerRequestOf reads the request Claim's type and insurer as written.
func TestPASAnswerRequestOf(t *testing.T) {
	bundle := []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Patient","id":"p"}},` +
		`{"resource":{"resourceType":"Claim","type":{"coding":[{"code":"professional"}]} ,"insurer":{"reference":"Organization/o"}}}]}`)
	got, err := PASAnswerRequestOf(bundle)
	if err != nil || string(got.ClaimType) != `{"coding":[{"code":"professional"}]}` || string(got.Insurer) != `{"reference":"Organization/o"}` {
		t.Fatalf("PASAnswerRequestOf = %+v, %v", got, err)
	}
	if _, err := PASAnswerRequestOf([]byte(`{"resourceType":"Bundle","entry":[]}`)); !errors.Is(err, ErrPASAnswerSourceAbsent) {
		t.Fatalf("a bundle with no Claim: err=%v, want ErrPASAnswerSourceAbsent", err)
	}
}

// answerClaimResponse is the ClaimResponse an answer carries, bare or as the
// first entry of its Bundle.
func answerClaimResponse(t *testing.T, out []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["resourceType"]) == `"Bundle"` {
		var b struct {
			Entry []struct {
				Resource map[string]json.RawMessage `json:"resource"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(out, &b); err != nil || len(b.Entry) == 0 {
			t.Fatalf("answer bundle: %v", err)
		}
		return b.Entry[0].Resource
	}
	return m
}

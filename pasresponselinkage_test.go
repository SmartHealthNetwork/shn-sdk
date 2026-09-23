package shnsdk

import "testing"

func TestPASResponseLinkage(t *testing.T) {
	request := []byte(`{"resourceType":"Bundle","entry":[{"fullUrl":"https://provider.example/fhir/Claim/sent","resource":{"resourceType":"Claim","id":"sent","identifier":[{"system":"urn:claim","value":"submitted"}],"related":[{"claim":{"identifier":{"system":"urn:claim","value":"prior"}}}],"item":[{"sequence":1,"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-ItemTraceNumber","valueIdentifier":{"system":"urn:trace","value":"one"}}]}]}}]}`)
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"absent", ``, true},
		{"payer own identifier", `,"identifier":[{"system":"urn:payer","value":"new-response"}],"preAuthRef":"new-authorization"`, true},
		{"request identifier", `,"request":{"identifier":{"system":"urn:claim","value":"submitted"}}`, true},
		{"prior identifier", `,"request":{"identifier":{"system":"urn:claim","value":"prior"}}`, true},
		{"relative reference", `,"request":{"reference":"Claim/sent"}`, true},
		{"exact full url", `,"request":{"reference":"https://provider.example/fhir/Claim/sent"}`, true},
		{"unrelated identifier", `,"request":{"identifier":{"system":"urn:claim","value":"foreign"}}`, false},
		{"other identifier namespace", `,"request":{"identifier":{"system":"urn:other","value":"submitted"}}`, false},
		{"foreign reference", `,"request":{"reference":"https://foreign.example/fhir/Claim/sent"}`, false},
		{"conflicting dual request", `,"request":{"reference":"Claim/foreign","identifier":{"system":"urn:claim","value":"submitted"}}`, false},
		{"unresolved display only", `,"request":{"display":"request"}`, false},
		{"malformed request", `,"request":"Claim/sent"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := []byte(`{"resourceType":"ClaimResponse"` + tc.fields + `}`)
			err := ValidatePASResponseLinkage(request, answer)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestContinuationResponseLinkage(t *testing.T) {
	c := PriorAuthContinuation{ClaimIdentifiers: []PASIdentifier{{System: "urn:claim", Value: "known"}}, Items: []PASInquiryItem{{TraceNumber: PASIdentifier{System: "urn:trace", Value: "one"}}}}
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"absent", ``, true},
		{"known request", `,"request":{"identifier":{"system":"urn:claim","value":"known"}}`, true},
		{"wrong request", `,"request":{"identifier":{"system":"urn:claim","value":"other"}}`, false},
		{"URL unavailable", `,"request":{"reference":"Claim/known"}`, false},
		{"known trace", `,"item":[{"extension":[{"url":"` + pasExtItemTraceNumber + `","valueIdentifier":{"system":"urn:trace","value":"one"}}]}]`, true},
		{"wrong trace", `,"item":[{"extension":[{"url":"` + pasExtItemTraceNumber + `","valueIdentifier":{"system":"urn:trace","value":"other"}}]}]`, false},
		{"match cannot hide conflict", `,"request":{"identifier":{"system":"urn:claim","value":"known"}},"item":[{"extension":[{"url":"` + pasExtItemTraceNumber + `","valueIdentifier":{"system":"urn:trace","value":"other"}}]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := c.ValidateResponseLinkage([]byte(`{"resourceType":"ClaimResponse"` + tc.fields + `}`))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

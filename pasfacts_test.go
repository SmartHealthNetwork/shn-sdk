package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Rows for the request facts a PAS Claim states: each is carried byte for byte
// from its source (the caller, else the order), omitted when optional at the
// line and absent, and REFUSED — before anything is built — when required and
// absent, with the element and the expected source named. Submit and update, at
// every line.

var pasLines = []string{"2.0", "2.1", "2.2"}

// compactJSON is the JSON value's canonical form: every member and value as
// written, object members in sorted order. The builders assemble the bundle
// through generic JSON maps, which order an object's members; FHIR JSON gives
// member order no meaning, so "byte for byte" here is every member and value.
func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	return canonJSON(raw)
}

func canonJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// wrapCoding is the CodeableConcept a single carried coding becomes.
func wrapCoding(coding string) string { return canonJSON([]byte(`{"coding":[` + coding + `]}`)) }

// bundleClaims returns the bundle's Claim resources in entry order (the
// operative Claim first; an update's prior-Claim restatement after it).
func bundleClaims(t *testing.T, bundleJSON []byte) []map[string]json.RawMessage {
	t.Helper()
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	var out []map[string]json.RawMessage
	for _, e := range bundle.Entry {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(e.Resource, &m); err != nil {
			continue
		}
		if string(m["resourceType"]) == `"Claim"` {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		t.Fatal("bundle has no Claim")
	}
	return out
}

func itemExtension(t *testing.T, item map[string]json.RawMessage, url string) (json.RawMessage, bool) {
	t.Helper()
	var exts []struct {
		URL   string          `json:"url"`
		Value json.RawMessage `json:"valueCodeableConcept"`
	}
	if raw, ok := item["extension"]; ok {
		if err := json.Unmarshal(raw, &exts); err != nil {
			t.Fatalf("item extension: %v", err)
		}
	}
	for _, e := range exts {
		if e.URL == url {
			return e.Value, true
		}
	}
	return nil, false
}

type factBuild func(line string, f PASClaimFacts, order []byte) ([]byte, error)

func submitBuild(t *testing.T) factBuild {
	return func(line string, f PASClaimFacts, order []byte) ([]byte, error) {
		in := conformantSubmitInputs(t)
		in.PASClaimFacts = f
		if order != nil {
			in.SR = order
		}
		return BuildConformantClaimBundleAtLineExact(line, in)
	}
}

func updateBuild(t *testing.T) factBuild {
	return func(line string, f PASClaimFacts, order []byte) ([]byte, error) {
		in := conformantUpdateInputsFromGolden(t)
		in.PASClaimFacts = f
		if order != nil {
			in.SR = order
		}
		return BuildConformantClaimUpdateBundleAtLineExact(line, in)
	}
}

func TestPASClaimFacts_CarriedByteForByte(t *testing.T) {
	for name, build := range map[string]factBuild{"submit": submitBuild(t), "update": updateBuild(t)} {
		for _, line := range pasLines {
			out, err := build(line, testClaimFacts(), nil)
			if err != nil {
				t.Fatalf("%s %s: %v", name, line, err)
			}
			claim := bundleClaims(t, out)[0]
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(claim["item"], &items); err != nil {
				t.Fatal(err)
			}
			for el, pair := range map[string][2]string{
				"type":     {compactJSON(t, claim["type"]), wrapCoding(testClaimTypeJSON)},
				"priority": {compactJSON(t, claim["priority"]), wrapCoding(testPriorityJSON)},
				"category": {compactJSON(t, items[0]["category"]), wrapCoding(testCategoryJSON)},
				"location": {compactJSON(t, items[0]["locationCodeableConcept"]), wrapCoding(testPlaceOfServiceJSON)},
			} {
				if pair[0] != pair[1] {
					t.Errorf("%s %s Claim %s = %s, want %s", name, line, el, pair[0], pair[1])
				}
			}
			// The stated request type is carried at every line (0..1 at 2.0).
			reqType, hasReq := itemExtension(t, items[0], pasExtServiceItemRequestType)
			if !hasReq || compactJSON(t, reqType) != wrapCoding(testRequestTypeJSON) {
				t.Errorf("%s %s requestType = %s, want %s", name, line, reqType, wrapCoding(testRequestTypeJSON))
			}
		}
	}
}

func TestPASClaimFacts_AbsentRequiredRefuses(t *testing.T) {
	type row struct {
		element string
		drop    func(*PASClaimFacts)
		lines   []string // the lines at which absence refuses
		source  string
	}
	rows := []row{
		{"Claim.type", func(f *PASClaimFacts) { f.ClaimType = nil }, pasLines, "ClaimType"},
		{"Claim.priority", func(f *PASClaimFacts) { f.Priority = nil }, pasLines, "Priority"},
		{"Claim.item.category", func(f *PASClaimFacts) { f.ServiceCategory = nil }, pasLines, "ServiceCategory"},
		{"Claim.item.location[x]", func(f *PASClaimFacts) { f.PlaceOfService = nil }, []string{"2.1", "2.2"}, "PlaceOfService"},
		{"Claim.item requestType", func(f *PASClaimFacts) { f.RequestType = nil }, []string{"2.1", "2.2"}, "RequestType"},
	}
	for name, build := range map[string]factBuild{"submit": submitBuild(t), "update": updateBuild(t)} {
		for _, r := range rows {
			for _, line := range pasLines {
				f := testClaimFacts()
				r.drop(&f)
				out, err := build(line, f, nil)
				refuses := false
				for _, l := range r.lines {
					refuses = refuses || l == line
				}
				if !refuses {
					if err != nil {
						t.Errorf("%s %s without %s: %v, want it built (optional at this line)", name, line, r.element, err)
						continue
					}
					if r.element == "Claim.item.location[x]" {
						if _, has := bundleClaims(t, out)[0]["item"]; has {
							var items []map[string]json.RawMessage
							_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
							if _, loc := items[0]["locationCodeableConcept"]; loc {
								t.Errorf("%s 2.0 without a place of service wrote one", name)
							}
						}
					}
					continue
				}
				if err == nil || out != nil {
					t.Errorf("%s %s without %s: built (%d bytes), want a refusal", name, line, r.element, len(out))
					continue
				}
				if !errors.Is(err, ErrPASClaimFactAbsent) || !strings.Contains(err.Error(), r.element) || !strings.Contains(err.Error(), r.source) || !strings.Contains(err.Error(), "fidelity of SHN's own build") {
					t.Errorf("%s %s without %s: %v, want ErrPASClaimFactAbsent naming the element and %q", name, line, r.element, err, r.source)
				}
			}
		}
	}
}

// orderWithFacts is a ServiceRequest stating its own priority, category and
// locationCode. No published mapping derives the Claim's facts from them, so
// the builder never reads them.
func orderWithFacts(t *testing.T) []byte {
	t.Helper()
	sr, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(sr, &m); err != nil {
		t.Fatal(err)
	}
	m["priority"] = json.RawMessage(`"urgent"`)
	m["category"] = json.RawMessage(`[{"coding":[{"system":"https://codesystem.x12.org/005010/1365","code":"1","display":"Medical Care"}]}]`)
	m["locationCode"] = json.RawMessage(`[{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set","code":"12","display":"Home"}]}]`)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPASClaimFacts_NeverReadFromTheOrder(t *testing.T) {
	order := orderWithFacts(t)
	for name, build := range map[string]factBuild{"submit": submitBuild(t), "update": updateBuild(t)} {
		for _, line := range pasLines {
			for el, drop := range map[string]func(*PASClaimFacts){
				"Claim.priority":      func(f *PASClaimFacts) { f.Priority = nil },
				"Claim.item.category": func(f *PASClaimFacts) { f.ServiceCategory = nil },
			} {
				f := testClaimFacts()
				drop(&f)
				out, err := build(line, f, order)
				if err == nil || out != nil || !errors.Is(err, ErrPASClaimFactAbsent) || !strings.Contains(err.Error(), el) {
					t.Errorf("%s %s: the order states %s and the requester does not: err=%v, want a refusal (the order is never read)", name, line, el, err)
				}
			}
			f := testClaimFacts()
			f.PlaceOfService = nil
			out, err := build(line, f, order)
			if line == "2.0" {
				if err != nil {
					t.Fatalf("%s 2.0: %v", name, err)
				}
				var items []map[string]json.RawMessage
				_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
				if _, has := items[0]["locationCodeableConcept"]; has {
					t.Errorf("%s 2.0: the order's locationCode became the Claim's place of service", name)
				}
			} else if err == nil {
				t.Errorf("%s %s: place of service read from the order", name, line)
			}
		}
	}
}

// A location in either code system X12278LocationType includes is carried as
// stated: a NUBC type of bill is as valid as a CMS place of service.
func TestPASClaimFacts_NUBCLocationCarried(t *testing.T) {
	const nubc = `{"system":"https://www.nubc.org/CodeSystem/TypeOfBill","code":"0111","display":"Hospital inpatient, admit through discharge claim"}`
	for name, build := range map[string]factBuild{"submit": submitBuild(t), "update": updateBuild(t)} {
		for _, line := range pasLines {
			f := testClaimFacts()
			f.ClaimType = json.RawMessage(`{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"institutional"}`)
			f.PlaceOfService = json.RawMessage(nubc)
			out, err := build(line, f, nil)
			if err != nil {
				t.Fatalf("%s %s: a NUBC location refused: %v", name, line, err)
			}
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(bundleClaims(t, out)[0]["item"], &items); err != nil {
				t.Fatal(err)
			}
			if got := compactJSON(t, items[0]["locationCodeableConcept"]); got != wrapCoding(nubc) {
				t.Errorf("%s %s location = %s, want %s", name, line, got, wrapCoding(nubc))
			}
		}
	}
}

func TestPASClaimFacts_InvalidCodingRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*PASClaimFacts)
	}{
		{"category from another system", func(f *PASClaimFacts) {
			f.ServiceCategory = json.RawMessage(`{"system":"http://snomed.info/sct","code":"1"}`)
		}},
		{"place of service from another system", func(f *PASClaimFacts) {
			f.PlaceOfService = json.RawMessage(`{"system":"urn:x","code":"11"}`)
		}},
		{"request type from another system", func(f *PASClaimFacts) {
			f.RequestType = json.RawMessage(`{"system":"urn:x","code":"IN"}`)
		}},
		{"priority with no system", func(f *PASClaimFacts) { f.Priority = json.RawMessage(`{"code":"normal"}`) }},
		{"claim type with no code", func(f *PASClaimFacts) {
			f.ClaimType = json.RawMessage(`{"system":"http://terminology.hl7.org/CodeSystem/claim-type"}`)
		}},
	} {
		f := testClaimFacts()
		tc.set(&f)
		out, err := submitBuild(t)("2.2", f, nil)
		if err == nil || out != nil || !errors.Is(err, ErrPASClaimFactInvalid) {
			t.Errorf("%s: err=%v, want ErrPASClaimFactInvalid and nothing built", tc.name, err)
		}
	}
}

func TestPASClaimFacts_SubmitCertificationTypeIsTheOperation(t *testing.T) {
	for _, line := range pasLines {
		out, err := submitBuild(t)(line, testClaimFacts(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
		cert, has := itemExtension(t, items[0], pasExtCertificationType)
		if line == "2.0" {
			if has {
				t.Errorf("2.0 submit wrote certificationType")
			}
			continue
		}
		if !has || compactJSON(t, cert) != compactJSON(t, pasSubmitCertificationType) {
			t.Errorf("%s submit certificationType = %s, want X12 1322 I (a new request)", line, cert)
		}
	}
}

// TestPASUpdate_CertificationTypeFromTheCaller: an amendment's
// certificationType is the caller's, carried exactly as stated at every line
// (the PAS 2.0.1 Claim allows Claim.item.extension:certificationType 0..1,
// extension-certificationType, valueCodeableConcept bound required to X12
// 1322), ahead of the request type as from 2.1. Absent, it is omitted at 2.0
// and refused from 2.1, where it is 1..1.
func TestPASUpdate_CertificationTypeFromTheCaller(t *testing.T) {
	const revised = `{"system":"https://codesystem.x12.org/005010/1322","code":"I","display":"as the caller stated it"}`
	for _, line := range pasLines {
		in := conformantUpdateInputsFromGolden(t)
		in.CertificationType = json.RawMessage(revised)
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
		cert, has := itemExtension(t, items[0], pasExtCertificationType)
		if !has || compactJSON(t, cert) != wrapCoding(revised) {
			t.Errorf("%s update certificationType = %s, want the caller's %s", line, cert, wrapCoding(revised))
		}
		var exts []struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(items[0]["extension"], &exts)
		var order []string
		for _, e := range exts {
			if e.URL == pasExtCertificationType || e.URL == pasExtServiceItemRequestType {
				order = append(order, e.URL)
			}
		}
		if len(order) != 2 || order[0] != pasExtCertificationType {
			t.Errorf("%s update item extensions = %q, want certificationType then requestType", line, order)
		}
		in.CertificationType = nil
		out, err = BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if line == "2.0" {
			if err != nil {
				t.Errorf("2.0 update without a certificationType: %v, want built", err)
				continue
			}
			_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
			if cert, has := itemExtension(t, items[0], pasExtCertificationType); has {
				t.Errorf("2.0 update without a certificationType wrote one: %s", cert)
			}
			continue
		}
		if err == nil || out != nil || !errors.Is(err, ErrPASClaimFactAbsent) || !strings.Contains(err.Error(), "certificationType") || !strings.Contains(err.Error(), "the requester's CertificationType") {
			t.Errorf("%s update without a certificationType: %v, want a refusal naming it", line, err)
		}
	}
}

// TestPASUpdate_CertificationTypeOutsideX12Refused: a stated
// certificationType outside the X12 1322 system is refused at every line, 2.0
// included (its binding is required there too), and nothing is built; the
// pre-flight check answers the same.
func TestPASUpdate_CertificationTypeOutsideX12Refused(t *testing.T) {
	for _, line := range pasLines {
		in := conformantUpdateInputsFromGolden(t)
		in.CertificationType = json.RawMessage(`{"system":"http://example.org/not-x12","code":"I"}`)
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err == nil || out != nil || !errors.Is(err, ErrPASClaimFactInvalid) || !strings.Contains(err.Error(), "certificationType") {
			t.Errorf("%s update with a non-X12 certificationType: %v, want ErrPASClaimFactInvalid and nothing built", line, err)
		}
		if err := CheckPASUpdateFactsAtLine(line, in.PASClaimFacts, in.CertificationType); !errors.Is(err, ErrPASClaimFactInvalid) {
			t.Errorf("%s CheckPASUpdateFactsAtLine with a non-X12 certificationType: %v, want ErrPASClaimFactInvalid", line, err)
		}
	}
}

// TestPASUpdate_InfoChangedDerivedFromThePriorClaim: the update builder
// derives the PAS infoChanged marker from the recorded submission, item by
// item (PAS 2.2.1 PASTempCodes; the IG's Claim-HomecareAuthorizationUpdateExample
// pattern): an item sent before exactly as now carries none, one whose
// sequence the submission had no item for carries "added", one that differs
// from the submitted item carries "changed". No caller states it. At every
// line, against the submission built at that line.
func TestPASUpdate_InfoChangedDerivedFromThePriorClaim(t *testing.T) {
	marker := func(t *testing.T, out []byte) (string, int) {
		t.Helper()
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
		var exts []struct {
			URL       string `json:"url"`
			ValueCode string `json:"valueCode"`
		}
		_ = json.Unmarshal(items[0]["extension"], &exts)
		code, n := "", 0
		for _, e := range exts {
			if e.URL == pasInfoChangedExtensionURL {
				code, n = e.ValueCode, n+1
			}
		}
		return code, n
	}
	editItem := func(t *testing.T, p PASPriorClaim, edit func(map[string]any)) PASPriorClaim {
		t.Helper()
		var it map[string]any
		if err := json.Unmarshal(p.Items[0], &it); err != nil {
			t.Fatal(err)
		}
		edit(it)
		raw, _ := json.Marshal(it)
		p.Items = []json.RawMessage{raw}
		return p
	}
	for _, line := range pasLines {
		t.Run(line, func(t *testing.T) {
			prior := testPriorClaimAt(t, line)
			for name, tc := range map[string]struct {
				prior PASPriorClaim
				want  string
			}{
				"unchanged": {prior, ""},
				// The submission held the same item already marked: still unchanged.
				"unchanged, the submitted item marked": {editItem(t, prior, func(it map[string]any) {
					ext, _ := it["extension"].([]any)
					it["extension"] = append(ext, map[string]any{"url": pasInfoChangedExtensionURL, "valueCode": "changed"})
				}), ""},
				"added": {editItem(t, prior, func(it map[string]any) { it["sequence"] = 2 }), pasInfoChangedAdded},
				"changed": {editItem(t, prior, func(it map[string]any) {
					it["category"] = map[string]any{"coding": []any{map[string]any{"system": PASSystemX12ServiceType, "code": "42"}}}
				}), pasInfoChangedChanged},
				// A member the item did not carry before is a change too.
				"changed by a member added": {editItem(t, prior, func(it map[string]any) { delete(it, "category") }), pasInfoChangedChanged},
				// The certificationType is compared only when both sides state one:
				// a submitted item of another kind is changed (PAS 2.2.1
				// Claim-HomecareAuthorizationUpdateExample marks its item 2, "I" then
				// "3", changed), while one stating none (as a 2.0 submission states
				// none) leaves nothing to compare and is the same item.
				"changed, the submitted item of another kind": {editItem(t, prior, func(it map[string]any) {
					ext, _ := it["extension"].([]any)
					kept := []any{map[string]any{"url": pasExtCertificationType, "valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"system": PASSystemX12CertificationType, "code": "S"}}}}}
					for _, e := range ext {
						if em, _ := e.(map[string]any); em["url"] != pasExtCertificationType {
							kept = append(kept, e)
						}
					}
					it["extension"] = kept
				}), pasInfoChangedChanged},
				"unchanged, the submitted item stating no kind": {editItem(t, prior, func(it map[string]any) {
					ext, _ := it["extension"].([]any)
					var kept []any
					for _, e := range ext {
						if em, _ := e.(map[string]any); em["url"] != pasExtCertificationType {
							kept = append(kept, e)
						}
					}
					if kept == nil {
						delete(it, "extension")
					} else {
						it["extension"] = kept
					}
				}), ""},
			} {
				in := conformantUpdateInputsFromGolden(t)
				in.PriorClaim = tc.prior
				out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				code, n := marker(t, out)
				switch {
				case tc.want == "" && n != 0:
					t.Errorf("%s: the item carries infoChanged %q, want none", name, code)
				case tc.want != "" && (n != 1 || code != tc.want):
					t.Errorf("%s: infoChanged %q ×%d, want %q once", name, code, n, tc.want)
				}
			}
		})
	}
}

// The prior-Claim entry restates the submitted type and priority exactly as
// sent: every member of every coding (version, userSelected, an extension)
// and the CodeableConcept's text, never rebuilt from system and code.
const (
	testSubmittedTypeJSON     = `{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","version":"1.0.0","code":"professional","display":"Professional","userSelected":true,"extension":[{"url":"http://example.org/ext","valueString":"as the requester's system wrote it"}]}],"text":"Professional"}`
	testSubmittedPriorityJSON = `{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"stat","display":"Immediate"}]}`
)

func TestPASUpdate_PriorClaimRestatesTheRecordedSubmission(t *testing.T) {
	for _, line := range pasLines {
		prior := testPriorClaimAt(t, line)
		prior.ClaimType = json.RawMessage(testSubmittedTypeJSON)
		prior.Priority = json.RawMessage(testSubmittedPriorityJSON)
		prior.Created = "2026-05-30T08:15:00-04:00"
		in := conformantUpdateInputsFromGolden(t)
		in.PriorClaim = prior
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		claims := bundleClaims(t, out)
		if def, _ := PASLineDef(line); !def.UpdateCarriesPriorClaim {
			// PAS 2.0.1's request Bundle allows one Claim: the update names its prior by
			// identifier and restates nothing.
			if len(claims) != 1 {
				t.Fatalf("%s: %d Claims, want the update alone", line, len(claims))
			}
			continue
		}
		if len(claims) != 2 {
			t.Fatalf("%s: %d Claims, want the update and the prior-Claim restatement", line, len(claims))
		}
		p := claims[1]
		if got := string(p["created"]); got != `"2026-05-30T08:15:00-04:00"` {
			t.Errorf("%s prior created = %s, want the recorded submission's, as sent", line, got)
		}
		if got := compactJSON(t, p["type"]); got != canonJSON([]byte(testSubmittedTypeJSON)) {
			t.Errorf("%s prior type = %s, want the submitted CodeableConcept as sent", line, got)
		}
		if got := compactJSON(t, p["priority"]); got != canonJSON([]byte(testSubmittedPriorityJSON)) {
			t.Errorf("%s prior priority = %s", line, got)
		}
		if bytes.Contains(p["created"], []byte(in.Created.UTC().Format("2006-01-02T15"))) {
			t.Errorf("%s prior created restates the amendment's own time", line)
		}
	}
}

func TestPASUpdate_PriorClaimWithoutRecordedCreatedRefuses(t *testing.T) {
	for _, line := range pasLines {
		for name, mutate := range map[string]func(*PASPriorClaim){
			"no created (a record written before this release)": func(p *PASPriorClaim) { p.Created = "" },
			"no type":     func(p *PASPriorClaim) { p.ClaimType = nil },
			"type null":   func(p *PASPriorClaim) { p.ClaimType = json.RawMessage("null") },
			"no priority": func(p *PASPriorClaim) { p.Priority = nil },
			"no items":    func(p *PASPriorClaim) { p.Items = nil },
		} {
			in := conformantUpdateInputsFromGolden(t)
			mutate(&in.PriorClaim)
			out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
			if err == nil || out != nil || !errors.Is(err, ErrPASClaimFactAbsent) || !strings.Contains(err.Error(), "prior Claim") {
				t.Errorf("%s %s: err=%v, want a refusal naming the prior Claim", line, name, err)
			}
			if strings.HasPrefix(name, "no created") && (err == nil || !strings.Contains(err.Error(), "written before this release")) {
				t.Errorf("%s: %v, want the refusal to say the record predates this release", line, err)
			}
		}
	}
}

func TestPriorAuthContinuation_RecordsCreated(t *testing.T) {
	out, err := BuildConformantClaimBundleAtLineExact("2.2", conformantSubmitInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewPriorAuthContinuation("2.2", "payer-holder", "MBR-COVERED", out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Created != "2026-06-01T12:00:00Z" {
		t.Errorf("continuation created = %q, want the submitted Claim.created", c.Created)
	}
	p, err := PASPriorClaimOf(out)
	if err != nil {
		t.Fatal(err)
	}
	if p.Created != c.Created || !strings.Contains(string(p.ClaimType), `"professional"`) || !strings.Contains(string(p.Priority), PASSystemProcessPriority) || len(p.Items) != 1 {
		t.Errorf("PASPriorClaimOf = %+v, want the submitted Claim's own type, priority, created and item", p)
	}
	// An empty record (a handle written before v0.62.0 carries none) is refused.
	if err := (PASPriorClaim{}).check(); !errors.Is(err, ErrPASClaimFactAbsent) {
		t.Errorf("an empty record: %v, want it refused", err)
	}
}

// TestPASClaimFacts_NullIsAbsent: a fact stated as JSON null states nothing
// (FHIR JSON has no null value). Required at the line, it is refused as
// absent — never as an invalid coding; optional at the line (place of service
// and request type at 2.0), it is omitted. Submit and update, every line.
func TestPASClaimFacts_NullIsAbsent(t *testing.T) {
	null := json.RawMessage("null")
	rows := []struct {
		element string
		set     func(*PASClaimFacts)
		lines   []string // the lines at which absence refuses
	}{
		{"Claim.type", func(f *PASClaimFacts) { f.ClaimType = null }, pasLines},
		{"Claim.priority", func(f *PASClaimFacts) { f.Priority = null }, pasLines},
		{"Claim.item.category", func(f *PASClaimFacts) { f.ServiceCategory = null }, pasLines},
		{"Claim.item.location[x]", func(f *PASClaimFacts) { f.PlaceOfService = null }, []string{"2.1", "2.2"}},
		{"Claim.item requestType", func(f *PASClaimFacts) { f.RequestType = null }, []string{"2.1", "2.2"}},
	}
	for name, build := range map[string]factBuild{"submit": submitBuild(t), "update": updateBuild(t)} {
		for _, r := range rows {
			for _, line := range pasLines {
				f := testClaimFacts()
				r.set(&f)
				out, err := build(line, f, nil)
				refuses := false
				for _, l := range r.lines {
					refuses = refuses || l == line
				}
				if !refuses {
					if err != nil {
						t.Errorf("%s %s with %s null: %v, want it built with the element omitted", name, line, r.element, err)
						continue
					}
					if bytes.Contains(out, []byte("null")) {
						t.Errorf("%s %s with %s null wrote a null", name, line, r.element)
					}
					var items []map[string]json.RawMessage
					_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
					if _, loc := items[0]["locationCodeableConcept"]; loc && r.element == "Claim.item.location[x]" {
						t.Errorf("%s %s with a null place of service wrote one", name, line)
					}
					continue
				}
				if err == nil || out != nil || !errors.Is(err, ErrPASClaimFactAbsent) || errors.Is(err, ErrPASClaimFactInvalid) || !strings.Contains(err.Error(), r.element) {
					t.Errorf("%s %s with %s null: %v, want it refused as absent", name, line, r.element, err)
				}
			}
		}
	}
	// The amendment's certificationType stated as null is absent too.
	in := conformantUpdateInputsFromGolden(t)
	in.CertificationType = null
	for _, line := range pasLines {
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if line == "2.0" {
			if err != nil {
				t.Errorf("2.0 update with a null certificationType: %v, want built", err)
				continue
			}
			var items []map[string]json.RawMessage
			_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
			if cert, has := itemExtension(t, items[0], pasExtCertificationType); has {
				t.Errorf("2.0 update with a null certificationType wrote one: %s", cert)
			}
			continue
		}
		if out != nil || !errors.Is(err, ErrPASClaimFactAbsent) || !strings.Contains(err.Error(), "certificationType") {
			t.Errorf("%s update with a null certificationType: %v, want it refused as absent", line, err)
		}
	}
}

// TestPASClaimFacts_CheckIsTheBuildersRule: CheckPASClaimFactsAtLine and
// CheckPASUpdateFactsAtLine answer exactly what the builders answer for the
// same facts — nil where they build, the same refusal text where they refuse —
// so a caller checking before its first leg cannot drift from the build.
func TestPASClaimFacts_CheckIsTheBuildersRule(t *testing.T) {
	null := json.RawMessage("null")
	wrong := json.RawMessage(`{"system":"http://example.org/not-the-system","code":"X"}`)
	edits := map[string]func(*PASClaimFacts){
		"all stated":           func(*PASClaimFacts) {},
		"type absent":          func(f *PASClaimFacts) { f.ClaimType = nil },
		"priority null":        func(f *PASClaimFacts) { f.Priority = null },
		"category wrong":       func(f *PASClaimFacts) { f.ServiceCategory = wrong },
		"location absent":      func(f *PASClaimFacts) { f.PlaceOfService = nil },
		"location wrong":       func(f *PASClaimFacts) { f.PlaceOfService = wrong },
		"request type absent":  func(f *PASClaimFacts) { f.RequestType = nil },
		"request type invalid": func(f *PASClaimFacts) { f.RequestType = json.RawMessage(`{"code":"AR"}`) },
	}
	certs := map[string]json.RawMessage{
		"cert stated": json.RawMessage(`{"system":"https://codesystem.x12.org/005010/1322","code":"I"}`),
		"cert absent": nil,
		"cert null":   null,
		"cert wrong":  wrong,
	}
	errText := func(err error) string {
		if err == nil {
			return "<nil>"
		}
		return err.Error()
	}
	refusals := 0
	for _, line := range pasLines {
		for name, edit := range edits {
			f := testClaimFacts()
			edit(&f)
			sin := conformantSubmitInputs(t)
			sin.PASClaimFacts = f
			_, buildErr := BuildConformantClaimBundleAtLineExact(line, sin)
			if got, want := errText(CheckPASClaimFactsAtLine(line, f)), errText(buildErr); got != want {
				t.Errorf("submit %s %s: check %q, build %q", line, name, got, want)
			}
			for cname, cert := range certs {
				uin := conformantUpdateInputsFromGolden(t)
				uin.PASClaimFacts, uin.CertificationType = f, cert
				_, buildErr := BuildConformantClaimUpdateBundleAtLineExact(line, uin)
				if got, want := errText(CheckPASUpdateFactsAtLine(line, f, cert)), errText(buildErr); got != want {
					t.Errorf("update %s %s %s: check %q, build %q", line, name, cname, got, want)
				}
				if buildErr != nil {
					refusals++
				}
			}
		}
	}
	if refusals == 0 {
		t.Fatal("no row refused: the comparison never saw a refusal")
	}
	if err := CheckPASClaimFactsAtLine("9.9", testClaimFacts()); err == nil {
		t.Error("an unknown line was checked as buildable")
	}
}

// TestPASClaimFacts_RequestTypeCarriedAtEveryLine: a stated request type is
// carried on the item's requestType extension (extension-
// serviceItemRequestType, 0..1 on Claim.item at PAS 2.0.1, 1..1 from 2.1.0)
// at every line, submit and update, byte for byte; at 2.0, where it is
// optional, an absent one is omitted and the build is not refused.
func TestPASClaimFacts_RequestTypeCarriedAtEveryLine(t *testing.T) {
	for name, build := range map[string]factBuild{"submit": submitBuild(t), "update": updateBuild(t)} {
		for _, line := range pasLines {
			out, err := build(line, testClaimFacts(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var items []map[string]json.RawMessage
			_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
			rt, hasRT := itemExtension(t, items[0], pasExtServiceItemRequestType)
			if !hasRT || canonJSON(rt) != wrapCoding(testRequestTypeJSON) {
				t.Errorf("%s %s: requestType %s (written=%v), want the stated coding", name, line, rt, hasRT)
			}
		}
		f := testClaimFacts()
		f.RequestType = nil
		out, err := build("2.0", f, nil)
		if err != nil {
			t.Fatalf("%s 2.0: an absent request type is refused: %v", name, err)
		}
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(bundleClaims(t, out)[0]["item"], &items)
		if _, hasRT := itemExtension(t, items[0], pasExtServiceItemRequestType); hasRT {
			t.Errorf("%s 2.0: an absent request type is written", name)
		}
	}
}

// An amendment's certificationType is compared by what it states, system and
// code: one restated without the display the submission carried states the
// same kind of request and marks nothing, and a different code marks the item
// changed.
func TestPASUpdate_CertificationTypeComparedBySystemAndCode(t *testing.T) {
	item := func(cert string) json.RawMessage {
		return json.RawMessage(`{"sequence":1,"productOrService":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"72148"}]},` +
			`"extension":[{"url":"` + pasExtCertificationType + `","valueCodeableConcept":{"coding":[` + cert + `]}}]}`)
	}
	bundle := func(it json.RawMessage) []byte {
		return []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim","item":[` + string(it) + `]}}]}`)
	}
	sent := item(`{"system":"` + PASSystemX12CertificationType + `","code":"I","display":"Initial"}`)
	for name, tc := range map[string]struct {
		cert string
		want string
	}{
		"the same code without its display": {`{"system":"` + PASSystemX12CertificationType + `","code":"I"}`, ""},
		"another code":                      {`{"system":"` + PASSystemX12CertificationType + `","code":"4","display":"Revised"}`, pasInfoChangedChanged},
	} {
		marks, err := pasInfoChangedMarks(bundle(item(tc.cert)), []json.RawMessage{sent})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if marks[0] != tc.want {
			t.Errorf("%s: marked %q, want %q", name, marks[0], tc.want)
		}
	}
}

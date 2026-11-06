package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// The supplemental records an amendment carries — the report and the
// Provenance that attributes it — are a participant's own records: a
// facility's, read on the federated query, or the provider's own. The
// Carried builders (BuildConformantClaimUpdateBundleCarried and its AtLine
// form), which every row in this file builds with, place them in the update
// Bundle as the participant wrote them:
// their own ids, their meta (profile included), their members in their own
// order and spelling, with each entry's fullUrl built from the resource's own
// id. Nothing about the bytes changes.

const (
	carriedReportID     = "facility-dr-7"
	carriedProvenanceID = "facility-prov-3"
)

// encodeAsWritten encodes v the way a participant's own system might: indented,
// with < > & written as themselves.
func encodeAsWritten(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "   ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// carriedRecordInputs are the golden amendment inputs at line, with a report
// and a Provenance as a participant's own system writes them: their own ids,
// a declared profile, a narrative holding markup, indented.
func carriedRecordInputs(t *testing.T, line string) ConformantClaimUpdateInputs {
	t.Helper()
	in := conformantUpdateInputsFromGolden(t)
	in.PriorClaim = testPriorClaimAt(t, line)
	var dr, prov map[string]any
	if err := json.Unmarshal(in.DiagnosticReport, &dr); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(in.Provenance, &prov); err != nil {
		t.Fatal(err)
	}
	dr["id"] = carriedReportID
	dr["meta"] = map[string]any{
		"versionId": "4",
		"profile":   []any{"http://hl7.org/fhir/us/core/StructureDefinition/us-core-diagnosticreport-note"},
	}
	dr["text"] = map[string]any{"status": "generated", "div": `<div xmlns="http://www.w3.org/1999/xhtml">Operative note &amp; findings &lt;as written&gt;</div>`}
	prov["id"] = carriedProvenanceID
	prov["meta"] = map[string]any{"profile": []any{"http://hl7.org/fhir/us/core/StructureDefinition/us-core-provenance"}}
	prov["target"] = []any{map[string]any{"reference": "DiagnosticReport/" + carriedReportID}}
	in.DiagnosticReport = encodeAsWritten(t, dr)
	in.Provenance = encodeAsWritten(t, prov)
	return in
}

// entryOfType returns the fullUrl and the exact resource bytes of the one
// entry of resourceType in bundle.
func entryOfType(t *testing.T, bundle []byte, resourceType string) (string, []byte) {
	t.Helper()
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	var url string
	var raw []byte
	n := 0
	for _, e := range b.Entry {
		var head struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(e.Resource, &head) == nil && head.ResourceType == resourceType {
			url, raw = e.FullURL, e.Resource
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d %s entries, want one", n, resourceType)
	}
	return url, raw
}

// TestPASUpdate_CarriesTheReportAsWritten: the report's bytes in the update
// Bundle are the bytes the participant wrote, and its fullUrl is built from its
// own id. Every line.
func TestPASUpdate_CarriesTheReportAsWritten(t *testing.T) {
	for _, line := range pasLines {
		in := carriedRecordInputs(t, line)
		out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		url, got := entryOfType(t, out, "DiagnosticReport")
		if !bytes.Equal(got, in.DiagnosticReport) {
			t.Errorf("%s: the report is not carried as written\n got: %s\nwant: %s", line, got, in.DiagnosticReport)
		}
		if want := pasBundleBaseURL + "/DiagnosticReport/" + carriedReportID; url != want {
			t.Errorf("%s: report fullUrl %q, want %q (its own id)", line, url, want)
		}
	}
}

// TestPASUpdate_CarriesTheProvenanceAsWritten: the Provenance's bytes are the
// bytes the participant wrote — its own id, its target as written, its meta —
// and its fullUrl is built from its own id. Every line.
func TestPASUpdate_CarriesTheProvenanceAsWritten(t *testing.T) {
	for _, line := range pasLines {
		in := carriedRecordInputs(t, line)
		out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		url, got := entryOfType(t, out, "Provenance")
		if !bytes.Equal(got, in.Provenance) {
			t.Errorf("%s: the Provenance is not carried as written\n got: %s\nwant: %s", line, got, in.Provenance)
		}
		if want := pasBundleBaseURL + "/Provenance/" + carriedProvenanceID; url != want {
			t.Errorf("%s: Provenance fullUrl %q, want %q (its own id)", line, url, want)
		}
	}
}

// TestPASUpdate_TheClaimNamesTheReportByItsOwnID: where the amendment's Claim
// links the attached answer (the pend asks for it), it names the report by
// the report's own id, which resolves to the report's entry.
func TestPASUpdate_TheClaimNamesTheReportByItsOwnID(t *testing.T) {
	for _, line := range pasLines {
		in := answerLinkInputs(t, line)
		carried := carriedRecordInputs(t, line)
		var dr map[string]any
		if err := json.Unmarshal(in.DiagnosticReport, &dr); err != nil {
			t.Fatal(err)
		}
		var own map[string]any
		_ = json.Unmarshal(carried.DiagnosticReport, &own)
		dr["id"] = own["id"]
		in.DiagnosticReport = encodeAsWritten(t, dr)
		in.Provenance = carried.Provenance
		in.Pend = answerLinkPend(t, line, PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
		if def, _ := PASLineDef(line); def.SupportingInfoDocumentInformationRequired {
			// The line links only on the requester's statement of the report's type.
			in.ReportType = json.RawMessage(`{"system":"` + pasSystemX12AttachmentReports + `","code":"PY"}`)
		}
		out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		v := answerLinkRead(t, out)
		if len(v.supportingInfo) == 0 {
			t.Fatalf("%s: the Claim links no answer", line)
		}
		var joined string
		for _, si := range v.supportingInfo {
			joined += string(si)
		}
		if !strings.Contains(joined, "DiagnosticReport/"+carriedReportID) {
			t.Errorf("%s: the Claim's supportingInfo does not name the report by its own id: %s", line, joined)
		}
		if strings.Contains(joined, conformantPASDRID) {
			t.Errorf("%s: the Claim names the report by the builder's fixed id: %s", line, joined)
		}
	}
}

// TestPASBuilders_KeepTheOrdersProfile: the order a submission and an
// amendment carry keeps the profile its record declares, whether the
// participant's own system wrote it or the SDK built it for a participant
// that cannot (BuildServiceRequestCoded declares the profile it builds to).
// Every line.
func TestPASBuilders_KeepTheOrdersProfile(t *testing.T) {
	const profile = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-servicerequest"
	withProfile := func(t *testing.T, sr []byte) []byte {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(sr, &m); err != nil {
			t.Fatal(err)
		}
		m["meta"] = map[string]any{"profile": []any{profile}}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	srProfile := func(t *testing.T, bundle []byte) []string {
		t.Helper()
		_, raw := entryOfType(t, bundle, "ServiceRequest")
		var sr struct {
			Meta struct {
				Profile []string `json:"profile"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(raw, &sr); err != nil {
			t.Fatal(err)
		}
		return sr.Meta.Profile
	}
	for _, line := range pasLines {
		sub := conformantSubmitInputs(t)
		sub.SR = withProfile(t, sub.SR)
		out, err := BuildConformantClaimBundleAtLineCarried(line, sub)
		if err != nil {
			t.Fatalf("%s submission: %v", line, err)
		}
		if got := srProfile(t, out); len(got) != 1 || got[0] != profile {
			t.Errorf("%s: the submission's order declares %v, want its record's %s", line, got, profile)
		}
		upd := carriedRecordInputs(t, line)
		upd.SR = withProfile(t, upd.SR)
		out, err = BuildConformantClaimUpdateBundleAtLineCarried(line, upd)
		if err != nil {
			t.Fatalf("%s amendment: %v", line, err)
		}
		if got := srProfile(t, out); len(got) != 1 || got[0] != profile {
			t.Errorf("%s: the amendment's order declares %v, want its record's %s", line, got, profile)
		}
	}
}

// TestPASCarriedBuilders_KeepEachRecordsProfile: on the Carried builders the
// Coverage, the person it names beside the member and the payer's
// Organization keep the profile their records declare; the Exact builders drop
// it (TestPASExactBuilders_KeepTheirBytes). Submission and amendment, every
// line.
func TestPASCarriedBuilders_KeepEachRecordsProfile(t *testing.T) {
	const parent = `{"resourceType":"Patient","id":"parent","meta":{"profile":["http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"]},"identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"name":[{"family":"Parent","given":["Pat"]}]}`
	withProfile := func(t *testing.T, rec []byte, profile string) []byte {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(rec, &m); err != nil {
			t.Fatal(err)
		}
		m["meta"] = map[string]any{"profile": []any{profile}}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	profileOf := func(t *testing.T, bundle []byte, resourceType, id string) []string {
		t.Helper()
		var b struct {
			Entry []struct {
				Resource struct {
					ResourceType string `json:"resourceType"`
					ID           string `json:"id"`
					Meta         struct {
						Profile []string `json:"profile"`
					} `json:"meta"`
				} `json:"resource"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(bundle, &b); err != nil {
			t.Fatal(err)
		}
		for _, e := range b.Entry {
			if e.Resource.ResourceType == resourceType && e.Resource.ID == id {
				return e.Resource.Meta.Profile
			}
		}
		t.Fatalf("no %s/%s entry", resourceType, id)
		return nil
	}
	const covProfile = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-coverage"
	const orgProfile = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-organization"
	coverage := entryCoverage("MBR-COVERED", "Patient/parent", []string{"subscriber"}, "")
	for _, line := range pasLines {
		sub := conformantSubmitInputs(t)
		sub.Coverage, sub.CoverageParties = withProfile(t, coverage, covProfile), [][]byte{[]byte(parent)}
		sub.Insurer = withProfile(t, sub.Insurer, orgProfile)
		built, err := BuildConformantClaimBundleAtLineCarried(line, sub)
		if err != nil {
			t.Fatalf("%s submission: %v", line, err)
		}
		upd := carriedRecordInputs(t, line)
		upd.Coverage, upd.CoverageParties, upd.Insurer = sub.Coverage, sub.CoverageParties, sub.Insurer
		if upd.PriorClaim, err = PASPriorClaimOf(built); err != nil {
			t.Fatal(err)
		}
		amended, err := BuildConformantClaimUpdateBundleAtLineCarried(line, upd)
		if err != nil {
			t.Fatalf("%s amendment: %v", line, err)
		}
		var org struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(sub.Insurer, &org); err != nil {
			t.Fatal(err)
		}
		for kind, bundle := range map[string][]byte{"submission": built, "amendment": amended} {
			for _, want := range []struct{ rt, id, profile string }{
				{"Coverage", "cov-mbr-covered", covProfile},
				{"Patient", "parent", "http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"},
				{"Organization", org.ID, orgProfile},
			} {
				if got := profileOf(t, bundle, want.rt, want.id); len(got) != 1 || got[0] != want.profile {
					t.Errorf("%s %s: %s/%s declares %v, want its record's %s", line, kind, want.rt, want.id, got, want.profile)
				}
			}
		}
	}
}

// TestPASUpdate_ARecordNotCarriableAsWrittenRefuses: a report or Provenance
// the builder cannot carry byte for byte under its own id refuses the
// amendment (ErrPASRecordNotCarried), the fidelity of SHN's own build: a
// second value after the record, bytes that are not UTF-8, a record with no
// id, a Provenance that does not attribute the report it rides with. Nothing
// is built. Every line.
func TestPASUpdate_ARecordNotCarriableAsWrittenRefuses(t *testing.T) {
	invalidUTF8 := func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"status"`), []byte("\"x\": \"\xff\",\n   \"status\""), 1)
	}
	without := func(t *testing.T, b []byte, key string) []byte {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, key)
		return encodeAsWritten(t, m)
	}
	for name, row := range map[string]struct {
		mutate func(*testing.T, *ConformantClaimUpdateInputs)
		want   string
	}{
		"a second value after the report": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.DiagnosticReport = append(in.DiagnosticReport, []byte(` {"resourceType":"DiagnosticReport"}`)...)
		}, "the supplemental report is not JSON, or not exactly one JSON object"},
		"a report behind white space JSON does not allow": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.DiagnosticReport = append([]byte("\u00a0"), in.DiagnosticReport...)
		}, "the supplemental report is not a JSON object"},
		"a report that is not UTF-8": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.DiagnosticReport = invalidUTF8(in.DiagnosticReport)
		}, "the supplemental report is not UTF-8"},
		"a report with no id": {func(t *testing.T, in *ConformantClaimUpdateInputs) {
			in.DiagnosticReport = without(t, in.DiagnosticReport, "id")
		}, "the supplemental report has no id"},
		"a second value after the Provenance": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.Provenance = append(in.Provenance, []byte(`{}`)...)
		}, "the Provenance attributing the report is not JSON, or not exactly one JSON object"},
		"a Provenance that is not UTF-8": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.Provenance = bytes.Replace(in.Provenance, []byte(`"recorded"`), []byte("\"x\": \"\xff\",\n   \"recorded\""), 1)
		}, "the Provenance attributing the report is not UTF-8"},
		"a Provenance with no id": {func(t *testing.T, in *ConformantClaimUpdateInputs) {
			in.Provenance = without(t, in.Provenance, "id")
		}, "the Provenance attributing the report has no id"},
		"a report whose id member is spelled Id": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.DiagnosticReport = bytes.Replace(in.DiagnosticReport, []byte(`"id"`), []byte(`"Id"`), 1)
		}, "the supplemental report has no id"},
		"a report with two id members": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.DiagnosticReport = bytes.Replace(in.DiagnosticReport, []byte(`"id"`), []byte(`"id": "another", "id"`), 1)
		}, "the supplemental report repeats a member"},
		"a Provenance whose resourceType member is spelled resourcetype": {func(_ *testing.T, in *ConformantClaimUpdateInputs) {
			in.Provenance = bytes.Replace(in.Provenance, []byte(`"resourceType"`), []byte(`"resourcetype"`), 1)
		}, "the Provenance attributing the report is not a Provenance"},
	} {
		for _, line := range pasLines {
			in := carriedRecordInputs(t, line)
			row.mutate(t, &in)
			out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
			if err == nil || out != nil {
				t.Errorf("%s %s: built %d bytes, want the amendment refused", line, name, len(out))
				continue
			}
			if !errors.Is(err, ErrPASRecordNotCarried) || !strings.Contains(err.Error(), "fidelity of SHN's own build") || !strings.Contains(err.Error(), row.want) {
				t.Errorf("%s %s: %v, want ErrPASRecordNotCarried naming %q", line, name, err, row.want)
			}
		}
	}
}

// TestConformantClaimBuilders_AbsoluteRefsHasNoEffect: the Carried builders write a
// Bundle's references as FHIR R4 resolves them, relative to the entries'
// fullUrls, on every lane; AbsoluteRefs changes nothing. Every line.
func TestConformantClaimBuilders_AbsoluteRefsHasNoEffect(t *testing.T) {
	for _, line := range pasLines {
		sub := conformantSubmitInputs(t)
		plain, err := BuildConformantClaimBundleAtLineCarried(line, sub)
		if err != nil {
			t.Fatalf("%s submission: %v", line, err)
		}
		sub.AbsoluteRefs = true
		set, err := BuildConformantClaimBundleAtLineCarried(line, sub)
		if err != nil {
			t.Fatalf("%s submission, AbsoluteRefs: %v", line, err)
		}
		if !bytes.Equal(plain, set) {
			t.Errorf("%s: AbsoluteRefs changed the submission", line)
		}
		upd := carriedRecordInputs(t, line)
		plain, err = BuildConformantClaimUpdateBundleAtLineCarried(line, upd)
		if err != nil {
			t.Fatalf("%s amendment: %v", line, err)
		}
		upd.AbsoluteRefs = true
		set, err = BuildConformantClaimUpdateBundleAtLineCarried(line, upd)
		if err != nil {
			t.Fatalf("%s amendment, AbsoluteRefs: %v", line, err)
		}
		if !bytes.Equal(plain, set) {
			t.Errorf("%s: AbsoluteRefs changed the amendment", line)
		}
	}
}

// r4Resolve is the absolute URL ref, a reference in the Bundle entry whose
// fullUrl is from, resolves to under FHIR R4's Bundle rules: an absolute
// reference is itself; a relative Type/id is appended to the root of from, a
// RESTful fullUrl ("" when from is not one).
func r4Resolve(from, ref string) string {
	if strings.Contains(ref, ":") {
		return ref
	}
	i := strings.LastIndex(from, "/")
	if i < 0 {
		return ""
	}
	j := strings.LastIndex(from[:i], "/")
	if j < 0 || !strings.Contains(from, "://") {
		return ""
	}
	return from[:j] + "/" + ref
}

// TestPASCarriedUpdate_CarriesEveryProvenanceTargetAsWritten: the Provenance
// target is the participant's own content, so the Carried builders carry it as
// written whatever it names and never judge it: a target at the report's own
// version or another, under the Bundle's base or the facility's, naming
// another report, or in a member spelled in another case. Whether it names the
// report is the payer side's content check (ProvenanceTargetNames), at the
// levels the payer's gateway runs it. Every line.
func TestPASCarriedUpdate_CarriesEveryProvenanceTargetAsWritten(t *testing.T) {
	withTarget := func(t *testing.T, in *ConformantClaimUpdateInputs, target string) {
		var m map[string]any
		if err := json.Unmarshal(in.Provenance, &m); err != nil {
			t.Fatal(err)
		}
		m["target"] = []any{map[string]any{"reference": target}}
		in.Provenance = encodeAsWritten(t, m)
	}
	withoutVersion := func(t *testing.T, in *ConformantClaimUpdateInputs) {
		var m map[string]any
		if err := json.Unmarshal(in.DiagnosticReport, &m); err != nil {
			t.Fatal(err)
		}
		delete(m["meta"].(map[string]any), "versionId")
		in.DiagnosticReport = encodeAsWritten(t, m)
	}
	rel := "DiagnosticReport/" + carriedReportID
	for _, row := range []struct {
		name      string
		target    string
		unversion bool
		mutate    func([]byte) []byte
	}{
		{name: "the report's own version", target: rel + "/_history/4"},
		{name: "the report's own version, under the bundle's base", target: pasBundleBaseURL + "/" + rel + "/_history/4"},
		{name: "another version", target: rel + "/_history/3"},
		{name: "a version of a report that carries none", target: rel + "/_history/7", unversion: true},
		{name: "no version, on a report that carries none", target: rel, unversion: true},
		{name: "a version that is not a FHIR id", target: rel + "/_history/", unversion: true},
		{name: "under the facility's own base", target: "https://facility.example/fhir/" + rel},
		{name: "versioned under the facility's own base", target: "https://facility.example/fhir/" + rel + "/_history/4"},
		{name: "another report", target: "DiagnosticReport/another"},
		{name: "a target member spelled Target", target: rel, mutate: func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"target"`), []byte(`"Target"`), 1)
		}},
	} {
		for _, line := range pasLines {
			in := carriedRecordInputs(t, line)
			if row.unversion {
				withoutVersion(t, &in)
			}
			withTarget(t, &in, row.target)
			if row.mutate != nil {
				in.Provenance = row.mutate(in.Provenance)
			}
			out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
			if err != nil {
				t.Errorf("%s %s: %v, want the Provenance carried as written", line, row.name, err)
				continue
			}
			if _, got := entryOfType(t, out, "Provenance"); !bytes.Equal(got, in.Provenance) {
				t.Errorf("%s %s: the Provenance is not carried as written\n got: %s\nwant: %s", line, row.name, got, in.Provenance)
			}
		}
	}
}

// TestPASCarry_ParticipantTextCannotHoldAPlaceholder: a participant's text
// that spells a carried record's placeholder, under the zero key the first
// encoding uses, for the first record and for a later one, cannot take a
// record's place: the placeholders the records are spliced at are keyed to the
// Bundle's own content, so the request is built, the text is carried as
// written and every record lands at its own entry. Every line.
func TestPASCarry_ParticipantTextCannotHoldAPlaceholder(t *testing.T) {
	note := func(i int) string {
		return `\u0000shn-carried-` + pasCarryKeyZero + `-` + strconv.Itoa(i)
	}
	// Each note's text is exactly a placeholder's JSON value.
	planted := `[{"text":"` + note(0) + `"},{"text":"` + note(1) + `"},{"text":"` + note(2) + `"}]`
	for _, line := range pasLines {
		in := carriedRecordInputs(t, line)
		var sr map[string]any
		if err := json.Unmarshal(in.SR, &sr); err != nil {
			t.Fatal(err)
		}
		sr["note"] = "placeholder"
		b, err := json.Marshal(sr)
		if err != nil {
			t.Fatal(err)
		}
		in.SR = bytes.Replace(b, []byte(`"placeholder"`), []byte(planted), 1)
		out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
		if err != nil {
			t.Fatalf("%s: %v, want the request built", line, err)
		}
		if !bytes.Contains(out, []byte(`"note":`+planted)) {
			t.Errorf("%s: the order's notes are not carried as written", line)
		}
		if _, got := entryOfType(t, out, "DiagnosticReport"); !bytes.Equal(got, in.DiagnosticReport) {
			t.Errorf("%s: the report is not at its own entry", line)
		}
		if _, got := entryOfType(t, out, "Provenance"); !bytes.Equal(got, in.Provenance) {
			t.Errorf("%s: the Provenance is not at its own entry", line)
		}
	}
}

// TestPASCarry_RefusesAPlaceholderNotPlacedExactlyOnce: the splice places a
// record only at the one place its placeholder occurs; a placeholder the
// encoding holds twice, or not at all, refuses the build
// (ErrPASRecordNotCarried) rather than guess. The content key makes this
// unreachable from a participant's text (above), so the guard is exercised
// directly.
func TestPASCarry_RefusesAPlaceholderNotPlacedExactlyOnce(t *testing.T) {
	for name, layout := range map[string]func(h0, h1 string) string{
		"the first record's placeholder twice": func(h0, h1 string) string { return "[" + h0 + "," + h0 + "," + h1 + "]" },
		"a later record's placeholder twice":   func(h0, h1 string) string { return "[" + h0 + "," + h1 + "," + h1 + "]" },
		"a placeholder missing":                func(h0, h1 string) string { return "[" + h0 + "]" },
	} {
		var c pasCarry
		h0 := c.hold([]byte(`{"resourceType":"DiagnosticReport","id":"a"}`))
		h1 := c.hold([]byte(`{"resourceType":"Provenance","id":"b"}`))
		out, err := c.encode(func() ([]byte, error) { return []byte(layout(string(h0), string(h1))), nil })
		if err == nil || out != nil || !errors.Is(err, ErrPASRecordNotCarried) || !strings.Contains(err.Error(), "exactly once") {
			t.Errorf("%s: built %q, %v; want ErrPASRecordNotCarried", name, out, err)
		}
	}
	// Each placed once: both records land at their own places.
	var c pasCarry
	h0 := c.hold([]byte(`{"a":1}`))
	h1 := c.hold([]byte(`{"b":2}`))
	out, err := c.encode(func() ([]byte, error) { return []byte("[" + string(h0) + "," + string(h1) + "]"), nil })
	if err != nil || string(out) != `[{"a":1},{"b":2}]` {
		t.Errorf("placed once: %q, %v", out, err)
	}
}

// ProvenanceTargetNames is the rule the payer side reads a Provenance's
// target by: exactly the relative "<type>/<id>" or exactly the resource's
// entry fullUrl, with or without a version; a version must be the resource's
// own meta.versionId. A reference under another base names another server's
// resource, whatever its path ends in.
func TestProvenanceTargetNames(t *testing.T) {
	const own = "https://shn.example/fhir/DiagnosticReport/dr-1"
	for _, row := range []struct {
		ref, fullURL, version string
		want                  bool
	}{
		{"DiagnosticReport/dr-1", own, "", true},
		{"DiagnosticReport/dr-1", own, "2", true},
		{own, own, "", true},
		{own + "/_history/2", own, "2", true},
		{"DiagnosticReport/dr-1/_history/2", own, "2", true},
		{"DiagnosticReport/dr-1/_history/2", own, "", false},
		{"DiagnosticReport/dr-1/_history/3", own, "2", false},
		{own + "/_history/3", own, "2", false},
		{"https://facility.example/fhir/DiagnosticReport/dr-1", own, "", false},
		{"https://facility.example/fhir/DiagnosticReport/dr-1/_history/2", own, "2", false},
		{"x/DiagnosticReport/dr-1", own, "", false},
		{own, "", "", false},
		{own, "https://other.example/fhir/DiagnosticReport/dr-1", "", false},
		{"DiagnosticReport/dr-1/_history/", own, "", false},
		{"DiagnosticReport/dr-1/_history/2/x", own, "", false},
		{"DiagnosticReport/dr-2", own, "", false},
		{"DiagnosticReport/dr-2/_history/2", own, "2", false},
		{"QuestionnaireResponse/dr-1", own, "", false},
		{"https://shn.example/fhir/SupplementalDiagnosticReport/dr-1", own, "", false},
		{"#dr-1", own, "", false},
		{"", own, "", false},
	} {
		if got := ProvenanceTargetNames(row.ref, row.fullURL, "DiagnosticReport", "dr-1", row.version); got != row.want {
			t.Errorf("ProvenanceTargetNames(%q, fullUrl %q, version %q) = %v, want %v", row.ref, row.fullURL, row.version, got, row.want)
		}
	}
	// An empty type or id names nothing, whatever the reference.
	for _, args := range [][2]string{{"DiagnosticReport", ""}, {"", "dr-1"}} {
		if ProvenanceTargetNames("DiagnosticReport/", "https://shn.example/fhir/DiagnosticReport/", args[0], args[1], "") || ProvenanceTargetNames("/dr-1", "", args[0], args[1], "") {
			t.Errorf("ProvenanceTargetNames with type %q, id %q named a target", args[0], args[1])
		}
	}
}

// TestPASCarriedUpdate_ReadsThePriorClaimByExactName: where the amendment
// carries the prior Claim (from PAS 2.1), the Carried builders read its
// resourceType, id, identifiers and items by exact member name, as they read
// the report and the Provenance: a member spelled in another case is not read,
// so the record is refused, never carried under an id it does not hold.
func TestPASCarriedUpdate_ReadsThePriorClaimByExactName(t *testing.T) {
	for name, row := range map[string]struct{ from, to, want string }{
		"its id spelled Id":                     {`"id"`, `"Id"`, "has no id"},
		"its resourceType spelled ResourceType": {`"resourceType"`, `"ResourceType"`, "is not a Claim"},
		"its identifier spelled Identifier":     {`"identifier"`, `"Identifier"`, "first identifier"},
		"its item spelled Item":                 {`"item"`, `"Item"`, "no items"},
	} {
		for _, line := range []string{"2.1", "2.2"} {
			in := carriedRecordInputs(t, line)
			in.PriorClaim.Claim = bytes.Replace(in.PriorClaim.Claim, []byte(row.from), []byte(row.to), 1)
			out, err := BuildConformantClaimUpdateBundleAtLineCarried(line, in)
			if err == nil || out != nil || !errors.Is(err, ErrPASPriorClaimNotCarried) || !strings.Contains(err.Error(), row.want) {
				t.Errorf("%s %s: built %d bytes, %v; want ErrPASPriorClaimNotCarried naming %q", line, name, len(out), err, row.want)
			}
		}
	}
}

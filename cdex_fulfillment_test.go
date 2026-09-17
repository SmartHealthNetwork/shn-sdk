package shnsdk_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// A CDex fulfillment extends the payer's request Task and carries the
// facility's records exactly. These rows pin the exact output bytes: the
// request Task changes only by its status, one appended contained Bundle and
// one appended output; the records are the facility's own bytes, with an id
// written only where the contained Bundle needs one.

// cdexBackslash is a backslash, spelled so no escape sequence appears in this
// source.
var cdexBackslash = string(rune(92))

// cdexFixtureTask is the hostile-but-legal CDex request Task: multi-line with
// CRLF and tab indentation, decimal lexemes in an unknown extension, an
// existing contained Bundle with the id "results", and two existing outputs
// (one referencing "#results").
func cdexFixtureTask(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("internal/splice/testdata/relayfidelity/valid/cdex-query-task.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cdexRecords is a facility searchset with the lexemes a decode and
// re-encode would change, unknown members at two depths, nested arrays and
// escapes.
func cdexRecords() string {
	return `{"resourceType":"Bundle","type":"searchset","total":2,"x-vendor":{"score":[1.50,[1e2,-0.0]]},"entry":[` +
		`{"fullUrl":"urn:shn:fedquery:0","resource":{"resourceType":"DiagnosticReport","id":"dr-new","status":"final","code":{"text":"op note ` + cdexBackslash + `u00e9 a` + cdexBackslash + `/b <&>"},"subject":{"reference":"Patient/MBR-UC05"},` +
		`"extension":[{"url":"http://example.org/fhir/StructureDefinition/unknown","valueDecimal":1.50}],"futureElement":{"nested":[[1e2,-0.0],[9007199254740993]]}}},` +
		`{"fullUrl":"urn:shn:fedquery:1","resource":{"resourceType":"Provenance","id":"prov-new","target":[{"reference":"DiagnosticReport/dr-new"}],"recorded":"2026-06-04T00:00:00Z","agent":[{"who":{"display":"metro-spine"}}]}}]}`
}

// cdexOutput is the output element a fulfillment appends.
func cdexOutput(id string) string {
	return `{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp","code":"data-query"}]},"valueReference":{"reference":"#` + id + `"}}`
}

// withID is compact records with an id member appended, as a fulfillment
// writes it.
func withID(records, id string) string {
	return records[:len(records)-1] + `,"id":"` + id + `"}`
}

// replaceOnce replaces the single occurrence of old, failing if old does not
// occur exactly once.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("anchor %q occurs %d times, want 1", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

func fulfil(t *testing.T, task, records string) string {
	t.Helper()
	out, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(records))
	if err != nil {
		t.Fatalf("BuildCDexQueryResult: %v", err)
	}
	return string(out)
}

func assertBytes(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("output bytes differ\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildCDexQueryResult_TaskUnchangedExceptDeclaredEdits: the fixture
// Task keeps every byte (layout, CRLF, tabs, decimals, narrative escapes,
// existing contained Bundle and outputs) apart from the status token and the
// two appended elements, which copy the arrays' own layout. The existing id
// "results" (also referenced by an existing output) makes the new Bundle
// "results-1".
func TestBuildCDexQueryResult_TaskUnchangedExceptDeclaredEdits(t *testing.T) {
	task := string(cdexFixtureTask(t))
	records := cdexRecords()
	want := replaceOnce(t, task, `"status": "requested"`, `"status": "completed"`)
	want = replaceOnce(t, want, "\"total\": 0\n    }", "\"total\": 0\n    },\n    "+withID(records, "results-1"))
	want = replaceOnce(t, want, "\"valueString\": \"received & queued <no data yet>\"\n    }",
		"\"valueString\": \"received & queued <no data yet>\"\n    },\n    "+cdexOutput("results-1"))
	assertBytes(t, fulfil(t, task, records), want)
}

// TestBuildCDexQueryResult_CompactTaskGainsContainedAndOutput: a Task with
// neither contained nor output gets both, written compact after its last
// member; the SDK's own request builder is the input.
func TestBuildCDexQueryResult_CompactTaskGainsContainedAndOutput(t *testing.T) {
	task := string(validCDexTask(t))
	records := cdexRecords()
	want := replaceOnce(t, task, `"status":"requested"`, `"status":"completed"`)
	want = want[:len(want)-1] + `,"contained":[` + withID(records, "results") + `],"output":[` + cdexOutput("results") + `]}`
	assertBytes(t, fulfil(t, task, records), want)
}

// TestBuildCDexQueryResult_NumericLexemesExact: 1.50, 2^53+1, 1e2 and -0.0
// in the facility's records and in the payer's Task survive byte for byte,
// as do nested arrays, unknown members and escapes.
func TestBuildCDexQueryResult_NumericLexemesExact(t *testing.T) {
	task := string(cdexFixtureTask(t))
	records := cdexRecords()
	got := fulfil(t, task, records)
	if !strings.Contains(got, withID(records, "results-1")) {
		t.Fatalf("the facility's records are not embedded byte for byte:\n%s", got)
	}
	for _, lexeme := range []string{
		`"valueDecimal":1.50`, `[[1e2,-0.0],[9007199254740993]]`, `"score":[1.50,[1e2,-0.0]]`,
		`op note ` + cdexBackslash + `u00e9 a` + cdexBackslash + `/b <&>`,
		"\"valueDecimal\": 1.50", "\"valueDecimal\": 9007199254740993", "\"valueDecimal\": 1e2",
		`&lt;`, `&amp;&amp;`,
	} {
		if !strings.Contains(got, lexeme) {
			t.Errorf("lexeme %q changed", lexeme)
		}
	}
	for _, rewritten := range []string{`1.5,`, `1.5]`, `1.5}`, `9007199254740992`, `100`, `é`} {
		if strings.Contains(got, rewritten) {
			t.Errorf("found re-encoded form %q", rewritten)
		}
	}
}

// TestBuildCDexQueryResult_ExistingContainedAndSeveralOutputsKept: an
// existing contained Bundle with another id and several existing outputs
// stay in place; the new ones are appended after them.
func TestBuildCDexQueryResult_ExistingContainedAndSeveralOutputsKept(t *testing.T) {
	task := `{"resourceType":"Task","id":"t1","status":"in-progress","intent":"order",` +
		`"contained":[{"resourceType":"Bundle","id":"other","type":"searchset"}],` +
		`"output":[{"type":{"text":"a"},"valueString":"x"},{"type":{"text":"b"},"valueReference":{"reference":"#other"}}]}`
	records := cdexRecords()
	want := replaceOnce(t, task, `"status":"in-progress"`, `"status":"completed"`)
	want = replaceOnce(t, want, `"type":"searchset"}]`, `"type":"searchset"},`+withID(records, "results")+`]`)
	want = replaceOnce(t, want, `"#other"}}]`, `"#other"}},`+cdexOutput("results")+`]`)
	assertBytes(t, fulfil(t, task, records), want)
}

// TestBuildCDexQueryResult_CollisionFreeID: the contained id is "results",
// or "results-<n>" for the smallest free n, avoiding every contained id and
// every local reference in the Task; a facility id that is free is kept and
// referenced, one that collides is replaced in place.
func TestBuildCDexQueryResult_CollisionFreeID(t *testing.T) {
	records := cdexRecords()
	bundle := func(id string) string { return `{"resourceType":"Bundle","id":"` + id + `","type":"collection"}` }
	for _, row := range []struct {
		name, contained, extra, records, wantRecords, wantID string
	}{
		{"results taken", bundle("results"), "", records, withID(records, "results-1"), "results-1"},
		{"results and results-1 taken", bundle("results") + "," + bundle("results-1"), "", records, withID(records, "results-2"), "results-2"},
		{"only results-2 taken", bundle("results-2"), "", records, withID(records, "results"), "results"},
		{"dangling local reference", "", `"basedOn":[{"reference":"#results"}],`, records, withID(records, "results-1"), "results-1"},
		{"facility id free", bundle("results"), "", withID(records, "rec-9"), withID(records, "rec-9"), "rec-9"},
		{"facility id collides", bundle("rec-9"), "", withID(records, "rec-9"), withID(records, "results"), "results"},
		{"facility id collides with a reference", "", `"basedOn":[{"reference":"#rec-9"}],`, withID(records, "rec-9"), withID(records, "results"), "results"},
	} {
		t.Run(row.name, func(t *testing.T) {
			contained := ""
			if row.contained != "" {
				contained = `"contained":[` + row.contained + `],`
			}
			task := `{"resourceType":"Task",` + row.extra + contained + `"status":"accepted"}`
			var want string
			if row.contained != "" {
				want = `{"resourceType":"Task",` + row.extra + `"contained":[` + row.contained + `,` + row.wantRecords + `],"status":"completed","output":[` + cdexOutput(row.wantID) + `]}`
			} else {
				want = `{"resourceType":"Task",` + row.extra + `"status":"completed","contained":[` + row.wantRecords + `],"output":[` + cdexOutput(row.wantID) + `]}`
			}
			assertBytes(t, fulfil(t, task, row.records), want)
			f, err := shnsdk.BuildCDexFulfillment([]byte(task), []byte(row.records))
			if err != nil {
				t.Fatal(err)
			}
			if f.ResultsID != row.wantID {
				t.Fatalf("ResultsID = %q, want %q", f.ResultsID, row.wantID)
			}
		})
	}
	t.Run("multi-line records gain the id in their own layout", func(t *testing.T) {
		pretty := "{\n  \"resourceType\": \"Bundle\",\n  \"type\": \"searchset\"\n}\n"
		task := `{"resourceType":"Task","status":"received"}`
		want := `{"resourceType":"Task","status":"completed","contained":[` +
			"{\n  \"resourceType\": \"Bundle\",\n  \"type\": \"searchset\",\n  \"id\": \"results\"\n}" +
			`],"output":[` + cdexOutput("results") + `]}`
		assertBytes(t, fulfil(t, task, pretty), want)
	})
	t.Run("an empty facility id is refused", func(t *testing.T) {
		_, err := shnsdk.BuildCDexQueryResult([]byte(`{"resourceType":"Task","status":"requested"}`),
			[]byte(`{"resourceType":"Bundle","id":"","type":"searchset"}`))
		if err == nil {
			t.Fatal("want an error for an empty Bundle id")
		}
	})
	t.Run("a non-string facility id is refused", func(t *testing.T) {
		_, err := shnsdk.BuildCDexQueryResult([]byte(`{"resourceType":"Task","status":"requested"}`),
			[]byte(`{"resourceType":"Bundle","id":7,"type":"searchset"}`))
		if err == nil {
			t.Fatal("want an error for a non-string Bundle id")
		}
	})
}

const cdexSig = `{"type":[{"system":"urn:iso-astm:E1762-95:2013","code":"1.2.840.10065.1.12.1.1"}],"when":"2026-06-04T00:00:00Z","who":{"display":"payer"}}`

// TestBuildCDexQueryResult_SignedTaskRefused: a request Task that carries a
// Signature element anywhere, or that a signed Provenance in the supplied
// documents targets, is refused rather than extended.
func TestBuildCDexQueryResult_SignedTaskRefused(t *testing.T) {
	sigWhenExt := strings.Replace(cdexSig, `"when":"2026-06-04T00:00:00Z"`, `"_when":{"extension":[{"url":"x","valueString":"y"}]}`, 1)
	signedProv := func(target string) string {
		return `{"fullUrl":"urn:uuid:9b2c1f7e-1111-4c1e-9a6b-2f0b7c5d3e10","resource":{"resourceType":"Provenance","id":"sp","target":[{"reference":"` + target + `"}],"recorded":"2026-06-04T00:00:00Z","agent":[{"who":{"display":"f"}}],"signature":[` + cdexSig + `]}}`
	}
	recordsWith := func(entry string) string {
		return `{"resourceType":"Bundle","type":"searchset","entry":[` + entry + `]}`
	}
	plain := `{"resourceType":"Bundle","type":"searchset"}`
	for _, row := range []struct {
		name, task, records string
	}{
		{"Signature in a Task extension", `{"resourceType":"Task","id":"t1","status":"requested","extension":[{"url":"x","valueSignature":` + cdexSig + `}]}`, plain},
		{"Signature with an extended when", `{"resourceType":"Task","id":"t1","status":"requested","extension":[{"url":"x","valueSignature":` + sigWhenExt + `}]}`, plain},
		{"signed Bundle contained in the Task", `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Bundle","id":"b","type":"collection","signature":` + cdexSig + `}]}`, plain},
		{"signed Provenance contained in the Task", `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Provenance","id":"p","target":[{"reference":"#"}],"signature":[` + cdexSig + `]}]}`, plain},
		{"supplied signed Provenance targets Task/id", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("Task/t1"))},
		{"supplied signed Provenance targets a versioned absolute Task URL", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("https://payer.example/fhir/Task/t1/_history/2"))},
	} {
		t.Run(row.name, func(t *testing.T) {
			out, err := shnsdk.BuildCDexQueryResult([]byte(row.task), []byte(row.records))
			if !errors.Is(err, shnsdk.ErrCDexSignedContent) {
				t.Fatalf("want ErrCDexSignedContent, got err=%v out=%s", err, out)
			}
			if out != nil {
				t.Fatalf("a refusal must not return a Task: %s", out)
			}
		})
	}
	// Controls: signatures that cover nothing the fulfillment changes.
	for _, row := range []struct {
		name, task, records string
	}{
		{"signed Provenance targets another resource", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("DiagnosticReport/dr-new"))},
		{"signed Provenance targets another Task", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("Task/t10"))},
		{"unsigned Provenance targets the Task", `{"resourceType":"Task","id":"t1","status":"requested"}`,
			recordsWith(`{"resource":{"resourceType":"Provenance","id":"up","target":[{"reference":"Task/t1"}]}}`)},
	} {
		t.Run(row.name, func(t *testing.T) {
			if _, err := shnsdk.BuildCDexQueryResult([]byte(row.task), []byte(row.records)); err != nil {
				t.Fatalf("want a fulfillment, got %v", err)
			}
		})
	}
}

// TestBuildCDexQueryResult_SignedRecordsNotEdited: a signed records Bundle
// is embedded only when it needs no id change.
func TestBuildCDexQueryResult_SignedRecordsNotEdited(t *testing.T) {
	task := `{"resourceType":"Task","status":"requested"}`
	signedNoID := `{"resourceType":"Bundle","type":"searchset","signature":` + cdexSig + `}`
	if _, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(signedNoID)); !errors.Is(err, shnsdk.ErrCDexSignedContent) {
		t.Fatalf("a signed records Bundle that needs an id: want ErrCDexSignedContent, got %v", err)
	}
	collides := `{"resourceType":"Task","status":"requested","contained":[{"resourceType":"Patient","id":"r1"}]}`
	signedR1 := `{"resourceType":"Bundle","id":"r1","type":"searchset","signature":` + cdexSig + `}`
	if _, err := shnsdk.BuildCDexQueryResult([]byte(collides), []byte(signedR1)); !errors.Is(err, shnsdk.ErrCDexSignedContent) {
		t.Fatalf("a signed records Bundle whose id collides: want ErrCDexSignedContent, got %v", err)
	}
	want := `{"resourceType":"Task","status":"completed","contained":[` + signedR1 + `],"output":[` + cdexOutput("r1") + `]}`
	assertBytes(t, fulfil(t, task, signedR1), want)
}

// TestBuildCDexQueryResult_DisallowedStatusRefused: only a Task that is
// requested, received, accepted or in progress can be fulfilled.
func TestBuildCDexQueryResult_DisallowedStatusRefused(t *testing.T) {
	records := []byte(cdexRecords())
	for _, status := range []string{`"draft"`, `"completed"`, `"cancelled"`, `"failed"`, `"rejected"`, `"on-hold"`, `"ready"`, `""`, `"Requested"`, `1`, `null`} {
		task := []byte(`{"resourceType":"Task","status":` + status + `}`)
		out, err := shnsdk.BuildCDexQueryResult(task, records)
		if !errors.Is(err, shnsdk.ErrCDexTaskStatus) || out != nil {
			t.Errorf("status %s: want ErrCDexTaskStatus, got err=%v out=%s", status, err, out)
		}
	}
	if _, err := shnsdk.BuildCDexQueryResult([]byte(`{"resourceType":"Task"}`), records); !errors.Is(err, shnsdk.ErrCDexTaskStatus) {
		t.Errorf("no status: want ErrCDexTaskStatus, got %v", err)
	}
	for _, status := range []string{"requested", "received", "accepted", "in-progress"} {
		task := []byte(`{"resourceType":"Task","status":"` + status + `"}`)
		if _, err := shnsdk.BuildCDexQueryResult(task, records); err != nil {
			t.Errorf("status %s: %v", status, err)
		}
	}
}

// TestBuildCDexQueryResult_MalformedInputsRefused: inputs that are not one
// unambiguous Task and one unambiguous Bundle are refused.
func TestBuildCDexQueryResult_MalformedInputsRefused(t *testing.T) {
	task := `{"resourceType":"Task","status":"requested"}`
	records := cdexRecords()
	for _, row := range []struct{ name, task, records string }{
		{"not a Task", `{"resourceType":"ServiceRequest","status":"requested"}`, records},
		{"Task is an array", `[{"resourceType":"Task","status":"requested"}]`, records},
		{"Task has a duplicate member", `{"resourceType":"Task","status":"requested","Status":"completed"}`, records},
		{"Task followed by a second document", task + ` {}`, records},
		{"Task is not JSON", `{"resourceType":"Task",`, records},
		{"contained is not an array", `{"resourceType":"Task","status":"requested","contained":{}}`, records},
		{"output is not an array", `{"resourceType":"Task","status":"requested","output":"x"}`, records},
		{"records are not a Bundle", task, `{"resourceType":"Observation"}`},
		{"records have a duplicate member", task, `{"resourceType":"Bundle","type":"searchset","type":"collection"}`},
		{"records are not JSON", task, `{"resourceType":"Bundle"`},
		{"records are empty", task, ``},
	} {
		t.Run(row.name, func(t *testing.T) {
			if out, err := shnsdk.BuildCDexQueryResult([]byte(row.task), []byte(row.records)); err == nil {
				t.Fatalf("want an error, got %s", out)
			}
		})
	}
}

// TestBuildCDexFulfillment_CopiedSpansExact: every span the fulfillment
// reports as copied is a whole value, identical in its source and in the
// Task, and together they account for every member the fulfillment does not
// author.
func TestBuildCDexFulfillment_CopiedSpansExact(t *testing.T) {
	task := cdexFixtureTask(t)
	records := []byte(cdexRecords())
	f, err := shnsdk.BuildCDexFulfillment(task, records)
	if err != nil {
		t.Fatal(err)
	}
	want, err := shnsdk.BuildCDexQueryResult(task, records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Task, want) {
		t.Fatal("BuildCDexQueryResult and BuildCDexFulfillment disagree")
	}
	fromTask := map[string]bool{}
	fromRecords := map[string]bool{}
	for _, c := range f.Copied {
		src := task
		if c.FromRecords {
			src = records
		}
		if c.Start < 0 || c.End > len(src) || c.Start >= c.End || c.At < 0 || c.At+(c.End-c.Start) > len(f.Task) {
			t.Fatalf("copied span out of range: %+v", c)
		}
		piece := src[c.Start:c.End]
		if !bytes.Equal(piece, f.Task[c.At:c.At+len(piece)]) {
			t.Fatalf("copied span %+v is not identical in the Task", c)
		}
		if !json.Valid(piece) {
			t.Fatalf("copied span %+v is not a whole JSON value: %s", c, piece)
		}
		if c.FromRecords {
			fromRecords[string(piece)] = true
		} else {
			fromTask[string(piece)] = true
		}
	}
	// Every Task member except status, and every existing contained and
	// output element, is reported.
	var tm map[string]json.RawMessage
	if err := json.Unmarshal(task, &tm); err != nil {
		t.Fatal(err)
	}
	for name, v := range tm {
		switch name {
		case "status":
			if fromTask[string(v)] {
				t.Errorf("the replaced status is reported as copied")
			}
		case "contained", "output":
			var elems []json.RawMessage
			if err := json.Unmarshal(v, &elems); err != nil {
				t.Fatal(err)
			}
			for _, e := range elems {
				if !fromTask[string(e)] {
					t.Errorf("existing %s element not reported as copied: %s", name, e)
				}
			}
		default:
			if !fromTask[string(v)] {
				t.Errorf("Task member %s not reported as copied", name)
			}
		}
	}
	var rm map[string]json.RawMessage
	if err := json.Unmarshal(records, &rm); err != nil {
		t.Fatal(err)
	}
	for name, v := range rm {
		if !fromRecords[string(v)] {
			t.Errorf("records member %s not reported as copied", name)
		}
	}

	// An unedited records Bundle is reported whole.
	withOwnID := []byte(withID(cdexRecords(), "rec-9"))
	f, err = shnsdk.BuildCDexFulfillment([]byte(`{"resourceType":"Task","status":"requested"}`), withOwnID)
	if err != nil {
		t.Fatal(err)
	}
	whole := false
	for _, c := range f.Copied {
		if c.FromRecords && c.Start == 0 && c.End == len(withOwnID) {
			whole = true
		}
	}
	if !whole {
		t.Fatalf("an unedited records Bundle is not reported as one copied span: %+v", f.Copied)
	}
}

// fulfilledWithDecoy is the fixture Task whose existing "results" Bundle
// holds a decoy DiagnosticReport, fulfilled with cdexRecords.
func fulfilledWithDecoy(t *testing.T) []byte {
	t.Helper()
	task := replaceOnce(t, string(cdexFixtureTask(t)), `"total": 0`,
		`"total": 1, "entry": [{"resource": {"resourceType": "DiagnosticReport", "id": "dr-decoy"}}, {"resource": {"resourceType": "Provenance", "id": "prov-decoy"}}]`)
	out, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(cdexRecords()))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestExtractCDexEvidence_FollowsAddedOutput: extraction follows the
// data-query output the fulfillment added to its contained Bundle, never the
// first contained Bundle, and returns the facility's resource bytes exactly.
func TestExtractCDexEvidence_FollowsAddedOutput(t *testing.T) {
	out := fulfilledWithDecoy(t)
	dr, prov, err := shnsdk.ExtractCDexEvidence(out)
	if err != nil {
		t.Fatal(err)
	}
	records := cdexRecords()
	if !strings.Contains(string(dr), `"id":"dr-new"`) || !strings.Contains(records, string(dr)) {
		t.Fatalf("DiagnosticReport is not the facility's, byte for byte: %s", dr)
	}
	if !strings.Contains(string(prov), `"id":"prov-new"`) || !strings.Contains(records, string(prov)) {
		t.Fatalf("Provenance is not the facility's, byte for byte: %s", prov)
	}
}

// TestExtractCDexEvidence_ReferenceDrivenRefusals: without a data-query
// output that resolves to a contained Bundle there is no evidence, whatever
// else the Task contains; the last data-query output wins.
func TestExtractCDexEvidence_ReferenceDrivenRefusals(t *testing.T) {
	dq := func(ref string) string {
		return `{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp","code":"data-query"}]},"valueReference":{"reference":"` + ref + `"}}`
	}
	evidence := func(dr string) string {
		return `{"resourceType":"Bundle","id":"` + dr + `-b","type":"searchset","entry":[{"resource":{"resourceType":"DiagnosticReport","id":"` + dr + `"}},{"resource":{"resourceType":"Provenance","id":"p-` + dr + `"}}]}`
	}
	for _, row := range []struct{ name, task string }{
		{"no output", `{"resourceType":"Task","status":"completed","contained":[` + evidence("a") + `]}`},
		{"output is not a data-query", `{"resourceType":"Task","contained":[` + evidence("a") + `],"output":[{"type":{"text":"x"},"valueReference":{"reference":"#a-b"}}]}`},
		{"dangling reference", `{"resourceType":"Task","contained":[` + evidence("a") + `],"output":[` + dq("#missing") + `]}`},
		{"non-local reference", `{"resourceType":"Task","contained":[` + evidence("a") + `],"output":[` + dq("Bundle/a-b") + `]}`},
		{"ambiguous contained id", `{"resourceType":"Task","contained":[` + evidence("a") + `,` + evidence("a") + `],"output":[` + dq("#a-b") + `]}`},
		{"ambiguous contained id on another resource", `{"resourceType":"Task","contained":[` + evidence("a") + `,{"resourceType":"Patient","id":"a-b"}],"output":[` + dq("#a-b") + `]}`},
		{"reference to a non-Bundle", `{"resourceType":"Task","contained":[{"resourceType":"Patient","id":"pt"}],"output":[` + dq("#pt") + `]}`},
		{"duplicate member", `{"resourceType":"Task","contained":[` + evidence("a") + `],"output":[` + dq("#a-b") + `],"Output":[]}`},
		{"not JSON", `{"resourceType":"Task"`},
	} {
		t.Run(row.name, func(t *testing.T) {
			if dr, _, err := shnsdk.ExtractCDexEvidence([]byte(row.task)); err == nil {
				t.Fatalf("want an error, got %s", dr)
			}
		})
	}
	t.Run("last data-query output wins", func(t *testing.T) {
		task := `{"resourceType":"Task","contained":[` + evidence("first") + `,` + evidence("second") + `],"output":[` + dq("#second-b") + `,` + dq("#first-b") + `]}`
		dr, _, err := shnsdk.ExtractCDexEvidence([]byte(task))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(dr), `"id":"first"`) {
			t.Fatalf("want the last data-query output's Bundle, got %s", dr)
		}
	})
}

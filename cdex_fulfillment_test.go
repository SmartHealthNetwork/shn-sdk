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

// The carriers a gateway's relay names for the signature that covers a
// document it would edit, which a signed-content refusal ends with.
const (
	cdexCarrierBundle     = "Bundle.signature"
	cdexCarrierProvenance = "Provenance.signature"
	cdexCarrierInTask     = "Signature in Task"
	cdexCarrierInBundle   = "Signature in Bundle"
)

// assertSignedRefusal asserts that fulfilling task with records is refused as
// signed content, naming exactly carrier, and returns no Task.
func assertSignedRefusal(t *testing.T, task, records, carrier string) {
	t.Helper()
	out, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(records))
	if !errors.Is(err, shnsdk.ErrCDexSignedContent) || err.Error() != shnsdk.ErrCDexSignedContent.Error()+": "+carrier || out != nil {
		t.Fatalf("want ErrCDexSignedContent naming %q, got err=%v out=%s", carrier, err, out)
	}
}

// assertFulfilled asserts that fulfilling task with records builds a Task.
func assertFulfilled(t *testing.T, task, records string) {
	t.Helper()
	out, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(records))
	if err != nil || out == nil {
		t.Fatalf("want a fulfillment, got err=%v", err)
	}
}

// TestBuildCDexQueryResult_SignedTaskRefused: the fulfillment is refused when
// a signature covers the fulfilled Task, as the gateway relay reads it: a
// Signature element the Task holds itself, a signed Provenance contained in
// it whose target "#" names it, or a signed Provenance in the records (which
// the fulfilled Task contains) whose target names the Task by type and id,
// version or not. A signature over only a resource the Task contains covers
// that resource and not the Task, so a contained Bundle's own signature is
// carried (earlier releases refused it), as are a signed Provenance naming
// another resource and an unsigned one naming the Task.
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
		name, task, records, carrier string
	}{
		{"Signature in a Task extension", `{"resourceType":"Task","id":"t1","status":"requested","extension":[{"url":"x","valueSignature":` + cdexSig + `}]}`, plain, cdexCarrierInTask},
		{"Signature with an extended when", `{"resourceType":"Task","id":"t1","status":"requested","extension":[{"url":"x","valueSignature":` + sigWhenExt + `}]}`, plain, cdexCarrierInTask},
		{"signed Provenance contained in the Task", `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Provenance","id":"p","target":[{"reference":"#"}],"signature":[` + cdexSig + `]}]}`, plain, cdexCarrierProvenance},
		{"supplied signed Provenance targets Task/id", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("Task/t1")), cdexCarrierProvenance},
		{"supplied signed Provenance targets a versioned absolute Task URL", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("https://payer.example/fhir/Task/t1/_history/2")), cdexCarrierProvenance},
	} {
		t.Run(row.name, func(t *testing.T) {
			assertSignedRefusal(t, row.task, row.records, row.carrier)
		})
	}
	// Controls: signatures that cover nothing the fulfillment changes.
	for _, row := range []struct {
		name, task, records string
	}{
		{"signed Bundle contained in the Task", `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Bundle","id":"b","type":"collection","signature":` + cdexSig + `}]}`, plain},
		{"signed Provenance targets another resource", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("DiagnosticReport/dr-new"))},
		{"signed Provenance targets another Task", `{"resourceType":"Task","id":"t1","status":"requested"}`, recordsWith(signedProv("Task/t10"))},
		{"unsigned Provenance targets the Task", `{"resourceType":"Task","id":"t1","status":"requested"}`,
			recordsWith(`{"resource":{"resourceType":"Provenance","id":"up","target":[{"reference":"Task/t1"}]}}`)},
	} {
		t.Run(row.name, func(t *testing.T) {
			assertFulfilled(t, row.task, row.records)
		})
	}
}

// TestBuildCDexQueryResult_SignedRecordsNotEdited: a records Bundle that its
// own signature covers is embedded only when it needs no id change; where the
// fulfillment would write or rewrite its id, the relay reads the records as
// supplied and the fulfillment is refused naming Bundle.signature.
func TestBuildCDexQueryResult_SignedRecordsNotEdited(t *testing.T) {
	task := `{"resourceType":"Task","status":"requested"}`
	signedNoID := `{"resourceType":"Bundle","type":"searchset","signature":` + cdexSig + `}`
	t.Run("a signed records Bundle that needs an id", func(t *testing.T) {
		assertSignedRefusal(t, task, signedNoID, cdexCarrierBundle)
	})
	collides := `{"resourceType":"Task","status":"requested","contained":[{"resourceType":"Patient","id":"r1"}]}`
	signedR1 := `{"resourceType":"Bundle","id":"r1","type":"searchset","signature":` + cdexSig + `}`
	t.Run("a signed records Bundle whose id collides", func(t *testing.T) {
		assertSignedRefusal(t, collides, signedR1, cdexCarrierBundle)
	})
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
		return `{"resourceType":"Bundle","id":"` + dr + `-b","type":"searchset","entry":[{"resource":{"resourceType":"DiagnosticReport","id":"` + dr + `"}},` +
			`{"resource":{"resourceType":"Provenance","id":"p-` + dr + `","target":[{"reference":"DiagnosticReport/` + dr + `"}]}}]}`
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

// evidenceTask is a fulfilled Task whose contained records Bundle has the
// given entries.
func evidenceTask(entries ...string) []byte {
	return []byte(`{"resourceType":"Task","status":"completed","contained":[{"resourceType":"Bundle","id":"results","type":"searchset","entry":[` +
		strings.Join(entries, ",") + `]}],"output":[{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp","code":"data-query"}]},"valueReference":{"reference":"#results"}}]}`)
}

func reportEntry(fullURL, id string) string {
	e := `{"resource":{"resourceType":"DiagnosticReport","id":"` + id + `","status":"final"}}`
	if fullURL != "" {
		e = `{"fullUrl":"` + fullURL + `",` + e[1:]
	}
	return e
}

func provenanceEntry(id string, targets ...string) string {
	var refs []string
	for _, t := range targets {
		refs = append(refs, `{"reference":"`+t+`"}`)
	}
	return `{"resource":{"resourceType":"Provenance","id":"` + id + `","target":[` + strings.Join(refs, ",") + `],"recorded":"2026-06-04T00:00:00Z"}}`
}

// TestExtractCDexEvidence_ProvenanceTargetsTheReport: the evidence is the
// records' last DiagnosticReport and a Provenance that names that report as
// its target (by DiagnosticReport/<id>, or by the report entry's fullUrl),
// never the last Provenance of the Bundle regardless of what it attributes.
func TestExtractCDexEvidence_ProvenanceTargetsTheReport(t *testing.T) {
	t.Run("provenance of the last report, listed first", func(t *testing.T) {
		task := evidenceTask(reportEntry("", "dr-old"), reportEntry("", "dr-new"),
			provenanceEntry("p-new", "DiagnosticReport/dr-new"), provenanceEntry("p-old", "DiagnosticReport/dr-old"))
		dr, prov, err := shnsdk.ExtractCDexEvidence(task)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(dr), `"id":"dr-new"`) || !strings.Contains(string(prov), `"id":"p-new"`) {
			t.Fatalf("got report %s with provenance %s", dr, prov)
		}
		if !bytes.Contains(task, prov) || !bytes.Contains(task, dr) {
			t.Fatal("evidence is not the records' own bytes")
		}
	})
	t.Run("provenance names the report by its fullUrl among other targets", func(t *testing.T) {
		task := evidenceTask(reportEntry("urn:uuid:5d3c", "dr-1"), provenanceEntry("p-1", "Patient/pt", "urn:uuid:5d3c"))
		if _, prov, err := shnsdk.ExtractCDexEvidence(task); err != nil || !strings.Contains(string(prov), `"id":"p-1"`) {
			t.Fatalf("got %s %v", prov, err)
		}
	})
	t.Run("of several provenances of the report the last is returned", func(t *testing.T) {
		task := evidenceTask(reportEntry("", "dr-1"), provenanceEntry("p-a", "DiagnosticReport/dr-1"),
			provenanceEntry("p-other", "DiagnosticReport/dr-0"), provenanceEntry("p-b", "DiagnosticReport/dr-1"))
		if _, prov, err := shnsdk.ExtractCDexEvidence(task); err != nil || !strings.Contains(string(prov), `"id":"p-b"`) {
			t.Fatalf("got %s %v", prov, err)
		}
	})
	for _, row := range []struct {
		name    string
		entries []string
	}{
		{"only provenance attributes another report", []string{reportEntry("", "dr-old"), reportEntry("", "dr-new"),
			provenanceEntry("p-old", "DiagnosticReport/dr-old")}},
		{"provenance without a target", []string{reportEntry("", "dr-1"), provenanceEntry("p-1")}},
		{"target names the id under another type", []string{reportEntry("", "dr-1"), provenanceEntry("p-1", "Patient/dr-1")}},
		{"target names a fullUrl the report does not have", []string{reportEntry("urn:uuid:aaaa", "dr-1"), provenanceEntry("p-1", "urn:uuid:bbbb")}},
		{"report without id or fullUrl", []string{`{"resource":{"resourceType":"DiagnosticReport","status":"final"}}`, provenanceEntry("p-1", "DiagnosticReport/")}},
		{"no report", []string{provenanceEntry("p-1", "DiagnosticReport/dr-1")}},
		{"no provenance", []string{reportEntry("", "dr-1")}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if dr, prov, err := shnsdk.ExtractCDexEvidence(evidenceTask(row.entries...)); err == nil {
				t.Fatalf("want an error, got %s / %s", dr, prov)
			}
		})
	}
}

// TestBuildCDexQueryResult_OwnSignatureInAnyCase: the gateway relay reads a
// Provenance's or a Bundle's own signature under its member's name in any
// case: one named `Signature`, `SIGNATURE` or `ſignature` (U+017F folds to s)
// covers what `signature` does, so a signed Provenance naming the request
// Task, in the records or contained in the Task, refuses naming
// Provenance.signature, and the records Bundle's own signature, or a signed
// Provenance naming it, refuses where its id is written, naming
// Bundle.signature or Provenance.signature. A member of another name is no
// signature. The signature values here are not shaped as Signatures (the
// records Bundle's is an empty object), so only the member's name can refuse.
func TestBuildCDexQueryResult_OwnSignatureInAnyCase(t *testing.T) {
	task := `{"resourceType":"Task","id":"t1","status":"requested"}`
	prov := func(member string) string {
		return `{"resourceType":"Bundle","type":"searchset","entry":[{"resource":{"resourceType":"Provenance","id":"sp","target":[{"reference":"Task/t1"}],"` + member + `":[` + cdexSig + `]}}]}`
	}
	contained := func(member string) string {
		return `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Provenance","id":"p","target":[{"reference":"#"}],"` + member + `":[{"display":"not shaped"}]}]}`
	}
	root := func(member string) string {
		return `{"resourceType":"Bundle","type":"searchset","` + member + `":{}}`
	}
	// The records Bundle's id r1 is taken in the Task, so it must be written.
	collides := `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Patient","id":"r1"}]}`
	provOnRecords := func(member string) string {
		return `{"resourceType":"Bundle","id":"r1","type":"searchset","entry":[{"resource":{"resourceType":"Provenance","id":"sp","target":[{"reference":"Bundle/r1"}],"` + member + `":[{"display":"not shaped"}]}}]}`
	}
	plain := `{"resourceType":"Bundle","type":"searchset"}`
	for _, member := range []string{"signature", "Signature", "SIGNATURE", "ſignature"} {
		for _, row := range []struct {
			name, task, records, carrier string
		}{
			{"a supplied Provenance targets the Task", task, prov(member), cdexCarrierProvenance},
			{"a Provenance contained in the Task targets it", contained(member), plain, cdexCarrierProvenance},
			{"the records Bundle's own signature", task, root(member), cdexCarrierBundle},
			{"a supplied Provenance targets the records Bundle", collides, provOnRecords(member), cdexCarrierProvenance},
		} {
			t.Run(member+"/"+row.name, func(t *testing.T) {
				assertSignedRefusal(t, row.task, row.records, row.carrier)
			})
		}
	}
	// Controls: a member whose name only resembles the signature's is not one.
	for _, member := range []string{"signatures", "sig", "xsignature"} {
		for _, row := range []struct{ name, task, records string }{
			{"a supplied Provenance targets the Task", task, prov(member)},
			{"a Provenance contained in the Task targets it", contained(member), plain},
			{"the records Bundle", task, root(member)},
			{"a supplied Provenance targets the records Bundle", collides, provOnRecords(member)},
		} {
			t.Run("control "+member+"/"+row.name, func(t *testing.T) {
				assertFulfilled(t, row.task, row.records)
			})
		}
	}
}

// TestBuildCDexQueryResult_SignatureShapeInAnyCase: the gateway relay reads a
// Signature's members (type, when or _when, and who) by their names in any
// case, and a Signature covers the nearest resource that holds it: one in the
// request Task refuses naming "Signature in Task", and one in the records
// Bundle, when its id must be written, refuses naming "Signature in Bundle",
// as the exact-case shape does. At a place no FHIR R4 type describes, a shape
// that lacks a member, or whose names only resemble them, is no Signature; at
// a place R4 types Signature it is one all the same
// (TestBuildCDexQueryResult_SignatureByItsPlace).
func TestBuildCDexQueryResult_SignatureShapeInAnyCase(t *testing.T) {
	const typ, when, who = `[{"system":"urn:iso-astm:E1762-95:2013","code":"1.2.840.10065.1.12.1.1"}]`, `"2026-06-04T00:00:00Z"`, `{"display":"payer"}`
	shape := func(tn, wn, on string) string {
		return `{"` + tn + `":` + typ + `,"` + wn + `":` + when + `,"` + on + `":` + who + `}`
	}
	inTask := func(sig string) string {
		return `{"resourceType":"Task","id":"t1","status":"requested","extension":[{"url":"x","valueSignature":` + sig + `}]}`
	}
	inRecords := func(sig string) string {
		return `{"resourceType":"Bundle","type":"searchset","meta":{"extension":[{"url":"x","valueSignature":` + sig + `}]}}`
	}
	task := `{"resourceType":"Task","id":"t1","status":"requested"}`
	plain := `{"resourceType":"Bundle","type":"searchset"}`
	for _, sig := range []string{
		shape("type", "when", "who"),
		shape("Type", "WHEN", "Who"),
		shape("TYPE", "_When", "WHO"),
		shape("tYPE", "_when", "wHo"),
	} {
		t.Run("Task "+sig[:12], func(t *testing.T) {
			assertSignedRefusal(t, inTask(sig), plain, cdexCarrierInTask)
		})
		t.Run("records "+sig[:12], func(t *testing.T) {
			assertSignedRefusal(t, task, inRecords(sig), cdexCarrierInBundle)
		})
	}
	// Controls: a shape missing who, and names that only resemble the
	// members, at a place no R4 type describes (a member R4 does not define).
	untypedInTask := func(v string) string {
		return `{"resourceType":"Task","id":"t1","status":"requested","x-note":` + v + `}`
	}
	untypedInRecords := func(v string) string {
		return `{"resourceType":"Bundle","type":"searchset","x-note":` + v + `}`
	}
	for _, sig := range []string{
		`{"Type":` + typ + `,"When":` + when + `}`,
		shape("types", "when", "who"),
		shape("type", "when", "whom"),
	} {
		t.Run("control Task "+sig[:12], func(t *testing.T) {
			if _, err := shnsdk.BuildCDexQueryResult([]byte(untypedInTask(sig)), []byte(plain)); err != nil {
				t.Fatalf("want a fulfillment, got %v", err)
			}
		})
		t.Run("control records "+sig[:12], func(t *testing.T) {
			if _, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(untypedInRecords(sig))); err != nil {
				t.Fatalf("want a fulfillment, got %v", err)
			}
		})
	}
}

// TestBuildCDexQueryResult_SignatureByItsPlace: the gateway relay reads an
// object at a place FHIR R4 types Signature as a Signature whatever its
// members (an extension's valueSignature at any depth, a Task input's or
// output's), its members' names read in any case, and the Signature covers
// the nearest resource that holds it: one the request Task holds refuses
// naming "Signature in Task", and one the records Bundle holds, when its id
// must be written, refuses naming "Signature in Bundle". The value here has
// none of a Signature's members, so only its place can refuse it. A Signature
// in a resource the Task contains covers that resource, not the Task, and is
// carried (earlier releases refused it). The same value at a place no R4 type
// describes, or one R4 types otherwise, is no Signature, nor is a contained
// Bundle's own signature, which is that Bundle's.
func TestBuildCDexQueryResult_SignatureByItsPlace(t *testing.T) {
	const bare = `{"data":"c2ln"}`
	taskWith := func(members string) string {
		return `{"resourceType":"Task","id":"t1","status":"requested","intent":"order",` + members + `}`
	}
	recordsWith := func(members string) string {
		return `{"resourceType":"Bundle","type":"searchset",` + members + `}`
	}
	task := taskWith(`"priority":"routine"`)
	plain := recordsWith(`"total":0`)
	for _, row := range []struct{ name, members string }{
		{"an extension's valueSignature", `"extension":[{"url":"x","valueSignature":` + bare + `}]`},
		{"a modifier extension's valueSignature", `"modifierExtension":[{"url":"x","valueSignature":` + bare + `}]`},
		{"a nested extension's valueSignature", `"extension":[{"url":"x","extension":[{"url":"y","valueSignature":` + bare + `}]}]`},
		{"an input's valueSignature", `"input":[{"type":{"text":"x"},"valueSignature":` + bare + `}]`},
		{"an output's valueSignature", `"output":[{"type":{"text":"x"},"valueSignature":` + bare + `}]`},
		{"an extension's valueSignature on an input", `"input":[{"type":{"text":"x"},"valueString":"v","extension":[{"url":"x","valueSignature":` + bare + `}]}]`},
		{"an extension's valueSignature on a primitive", `"_status":{"extension":[{"url":"x","valueSignature":` + bare + `}]}`},
		{"names in another case", `"Extension":[{"url":"x","VALUESIGNATURE":` + bare + `}]`},
		{"an extension's valueSignature under a member R4 does not define", `"x-note":{"extension":[{"url":"x","valueSignature":` + bare + `}]}`},
	} {
		t.Run("Task/"+row.name, func(t *testing.T) {
			assertSignedRefusal(t, taskWith(row.members), plain, cdexCarrierInTask)
		})
	}
	for _, row := range []struct{ name, members string }{
		{"meta's extension's valueSignature", `"meta":{"extension":[{"url":"x","valueSignature":` + bare + `}]}`},
		{"an entry's extension's valueSignature", `"entry":[{"extension":[{"url":"x","valueSignature":` + bare + `}]}]`},
		{"a link's modifier extension's valueSignature", `"link":[{"relation":"self","url":"u","modifierExtension":[{"url":"x","valueSignature":` + bare + `}]}]`},
	} {
		t.Run("records/"+row.name, func(t *testing.T) {
			assertSignedRefusal(t, task, recordsWith(row.members), cdexCarrierInBundle)
		})
	}
	// Controls.
	for _, row := range []struct{ name, task, records string }{
		{"Task: a member R4 does not define", taskWith(`"x-note":` + bare), plain},
		{"Task: a place R4 types Annotation", taskWith(`"note":[` + bare + `]`), plain},
		{"Task: an input's value of another type", taskWith(`"input":[{"type":{"text":"x"},"valueAttachment":` + bare + `}]`), plain},
		{"Task: a contained Bundle's own signature", taskWith(`"contained":[{"resourceType":"Bundle","id":"b","type":"collection","signature":` + bare + `}]`), plain},
		{"Task: an extension's valueSignature in a contained resource", taskWith(`"contained":[{"resourceType":"Patient","id":"p","extension":[{"url":"x","valueSignature":` + bare + `}]}]`), plain},
		{"records: a member R4 does not define", task, recordsWith(`"x-note":` + bare)},
		{"records: an entry resource's own extension", task, recordsWith(`"entry":[{"resource":{"resourceType":"Patient","id":"p","extension":[{"url":"x","valueSignature":` + bare + `}]}}]`)},
	} {
		t.Run("control "+row.name, func(t *testing.T) {
			assertFulfilled(t, row.task, row.records)
		})
	}
}

// TestBuildCDexQueryResult_ProvenanceTargetMembersInAnyCase: the gateway
// relay reads the members that say what a Provenance's own signature covers
// by their names in any case: the Provenance's resourceType and target, each
// target's reference, and the id of the request Task or the records Bundle it
// names. A signed Provenance naming the request Task through any of them, or
// naming the records Bundle when its id must be written, refuses naming
// Provenance.signature, as the exact names do; a member whose name only
// resembles one names nothing. Records whose id is spelled "ID" are read as
// signed all the same, but no fulfillment of them is built when they need an
// id: an "id" written beside "ID" would give the Bundle two members of one
// name in any case, which the splice refuses before signed content is read.
func TestBuildCDexQueryResult_ProvenanceTargetMembersInAnyCase(t *testing.T) {
	const signed = `"signature":[{"data":"c2ln"}]`
	prov := func(rt, target, ref, to string) string {
		return `{"` + rt + `":"Provenance","id":"sp","` + target + `":[{"` + ref + `":"` + to + `"}],` + signed + `}`
	}
	task := func(id string) string {
		return `{"resourceType":"Task","` + id + `":"t1","status":"requested"}`
	}
	containing := func(p string) string {
		return `{"resourceType":"Task","id":"t1","status":"requested","contained":[` + p + `]}`
	}
	records := func(id string, entries ...string) string {
		b := `{"resourceType":"Bundle",`
		if id != "" {
			b += `"` + id + `":"r1",`
		}
		b += `"type":"searchset","entry":[`
		for i, e := range entries {
			if i > 0 {
				b += ","
			}
			b += `{"resource":` + e + `}`
		}
		return b + `]}`
	}
	// The records Bundle's id r1 is taken in the Task, so it must be written.
	collides := `{"resourceType":"Task","id":"t1","status":"requested","contained":[{"resourceType":"Patient","id":"r1"}]}`
	for _, row := range []struct {
		name, task, records string
	}{
		{"records: Target", task("id"), records("", prov("resourceType", "Target", "reference", "Task/t1"))},
		{"records: REFERENCE", task("id"), records("", prov("resourceType", "target", "REFERENCE", "Task/t1"))},
		{"records: ResourceType", task("id"), records("", prov("ResourceType", "target", "reference", "Task/t1"))},
		{"records: the Task's ID", task("ID"), records("", prov("resourceType", "target", "reference", "Task/t1"))},
		{"Task: TARGET by #", containing(prov("resourceType", "TARGET", "reference", "#")), records("")},
		{"Task: Reference by type and id", containing(prov("resourceType", "target", "Reference", "Task/t1")), records("")},
		{"Task: RESOURCETYPE", containing(prov("RESOURCETYPE", "target", "reference", "#")), records("")},
		{"Task: the Task's Id", `{"resourceType":"Task","Id":"t1","status":"requested","contained":[` + prov("resourceType", "target", "reference", "Task/t1") + `]}`, records("")},
		{"records Bundle: Target, its id written", collides, records("id", prov("resourceType", "Target", "reference", "Bundle/r1"))},
	} {
		t.Run(row.name, func(t *testing.T) {
			assertSignedRefusal(t, row.task, row.records, cdexCarrierProvenance)
		})
	}
	// Records whose id is named ID: the id the fulfillment writes would
	// repeat it, so it cannot be written, and a signature covering the records
	// refuses the fulfillment first, as one covering records whose id is
	// written does.
	t.Run("records Bundle: its ID, its id written", func(t *testing.T) {
		assertSignedRefusal(t, task("id"), records("ID", prov("resourceType", "target", "reference", "Bundle/r1")), cdexCarrierProvenance)
	})
	t.Run("control records Bundle: its ID, unsigned", func(t *testing.T) {
		out, err := shnsdk.BuildCDexQueryResult([]byte(task("id")), []byte(records("ID", prov("resourceType", "target", "reference", "Bundle/r2"))))
		if err == nil || errors.Is(err, shnsdk.ErrCDexSignedContent) || out != nil {
			t.Fatalf("want the id write refused, not as signed content, got err=%v out=%s", err, out)
		}
	})
	// Controls: names that only resemble the members, and a target that
	// names another resource.
	for _, row := range []struct{ name, task, records string }{
		{"targets", task("id"), records("", prov("resourceType", "targets", "reference", "Task/t1"))},
		{"ref", task("id"), records("", prov("resourceType", "target", "ref", "Task/t1"))},
		{"resourceTypes", task("id"), records("", prov("resourceTypes", "target", "reference", "Task/t1"))},
		{"the Task's ids", task("ids"), records("", prov("resourceType", "target", "reference", "Task/t1"))},
		{"another Task", task("ID"), records("", prov("resourceType", "Target", "REFERENCE", "Task/t2"))},
		{"records Bundle: another id", collides, records("id", prov("resourceType", "Target", "reference", "Bundle/r2"))},
	} {
		t.Run("control "+row.name, func(t *testing.T) {
			assertFulfilled(t, row.task, row.records)
		})
	}
}

// TestBuildCDexQueryResult_SignedAsTheRelayReadsIt: the fulfillment is
// refused where the gateway relay reads the fulfilled Task, or the records as
// supplied when their id is written, as signed. A Signature is read with the
// resource that holds it, even when it names a resourceType of its own, and
// refuses naming "Signature in Bundle"; a signed Provenance's "#" target names
// the resource containing it, and on while that resource is itself contained,
// so one in the records' own contained, an entry's or meta's, names the
// records and, once they are contained in the Task, the Task, refusing naming
// Provenance.signature; and a reference names the resource of its last two
// segments once a version is cut from its end, whatever "/_history/" stands
// earlier in it. A "#" from a Provenance contained in an entry's resource
// names only that resource, which is not contained, and a reference that
// ends in "/" after its version names no Task; both are carried, where
// earlier releases refused them.
func TestBuildCDexQueryResult_SignedAsTheRelayReadsIt(t *testing.T) {
	const shaped = `"type":[{"code":"c"}],"when":"2020-01-01T00:00:00Z","who":{"reference":"Practitioner/p"}`
	signedProv := func(ref string) string {
		return `{"resourceType":"Provenance","target":[{"reference":"` + ref + `"}],"recorded":"2020-01-01T00:00:00Z","agent":[],"signature":[{"data":"c2ln"}]}`
	}
	task := `{"resourceType":"Task","id":"t1","status":"requested","intent":"order"}`
	plain := `{"resourceType":"Bundle","id":"b1","type":"collection"}`
	// The records Bundle's id results is taken in the Task, so it must be
	// rewritten.
	collides := `{"resourceType":"Task","id":"t1","status":"requested","intent":"order","contained":[{"resourceType":"Basic","id":"results"}]}`
	for _, row := range []struct{ name, task, records, carrier string }{
		{"records: a Signature with a resourceType of its own at meta", task, `{"resourceType":"Bundle","type":"collection","meta":{"resourceType":"Basic",` + shaped + `}}`, cdexCarrierInBundle},
		{"records: a Signature with a resourceType of its own at a member R4 does not define", task, `{"resourceType":"Bundle","type":"collection","foo":{"resourceType":"X",` + shaped + `}}`, cdexCarrierInBundle},
		{"records: a Signature with a resourceType of its own at a valueSignature", task, `{"resourceType":"Bundle","type":"collection","extension":[{"url":"u","valueSignature":{"resourceType":"X",` + shaped + `}}]}`, cdexCarrierInBundle},
		{"records: a contained Provenance's #, read first as naming the Task", task, `{"resourceType":"Bundle","type":"collection","contained":[` + signedProv("#") + `]}`, cdexCarrierProvenance},
		{"records: an entry's contained Provenance's #, read first as naming the Task", task, `{"resourceType":"Bundle","type":"collection","entry":[{"contained":[` + signedProv("#") + `]}]}`, cdexCarrierProvenance},
		{"records: a version path before the type and id", collides, `{"resourceType":"Bundle","id":"results","type":"collection","entry":[{"resource":` + signedProv("http://a/_history/v/Bundle/results") + `}]}`, cdexCarrierProvenance},
		{"Task: a version path before the type and id", `{"resourceType":"Task","id":"t1","status":"requested","intent":"order","contained":[` + signedProv("http://a/_history/v/Task/t1") + `]}`, plain, cdexCarrierProvenance},
		{"Task: a version at the end", task, `{"resourceType":"Bundle","id":"b1","type":"collection","entry":[{"resource":` + signedProv("http://a/Task/t1/_history/2") + `}]}`, cdexCarrierProvenance},
		{"Task: the records' contained Provenance's #, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","contained":[` + signedProv("#") + `]}`, cdexCarrierProvenance},
		{"Task: a contained Provenance named by RESOURCETYPE, its target #, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","contained":[{"RESOURCETYPE":"Provenance","target":[{"reference":"#"}],"recorded":"2020-01-01T00:00:00Z","agent":[],"signature":[{"data":"c2ln"}]}]}`, cdexCarrierProvenance},
		{"Task: an entry's contained Provenance's #, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","entry":[{"contained":[` + signedProv("#") + `]}]}`, cdexCarrierProvenance},
		{"Task: the records' meta's contained Provenance's #, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","meta":{"contained":[` + signedProv("#") + `]}}`, cdexCarrierProvenance},
		{"Task: a Provenance's # contained in an entry resource named by RESOURCETYPE, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","entry":[{"resource":{"RESOURCETYPE":"Basic","contained":[` + signedProv("#") + `]}}]}`, cdexCarrierProvenance},
		{"Task: a Provenance's # contained under a member of a contained resource, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","contained":[{"resourceType":"Basic","x-note":{"contained":[` + signedProv("#") + `]}}]}`, cdexCarrierProvenance},
		{"Task: a Provenance's # contained in the records' CONTAINED resource, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","CONTAINED":[{"resourceType":"Basic","contained":[` + signedProv("#") + `]}]}`, cdexCarrierProvenance},
	} {
		t.Run(row.name, func(t *testing.T) {
			assertSignedRefusal(t, row.task, row.records, row.carrier)
		})
	}
	// Controls: a reference to another Task, a reference that ends in "/"
	// after its version, and a "#" that names an entry's resource, which is
	// not contained, whether the records' id is written or kept.
	for _, row := range []struct{ name, task, records string }{
		{"records: an entry resource's contained Provenance's #", task, `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Basic","contained":[` + signedProv("#") + `]}}]}`},
		{"Task: a version cut at its first /_history/", task, `{"resourceType":"Bundle","id":"b1","type":"collection","entry":[{"resource":` + signedProv("Task/t1/_history/2/") + `}]}`},
		{"another Task's id after a version path", `{"resourceType":"Task","id":"t1","status":"requested","intent":"order","contained":[` + signedProv("http://a/_history/v/Task/t2") + `]}`, plain},
		{"records kept as they are", task, `{"resourceType":"Bundle","id":"b1","type":"collection","entry":[{"resource":{"resourceType":"Basic","contained":[` + signedProv("#") + `]}}]}`},
		{"a # under a member of an entry resource, kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","entry":[{"resource":{"resourceType":"Basic","x-note":{"contained":[` + signedProv("#") + `]}}}]}`},
		{"a contained signed Provenance with another target, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","contained":[` + signedProv("Basic/b9") + `]}`},
		{"an unsigned contained Provenance's #, the records kept", task, `{"resourceType":"Bundle","id":"b1","type":"collection","contained":[{"resourceType":"Provenance","target":[{"reference":"#"}],"recorded":"2020-01-01T00:00:00Z","agent":[]}]}`},
	} {
		t.Run("control "+row.name, func(t *testing.T) {
			assertFulfilled(t, row.task, row.records)
		})
	}
}

// cdexSignedProv is a Provenance whose resourceType, target, reference and
// signature members are named rt, target, ref and sig, of type typ, whose one
// target is to; sig "" leaves it unsigned.
func cdexSignedProv(rt, target, ref, sig, typ, to string) string {
	p := `{"` + rt + `":"` + typ + `","id":"sp","` + target + `":[{"` + ref + `":"` + to + `"}],"recorded":"2026-06-04T00:00:00Z","agent":[{"who":{"display":"f"}}]`
	if sig != "" {
		p += `,"` + sig + `":[` + cdexSig + `]`
	}
	return p + `}`
}

// cdexWithEntry is records, a Bundle, with an entry holding resource first.
func cdexWithEntry(records, resource string) string {
	return strings.Replace(records, `{"resourceType":"Bundle",`, `{"resourceType":"Bundle","entry":[{"resource":`+resource+`}],`, 1)
}

// cdexProvNamingTheTask are signed Provenances the relay reads as naming the
// request Task (id t1), so as covering it once the Task contains them.
var cdexProvNamingTheTask = []struct{ name, prov string }{
	{"exact names", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Task/t1")},
	{"Target", cdexSignedProv("resourceType", "Target", "reference", "signature", "Provenance", "Task/t1")},
	{"REFERENCE", cdexSignedProv("resourceType", "target", "REFERENCE", "signature", "Provenance", "Task/t1")},
	{"Signature", cdexSignedProv("resourceType", "target", "reference", "Signature", "Provenance", "Task/t1")},
	{"RESOURCETYPE", cdexSignedProv("RESOURCETYPE", "target", "reference", "signature", "Provenance", "Task/t1")},
	{"a version", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Task/t1/_history/2")},
	{"an absolute URL with a version", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "https://payer.example/fhir/Task/t1/_history/2")},
}

// cdexProvNotNamingTheTask are Provenances the relay reads as covering no
// Task: unsigned, naming another Task, of the resource type "provenance",
// which is no Provenance, a reference that ends in "/" after its version,
// and a local reference, which names only a contained resource.
var cdexProvNotNamingTheTask = []struct{ name, prov string }{
	{"unsigned", cdexSignedProv("resourceType", "target", "reference", "", "Provenance", "Task/t1")},
	{"another Task", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Task/other")},
	{"type provenance", cdexSignedProv("resourceType", "target", "reference", "signature", "provenance", "Task/t1")},
	{"a version cut at its first /_history/", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Task/t1/_history/2/")},
	{"#t1", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "#t1")},
	{"a member that only resembles target", cdexSignedProv("resourceType", "targets", "reference", "signature", "Provenance", "Task/t1")},
	{"another type with the Task's id", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Patient/t1")},
	{"the Task's type in another case", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "task/t1")},
	{"a # reference ending in Task/t1", cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "#x/Task/t1")},
}

// A known residue, pinned: a signed Provenance in the records Bundle's own
// contained, whose target is "#", reaches the request Task only through the
// fulfilled Task that contains the records, as the relay reads "#" up through
// each container. A fulfillment that can be built is refused for it. One past
// the reader's limits whose records keep their own id is answered with
// ErrCDexLimit: the failed build reads the records' signed Provenances that
// name the Task by type and id, not a "#" climb. shn-sdk v1.14.0 refused it
// first. This row becomes a refusal row when the failed build is answered
// through signedcontent's own reader, never a copy of its container walk.
func TestBuildCDexFulfillment_AContainedHashProvenanceOnAFailedBuildIsNotReadYet(t *testing.T) {
	task := `{"resourceType":"Task","id":"t1","status":"requested"}`
	records := func(depth int, signed bool) string {
		sig := ""
		if signed {
			sig = "signature"
		}
		return `{"resourceType":"Bundle","id":"r1","type":"collection","contained":[` +
			cdexSignedProv("resourceType", "target", "reference", sig, "Provenance", "#") +
			`],"x":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`
	}
	for _, build := range cdexBuilders {
		if err := build(task, records(1, true)); !errors.Is(err, shnsdk.ErrCDexSignedContent) {
			t.Fatalf("built: got %v, want ErrCDexSignedContent", err)
		}
	}
	limit := 0
	for d := 1; d < 90 && limit == 0; d++ {
		if errors.Is(cdexBuilders[0](task, records(d, false)), shnsdk.ErrCDexLimit) {
			limit = d
		}
	}
	if limit == 0 {
		t.Fatal("fixture: no records take the fulfilled Task past the reader's limits")
	}
	for _, build := range cdexBuilders {
		if err := build(task, records(limit, true)); !errors.Is(err, shnsdk.ErrCDexLimit) || errors.Is(err, shnsdk.ErrCDexSignedContent) {
			t.Fatalf("past the limits: got %v, want ErrCDexLimit (the residue this row pins)", err)
		}
	}
}

// cdexBuilders are the two builders, each returning only its error.
var cdexBuilders = []func(task, records string) error{
	func(task, records string) error {
		out, err := shnsdk.BuildCDexQueryResult([]byte(task), []byte(records))
		if err != nil && out != nil {
			return errors.New("a refusal returned a Task")
		}
		return err
	},
	func(task, records string) error {
		f, err := shnsdk.BuildCDexFulfillment([]byte(task), []byte(records))
		if err != nil && f.Task != nil {
			return errors.New("a refusal returned a Task")
		}
		return err
	},
}

// assertSignedRefusalBoth is assertSignedRefusal through both builders.
func assertSignedRefusalBoth(t *testing.T, task, records, carrier string) {
	t.Helper()
	assertSignedRefusal(t, task, records, carrier)
	f, err := shnsdk.BuildCDexFulfillment([]byte(task), []byte(records))
	if !errors.Is(err, shnsdk.ErrCDexSignedContent) || err.Error() != shnsdk.ErrCDexSignedContent.Error()+": "+carrier || f.Task != nil {
		t.Fatalf("BuildCDexFulfillment: want ErrCDexSignedContent naming %q, got err=%v task=%s", carrier, err, f.Task)
	}
}

// A fulfillment that cannot be built once its inputs are read is refused as
// signed content first, where a signature covers the request Task, where a
// signed Provenance among the records names the request Task by type and id,
// or where a signature covers the records whose id it would write, each as
// supplied; the same inputs unsigned keep the failure that stops them. The
// relay is never asked about a fulfillment that is not built, so the SDK
// answers in its place.
func TestBuildCDexFulfillment_SignedContentBeforeAFailedBuild(t *testing.T) {
	signedTask := `{"resourceType":"Task","id":"t1","status":"requested","extension":[{"url":"x","valueSignature":` + cdexSig + `}]}`
	plainTask := `{"resourceType":"Task","id":"t1","status":"requested"}`
	// deep is records holding a value as deep as the reader carries, so the
	// fulfilled Task, which holds them two levels down, is past its depth.
	deep := func(head string) string {
		for p := 1; p < 80; p++ {
			r := `{"resourceType":"Bundle",` + head + `"type":"collection","x":` + strings.Repeat("[", p) + strings.Repeat("]", p) + `}`
			if jsonDepth(t, []byte(r)) == cdexLimitDepth {
				return r
			}
		}
		t.Fatal("fixture: no records at the reader's depth")
		return ""
	}
	for _, row := range []struct {
		name, records string
		failed        func(t *testing.T, err error)
	}{
		{"the records' id named ID, which cannot be written", `{"resourceType":"Bundle","ID":"r1","type":"collection"}`, func(t *testing.T, err error) {
			if err == nil || errors.Is(err, shnsdk.ErrCDexSignedContent) || !strings.Contains(err.Error(), "write records bundle id") {
				t.Fatalf("unsigned: got %v, want the id write refused", err)
			}
		}},
		{"a records id that is not a string", `{"resourceType":"Bundle","id":5,"type":"collection"}`, func(t *testing.T, err error) {
			if err == nil || errors.Is(err, shnsdk.ErrCDexSignedContent) || !strings.Contains(err.Error(), "id is not a string") {
				t.Fatalf("unsigned: got %v, want the id refused", err)
			}
		}},
		{"an empty records id", `{"resourceType":"Bundle","id":"","type":"collection"}`, func(t *testing.T, err error) {
			if err == nil || errors.Is(err, shnsdk.ErrCDexSignedContent) || !strings.Contains(err.Error(), "id is empty") {
				t.Fatalf("unsigned: got %v, want the id refused", err)
			}
		}},
		{"a fulfilled Task past the reader's depth", deep(`"id":"r1",`), func(t *testing.T, err error) {
			if !errors.Is(err, shnsdk.ErrCDexLimit) || errors.Is(err, shnsdk.ErrCDexSignedContent) {
				t.Fatalf("unsigned: got %v, want ErrCDexLimit", err)
			}
		}},
	} {
		t.Run("signed Task: "+row.name, func(t *testing.T) {
			assertSignedRefusal(t, signedTask, row.records, cdexCarrierInTask)
		})
		t.Run("control: "+row.name, func(t *testing.T) {
			out, err := shnsdk.BuildCDexQueryResult([]byte(plainTask), []byte(row.records))
			if out != nil {
				t.Fatalf("unsigned: a refusal must not return a Task: %s", out)
			}
			row.failed(t, err)
		})
		// A signed Provenance among the records that names the request Task
		// covers the fulfilled Task, which contains the records, so it
		// refuses the failed build first, through both builders, as it
		// refuses a built one (TestBuildCDexQueryResult_SignedTaskRefused,
		// "supplied signed Provenance targets Task/id"); each name read as
		// the relay reads it.
		for _, p := range cdexProvNamingTheTask {
			t.Run("records' signed Provenance names the Task, "+p.name+": "+row.name, func(t *testing.T) {
				assertSignedRefusalBoth(t, plainTask, cdexWithEntry(row.records, p.prov), cdexCarrierProvenance)
			})
		}
		t.Run("records' signed Provenance names the Task by an ID: "+row.name, func(t *testing.T) {
			assertSignedRefusalBoth(t, `{"resourceType":"Task","ID":"t1","status":"requested"}`, cdexWithEntry(row.records, cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Task/t1")), cdexCarrierProvenance)
		})
		// Controls: the same Provenance unsigned, or naming another Task, or
		// read by the relay as no Provenance or as naming no Task, keeps the
		// failure that stops the build.
		for _, p := range cdexProvNotNamingTheTask {
			t.Run("control: records' Provenance, "+p.name+": "+row.name, func(t *testing.T) {
				for _, build := range cdexBuilders {
					err := build(plainTask, cdexWithEntry(row.records, p.prov))
					row.failed(t, err)
				}
			})
		}
	}
	// The records that refuse each failed build above, built: refused the
	// same, and their unsigned and other-Task controls built.
	t.Run("built: records' signed Provenance names the Task", func(t *testing.T) {
		for _, p := range cdexProvNamingTheTask {
			assertSignedRefusalBoth(t, plainTask, cdexWithEntry(`{"resourceType":"Bundle","id":"r1","type":"collection"}`, p.prov), cdexCarrierProvenance)
		}
	})
	t.Run("built control: records' Provenance that names no Task", func(t *testing.T) {
		for _, p := range cdexProvNotNamingTheTask {
			assertFulfilled(t, plainTask, cdexWithEntry(`{"resourceType":"Bundle","id":"r1","type":"collection"}`, p.prov))
		}
	})
	// Records the reader refuses keep the reader's failure, whatever their
	// text holds: unreadable is not unsigned, and not signed either.
	signedToTask := cdexSignedProv("resourceType", "target", "reference", "signature", "Provenance", "Task/t1")
	t.Run("records the reader refuses: a member named twice", func(t *testing.T) {
		recs := `{"resourceType":"Bundle","id":"","id":"","type":"collection","entry":[{"resource":` + signedToTask + `}]}`
		for _, build := range cdexBuilders {
			if err := build(plainTask, recs); err == nil || errors.Is(err, shnsdk.ErrCDexSignedContent) || !strings.Contains(err.Error(), "parse records bundle") {
				t.Fatalf("got %v, want the records' parse refused", err)
			}
		}
	})
	t.Run("records the reader refuses: past its depth", func(t *testing.T) {
		recs := cdexWithEntry(`{"resourceType":"Bundle","id":"","type":"collection","x":`+strings.Repeat("[", cdexLimitDepth)+strings.Repeat("]", cdexLimitDepth)+`}`, signedToTask)
		for _, build := range cdexBuilders {
			if err := build(plainTask, recs); !errors.Is(err, shnsdk.ErrCDexLimit) || errors.Is(err, shnsdk.ErrCDexSignedContent) {
				t.Fatalf("got %v, want ErrCDexLimit", err)
			}
		}
	})
	// Signed records past the reader's depth once fulfilled: refused as signed
	// content where the fulfillment writes their id, and past the limit where
	// it keeps it, since only records whose id is written are asked about.
	signedDeep := func(head string) string {
		r := deep(head)
		return strings.Replace(r, `"type":"collection",`, `"type":"collection","signature":`+cdexSig+`,`, 1)
	}
	t.Run("signed records, their id written, past the depth once fulfilled", func(t *testing.T) {
		assertSignedRefusal(t, plainTask, signedDeep(""), cdexCarrierBundle)
	})
	t.Run("control: signed records, their id kept, past the depth once fulfilled", func(t *testing.T) {
		_, err := shnsdk.BuildCDexQueryResult([]byte(plainTask), []byte(signedDeep(`"id":"r1",`)))
		if !errors.Is(err, shnsdk.ErrCDexLimit) || errors.Is(err, shnsdk.ErrCDexSignedContent) {
			t.Fatalf("got %v, want ErrCDexLimit", err)
		}
	})
}

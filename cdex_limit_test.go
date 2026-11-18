package shnsdk_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// The reader's limits on every document a CDex fulfillment reads or builds:
// 16 MiB, nesting 64 deep, 1,000,000 JSON tokens (each value and each member
// name is one).
const (
	cdexLimitBytes  = 16 << 20
	cdexLimitDepth  = 64
	cdexLimitTokens = 1_000_000
)

// jsonTokens counts a document's tokens as the reader does: each value
// (an object or array once, at its start) and each member name.
func jsonTokens(t *testing.T, b []byte) int {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return n
		}
		if err != nil {
			t.Fatalf("count tokens: %v", err)
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			continue
		}
		n++
	}
}

// jsonDepth is a document's deepest nesting of objects and arrays.
func jsonDepth(t *testing.T, b []byte) int {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	depth, deepest := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return deepest
		}
		if err != nil {
			t.Fatalf("measure depth: %v", err)
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
			deepest = max(deepest, depth)
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
}

// cdexPadding grows an object by p: a string member p bytes longer (size), an
// array member with p more elements (tokens), or a member nested p deeper
// (depth). Each step adds exactly one to the measure it pads.
type cdexPadding struct {
	name    string
	limit   int
	reader  error // the reader's own error for this limit, kept in the chain
	measure func(*testing.T, []byte) int
	pad     func(obj string, p int) string
}

var cdexPaddings = []cdexPadding{
	{"size", cdexLimitBytes, splice.ErrSizeLimit, func(_ *testing.T, b []byte) int { return len(b) }, func(obj string, p int) string {
		return obj[:len(obj)-1] + `,"x":"` + strings.Repeat("a", p) + `"}`
	}},
	{"tokens", cdexLimitTokens, splice.ErrTokenLimit, jsonTokens, func(obj string, p int) string {
		return obj[:len(obj)-1] + `,"x":[` + strings.TrimSuffix(strings.Repeat("0,", p), ",") + `]}`
	}},
	{"depth", cdexLimitDepth, splice.ErrDepthLimit, jsonDepth, func(obj string, p int) string {
		return obj[:len(obj)-1] + `,"x":` + strings.Repeat("[", p) + strings.Repeat("]", p) + `}`
	}},
}

const (
	cdexLimitTask    = `{"resourceType":"Task","status":"requested"}`
	cdexLimitRecords = `{"resourceType":"Bundle","type":"searchset","id":"r"}`
)

// cdexBuild runs both builders on the inputs and checks they agree.
func cdexBuild(t *testing.T, task, records string) ([]byte, error) {
	t.Helper()
	f, err := shnsdk.BuildCDexFulfillment([]byte(task), []byte(records))
	out, qerr := shnsdk.BuildCDexQueryResult([]byte(task), []byte(records))
	if (err == nil) != (qerr == nil) || errors.Is(err, shnsdk.ErrCDexLimit) != errors.Is(qerr, shnsdk.ErrCDexLimit) {
		t.Fatalf("the builders disagree: BuildCDexFulfillment %v, BuildCDexQueryResult %v", err, qerr)
	}
	if err == nil && !bytes.Equal(f.Task, out) {
		t.Fatal("the builders' Tasks differ")
	}
	return f.Task, err
}

// cdexWantLimit wants ErrCDexLimit with the reader's own error for the limit
// that stopped the build still in the chain.
func cdexWantLimit(t *testing.T, what string, err, reader error) {
	t.Helper()
	if !errors.Is(err, shnsdk.ErrCDexLimit) {
		t.Fatalf("%s: got %v, want ErrCDexLimit", what, err)
	}
	if !errors.Is(err, reader) {
		t.Fatalf("%s: %v does not keep the reader's %v", what, err, reader)
	}
	if errors.Is(err, shnsdk.ErrCDexSignedContent) || errors.Is(err, shnsdk.ErrCDexTaskStatus) {
		t.Fatalf("%s: %v is also another refusal", what, err)
	}
}

// The fulfilled Task holds the request Task and the records Bundle, so it can
// pass the reader's limits when neither does alone. At each limit it is built;
// one past it, the fulfillment is refused with ErrCDexLimit, whether the
// request Task or the records Bundle grew. (The fulfilled Task is as deep as
// a request Task deeper than the records: that edge is the request Task's
// own, in TestBuildCDexFulfillment_InputsPastEachLimit.)
func TestBuildCDexFulfillment_FulfilledTaskAtEachLimit(t *testing.T) {
	for _, pd := range cdexPaddings {
		for _, grows := range []string{"the request Task", "the records Bundle"} {
			if pd.name == "depth" && grows == "the request Task" {
				continue
			}
			t.Run(pd.name+", "+grows, func(t *testing.T) {
				inputs := func(p int) (string, string) {
					if grows == "the request Task" {
						return pd.pad(cdexLimitTask, p), cdexLimitRecords
					}
					return cdexLimitTask, pd.pad(cdexLimitRecords, p)
				}
				// Padded 20, the grown input is what sets the fulfilled Task's
				// measure (deeper than the output element the fulfillment adds),
				// so each further step adds one.
				const p0 = 20
				task, records := inputs(p0)
				out, err := cdexBuild(t, task, records)
				if err != nil {
					t.Fatalf("a small fulfillment: %v", err)
				}
				p := p0 + pd.limit - pd.measure(t, out)
				task, records = inputs(p)
				if m := pd.measure(t, []byte(task)); m >= pd.limit {
					t.Fatalf("the request Task alone is at %d, want it within the limit", m)
				}
				if m := pd.measure(t, []byte(records)); m >= pd.limit {
					t.Fatalf("the records Bundle alone is at %d, want it within the limit", m)
				}
				out, err = cdexBuild(t, task, records)
				if err != nil {
					t.Fatalf("a fulfilled Task at the %s limit: %v", pd.name, err)
				}
				if m := pd.measure(t, out); m != pd.limit {
					t.Fatalf("the fulfilled Task's %s is %d, want exactly %d", pd.name, m, pd.limit)
				}
				task, records = inputs(p + 1)
				_, err = cdexBuild(t, task, records)
				cdexWantLimit(t, "a fulfilled Task one past the "+pd.name+" limit", err, pd.reader)
			})
		}
	}
}

// A request Task or a records Bundle past a limit on its own is refused with
// ErrCDexLimit. A request Task nested exactly 64 deep is fulfilled: the
// fulfillment adds no depth to it.
func TestBuildCDexFulfillment_InputsPastEachLimit(t *testing.T) {
	for _, pd := range cdexPaddings {
		for _, input := range []string{"the request Task", "the records Bundle"} {
			t.Run(pd.name+", "+input, func(t *testing.T) {
				doc := cdexLimitTask
				if input == "the records Bundle" {
					doc = cdexLimitRecords
				}
				const p0 = 20
				p := p0 + pd.limit - pd.measure(t, []byte(pd.pad(doc, p0)))
				for _, step := range []int{0, 1} {
					grown := pd.pad(doc, p+step)
					if m := pd.measure(t, []byte(grown)); m != pd.limit+step {
						t.Fatalf("%s at %d, want %d", input, m, pd.limit+step)
					}
					task, records := cdexLimitTask, cdexLimitRecords
					if input == "the request Task" {
						task = grown
					} else {
						records = grown
					}
					out, err := cdexBuild(t, task, records)
					switch {
					case step == 1:
						cdexWantLimit(t, input+" one past the "+pd.name+" limit", err, pd.reader)
					case pd.name == "depth" && input == "the request Task":
						if err != nil {
							t.Fatalf("a request Task nested exactly %d deep: %v", pd.limit, err)
						}
						if m := jsonDepth(t, out); m != pd.limit {
							t.Fatalf("the fulfilled Task is nested %d deep, want %d", m, pd.limit)
						}
					default:
						// At the limit the input is read, and the fulfilled Task,
						// which holds it, is past the limit.
						cdexWantLimit(t, input+" at the "+pd.name+" limit", err, pd.reader)
					}
				}
			})
		}
	}
}

// Records within the limits whose id the fulfillment must write, and which
// writing it takes past a limit, are refused with ErrCDexLimit: an id added
// (a member name and its value: two tokens, and its bytes), or one written
// in place of an id the Task already uses ("r" becomes "results", longer).
// The id is a member of the records' root, so it adds no depth.
func TestBuildCDexFulfillment_RecordsPastALimitOnceTheirIDIsWritten(t *testing.T) {
	bare := `{"resourceType":"Bundle","type":"searchset"}`
	added := len(`,"id":"results"`)
	owned := `{"resourceType":"Bundle","type":"searchset","id":"r"}`
	usesR := `{"resourceType":"Task","status":"requested","contained":[{"resourceType":"Basic","id":"r"}]}`
	for _, row := range []struct {
		name, task string
		records    func(t *testing.T) string
		reader     error
	}{
		{"an id added, past the size limit", cdexLimitTask, func(t *testing.T) string {
			pd := cdexPaddings[0]
			return pd.pad(bare, cdexLimitBytes-added+1-len(pd.pad(bare, 0)))
		}, splice.ErrSizeLimit},
		{"an id added, past the token limit", cdexLimitTask, func(t *testing.T) string {
			pd := cdexPaddings[1]
			return pd.pad(bare, 20+cdexLimitTokens-1-jsonTokens(t, []byte(pd.pad(bare, 20))))
		}, splice.ErrTokenLimit},
		{"an id written in place of one the Task uses, past the size limit", usesR, func(t *testing.T) string {
			pd := cdexPaddings[0]
			return pd.pad(owned, cdexLimitBytes-len(pd.pad(owned, 0)))
		}, splice.ErrSizeLimit},
	} {
		t.Run(row.name, func(t *testing.T) {
			records := row.records(t)
			if len(records) > cdexLimitBytes || jsonTokens(t, []byte(records)) > cdexLimitTokens {
				t.Fatal("the records are past a limit before their id is written")
			}
			_, err := cdexBuild(t, row.task, records)
			cdexWantLimit(t, row.name, err, row.reader)
		})
	}
}

// Every other refusal is not a limit refusal.
func TestBuildCDexFulfillment_OtherRefusalsAreNotLimits(t *testing.T) {
	for _, row := range []struct{ name, task, records string }{
		{"not a Task", `{"resourceType":"ServiceRequest","status":"requested"}`, cdexLimitRecords},
		{"a repeated member", `{"resourceType":"Task","status":"requested","Status":"completed"}`, cdexLimitRecords},
		{"not JSON", `{"resourceType":"Task",`, cdexLimitRecords},
		{"records not JSON", cdexLimitTask, `{"resourceType":"Bundle"`},
		{"a status that does not allow fulfillment", `{"resourceType":"Task","status":"completed"}`, cdexLimitRecords},
		{"signed content", `{"resourceType":"Task","status":"requested","input":[{"valueSignature":` + cdexSig + `}]}`, cdexLimitRecords},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := cdexBuild(t, row.task, row.records)
			if err == nil || errors.Is(err, shnsdk.ErrCDexLimit) {
				t.Fatalf("got %v, want a refusal other than ErrCDexLimit", err)
			}
		})
	}
}

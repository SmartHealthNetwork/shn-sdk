package shnsdk_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// retractedVersions are the published versions withdrawn from version selection.
// A {low, high} pair with low != high is a retracted range and must appear as
// that exact `[low, high]` interval. Every listed withdrawal must remain in
// go.mod in each subsequent release.
var retractedVersions = [][2]string{
	{"v0.9.0", "v0.9.0"},
	{"v0.54.0", "v0.55.0"},
}

// retracts reports whether modFile carries a top-level retract directive for
// exactly the interval [low, high]: `retract low` or `retract [low, high]`.
// This module states each withdrawal as its own directive, not in a block.
func retracts(modFile []byte, low, high string) bool {
	entry := regexp.QuoteMeta(low)
	if low != high {
		entry = `\[\s*` + regexp.QuoteMeta(low) + `\s*,\s*` + regexp.QuoteMeta(high) + `\s*\]`
	}
	return regexp.MustCompile(`(?m)^retract\s+` + entry + `\s*(//.*)?$`).Match(modFile)
}

// retractEntries lists every retract directive in modFile as a {low, high}
// pair, whether stated at top level (`retract v` / `retract [lo, hi]`) or as an
// entry line inside a `retract (` block. Comments are ignored.
func retractEntries(modFile []byte) [][2]string {
	entry := regexp.MustCompile(`^(?:\[\s*(v[^\s,\]]+)\s*,\s*(v[^\s,\]]+)\s*\]|(v\S+))\s*(?://.*)?$`)
	var out [][2]string
	add := func(s string) {
		if m := entry.FindStringSubmatch(s); m != nil {
			if m[3] != "" {
				out = append(out, [2]string{m[3], m[3]})
			} else {
				out = append(out, [2]string{m[1], m[2]})
			}
		}
	}
	inBlock := false
	for _, line := range strings.Split(string(modFile), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case inBlock && t == ")":
			inBlock = false
		case inBlock:
			add(t)
		case regexp.MustCompile(`^retract\s*\($`).MatchString(t):
			inBlock = true
		case strings.HasPrefix(t, "retract "):
			add(strings.TrimSpace(strings.TrimPrefix(t, "retract ")))
		}
	}
	return out
}

// sameRetractions reports whether got and want hold the same intervals,
// order aside.
func sameRetractions(got, want [][2]string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[[2]string]int{}
	for _, r := range want {
		seen[r]++
	}
	for _, r := range got {
		if seen[r] == 0 {
			return false
		}
		seen[r]--
	}
	return true
}

// TestGoModRetainsRetractions: `retract` directives are only honored from the
// HIGHEST released version's go.mod. Drop one in a later cut and it silently
// stops reaching anyone: the proxy keeps serving the withdrawn version and
// `go get` stops warning. Deleting a tag does not withdraw a Go module, so these
// directives are the only thing that reaches a consumer.
func TestGoModRetainsRetractions(t *testing.T) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, r := range retractedVersions {
		if !retracts(b, r[0], r[1]) {
			t.Errorf("sdk/go.mod no longer retracts [%s, %s]: a release cut from this tree would put a withdrawn version back into version selection. Carry every retraction forward at every cut.", r[0], r[1])
		}
	}
	// The directives must be exactly the listed ones: an extra or wider entry
	// could withdraw the very release being cut.
	if got := retractEntries(b); !sameRetractions(got, retractedVersions) {
		t.Errorf("sdk/go.mod retracts %v, want exactly %v", got, retractedVersions)
	}
}

// TestRetracts_RejectionRows: the matcher must not be satisfied by a narrower
// or shifted interval, by a single version standing in for a range, by a
// commented-out directive, or by a version that appears only in a require line.
func TestRetracts_RejectionRows(t *testing.T) {
	const head = "module example.com/m\n\nrequire example.com/dep v0.54.0\n\n"
	accept := []struct {
		name, mod, low, high string
	}{
		{"range", head + "// Withdrawn: superseded.\nretract [v0.54.0, v0.55.0]\n", "v0.54.0", "v0.55.0"},
		{"single", head + "retract v0.9.0\n", "v0.9.0", "v0.9.0"},
	}
	for _, c := range accept {
		if !retracts([]byte(c.mod), c.low, c.high) {
			t.Errorf("%s: retracts(%s, %s) = false, want true", c.name, c.low, c.high)
		}
	}
	reject := []struct {
		name, mod, low, high string
	}{
		{"narrower range", head + "retract [v0.54.0, v0.54.1]\n", "v0.54.0", "v0.55.0"},
		{"shifted range", head + "retract [v0.54.1, v0.55.0]\n", "v0.54.0", "v0.55.0"},
		{"single standing in for range", head + "retract v0.54.0\n", "v0.54.0", "v0.55.0"},
		{"commented out", head + "// retract [v0.54.0, v0.55.0]\n", "v0.54.0", "v0.55.0"},
		{"only in require", head, "v0.54.0", "v0.54.0"},
		{"range does not satisfy single", head + "retract [v0.54.0, v0.55.0]\n", "v0.54.0", "v0.54.0"},
		{"single with a longer patch number", head + "retract v0.9.01\n", "v0.9.0", "v0.9.0"},
		{"single with a pre-release suffix", head + "retract v0.9.0-rc.1\n", "v0.9.0", "v0.9.0"},
	}
	for _, c := range reject {
		if retracts([]byte(c.mod), c.low, c.high) {
			t.Errorf("%s: retracts(%s, %s) = true, want false", c.name, c.low, c.high)
		}
	}
}

// TestRetractEntries_ExactSet: the exact-set check sees every directive, so an
// extra or wider retraction beside the listed ones fails it.
func TestRetractEntries_ExactSet(t *testing.T) {
	const head = "module example.com/m\n\nrequire example.com/dep v0.56.0\n\n"
	want := [][2]string{{"v0.9.0", "v0.9.0"}, {"v0.54.0", "v0.55.0"}}
	exact := head + "retract v0.9.0\n\n// Withdrawn: superseded.\nretract [v0.54.0, v0.55.0]\n"
	if got := retractEntries([]byte(exact)); !sameRetractions(got, want) {
		t.Fatalf("exact directives: got %v, want %v", got, want)
	}
	reject := []struct{ name, mod string }{
		{"extra wider range", exact + "retract [v0.54.0, v0.56.0]\n"},
		{"extra single", exact + "retract v0.56.0 // oops\n"},
		{"extra block entry", exact + "retract (\n\tv0.56.0\n)\n"},
		{"one missing", head + "retract v0.9.0\n"},
	}
	for _, c := range reject {
		if got := retractEntries([]byte(c.mod)); sameRetractions(got, want) {
			t.Errorf("%s: exact-set check passed with %v", c.name, got)
		}
	}
}

// reasonLines maps each retract directive in modFile to the number of
// consecutive comment lines directly above it: its reason. A `go get` warning
// shows only the first physical line of that comment, so every reason must be
// exactly one line: a wrapped reason reaches a consumer clipped mid-clause, and
// a missing one reaches them as no reason at all.
func reasonLines(modFile []byte) map[[2]string]int {
	entry := regexp.MustCompile(`^(?:\[\s*(v[^\s,\]]+)\s*,\s*(v[^\s,\]]+)\s*\]|(v\S+))\s*(?://.*)?$`)
	out := map[[2]string]int{}
	comments := 0
	inBlock := false
	for _, line := range strings.Split(string(modFile), "\n") {
		t := strings.TrimSpace(line)
		s := ""
		switch {
		case strings.HasPrefix(t, "//"):
			comments++
			continue
		case inBlock && t == ")":
			inBlock = false
		case inBlock:
			s = t
		case regexp.MustCompile(`^retract\s*\($`).MatchString(t):
			inBlock = true
		case strings.HasPrefix(t, "retract "):
			s = strings.TrimSpace(strings.TrimPrefix(t, "retract "))
		}
		if m := entry.FindStringSubmatch(s); m != nil {
			if m[3] != "" {
				out[[2]string{m[3], m[3]}] = comments
			} else {
				out[[2]string{m[1], m[2]}] = comments
			}
		}
		comments = 0
	}
	return out
}

// TestRetractReasonsAreOneLine: every retraction's reason in go.mod is exactly
// one physical comment line.
func TestRetractReasonsAreOneLine(t *testing.T) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for r, n := range reasonLines(b) {
		if n != 1 {
			t.Errorf("go.mod retraction [%s, %s] has a %d-line reason; write it on exactly one line", r[0], r[1], n)
		}
	}
}

// TestReasonLines_RejectionRows: a wrapped or missing reason is counted as
// such, in a block and at top level.
func TestReasonLines_RejectionRows(t *testing.T) {
	cases := []struct {
		name, mod string
		want      int
	}{
		{"one line at top level", "module m\n\n// Withdrawn: superseded.\nretract v0.9.0\n", 1},
		{"wrapped at top level", "module m\n\n// Withdrawn: the reason\n// runs on.\nretract v0.9.0\n", 2},
		{"missing at top level", "module m\n\nretract v0.9.0\n", 0},
		{"blank line breaks the reason", "module m\n\n// Withdrawn: superseded.\n\nretract v0.9.0\n", 0},
		{"one line in a block", "module m\n\n// block header\n// more header\nretract (\n\t// Withdrawn: superseded.\n\tv0.9.0\n)\n", 1},
		{"wrapped in a block", "module m\n\nretract (\n\t// Withdrawn: the reason\n\t// runs on.\n\tv0.9.0\n)\n", 2},
		{"missing in a block", "module m\n\n// block header\nretract (\n\tv0.9.0\n)\n", 0},
	}
	for _, c := range cases {
		got, ok := reasonLines([]byte(c.mod))[[2]string{"v0.9.0", "v0.9.0"}]
		if !ok || got != c.want {
			t.Errorf("%s: reasonLines = %d (found %v), want %d", c.name, got, ok, c.want)
		}
	}
}

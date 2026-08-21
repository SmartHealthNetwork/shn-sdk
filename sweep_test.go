package shnsdk_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// internalTokenPattern is the publish-runbook's internal-vocabulary sweep, made
// executable so it runs in every `go test ./...` of this module rather than as
// a grep someone remembers at cut time. It catches the shorthand a private
// planning process leaves behind in comments and strings — plan/slice task
// numbers, internal design-decision ids, private repository paths, internal
// tooling names, review-round and issue references, decision-ledger entries,
// and pointers into private design notes — none of which mean anything to a
// partner reading this module's source after it is published as a standalone
// snapshot.
//
// The pattern is IDENTICAL to the gateway module's sweep (gateway/sweep_test.go
// in the monorepo): the two published modules are held to one vocabulary
// contract, and every arm here has a form that actually leaked into a release
// candidate of one module or the other. Three notes on arms that look odd:
//
//   - `#[0-9]{2,}\b` catches monorepo issue/PR references. Two reached this
//     module's tree between consecutive cuts — one in shipped code, one as a
//     test section header — and both would have been first-time public
//     introductions had the cut-time grep not been run. An issue number is
//     meaningless to a consumer of the published API, and a module version is
//     immutable once fetched, so shipping one burns a version number.
//   - `(?i:spec §|spec[ (]*<date>)` catches pointers into private design notes.
//     The fix for a match is never to widen the grep: state what the note
//     DECIDED, since a partner cannot read it.
//   - Every token in this pattern is itself published, because this file ships
//     inside the module and must state its own pattern literally. That is
//     harmless for planning vocabulary, which is why this class can live here.
//     Anything whose LIST ENTRY WOULD BE THE DISCLOSURE (a partner's name, an
//     internal tool's name) is enforced from the monorepo root instead, where
//     the list does not ship. Do not add such an arm here, and never split or
//     obfuscate a literal to satisfy a grep — that is the exact evasion these
//     sweeps exist to catch.
//
// Published spec ids are DELIBERATELY not in this pattern: FR-G*, AI-G*, OWD-G*
// and UC-0X are partner-facing vocabulary — they appear in the published
// participant protocol and preview guides under docs/ — and must keep appearing
// in code and comments here too. A ban on UC-0X in shipped code once lived only
// in the publish runbook while the tree carried the tags across releases; it
// was retired as inconsistent with the published guides, not enforced.
const internalTokenPattern = `S5b|Task[ -][0-9]|(?i:\btask-[0-9])|per the plan|Material-|infra/|goldengen|shn-platform|\bE[0-9][a-z][0-9]?\b|\bD[0-9]\b` +
	`|\bK1\b|PR #[0-9]+|#[0-9]{2,}\b|docs/superpowers|(?i:\bslice[ -][0-9]\b)|\bBo\b|review-fixes|\bround-[0-9]\b` +
	`|ledger[ -][0-9]|(?i:ledger[ -]item[ -][0-9])|option[ -][A-Z] ruling|A′|\bA'[ .,)]|\bT-[0-9]\b|\b[SM]F[0-9]+\b` +
	`|(?i:spec §|spec[ (]*[0-9]{4}-[0-9]{2}-[0-9]{2})`

// sweepSkipFiles are the module-root test files excluded from the sweep.
//
//   - sweep_test.go: this file. It must state the pattern (and the allowlist
//     substrings) literally, so it necessarily matches itself. Excluding it is
//     safe precisely because it holds no prose about the module's behavior —
//     only the pattern and the allowlist table below, both of which are the
//     subject of review whenever they change.
//   - deps_test.go: its assertion is *about* the substrate module path ("this
//     module must never depend on shn-platform/internal"), so the token is the
//     load-bearing subject of the check, not leaked prose. Rewording it would
//     break the dependency-purity test it exists to run.
var sweepSkipFiles = map[string]bool{
	"sweep_test.go": true,
	"deps_test.go":  true,
}

// sweepSkipDirs are walked past entirely. testdata/ holds the binary wire
// vectors: base64 ciphertext yields `E[0-9][a-z]` false hits by the hundred,
// and nothing under it is prose a partner reads.
var sweepSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"testdata":     true,
}

// sweepAllowlist pins the individual lines that legitimately match the broad
// pattern above. Key = module-relative slash path; value = exact substrings of
// the offending line. An entry means "this match is a false positive or is
// genuinely partner-facing" — every entry carries a WHY comment. Adding one is
// a review decision, not a way to silence the sweep: the substring must be
// specific enough that unrelated prose on the same line cannot hide behind it.
//
// Empty is the goal state and is a stronger claim than any entry: it says the
// shipped module contains no line that even LOOKS like internal vocabulary. The
// table is kept (rather than deleted with the last entry) because the broad
// token classes — a bare `D<digit>` or `E<digit><letter>` — will eventually
// collide with real partner-facing vocabulary (X12/EDI qualifiers, for one),
// and the reviewable place to record that judgement is here.
var sweepAllowlist = map[string][]string{}

// TestInternalTokenPattern_Forms is the rejection test for the pattern itself,
// and it exists because TestNoInternalTokens ALONE CAN PASS FOR THE WRONG
// REASON: it asserts only that the tree holds no match, so a pattern that
// describes nothing is indistinguishable from a tree that is clean. That is
// how a gateway release once shipped sixteen design-note references under a
// green sweep and had to be withdrawn.
//
// Every "must match" row is a form that actually leaked from one of the two
// published modules (or the near-twin a narrower grep missed); every "must not
// match" row pins the boundary so a later widening cannot quietly swallow
// ordinary prose about a published specification, which is legitimate
// partner-facing vocabulary.
func TestInternalTokenPattern_Forms(t *testing.T) {
	re := regexp.MustCompile(internalTokenPattern)

	mustMatch := []string{
		// Design-note pointers: section-only, date + section, bare date, and
		// the sentence-initial capital that got past a case-sensitive arm.
		`// (spec §1 invariant: ok:false ⇒ failure present)`,
		`// machine classification (spec 2026-08-09 §1/§2): present, exact code`,
		`// the drift rule this encodes was settled in spec 2026-08-10`,
		`// Spec 2026-08-11, F7 — a HAPI instance hosts exactly ONE version`,
		// Punctuation between the word and the date.
		`// Adversarial row for the opaque-payload message-frame spec (2026-07-17):`,
		// Lowercase hyphenated task ids — the case blind spot on the Task arm.
		`// fix-round finding 1 (task-3 review) — routeInfoFor must render`,
		// Monorepo issue references: the two forms that reached this module.
		`// the answer-count floor (#427): a questionnaire item that admits`,
		`// --- #432 walkers: item recursion ---`,
		// Decision-ledger shorthand, including the hyphenated spelling that
		// survived a first scrub pass.
		`// resolved per ledger item 5 — the member fence stays`,
		`// identifier semantics per ledger-2b: a member number, not a reference`,
		`// the option-C ruling keeps the urn:shn:coverage id as a member number`,
		// Review-finding shorthand.
		`// SF5: the pended ledger must not be consulted on a fresh submit`,
	}
	for _, line := range mustMatch {
		if m := re.FindString(line); m == "" {
			t.Errorf("pattern does not describe a form that has ALREADY leaked — a green sweep would be meaningless against it:\n\t%s", line)
		}
	}

	// A citation that wraps across a comment break holds no single line with
	// the token, so it is unreachable by the pattern no matter how the pattern
	// is spelled. sweepUnits is what closes that, and these rows are its
	// rejection test.
	mustMatchWrapped := []string{
		"\t// frame carrying the app status and relays it 200-to-Hub (verbatim; spec\n\t// 2026-07-17) — the error-branch sibling of respondLeg.",
		"// Also writes a *RouteRefusalError (the version-routing legible refusal, spec\n// 2026-08-10 §4) as its 422 — one chokepoint covers every origination site.",
	}
	for _, block := range mustMatchWrapped {
		var joined string
		for _, u := range sweepUnits(block) {
			if u.joined {
				joined = u.text
			}
		}
		if joined == "" {
			t.Errorf("sweepUnits produced no joined unit for a wrapped comment — wrapped citations would stay unreachable:\n\t%q", block)
			continue
		}
		if m := re.FindString(joined); m == "" {
			t.Errorf("pattern does not describe a WRAPPED form that has already leaked (joined: %q):\n\t%s", joined, block)
		}
	}

	// Boundary rows, one per widening above. Without them a widening is a
	// one-way ratchet — nothing states where it is supposed to STOP.
	mustNotMatch := []string{
		// Prose about a PUBLISHED spec is partner-facing and must survive.
		`// the FHIR specification requires a searchset Bundle here`,
		`// see the spec for the full profile list`,
		`// US Core §3.1.1 pins the identifier slice`,
		// A bare date is not a reference to a private design note.
		`// last reviewed 2026-08-10 against the published IG`,
		// A comma is ordinary prose about a dated edition of a PUBLISHED spec.
		`// built against the FHIR spec, 2026-08-10 edition, per the IG`,
		// Lowercase `task <n>` with a SPACE is ordinary English.
		`// the operator runs task 1 before task 2 during a cut`,
		// Published scenario and requirement ids are partner vocabulary.
		`// DeviceRequest order (UC-02 hospital-bed E0250) selects the request`,
		`// FR-G53 carry contract: the reviewer extension survives the round trip`,
		// A bare `#` + single digit is a list marker, not an issue reference.
		`// step #3 of the handshake sends the assertion`,
	}
	for _, line := range mustNotMatch {
		if m := re.FindString(line); m != "" {
			t.Errorf("pattern is too broad — it flags ordinary prose about published vocabulary as internal (matched %q):\n\t%s", m, line)
		}
	}

	// Joining must not MANUFACTURE a reference that exists in neither line.
	mustNotMatchWrapped := []string{
		"* Validated against the published FHIR spec\n* 2026-08-10 was the cut date for this line",
		"// The FHIR specification lists the profile set; the cut\n// date 2026-08-10 is recorded in the published release notes.",
	}
	for _, block := range mustNotMatchWrapped {
		for _, u := range sweepUnits(block) {
			if !u.joined {
				continue
			}
			if m := re.FindString(u.text); m != "" {
				t.Errorf("joining MANUFACTURED an internal reference that is in neither source line (matched %q, joined: %q):\n\t%s", m, u.text, block)
			}
		}
	}
}

// TestNoInternalTokens keeps the published SDK snapshot free of internal
// planning vocabulary BY CONSTRUCTION, rather than by a manual grep at publish
// time. It walks the whole module tree — not just Go files, since the docs,
// fixtures and CLI sources ship too — and fails naming file:line for every
// non-allowlisted match.
func TestNoInternalTokens(t *testing.T) {
	re := regexp.MustCompile(internalTokenPattern)

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if sweepSkipDirs[d.Name()] && path != "." {
				return fs.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(path)
		if sweepSkipFiles[rel] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > 4<<20 {
			return nil // outsized blob: not source prose
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return nil // binary
		}
		allowed := sweepAllowlist[rel]
		// Keyed by line AND token: an operator scrubbing a cut has to see
		// EVERY offense, so one token must never suppress the report of a
		// different token that happens to share its comment run.
		reported := map[reportKey]bool{}
		for _, u := range sweepUnits(string(b)) {
			if sweepLineAllowed(u.text, allowed) {
				continue
			}
			for _, m := range dedupe(re.FindAllString(u.text, -1)) {
				if u.joined && anyReported(reported, u.start, u.end, m) {
					continue
				}
				reported[reportKey{u.start, m}] = true
				t.Errorf("%s:%d: internal-vocabulary token %q leaks into the published module — reword to public vocabulary (published spec ids FR-G*/AI-G*/OWD-G*/UC-0X are fine), or add the line to sweepAllowlist with a WHY comment.\n\t%s",
					rel, u.start, m, sweepExcerpt(u.text, m))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module tree: %v", err)
	}
}

// sweepCommentPrefix matches the comment marker that opens a continuation line
// in the file types this sweep walks: Go `//` and shell/YAML `#`. `*` (a
// block-comment body) is deliberately absent: markdown uses it for bullets, and
// joining two adjacent bullets manufactures a reference that is in neither one.
var sweepCommentPrefix = regexp.MustCompile(`^\s*(?://+|#+)[ \t]?`)

// sweepUnit is one chunk of a file the sweep matches against: either a raw
// line, or a run of consecutive comment lines joined into one logical line.
type sweepUnit struct {
	start, end int // 1-indexed, inclusive; start == end for a raw line
	text       string
	joined     bool
}

// sweepUnits returns every raw line, PLUS each run of consecutive comment lines
// joined into a single logical line reported at the run's first line. The
// joined form exists because a citation that WRAPS across a comment break is
// otherwise structurally unreachable by every arm of the pattern: the sweep
// splits on "\n", so `(verbatim; spec` / `// 2026-07-17)` contains no single
// line holding `spec <date>`. A parenthetical citation at the end of a sentence
// is precisely what wraps, so this class is the one most exposed to it.
func sweepUnits(content string) []sweepUnit {
	lines := strings.Split(content, "\n")
	units := make([]sweepUnit, 0, len(lines)+8)
	for i, line := range lines {
		units = append(units, sweepUnit{start: i + 1, end: i + 1, text: line})
	}
	for i := 0; i < len(lines); {
		prefix := sweepCommentPrefix.FindString(lines[i])
		if prefix == "" {
			i++
			continue
		}
		j, parts := i, []string(nil)
		for j < len(lines) {
			p := sweepCommentPrefix.FindString(lines[j])
			if p == "" {
				break
			}
			parts = append(parts, strings.TrimSpace(lines[j][len(p):]))
			j++
		}
		if len(parts) > 1 {
			units = append(units, sweepUnit{
				start: i + 1, end: j, text: strings.Join(parts, " "), joined: true,
			})
		}
		i = j
	}
	return units
}

// sweepExcerpt trims the reported text to a window around the match. A joined
// comment run can be an entire doc comment, and dumping all of it buries the
// token the operator has to go and reword.
func sweepExcerpt(text, match string) string {
	text = strings.TrimSpace(text)
	i := strings.Index(text, match)
	if i < 0 {
		return text
	}
	start, end := i-60, i+len(match)+60
	prefix, suffix := "…", "…"
	if start < 0 {
		start, prefix = 0, ""
	}
	if end > len(text) {
		end, suffix = len(text), ""
	}
	// These comments carry §, — and ⇒, so a byte offset can land mid-rune.
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	return prefix + strings.TrimSpace(text[start:end]) + suffix
}

// reportKey identifies one reported offense: the line it was named at, and the
// token that was named.
type reportKey struct {
	line  int
	token string
}

// anyReported reports whether THIS token was already named on any line in
// [start,end].
func anyReported(reported map[reportKey]bool, start, end int, token string) bool {
	for i := start; i <= end; i++ {
		if reported[reportKey{i, token}] {
			return true
		}
	}
	return false
}

// dedupe removes repeated matches, preserving order: one line naming the same
// token twice is one offense to reword.
func dedupe(matches []string) []string {
	seen := make(map[string]bool, len(matches))
	out := matches[:0:0]
	for _, m := range matches {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func sweepLineAllowed(line string, allowed []string) bool {
	for _, sub := range allowed {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

package shnsdk_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoForbiddenDependencies asserts the SDK module never pulls the substrate's
// internal packages or heavy server deps. This is the public/private IP boundary
// (no internal/ leaks) AND the dependency-light guarantee. Load-bearing.
func TestNoForbiddenDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	// The substrate IP boundary (shn-platform/*) and the heavy server deps a
	// dependency-light participant SDK must never pull. (HAPI is the substrate's
	// FHIR validator/server, reached over HTTP — there is no Go HAPI client module
	// to forbid; a participant talks FHIR via samply/golang-fhir-models, which is
	// allowed.)
	forbidden := []string{
		"shn-platform/internal",
		"shn-platform/cmd",
		"shn-platform/tools",
		"open-policy-agent",
		"jackc/pgx",
		"jackc/pgconn",
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range forbidden {
			if strings.Contains(line, f) {
				t.Errorf("forbidden dependency %q (via %q)", f, line)
			}
		}
	}
}

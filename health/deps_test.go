package health_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestHealthPackageIsStdlibOnly pins sdk/health to the standard library. The
// health payload contract must stay dependency-free: it is mounted on every
// service including partner-run gateways, and dependency-lightness is the
// reason it lives in the sdk at all (spec §5.5). The module-wide
// TestNoForbiddenDependencies does NOT enforce this (it allows fhir-models,
// x/crypto module-wide), hence this per-package gate.
func TestHealthPackageIsStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(dep, ".") && !strings.HasPrefix(dep, "github.com/SmartHealthNetwork/shn-sdk/health") {
			// A dot in the first path segment marks a non-stdlib module path.
			if !strings.Contains(strings.SplitN(dep, "/", 2)[0], ".") {
				continue // stdlib (e.g. net/http, encoding/json)
			}
			t.Errorf("sdk/health must be stdlib-only; found dependency %q", dep)
		}
	}
}

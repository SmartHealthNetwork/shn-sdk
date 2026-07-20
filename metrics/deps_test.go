package metrics

import (
	"os/exec"
	"strings"
	"testing"
)

// The emitter sits in every probe path and the published SDK: stdlib-only by
// policy, same gate as sdk/health (spec §4).
func TestMetricsPackageIsStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/SmartHealthNetwork/shn-sdk") {
			continue
		}
		if strings.Contains(strings.SplitN(dep, "/", 2)[0], ".") {
			t.Errorf("non-stdlib dependency: %s", dep)
		}
	}
}

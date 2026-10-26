package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestPriorAuth_Approved drives `shn priorauth` against the fake network (reusing the
// doctor fake, now extended with the three PA legs): it resolves Payer+Endpoints from
// the discovery descriptor and runs the MBR-COVERED→approved prior-auth path, printing the
// outcome.
func TestPriorAuth_Approved(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "outcome=approved") {
		t.Errorf("stdout should report outcome=approved: %s", stdout)
	}
	if !strings.Contains(stdout, "preAuthRef=") {
		t.Errorf("stdout should report preAuthRef: %s", stdout)
	}
}

// TestPriorAuth_RequiresFlags: missing --member/--discovery/--id is a usage error.
func TestPriorAuth_RequiresFlags(t *testing.T) {
	_, _, code := runCLI("priorauth", "--member", "MBR-COVERED")
	if code == 0 {
		t.Fatal("priorauth without --discovery/--id should fail")
	}
}

func TestPriorAuthResumeHandleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shn-resume.json")
	want := shnsdk.PriorAuthResume{
		OriginalCorrelationID: "corr-1",
		PatientRef:            "Patient/MBR-UC04",
		CoverageRef:           "Coverage/MBR-UC04",
		MemberID:              "MBR-UC04",
		SubjectPCI:            "pci:abc",
		QRJSON:                []byte(`{"resourceType":"QuestionnaireResponse"}`),
		SRJSON:                []byte(`{"resourceType":"ServiceRequest"}`),
		NeededItems:           []shnsdk.NeededItem{{Code: "operative-diagnostic-report", Display: "operative-diagnostic-report"}},
	}
	if err := writeResumeHandle(path, want); err != nil {
		t.Fatalf("writeResumeHandle: %v", err)
	}
	got, err := readResumeHandle(path)
	if err != nil {
		t.Fatalf("readResumeHandle: %v", err)
	}
	if got.OriginalCorrelationID != want.OriginalCorrelationID || got.SubjectPCI != want.SubjectPCI {
		t.Errorf("round-trip lost fields: got %+v", got)
	}
	// MemberID is the additive field the resume ClaimUpdate stamps as the
	// Coverage's bare urn:shn:coverage identifier value — it must survive the handle.
	if got.MemberID != want.MemberID {
		t.Errorf("MemberID round-trip drift: got %q, want %q", got.MemberID, want.MemberID)
	}
	if string(got.QRJSON) != string(want.QRJSON) {
		t.Errorf("QRJSON round-trip drift: %s", got.QRJSON)
	}
}

// TestPriorAuth_DenyIsNonZero: a denied claim surfaces as a clear nonzero exit
// (Outcome "denied" with the A3 reason; the command reports it and exits nonzero).
func TestPriorAuth_DenyIsNonZero(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	f.paDeny = true

	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code == 0 {
		t.Fatalf("priorauth with a denied claim should exit nonzero\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

// TestPriorAuth_DirectoryResolution: the fixture personas carry a payerId (the
// default) — priorauth resolves its test counterparty from the directory, with no
// legacy visibility note on stderr.
func TestPriorAuth_DirectoryResolution(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "outcome=approved") {
		t.Errorf("stdout should report outcome=approved: %s", stdout)
	}
	if strings.Contains(stderr, "resolved via demoResponders") {
		t.Errorf("stderr must not mention the legacy fallback on the directory-resolution path: %s", stderr)
	}
}

// TestPriorAuth_LegacyFallbackNote (R4): personas without payerId → priorauth falls
// back to the legacy demoResponders path and prints the visibility note on stderr.
func TestPriorAuth_LegacyFallbackNote(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	for i := range f.personas {
		f.personas[i].PayerID = nil
	}
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stderr, "note: resolved via demoResponders — network predates persona payerId") {
		t.Errorf("stderr should report the R4 visibility note: %s", stderr)
	}
}

// TestPriorAuth_PayerIDNoMatchRefused (R3): the persona's payerId matches zero
// /holders rows → refuse (rc=1), never fall back to the legacy responder.
func TestPriorAuth_PayerIDNoMatchRefused(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	f.holders[0]["payerIds"] = []map[string]string{{"system": shnsdk.CMSPayerIdentity.System, "value": "99999"}}
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != 1 {
		t.Fatalf("priorauth exit=%d, want 1\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "resolves to no holder in the directory") {
		t.Errorf("stderr should report the zero-match refusal: %s", stderr)
	}
}

// TestPriorAuthResume_HandleCarriesPayerID: a pended run stamps the resume handle
// with the persona's payerId, and `priorauth resume` re-resolves the test payer from
// the directory (no legacy note) using the stamped handle.
func TestPriorAuthResume_HandleCarriesPayerID(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	f.paPended = true
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	resumeOut := filepath.Join(dir, "resume.json")
	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir, "--resume-out", resumeOut)
	if code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "outcome=pended") {
		t.Fatalf("stdout should report outcome=pended: %s", stdout)
	}
	raw, err := os.ReadFile(resumeOut)
	if err != nil {
		t.Fatalf("read resume handle: %v", err)
	}
	if !strings.Contains(string(raw), `"payerId"`) {
		t.Errorf("resume handle should carry payerId: %s", raw)
	}

	stdout2, stderr2, code2 := runCLI("priorauth", "resume", "--resume", resumeOut, "--report-file", writeOwnReport(t, dir, resumeOut), "--provenance-agent-system", "http://smarthealth.network/ids/holder", "--provenance-agent-value", devID, "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code2 != exitOK {
		t.Fatalf("priorauth resume exit=%d (want %d)\nstdout=%s\nstderr=%s", code2, exitOK, stdout2, stderr2)
	}
	if !strings.Contains(stdout2, "outcome=approved") {
		t.Errorf("stdout should report outcome=approved after resume: %s", stdout2)
	}
	if strings.Contains(stderr2, "resolved via demoResponders") {
		t.Errorf("resume with a payerId-carrying handle must not take the legacy path: %s", stderr2)
	}
}

// TestPriorAuthResume_WithoutTheRetiredFields: a request pended on a 1.1.0
// network resumes once that network states 2.0.0, whose descriptor leaves out
// endpoints.patientAccess, contractVersions and demoResponders. A handle that
// carries the persona's payerId resumes to its outcome with no legacy path; a
// handle without one has nothing to fall back to and refuses, saying why.
func TestPriorAuthResume_WithoutTheRetiredFields(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	f.paPended = true
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub
	resumeOut := filepath.Join(dir, "resume.json")
	if stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir, "--resume-out", resumeOut); code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	f.withoutRetired = true
	requireWithoutRetired(t, srv.URL+"/discovery")
	resume := func(handle string) (string, string, int) {
		return runCLI("priorauth", "resume", "--resume", handle, "--report-file", writeOwnReport(t, dir, handle), "--provenance-agent-system", "http://smarthealth.network/ids/holder", "--provenance-agent-value", devID, "--discovery", srv.URL, "--id", devID, "-keys", dir)
	}

	raw, err := os.ReadFile(resumeOut)
	if err != nil {
		t.Fatalf("read resume handle: %v", err)
	}
	var h map[string]json.RawMessage
	if err := json.Unmarshal(raw, &h); err != nil || h["payerId"] == nil {
		t.Fatalf("the pend's handle carries no payerId (%v): %s", err, raw)
	}
	delete(h, "payerId")
	bare, _ := json.Marshal(h)
	bareOut := filepath.Join(dir, "resume-without-payerid.json")
	if err := os.WriteFile(bareOut, bare, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := resume(bareOut)
	if code == exitOK || !strings.Contains(stdout+stderr, "no persona payerId, no demoResponders") {
		t.Errorf("a handle without payerId against a descriptor without demoResponders: exit %d, want a refusal saying why\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	stdout, stderr, code = resume(resumeOut)
	if code != exitOK || !strings.Contains(stdout, "outcome=approved") {
		t.Fatalf("priorauth resume exit=%d (want %d, approved)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if strings.Contains(stderr, "demoResponders") {
		t.Errorf("resume with a payerId-carrying handle took the legacy path: %s", stderr)
	}
}

// TestPriorAuthResume_LegacyHandle (R4 compat): a resume handle written before this
// migration (no payerId field) still resumes — via the legacy demoResponders path,
// with the visibility note on stderr.
func TestPriorAuthResume_LegacyHandle(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	f.paPended = true
	for i := range f.personas {
		f.personas[i].PayerID = nil
	}
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	resumeOut := filepath.Join(dir, "resume.json")
	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir, "--resume-out", resumeOut)
	if code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	raw, err := os.ReadFile(resumeOut)
	if err != nil {
		t.Fatalf("read resume handle: %v", err)
	}
	if strings.Contains(string(raw), `"payerId"`) {
		t.Errorf("a legacy-path pend must not stamp payerId onto the handle: %s", raw)
	}

	stdout2, stderr2, code2 := runCLI("priorauth", "resume", "--resume", resumeOut, "--report-file", writeOwnReport(t, dir, resumeOut), "--provenance-agent-system", "http://smarthealth.network/ids/holder", "--provenance-agent-value", devID, "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code2 != exitOK {
		t.Fatalf("priorauth resume exit=%d (want %d)\nstdout=%s\nstderr=%s", code2, exitOK, stdout2, stderr2)
	}
	if !strings.Contains(stdout2, "outcome=approved") {
		t.Errorf("stdout should report outcome=approved after resume: %s", stdout2)
	}
	if !strings.Contains(stderr2, "note: resolved via demoResponders — network predates persona payerId") {
		t.Errorf("resume with a payerId-less handle should take the legacy path and note it: %s", stderr2)
	}
}

// TestPriorAuthResume_HandleWithoutPayerIDIgnoresLiveNetworkPersonas: sharper R4 compat
// case than TestPriorAuthResume_LegacyHandle above — here the live network's personas
// DO carry a payerId (default fixture, untouched), but the handle itself is downgraded
// to the pre-migration shape (payerId stripped after the pend). Resume must still take
// the legacy path, driven by handle.PayerID — NOT by re-deriving a payerId from the
// live network's disc.DemoPersonas. Regression guard: catches resume consulting the
// wrong source of truth for the payer-identity claim.
func TestPriorAuthResume_HandleWithoutPayerIDIgnoresLiveNetworkPersonas(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	f.paPended = true
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	resumeOut := filepath.Join(dir, "resume.json")
	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir, "--resume-out", resumeOut)
	if code != exitOK {
		t.Fatalf("priorauth exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}

	// Downgrade the just-written handle to the pre-migration shape, even though the
	// network we resume against still advertises personas WITH a payerId.
	handle, err := readResumeHandle(resumeOut)
	if err != nil {
		t.Fatalf("readResumeHandle: %v", err)
	}
	if handle.PayerID == nil {
		t.Fatalf("precondition: the pend should have stamped payerId onto the handle")
	}
	handle.PayerID = nil
	if err := writeResumeHandle(resumeOut, handle); err != nil {
		t.Fatalf("writeResumeHandle: %v", err)
	}

	stdout2, stderr2, code2 := runCLI("priorauth", "resume", "--resume", resumeOut, "--report-file", writeOwnReport(t, dir, resumeOut), "--provenance-agent-system", "http://smarthealth.network/ids/holder", "--provenance-agent-value", devID, "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code2 != exitOK {
		t.Fatalf("priorauth resume exit=%d (want %d)\nstdout=%s\nstderr=%s", code2, exitOK, stdout2, stderr2)
	}
	if !strings.Contains(stdout2, "outcome=approved") {
		t.Errorf("stdout should report outcome=approved after resume: %s", stdout2)
	}
	if !strings.Contains(stderr2, "note: resolved via demoResponders — network predates persona payerId") {
		t.Errorf("a payerId-less handle must take the legacy path driven by the HANDLE, even though the live network's personas carry a payerId: %s", stderr2)
	}
}

// writeOwnReport writes, beside the resume handle, a DiagnosticReport as the caller's own
// system holds it, about the handle's patient, and returns its path: what
// `shn priorauth resume --report-file` sends. Its clinical values are this test record's own.
func writeOwnReport(t *testing.T, dir, resumePath string) string {
	t.Helper()
	raw, err := os.ReadFile(resumePath)
	if err != nil {
		t.Fatalf("read resume handle: %v", err)
	}
	var h struct {
		PatientRef string `json:"patientRef"`
	}
	if err := json.Unmarshal(raw, &h); err != nil || h.PatientRef == "" {
		t.Fatalf("the resume handle names no patient (%v): %s", err, raw)
	}
	report := `{"resourceType":"DiagnosticReport","id":"dr-own","status":"final",` +
		`"category":[{"coding":[{"system":"http://loinc.org","code":"LP29684-5","display":"Radiology"}]}],` +
		`"code":{"coding":[{"system":"http://loinc.org","code":"18748-4","display":"Diagnostic imaging study"}]},` +
		`"subject":{"reference":"` + h.PatientRef + `"},"effectiveDateTime":"2026-04-30"}`
	path := filepath.Join(dir, "own-report.json")
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// `shn priorauth resume` sends the report file the caller names, and builds none: without
// --report-file it is a usage error, and a file it cannot read stops it before the network.
func TestPriorAuthResume_ReportFile(t *testing.T) {
	dir := t.TempDir()
	resumeOut := filepath.Join(dir, "shn-resume.json")
	if err := os.WriteFile(resumeOut, []byte(`{"patientRef":"Patient/MBR-UC04"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI("priorauth", "resume", "--resume", resumeOut, "--discovery", "http://127.0.0.1:1", "--id", "acme", "-keys", dir)
	if code != exitUsage || !strings.Contains(stderr, "--report-file are required") {
		t.Fatalf("no --report-file: exit %d (want %d), stderr %s", code, exitUsage, stderr)
	}
	_, stderr, code = runCLI("priorauth", "resume", "--resume", resumeOut, "--report-file", filepath.Join(dir, "absent.json"), "--discovery", "http://127.0.0.1:1", "--id", "acme", "-keys", dir)
	if code != 1 || !strings.Contains(stderr, "read report") {
		t.Fatalf("an unreadable --report-file: exit %d (want 1), stderr %s", code, stderr)
	}
}

// TestPriorAuthResume_WireVersionMembership: priorauth resume goes on when the
// network accepts a wire version this CLI speaks beside its own, and refuses
// with the upgrade prompt when it shares none.
func TestPriorAuthResume_WireVersionMembership(t *testing.T) {
	f, devID, dir := newFakeNetwork(t)
	f.paPended = true
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub
	resumeOut := filepath.Join(dir, "resume.json")
	if stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir, "--resume-out", resumeOut); code != exitOK {
		t.Fatalf("fixture: priorauth exit=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	resume := func() (string, int) {
		stdout, stderr, code := runCLI("priorauth", "resume", "--resume", resumeOut, "--report-file", writeOwnReport(t, dir, resumeOut), "--provenance-agent-system", "http://smarthealth.network/ids/holder", "--provenance-agent-value", devID, "--discovery", srv.URL, "--id", devID, "-keys", dir)
		return stdout + stderr, code
	}

	f.discWireVersion, f.discAccepted = "9.9.9", []string{"8.8.8"}
	if out, code := resume(); code != 1 || !strings.Contains(out, "upgrade your SDK/CLI") || !strings.Contains(out, `accepts ["8.8.8"]`) {
		t.Fatalf("a network sharing no wire version: exit %d, want 1 with the upgrade prompt\n%s", code, out)
	}
	f.discAccepted = []string{"9.9.9", shnsdk.WireProtocolVersion}
	if out, code := resume(); code != exitOK || !strings.Contains(out, "outcome=approved") {
		t.Fatalf("a network accepting this CLI's wire version: exit %d, want the resume to complete\n%s", code, out)
	}
}

package main

import (
	"path/filepath"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestPriorAuth_Approved drives `shn priorauth` against the fake sandbox (reusing the
// doctor fake, now extended with the three PA legs): it resolves Payer+Endpoints from
// the discovery descriptor and runs the UC-03 MBR-COVERED→approved path, printing the
// outcome.
func TestPriorAuth_Approved(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
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
	if string(got.QRJSON) != string(want.QRJSON) {
		t.Errorf("QRJSON round-trip drift: %s", got.QRJSON)
	}
}

// TestPriorAuth_DenyIsNonZero: a denied claim surfaces as a clear nonzero exit
// (Outcome "denied" with the A3 reason; the command reports it and exits nonzero).
func TestPriorAuth_DenyIsNonZero(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	f.paDeny = true

	stdout, stderr, code := runCLI("priorauth", "--member", "MBR-COVERED", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code == 0 {
		t.Fatalf("priorauth with a denied claim should exit nonzero\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

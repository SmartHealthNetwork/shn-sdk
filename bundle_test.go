package shnsdk_test

import (
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

func TestWriteLoadBundle_RoundTrip(t *testing.T) {
	id, err := shnsdk.GenerateIdentity("holder-x")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := shnsdk.WriteBundle(dir, id, "provider", "https://gw.example"); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	b, err := shnsdk.LoadBundle(dir)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if b.Manifest.ID != "holder-x" || b.Manifest.Role != "provider" || b.Manifest.BaseURL != "https://gw.example" {
		t.Fatalf("manifest = %+v", b.Manifest)
	}
	if b.Identity.HolderID != "holder-x" {
		t.Fatalf("identity holder = %q", b.Identity.HolderID)
	}
	// keys recovered: signing priv must verify-pair with the manifest's signPub
	if len(b.Identity.SignPriv) != 64 || b.Identity.EncPriv == nil {
		t.Fatalf("keys not recovered: sign=%d enc=%v", len(b.Identity.SignPriv), b.Identity.EncPriv)
	}
}

func TestLoadBundle_MissingManifest(t *testing.T) {
	if _, err := shnsdk.LoadBundle(t.TempDir()); err == nil {
		t.Fatal("LoadBundle on empty dir = nil error, want error")
	}
}

// Ensure WriteBundle is usable even with no role or baseURL (keygen bare case).
func TestWriteLoadBundle_EmptyOptionals(t *testing.T) {
	id, err := shnsdk.GenerateIdentity("holder-y")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := shnsdk.WriteBundle(dir, id, "", ""); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	b, err := shnsdk.LoadBundle(dir)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if b.Manifest.ID != "holder-y" {
		t.Fatalf("manifest id = %q", b.Manifest.ID)
	}
}

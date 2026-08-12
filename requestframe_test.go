package shnsdk

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Request frames (spec 2026-08-11 slice 4): requestFrames is the additive registry capability
// that gates REQUEST-payload framing. It defaults ON for SHN builds — auto-stamped
// at registration exactly like MessageFrames.

func TestSupportedRequestFramesDeclaresV1(t *testing.T) {
	got := SupportedRequestFrames()
	if !reflect.DeepEqual(got, []string{RequestFrameV1}) {
		t.Fatalf("SupportedRequestFrames() = %v, want [%s]", got, RequestFrameV1)
	}
}

func TestSupportsRequestFrameV1(t *testing.T) {
	if SupportsRequestFrameV1(nil) {
		t.Fatal("absent declaration must NOT be treated as capable (the request-frame fence)")
	}
	if SupportsRequestFrameV1([]string{"v2"}) {
		t.Fatal("unknown token must not be treated as v1")
	}
	if !SupportsRequestFrameV1([]string{"v1"}) {
		t.Fatal("v1 must be recognized")
	}
}

func TestRegistrationSelfDeclaresRequestFrames(t *testing.T) {
	id, err := GenerateIdentity("h1")
	if err != nil {
		t.Fatal(err)
	}
	req := id.Registration("provider", "https://example.test")
	if !SupportsRequestFrameV1(req.RequestFrames) {
		t.Fatalf("Registration must self-declare requestFrames v1 (MessageFrames precedent); got %v", req.RequestFrames)
	}
	// The field must ride the wire additively, outside the frozen 5-field PoP layout.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["requestFrames"]; !ok {
		t.Fatalf("registration body must carry requestFrames; got %s", raw)
	}
}

// D1a: the registrant's DECLARED contract-version set must be overridable so the
// registry entry carries what this deployment declares, not the build constant.
func TestRegistrationWithDeclaredOverridesContractVersions(t *testing.T) {
	id, err := GenerateIdentity("h1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ContractPAPAS22, ContractPACRD22}
	req := id.RegistrationWithDeclared("provider", "https://example.test", want)
	if !reflect.DeepEqual(req.ContractVersions, want) {
		t.Fatalf("ContractVersions = %v, want %v", req.ContractVersions, want)
	}
	// nil declared ⇒ the build constant (library users keep today's behavior).
	def := id.RegistrationWithDeclared("provider", "https://example.test", nil)
	if !reflect.DeepEqual(def.ContractVersions, SupportedContractVersions()) {
		t.Fatalf("nil declared must fall back to SupportedContractVersions(); got %v", def.ContractVersions)
	}
	// PoP must be unchanged by the override (frozen 5-field signing payload).
	if def.Pop != req.Pop {
		t.Fatal("declared-set override must not change the proof-of-possession signature")
	}
}

func TestParseDeclaredContractVersions(t *testing.T) {
	t.Run("empty is the build default", func(t *testing.T) {
		got, err := ParseDeclaredContractVersions("")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, SupportedContractVersions()) {
			t.Fatalf("got %v, want the build default %v", got, SupportedContractVersions())
		}
	})
	t.Run("valid subset", func(t *testing.T) {
		got, err := ParseDeclaredContractVersions(" pa.pas@2.2 , pa.crd@2.0 ")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []string{ContractPAPAS22, ContractPACRD20}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("junk token rejected", func(t *testing.T) {
		if _, err := ParseDeclaredContractVersions("not-a-token"); err == nil {
			t.Fatal("want error for a malformed token")
		}
	})
	t.Run("non-native token rejected", func(t *testing.T) {
		if _, err := ParseDeclaredContractVersions("pa.pas@9.9"); err == nil {
			t.Fatal("want error for a token outside NativeContractVersions()")
		}
	})
}

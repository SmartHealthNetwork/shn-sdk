package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// payerCDexStatement is the part of a payer CDex base's statement the rows read.
type payerCDexStatement struct {
	ResourceType        string   `json:"resourceType"`
	Kind                string   `json:"kind"`
	FhirVersion         string   `json:"fhirVersion"`
	ImplementationGuide []string `json:"implementationGuide"`
	Rest                []struct {
		Mode          string `json:"mode"`
		Documentation string `json:"documentation"`
		Security      struct {
			Description string `json:"description"`
		} `json:"security"`
		Resource []struct {
			Type             string   `json:"type"`
			SupportedProfile []string `json:"supportedProfile"`
			Interaction      []struct {
				Code string `json:"code"`
			} `json:"interaction"`
			Operation []any `json:"operation"`
		} `json:"resource"`
	} `json:"rest"`
}

func buildPayerCDexStatement(t *testing.T, base PayerCDexBase) (payerCDexStatement, []byte) {
	t.Helper()
	at := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	b, err := BuildPayerCDexIngressCapabilityStatement(at, base)
	if err != nil {
		t.Fatalf("BuildPayerCDexIngressCapabilityStatement(%+v): %v", base, err)
	}
	var cs payerCDexStatement
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, err := BuildPayerCDexIngressCapabilityStatement(at, base)
	if err != nil || !bytes.Equal(b, b2) {
		t.Fatalf("BuildPayerCDexIngressCapabilityStatement(%+v) is not deterministic", base)
	}
	return cs, b
}

// Each base declares exactly the Task interactions it serves: create at
// every base, and read and vread only at a provider's base whose provider
// serves them. Nothing else is declared: no other resource, operation or
// implementationGuide.
func TestBuildPayerCDexIngressCapabilityStatement_Interactions(t *testing.T) {
	for _, row := range []struct {
		base PayerCDexBase
		want []string
		doc  string
	}{
		{PayerCDexBase{}, []string{"create"}, "listed at /cdex/providers"},
		{PayerCDexBase{Provider: true}, []string{"create"}, "does not serve a payer's read"},
		{PayerCDexBase{Provider: true, Reads: true}, []string{"create", "read", "vread"}, "answered 404"},
	} {
		cs, _ := buildPayerCDexStatement(t, row.base)
		if cs.ResourceType != "CapabilityStatement" || cs.Kind != "instance" || cs.FhirVersion != "4.0.1" {
			t.Fatalf("%+v: resourceType/kind/fhirVersion = %q/%q/%q", row.base, cs.ResourceType, cs.Kind, cs.FhirVersion)
		}
		if cs.ImplementationGuide != nil {
			t.Fatalf("%+v: implementationGuide %q; this library pins no CDex package to name one by", row.base, cs.ImplementationGuide)
		}
		if len(cs.Rest) != 1 || cs.Rest[0].Mode != "server" || len(cs.Rest[0].Resource) != 1 {
			t.Fatalf("%+v: rest = %+v, want one server rest with one resource", row.base, cs.Rest)
		}
		res := cs.Rest[0].Resource[0]
		if res.Type != "Task" || !slices.Equal(res.SupportedProfile, []string{cdexTaskAttachmentRequestProfile}) || res.Operation != nil {
			t.Fatalf("%+v: resource = %+v", row.base, res)
		}
		var got []string
		for _, i := range res.Interaction {
			got = append(got, i.Code)
		}
		if !slices.Equal(got, row.want) {
			t.Fatalf("%+v: interactions %q, want %q", row.base, got, row.want)
		}
		if !strings.Contains(cs.Rest[0].Documentation, row.doc) {
			t.Fatalf("%+v: documentation %q does not say %q", row.base, cs.Rest[0].Documentation, row.doc)
		}
		sec := cs.Rest[0].Security.Description
		if reads := strings.Contains(sec, "system/Task.read"); reads != row.base.Reads || !strings.Contains(sec, "system/Task.write") {
			t.Fatalf("%+v: security %q", row.base, sec)
		}
	}
}

// The canonical is the CDex Task Attachment Request profile's, pinned here
// so a typo cannot pass as a profile the validator does not hold.
func TestBuildPayerCDexIngressCapabilityStatement_Profile(t *testing.T) {
	if cdexTaskAttachmentRequestProfile != "http://hl7.org/fhir/us/davinci-cdex/StructureDefinition/cdex-task-attachment-request" {
		t.Fatalf("profile %q", cdexTaskAttachmentRequestProfile)
	}
}

// The plain base serves no read, so a statement for reads there is refused,
// and nothing is built.
func TestBuildPayerCDexIngressCapabilityStatement_RefusesReadsOnThePlainBase(t *testing.T) {
	b, err := BuildPayerCDexIngressCapabilityStatement(time.Unix(0, 0), PayerCDexBase{Reads: true})
	if !errors.Is(err, ErrPayerCDexBase) || b != nil {
		t.Fatalf("plain base with reads: %q, %v; want ErrPayerCDexBase and nothing built", b, err)
	}
}

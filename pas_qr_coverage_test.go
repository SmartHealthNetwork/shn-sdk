package shnsdk

import (
	"encoding/json"
	"testing"
)

func TestConformantPAS22OwnsQRCoverageReference(t *testing.T) {
	in := conformantSubmitInputs(t)
	qr, err := FillQuestionnaireAtLine("2.2", demoLumbarQuestionnaire(), DemoLumbarContext(), QRContext{
		PatientRef: in.PatientRef, CoverageRef: "Coverage/MBR-COVERED", OrderRef: "ServiceRequest/old-order", Authored: in.Created,
	})
	if err != nil {
		t.Fatal(err)
	}
	in.QR = qr
	update := conformantUpdateInputsFromGolden(t)
	update.QR = qr
	for name, build := range map[string]func() ([]byte, error){
		"submit": func() ([]byte, error) { return BuildConformantClaimBundleAtLine("2.2", in) },
		"update": func() ([]byte, error) { return BuildConformantClaimUpdateBundleAtLine("2.2", update) },
	} {
		t.Run(name, func(t *testing.T) {
			data, err := build()
			if err != nil {
				t.Fatal(err)
			}
			coverage := coverageEntryFromBundle(t, data)
			var bundle struct {
				Entry []struct {
					Resource struct {
						ResourceType string `json:"resourceType"`
						Extension    []struct {
							URL            string `json:"url"`
							ValueReference struct {
								Reference string `json:"reference"`
							} `json:"valueReference"`
						} `json:"extension"`
					} `json:"resource"`
				} `json:"entry"`
			}
			if err := json.Unmarshal(data, &bundle); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range bundle.Entry {
				if e.Resource.ResourceType == "QuestionnaireResponse" {
					for _, ext := range e.Resource.Extension {
						if ext.URL == qrCoverageExt {
							found = true
							if ext.ValueReference.Reference != "Coverage/"+coverage.ID {
								t.Errorf("QR coverage %q does not resolve to builder Coverage/%s", ext.ValueReference.Reference, coverage.ID)
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("missing DTR 2.2 qr-coverage extension")
			}
		})
	}
}

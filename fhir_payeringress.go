package shnsdk

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// cdexTaskAttachmentRequestProfile is the CDex Task Attachment Request
// profile, unversioned: the payer's ingress takes the Task its system creates
// at a provider as that profile states it.
const cdexTaskAttachmentRequestProfile = "http://hl7.org/fhir/us/davinci-cdex/StructureDefinition/cdex-task-attachment-request"

// PayerCDexBase says which base of a payer gateway's Da Vinci ingress a
// CapabilityStatement describes.
type PayerCDexBase struct {
	// Provider is true for the base of one provider
	// ("/cdex/provider/<provider>", listed at "/cdex/providers"), where a
	// Task is created for that provider and read; false for the plain base,
	// where a Task is created for the provider its owner names.
	Provider bool
	// Reads is true when that provider serves a payer's read and vread of the
	// Task it created there. Only a provider's base serves reads, so it is an
	// error on the plain base.
	Reads bool
}

// ErrPayerCDexBase is a PayerCDexBase that names no base: reads on the plain
// base.
var ErrPayerCDexBase = errors.New("shnsdk: the plain CDex base serves no Task read")

// BuildPayerCDexIngressCapabilityStatement returns the CapabilityStatement of
// one base of a payer gateway's Da Vinci ingress, served at that base's
// GET /metadata: FHIR R4's capabilities interaction, which every server
// supports.
//
// Every base declares Task create, of the CDex Task Attachment Request
// profile. A provider's base also declares Task read and vread when base.Reads
// says that provider serves them, and the plain base never does: a Task is
// read at the base of the provider it was created for. The statement names
// the profile it takes, unversioned, and no implementationGuide: this library
// holds no pinned CDex package version to name one by, and never makes one
// up.
func BuildPayerCDexIngressCapabilityStatement(created time.Time, base PayerCDexBase) ([]byte, error) {
	if base.Reads && !base.Provider {
		return nil, ErrPayerCDexBase
	}
	doc := "Da Vinci CDex ingress of a payer's Smart Gateway. Create a CDex Task Attachment Request here (POST [base]/Task): it is carried to the provider its Task.owner names. A Task is read at the base of the provider it was created for, listed at /cdex/providers, where that provider serves reads."
	sec := "SMART Backend Services (client_credentials, private_key_jwt); configuration at [base]/.well-known/smart-configuration. A create needs a system/Task scope that permits it, such as system/Task.write, system/Task.*, or a SMART v2 scope with c."
	interactions := []fhir.CapabilityStatementRestResourceInteraction{{Code: fhir.TypeRestfulInteractionCreate}}
	if base.Provider {
		doc = "Da Vinci CDex ingress of a payer's Smart Gateway, at the base of one provider. Create a CDex Task Attachment Request here (POST [base]/Task): it is carried to this provider."
		if base.Reads {
			doc += " Read (GET [base]/Task/[id]) and vread (GET [base]/Task/[id]/_history/[vid]) a Task created for this provider: the provider's answer is delivered only for a Task the network carried for this payer, and any other is answered 404."
			sec += " A read or vread needs one that permits reading, such as system/Task.read, system/Task.*, or a SMART v2 scope with r."
			interactions = append(interactions,
				fhir.CapabilityStatementRestResourceInteraction{Code: fhir.TypeRestfulInteractionRead},
				fhir.CapabilityStatementRestResourceInteraction{Code: fhir.TypeRestfulInteractionVread},
			)
		} else {
			doc += " This provider does not serve a payer's read of its Task through the network."
		}
	}
	cs := fhir.CapabilityStatement{
		Status:      fhir.PublicationStatusActive,
		Date:        created.UTC().Format(time.RFC3339),
		Kind:        fhir.CapabilityStatementKindInstance,
		FhirVersion: fhir.FHIRVersion4_0_1,
		Format:      []string{"json"},
		Software:    &fhir.CapabilityStatementSoftware{Name: "SHN Payer Da Vinci Ingress"},
		Implementation: &fhir.CapabilityStatementImplementation{
			Description: "SHN payer gateway Da Vinci ingress (CDex Task Attachment Request).",
		},
		Rest: []fhir.CapabilityStatementRest{{
			Mode:          fhir.RestfulCapabilityModeServer,
			Documentation: &doc,
			Security:      &fhir.CapabilityStatementRestSecurity{Description: &sec},
			Resource: []fhir.CapabilityStatementRestResource{{
				Type:             fhir.ResourceTypeTask,
				SupportedProfile: []string{cdexTaskAttachmentRequestProfile},
				Interaction:      interactions,
			}},
		}},
	}
	return json.Marshal(cs) // fhir.CapabilityStatement.MarshalJSON injects resourceType
}

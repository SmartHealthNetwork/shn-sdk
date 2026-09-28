package shnsdk

import "fmt"

// OrderDispatchInputs are the inputs of the deprecated
// BuildConformantOrderDispatchRequest. They name the patient by id and carry
// no Patient record, so from shn-sdk v0.59.0 that builder refuses them; build
// an order-dispatch request with BuildCRDRequest (CRDRequestInputs) from your
// own Patient instead.
type OrderDispatchInputs struct {
	// PatientID is the bare patient id (without the "Patient/" prefix).
	PatientID string
	// PatientRef is the full patient reference ("Patient/<id>").
	PatientRef string
	// OrderRef is the full DeviceRequest reference ("DeviceRequest/<id>").
	OrderRef string
	// PerformerRef is the full Organization reference ("Organization/<id>")
	// of the supplier the order is dispatched to.
	PerformerRef string
	// DeviceRequest is the raw FHIR DeviceRequest JSON.
	DeviceRequest []byte
	// Supplier is the raw FHIR Organization JSON for the supplying DME Organization.
	Supplier []byte
	// Coverage is the raw FHIR Coverage JSON.
	Coverage []byte
	// Payer is the payer Organization identifier (system|value).
	Payer PayerIdentifier
}

// BuildConformantOrderDispatchRequest builds nothing and returns an error.
//
// A CRD order-dispatch request carries the participant's own Patient record as
// its patient prefetch. OrderDispatchInputs names the patient by id and holds
// no Patient, so the only request this builder could build would carry a
// Patient it made up (an id-only stub), a Coverage with its payor rewritten
// and a payer Organization it also made up. From shn-sdk v0.59.0 it refuses
// instead, whatever its inputs. Earlier releases sent that request.
//
// Deprecated: use BuildCRDRequest with the order-dispatch hook, which takes
// the participant's own Patient and its prefetch values exactly as held. From
// shn-sdk v0.59.0 this builder returns an error naming BuildCRDRequest; a
// later release removes it.
func BuildConformantOrderDispatchRequest(in OrderDispatchInputs) ([]byte, error) {
	return nil, fmt.Errorf("shnsdk: BuildConformantOrderDispatchRequest: %w", errCRDRequestNeedsPatient)
}

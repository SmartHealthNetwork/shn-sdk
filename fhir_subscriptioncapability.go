package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// The Subscriptions R5 Backport IG's canonicals (hl7.fhir.uv.subscriptions-backport,
// STU 1.1) that a statement declaring Subscription names.
const (
	// backportSubscriptionTopicCanonicalExt is the extension whose context is
	// CapabilityStatement.rest.resource, with a canonical value, naming one
	// SubscriptionTopic a server supports (StructureDefinition
	// capabilitystatement-subscriptiontopic-canonical).
	backportSubscriptionTopicCanonicalExt = "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/capabilitystatement-subscriptiontopic-canonical"
	// backportSubscriptionStatusOp is the $status operation on Subscription
	// (OperationDefinition backport-subscription-status).
	backportSubscriptionStatusOp = "http://hl7.org/fhir/uv/subscriptions-backport/OperationDefinition/backport-subscription-status"
)

// CapabilitySearchParam is one search parameter a CapabilityStatement
// declares for a resource: its name, the canonical of the SearchParameter
// that defines it, and its type, a FHIR R4 search parameter type code
// ("number", "date", "string", "token", "reference", "composite",
// "quantity", "uri" or "special").
type CapabilitySearchParam struct{ Name, Definition, Type string }

// SubscriptionCapability is what a FHIR base serves of a topic Subscription,
// for WithSubscription to declare.
type SubscriptionCapability struct {
	// SearchParams are exactly the search parameters the base serves for a
	// Subscription search, in the order declared: for a subscribe leg, url
	// (uri), status (token) and _id (token). Result and paging parameters are
	// not search parameters and are not listed.
	SearchParams []CapabilitySearchParam
	// StatusOperation declares the instance $status operation.
	StatusOperation bool
	// TopicCanonicals are the SubscriptionTopic canonicals the base declares,
	// each as one subscriptiontopic-canonical extension; empty for a topic
	// that needs no advertising, such as PAS's.
	TopicCanonicals []string
}

// ErrSubscriptionCapability is a statement WithSubscription does not add to,
// or a SubscriptionCapability it cannot declare.
var ErrSubscriptionCapability = errors.New("shnsdk: cannot declare Subscription")

// WithSubscription returns stmt with one rest[mode=server].resource entry for
// Subscription, after the resources stmt declares: the interactions create,
// read, vread, update, delete and search-type, exactly c.SearchParams, the
// instance $status operation (named "status", the Backport IG's
// backport-subscription-status OperationDefinition) when c.StatusOperation is
// set, and one subscriptiontopic-canonical extension per c.TopicCanonicals
// entry, on that resource entry, which is the extension's context in the
// Subscriptions R5 Backport IG (STU 1.1). Everything else in stmt is kept as
// it is, and the output is marshalled as the statement builders marshal
// theirs, so a statement from BuildProviderIngressCapabilityStatement or
// BuildPayerCDexIngressCapabilityStatement is kept byte for byte around the
// new entry.
//
// It refuses, as ErrSubscriptionCapability, a statement that is not a
// CapabilityStatement, that this library would not marshal back byte for byte
// (so nothing in it would be lost), that has no rest of mode server or more
// than one, or that already declares Subscription there; and a capability
// with no search parameter, a search parameter without a name, named twice,
// without an absolute definition or of a type that is not a search parameter
// type, or a topic canonical that is empty, not absolute or named twice.
//
// The subscriptiontopic-canonical extension and the $status OperationDefinition
// canonical are certified by this library's unit tests only, until the
// Backport IG is in the FHIR validator's package closure.
func WithSubscription(stmt []byte, c SubscriptionCapability) ([]byte, error) {
	params, err := subscriptionSearchParamsOf(c.SearchParams)
	if err != nil {
		return nil, err
	}
	var exts []fhir.Extension
	seenTopic := map[string]bool{}
	for _, topic := range c.TopicCanonicals {
		if !absoluteURI(topic) || seenTopic[topic] {
			return nil, fmt.Errorf("%w: topic canonical %q is not absolute, or is named twice", ErrSubscriptionCapability, topic)
		}
		seenTopic[topic] = true
		exts = append(exts, fhir.Extension{Url: backportSubscriptionTopicCanonicalExt, ValueCanonical: &topic})
	}
	var head struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(stmt, &head); err != nil || head.ResourceType != "CapabilityStatement" {
		return nil, fmt.Errorf("%w: not a CapabilityStatement", ErrSubscriptionCapability)
	}
	cs, err := fhir.UnmarshalCapabilityStatement(stmt)
	if err != nil {
		return nil, fmt.Errorf("%w: the statement cannot be read: %v", ErrSubscriptionCapability, err)
	}
	if again, err := json.Marshal(cs); err != nil || !bytes.Equal(again, stmt) {
		return nil, fmt.Errorf("%w: the statement holds what this library would not write back as given", ErrSubscriptionCapability)
	}
	server := -1
	for i, rest := range cs.Rest {
		if rest.Mode != fhir.RestfulCapabilityModeServer {
			continue
		}
		if server >= 0 {
			return nil, fmt.Errorf("%w: the statement has more than one rest of mode server", ErrSubscriptionCapability)
		}
		server = i
	}
	if server < 0 {
		return nil, fmt.Errorf("%w: the statement has no rest of mode server", ErrSubscriptionCapability)
	}
	for _, r := range cs.Rest[server].Resource {
		if r.Type == fhir.ResourceTypeSubscription {
			return nil, fmt.Errorf("%w: the statement already declares Subscription", ErrSubscriptionCapability)
		}
	}
	entry := fhir.CapabilityStatementRestResource{
		Extension:   exts,
		Type:        fhir.ResourceTypeSubscription,
		SearchParam: params,
	}
	for _, code := range []fhir.TypeRestfulInteraction{
		fhir.TypeRestfulInteractionCreate, fhir.TypeRestfulInteractionRead, fhir.TypeRestfulInteractionVread,
		fhir.TypeRestfulInteractionUpdate, fhir.TypeRestfulInteractionDelete, fhir.TypeRestfulInteractionSearchType,
	} {
		entry.Interaction = append(entry.Interaction, fhir.CapabilityStatementRestResourceInteraction{Code: code})
	}
	if c.StatusOperation {
		// R4 names an operation here without its "$".
		entry.Operation = []fhir.CapabilityStatementRestResourceOperation{{Name: "status", Definition: backportSubscriptionStatusOp}}
	}
	cs.Rest[server].Resource = append(cs.Rest[server].Resource, entry)
	return json.Marshal(cs) // fhir.CapabilityStatement.MarshalJSON injects resourceType
}

// subscriptionSearchParamsOf checks ps (WithSubscription) and returns them as
// the statement's search parameters.
func subscriptionSearchParamsOf(ps []CapabilitySearchParam) ([]fhir.CapabilityStatementRestResourceSearchParam, error) {
	if len(ps) == 0 {
		return nil, fmt.Errorf("%w: a search-type interaction names its search parameters", ErrSubscriptionCapability)
	}
	var out []fhir.CapabilityStatementRestResourceSearchParam
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name == "" || seen[p.Name] {
			return nil, fmt.Errorf("%w: search parameter %q has no name, or is named twice", ErrSubscriptionCapability, p.Name)
		}
		if !absoluteURI(p.Definition) {
			return nil, fmt.Errorf("%w: search parameter %q has no absolute definition", ErrSubscriptionCapability, p.Name)
		}
		typ, ok := searchParamTypes[p.Type]
		if !ok {
			return nil, fmt.Errorf("%w: search parameter %q has type %q, not a search parameter type", ErrSubscriptionCapability, p.Name, p.Type)
		}
		seen[p.Name] = true
		def := p.Definition
		out = append(out, fhir.CapabilityStatementRestResourceSearchParam{Name: p.Name, Definition: &def, Type: typ})
	}
	return out, nil
}

// searchParamTypes are FHIR R4's search parameter type codes.
var searchParamTypes = map[string]fhir.SearchParamType{
	"number": fhir.SearchParamTypeNumber, "date": fhir.SearchParamTypeDate, "string": fhir.SearchParamTypeString,
	"token": fhir.SearchParamTypeToken, "reference": fhir.SearchParamTypeReference, "composite": fhir.SearchParamTypeComposite,
	"quantity": fhir.SearchParamTypeQuantity, "uri": fhir.SearchParamTypeUri, "special": fhir.SearchParamTypeSpecial,
}

// absoluteURI reports whether s is an absolute URI with a scheme and nothing
// a canonical never holds (a space or a control character).
func absoluteURI(s string) bool {
	if !carriesAsQuery(s) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.IsAbs() && (u.Host != "" || u.Opaque != "")
}

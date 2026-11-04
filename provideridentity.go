package shnsdk

// ProviderIdentifier is one organization identifier (system|value) a provider
// is operator-attested for (Holder.ProviderIDs). It has the shape of a
// PayerIdentifier, as a separate type so a provider's identifier is never read
// as a payer's.
type ProviderIdentifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

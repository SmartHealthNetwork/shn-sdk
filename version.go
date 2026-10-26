package shnsdk

// WireProtocolVersion is the SHN wire-protocol version this SDK declares; nothing
// it sends carries it. It is one of WireProtocolVersions. The SHN network built
// alongside this SDK advertises one of WireProtocolVersions too. shn doctor and
// shn priorauth refuse a network that neither speaks nor accepts a version this
// SDK speaks (Discovery.SharesWireProtocol).
//
// The SDK declares 2.0.0 and still speaks 1.1.0 beside it, for the networks
// that move from one to the other. It names each leg it sends by its
// workstream-prefixed name, the name 2.0.0 uses; a network's Hub reads it once
// the Hub reads either name (PARTICIPANT_PROTOCOL.md §4.3); an older Hub
// refuses it as an unknown transaction type, and the version check above
// cannot tell the two apart. It sends no
// involvement value (a gateway does). It reads a 2.0.0 descriptor, which leaves
// out endpoints.patientAccess, contractVersions and demoResponders: its CLI
// finds the test payer by the persona's payerId, and resumes by the payerId
// its handle carries; a persona or a handle without one falls back to
// demoResponders, so against a 2.0.0 descriptor it refuses and says why.
const WireProtocolVersion = "2.0.0"

// WireProtocolVersions returns the wire protocol versions this SDK speaks. A
// network's descriptor states the version it speaks (WireProtocolVersion) and
// the versions it accepts (WireProtocolVersionsAccepted); each is one of these.
func WireProtocolVersions() []string { return []string{"1.1.0", WireProtocolVersion} }

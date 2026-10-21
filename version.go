package shnsdk

// WireProtocolVersion is the SHN wire-protocol version this SDK sends; it is one
// of WireProtocolVersions. The SHN network built alongside this SDK advertises
// one of WireProtocolVersions too. shn doctor refuses a network advertising a
// version this SDK does not support.
const WireProtocolVersion = "1.1.0"

// WireProtocolVersions returns the wire protocol versions this SDK speaks. A
// network's descriptor states the version it speaks (WireProtocolVersion) and
// the versions it accepts (WireProtocolVersionsAccepted); each is one of these.
func WireProtocolVersions() []string { return []string{WireProtocolVersion} }

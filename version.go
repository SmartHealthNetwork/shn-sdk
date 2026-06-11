package shnsdk

// WireProtocolVersion is the SHN wire-protocol version this SDK speaks. It MUST equal
// the substrate's internal/wire.ProtocolVersion (enforced by test/sdkparity). shn
// doctor refuses a sandbox advertising a version this SDK does not support.
const WireProtocolVersion = "1.1.0"

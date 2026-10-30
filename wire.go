package shnsdk

import "encoding/json"

// MaxRequestBytes and MaxResponseBytes cap inbound/outbound HTTP body sizes (DoS
// guard). They match the network's own request/response limits (8 MiB) so an
// SDK participant applies the same limits the network does.
const (
	MaxRequestBytes  = 8 << 20 // 8 MiB
	MaxResponseBytes = 8 << 20 // 8 MiB
)

// EncodeEnvelope marshals an Envelope to its JSON wire form. The Ciphertext
// []byte field is automatically base64-encoded by encoding/json. Ported from
// the network's own envelope encoder (plain json.Marshal) so the wire bytes match.
func EncodeEnvelope(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

// DecodeEnvelope unmarshals JSON wire bytes into an Envelope. Ported from
// the network's own envelope decoder.
func DecodeEnvelope(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

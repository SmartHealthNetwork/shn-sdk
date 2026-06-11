package shnsdk

import "encoding/json"

// MaxRequestBytes and MaxResponseBytes cap inbound/outbound HTTP body sizes (DoS
// guard). They match internal/wire.MaxRequestBytes/MaxResponseBytes (8 MiB) so an
// SDK participant applies the same limits the substrate does.
const (
	MaxRequestBytes  = 8 << 20 // 8 MiB
	MaxResponseBytes = 8 << 20 // 8 MiB
)

// EncodeEnvelope marshals an Envelope to its JSON wire form. The Ciphertext
// []byte field is automatically base64-encoded by encoding/json. Ported from
// internal/wire.EncodeEnvelope (plain json.Marshal) so the wire bytes match.
func EncodeEnvelope(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

// DecodeEnvelope unmarshals JSON wire bytes into an Envelope. Ported from
// internal/wire.DecodeEnvelope.
func DecodeEnvelope(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

package shnsdk

// MaxAttachmentBytes is the network's ceiling on the body of one
// documentation attachment submission (the cdex-attachment-submit leg, CDex
// $submit-attachment): no gateway's setting for the leg may be above it. It
// is the bound the gateways' strict JSON scan reads a body to.
const MaxAttachmentBytes = 16 << 20 // 16 MiB

// MaxAttachmentRequestBytes caps the sealed envelope that carries an
// attachment submission: a body at MaxAttachmentBytes, with its request frame
// and seal, base64 in the envelope (about 4/3 of it), plus room for the
// envelope's metadata.
const MaxAttachmentRequestBytes = 22 << 20 // 22 MiB

// requestCaps are the legs whose request envelope has a cap of its own, by
// the leg's flat transaction type; every other leg's is MaxRequestBytes.
var requestCaps = map[string]int{
	"cdex-attachment-submit": MaxAttachmentRequestBytes,
}

// MaxRequestBytesFor returns the request envelope cap of the leg named by
// txType, in either form of its name: its own cap when it has one, and
// MaxRequestBytes for every other leg and for a name no leg has.
func MaxRequestBytesFor(txType string) int {
	if flat, ok := FlatTransactionType(txType); ok {
		if n, ok := requestCaps[flat]; ok {
			return n
		}
	}
	return MaxRequestBytes
}

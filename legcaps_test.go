package shnsdk

import (
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

// Every leg of the taxonomy has a request cap: the global MaxRequestBytes, but
// for the legs with their own (requestCaps), in either form of the leg's name.
// A name no leg has gets the global cap, never a larger one.
func TestMaxRequestBytesFor(t *testing.T) {
	legs := PrefixedTransactionTypes()
	if len(legs) == 0 {
		t.Fatal("no legs in the taxonomy table")
	}
	for flat, prefixed := range legs {
		want := MaxRequestBytes
		if flat == "cdex-attachment-submit" {
			want = MaxAttachmentRequestBytes
		}
		if got := MaxRequestBytesFor(flat); got != want {
			t.Errorf("%s: cap %d, want %d", flat, got, want)
		}
		if got := MaxRequestBytesFor(prefixed); got != want {
			t.Errorf("%s: cap %d, want %d", prefixed, got, want)
		}
	}
	for leg := range requestCaps {
		if _, ok := FlatTransactionType(leg); !ok {
			t.Errorf("requestCaps names %q, which is no leg of the taxonomy", leg)
		}
	}
	// Only the leg's own name earns its cap: another workstream's prefix or
	// another case is no name of the leg.
	for _, name := range []string{"", "no-such-leg", "pa.no-such-leg", "fqs.cdex-attachment-submit", "CDEX-ATTACHMENT-SUBMIT"} {
		if got := MaxRequestBytesFor(name); got != MaxRequestBytes {
			t.Errorf("%q: cap %d, want the global %d", name, got, MaxRequestBytes)
		}
	}
}

// The attachment leg's envelope cap carries a body at the attachment ceiling:
// the body, its request frame and the seal, base64 in the envelope, plus the
// envelope's metadata (its token and the involved list). And it is no larger
// than that needs, so the leg does not admit far more than the ceiling.
func TestMaxAttachmentRequestBytesCarriesTheCeiling(t *testing.T) {
	// The frame's fixed prefix and its largest header, and the seal.
	const frameAndSeal = 6 + maxFrameHeaderBytes + box.AnonymousOverhead
	const metadata = 128 << 10 // the token and up to 16 involved patients' tokens
	sealed := base64.StdEncoding.EncodedLen(MaxAttachmentBytes + frameAndSeal)
	if MaxAttachmentRequestBytes < sealed+metadata {
		t.Fatalf("envelope cap %d, under the %d a body at the ceiling seals to with its metadata", MaxAttachmentRequestBytes, sealed+metadata)
	}
	if MaxAttachmentRequestBytes > sealed+(1<<20) {
		t.Fatalf("envelope cap %d, more than 1 MiB over the %d a body at the ceiling needs", MaxAttachmentRequestBytes, sealed)
	}
	if MaxAttachmentBytes != 16<<20 {
		t.Fatalf("attachment ceiling %d, want 16 MiB (the relay scan's bound)", MaxAttachmentBytes)
	}
	if MaxAttachmentRequestBytes <= MaxRequestBytes {
		t.Fatalf("attachment envelope cap %d is not above the global %d", MaxAttachmentRequestBytes, MaxRequestBytes)
	}
}

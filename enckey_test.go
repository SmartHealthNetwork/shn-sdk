package shnsdk

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// lowOrderEncodings are the X25519 public keys of small order libsodium's blocklist
// names, each with its unused top bit clear and set: u=0, u=1, u=p-1, the two points
// of order 8, and the non-canonical p and p+1.
func lowOrderEncodings(t *testing.T) map[string]*[32]byte {
	t.Helper()
	base := map[string]string{
		"zero":        "0000000000000000000000000000000000000000000000000000000000000000",
		"one":         "0100000000000000000000000000000000000000000000000000000000000000",
		"p-1":         "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
		"order 8 (a)": "e0eb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b800",
		"order 8 (b)": "5f9c95bca3508c24b1d0b1559c83ef5b04445cc4581c8e86d8224eddd09f1157",
		"p":           "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
		"p+1":         "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	}
	out := map[string]*[32]byte{}
	for name, h := range base {
		for _, top := range []bool{false, true} {
			b, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			if top {
				b[31] |= 0x80
				name += ", top bit set"
			}
			var k [32]byte
			copy(k[:], b)
			out[name] = &k
		}
	}
	return out
}

// REJECTION: Seal refuses a recipient key of small order, in every encoding, and a nil
// one, before sealing anything; a valid key still seals and opens.
func TestSeal_RefusesALowOrderRecipient(t *testing.T) {
	for name, k := range lowOrderEncodings(t) {
		if env, err := Seal(Metadata{}, []byte("payload"), k); !errors.Is(err, ErrLowOrderKey) || env.Ciphertext != nil {
			t.Errorf("%s: %v, %d bytes sealed; want ErrLowOrderKey and nothing sealed", name, err, len(env.Ciphertext))
		}
	}
	if _, err := Seal(Metadata{}, []byte("payload"), nil); !errors.Is(err, ErrLowOrderKey) {
		t.Errorf("a nil key: %v, want ErrLowOrderKey", err)
	}
	id, err := GenerateIdentity("h")
	if err != nil {
		t.Fatal(err)
	}
	env, err := Seal(Metadata{}, []byte("payload"), id.EncPub)
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := Open(env, id.EncPub, id.EncPriv); err != nil || string(pt) != "payload" {
		t.Fatalf("round trip: %q, %v", pt, err)
	}
}

// LoadBundle's Identity.EncPub is always the key enc.key derives, never manifest.json's;
// EncKeyCheck names how the manifest's compares, with public fingerprints only.
func TestLoadBundle_EncPubIsTheDerivedKey(t *testing.T) {
	id, err := GenerateIdentity("h")
	if err != nil {
		t.Fatal(err)
	}
	other, err := GenerateIdentity("other")
	if err != nil {
		t.Fatal(err)
	}
	derivedFp := KeyFingerprint(id.EncPub[:])
	b64 := func(k *[32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }
	zero := [32]byte{}
	type encKeyRow struct {
		manifestEncPub *string // nil: the field absent
		verdict        EncKeyVerdict
		manifestFp     string
	}
	rows := map[string]encKeyRow{
		"the manifest holds the derived key": {ptr(b64(id.EncPub)), EncKeyOK, derivedFp},
		"no encPub in the manifest":          {nil, EncKeyManifestMissing, ""},
		"an empty encPub":                    {ptr(""), EncKeyManifestMissing, ""},
		"an encPub that is not base64":       {ptr("not-base64!!"), EncKeyManifestMalformed, ""},
		"an encPub of the wrong length":      {ptr(base64.StdEncoding.EncodeToString(make([]byte, 16))), EncKeyManifestMalformed, ""},
		"an all-zero encPub":                 {ptr(b64(&zero)), EncKeyManifestLowOrder, KeyFingerprint(zero[:])},
		"another key's encPub":               {ptr(b64(other.EncPub)), EncKeyManifestDiffers, KeyFingerprint(other.EncPub[:])},
	}
	// Every encoding of every point of small order, not only the all-zero key.
	for name, k := range lowOrderEncodings(t) {
		rows["a low-order encPub: "+name] = encKeyRow{ptr(b64(k)), EncKeyManifestLowOrder, KeyFingerprint(k[:])}
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteBundle(dir, id, "provider", "https://p.example"); err != nil {
				t.Fatal(err)
			}
			setManifestEncPub(t, dir, row.manifestEncPub)
			b, err := LoadBundle(dir)
			if err != nil {
				t.Fatal(err)
			}
			if b.Identity.EncPub == nil || *b.Identity.EncPub != *id.EncPub {
				t.Fatalf("Identity.EncPub is not the key enc.key derives")
			}
			got := b.EncKeyCheck()
			if got.Verdict != row.verdict || got.Derived != derivedFp || got.Manifest != row.manifestFp {
				t.Fatalf("EncKeyCheck = %+v, want %s derived=%s manifest=%q", got, row.verdict, derivedFp, row.manifestFp)
			}
			// The identity opens what is sealed to its own key, whatever the manifest says.
			env, err := Seal(Metadata{}, []byte("payload"), b.Identity.EncPub)
			if err != nil {
				t.Fatal(err)
			}
			if pt, err := Open(env, b.Identity.EncPub, b.Identity.EncPriv); err != nil || string(pt) != "payload" {
				t.Fatalf("round trip: %q, %v", pt, err)
			}
		})
	}
	// enc.key itself still decides: missing or malformed, the bundle does not load.
	for name, contents := range map[string]*string{"enc.key missing": nil, "enc.key malformed": ptr("bm90IGEga2V5")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteBundle(dir, id, "provider", "https://p.example"); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, bundleEncKeyFile)
			if contents == nil {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, []byte(*contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadBundle(dir); err == nil {
				t.Fatal("loaded a bundle with no usable enc.key")
			}
		})
	}
}

func ptr(s string) *string { return &s }

// setManifestEncPub rewrites dir's manifest.json with encPub set to *v, or removed
// when v is nil.
func setManifestEncPub(t *testing.T, dir string, v *string) {
	t.Helper()
	p := filepath.Join(dir, bundleManifestFile)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		delete(m, "encPub")
	} else {
		m["encPub"] = *v
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// KeyFingerprint is the registrar's and the Accounts service's encPubFp shape: the first
// 8 hex characters of the key's SHA-256. A fixed vector pins it.
func TestKeyFingerprint_IsTheRegistrarShape(t *testing.T) {
	if got := KeyFingerprint(make([]byte, 32)); got != "66687aad" {
		t.Fatalf("KeyFingerprint(32 zero bytes) = %q, want 66687aad", got)
	}
}

package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// auditVectors lists the committed audit record vectors (testdata/vectors/audit/,
// see the vectors README). Each names its record, its canonical signable bytes as
// hex, and a signature under audit_signer_pub.b64.
var auditVectors = []string{
	"v1-exchange.json",
	"v1-exchange-recordid.json",
	"v1-exchange-involvement.json",
	"v2-exchange.json",
	"v2-exchange-resend.json",
	"v2-authz-decision.json",
	"v2-conformance-finding.json",
	"v2-probe.json",
	"v2-exchange-involvement.json",
	"v2-module-details-authored.json",
	"v2-module-details-bridged.json",
	"v2-gateway-start.json",
	"v2-refused-builder-on-other-edit.json",
	"v3-exchange-involved-complete.json",
	"v3-exchange-involved-incomplete.json",
	"v3-exchange-involved-unknown.json",
	"v3-refused-omitted-on-complete.json",
}

// refusedVectorName matches a refused vector's name, at any record version.
var refusedVectorName = regexp.MustCompile(`^v[0-9]+-refused-`)

type auditVector struct {
	Record    json.RawMessage `json:"record"`
	Signable  string          `json:"signable"`
	Signature string          `json:"signature"`
	// Refused names the member rule the Audit Plane refuses the record
	// under, although its signature verifies; "" for an accepted record.
	Refused string `json:"refused"`
}

// TestVectorAuditRecordSignableContent: decoding each vector's record and
// canonicalising it with SignableContent reproduces the vector's signable bytes,
// and the vector's signature verifies over them under the fixed test key. A drift
// in the canonical form of any record version fails here.
func TestVectorAuditRecordSignableContent(t *testing.T) {
	dir := filepath.Join(vectorsDir(t), "audit")
	pub := ed25519.PublicKey(readB64Vector(t, dir, "audit_signer_pub.b64"))
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(auditVectors) {
		t.Fatalf("%d vector files under audit/, %d listed in auditVectors: list every vector", len(files), len(auditVectors))
	}
	for _, name := range auditVectors {
		t.Run(name, func(t *testing.T) {
			var v auditVector
			if err := json.Unmarshal(readVector(t, dir, name), &v); err != nil {
				t.Fatalf("decode vector: %v", err)
			}
			if refused := refusedVectorName.MatchString(name); refused != (v.Refused != "") {
				t.Fatalf("refused member %q on a vector whose name says refused=%v", v.Refused, refused)
			}
			var rec AuditRecord
			if err := json.Unmarshal(v.Record, &rec); err != nil {
				t.Fatalf("decode record: %v", err)
			}
			// A version 3 vector's involved member is refused exactly when
			// the vector says so: its one member rule is ValidInvolved's.
			if strings.HasPrefix(name, "v3-") {
				if rec.Version != AuditRecordVersion3 || rec.Involved == nil {
					t.Fatalf("a version 3 vector with version %d, involved %v", rec.Version, rec.Involved)
				}
				if ValidInvolved(rec.Involved) != (v.Refused == "") {
					t.Fatalf("ValidInvolved = %v on a vector whose refused member is %q", ValidInvolved(rec.Involved), v.Refused)
				}
			}
			want, err := hex.DecodeString(v.Signable)
			if err != nil {
				t.Fatalf("decode signable hex: %v", err)
			}
			got := SignableContent(rec)
			if string(got) != string(want) {
				t.Fatalf("SignableContent drifted:\n got %s\nwant %s", got, want)
			}
			sig, err := base64.StdEncoding.DecodeString(v.Signature)
			if err != nil {
				t.Fatalf("decode signature: %v", err)
			}
			if !ed25519.Verify(pub, got, sig) {
				t.Fatal("vector signature does not verify over the signable bytes")
			}
			// A changed member changes the signed bytes.
			rec.Sender += "-x"
			if ed25519.Verify(pub, SignableContent(rec), sig) {
				t.Fatal("signature verified a record with a changed sender")
			}
		})
	}
}

package shnsdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// exactBuilderPins are the SHA-256 of each case's bytes as shn-sdk v1.8.0
// built them, taken from the builders before the Carried builders were added.
var exactBuilderPins = map[string]string{
	"provenance":                          "2d59442764edd4d5e13ca30574cbec67cb2a72c2553bda59ad64a7fc375aa86d",
	"provenance-identifier":               "6f40eb4ed0ce58825dc61336a9899e254355f7dd510ec241b58bc2ff632f364b",
	"provenance-policy":                   "3920d2ac66186688ef87488aad200085b632f1acfe7e524d176cbf7b55d4caaa",
	"submit-coverage-meta/2.0/absolute":   "1f128f01daf559450ac3df76be201b526d475377d95d7059ec79a924e93c7ddb",
	"submit-coverage-meta/2.0/relative":   "7f6656d66cf9ca0fbaf11bba00938ec66f63eab0d52a2e469e1381d5da06cf09",
	"submit-coverage-meta/2.1/absolute":   "35df4dc5db9cb0c0269fde3bc0c661ab0b0f2b19b54ba466cab1f9c0962d89ff",
	"submit-coverage-meta/2.1/relative":   "d1bdf037f4a5c9516a6c37255b5b8a0ba72a37281e358ce54db3f2c9071312ff",
	"submit-coverage-meta/2.2/absolute":   "35df4dc5db9cb0c0269fde3bc0c661ab0b0f2b19b54ba466cab1f9c0962d89ff",
	"submit-coverage-meta/2.2/relative":   "d1bdf037f4a5c9516a6c37255b5b8a0ba72a37281e358ce54db3f2c9071312ff",
	"submit-order-meta/2.0/absolute":      "33d4b26ad728de5201e65b79b30b2fa8b1ee12fdec841526a5431bd0aa7b638f",
	"submit-order-meta/2.0/relative":      "25edff04aa75ab5cf21287ba7040c93f14be8fa9ccdc54e3498d53d59a48b855",
	"submit-order-meta/2.1/absolute":      "72ecbe9790c709ffea8ca610a72db09becd96a3522e5069693592dd555492273",
	"submit-order-meta/2.1/relative":      "116a9537f84f77c236cef98fd1e6370ffb29896d87b4d94a067eeacb1bfdd66a",
	"submit-order-meta/2.2/absolute":      "72ecbe9790c709ffea8ca610a72db09becd96a3522e5069693592dd555492273",
	"submit-order-meta/2.2/relative":      "116a9537f84f77c236cef98fd1e6370ffb29896d87b4d94a067eeacb1bfdd66a",
	"submit-party-meta/2.0/absolute":      "c10035a38c7bde1d613cc073b2514959832ec6a7a21d7d560343a7e998943ca7",
	"submit-party-meta/2.0/relative":      "2a66155199438c561be03cfa3c38e563423b36dd8bf18f7890be8963cfaf3e25",
	"submit-party-meta/2.1/absolute":      "f4656d911d486ae44545104fe39c1f17d6c4a89c32a3a424b781209758af84b4",
	"submit-party-meta/2.1/relative":      "df4158360a5aa2afabbe9521cf0f9d0caa7d2d28afc5c7b826ab0cad5352e1b0",
	"submit-party-meta/2.2/absolute":      "f4656d911d486ae44545104fe39c1f17d6c4a89c32a3a424b781209758af84b4",
	"submit-party-meta/2.2/relative":      "df4158360a5aa2afabbe9521cf0f9d0caa7d2d28afc5c7b826ab0cad5352e1b0",
	"submit-payer-meta/2.0/absolute":      "0e9ba4267c1fddb40d7e86ef196672d07927907fe305003dbf45383b31d76062",
	"submit-payer-meta/2.0/relative":      "45ce053b9e5fd8f17a57ea3ff3181b8dbbe042421d0127487b55d57dbe4a82f7",
	"submit-payer-meta/2.1/absolute":      "2367ee77479dfdadf6f622fa9831ffd220d41c19bdd6f64adea28cae9a3c9566",
	"submit-payer-meta/2.1/relative":      "6ded90064229d2ebb3b5a51c649e67b730971227a00f9af1c7e20ef27f005d22",
	"submit-payer-meta/2.2/absolute":      "2367ee77479dfdadf6f622fa9831ffd220d41c19bdd6f64adea28cae9a3c9566",
	"submit-payer-meta/2.2/relative":      "6ded90064229d2ebb3b5a51c649e67b730971227a00f9af1c7e20ef27f005d22",
	"submit-plain/2.0/absolute":           "481585597418f727887320915e3d2ce115635427b41b7e727acf8fa80154e4c9",
	"submit-plain/2.0/relative":           "110a0cbbd8d21786277b936254ef9c1ea4e605087bc0a3ea4dda2bf5c812ff99",
	"submit-plain/2.1/absolute":           "b4e0e136968cf7f4b3a0f32d0ce0877b130e7e9dc1cfa13cc17a3ed15606005a",
	"submit-plain/2.1/relative":           "b061662d5118a5cd0bdca143894c0a6eeeabbe009b7726372cb4d2999c2be2a9",
	"submit-plain/2.2/absolute":           "b4e0e136968cf7f4b3a0f32d0ce0877b130e7e9dc1cfa13cc17a3ed15606005a",
	"submit-plain/2.2/relative":           "b061662d5118a5cd0bdca143894c0a6eeeabbe009b7726372cb4d2999c2be2a9",
	"update-coverage-meta/2.0/absolute":   "9b65fab8ca399ed5c9bd43e67c607f0f4556492677377a91171e259e85a8fb98",
	"update-coverage-meta/2.0/relative":   "230669fe1e720c6f7b10a1750e81884f8c2f7dd2376e6878d772b27247a71421",
	"update-coverage-meta/2.1/absolute":   "c21b39972b43c8ba34e2acb7e9e8b667e226d3e34a3e1c876450c94738a39d35",
	"update-coverage-meta/2.1/relative":   "059fb19be000b0a9a53e160c6531aade03d31613783bd07bb42b1572c9d3544e",
	"update-coverage-meta/2.2/absolute":   "c21b39972b43c8ba34e2acb7e9e8b667e226d3e34a3e1c876450c94738a39d35",
	"update-coverage-meta/2.2/relative":   "059fb19be000b0a9a53e160c6531aade03d31613783bd07bb42b1572c9d3544e",
	"update-no-report/2.0/absolute":       "e9904f9232be1e0fbf15a741ecf3d3819acc36760e33695dc5a7fee3b7ba083a",
	"update-no-report/2.0/relative":       "984293718de68af5443481eeec04073f1764bdbc1c867f8255c832f293f11d60",
	"update-no-report/2.1/absolute":       "cbb76f717ab09cf4e5dfd92a6bf1b3f886a74cdc8a31459e4879c2adffd357da",
	"update-no-report/2.1/relative":       "0d819e84fc5e0b55f6976c3e0f518d5dd826e3da1802d04d7386a62f907835fd",
	"update-no-report/2.2/absolute":       "cbb76f717ab09cf4e5dfd92a6bf1b3f886a74cdc8a31459e4879c2adffd357da",
	"update-no-report/2.2/relative":       "0d819e84fc5e0b55f6976c3e0f518d5dd826e3da1802d04d7386a62f907835fd",
	"update-order-meta/2.0/absolute":      "d6e03a9308294a970c274e829a72ec478d740d6a64a4ea06e40a383d34eb7289",
	"update-order-meta/2.0/relative":      "3134d30868ec719482fbbd47e43b7b913296451504921798560b2f7e2f7e322e",
	"update-order-meta/2.1/absolute":      "8aa0e57d2ef145bde0a29e933d76c0751bbb7fd48df46491baca1d2369d5ebd1",
	"update-order-meta/2.1/relative":      "81ef1f4337b489a361b81bd83159f6c6e19d1e66cadd329e241a9bf540bf5d7a",
	"update-order-meta/2.2/absolute":      "8aa0e57d2ef145bde0a29e933d76c0751bbb7fd48df46491baca1d2369d5ebd1",
	"update-order-meta/2.2/relative":      "81ef1f4337b489a361b81bd83159f6c6e19d1e66cadd329e241a9bf540bf5d7a",
	"update-party-meta/2.0/absolute":      "6b8109eb61fb8eabd6fbaa3cfb5244143e07cf69862332cac94152cc1625dadf",
	"update-party-meta/2.0/relative":      "f917d779e3b1c4a1bb8de486ac2ee71b9203d9cccff92642980dee112eb7e882",
	"update-party-meta/2.1/absolute":      "b6db99230226f36c94e0c9dd3877c8a4dd7ddc8fce12295be1859959253cc984",
	"update-party-meta/2.1/relative":      "125cccae5b15d3e9bd8863e58873ec5c28172f61d343d386ae49b12f3b3465bf",
	"update-party-meta/2.2/absolute":      "b6db99230226f36c94e0c9dd3877c8a4dd7ddc8fce12295be1859959253cc984",
	"update-party-meta/2.2/relative":      "125cccae5b15d3e9bd8863e58873ec5c28172f61d343d386ae49b12f3b3465bf",
	"update-payer-meta/2.0/absolute":      "a409641074a437dc4486aaeb3788f3fdf70cc38728024960595929c37c9de6db",
	"update-payer-meta/2.0/relative":      "9f4ca7c7182c1fc6e92cace67966852ca144cca8d2affc8b96469ce64f237953",
	"update-payer-meta/2.1/absolute":      "24d606f7a98644e79f0ef2ca6e3ac64bad4cdabcbd78fd81f2651a0e0b9e9879",
	"update-payer-meta/2.1/relative":      "9e890d6cff61f414703e318c574c9e88c96fe02e9ddcb14dbf078532d2569077",
	"update-payer-meta/2.2/absolute":      "24d606f7a98644e79f0ef2ca6e3ac64bad4cdabcbd78fd81f2651a0e0b9e9879",
	"update-payer-meta/2.2/relative":      "9e890d6cff61f414703e318c574c9e88c96fe02e9ddcb14dbf078532d2569077",
	"update-plain/2.0/absolute":           "5087b0538368c7f6a438a19859b259c021c2aa9ae428d47efc7c76a4d16b615b",
	"update-plain/2.0/relative":           "59068257fe636d5fbb5559fe394b4b5e7a9a60c43832dbeee96d197480f2dd29",
	"update-plain/2.1/absolute":           "ff59733e42e0880e172e197da45b6a69be45e68311054f593ca95d60e23dbecd",
	"update-plain/2.1/relative":           "0d491e8c338119143e62658c56dc805f8d532827066fbce8ee3b339f43d28e98",
	"update-plain/2.2/absolute":           "ff59733e42e0880e172e197da45b6a69be45e68311054f593ca95d60e23dbecd",
	"update-plain/2.2/relative":           "0d491e8c338119143e62658c56dc805f8d532827066fbce8ee3b339f43d28e98",
	"update-prior-indented/2.0/absolute":  "5087b0538368c7f6a438a19859b259c021c2aa9ae428d47efc7c76a4d16b615b",
	"update-prior-indented/2.0/relative":  "59068257fe636d5fbb5559fe394b4b5e7a9a60c43832dbeee96d197480f2dd29",
	"update-prior-indented/2.1/absolute":  "ff59733e42e0880e172e197da45b6a69be45e68311054f593ca95d60e23dbecd",
	"update-prior-indented/2.1/relative":  "0d491e8c338119143e62658c56dc805f8d532827066fbce8ee3b339f43d28e98",
	"update-prior-indented/2.2/absolute":  "ff59733e42e0880e172e197da45b6a69be45e68311054f593ca95d60e23dbecd",
	"update-prior-indented/2.2/relative":  "0d491e8c338119143e62658c56dc805f8d532827066fbce8ee3b339f43d28e98",
	"update-provenance-meta/2.0/absolute": "9c769ecb19246be76a761efad5211a0e3b6fedcaae4b34b4d2c853557923e3bd",
	"update-provenance-meta/2.0/relative": "0d07593d1fc21e83948b4ce9c955a0e4184e0371a76f148c400bb491f59de043",
	"update-provenance-meta/2.1/absolute": "52bf9df11ae1d37e54226f08af6d7b8107fba756c26664abf55d3438d66afee3",
	"update-provenance-meta/2.1/relative": "1e1cace4996bf4499b7c7ad04c5f5bbeac689ce79ccf607bdf655d21b4665f1a",
	"update-provenance-meta/2.2/absolute": "52bf9df11ae1d37e54226f08af6d7b8107fba756c26664abf55d3438d66afee3",
	"update-provenance-meta/2.2/relative": "1e1cace4996bf4499b7c7ad04c5f5bbeac689ce79ccf607bdf655d21b4665f1a",
	"update-report-meta/2.0/absolute":     "9e2a4dc60c8fd1892a9c7291fce720308d202ef171d6c56c0dece8cafaeae9b4",
	"update-report-meta/2.0/relative":     "822f979c52fc176b3375bce3a5b5de40eecfd5fc7454bd4b2ae767d703013f54",
	"update-report-meta/2.1/absolute":     "70c228c029ada2c370f1dd15018114ddc3c85aaafff95bef30edcdf09723e103",
	"update-report-meta/2.1/relative":     "7237fe38e971387aace5960bc557ac9efb43eecac86bac0af76bac4dd24bef16",
	"update-report-meta/2.2/absolute":     "70c228c029ada2c370f1dd15018114ddc3c85aaafff95bef30edcdf09723e103",
	"update-report-meta/2.2/relative":     "7237fe38e971387aace5960bc557ac9efb43eecac86bac0af76bac4dd24bef16",
}

// The Deprecated Exact builders, and the Deprecated Provenance builders, keep
// the bytes shn-sdk v1.8.0 built: a participant's meta.profile dropped from the
// order, the Coverage, the persons it names, the payer's Organization, the
// report and the Provenance; the report and the Provenance under bundle-local
// ids, re-encoded, the Provenance target re-pointed; the prior Claim compacted;
// references written absolute when AbsoluteRefs is set; a Provenance with no
// id. A case they refuse stays refused, pinned by its error text. The Carried
// builders share their steps, so a change to a shared step cannot move these
// bytes unnoticed. Every line, both AbsoluteRefs settings.
func TestPASExactBuilders_KeepTheirBytes(t *testing.T) {
	withMeta := func(rec []byte, rt string) []byte {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(rec, &m); err != nil {
			t.Fatal(err)
		}
		m["meta"] = map[string]any{"versionId": "3", "profile": []any{"http://example.org/StructureDefinition/" + rt}}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	indented := func(rec []byte) []byte {
		t.Helper()
		var b bytes.Buffer
		if err := json.Indent(&b, rec, "", "   "); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	const parent = `{"resourceType":"Patient","id":"parent","meta":{"profile":["http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"]},"identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"name":[{"family":"Parent","given":["Pat"]}]}`
	got := map[string][]byte{}
	for _, line := range pasLines {
		for _, abs := range []bool{false, true} {
			suf := "/" + line + map[bool]string{false: "/relative", true: "/absolute"}[abs]
			submit := func(name string, in ConformantClaimInputs) {
				t.Helper()
				b, err := BuildConformantClaimBundleAtLineExact(line, in)
				if err != nil {
					b = []byte("refused: " + err.Error())
				}
				got["submit-"+name+suf] = b
			}
			in := conformantSubmitInputs(t)
			in.AbsoluteRefs = abs
			submit("plain", in)
			c := in
			c.Coverage = withMeta(in.Coverage, "Coverage")
			submit("coverage-meta", c)
			c = in
			c.Insurer = withMeta(in.Insurer, "Organization")
			submit("payer-meta", c)
			c = in
			c.SR = withMeta(in.SR, "ServiceRequest")
			submit("order-meta", c)
			c = in
			c.Coverage, c.CoverageParties = entryCoverage("MBR-COVERED", "Patient/parent", []string{"subscriber"}, ""), [][]byte{[]byte(parent)}
			submit("party-meta", c)

			// Each amendment continues the submission its own inputs and lane
			// sent (testSubmissionFor); prior-indented carries it re-indented.
			update := func(name string, u ConformantClaimUpdateInputs) {
				t.Helper()
				if u.CoverageParties == nil {
					u.PriorClaim = testSubmissionFor(t, line, u)
				} else { // as entryBuild continues a dependent's submission
					sub := conformantSubmitInputs(t)
					sub.AbsoluteRefs, sub.Coverage, sub.CoverageParties = u.AbsoluteRefs, u.Coverage, u.CoverageParties
					built, err := BuildConformantClaimBundleAtLineExact(line, sub)
					if err != nil {
						t.Fatal(err)
					}
					if u.PriorClaim, err = PASPriorClaimOf(built); err != nil {
						t.Fatal(err)
					}
				}
				if name == "prior-indented" {
					u.PriorClaim.Claim = indented(u.PriorClaim.Claim)
				}
				b, err := BuildConformantClaimUpdateBundleAtLineExact(line, u)
				if err != nil {
					b = []byte("refused: " + err.Error())
				}
				got["update-"+name+suf] = b
			}
			u := conformantUpdateInputsFromGolden(t)
			u.AbsoluteRefs = abs
			update("plain", u)
			v := u
			v.Coverage = withMeta(u.Coverage, "Coverage")
			update("coverage-meta", v)
			v = u
			v.Insurer = withMeta(u.Insurer, "Organization")
			update("payer-meta", v)
			v = u
			v.SR = withMeta(u.SR, "ServiceRequest")
			update("order-meta", v)
			v = u
			v.DiagnosticReport = indented(withMeta(u.DiagnosticReport, "DiagnosticReport"))
			update("report-meta", v)
			v = u
			v.Provenance = indented(withMeta(u.Provenance, "Provenance"))
			update("provenance-meta", v)
			v = u
			v.DiagnosticReport = nil
			update("no-report", v)
			update("prior-indented", u)
			v = u
			v.Coverage, v.CoverageParties = entryCoverage("MBR-COVERED", "Patient/parent", []string{"subscriber"}, ""), [][]byte{[]byte(parent)}
			update("party-meta", v)
		}
	}
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	for name, build := range map[string]func() ([]byte, error){
		"provenance": func() ([]byte, error) { return BuildProvenance("DiagnosticReport/dr-1", "Organization/o", now) },
		"provenance-policy": func() ([]byte, error) {
			return BuildProvenanceWithPolicy("DiagnosticReport/dr-1", "Organization/o", "Consent/c", "TREAT", now)
		},
		"provenance-identifier": func() ([]byte, error) {
			return BuildProvenanceWithIdentifier("DiagnosticReport/dr-1", ProvenanceIdentifier{System: "http://hl7.org/fhir/sid/us-npi", Value: "1234567893"}, now)
		},
	} {
		b, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got[name] = b
	}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	var moved []string
	for _, name := range names {
		sum := sha256.Sum256(got[name])
		if h := hex.EncodeToString(sum[:]); h != exactBuilderPins[name] {
			moved = append(moved, "\t\""+name+"\": \""+h+"\",")
		}
	}
	if len(got) != len(exactBuilderPins) || len(moved) > 0 {
		t.Fatalf("%d cases built, %d pinned; these moved from their pins:\n%s", len(got), len(exactBuilderPins), strings.Join(moved, "\n"))
	}
}

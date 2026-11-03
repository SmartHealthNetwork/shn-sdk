package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// ErrPASCoveragePartyNotCarried is the refusal of a request builder whose
// member's Coverage names, as its subscriber or policyHolder, a person who is
// not the member (a dependent's parent) when the participant's own record of
// that person was not supplied, or cannot ride the request as an entry of its
// own (ConformantClaimInputs.CoverageParties). It is the fidelity of SHN's own
// build: the build carries the participant's own record of that person and
// never makes one up, and it does not drop what the Coverage asserts, so the
// request is not built and nothing is sealed or sent. It is never a judgement
// of anyone's payload.
var ErrPASCoveragePartyNotCarried = errors.New("a person the member's Coverage names cannot be carried from the participant's own records, so the request is not built: SHN's build never makes up a record or drops what the Coverage asserts (fidelity of SHN's own build)")

// pasEntryParty is the participant's own Patient record of a person the
// member's Coverage names as subscriber or policyHolder who is not the member,
// prepared to ride the request as a Patient entry of its own: the Coverage's
// party as CoverageEntryParties decides it.
type pasEntryParty struct {
	id  string
	raw []byte
	// record is the participant's record exactly as supplied, before
	// meta.profile is taken out: what an inquiry, which carries every record as
	// its own bytes, embeds.
	record []byte
	// versionId is the record's meta.versionId, or "" when it states none.
	versionId string
}

// pasCoverageSlotParty prepares the person a Coverage's subscriber or
// policyHolder slot names, by the literal Patient reference key ("Patient/<id>",
// patientKey), from the records the caller supplied. The record is the one
// supplied Patient with that id; it is carried as the participant's system
// holds it, less meta.profile (the PAS context declares none). Every way it
// cannot be carried is ErrPASCoveragePartyNotCarried, naming the slot:
//   - no supplied record has that id, or more than one does;
//   - the record is not one strict JSON document, or repeats a member name;
//   - it contains other resources, its identifier is not a list, or it spells
//     a member the party rule reads in another case (coveragePartyFault, and
//     meta);
//   - it makes a reference: nothing it could name rides the request, and a
//     payer refuses a request whose graph names anything it cannot resolve.
func pasCoverageSlotParty(field, key string, supplied [][]byte) (pasEntryParty, error) {
	notCarried := func(format string, args ...any) error {
		return fmt.Errorf("%w: your Coverage record's %s names %s, %s", ErrPASCoveragePartyNotCarried, field, key, fmt.Sprintf(format, args...))
	}
	id := strings.TrimPrefix(key, "Patient/")
	var found []byte
	for _, rec := range supplied {
		var head struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if json.Unmarshal(rec, &head) != nil || head.ResourceType != "Patient" || head.ID != id {
			continue
		}
		if found != nil {
			return pasEntryParty{}, notCarried("and more than one of the Patient records supplied has that id")
		}
		found = rec
	}
	if found == nil {
		return pasEntryParty{}, notCarried("a person other than the member, and no Patient record of that person was supplied")
	}
	if !fhirIDPattern.MatchString(id) {
		return pasEntryParty{}, notCarried("whose record has no valid id")
	}
	if _, err := splice.Scan(found, splice.DefaultLimits()); err != nil {
		var dk *splice.DuplicateKeyError
		if errors.As(err, &dk) {
			return pasEntryParty{}, notCarried("whose record repeats the member name %q in %s", dk.Key, jsonObjectPathAt(found, dk.Offset, "Patient"))
		}
		return pasEntryParty{}, notCarried("whose record is not one strict JSON document: %v", err)
	}
	var party map[string]any
	if err := json.Unmarshal(found, &party); err != nil {
		return pasEntryParty{}, notCarried("whose record is not one JSON object")
	}
	if why := coveragePartyFault(party); why != "" {
		return pasEntryParty{}, notCarried("whose record %s, and a person carried beside the member contains nothing, lists its identifiers and spells its members exactly", why)
	}
	if k := caseVariantMember(party, "meta"); k != "" {
		return pasEntryParty{}, notCarried("whose record has a member named %q, a name the rule reads spelled in another case", k)
	}
	if refs := referencesIn("Patient", party); len(refs) > 0 {
		return pasEntryParty{}, notCarried("whose record references records this request does not carry (%s): a payer refuses a request whose graph names anything it cannot resolve", strings.Join(refs, "; "))
	}
	out, err := stripMetaProfile(found)
	if err != nil {
		return pasEntryParty{}, fmt.Errorf("strip party meta: %w", err)
	}
	version := ""
	if meta, ok := party["meta"].(map[string]any); ok {
		version, _ = meta["versionId"].(string)
	}
	return pasEntryParty{id: id, raw: out, record: found, versionId: version}, nil
}

// pasCoverageSlotVersion refuses a slot that names one version of the
// person's record ("Patient/<id>/_history/<v>") when the record supplied is
// not that version (its meta.versionId differs, or it states none): the
// record carried would be another version than the one the Coverage names.
// A slot naming that very version names the entry carried, which is that
// version, so it is re-pointed like an unversioned one.
func pasCoverageSlotVersion(field, ref string, party pasEntryParty) error {
	i := strings.LastIndex(ref, "/_history/")
	if i < 0 {
		return nil
	}
	want := ref[i+len("/_history/"):]
	if want == "" {
		return fmt.Errorf("%w: your Coverage record's %s names Patient/%s with an empty version", ErrPASCoveragePartyNotCarried, field, party.id)
	}
	if want == party.versionId {
		return nil
	}
	have := "states no version"
	if party.versionId != "" {
		have = fmt.Sprintf("is version %q", party.versionId)
	}
	return fmt.Errorf("%w: your Coverage record's %s names version %q of Patient/%s, and the record supplied %s", ErrPASCoveragePartyNotCarried, field, want, party.id, have)
}

// referencesIn lists, in path order, every reference a decoded resource (or
// element) makes under path: each member named "reference" in any case, with
// the value it holds. A local reference ("#id") counts too, since a party
// contains nothing it could name.
func referencesIn(path string, v any) []string {
	var found []string
	var walk func(string, any)
	walk = func(at string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				if strings.EqualFold(k, "reference") {
					s, _ := x[k].(string)
					found = append(found, at+"."+k+" -> "+s)
					continue
				}
				walk(at+"."+k, x[k])
			}
		case []any:
			for i, c := range x {
				walk(fmt.Sprintf("%s[%d]", at, i), c)
			}
		}
	}
	walk(path, v)
	return found
}

// checkPASCoverageEntryParties confirms, on the built request, that every
// person the build added beside the member is the Coverage's party exactly by
// CoverageEntryParties' rule: named by the Coverage's subscriber or
// policyHolder and by nothing else in the request. A request where another
// element also names that person (a Claim, a QuestionnaireResponse subject, an
// entry sharing its fullUrl) is refused as ErrPASCoveragePartyNotCarried: the
// person would ride as something other than the Coverage's party.
func checkPASCoverageEntryParties(bundle []byte, parties []pasEntryParty) error {
	if len(parties) == 0 {
		return nil
	}
	var b map[string]any
	if err := json.Unmarshal(bundle, &b); err != nil {
		return fmt.Errorf("read the built request: %w", err)
	}
	named := CoverageEntryParties(b)
	for _, p := range parties {
		if u := pasBundleBaseURL + "/Patient/" + p.id; !named[u] {
			return fmt.Errorf("%w: the Patient %s the Coverage names beside the member is not its party alone in the built request (another element names it, or it shares an entry's fullUrl)", ErrPASCoveragePartyNotCarried, u)
		}
	}
	return nil
}

// pasCoverageSlotToEntry re-points a subscriber or policyHolder slot naming
// the Coverage's party (key, "Patient/<id>") at that party's own entry in the
// request, by the bundle-local relative reference every other entry is named
// by, keeping every other member of the slot as the record has it. A slot
// whose other members make a reference is refused: nothing they name rides
// the request.
func pasCoverageSlotToEntry(field string, raw json.RawMessage, key string) (json.RawMessage, error) {
	// The slot's members are kept as the record's own bytes, so a number or a
	// string inside one is carried exactly as written; only the reference is
	// replaced.
	var slot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &slot); err != nil {
		return nil, fmt.Errorf("your Coverage record's %s: %w", field, err)
	}
	rest := make(map[string]any, len(slot))
	for k, v := range slot {
		if k == "reference" {
			continue
		}
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, fmt.Errorf("your Coverage record's %s.%s: %w", field, k, err)
		}
		rest[k] = x
	}
	if refs := referencesIn("Coverage."+field, rest); len(refs) > 0 {
		return nil, fmt.Errorf("%w: your Coverage record's %s references records this request does not carry beside the person it names (%s)", ErrPASCoveragePartyNotCarried, field, strings.Join(refs, "; "))
	}
	ref, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	slot["reference"] = ref
	return json.Marshal(slot)
}

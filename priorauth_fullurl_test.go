package shnsdk

import (
	"regexp"
	"testing"
)

var collectionEntryV5URN = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The draftOrders entry the prior-authorization flow writes is named by an
// absolute urn:uuid, as FHIR R4 requires of a Bundle entry: the same seed and
// entry give the same one, each entry and seed has its own, and it is never
// the resource's own id, which a HAPI-based payer would take as the id.
func TestCollectionEntryFullURL(t *testing.T) {
	const seed = "d1577c69-dfbe-44ad-ba6d-3e05e953b2ea/draftOrders"
	got := collectionEntryFullURL(seed, 0, "sr-1")
	if got != "urn:uuid:cca38da6-7917-5cdb-b66f-ac0da785b2da" || got != collectionEntryFullURL(seed, 0, "sr-1") {
		t.Fatalf("fullUrl %q, want the stable version 5 UUID of %s/0 in the URL name space", got, seed)
	}
	for _, other := range []string{collectionEntryFullURL(seed, 1, "sr-1"), collectionEntryFullURL("another/draftOrders", 0, "sr-1")} {
		if other == got || !collectionEntryV5URN.MatchString(other) {
			t.Errorf("fullUrl %q beside %q", other, got)
		}
	}
	taken := nameBasedUUID(seed + "/0")
	if u := collectionEntryFullURL(seed, 0, taken); u == "urn:uuid:"+taken || !collectionEntryV5URN.MatchString(u) {
		t.Errorf("a resource whose id is %s got fullUrl %q", taken, u)
	}
	if v := nameBasedUUID("http://www.example.com"); v != "2d32a3f1-fac2-52a6-ac52-fe9e87e628ea" {
		t.Errorf("nameBasedUUID(http://www.example.com) = %s", v)
	}
}

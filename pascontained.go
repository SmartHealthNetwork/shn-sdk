package shnsdk

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The contained resources a prior-authorization request carries, and the one
// rule that decides whether they may ride at all.
//
// FHIR dom-3: a resource contained in another resource SHALL be referred to from
// elsewhere in the resource that contains it. A contained resource nothing points
// at is a record that travels with the message and asserts nothing — the receiver
// cannot say whose it is or what role it plays — so the base specification makes
// it an error, and a pinned IG-profile $validate rejects the whole resource for
// it ("The contained resource '…' is not referenced to from elsewhere").
//
// This builder reads the PARTICIPANT'S OWN records and re-points exactly three
// references — the coverage's payor, the claim's insurer and the claim's
// coverage — at the entries the request carries. A re-point that moves the ONE
// reference to a contained resource strands it. That is not a validation detail:
// the message would name a payer twice and assert nothing with one of them.
//
// So the assembled request is checked before it is returned, on every lane. The
// builders drop the contained record a re-point displaces when the same record
// rides as an entry (dropContainedPayerOrg); anything else that ends up stranded
// is refused here, naming the resource and the record, rather than emitted for a
// validator or a peer to reject later.

// checkPASContainedReferenced refuses a built request whose Coverage or Claim
// carries a contained resource that nothing in it references.
//
// Validation cannot be this guard on every lane — egress $validate runs against
// the lane's pinned IG profiles, and a request built for a peer is refused at
// that peer rather than here — so the builder that authored the bytes says so
// first, at the seam where the displaced reference was made.
//
// SCOPE, stated rather than implied: it covers the two resources whose local
// references THIS BUILDER rewrites — the coverage (payor) and the claim (insurer,
// insurance.coverage). A caller's own QuestionnaireResponse may contain the
// qr-context records a DTR fill put there; whether those are referenced is a fact
// about the record the caller handed in, not about anything re-pointed here, and
// refusing it at this seam would turn an egress-validation finding about a
// participant's own QR into a build failure with a different name. That stays
// where it already is: the egress $validate the gateway runs on what it emits.
var pasContainedGuardedTypes = []string{"Coverage", "Claim"}

func checkPASContainedReferenced(bundle []byte) error {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return fmt.Errorf("read the built request: %w", err)
	}
	var stranded []string
	for _, e := range b.Entry {
		var r struct {
			ResourceType string            `json:"resourceType"`
			ID           string            `json:"id"`
			Contained    []json.RawMessage `json:"contained"`
		}
		if json.Unmarshal(e.Resource, &r) != nil || len(r.Contained) == 0 ||
			!slices.Contains(pasContainedGuardedTypes, r.ResourceType) {
			continue
		}
		// Every local reference the container makes, EXCEPT the ones a contained
		// resource makes to itself: a record pointing at nothing but itself is the
		// stranded record, not its own referrer.
		outer := localReferences(e.Resource, true)
		within := make([]map[string]bool, len(r.Contained))
		for i, c := range r.Contained {
			within[i] = map[string]bool{}
			for _, ref := range localReferences(c, false) {
				within[i][ref] = true
			}
		}
		for i, c := range r.Contained {
			var head struct {
				ResourceType string `json:"resourceType"`
				ID           string `json:"id"`
			}
			if json.Unmarshal(c, &head) != nil || head.ID == "" {
				continue
			}
			referenced := slices.Contains(outer, head.ID)
			for j := range within {
				if j != i && within[j][head.ID] {
					referenced = true
				}
			}
			if !referenced {
				stranded = append(stranded, fmt.Sprintf("%s/%s contains %s %q",
					blankAsUnnamed(r.ResourceType), blankAsUnnamed(r.ID),
					blankAsUnnamed(head.ResourceType), head.ID))
			}
		}
	}
	if len(stranded) == 0 {
		return nil
	}
	slices.Sort(stranded)
	return fmt.Errorf("the request carries a contained record nothing in the resource that contains it references (%s): a contained resource that is referred to from nowhere asserts nothing and is a FHIR dom-3 error, so either the reference that named it belongs on the request or the record does not",
		strings.Join(stranded, "; "))
}

// localReferences reads every "#id" reference a resource makes, anywhere in its
// tree. When skipContained is true the "contained" array is not descended into,
// so the result is what the CONTAINER itself points at and nothing else.
func localReferences(resourceJSON []byte, skipContained bool) []string {
	var found []string
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			for k, v := range obj {
				switch {
				case k == "contained" && skipContained:
					continue
				case k == "reference":
					var ref string
					if json.Unmarshal(v, &ref) == nil && strings.HasPrefix(ref, "#") && len(ref) > 1 {
						found = append(found, strings.TrimPrefix(ref, "#"))
					}
				default:
					walk(v)
				}
			}
			return
		}
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			for _, v := range arr {
				walk(v)
			}
		}
	}
	walk(resourceJSON)
	return found
}

// blankAsUnnamed renders an absent element as a word, so a refusal never reads
// "/ contains  \"cms-payer\"".
func blankAsUnnamed(v string) string {
	if strings.TrimSpace(v) == "" {
		return "an unnamed resource"
	}
	return v
}

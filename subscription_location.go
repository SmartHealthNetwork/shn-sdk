package shnsdk

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSubscriptionLocation is a FrameHeaderSubscriptionLocation value that is
// not a path to a Subscription by its id: the gateway that reads it refuses
// it, and frames nothing on.
var ErrSubscriptionLocation = errors.New("shnsdk: not a Subscription location")

// ParseSubscriptionLocation reads a FrameHeaderSubscriptionLocation value:
// "Subscription/<id>", or "Subscription/<id>/_history/<vid>", whose vid it
// returns; vid is empty for the first form. <id> and <vid> are FHIR ids
// ([A-Za-z0-9\-\.]{1,64}) other than the dot segments "." and "..". Anything
// else is ErrSubscriptionLocation: an empty value, an absolute URL or a path
// with a leading slash, another resource type, another segment, and a query
// or a fragment, which no FHIR id holds, among them.
func ParseSubscriptionLocation(v string) (id, vid string, err error) {
	rest, ok := strings.CutPrefix(v, "Subscription/")
	if !ok {
		return "", "", fmt.Errorf("%w: it does not begin Subscription/", ErrSubscriptionLocation)
	}
	id, history, versioned := strings.Cut(rest, "/")
	if !isFHIRPathID(id) {
		return "", "", fmt.Errorf("%w: the Subscription id is not a FHIR id, or is a dot segment", ErrSubscriptionLocation)
	}
	if !versioned {
		return id, "", nil
	}
	vid, ok = strings.CutPrefix(history, "_history/")
	if !ok || !isFHIRPathID(vid) {
		return "", "", fmt.Errorf("%w: after the id, only /_history/ and a version id", ErrSubscriptionLocation)
	}
	return id, vid, nil
}

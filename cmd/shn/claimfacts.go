package main

// claimfacts.go resolves the request facts a prior-authorization claim states
// for `shn priorauth` and `shn doctor`: the claim type, priority,
// service category, place of service and request type. Each comes from the
// persona's own statement in the network's discovery descriptor (the test
// participant's statement of its request), or from a flag, and a flag beats
// the descriptor. Neither supplies a value — the CLI never fills one in — so a
// required fact with no source refuses, naming both sources.

import (
	"encoding/json"
	"flag"
	"fmt"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// claimFactFlags are the request-fact flags of `shn priorauth`. Each takes a
// CODE; the system is the one the element's binding draws from.
type claimFactFlags struct {
	priority, claimType, category, placeOfService, requestType *string
}

func registerClaimFactFlags(fs *flag.FlagSet) claimFactFlags {
	return claimFactFlags{
		priority:       fs.String("priority", "", "Claim.priority: a process priority code ("+shnsdk.PASSystemProcessPriority+"), e.g. normal; overrides the persona's order"),
		claimType:      fs.String("claim-type", "", "Claim.type: a code from the line's ClaimTypes system ("+shnsdk.PASSystemClaimType+"), e.g. professional; overrides the persona's order"),
		category:       fs.String("service-category", "", "Claim.item.category: an X12 1365 service type code ("+shnsdk.PASSystemX12ServiceType+"); overrides the persona's order"),
		placeOfService: fs.String("place-of-service", "", "Claim.item.location: a CMS place-of-service code ("+shnsdk.PASSystemPlaceOfService+"); overrides the persona's order"),
		requestType:    fs.String("request-type", "", "Claim.item requestType: an X12 1525 request category code ("+shnsdk.PASSystemX12RequestCategory+"); overrides the persona's order"),
	}
}

// noClaimFactFlags is the flag set of a caller with no flags (shn doctor): the
// descriptor is the only source.
func noClaimFactFlags() claimFactFlags {
	empty := func() *string { s := ""; return &s }
	return claimFactFlags{empty(), empty(), empty(), empty(), empty()}
}

// personaClaimFacts resolves each fact: the flag when given, else the
// persona's descriptor order, else nothing. ClaimType, Priority and
// ServiceCategory are required at the line `shn priorauth` submits (PAS 2.0)
// and refuse without a source; place of service and request type are
// optional there and are omitted when neither source states them.
func personaClaimFacts(order *shnsdk.DiscoveryOrder, f claimFactFlags) (shnsdk.PASClaimFacts, error) {
	if order == nil {
		order = &shnsdk.DiscoveryOrder{}
	}
	stated := map[string]*shnsdk.PASCoding{
		"claim-type": order.ClaimType, "priority": order.Priority, "service-category": order.ServiceCategory,
		"place-of-service": order.PlaceOfService, "request-type": order.RequestType,
	}
	var out shnsdk.PASClaimFacts
	for _, x := range []struct {
		element, flagName, system, flagCode string
		required                            bool
		dst                                 *json.RawMessage
	}{
		{"Claim.type", "claim-type", shnsdk.PASSystemClaimType, *f.claimType, true, &out.ClaimType},
		{"Claim.priority", "priority", shnsdk.PASSystemProcessPriority, *f.priority, true, &out.Priority},
		{"Claim.item.category", "service-category", shnsdk.PASSystemX12ServiceType, *f.category, true, &out.ServiceCategory},
		{"Claim.item.location", "place-of-service", shnsdk.PASSystemPlaceOfService, *f.placeOfService, false, &out.PlaceOfService},
		{"Claim.item requestType", "request-type", shnsdk.PASSystemX12RequestCategory, *f.requestType, false, &out.RequestType},
	} {
		c := stated[x.flagName]
		if x.flagCode != "" {
			c = &shnsdk.PASCoding{System: x.system, Code: x.flagCode}
		}
		if c == nil || c.Code == "" {
			if x.required {
				return out, fmt.Errorf("%s %w (expected from --%s, or from the persona's order in the network's discovery descriptor; neither states it)", x.element, shnsdk.ErrPASClaimFactAbsent, x.flagName)
			}
			continue
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return out, err
		}
		*x.dst = raw
	}
	return out, nil
}

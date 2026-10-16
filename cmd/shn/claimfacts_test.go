package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

func parsedFactFlags(t *testing.T, args ...string) claimFactFlags {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerClaimFactFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f
}

func descriptorOrder() *shnsdk.DiscoveryOrder {
	return &shnsdk.DiscoveryOrder{
		System: "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", Code: "E0250",
		Priority:        &shnsdk.PASCoding{System: shnsdk.PASSystemProcessPriority, Code: "normal"},
		ClaimType:       &shnsdk.PASCoding{System: shnsdk.PASSystemClaimType, Code: "professional"},
		ServiceCategory: &shnsdk.PASCoding{System: shnsdk.PASSystemX12ServiceType, Code: "42", Display: "Home Health Care"},
		PlaceOfService:  &shnsdk.PASCoding{System: shnsdk.PASSystemPlaceOfService, Code: "12", Display: "Home"},
		RequestType:     &shnsdk.PASCoding{System: shnsdk.PASSystemX12RequestCategory, Code: "HS", Display: "Health Services Review"},
	}
}

func codingOf(t *testing.T, raw json.RawMessage) shnsdk.PASCoding {
	t.Helper()
	var c shnsdk.PASCoding
	if raw == nil {
		return c
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPersonaClaimFacts_DescriptorIsTheSource(t *testing.T) {
	f, err := personaClaimFacts(descriptorOrder(), parsedFactFlags(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]shnsdk.PASCoding{
		"claimType": codingOf(t, f.ClaimType), "priority": codingOf(t, f.Priority),
		"category": codingOf(t, f.ServiceCategory), "location": codingOf(t, f.PlaceOfService), "requestType": codingOf(t, f.RequestType),
	} {
		want := map[string]shnsdk.PASCoding{
			"claimType":   *descriptorOrder().ClaimType,
			"priority":    *descriptorOrder().Priority,
			"category":    *descriptorOrder().ServiceCategory,
			"location":    *descriptorOrder().PlaceOfService,
			"requestType": *descriptorOrder().RequestType,
		}[name]
		if got != want {
			t.Errorf("%s = %+v, want the descriptor's %+v", name, got, want)
		}
	}
}

func TestPersonaClaimFacts_FlagBeatsDescriptor(t *testing.T) {
	f, err := personaClaimFacts(descriptorOrder(), parsedFactFlags(t,
		"--priority", "stat", "--claim-type", "institutional", "--service-category", "1",
		"--place-of-service", "11", "--request-type", "IN"))
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]shnsdk.PASCoding{
		"priority":    {codingOf(t, f.Priority), {System: shnsdk.PASSystemProcessPriority, Code: "stat"}},
		"claimType":   {codingOf(t, f.ClaimType), {System: shnsdk.PASSystemClaimType, Code: "institutional"}},
		"category":    {codingOf(t, f.ServiceCategory), {System: shnsdk.PASSystemX12ServiceType, Code: "1"}},
		"location":    {codingOf(t, f.PlaceOfService), {System: shnsdk.PASSystemPlaceOfService, Code: "11"}},
		"requestType": {codingOf(t, f.RequestType), {System: shnsdk.PASSystemX12RequestCategory, Code: "IN"}},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %+v, want the flag's %+v", name, pair[0], pair[1])
		}
	}
}

func TestPersonaClaimFacts_NeitherSourceRefuses(t *testing.T) {
	for _, tc := range []struct {
		flag, element string
		drop          func(*shnsdk.DiscoveryOrder)
	}{
		{"claim-type", "Claim.type", func(o *shnsdk.DiscoveryOrder) { o.ClaimType = nil }},
		{"priority", "Claim.priority", func(o *shnsdk.DiscoveryOrder) { o.Priority = nil }},
		{"service-category", "Claim.item.category", func(o *shnsdk.DiscoveryOrder) { o.ServiceCategory = nil }},
	} {
		o := descriptorOrder()
		tc.drop(o)
		_, err := personaClaimFacts(o, parsedFactFlags(t))
		if err == nil || !errors.Is(err, shnsdk.ErrPASClaimFactAbsent) || !strings.Contains(err.Error(), "--"+tc.flag) ||
			!strings.Contains(err.Error(), "discovery descriptor") || !strings.Contains(err.Error(), tc.element) {
			t.Errorf("no %s anywhere: %v, want a refusal naming %s, --%s and the descriptor", tc.flag, err, tc.element, tc.flag)
		}
		// The flag alone is enough.
		if _, err := personaClaimFacts(o, parsedFactFlags(t, "--"+tc.flag, "x")); err != nil {
			t.Errorf("--%s with no descriptor value: %v", tc.flag, err)
		}
	}
	// Optional at the line the CLI submits (PAS 2.0): omitted, not refused.
	o := descriptorOrder()
	o.PlaceOfService, o.RequestType = nil, nil
	f, err := personaClaimFacts(o, parsedFactFlags(t))
	if err != nil || f.PlaceOfService != nil || f.RequestType != nil {
		t.Errorf("no place of service / request type: facts=%+v err=%v, want both omitted", f, err)
	}
	// A persona with no order block states nothing.
	if _, err := personaClaimFacts(nil, parsedFactFlags(t)); err == nil {
		t.Error("nil descriptor order: want a refusal")
	}
}

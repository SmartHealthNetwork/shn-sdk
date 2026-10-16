package shnsdk

import "testing"

func TestDiscovery_ScenarioPersonaMatchesAllThreeKeys(t *testing.T) {
	d := Discovery{ScenarioPersonas: []DiscoveryScenarioPersona{
		{Scenario: "uc04", Lane: "demo", MemberID: "A"},
		{Scenario: "uc04", Lane: "demo", PersonaSet: "canary", MemberID: "B"},
		{Scenario: "uc04", Lane: "provider-data", MemberID: "C"},
		{Scenario: "uc08", Lane: "demo", MemberID: "D"},
	}}
	for _, tc := range []struct{ scenario, lane, set, want string }{
		{"uc04", "demo", "", "A"}, {"uc04", "demo", "canary", "B"}, {"uc04", "provider-data", "", "C"}, {"uc08", "demo", "", "D"},
	} {
		p, ok := d.ScenarioPersona(tc.scenario, tc.lane, tc.set)
		if !ok || p.MemberID != tc.want {
			t.Errorf("ScenarioPersona(%s,%s,%q) = %+v, %v, want %s", tc.scenario, tc.lane, tc.set, p, ok, tc.want)
		}
	}
	if _, ok := d.ScenarioPersona("uc08", "provider-data", ""); ok {
		t.Error("found a persona the descriptor does not state")
	}
}

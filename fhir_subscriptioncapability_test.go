package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// subscribeSearchParams are the search parameters a subscribe leg serves
// exactly, so a base declares no search it would refuse: url, status and _id.
var subscribeSearchParams = []CapabilitySearchParam{
	{"url", "http://hl7.org/fhir/SearchParameter/Subscription-url", "uri"},
	{"status", "http://hl7.org/fhir/SearchParameter/Subscription-status", "token"},
	{"_id", "http://hl7.org/fhir/SearchParameter/Resource-id", "token"},
}

const hrexTaskTopic = "http://hl7.org/fhir/us/davinci-hrex/SubscriptionTopic/Task"

// subscriptionBaseStatements are the statements WithSubscription composes
// onto: the provider's ingress (a PAS per-payer base) and each base of a
// payer's CDex ingress.
func subscriptionBaseStatements(t *testing.T) map[string][]byte {
	t.Helper()
	at := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	out := map[string][]byte{}
	b, err := BuildProviderIngressCapabilityStatement(at, SupportedContractVersions())
	if err != nil {
		t.Fatal(err)
	}
	out["provider ingress"] = b
	for name, base := range map[string]PayerCDexBase{"payer CDex plain": {}, "payer CDex provider": {Provider: true}, "payer CDex provider reads": {Provider: true, Reads: true}} {
		b, err := BuildPayerCDexIngressCapabilityStatement(at, base)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = b
	}
	return out
}

// The Subscription entry WithSubscription adds, byte for byte, for each
// capability: the interactions, the search parameters, $status only when
// asked, and one topic extension per canonical.
func TestWithSubscription_AddsTheEntry(t *testing.T) {
	const (
		interactions = `"interaction":[{"code":"create"},{"code":"read"},{"code":"vread"},{"code":"update"},{"code":"delete"},{"code":"search-type"}]`
		params       = `"searchParam":[{"name":"url","definition":"http://hl7.org/fhir/SearchParameter/Subscription-url","type":"uri"},{"name":"status","definition":"http://hl7.org/fhir/SearchParameter/Subscription-status","type":"token"},{"name":"_id","definition":"http://hl7.org/fhir/SearchParameter/Resource-id","type":"token"}]`
		status       = `"operation":[{"name":"status","definition":"http://hl7.org/fhir/uv/subscriptions-backport/OperationDefinition/backport-subscription-status"}]`
		topicExt     = `"extension":[{"url":"http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/capabilitystatement-subscriptiontopic-canonical","valueCanonical":"http://hl7.org/fhir/us/davinci-hrex/SubscriptionTopic/Task"}]`
	)
	for name, row := range map[string]struct {
		c    SubscriptionCapability
		want string
	}{
		"PAS: no topic, $status": {SubscriptionCapability{SearchParams: subscribeSearchParams, StatusOperation: true},
			`{"type":"Subscription",` + interactions + `,` + params + `,` + status + `}`},
		"no topic, no $status": {SubscriptionCapability{SearchParams: subscribeSearchParams},
			`{"type":"Subscription",` + interactions + `,` + params + `}`},
		"CDex: the Task topic, $status": {SubscriptionCapability{SearchParams: subscribeSearchParams, StatusOperation: true, TopicCanonicals: []string{hrexTaskTopic}},
			`{` + topicExt + `,"type":"Subscription",` + interactions + `,` + params + `,` + status + `}`},
		"the Task topic, no $status": {SubscriptionCapability{SearchParams: subscribeSearchParams, TopicCanonicals: []string{hrexTaskTopic}},
			`{` + topicExt + `,"type":"Subscription",` + interactions + `,` + params + `}`},
		"two topics, one parameter": {SubscriptionCapability{SearchParams: subscribeSearchParams[:1], TopicCanonicals: []string{hrexTaskTopic, "http://example.org/SubscriptionTopic/b|1.0"}},
			`{"extension":[{"url":"` + backportSubscriptionTopicCanonicalExt + `","valueCanonical":"` + hrexTaskTopic + `"},{"url":"` + backportSubscriptionTopicCanonicalExt + `","valueCanonical":"http://example.org/SubscriptionTopic/b|1.0"}],"type":"Subscription",` + interactions +
				`,"searchParam":[{"name":"url","definition":"http://hl7.org/fhir/SearchParameter/Subscription-url","type":"uri"}]}`},
	} {
		for base, stmt := range subscriptionBaseStatements(t) {
			t.Run(name+"/"+base, func(t *testing.T) {
				in := bytes.Clone(stmt)
				out, err := WithSubscription(stmt, row.c)
				if err != nil {
					t.Fatalf("WithSubscription: %v", err)
				}
				if !bytes.Equal(stmt, in) {
					t.Fatal("WithSubscription changed its input")
				}
				again, err := WithSubscription(stmt, row.c)
				if err != nil || !bytes.Equal(out, again) {
					t.Fatal("WithSubscription is not deterministic")
				}
				var got struct {
					Rest []struct {
						Mode     string            `json:"mode"`
						Resource []json.RawMessage `json:"resource"`
					} `json:"rest"`
				}
				if err := json.Unmarshal(out, &got); err != nil || len(got.Rest) != 1 || got.Rest[0].Mode != "server" {
					t.Fatalf("output %s: %v", out, err)
				}
				res := got.Rest[0].Resource
				if last := string(res[len(res)-1]); last != row.want {
					t.Fatalf("Subscription entry\n%s\nwant\n%s", last, row.want)
				}
				// Everything around the entry is the statement as built, byte
				// for byte: the output is the input with the entry inserted at
				// the end of the server rest's resources.
				cut := bytes.Replace(out, []byte(","+row.want+"]"), []byte("]"), 1)
				if !bytes.Equal(cut, stmt) {
					t.Fatalf("around the entry, the output\n%s\nis not the statement\n%s", cut, stmt)
				}
				if _, err := fhir.UnmarshalCapabilityStatement(out); err != nil {
					t.Fatalf("the output is not a CapabilityStatement: %v", err)
				}
			})
		}
	}
}

// Each search parameter type code reads as itself, and the output carries it
// as given.
func TestWithSubscription_EverySearchParamType(t *testing.T) {
	stmt := subscriptionBaseStatements(t)["provider ingress"]
	for code := range searchParamTypes {
		out, err := WithSubscription(stmt, SubscriptionCapability{SearchParams: []CapabilitySearchParam{{"p", "http://example.org/SearchParameter/p", code}}})
		if err != nil || !strings.Contains(string(out), `"searchParam":[{"name":"p","definition":"http://example.org/SearchParameter/p","type":"`+code+`"}]`) {
			t.Fatalf("%s: %v %s", code, err, out)
		}
	}
	if len(searchParamTypes) != 9 {
		t.Fatalf("%d search parameter types, want R4's 9", len(searchParamTypes))
	}
}

// Every statement and capability WithSubscription cannot honestly compose
// onto is refused, as ErrSubscriptionCapability: the rejection rows.
func TestWithSubscription_Refuses(t *testing.T) {
	stmt := subscriptionBaseStatements(t)["payer CDex provider reads"]
	ok := SubscriptionCapability{SearchParams: subscribeSearchParams, StatusOperation: true, TopicCanonicals: []string{hrexTaskTopic}}
	withSub, err := WithSubscription(stmt, ok)
	if err != nil {
		t.Fatal(err)
	}
	mustMarshal := func(cs fhir.CapabilityStatement) []byte {
		b, err := json.Marshal(cs)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base, err := fhir.UnmarshalCapabilityStatement(stmt)
	if err != nil {
		t.Fatal(err)
	}
	noRest, clientOnly, twoServers := base, base, base
	noRest.Rest = nil
	clientOnly.Rest = []fhir.CapabilityStatementRest{base.Rest[0]}
	clientOnly.Rest[0].Mode = fhir.RestfulCapabilityModeClient
	twoServers.Rest = []fhir.CapabilityStatementRest{base.Rest[0], base.Rest[0]}
	param := func(p CapabilitySearchParam) SubscriptionCapability {
		return SubscriptionCapability{SearchParams: []CapabilitySearchParam{p}}
	}
	const (
		notCS      = "not a CapabilityStatement"
		unreadable = "cannot be read"
		notAsGiven = "would not write back as given"
		noServer   = "has no rest of mode server"
		twoServer  = "more than one rest of mode server"
		already    = "already declares Subscription"
		noParams   = "names its search parameters"
		paramName  = "has no name, or is named twice"
		paramDef   = "has no absolute definition"
		paramType  = "not a search parameter type"
		topicBad   = "is not absolute, or is named twice"
	)
	for name, row := range map[string]struct {
		stmt []byte
		c    SubscriptionCapability
		why  string
	}{
		// Statements.
		"not JSON":                            {[]byte("<CapabilityStatement/>"), ok, notCS},
		"empty":                               {nil, ok, notCS},
		"another resource":                    {[]byte(`{"resourceType":"Bundle","type":"searchset"}`), ok, notCS},
		"no resourceType":                     {bytes.Replace(stmt, []byte(`,"resourceType":"CapabilityStatement"`), nil, 1), ok, notCS},
		"resourceType in another case":        {bytes.Replace(stmt, []byte(`"resourceType":"CapabilityStatement"`), []byte(`"resourceType":"capabilitystatement"`), 1), ok, notCS},
		"an unknown resource type":            {bytes.Replace(stmt, []byte(`"type":"Task"`), []byte(`"type":"Tusk"`), 1), ok, unreadable},
		"a member of the wrong type":          {bytes.Replace(stmt, []byte(`{"status":"active"`), []byte(`{"status":1`), 1), ok, unreadable},
		"a member the model does not read":    {bytes.Replace(stmt, []byte(`{"status"`), []byte(`{"_status":{"id":"x"},"status"`), 1), ok, notAsGiven},
		"a member no CapabilityStatement has": {bytes.Replace(stmt, []byte(`{"status"`), []byte(`{"x-vendor":1,"status"`), 1), ok, notAsGiven},
		"written another way":                 {append(bytes.Clone(stmt), '\n'), ok, notAsGiven},
		"no rest":                             {mustMarshal(noRest), ok, noServer},
		"only a client rest":                  {mustMarshal(clientOnly), ok, noServer},
		"two server rests":                    {mustMarshal(twoServers), ok, twoServer},
		"already declares Subscription":       {withSub, ok, already},
		// Capabilities.
		"no search parameter":        {stmt, SubscriptionCapability{StatusOperation: true}, noParams},
		"a parameter with no name":   {stmt, param(CapabilitySearchParam{"", "http://hl7.org/fhir/SearchParameter/Subscription-url", "uri"}), paramName},
		"a parameter named twice":    {stmt, SubscriptionCapability{SearchParams: []CapabilitySearchParam{subscribeSearchParams[0], subscribeSearchParams[0]}}, paramName},
		"no definition":              {stmt, param(CapabilitySearchParam{"url", "", "uri"}), paramDef},
		"a relative definition":      {stmt, param(CapabilitySearchParam{"url", "SearchParameter/Subscription-url", "uri"}), paramDef},
		"a definition with a space":  {stmt, param(CapabilitySearchParam{"url", "http://hl7.org/fhir/Search Parameter", "uri"}), paramDef},
		"no type":                    {stmt, param(CapabilitySearchParam{"url", "http://hl7.org/fhir/SearchParameter/Subscription-url", ""}), paramType},
		"a type in another case":     {stmt, param(CapabilitySearchParam{"url", "http://hl7.org/fhir/SearchParameter/Subscription-url", "URI"}), paramType},
		"a type that is no type":     {stmt, param(CapabilitySearchParam{"url", "http://hl7.org/fhir/SearchParameter/Subscription-url", "url"}), paramType},
		"a type in quotes":           {stmt, param(CapabilitySearchParam{"url", "http://hl7.org/fhir/SearchParameter/Subscription-url", `"uri"`}), paramType},
		"an empty topic":             {stmt, SubscriptionCapability{SearchParams: subscribeSearchParams, TopicCanonicals: []string{""}}, topicBad},
		"a relative topic":           {stmt, SubscriptionCapability{SearchParams: subscribeSearchParams, TopicCanonicals: []string{"SubscriptionTopic/Task"}}, topicBad},
		"a topic named twice":        {stmt, SubscriptionCapability{SearchParams: subscribeSearchParams, TopicCanonicals: []string{hrexTaskTopic, hrexTaskTopic}}, topicBad},
		"a topic with a line break":  {stmt, SubscriptionCapability{SearchParams: subscribeSearchParams, TopicCanonicals: []string{hrexTaskTopic + "\n"}}, topicBad},
		"a topic with only a scheme": {stmt, SubscriptionCapability{SearchParams: subscribeSearchParams, TopicCanonicals: []string{"http:"}}, topicBad},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := WithSubscription(row.stmt, row.c)
			if !errors.Is(err, ErrSubscriptionCapability) || out != nil || !strings.Contains(err.Error(), row.why) {
				t.Fatalf("WithSubscription = %s, %v; want ErrSubscriptionCapability, %q", out, err, row.why)
			}
		})
	}
	// The rows above change exactly what they name: the statement they
	// start from composes.
	if !strings.Contains(string(stmt), `{"status":"active"`) || !strings.Contains(string(stmt), `"type":"Task"`) {
		t.Fatal("the rows' edits no longer reach the statement")
	}
}

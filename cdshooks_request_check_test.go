package shnsdk

import (
	"slices"
	"strings"
	"testing"
)

// cdsReqAuth is a conformant fhirServer and fhirAuthorization, so a request
// carrying them is conformant at CRD 2.2 too.
const cdsReqAuth = `"fhirServer":"https://ehr.example.org/fhir","fhirAuthorization":{"access_token":"t","token_type":"Bearer","expires_in":300,"scope":"user/Patient.read","subject":"crd-service"},`

const cdsReqDrafts = `{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"ServiceRequest/sr-1","resource":{"resourceType":"ServiceRequest","id":"sr-1"}}]}`

// cdsReq is an order-select request whose context is ctx, with the base
// envelope's members replaced by envelope when it is not "".
func cdsReq(envelope, ctx string) string {
	if envelope == "" {
		envelope = `"hook":"order-select","hookInstance":"` + crdReqHookInst + `",` + cdsReqAuth
	}
	return `{` + envelope + `"context":` + ctx + `,"prefetch":{"patient":{"resourceType":"Patient","id":"p1"},"coverage":null}}`
}

const cdsSelectCtx = `{"userId":"PractitionerRole/pr-1","patientId":"p1","selections":["ServiceRequest/sr-1"],"draftOrders":` + cdsReqDrafts + `}`

func cdsSignReq(ctx string) string {
	return strings.Replace(cdsReq("", ctx), `"order-select"`, `"order-sign"`, 1)
}

func cdsDispatchReq(ctx string) string {
	return strings.Replace(cdsReq("", ctx), `"order-select"`, `"order-dispatch"`, 1)
}

const cdsDispatchCtx = `{"patientId":"p1","dispatchedOrders":["DeviceRequest/dr-1"],"performer":"Organization/dme-1"}`

// requestRow is a certifierRow whose negative may also draw the rules in
// also, reported before it: a member CRD 2.2 requires cannot be dropped alone
// when the other CRD 2.2 member is the only one present.
type requestRow struct {
	certifierRow
	also []string
}

func requestCertifierRows() []requestRow {
	inst := `"hookInstance":"` + crdReqHookInst + `",`
	auth := func(members string) string {
		return `"hook":"order-select",` + inst + `"fhirServer":"https://ehr.example.org/fhir","fhirAuthorization":{` + members + `},`
	}
	const authOK = `"access_token":"t","token_type":"Bearer","expires_in":300,"scope":"user/Patient.read","subject":"crd-service"`
	rows := []certifierRow{
		{"request.json", "2.2", cdsReq("", cdsSelectCtx), `{"hook":"order-select","hook":"order-sign"}`, ""},
		{"request.object", "2.2", cdsReq("", cdsSelectCtx), `[]`, ""},
		{"request.hook", "2.2", cdsReq("", cdsSelectCtx), cdsReq(inst+cdsReqAuth, `{}`), "hook"},
		{"request.hookInstance", "2.2", cdsReq("", cdsSelectCtx), cdsReq(`"hook":"order-select","hookInstance":"call-1",`+cdsReqAuth, cdsSelectCtx), "hookInstance"},
		{"request.fhirServer", "2.0", cdsReq("", cdsSelectCtx), cdsReq(`"hook":"order-select",`+inst+`"fhirServer":7,`, cdsSelectCtx), "fhirServer"},
		{"request.fhirServer.authorization", "2.0", cdsReq("", cdsSelectCtx),
			cdsReq(`"hook":"order-select",`+inst+`"fhirAuthorization":{`+authOK+`},`, cdsSelectCtx), "fhirServer"},
		{"request.fhirAuthorization", "2.0", cdsReq("", cdsSelectCtx),
			cdsReq(`"hook":"order-select",`+inst+`"fhirServer":"https://ehr.example.org/fhir","fhirAuthorization":"Bearer t",`, cdsSelectCtx), "fhirAuthorization"},
		{"fhirAuthorization.access_token", "2.2", cdsReq(auth(authOK), cdsSelectCtx),
			cdsReq(auth(strings.Replace(authOK, `"access_token":"t",`, ``, 1)), cdsSelectCtx), "fhirAuthorization.access_token"},
		{"fhirAuthorization.token_type", "2.2", cdsReq(auth(authOK), cdsSelectCtx),
			cdsReq(auth(strings.Replace(authOK, `"Bearer"`, `"bearer"`, 1)), cdsSelectCtx), "fhirAuthorization.token_type"},
		{"fhirAuthorization.expires_in", "2.2", cdsReq(auth(authOK), cdsSelectCtx),
			cdsReq(auth(strings.Replace(authOK, `300`, `300.5`, 1)), cdsSelectCtx), "fhirAuthorization.expires_in"},
		{"fhirAuthorization.scope", "2.2", cdsReq(auth(authOK), cdsSelectCtx),
			cdsReq(auth(strings.Replace(authOK, `"user/Patient.read"`, `""`, 1)), cdsSelectCtx), "fhirAuthorization.scope"},
		{"fhirAuthorization.subject", "2.2", cdsReq(auth(authOK), cdsSelectCtx),
			cdsReq(auth(strings.Replace(authOK, `,"subject":"crd-service"`, ``, 1)), cdsSelectCtx), "fhirAuthorization.subject"},
		{"fhirAuthorization.patient", "2.2", cdsReq(auth(authOK+`,"patient":"p1"`), cdsSelectCtx),
			cdsReq(auth(authOK+`,"patient":1`), cdsSelectCtx), "fhirAuthorization.patient"},
		{"fhirAuthorization.patient.scope", "2.2",
			cdsReq(auth(strings.Replace(authOK, `user/Patient.read`, `patient/Patient.read launch`, 1)+`,"patient":"p1"`), cdsSelectCtx),
			cdsReq(auth(strings.Replace(authOK, `user/Patient.read`, `launch patient/Patient.read`, 1)), cdsSelectCtx), "fhirAuthorization.patient"},
		{"request.context", "2.2", cdsReq("", cdsSelectCtx), cdsReq("", `[]`), "context"},
		{"request.prefetch", "2.2", cdsReq("", cdsSelectCtx),
			`{"hook":"order-select",` + inst + cdsReqAuth + `"context":` + cdsSelectCtx + `,"prefetch":[]}`, "prefetch"},
		{"prefetch.value", "2.2", cdsReq("", cdsSelectCtx), strings.Replace(cdsReq("", cdsSelectCtx), `"coverage":null`, `"coverage":"Coverage/c1"`, 1), "prefetch.coverage"},
		{"request.extension", "2.2",
			strings.Replace(cdsReq("", cdsSelectCtx), `"context":`, `"extension":{"org.example.flag":true},"context":`, 1),
			strings.Replace(cdsReq("", cdsSelectCtx), `"context":`, `"extension":["flag"],"context":`, 1), "extension"},
		{"context.userId", "2.2", cdsSignReq(cdsSelectCtx), cdsSignReq(strings.Replace(cdsSelectCtx, `"userId":"PractitionerRole/pr-1",`, ``, 1)), "context.userId"},
		{"context.patientId", "2.2", cdsReq("", cdsSelectCtx), cdsReq("", strings.Replace(cdsSelectCtx, `"patientId":"p1"`, `"patientId":""`, 1)), "context.patientId"},
		{"context.encounterId", "2.2",
			cdsReq("", strings.Replace(cdsSelectCtx, `"patientId":"p1",`, `"patientId":"p1","encounterId":"e1",`, 1)),
			cdsReq("", strings.Replace(cdsSelectCtx, `"patientId":"p1",`, `"patientId":"p1","encounterId":["e1"],`, 1)), "context.encounterId"},
		{"context.draftOrders", "2.2", cdsSignReq(cdsSelectCtx),
			cdsSignReq(strings.Replace(cdsSelectCtx, `"resourceType":"Bundle"`, `"resourceType":"List"`, 1)), "context.draftOrders"},
		{"context.selections", "2.2", cdsReq("", cdsSelectCtx), cdsReq("", strings.Replace(cdsSelectCtx, `["ServiceRequest/sr-1"]`, `[]`, 1)), "context.selections"},
		{"context.selections.draftOrders", "2.2", cdsReq("", cdsSelectCtx),
			cdsReq("", strings.Replace(cdsSelectCtx, `["ServiceRequest/sr-1"]`, `["ServiceRequest/sr-2"]`, 1)), "context.selections[0]"},
		{"context.dispatchedOrders", "2.2", cdsDispatchReq(cdsDispatchCtx),
			cdsDispatchReq(strings.Replace(cdsDispatchCtx, `["DeviceRequest/dr-1"]`, `"DeviceRequest/dr-1"`, 1)), "context.dispatchedOrders"},
		{"context.performer", "2.2", cdsDispatchReq(cdsDispatchCtx),
			cdsDispatchReq(strings.Replace(cdsDispatchCtx, `,"performer":"Organization/dme-1"`, ``, 1)), "context.performer"},
		{"context.fulfillmentTasks", "2.2",
			cdsDispatchReq(strings.Replace(cdsDispatchCtx, `}`, `,"fulfillmentTasks":[{"resourceType":"Task","id":"t1"}]}`, 1)),
			cdsDispatchReq(strings.Replace(cdsDispatchCtx, `}`, `,"fulfillmentTasks":[{"resourceType":"ServiceRequest","id":"t1"}]}`, 1)), "context.fulfillmentTasks"},
		{"crd.fhirAuthorization", "2.2", cdsReq("", cdsSelectCtx),
			cdsReq(`"hook":"order-select",`+inst+`"fhirServer":"https://ehr.example.org/fhir",`, cdsSelectCtx), "fhirAuthorization"},
		{"crd.selections", "2.2", cdsReq("", cdsSelectCtx),
			cdsReq("", strings.NewReplacer(`["ServiceRequest/sr-1"]`, `["ServiceRequest/sr 1"]`, `"id":"sr-1"`, `"id":"sr 1"`).Replace(cdsSelectCtx)), "context.selections[0]"},
		{"crd.dispatchedOrders", "2.2", cdsDispatchReq(cdsDispatchCtx),
			cdsDispatchReq(strings.Replace(cdsDispatchCtx, `["DeviceRequest/dr-1"]`, `[]`, 1)), "context.dispatchedOrders"},
		{"line", "2.1", cdsReq("", cdsSelectCtx), cdsReq("", cdsSelectCtx), ""},
	}
	out := make([]requestRow, 0, len(rows)+1)
	for _, r := range rows {
		out = append(out, requestRow{certifierRow: r})
	}
	// Without fhirAuthorization, so request.fhirServer.authorization cannot
	// fire; crd.fhirAuthorization fires with it.
	return append(out, requestRow{certifierRow{"crd.fhirServer", "2.2", cdsReq("", cdsSelectCtx),
		cdsReq(`"hook":"order-select",`+inst, cdsSelectCtx), "fhirServer"}, []string{"crd.fhirAuthorization@fhirAuthorization"}})
}

// TestCDSHooksRequestCertifierRules settles each request certifier rule with
// one conformant and one non-conformant request: the conformant one yields no
// violation, the non-conformant one exactly the rule's violation at its path.
func TestCDSHooksRequestCertifierRules(t *testing.T) {
	rows := requestCertifierRows()
	covered := map[string]bool{}
	for _, r := range rows {
		covered[r.rule] = true
		t.Run(r.rule, func(t *testing.T) {
			if got := CheckCDSHooksRequest([]byte(r.positive), r.line); len(got) != 0 {
				t.Fatalf("conformant request reported %+v\n%s", got, r.positive)
			}
			negLine := r.line
			if r.rule == "line" {
				negLine = "9.9"
			}
			got := CheckCDSHooksRequest([]byte(r.negative), negLine)
			var rules []string
			for _, v := range got {
				rules = append(rules, v.Rule+"@"+v.Path)
			}
			want := append([]string{r.rule + "@" + r.path}, r.also...)
			slices.Sort(rules)
			slices.Sort(want)
			if !slices.Equal(rules, want) {
				t.Fatalf("want exactly %v, got %v\n%s", want, rules, r.negative)
			}
			wantSev := SeverityError
			if r.rule == "fhirAuthorization.patient.scope" {
				wantSev = SeverityWarning
			}
			for _, v := range got {
				if v.Rule == r.rule && v.Severity != wantSev {
					t.Fatalf("severity %q, want %q", v.Severity, wantSev)
				}
			}
		})
	}
	for _, rule := range CDSHooksRequestRules() {
		if !covered[rule.ID] {
			t.Errorf("rule %s has no certifier row", rule.ID)
		}
		if rule.Citation == "" || rule.Description == "" {
			t.Errorf("rule %s lacks a citation or description", rule.ID)
		}
		if rule.Severity != SeverityError && rule.Severity != SeverityWarning {
			t.Errorf("rule %s has severity %q", rule.ID, rule.Severity)
		}
		if strings.HasPrefix(rule.ID, "crd.") != rule.CRDOnly {
			t.Errorf("rule %s: CRDOnly %v", rule.ID, rule.CRDOnly)
		}
	}
	if len(CDSHooksRequestRules()) != len(rows) {
		t.Errorf("%d rules, %d rows", len(CDSHooksRequestRules()), len(rows))
	}
}

// TestCDSHooksRequestCertifier_CRDRulesOnlyAt22: CRD 2.0.1 and 2.1.0 publish
// no request model, so a request without fhirServer and fhirAuthorization is
// conformant plain CDS Hooks, and at 2.0 and 2.1, and only 2.2 reports both.
func TestCDSHooksRequestCertifier_CRDRulesOnlyAt22(t *testing.T) {
	body := []byte(cdsReq(`"hook":"order-select","hookInstance":"`+crdReqHookInst+`",`, cdsSelectCtx))
	for _, line := range []string{"", "2.0", "2.1"} {
		if got := CheckCDSHooksRequest(body, line); len(got) != 0 {
			t.Errorf("line %q: %+v", line, got)
		}
	}
	got := CheckCDSHooksRequest(body, "2.2")
	want := []Violation{{"crd.fhirServer", "fhirServer", SeverityError}, {"crd.fhirAuthorization", "fhirAuthorization", SeverityError}}
	if !slices.Equal(got, want) {
		t.Errorf("line 2.2: %+v, want %+v", got, want)
	}
}

// TestCDSHooksRequestCertifier_BuiltRequests: every request BuildCRDRequest
// builds is conformant CDS Hooks at every line, apart from the fhirServer and
// fhirAuthorization CRD 2.2 asks for and the network omits by design.
func TestCDSHooksRequestCertifier_BuiltRequests(t *testing.T) {
	sign := orderSelectInputs()
	sign.Hook, sign.Selections = "order-sign", nil
	for name, in := range map[string]CRDRequestInputs{"order-select": orderSelectInputs(), "order-sign": sign, "order-dispatch": dispatchInputs()} {
		body, err := BuildCRDRequest(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, line := range []string{"", "2.0", "2.1"} {
			if got := CheckCDSHooksRequest(body, line); len(got) != 0 {
				t.Errorf("%s at %q: %+v", name, line, got)
			}
		}
		var rules []string
		for _, v := range CheckCDSHooksRequest(body, "2.2") {
			rules = append(rules, v.Rule)
		}
		if !slices.Equal(rules, []string{"crd.fhirServer", "crd.fhirAuthorization"}) {
			t.Errorf("%s at 2.2: %v", name, rules)
		}
	}
}

// TestCDSHooksRequestCertifier_UnknownHookChecksTheEnvelope: a hook whose
// context the certifier does not know has its envelope checked and its
// context taken as any object.
func TestCDSHooksRequestCertifier_UnknownHookChecksTheEnvelope(t *testing.T) {
	body := []byte(`{"hook":"patient-view","hookInstance":"` + crdReqHookInst + `","context":{"patientId":7}}`)
	if got := CheckCDSHooksRequest(body, ""); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// TestCDSHooksRequestCertifier_ReportsEveryViolation: the certifier lists
// every problem, in the order it checks (prefetch keys sorted), rather than
// stopping at the first.
func TestCDSHooksRequestCertifier_ReportsEveryViolation(t *testing.T) {
	body := []byte(`{"hook":"order-select","hookInstance":"x","context":{"patientId":"p1","selections":["ServiceRequest/sr-9"],"draftOrders":` + cdsReqDrafts + `},"prefetch":{"b":7,"a":"x"}}`)
	var rules []string
	for _, v := range CheckCDSHooksRequest(body, "2.2") {
		rules = append(rules, v.Rule+"@"+v.Path)
	}
	want := []string{
		"request.hookInstance@hookInstance",
		"crd.fhirServer@fhirServer",
		"crd.fhirAuthorization@fhirAuthorization",
		"context.userId@context.userId",
		"context.selections.draftOrders@context.selections[0]",
		"prefetch.value@prefetch.a",
		"prefetch.value@prefetch.b",
	}
	if !slices.Equal(rules, want) {
		t.Fatalf("got %v\nwant %v", rules, want)
	}
}

// TestCDSHooksRequestCertifier_EachHookItsContext: a context rule shared by
// hooks fires in each hook's own branch, not only in the one its table row
// mutates.
func TestCDSHooksRequestCertifier_EachHookItsContext(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{"order-select userId", cdsReq("", strings.Replace(cdsSelectCtx, `"userId":"PractitionerRole/pr-1",`, ``, 1)), "context.userId@context.userId"},
		{"order-select encounterId", cdsReq("", strings.Replace(cdsSelectCtx, `"patientId":"p1",`, `"patientId":"p1","encounterId":7,`, 1)), "context.encounterId@context.encounterId"},
		{"order-select draftOrders", cdsReq("", strings.Replace(cdsSelectCtx, `"resourceType":"Bundle"`, `"resourceType":"List"`, 1)), "context.draftOrders@context.draftOrders"},
		{"order-sign patientId", cdsSignReq(strings.Replace(cdsSelectCtx, `"patientId":"p1",`, ``, 1)), "context.patientId@context.patientId"},
		{"order-sign encounterId", cdsSignReq(strings.Replace(cdsSelectCtx, `"patientId":"p1",`, `"patientId":"p1","encounterId":7,`, 1)), "context.encounterId@context.encounterId"},
		{"order-dispatch patientId", cdsDispatchReq(strings.Replace(cdsDispatchCtx, `"patientId":"p1",`, ``, 1)), "context.patientId@context.patientId"},
	} {
		var got []string
		for _, v := range CheckCDSHooksRequest([]byte(c.body), "2.2") {
			got = append(got, v.Rule+"@"+v.Path)
		}
		if !slices.Equal(got, []string{c.want}) {
			t.Errorf("%s: got %v, want [%s]\n%s", c.name, got, c.want, c.body)
		}
	}
}

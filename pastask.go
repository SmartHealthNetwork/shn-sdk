package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// The Da Vinci PAS pended-response Task (profile-task).
//
// A pended PAS response carries a Task for each kind of information the payer
// asks for. The facts below are read from the published PAS packages
// (hl7.fhir.us.davinci-pas 2.0.1, 2.1.0 and 2.2.1,
// StructureDefinition-profile-task, CodeSystem-PASTempCodes and
// ValueSet-PASTaskCodes):
//
//   - identifier is 1..* ("Payers tracking identifier"); status is 1..1 and
//     bound (required) to the HRex task status value set (requested,
//     accepted, rejected, in-progress, failed, completed, on-hold); intent is
//     "order"; for is 1..1 (the PAS Beneficiary).
//   - code is 1..1 and bound (required) to PASTaskCodes, which holds two
//     PASTempCodes codes at every line: attachment-request-code and
//     attachment-request-questionnaire.
//   - reasonCode is 1..1, fixed to PASTempCodes#priorAuthorization;
//     reasonReference is 1..1, a Reference to the PAS Claim.
//   - input is 2..*. input:PayerURL is 1..1 (type PASTempCodes#payer-url,
//     valueUrl).
//   - input:AttachmentsNeeded (type attachments-needed) is 0..* with a
//     valueCodeableConcept: at 2.0.1 from AttachmentRequestCodes (LOINC class
//     ATTACH, or X12 755); at 2.1.0 from pas-loinc-attachment-codes (LOINC);
//     at 2.2.1 from the LOINC value set valid-hl7-attachment-requests.
//   - Questionnaires: at 2.0.1, input:QuestionnairesNeeded (type
//     questionnaires-needed) with a valueIdentifier; at 2.1.0 and 2.2.1,
//     input:QuestionnaireContext (type questionnaire-context) with a
//     valueString, the context a later DTR request carries.
//   - Every attachment and questionnaire input carries the request line it is
//     about in a line-number extension (1..1): extension-paLineNumber
//     (valueInteger) at 2.0.1 and 2.1.0, extension-serviceLineNumber
//     (valuePositiveInt) at 2.2.1.
//   - Invariants: a Task coded attachment-request-code has an
//     attachments-needed input (AttachmentNeeded, every line); a Task coded
//     attachment-request-questionnaire has a questionnaires-needed input at
//     2.0.1 (QuestionnaireNeeded) and a questionnaire-context input at 2.1.0
//     and 2.2.1 (QuestionnaireContext).
//   - requester and owner are 1..1, each with identifier 1..1, at 2.0.1
//     (both described as "Payer ID"); at 2.1.0 and 2.2.1 they are 0..1 with
//     identifier 1..1 (both described as "Provider ID - only send the
//     identifier"). The IG's example Task (AdditionalInformationTaskExample,
//     the same at all three lines) sends one NPI identifier
//     (http://hl7.org/fhir/sid/us-npi) as both requester and owner. Because
//     the published texts disagree on whose identifier these are, the
//     builder never derives them: the payer supplies both.
//
// BuildPendedTasks builds these Tasks from facts only the payer has; it never
// invents an identifier, a status, a follow-up URL or a requested item. A
// Task a payer sent is never rebuilt: ParsePendedResponseDetail reads it and
// keeps its bytes. The CDex attachment-request Task (BuildCDexTaskDataRequest)
// is a different contract with its own profile and codes.

// PASTaskProfile is the Da Vinci PAS pended-response Task profile. The Task
// builder declares it with the line's package version (for example
// "…/profile-task|2.0.1").
const PASTaskProfile = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-task"

const (
	// PASTempCodesSystem is the PAS temporary code system every Task code,
	// reason and input type is drawn from.
	PASTempCodesSystem = "http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes"
	// PASTaskCodeAttachments is the Task code for a request for attachments.
	PASTaskCodeAttachments = "attachment-request-code"
	// PASTaskCodeQuestionnaires is the Task code for a request for
	// questionnaires.
	PASTaskCodeQuestionnaires = "attachment-request-questionnaire"

	pasTaskReasonCode             = "priorAuthorization"
	pasTaskInputPayerURL          = "payer-url"
	pasTaskInputAttachments       = "attachments-needed"
	pasTaskInputQuestionnaires    = "questionnaires-needed"
	pasTaskInputQuestionnaireCtx  = "questionnaire-context"
	pasExtPALineNumber            = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-paLineNumber"
	pasExtServiceLineNumber       = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-serviceLineNumber"
	pasSystemLOINC                = "http://loinc.org"
	pasSystemX12AttachmentReports = "https://codesystem.x12.org/005010/755"
)

// PASIdentifier is a FHIR Identifier (system and value).
type PASIdentifier struct {
	System string `json:"system,omitempty"`
	Value  string `json:"value"`
}

// PASCoding is a FHIR Coding.
type PASCoding struct {
	System  string `json:"system"`
	Code    string `json:"code"`
	Display string `json:"display,omitempty"`
}

// PendedItem is what a payer needs before it decides one request line.
type PendedItem struct {
	// Sequence is the request Claim's item.sequence the need is about.
	Sequence int
	// AttachmentCodes are the attachment kinds the payer asks for (LOINC at
	// every line; X12 755 report types are also allowed at PAS 2.0).
	AttachmentCodes []PASCoding
	// QuestionnaireIDs identify the questionnaires the payer asks to be
	// completed. PAS 2.0 only.
	QuestionnaireIDs []PASIdentifier
	// QuestionnaireContexts are the contexts the payer gives for the
	// questionnaires to complete; a later DTR request carries one as its
	// context. PAS 2.1 and 2.2 only.
	QuestionnaireContexts []string
}

// PendedTaskInputs are the payer's facts for the Tasks of a pended response.
//
// Required at every line: ID, Identifier (system and value), Status, Patient,
// Claim, PayerURL and at least one Items entry with a need. Requester and
// Owner are required at PAS 2.0 and optional at 2.1 and 2.2.
type PendedTaskInputs struct {
	// ID is the Task.id. When both kinds of information are needed, the
	// questionnaire Task's id is ID + "-questionnaire".
	ID string
	// Identifier is the payer's tracking identifier for this request for
	// information. Both Tasks carry it when both kinds are needed.
	Identifier PASIdentifier
	// Status is the Task.status, one of the HRex task status codes:
	// requested, accepted, rejected, in-progress, failed, completed, on-hold.
	Status string
	// Patient is the Task.for reference: the request's Claim.patient.
	Patient string
	// Claim is the Task.reasonReference: the request Claim (its fullUrl, or
	// a Claim/<id> reference).
	Claim string
	// Requester and Owner are the Task.requester and Task.owner identifiers.
	Requester PASIdentifier
	Owner     PASIdentifier
	// PayerURL is the payer's follow-up endpoint (an absolute http or https
	// URL).
	PayerURL string
	// AuthoredOn, when set, is written as Task.authoredOn.
	AuthoredOn time.Time
	// Items are the needs, one per request line.
	Items []PendedItem
}

// hrexTaskStatuses is the HRex task status value set (HRex 1.1.0,
// ValueSet-hrex-task-status), which profile-task binds Task.status to.
var hrexTaskStatuses = []string{"requested", "accepted", "rejected", "in-progress", "failed", "completed", "on-hold"}

// fhirIDPattern is the FHIR id datatype.
var fhirIDPattern = regexp.MustCompile(`^[A-Za-z0-9\-.]{1,64}$`)

type pasTaskCC struct {
	Coding []PASCoding `json:"coding"`
}

type pasTaskRef struct {
	Reference string `json:"reference"`
}

type pasTaskIdentifierRef struct {
	Identifier PASIdentifier `json:"identifier"`
}

type pasTaskLineExt struct {
	URL              string `json:"url"`
	ValueInteger     *int   `json:"valueInteger,omitempty"`
	ValuePositiveInt *int   `json:"valuePositiveInt,omitempty"`
}

type pasTaskInput struct {
	Extension            []pasTaskLineExt `json:"extension,omitempty"`
	Type                 pasTaskCC        `json:"type"`
	ValueURL             *string          `json:"valueUrl,omitempty"`
	ValueCodeableConcept *pasTaskCC       `json:"valueCodeableConcept,omitempty"`
	ValueIdentifier      *PASIdentifier   `json:"valueIdentifier,omitempty"`
	ValueString          *string          `json:"valueString,omitempty"`
}

type pasTaskMetaJSON struct {
	Profile []string `json:"profile"`
}

type pasPendedTask struct {
	ResourceType    string                `json:"resourceType"`
	ID              string                `json:"id"`
	Meta            pasTaskMetaJSON       `json:"meta"`
	Identifier      []PASIdentifier       `json:"identifier"`
	Status          string                `json:"status"`
	Intent          string                `json:"intent"`
	Code            pasTaskCC             `json:"code"`
	For             pasTaskRef            `json:"for"`
	AuthoredOn      string                `json:"authoredOn,omitempty"`
	Requester       *pasTaskIdentifierRef `json:"requester,omitempty"`
	Owner           *pasTaskIdentifierRef `json:"owner,omitempty"`
	ReasonCode      pasTaskCC             `json:"reasonCode"`
	ReasonReference pasTaskRef            `json:"reasonReference"`
	Input           []pasTaskInput        `json:"input"`
}

func pasTempCode(code string) pasTaskCC {
	return pasTaskCC{Coding: []PASCoding{{System: PASTempCodesSystem, Code: code}}}
}

// BuildPendedTasks builds the Da Vinci PAS pended-response Task(s) at a PAS
// line ("2.0", "2.1", "2.2"): one Task coded attachment-request-code when
// attachments are needed, one coded attachment-request-questionnaire when
// questionnaires are needed, and both, in that order, when both are needed.
// Each Task carries the payer URL and only the needs of its own kind, each
// with the line number of the request item it is about.
//
// Refusals (an error, never a partial Task): an unknown line; a missing or
// malformed required fact (see PendedTaskInputs); a status outside the HRex
// task status codes; a Patient that is not a Patient reference; a Claim that
// is not a Claim reference; a PayerURL that is not an absolute http or https
// URL; no item, an item without a need, an item sequence below 1 or repeated;
// questionnaire identifiers at 2.1 or 2.2, or questionnaire contexts at 2.0;
// an attachment code without a system and code, or from a system the line
// does not bind (LOINC at every line, X12 755 at 2.0).
func BuildPendedTasks(line string, in PendedTaskInputs) ([][]byte, error) {
	out, err := buildPendedTasks(line, in)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: BuildPendedTasks: %w", err)
	}
	return out, nil
}

// pasClaimReference reports whether ref names a Claim: "Claim/<id>", an
// absolute URL whose last two segments are Claim/<id> (a version suffix
// allowed), or a urn:uuid fullUrl.
func pasClaimReference(ref string) bool {
	if strings.HasPrefix(ref, "urn:uuid:") {
		return len(ref) > len("urn:uuid:")
	}
	if i := strings.Index(ref, "/_history/"); i >= 0 {
		ref = ref[:i]
	}
	j := strings.LastIndex(ref, "/")
	if j < 0 {
		return false
	}
	head, id := ref[:j], ref[j+1:]
	if !fhirIDPattern.MatchString(id) {
		return false
	}
	return head == "Claim" || strings.HasSuffix(head, "/Claim")
}

// validIdentifier reports whether id has both a system and a value.
func validIdentifier(id PASIdentifier) bool {
	return strings.TrimSpace(id.System) != "" && strings.TrimSpace(id.Value) != ""
}

func buildPendedTasks(line string, in PendedTaskInputs) ([][]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("unknown PAS line %q", line)
	}
	switch {
	case !fhirIDPattern.MatchString(in.ID):
		return nil, fmt.Errorf("Task id %q is not a FHIR id", in.ID)
	case !validIdentifier(in.Identifier):
		return nil, errors.New("the payer's Task identifier (system and value) is required")
	case !slices.Contains(hrexTaskStatuses, in.Status):
		return nil, fmt.Errorf("Task status %q is not an HRex task status (%s)", in.Status, strings.Join(hrexTaskStatuses, ", "))
	case patientKey(in.Patient) == "":
		return nil, fmt.Errorf("Task for %q is not a Patient reference", in.Patient)
	case in.Claim == "":
		return nil, errors.New("the request Claim reference (Task.reasonReference) is required")
	case !pasClaimReference(in.Claim):
		return nil, fmt.Errorf("Task reasonReference %q is not a Claim reference", in.Claim)
	}
	if err := checkPayerURL(in.PayerURL); err != nil {
		return nil, err
	}
	requester, owner := in.Requester, in.Owner
	for _, p := range []struct {
		name string
		id   PASIdentifier
	}{{"requester", requester}, {"owner", owner}} {
		empty := p.id.System == "" && p.id.Value == ""
		switch {
		case empty && line == "2.0":
			return nil, fmt.Errorf("Task %s identifier is required at PAS 2.0", p.name)
		case !empty && !validIdentifier(p.id):
			return nil, fmt.Errorf("Task %s identifier needs a system and a value", p.name)
		}
	}
	if len(in.Items) == 0 {
		return nil, errors.New("a pended response names at least one needed item")
	}

	lineExt := func(seq int) []pasTaskLineExt {
		n := seq
		if line == "2.2" {
			return []pasTaskLineExt{{URL: pasExtServiceLineNumber, ValuePositiveInt: &n}}
		}
		return []pasTaskLineExt{{URL: pasExtPALineNumber, ValueInteger: &n}}
	}
	items := slices.Clone(in.Items)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	var attachments, questionnaires []pasTaskInput
	for i, it := range items {
		if it.Sequence < 1 {
			return nil, fmt.Errorf("item %d: sequence %d is below 1", i, it.Sequence)
		}
		if i > 0 && items[i-1].Sequence == it.Sequence {
			return nil, fmt.Errorf("item sequence %d is listed twice", it.Sequence)
		}
		if len(it.AttachmentCodes)+len(it.QuestionnaireIDs)+len(it.QuestionnaireContexts) == 0 {
			return nil, fmt.Errorf("item %d names no needed attachment or questionnaire", it.Sequence)
		}
		switch {
		case line == "2.0" && len(it.QuestionnaireContexts) > 0:
			return nil, fmt.Errorf("item %d: questionnaire contexts are not defined at PAS 2.0.1 (use QuestionnaireIDs)", it.Sequence)
		case line != "2.0" && len(it.QuestionnaireIDs) > 0:
			return nil, fmt.Errorf("item %d: questionnaire identifiers are not defined at PAS %s (use QuestionnaireContexts)", it.Sequence, def.PackageVersion)
		}
		for _, c := range it.AttachmentCodes {
			if strings.TrimSpace(c.System) == "" || strings.TrimSpace(c.Code) == "" {
				return nil, fmt.Errorf("item %d: an attachment code needs a system and a code", it.Sequence)
			}
			allowed := c.System == pasSystemLOINC || (line == "2.0" && c.System == pasSystemX12AttachmentReports)
			if !allowed {
				return nil, fmt.Errorf("item %d: attachment code system %q is not bound at PAS %s", it.Sequence, c.System, def.PackageVersion)
			}
			cc := pasTaskCC{Coding: []PASCoding{c}}
			attachments = append(attachments, pasTaskInput{Extension: lineExt(it.Sequence), Type: pasTempCode(pasTaskInputAttachments), ValueCodeableConcept: &cc})
		}
		for _, q := range it.QuestionnaireIDs {
			if !validIdentifier(q) {
				return nil, fmt.Errorf("item %d: a questionnaire identifier needs a system and a value", it.Sequence)
			}
			v := q
			questionnaires = append(questionnaires, pasTaskInput{Extension: lineExt(it.Sequence), Type: pasTempCode(pasTaskInputQuestionnaires), ValueIdentifier: &v})
		}
		for _, c := range it.QuestionnaireContexts {
			if strings.TrimSpace(c) == "" {
				return nil, fmt.Errorf("item %d: a questionnaire context is empty", it.Sequence)
			}
			v := c
			questionnaires = append(questionnaires, pasTaskInput{Extension: lineExt(it.Sequence), Type: pasTempCode(pasTaskInputQuestionnaireCtx), ValueString: &v})
		}
	}

	payerURL := in.PayerURL
	build := func(id, code string, needs []pasTaskInput) ([]byte, error) {
		t := pasPendedTask{
			ResourceType:    "Task",
			ID:              id,
			Meta:            pasTaskMetaJSON{Profile: []string{PASTaskProfile + "|" + def.PackageVersion}},
			Identifier:      []PASIdentifier{in.Identifier},
			Status:          in.Status,
			Intent:          "order",
			Code:            pasTempCode(code),
			For:             pasTaskRef{Reference: in.Patient},
			ReasonCode:      pasTempCode(pasTaskReasonCode),
			ReasonReference: pasTaskRef{Reference: in.Claim},
			Input:           append([]pasTaskInput{{Type: pasTempCode(pasTaskInputPayerURL), ValueURL: &payerURL}}, needs...),
		}
		if !in.AuthoredOn.IsZero() {
			t.AuthoredOn = in.AuthoredOn.UTC().Format(time.RFC3339)
		}
		if validIdentifier(requester) {
			t.Requester = &pasTaskIdentifierRef{Identifier: requester}
		}
		if validIdentifier(owner) {
			t.Owner = &pasTaskIdentifierRef{Identifier: owner}
		}
		return json.Marshal(t)
	}
	var tasks [][]byte
	if len(attachments) > 0 {
		b, err := build(in.ID, PASTaskCodeAttachments, attachments)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, b)
	}
	if len(questionnaires) > 0 {
		id := in.ID
		if len(attachments) > 0 {
			id = in.ID + "-questionnaire"
			if !fhirIDPattern.MatchString(id) {
				return nil, fmt.Errorf("Task id %q is not a FHIR id", id)
			}
		}
		b, err := build(id, PASTaskCodeQuestionnaires, questionnaires)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, b)
	}
	return tasks, nil
}

// checkPayerURL requires an absolute http or https URL.
func checkPayerURL(raw string) error {
	if raw == "" {
		return errors.New("the payer URL (Task input payer-url) is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(raw, " \t\r\n") {
		return fmt.Errorf("the payer URL %q is not an absolute http or https URL", raw)
	}
	return nil
}

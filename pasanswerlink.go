package shnsdk

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// An amendment's attached answer (the supplemental report it carries, or, when
// it carries none, its amended QuestionnaireResponse) is documentation
// answering the payer's request for a specific item. It is linked to
// that item through Claim.item.informationSequence, which points at a new
// Claim.supportingInfo entry referencing the answer, and only where the
// payer's OWN pend ties the answer to the item: a PAS pended-response Task
// whose need on that item's line (extension-paLineNumber /
// extension-serviceLineNumber, the Claim item.sequence the need is about) is
// one the answer meets. Nothing is linked by default, and an answer no pended
// item asked for links nothing.
//
// The entry is the PAS Claim.supportingInfo:AdditionalInformation slice
// (category PASTempCodes#additionalInformation, valueReference to the
// answer), with the same shape at PAS 2.0.1 and 2.1.0; Claim.item.
// informationSequence is 0..* at every line. At PAS 2.2.1 the slice also
// requires extension:documentInformation with an X12 PWK01 report type
// (PASDef.SupportingInfoDocumentInformationRequired) that neither the pend nor
// the answer states, so at 2.2 nothing is linked rather than a report type
// minted.

// pasSupportingInfoCategoryAdditional is the PASTempCodes category of the
// Claim.supportingInfo:AdditionalInformation slice (its patternCodeableConcept
// at 2.0.1, 2.1.0 and 2.2.1).
const pasSupportingInfoCategoryAdditional = "additionalInformation"

// linkPASAnswer links answer (a resource of the amendment's Bundle, with its
// bundle-local id) to the items of claimJSON the payer's pend asks it for.
// It returns claimJSON unchanged when nothing links.
func linkPASAnswer(def PASDef, claimJSON, pend, answer []byte) ([]byte, error) {
	if def.SupportingInfoDocumentInformationRequired || len(pend) == 0 || len(answer) == 0 {
		return claimJSON, nil
	}
	asked := pasPendedItemsAnswered(pend, answer)
	if len(asked) == 0 {
		return claimJSON, nil
	}
	var head struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(answer, &head); err != nil || head.ResourceType == "" || head.ID == "" {
		return nil, fmt.Errorf("link the attached answer: the answer names no resource type and id")
	}
	var claim map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &claim); err != nil {
		return nil, fmt.Errorf("link the attached answer: %w", err)
	}
	var items []map[string]json.RawMessage
	if raw, ok := claim["item"]; ok {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("link the attached answer: items: %w", err)
		}
	}
	var infos []map[string]json.RawMessage
	if raw, ok := claim["supportingInfo"]; ok {
		if err := json.Unmarshal(raw, &infos); err != nil {
			return nil, fmt.Errorf("link the attached answer: supportingInfo: %w", err)
		}
	}
	next := 1
	for _, si := range infos {
		var seq int
		_ = json.Unmarshal(si["sequence"], &seq)
		if seq >= next {
			next = seq + 1
		}
	}
	linked := false
	for _, it := range items {
		var seq int
		if err := json.Unmarshal(it["sequence"], &seq); err != nil || !slices.Contains(asked, seq) {
			continue
		}
		var infoSeq []int
		if raw, ok := it["informationSequence"]; ok {
			if err := json.Unmarshal(raw, &infoSeq); err != nil {
				return nil, fmt.Errorf("link the attached answer: item %d informationSequence: %w", seq, err)
			}
		}
		it["informationSequence"], _ = json.Marshal(append(infoSeq, next))
		linked = true
	}
	if !linked {
		return claimJSON, nil
	}
	entry, err := json.Marshal(map[string]any{
		"sequence": next,
		"category": map[string]any{"coding": []any{map[string]any{
			"system": PASTempCodesSystem, "code": pasSupportingInfoCategoryAdditional,
		}}},
		"valueReference": map[string]any{"reference": head.ResourceType + "/" + head.ID},
	})
	if err != nil {
		return nil, err
	}
	var e map[string]json.RawMessage
	_ = json.Unmarshal(entry, &e)
	infos = append(infos, e)
	if claim["supportingInfo"], err = json.Marshal(infos); err != nil {
		return nil, err
	}
	if claim["item"], err = json.Marshal(items); err != nil {
		return nil, err
	}
	return json.Marshal(claim)
}

// pasPendedItemsAnswered returns, in order, the item sequences the payer's
// pend asks for something the answer is: a pended-response Task need on that
// line whose attachment code (system and code) is one the answer's code
// carries, or whose questionnaire the answer (a QuestionnaireResponse)
// answers — named by a questionnaires-needed identifier (PAS 2.0.1), or by
// the valueCanonical the reference payer sends there, which the parser
// reports as nonconformant and which is still the payer's own statement of
// what that line needs. A PAS 2.1/2.2 questionnaire-context is the context a
// later DTR request carries, which a QuestionnaireResponse does not state, so
// it ties nothing. A pend that does not parse as pended ties nothing.
func pasPendedItemsAnswered(pend, answer []byte) []int {
	pended, detail, err := ParsePendedResponseDetail(pend)
	if err != nil || !pended {
		return nil
	}
	var a struct {
		ResourceType  string `json:"resourceType"`
		Questionnaire string `json:"questionnaire"`
		Code          struct {
			Coding []PASCoding `json:"coding"`
		} `json:"code"`
	}
	if json.Unmarshal(answer, &a) != nil {
		return nil
	}
	isQR := a.ResourceType == "QuestionnaireResponse"
	var out []int
	add := func(seq int) {
		if seq > 0 && !slices.Contains(out, seq) {
			out = append(out, seq)
		}
	}
	for _, t := range detail.Tasks {
		for _, it := range t.Items {
			for _, need := range it.AttachmentCodes {
				for _, c := range a.Code.Coding {
					if need.System != "" && need.Code != "" && need.System == c.System && need.Code == c.Code {
						add(it.Sequence)
					}
				}
			}
			for _, id := range it.QuestionnaireIDs {
				if isQR && pasCanonicalNames(id.Value, a.Questionnaire) {
					add(it.Sequence)
				}
			}
		}
		for _, nc := range t.Nonconformant {
			if isQR && nc.Type == pasTaskInputQuestionnaires && pasCanonicalNames(nc.Text, a.Questionnaire) {
				add(nc.Sequence)
			}
		}
	}
	sort.Ints(out)
	return out
}

// pasCanonicalNames reports whether the canonical a payer named is the one a
// QuestionnaireResponse answers: the same canonical, or — FHIR canonical
// matching — a version-less canonical naming the answered one's URL.
func pasCanonicalNames(named, answered string) bool {
	if named == "" || answered == "" {
		return false
	}
	if named == answered {
		return true
	}
	url, _, versioned := strings.Cut(answered, "|")
	return versioned && !strings.Contains(named, "|") && named == url
}

package shnsdk

import "strings"

// A blank member (empty, only whitespace, or "Patient/" with nothing but
// whitespace after it: a reference's prefix read as the member) names no
// patient, as the network reads a member. Searched as a
// member identifier, a blank code matches every identifier of the member system
// (FHIR token search: "system|" names the system and any value), so a reader that
// returned one would bind whichever patient a system of record holds alone. Each
// reader's refusal of one wraps ErrBlankMember, so a caller tells it apart with
// errors.Is whatever reader refused it.

// errNoMember is a coverage eligibility request whose patient names no member: an
// empty reference, the bare "Patient/", only whitespace where the id goes, or a
// blank member read through the Patient the reference names. It is an
// *EligibilityRequestRefusal carrying the text a payer's Smart Gateway answers the
// same request with, and wraps ErrBlankMember.
var errNoMember error = namesNoOne{&EligibilityRequestRefusal{"eligibility request names its patient by a reference that names no one, so the request names no patient to bind"}}

type namesNoOne struct{ refusal *EligibilityRequestRefusal }

func (e namesNoOne) Error() string   { return e.refusal.Error() }
func (e namesNoOne) Unwrap() []error { return []error{e.refusal, ErrBlankMember} }

// errTaskForNamesNoPatient is a federated-query Task whose Task.for names no
// patient ("Patient/" with no id, or only whitespace where it goes). It wraps
// ErrBlankMember.
var errTaskForNamesNoPatient error = blankMemberError{"cdex: Task.for names no patient"}

type blankMemberError struct{ text string }

func (e blankMemberError) Error() string { return e.text }
func (e blankMemberError) Unwrap() error { return ErrBlankMember }

// msgInquiryNamesNoOne is a PAS inquiry whose Claim.patient names no member: the
// text a payer's Smart Gateway answers the same inquiry with.
const msgInquiryNamesNoOne = "cannot identify the patient: the request names its patient by a reference that names no one"

// errContinuationNamesNoOne is a continuation whose member id names no member. It
// wraps ErrBlankMember.
var errContinuationNamesNoOne error = blankMemberError{"shnsdk: continuation: the member id names no member"}

// memberNamesNoOne reports whether a member names no patient: blank once one
// "Patient/" prefix is removed, as a Smart Gateway reads a member.
func memberNamesNoOne(member string) bool {
	id, _ := strings.CutPrefix(member, "Patient/")
	return strings.TrimSpace(id) == ""
}

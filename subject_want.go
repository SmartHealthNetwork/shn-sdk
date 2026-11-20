package shnsdk

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"time"
)

// SubjectWant is what a verifier expects a token's subject to be, stated
// structurally: from the leg (whether its request, or its answer, names a
// patient) and from what the verifier itself holds. It never rides in an empty
// string: VerifyBound's empty wantSubject reads the same whether the leg names
// no patient, the verifier does not hold the patient, or the value came out
// empty by accident, and unknown is not empty. The zero value states nothing
// and is refused. The network's own verifier states its expectation the same
// way, and the SDK parity suite holds the two to one reading.
type SubjectWant struct {
	kind subjectKind
	pci  string
}

type subjectKind int

const (
	subjectUnstated subjectKind = iota
	subjectIs
	subjectNamed
	subjectNone
	subjectFromAnswer
	subjectChecked
)

// SubjectIs expects the patient the verifier holds: the token's subject must
// be pci. A pci that is empty or blank states no patient and is refused
// (ErrSubjectUnstated), never read as "do not check".
func SubjectIs(pci string) SubjectWant { return SubjectWant{kind: subjectIs, pci: pci} }

// SubjectNamed expects some patient, on a leg that names one, from a verifier
// that does not hold it (a responder that answers for the patient the token
// names): the token must name a patient identifier ("pci:" and a value).
func SubjectNamed() SubjectWant { return SubjectWant{kind: subjectNamed} }

// SubjectNone expects no patient, on a leg whose side names none (a topic
// Subscription's subscribe and signal legs).
func SubjectNone() SubjectWant { return SubjectWant{kind: subjectNone} }

// SubjectFromAnswer expects the patient an answer discloses, or none, on a
// leg that takes its patient from the answer (a payer's read of a CDex Task).
func SubjectFromAnswer() SubjectWant { return SubjectWant{kind: subjectFromAnswer} }

// SubjectChecked states that the caller checks the token's subject itself,
// after the call, by a rule of its own: the call does not check it. It is a
// statement, never a default.
func SubjectChecked() SubjectWant { return SubjectWant{kind: subjectChecked} }

// ErrSubjectUnstated is a verify whose expected subject states nothing: the
// zero SubjectWant, or SubjectIs with no patient.
var ErrSubjectUnstated = errors.New("shnsdk: no expected subject")

// ErrSubjectMismatch is a token whose subject does not fit the expected one.
// VerifyBoundWant checks the subject after every other binding and before the
// payload hash, so this error means every other binding held but the hash,
// which was not checked.
var ErrSubjectMismatch = errors.New("shnsdk: subject mismatch")

// isPatientID reports whether subject names a patient: a network patient
// identifier ("pci:" and a value).
func isPatientID(subject string) bool {
	return strings.HasPrefix(subject, "pci:") && len(subject) > len("pci:")
}

// stated reports whether w states an expectation.
func (w SubjectWant) stated() bool {
	switch w.kind {
	case subjectIs:
		return strings.TrimSpace(w.pci) != ""
	case subjectNamed, subjectNone, subjectFromAnswer, subjectChecked:
		return true
	}
	return false
}

// check reports whether subject fits w; w is stated.
func (w SubjectWant) check(subject string) error {
	fits := false
	switch w.kind {
	case subjectIs:
		fits = subject == w.pci
	case subjectNamed:
		fits = isPatientID(subject)
	case subjectNone:
		fits = subject == ""
	case subjectFromAnswer:
		fits = subject == "" || isPatientID(subject)
	case subjectChecked:
		fits = true
	}
	if !fits {
		return ErrSubjectMismatch
	}
	return nil
}

// ErrBindingUnstated is a verify that leaves one of the token's bindings
// unstated: an empty or blank expected frame, operation, correlation, holder
// or, on a token borne by an envelope, payload hash. It is the verifier's own
// fault, never a fact about the token, and is refused before the token is
// read: unknown is not empty, so an empty expectation is never read as "do
// not check".
var ErrBindingUnstated = errors.New("shnsdk: no expected binding")

// bindingsStated reports whether every expected binding states a value.
func bindingsStated(wants ...string) bool {
	for _, w := range wants {
		if strings.TrimSpace(w) == "" {
			return false
		}
	}
	return true
}

// VerifyBoundWant verifies a token borne by a sealed envelope, with every
// expectation stated: the frame, operation, correlation, holder and payload
// hash each required (an empty or blank one is refused, ErrBindingUnstated)
// and the subject stated structurally (SubjectWant; ErrSubjectUnstated).
// Both are refused before the token is read. Then, in VerifyBound's order:
// the signature and expiry, a token carrying an Involvement refused, the
// frame, operation, correlation and holder, the subject (ErrSubjectMismatch),
// and the payload hash last, strictly.
func VerifyBoundWant(t Token, authzPub ed25519.PublicKey, now time.Time, wantFrame, wantOp, wantCorr, wantHolder string, subject SubjectWant, wantPayloadHash string) error {
	if !bindingsStated(wantFrame, wantOp, wantCorr, wantHolder, wantPayloadHash) {
		return ErrBindingUnstated
	}
	if !subject.stated() {
		return ErrSubjectUnstated
	}
	if err := verifyToken(t, authzPub, now); err != nil {
		return err
	}
	if err := refuseInvolvedToken(t); err != nil {
		return err
	}
	if err := sdkVerifyBindings(t, wantFrame, wantOp, wantCorr, wantHolder, ""); err != nil {
		return err
	}
	if err := subject.check(t.Subject); err != nil {
		return err
	}
	if t.PayloadHash == "" || t.PayloadHash != wantPayloadHash {
		return errors.New("shnsdk: payload hash mismatch")
	}
	return nil
}

// VerifyBoundNoPayloadWant verifies the token of a direct read, which binds
// no payload: the frame, operation, correlation and holder each required
// (ErrBindingUnstated) and the subject stated (ErrSubjectUnstated), refused
// before the token is read; a token carrying a payload hash is refused; and
// every other check is VerifyBoundWant's but the payload hash.
func VerifyBoundNoPayloadWant(t Token, pub ed25519.PublicKey, now time.Time, wantFrame, wantOp, wantCorr, wantHolder string, subject SubjectWant) error {
	if !bindingsStated(wantFrame, wantOp, wantCorr, wantHolder) {
		return ErrBindingUnstated
	}
	if !subject.stated() {
		return ErrSubjectUnstated
	}
	if t.PayloadHash != "" {
		return errors.New("shnsdk: payload hash present on non-envelope token")
	}
	if err := verifyToken(t, pub, now); err != nil {
		return err
	}
	if err := refuseInvolvedToken(t); err != nil {
		return err
	}
	if err := sdkVerifyBindings(t, wantFrame, wantOp, wantCorr, wantHolder, ""); err != nil {
		return err
	}
	return subject.check(t.Subject)
}

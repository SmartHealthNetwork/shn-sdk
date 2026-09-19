package shnsdk

import "testing"

// A continuation built from an AMENDMENT knows the claim the amendment names as
// the one it amends, as well as the amendment's own identifier.
//
// The payer's later answers go on referring to the request it first stored, so a
// continuation that knew only the amendment's identifier could not recognize a
// decision about the thing it is a continuation OF. Both are read off the bytes
// actually sent — never carried in from a caller's memory of the earlier request.
func TestNewPriorAuthContinuation_KnowsTheClaimAnAmendmentAmends(t *testing.T) {
	upd := conformantUpdateInputsFromGolden(t)
	bundle, err := BuildConformantClaimUpdateBundle(upd)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle: %v", err)
	}
	cont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-COVERED", bundle, nil)
	if err != nil {
		t.Fatalf("NewPriorAuthContinuation: %v", err)
	}
	var own, prior bool
	for _, id := range cont.ClaimIdentifiers {
		switch id.Value {
		case upd.Corr:
			own = true
		case upd.OriginalCorr:
			prior = true
		}
	}
	if !own {
		t.Fatalf("the continuation does not hold the amendment's own claim identifier %q: %+v", upd.Corr, cont.ClaimIdentifiers)
	}
	if !prior {
		t.Fatalf("the continuation does not hold the claim the amendment amends (%q): %+v", upd.OriginalCorr, cont.ClaimIdentifiers)
	}

	// REJECTION half: a SUBMISSION amends nothing, so it names one identifier and
	// no more — the reader adds only what the bytes state.
	sub := conformantSubmitInputs(t)
	subBundle, err := BuildConformantClaimBundle(sub)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	subCont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-COVERED", subBundle, nil)
	if err != nil {
		t.Fatalf("NewPriorAuthContinuation(submit): %v", err)
	}
	if len(subCont.ClaimIdentifiers) != 1 || subCont.ClaimIdentifiers[0].Value != sub.Corr {
		t.Fatalf("a submission names one claim identifier, its own: %+v", subCont.ClaimIdentifiers)
	}
}

// A continuation records the administrative reference number a payer states for
// an answer AS A WHOLE, not only the ones it states per item.
//
// The reference payer this network is measured against puts exactly one on the
// ClaimResponse's own extension and none on its items, so a reader that looked
// only at items recorded nothing for every answer anyone has ever seen from it.
//
// The second half is the part that is easy to get backwards: the recorded
// reference does NOT go on to narrow the inquiry. A payer states it while it
// holds the request and withdraws it when it decides — measured against the
// pinned image, which answers an inquiry by it while pended and stops once
// resolved — so an inquiry naming it would go blind at exactly the moment the
// decision arrived.
func TestPriorAuthContinuation_RecordsTheAnswersOwnAdministrativeReference(t *testing.T) {
	sub := conformantSubmitInputs(t)
	bundle, err := BuildConformantClaimBundle(sub)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	cont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-COVERED", bundle, nil)
	if err != nil {
		t.Fatalf("NewPriorAuthContinuation: %v", err)
	}
	trace := cont.Items[0].TraceNumber

	// The pend the reference payer sends: the administrative reference at the
	// ROOT, the item carrying only its trace number.
	pend := []byte(`{"resourceType":"ClaimResponse","id":"1806","status":"active","use":"preauthorization","outcome":"queued",` +
		`"extension":[{"url":"` + pasExtAdministrationReferenceNumber + `","valueString":"AUTH-PEND0007"}],` +
		`"patient":{"reference":"Patient/MBR-COVERED"},` +
		`"item":[{"itemSequence":1,"extension":[{"url":"` + pasExtItemTraceNumber +
		`","valueIdentifier":{"system":"` + trace.System + `","value":"` + trace.Value + `"}}]}]}`)
	if err := cont.Record(pend); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if cont.AdministrationReferenceNumber != "AUTH-PEND0007" {
		t.Fatalf("the continuation recorded administrative reference %q, want the one the payer stated at the root",
			cont.AdministrationReferenceNumber)
	}

	// And the inquiry it produces asks by the facts that outlive the pend, not by
	// the reference the payer withdraws when it decides.
	in := cont.InquiryInputs("inq", PASIdentifier{System: PASInquiryIdentifierSystem, Value: "t"},
		PASInquiryRecords{}, sub.Created)
	for _, it := range in.Items {
		if it.AdministrationReferenceNumber != "" {
			t.Fatalf("the inquiry narrows by administrative reference %q; the payer withdraws it on resolution, so the inquiry would go blind exactly when the decision arrives",
				it.AdministrationReferenceNumber)
		}
	}
	if in.Items[0].TraceNumber != trace {
		t.Fatalf("the inquiry asks by item trace number %+v, want the submitted %+v", in.Items[0].TraceNumber, trace)
	}

	// The SAME rule for the per-ITEM reference, which the reference payer states on
	// one of its two re-pend shapes (the amendment that lands after its resolution
	// timer already approved the prior pend; live capture
	// gateway/engine/testdata/br-payer/pas-update-response-amend-after-resolution.json).
	// This half is not hypothetical: it is the shape that sent a real inquiry blind.
	rePend := []byte(`{"resourceType":"ClaimResponse","id":"1806","status":"active","use":"preauthorization","outcome":"complete",` +
		`"patient":{"reference":"Patient/MBR-COVERED"},` +
		`"item":[{"itemSequence":1,"extension":[` +
		`{"url":"` + pasExtItemTraceNumber + `","valueIdentifier":{"system":"` + trace.System + `","value":"` + trace.Value + `"}},` +
		`{"url":"` + pasExtAdministrationReferenceNumber + `","valueString":"AUTH-PEND-9"}]}]}`)
	if err := cont.Record(rePend); err != nil {
		t.Fatalf("Record(item-level): %v", err)
	}
	if cont.Items[0].AdministrationReferenceNumber != "AUTH-PEND-9" {
		t.Fatalf("the continuation recorded item administrative reference %q, want the one the payer stated on the item",
			cont.Items[0].AdministrationReferenceNumber)
	}
	in = cont.InquiryInputs("inq", PASIdentifier{System: PASInquiryIdentifierSystem, Value: "t"},
		PASInquiryRecords{}, sub.Created)
	if in.Items[0].AdministrationReferenceNumber != "" {
		t.Fatalf("the inquiry narrows by the item's administrative reference %q; the payer withdraws it on resolution, so the inquiry would go blind exactly when the decision arrives",
			in.Items[0].AdministrationReferenceNumber)
	}
	// And the continuation itself is not mutated by building an inquiry from it.
	if cont.Items[0].AdministrationReferenceNumber != "AUTH-PEND-9" {
		t.Fatal("building an inquiry erased what the payer said from the continuation")
	}
}

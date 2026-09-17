package shnsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

func newDTRClientSubstrate(t *testing.T) *paFakeSubstrate {
	t.Helper()
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	return &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), paRequired: true,
	}
}

// TestClientDTR_RefusesFramedOpWithoutCapability: a framed DTR operation is
// never sent to a payer that has not declared v1op, because a receiver that
// does not know the operation header silently drops it. The refusal happens
// before anything is authorized or routed.
func TestClientDTR_RefusesFramedOpWithoutCapability(t *testing.T) {
	for _, frames := range [][]string{nil, {"v1"}, {"v1", "V1OP"}} {
		f := newDTRClientSubstrate(t)
		id, ep, payer, _ := newPATestRig(t, f)
		payer.RequestFrames = frames
		_, err := id.runOperationLeg(context.Background(), http.DefaultClient, ep, payer, "pci",
			"dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-questionnaire",
			FrameOperationQuestionnairePackage, []byte(`{"resourceType":"Parameters"}`))
		if !errors.Is(err, ErrFramedDTRUnsupported) {
			t.Fatalf("frames %q: err = %v, want ErrFramedDTRUnsupported", frames, err)
		}
		if !strings.Contains(err.Error(), "payer gateway does not support framed DTR operations (upgrade required)") {
			t.Fatalf("frames %q: error text %q", frames, err)
		}
		if f.authorizeCalls != 0 || f.capturedRequestFramed != nil {
			t.Fatalf("frames %q: the refused leg reached the network (authorize=%d)", frames, f.authorizeCalls)
		}
	}

	// An operation this SDK does not define is refused too, even to a capable
	// peer.
	f := newDTRClientSubstrate(t)
	id, ep, payer, _ := newPATestRig(t, f)
	payer.RequestFrames = []string{"v1", "v1op"}
	if _, err := id.runOperationLeg(context.Background(), http.DefaultClient, ep, payer, "pci",
		"dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-questionnaire",
		"populate", []byte(`{}`)); err == nil || f.authorizeCalls != 0 {
		t.Fatalf("unknown operation: err=%v authorize=%d", err, f.authorizeCalls)
	}
}

// TestClientDTR_LegacyEnvelopeOnlyForAuthored: RunPriorAuth sends the older
// questionnaire envelope only to a payer that has not declared v1op; a payer
// that has receives the $questionnaire-package Parameters in a framed
// operation, carrying the Coverage, the payer's own updated order, the
// questionnaire canonical with its version, and the payer's assertion id as
// context.
func TestClientDTR_LegacyEnvelopeOnlyForAuthored(t *testing.T) {
	versioned := SupportedQuestionnaireCanonical + "|2026.1"
	for _, tc := range []struct {
		name       string
		frames     []string
		assertion  string
		wantFramed bool
		wantOp     bool
	}{
		{"no request frames", nil, "assert-1", false, false},
		{"v1 only", []string{"v1"}, "assert-1", true, false},
		{"v1 and v1op", []string{"v1", "v1op"}, "assert-1", true, true},
		{"v1op only", []string{"v1op"}, "assert-1", true, true},
		{"v1op, legacy card answer", []string{"v1", "v1op"}, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDTRClientSubstrate(t)
			f.crdAssertionID = tc.assertion
			f.crdQuestionnaire = versioned
			id, ep, payer, _ := newPATestRig(t, f)
			payer.RequestFrames = tc.frames
			res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
			if err != nil {
				t.Fatalf("RunPriorAuth: %v", err)
			}
			if res.Outcome != "approved" {
				t.Fatalf("outcome %q", res.Outcome)
			}
			const tx = "dtr-questionnaire-fetch"
			if f.capturedRequestFramed[tx] != tc.wantFramed {
				t.Fatalf("framed = %v, want %v", f.capturedRequestFramed[tx], tc.wantFramed)
			}
			if tc.wantFramed && f.capturedRequestClaim[tx] != ContractPADTR20 {
				t.Fatalf("contract claim = %q", f.capturedRequestClaim[tx])
			}
			sent := f.capturedDTRFetch
			op := f.capturedRequestOperation[tx]
			if !tc.wantOp {
				if op != "" {
					t.Fatalf("operation header %q sent to a peer without v1op", op)
				}
				var env QuestionnaireFetchRequest
				if err := json.Unmarshal(sent, &env); err != nil || env.Canonical != SupportedQuestionnaireCanonical || len(env.Coverage) == 0 {
					t.Fatalf("legacy envelope = %s (%v)", sent, err)
				}
				return
			}

			if op != FrameOperationQuestionnairePackage {
				t.Fatalf("operation = %q, want %q", op, FrameOperationQuestionnairePackage)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(sent, &top); err != nil {
				t.Fatal(err)
			}
			if _, legacy := top["canonical"]; legacy {
				t.Fatalf("the legacy envelope was sent to a v1op peer: %s", sent)
			}
			_, params := decodePkg(t, sent)
			var names []string
			byName := map[string][]pkgParam{}
			for _, p := range params {
				names = append(names, p.Name)
				byName[p.Name] = append(byName[p.Name], p)
			}
			wantNames := "coverage,questionnaire"
			if tc.assertion != "" {
				wantNames = "coverage,order,questionnaire,context"
			}
			if strings.Join(names, ",") != wantNames {
				t.Fatalf("parameters %q, want %s: %s", names, wantNames, sent)
			}
			var cov struct {
				ResourceType string `json:"resourceType"`
				ID           string `json:"id"`
			}
			if err := json.Unmarshal(byName["coverage"][0].Resource, &cov); err != nil || cov.ResourceType != "Coverage" || cov.ID != "coverage-"+demoPARequest().Member {
				t.Fatalf("coverage = %s", byName["coverage"][0].Resource)
			}
			wantQ := versioned
			if tc.assertion == "" {
				wantQ = SupportedQuestionnaireCanonical // the card answer names it unversioned
			}
			if got := *byName["questionnaire"][0].ValueCanonical; got != wantQ {
				t.Fatalf("questionnaire = %q, want the payer's %q", got, wantQ)
			}
			if tc.assertion == "" {
				return
			}
			if !bytes.Equal(byName["order"][0].Resource, f.crdAnsweredOrder) {
				t.Fatalf("order is not the payer's updated order:\n got %s\nwant %s", byName["order"][0].Resource, f.crdAnsweredOrder)
			}
			if got := *byName["context"][0].ValueString; got != tc.assertion {
				t.Fatalf("context = %q, want %q", got, tc.assertion)
			}
		})
	}
}

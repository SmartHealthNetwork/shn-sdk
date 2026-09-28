package shnsdk

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// responder_refusal_test.go — which of the Responder's own refusals travel
// framed. Once the Hub's assertion and the leg token are verified and the
// payload is open, a refusal the Responder writes about the request it was
// sent is its answer: to a frame-capable requester it is sealed as a v1 frame
// (the refusal's status, application/json, {"error": msg}) into an authorized
// response leg relayed 200 to the Hub, the PARTICIPANT_PROTOCOL §6.3 rule for
// post-authentication refusals. Everything decided before that point (the
// assertion, the envelope, the metadata, the token, decryption) and a failure
// building the response leg itself stay bare, as does every refusal to a
// requester that does not decode frames.

// countingAdjudicator counts every call that reaches the payer's decision
// surface, so a refusal row can prove the request never got there.
type countingAdjudicator struct {
	inner Adjudicator
	calls atomic.Int32
}

func (a *countingAdjudicator) Eligibility(m string) (bool, string) {
	a.calls.Add(1)
	return a.inner.Eligibility(m)
}

func (a *countingAdjudicator) OrderSelect(c string) (bool, string) {
	a.calls.Add(1)
	return a.inner.OrderSelect(c)
}

func (a *countingAdjudicator) Questionnaire(c string) ([]byte, bool) {
	a.calls.Add(1)
	return a.inner.Questionnaire(c)
}

func (a *countingAdjudicator) PriorAuth(qr []byte, dr bool) (PASDecision, error) {
	a.calls.Add(1)
	return a.inner.PriorAuth(qr, dr)
}

// refusalResponder builds a Responder that stamps contract versions on its
// successes (so an unstamped refusal frame is a real property, not the
// default) with the given frame resolver and requester-key resolver.
func (h *paTestHarness) refusalResponder(t *testing.T, ident Identity, adj Adjudicator, frames func(string) []string, resolveEnc func(string) (*[32]byte, bool)) *httptest.Server {
	t.Helper()
	if resolveEnc == nil {
		resolveEnc = func(holderID string) (*[32]byte, bool) {
			if holderID == h.senderID {
				return h.senderEncPub, true
			}
			return nil, false
		}
	}
	r, err := NewResponder(ResponderConfig{
		Identity:             ident,
		AuthzURL:             h.authzSrv.URL,
		AuthzPub:             h.authzPub,
		HubTransportPub:      h.hubPub,
		ResolveEnc:           resolveEnc,
		ResolveFrames:        frames,
		StampContractVersion: true,
		Adjudicator:          adj,
		Clock:                func() time.Time { return h.now },
		Client:               h.authzSrv.Client(),
	})
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// claimWithQRItem appends item to the QuestionnaireResponse of a conformant
// Claim Bundle.
func claimWithQRItem(t *testing.T, bundle, item []byte) []byte {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	var it any
	if err := json.Unmarshal(item, &it); err != nil {
		t.Fatal(err)
	}
	added := false
	for _, e := range b["entry"].([]any) {
		res, _ := e.(map[string]any)["resource"].(map[string]any)
		if res != nil && res["resourceType"] == "QuestionnaireResponse" {
			items, _ := res["item"].([]any)
			res["item"] = append(items, it)
			added = true
			break
		}
	}
	if !added {
		t.Fatal("the Claim Bundle carries no QuestionnaireResponse")
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// clinicianItem is a hand-entered clinician answer; attested=false strips its
// FR-16 attestation, which the attestation fence refuses.
func clinicianItem(t *testing.T, attested bool) []byte {
	t.Helper()
	item, err := BuildManualAttestedItem("functional-status-oswestry", "42", Attestation{NPI: "1999999999", Text: "I attest these are my clinical findings.", When: "2026-06-04"})
	if err != nil {
		t.Fatal(err)
	}
	if attested {
		return item
	}
	var m map[string]any
	if err := json.Unmarshal(item, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "extension")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// postDecryptionRefusal is one refusal the Responder writes after the payload
// is open, and the request that draws it.
type postDecryptionRefusal struct {
	name, txType, op string
	payload          func(t *testing.T, h *paTestHarness) []byte
	status           int
	// msg is the refusal's closed message (for the attestation fence, the
	// fence's own reason for this payload).
	msg func(t *testing.T, payload []byte) string
}

func fixed(msg string) func(*testing.T, []byte) string {
	return func(*testing.T, []byte) string { return msg }
}

func fenceReason(t *testing.T, payload []byte) string {
	t.Helper()
	reason, ok := fenceAttestedItems(payload)
	if ok || !strings.Contains(reason, "FR-16") {
		t.Fatalf("the fence does not refuse this payload for FR-16: ok=%v reason=%q", ok, reason)
	}
	return reason
}

func unattestedClaim(t *testing.T, h *paTestHarness) []byte {
	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	return claimWithQRItem(t, buildConformantClaim(t, "MBR-001", "refusal-fence", qr, h.now), clinicianItem(t, false))
}

// postDecryptionRefusals enumerates every refusal handleInbound writes itself
// between opening the payload and building the response leg. The handlers'
// own refusals (a request they cannot read, an Adjudicator error) take the same
// relay path and are pinned in their own suites.
func postDecryptionRefusals() []postDecryptionRefusal {
	return []postDecryptionRefusal{
		{"request frame with an unsupported version", "crd-order-select", "crd-order-select",
			func(*testing.T, *paTestHarness) []byte { return []byte{0x00, 0xFF, 0, 0, 0, 0} },
			http.StatusBadRequest, fixed("request frame decode failed")},
		{"request frame whose header overruns it", "pas-claim", "pas-submit",
			func(t *testing.T, _ *paTestHarness) []byte {
				f := mustEncodeRawFrame(t, `{"status":200}`, nil)
				return f[:len(f)-2]
			},
			http.StatusBadRequest, fixed("request frame decode failed")},
		{"operation header on a leg other than DTR", "crd-order-select", "crd-order-select",
			func(t *testing.T, _ *paTestHarness) []byte {
				return frameDTROp(t, FrameOperationQuestionnairePackage, buildConformantCRD(t, "MBR-001", "72148"))
			},
			http.StatusBadRequest, fixed("operation header is not defined for this transaction type")},
		{"pas-claim attestation fence", "pas-claim", "pas-submit", unattestedClaim, http.StatusForbidden, fenceReason},
		{"pas-claim-update attestation fence", "pas-claim-update", "pas-update-submit", unattestedClaim, http.StatusForbidden, fenceReason},
	}
}

// TestSDKResponder_PostDecryptionRefusalsFramed: each refusal the Responder
// writes after it has authenticated the leg and opened the payload reaches a
// frame-capable requester as its answer — HTTP 200 to the Hub carrying a
// response leg sealed to the requester, of the request's transaction type and
// correlation, authorized for that leg's response operation; inside it exactly
// frame(status, application/json, {"error": msg}), unstamped even though the
// Responder stamps its successes, and the request never reached the
// Adjudicator.
func TestSDKResponder_PostDecryptionRefusalsFramed(t *testing.T) {
	for i, row := range postDecryptionRefusals() {
		t.Run(row.name, func(t *testing.T) {
			h, ident, _ := newPAHarness(t)
			adj := &countingAdjudicator{inner: &paTestAdjudicator{now: h.now}}
			srv := h.refusalResponder(t, ident, adj, framesV1, nil)
			corr := "framed-refusal-" + hex.EncodeToString([]byte{byte(i)})
			payload := row.payload(t, h)
			envBytes, hubHdr := h.buildForwardEnv(t, row.txType, row.op, corr, payload)

			resp := postInbound(t, srv, envBytes, hubHdr)
			raw := readBody(t, resp)
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("HTTP %d (%s), want the refusal sealed into a 200 response leg; body: %s", resp.StatusCode, resp.Header.Get("Content-Type"), raw)
			}
			respEnv, err := DecodeEnvelope(raw)
			if err != nil {
				t.Fatalf("the answer is not a response leg: %v: %s", err, raw)
			}
			m := respEnv.Metadata
			if m.Sender != h.responderID || m.Recipient != h.senderID || m.TransactionType != row.txType || m.CorrelationID != corr || m.AuthorityFrame != "payer-coverage" {
				t.Fatalf("response leg metadata = %+v, want %s → %s, %s, correlation %s, payer-coverage", m, h.responderID, h.senderID, row.txType, corr)
			}
			var tok Token
			if err := json.Unmarshal([]byte(m.AuthzToken), &tok); err != nil {
				t.Fatalf("response leg token: %v", err)
			}
			if err := VerifyBound(tok, h.authzPub, h.now, "payer-coverage", responseOp(row.txType), corr, h.responderID, "", sha256Hex(respEnv.Ciphertext)); err != nil {
				t.Fatalf("the response leg is not authorized for %s: %v", responseOp(row.txType), err)
			}

			body, _ := json.Marshal(map[string]string{"error": row.msg(t, payload)})
			want, err := EncodeHTTPFrame(row.status, "application/json", body)
			if err != nil {
				t.Fatal(err)
			}
			if got := h.openResponse(t, raw); !bytes.Equal(got, want) {
				t.Fatalf("sealed answer = %q\nwant exactly  %q", got, want)
			}
			if n := adj.calls.Load(); n != 0 {
				t.Fatalf("the refused request reached the Adjudicator %d time(s)", n)
			}
		})
	}
}

// TestSDKResponder_PostDecryptionRefusalsBareToLegacyRequester: to a
// requester that does not decode frames (no resolver, or one that does not
// advertise v1) the same refusals keep the pre-frame contract byte for byte —
// the refusal's own status and the bare {"error": msg} body, never a sealed
// leg.
func TestSDKResponder_PostDecryptionRefusalsBareToLegacyRequester(t *testing.T) {
	resolvers := map[string]func(string) []string{
		"no resolver":        nil,
		"v1 not advertised":  func(string) []string { return nil },
		"another frame only": func(string) []string { return []string{"v2"} },
	}
	for rname, frames := range resolvers {
		for i, row := range postDecryptionRefusals() {
			t.Run(rname+"/"+row.name, func(t *testing.T) {
				h, ident, _ := newPAHarness(t)
				adj := &countingAdjudicator{inner: &paTestAdjudicator{now: h.now}}
				srv := h.refusalResponder(t, ident, adj, frames, nil)
				payload := row.payload(t, h)
				envBytes, hubHdr := h.buildForwardEnv(t, row.txType, row.op, "legacy-refusal-"+hex.EncodeToString([]byte{byte(i)}), payload)
				resp := postInbound(t, srv, envBytes, hubHdr)
				raw := readBody(t, resp)
				var want bytes.Buffer
				_ = json.NewEncoder(&want).Encode(map[string]string{"error": row.msg(t, payload)})
				if resp.StatusCode != row.status || !bytes.Equal(raw, want.Bytes()) {
					t.Fatalf("HTTP %d %q, want the bare %d %q", resp.StatusCode, raw, row.status, want.Bytes())
				}
				if n := adj.calls.Load(); n != 0 {
					t.Fatalf("the refused request reached the Adjudicator %d time(s)", n)
				}
			})
		}
	}
}

// TestSDKResponder_AttestationFenceControl is the fence rows' control: the
// same Claim with the clinician item attested passes the fence and is
// adjudicated, framed as a stamped 200 — so the framed refusal above is the
// fence's verdict on the missing attestation and nothing else.
func TestSDKResponder_AttestationFenceControl(t *testing.T) {
	h, ident, _ := newPAHarness(t)
	adj := &countingAdjudicator{inner: &paTestAdjudicator{now: h.now}}
	srv := h.refusalResponder(t, ident, adj, framesV1, nil)
	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	claim := claimWithQRItem(t, buildConformantClaim(t, "MBR-001", "fence-control", qr, h.now), clinicianItem(t, true))
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "fence-control", claim)
	resp := postInbound(t, srv, envBytes, hubHdr)
	raw := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, raw)
	}
	hdr, body, err := DecodeHTTPFrame(h.openResponse(t, raw))
	if err != nil || hdr.Status != http.StatusOK || hdr.Headers[FrameHeaderContractVersion] != ContractPAPAS20 {
		t.Fatalf("attested claim: frame status=%d contract=%q err=%v body=%s", hdr.Status, hdr.Headers[FrameHeaderContractVersion], err, body)
	}
	if res, err := ParseClaimResponse(body); err != nil || res.Outcome != "approved" {
		t.Fatalf("attested claim: outcome=%+v err=%v", res, err)
	}
	if adj.calls.Load() != 1 {
		t.Fatalf("the attested claim reached the Adjudicator %d time(s), want once", adj.calls.Load())
	}
}

// errReader fails every read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestSDKResponder_PreAuthRefusalsBare: every refusal decided before the leg
// is authenticated and its payload opened stays a bare transport failure,
// even to a requester that decodes frames — the requester is not yet
// established, so there is nobody to seal an answer to. Each row is the valid
// exchange minus one mutation, and each answer is exactly the bare
// {"error": msg} with its status.
func TestSDKResponder_PreAuthRefusalsBare(t *testing.T) {
	h, ident, _ := newPAHarness(t)
	cer, err := BuildEligibilityRequest("MBR-001", "9999999999", h.now)
	if err != nil {
		t.Fatal(err)
	}
	// forward builds the valid eligibility leg with one mutation applied to its
	// metadata and token before it is encoded.
	forward := func(t *testing.T, corr string, sealTo *[32]byte, mutate func(*Envelope, *Token)) ([]byte, string) {
		meta := Metadata{Sender: h.senderID, Recipient: h.responderID, TransactionType: "coverage-eligibility",
			AuthorityFrame: "provider-tpo", Timestamp: h.now.UTC().Format(time.RFC3339), CorrelationID: corr}
		env, err := Seal(meta, cer, sealTo)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(env.Ciphertext)
		tok := Token{Operation: "eligibility-inquiry", Scope: "coverage", Subject: ResolvePCI("MBR-001", "1975-04-02", "Johansson"),
			Frame: "provider-tpo", CorrelationID: corr, Holder: h.senderID, PayloadHash: hex.EncodeToString(sum[:]), Expiry: h.now.Add(time.Hour)}
		if mutate != nil {
			mutate(&env, &tok)
		}
		tok.Signature = ed25519.Sign(h.authzPriv, tokenSigningPayload(tok))
		tokJSON, _ := json.Marshal(tok)
		if env.Metadata.AuthzToken == "" {
			env.Metadata.AuthzToken = string(tokJSON)
		}
		b, err := EncodeEnvelope(env)
		if err != nil {
			t.Fatal(err)
		}
		return b, makeHubAssertion(t, h.hubPriv, "hub", h.responderID, h.now, 2*time.Minute, "jti-"+corr)
	}
	other, err := GenerateIdentity("someone-else")
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name   string
		send   func(t *testing.T, srv *httptest.Server) (int, []byte)
		status int
		msg    string
		// reached is how many times the row's control delivery reaches the
		// Adjudicator (the replay row delivers the valid leg once first).
		reached int32
	}{
		{"no hub assertion", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, _ := forward(t, "pre-1", h.responderEnc, nil)
			return do(t, srv, b, "")
		}, http.StatusForbidden, "missing or invalid hub assertion", 0},
		{"replayed hub assertion", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-2", h.responderEnc, nil)
			if st, body := do(t, srv, b, hdr); st != http.StatusOK {
				t.Fatalf("control: the first delivery answered %d: %s", st, body)
			}
			return do(t, srv, b, hdr)
		}, http.StatusForbidden, "missing or invalid hub assertion", 1},
		{"body that cannot be read", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			_, hdr := forward(t, "pre-3", h.responderEnc, nil)
			req := httptest.NewRequest(http.MethodPost, "/substrate/inbound", errReader{})
			req.Header.Set("X-Hub-Assertion", hdr)
			rec := httptest.NewRecorder()
			srv.Config.Handler.ServeHTTP(rec, req)
			return rec.Code, rec.Body.Bytes()
		}, http.StatusBadRequest, "read body failed", 0},
		{"body that is not an envelope", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			_, hdr := forward(t, "pre-4", h.responderEnc, nil)
			return do(t, srv, []byte("{not json"), hdr)
		}, http.StatusBadRequest, "decode envelope failed", 0},
		{"no authority frame", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-5", h.responderEnc, func(e *Envelope, _ *Token) { e.Metadata.AuthorityFrame = "" })
			return do(t, srv, b, hdr)
		}, http.StatusBadRequest, "missing authority frame", 0},
		{"no correlation id", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-6", h.responderEnc, func(e *Envelope, _ *Token) { e.Metadata.CorrelationID = "" })
			return do(t, srv, b, hdr)
		}, http.StatusBadRequest, "missing correlation id", 0},
		{"addressed to another holder", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-7", h.responderEnc, func(e *Envelope, _ *Token) { e.Metadata.Recipient = "someone-else" })
			return do(t, srv, b, hdr)
		}, http.StatusForbidden, "envelope not addressed to this holder", 0},
		{"unknown transaction type", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-8", h.responderEnc, func(e *Envelope, _ *Token) { e.Metadata.TransactionType = "unsupported-tx" })
			return do(t, srv, b, hdr)
		}, http.StatusBadRequest, "unknown transaction type", 0},
		{"token that is not JSON", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-9", h.responderEnc, func(e *Envelope, _ *Token) { e.Metadata.AuthzToken = "{not json" })
			return do(t, srv, b, hdr)
		}, http.StatusForbidden, "invalid authz token", 0},
		{"token bound to other ciphertext", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-10", h.responderEnc, func(_ *Envelope, tok *Token) { tok.PayloadHash = strings.Repeat("00", 32) })
			return do(t, srv, b, hdr)
		}, http.StatusForbidden, "authz verification failed", 0},
		{"sealed to another holder's key", func(t *testing.T, srv *httptest.Server) (int, []byte) {
			b, hdr := forward(t, "pre-11", other.EncPub, nil)
			return do(t, srv, b, hdr)
		}, http.StatusBadRequest, "decryption failed", 0},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			adj := &countingAdjudicator{inner: &paTestAdjudicator{now: h.now}}
			srv := h.refusalResponder(t, ident, adj, framesV1, nil)
			status, raw := row.send(t, srv)
			var want bytes.Buffer
			_ = json.NewEncoder(&want).Encode(map[string]string{"error": row.msg})
			if status != row.status || !bytes.Equal(raw, want.Bytes()) {
				t.Fatalf("HTTP %d %q, want the bare %d %q", status, raw, row.status, want.Bytes())
			}
			if n := adj.calls.Load(); n != row.reached {
				t.Fatalf("the Adjudicator was reached %d time(s), want %d: a refused request reached it", n, row.reached)
			}
		})
	}
}

// do posts an inbound leg (an empty assertion is omitted) and returns the
// HTTP status and body.
func do(t *testing.T, srv *httptest.Server, body []byte, hdr string) (int, []byte) {
	t.Helper()
	resp := postInbound(t, srv, body, hdr)
	return resp.StatusCode, readBody(t, resp)
}

// TestSDKResponder_RefusalResponseLegFailureStaysBare: a refusal the Responder
// would frame still needs a response leg; when that leg cannot be built (the
// requester's key does not resolve, the Authorization Framework refuses the
// response leg) the failure is the Responder's machinery and stays bare, never
// a partial or unauthorized frame.
func TestSDKResponder_RefusalResponseLegFailureStaysBare(t *testing.T) {
	t.Run("requester key not resolvable", func(t *testing.T) {
		h, ident, _ := newPAHarness(t)
		srv := h.refusalResponder(t, ident, &paTestAdjudicator{now: h.now}, framesV1, func(string) (*[32]byte, bool) { return nil, false })
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "leg-fail-1", unattestedClaim(t, h))
		status, raw := do(t, srv, envBytes, hubHdr)
		assertBare(t, status, raw, http.StatusBadGateway, "requester key not resolvable")
	})
	t.Run("response leg not authorized", func(t *testing.T) {
		h, ident, _ := newPAHarness(t)
		failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}))
		t.Cleanup(failing.Close)
		h.authzSrv = failing
		srv := h.refusalResponder(t, ident, &paTestAdjudicator{now: h.now}, framesV1, nil)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "leg-fail-2", unattestedClaim(t, h))
		status, raw := do(t, srv, envBytes, hubHdr)
		assertBare(t, status, raw, http.StatusBadGateway, "authorize response leg failed")
	})
}

func assertBare(t *testing.T, status int, raw []byte, wantStatus int, msg string) {
	t.Helper()
	var want bytes.Buffer
	_ = json.NewEncoder(&want).Encode(map[string]string{"error": msg})
	if status != wantStatus || !bytes.Equal(raw, want.Bytes()) {
		t.Fatalf("HTTP %d %q, want the bare %d %q", status, raw, wantStatus, want.Bytes())
	}
}

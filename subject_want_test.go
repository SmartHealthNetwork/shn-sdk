package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A verifier states what a token's subject must be, structurally: the
// patient it holds, some patient, none, the answer's patient or none, or that
// it checks the subject itself. An expectation it did not state, or a held
// patient that came out empty, is never read as "do not check" (unknown is
// not empty): it is refused before the token is read for its subject.
func TestVerifyBoundWant_TheSubjectIsCheckedAsStated(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	tok := func(subject string) Token {
		return signTestToken(Token{Operation: "op", Scope: "s", Subject: subject, Frame: "f", Holder: "h",
			CorrelationID: "c", PayloadHash: "hash", Expiry: now.Add(time.Hour)}, priv)
	}
	const pci = "pci:00000000000000000000000000000001"
	for name, row := range map[string]struct {
		subject string
		want    SubjectWant
		err     error // nil: accepted
	}{
		"the held patient":                   {pci, SubjectIs(pci), nil},
		"another patient than the held one":  {"pci:00000000000000000000000000000002", SubjectIs(pci), ErrSubjectMismatch},
		"no patient where one is held":       {"", SubjectIs(pci), ErrSubjectMismatch},
		"a held patient that came out empty": {"", SubjectIs(""), ErrSubjectUnstated},
		"a held patient that is blank":       {" ", SubjectIs(" "), ErrSubjectUnstated},
		"some patient, named":                {pci, SubjectNamed(), nil},
		"some patient, none named":           {"", SubjectNamed(), ErrSubjectMismatch},
		"some patient, not a pci":            {"member-1", SubjectNamed(), ErrSubjectMismatch},
		"some patient, the bare prefix":      {"pci:", SubjectNamed(), ErrSubjectMismatch},
		"no patient, none named":             {"", SubjectNone(), nil},
		"no patient, one named":              {pci, SubjectNone(), ErrSubjectMismatch},
		"the answer's patient":               {pci, SubjectFromAnswer(), nil},
		"the answer's, none":                 {"", SubjectFromAnswer(), nil},
		"the answer's, not a pci":            {"member-1", SubjectFromAnswer(), ErrSubjectMismatch},
		"checked by the verifier, any":       {"member-1", SubjectChecked(), nil},
		"checked by the verifier, none":      {"", SubjectChecked(), nil},
		"an expectation never stated":        {pci, SubjectWant{}, ErrSubjectUnstated},
	} {
		t.Run(name, func(t *testing.T) {
			err := VerifyBoundWant(tok(row.subject), pub, now, "f", "op", "c", "h", row.want, "hash")
			if row.err == nil && err != nil {
				t.Fatalf("refused: %v; want accepted", err)
			}
			if row.err != nil && !errors.Is(err, row.err) {
				t.Fatalf("err %v; want %v", err, row.err)
			}
		})
	}
}

// The subject is checked after every other binding and before the payload
// hash, so a payload hash refusal still means every other binding held, the
// subject included, and ErrSubjectMismatch means every binding but the
// payload hash held. Every other binding is VerifyBound's own.
func TestVerifyBoundWant_TheOrderOfTheChecks(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	const pci = "pci:00000000000000000000000000000001"
	tok := signTestToken(Token{Operation: "op", Scope: "s", Subject: pci, Frame: "f", Holder: "h",
		CorrelationID: "c", PayloadHash: "hash", Expiry: now.Add(time.Hour)}, priv)
	if err := VerifyBoundWant(tok, pub, now, "f", "op", "c", "h", SubjectNone(), "other"); !errors.Is(err, ErrSubjectMismatch) {
		t.Fatalf("a wrong subject and a wrong payload: %v; want ErrSubjectMismatch first", err)
	}
	if err := VerifyBoundWant(tok, pub, now, "f", "op", "c", "h", SubjectIs(pci), "other"); err == nil || !strings.Contains(err.Error(), "payload hash mismatch") {
		t.Fatalf("a wrong payload alone: %v; want the payload hash refusal", err)
	}
	if err := VerifyBoundWant(tok, pub, now, "g", "op", "c", "h", SubjectNone(), "other"); err == nil || errors.Is(err, ErrSubjectMismatch) || strings.Contains(err.Error(), "payload hash mismatch") {
		t.Fatalf("a wrong frame: %v; want the frame refusal first", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyBoundWant(tok, other, now, "f", "op", "c", "h", SubjectIs(pci), "hash"); err == nil {
		t.Fatal("a token signed by another key was accepted")
	}
	// VerifyBound, deprecated, keeps its reading: an empty wantSubject still
	// skips the subject there.
	if err := VerifyBound(tok, pub, now, "f", "op", "c", "h", "", "hash"); err != nil {
		t.Fatalf("VerifyBound with an empty wantSubject: %v; want its existing reading", err)
	}
	// A token carrying an Involvement is refused, as VerifyBound refuses it.
	inv := tok
	inv.Involvement = "requester-held"
	if err := VerifyBoundWant(signTestToken(inv, priv), pub, now, "f", "op", "c", "h", SubjectIs(pci), "hash"); err == nil {
		t.Fatal("a token carrying an Involvement was accepted")
	}
}

// TestNoSDKCodeCallsTheDeprecatedVerifiers fails any use of VerifyBound or
// VerifyBoundNoPayload in the SDK's own code, a call or a function value,
// read from the parsed code: the SDK states the subject it expects
// (VerifyBoundWant) at every verify. Only their declarations name them.
func TestNoSDKCodeCallsTheDeprecatedVerifiers(t *testing.T) {
	deprecated := map[string]bool{"VerifyBound": true, "VerifyBoundNoPayload": true}
	files := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && path != "." {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		declared := map[*ast.Ident]bool{}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				declared[fn.Name] = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && deprecated[id.Name] && !declared[id] {
				t.Errorf("%s uses the deprecated %s; state the subject with VerifyBoundWant", fset.Position(id.Pos()), id.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 50 {
		t.Fatalf("walked %d files; want the SDK's code", files)
	}
}

// VerifyBoundNoPayloadWant refuses every token VerifyBoundNoPayload refuses
// on a binding other than the subject: one carrying a payload hash, one
// carrying an Involvement, a bad signature, an expired one, and one bound to
// another frame, operation, correlation or holder.
func TestVerifyBoundNoPayloadWant_RefusesEveryOtherUnboundToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0).UTC()
	const pci = "pci:00000000000000000000000000000001"
	good := Token{Operation: "op", Scope: "s", Subject: pci, Frame: "f", Holder: "h", CorrelationID: "c", Expiry: now.Add(time.Hour)}
	if err := VerifyBoundNoPayloadWant(signTestToken(good, priv), pub, now, "f", "op", "c", "h", SubjectIs(pci)); err != nil {
		t.Fatalf("a bound token: %v", err)
	}
	mutate := func(m func(*Token)) Token { tok := good; m(&tok); return signTestToken(tok, priv) }
	rows := map[string]struct {
		tok Token
		key ed25519.PublicKey
		at  time.Time
	}{
		"a payload hash":      {mutate(func(t *Token) { t.PayloadHash = "hash" }), pub, now},
		"an Involvement":      {mutate(func(t *Token) { t.Involvement = "requester-held" }), pub, now},
		"another key":         {signTestToken(good, priv), other, now},
		"expired":             {signTestToken(good, priv), pub, now.Add(2 * time.Hour)},
		"another frame":       {mutate(func(t *Token) { t.Frame = "g" }), pub, now},
		"another operation":   {mutate(func(t *Token) { t.Operation = "op2" }), pub, now},
		"another correlation": {mutate(func(t *Token) { t.CorrelationID = "c2" }), pub, now},
		"another holder":      {mutate(func(t *Token) { t.Holder = "h2" }), pub, now},
	}
	for name, row := range rows {
		err := VerifyBoundNoPayloadWant(row.tok, row.key, row.at, "f", "op", "c", "h", SubjectIs(pci))
		if err == nil || errors.Is(err, ErrSubjectMismatch) || errors.Is(err, ErrSubjectUnstated) {
			t.Errorf("a token with %s: %v; want its own refusal", name, err)
		}
	}
}

// VerifyBoundNoPayloadWant holds the subject as stated: a token whose subject
// does not fit is ErrSubjectMismatch, and an expectation that states nothing
// is ErrSubjectUnstated before the token is read.
func TestVerifyBoundNoPayloadWant_TheSubjectIsCheckedAsStated(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0).UTC()
	const pci = "pci:00000000000000000000000000000001"
	tok := func(subject string) Token {
		return signTestToken(Token{Operation: "op", Scope: "s", Subject: subject, Frame: "f", Holder: "h", CorrelationID: "c", Expiry: now.Add(time.Hour)}, priv)
	}
	rows := []struct {
		name    string
		subject string
		want    SubjectWant
		err     error
	}{
		{"the held patient", pci, SubjectIs(pci), nil},
		{"another patient for the held one", "pci:00000000000000000000000000000002", SubjectIs(pci), ErrSubjectMismatch},
		{"none for the held patient", "", SubjectIs(pci), ErrSubjectMismatch},
		{"some patient", pci, SubjectNamed(), nil},
		{"no patient identifier for some patient", "member-1", SubjectNamed(), ErrSubjectMismatch},
		{"none for some patient", "", SubjectNamed(), ErrSubjectMismatch},
		{"none", "", SubjectNone(), nil},
		{"a patient for none", pci, SubjectNone(), ErrSubjectMismatch},
		{"the answer's patient", pci, SubjectFromAnswer(), nil},
		{"not a patient for the answer's", "member-1", SubjectFromAnswer(), ErrSubjectMismatch},
		{"an empty held patient", pci, SubjectIs(""), ErrSubjectUnstated},
		{"an unstated expectation", pci, SubjectWant{}, ErrSubjectUnstated},
	}
	for _, row := range rows {
		err := VerifyBoundNoPayloadWant(tok(row.subject), pub, now, "f", "op", "c", "h", row.want)
		if (row.err == nil && err != nil) || (row.err != nil && !errors.Is(err, row.err)) {
			t.Errorf("%s: %v; want %v", row.name, err, row.err)
		}
	}
	// Unstated is refused before the token is read: even a token carrying a
	// payload hash, which the form refuses, is refused as unstated first.
	hashed := Token{Operation: "op", Subject: pci, Frame: "f", Holder: "h", CorrelationID: "c", PayloadHash: "hash", Expiry: now.Add(time.Hour)}
	if err := VerifyBoundNoPayloadWant(signTestToken(hashed, priv), pub, now, "f", "op", "c", "h", SubjectWant{}); !errors.Is(err, ErrSubjectUnstated) {
		t.Errorf("an unstated expectation on a token carrying a payload hash: %v; want ErrSubjectUnstated first", err)
	}
}

// The Want forms require every binding: an empty or blank expected frame,
// operation, correlation, holder or (borne by an envelope) payload hash is
// ErrBindingUnstated, refused before the token is read, so an unsigned token
// answers it too; and before the subject is read.
func TestVerifyBoundWant_AnEmptyBindingIsRefusedBeforeTheToken(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	const pci = "pci:00000000000000000000000000000001"
	tok := signTestToken(Token{Operation: "op", Scope: "s", Subject: pci, Frame: "f", Holder: "h",
		CorrelationID: "c", PayloadHash: "hash", Expiry: now.Add(time.Hour)}, priv)
	unsigned := tok
	unsigned.Signature = nil
	args := [5]string{"f", "op", "c", "h", "hash"}
	if err := VerifyBoundWant(tok, pub, now, args[0], args[1], args[2], args[3], SubjectIs(pci), args[4]); err != nil {
		t.Fatalf("every binding stated: %v", err)
	}
	for i := range args {
		for _, empty := range []string{"", " "} {
			a := args
			a[i] = empty
			for _, tk := range []Token{tok, unsigned} {
				if err := VerifyBoundWant(tk, pub, now, a[0], a[1], a[2], a[3], SubjectIs(pci), a[4]); !errors.Is(err, ErrBindingUnstated) {
					t.Fatalf("VerifyBoundWant, binding %d %q: %v, want ErrBindingUnstated", i, empty, err)
				}
				if err := VerifyBoundWant(tk, pub, now, a[0], a[1], a[2], a[3], SubjectWant{}, a[4]); !errors.Is(err, ErrBindingUnstated) {
					t.Fatalf("VerifyBoundWant, binding %d %q and no subject: %v, want ErrBindingUnstated first", i, empty, err)
				}
			}
			if i == 4 {
				continue
			}
			ntok := tok
			ntok.PayloadHash = ""
			if err := VerifyBoundNoPayloadWant(ntok, pub, now, a[0], a[1], a[2], a[3], SubjectIs(pci)); !errors.Is(err, ErrBindingUnstated) {
				t.Fatalf("VerifyBoundNoPayloadWant, binding %d %q: %v, want ErrBindingUnstated", i, empty, err)
			}
		}
	}
}

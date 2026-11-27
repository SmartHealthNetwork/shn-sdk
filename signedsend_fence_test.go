package shnsdk

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SmartHealthNetwork/shn-sdk/internal/signedsend"
)

// signedSendDir is the one directory whose code may write a request's
// headers by any name.
const signedSendDir = "internal/signedsend"

// Every request in this module that carries an SHN signed header is sent
// through signedsend.Do, which follows no redirect. The fence reads the
// module's non-test code with its types, so it sees what the code writes,
// however the code spells it: outside signedSendDir, every write to an
// http.Header names a constant that is not a signed header. A write by a name
// it cannot read as a constant, a header map taken whole into a request, a Set
// or Add used as a value, and a header map that leaves http.Header where the
// fence cannot follow it all fail it, because each could carry a signed header
// past Do. It fences honest mistakes in code that sets headers: reflect,
// unsafe and a request decoded whole from bytes (http.ReadRequest, or a
// decoder given the request) are outside it.
func TestSignedHeadersAreSetOnlyBySignedSend(t *testing.T) {
	pkgs := map[string]map[string]string{} // package dir -> file -> source
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if pkgs[dir] == nil {
			pkgs[dir] = map[string]string{}
		}
		pkgs[dir][filepath.ToSlash(path)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs[signedSendDir]) == 0 || len(pkgs["cmd/shn"]) == 0 {
		t.Fatalf("found no %s or cmd/shn: the walk is not over this module", signedSendDir)
	}
	problems, err := signedHeaderProblems(pkgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// signedHeaderProblems type-checks each package (its directory mapped to its
// files' sources) and names each write to an http.Header, outside
// signedSendDir, that names a signed header or a name that is not a constant.
// A package that does not type-check is an error, never read as clean.
func signedHeaderProblems(pkgs map[string]map[string]string) ([]string, error) {
	fset, imp := fenceImporter()
	imports := fenceImports{checked: map[string]*types.Package{}, next: imp}
	var problems []string
	for _, dir := range sortedKeys(pkgs) {
		if dir == signedSendDir {
			continue
		}
		var files []*ast.File
		for _, name := range sortedKeys(pkgs[dir]) {
			f, err := parser.ParseFile(fset, name, pkgs[dir][name], 0)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
		}
		info := &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		conf := types.Config{Importer: imports}
		pkg, err := conf.Check(dir, fset, files, info)
		if err != nil {
			return nil, fmt.Errorf("type-check %s: %w", dir, err)
		}
		imports.checked[dir] = pkg
		for _, f := range files {
			problems = append(problems, headerWrites(fset, f, info)...)
			problems = append(problems, headerEscapes(fset, f, info, dir)...)
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// testImportPrefix begins the directory of a package a test checks for
// another to import; no directory of the module begins with it.
const testImportPrefix = "example.com/"

// fenceImports imports a package the fence has already checked, by its
// directory under testImportPrefix, and every other through next. The
// module's packages import each other by module path, so only a test's
// packages use it.
type fenceImports struct {
	checked map[string]*types.Package
	next    types.ImporterFrom
}

func (i fenceImports) Import(path string) (*types.Package, error) {
	return i.ImportFrom(path, "", 0)
}

func (i fenceImports) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if p, ok := i.checked[path]; ok && strings.HasPrefix(path, testImportPrefix) {
		return p, nil
	}
	return i.next.ImportFrom(path, dir, mode)
}

// fenceImporter is the file set and source importer every fence check
// shares, so the packages each imports are type-checked once per test binary.
var fenceImporter = sync.OnceValues(func() (*token.FileSet, types.ImporterFrom) {
	// A -trimpath test binary knows no GOROOT of its own: the Makefile builds
	// every test that way, and a toolchain run as itself (GOTOOLCHAIN=local, as
	// in CI) sets none in the environment. The standard library's source is
	// then where the go command says. Should that fail too, every import fails,
	// and the fence with it.
	if build.Default.GOROOT == "" {
		if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
			build.Default.GOROOT = strings.TrimSpace(string(out))
		}
	}
	fset := token.NewFileSet()
	return fset, importer.ForCompiler(fset, "source", nil).(types.ImporterFrom)
})

// headerWrites names the header writes in f that the fence refuses.
func headerWrites(fset *token.FileSet, f *ast.File, info *types.Info) []string {
	var out []string
	at := func(n ast.Node, format string, args ...any) {
		out = append(out, fset.Position(n.Pos()).String()+": "+fmt.Sprintf(format, args...))
	}
	// name reads e as a header name: its constant string value, if it has one.
	name := func(e ast.Expr) (string, bool) {
		tv, ok := info.Types[e]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
			return "", false
		}
		return constant.StringVal(tv.Value), true
	}
	// key checks one header name the code writes.
	key := func(n ast.Node, e ast.Expr) {
		switch s, ok := name(e); {
		case !ok:
			at(n, "writes a header by a name that is not a constant: a signed one would escape signedsend.Do")
		case signedsend.IsSigned(s):
			at(n, "writes the signed header %s itself: send the request with signedsend.Do", s)
		}
	}
	calls := map[*ast.SelectorExpr]*ast.CallExpr{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
				calls[sel] = c
			}
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			sel, ok := info.Selections[n]
			if !ok {
				return true
			}
			// The method's declared receiver, so a Set promoted through an
			// embedding is http.Header's own.
			switch {
			case sel.Kind() != types.FieldVal && isHeader(declaredRecv(sel)) && (n.Sel.Name == "Set" || n.Sel.Name == "Add"):
				c := calls[n]
				if c == nil || sel.Kind() == types.MethodExpr {
					at(n, "uses http.Header's %s as a value: the names it writes cannot be read", n.Sel.Name)
				} else if len(c.Args) == 2 {
					key(c, c.Args[0])
				}
			}
		case *ast.AssignStmt:
			// A header map assigned whole (a request's Header or Trailer
			// replaced, a write through a pointer) is headerEscapes'.
			for _, lhs := range n.Lhs {
				if l, ok := lhs.(*ast.IndexExpr); ok {
					if tv, ok := info.Types[l.X]; ok && isHeader(tv.Type) {
						key(n, l.Index)
					}
				}
			}
		case *ast.CompositeLit:
			if tv, ok := info.Types[n]; ok && isHeaderShaped(tv.Type) {
				for _, el := range n.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						key(n, kv.Key)
					}
				}
			}
		case *ast.CallExpr:
			// A conversion to http.Header takes a map whose names the
			// fence reads only when it is a literal here.
			if tv, ok := info.Types[n.Fun]; ok && tv.IsType() && isHeader(tv.Type) && len(n.Args) == 1 {
				if _, lit := ast.Unparen(n.Args[0]).(*ast.CompositeLit); !lit {
					at(n, "converts a map to http.Header: the names it writes cannot be read")
				}
			}
		}
		return true
	})
	return out
}

// sdkModule is this module's path: a function declared in it has its body
// fenced, so a header map passed to it is followed there.
const sdkModule = "github.com/SmartHealthNetwork/shn-sdk"

// headerEscapes names each place in f where a header map (an http.Header, or
// a pointer to one at any depth) goes somewhere the fence does not read its
// writes: a conversion or assignment to another type (another map type,
// textproto.MIMEHeader, an interface); a field or element of another value,
// a literal or a channel, which carry it out with them (a request's Header or
// Trailer replaced whole is one); a write through a pointer; a function
// outside this module (maps.Copy writes into its first argument), an
// interface's method or a generic one. Writes through a header map that
// stays an http.Header variable are read where they happen, so method calls,
// indexes, ranges, comparisons, variables of the same type and this module's
// own functions taking one are followed.
func headerEscapes(fset *token.FileSet, f *ast.File, info *types.Info, pkgPath string) []string {
	parent := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parent[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	typeOf := func(e ast.Expr) types.Type {
		if id, ok := e.(*ast.Ident); ok {
			if obj := info.Defs[id]; obj != nil {
				return obj.Type()
			}
			if obj := info.Uses[id]; obj != nil {
				return obj.Type()
			}
		}
		if tv, ok := info.Types[e]; ok {
			return tv.Type
		}
		return nil
	}
	isBlank := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == "_" }
	// into reports whether a header map assigned to lhs stays where the
	// fence reads it: a variable, whose every use is read. A field or
	// element of another value would carry it out with that value.
	into := func(lhs ast.Expr) bool {
		_, ok := ast.Unparen(lhs).(*ast.Ident)
		return ok
	}
	same := func(target, t types.Type) bool { return target != nil && types.Identical(target, t) }
	// result is the type of the i'th result of the function enclosing n.
	result := func(n ast.Node, i int) types.Type {
		for p := parent[n]; p != nil; p = parent[p] {
			var sig *types.Signature
			switch p := p.(type) {
			case *ast.FuncLit:
				sig, _ = typeOf(p).(*types.Signature)
			case *ast.FuncDecl:
				if obj := info.Defs[p.Name]; obj != nil {
					sig, _ = obj.Type().(*types.Signature)
				}
			default:
				continue
			}
			if sig == nil || i >= sig.Results().Len() {
				return nil
			}
			return sig.Results().At(i).Type()
		}
		return nil
	}
	// param is the declared type of the parameter arg i takes in a call of
	// fn, a function this module declares; nil when fn is anything else.
	param := func(c *ast.CallExpr, i int) types.Type {
		var obj types.Object
		methodExpr := false
		switch fun := ast.Unparen(c.Fun).(type) {
		case *ast.Ident:
			obj = info.Uses[fun]
		case *ast.SelectorExpr:
			if sel, ok := info.Selections[fun]; ok {
				obj, methodExpr = sel.Obj(), sel.Kind() == types.MethodExpr
			} else {
				obj = info.Uses[fun.Sel]
			}
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil || (fn.Pkg().Path() != pkgPath && fn.Pkg().Path() != sdkModule && !strings.HasPrefix(fn.Pkg().Path(), sdkModule+"/")) {
			return nil
		}
		sig := fn.Origin().Type().(*types.Signature)
		if r := sig.Recv(); r != nil && types.IsInterface(r.Type()) {
			// An interface's method: what implements it may be outside
			// this module.
			return nil
		}
		if methodExpr {
			// A method expression takes its receiver as the first argument.
			if i == 0 {
				return sig.Recv().Type()
			}
			i--
		}
		ps := sig.Params()
		switch {
		case sig.Variadic() && i >= ps.Len()-1:
			if c.Ellipsis.IsValid() {
				return ps.At(ps.Len() - 1).Type()
			}
			return ps.At(ps.Len() - 1).Type().(*types.Slice).Elem()
		case i < ps.Len():
			return ps.At(i).Type()
		}
		return nil
	}
	// target is the type a value e flows into where it is used: the
	// variable, field, element, result or parameter that takes it. Nil when
	// e flows into no typed destination.
	target := func(e ast.Expr) types.Type {
		switch p := parent[e].(type) {
		case *ast.AssignStmt:
			for i, r := range p.Rhs {
				if r == e && len(p.Lhs) == len(p.Rhs) {
					return typeOf(p.Lhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, v := range p.Values {
				if v == e && len(p.Names) == len(p.Values) {
					return typeOf(p.Names[i])
				}
			}
		case *ast.ReturnStmt:
			for i, r := range p.Results {
				if r == e {
					return result(p, i)
				}
			}
		case *ast.SendStmt:
			if p.Value == e {
				if ch, ok := typeOf(p.Chan).Underlying().(*types.Chan); ok {
					return ch.Elem()
				}
			}
		case *ast.CallExpr:
			sig, ok := info.Types[p.Fun].Type.(*types.Signature)
			if !ok {
				return nil
			}
			ps := sig.Params()
			for i, a := range p.Args {
				switch {
				case a != e, p.Ellipsis.IsValid() && i == len(p.Args)-1:
					// A slice passed whole with ... is not a map.
				case sig.Variadic() && i >= ps.Len()-1:
					return ps.At(ps.Len() - 1).Type().(*types.Slice).Elem()
				case i < ps.Len():
					return ps.At(i).Type()
				}
			}
		case *ast.KeyValueExpr:
			lit, ok := parent[p].(*ast.CompositeLit)
			if !ok || p.Value != e {
				return nil
			}
			switch u := typeOf(lit).Underlying().(type) {
			case *types.Struct:
				if id, ok := p.Key.(*ast.Ident); ok {
					for i := range u.NumFields() {
						if u.Field(i).Name() == id.Name {
							return u.Field(i).Type()
						}
					}
				}
			case *types.Map:
				return u.Elem()
			case *types.Slice:
				return u.Elem()
			case *types.Array:
				return u.Elem()
			}
		case *ast.CompositeLit:
			for i, el := range p.Elts {
				if el != e {
					continue
				}
				switch u := typeOf(p).Underlying().(type) {
				case *types.Struct:
					if i < u.NumFields() {
						return u.Field(i).Type()
					}
				case *types.Slice:
					return u.Elem()
				case *types.Array:
					return u.Elem()
				}
			}
		}
		return nil
	}
	var out []string
	// One call or comma-ok expression assigning several values: each element
	// of the tuple goes to its own left side, where no expression of its own
	// stands for it, so it is matched to that left side here.
	ast.Inspect(f, func(n ast.Node) bool {
		var lhs, rhs []ast.Expr
		switch n := n.(type) {
		case *ast.AssignStmt:
			lhs, rhs = n.Lhs, n.Rhs
		case *ast.ValueSpec:
			for _, id := range n.Names {
				lhs = append(lhs, id)
			}
			rhs = n.Values
		default:
			return true
		}
		if len(rhs) != 1 || len(lhs) < 2 {
			return true
		}
		tup, ok := info.Types[rhs[0]].Type.(*types.Tuple)
		if !ok || tup.Len() != len(lhs) {
			return true
		}
		for i, l := range lhs {
			t, lt := tup.At(i).Type(), typeOf(l)
			switch {
			case isBlank(l):
			case isHeader(t) && !(into(l) && same(lt, t)):
				out = append(out, fset.Position(l.Pos()).String()+": a header map leaves http.Header here, where the fence cannot read what is written to it: keep it an http.Header, or send through signedsend.Do")
			case !isHeader(t) && isHeaderShaped(t) && lt != nil && isHeader(lt):
				out = append(out, fset.Position(l.Pos()).String()+": a map becomes an http.Header here, where the fence did not read what was written to it: build the header as an http.Header, or send through signedsend.Do")
			}
		}
		return true
	})
	for e, tv := range info.Types {
		if tv.IsType() || e.Pos() < f.Pos() || e.End() > f.End() {
			continue
		}
		if !isHeader(tv.Type) {
			// A map of http.Header's shape becomes one, without a
			// conversion, where an http.Header takes it; its writes were
			// never read. Its own literal's names are read where it is
			// written.
			if _, lit := e.(*ast.CompositeLit); !lit && isHeaderShaped(tv.Type) {
				if t := target(e); t != nil && isHeader(t) {
					out = append(out, fset.Position(e.Pos()).String()+": a map becomes an http.Header here, where the fence did not read what was written to it: build the header as an http.Header, or send through signedsend.Do")
				}
			}
			continue
		}
		ok := false
		switch p := parent[e].(type) {
		case *ast.ParenExpr, *ast.UnaryExpr, *ast.StarExpr, *ast.BinaryExpr:
			// The result, a header map or a pointer to one, is read in turn;
			// a comparison writes nothing.
			ok = true
		case *ast.SelectorExpr:
			// http.Header has no fields, so this is one of its methods:
			// Set and Add are read where they are called.
			ok = true
		case *ast.IndexExpr:
			ok = p.X == e
		case *ast.RangeStmt:
			ok = p.X == e
		case *ast.AssignStmt:
			ok = true // a left side, unless e is a value assigned
			for i, r := range p.Rhs {
				if r == e {
					ok = len(p.Lhs) == len(p.Rhs) && (isBlank(p.Lhs[i]) || same(typeOf(p.Lhs[i]), tv.Type) && into(p.Lhs[i]))
				}
			}
		case *ast.ValueSpec:
			for i, v := range p.Values {
				if v == e {
					ok = len(p.Names) == len(p.Values) && (isBlank(p.Names[i]) || same(typeOf(p.Names[i]), tv.Type))
				}
			}
		case *ast.ReturnStmt:
			for i, r := range p.Results {
				if r == e {
					ok = same(result(p, i), tv.Type)
				}
			}
		case *ast.CallExpr:
			if ftv, found := info.Types[p.Fun]; found && ftv.IsType() {
				ok = same(ftv.Type, tv.Type)
				break
			}
			if id, isID := ast.Unparen(p.Fun).(*ast.Ident); isID {
				if b, isB := info.Uses[id].(*types.Builtin); isB {
					ok = b.Name() == "len" || b.Name() == "delete" || b.Name() == "clear"
					break
				}
			}
			for i, a := range p.Args {
				if a == e {
					ok = same(param(p, i), tv.Type)
				}
			}
		}
		if !ok {
			out = append(out, fset.Position(e.Pos()).String()+": a header map leaves http.Header here, where the fence cannot read what is written to it: keep it an http.Header, or send through signedsend.Do")
		}
	}
	return out
}

// declaredRecv is the receiver type a selected method is declared on.
func declaredRecv(sel *types.Selection) types.Type {
	if fn, ok := sel.Obj().(*types.Func); ok {
		if r := fn.Type().(*types.Signature).Recv(); r != nil {
			return r.Type()
		}
	}
	return sel.Recv()
}

// isNamed reports whether t, or what it points to at any depth, is the named type
// pkg.name.
func isNamed(t types.Type, pkg, name string) bool {
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkg && n.Obj().Name() == name
}

func isHeader(t types.Type) bool { return isNamed(t, "net/http", "Header") }

// isHeaderShaped reports whether t is http.Header or a map[string][]string,
// which converts to one.
func isHeaderShaped(t types.Type) bool {
	if isHeader(t) {
		return true
	}
	m, ok := t.Underlying().(*types.Map)
	if !ok {
		return false
	}
	k, kok := m.Key().(*types.Basic)
	s, sok := m.Elem().(*types.Slice)
	if !kok || !sok || k.Kind() != types.String {
		return false
	}
	e, eok := s.Elem().(*types.Basic)
	return eok && e.Kind() == types.String
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// The fence's rejection rows: each way of writing a signed header, or a
// header by a name the fence cannot read, outside the helper is found; a
// constant unsigned name, the helper's call, and other maps are not.
func TestSignedHeaderFence_Rows(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		found     bool
	}{
		{"a literal", `req.Header.Set("X-Holder-Assertion", v)`, true},
		{"a literal in another case", `req.Header.Set("x-holder-assertion", v)`, true},
		{"Add", `req.Header.Add("X-Holder-Assertion", v)`, true},
		{"the package's own constant", `req.Header.Set(hdrName, v)`, true},
		{"the helper's constant", `req.Header.Set(signedsend.HolderAssertion, v)`, true},
		{"a constant built by concatenation", `req.Header.Set("X-Holder-"+"Assertion", v)`, true},
		{"an alias with a literal", `h := req.Header; h.Set("X-Holder-Assertion", v)`, true},
		{"an alias with a variable", `h := req.Header; h.Set(k, v)`, true},
		{"a variable", `req.Header.Set(k, v)`, true},
		{"a range over a map", `for k, v := range m { req.Header.Set(k, v) }`, true},
		{"an index by the package's constant", `req.Header[hdrName] = []string{v}`, true},
		{"an index by the helper's constant", `req.Header[signedsend.HolderAssertion] = []string{v}`, true},
		{"an index by a variable", `req.Header[k] = []string{v}`, true},
		{"an index on an alias", `h := req.Header; h["X-Holder-Assertion"] = []string{v}`, true},
		{"an http.Header literal", `req.Header = http.Header{"Content-Type": {v}}`, true},
		{"an http.Header literal naming it", `_ = http.Header{"X-Holder-Assertion": {v}}`, true},
		{"a map[string][]string literal naming it", `_ = map[string][]string{"X-Holder-Assertion": {v}}`, true},
		{"a conversion of a map", `_ = http.Header(mm)`, true},
		{"a request's headers replaced", `req.Header = make(http.Header)`, true},
		{"a request's headers cloned in", `req.Header = other.Header.Clone()`, true},
		{"a request built with headers", `_ = &http.Request{Header: other.Header}`, true},
		{"Set as a method value", `set := req.Header.Set; set("X-Holder-Assertion", v)`, true},
		{"Set as a method expression", `http.Header.Set(req.Header, "X-Holder-Assertion", v)`, true},
		{"another header", `req.Header.Set("Content-Type", v)`, false},
		{"another header by a constant", `req.Header.Set(ctName, v)`, false},
		{"an index naming another header", `req.Header["Accept"] = []string{v}`, false},
		{"an http.Header literal naming another header", `_ = http.Header{"Accept": {v}}`, false},
		{"query values by a variable", `q := url.Values{}; q.Set(k, v)`, false},
		{"a map[string]string by a variable", `ms := map[string]string{}; ms[k] = v`, false},
		{"the helper's call", `_, _ = signedsend.Do(nil, req, map[string]string{signedsend.HolderAssertion: v})`, false},
		{"a header read", `_ = req.Header.Get(k)`, false},
		{"assigned to a map type", `var mh map[string][]string = req.Header; mh["X-Holder-Assertion"] = []string{v}`, true},
		{"converted to textproto.MIMEHeader", `textproto.MIMEHeader(req.Header).Set("X-Holder-Assertion", v)`, true},
		{"assigned to an interface", `var s interface{ Set(string, string) } = req.Header; s.Set("X-Holder-Assertion", v)`, true},
		{"Set promoted through an embedding", `var e struct{ http.Header }; e.Set("X-Holder-Assertion", v)`, true},
		{"stored in a struct field", `var x struct{ H http.Header }; x.H = req.Header; _ = json.Unmarshal(nil, &x)`, true},
		{"stored in a slice", `hs := make([]http.Header, 1); hs[0] = req.Header`, true},
		{"stored in a map", `hm := map[string]http.Header{}; hm[k] = req.Header`, true},
		{"a pointer to a pointer", `hp := &req.Header; _ = json.Unmarshal(nil, &hp)`, true},
		{"passed to an interface's method", `var pu putter; pu.put(req.Header)`, true},
		{"a method expression's other parameter", `tee.two(tee{}, req.Header, nil)`, true},
		{"a method expression's header parameter", `tee.two(tee{}, nil, req.Header)`, false},
		{"a map written, then made the request's headers", `mm2 := map[string][]string{}; mm2[signedsend.HolderAssertion] = []string{v}; req.Header = mm2`, true},
		{"a map made the request's trailers", `req.Trailer = mm`, true},
		{"a map written through a header pointer", `hp := &req.Header; *hp = mm`, true},
		{"a map in a request literal", `_ = &http.Request{Header: mm}`, true},
		{"a map in a positional literal", `_ = []http.Header{mm}`, true},
		{"a map declared as a header", `var hh http.Header = mm; _ = hh`, true},
		{"a map returned as a header", `_ = func() http.Header { return mm }`, true},
		{"a map passed as a header", `addCT(mm)`, true},
		{"a map sent as a header", `ch := make(chan http.Header, 1); ch <- mm`, true},
		{"a map in a positional struct literal", `_ = struct{ H http.Header }{mm}`, true},
		{"a map in a positional array literal", `_ = [1]http.Header{mm}`, true},
		{"a map in a keyed map literal", `_ = map[string]http.Header{"a": mm}`, true},
		{"a map in a keyed slice literal", `_ = []http.Header{0: mm}`, true},
		{"a map in a keyed array literal", `_ = [1]http.Header{0: mm}`, true},
		{"a map passed as a variadic header", `addAll(mm)`, true},
		{"maps passed as a header slice", `addAll([]http.Header{}...)`, false},
		{"a map from a call made the request's headers", `req.Header, _ = build()`, true},
		{"a map from a comma-ok index made the request's headers", `byName := map[string]map[string][]string{}; var ok2 bool; req.Header, ok2 = byName[k]; _ = ok2`, true},
		{"a map from a type assertion made the request's headers", `var anyv any = mm; req.Header, _ = anyv.(map[string][]string)`, true},
		{"a map from a call written through a header pointer", `hp := &req.Header; *hp, _ = build()`, true},
		{"maps from a call declared as headers", `var ha, hb http.Header = twoMaps(); _, _ = ha, hb`, true},
		{"a header from a call stored in a field", `var x struct{ H http.Header }; x.H, _ = hdr()`, true},
		{"a header from a call assigned to another type", `var anyh any; anyh, _ = hdr(); _ = anyh`, true},
		{"a header from a call kept in a variable", `h, err := hdr(); _, _ = h, err`, false},
		{"a header from a call dropped", `_, err := hdr(); _ = err`, false},
		{"a header from a call dropped by assignment", `var err error; _, err = hdr(); _ = err`, false},
		{"a map from a call dropped by assignment", `var err error; _, err = build(); _ = err`, false},
		{"a map from a call kept as a map", `mm2, err := build(); _, _ = mm2, err`, false},
		{"a map kept as a map", `mm2 := mm; _ = mm2`, false},
		{"a map literal made the request's headers", `req.Header = map[string][]string{"Accept": {v}}`, false},
		{"no headers", `req.Header = nil`, false},
		{"an embedding built from it", `struct{ http.Header }{req.Header}.Set("Content-Type", v)`, true},
		{"copied into by maps.Copy", `maps.Copy(req.Header, mm)`, true},
		{"passed to a generic function", `setAny(req.Header, k, v)`, true},
		{"written through a pointer", `hp := &req.Header; *hp = other.Header.Clone()`, true},
		{"a request's headers replaced through an embedding", `r := struct{ *http.Request }{req}; r.Header = other.Header`, true},
		{"a request's trailers replaced", `req.Trailer = other.Header`, true},
		{"assigned to a map variable", `var mh map[string][]string; mh = req.Header; mh[k] = nil`, true},
		{"returned as a map type", `_ = func() map[string][]string { return req.Header }`, true},
		{"sent on a channel", `ch := make(chan http.Header, 1); ch <- req.Header`, true},
		{"passed to a method of a generic type", `sink[http.Header]{}.put(req.Header)`, true},
		{"passed to a function value", `fn := addCT; fn(req.Header)`, true},
		{"passed to this module's function taking one", `addCT(req.Header)`, false},
		{"returned as itself", `_ = func() http.Header { return req.Header }`, false},
		{"a clone read", `h := other.Header.Clone(); _ = h.Get(k)`, false},
		{"compared with nil", `_ = req.Header == nil`, false},
		{"its length", `_ = len(req.Header)`, false},
		{"a range over it", `for k2 := range other.Header { _ = k2 }`, false},
		{"a delete from it", `delete(req.Header, k)`, false},
		{"a pointer taken and read", `hp := &req.Header; _ = hp.Get(k)`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := `package p

import (
	"net/http"
	"encoding/json"
	"maps"
	"net/textproto"
	"net/url"

	"github.com/SmartHealthNetwork/shn-sdk/internal/signedsend"
)

const hdrName = "X-Holder-Assertion"
const ctName = "Content-Type"

var (
	_ = url.Values{}
	_ = signedsend.HolderAssertion
	_ = textproto.MIMEHeader{}
	_ = json.Unmarshal
	_ = maps.Copy[map[string]int, map[string]int]
)

// addCT is a function of the package's own: its writes are read where it
// writes them.
func addCT(h http.Header) { h.Set(ctName, "text/plain") }

// setAny writes through a type parameter, where the fence cannot read it.
func setAny[M ~map[string][]string](m M, k, v string) { m[k] = []string{v} }

// addAll is a variadic function of the package's own.
func addAll(hs ...http.Header) {
	for _, h := range hs {
		addCT(h)
	}
}

// build, twoMaps and hdr return header maps alongside other values.
func build() (map[string][]string, error) { return map[string][]string{"Accept": {"x"}}, nil }

func twoMaps() (map[string][]string, map[string][]string) { return nil, nil }

func hdr() (http.Header, error) { return http.Header{}, nil }

// putter's method may be implemented anywhere.
type putter interface{ put(http.Header) }

// tee's method takes a header map after another parameter.
type tee struct{}

func (tee) two(a any, h http.Header) {}

// sink's methods take a type parameter, where the fence cannot read a write.
type sink[T any] struct{}

func (sink[T]) put(T) {}

func f(req, other *http.Request, k, v string, m map[string]string, mm map[string][]string) {
	_, _, _, _ = k, v, m, mm
	` + tc.src + `
}
`
			problems, err := signedHeaderProblems(map[string]map[string]string{"p": {"p/p.go": src}})
			if err != nil {
				t.Fatal(err)
			}
			if (len(problems) > 0) != tc.found {
				t.Fatalf("problems %q, want found=%v", problems, tc.found)
			}
		})
	}
}

// A function outside this module that takes a header map is outside the
// fence, which never reads its body: passing one to it is found.
func TestSignedHeaderFence_AFunctionOutsideTheModule(t *testing.T) {
	ext := "package ext\n\nimport \"net/http\"\n\nfunc Keep(h http.Header) {}\n"
	for _, tc := range []struct {
		call  string
		found bool
	}{
		{`ext.Keep(req.Header)`, true},
		{`ext.Keep(nil)`, false},
	} {
		p := "package p\n\nimport (\n\t\"example.com/ext\"\n\t\"net/http\"\n)\n\nfunc f(req *http.Request) { " + tc.call + " }\n"
		problems, err := signedHeaderProblems(map[string]map[string]string{"example.com/ext": {"example.com/ext/ext.go": ext}, "p": {"p/p.go": p}})
		if err != nil {
			t.Fatal(err)
		}
		if (len(problems) > 0) != tc.found {
			t.Errorf("%s: problems %q, want found=%v", tc.call, problems, tc.found)
		}
	}
}

// A package the fence cannot type-check is an error, never read as clean.
func TestSignedHeaderFence_AnUncheckablePackageFails(t *testing.T) {
	if _, err := signedHeaderProblems(map[string]map[string]string{"p": {"p/p.go": "package p\nfunc f() { undefined() }\n"}}); err == nil {
		t.Fatal("a package that does not type-check read as clean")
	}
}

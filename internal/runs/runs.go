// Package runs finds, per package of a Go module, each external program it
// starts, and judges that against a rules file the caller gives.
//
// Built on: the standard library (go/parser, go/types), internal/gitrun for
// the tracked files.
// Adds: the program named to os/exec's Command, CommandContext and LookPath,
// resolved from the module's own source; the rules file; the judgement.
// Configured by: the caller's rules file. The package holds no program name,
// no package name and no layer of any product.
package runs

// The replaceability rule, for programs: every external program is run from
// one package, its adapter, so replacing it means changing that package
// (issue 7). Nothing here runs the program; this reads the tree and counts who
// does. Which package imports a library is the caller's own layers rules.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strings"
)

// Finding is one thing the check reports: a file and line where there is one.
type Finding struct {
	File string
	Line int
	What string
}

// sourceFile is a non-test Go file of the tree worth reading: not a test, not
// under testdata, not hidden.
func sourceFile(name string) bool {
	if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "testdata" || part == "vendor" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") {
			return false
		}
	}
	return true
}

// packageOf is the folder a file is in, "." for the root.
func packageOf(name string) string { return path.Dir(name) }

// Runs is each package's external programs: the name passed to exec.Command,
// exec.CommandContext or exec.LookPath, as its last element (so
// /usr/bin/qemu and qemu are one). Names are resolved with go/types, so a
// constant counts as the program it holds, declared in this package or in
// another of the module.
//
// A variable passes only when its value comes from a constant or from a
// function of the package that runs it. Any other source (a parameter, a
// field, a map, a range, another package's function) is a finding of its own: the
// program is then chosen somewhere the check cannot see.
//
// Types are checked from the module's own source only (go/types, with an
// importer that serves the module's packages and a stand-in, with the right
// import path and no members, for the rest). That is enough to know a call is
// os/exec's and what a constant holds, and it costs no `go list`, download or
// build, which the standard library's source importer needs.
//
// It does not see a program started any other way than the three calls above:
// a dot-imported os/exec, an exec.Cmd with its Path set, os.StartProcess and
// syscall.Exec. Those are not findings; nothing reports them.
func Runs(files map[string][]byte, module string) (runs map[string][]string, unchecked []Finding, err error) {
	fset := token.NewFileSet()
	byDir, err := parseSources(fset, files)
	if err != nil {
		return nil, nil, err
	}
	c := &checker{module: module, fset: fset, byDir: byDir, pkgs: map[string]*types.Package{}, infos: map[string]*types.Info{}}
	found := map[string]map[string]bool{}
	for _, dir := range sortedKeys(byDir) {
		c.check(dir)
		for _, f := range byDir[dir] {
			unchecked = append(unchecked, c.programsOf(dir, f, found)...)
		}
	}
	runs = map[string][]string{}
	for dir, set := range found {
		runs[dir] = sortedKeys(set)
	}
	return runs, unchecked, nil
}

// parseSources parses each source file of files, by the folder it is in.
func parseSources(fset *token.FileSet, files map[string][]byte) (map[string][]*ast.File, error) {
	byDir := map[string][]*ast.File{}
	for _, name := range sortedKeys(files) {
		if !sourceFile(name) {
			continue
		}
		f, err := parser.ParseFile(fset, name, files[name], parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		byDir[packageOf(name)] = append(byDir[packageOf(name)], f)
	}
	return byDir, nil
}

// programsOf adds to found, under dir, each program file f runs, and is a
// finding for each name it cannot follow.
func (c *checker) programsOf(dir string, f *ast.File, found map[string]map[string]bool) []Finding {
	var unchecked []Finding
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		arg, ok := c.programArg(dir, call)
		if !ok {
			return true
		}
		prog, bad := c.program(dir, f, arg)
		switch {
		case bad:
			at := c.fset.Position(arg.Pos())
			unchecked = append(unchecked, Finding{File: at.Filename, Line: at.Line,
				What: "program from an unchecked variable: its name is chosen where this check cannot see it"})
		case prog != "":
			if found[dir] == nil {
				found[dir] = map[string]bool{}
			}
			found[dir][path.Base(prog)] = true
		}
		return true
	})
	return unchecked
}

// checker type-checks the module's packages from source, each once.
type checker struct {
	module string
	fset   *token.FileSet
	byDir  map[string][]*ast.File
	pkgs   map[string]*types.Package // by folder
	infos  map[string]*types.Info
}

// Import serves the module's own packages from source and a stand-in for any
// other, with its import path and no members.
func (c *checker) Import(p string) (*types.Package, error) {
	if dir, ok := c.dirOf(p); ok {
		return c.check(dir), nil
	}
	name := path.Base(p)
	if strings.HasPrefix(name, "v") && len(name) <= 3 { // a /v2 suffix
		name = path.Base(path.Dir(p))
	}
	pkg := types.NewPackage(p, strings.ReplaceAll(name, "-", "_"))
	pkg.MarkComplete()
	return pkg, nil
}

func (c *checker) dirOf(p string) (string, bool) {
	switch {
	case p == c.module:
		_, ok := c.byDir["."]
		return ".", ok
	case strings.HasPrefix(p, c.module+"/"):
		dir := strings.TrimPrefix(p, c.module+"/")
		_, ok := c.byDir[dir]
		return dir, ok
	}
	return "", false
}

func (c *checker) check(dir string) *types.Package {
	if pkg, ok := c.pkgs[dir]; ok {
		return pkg
	}
	files := c.byDir[dir]
	// Set before checking, so an import cycle (which Go forbids) cannot loop.
	ip := c.module
	if dir != "." {
		ip += "/" + dir
	}
	pkg := types.NewPackage(ip, "x")
	c.pkgs[dir] = pkg
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
		Defs:  map[*ast.Ident]types.Object{},
	}
	c.infos[dir] = info
	conf := types.Config{
		Importer: c,
		// Both a file for Linux and one for Windows can declare a name; a
		// stand-in has no members. Neither is the check's business, and what
		// it can still resolve it resolves.
		Error:            func(error) {},
		FakeImportC:      true,
		IgnoreFuncBodies: false,
	}
	checked, _ := conf.Check(ip, c.fset, files, info) //nolint:errcheck // errors are swallowed above on purpose
	if checked != nil {
		c.pkgs[dir] = checked
	}
	return c.pkgs[dir]
}

// programArg is the argument naming the program when call is exec.Command,
// exec.CommandContext or exec.LookPath.
func (c *checker) programArg(dir string, call *ast.CallExpr) (ast.Expr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, false
	}
	pn, ok := c.infos[dir].Uses[id].(*types.PkgName)
	if !ok || pn.Imported().Path() != "os/exec" {
		return nil, false
	}
	switch sel.Sel.Name {
	case "Command", "LookPath":
		return first(call.Args)
	case "CommandContext":
		if len(call.Args) < 2 {
			return nil, false
		}
		return call.Args[1], true
	}
	return nil, false
}

func first(args []ast.Expr) (ast.Expr, bool) {
	if len(args) == 0 {
		return nil, false
	}
	return args[0], true
}

// program is the name arg holds: its constant value, or "" and bad when it
// comes from a source the check cannot follow.
func (c *checker) program(dir string, file *ast.File, arg ast.Expr) (prog string, bad bool) {
	info := c.infos[dir]
	if tv, ok := info.Types[arg]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), false
	}
	if c.fromOwnPackage(dir, file, arg, 0) {
		return "", false
	}
	return "", true
}

// fromOwnPackage is whether the value of e comes from a constant or from a
// function declared in the package at dir: through a call of one, or a local
// variable every assignment to which does.
func (c *checker) fromOwnPackage(dir string, file *ast.File, e ast.Expr, depth int) bool {
	if depth > 8 {
		return false
	}
	if tv, ok := c.infos[dir].Types[e]; ok && tv.Value != nil {
		return true
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return c.fromOwnPackage(dir, file, e.X, depth+1)
	case *ast.BinaryExpr:
		return c.fromOwnPackage(dir, file, e.X, depth+1) && c.fromOwnPackage(dir, file, e.Y, depth+1)
	case *ast.CallExpr:
		return c.callsOwnFunc(dir, e)
	case *ast.Ident:
		return c.localFromOwnPackage(dir, file, e, depth)
	}
	return false
}

// callsOwnFunc is whether call calls a function declared in the package at dir.
func (c *checker) callsOwnFunc(dir string, call *ast.CallExpr) bool {
	var id *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		id = fn
	case *ast.SelectorExpr:
		id = fn.Sel
	}
	if id == nil {
		return false
	}
	obj, ok := c.infos[dir].Uses[id].(*types.Func)
	return ok && obj.Pkg() != nil && obj.Pkg() == c.pkgs[dir]
}

// localFromOwnPackage is whether id is a local variable every assignment to
// which comes from the package at dir.
func (c *checker) localFromOwnPackage(dir string, file *ast.File, id *ast.Ident, depth int) bool {
	v, ok := c.infos[dir].Uses[id].(*types.Var)
	if !ok || v.Parent() == v.Pkg().Scope() {
		return false // a package variable: set from anywhere
	}
	assigned := c.assignments(dir, file, v)
	if len(assigned) == 0 {
		return false // a parameter: its value is the caller's
	}
	for _, rhs := range assigned {
		if !c.fromOwnPackage(dir, file, rhs, depth+1) {
			return false
		}
	}
	return true
}

// assignments is every expression assigned to the local variable v in the
// package, from `v := e`, `v = e` and `var v = e`. One of a multiple-value call's results is followed as the call. A variable
// whose address is taken reports an unfollowable one.
func (c *checker) assignments(dir string, _ *ast.File, v *types.Var) []ast.Expr {
	info, pkg := c.infos[dir], c.pkgs[dir]
	var out []ast.Expr
	for _, f := range c.byDir[dir] {
		ast.Inspect(f, func(n ast.Node) bool {
			out = append(out, assignedBy(pkg, info, n, v)...)
			return true
		})
	}
	return out
}

// assignedBy is what node n assigns to v: the right-hand sides of an
// assignment or declaration, or an unfollowable one where v's address is taken
// or a range assigns it, each in or out of parentheses.
func assignedBy(pkg *types.Package, info *types.Info, n ast.Node, v *types.Var) []ast.Expr {
	var out []ast.Expr
	switch n := n.(type) {
	case *ast.AssignStmt:
		out = assignedByStmt(pkg, info, n, v)
	case *ast.ValueSpec:
		for i, id := range n.Names {
			if info.Defs[id] == v && i < len(n.Values) {
				out = append(out, n.Values[i])
			}
		}
	case *ast.RangeStmt:
		for _, e := range []ast.Expr{n.Key, n.Value} {
			if isVar(pkg, info, e, v) {
				out = append(out, &ast.BadExpr{}) // its values are the ranged thing's
			}
		}
	case *ast.UnaryExpr:
		if n.Op == token.AND && isVar(pkg, info, n.X, v) {
			out = append(out, &ast.BadExpr{})
		}
	}
	return out
}

// assignedByStmt is what the assignment n assigns to v, one right-hand side
// for each time v is on its left.
func assignedByStmt(pkg *types.Package, info *types.Info, n *ast.AssignStmt, v *types.Var) []ast.Expr {
	var out []ast.Expr
	for i, lhs := range n.Lhs {
		if !isVar(pkg, info, lhs, v) {
			continue
		}
		switch {
		case len(n.Rhs) == len(n.Lhs):
			out = append(out, n.Rhs[i])
		case len(n.Rhs) == 1:
			out = append(out, n.Rhs[0]) // one of a multiple-value call's results
		default:
			out = append(out, &ast.BadExpr{})
		}
	}
	return out
}

// isVar is whether e, in or out of parentheses, is v by name: where it is
// declared, as the type check recorded it, and anywhere else, the object its
// name finds from where it stands. That holds where the check recorded no use,
// as for a range over a value of a stand-in package; a range variable's scope
// starts at the body, so its declaration is not found that way. With no scope
// there, the name is taken for v when it is spelled as v is.
func isVar(pkg *types.Package, info *types.Info, e ast.Expr, v *types.Var) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok {
		return false
	}
	if obj, ok := info.Defs[id]; ok {
		return obj == v
	}
	s := pkg.Scope().Innermost(id.Pos())
	if s == nil {
		return id.Name == v.Name()
	}
	_, obj := s.LookupParent(id.Name, id.Pos())
	return obj == v
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

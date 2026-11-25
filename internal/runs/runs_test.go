package runs

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"
	"testing"
)

const testModule = "example.com/app"

func tree(files map[string]string) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range files {
		out[k] = []byte(v)
	}
	return out
}

func prog(name string) string {
	return "package x\n\nimport \"os/exec\"\n\nfunc run() { _ = exec.Command(" + name + ") }\n"
}

// Each package's programs are counted, whether named by a literal or a
// constant in the package, and a path is its last element, so two packages
// that run one program both name it.
//
// What it costs when this fails: two packages run the same program and the caller's count of
// its runners sees one, so the tool's one adapter per program stops being true unseen.
func TestEachPackageThatRunsAProgramIsCounted(t *testing.T) {
	t.Parallel()
	runs, unchecked, err := Runs(tree(map[string]string{
		"a/a.go": prog(`"qemu"`),
		"b/b.go": prog(`"/usr/bin/qemu"`),
		"d/d.go": "package d\n\nimport \"os/exec\"\n\nconst local = \"git\"\n\nfunc run() { _, _ = exec.LookPath(local) }\n",
	}), testModule)
	if err != nil || len(unchecked) != 0 {
		t.Fatalf("%v %v", err, unchecked)
	}
	want := map[string][]string{"a": {"qemu"}, "b": {"qemu"}, "d": {"git"}}
	if !maps.EqualFunc(runs, want, slices.Equal) {
		t.Errorf("runs %v, want %v", runs, want)
	}
}

// A constant counts as the program it holds, from two packages, and also when
// it is declared in another package of the module.
//
// What it costs when this fails: a program named through a constant is not counted, and the second
// package that runs it goes unseen.
func TestAConstantCountsAsItsProgram(t *testing.T) {
	t.Parallel()
	runs, _, err := Runs(tree(map[string]string{
		"consts/c.go": "package consts\n\nconst Bin = \"/usr/bin/\" + \"tool\"\n",
		"c/c.go":      "package c\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/app/consts\"\n)\n\nfunc run() { _ = exec.Command(consts.Bin) }\n",
		"a/a.go":      "package a\n\nimport \"os/exec\"\n\nconst qemuBin = \"/usr/bin/qemu-system-x86_64\"\n\nfunc run() { _ = exec.Command(qemuBin) }\n",
		"b/b.go":      "package b\n\nimport \"os/exec\"\n\nconst qemuBin = \"/usr/bin/qemu-system-x86_64\"\n\nfunc run() { _ = exec.CommandContext(nil, qemuBin) }\n",
	}), testModule)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"a": {"qemu-system-x86_64"}, "b": {"qemu-system-x86_64"}, "c": {"tool"}}
	if !maps.EqualFunc(runs, want, slices.Equal) {
		t.Errorf("runs %v, want %v (c's is the constant declared in consts)", runs, want)
	}
}

// A variable whose value comes from a constant or a function of its own
// package passes; the program it names counts.
//
// What it costs when this fails: a program named through the package's own constant or function is
// refused, and every adapter is made to spell the name out.
func TestAVariableFromTheAdapterPasses(t *testing.T) {
	t.Parallel()
	_, unchecked, err := Runs(tree(map[string]string{
		"a/a.go": `package a

import (
	"os"
	"os/exec"
)

const fallback = "git"

func which() string { return fallback }

func viaFunction() { _ = exec.Command(which()) }

func pair() (string, string) { return fallback, "x" }

func viaPair() {
	_, p := pair()
	_ = exec.Command(p)
}

func viaLocal() {
	p := which()
	if p == "" {
		p = fallback
	}
	_ = exec.Command(p)
}

// Another p, which a range over a stand-in sets with no type recorded.
func elsewhere() {
	p := "x"
	for _, p = range os.Args {
		_ = p
	}
}
`,
	}), testModule)
	if err != nil || len(unchecked) != 0 {
		t.Errorf("%v, %v", err, unchecked)
	}
}

// A name the type check left unrecorded is looked up from where it stands,
// and where there is no scope to look in, it is the variable it is spelled as,
// in or out of parentheses.
//
// What it costs when this fails: a local that a range over another package's
// value sets is followed as if nothing set it, and each name the range yields
// runs with no finding.
func TestANameWithNoTypeAndNoScopeIsTheVariableItSpells(t *testing.T) {
	t.Parallel()
	pkg := types.NewPackage("example.com/app/a", "a")
	v := types.NewVar(token.NoPos, pkg, "p", types.Typ[types.String])
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	cases := []struct {
		e    ast.Expr
		want bool
	}{
		{&ast.Ident{Name: "p"}, true},
		{&ast.ParenExpr{X: &ast.Ident{Name: "p"}}, true},
		{&ast.Ident{Name: "q"}, false},
		{&ast.BasicLit{Kind: token.STRING, Value: `"p"`}, false},
	}
	ran := 0
	for _, c := range cases {
		if got := isVar(pkg, info, c.e, v); got != c.want {
			t.Errorf("isVar(%#v) = %v, want %v", c.e, got, c.want)
		}
		ran++
	}
	if ran != len(cases) || ran == 0 {
		t.Fatalf("ran %d of %d cases", ran, len(cases))
	}
}

// Any other source is a finding of its own: a parameter, a package variable,
// another package's function, a field, a range that assigns the variable.
//
// What it costs when this fails: a program named by a parameter, a field, a range or another package passes
// unchecked, and the check never learns which program runs.
func TestAVariableFromElsewhereIsAFinding(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"parameter":                       "package a\n\nimport \"os/exec\"\n\nfunc run(p string) { _ = exec.Command(p) }\n",
		"package var":                     "package a\n\nimport \"os/exec\"\n\nvar p = \"git\"\n\nfunc run() { _ = exec.Command(p) }\n",
		"field":                           "package a\n\nimport \"os/exec\"\n\ntype T struct{ P string }\n\nfunc (t T) run() { _ = exec.Command(t.P) }\n",
		"other package":                   "package a\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/app/b\"\n)\n\nfunc run() { _ = exec.Command(b.Which()) }\n",
		"local from them":                 "package a\n\nimport \"os/exec\"\n\nfunc run(p string) {\n\tq := p\n\t_ = exec.Command(q)\n}\n",
		"pair from them":                  "package a\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/app/b\"\n)\n\nfunc run() {\n\t_, p := b.Pair()\n\t_ = exec.Command(p)\n}\n",
		"address taken":                   "package a\n\nimport \"os/exec\"\n\nfunc set(*string) {}\n\nfunc run() {\n\tp := \"git\"\n\tset(&p)\n\t_ = exec.Command(p)\n}\n",
		"range assigns it":                "package a\n\nimport \"os/exec\"\n\nfunc run(names []string) {\n\tp := \"git\"\n\tfor _, p = range names {\n\t\t_ = exec.Command(p)\n\t}\n}\n",
		"range assigns the key":           "package a\n\nimport \"os/exec\"\n\nfunc run(m map[string]int) {\n\tp := \"git\"\n\tfor p = range m {\n\t\t_ = exec.Command(p)\n\t}\n}\n",
		"range defines it":                "package a\n\nimport \"os/exec\"\n\nfunc run(names []string) {\n\tfor _, p := range names {\n\t\t_ = exec.Command(p)\n\t\tp = \"git\"\n\t}\n}\n",
		"range over os.Args":              "package a\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n)\n\nfunc run() {\n\tp := \"git\"\n\tfor _, p = range os.Args[1:] {\n\t\t_ = exec.Command(p)\n\t}\n}\n",
		"range over strings.Fields":       "package a\n\nimport (\n\t\"os/exec\"\n\t\"strings\"\n)\n\nfunc run(s string) {\n\tp := \"git\"\n\tfor _, p = range strings.Fields(s) {\n\t\t_ = exec.Command(p)\n\t}\n}\n",
		"range over an iter.Seq":          "package a\n\nimport (\n\t\"iter\"\n\t\"os/exec\"\n)\n\nfunc run(seq iter.Seq[string]) {\n\tp := \"git\"\n\tfor p = range seq {\n\t\t_ = exec.Command(p)\n\t}\n}\n",
		"range assigns it in parentheses": "package a\n\nimport \"os/exec\"\n\nfunc run(m map[string]int) {\n\tp := \"git\"\n\tfor (p) = range m {\n\t\t_ = exec.Command(p)\n\t}\n}\n",
		"assigned in parentheses":         "package a\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/app/b\"\n)\n\nfunc run() {\n\tp := \"git\"\n\t(p) = b.Which()\n\t_ = exec.Command(p)\n}\n",
		"address taken in parentheses":    "package a\n\nimport \"os/exec\"\n\nfunc set(*string) {}\n\nfunc run() {\n\tp := \"git\"\n\tset(&(p))\n\t_ = exec.Command(p)\n}\n",
	}
	ran := 0
	for name, src := range cases {
		_, unchecked, err := Runs(tree(map[string]string{
			"a/a.go": src,
			"b/b.go": "package b\n\nfunc Which() string { return \"git\" }\n\nfunc Pair() (string, string) { return \"git\", \"git\" }\n",
		}), testModule)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(unchecked) != 1 || !strings.Contains(unchecked[0].What, "unchecked variable") || unchecked[0].File != "a/a.go" {
			t.Errorf("%s: %v", name, unchecked)
		}
		ran++
	}
	if len(cases) < 5 || ran != len(cases) {
		t.Fatalf("ran %d of %d cases, and fewer than five prove little", ran, len(cases))
	}
}

// Calls that are not os/exec's are not programs.
//
// What it costs when this fails: a method called Command on any other type is taken for a program,
// and the check refuses code that runs nothing.
func TestOnlyOsExecNamesAProgram(t *testing.T) {
	t.Parallel()
	runs, unchecked, err := Runs(tree(map[string]string{
		"a/a.go": "package a\n\nimport \"example.org/exec\"\n\nfunc run(p string) { _ = exec.Command(p) }\n",
		"b/b.go": "package b\n\ntype c struct{}\n\nfunc (c) Command(p string) {}\n\nfunc run(p string) { c{}.Command(p) }\n",
	}), testModule)
	if err != nil || len(runs) != 0 || len(unchecked) != 0 {
		t.Errorf("%v %v %v", runs, unchecked, err)
	}
}

// Only the code a program ships is read: a test, testdata, vendor, and a dot
// or underscore folder run nothing that counts, and an unparsable one of them
// is no error. A source file that does not parse is an error that names it.
//
// What it costs when this fails: a test that runs git is counted as a second runner and a clean
// tree is refused, or a file the check could not read passes as running nothing.
func TestRunsReadsOnlyTheShippedSource(t *testing.T) {
	t.Parallel()
	runs, unchecked, err := Runs(tree(map[string]string{
		"a/a.go":           prog(`"git"`),
		"a/a_test.go":      prog(`"qemu"`),
		"a/testdata/x.go":  prog(`"qemu"`),
		"vendor/v/v.go":    prog(`"qemu"`),
		".hidden/h.go":     prog(`"qemu"`),
		"_old/o.go":        prog(`"qemu"`),
		"b/broken_test.go": "package b\n\nfunc {",
		"b/README.md":      "exec.Command(\"qemu\")",
	}), testModule)
	want := map[string][]string{"a": {"git"}}
	if err != nil || len(unchecked) != 0 || !maps.EqualFunc(runs, want, slices.Equal) {
		t.Errorf("runs %v, unchecked %v, err %v; want %v and nothing else", runs, unchecked, err, want)
	}
	_, _, err = Runs(tree(map[string]string{"c/c.go": "package c\n\nfunc {"}), testModule)
	if err == nil || !strings.HasPrefix(err.Error(), "c/c.go: ") {
		t.Errorf("a source file that does not parse gave %v, want an error that names c/c.go", err)
	}
}

package runs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	res, err := g.Run(context.Background(), dir, full...)
	if err != nil || res.Code != 0 {
		t.Fatalf("git %s failed: %v\n%s", args[0], err, res.Stderr)
	}
}

// module is a git repository built now: a go.mod for testModule and files,
// all tracked.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	all := map[string]string{"go.mod": "module " + testModule + "\n\ngo 1.22\n"}
	for k, v := range files {
		all[k] = v
	}
	for name, body := range all {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "m")
	return dir
}

func rulesFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runRuns(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb)
	return code, out.String() + errb.String()
}

func src(pkg, name string) string {
	return "package " + pkg + "\n\nimport \"os/exec\"\n\nfunc Run() { _ = exec.Command(" + name + ") }\n"
}

// A clean module is green, and the line counts it.
//
// What it costs when this fails: a tree that follows its rules is refused.
func TestACleanModuleIsGreen(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{
		"consts/c.go":  "package consts\n\nconst Git = \"git\"\n",
		"adapter/a.go": "package adapter\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/app/consts\"\n)\n\nfunc Run() { _ = exec.Command(consts.Git) }\n",
		"dev/d.go":     src("dev", `"make"`),
	})
	code, out := runRuns(t, "--root", dir, "--rules", rulesFile(t, "# rows\ngit adapter\nmake dev\n"))
	if code != 0 || !strings.Contains(out, "green, 2 programs in 2 packages, 0 unseen sites") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// A program started from a second package is red, and the line names both.
//
// What it costs when this fails: a second adapter for a program passes, and the
// one-adapter rule stops being true unseen.
func TestAProgramFromASecondPackageIsRed(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{"a/a.go": src("a", `"git"`), "b/b.go": src("b", `"git"`)})
	code, out := runRuns(t, "--root", dir, "--rules", rulesFile(t, "git a\n"))
	if code != 1 || !strings.Contains(out, "package b starts git, which only a may start") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// A program with no row is red.
//
// What it costs when this fails: a program nobody ruled on starts with no word.
func TestAProgramWithNoRowIsRed(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{"a/a.go": src("a", `"git"`), "b/b.go": src("b", `"qemu"`)})
	code, out := runRuns(t, "--root", dir, "--rules", rulesFile(t, "git a\n"))
	if code != 1 || !strings.Contains(out, "package b starts qemu, which has no row") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// A variable program outside the unseen row is red, with its file and line; on
// the row it is not.
//
// What it costs when this fails: a program chosen where the check cannot see
// passes, or a package the rules allow it in is refused.
func TestAnUnseenSiteIsRedOutsideTheUnseenRow(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{"a/a.go": "package a\n\nimport \"os/exec\"\n\nfunc Run(p string) {\n\t_ = exec.Command(p)\n}\n"})
	code, out := runRuns(t, "--root", dir, "--rules", rulesFile(t, "git b\n"))
	if code != 1 || !strings.Contains(out, "a/a.go:6: a program chosen where the check cannot see it, in package a") {
		t.Fatalf("exit %d: %s", code, out)
	}
	code, out = runRuns(t, "--root", dir, "--rules", rulesFile(t, "unseen a\n"))
	if code != 0 || !strings.Contains(out, "0 programs in 0 packages, 1 unseen sites") {
		t.Fatalf("on the row: exit %d: %s", code, out)
	}
}

// A row, or a package on a row, that nothing used is red.
//
// What it costs when this fails: a rule outlives the code it covered and the
// rules file says more than the tree does.
func TestAnUnusedRowIsRed(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{"a/a.go": src("a", `"git"`)})
	code, out := runRuns(t, "--root", dir, "--rules", rulesFile(t, "git a z\nqemu a\nunseen a\n"))
	for _, want := range []string{
		"the row for git names package z, which does not start it",
		"the row for qemu names package a, which does not start it",
		"the unseen row names package a, which has no such site",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("exit %d", code)
	}
}

// A source file that does not parse is red and named; a test file, testdata and
// another module in the tree are not read.
//
// What it costs when this fails: a file the check could not read passes, or a
// test that runs git counts as a runner.
func TestOnlyTheModulesShippedSourceIsRead(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{
		"a/a.go":          src("a", `"git"`),
		"a/a_test.go":     src("a", `"qemu"`),
		"a/testdata/x.go": src("x", `"qemu"`),
		"other/go.mod":    "module example.com/other\n",
		"other/o.go":      src("other", `"qemu"`),
	})
	rules := rulesFile(t, "git a\n")
	if code, out := runRuns(t, "--root", dir, "--rules", rules); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	bad := module(t, map[string]string{"c/c.go": "package c\n\nfunc {"})
	code, out := runRuns(t, "--root", bad, "--rules", rules)
	if code != 1 || !strings.Contains(out, "c/c.go: does not parse") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// list prints what each package starts and the unseen sites, and judges nothing.
//
// What it costs when this fails: a caller writing its first rules file has no
// list to start from.
func TestListPrintsAndJudgesNothing(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{
		"a/a.go": src("a", `"/usr/bin/git"`),
		"b/b.go": "package b\n\nimport \"os/exec\"\n\nfunc Run(p string) { _ = exec.Command(p) }\n",
	})
	code, out := runRuns(t, "list", "--root", dir)
	if code != 0 || out != "a git\nunseen b/b.go:5\n" {
		t.Fatalf("exit %d: %q", code, out)
	}
}

// Anything that cannot be read or is not asked for is exit 2, never green.
//
// What it costs when this fails: a mistyped rules path or a folder that is not
// a repository passes.
func TestUsageAndUnreadableAreExitTwo(t *testing.T) {
	t.Parallel()
	dir := module(t, map[string]string{"a/a.go": src("a", `"git"`)})
	rules := rulesFile(t, "git a\n")
	cases := map[string][]string{
		"no rules":            {"--root", dir},
		"rules is missing":    {"--root", dir, "--rules", filepath.Join(dir, "none")},
		"an argument":         {"--root", dir, "--rules", rules, "extra"},
		"empty rules":         {"--root", dir, "--rules", rulesFile(t, "# nothing\n")},
		"a row with no pkg":   {"--root", dir, "--rules", rulesFile(t, "git\n")},
		"two rows":            {"--root", dir, "--rules", rulesFile(t, "git a\ngit b\n")},
		"a package twice":     {"--root", dir, "--rules", rulesFile(t, "git a a\n")},
		"not a work tree":     {"--root", t.TempDir(), "--rules", rules},
		"list takes no rules": {"list", "--rules", rules},
	}
	for name, args := range cases {
		if code, out := runRuns(t, args...); code != 2 {
			t.Errorf("%s: exit %d: %s", name, code, out)
		}
	}
}

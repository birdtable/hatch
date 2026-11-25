package age

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// now is the moment every row counts a module's age to.
var now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

// release is a module version the file proxy serves, with the time it says it was released.
type release struct {
	path, version string
	age           time.Duration
}

// proxyURL is the file:// URL of dir, as the go command reads it on every system.
func proxyURL(dir string) string {
	d := filepath.ToSlash(dir)
	if !strings.HasPrefix(d, "/") {
		d = "/" + d
	}
	return "file://" + d
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture makes a file module proxy that serves each release with its time, a module that requires them
// all in go.mod and in tools/tool.mod as the caller's two files say, and works in that module with an
// environment that reaches the proxy alone: no network, no sum database, a module cache of its own.
func fixture(t *testing.T, goMod, toolMod []release) {
	t.Helper()
	proxy := t.TempDir()
	for _, r := range append(append([]release{}, goMod...), toolMod...) {
		base := filepath.Join(proxy, r.path, "@v", r.version)
		write(t, base+".info", fmt.Sprintf(`{"Version":%q,"Time":%q}`, r.version, now.Add(-r.age).Format(time.RFC3339)))
		write(t, base+".mod", "module "+r.path+"\n")
	}
	dir := t.TempDir()
	file := func(releases []release) string {
		s := "module example.test/app\n\ngo 1.24\n\n"
		for _, r := range releases {
			s += fmt.Sprintf("require %s %s\n", r.path, r.version)
		}
		return s
	}
	write(t, filepath.Join(dir, "go.mod"), file(goMod))
	write(t, filepath.Join(dir, "tools", "tool.mod"), file(toolMod))
	t.Setenv("GOPROXY", proxyURL(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=mod -modcacherw")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "off")
	t.Chdir(dir)
}

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, now, &out, &errb)
	return code, out.String(), errb.String()
}

const day = 24 * time.Hour

// A module version younger than the window is red and its line names the module, its release time and the
// day it is allowed from; the same with an exception row is green and prints the exception and its reason.
//
// What it costs when this fails: a build takes a dependency released days ago, or an exception the
// caller wrote is not seen and the build is red for good.
func TestAYoungModuleIsRefusedAndAnExceptionAllowsIt(t *testing.T) {
	fixture(t, []release{{"example.com/young", "v1.2.0", 2 * day}, {"example.com/old", "v1.0.0", 30 * day}}, nil)
	code, out, errb := run("--days", "7", "--modfile", "go.mod")
	for _, want := range []string{
		"example.com/young@v1.2.0 is 2 days old, under the 7 days: released 2026-11-19T12:00:00Z, allowed from 2026-11-26",
		"1 of 2 modules refused",
	} {
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("a young module: exit %d, want 1 and %q\nstdout: %s\nstderr: %s", code, want, out, errb)
		}
	}
	if strings.Contains(out, "example.com/old") {
		t.Errorf("an old module is named:\n%s", out)
	}
	write(t, "exceptions.txt", "# the one the audit needs\nexample.com/young@v1.2.0 a fix we need before the window ends\n")
	code, out, errb = run("--days", "7", "--modfile", "go.mod", "--exceptions", "exceptions.txt")
	if code != 0 || !strings.Contains(out, "example.com/young@v1.2.0 excepted: a fix we need before the window ends") || !strings.Contains(out, "2 modules checked, none refused") {
		t.Errorf("an excepted young module: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// An exception that matches no refused module is printed and does not fail the run.
//
// What it costs when this fails: a stale exception is never seen, or it turns a green run red.
func TestAnExceptionThatMatchesNothingIsPrintedAndDoesNotFail(t *testing.T) {
	fixture(t, []release{{"example.com/old", "v1.0.0", 30 * day}}, nil)
	write(t, "exceptions.txt", "example.com/gone@v9.9.9 a module this repo no longer holds\nexample.com/old@v1.0.0 old enough already\n")
	code, out, errb := run("--days", "7", "--modfile", "go.mod", "--exceptions", "exceptions.txt")
	for _, want := range []string{"exception example.com/gone@v9.9.9 matches no module that is refused", "exception example.com/old@v1.0.0 matches no module that is refused"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d, want 0 and %q\nstdout: %s\nstderr: %s", code, want, out, errb)
		}
	}
}

// A pseudo-version is refused, whatever time it says, unless it is excepted: its time is its commit's own.
//
// What it costs when this fails: a pin to a commit with a time set back by its author passes the age check.
func TestAPseudoVersionIsRefusedUnlessExcepted(t *testing.T) {
	pseudo := "v0.0.0-20260101000000-abcdef123456"
	fixture(t, []release{{"example.com/commit", pseudo, 100 * day}}, nil)
	code, out, errb := run("--days", "7", "--modfile", "go.mod")
	if code != 1 || !strings.Contains(out, "example.com/commit@"+pseudo+" is a pseudo-version") {
		t.Errorf("a pseudo-version: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
	write(t, "exceptions.txt", "example.com/commit@"+pseudo+" pinned to the commit that holds the fix\n")
	if code, out, errb = run("--days", "7", "--modfile", "go.mod", "--exceptions", "exceptions.txt"); code != 0 || !strings.Contains(out, "excepted: pinned to the commit") {
		t.Errorf("an excepted pseudo-version: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// Every form of pseudo-version is refused, not only the one git makes before any tag: the two it makes once a
// tag exists put a dot, not a dash, before the commit time, and a v2 path with no go.mod adds +incompatible.
//
// What it costs when this fails: a pin to a commit after a tag, with a time set back by its author, passes the
// age check.
func TestEveryFormOfPseudoVersionIsRefused(t *testing.T) {
	for _, pseudo := range []string{
		"v1.2.4-0.20200101000000-abcdef123456",
		"v1.2.3-pre.0.20200101000000-abcdef123456",
		"v2.0.1-0.20200101000000-abcdef123456+incompatible",
	} {
		fixture(t, []release{{"example.com/commit", pseudo, 100 * day}}, nil)
		code, out, errb := run("--days", "7", "--modfile", "go.mod")
		if code != 1 || !strings.Contains(out, "example.com/commit@"+pseudo+" is a pseudo-version") {
			t.Errorf("%s: exit %d, want 1 and a pseudo-version refusal\nstdout: %s\nstderr: %s", pseudo, code, out, errb)
		}
	}
}

// A module replaced by a folder has no release time: it is refused by name, and the exception is its require
// line's module@version.
//
// What it costs when this fails: a replace to a local folder is a silent pass, or its refusal names a key the
// exception cannot be written for.
func TestAModuleReplacedByAFolderHasNoTimeAndIsRefusedByItsRequireLine(t *testing.T) {
	fixture(t, []release{{"example.com/dep", "v1.0.0", 30 * day}}, nil)
	write(t, "local/go.mod", "module example.com/dep\n")
	write(t, "go.mod", "module example.test/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ./local\n")
	code, out, errb := run("--days", "7", "--modfile", "go.mod")
	if code != 1 || !strings.Contains(out, "example.com/dep@v1.0.0 is replaced by a folder, which has no release time") {
		t.Errorf("a folder replace: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
	write(t, "exceptions.txt", "example.com/dep@v1.0.0 developed beside this repo\n")
	if code, out, errb = run("--days", "7", "--modfile", "go.mod", "--exceptions", "exceptions.txt"); code != 0 || !strings.Contains(out, "excepted: developed beside this repo") {
		t.Errorf("an excepted folder replace: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// Each modfile is read, the tools files too, and a refusal names the file it came from; the window is the
// caller's: ten days old is old enough for 7 and not for 14.
//
// What it costs when this fails: a tool pinned in tools/*.mod is never aged, or the window is a number of
// hatch's own.
func TestEachModfileIsReadAndTheWindowIsTheCallers(t *testing.T) {
	fixture(t, []release{{"example.com/lib", "v1.0.0", 30 * day}}, []release{{"example.com/tool", "v1.5.0", 10 * day}})
	code, out, errb := run("--days", "7", "--modfile", "go.mod", "--modfile", filepath.Join("tools", "tool.mod"))
	if code != 0 {
		t.Errorf("window 7: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
	code, out, errb = run("--days", "14", "--modfile", "go.mod", "--modfile", filepath.Join("tools", "tool.mod"))
	if code != 1 || !strings.Contains(out, filepath.Join("tools", "tool.mod")+": example.com/tool@v1.5.0 is 10 days old, under the 14 days") || strings.Contains(out, "go.mod: example.com/lib") {
		t.Errorf("window 14: exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// A go list that cannot read the modules (a cold cache with no proxy to reach) is exit 2 with its reason on
// stderr, never green.
//
// What it costs when this fails: an offline run with a cold cache checks nothing and passes.
func TestAGoListThatFailsIsNeverGreen(t *testing.T) {
	fixture(t, []release{{"example.com/old", "v1.0.0", 30 * day}}, nil)
	t.Setenv("GOPROXY", proxyURL(filepath.Join(t.TempDir(), "nowhere")))
	code, out, errb := run("--days", "7", "--modfile", "go.mod")
	if code != 2 || strings.Contains(out, "none refused") || !strings.Contains(errb, "go list exited") {
		t.Errorf("exit %d, want 2 and go list's reason on stderr\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// The flags have no default: a missing or zero window, no modfile, or an exceptions line with no reason is a
// usage error, exit 2.
//
// What it costs when this fails: hatch holds a window of its own, or an exception without its reason passes.
func TestTheFlagsHaveNoDefaultAndAnExceptionNeedsItsReason(t *testing.T) {
	fixture(t, []release{{"example.com/old", "v1.0.0", 30 * day}}, nil)
	write(t, "bare.txt", "example.com/old@v1.0.0\n")
	write(t, "twice.txt", "example.com/old@v1.0.0 one\nexample.com/old@v1.0.0 two\n")
	write(t, "nokey.txt", "example.com/old a reason\n")
	for _, args := range [][]string{
		{},
		{"--modfile", "go.mod"},
		{"--days", "7"},
		{"--days", "0", "--modfile", "go.mod"},
		{"--days", "7", "--modfile", "go.mod", "--exceptions", "bare.txt"},
		{"--days", "7", "--modfile", "go.mod", "--exceptions", "twice.txt"},
		{"--days", "7", "--modfile", "go.mod", "--exceptions", "nokey.txt"},
		{"--days", "7", "--modfile", "go.mod", "--exceptions", "missing.txt"},
		{"--days", "7", "--modfile", "go.mod", "extra"},
	} {
		if code, out, errb := run(args...); code != 2 || out != "" || errb == "" {
			t.Errorf("%v: exit %d, want 2, nothing on stdout, a reason on stderr\nstdout: %s\nstderr: %s", args, code, out, errb)
		}
	}
}

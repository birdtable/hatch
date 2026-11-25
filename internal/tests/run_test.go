package tests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// module makes a one-test module in a fresh folder and works in it.
func module(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/m\n\ngo 1.24\n")
	write("m_test.go", "package m\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nvar _ = time.Second\n\n"+body)
	t.Chdir(dir)
}

// houseFlags are the policy flags the tests run with: the rule and words the first program that used
// hatch gave. The second-consumer test runs with others.
var houseFlags = []string{"--red-when", "failed,none-ran,all-skipped", "--green-word", "green", "--red-word", "red"}

// run is runRaw with the house policy flags in front of args.
func run(args ...string) (int, string, string) {
	return runRaw(append(slices.Clone(houseFlags), args...)...)
}

func runRaw(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRunRefusesNoTimeout(t *testing.T) {
	for _, args := range [][]string{{}, {"./..."}, {"--timeout", "0s"}} {
		code, out, errs := run(args...)
		if code != 2 || out != "" || !strings.Contains(errs, "--timeout is required") {
			t.Fatalf("%v: code %d, out %q, err %q", args, code, out, errs)
		}
	}
}

func TestRunGreenDropsTheStream(t *testing.T) {
	module(t, "func TestA(t *testing.T) {}\n")
	code, out, _ := run("--timeout", "2m", "./...")
	if code != 0 || strings.TrimSpace(out) != "hatch tests: 1 passed, 0 failed, 0 skipped: green" {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestRunRedPrintsTheStream(t *testing.T) {
	module(t, "func TestA(t *testing.T) { t.Fatal(\"boom\") }\n")
	code, out, _ := run("--timeout", "2m", "./...")
	if code != 1 || !strings.Contains(out, `"Action":"fail"`) || !strings.Contains(out, "red, tests failed") {
		t.Fatalf("code %d, out %q", code, out)
	}
}

// The deadline covers the whole go test run, build included, so the run is red with the same words whether
// the fixture's build or its sleeping test is what the 2 s ran out on. Nothing here assumes the sleep began.
func TestRunTimeoutIsRed(t *testing.T) {
	module(t, "func TestA(t *testing.T) { time.Sleep(time.Minute) }\n")
	code, out, _ := run("--timeout", "2s", "-timeout", "5m", "./...")
	if code != 1 || !strings.Contains(out, "red, timed out after 2s") {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestRunOutputCapIsRed(t *testing.T) {
	old := maxOutput
	maxOutput = 1024
	t.Cleanup(func() { maxOutput = old })
	module(t, "func TestA(t *testing.T) { t.Log(string(make([]byte, 0))); for i := 0; i < 200; i++ { println(\"0123456789012345678901234567890123456789\") } }\n")
	code, out, errs := run("--timeout", "2m", "-v", "./...")
	if code != 1 || !strings.Contains(out, "red, ") || !strings.Contains(out, "output cap") {
		t.Fatalf("code %d, out %q, err %q", code, out, errs)
	}
}

// The same go test output is green or red by the flags alone, and each word is the caller's.
//
// What it costs when this fails: a program is held to a rule it did not choose, or says a word it did not give.
func TestRunTheVerdictComesFromTheFlagsAlone(t *testing.T) {
	const skipped = "func TestA(t *testing.T) { t.Skip(\"later\") }\n"
	for _, c := range []struct {
		name  string
		body  string
		flags []string
		code  int
		want  string
	}{
		{"every skip is red", skipped, []string{"--red-when", "all-skipped", "--green-word", "ok", "--red-word", "bad"}, 1, "bad, every test skipped"},
		{"the same run, rule without it", skipped, []string{"--red-when", "failed,none-ran", "--green-word", "ok", "--red-word", "bad"}, 0, "0 passed, 0 failed, 1 skipped: ok"},
		{"the same words with =", skipped, []string{"--red-when=all-skipped", "--green-word=fine", "--red-word=broke"}, 1, "broke, every test skipped"},
		{"a failed test", "func TestA(t *testing.T) { t.Fatal(\"boom\") }\n", []string{"--red-when", "failed", "--green-word", "ok", "--red-word", "bad"}, 1, "bad, tests failed"},
		{"no test ran", "", []string{"--red-when", "none-ran", "--green-word", "ok", "--red-word", "bad"}, 1, "bad, no tests ran"},
	} {
		module(t, c.body)
		code, out, errs := runRaw(append(c.flags, "--timeout", "2m", "./...")...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s: code %d, out %q, err %q, want %q", c.name, code, out, errs, c.want)
		}
		// The verdict line is the last line: the stream before it, on a red, is go test's own.
		last := out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:]
		if strings.Contains(last, "green") || strings.Contains(last, "red") {
			t.Errorf("%s: a house word is in the verdict line %q", c.name, last)
		}
	}
}

// A rule, a word or a flag left out is refused with exit 2, runs nothing, and says which.
//
// What it costs when this fails: a default stands in for what the caller did not say.
func TestRunRefusesAPolicyFlagLeftOutOrBad(t *testing.T) {
	module(t, "func TestA(t *testing.T) {}\n") // a run that is wrongly let through runs this, not the repo
	for _, c := range []struct {
		name, want string
		args       []string
	}{
		{"no rule", "--red-when is required", []string{"--green-word", "g", "--red-word", "r"}},
		{"a blank rule", "--red-when", []string{"--red-when", "", "--green-word", "g", "--red-word", "r"}},
		{"an unknown name", "\"never\" is not one of", []string{"--red-when", "never", "--green-word", "g", "--red-word", "r"}},
		{"a repeated name", "named twice", []string{"--red-when", "failed,failed", "--green-word", "g", "--red-word", "r"}},
		{"no green word", "--green-word is required", []string{"--red-when", "failed", "--red-word", "r"}},
		{"a blank red word", "--red-word is required", []string{"--red-when", "failed", "--green-word", "g", "--red-word", " "}},
		{"one word for both", "must differ", []string{"--red-when", "failed", "--green-word", "x", "--red-word", "x"}},
		{"a flag with no value", "--red-word needs a value", []string{"--red-when", "failed", "--green-word", "g", "--red-word"}},
	} {
		code, out, errs := runRaw(append([]string{"--timeout", "1m"}, c.args...)...)
		if code != 2 || out != "" || !strings.Contains(errs, c.want) {
			t.Errorf("%s: code %d, out %q, err %q, want %q", c.name, code, out, errs, c.want)
		}
	}
}

// A run with one passing and one skipped test is green under failed and red under failed,any-skipped, and the
// red line names the skipped test.
//
// What it costs when this fails: a caller that makes any skip a failure (go-run's tier) is switched to hatch and its
// guard goes quiet, or a red line says a skip happened without saying which test.
func TestRunAnySkippedNamesTheSkippedTest(t *testing.T) {
	module(t, "func TestA(t *testing.T) {}\nfunc TestB(t *testing.T) { t.Skip(\"later\") }\n")
	flags := func(rule string) []string {
		return []string{"--red-when", rule, "--green-word", "ok", "--red-word", "bad", "--timeout", "2m", "./..."}
	}
	if code, out, errs := runRaw(flags("failed")...); code != 0 || !strings.Contains(out, "1 passed, 0 failed, 1 skipped: ok") {
		t.Errorf("failed alone: code %d, out %q, err %q", code, out, errs)
	}
	code, out, errs := runRaw(flags("failed,any-skipped")...)
	verdict := out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:]
	if code != 1 || !strings.Contains(verdict, "bad, 1 skipped: ") || !strings.Contains(verdict, ".TestB") || strings.Contains(verdict, "TestA") {
		t.Errorf("failed,any-skipped: code %d, out %q, err %q", code, out, errs)
	}
}

// A green run with skipped tests names each one above the verdict line: its package, its name and the reason its
// t.Skip gave, a skip that gave none as that; the verdict line stays the last line and the exit code 0. A run
// with no skips prints no skips line (TestRunGreenDropsTheStream).
//
// What it costs when this fails: a tier line that passes with skipped tests cannot say which, so a plant that
// goes quiet shows nowhere.
func TestRunGreenNamesTheSkippedTests(t *testing.T) {
	module(t, "func TestA(t *testing.T) {}\n"+
		"func TestB(t *testing.T) { t.Skip(\"later, not now\") }\n"+
		"func TestC(t *testing.T) { t.Skip() }\n"+
		"func TestD(t *testing.T) { t.Run(\"x\", func(t *testing.T) { t.Skipf(\"no %s\", \"net\") }) }\n")
	code, out, errs := run("--timeout", "2m", "./...")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{
		"hatch tests: skipped example.test/m.TestB: later, not now",
		"hatch tests: skipped example.test/m.TestC: no reason given",
		"hatch tests: skipped example.test/m.TestD/x: no net",
		"hatch tests: 1 passed, 0 failed, 3 skipped: green",
	}
	if code != 0 || !slices.Equal(lines, want) {
		t.Errorf("code %d, out %q, err %q, want the lines %q", code, out, errs, want)
	}
}

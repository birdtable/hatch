// Package tests runs go test and judges the run. The caller says which counts
// make a run red and which two words the verdict line ends on; there is no
// default for either.
//
// Built on: the standard library, go test -json,
// github.com/birdtable/go-proc/proc, which runs go, and
// gotest.tools/gotestsum/testjson, which reads the events.
// Adds: the leaf-test counts over testjson's record of the run, the verdict line,
// a run that is killed at its timeout, tree and all, and the package list without
// the caller's excludes.
// Configured by: the caller, with --timeout, --red-when, --green-word and
// --red-word.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/birdtable/go-proc/proc"
	"gotest.tools/gotestsum/testjson"
)

// ExitNoGo is the exit code of a run that found no go on PATH. It is its own:
// 1 is a red run and 2 is a parse or usage error.
const ExitNoGo = 3

// Summary counts the leaf tests of a run. A parent test is not counted when it
// passes or fails: a parent whose subtests all skip would otherwise read as a
// pass. A parent that skips itself, after its subtests ran, counts as a skip.
type Summary struct {
	Pass, Fail, Skip int
	// Skipped names the skipped leaf tests, sorted, each as its package and its test name.
	Skipped []string
	// Reasons is, for each name in Skipped, the reason its t.Skip gave: the text of the first line of
	// its output that is not go test's own, without the file and line the testing package put in
	// front. A skip that gave none has "".
	Reasons map[string]string
}

// event is what the reader before testjson took from a line. A line testjson refuses is read
// as one, so that the lines refused are the ones that were refused before.
type event struct {
	Action  string
	Package string
	Test    string
}

// reader is the handler of a scan: testjson calls it for each event and for each line it
// could not read as one.
type reader struct {
	// out is where a package's line goes as its event arrives, under --stream; nil without it.
	out io.Writer
	// bad is the first line that opens a JSON object and is not a test event.
	bad error
}

// Event prints a line for a package's pass, fail or skip event: the action, a space and the
// import path. An event of a test, or of another action, prints nothing. A line that cannot
// be written is lost, not the scan: Event never stops it.
func (r *reader) Event(e testjson.TestEvent, _ *testjson.Execution) error {
	if r.out != nil && e.PackageEvent() && e.Package != "" && e.Action.IsTerminal() {
		_, _ = fmt.Fprintf(r.out, "%s %s\n", e.Action, e.Package)
	}
	return nil
}

// Err is given each line testjson did not read as an event. A line that opens no JSON object
// is not of the stream and is passed over; one that does, and is not an event, is kept as
// the scan's error.
func (r *reader) Err(text string) error {
	if line := strings.TrimSpace(text); r.bad == nil && strings.HasPrefix(line, "{") {
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			r.bad = fmt.Errorf("bad test event: %w", err)
		}
	}
	return nil
}

// Parse reads a `go test -json` stream and counts leaf tests by outcome.
func Parse(r io.Reader) (Summary, error) {
	return scan(r, nil)
}

// scan reads a stream through testjson.ScanTestOutput and counts its leaf tests. With out
// set, a line for each package is printed there as its event is read.
func scan(r io.Reader, out io.Writer) (Summary, error) {
	h := &reader{out: out}
	ex, err := testjson.ScanTestOutput(testjson.ScanConfig{Stdout: r, Handler: h, IgnoreNonJSONOutputLines: true})
	switch {
	case err != nil:
		return Summary{}, err
	case h.bad != nil:
		return Summary{}, h.bad
	}
	return leaves(ex), nil
}

// leaves counts hatch's way over testjson's record. testjson counts every test case: a parent
// beside its subtests, a test once for each time it ran, and a test that never ended as a
// failure it files itself. hatch counts a test name once, by its last outcome, only when go
// test reported one, and a parent only when it skipped itself.
func leaves(ex *testjson.Execution) Summary {
	var s Summary
	for _, name := range ex.Packages() {
		pkg := ex.Package(name)
		type end struct {
			id     int
			action testjson.Action
		}
		// last is each test name's outcome. A case's ID rises through its package as the
		// events arrive, so the highest ID of a name is its last outcome.
		last := map[string]end{}
		note := func(cases []testjson.TestCase, action testjson.Action) {
			for _, c := range cases {
				// A negative Elapsed is testjson's mark on a test with no end event.
				if n := c.Test.Name(); c.Elapsed >= 0 && c.ID > last[n].id {
					last[n] = end{c.ID, action}
				}
			}
		}
		note(pkg.Passed, testjson.ActionPass)
		note(pkg.Failed, testjson.ActionFail)
		note(pkg.Skipped, testjson.ActionSkip)
		// A test is a parent when any other test of its package is named under it.
		// Mark parents by ancestry, not by neighbours in a sort: a sibling such as
		// TestA#01 or TestA-1 sorts between TestA and TestA/x.
		parents := map[string]bool{}
		for n := range last {
			for i := range len(n) {
				if n[i] == '/' {
					parents[n[:i]] = true
				}
			}
		}
		for n, e := range last {
			// A parent counts only when it skipped itself: a parent that passes or
			// fails stands for its subtests, but a skip is its own event.
			switch {
			case parents[n] && e.action != testjson.ActionSkip:
			case e.action == testjson.ActionPass:
				s.Pass++
			case e.action == testjson.ActionFail:
				s.Fail++
			case e.action == testjson.ActionSkip:
				s.Skip++
				s.Skipped = append(s.Skipped, name+"."+n)
				if s.Reasons == nil {
					s.Reasons = map[string]string{}
				}
				s.Reasons[name+"."+n] = skipReason(pkg, e.id)
			}
		}
	}
	slices.Sort(s.Skipped)
	return s
}

// skipRE is the file and line the testing package puts in front of what a test logs: "m_test.go:12: ".
var skipRE = regexp.MustCompile(`^\S+\.go:\d+: ?`)

// skipReason is the reason a skipped test gave, from the output testjson kept for it: its first line that
// is not go test's own ("=== RUN", "--- SKIP"), without the file and line before it.
func skipReason(pkg *testjson.Package, id int) string {
	var out strings.Builder
	_ = pkg.WriteOutputTo(&out, id)
	for line := range strings.SplitSeq(out.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "=== ") && !strings.HasPrefix(line, "--- ") {
			return skipRE.ReplaceAllString(line, "")
		}
	}
	return ""
}

// A Rule names the counts that make a run red. A count the rule does not name
// never does: a rule without Failed lets a failing run read green.
type Rule struct {
	// Failed: go test exited non-zero, or a test failed.
	Failed bool
	// NoneRan: no test passed or skipped.
	NoneRan bool
	// AllSkipped: tests ran and every one skipped.
	AllSkipped bool
	// AnySkipped: one or more leaf tests skipped.
	AnySkipped bool
}

// ruleNames are the names --red-when takes, in the order Judge asks them.
var ruleNames = []string{"failed", "none-ran", "all-skipped", "any-skipped"}

// ParseRule reads a --red-when value: names from failed, none-ran,
// all-skipped and any-skipped, joined with commas. A blank value, an unknown or a repeated
// name is refused.
func ParseRule(v string) (Rule, error) {
	var r Rule
	seen := map[string]bool{}
	for name := range strings.SplitSeq(v, ",") {
		name = strings.TrimSpace(name)
		switch {
		case !slices.Contains(ruleNames, name):
			return Rule{}, fmt.Errorf("--red-when: %q is not one of %s", name, strings.Join(ruleNames, ", "))
		case seen[name]:
			return Rule{}, fmt.Errorf("--red-when: %q is named twice", name)
		}
		seen[name] = true
		r = Rule{Failed: seen["failed"], NoneRan: seen["none-ran"], AllSkipped: seen["all-skipped"], AnySkipped: seen["any-skipped"]}
	}
	return r, nil
}

// Words are the two words a verdict line ends on: Green for a run the rule
// passes, Red for one it does not.
type Words struct {
	Green, Red string
}

// check refuses a blank word, or the same word for both.
func (w Words) check() error {
	switch {
	case strings.TrimSpace(w.Green) == "":
		return errors.New("--green-word is required, and not blank")
	case strings.TrimSpace(w.Red) == "":
		return errors.New("--red-word is required, and not blank")
	case w.Green == w.Red:
		return errors.New("--green-word and --red-word must differ")
	}
	return nil
}

// Judge returns the verdict line and whether the run is green, by the caller's
// rule and words. testsFailed is whether go test itself exited non-zero.
func Judge(s Summary, testsFailed bool, r Rule, w Words) (string, bool) {
	line := fmt.Sprintf("hatch tests: %d passed, %d failed, %d skipped", s.Pass, s.Fail, s.Skip)
	switch {
	case r.Failed && (testsFailed || s.Fail > 0):
		return line + ": " + w.Red + ", tests failed", false
	case r.NoneRan && s.Pass+s.Skip == 0:
		return line + ": " + w.Red + ", no tests ran", false
	case r.AnySkipped && s.Skip > 0:
		return line + ": " + w.Red + ", " + skippedWords(s), false
	case r.AllSkipped && s.Pass == 0 && s.Skip > 0:
		return line + ": " + w.Red + ", every test skipped", false
	}
	return line + ": " + w.Green, true
}

// maxNamed is how many skipped tests a red line names before it counts the rest.
const maxNamed = 10

// skippedWords says how many tests skipped and names the first maxNamed.
func skippedWords(s Summary) string {
	names := s.Skipped
	more := 0
	if len(names) > maxNamed {
		names, more = names[:maxNamed], len(names)-maxNamed
	}
	out := fmt.Sprintf("%d skipped: %s", s.Skip, strings.Join(names, ", "))
	if more > 0 {
		out += fmt.Sprintf(", and %d more", more)
	}
	return out
}

const usage = `usage: hatch tests --timeout <duration> --red-when <rule> --green-word <word> --red-word <word> [--stream] [go test arguments]
  --timeout     how long the whole go test run may take, such as 10m. It is
                required: a run with none is refused, never left without a deadline.
                A run that reaches it is killed, tree and all, and is red.
  --red-when    which counts make the run red: names from failed (go test exited
                non-zero or a test failed), none-ran, all-skipped and any-skipped (one or
                more tests skipped, named in the line), joined with
                commas. Required: a name left out never makes a run red.
  --green-word  the word a green verdict line ends on. Required.
  --red-word    the word a red verdict line carries. Required, and not the green one.
  --stream      opt-in: print one line for each package as its pass, fail or skip event
                arrives, such as "pass example.com/p", before the verdict line. The verdict, the
                counts and the output of a red run are as without it.

  A green run that skipped tests prints a line for each above the verdict line, which
  stays the last: "hatch tests: skipped <package>.<Test>: <reason>" (the reason its t.Skip
  gave, or "no reason given"). With no skips it prints none. Exit codes and --red-when are as before.

usage: hatch tests packages [exclude...]
  Prints the import paths of go list ./..., one a line, without the paths
  named: an exact import path, or one ending in /..., which drops that path and
  everything under it. No path is dropped by default. Exit 1 when none is left.
`

// maxOutput is how much of each stream a run keeps. A run that writes more is
// ended and is red: its verdict would rest on a stream cut short.
var maxOutput = 1 << 29

// drain is how long a run that is ended reads the output a process still holds, as go-proc v0.5.0 takes it
// (as given, and refused at zero on Windows). It is the 10 s go-proc v0.4.0 defaulted to.
const drain = 10 * time.Second

// graceFor is how long a run that is ended has between SIGTERM and SIGKILL: 10 s on Unix, and none on
// Windows, which sends no TERM and refuses a Grace above zero.
func graceFor(goos string) time.Duration {
	if goos == "windows" {
		return 0
	}
	return 10 * time.Second
}

// options are hatch's own flags, as the caller gave them.
type options struct {
	timeout time.Duration
	redWhen string
	words   Words
	hasRule bool
	stream  bool
}

// split reads hatch's own flags (--timeout, --red-when, --green-word and
// --red-word) from the front of args. Only the double-dash spelling is
// hatch's: -timeout is go test's own and goes through to it. It returns the
// flags given and the go test arguments.
func split(args []string) (options, []string, error) {
	var o options
	for len(args) > 0 {
		name, v, found := strings.Cut(args[0], "=")
		if name == "--" {
			return o, args[1:], nil
		}
		switch name {
		case "--stream":
			if found {
				return options{}, nil, errors.New("--stream takes no value")
			}
			o.stream, args = true, args[1:]
			continue
		case "--timeout", "--red-when", "--green-word", "--red-word":
		default:
			return o, args, nil
		}
		if found {
			args = args[1:]
		} else {
			if len(args) < 2 {
				return options{}, nil, fmt.Errorf("%s needs a value", name)
			}
			v, args = args[1], args[2:]
		}
		switch name {
		case "--timeout":
			d, err := time.ParseDuration(v)
			if err != nil {
				return options{}, nil, fmt.Errorf("--timeout: %w", err)
			}
			o.timeout = d
		case "--red-when":
			o.redWhen, o.hasRule = v, true
		case "--green-word":
			o.words.Green = v
		case "--red-word":
			o.words.Red = v
		}
	}
	return o, args, nil
}

// Run executes `go test -json` with args under proc.Run and judges it, or, when the first argument is `packages`,
// lists the packages without the excludes that follow it (see Packages). The
// first arguments are hatch's own (--timeout, --red-when, --green-word and
// --red-word, all required); the rest go to go test. It
// returns the exit code: 0 green, 1 red, 2 refused or failed to run, ExitNoGo when go is not on PATH.
//
// proc.Run keeps the stream, so the stream is held whole until the run ends:
// it is printed on a red, and dropped on a green.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "packages" {
		return Packages(ctx, args[1:], stdout, stderr)
	}
	o, goArgs, err := split(args)
	var rule Rule
	switch {
	case err != nil:
	case o.timeout <= 0:
		err = errors.New("--timeout is required, and more than zero")
	case !o.hasRule:
		err = errors.New("--red-when is required: no default rule stands in for it")
	default:
		if rule, err = ParseRule(o.redWhen); err == nil {
			err = o.words.check()
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "hatch tests:", err)
		io.WriteString(stderr, usage)
		return 2
	}
	timeout, words := o.timeout, o.words
	goPath, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		fmt.Fprintln(stderr, "hatch tests: go not found on PATH")
		return ExitNoGo
	}
	if err == nil {
		goPath, err = filepath.Abs(goPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, "hatch tests:", err)
		return 2
	}
	cmd := proc.Cmd{
		Path:      goPath,
		Args:      append([]string{"test", "-json"}, goArgs...),
		Env:       os.Environ(),
		Timeout:   timeout,
		Grace:     graceFor(runtime.GOOS),
		Drain:     drain,
		MaxOutput: maxOutput,
	}
	// Under --stream go-proc gives the stream to a writer as it reads it, and a scan of
	// what is written prints a line for each package at its event. The stream is kept whole as ever.
	end := func() {}
	if o.stream {
		cmd.Stdout, end = live(stdout)
	}
	res, err := proc.Run(ctx, cmd)
	end()
	var ended proc.Ended
	switch {
	case errors.As(err, &ended):
		// The run was cut short: red, with what it wrote before.
		fmt.Fprint(stdout, string(res.Stdout))
		fmt.Fprintf(stderr, "%s", res.Stderr)
		fmt.Fprintf(stdout, "hatch tests: %s, %s\n", words.Red, endedWhy(ended, timeout))
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "hatch tests:", err)
		return 2
	}
	s, perr := Parse(bytes.NewReader(res.Stdout))
	if perr != nil {
		fmt.Fprintln(stderr, "hatch tests:", perr)
		return 2
	}
	line, ok := Judge(s, res.Code != 0, rule, words)
	if !ok {
		// Show the raw stream only when red, so a green run stays short.
		fmt.Fprint(stdout, string(res.Stdout))
	} else {
		// A green run names what it skipped, a line for each, above the verdict line, which stays the last.
		for _, name := range s.Skipped {
			fmt.Fprintf(stdout, "hatch tests: skipped %s: %s\n", name, skipWhy(s.Reasons[name]))
		}
	}
	// go test's own stderr (build errors, warnings) is shown either way.
	fmt.Fprintf(stderr, "%s", res.Stderr)
	fmt.Fprintln(stdout, line)
	if !ok {
		return 1
	}
	return 0
}

func endedWhy(e proc.Ended, timeout time.Duration) string {
	if e.Why == proc.WhyDeadline {
		return fmt.Sprintf("timed out after %s", timeout)
	}
	return e.Error()
}

// skipWhy is a skipped test's reason as its line prints it: the words it gave, or that it gave none.
func skipWhy(reason string) string {
	if reason == "" {
		return "no reason given"
	}
	return reason
}

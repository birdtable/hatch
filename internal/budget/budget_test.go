package budget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/go-proc/load"
)

// gateRow reads the layout of budgets: a list of ids, each with a limit key.
const gateRow = `^  - id: (?P<id>\S+)\n(?:    [a-z_]+: .*\n)*?    limit: (?P<limit>[0-9]+)\n`

var gate = regexp.MustCompile("(?m)" + gateRow)

const budgets = `budgets:
  - id: tier-pre-push
    limit: 180
  - id: tier-pre-commit
    limit: 20
  - id: tier-dup
    limit: 1
  - id: tier-dup
    limit: 2
  - id: fast
    limit: 5
`

// file writes the budgets file under a name no policy gives it.
func file(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "any-name.yaml")
	if err := os.WriteFile(p, []byte(budgets), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixed(l float64) LoadSource {
	return func(context.Context, int) (float64, int, error) { return l, 0, nil }
}

// onCPUsLoad is a load read on n CPUs.
func onCPUsLoad(l float64, n int) LoadSource {
	return func(context.Context, int) (float64, int, error) { return l, n, nil }
}

// unknown is a system with no load average, as HostLoad reports one: go-proc's refusal after ErrNoLoadAverage.
func unknown(context.Context, int) (float64, int, error) {
	return 0, 0, fmt.Errorf("%w: %w", ErrNoLoadAverage, load.ErrNoAverage)
}

// judge runs the command on the tier-pre-push row, which the test has run for
// took seconds, with the flags in extra after the three it always passes.
func judge(t *testing.T, took int, load LoadSource, extra ...string) (int, string, string) {
	t.Helper()
	now := time.Unix(1_000_000, 0)
	args := append([]string{"--budgets", file(t), "--row", gateRow, "--window", "1", "--noun", "the tier", "--excused", "not a floor", "--prefix", "tier-", "--tier", "pre-push", "--since", strconv.Itoa(int(now.Unix()) - took)}, extra...)
	hasLimit, hasWhen := false, false
	for _, e := range extra {
		hasLimit = hasLimit || e == "--limit"
		hasWhen = hasWhen || e == "--loaded-when"
	}
	if hasLimit && !hasWhen {
		args = append(args, "--loaded-when", "either")
	}
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, now, load, &out, &errb)
	return code, out.String(), errb.String()
}

func TestOverBudget(t *testing.T) {
	cases := []struct {
		name  string
		took  int
		extra []string
		end   LoadSource
		code  int
		say   string // in stdout
	}{
		{"loaded before", 769, []string{"--limit", "10", "--load-before", "100"}, fixed(2), 0, "over budget, not a floor: load 100 before the tier, above the limit of 10"},
		{"loaded at the end", 769, []string{"--limit", "10", "--load-before", "2"}, fixed(100), 0, "over budget, not a floor: load 100 at the end of the tier, above the limit of 10"},
		{"loaded at both", 769, []string{"--limit", "10", "--load-before", "50"}, fixed(100), 0, "load 50 before the tier and 100 at its end, above the limit of 10"},
		{"both quiet", 769, []string{"--limit", "10", "--load-before", "2"}, fixed(3), 1, ""},
		{"load at the limit is not above it", 769, []string{"--limit", "10", "--load-before", "10"}, fixed(10), 1, ""},
		{"no sample before, quiet end", 769, []string{"--limit", "10"}, fixed(3), 1, ""},
		{"unreadable, none before", 769, []string{"--limit", "10"}, unknown, 1, "load unknown: " + load.ErrNoAverage.Error()},
		{"unreadable end, loaded before", 769, []string{"--limit", "10", "--load-before", "100"}, unknown, 0, "load 100 before the tier"},
		{"within budget, loaded", 100, []string{"--limit", "10", "--load-before", "100"}, fixed(100), 0, ""},
		{"within budget, quiet", 100, []string{"--limit", "10", "--load-before", "2"}, fixed(3), 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errb := judge(t, c.took, c.end, c.extra...)
			if code != c.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.code, out, errb)
			}
			if !strings.Contains(out, c.say) {
				t.Fatalf("stdout lacks %q:\n%s", c.say, out)
			}
			if c.code == 1 && !strings.Contains(errb, "over its budget") {
				t.Fatalf("a red run says why on stderr: %q", errb)
			}
		})
	}
}

// The load threshold is the flag's value: the same load of 12 is forgiven
// under a limit of 10 and judged strictly under a limit of 15, where the old
// default was the CPU count.
func TestTheLimitFlagIsTheThreshold(t *testing.T) {
	for _, c := range []struct {
		limit string
		code  int
	}{{"10", 0}, {"11.9", 0}, {"12", 1}, {"15", 1}, {"0", 0}} {
		code, out, errb := judge(t, 769, fixed(12), "--limit", c.limit)
		if code != c.code {
			t.Fatalf("--limit %s: exit %d, want %d\nstdout: %s\nstderr: %s", c.limit, code, c.code, out, errb)
		}
	}
}

// With no --limit the check is off: the load excuses nothing, however high.
func TestNoLimitMeansTheLoadExcusesNothing(t *testing.T) {
	code, out, errb := judge(t, 769, fixed(1000), "--load-before", "1000")
	if code != 1 || strings.Contains(out, "not a floor") || !strings.Contains(errb, "over its budget") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// The prefix is the flag's value, and the file is the path the flag names.
func TestPrefixAndBudgetsAreTheFlags(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Run(context.Background(), append(args, "--since", strconv.Itoa(int(now.Unix())-1)), now, fixed(0), &out, &errb)
		return code, out.String() + errb.String()
	}
	p := file(t)
	if code, say := run("--budgets", p, "--row", gateRow, "--tier", "fast"); code != 0 || !strings.Contains(say, "budget: fast took 1 s of 5 s") {
		t.Fatalf("no prefix, the id is --tier: exit %d: %s", code, say)
	}
	if code, say := run("--budgets", p, "--row", gateRow, "--prefix", "tier-", "--tier", "fast"); code != 1 || !strings.Contains(say, "tier-fast") {
		t.Fatalf("a prefix names another row: exit %d: %s", code, say)
	}
	if code, say := run("--budgets", filepath.Join(t.TempDir(), "none.yaml"), "--row", gateRow, "--tier", "fast"); code != 1 || !strings.Contains(say, "none.yaml") {
		t.Fatalf("a file that is not there: exit %d: %s", code, say)
	}
}

// The advice is the flag's text, and with none set a failure adds none.
func TestAdviceIsTheFlag(t *testing.T) {
	_, _, errb := judge(t, 769, fixed(0), "--advice", "split the tier")
	if !strings.HasSuffix(errb, "is over its budget: split the tier\n") {
		t.Fatalf("stderr %q", errb)
	}
	_, _, errb = judge(t, 769, fixed(0))
	if !strings.HasSuffix(errb, "is over its budget\n") || strings.Contains(errb, "move the heaviest") {
		t.Fatalf("stderr %q", errb)
	}
}

// An unknown load never excuses an overrun.
func TestUnknownLoadIsStrict(t *testing.T) {
	code, out, _ := judge(t, 769, unknown, "--limit", "1")
	if code != 1 || !strings.Contains(out, "load unknown: "+load.ErrNoAverage.Error()) {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
}

// A negative took, which clock skew can produce, is an error that names the
// line, never a pass; a took of 0 is judged.
func TestNegativeTook(t *testing.T) {
	code, out, errb := judge(t, -1, fixed(100), "--limit", "10")
	if code != 1 || !strings.Contains(errb, "negative") || !strings.Contains(errb, "tier-pre-push") {
		t.Fatalf("took=-1: exit %d, stdout %q, stderr %q", code, out, errb)
	}
	if strings.Contains(out, "took") {
		t.Fatalf("a refused line is not judged: %q", out)
	}
	code, out, errb = judge(t, 0, fixed(3), "--limit", "10")
	if code != 0 || !strings.Contains(out, "tier-pre-push took 0 s of 180 s") {
		t.Fatalf("took=0: exit %d, stdout %q, stderr %q", code, out, errb)
	}
}

// A system with no load average (Windows) says so on stderr beside an overrun, with go-proc's
// own refusal, so nobody reads the overrun as load-free.
func TestOverrunWithNoLoadAverageSaysLoadUnknown(t *testing.T) {
	code, _, errb := judge(t, 769, unknown, "--limit", "10")
	if code != 1 || !strings.Contains(errb, "over its budget") || !strings.Contains(errb, "load unknown: "+load.ErrNoAverage.Error()) {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	// A readable load says nothing of the kind.
	_, _, errb = judge(t, 769, fixed(3), "--limit", "10")
	if strings.Contains(errb, "load unknown") {
		t.Fatalf("a known load is not unknown: %q", errb)
	}
}

func TestLimit(t *testing.T) {
	if n, ok := Limit([]byte(budgets), gate, "tier-pre-push"); !ok || n != 180 {
		t.Fatalf("got %d %v", n, ok)
	}
	if _, ok := Limit([]byte(budgets), gate, "tier-dup"); ok {
		t.Fatal("an id with two rows has no limit")
	}
	if _, ok := Limit([]byte(budgets), gate, "tier-none"); ok {
		t.Fatal("no row, no limit")
	}
}

func TestLoadOnly(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"--load", "--window", "1"}, time.Now(), fixed(1.5), &out, &errb); code != 0 || out.String() != "1.50\n" {
		t.Fatalf("exit %d, %q", code, out.String())
	}
	out.Reset()
	if code := Run(context.Background(), []string{"--load", "--window", "1"}, time.Now(), func(context.Context, int) (float64, int, error) { return 0, 0, errors.New("x") }, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

func TestUsage(t *testing.T) {
	p := file(t)
	for _, args := range [][]string{
		{},
		{"--tier", "x", "--since", "5"},
		{"--budgets", p, "--row", gateRow, "--since", "5"},
		{"--budgets", p, "--row", gateRow, "--tier", "x"},
		{"--budgets", p, "--row", gateRow, "--tier", "x", "--since", "5", "--window", "1", "--loaded-when", "end", "--limit", "-1"},
		{"--budgets", p, "--row", gateRow, "--tier", "x", "--since", "5", "--window", "1", "--loaded-when", "end", "--limit", "NaN"},
		{"--budgets", p, "--row", gateRow, "--tier", "x", "--since", "5", "--limit", "1", "--window", "1"},
		{"--budgets", p, "--row", gateRow, "--tier", "x", "--since", "5", "--limit", "1", "--loaded-when", "sometimes", "--window", "1"},
		{"--budgets", p, "--row", gateRow, "--tier", "x", "--since", "5", "--window", "7"},
		{"--budgets", p, "--row", gateRow, "--tier", "x", "--since", "5", "--load-before", "3"},
		{"--budgets", p, "--row", "no groups", "--tier", "x", "--since", "5"},
		{"--budgets", p, "--tier", "x", "--since", "5"},
		{"--load"},
		{"--bogus"},
		{"extra"},
	} {
		var out, errb bytes.Buffer
		if code := Run(context.Background(), args, time.Now(), fixed(0), &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if !strings.Contains(errb.String(), "unset") {
			t.Fatalf("%v: the usage says what an unset flag means: %q", args, errb.String())
		}
	}
}

// hatch holds no policy of a project: its usage names no path, tier or advice.
func TestUsageNamesNoProjectValue(t *testing.T) {
	for _, s := range []string{"gate/", "budgets.yaml", "tier-", "pre-push", "pre-commit", "release"} {
		if strings.Contains(usage, s) {
			t.Fatalf("usage holds %q", s)
		}
	}
}

// A budgets file written on Windows has CRLF line ends and reads the same.
func TestLimitReadsCRLF(t *testing.T) {
	crlf := []byte(strings.ReplaceAll(budgets, "\n", "\r\n"))
	for _, id := range []string{"tier-pre-push", "tier-pre-commit", "tier-dup", "tier-none"} {
		wantN, wantOK := Limit([]byte(budgets), gate, id)
		gotN, gotOK := Limit(crlf, gate, id)
		if gotN != wantN || gotOK != wantOK {
			t.Fatalf("%s: CRLF gives %d %v, LF gives %d %v", id, gotN, gotOK, wantN, wantOK)
		}
	}
	if n, ok := Limit(crlf, gate, "tier-pre-push"); !ok || n != 180 {
		t.Fatalf("got %d %v", n, ok)
	}
}

// A budgets file in another layout is read through the caller's --row, and a
// run with other words prints them: the second consumer of the command.
func TestAnotherLayoutAndOtherWords(t *testing.T) {
	p := filepath.Join(t.TempDir(), "limits.ini")
	if err := os.WriteFile(p, []byte("[limits]\r\nci.fast = 5\r\nci.slow = 90\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	run := func(took int, load LoadSource, extra ...string) (int, string, string) {
		args := append([]string{"--budgets", p, "--row", `^(?P<id>[a-z.]+) = (?P<limit>[0-9]+)\r?$`, "--prefix", "ci.", "--tier", "slow", "--since", strconv.Itoa(int(now.Unix()) - took)}, extra...)
		var out, errb bytes.Buffer
		code := Run(context.Background(), args, now, load, &out, &errb)
		return code, out.String(), errb.String()
	}
	code, out, _ := run(200, fixed(50), "--window", "5", "--limit", "8", "--loaded-when", "end", "--noun", "the build", "--excused", "load, not the code")
	if code != 0 || !strings.Contains(out, "ci.slow took 200 s of 90 s") ||
		!strings.Contains(out, "ci.slow is over budget, load, not the code: load 50 at the end of the build, above the limit of 8") {
		t.Fatalf("exit %d: %s", code, out)
	}
	// With --limit, neither word has a default: a missing or blank one is a usage error.
	for _, words := range [][]string{{}, {"--noun", "the build"}, {"--excused", "forgiven"}, {"--noun", " ", "--excused", "x"}, {"--noun", "x", "--excused", ""}} {
		code, out, errb := run(200, fixed(50), append([]string{"--window", "5", "--limit", "8", "--loaded-when", "end"}, words...)...)
		if code != 2 || out != "" || !strings.Contains(errb, "required with --limit") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", words, code, out, errb)
		}
	}
	// With no --limit the load lines name no span.
	_, out, _ = run(1, fixed(50), "--window", "5")
	if !strings.Contains(out, "load 50.0 at its end; none was sampled before\n") {
		t.Fatalf("%s", out)
	}
}

// --loaded-when names the sample that may excuse an overrun.
func TestLoadedWhenIsTheFlag(t *testing.T) {
	for _, c := range []struct {
		when string
		code int
	}{{"before", 0}, {"end", 1}, {"either", 0}, {"before-and-end", 1}} {
		code, out, errb := judge(t, 769, fixed(1), "--limit", "10", "--load-before", "100", "--loaded-when", c.when)
		if code != c.code {
			t.Fatalf("--loaded-when %s: exit %d\nstdout: %s\nstderr: %s", c.when, code, out, errb)
		}
	}
}

// --window names the load average that is read, and with none no load is read.
func TestWindowIsTheFlag(t *testing.T) {
	var got []int
	spy := func(_ context.Context, w int) (float64, int, error) { got = append(got, w); return 1, 0, nil }
	judge(t, 1, spy, "--window", "15")
	if len(got) != 1 || got[0] != 15 {
		t.Fatalf("windows read: %v", got)
	}
	got = nil
	now := time.Unix(1_000_000, 0)
	var out, errb bytes.Buffer
	args := []string{"--budgets", file(t), "--row", gateRow, "--tier", "fast", "--prefix", "", "--since", strconv.Itoa(int(now.Unix()) - 1)}
	if code := Run(context.Background(), args, now, spy, &out, &errb); code != 0 || len(got) != 0 || strings.Contains(out.String(), "load") {
		t.Fatalf("no --window: exit %d, read %v, stdout %q", code, got, out.String())
	}
}

// An error of load.Read, with go-proc's words after it, is the typed unknown
// and is printed as one, on every system.
func TestUnknownLoadKeepsTheReason(t *testing.T) {
	err := fmt.Errorf("%w: %w", ErrNoLoadAverage, errors.New("this system keeps no load average that this package reads"))
	if !errors.Is(err, ErrNoLoadAverage) {
		t.Fatal("not typed")
	}
	if got := unreadable(err); got != "load unknown: this system keeps no load average that this package reads" {
		t.Fatalf("got %q", got)
	}
	if got := unreadable(ErrNoLoadAverage); got != "load unknown" {
		t.Fatalf("got %q", got)
	}
}

// The CPU count the load was read on is a fact on the load line, in both of its forms, and is left
// out when the source gave none.
func TestTheLoadLineNamesTheCPUs(t *testing.T) {
	for _, c := range []struct {
		name   string
		load   LoadSource
		extra  []string
		suffix string
	}{
		{"before and end", onCPUsLoad(7, 16), []string{"--load-before", "3"}, "load 3.0 before the tier, 7.0 at its end, on 16 CPUs\n"},
		{"none before", onCPUsLoad(7, 16), nil, "load 7.0 at its end, on 16 CPUs; none was sampled before the tier\n"},
		{"one CPU", onCPUsLoad(7, 1), []string{"--load-before", "3"}, "7.0 at its end, on 1 CPU\n"},
		{"the source gave none", fixed(7), []string{"--load-before", "3"}, "7.0 at its end\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, out, _ := judge(t, 1, c.load, c.extra...)
			if !strings.Contains(out, c.suffix) {
				t.Fatalf("stdout lacks %q:\n%s", c.suffix, out)
			}
		})
	}
}

// --load prints the figure alone, for a script that reads it, whatever the CPUs.
func TestLoadOnlyPrintsNoCPUs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"--load", "--window", "1"}, time.Now(), onCPUsLoad(1.5, 8), &out, &errb); code != 0 || out.String() != "1.50\n" {
		t.Fatalf("exit %d, %q", code, out.String())
	}
}

// before-and-end excuses an overrun only when both samples are above the limit: either excuses it
// on the end sample alone when none was taken before, which this value never does.
func TestBeforeAndEndNeedsBothSamples(t *testing.T) {
	for _, c := range []struct {
		name   string
		when   string
		extra  []string
		end    float64
		code   int
		reason string
	}{
		{"both above", "before-and-end", []string{"--load-before", "50"}, 100, 0, "load 50 before the tier and 100 at its end, above the limit of 10"},
		{"only before above", "before-and-end", []string{"--load-before", "50"}, 2, 1, ""},
		{"only end above", "before-and-end", []string{"--load-before", "2"}, 100, 1, ""},
		{"no sample before, end above", "before-and-end", nil, 100, 1, ""},
		{"the same, under either", "either", nil, 100, 0, "load 100 at the end of the tier"},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"--limit", "10", "--loaded-when", c.when}, c.extra...)
			code, out, errb := judge(t, 769, fixed(c.end), args...)
			if code != c.code || !strings.Contains(out, c.reason) {
				t.Fatalf("exit %d, want %d; stdout lacks %q?\nstdout: %s\nstderr: %s", code, c.code, c.reason, out, errb)
			}
		})
	}
}

// before-and-end is a value --loaded-when takes, and a word the usage says.
func TestBeforeAndEndIsInTheUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"-h"}, time.Now(), fixed(1), &out, &errb); code != exitUsage || !strings.Contains(errb.String(), "before-and-end") {
		t.Fatalf("exit %d, usage: %q", code, errb.String())
	}
}

// The figures of a solo cold run: 6.8 sampled before the run, 20.2 at the tier's end, a limit of
// 15, the tier over its time. The end sample always holds the tier's own load, so end and either excuse it by its
// own work and pass it; before and before-and-end, which tell another job's load from the tier's own, refuse it.
//
// What it costs when this fails: a mode that claims to tell contention from own load excuses a tier that loaded
// the machine itself, or a mode that counts the tier's own load stops being a choice of the caller's.
func TestATierThatLoadedTheMachineItselfIsOnlyExcusedByTheModesThatCountItsOwnLoad(t *testing.T) {
	for _, c := range []struct {
		when string
		code int
		why  string
	}{
		{"before", 1, "the sample before the run (6.8) is below the limit, so another job's load is not shown"},
		{"before-and-end", 1, "the sample before the run (6.8) is below the limit, so another job's load is not shown"},
		{"end", 0, "end counts the tier's own load, which is what 20.2 is"},
		{"either", 0, "either counts the tier's own load at the end, which is what 20.2 is"},
	} {
		t.Run(c.when, func(t *testing.T) {
			code, out, errb := judge(t, 769, fixed(20.2), "--limit", "15", "--loaded-when", c.when, "--load-before", "6.8")
			if code != c.code {
				t.Fatalf("exit %d, want %d: %s\nstdout: %s\nstderr: %s", code, c.code, c.why, out, errb)
			}
		})
	}
}

// A machine loaded before the run, 30 against a limit of 15, excuses the overrun under before whatever the end
// sample is, and under before-and-end when the end sample is above the limit too; with the end below it,
// before-and-end refuses.
//
// What it costs when this fails: a tier that ran on a machine another job was loading fails its budget.
func TestAMachineLoadedBeforeTheRunExcusesTheOverrun(t *testing.T) {
	for _, c := range []struct {
		when string
		end  float64
		code int
	}{
		{"before", 20.2, 0},
		{"before", 4, 0},
		{"before-and-end", 20.2, 0},
		{"before-and-end", 4, 1},
	} {
		code, out, errb := judge(t, 769, fixed(c.end), "--limit", "15", "--loaded-when", c.when, "--load-before", "30")
		if code != c.code || (c.code == 0 && !strings.Contains(out, "load 30 before the tier")) {
			t.Errorf("%s, end %g: exit %d, want %d\nstdout: %s\nstderr: %s", c.when, c.end, code, c.code, out, errb)
		}
	}
}

// The usage says where the caller samples and what the end sample holds: --load-before is a sample from before
// the caller's first unit of work, a sample between two tiers holds the earlier tier's load, and end and either
// excuse a tier by its own work.
//
// What it costs when this fails: hatch's words tell a caller to sample at the tier's start again, and every
// mode excuses.
func TestTheUsageSaysWhereToSampleAndWhatTheEndSampleHolds(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"-h"}, time.Now(), fixed(1), &out, &errb); code != exitUsage {
		t.Fatalf("exit %d, usage: %q", code, errb.String())
	}
	flat := strings.Join(strings.Fields(errb.String()), " ")
	for _, s := range []string{
		"before its first unit of work on the machine",
		"A sample taken between two tiers of one run holds the earlier tier's load",
		"The end sample always holds the judged tier's own load",
		"end and either therefore excuse a tier by its own work",
	} {
		if !strings.Contains(flat, s) {
			t.Errorf("the usage lacks %q:\n%s", s, errb.String())
		}
	}
	if strings.Contains(flat, "just before the tier") {
		t.Errorf("the usage still says the sample is taken just before the tier:\n%s", errb.String())
	}
}

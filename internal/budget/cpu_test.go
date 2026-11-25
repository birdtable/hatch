package budget

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cpuRow reads the layout of budgets' CPU limits: the key cpu_limit of an id's entry, the wall limit
// beside it read by gateRow.
const cpuRow = `^  - id: (?P<id>\S+)\n(?:    [a-z_]+: .*\n)*?    cpu_limit: (?P<limit>[0-9]+)\n`

// cpuBudgets holds each tier's wall limit and, but for "nocpu", its CPU limit, in seconds.
const cpuBudgets = `budgets:
  - id: tier-burn-over
    limit: 1000
    cpu_limit: 0
  - id: tier-burn-under
    limit: 1000
    cpu_limit: 100
  - id: tier-sleep
    limit: 1000
    cpu_limit: 1
  - id: tier-orphan
    limit: 1000
    cpu_limit: 100
  - id: tier-nocpu
    limit: 1000
  - id: tier-wall
    limit: 5
    cpu_limit: 100
`

// TestHelperProcess is the command the rows run, this test binary started with -test.run: the program
// after the "--" is the one it plays.
func TestHelperProcess(t *testing.T) {
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "burn":
		x := 1.0
		for i := 0; i < 60_000_000; i++ {
			x = x*1.0000001 + 1e-9
		}
		if x < 0 {
			os.Exit(3)
		}
	case "sleep":
		time.Sleep(600 * time.Millisecond)
	case "hang":
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(7)
	case "env":
		// env <name> <value>: exit 5 unless the caller's variable reached this process as set.
		if os.Getenv(args[1]) != args[2] {
			os.Exit(5)
		}
	case "mark":
		os.WriteFile(args[1], []byte("ran"), 0o644)
	case "orphan":
		// A grandchild that this process does not wait for, and that outlives it.
		// The grandchild must stay in this process group and go unwaited for, which proc.Run does not do.
		p, err := os.StartProcess(os.Args[0], []string{os.Args[0], "-test.run=^TestHelperProcess$", "--", "sleep"}, &os.ProcAttr{}) //nolint:forbidigo // a row's own orphan, that no proc call makes
		if err != nil {
			os.Exit(4)
		}
		p.Release()
	}
}

// helper is the command of a tier: this test binary playing the program named.
func helper(program string, more ...string) []string {
	return append([]string{"--", os.Args[0], "-test.run=^TestHelperProcess$", "--", program}, more...)
}

func cpuFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "any-name.yaml")
	if err := os.WriteFile(p, []byte(cpuBudgets), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// judgeCPU runs the tier's command under --cpu, the wall time since the second given.
func judgeCPU(t *testing.T, tier string, took int, extra []string, command []string) (int, string, string) {
	t.Helper()
	now := time.Now()
	args := []string{"--cpu", "--cpu-row", cpuRow, "--budgets", cpuFile(t), "--row", gateRow, "--prefix", "tier-", "--tier", tier,
		"--since", strconv.Itoa(int(now.Unix()) - took)}
	args = append(append(args, extra...), command...)
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, now, fixed(50), &out, &errb)
	return code, out.String(), errb.String()
}

// loaded keeps every CPU busy until the returned function is called.
func loaded() (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2*runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x := 0
			for {
				select {
				case <-done:
					return
				default:
					for j := 0; j < 1_000_000; j++ {
						x += j
					}
				}
			}
		}()
	}
	return func() { close(done); wg.Wait() }
}

// A CPU-burning child has the same verdict on a quiet machine and on a loaded one: over a limit of 0
// and under a limit of 100, whatever the wall time was.
func TestACPUBurningChildHasTheSameVerdictQuietAndLoaded(t *testing.T) {
	for _, load := range []bool{false, true} {
		if load {
			defer loaded()()
		}
		code, out, errb := judgeCPU(t, "burn-over", 0, nil, helper("burn"))
		if code != exitFailed || !strings.Contains(errb, "tier-burn-over is over its CPU budget") || !strings.Contains(out, "of CPU of 0 s") {
			t.Fatalf("loaded=%v, limit 0: got %d, out %q, err %q", load, code, out, errb)
		}
		code, out, errb = judgeCPU(t, "burn-under", 0, nil, helper("burn"))
		if code != exitDone || !strings.Contains(out, "tier-burn-under used ") || errb != "" {
			t.Fatalf("loaded=%v, limit 100: got %d, out %q, err %q", load, code, out, errb)
		}
	}
}

// A sleeping child uses no CPU, however long it takes, and stays under its limit.
func TestASleepingChildStaysUnderItsCPULimit(t *testing.T) {
	code, out, errb := judgeCPU(t, "sleep", 0, nil, helper("sleep"))
	if code != exitDone || errb != "" {
		t.Fatalf("got %d, out %q, err %q", code, out, errb)
	}
	// No exact figure: a program's start costs some CPU, 0.1 s of the printed figure on one Windows run in
	// five. The child slept 600 ms, so a CPU time of half that or less says it did not run; a program that
	// burned for those 600 ms uses about all of them, and the row is red.
	m := regexp.MustCompile(`tier-sleep used ([0-9.]+) s of CPU of 1 s`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no CPU line in %q", out)
	}
	if used, err := strconv.ParseFloat(m[1], 64); err != nil || used > 0.3 {
		t.Fatalf("a child that slept 600 ms used %q s of CPU, over 0.3 s, in %q", m[1], out)
	}
}

// Where no job object can be made (Windows), or the sum cannot be read, the tier is a tree not read, by name.
func TestNoJobObjectIsATreeNotRead(t *testing.T) {
	old := newMeterFor
	defer func() { newMeterFor = old }()
	newMeterFor = func() (meter, error) { return nil, notRead("no job object to hold the tier: access denied") }
	code, out, errb := judgeCPU(t, "burn-under", 0, nil, helper("burn"))
	if code != exitFailed || !strings.Contains(errb, "tree not read: no job object to hold the tier") || strings.Contains(out, "used") {
		t.Fatalf("got %d, out %q, err %q", code, out, errb)
	}
	if !errors.Is(notRead("x"), ErrTreeNotRead) {
		t.Fatal("a tree not read is not ErrTreeNotRead")
	}
}

// A tier with no CPU row is refused by name before its command runs, and is not judged by wall time.
func TestATierWithNoCPURowIsRefusedByName(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "ran")
	code, out, errb := judgeCPU(t, "nocpu", 0, nil, helper("mark", mark))
	if code != exitFailed || !strings.Contains(errb, "no single CPU limit for tier-nocpu") || out != "" {
		t.Fatalf("got %d, out %q, err %q", code, out, errb)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the command ran")
	}
}

// A tier over its wall limit with its CPU under passes, and the output names the wall time and the load.
func TestOverItsWallLimitWithItsCPUUnderPassesAndSaysSo(t *testing.T) {
	code, out, errb := judgeCPU(t, "wall", 100, []string{"--window", "1", "--noun", "the tier"}, helper("mark", filepath.Join(t.TempDir(), "m")))
	if code != exitDone || errb != "" || !strings.Contains(out, "tier-wall took 10") || !strings.Contains(out, "load 50.0 at its end") ||
		!strings.Contains(out, "over its wall limit of 5 s, and passes or fails on its CPU time alone") {
		t.Fatalf("got %d, out %q, err %q", code, out, errb)
	}
}

// A child still running at the hang deadline is killed, and the tier fails as hung, by name.
func TestAChildThatOutlivesTheHangDeadlineIsKilledAndFailsAsHung(t *testing.T) {
	old := hangAfter
	defer func() { hangAfter = old }()
	hangAfter = func(int) time.Duration { return 300 * time.Millisecond }
	began := time.Now()
	code, out, errb := judgeCPU(t, "sleep", 0, nil, helper("hang"))
	if code != exitFailed || !strings.Contains(errb, "hung: still running at 300ms") || strings.Contains(out, "used") || time.Since(began) > 20*time.Second {
		t.Fatalf("got %d, out %q, err %q", code, out, errb)
	}
}

// The command's own failure is the tier's, and without --cpu the words after the flags stay a usage error.
func TestACommandThatFailsAndTheFlagsApart(t *testing.T) {
	code, _, errb := judgeCPU(t, "burn-under", 0, nil, helper("fail"))
	if code != exitFailed || !strings.Contains(errb, "the command exited 7") {
		t.Fatalf("got %d, err %q", code, errb)
	}
	var out, errs bytes.Buffer
	if got := Run(context.Background(), []string{"--cpu", "--budgets", "x", "--row", gateRow, "--tier", "t", "--since", "1", "--", "true"}, time.Now(), fixed(0), &out, &errs); got != exitUsage {
		t.Fatalf("--cpu with no --cpu-row: got %d", got)
	}
	if got := Run(context.Background(), []string{"--cpu-row", cpuRow, "--budgets", "x", "--row", gateRow, "--tier", "t", "--since", "1"}, time.Now(), fixed(0), &out, &errs); got != exitUsage {
		t.Fatalf("--cpu-row with no --cpu: got %d", got)
	}
}

// The tier runs with the caller's own environment, as a tier does without --cpu: a tier that runs go finds its
// cache (on Windows GOCACHE comes from LocalAppData, which go-proc's bare environment leaves out), and a variable
// the caller set reaches the member.
//
// What it costs when this fails: on Windows every go command of a tier under --cpu exits 1 with "GOCACHE is not
// defined and %LocalAppData% is not defined", before the tier's first member does its work.
func TestTheTierRunsWithTheCallersEnvironment(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	if code, out, errb := judgeCPU(t, "burn-under", 0, nil, []string{"--", "go", "env", "GOCACHE"}); code != exitDone || errb != "" {
		t.Errorf("go env GOCACHE under --cpu: got %d, out %q, err %q", code, out, errb)
	}
	t.Setenv("HATCH_TEST_CALLER_VARIABLE", "set by the caller")
	if code, _, errb := judgeCPU(t, "burn-under", 0, nil, helper("env", "HATCH_TEST_CALLER_VARIABLE", "set by the caller")); code != exitDone {
		t.Errorf("a variable the caller set: got %d, err %q", code, errb)
	}
}

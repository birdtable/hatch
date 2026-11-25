package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/birdtable/go-testkit/taskfile"
	"go.yaml.in/yaml/v3"
)

// The Taskfile calls Go: testkit's lint holds Taskfile.yml to no shell utility, pipe, redirection, shell loop,
// /tmp path or path joined in shell.
//
// What it costs when this fails: a Taskfile step runs in go-task's interpreter, which does not run shell as
// a Unix shell's does on Windows.
func TestTheTaskfileCallsGo(t *testing.T) {
	t.Parallel()
	taskfile.Check(t, "../..")
}

func runGates(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

// fmt lists the files gofmt would change and fails, and passes a folder of formatted files.
//
// What it costs when this fails: an unformatted file passes the commit tier, or a formatted one fails it.
func TestFmtFailsOnAFileGofmtWouldChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte("package x\n\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errb := runGates(t, "fmt", dir); code != 0 || out != "" {
		t.Errorf("a formatted folder: exit %d, want 0 and nothing printed\nstdout: %s\nstderr: %s", code, out, errb)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte("package x\nfunc  G( ) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errb := runGates(t, "fmt", dir)
	if code != 1 || !strings.Contains(out, "gofmt would change:") || !strings.Contains(out, "bad.go") || strings.Contains(out, "ok.go") {
		t.Errorf("an unformatted file: exit %d, want 1 naming bad.go alone\nstdout: %s\nstderr: %s", code, out, errb)
	}
}

// A form the usage does not give is exit 2.
//
// What it costs when this fails: a mistyped step in the Taskfile passes as a gate that ran.
func TestAFormOfNoUsageIsExit2(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{}, {"nope"}, {"vet", "extra"}, {"now", "x"}, {"budget"}, {"budget", "a", "b", "c", "d"}, {"fmt", "a", "b"}} {
		if code, _, errb := runGates(t, args...); code != 2 || !strings.Contains(errb, "usage: gates") {
			t.Errorf("%v: exit %d, want 2 and the usage\nstderr: %s", args, code, errb)
		}
	}
}

// The budget a tier is held to depends on the kind of host: the tier's own row on the kind of host the file names,
// the -session row on a Linux or Windows x86-64 release session that is no hosted runner and where the file has
// one, and none on any other host.
//
// What it costs when this fails: a tier is held to a budget of another kind of host, or to none on a host that has one.
func TestTheBudgetRowFollowsTheKindOfHost(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                 string
		kind, host, tier     string
		onRunner, hasSession bool
		want                 string
	}{
		{"the file's own kind of host", "Darwin-arm64", "Darwin-arm64", "pre-push", false, false, "pre-push"},
		{"the file's kind on a runner", "Darwin-arm64", "Darwin-arm64", "release", true, true, "release"},
		{"a Linux release session", "Linux-x86_64", "Darwin-arm64", "release", false, true, "release-session"},
		{"a Windows release session", "Windows-x86_64", "Darwin-arm64", "release", false, true, "release-session"},
		{"a hosted runner has none", "Linux-x86_64", "Darwin-arm64", "release", true, true, ""},
		{"no -session row has none", "Linux-x86_64", "Darwin-arm64", "pre-push", false, false, ""},
		{"an Intel Mac has none", "Darwin-x86_64", "Darwin-arm64", "pre-push", false, true, ""},
		{"Linux on arm has none", "Linux-arm64", "Darwin-arm64", "release", false, true, ""},
	} {
		if got := budgetID(c.kind, c.host, c.tier, c.onRunner, c.hasSession); got != c.want {
			t.Errorf("%s: row %q, want %q", c.name, got, c.want)
		}
	}
}

// The toolchain's names for a system and a processor make the kind of host budgets.yaml names.
//
// What it costs when this fails: a macOS tier is held to no budget, or a Linux one to macOS's.
func TestTheKindOfHostIsTheSystemAndTheProcessor(t *testing.T) {
	t.Parallel()
	for in, want := range map[[2]string]string{
		{"darwin", "arm64"}: "Darwin-arm64", {"darwin", "amd64"}: "Darwin-x86_64",
		{"linux", "amd64"}: "Linux-x86_64", {"windows", "amd64"}: "Windows-x86_64", {"freebsd", "arm64"}: "freebsd-arm64",
	} {
		if got := kindOf(in[0], in[1]); got != want {
			t.Errorf("kindOf(%s, %s) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// taskCmd is one command of a task: a plain string, or a mapping with a cmd and a timeout.
type taskCmd struct {
	Cmd     string `yaml:"cmd"`
	Timeout string `yaml:"timeout"`
}

func (c *taskCmd) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&c.Cmd)
	}
	type plain taskCmd
	return n.Decode((*plain)(c))
}

// taskfileTask is the part of a task of Taskfile.yml the rows read.
type taskfileTask struct {
	Cmds []taskCmd `yaml:"cmds"`
}

// The three members of a tier that are one go command, lint, taskfile and second-consumer, repeat the command
// their Taskfile task holds, with its deadline: the task and the tier are the same step.
//
// What it costs when this fails: `task lint` and the lint the push tier runs differ, so a change to one is
// not the other's.
func TestTheMembersOfATierAreTheTaskfilesTasks(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Tasks map[string]taskfileTask `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		args    []string
		timeout time.Duration
	}{
		"lint":            {lintArgs, lintTimeout},
		"taskfile":        {taskfileArgs, 5 * time.Minute},
		"second-consumer": {secondConsumerArgs, 5 * time.Minute},
	} {
		task, ok := file.Tasks[name]
		if !ok || len(task.Cmds) != 1 {
			t.Errorf("task %s: want one command in Taskfile.yml", name)
			continue
		}
		got := strings.ReplaceAll(task.Cmds[0].Cmd, "'", "")
		if wantCmd := "go " + strings.Join(want.args, " "); got != wantCmd {
			t.Errorf("task %s holds %q, the tier's member %q", name, got, wantCmd)
		}
		if d, err := time.ParseDuration(task.Cmds[0].Timeout); err != nil || d != want.timeout {
			t.Errorf("task %s has timeout %q, the tier's member %v", name, task.Cmds[0].Timeout, want.timeout)
		}
	}
	for tier, names := range tiers {
		for _, name := range names {
			if _, ok := members[name]; !ok {
				t.Errorf("tier %s names %s, which is no member", tier, name)
			}
			if _, ok := file.Tasks[name]; !ok {
				t.Errorf("tier %s names %s, which is no task of Taskfile.yml", tier, name)
			}
		}
		task, ok := file.Tasks[tier]
		if !ok || len(task.Cmds) != 1 || task.Cmds[0].Cmd != "go tool gates tier "+tier {
			t.Errorf("task %s is not the one call `go tool gates tier %s`", tier, tier)
		}
	}
}

// A tier runs its members together, answers 0 only when every one did, and ends the others when one fails.
//
// What it costs when this fails: a failed member leaves a tier green, or a tier waits out the members a red
// one has already made pointless.
func TestATierRunsItsMembersTogetherAndStopsAtAFailure(t *testing.T) {
	t.Parallel()
	pass := func(_ context.Context, o, _ io.Writer) int { fmt.Fprintln(o, "ran"); return 0 }
	fail := func(context.Context, io.Writer, io.Writer) int { return 1 }
	var cancelled atomic.Bool
	wait := func(ctx context.Context, _, _ io.Writer) int {
		select {
		case <-ctx.Done():
			cancelled.Store(true)
			return 1
		case <-time.After(30 * time.Second):
			return 0
		}
	}
	var out, errb bytes.Buffer
	if code := together(context.Background(), []string{"a", "b"}, map[string]member{"a": pass, "b": pass}, &out, &errb); code != 0 || strings.Count(out.String(), "ran") != 2 {
		t.Errorf("two members that pass: exit %d\n%s", code, out.String())
	}
	if code := together(context.Background(), []string{"a", "b"}, map[string]member{"a": pass, "b": fail}, &out, &errb); code != 1 {
		t.Errorf("one member that fails: exit %d, want 1", code)
	}
	if code := together(context.Background(), []string{"a", "w"}, map[string]member{"a": fail, "w": wait}, &out, &errb); code != 1 || !cancelled.Load() {
		t.Errorf("a failure must end the member still waiting: exit %d, cancelled %v", code, cancelled.Load())
	}
	if code := together(context.Background(), []string{"nope"}, map[string]member{}, &out, &errb); code != 2 {
		t.Errorf("a name that is no member: exit %d, want 2", code)
	}
}

// The pre-push tier is judged by the CPU time of its members: its members are a command of their own,
// `gates members <tier>`, which runs them together and judges nothing, and a tier that is none is a usage error. The
// pre-push tier's test member is the one run with -short, the release tier's the one without.
//
// What it costs when this fails: hatch budget --cpu runs a command that is no tier's members, or the push tier runs
// the rows on the real gitleaks that the release tier is to hold alone.
func TestThePushTierRunsItsMembersApartAndWithShortTests(t *testing.T) {
	t.Parallel()
	if code, _, errb := runGates(t, "members", "no-such-tier"); code != 2 || !strings.Contains(errb, "usage") {
		t.Errorf("members of a tier that is none: exit %d, want 2 with the usage\n%s", code, errb)
	}
	if code, _, _ := runGates(t, "members"); code != 2 {
		t.Errorf("members of no tier: exit %d, want 2", code)
	}
	if got := tiers[cpuJudged]; !slicesHas(got, "test-push") || slicesHas(got, "test") {
		t.Errorf("the %s tier holds %v, want test-push and not test", cpuJudged, got)
	}
	if got := tiers["pre-commit"]; slicesHas(got, "test") || slicesHas(got, "test-push") {
		t.Errorf("the pre-commit tier holds %v, which runs no tests", got)
	}
}

func slicesHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

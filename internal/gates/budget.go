package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/birdtable/hatch/internal/budget"
)

// hostKind is the kind of host the tier runs on, as budgets.yaml's host line names it: the system and the
// processor, "Darwin-arm64", "Linux-x86_64". The toolchain says which it is (go env GOHOSTOS and GOHOSTARCH),
// as no code of the repository reads runtime.GOOS. A Windows host is "Windows-x86_64" where the shell's
// uname said MINGW64_NT-10.0-.. and the same processor.
func hostKind(ctx context.Context) (kind, system string, err error) {
	var out strings.Builder
	code, err := start(ctx, "go", "", nil, time.Minute, &out, io.Discard, "env", "GOHOSTOS", "GOHOSTARCH")
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(out.String())
	if code != 0 || len(fields) != 2 {
		return "", "", fmt.Errorf("go env GOHOSTOS GOHOSTARCH exited %d and printed %q", code, out.String())
	}
	return kindOf(fields[0], fields[1]), fields[0], nil
}

// kindOf is the kind of host the toolchain's system and processor names make.
func kindOf(system, arch string) string {
	name := map[string]string{"linux": "Linux", "darwin": "Darwin", "windows": "Windows"}[system]
	if name == "" {
		name = system
	}
	if arch == "amd64" {
		arch = "x86_64"
	}
	return name + "-" + arch
}

// budgetID is the row of budgets.yaml a tier is held to on a host of this kind, "" when none applies: the
// tier's own row on the kind of host the file names; the tier's -session row on a release session (Linux or
// Windows, x86-64) that is not a hosted runner, where the file has one; and none on any other host.
func budgetID(kind, host, tier string, onRunner, hasSession bool) string {
	switch {
	case kind == host:
		return tier
	case (kind == "Linux-x86_64" || kind == "Windows-x86_64") && !onRunner && hasSession:
		return tier + "-session"
	}
	return ""
}

// hostLine is the host budgets.yaml names: the text after "host:" on its line.
var hostLine = regexp.MustCompile(`(?m)^host: *(.*)$`)

// rowExpr is one row of budgets.yaml, as the Taskfile gave hatch budget.
const rowExpr = `^(?P<id>[a-z-]+): (?P<limit>[0-9]+)$`

// held holds a tier to its budget in budgets.yaml: the elapsed seconds since start, judged by hatch budget.
// A pre-commit or pre-push over its budget passes when the 1-minute load sampled as the tier started, load, is
// above 15; the release tier is held at any load. On a host of the kind budgets.yaml
// names the budget is the tier's; on a release session it is the tier's -session row where there is one; on
// any other host, and on a hosted runner (GITHUB_ACTIONS set), none is known and none applies.
func held(ctx context.Context, tier, since, load string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile("budgets.yaml")
	if err != nil {
		fmt.Fprintf(stderr, "gates: %v\n", err)
		return exitRed
	}
	kind, _, err := hostKind(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "gates: %v\n", err)
		return exitRed
	}
	host := ""
	if m := hostLine.FindSubmatch(data); m != nil {
		host = strings.TrimSpace(string(m[1]))
	}
	hasSession := regexp.MustCompile("(?m)^" + regexp.QuoteMeta(tier) + "-session: ").Match(data)
	id := budgetID(kind, host, tier, os.Getenv("GITHUB_ACTIONS") != "", hasSession)
	if id == "" {
		fmt.Fprintf(stdout, "%s: no budget for this kind of host (%s), so none applies\n", tier, kind)
		return exitOK
	}
	args := []string{"--budgets", "budgets.yaml", "--row", rowExpr, "--tier", id, "--since", since}
	if tier != "release" {
		args = append(args, "--window", "1", "--limit", "15", "--loaded-when", "before",
			"--noun", "the tier", "--excused", "load, not the code")
		if load != "" {
			args = append(args, "--load-before", load)
		}
	}
	return budget.Run(ctx, args, time.Now(), budget.HostLoad, stdout, stderr)
}

// currentLoad is the machine's 1-minute load average as a number to print, and "" where the host keeps none
// the budget reads: the shell read /proc/loadavg on Linux and sysctl on macOS, and no other system.
func currentLoad(ctx context.Context) string {
	_, system, err := hostKind(ctx)
	if err != nil || (system != "linux" && system != "darwin") {
		return ""
	}
	l, _, err := budget.HostLoad(ctx, 1)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%.2f", l)
}

// sampleLoad prints currentLoad.
func sampleLoad(ctx context.Context, stdout io.Writer) int {
	fmt.Fprintln(stdout, currentLoad(ctx))
	return exitOK
}

// cpuJudged is the tier that is judged by CPU time, not wall time.
const cpuJudged = "pre-push"

// cpuRowExpr is one row of budgets.yaml's CPU limits: the tier's name and "-cpu".
const cpuRowExpr = `^(?P<id>[a-z-]+)-cpu: (?P<limit>[0-9]+)$`

// cpuTier runs the members of the tier in a tree of its own, under hatch budget --cpu, and holds the user plus
// system time of that tree to the tier's CPU row of budgets.yaml: the same on a quiet machine and a loaded one, so
// no load excuses it. The tier's wall row is hatch's hang guard only, a run still going at ten times it is killed.
// On a host the file does not name, and on a hosted runner, the members run and no budget applies.
func cpuTier(ctx context.Context, name, since, load string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile("budgets.yaml")
	if err != nil {
		fmt.Fprintf(stderr, "gates: %v\n", err)
		return exitRed
	}
	kind, _, err := hostKind(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "gates: %v\n", err)
		return exitRed
	}
	host := ""
	if m := hostLine.FindSubmatch(data); m != nil {
		host = strings.TrimSpace(string(m[1]))
	}
	if id := budgetID(kind, host, name, os.Getenv("GITHUB_ACTIONS") != "", false); id == "" {
		if code := membersOf(ctx, name, stdout, stderr); code != exitOK {
			return code
		}
		fmt.Fprintf(stdout, "%s: no budget for this kind of host (%s), so none applies\n", name, kind)
		return exitOK
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "gates: %v\n", err)
		return exitRed
	}
	args := []string{"--cpu", "--budgets", "budgets.yaml", "--row", rowExpr, "--cpu-row", cpuRowExpr, "--tier", name,
		"--since", since, "--window", "1", "--noun", "the tier"}
	if load != "" {
		args = append(args, "--load-before", load)
	}
	args = append(args, "--", self, "members", name)
	return budget.Run(ctx, args, time.Now(), budget.HostLoad, stdout, stderr)
}

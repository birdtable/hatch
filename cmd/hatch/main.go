// Command hatch is one pinned command for the checks that projects share.
//
// Built on: internal/budget and internal/tests, and through them the standard
// library and go-proc.
// Adds: the commands, budget, tests, names, runs, secrets, age, tag and writes, and their exit codes.
// Configured by: the calling project, through the flags of each command.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/birdtable/hatch/internal/age"
	"github.com/birdtable/hatch/internal/budget"
	"github.com/birdtable/hatch/internal/names"
	"github.com/birdtable/hatch/internal/runs"
	"github.com/birdtable/hatch/internal/secrets"
	"github.com/birdtable/hatch/internal/tag"
	"github.com/birdtable/hatch/internal/tests"
	"github.com/birdtable/hatch/internal/writes"
)

// usage names the exit codes: the budget command's own are in its usage.
const usage = `usage: hatch <budget|tests|names|runs|secrets|age|tag|writes> [args]

hatch names checks a hashed list of forbidden names against a project: run it with no flag for its usage.
hatch runs checks which package starts which external program against a rules file: run it with no flag for its usage.
hatch secrets runs the caller's gitleaks over the history and is green only when it scanned a commit and found nothing: a wrong flag prints its usage.
hatch age refuses a Go module version younger than the caller's window, read from go list: run it with no flag for its usage.
hatch tag holds a tree to what a release built from it must be, at three moments (before the tag, --snapshot, --tagged): run it with no flag for its usage.

hatch writes runs a command and is green only when it wrote nothing under a root, listing the tree before and after: run it with no flag for its usage.

hatch tests packages [exclude...] prints go list ./... without the paths named; exit 1 when none is left.

exit codes of hatch tests:
  0  green
  1  red, by the rule --red-when names: a test failed, none ran, or every test skipped
  2  a usage or run error
  3  go not found on PATH
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "budget":
		return budget.Run(context.Background(), args[1:], time.Now(), budget.HostLoad, os.Stdout, os.Stderr)
	case "tests":
		return tests.Run(context.Background(), args[1:], os.Stdout, os.Stderr)
	case "secrets":
		return secrets.Run(context.Background(), args[1:], os.Stdout, os.Stderr)
	case "runs":
		return runs.Run(context.Background(), args[1:], os.Stdout, os.Stderr)
	case "writes":
		return writes.Run(context.Background(), args[1:], os.Stdout, os.Stderr)
	case "tag":
		return tag.Run(context.Background(), args[1:], os.Stdout, os.Stderr)
	case "age":
		return age.Run(context.Background(), args[1:], time.Now(), os.Stdout, os.Stderr)
	case "names":
		return names.Run(context.Background(), args[1:], os.Stdin, os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "hatch: unknown command %q\n", args[0])
		return 2
	}
}

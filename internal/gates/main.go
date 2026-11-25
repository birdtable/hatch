// Command gates is the Go the repository's Taskfile.yml calls where it once ran shell the gofmt check, go vet and go build for each system, the race tests judged by hatch
// itself, and the budget a tier is held to. It is the repository's own, not a product: nothing
// ships it, and the Taskfile runs it as a tool of go.mod (go tool gates), which go builds once and caches.
//
// Built on: go-proc's proc.Run, which starts every program, and internal/budget, which judges a
// tier; the standard library for the rest.
// Adds: the Taskfile's steps as they were, each one a subcommand, so the Taskfile holds no shell.
// Configured by: the Taskfile's arguments and budgets.yaml; no value is held here that the shell did
// not hold.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/birdtable/go-proc/proc"
)

const (
	exitOK    = 0
	exitRed   = 1
	exitUsage = 2

	// maxOutput caps what one program may write on a stream. The shell had no cap; the cap is far above any
	// tier's output, and a run past it is ended and red, never cut short and green.
	maxOutput = 1 << 28
)

const usage = `usage: gates <fmt [folder]|vet|build|test|test-push|tier <pre-commit|pre-push>|members <pre-commit|pre-push>|now|load|budget <tier> <start> [<load>]>
`

// systems are the six systems vet and build run for, in the order the Taskfile's matrix gave them.
var systems = []struct{ os, arch string }{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// run runs one subcommand and returns its exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch rest := args[1:]; args[0] {
	case "fmt":
		if len(rest) > 1 {
			break
		}
		dir := "."
		if len(rest) == 1 {
			dir = rest[0]
		}
		return formatted(ctx, dir, stdout, stderr)
	case "vet", "build":
		if len(rest) != 0 {
			break
		}
		return eachSystem(ctx, args[0], stdout, stderr)
	case "test":
		if len(rest) != 0 {
			break
		}
		return races(ctx, stdout, stderr)
	case "test-push":
		if len(rest) != 0 {
			break
		}
		return pushRaces(ctx, stdout, stderr)
	case "tier":
		if len(rest) != 1 {
			break
		}
		return tier(ctx, rest[0], stdout, stderr)
	case "members":
		if len(rest) != 1 {
			break
		}
		return membersOf(ctx, rest[0], stdout, stderr)
	case "now":
		if len(rest) != 0 {
			break
		}
		fmt.Fprintln(stdout, time.Now().Unix())
		return exitOK
	case "load":
		if len(rest) != 0 {
			break
		}
		return sampleLoad(ctx, stdout)
	case "budget":
		if len(rest) != 2 && len(rest) != 3 {
			break
		}
		load := ""
		if len(rest) == 3 {
			load = rest[2]
		}
		return held(ctx, rest[0], rest[1], load, stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return exitUsage
}

// find is the absolute path of the program name on PATH.
func find(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	return filepath.Abs(p)
}

// start runs the program name with args in dir, with env added to the caller's environment, the output
// written as it comes to stdout and stderr, and the deadline given. It returns the result's exit code.
func start(ctx context.Context, name, dir string, env []string, timeout time.Duration, stdout, stderr io.Writer, args ...string) (int, error) {
	path, err := find(name)
	if err != nil {
		return exitRed, err
	}
	res, err := proc.Run(ctx, proc.Cmd{
		Path:      path,
		Args:      args,
		Dir:       dir,
		Env:       append(os.Environ(), env...),
		Timeout:   timeout,
		MaxOutput: maxOutput,
		Drain:     2 * time.Second,
		Stdout:    stdout,
		Stderr:    stderr,
	})
	if err != nil {
		return exitRed, err
	}
	return res.Code, nil
}

// finish turns what start gave into an exit code, saying why on stderr when the program did not run.
func finish(code int, err error, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintf(stderr, "gates: %v\n", err)
		return exitRed
	}
	return code
}

// formatted fails on a file gofmt would change: it lists them, as the shell step did.
func formatted(ctx context.Context, dir string, stdout, stderr io.Writer) int {
	var out strings.Builder
	code, err := start(ctx, "gofmt", dir, nil, time.Minute, &out, stderr, "-l", ".")
	if err != nil || code != 0 {
		return finish(max(code, exitRed), err, stderr)
	}
	if list := strings.TrimSpace(out.String()); list != "" {
		fmt.Fprintf(stdout, "gofmt would change:\n%s\n", list)
		return exitRed
	}
	return exitOK
}

// eachSystem runs go vet or go build for each of the six systems, without cgo, and stops at the first
// that fails.
func eachSystem(ctx context.Context, verb string, stdout, stderr io.Writer) int {
	for _, s := range systems {
		fmt.Fprintf(stdout, "gates: go %s, GOOS=%s GOARCH=%s\n", verb, s.os, s.arch)
		env := []string{"CGO_ENABLED=0", "GOOS=" + s.os, "GOARCH=" + s.arch}
		if code, err := start(ctx, "go", "", env, 5*time.Minute, stdout, stderr, verb, "./..."); err != nil || code != 0 {
			return finish(code, err, stderr)
		}
	}
	return exitOK
}

// races runs the tests with the race detector, shuffled, judged by hatch tests itself: a run that skipped
// every test or ran none is red. cgo is on, which -race needs, whatever the caller's environment holds. With short,
// the run is go test -short: the rows on the real gitleaks skip, and the pre-push tier takes the stand-in rows alone;
// the release tier takes every row.
func races(ctx context.Context, stdout, stderr io.Writer) int {
	return racesRun(ctx, false, stdout, stderr)
}

// pushRaces is races with -short: the pre-push tier's test member.
func pushRaces(ctx context.Context, stdout, stderr io.Writer) int {
	return racesRun(ctx, true, stdout, stderr)
}

func racesRun(ctx context.Context, short bool, stdout, stderr io.Writer) int {
	args := []string{"run", "./cmd/hatch", "tests", "--timeout", "10m", "--red-when", "failed,none-ran,all-skipped",
		"--green-word", "green", "--red-word", "red", "-count=1", "-race", "-shuffle=on"}
	if short {
		args = append(args, "-short")
	}
	code, err := start(ctx, "go", "", []string{"CGO_ENABLED=1"}, 10*time.Minute, stdout, stderr, append(args, "./...")...)
	return finish(code, err, stderr)
}

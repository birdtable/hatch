package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// A member is one step of a tier: it writes what it prints on the two streams and returns an exit code.
type member func(ctx context.Context, stdout, stderr io.Writer) int

// goStep is a member that runs `go` with args, under a deadline.
func goStep(timeout time.Duration, args ...string) member {
	return func(ctx context.Context, stdout, stderr io.Writer) int {
		code, err := start(ctx, "go", "", nil, timeout, stdout, stderr, args...)
		return finish(code, err, stderr)
	}
}

// The members of the tiers, each the step the Taskfile's task of the same name is. lint, taskfile and
// second-consumer repeat the command their Taskfile task holds, and a row holds the two equal.
var members = map[string]member{
	"fmt":             func(ctx context.Context, o, e io.Writer) int { return formatted(ctx, ".", o, e) },
	"vet":             func(ctx context.Context, o, e io.Writer) int { return eachSystem(ctx, "vet", o, e) },
	"build":           func(ctx context.Context, o, e io.Writer) int { return eachSystem(ctx, "build", o, e) },
	"test":            races,
	"test-push":       pushRaces,
	"lint":            goStep(lintTimeout, lintArgs...),
	"taskfile":        goStep(5*time.Minute, taskfileArgs...),
	"second-consumer": goStep(5*time.Minute, secondConsumerArgs...),
}

// The commands of the three members that are one go command, as the Taskfile's tasks of the same names
// hold them.
var (
	lintArgs           = []string{"tool", "-modfile=tools/golangci-lint.mod", "golangci-lint", "run", "./..."}
	lintTimeout        = 10 * time.Minute
	taskfileArgs       = []string{"test", "-count=1", "-run", "^TestTheTaskfileCallsGo$", "./internal/gates/"}
	secondConsumerArgs = []string{"test", "-count=1", "-run", "^TestSecondConsumer", "./..."}
)

// tiers are the members each tier runs, together.
var tiers = map[string][]string{
	"pre-commit": {"fmt", "vet", "taskfile", "second-consumer"},
	"pre-push":   {"fmt", "vet", "lint", "build", "test-push", "taskfile", "second-consumer"},
}

// lockedWriter lets the members of a tier share one stream: a Write of one member is never split by one of
// another's.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// together runs the members at once, as go-task ran a task's deps, and ends the others when one fails: it
// waits for every member and answers 0 only when every one answered 0.
func together(ctx context.Context, names []string, table map[string]member, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	out, errs := lockedWriter{&mu, stdout}, lockedWriter{&mu, stderr}
	var wg sync.WaitGroup
	codes := make([]int, len(names))
	for i, name := range names {
		m, ok := table[name]
		if !ok {
			fmt.Fprintf(stderr, "gates: no member %q\n", name)
			return exitUsage
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fmt.Fprintf(out, "gates: %s\n", name)
			if codes[i] = m(ctx, out, errs); codes[i] != 0 {
				cancel()
			}
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c != 0 {
			return exitRed
		}
	}
	return exitOK
}

// tier runs a tier: its members together, then the tier held to its budget. The tier's start second and its
// 1-minute load are taken before any member runs, as the Taskfile's vars were. The tool is started once for
// the whole tier, not once for each step. The pre-push tier is judged by the CPU time of its members
// (hatch budget --cpu): see cpuTier.
func tier(ctx context.Context, name string, stdout, stderr io.Writer) int {
	names, ok := tiers[name]
	if !ok {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	load := currentLoad(ctx)
	since := strconv.FormatInt(time.Now().Unix(), 10)
	if name == cpuJudged {
		return cpuTier(ctx, name, since, load, stdout, stderr)
	}
	if code := together(ctx, names, members, stdout, stderr); code != exitOK {
		return code
	}
	return held(ctx, name, since, load, stdout, stderr)
}

// membersOf runs the members of a tier together and judges nothing: it is the command cpuTier has hatch budget
// run in a tree of its own.
func membersOf(ctx context.Context, name string, stdout, stderr io.Writer) int {
	names, ok := tiers[name]
	if !ok {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	return together(ctx, names, members, stdout, stderr)
}

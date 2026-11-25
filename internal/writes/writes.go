// Package writes holds a command to writing nothing in a folder tree: it takes a listing of the tree before
// the command and one after, and names every path the command added, changed or removed.
//
// Built on: go-files' treesum, which lists a tree by handle, follows no link and refuses what a pinned tree
// should not hold, and go-proc's proc.Run, which starts the command; the standard library for the rest.
// Adds: the diff of the two listings by path, the exit code of the command kept beside the list, and a
// refusal of the tree that names its path.
// Configured by: the calling project: the root, the names to leave out, the caps on the walk, the command and its deadline.
package writes

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/birdtable/go-files/treesum"
	"github.com/birdtable/go-proc/proc"

	"github.com/birdtable/hatch/internal/gitrun"
)

const (
	exitGreen  = 0
	exitRed    = 1
	exitUsage  = 2
	exitNoGit  = 3
	gitTimeout = 2 * time.Minute

	// maxOutput caps what the command may write on a stream, as hatch budget caps it: a run past it is
	// ended and red, never cut short and green.
	maxOutput = 1 << 28

	// defaultEntries and defaultBytes are the caps of the walk when the caller names none: the most entries
	// and the most bytes of file content one listing takes.
	defaultEntries = 200_000
	defaultBytes   = 1 << 30

	defaultTimeout = 30 * time.Minute
)

const usage = `usage: hatch writes [--root <dir>] [--exclude <name>]... [--max-entries <n>] [--max-bytes <n>] [--timeout <duration>] -- <command> [args]

hatch writes runs the command and is green only when the command wrote nothing under the root.

--root         the folder to hold the command to (default: the top of the git checkout the working folder is in)
--exclude      a name to leave out of both listings, at any depth, and to leave unopened (repeatable; none by default)
--max-entries  the most entries (folders, files and links) one listing takes (default 200000)
--max-bytes    the most bytes of file content one listing hashes (default 1073741824)
--timeout      how long the command may run (default 30m); it is ended, with its tree, at the deadline

It lists the tree under the root before the command and after it, with go-files' treesum: every file by the
hash of its content, every link by its target and never followed, every folder, the files git ignores and
a folder named .git among them. A path in one listing and not the other is added or removed, and one in both
with another line is changed. It prints each as "added <path>", "changed <path>" or "removed <path>", one per
line, sorted by path, on stdout. A mode, a time and an owner are not listed, so a command that only touches a file
or changes its mode wrote nothing.

A name given to --exclude is skipped inside the walk, as treesum's Skip: a folder of that name is never opened, so what is
under it is neither listed, counted nor refused (a hard-linked node_modules under pnpm does not stop the run), and a
write under it, or to it, is no write. A name is one path element: a name that is blank or holds a slash is a usage
error, and a name that matches nothing changes nothing. A link whose target reaches a name left out is still refused,
as the listing does not cover it.

A tree that treesum refuses, before or after the command, is a refusal that names the path: a device, a FIFO, a
socket, a file with a second name, a link that leads out of the tree or loops, a name that is not UTF-8 text,
and a tree over a cap. Nothing is skipped but what --exclude names. A refusal before the command ends the run before the command
starts.

The command's own exit code is kept: a command that fails ends the run with its code, and the list of what it
wrote is printed beside it. A command that is ended, or killed by a signal, is exit 1.

exit codes of hatch writes:
  0  the command succeeded and wrote nothing
  1  the command wrote, or the tree was refused, or the command was ended
  2  a usage error, or a command that cannot be started
  3  git not found on PATH, when no --root is given
  any other code is the command's own
`

// Run runs hatch writes and returns its exit code: the command's own when it fails.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("writes", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	root := fl.String("root", "", "")
	entries := fl.Int("max-entries", defaultEntries, "")
	size := fl.Int64("max-bytes", defaultBytes, "")
	timeout := fl.Duration("timeout", defaultTimeout, "")
	skip := map[string]bool{}
	bad := false
	fl.Func("exclude", "", func(name string) error {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\") {
			bad = true
		}
		skip[name] = true
		return nil
	})
	if err := fl.Parse(args); err != nil || bad || fl.NArg() == 0 || *entries <= 0 || *size <= 0 || *timeout <= 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	argv := fl.Args()
	path, err := exec.LookPath(argv[0])
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		fmt.Fprintf(stderr, "hatch writes: %s not found on PATH\n", argv[0])
		return exitUsage
	}
	top := *root
	if top == "" {
		var code int
		if top, code = checkoutTop(ctx, stderr); code != exitGreen {
			return code
		}
	}
	folder, err := os.OpenRoot(top)
	if err != nil {
		fmt.Fprintf(stderr, "hatch writes: the root %s cannot be opened: %v\n", top, err)
		return exitUsage
	}
	defer folder.Close()
	caps := treesum.Caps{Entries: *entries, Bytes: *size}
	if len(skip) > 0 {
		caps.Skip = func(rel string) bool { return skip[rel[strings.LastIndexByte(rel, '/')+1:]] }
	}

	_, before, err := treesum.Sum(folder, ".", caps)
	if err != nil {
		fmt.Fprintf(stderr, "hatch writes: refused, and the command was not run: %s\n", refusal(err))
		return exitRed
	}
	res, runErr := proc.Run(ctx, proc.Cmd{
		Path:      path,
		Args:      argv[1:],
		Env:       os.Environ(),
		Timeout:   *timeout,
		MaxOutput: maxOutput,
		Drain:     2 * time.Second,
		Stdout:    stdout,
		Stderr:    stderr,
	})
	code := res.Code
	if runErr != nil {
		fmt.Fprintf(stderr, "hatch writes: the command did not finish: %v\n", runErr)
		code = exitRed
	}
	if code < 0 {
		code = exitRed
	}
	_, after, err := treesum.Sum(folder, ".", caps)
	if err != nil {
		fmt.Fprintf(stderr, "hatch writes: refused after the command: %s\n", refusal(err))
		return max(code, exitRed)
	}
	wrote := diff(before, after)
	for _, w := range wrote {
		fmt.Fprintln(stdout, w)
	}
	switch {
	case code != exitGreen:
		return code
	case len(wrote) > 0:
		fmt.Fprintf(stderr, "hatch writes: refused: %s wrote %d paths under %s\n", argv[0], len(wrote), top)
		return exitRed
	}
	fmt.Fprintf(stdout, "ok: %s wrote nothing under %s\n", argv[0], top)
	return exitGreen
}

// refusal is what treesum refused, with the cap flags named when a cap was the reason.
func refusal(err error) string {
	if errors.Is(err, treesum.ErrCap) {
		return fmt.Sprintf("%v (raise --max-entries or --max-bytes, or take a root with less in it)", err)
	}
	return err.Error()
}

// checkoutTop is the top of the git checkout the working folder is in, the default root.
func checkoutTop(ctx context.Context, stderr io.Writer) (string, int) {
	gitPath, err := gitrun.Find()
	if err != nil {
		fmt.Fprintln(stderr, "hatch writes: git is not on PATH, and no --root was given")
		return "", exitNoGit
	}
	g := gitrun.Git{Path: gitPath, Timeout: gitTimeout, MaxOutput: 1 << 20}
	out, err := g.Out(ctx, ".", "rev-parse", "--show-toplevel")
	if top := strings.TrimSpace(out); err == nil && top != "" {
		return filepath.FromSlash(top), exitGreen
	}
	fmt.Fprintln(stderr, "hatch writes: the working folder is in no git checkout: give --root")
	return "", exitUsage
}

// diff names each path that is in one listing and not the other, or in both with another line, sorted by path.
func diff(before, after []byte) []string {
	was, now := lines(before), lines(after)
	var out []string
	for p, l := range now {
		switch old, ok := was[p]; {
		case !ok:
			out = append(out, "added "+p)
		case old != l:
			out = append(out, "changed "+p)
		}
	}
	for p := range was {
		if _, ok := now[p]; !ok {
			out = append(out, "removed "+p)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		_, pa, _ := strings.Cut(a, " ")
		_, pb, _ := strings.Cut(b, " ")
		return strings.Compare(pa, pb)
	})
	return out
}

// lines is a listing as its lines by path: the second field of a line is its path.
func lines(listing []byte) map[string]string {
	m := map[string]string{}
	for l := range strings.Lines(string(listing)) {
		f := strings.SplitN(l, "\t", 3)
		m[f[min(1, len(f)-1)]] = l
	}
	return m
}

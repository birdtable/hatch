// Package names checks a hashed list of forbidden names against what a
// project publishes: its tracked files and their paths, its branch name, each
// commit past a base the caller names (message, added lines, added paths),
// the authors' addresses, and text on stdin.
//
// Built on: the standard library, internal/gitrun (every git call, through
// go-proc's proc.Run with gitenv.Env and gitenv.ReadArgs), and go-files'
// trust.ReadOwned (the list and the exceptions file, 1 MiB at most).
// Adds: the list line (a SHA-256, then an optional letter count and what), the
// matcher (runs of 1 to 4 words, joined by a space and run together; with a
// count, a word glued inside a longer one and hyphen-joined words), the
// exceptions file, and plants built at run time before every scan.
// Configured by: the caller, in every flag of hatch names. The list, where it
// lies, its exceptions and the address pattern are the caller's; the package
// holds none. A name is never printed: a finding is <name>, a file and a line.
package names

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/birdtable/go-files/trust"
	"github.com/birdtable/hatch/internal/gitrun"
)

const (
	exitClean   = 0
	exitFound   = 1
	exitUsage   = 2
	exitNoGit   = 3
	maxStdin    = 4 << 20
	gitTimeout  = 2 * time.Minute
	gitMaxBytes = 256 << 20
)

const usage = `usage: hatch names --list <file> [--outside-tree] [--exceptions <file>] [--dir <folder>] [--tree] [--branch] [--base <rev>] [--addresses <regexp>] [--stdin]
       hatch names hash [--letters]   (reads the name from stdin, never from an argument)

--list          the list of hashed names (required): a line is a SHA-256 in hex, then an optional
                letter count and a what; blank lines and lines that start with # are passed over
--outside-tree  refuse a list that lies inside the work tree
--exceptions    the exceptions file: one exception a block, keys file, pattern, what and why
--dir           a folder of the repository to scan (default .)
--tree          scan every tracked file and its path
--branch        scan the branch name
--base          scan each commit past this revision: its message, the lines and paths it adds
--addresses     a regular expression each commit's author and committer address must match whole
--stdin         scan the text on stdin, as for a pull request's title and body

The list and the exceptions file are each read as a regular file, through no link, that the running
account (or the system) owns and no other account can write, 1 MiB at most; a refusal names the flag
and a reason, never the path.

At least one of --tree, --branch, --base and --stdin is required. A finding prints as <name>, the
file and the line; the name, the text around it and the list never print.

exit codes of hatch names:
  0  clean
  1  a finding
  2  a usage or run error, a list or surface that cannot be read, or a plant the scan missed
  3  git not found on PATH
`

// Run runs hatch names and returns its exit code. It fails closed: anything
// it cannot read or prove is exit 2, never a pass. The list and the exceptions
// file are read as trust.ReadOwned reads them; mounts are the caller's, passed
// to it.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, mounts ...trust.Who) int {
	if len(args) > 0 && args[0] == "hash" {
		return runHash(args[1:], stdin, stdout, stderr)
	}
	fl := flag.NewFlagSet("names", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var (
		listPath, exPath, dir, base, addr string
		outside, tree, branch, readStdin  bool
	)
	fl.StringVar(&listPath, "list", "", "")
	fl.StringVar(&exPath, "exceptions", "", "")
	fl.StringVar(&dir, "dir", ".", "")
	fl.StringVar(&base, "base", "", "")
	fl.StringVar(&addr, "addresses", "", "")
	fl.BoolVar(&outside, "outside-tree", false, "")
	fl.BoolVar(&tree, "tree", false, "")
	fl.BoolVar(&branch, "branch", false, "")
	fl.BoolVar(&readStdin, "stdin", false, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	if listPath == "" || !(tree || branch || base != "" || readStdin) {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "hatch names: %v\n", gitrun.Bare(err))
		if errors.Is(err, gitrun.ErrNoGit) {
			return exitNoGit
		}
		return exitUsage
	}
	if addr != "" && base == "" {
		return fail(errors.New("--addresses reads the author and committer of each commit past a base, so it needs --base"))
	}
	addresses, err := compileAddresses(addr)
	if err != nil {
		return fail(err)
	}
	if err := prove(); err != nil {
		return fail(err)
	}
	listData, err := readOwned("--list", listPath, mounts...)
	if err != nil {
		return fail(err)
	}
	l, err := ReadList(listData)
	if err != nil {
		return fail(err)
	}
	var ex *Exceptions
	if exPath != "" {
		data, err := readOwned("--exceptions", exPath, mounts...)
		if err != nil {
			return fail(err)
		}
		if ex, err = ReadExceptions(data); err != nil {
			return fail(err)
		}
	}
	gp, err := gitrun.Find()
	if err != nil {
		return fail(err)
	}
	s := scanner{git: gitrun.Git{Path: gp, Timeout: gitTimeout, MaxOutput: gitMaxBytes}}
	if outside {
		if err := s.refuseInside(ctx, dir, listPath); err != nil {
			return fail(err)
		}
	}
	t, err := s.read(ctx, dir, want{tree: tree, branch: branch, base: base})
	if err != nil {
		return fail(err)
	}
	if len(t.missing) > 0 {
		p := t.missing[0]
		return fail(fmt.Errorf("%d tracked files are gone from disk and still in git, so what git holds of them is unread (the first is %s): restore them or git rm them", len(t.missing), masked(p, l.hits(p))))
	}
	if readStdin {
		data, err := io.ReadAll(io.LimitReader(stdin, maxStdin+1))
		switch {
		case err != nil:
			return fail(fmt.Errorf("stdin cannot be read: %w", err))
		case len(data) > maxStdin:
			return fail(errors.New("stdin is longer than 4 MiB"))
		case len(bytes.TrimSpace(data)) == 0:
			return fail(fmt.Errorf("%w: stdin is empty", ErrEmpty))
		}
		t.stdin = string(data)
	}
	fs := judge(l, ex, t, addresses)
	for _, f := range fs {
		fmt.Fprintln(stdout, f.text)
	}
	if len(fs) > 0 {
		fmt.Fprintf(stderr, "hatch names: %d finding(s)\n", len(fs))
		return exitFound
	}
	return exitClean
}

// refuseInside is an error when the list lies inside the work tree dir is in.
func (s scanner) refuseInside(ctx context.Context, dir, list string) error {
	top, err := s.git.Top(ctx, dir)
	if err != nil {
		return err
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return fmt.Errorf("the work tree cannot be resolved: %w", gitrun.Bare(err))
	}
	abs, err := filepath.Abs(list)
	if err != nil {
		return err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return fmt.Errorf("the list cannot be resolved: %w", gitrun.Bare(err))
	}
	rel, err := filepath.Rel(top, abs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return errors.New("the list lies inside the work tree and --outside-tree is set")
	}
	return nil
}

// compileAddresses is the pattern --addresses names, made to match whole,
// nil when none is given.
func compileAddresses(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	re, err := whole(p)
	if err != nil {
		return nil, errors.New("--addresses is not a regular expression")
	}
	return re, nil
}

// runHash prints the list lines of the name on stdin. The name never comes
// from an argument, which a shell history and a process list would keep.
func runHash(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("names hash", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	letters := fl.Bool("letters", false, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 4096))
	if err != nil {
		fmt.Fprintln(stderr, "hatch names: stdin cannot be read")
		return exitUsage
	}
	out, err := HashLines(strings.TrimRight(string(data), "\r\n"), *letters)
	if err != nil {
		fmt.Fprintf(stderr, "hatch names: %v\n", err)
		return exitUsage
	}
	for _, line := range out {
		fmt.Fprintln(stdout, line)
	}
	return exitClean
}

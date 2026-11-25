package names

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/go-proc/proc"

	"github.com/birdtable/hatch/internal/gitrun"
)

// TestMain lets this test binary stand in for git: gitrun starts git as
// `git -C <dir> ...`, and go test never passes -C first, so a first argument
// of -C is the stand-in. It runs the real git, and for a log adds a record git
// never wrote, the shape of a log that holds a commit git's count does not.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-C" {
		os.Exit(fakeGit(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeGit(args []string) int {
	real, err := gitrun.Find()
	if err != nil {
		return 97
	}
	res, err := proc.Run(context.Background(), proc.Cmd{Path: real, Args: args, Timeout: time.Minute, MaxOutput: 1 << 24, Drain: 2 * time.Second})
	if err != nil {
		return 98
	}
	_, _ = os.Stdout.Write(res.Stdout)
	for _, a := range args {
		if a == "log" {
			fmt.Fprintf(os.Stdout, "\x00%s\x1fa@example.invalid\x1fa@example.invalid\x1fa forged record", strings.Repeat("ab", 20))
		}
	}
	_, _ = os.Stderr.Write(res.Stderr)
	return res.Code
}

// past holds the log it read to git's own count of the commits: a log with one
// more commit than rev-list counts is an error, and the same repository read
// by the real git is not.
//
// What it costs when this fails: a log that holds more or fewer commits than
// the history git counts is scanned, and the commits it left out go unread.
func TestPastHoldsTheLogToGitsOwnCount(t *testing.T) {
	dir := repo(t)
	write(t, dir, "b.txt", "second\n")
	commitAll(t, dir, "second")
	real, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	git := func(path string) scanner {
		return scanner{git: gitrun.Git{Path: path, Timeout: time.Minute, MaxOutput: 1 << 20}}
	}
	if _, _, err := git(real).past(context.Background(), dir, "HEAD~1"); err != nil {
		t.Fatalf("the real git: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = git(self).past(context.Background(), dir, "HEAD~1")
	if err == nil || !strings.Contains(err.Error(), "rev-list counts") {
		t.Fatalf("a log with a commit git did not count gave %v", err)
	}
}

// Package gitrun runs git for the hatch commands that read a repository, and
// reads the files git tracks.
//
// Built on: the standard library and github.com/birdtable/go-proc, its
// proc.Run with gitenv.Env and gitenv.ReadArgs.
// Adds: one bounded call that reads no configuration of the host's and
// honours no replace ref, an exit error that never holds what git printed,
// and the tracked files as they are on disk.
// Configured by: the command, which gives the deadline and the output cap.
package gitrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/birdtable/go-proc/gitenv"
	"github.com/birdtable/go-proc/proc"
)

// ErrNoGit is a PATH that holds no git.
var ErrNoGit = errors.New("git not found on PATH")

// Git is the git on PATH and the bounds of each run of it.
type Git struct {
	// Path is git's absolute path, as Find gives it.
	Path string
	// Timeout is how long one run may take, and MaxOutput how many bytes of
	// each stream it may write. A run past either is ended, and is an error.
	Timeout   time.Duration
	MaxOutput int
}

// Find is the absolute path of the git on PATH, or ErrNoGit.
func Find() (string, error) {
	p, err := exec.LookPath("git")
	if errors.Is(err, exec.ErrNotFound) {
		return "", ErrNoGit
	}
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

// ExitError is a git that ran and exited non-zero. Its message is the verb
// and the code alone: what git printed may hold a path or a ref's name, so
// it is in Stderr for a caller that may show it, and never in the message.
type ExitError struct {
	Verb   string
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string { return fmt.Sprintf("git %s exited %d", e.Verb, e.Code) }

// Run runs git in dir with args and returns its result, whatever its exit
// code. The environment is gitenv.Env over proc.Bare, so no variable of the
// caller's and no configuration of the host's moves it, with replace refs
// off; the arguments are gitenv.ReadArgs, so no hook and no file system
// monitor of the folder's runs. An error is a git that did not start, or a
// run that was ended at its bounds.
func (g Git) Run(ctx context.Context, dir string, args ...string) (proc.Result, error) {
	return g.run(ctx, dir, nil, args...)
}

// RunIndex is Run with GIT_INDEX_FILE set to index, which is the one variable of
// the caller's it hands on: the index a hook's git is committing, which is not
// .git/index under git commit -a or git commit <path>. index is made absolute
// (git reads a relative one from the folder the hook runs in, the work tree's
// top, and hatch's own folder is the same one) and an empty one is Run.
func (g Git) RunIndex(ctx context.Context, dir, index string, args ...string) (proc.Result, error) {
	if index == "" {
		return g.run(ctx, dir, nil, args...)
	}
	abs, err := filepath.Abs(index)
	if err != nil {
		return proc.Result{}, err
	}
	return g.run(ctx, dir, []string{"GIT_INDEX_FILE=" + abs}, args...)
}

func (g Git) run(ctx context.Context, dir string, extra []string, args ...string) (proc.Result, error) {
	return proc.Run(ctx, proc.Cmd{
		Path:      g.Path,
		Args:      gitenv.ReadArgs(dir, args...),
		Env:       gitenv.Env(proc.Bare(), append([]string{"GIT_NO_REPLACE_OBJECTS=1"}, extra...)...),
		Timeout:   g.Timeout,
		MaxOutput: g.MaxOutput,
		// Windows refuses a run of no Drain: it bounds each read of the
		// output after the kill, and git leaves nothing behind it.
		Drain: 2 * time.Second,
	})
}

// Remote runs git in dir for a verb that reads a remote, as a plain git run there would: the caller's own
// environment (its HOME, with the git configuration and the credential helper or .netrc there), less only the
// variables that name a repository, its index or its configuration, which gitenv.Repo takes out so a hook's
// GIT_DIR cannot turn the call to another repository. GIT_TERMINAL_PROMPT=0 means nothing waits on a person.
// Every other call keeps Run's environment: a remote is the one read that needs the caller's sign-in.
func (g Git) Remote(ctx context.Context, dir string, args ...string) (proc.Result, error) {
	return proc.Run(ctx, proc.Cmd{
		Path:      g.Path,
		Args:      gitenv.ReadArgs(dir, args...),
		Env:       gitenv.Repo(os.Environ(), "GIT_TERMINAL_PROMPT=0"),
		Timeout:   g.Timeout,
		MaxOutput: g.MaxOutput,
		Drain:     2 * time.Second,
	})
}

// Out runs git in dir and returns its stdout. A non-zero exit is an
// *ExitError.
func (g Git) Out(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := g.Run(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", Verb(args), err)
	}
	if res.Code != 0 {
		return "", &ExitError{Verb: Verb(args), Code: res.Code, Stderr: res.Stderr}
	}
	return string(res.Stdout), nil
}

// Verb is git's command in args, past any -c setting before it.
func Verb(args []string) string {
	for len(args) > 2 && args[0] == "-c" {
		args = args[2:]
	}
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// Top is the root of the work tree dir lies in, absolute. A folder that is
// in no work tree is an error.
func (g Git) Top(ctx context.Context, dir string) (string, error) {
	out, err := g.Out(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimRight(out, "\r\n")
	if top == "" {
		return "", errors.New("git rev-parse printed no work tree")
	}
	return filepath.Abs(filepath.FromSlash(top))
}

// TrackedPaths is the path of every file git tracks under root, relative to
// it, with / between the parts, in git's order.
func (g Git) TrackedPaths(ctx context.Context, root string) ([]string, error) {
	out, err := g.Out(ctx, root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// ReadTracked reads the tracked file rel under root as it is on disk. A link
// is read as its target, which is what git stores. ok is false for a file
// deleted from disk and for a name that is neither a file nor a link (a
// submodule's folder). An error holds no path: a path may hold what the
// caller guards.
func ReadTracked(root, rel string) (data []byte, ok bool, err error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, Bare(err)
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return nil, false, Bare(err)
		}
		return []byte(target), true, nil
	case !info.Mode().IsRegular():
		return nil, false, nil
	}
	data, err = os.ReadFile(full) //nolint:gosec // a tracked file of the repository the caller named
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, Bare(err)
	}
	return data, true, nil
}

// Gone is whether the tracked file rel is no longer on disk under root: a
// file deleted and not removed from git, which ReadTracked answers with ok
// false as it does a submodule's folder.
func Gone(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return errors.Is(err, fs.ErrNotExist)
}

// Bare is err without the path a file error carries, for a caller whose
// path may hold what it guards.
func Bare(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

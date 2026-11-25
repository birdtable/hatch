// Package tag holds a tree to what a release built from it must be: the commit its tag names.
//
// Built on: the standard library and internal/gitrun, which runs every git call through go-proc's proc.Run
// with gitenv.Env and gitenv.ReadArgs.
// Adds: the three moments of a release, before the tag, the recipe's dry release (--snapshot) and the
// publish (--tagged), each refusing a tree that is not clean; the remote asked with git ls-remote, the one
// ref that answers for a name (a decoy whose name ends in it does not), and a refusal that names its reason.
// It changes no ref.
// Configured by: the caller: the remote and the branch are its flags, with no default, and the version is the
// word its changelog tool prints; hatch reads no changelog.
package tag

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

const (
	exitOK     = 0
	exitRefuse = 1
	exitUsage  = 2
	exitNoGit  = 3
	gitTimeout = 2 * time.Minute
)

const usage = `usage: hatch tag --remote <name> --branch <name> [--dir <folder>] <version>
       hatch tag --remote <name> --snapshot [--dir <folder>]
       hatch tag --remote <name> --tagged [--on <branch>] [--dir <folder>] <version>

Any of the three takes --prefix <folder> before the version, for a module in a folder of the repository.

--remote    the remote the release is held to; required, and a name, not an option of git's
--branch    the branch a release is tagged on; required before the tag, and not given with the others
--dir       a folder of the repository (default .)
--snapshot  the recipe's dry release: a clean tree at any sha, and a branch the remote holds the same
--tagged    the publish: HEAD is the annotated tag <version>, and the remote's tag is the same commit,
            which is on the remote's default branch, or on the branch --on names
--on        the branch a --tagged commit must be on, when it is not the remote's default branch
--prefix    a folder of the repository (one or more slash-separated module-path elements: lower case, digits,
            - . and _), so the tag is <prefix>/<version>, as Go reads the tag of a module in a folder

Every moment refuses a tree that is not clean: the release builds the commit, so an edit it does not hold
would ship unseen. Before the tag, HEAD is the branch, the remote's branch is the same commit (not behind,
ahead or apart), and <version> is a tag neither here nor on the remote. --snapshot runs at any sha: on a
branch the remote holds, the branch is the remote's. --tagged holds HEAD to the annotated tag <version>, and
the remote's tag to the same commit, and that commit to be an ancestor of the remote's default branch (read
from refs/remotes/<remote>/HEAD, or asked with git ls-remote --symref) or of the remote's --on branch: a tag on a
side branch is refused by name. <version> is a release tag: v, a semantic version, a pre-release
allowed; it is refused before git is asked when it is not. The version is the caller's, as its changelog
tool prints it: hatch reads no changelog.

hatch tag asks the remote with git ls-remote and changes no ref, with the caller's own environment (its HOME,
so its git configuration and sign-in) and no prompt; a remote that is asked and cannot be read is a refusal.
--snapshot on a detached HEAD asks none, as there is no branch to compare, so it passes there whatever the
remote is. A refusal names its reason on stderr and exits 1; a pass prints one line on stdout.

exit codes of hatch tag:
  0  the tree is what the moment asks
  1  refused, with its reason on stderr
  2  a usage error
  3  git not found on PATH
`

// tagForm is a release tag: v and a semantic version, a pre-release allowed.
var tagForm = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

type mode int

const (
	before mode = iota
	snapshot
	tagged
)

// request is what the flags and the word asked for.
type request struct {
	mode                                 mode
	dir, remote, branch, tag, prefix, on string
}

// errRefused begins every refusal, so its line says so.
var errRefused = errors.New("refused")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errRefused, fmt.Sprintf(format, a...))
}

// Run runs hatch tag and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	req, ok := parse(args)
	if !ok {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	if req.mode != snapshot {
		if refusal := req.name(); refusal != nil {
			fmt.Fprintf(stderr, "hatch tag: %v\n", refusal)
			return exitRefuse
		}
	}
	path, err := gitrun.Find()
	if err != nil {
		fmt.Fprintf(stderr, "hatch tag: %v\n", err)
		return exitNoGit
	}
	g := &guard{ctx: ctx, git: gitrun.Git{Path: path, Timeout: gitTimeout, MaxOutput: 1 << 20}, req: req}
	said, err := g.step()
	if err != nil {
		fmt.Fprintf(stderr, "hatch tag: %v\n", err)
		return exitRefuse
	}
	fmt.Fprintf(stdout, "hatch tag: ok: %s\n", said)
	return exitOK
}

// parse reads the flags and the word. It is false for any form the usage does not give.
func parse(args []string) (request, bool) {
	fl := flag.NewFlagSet("tag", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	dir := fl.String("dir", ".", "")
	remote := fl.String("remote", "", "")
	branch := fl.String("branch", "", "")
	snap := fl.Bool("snapshot", false, "")
	tg := fl.Bool("tagged", false, "")
	prefix := fl.String("prefix", "", "")
	on := fl.String("on", "", "")
	if err := fl.Parse(args); err != nil {
		return request{}, false
	}
	req := request{dir: *dir, remote: *remote, branch: *branch, prefix: *prefix, on: *on}
	words := 1
	switch {
	case *snap && *tg:
		return request{}, false
	case *snap:
		req.mode, words = snapshot, 0
	case *tg:
		req.mode = tagged
	}
	rest := fl.Args()
	// A remote is a word of git's command line: one that starts with a dash would be read as an option of
	// ls-remote, so it is no remote.
	switch {
	case len(rest) != words, req.remote == "", strings.HasPrefix(req.remote, "-"), req.dir == "":
		return request{}, false
	case req.mode == before && req.branch == "", req.mode != before && req.branch != "":
		return request{}, false
	case req.mode == before && (strings.HasPrefix(req.branch, "-") || strings.ContainsAny(req.branch, " \t\r\n:")):
		return request{}, false
	case req.mode == snapshot && req.prefix != "", req.mode != tagged && req.on != "":
		return request{}, false
	case req.on != "" && (strings.HasPrefix(req.on, "-") || strings.ContainsAny(req.on, " \t\r\n:")):
		return request{}, false
	}
	if words == 1 {
		req.tag = rest[0]
	}
	return req, true
}

// prefixElement is one folder name of a prefix: a module-path element, lower case, digits, - . and _.
var prefixElement = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// name checks the version, and with a prefix the prefix, and sets req.tag to the tag's name: the version, or
// <prefix>/<version>. The version may be given with the prefix already on it.
func (r *request) name() error {
	v := r.tag
	if r.prefix != "" {
		for _, e := range strings.Split(r.prefix, "/") {
			if !prefixElement.MatchString(e) || strings.Contains(e, "..") || strings.HasSuffix(e, ".") {
				return refuse("%q is no prefix: it is folder names of lower case, digits, - . and _, joined by /, with no .. and no / at either end", r.prefix)
			}
		}
		v = strings.TrimPrefix(v, r.prefix+"/")
	}
	if !tagForm.MatchString(v) {
		return refuse("no version to release: %q is not a release tag (v, then a semantic version); batch one and commit the changelog", v)
	}
	if r.prefix != "" {
		v = r.prefix + "/" + v
	}
	r.tag = v
	return nil
}

// guard asks git in one repository.
type guard struct {
	ctx    context.Context
	git    gitrun.Git
	req    request
	head   string
	branch string
}

// run runs git in the repository and answers its standard output and its exit code; err is a git that did
// not run at all, or was ended.
func (g *guard) run(args ...string) (string, int, error) {
	res, err := g.git.Run(g.ctx, g.req.dir, args...)
	return string(res.Stdout), res.Code, err
}

// remote is run for the git verbs that read the remote: the caller's environment, with its sign-in.
func (g *guard) remote(args ...string) (string, int, error) {
	res, err := g.git.Remote(g.ctx, g.req.dir, args...)
	return string(res.Stdout), res.Code, err
}

// ok is whether git ran and exited 0.
func (g *guard) ok(args ...string) bool {
	_, code, err := g.run(args...)
	return err == nil && code == 0
}

// line is the first line git printed, when it ran and exited 0.
func (g *guard) line(args ...string) (string, bool) {
	out, code, err := g.run(args...)
	if err != nil || code != 0 {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func (g *guard) step() (string, error) {
	status, code, err := g.run("status", "--porcelain", "--untracked-files=normal")
	if err != nil || code != 0 {
		return "", refuse("git status failed")
	}
	if n := countLines(status); n > 0 {
		return "", refuse("the tree is not clean, %d paths: commit or remove what git status lists, so the release is the commit", n)
	}
	head, ok := g.line("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if !ok {
		return "", refuse("HEAD names no commit")
	}
	g.head = head
	g.branch, _ = g.line("symbolic-ref", "-q", "--short", "HEAD")
	switch g.req.mode {
	case snapshot:
		return g.snapshot()
	case before:
		return g.before()
	}
	return g.tagged()
}

func (g *guard) snapshot() (string, error) {
	if g.branch != "" {
		tip, ok, err := g.onRemote("refs/heads/" + g.branch)
		if err != nil {
			return "", refuse("%s could not be read, so the branch was not compared", g.req.remote)
		}
		if ok {
			if err := g.sameAsRemote(tip); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s at %s, clean, and %s's %s", g.branch, g.head, g.req.remote, g.branch), nil
		}
	}
	name := g.branch
	if name == "" {
		name = "a detached HEAD"
	}
	return fmt.Sprintf("%s at %s, clean; %s holds no such branch, so none was compared", name, g.head, g.req.remote), nil
}

func (g *guard) before() (string, error) {
	v, main := g.req.tag, g.req.branch
	if g.branch != main {
		name := g.branch
		if name == "" {
			name = "detached"
		}
		return "", refuse("HEAD is %s, not %s: a release is tagged on %s", name, main, main)
	}
	tip, ok, err := g.onRemote("refs/heads/" + main)
	if err != nil || !ok {
		return "", refuse("%s has no %s, or it could not be read", g.req.remote, main)
	}
	if err := g.sameAsRemote(tip); err != nil {
		return "", err
	}
	if g.ok("rev-parse", "-q", "--verify", "refs/tags/"+v) {
		return "", refuse("the tag %s exists here: it was released, so batch the next version", v)
	}
	// ls-remote --exit-code exits 2 for no matching ref; 0 is the tag found, and anything else a remote it
	// could not read.
	_, code, err := g.remote("ls-remote", "--exit-code", "--tags", g.req.remote, "refs/tags/"+v)
	switch {
	case err == nil && code == 0:
		return "", refuse("the tag %s exists on %s: it was released, so batch the next version", v, g.req.remote)
	case err != nil || code != 2:
		return "", refuse("%s's tags could not be read", g.req.remote)
	}
	return fmt.Sprintf("%s at %s, clean, and %s's %s; %s is not a tag yet", main, g.head, g.req.remote, main, v), nil
}

func (g *guard) tagged() (string, error) {
	v := g.req.tag
	if kind, ok := g.line("cat-file", "-t", "refs/tags/"+v); !ok || kind != "tag" {
		return "", refuse("%s is no annotated tag here: a release is published from the tag a person made with git tag -a", v)
	}
	if at, ok := g.line("rev-parse", "refs/tags/"+v+"^{commit}"); !ok || at != g.head {
		return "", refuse("%s is not HEAD: check out the tag, then publish", v)
	}
	there, ok, err := g.onRemote("refs/tags/" + v + "^{}")
	if err != nil || !ok {
		return "", refuse("%s has no tag %s: push the tag, then publish", g.req.remote, v)
	}
	if there != g.head {
		return "", refuse("%s's %s is %s, not HEAD %s", g.req.remote, v, there, g.head)
	}
	branch, err := g.onBranch(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s at %s, annotated, clean, the same on %s and on %s's %s", v, g.head, g.req.remote, g.req.remote, branch), nil
}

// onBranch is the branch the tagged commit is an ancestor of, the --on branch or the remote's default one, as the
// remote holds it now; a commit that is on neither is refused by the branch's name.
func (g *guard) onBranch(v string) (string, error) {
	name := g.req.on
	if name == "" {
		var ok bool
		if name, ok = g.defaultBranch(); !ok {
			return "", refuse("%s's default branch could not be read, so %s was not held to it: name the branch with --on", g.req.remote, v)
		}
	}
	tip, ok, err := g.onRemote("refs/heads/" + name)
	if err != nil || !ok {
		return "", refuse("%s has no branch %s, or it could not be read", g.req.remote, name)
	}
	if !g.ok("cat-file", "-e", tip+"^{commit}") {
		return "", refuse("%s's %s is %s, which is not here: fetch, then publish", g.req.remote, name, tip)
	}
	if !g.ok("merge-base", "--is-ancestor", g.head, tip) {
		return "", refuse("%s is %s, which is not on %s's %s (%s): a release is tagged on the branch", v, g.head, g.req.remote, name, tip)
	}
	return name, nil
}

// defaultBranch is the name of the remote's default branch: refs/remotes/<remote>/HEAD, or what git ls-remote
// --symref says HEAD points at.
func (g *guard) defaultBranch() (string, bool) {
	if ref, ok := g.line("symbolic-ref", "-q", "refs/remotes/"+g.req.remote+"/HEAD"); ok {
		if name, found := strings.CutPrefix(ref, "refs/remotes/"+g.req.remote+"/"); found && name != "" {
			return name, true
		}
	}
	out, code, err := g.remote("ls-remote", "--symref", g.req.remote, "HEAD")
	if err != nil || code != 0 {
		return "", false
	}
	for l := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimRight(l, "\r"), "ref: refs/heads/"); ok {
			if name, _, ok := strings.Cut(rest, "\t"); ok && name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// onRemote answers the commit the remote's ref names, and false when it has none. ls-remote --exit-code exits
// 2 for no matching ref: any other failure is an error, a remote that could not be read.
// ls-remote matches a pattern by its tail, so refs/tags/v1^{} also lists a decoy x/refs/tags/v1^{}; only the
// line whose ref is ref itself, and exactly one, answers.
func (g *guard) onRemote(ref string) (string, bool, error) {
	out, code, err := g.remote("ls-remote", "--exit-code", g.req.remote, ref)
	switch {
	case err != nil:
		return "", false, err
	case code == 2:
		return "", false, nil
	case code != 0:
		return "", false, fmt.Errorf("git ls-remote exited %d", code)
	}
	var found []string
	for l := range strings.SplitSeq(out, "\n") {
		if sha, name, ok := strings.Cut(strings.TrimRight(l, "\r"), "\t"); ok && name == ref {
			found = append(found, sha)
		}
	}
	if len(found) != 1 || found[0] == "" {
		return "", false, nil
	}
	return found[0], true, nil
}

// sameAsRemote refuses a branch that is behind, ahead of, or apart from its remote's: the release is tagged on
// the commit the remote holds.
func (g *guard) sameAsRemote(tip string) error {
	if tip == g.head {
		return nil
	}
	b, r := g.branch, g.req.remote
	if g.ok("cat-file", "-e", tip+"^{commit}") && g.ok("merge-base", "--is-ancestor", g.head, tip) {
		return refuse("%s is behind %s's %s: HEAD is %s and %s has %s; pull, then run the release again", b, r, b, g.head, r, tip)
	}
	if g.ok("merge-base", "--is-ancestor", tip, g.head) {
		return refuse("%s is ahead of %s's %s: HEAD is %s and %s has %s; land it first", b, r, b, g.head, r, tip)
	}
	return refuse("%s is not %s's %s: HEAD is %s and %s has %s; fetch and pull", b, r, b, g.head, r, tip)
}

// countLines is how many lines of out are not empty.
func countLines(out string) int {
	n := 0
	for l := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

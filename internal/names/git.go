package names

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/birdtable/hatch/internal/gitrun"
)

// scanner is the git a scan runs.
type scanner struct {
	git gitrun.Git
}

// want is the surfaces of a repository a scan was asked for: the tree, the
// branch, and, when base is not "", the commits past it.
type want struct {
	tree, branch bool
	base         string
}

// read reads the surfaces w names from the repository dir lies in. It fails
// closed: a folder in no work tree, a tree of no tracked file, a file that
// cannot be read, a HEAD on no branch when the branch is asked for, a base
// that is no commit of the clone and a log git's own count does not match
// are each an error, never an empty read.
func (s scanner) read(ctx context.Context, dir string, w want) (taken, error) {
	var t taken
	top, err := s.git.Top(ctx, dir)
	if err != nil {
		return t, err
	}
	if w.tree {
		t.tree = true
		if t.paths, err = s.git.TrackedPaths(ctx, top); err != nil {
			return t, err
		}
		if len(t.paths) == 0 {
			return t, fmt.Errorf("%w: git tracks no file here", ErrEmpty)
		}
		t.files = map[string][]byte{}
		for _, p := range t.paths {
			data, ok, err := gitrun.ReadTracked(top, p)
			if err != nil {
				return t, fmt.Errorf("a tracked file cannot be read: %w", err)
			}
			if ok {
				t.files[p] = data
			} else if gitrun.Gone(top, p) {
				t.missing = append(t.missing, p)
			}
		}
	}
	if w.branch {
		if t.branch, err = s.branch(ctx, top); err != nil {
			return t, err
		}
		if t.branch == "" {
			return t, errors.New("HEAD is on no branch, so --branch has nothing to read: give the branch's name on --stdin")
		}
	}
	if w.base != "" {
		if t.commits, t.rewrites, err = s.past(ctx, top, w.base); err != nil {
			return t, err
		}
	}
	return t, nil
}

// branch is the branch HEAD is on in dir, "" when it is on none.
func (s scanner) branch(ctx context.Context, dir string) (string, error) {
	r, err := s.git.Run(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	switch {
	case err != nil:
		return "", fmt.Errorf("git symbolic-ref: %w", err)
	case r.Code == 1:
		return "", nil
	case r.Code != 0:
		return "", &gitrun.ExitError{Verb: "symbolic-ref", Code: r.Code, Stderr: r.Stderr}
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// past reads the commits of dir past base, newest first, each with its
// patch. It refuses a clone without base, and a log whose commits do not
// match git's own count of them. A replace ref, a grafts file or a shallow
// clone is a finding, and no commit is read.
func (s scanner) past(ctx context.Context, dir, base string) ([]commit, []string, error) {
	r, err := s.git.Run(ctx, dir, "rev-parse", "-q", "--verify", base+"^{commit}")
	if err != nil {
		return nil, nil, fmt.Errorf("git rev-parse: %w", err)
	}
	from := strings.TrimSpace(string(r.Stdout))
	if r.Code != 0 || !commitID(from) {
		return nil, nil, errors.New("the base --commits names is no commit of this clone, so what is past it cannot be told: fetch it first")
	}
	found, err := s.rewrites(ctx, dir)
	if err != nil || len(found) > 0 {
		return nil, found, err
	}
	span := from + "..HEAD"
	count, err := s.git.Out(ctx, dir, "rev-list", "--count", span)
	if err != nil {
		return nil, nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return nil, nil, errors.New("git rev-list printed what is not a count of commits")
	}
	log, err := s.git.Out(ctx, dir, "log", "-z", "--no-show-signature", "--no-color", "--format=%H%x1f%ae%x1f%ce%x1f%B", span)
	if err != nil {
		return nil, nil, err
	}
	commits, err := parseLog(log)
	if err != nil {
		return nil, nil, err
	}
	if err := sameCount(len(commits), n); err != nil {
		return nil, nil, err
	}
	for i := range commits {
		if commits[i].patch, err = s.git.Out(ctx, dir, patchArgs(commits[i].id)...); err != nil {
			return nil, nil, err
		}
		names, err := s.git.Out(ctx, dir, writtenArgs(commits[i].id)...)
		if err != nil {
			return nil, nil, err
		}
		commits[i].written = writtenPaths(names)
	}
	return commits, nil, nil
}

// parseLog reads git log -z with the format past asks for: each commit's
// hash, author and committer addresses and message, the fields split by the
// unit separator and each commit ended by a NUL. A record it cannot read is
// refused without its text, which may hold a name.
func parseLog(out string) ([]commit, error) {
	var cs []commit
	for i, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 4)
		if len(f) != 4 || !commitID(f[0]) {
			return nil, fmt.Errorf("git log printed record %d in a shape the scan cannot read", i+1)
		}
		cs = append(cs, commit{id: f[0], author: f[1], committer: f[2], message: f[3]})
	}
	return cs, nil
}

// commitID is whether id is a commit's full id as git prints it: 40 or 64
// lower-case hex digits, for SHA-1 or SHA-256.
func commitID(id string) bool {
	return (len(id) == 40 || len(id) == 64) && strings.Trim(id, "0123456789abcdef") == ""
}

// rewrites is a finding for each way the repository in dir would show git a
// history other than the one a push of it sends: a ref under refs/replace/,
// which git shows in place of the commit it names, a grafts file, which
// gives a commit other parents, and a shallow clone, whose git shows a
// commit it holds without parents it does not. A scan refuses each by name
// before it reads history: that is plainer than undoing them, and a count of
// commits cannot see the shallow cut, as git counts the same cut history. A
// replace ref is named by the commit it replaces; one whose name is not a
// commit's id is not printed, as it may hold a name.
func (s scanner) rewrites(ctx context.Context, dir string) ([]string, error) {
	refs, err := s.git.Out(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/replace/")
	if err != nil {
		return nil, err
	}
	var found []string
	for ref := range strings.FieldsSeq(refs) {
		if !commitID(strings.TrimPrefix(ref, "refs/replace/")) {
			ref = "a ref under refs/replace/ whose name is not a commit's id"
		}
		found = append(found, ref+": git shows another commit in place of the one it names, and the scan refuses it: delete it with git replace -d")
	}
	grafts, err := s.git.Out(ctx, dir, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		return nil, err
	}
	file := filepath.FromSlash(strings.TrimRight(grafts, "\r\n"))
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	switch _, err := os.Lstat(file); {
	case err == nil:
		found = append(found, "info/grafts in the git folder: git gives a commit other parents than it has, and the scan refuses it: delete the file")
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("the git folder's info/grafts cannot be looked for: %w", gitrun.Bare(err))
	}
	shallow, err := s.git.Out(ctx, dir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(shallow) != "false" {
		found = append(found, "the clone is shallow: git shows its oldest commits without their parents, and the scan refuses it: run git fetch --unshallow")
	}
	return found, nil
}

// patchArgs are git's arguments for the patch of the commit id: what it
// adds, as text whatever the attributes say, with no textconv, external
// diff, rename, colour or relative path, against its first parent when it
// is a merge and against nothing when it is a root commit, with every
// submodule entry shown, and with the prefixes and the quoting of a path
// fixed, so no config of the repository's changes what the scan reads.
func patchArgs(id string) []string {
	return []string{"-c", "core.quotePath=false", "log", "-1", "-p", "--format=", "--text", "--no-textconv", "--no-ext-diff", "--no-renames", "--no-relative", "--no-color", "--diff-merges=first-parent", "--root", "--ignore-submodules=none", "--unified=0", "--src-prefix=a/", "--dst-prefix=b/", id}
}

// writtenArgs are git's arguments for the paths the commit id writes, with the
// same choices of parent and root as patchArgs: a status and a path for each
// file, NUL between the parts, so a path is never quoted. The patch has no
// line to read a path from for a file with no line, an empty file among them.
func writtenArgs(id string) []string {
	return []string{"-c", "core.quotePath=false", "log", "-1", "-z", "--name-status", "--format=", "--no-renames", "--no-relative", "--no-color", "--diff-merges=first-parent", "--root", "--ignore-submodules=none", id}
}

// writtenPaths reads what writtenArgs printed: the path of each file the commit
// adds, changes or changes the type of, and none of a file it removes.
func writtenPaths(out string) []string {
	var paths []string
	parts := strings.Split(out, "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		if parts[i] != "D" && parts[i+1] != "" {
			paths = append(paths, parts[i+1])
		}
	}
	return paths
}

// addedRun is one run of lines a patch adds to a file: the file's path, the
// line of the file the run starts on, and the lines.
type addedRun struct {
	path string
	line int
	text string
}

// hunkHead is a hunk's header, and the line of the new file it starts on.
var hunkHead = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

// parsePatch reads a patch patchArgs printed: the path each file it writes
// has, and each run of lines it adds. A file's header runs from its diff
// --git line to its first hunk, so a line it adds that looks like a header
// is read as a line.
func parsePatch(patch string) ([]string, []addedRun) {
	var r patchReader
	for line := range strings.SplitSeq(patch, "\n") {
		r.read(strings.TrimSuffix(line, "\r"))
	}
	r.flush()
	return r.paths, r.runs
}

// patchReader is parsePatch part way: what it has read, the file and the
// run it is in, whether it is in a file's header, and the line of the file
// the next added line lies on.
type patchReader struct {
	paths  []string
	runs   []addedRun
	cur    addedRun
	b      strings.Builder
	open   bool
	header bool
	next   int
}

// read reads one line of the patch.
func (r *patchReader) read(line string) {
	switch {
	case strings.HasPrefix(line, "diff --git "):
		r.flush()
		r.header, r.cur = true, addedRun{}
	case r.header && strings.HasPrefix(line, "@@"):
		r.flush()
		r.header = false
		if m := hunkHead.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				r.next = n
			}
		}
	case r.header:
		if p, ok := patchPath(line); ok {
			r.cur.path = p
			r.paths = append(r.paths, p)
		}
	case strings.HasPrefix(line, "+"):
		r.add(line[1:])
	case strings.HasPrefix(line, "@@"):
		r.flush()
		if m := hunkHead.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				r.next = n
			}
		}
	default:
		r.flush()
		if strings.HasPrefix(line, " ") {
			r.next++
		}
	}
}

// add adds one line to the run of added lines.
func (r *patchReader) add(text string) {
	if !r.open {
		r.open, r.cur.line = true, r.next
	} else {
		r.b.WriteByte('\n')
	}
	r.b.WriteString(text)
	r.next++
}

// flush ends the run of added lines, if one is open.
func (r *patchReader) flush() {
	if r.open {
		r.cur.text = r.b.String()
		r.runs = append(r.runs, r.cur)
		r.b.Reset()
		r.open = false
	}
}

// patchPath is the path a header line of a file names as the one written,
// unquoted when git quoted it: false for any other line, and for a file the
// commit removes.
func patchPath(line string) (string, bool) {
	p, ok := strings.CutPrefix(line, "+++ ")
	if !ok || p == "/dev/null" {
		return "", false
	}
	p = strings.TrimSuffix(p, "\t")
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	return strings.TrimPrefix(p, "b/"), true
}

// sameCount is an error when the log read got commits and git's own count
// says n: the scan then did not read the history git counts.
func sameCount(got, n int) error {
	if got != n {
		return fmt.Errorf("git log printed %d commits past the base, and git rev-list counts %d", got, n)
	}
	return nil
}

// Package secrets runs the caller's gitleaks over a repository's history, over
// the commits of a range, or over the staged change, and is green only when
// gitleaks ran, scanned what was asked and found nothing.
//
// Built on: the standard library, internal/gitrun (every git call, through
// go-proc's proc.Run with gitenv.Env and gitenv.ReadArgs) and go-proc's
// proc.Run, which starts gitleaks.
// Adds: the guards gitleaks leaves to its caller. gitleaks v8.30.1 exits 0
// on a repository with no commit, and on one whose config sets format.pretty,
// where it reads no commit; both are red here, as is a count it does not
// print, prints twice or prints as 0. For a range it holds gitleaks's count to
// git's: gitleaks v8.30.1 counts the commits that add text, so the count to meet
// is the commits of git log --text -p --diff-merges=first-parent <range> with a
// line added, merges among them, and an empty range is red. A staged change
// is that range of one temporary commit of the index on HEAD (an object, no ref,
// index or work tree moved), so it has every guard of a range.
// Two things hid a secret from every mode, and are shut. The branch's own
// .gitattributes could mark a file -diff or binary, and git then prints it as
// binary and gitleaks reads nothing of it: every git call, gitleaks's own
// included, reads its attributes from the empty tree (GIT_ATTR_SOURCE, git 2.42
// or later; an older git is refused by name, never run without it). And a
// secret written only in a merge's resolution is in no parent's diff: each
// merge's own diff, against its first parent, is put before gitleaks with
// --diff-merges=first-parent, and merges are in both counts. A third hid a
// file git judges binary, as one NUL byte makes it: git prints no line of it,
// and gitleaks reads nothing. Binary content is read as text: --text is in the
// log options gitleaks gets and the count's git call, and a staged change is
// read as a range, since gitleaks's own staged mode takes no option and reads
// no binary file. A clean image is green, as any file is.
// Configured by: the caller. The gitleaks is the caller's pinned binary: the
// one --gitleaks names, or "gitleaks" on PATH, with no other default and no
// fetch. Its config is the caller's --config.
package secrets

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/birdtable/go-proc/gitenv"
	"github.com/birdtable/go-proc/proc"

	"github.com/birdtable/hatch/internal/gitrun"
)

const (
	exitGreen  = 0
	exitRed    = 1
	exitUsage  = 2
	exitNoGit  = 3
	maxOutput  = 64 << 20
	gitTimeout = 2 * time.Minute
)

const usage = `usage: hatch secrets [--range <base>..<head> | --staged] [--gitleaks <path>] [--config <file>] [--dir <folder>] [--timeout <duration>]

--range     scan only the commits of <base>..<head>, and be red unless gitleaks scanned as many commits as
            git counts in it (a commit that adds no text is in neither count; a merge is, by its diff
            against its first parent), or the range holds none
--staged    scan only the staged change, as a range of one temporary commit of the index on HEAD (an
            object left for git gc; no ref, index or work tree moves), under the rules of --range
--gitleaks  the caller's pinned gitleaks: a path, or a name looked up on PATH (default gitleaks);
            hatch does not fetch it
--config    a gitleaks config, passed to it as --config
--dir       a folder of the repository to scan (default .)
--timeout   how long gitleaks may run (default 10m)

The attributes of the work tree and its branch are not read, so a .gitattributes cannot hide a file
(git 2.42 or later is required: an older git is red by name). A merge is scanned by its own diff
against its first parent, so a secret written only in a merge's resolution is read.

A file git calls binary is read as text, as is any other, so a secret in an image or after a NUL
byte is found.

It runs gitleaks over the whole history (or what --range or --staged names), with --redact, and is
green only when gitleaks ran, scanned at least one commit (or the commits of the range, or the
staged change's one) and found nothing. Red, with its reason on stderr: git reports no commit; the
repository's config sets format.pretty (gitleaks then reads no commit and exits 0); gitleaks is not
found or does not start; gitleaks exits with anything but 0; gitleaks logs an error; its output does
not hold one count of commits scanned; the count is 0, or for a range is not git's. Nothing a finding holds is printed beyond what
gitleaks prints with --redact.

exit codes of hatch secrets:
  0  green: gitleaks scanned at least one commit and found nothing
  1  red, with its reason on stderr
  2  a usage error (--range and --staged together, or a --range that is not <base>..<head>)
  3  git not found on PATH
`

var (
	countLine = regexp.MustCompile(`(?m)\b([0-9]+) commits? scanned`)
	errLine   = regexp.MustCompile(`(?m)\bERR\b`)
	rangeForm = regexp.MustCompile(`^[^\s-][^\s]*\.\.[^\s.][^\s]*$`)
)

// Run runs hatch secrets and returns its exit code. It fails closed: a result
// it cannot read is red, never green.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("secrets", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var gitleaks, config, dir, rng string
	var staged bool
	timeout := 10 * time.Minute
	fl.StringVar(&gitleaks, "gitleaks", "gitleaks", "")
	fl.StringVar(&config, "config", "", "")
	fl.StringVar(&dir, "dir", ".", "")
	fl.StringVar(&rng, "range", "", "")
	fl.BoolVar(&staged, "staged", false, "")
	fl.DurationVar(&timeout, "timeout", timeout, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 || gitleaks == "" || timeout <= 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	ranged := false
	fl.Visit(func(f *flag.Flag) { ranged = ranged || f.Name == "range" })
	if ranged && (staged || strings.Contains(rng, "...") || !rangeForm.MatchString(rng)) {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	red := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "hatch secrets: red: "+format+"\n", a...)
		return exitRed
	}
	gp, err := gitrun.Find()
	if err != nil {
		fmt.Fprintf(stderr, "hatch secrets: %v\n", err)
		return exitNoGit
	}
	g := gitrun.Git{Path: gp, Timeout: gitTimeout, MaxOutput: 1 << 20}
	top, err := g.Top(ctx, dir)
	if err != nil {
		return red("the folder is not in a git work tree, or git could not read it: %v", gitrun.Bare(err))
	}
	if staged {
		if reason := formatPretty(ctx, g, top); reason != "" {
			return red("%s", reason)
		}
	} else if reason := history(ctx, g, top); reason != "" {
		return red("%s", reason)
	}
	tree, err := attrTree(ctx, g, top)
	if err != nil {
		return red("%v", err)
	}
	if staged {
		// The staged change is the diff of the index against HEAD: a commit
		// of the index's tree on HEAD, made as an object and no more, is that
		// diff as a range of one, which every guard of a range then holds.
		var reason string
		if rng, reason = stagedCommit(ctx, g, top); reason != "" {
			return red("%s", reason)
		}
		ranged = true
	}
	want := 0
	if ranged {
		n, err := rangeCount(ctx, g, top, tree, rng)
		if err != nil {
			return red("%v", err)
		}
		if n < 1 && staged {
			return red("nothing is staged that adds text, so there is nothing to scan")
		}
		if n < 1 {
			return red("the range %s holds no commit that adds text, so there is nothing to scan", rng)
		}
		want = n
	}
	bin, err := find(gitleaks)
	if err != nil {
		return red("%v", err)
	}
	glArgs := []string{"git", "--no-banner", "--no-color", "--redact", "--verbose", "--exit-code", "1"}
	if config != "" {
		abs, err := filepath.Abs(config)
		if err != nil {
			return red("the config path cannot be resolved: %v", gitrun.Bare(err))
		}
		glArgs = append(glArgs, "--config", abs)
	}
	if ranged {
		glArgs = append(glArgs, "--log-opts="+noFilters+" "+binaryAsText+" "+mergeDiffs+" "+rng, ".")
	} else {
		glArgs = append(glArgs, "--log-opts=--full-history --all "+noFilters+" "+binaryAsText+" "+mergeDiffs, ".")
	}
	res, err := proc.Run(ctx, proc.Cmd{
		Path:      bin,
		Args:      glArgs,
		Dir:       top,
		Env:       gitenv.Env(proc.Bare(), "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_SOURCE="+tree),
		Timeout:   timeout,
		MaxOutput: maxOutput,
		// Windows refuses a run of no Drain: it bounds each read of the output
		// after the kill, and gitleaks's own git is waited for.
		Drain: 2 * time.Second,
	})
	// What gitleaks printed goes through as it is: --redact is its own.
	_, _ = stdout.Write(res.Stdout)
	_, _ = stderr.Write(res.Stderr)
	if err != nil {
		return red("gitleaks did not run to its end: %v", gitrun.Bare(err))
	}
	if res.Code != 0 {
		return red("gitleaks exited %d: it found a secret, or failed", res.Code)
	}
	if errLine.Match(res.Stderr) || errLine.Match(res.Stdout) {
		return red("gitleaks logged an error and exited 0, so what it read cannot be trusted")
	}
	counts := countLine.FindAllSubmatch(append(append([]byte{}, res.Stderr...), res.Stdout...), -1)
	if len(counts) != 1 {
		return red("gitleaks's output holds %d counts of commits scanned, not one, so the count cannot be read", len(counts))
	}
	n, err := strconv.Atoi(string(counts[0][1]))
	if err != nil {
		return red("gitleaks's count of commits scanned cannot be read")
	}
	if n < 1 {
		return red("gitleaks scanned 0 commits")
	}
	if ranged && n != want {
		return red("gitleaks scanned %d commits of %s and git counts %d, so a commit went unread", n, rng, want)
	}
	if staged {
		fmt.Fprintln(stdout, "hatch secrets: green, the staged change scanned, no secret found")
		return exitGreen
	}
	fmt.Fprintf(stdout, "hatch secrets: green, %d commits scanned, no secret found\n", n)
	return exitGreen
}

// stagedCommit is the range of a commit of the index's tree on HEAD (alone,
// when there is no HEAD), as its object and no more: no ref, no index and no
// work tree moves, and the object is left for git gc. A reason is why it
// cannot be made, an unmerged index among them.
func stagedCommit(ctx context.Context, g gitrun.Git, top string) (string, string) {
	head := ""
	if res, err := g.Run(ctx, top, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return "", "git could not read HEAD: " + gitrun.Bare(err).Error()
	} else if res.Code == 0 {
		head = strings.TrimSpace(string(res.Stdout))
	}
	// write-tree is the one call that reads the index the caller names: a
	// hook of git commit -a or git commit <path> is handed another file than
	// .git/index. It takes that file's lock and writes its cache tree back.
	res, err := g.RunIndex(ctx, top, callerIndex(ctx, g, top), "write-tree")
	if err != nil {
		return "", "git could not write the index as a tree: " + gitrun.Bare(err).Error()
	}
	if res.Code != 0 {
		if bytes.Contains(res.Stderr, []byte("lock")) {
			return "", "git could not write the index as a tree because its lock is held by another git (is GIT_INDEX_FILE the index being committed?), so the staged change cannot be read"
		}
		return "", "git could not write the index as a tree (an unmerged path?), so the staged change cannot be read"
	}
	args := []string{"-c", "user.name=hatch", "-c", "user.email=hatch@localhost", "-c", "commit.gpgsign=false", "commit-tree", strings.TrimSpace(string(res.Stdout)), "-m", "hatch secrets: the staged change"}
	if head != "" {
		args = append(args, "-p", head)
	}
	res, err = g.Run(ctx, top, args...)
	if err != nil {
		return "", "git could not make the commit of the staged change: " + gitrun.Bare(err).Error()
	}
	if res.Code != 0 {
		return "", "git could not make the commit of the staged change"
	}
	c := strings.TrimSpace(string(res.Stdout))
	if head == "" {
		return c, ""
	}
	return head + ".." + c, ""
}

// callerIndex is the caller's GIT_INDEX_FILE, made absolute, when it is a file
// directly in the git folder of the work tree being scanned, and "" otherwise.
// Every index git hands a hook (index, index.lock, next-index-<pid>.lock) is
// there, a linked work tree's own folder included. A hook of another repository,
// or of another work tree of this one, hands its index to a run on this folder
// with --dir, and that is not this work tree's staged change: write-tree on it
// would read what is not here, so it is dropped, as it was before.
func callerIndex(ctx context.Context, g gitrun.Git, top string) string {
	idx := os.Getenv("GIT_INDEX_FILE")
	if idx == "" {
		return ""
	}
	abs, err := filepath.Abs(idx)
	if err != nil {
		return ""
	}
	file, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	res, err := g.Run(ctx, top, "rev-parse", "--absolute-git-dir")
	if err != nil || res.Code != 0 {
		return ""
	}
	folder, err := filepath.EvalSymlinks(strings.TrimSpace(string(res.Stdout)))
	if err != nil || filepath.Dir(file) != folder {
		return ""
	}
	return abs
}

// rangeCount is how many commits of the range gitleaks v8.30.1 will scan: the
// commits that add text, a merge by its diff against its first parent. A commit that only deletes, renames or
// changes the mode of a file, adds a binary file or adds an empty file adds
// none, and gitleaks does not count it. The patch says it, as gitleaks reads it: a
// commit with a line added in a hunk, every file as text (--numstat prints - for
// a binary file even under --text, so it cannot say), and the attributes are the
// empty tree's, so none turns a file's diff off. A gitleaks bump
// can move this set, and the real-gitleaks test shows it.
func rangeCount(ctx context.Context, g gitrun.Git, top, tree, rng string) (int, error) {
	g.MaxOutput = maxOutput
	res, err := g.Run(ctx, top, "--attr-source="+tree, "log", binaryAsText, mergeDiffs, "-p", "-U0", "--no-color", "--no-ext-diff", "--no-textconv", "--format=commit %H", rng)
	if err != nil {
		return 0, fmt.Errorf("git could not count the commits of the range: %w", gitrun.Bare(err))
	}
	if res.Code != 0 {
		return 0, errors.New("git could not read the range: name <base>..<head> as git knows them")
	}
	// A line of a hunk starts with +, - or a space (-U0: no space); a header
	// line, the format's commit line and a hunk's own @@ line start with none.
	n, adds, hunk := 0, false, false
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		switch {
		case strings.HasPrefix(line, "commit "):
			adds, hunk = false, false
		case strings.HasPrefix(line, "diff --git "):
			hunk = false
		case strings.HasPrefix(line, "@@"):
			hunk = true
		case hunk && strings.HasPrefix(line, "+") && !adds:
			adds = true
			n++
		}
	}
	return n, nil
}

// history is why the repository's history cannot be scanned, or "": git
// reports no commit, or its config sets format.pretty.
func history(ctx context.Context, g gitrun.Git, top string) string {
	res, err := g.Run(ctx, top, "rev-list", "--count", "HEAD")
	if err != nil {
		return "git could not count the commits: " + gitrun.Bare(err).Error()
	}
	if res.Code != 0 {
		return "git reports no commit in this repository"
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout))); err != nil || n < 1 {
		return "git reports no commit in this repository"
	}
	return formatPretty(ctx, g, top)
}

// formatPretty is why gitleaks would read no commit of the repository, or "":
// the config sets format.pretty.
func formatPretty(ctx context.Context, g gitrun.Git, top string) string {
	// format.pretty set: gitleaks reads git's log in that format, reads no
	// commit, and exits 0. git config exits 1 when the key is unset.
	cfg, err := g.Run(ctx, top, "config", "--get-all", "format.pretty")
	if err != nil {
		return "git could not read its configuration: " + gitrun.Bare(err).Error()
	}
	switch cfg.Code {
	case 1:
		return ""
	case 0:
		return "the repository's config sets format.pretty, which makes gitleaks v8.30.1 read no commit and exit 0: unset it"
	default:
		return "git could not read its configuration, so format.pretty cannot be ruled out"
	}
}

// find is the gitleaks the caller names: a path as it is, or a name looked up
// on PATH. It is never fetched and has no other default.
func find(name string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", fmt.Errorf("gitleaks's path cannot be resolved: %w", gitrun.Bare(err))
		}
		return abs, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%s not found on PATH: name the caller's pinned gitleaks with --gitleaks", name)
		}
		return "", fmt.Errorf("%s cannot be found: %w", name, err)
	}
	return filepath.Abs(p)
}

// mergeDiffs is the log option that shows a merge's own diff, against its
// first parent: what the merge adds that main did not hold. A secret written
// only in the resolution is in it, and so is what the side branch brought,
// which its own commits showed already. --cc would show the resolution alone,
// in a combined format gitleaks does not read.
const mergeDiffs = "--diff-merges=first-parent"

// noFilters is what keeps a repository's own diff.external and textconv driver
// from changing the text gitleaks reads: its own git log -p passes neither.
const noFilters = "--no-ext-diff --no-textconv"

// binaryAsText makes git print the diff of a file it judges binary, as one NUL
// byte does, as it would a text file's: without it gitleaks reads nothing of
// the file and --numstat prints - for it.
const binaryAsText = "--text"

// Git's empty trees: the attributes of a tree that holds no file.
const (
	emptyTreeSHA1   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	emptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

// attrTree is the empty tree's id in the repository's object format, the
// source every git call reads its attributes from, or an error naming why
// there is none: a git older than 2.42 has no GIT_ATTR_SOURCE, and a scan
// without it would read the branch's own .gitattributes.
func attrTree(ctx context.Context, g gitrun.Git, top string) (string, error) {
	v, err := g.Out(ctx, top, "--version")
	if err != nil {
		return "", fmt.Errorf("git's version cannot be read: %w", gitrun.Bare(err))
	}
	if err := checkGitVersion(v); err != nil {
		return "", err
	}
	f, err := g.Out(ctx, top, "rev-parse", "--show-object-format")
	if err != nil {
		return "", fmt.Errorf("git's object format cannot be read: %w", gitrun.Bare(err))
	}
	switch strings.TrimSpace(f) {
	case "sha1":
		return emptyTreeSHA1, nil
	case "sha256":
		return emptyTreeSHA256, nil
	}
	return "", errors.New("git's object format is neither sha1 nor sha256, so its empty tree is not known")
}

var versionLine = regexp.MustCompile(`^git version ([0-9]+)\.([0-9]+)`)

// checkGitVersion is an error unless the line git --version printed names 2.42
// or later, the first git whose GIT_ATTR_SOURCE this scan needs.
func checkGitVersion(line string) error {
	m := versionLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return errors.New("git's version cannot be read, so GIT_ATTR_SOURCE cannot be relied on")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || (major == 2 && minor < 42) {
		return fmt.Errorf("git %d.%d is older than 2.42 and has no GIT_ATTR_SOURCE, so a .gitattributes of the branch could hide a file from gitleaks: use git 2.42 or later", major, minor)
	}
	return nil
}

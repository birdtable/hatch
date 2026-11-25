package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

// recordRun runs hatch secrets with the stand-in gitleaks in its recording mode
// (one commit scanned) and returns the exit code, hatch's stderr, and the
// arguments the stand-in was started with. These rows hold the guards that
// reach gitleaks as an argument, or reach it as the commit hatch makes of the
// staged index, with no real gitleaks: the push tier runs them.
func recordRun(t *testing.T, dir string, args ...string) (int, string, []string) {
	t.Helper()
	code, errb, argv, _ := recordRunEnv(t, dir, args...)
	return code, errb, argv
}

// recordRunEnv is recordRun with the GIT_NO_REPLACE_OBJECTS the stand-in was
// started with, as "GIT_NO_REPLACE_OBJECTS=<value>".
func recordRunEnv(t *testing.T, dir string, args ...string) (int, string, []string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argv")
	full := append([]string{"--gitleaks", self(t), "--config", config(t, "record "+path), "--dir", dir}, args...)
	code, _, errb := runSecrets(t, full...)
	data, err := os.ReadFile(path)
	if err != nil {
		return code, errb, nil, ""
	}
	env, _ := os.ReadFile(path + ".env")
	return code, errb, strings.Split(strings.TrimSpace(string(data)), "\n"), strings.TrimSpace(string(env))
}

// logOpts is the --log-opts value of what the stand-in was started with.
func logOpts(argv []string) string {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--log-opts="); ok {
			return v
		}
	}
	return ""
}

// stagedTree is the files of the commit a staged run scanned, the end of the
// range the stand-in was given.
func stagedTree(t *testing.T, dir string, argv []string) string {
	t.Helper()
	opts := strings.Fields(logOpts(argv))
	if len(opts) == 0 {
		t.Fatalf("no log options were recorded: %v", argv)
	}
	_, c, ok := strings.Cut(opts[len(opts)-1], "..")
	if !ok {
		t.Fatalf("the last log option is not a range: %v", opts)
	}
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	res, err := g.Run(context.Background(), dir, "ls-tree", "-r", "--name-only", c)
	if err != nil || res.Code != 0 {
		t.Fatalf("git ls-tree of the staged commit failed: %v\n%s", err, res.Stderr)
	}
	return string(res.Stdout)
}

// The log options hatch hands gitleaks, in each of the three modes, hold the
// four guards that stand between a repository's own settings and what
// gitleaks reads: no external diff driver and no textconv, every file as
// text, and a merge by its diff against its first parent. A stand-in that
// records its arguments sees them; the real gitleaks rows hold what they do
// to a planted secret, in the release tier.
//
// What it costs when this fails: a driver of the repository hides a token, a
// file with one NUL byte is read as empty, or a secret written only in a
// merge is not read, and a push tier on the stand-in is green.
func TestTheLogOptionsKeepEachGuard(t *testing.T) {
	t.Parallel()
	guards := []string{"--no-ext-diff", "--no-textconv", "--text", "--diff-merges=first-parent"}
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	commit(t, dir, "two", map[string]string{"b.txt": "two\n"})
	write(t, dir, "c.txt", "three\n")
	gitT(t, dir, "add", "c.txt")
	for mode, args := range map[string][]string{"range": {"--range", "HEAD~1..HEAD"}, "history": nil, "staged": {"--staged"}} {
		code, errb, argv := recordRun(t, dir, args...)
		if code != 0 {
			t.Errorf("%s: exit %d, want green:\n%s", mode, code, errb)
			continue
		}
		opts := logOpts(argv)
		for _, g := range guards {
			if !strings.Contains(" "+opts+" ", " "+g+" ") {
				t.Errorf("%s: the log options %q lack %s", mode, opts, g)
			}
		}
	}
}

// gitleaks is started with GIT_NO_REPLACE_OBJECTS=1 in each of the three
// modes, so a git replace ref of the repository cannot swap a commit that holds
// a token for a clean one. A stand-in that records its environment sees it; the
// real gitleaks row holds what it does to a replaced commit, in the release tier.
//
// What it costs when this fails: a replace ref hides a committed token from the
// range and history modes, and a push tier on the stand-in is green.
func TestGitleaksIsStartedWithNoReplaceObjects(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	commit(t, dir, "two", map[string]string{"b.txt": "two\n"})
	write(t, dir, "c.txt", "three\n")
	gitT(t, dir, "add", "c.txt")
	for mode, args := range map[string][]string{"range": {"--range", "HEAD~1..HEAD"}, "history": nil, "staged": {"--staged"}} {
		code, errb, _, env := recordRunEnv(t, dir, args...)
		if code != 0 {
			t.Errorf("%s: exit %d, want green:\n%s", mode, code, errb)
			continue
		}
		if env != "GIT_NO_REPLACE_OBJECTS=1" {
			t.Errorf("%s: gitleaks was started with %q, want GIT_NO_REPLACE_OBJECTS=1", mode, env)
		}
	}
}

// A hook of git commit -a or git commit <path> is handed another index than
// .git/index, and the staged commit is made of that one: a file staged only
// there is in the commit hatch scans.
//
// What it costs when this fails: a commit -a of a token is scanned from the
// index that lacks it, and passes.
func TestTheStagedCommitIsMadeOfTheIndexItIsGiven(t *testing.T) {
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, dir, "b.txt", "two\n")
	gitT(t, dir, "add", "b.txt")
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ".git"), "other-index", string(data))
	gitT(t, dir, "reset", "-q")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(dir, ".git", "other-index"))
	code, errb, argv := recordRun(t, dir, "--staged")
	if code != 0 {
		t.Fatalf("a staged change in the index given: exit %d, want green:\n%s", code, errb)
	}
	if tree := stagedTree(t, dir, argv); !strings.Contains(tree, "b.txt") {
		t.Errorf("the staged commit does not hold b.txt, staged in the index given:\n%s", tree)
	}
}

// The index a hook is handed is used only when it is a file of the repository
// scanned: another repository's is not, and the repository's own staged
// change is what is read.
//
// What it costs when this fails: a hook of a clone has its index read for a
// repository it only names, so what is staged in the one scanned is not.
func TestTheIndexOfAnotherRepositoryIsDroppedAndTheOwnIsRead(t *testing.T) {
	x := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, x, "k.txt", "own staged\n")
	gitT(t, x, "add", "k.txt")
	y := t.TempDir()
	gitT(t, y, "clone", "-q", x, "y")
	yy := filepath.Join(y, "y")
	write(t, yy, "b.txt", "clean text\n")
	gitT(t, yy, "add", "b.txt")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(yy, ".git", "index"))
	code, errb, argv := recordRun(t, x, "--staged")
	if code != 0 {
		t.Fatalf("another repository's index: exit %d, want green on its own staged change:\n%s", code, errb)
	}
	tree := stagedTree(t, x, argv)
	if !strings.Contains(tree, "k.txt") || strings.Contains(tree, "b.txt") {
		t.Errorf("the staged commit is not the scanned repository's own change:\n%s", tree)
	}
}

// The index of the main work tree is not used for a scan of a linked one: only
// a file directly in the scanned work tree's own git folder is, and the folder
// the two share is not that folder.
//
// What it costs when this fails: a hook of a linked work tree has the main work
// tree's index read for it, so what is staged in the linked one is not read, and
// a secret in it passes.
func TestTheIndexOfTheMainWorkTreeIsDroppedForALinkedOne(t *testing.T) {
	x := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, x, "m.txt", "main staged\n")
	gitT(t, x, "add", "m.txt")
	w := filepath.Join(t.TempDir(), "w")
	gitT(t, x, "worktree", "add", "-q", "-b", "linked", w)
	write(t, w, "l.txt", "linked staged\n")
	gitT(t, w, "add", "l.txt")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(x, ".git", "index"))
	code, errb, argv := recordRun(t, w, "--staged")
	if code != 0 {
		t.Fatalf("the main work tree's index, on a linked one: exit %d, want green on its own staged change:\n%s", code, errb)
	}
	tree := stagedTree(t, w, argv)
	if !strings.Contains(tree, "l.txt") || strings.Contains(tree, "m.txt") {
		t.Errorf("the staged commit is not the linked work tree's own change:\n%s", tree)
	}
}

// probe is a planted secret, and the range of the one commit that holds it.
type probe struct{ name, secret, rng string }

// planted is a repository whose commits hide a secret from a scan that does
// not hold the guards, one commit for each: a file a diff driver's textconv
// prints nothing of, a token followed by one NUL byte, and a merge whose
// resolution alone holds a token. The driver is the repository's own, set in
// its config and its info/attributes.
func planted(t *testing.T) (string, []probe) {
	t.Helper()
	dir := repo(t, map[string]string{"a.txt": "nothing here\n"})
	gitT(t, dir, "config", "diff.external", "true")
	gitT(t, dir, "config", "diff.hide.textconv", "true")
	write(t, filepath.Join(dir, ".git", "info"), "attributes", "* diff=hide\n")
	head := func() string { return strings.TrimSpace(revParse(t, dir)) }
	var probes []probe
	step := func(name string, files map[string]string, secret string) {
		from := head()
		commit(t, dir, name, files)
		probes = append(probes, probe{name, secret, from + ".." + head()})
	}
	driver, nul, merge := tokenForTest(t), tokenForTest(t), tokenForTest(t)
	step("a driver's textconv", map[string]string{"d.txt": "token = " + driver + "\n"}, driver)
	step("a NUL byte after the token", map[string]string{"k.txt": "token = " + nul + "\x00", "n.txt": "clean\n"}, nul)
	from := head()
	gitT(t, dir, "checkout", "-q", "-b", "side")
	commit(t, dir, "side", map[string]string{"s.txt": "side\n"})
	gitT(t, dir, "checkout", "-q", "main")
	commit(t, dir, "main", map[string]string{"m.txt": "main\n"})
	gitT(t, dir, "merge", "-q", "--no-ff", "--no-commit", "side")
	write(t, dir, "r.txt", "token = "+merge+"\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "merge")
	probes = append(probes, probe{"a secret only in a merge", merge, from + "..HEAD"})
	return dir, probes
}

// The push tier keeps the real gitleaks once for each mode, so a push reads a
// planted secret with the tool and not with a stand-in. This row and the next
// run under -short: the range, one range for each probe, and the history, on
// one tree. The rest of the real rows are the release tier's.
//
// What it costs when this fails: a regression in how hatch calls the real
// gitleaks, which the stand-in rows cannot see, reaches main through a green
// push tier.
func TestTheRealGitleaksReadsARangeAndTheHistoryInThePushTier(t *testing.T) {
	t.Parallel()
	bin := realGitleaksInPush(t)
	dir, probes := planted(t)
	red := func(name, secret string, args ...string) {
		t.Helper()
		code, out, errb := runSecrets(t, append([]string{"--gitleaks", bin, "--dir", dir}, args...)...)
		// gitleaks's own finding, not the count's refusal of a commit it read as empty.
		if code != 1 || strings.Contains(out+errb, secret) || !strings.Contains(out+errb, "gitleaks exited 1") {
			t.Errorf("%s: exit %d, want 1 with gitleaks's reason, and the token in the output: %v\n%s%s", name, code, strings.Contains(out+errb, secret), out, errb)
		}
	}
	for _, p := range probes {
		red(p.name+", in a range", p.secret, "--range", p.rng)
	}
	red("the history", probes[0].secret)
	clean := repo(t, map[string]string{"a.txt": "one\n"})
	commit(t, clean, "two", map[string]string{"b.txt": "two\n"})
	if code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", clean, "--range", "HEAD~1..HEAD"); code != 0 || !strings.Contains(out, "green, 1 commits scanned") {
		t.Errorf("a clean range: exit %d:\n%s%s", code, out, errb)
	}
}

// The staged mode on the real gitleaks, in the push tier: a token staged only
// in the index a hook is given is red, another repository's index is dropped
// for the scanned one's own, and a clean staged change is green. It sets the
// environment, so it runs alone.
//
// What it costs when this fails: a commit -a of a token, or a hook of a clone,
// passes the real tool while the stand-in rows stay green.
func TestTheRealGitleaksReadsTheIndexItIsGivenInThePushTier(t *testing.T) {
	bin := realGitleaksInPush(t)
	secret := tokenForTest(t)
	x := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, x, "k.txt", "token = "+secret+"\n")
	gitT(t, x, "add", "k.txt")
	data, err := os.ReadFile(filepath.Join(x, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(x, ".git"), "other-index", string(data))
	gitT(t, x, "reset", "-q")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(x, ".git", "other-index"))
	code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", x, "--staged")
	if code != 1 || strings.Contains(out+errb, secret) || !strings.Contains(out+errb, "gitleaks exited 1") {
		t.Errorf("a token only in the index given: exit %d, want 1 with gitleaks's reason, and the token in the output: %v\n%s%s", code, strings.Contains(out+errb, secret), out, errb)
	}
	y := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, y, "k.txt", "token = "+secret+"\n")
	gitT(t, y, "add", "k.txt")
	z := t.TempDir()
	gitT(t, z, "clone", "-q", y, "z")
	zz := filepath.Join(z, "z")
	write(t, zz, "b.txt", "clean text\n")
	gitT(t, zz, "add", "b.txt")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(zz, ".git", "index"))
	code, out, errb = runSecrets(t, "--gitleaks", bin, "--dir", y, "--staged")
	if code != 1 || !strings.Contains(out+errb, "gitleaks exited 1") {
		t.Errorf("another repository's index: exit %d, want 1 on the scanned one's own change:\n%s%s", code, out, errb)
	}
	t.Setenv("GIT_INDEX_FILE", "")
	c := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, c, "n.txt", "nothing again\n")
	gitT(t, c, "add", "n.txt")
	if code, out, errb = runSecrets(t, "--gitleaks", bin, "--dir", c, "--staged"); code != 0 || !strings.Contains(out, "the staged change scanned") {
		t.Errorf("a clean staged change: exit %d:\n%s%s", code, out, errb)
	}
}

// replaced is a repository whose commit that holds a token has a git replace
// ref to a clean commit of the same parent: a scan that reads the replacement
// sees nothing. The range is of the token's commit alone.
func replaced(t *testing.T) (dir, secret, rng string) {
	t.Helper()
	dir = repo(t, map[string]string{"a.txt": "nothing here\n"})
	secret = tokenForTest(t)
	base := strings.TrimSpace(revParse(t, dir))
	commit(t, dir, "token", map[string]string{"k.txt": "token = " + secret + "\n"})
	bad := strings.TrimSpace(revParse(t, dir))
	gitT(t, dir, "checkout", "-q", "--detach", base)
	commit(t, dir, "clean", map[string]string{"n.txt": "clean\n"})
	clean := strings.TrimSpace(revParse(t, dir))
	gitT(t, dir, "checkout", "-q", "main")
	gitT(t, dir, "replace", bad, clean)
	return dir, secret, base + ".." + bad
}

// The real gitleaks reads the commit that holds a token, not the clean commit
// a git replace ref swaps for it, in the range and the history, and a staged
// change is read against the parent it has, not a replacement of it.
//
// What it costs when this fails: git replace, which a clone can carry, hides a
// committed token from the range and history modes, and a staged token from
// the staged one, and the push is green.
func TestTheRealGitleaksReadsACommitAReplaceRefHides(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	dir, secret, rng := replaced(t)
	for name, args := range map[string][]string{"a range": {"--range", rng}, "the history": nil} {
		code, out, errb := runSecrets(t, append([]string{"--gitleaks", bin, "--dir", dir}, args...)...)
		if code != 1 || strings.Contains(out+errb, secret) || !strings.Contains(out+errb, "gitleaks exited 1") {
			t.Errorf("%s of a replaced commit: exit %d, want 1 with gitleaks's reason, and the token in the output: %v\n%s%s", name, code, strings.Contains(out+errb, secret), out, errb)
		}
	}
	// A staged token whose parent is replaced by a commit that already holds the file.
	staged := repo(t, map[string]string{"a.txt": "one\n"})
	base := strings.TrimSpace(revParse(t, staged))
	token := tokenForTest(t)
	gitT(t, staged, "checkout", "-q", "--detach", base)
	commit(t, staged, "holds the file", map[string]string{"k.txt": "token = " + token + "\n"})
	holds := strings.TrimSpace(revParse(t, staged))
	gitT(t, staged, "checkout", "-q", "main")
	gitT(t, staged, "replace", base, holds)
	write(t, staged, "k.txt", "token = "+token+"\n")
	gitT(t, staged, "add", "k.txt")
	code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", staged, "--staged")
	if code != 1 || strings.Contains(out+errb, token) || !strings.Contains(out+errb, "gitleaks exited 1") {
		t.Errorf("a staged token over a replaced parent: exit %d, want 1 with gitleaks's reason, and the token in the output: %v\n%s%s", code, strings.Contains(out+errb, token), out, errb)
	}
}

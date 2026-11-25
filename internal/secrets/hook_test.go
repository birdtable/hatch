package secrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/birdtable/go-proc/gitenv"
	"github.com/birdtable/go-proc/proc"
)

// hatchBin is the hatch program the hook rows run, built once for the test binary and removed by TestMain.
var (
	hatchOnce sync.Once
	hatchPath string
	hatchDir  string
	hatchErr  error
)

// hatchBinary is the path of the hatch program built from cmd/hatch, built once for all the rows of the test
// binary (the build was the cost of each row). The folder it is built into holds the one file, so the
// test names no system (an .exe on Windows): the folder holds the one file.
func hatchBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("a row on the real gitleaks: the release tier runs it")
	}
	hatchOnce.Do(func() { hatchPath, hatchErr = buildHatch() })
	if hatchErr != nil {
		t.Fatal(hatchErr)
	}
	return hatchPath
}

func buildHatch() (string, error) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("go is not on PATH: %v", err)
	}
	dir, err := os.MkdirTemp("", "hatch-bin")
	if err != nil {
		return "", err
	}
	hatchDir = dir
	src, err := filepath.Abs(filepath.Join("..", "..", "cmd", "hatch"))
	if err != nil {
		return "", err
	}
	res, err := proc.Run(context.Background(), proc.Cmd{
		Path:      goPath,
		Args:      []string{"build", "-o", dir + string(filepath.Separator), src},
		Env:       os.Environ(),
		Timeout:   10 * time.Minute,
		MaxOutput: 1 << 20,
		Drain:     2 * time.Second,
	})
	if err != nil || res.Code != 0 {
		return "", fmt.Errorf("go build of hatch failed: %v\n%s", err, res.Stderr)
	}
	built, err := os.ReadDir(dir)
	if err != nil || len(built) != 1 {
		return "", fmt.Errorf("the build folder holds %d files, want the one program: %v", len(built), err)
	}
	return filepath.Join(dir, built[0].Name()), nil
}

// realCommit is a git commit run as a person runs it, with its hooks on:
// gitT turns them off. The environment is the caller's less the variables that
// name a repository, so a run under a hook of this repository's own is not
// moved. It is the exit code and what git and the hook printed.
func realCommit(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "commit", "-q", "-m", "x"}, args...)
	res, err := proc.Run(context.Background(), proc.Cmd{
		Path:      p,
		Args:      append([]string{"-C", dir}, full...),
		Env:       gitenv.Repo(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"),
		Timeout:   5 * time.Minute,
		MaxOutput: 1 << 20,
		Drain:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("git commit did not run: %v", err)
	}
	return res.Code, string(res.Stdout) + string(res.Stderr)
}

// hook puts a pre-commit hook in dir that runs hatch secrets --staged on the
// pinned gitleaks. It is a sh script, which git runs through its own sh on
// every system (git for Windows included), so no .bat is written.
func hook(t *testing.T, dir, hatch, gitleaks string) {
	t.Helper()
	body := "#!/bin/sh\nexec \"" + filepath.ToSlash(hatch) + "\" secrets --staged --gitleaks \"" + filepath.ToSlash(gitleaks) + "\" --dir .\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A real git commit -a and git commit <path> hand the hook another index than
// .git/index (index.lock, next-index-<pid>.lock), which is where their change
// is staged: the staged pass reads that one, so a token in the change is red
// with gitleaks's reason, and a clean change commits.
//
// What it costs when this fails: a commit -a or commit <path> of a token
// passes the hook (it read .git/index, which lacks the change), or every such
// commit is refused for a lock the scan itself took.
func TestAHookOfACommitReadsTheIndexItIsGiven(t *testing.T) {
	t.Parallel()
	gl := realGitleaks(t)
	hatch := hatchBinary(t)
	secret := tokenForTest(t)
	for _, mode := range []struct {
		name string
		args []string
		// stage puts the change in the work tree, and in the index for a
		// mode that reads the index alone.
		stage func(t *testing.T, dir, body string)
	}{
		{"commit -a", []string{"-a"}, func(t *testing.T, dir, body string) {
			write(t, dir, "a.txt", body)
		}},
		{"commit <path>", []string{"a.txt"}, func(t *testing.T, dir, body string) {
			write(t, dir, "a.txt", body)
			// Another file is staged and is not in the commit.
			write(t, dir, "c.txt", "staged apart\n")
			gitT(t, dir, "add", "c.txt")
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			dir := repo(t, map[string]string{"a.txt": "one\n", "b.txt": "one\n"})
			hook(t, dir, hatch, gl)
			mode.stage(t, dir, "token = "+secret+"\n")
			code, out := realCommit(t, dir, mode.args...)
			if code == 0 || !strings.Contains(out, "gitleaks exited 1") || strings.Contains(out, secret) {
				t.Errorf("a token: exit %d, want a refusal with gitleaks's reason, and the token in the output: %v\n%s", code, strings.Contains(out, secret), out)
			}
			if strings.Contains(out, "lock") {
				t.Errorf("a token: the refusal is about a lock:\n%s", out)
			}
			mode.stage(t, dir, "clean text\n")
			if code, out := realCommit(t, dir, mode.args...); code != 0 {
				t.Errorf("a clean change: exit %d, want the commit made:\n%s", code, out)
			}
		})
	}
}

// write is a file of body in dir, in the work tree.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A repository's own diff drivers change what git log -p prints, and so what
// gitleaks reads, unless its log is run with no external diff and no textconv:
// a textconv program that prints nothing hides a token in every mode, and a
// diff.external is set beside it, as the issue describes it (git log runs none
// without --ext-diff, so that one is a control). The driver is chosen by the
// repository's own .git/info/attributes: the empty tree's attributes leave a
// committed one unread, and this file is not the branch's.
//
// What it costs when this fails: one line of the repository's own config
// hides every secret from the scan.
func TestADiffDriverHidesNoSecret(t *testing.T) {
	t.Parallel()
	gl := realGitleaks(t)
	secret := tokenForTest(t)
	driver := func(t *testing.T, dir string) {
		t.Helper()
		gitT(t, dir, "config", "diff.external", "true")
		gitT(t, dir, "config", "diff.hide.textconv", "true")
		write(t, filepath.Join(dir, ".git", "info"), "attributes", "* diff=hide\n")
	}
	dir := repo(t, map[string]string{"a.txt": "nothing here\n"})
	driver(t, dir)
	commit(t, dir, "leak", map[string]string{"k.txt": "token = " + secret + "\n"})
	for mode, args := range map[string][]string{"range": {"--range", "HEAD~1..HEAD"}, "history": nil} {
		code, out, errb := runSecrets(t, append([]string{"--gitleaks", gl, "--dir", dir}, args...)...)
		// gitleaks's own finding, not the count's refusal of a commit it read as empty.
		if code != 1 || strings.Contains(out+errb, secret) || !strings.Contains(out+errb, "gitleaks exited 1") {
			t.Errorf("%s: exit %d, want 1 with gitleaks's reason, and the token in the output: %v\n%s%s", mode, code, strings.Contains(out+errb, secret), out, errb)
		}
	}
	staged := repo(t, map[string]string{"a.txt": "nothing here\n"})
	driver(t, staged)
	write(t, staged, "k.txt", "token = "+secret+"\n")
	gitT(t, staged, "add", "k.txt")
	if code, out, errb := runSecrets(t, "--gitleaks", gl, "--dir", staged, "--staged"); code != 1 || !strings.Contains(out+errb, "gitleaks exited 1") {
		t.Errorf("staged: exit %d, want 1 with gitleaks's reason\n%s%s", code, out, errb)
	}
}

// A run whose index is locked by another git is red and says the lock, not an
// unmerged path.
//
// What it costs when this fails: a held lock is reported as a merge in
// progress, and the person looks for the wrong cause.
func TestAHeldIndexLockIsNamed(t *testing.T) {
	gl := realGitleaks(t)
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	write(t, dir, "b.txt", "two\n")
	gitT(t, dir, "add", "b.txt")
	idx := filepath.Join(dir, ".git", "held-index")
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ".git"), "held-index", string(data))
	write(t, filepath.Join(dir, ".git"), "held-index.lock", "held\n")
	t.Setenv("GIT_INDEX_FILE", idx)
	code, out, errb := runSecrets(t, "--gitleaks", gl, "--dir", dir, "--staged")
	if code != 1 || !strings.Contains(errb, "its lock is held") || strings.Contains(errb, "unmerged") {
		t.Errorf("a held lock: exit %d, want 1 naming the lock:\n%s%s", code, out, errb)
	}
}

// The index a hook is handed is read only when it is a file directly in the git
// folder of the work tree scanned: a hook of another repository, or of another
// work tree of this one, running hatch with --dir on this folder, hands it an
// index that is not its staged change. Each row stages the token in the scanned
// work tree and a clean file whose blob is in the object store in the other, so
// the other's index writes as a tree there and, read, would be green.
//
// What it costs when this fails: a hook of a clone or of a linked work tree
// passes a token staged in the folder it names, with an index it never read.
func TestAnIndexOfAnotherWorkTreeIsNotRead(t *testing.T) {
	gl := realGitleaks(t)
	secret := tokenForTest(t)
	red := func(t *testing.T, index, dir string) {
		t.Helper()
		t.Setenv("GIT_INDEX_FILE", index)
		code, out, errb := runSecrets(t, "--gitleaks", gl, "--dir", dir, "--staged")
		if code != 1 || !strings.Contains(out+errb, "gitleaks exited 1") {
			t.Errorf("another work tree's index: exit %d, want 1 with gitleaks's reason:\n%s%s", code, out, errb)
		}
	}
	// blob puts a clean file's blob into x's object store, and leaves it unstaged.
	blob := func(t *testing.T, x string) {
		t.Helper()
		write(t, x, "clean.txt", "clean text\n")
		gitT(t, x, "add", "clean.txt")
		gitT(t, x, "rm", "-q", "--cached", "clean.txt")
	}
	t.Run("a clone", func(t *testing.T) {
		x := repo(t, map[string]string{"a.txt": "one\n"})
		blob(t, x)
		write(t, x, "k.txt", "token = "+secret+"\n")
		gitT(t, x, "add", "k.txt")
		y := filepath.Join(t.TempDir(), "y")
		gitT(t, filepath.Dir(y), "clone", "-q", x, y)
		write(t, y, "clean.txt", "clean text\n")
		gitT(t, y, "add", "clean.txt")
		red(t, filepath.Join(y, ".git", "index"), x)
	})
	t.Run("a linked work tree's index, on the main one", func(t *testing.T) {
		m := repo(t, map[string]string{"a.txt": "one\n"})
		w := filepath.Join(t.TempDir(), "w")
		gitT(t, m, "worktree", "add", "-q", "-b", "w", w)
		write(t, w, "clean.txt", "clean text\n")
		gitT(t, w, "add", "clean.txt")
		write(t, m, "k.txt", "token = "+secret+"\n")
		gitT(t, m, "add", "k.txt")
		red(t, filepath.Join(m, ".git", "worktrees", "w", "index"), m)
	})
	t.Run("the main work tree's index, on a linked one", func(t *testing.T) {
		m := repo(t, map[string]string{"a.txt": "one\n"})
		w := filepath.Join(t.TempDir(), "w")
		gitT(t, m, "worktree", "add", "-q", "-b", "w", w)
		write(t, m, "clean.txt", "clean text\n")
		gitT(t, m, "add", "clean.txt")
		write(t, w, "k.txt", "token = "+secret+"\n")
		gitT(t, w, "add", "k.txt")
		red(t, filepath.Join(m, ".git", "index"), w)
	})
}

// A hook of a linked work tree scanning itself still reads its own index: a
// real git commit -a in the work tree, through the hook of the main folder.
//
// What it costs when this fails: a commit -a from a linked work tree is red on
// a lock, or passes a token it never read.
func TestAHookOfALinkedWorkTreeReadsItsOwnIndex(t *testing.T) {
	gl := realGitleaks(t)
	hatch := hatchBinary(t)
	secret := tokenForTest(t)
	m := repo(t, map[string]string{"a.txt": "one\n"})
	hook(t, m, hatch, gl)
	w := filepath.Join(t.TempDir(), "w")
	gitT(t, m, "worktree", "add", "-q", "-b", "w", w)
	write(t, w, "a.txt", "token = "+secret+"\n")
	code, out := realCommit(t, w, "-a")
	if code == 0 || !strings.Contains(out, "gitleaks exited 1") || strings.Contains(out, "lock") || strings.Contains(out, secret) {
		t.Errorf("a token: exit %d, want a refusal with gitleaks's reason:\n%s", code, out)
	}
	write(t, w, "a.txt", "clean text\n")
	if code, out := realCommit(t, w, "-a"); code != 0 {
		t.Errorf("a clean change: exit %d, want the commit made:\n%s", code, out)
	}
}

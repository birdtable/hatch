package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/birdtable/go-proc/proc"

	"github.com/birdtable/hatch/internal/gitrun"
)

// TestMain lets this test binary stand in for gitleaks: gitleaks is started as
// `gitleaks git ...` and go test never passes git first, so a first argument
// of git is the stand-in. What it does is named by the file --config gives,
// one word, so a row picks it with no environment.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "git" {
		os.Exit(fakeGitleaks(os.Args[2:]))
	}
	code := m.Run()
	if hatchDir != "" {
		os.RemoveAll(hatchDir)
	}
	os.Exit(code)
}

func fakeGitleaks(args []string) int {
	mode := "clean"
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			b, err := os.ReadFile(args[i+1])
			if err != nil {
				return 96
			}
			mode = strings.TrimSpace(string(b))
		}
	}
	say := func(s string) { fmt.Fprintln(os.Stderr, s) }
	// "record <path>": the arguments gitleaks was started with go to the file, one
	// a line, and its GIT_NO_REPLACE_OBJECTS to <path>.env, and one commit is
	// scanned (guards_test.go reads them).
	if path, ok := strings.CutPrefix(mode, "record "); ok {
		if err := os.WriteFile(path, []byte(strings.Join(args, "\n")+"\n"), 0o600); err != nil {
			return 97
		}
		if err := os.WriteFile(path+".env", []byte("GIT_NO_REPLACE_OBJECTS="+os.Getenv("GIT_NO_REPLACE_OBJECTS")+"\n"), 0o600); err != nil {
			return 97
		}
		say("INF 1 commits scanned.")
		return 0
	}
	switch mode {
	case "clean":
		say("INF 3 commits scanned.")
	case "two":
		say("INF 2 commits scanned.")
	case "three":
		say("INF 3 commits scanned.")
	case "under":
		say("INF 1 commits scanned.")
	case "none":
		say("INF 0 commits scanned.")
	case "nocount":
		say("INF scanned ~6 bytes")
	case "twice":
		say("INF 3 commits scanned.")
		say("INF 3 commits scanned.")
	case "err":
		say("INF 3 commits scanned.")
		say("ERR [git] something git wrote")
	case "exit2":
		say("INF 3 commits scanned.")
		return 2
	case "finding":
		// What gitleaks prints with --redact: the secret is REDACTED.
		fmt.Println("Finding:     key = REDACTED\nSecret:      REDACTED\nRuleID:      generic-api-key\nFile:        a.txt\nLine:        1")
		say("INF 3 commits scanned.")
		say("WRN leaks found: 1")
		return 1
	}
	return 0
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	res, err := g.Run(context.Background(), dir, full...)
	if err != nil || res.Code != 0 {
		t.Fatalf("git %s failed: %v\n%s", args[0], err, res.Stderr)
	}
}

// repo is a git repository with the commits of files, one commit each.
func repo(t *testing.T, commits ...map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	for i, files := range commits {
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", fmt.Sprintf("c%d", i))
	}
	return dir
}

func config(t *testing.T, word string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(word+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runSecrets(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func self(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// What gitleaks says decides the verdict. Each row is a gitleaks the test
// stands in for, on a repository with commits, and only a clean one with a
// count is green.
//
// What it costs when this fails: a pass that read nothing, or found something,
// or could not say what it read, is green on a guard of private data.
func TestOnlyACountedCleanRunIsGreen(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	rows := []struct {
		mode string
		code int
		want string
	}{
		{"clean", 0, "green, 3 commits scanned"},
		{"none", 1, "scanned 0 commits"},
		{"nocount", 1, "holds 0 counts"},
		{"twice", 1, "holds 2 counts"},
		{"err", 1, "logged an error"},
		{"exit2", 1, "gitleaks exited 2"},
		{"finding", 1, "gitleaks exited 1"},
	}
	for _, r := range rows {
		code, out, errb := runSecrets(t, "--gitleaks", self(t), "--config", config(t, r.mode), "--dir", dir)
		if code != r.code || !strings.Contains(out+errb, r.want) {
			t.Errorf("%s: exit %d, want %d with %q:\n%s%s", r.mode, code, r.code, r.want, out, errb)
		}
	}
}

// A finding's output passes through as gitleaks wrote it and holds no secret:
// the planted one is made at run time and never checked in.
//
// What it costs when this fails: a secret reaches a log.
func TestAPlantedSecretIsRedAndNotInTheOutput(t *testing.T) {
	t.Parallel()
	secret := plant(t)
	dir := repo(t, map[string]string{"a.txt": "key = " + secret + "\n"})
	code, out, errb := runSecrets(t, "--gitleaks", self(t), "--config", config(t, "finding"), "--dir", dir)
	if code != 1 || strings.Contains(out+errb, secret) {
		t.Fatalf("exit %d, and the output holds the secret: %v", code, strings.Contains(out+errb, secret))
	}
}

func plant(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = "abcdefghijklmnopqrstuvwxyz0123456789"[b[i]%36]
	}
	return string(b)
}

// What git says is held first: no commit is red with git's reason, and so is a
// config that sets format.pretty, whatever gitleaks would say.
//
// What it costs when this fails: gitleaks v8.30.1 exits 0 on both, having read
// no commit, and the guard passes a history it never looked at.
func TestNoCommitAndFormatPrettyAreRed(t *testing.T) {
	t.Parallel()
	cfg := config(t, "clean")
	empty := repo(t)
	code, _, errb := runSecrets(t, "--gitleaks", self(t), "--config", cfg, "--dir", empty)
	if code != 1 || !strings.Contains(errb, "no commit") {
		t.Errorf("an empty history: exit %d: %s", code, errb)
	}
	pretty := repo(t, map[string]string{"a.txt": "one\n"})
	gitT(t, pretty, "config", "format.pretty", "oneline")
	code, _, errb = runSecrets(t, "--gitleaks", self(t), "--config", cfg, "--dir", pretty)
	if code != 1 || !strings.Contains(errb, "format.pretty") {
		t.Errorf("format.pretty: exit %d: %s", code, errb)
	}
	gitT(t, pretty, "config", "--unset", "format.pretty")
	if code, _, errb = runSecrets(t, "--gitleaks", self(t), "--config", cfg, "--dir", pretty); code != 0 {
		t.Errorf("the same repository with it unset: exit %d: %s", code, errb)
	}
}

// A gitleaks that is not there, or does not start, is red, and so is a folder
// that is no repository; a usage error is exit 2, and git not found is 3.
//
// What it costs when this fails: a mistyped path to gitleaks passes.
func TestAMissingGitleaksAndOtherFailuresAreNotGreen(t *testing.T) {
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	none := filepath.Join(t.TempDir(), "no-such-gitleaks")
	if code, _, errb := runSecrets(t, "--gitleaks", none, "--dir", dir); code != 1 {
		t.Errorf("a path with no file: exit %d: %s", code, errb)
	}
	notExec := filepath.Join(t.TempDir(), "gitleaks")
	if err := os.WriteFile(notExec, []byte("not a program"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runSecrets(t, "--gitleaks", notExec, "--dir", dir); code != 1 {
		t.Errorf("a file that does not start: exit %d: %s", code, errb)
	}
	if code, _, errb := runSecrets(t, "--gitleaks", "no-such-gitleaks-on-path", "--dir", dir); code != 1 || !strings.Contains(errb, "not found") {
		t.Errorf("a name not on PATH: exit %d: %s", code, errb)
	}
	if code, _, errb := runSecrets(t, "--gitleaks", self(t), "--dir", t.TempDir()); code != 1 {
		t.Errorf("a folder in no repository: exit %d: %s", code, errb)
	}
	for name, args := range map[string][]string{
		"an argument":      {"extra"},
		"an empty program": {"--gitleaks", ""},
		"a bad timeout":    {"--timeout", "0s"},
		"an unknown flag":  {"--nope"},
	} {
		if code, _, _ := runSecrets(t, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	t.Setenv("PATH", "")
	if code, _, _ := runSecrets(t, "--gitleaks", self(t), "--dir", dir); code != 3 {
		t.Errorf("no git: exit %d, want 3", code)
	}
}

// The real gitleaks, the one tools/gitleaks.mod pins and `go tool` builds, on every system with no PATH and no
// skip: a clean history is green, a
// secret planted now is red and not in the output, and the empty history and
// format.pretty are red as the stand-in rows say. It is skipped where there is
// no gitleaks; the stand-in rows above run everywhere.
//
// What it costs when this fails: the guard's reading of gitleaks's real output
// (the count line, the exit code) drifts from what gitleaks prints.
func TestTheRealGitleaks(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	clean := repo(t, map[string]string{"a.txt": "nothing here\n"})
	code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", clean)
	if code != 0 || !strings.Contains(out, "green, 1 commits scanned") {
		t.Fatalf("a clean history: exit %d:\n%s%s", code, out, errb)
	}
	secret := "ghp_" + plant(t) + plant(t)[:12]
	leak := repo(t, map[string]string{"a.txt": "token = " + secret + "\n"})
	code, out, errb = runSecrets(t, "--gitleaks", bin, "--dir", leak)
	if code != 1 || strings.Contains(out+errb, secret) {
		t.Fatalf("a planted secret: exit %d, in the output: %v\n%s%s", code, strings.Contains(out+errb, secret), out, errb)
	}
	if code, _, errb := runSecrets(t, "--gitleaks", bin, "--dir", repo(t)); code != 1 {
		t.Errorf("an empty history: exit %d: %s", code, errb)
	}
}

// realGitleaksOnce builds the pinned gitleaks once for the test binary: every row on the real gitleaks asks for it,
// and `go tool -n` is a process each time.
var (
	realOnce sync.Once
	realPath string
	realErr  error
)

// realGitleaks is the path of the gitleaks that tools/gitleaks.mod pins, built by go tool: the test gets it the
// same way on every system, with no PATH and no network of its own (the fetch job holds the modules). A tool that
// cannot be built fails the test: it is never a skip. A row on the real gitleaks is the release tier's: the
// pre-push tier runs the tests with -short (gates test-push), where the row is skipped, except the three rows of
// guards_test.go that name realGitleaksInPush, one for each mode. What the stand-in gitleaks can
// hold, the push tier holds there too (the log options, and the index the staged commit is made of).
func realGitleaks(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("a row on the real gitleaks: the release tier runs it")
	}
	return realGitleaksInPush(t)
}

// realGitleaksInPush is realGitleaks for the rows the push tier keeps: it is never skipped under -short.
func realGitleaksInPush(t *testing.T) string {
	t.Helper()
	realOnce.Do(func() { realPath, realErr = buildGitleaks() })
	if realErr != nil {
		t.Fatal(realErr)
	}
	return realPath
}

func buildGitleaks() (string, error) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("go is not on PATH: %v", err)
	}
	modfile, err := filepath.Abs(filepath.Join("..", "..", "tools", "gitleaks.mod"))
	if err != nil {
		return "", err
	}
	res, err := proc.Run(context.Background(), proc.Cmd{
		Path:      goPath,
		Args:      []string{"tool", "-n", "-modfile=" + modfile, "gitleaks"},
		Env:       os.Environ(),
		Timeout:   10 * time.Minute,
		MaxOutput: 1 << 20,
		Drain:     2 * time.Second,
	})
	if err != nil || res.Code != 0 {
		return "", fmt.Errorf("go tool could not build the pinned gitleaks: %v\n%s", err, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// commit adds a commit that holds the files, or none: an empty commit.
func commit(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

// A range is green only when gitleaks's count of commits equals git's count of
// the commits that add text in the range, and an empty range is red.
//
// What it costs when this fails: a gitleaks that read fewer commits than the
// range holds, or none, passes a range it did not scan.
func TestARangeIsGreenOnlyWhenGitleaksCountsAsGitDoes(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{"a.txt": "one\n"}, map[string]string{"b.txt": "two\n"}, map[string]string{"c.txt": "three\n"})
	rows := []struct {
		name, mode, rng, want string
		code                  int
	}{
		{"as many", "two", "HEAD~2..HEAD", "green, 2 commits scanned", 0},
		{"under-reported", "under", "HEAD~2..HEAD", "git counts 2", 1},
		{"over-reported", "clean", "HEAD~2..HEAD", "git counts 2", 1},
		{"no count", "nocount", "HEAD~2..HEAD", "holds 0 counts", 1},
		{"zero counted", "none", "HEAD~2..HEAD", "scanned 0 commits", 1},
		{"empty range", "clean", "HEAD..HEAD", "holds no commit", 1},
		{"unknown base", "two", "no-such-rev..HEAD", "could not read the range", 1},
	}
	for _, r := range rows {
		code, out, errb := runSecrets(t, "--gitleaks", self(t), "--config", config(t, r.mode), "--dir", dir, "--range", r.rng)
		if code != r.code || !strings.Contains(out+errb, r.want) {
			t.Errorf("%s: exit %d, want %d with %q:\n%s%s", r.name, code, r.code, r.want, out, errb)
		}
	}
}

// A merge is in the count by its own diff against its first parent, which
// gitleaks reads with --diff-merges=first-parent, and an empty commit is in
// neither side of the equality.
//
// What it costs when this fails: a range with a merge or an empty commit is
// red for ever, or the equality is loosened to let it pass.
func TestMergesAreInTheCountAndEmptyCommitsAreNot(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	gitT(t, dir, "checkout", "-q", "-b", "side")
	commit(t, dir, "side", map[string]string{"s.txt": "s\n"})
	gitT(t, dir, "checkout", "-q", "main")
	commit(t, dir, "work", map[string]string{"m.txt": "m\n"})
	commit(t, dir, "empty", nil)
	gitT(t, dir, "merge", "-q", "--no-ff", "side", "-m", "merge")
	commit(t, dir, "empty again", nil)
	// Five commits in git's log of the range, three that add text: the work,
	// the side and the merge, which adds the side's file to its first parent.
	code, out, errb := runSecrets(t, "--gitleaks", self(t), "--config", config(t, "three"), "--dir", dir, "--range", "HEAD~4..HEAD")
	if code != 0 || !strings.Contains(out, "green, 3 commits scanned") {
		t.Errorf("a range with a merge and two empty commits: exit %d:\n%s%s", code, out, errb)
	}
	code, _, errb = runSecrets(t, "--gitleaks", self(t), "--config", config(t, "two"), "--dir", dir, "--range", "HEAD~1..HEAD")
	if code != 1 || !strings.Contains(errb, "holds no commit") {
		t.Errorf("a range of one empty commit: exit %d: %s", code, errb)
	}
}

// A staged change is scanned as a range of one temporary commit: it is green
// only when gitleaks counts that one commit, once, and exits 0.
//
// What it costs when this fails: a staged scan that read nothing, or whose
// output cannot be read, lets a commit through.
func TestAStagedChangeIsGreenOnlyAsARangeOfOne(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{"a.txt": "one\n"})
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "b.txt")
	rows := []struct {
		mode, want string
		code       int
	}{
		{"under", "green, the staged change scanned", 0},
		{"two", "git counts 1", 1},
		{"none", "gitleaks scanned 0 commits", 1},
		{"nocount", "holds 0 counts", 1},
		{"twice", "holds 2 counts", 1},
		{"err", "logged an error", 1},
		{"finding", "gitleaks exited 1", 1},
	}
	for _, r := range rows {
		code, out, errb := runSecrets(t, "--gitleaks", self(t), "--config", config(t, r.mode), "--dir", dir, "--staged")
		if code != r.code || !strings.Contains(out+errb, r.want) {
			t.Errorf("%s: exit %d, want %d with %q:\n%s%s", r.mode, code, r.code, r.want, out, errb)
		}
	}
}

// A staged scan needs no commit: a repository before its first commit is
// scanned, and --range and --staged are two ways to ask, never both.
//
// What it costs when this fails: the first commit of a repository is
// refused, or a command that names two scans runs one and says nothing of the
// other.
func TestStagedNeedsNoCommitAndTheTwoModesExclude(t *testing.T) {
	t.Parallel()
	empty := repo(t)
	if err := os.WriteFile(filepath.Join(empty, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, empty, "add", "-A")
	if code, out, errb := runSecrets(t, "--gitleaks", self(t), "--config", config(t, "under"), "--dir", empty, "--staged"); code != 0 {
		t.Errorf("staged before the first commit: exit %d:\n%s%s", code, out, errb)
	}
	for name, args := range map[string][]string{
		"both":           {"--range", "a..b", "--staged"},
		"no dots":        {"--range", "main"},
		"three dots":     {"--range", "a...b"},
		"empty range":    {"--range", ""},
		"an option":      {"--range", "--all..b"},
		"a space inside": {"--range", "a..b --all"},
		"no head":        {"--range", "a.."},
	} {
		if code, _, _ := runSecrets(t, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

// The real gitleaks on a range and on a staged change: a secret planted now
// is red and not in the output, a clean one is green, and the count it prints
// for a range with a merge and an empty commit is the count git gives.
//
// What it costs when this fails: gitleaks's real count, or its staged output,
// drifts from what the stand-in rows assume, and the equality is red for
// every range or green for none.
func TestTheRealGitleaksOnARangeAndStaged(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	secret := "ghp_" + plant(t) + plant(t)[:12]
	dir := repo(t, map[string]string{"a.txt": "nothing here\n"})
	gitT(t, dir, "checkout", "-q", "-b", "side")
	commit(t, dir, "side", map[string]string{"s.txt": "side\n"})
	gitT(t, dir, "checkout", "-q", "main")
	commit(t, dir, "work", map[string]string{"m.txt": "work\n"})
	commit(t, dir, "empty", nil)
	gitT(t, dir, "merge", "-q", "--no-ff", "side", "-m", "merge")
	code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", dir, "--range", "HEAD~3..HEAD")
	if code != 0 || !strings.Contains(out, "green, 3 commits scanned") {
		t.Fatalf("a clean range with a merge and an empty commit: exit %d:\n%s%s", code, out, errb)
	}
	if code, _, errb = runSecrets(t, "--gitleaks", bin, "--dir", dir, "--range", "HEAD~2..HEAD~1"); code != 1 {
		t.Errorf("a range of one empty commit: exit %d: %s", code, errb)
	}
	base := strings.TrimSpace(revParse(t, dir))
	commit(t, dir, "leak", map[string]string{"k.txt": "token = " + secret + "\n"})
	code, out, errb = runSecrets(t, "--gitleaks", bin, "--dir", dir, "--range", base+"..HEAD")
	if code != 1 || strings.Contains(out+errb, secret) {
		t.Errorf("a planted secret in a range: exit %d, in the output: %v\n%s%s", code, strings.Contains(out+errb, secret), out, errb)
	}
	if err := os.WriteFile(filepath.Join(dir, "n.txt"), []byte("nothing again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "n.txt")
	if code, out, errb = runSecrets(t, "--gitleaks", bin, "--dir", dir, "--staged"); code != 0 || !strings.Contains(out, "the staged change scanned") {
		t.Errorf("a clean staged change: exit %d:\n%s%s", code, out, errb)
	}
	if err := os.WriteFile(filepath.Join(dir, "l.txt"), []byte("token = "+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "l.txt")
	code, out, errb = runSecrets(t, "--gitleaks", bin, "--dir", dir, "--staged")
	if code != 1 || strings.Contains(out+errb, secret) {
		t.Errorf("a staged secret: exit %d, in the output: %v\n%s%s", code, strings.Contains(out+errb, secret), out, errb)
	}
	gitT(t, dir, "reset", "-q")
	if code, _, errb = runSecrets(t, "--gitleaks", bin, "--dir", dir, "--staged"); code != 1 || !strings.Contains(errb, "nothing is staged") {
		t.Errorf("nothing staged: exit %d: %s", code, errb)
	}
}

// revParse is HEAD~1's full name in dir, the base a range starts from.
func revParse(t *testing.T, dir string) string {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	res, err := g.Run(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil || res.Code != 0 {
		t.Fatalf("git rev-parse failed: %v", err)
	}
	return string(res.Stdout)
}

// A commit that adds no text is not in gitleaks's count, so it is not in
// git's: one commit of each kind on the real tool, then all of them as a
// range. Only a commit that adds text is green alone, and the range of the
// lot is green on the count gitleaks logs.
//
// What it costs when this fails: a branch that deletes, renames or changes
// the mode of a file is red for ever with a commit "unread" that held nothing
// to read, or a gitleaks bump moves the set and this shows it.
func TestCommitsThatAddNoTextAreInNeitherCount(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	dir := repo(t, map[string]string{"f.txt": "one\ntwo\n", "mode.sh": "echo\n", "old.txt": "old\n", "del.txt": "del\n"})
	first := strings.TrimSpace(revParse(t, dir))
	steps := []struct {
		name  string
		do    func()
		green bool
	}{
		{"a mode change alone", func() {
			if err := os.Chmod(filepath.Join(dir, "mode.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
			commit(t, dir, "mode", nil)
		}, false},
		{"a pure rename", func() {
			gitT(t, dir, "mv", "old.txt", "ren.txt")
			commit(t, dir, "rename", nil)
		}, false},
		{"a deleted file", func() {
			gitT(t, dir, "rm", "-q", "del.txt")
			commit(t, dir, "delete", nil)
		}, false},
		{"a binary file added, read as text", func() { commit(t, dir, "binary", map[string]string{"b.bin": "a\x00b\x00c\n"}) }, true},
		{"an empty file added", func() { commit(t, dir, "empty file", map[string]string{"e.txt": ""}) }, false},
		{"a text edit", func() { commit(t, dir, "edit", map[string]string{"f.txt": "one\ntwo\nthree\n"}) }, true},
	}
	for _, st := range steps {
		st.do()
		code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", dir, "--range", "HEAD~1..HEAD")
		if st.green && (code != 0 || !strings.Contains(out, "green, 1 commits scanned")) {
			t.Errorf("%s: exit %d, want green with 1 commit:\n%s%s", st.name, code, out, errb)
		}
		if !st.green && (code != 1 || !strings.Contains(errb, "holds no commit that adds text")) {
			t.Errorf("%s: exit %d, want red by name:\n%s%s", st.name, code, out, errb)
		}
	}
	// A symlink is a commit the range holds whatever gitleaks makes of it: the
	// count it logs must still be git's.
	if err := os.Symlink("f.txt", filepath.Join(dir, "link")); err == nil {
		commit(t, dir, "symlink", nil)
	}
	code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", dir, "--range", first+"..HEAD")
	if code != 0 || !strings.Contains(out, "green, ") {
		t.Errorf("the range of them all: exit %d:\n%s%s", code, out, errb)
	}
}

// tokenForTest is a secret of the shape gitleaks reads, made at run time.
func tokenForTest(t *testing.T) string {
	t.Helper()
	return "ghp_" + plant(t) + plant(t)[:12]
}

// A committed .gitattributes cannot hide a file from any mode, and a secret
// written only in a merge's resolution is read: the two probes, each planted at run time and run on the real
// gitleaks, with a control that has no attribute and no merge.
//
// What it costs when this fails: a branch hides a secret from the scan with
// one line of its own, or with a merge, and the guard passes it.
func TestAnAttributeAndAMergeHideNoSecret(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	secret := tokenForTest(t)
	hidden := func(t *testing.T, attrFile, attrs, secretFile string) string {
		dir := repo(t, map[string]string{"a.txt": "nothing here\n"})
		// A driver whose textconv shows git something else: --text does not
		// undo it, and only the empty tree's attributes leave it unselected.
		gitT(t, dir, "config", "diff.hide.textconv", "echo")
		commit(t, dir, "attr", map[string]string{attrFile: attrs, secretFile: "token = " + secret + "\n"})
		return dir
	}
	red := func(t *testing.T, name, dir string, args ...string) {
		t.Helper()
		code, out, errb := runSecrets(t, append([]string{"--gitleaks", bin, "--dir", dir}, args...)...)
		if code != 1 || strings.Contains(out+errb, secret) {
			t.Errorf("%s: exit %d, want 1, and in the output: %v\n%s%s", name, code, strings.Contains(out+errb, secret), out, errb)
		}
	}
	// Each row turns the file's diff off another way, and leaves a text file in
	// the commit so a count of the range is not 0: a pattern, a macro, a
	// folder's own .gitattributes.
	for _, row := range []struct{ file, attrs, secret string }{
		{".gitattributes", "k.txt -diff\n", "k.txt"},
		{".gitattributes", "k.txt binary\n", "k.txt"},
		{".gitattributes", "k.txt diff=hide\n", "k.txt"},
		{".gitattributes", "*.txt -diff\n", "k.txt"},
		{".gitattributes", "[attr]hide -diff\nk.txt hide\n", "k.txt"},
		{"sub/.gitattributes", "k.txt -diff\n", "sub/k.txt"},
	} {
		dir := hidden(t, row.file, row.attrs, row.secret)
		name := row.file + " " + strings.ReplaceAll(strings.TrimSpace(row.attrs), "\n", "; ")
		red(t, "range with "+name, dir, "--range", "HEAD~1..HEAD")
		red(t, "history with "+name, dir)
	}
	// Staged: the attribute is in the work tree and the index, and the secret
	// is staged beside it.
	staged := repo(t, map[string]string{"a.txt": "nothing here\n"})
	if err := os.WriteFile(filepath.Join(staged, ".gitattributes"), []byte("k.txt -diff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "k.txt"), []byte("token = "+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, staged, "add", "-A")
	red(t, "staged with k.txt -diff", staged, "--staged")
	// A merge whose resolution alone holds the token: side and main are clean.
	m := repo(t, map[string]string{"a.txt": "base\n"})
	gitT(t, m, "checkout", "-q", "-b", "side")
	commit(t, m, "side", map[string]string{"s.txt": "side\n"})
	gitT(t, m, "checkout", "-q", "main")
	commit(t, m, "main", map[string]string{"m.txt": "main\n"})
	gitT(t, m, "merge", "-q", "--no-ff", "--no-commit", "side")
	if err := os.WriteFile(filepath.Join(m, "k.txt"), []byte("token = "+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, m, "add", "-A")
	gitT(t, m, "commit", "-q", "-m", "merge")
	red(t, "a secret only in a merge, in a range", m, "--range", "HEAD~2..HEAD")
	red(t, "a secret only in a merge, in the history", m)
	// The control: the same files with no attribute are red, and a clean merge is green.
	plain := repo(t, map[string]string{"a.txt": "nothing here\n"})
	commit(t, plain, "plain", map[string]string{"k.txt": "token = " + secret + "\n"})
	red(t, "the control with no attribute", plain, "--range", "HEAD~1..HEAD")
	clean := repo(t, map[string]string{"a.txt": "base\n"})
	gitT(t, clean, "checkout", "-q", "-b", "side")
	commit(t, clean, "side", map[string]string{"s.txt": "side\n"})
	gitT(t, clean, "checkout", "-q", "main")
	commit(t, clean, "main", map[string]string{"m.txt": "main\n"})
	gitT(t, clean, "merge", "-q", "--no-ff", "side", "-m", "merge")
	if code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", clean, "--range", "HEAD~2..HEAD"); code != 0 || !strings.Contains(out, "green, 3 commits scanned") {
		t.Errorf("a clean merge in a range: exit %d:\n%s%s", code, out, errb)
	}
	if code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", clean); code != 0 {
		t.Errorf("a clean merge in the history: exit %d:\n%s%s", code, out, errb)
	}
}

// The first git whose GIT_ATTR_SOURCE the scan needs is 2.42: an older git is
// refused by name, and a line that is not git's version is refused too.
//
// What it costs when this fails: a scan runs on a git that reads the branch's
// own .gitattributes, and passes what they hide.
func TestAnOlderGitIsRefusedByName(t *testing.T) {
	for line, ok := range map[string]bool{
		"git version 2.42.0":                 true,
		"git version 2.56.0":                 true,
		"git version 3.0.1":                  true,
		"git version 2.43.0.windows.1":       true,
		"git version 2.41.9":                 false,
		"git version 2.39.5 (Apple Git-154)": false,
		"git version 1.9.0":                  false,
		"something else":                     false,
		"":                                   false,
	} {
		if err := checkGitVersion(line + "\n"); (err == nil) != ok {
			t.Errorf("%q: error %v, want ok %v", line, err, ok)
		}
	}
}

// The count reads the empty tree's attributes too: a file marked -diff before
// the base, then a range whose one commit adds clean text to it alone, is a
// commit that adds text, and git's count must say so.
//
// What it costs when this fails: a range that edits a file the branch marks
// -diff is red by name for a commit that did add text, or a count read through
// the branch's attributes comes to differ from gitleaks's.
func TestTheCountReadsTheEmptyTreesAttributes(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	dir := repo(t, map[string]string{".gitattributes": "k.txt -diff\n", "k.txt": "one\n"})
	base := strings.TrimSpace(revParse(t, dir))
	commit(t, dir, "clean text", map[string]string{"k.txt": "one\ntwo\n"})
	code, out, errb := runSecrets(t, "--gitleaks", bin, "--dir", dir, "--range", base+"..HEAD")
	if code != 0 || !strings.Contains(out, "green, 1 commits scanned") {
		t.Errorf("a clean edit of a file marked -diff: exit %d:\n%s%s", code, out, errb)
	}
}

// pngWith is a real PNG, with text after its last chunk when there is any.
func pngWith(t *testing.T, after string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < 16; i++ {
		img.Set(i%4, i/4, color.RGBA{uint8(i * 16), 0x40, 0xff - uint8(i*16), 0xff})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String() + after
}

// gitleaks's default config allowlists a path ending in .png by name, so the
// image rows use a name it does not. A file git calls binary is read as text: a NUL byte after a secret, and a
// secret after a real image's last chunk, are red in every mode, and a clean
// image is green.
//
// What it costs when this fails: a secret in a file with one NUL byte, or
// hung on an image, passes every mode, with no attribute at all.
func TestABinaryFileIsReadAsText(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	secret := tokenForTest(t)
	base := func(t *testing.T) string { return repo(t, map[string]string{"a.txt": "nothing here\n"}) }
	run := func(t *testing.T, dir string, args ...string) (int, string) {
		t.Helper()
		code, out, errb := runSecrets(t, append([]string{"--gitleaks", bin, "--dir", dir}, args...)...)
		return code, out + errb
	}
	stage := func(t *testing.T, dir string, files map[string]string) {
		t.Helper()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		gitT(t, dir, "add", "-A")
	}
	leaks := map[string]map[string]string{
		"a NUL byte after the token": {"k.txt": "token = " + secret + "\x00", "n.txt": "clean\n"},
		"a token after a real PNG":   {"logo.img": pngWith(t, "\ntoken = "+secret+"\n")},
	}
	for name, files := range leaks {
		dir := base(t)
		commit(t, dir, "leak", files)
		for mode, args := range map[string][]string{"range": {"--range", "HEAD~1..HEAD"}, "history": nil} {
			if code, got := run(t, dir, args...); code != 1 || strings.Contains(got, secret) {
				t.Errorf("%s, %s: exit %d, want 1, and in the output: %v\n%s", name, mode, code, strings.Contains(got, secret), got)
			}
		}
		staged := base(t)
		stage(t, staged, files)
		if code, got := run(t, staged, "--staged"); code != 1 || strings.Contains(got, secret) {
			t.Errorf("%s, staged: exit %d, want 1, and in the output: %v\n%s", name, code, strings.Contains(got, secret), got)
		}
	}
	clean := base(t)
	commit(t, clean, "image", map[string]string{"logo.img": pngWith(t, "")})
	if code, got := run(t, clean, "--range", "HEAD~1..HEAD"); code != 0 || !strings.Contains(got, "green, 1 commits scanned") {
		t.Errorf("a clean PNG in a range: exit %d:\n%s", code, got)
	}
	if code, got := run(t, clean); code != 0 {
		t.Errorf("a clean PNG in the history: exit %d:\n%s", code, got)
	}
	staged := base(t)
	stage(t, staged, map[string]string{"logo.img": pngWith(t, "")})
	if code, got := run(t, staged, "--staged"); code != 0 || !strings.Contains(got, "the staged change scanned") {
		t.Errorf("a clean PNG staged: exit %d:\n%s", code, got)
	}
}

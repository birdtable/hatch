package names

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

// The tests build their own lists from made-up words: no test reads a real
// list, and none holds a name that is anyone's.
const (
	wordOne  = "quillfax"
	wordTwo  = "Brindle Ostrava"
	wordNon  = "Zoë Krüger"
	wordGlue = "vexhollow"
)

// listOf is a list of the names, each as HashLines writes it.
func listOf(t *testing.T, letters bool, ns ...string) *List {
	t.Helper()
	var b strings.Builder
	for _, n := range ns {
		ls, err := HashLines(n, letters)
		if err != nil {
			t.Fatal("HashLines refused a name of the test")
		}
		b.WriteString(strings.Join(ls, "\n") + "\n")
	}
	l, err := ReadList([]byte(b.String()))
	if err != nil {
		t.Fatalf("ReadList: %v", err)
	}
	return l
}

func TestEachSurfaceFindsAPlantAndPassesAClean(t *testing.T) {
	l := listOf(t, false, wordOne)
	patch := patchOf("src/b.txt", "+a line with "+wordOne)
	cases := []struct {
		name         string
		dirty, clean taken
		want         kind
	}{
		{"content", taken{tree: true, paths: []string{"a"}, files: map[string][]byte{"a": []byte("x " + wordOne)}}, taken{tree: true, paths: []string{"a"}, files: map[string][]byte{"a": []byte("x")}}, kindContent},
		{"path", taken{tree: true, paths: []string{wordOne + "/a"}}, taken{tree: true, paths: []string{"d/a"}}, kindPath},
		{"branch", taken{branch: "fix-" + wordOne}, taken{branch: "fix-a"}, kindBranch},
		{"message", taken{commits: []commit{{id: "abcdef0123456789", message: wordOne}}}, taken{commits: []commit{{id: "abcdef0123456789", message: "m"}}}, kindMessage},
		{"added line", taken{commits: []commit{{id: "abcdef0123456789", patch: patch}}}, taken{commits: []commit{{id: "abcdef0123456789", patch: patchOf("b", "+ok")}}}, kindAdded},
		{"added path", taken{commits: []commit{{id: "abcdef0123456789", patch: patchOf(wordOne+".txt", "+ok")}}}, taken{commits: []commit{{id: "abcdef0123456789", patch: patchOf("n.txt", "+ok")}}}, kindAddedPath},
		{"stdin", taken{stdin: wordOne}, taken{stdin: "title"}, kindStdin},
	}
	for _, c := range cases {
		if !has(judge(l, nil, c.dirty, nil), c.want, "") {
			t.Errorf("%s: a planted name was not found", c.name)
		}
		if fs := judge(l, nil, c.clean, nil); len(fs) != 0 {
			t.Errorf("%s: a clean input was flagged", c.name)
		}
	}
}

func TestTheMatcherFindsEachShapeOfName(t *testing.T) {
	cases := []struct {
		name    string
		letters bool
		list    string
		text    string
	}{
		{"a multi-word row, spaced", false, wordTwo, "see brindle ostrava here"},
		{"a multi-word row, run together", false, wordTwo, "BrindleOstrava"},
		{"a multi-word row, split by case", false, wordTwo, "brindleOstrava"},
		{"a word glued in lower case, with a count", true, wordGlue, "xxx" + wordGlue + "yyy"},
		{"a hyphen-joined word", false, "brindle-ostrava", "brindle-ostrava"},
		{"a non-ASCII word", false, wordNon, "by zoë krüger"},
		{"a name across a line break", false, wordTwo, "brindle\nostrava"},
	}
	for _, c := range cases {
		l := listOf(t, c.letters, c.list)
		if len(l.hits(c.text)) == 0 {
			t.Errorf("%s: not found", c.name)
		}
		if len(l.hits("an ordinary line of text")) != 0 {
			t.Errorf("%s: a clean line was flagged", c.name)
		}
	}
}

func TestAGluedWordIsNotFoundWithoutACount(t *testing.T) {
	if hs := listOf(t, false, wordGlue).hits("xxx" + wordGlue + "yyy"); len(hs) != 0 {
		t.Fatal("a row with no count found a word glued inside another")
	}
}

func TestFindingsNeverHoldTheName(t *testing.T) {
	l := listOf(t, true, wordOne)
	file := wordOne + "-notes.txt"
	fs := judge(l, nil, taken{tree: true, paths: []string{file}, files: map[string][]byte{file: []byte("a\nb " + wordOne + "\n")}, branch: wordOne, stdin: wordOne}, nil)
	if len(fs) == 0 {
		t.Fatal("nothing found")
	}
	if strings.Contains(strings.ToLower(lines(fs)), wordOne) {
		t.Fatalf("a finding prints the name:\n%s", lines(fs))
	}
	if !strings.Contains(lines(fs), ":2: "+mask) {
		t.Fatalf("a finding lacks its line:\n%s", lines(fs))
	}
}

func TestAnUnusedExceptionIsReported(t *testing.T) {
	l := listOf(t, false, wordOne)
	ex, err := ReadExceptions([]byte("file: a.txt\npattern: used " + wordOne + "\nwhy: a fixture\n\nfile: b.txt\npattern: nothing\nwhy: stale\n"))
	if err != nil {
		t.Fatal(err)
	}
	tk := taken{tree: true, paths: []string{"a.txt"}, files: map[string][]byte{"a.txt": []byte("used " + wordOne)}}
	fs := judge(l, ex, tk, nil)
	if has(fs, kindContent, "") {
		t.Fatal("an exception did not hold")
	}
	if !has(fs, kindUnused, "") || !strings.Contains(lines(fs), "exceptions:5:") {
		t.Fatalf("the unused exception was not reported:\n%s", lines(fs))
	}
}

func TestAnExceptionDoesNotHoldForAPath(t *testing.T) {
	l := listOf(t, false, wordOne)
	ex, err := ReadExceptions([]byte("file: " + wordOne + ".txt\npattern: .*\nwhy: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !has(judge(l, ex, taken{tree: true, paths: []string{wordOne + ".txt"}}, nil), kindPath, "") {
		t.Fatal("an exception held for a path")
	}
}

func TestBadInputsFailClosed(t *testing.T) {
	for name, data := range map[string]string{
		"an empty list":       "",
		"only comments":       "# nothing\n\n",
		"a name in the clear": wordOne + "\n",
		"a short hash":        "abcd\n",
		"a bad count":         hashOf("a") + " x\n",
		"a negative count":    hashOf("a") + " -3\n",
	} {
		l, err := ReadList([]byte(data))
		if err == nil || l != nil {
			t.Errorf("%s: read", name)
			continue
		}
		if strings.Contains(err.Error(), wordOne) {
			t.Errorf("%s: the error holds the line", name)
		}
	}
	if _, err := ReadList([]byte("\xef\xbb\xbf" + hashOf("a") + " 1 one\n")); err != nil {
		t.Errorf("a list with a byte-order mark: %v", err)
	}
	if _, err := ReadExceptions([]byte("file: a\nwhy: x\n")); err == nil {
		t.Error("an exception with neither pattern nor what was read")
	}
	if _, err := ReadExceptions([]byte("file: a\npattern: (\nwhy: x\n")); err == nil {
		t.Error("an exception with a bad pattern was read")
	}
	if _, err := HashLines("  ", false); err == nil {
		t.Error("a name of no word was hashed")
	}
	if _, err := HashLines("a b c d e", false); err == nil {
		t.Error("a name of five words was hashed")
	}
}

func TestTheProofRuns(t *testing.T) {
	if err := prove(); err != nil {
		t.Fatalf("prove: %v", err)
	}
}

// gitT runs git in dir through go-proc, as the code under test does, with an
// identity given by -c since the run holds no home. Git's automatic gc and
// maintenance are off: a commit would otherwise leave a detached process
// holding the output, which go-proc rightly reports. It fails the test on a
// non-zero exit.
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

// repo is a git repository of the test, with a commit on main.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "clean\n")
	gitT(t, dir, "add", "a.txt")
	gitT(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func write(t *testing.T, dir, name string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", msg)
}

// runNames runs hatch names on args and stdin, and fails when anything it
// printed holds a word.
func runNames(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	all := out.String() + errb.String()
	for _, w := range []string{wordOne, wordGlue} {
		if strings.Contains(strings.ToLower(all), w) {
			t.Fatalf("a run printed a name:\n%s", all)
		}
	}
	return code, all
}

func listFile(t *testing.T, ns ...string) string {
	t.Helper()
	var b strings.Builder
	for _, n := range ns {
		ls, _ := HashLines(n, true)
		b.WriteString(strings.Join(ls, "\n") + "\n")
	}
	p := filepath.Join(t.TempDir(), "list")
	write(t, filepath.Dir(p), "list", b.String())
	return p
}

func TestARunOnARepositoryFindsEachSurface(t *testing.T) {
	list := listFile(t, wordOne)
	dir := repo(t)
	if code, out := runNames(t, "", "--list", list, "--dir", dir, "--tree", "--branch", "--base", "HEAD"); code != 0 {
		t.Fatalf("a clean repository: exit %d\n%s", code, out)
	}
	write(t, dir, wordOne+".txt", "body\n")
	write(t, dir, "b.txt", "line one\nsee "+wordOne+"\n")
	commitAll(t, dir, "fix "+wordOne)
	code, out := runNames(t, "", "--list", list, "--dir", dir, "--tree", "--base", "HEAD~1")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	for _, want := range []string{"b.txt:2: <name>", "in its path", "message:1: <name>", "in a line it adds", "in the path it writes"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if code, out := runNames(t, wordOne+"\n", "--list", list, "--stdin"); code != 1 || !strings.Contains(out, "stdin:1: <name>") {
		t.Fatalf("stdin: exit %d\n%s", code, out)
	}
	if code, out := runNames(t, "a title\n", "--list", list, "--stdin"); code != 0 {
		t.Fatalf("clean stdin: exit %d\n%s", code, out)
	}
}

func TestARunFindsAPlantedBranchAndAnAddress(t *testing.T) {
	list := listFile(t, wordOne)
	dir := repo(t)
	gitT(t, dir, "switch", "-q", "-c", "fix-"+wordOne)
	if code, out := runNames(t, "", "--list", list, "--dir", dir, "--branch"); code != 1 || !strings.Contains(out, "branch: <name>") {
		t.Fatalf("branch: exit %d\n%s", code, out)
	}
	if code, out := runNames(t, "", "--list", list, "--dir", dir, "--base", "HEAD", "--addresses", `.+@users\.noreply\.example\.invalid`); code != 0 {
		t.Fatalf("no commit past the base: exit %d\n%s", code, out)
	}
	if code, out := runNames(t, "", "--list", list, "--dir", dir, "--base", "HEAD~0"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestAnAddressThatDoesNotMatchIsFound(t *testing.T) {
	list := listFile(t, wordOne)
	dir := repo(t)
	write(t, dir, "c.txt", "x\n")
	commitAll(t, dir, "second")
	code, out := runNames(t, "", "--list", list, "--dir", dir, "--base", "HEAD~1", "--addresses", `.+@users\.noreply\.example\.invalid`)
	if code != 1 || !strings.Contains(out, "does not match --addresses") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, out := runNames(t, "", "--list", list, "--dir", dir, "--base", "HEAD~1", "--addresses", `t@example\.invalid`); code != 0 {
		t.Fatalf("a matching address: exit %d\n%s", code, out)
	}
}

func TestOutsideTreeRefusesAListInTheTree(t *testing.T) {
	dir := repo(t)
	ls, _ := HashLines(wordOne, false)
	write(t, dir, "list", strings.Join(ls, "\n")+"\n")
	list := filepath.Join(dir, "list")
	code, out := runNames(t, "", "--list", list, "--outside-tree", "--dir", dir, "--branch")
	if code != 2 || !strings.Contains(out, "inside the work tree") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, out := runNames(t, "", "--list", list, "--dir", dir, "--branch"); code != 0 {
		t.Fatalf("a list in the tree without the flag: exit %d\n%s", code, out)
	}
	outside := listFile(t, wordOne)
	if code, out := runNames(t, "", "--list", outside, "--outside-tree", "--dir", dir, "--branch"); code != 0 {
		t.Fatalf("a list outside the tree: exit %d\n%s", code, out)
	}
}

func TestARunFailsClosed(t *testing.T) {
	list := listFile(t, wordOne)
	dir := repo(t)
	empty := t.TempDir()
	cases := map[string][]string{
		"no list given":             {"--tree"},
		"no surface":                {"--list", list},
		"a list that is absent":     {"--list", filepath.Join(t.TempDir(), "none"), "--tree", "--dir", dir},
		"a folder in no repository": {"--list", list, "--tree", "--dir", empty},
		"a base that is no commit":  {"--list", list, "--base", "nope", "--dir", dir},
		"an exceptions file absent": {"--list", list, "--tree", "--dir", dir, "--exceptions", filepath.Join(t.TempDir(), "none")},
		"a bad address pattern":     {"--list", list, "--tree", "--dir", dir, "--addresses", "("},
		"an argument":               {"--list", list, "--tree", "extra"},
		"addresses with no base":    {"--list", list, "--tree", "--dir", dir, "--addresses", "a@example\\.invalid"},
		"addresses on stdin alone":  {"--list", list, "--stdin", "--addresses", "a@example\\.invalid"},
	}
	for name, args := range cases {
		if code, out := runNames(t, "", args...); code != 2 {
			t.Errorf("%s: exit %d, want 2\n%s", name, code, out)
		}
	}
	if code, _ := runNames(t, "  \n", "--list", list, "--stdin"); code != 2 {
		t.Errorf("empty stdin: exit %d, want 2", code)
	}
	t.Setenv("PATH", "")
	if code, _ := runNames(t, "", "--list", list, "--tree", "--dir", dir); code != 3 {
		t.Errorf("no git: exit %d, want 3", code)
	}
}

func TestHashReadsStdinAndNeverAnArgument(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"hash"}, strings.NewReader(wordTwo+"\n"), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if n := strings.Count(out.String(), "\n"); n != 2 {
		t.Fatalf("%d lines for a two-word name, want 2:\n%s", n, out.String())
	}
	if strings.Contains(strings.ToLower(out.String()), "brindle") {
		t.Fatal("hash printed the name")
	}
	out.Reset()
	if code := Run(context.Background(), []string{"hash", wordOne}, strings.NewReader(""), &out, &errb); code != 2 || out.Len() != 0 {
		t.Fatalf("a name as an argument: exit %d", code)
	}
}

// The history refusals: each is a repository the test builds, and the run
// ends red with its own exit code and no name in the output. What it costs when
// one fails: a later change that drops it passes a scan of a history git shows
// other than the one a push sends.
func TestAHistoryGitShowsOtherwiseIsRefused(t *testing.T) {
	list := listFile(t, wordOne)
	two := func() (string, string) {
		dir := repo(t)
		write(t, dir, "b.txt", "second\n")
		commitAll(t, dir, "second")
		return dir, strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	}
	t.Run("a replace ref", func(t *testing.T) {
		dir, head := two()
		gitT(t, dir, "replace", head, strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD~1")))
		code, out := runNames(t, "", "--list", list, "--dir", dir, "--base", "HEAD~1")
		if code != 1 || !strings.Contains(out, "refs/replace/") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
	t.Run("a grafts file", func(t *testing.T) {
		dir, _ := two()
		p := filepath.Join(dir, ".git", "info", "grafts")
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out := runNames(t, "", "--list", list, "--dir", dir, "--base", "HEAD~1")
		if code != 1 || !strings.Contains(out, "info/grafts") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
	t.Run("a shallow clone", func(t *testing.T) {
		dir, _ := two()
		u := filepath.ToSlash(dir)
		if !strings.HasPrefix(u, "/") {
			u = "/" + u
		}
		clone := filepath.Join(t.TempDir(), "clone")
		gitT(t, filepath.Dir(clone), "clone", "-q", "--depth", "1", "file://"+u, clone)
		code, out := runNames(t, "", "--list", list, "--dir", clone, "--base", "HEAD")
		if code != 1 || !strings.Contains(out, "shallow") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
	t.Run("a head on no branch", func(t *testing.T) {
		dir, _ := two()
		gitT(t, dir, "checkout", "-q", "--detach")
		if code, out := runNames(t, "", "--list", list, "--dir", dir, "--branch"); code != 2 {
			t.Errorf("exit %d, want 2: %s", code, out)
		}
	})
	t.Run("a log that is not git's count", func(t *testing.T) {
		if err := sameCount(2, 2); err != nil {
			t.Errorf("equal counts: %v", err)
		}
		if err := sameCount(1, 2); err == nil {
			t.Error("a log of 1 against a count of 2 was no error")
		}
	})
}

// gitOut is what git prints in dir.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	out, err := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}.Out(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", args[0], err)
	}
	return out
}

// An empty file whose path holds a name, added by a commit past the base and removed by a later one, is read:
// git prints no line of it in the patch, and the path was published all the same.
//
// What it costs when this fails: a name in the path of a .gitkeep goes out in a push the scan passed.
func TestAnEmptyFileWhosePathHoldsANameIsFoundInTheHistory(t *testing.T) {
	list := listFile(t, wordOne)
	dir := repo(t)
	if err := os.Mkdir(filepath.Join(dir, wordOne), 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, dir, wordOne+"/.gitkeep", "")
	commitAll(t, dir, "an empty folder")
	gitT(t, dir, "rm", "-q", "-r", wordOne)
	gitT(t, dir, "commit", "-q", "-m", "gone again")
	for _, args := range [][]string{
		{"--base", "HEAD~2"},
		{"--tree", "--base", "HEAD~2"},
	} {
		code, out := runNames(t, "", append([]string{"--list", list, "--dir", dir}, args...)...)
		if code != 1 || !strings.Contains(out, "in the path it writes") {
			t.Errorf("%v: exit %d, want 1 and a path it writes\n%s", args, code, out)
		}
	}
}

// A tracked file that is gone from disk and still in git is a surface that cannot be read: exit 2, with its path
// masked, never a pass for the body HEAD holds.
//
// What it costs when this fails: deleting a file from the work tree hides its committed name from --tree.
func TestATrackedFileGoneFromDiskIsAnUnreadSurface(t *testing.T) {
	list := listFile(t, wordOne)
	dir := repo(t)
	write(t, dir, wordOne+".md", "see "+wordOne+"\n")
	commitAll(t, dir, "a body")
	if code, _ := runNames(t, "", "--list", list, "--dir", dir, "--tree"); code != 1 {
		t.Fatalf("the file on disk: exit %d, want 1", code)
	}
	if err := os.Remove(filepath.Join(dir, wordOne+".md")); err != nil {
		t.Fatal(err)
	}
	code, out := runNames(t, "", "--list", list, "--dir", dir, "--tree")
	if code != 2 || !strings.Contains(out, "gone from disk") || !strings.Contains(out, "<name>") {
		t.Errorf("the file gone: exit %d, want 2 and its masked path\n%s", code, out)
	}
}

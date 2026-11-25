package writes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

// gitIn runs git in dir and fails the test when it fails.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	if res, err := g.Run(context.Background(), dir, args...); err != nil || res.Code != 0 {
		t.Fatalf("git %s failed: %v\n%s", args[0], err, res.Stderr)
	}
}

// TestMain lets this test binary stand in for the command a row runs: "act <op>..." does each op in the working
// folder, in order, where an op is write:<path> (a new file, or the file's text made another), remove:<path>,
// link:<path>|<to> (the bar, not a colon: a Windows path holds one) (a hard link) or exit:<n>.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "act" {
		os.Exit(act(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func act(ops []string) int {
	code := 0
	for _, op := range ops {
		verb, arg, _ := strings.Cut(op, ":")
		var err error
		switch verb {
		case "write":
			err = os.MkdirAll(filepath.Dir(arg), 0o700)
			if err == nil {
				err = os.WriteFile(arg, []byte("written by act\n"), 0o600)
			}
		case "remove":
			err = os.Remove(arg)
		case "link":
			from, to, _ := strings.Cut(arg, "|")
			err = os.Link(from, to)
		case "exit":
			code, err = strconv.Atoi(arg)
		case "say":
			fmt.Println(arg)
		default:
			err = fmt.Errorf("no op %q", op)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "act:", err)
			return 99
		}
	}
	return code
}

// tree is a folder of plain files, one for each name, each holding its own name.
func tree(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(n+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// hold runs hatch writes on dir as the working folder of the command, which is this test binary acting, and
// answers both streams and the exit code.
func hold(t *testing.T, dir string, flags []string, ops ...string) (string, string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	args := append(append([]string{"--root", dir}, flags...), "--", exe, "act")
	// The command runs in this process's folder, as any command of hatch does: its ops name paths under dir.
	for i := range ops {
		verb, arg, _ := strings.Cut(ops[i], ":")
		switch verb {
		case "write", "remove":
			ops[i] = verb + ":" + filepath.Join(dir, arg)
		case "link":
			from, to, _ := strings.Cut(arg, "|")
			ops[i] = "link:" + filepath.Join(dir, from) + "|" + filepath.Join(dir, to)
		}
	}
	code := Run(context.Background(), append(args, ops...), &out, &errs)
	return out.String(), errs.String(), code
}

// A command that writes a file, changes one, removes one, writes a file git ignores or writes nothing is
// named by what it did, and only the first four are red; a command that fails keeps its own code and the list
// of what it wrote is printed beside it.
//
// What it costs when this fails: a command that writes into the tree passes as one that wrote nothing, or a
// failing command hides what it wrote.
func TestTheCommandIsHeldToWritingNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		files []string
		ops   []string
		code  int
		out   string // every line of it, in order
		err   string
	}{
		{"writes nothing", []string{"a.txt"}, []string{"say:hello"}, 0, "hello\nok: ", ""},
		{"writes a file", []string{"a.txt"}, []string{"write:new.txt"}, 1, "added new.txt\n", "wrote 1 paths"},
		{"writes a file in a folder that is new", []string{"a.txt"}, []string{"write:d/e/new.txt"}, 1, "added d\nadded d/e\nadded d/e/new.txt\n", "wrote 3 paths"},
		{"changes a file", []string{"a.txt"}, []string{"write:a.txt"}, 1, "changed a.txt\n", "wrote 1 paths"},
		{"removes a file", []string{"a.txt", "b.txt"}, []string{"remove:a.txt"}, 1, "removed a.txt\n", "wrote 1 paths"},
		{"writes a file git ignores", []string{".gitignore", "a.txt"}, []string{"write:out/x.bin"}, 1, "added out\nadded out/x.bin\n", "wrote 2 paths"},
		{"writes and removes, in the order of the paths", []string{"a.txt", "z.txt"}, []string{"write:m.txt", "remove:z.txt", "write:a.txt"}, 1, "changed a.txt\nadded m.txt\nremoved z.txt\n", "wrote 3 paths"},
		{"fails and writes", []string{"a.txt"}, []string{"write:new.txt", "exit:3"}, 3, "added new.txt\n", ""},
		{"fails and writes nothing", []string{"a.txt"}, []string{"exit:7"}, 7, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := tree(t, c.files...)
			if slices.Contains(c.files, ".gitignore") {
				gitIn(t, dir, "init", "-q")
				if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("out/\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out, errs, code := hold(t, dir, nil, c.ops...)
			if code != c.code || !strings.HasPrefix(out, c.out) || !strings.Contains(errs, c.err) {
				t.Errorf("exit %d, want %d; stdout %q, want it to start %q; stderr %q, want %q", code, c.code, out, c.out, errs, c.err)
			}
			if c.code == 0 && (errs != "" || strings.Count(out, "\n") != 2) {
				t.Errorf("a pass is the command's own line and one of ours: out %q, err %q", out, errs)
			}
			if c.code == 1 && strings.Contains(out, "ok: ") {
				t.Errorf("a red run says ok: %q", out)
			}
		})
	}
}

// A file written again as it was is no write: the listing holds no mode and no time, as treesum's README says,
// and hatch writes judges by the listing alone.
//
// What it costs when this fails: a command that only rewrites a file as it was is red for what no listing can tell.
func TestARewriteOfTheSameContentIsNoWrite(t *testing.T) {
	t.Parallel()
	dir := tree(t, "a.txt")
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("written by act\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := hold(t, dir, nil, "write:same.txt"); code != 0 || !strings.Contains(out, "ok: ") {
		t.Errorf("a file written as it was: exit %d, out %q, err %q", code, out, errs)
	}
}

// What treesum refuses is a red that names the path and is never a skip: a file with a second name, before the
// command (which is then not run) and after it, and a link out of the tree. A tree over its caps is refused
// and the flags that raise them are named.
//
// What it costs when this fails: a tree the walk could not judge passes as one that was held.
func TestWhatTreesumRefusesIsARedThatNamesThePath(t *testing.T) {
	t.Parallel()
	t.Run("a hard link before the command, which is not run", func(t *testing.T) {
		t.Parallel()
		dir := tree(t, "a.txt")
		if err := os.Link(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
			t.Skip("no hard links here:", err)
		}
		out, errs, code := hold(t, dir, nil, "write:ran.txt")
		if code != 1 || out != "" || !strings.Contains(errs, "and the command was not run") || !strings.Contains(errs, "a.txt") {
			t.Errorf("exit %d, out %q, err %q; want exit 1, no output, and the refusal naming a.txt", code, out, errs)
		}
		if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err == nil {
			t.Error("the command ran after a refusal of the tree")
		}
	})
	t.Run("a hard link made by the command", func(t *testing.T) {
		t.Parallel()
		dir := tree(t, "a.txt")
		out, errs, code := hold(t, dir, nil, "link:a.txt|b.txt")
		if code != 1 || out != "" || !strings.Contains(errs, "refused after the command") || !strings.Contains(errs, "a.txt") {
			t.Errorf("exit %d, out %q, err %q; want exit 1 and the refusal naming a.txt", code, out, errs)
		}
	})
	t.Run("a refusal after a failing command keeps its code", func(t *testing.T) {
		t.Parallel()
		out, errs, code := hold(t, tree(t, "a.txt"), nil, "link:a.txt|b.txt", "exit:5")
		if code != 5 || out != "" || !strings.Contains(errs, "refused after the command") {
			t.Errorf("exit %d, out %q, err %q; want 5", code, out, errs)
		}
	})
	t.Run("a link out of the tree", func(t *testing.T) {
		t.Parallel()
		dir := tree(t, "a.txt")
		if err := os.Symlink(filepath.Join("..", "outside"), filepath.Join(dir, "up")); err != nil {
			t.Skip("no symbolic links here:", err)
		}
		out, errs, code := hold(t, dir, nil, "say:x")
		if code != 1 || out != "" || !strings.Contains(errs, "up") || !strings.Contains(errs, "the command was not run") {
			t.Errorf("exit %d, out %q, err %q; want the refusal naming up", code, out, errs)
		}
	})
	t.Run("a tree over the caps", func(t *testing.T) {
		t.Parallel()
		out, errs, code := hold(t, tree(t, "a.txt", "b.txt", "c.txt"), []string{"--max-entries", "2"}, "say:x")
		if code != 1 || out != "" || !strings.Contains(errs, "--max-entries") {
			t.Errorf("exit %d, out %q, err %q; want the refusal naming the flags", code, out, errs)
		}
		out, errs, code = hold(t, tree(t, "a.txt"), []string{"--max-bytes", "1"}, "say:x")
		if code != 1 || out != "" || !strings.Contains(errs, "--max-bytes") {
			t.Errorf("exit %d, out %q, err %q; want the refusal naming the flags", code, out, errs)
		}
	})
}

// With no --root the root is the top of the git checkout the working folder is in: a command run in a folder
// below it that writes beside it is named by its path from the top.
//
// What it costs when this fails: a command run from a subfolder is held to that folder alone, and what it writes
// elsewhere in the checkout passes.
func TestTheDefaultRootIsTheTopOfTheCheckout(t *testing.T) {
	top := tree(t, "a.txt", "sub/b.txt")
	gitIn(t, top, "init", "-q")
	t.Chdir(filepath.Join(top, "sub"))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := Run(context.Background(), []string{"--", exe, "act", "write:../new.txt"}, &out, &errs)
	// git's own folder is in the tree and git init wrote it before the first listing: the new file is the one write.
	if code != 1 || !strings.HasPrefix(out.String(), "added new.txt\n") {
		t.Errorf("exit %d, out %q, err %q; want exit 1 and added new.txt from the top of the checkout", code, out.String(), errs.String())
	}
	out.Reset()
	errs.Reset()
	notRepo := t.TempDir()
	t.Chdir(notRepo)
	if code := Run(context.Background(), []string{"--", exe, "act", "say:x"}, &out, &errs); code != 2 || !strings.Contains(errs.String(), "give --root") {
		t.Errorf("outside a checkout: exit %d, err %q; want 2 and --root named", code, errs.String())
	}
}

// A form of no usage is exit 2: no command, an unknown flag, a cap or a deadline of zero, a command that is not on
// PATH, and a root that is not a folder. None of them runs the command.
//
// What it costs when this fails: a mistake in the arguments runs a command with no hold on it.
func TestAFormOfNoUsageIsExit2(t *testing.T) {
	t.Parallel()
	dir := tree(t, "a.txt")
	for _, args := range [][]string{
		{"--root", dir},
		{"--root", dir, "--"},
		{"--root", dir, "--nope", "--", "x"},
		{"--root", dir, "--max-entries", "0", "--", "x"},
		{"--root", dir, "--max-bytes", "-1", "--", "x"},
		{"--root", dir, "--timeout", "0s", "--", "x"},
		{"--root", dir, "--exclude", "", "--", "x"},
		{"--root", dir, "--exclude", " ", "--", "x"},
		{"--root", dir, "--exclude", "a/b", "--", "x"},
		{"--root", dir, "--exclude", `a\b`, "--", "x"},
		{"--root", dir, "--exclude"},
		{"--root", dir, "--", "hatch-writes-no-such-program"},
		{"--root", filepath.Join(dir, "a.txt"), "--", os.Args[0]},
		{"--root", filepath.Join(dir, "missing"), "--", os.Args[0]},
	} {
		var out, errs bytes.Buffer
		if code := Run(context.Background(), args, &out, &errs); code != 2 || out.Len() != 0 || errs.Len() == 0 {
			t.Errorf("%v: exit %d, out %q, err %q; want exit 2 and a reason on stderr", args, code, out.String(), errs.String())
		}
	}
}

// The usage names every flag and the exit codes.
//
// What it costs when this fails: a flag the command takes is in no text a person reads.
func TestTheUsageNamesEachFlagAndExitCode(t *testing.T) {
	t.Parallel()
	for _, w := range []string{"--root", "--exclude", "--max-entries", "--max-bytes", "--timeout", "added <path>", "changed <path>", "removed <path>", "exit codes of hatch writes", "the command's own"} {
		if !strings.Contains(usage, w) {
			t.Errorf("the usage lacks %q", w)
		}
	}
}

// pnpm is a tree whose node_modules holds a file with a second name, as a pnpm store links its packages.
func pnpm(t *testing.T) string {
	t.Helper()
	dir := tree(t, "a.txt", "node_modules/x/p.js")
	if err := os.Link(filepath.Join(dir, "node_modules", "x", "p.js"), filepath.Join(dir, "node_modules", "x", "q.js")); err != nil {
		t.Skip("no hard links here:", err)
	}
	return dir
}

// A folder named by --exclude, at any depth, is skipped inside the walk: a hard link under it is no refusal, which the
// same tree without the flag is, and a write under it is no write.
//
// What it costs when this fails: a tree with a pnpm node_modules cannot be held at all, or a command that writes
// into a folder the caller left out is red for it.
func TestAnExcludedNameIsLeftOutOfTheWalkAtAnyDepth(t *testing.T) {
	t.Parallel()
	t.Run("a hard link under it is green with the flag and red without", func(t *testing.T) {
		t.Parallel()
		out, errs, code := hold(t, pnpm(t), []string{"--exclude", "node_modules"}, "say:x")
		if code != 0 || !strings.Contains(out, "ok: ") || errs != "" {
			t.Errorf("with the flag: exit %d, out %q, err %q; want green", code, out, errs)
		}
		out, errs, code = hold(t, pnpm(t), nil, "say:x")
		if code != 1 || out != "" || !strings.Contains(errs, "the command was not run") || !strings.Contains(errs, "node_modules") {
			t.Errorf("without the flag: exit %d, out %q, err %q; want the refusal naming node_modules", code, out, errs)
		}
	})
	t.Run("a write under it, at the top or deeper, is no write", func(t *testing.T) {
		t.Parallel()
		dir := tree(t, "a.txt", "node_modules/x/p.js", "pkg/node_modules/y.js")
		out, errs, code := hold(t, dir, []string{"--exclude", "node_modules"}, "write:node_modules/x/p.js", "write:node_modules/new.js", "write:pkg/node_modules/y.js", "write:pkg/node_modules/z.js")
		if code != 0 || !strings.Contains(out, "ok: ") || errs != "" {
			t.Errorf("exit %d, out %q, err %q; want green", code, out, errs)
		}
	})
	t.Run("a write beside it is still named", func(t *testing.T) {
		t.Parallel()
		out, errs, code := hold(t, pnpm(t), []string{"--exclude", "node_modules"}, "write:node_modules/x/new.js", "write:new.txt")
		if code != 1 || !strings.HasPrefix(out, "added new.txt\n") || strings.Contains(out, "node_modules") || !strings.Contains(errs, "wrote 1 paths") {
			t.Errorf("exit %d, out %q, err %q; want only new.txt named", code, out, errs)
		}
	})
	t.Run("the flag repeats, and a name that is a file is left out too", func(t *testing.T) {
		t.Parallel()
		dir := tree(t, "a.txt", "dist/b.js", "notes.log")
		out, errs, code := hold(t, dir, []string{"--exclude", "dist", "--exclude", "notes.log"}, "write:dist/c.js", "write:notes.log")
		if code != 0 || !strings.Contains(out, "ok: ") || errs != "" {
			t.Errorf("exit %d, out %q, err %q; want green", code, out, errs)
		}
	})
}

// An exclude that matches nothing changes nothing: a write is named as it is with no flag, and a write that
// leaves the tree as it was is green as before.
//
// What it costs when this fails: giving a name that is not there hides a write elsewhere.
func TestAnExcludeThatMatchesNothingChangesNothing(t *testing.T) {
	t.Parallel()
	flags := []string{"--exclude", "no-such-name"}
	out, errs, code := hold(t, tree(t, "a.txt"), flags, "write:new.txt", "write:a.txt")
	if code != 1 || !strings.HasPrefix(out, "changed a.txt\nadded new.txt\n") || !strings.Contains(errs, "wrote 2 paths") {
		t.Errorf("exit %d, out %q, err %q; want the same two paths as with no flag", code, out, errs)
	}
	out, errs, code = hold(t, tree(t, "a.txt"), flags, "say:x")
	if code != 0 || !strings.Contains(out, "ok: ") || errs != "" {
		t.Errorf("a command that writes nothing: exit %d, out %q, err %q; want green", code, out, errs)
	}
	if _, _, code = hold(t, pnpm(t), flags, "say:x"); code != 1 {
		t.Errorf("a hard link under node_modules with another name excluded: exit %d, want 1", code)
	}
}

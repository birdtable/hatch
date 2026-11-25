package names

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/birdtable/go-files/trust"
)

// refusal runs hatch names on stdin with the flag given the file at path, and
// is its exit code and output. A run of a file that cannot be read safely
// exits 2 and names the flag and a reason, and never the path.
func refusal(t *testing.T, flag, path string) {
	t.Helper()
	args := []string{"--list", listFile(t, wordOne), "--stdin"}
	if flag == "--list" {
		args = []string{"--list", path, "--stdin"}
	} else {
		args = append(args, flag, path)
	}
	code, out := runNames(t, "clean text\n", args...)
	if code != 2 {
		t.Errorf("%s: exit %d, want 2\n%s", flag, code, out)
	}
	if !strings.Contains(out, flag+":") {
		t.Errorf("%s: the output does not name the flag:\n%s", flag, out)
	}
	for _, p := range []string{path, filepath.Dir(path), filepath.Base(filepath.Dir(path))} {
		if strings.Contains(out, p) {
			t.Errorf("%s: the output holds a path (%q):\n%s", flag, p, out)
		}
	}
}

func eachFlag(t *testing.T, f func(t *testing.T, flag string)) {
	for _, flag := range []string{"--list", "--exceptions"} {
		t.Run(flag, func(t *testing.T) { f(t, flag) })
	}
}

// body is a file each flag reads: a list line is a hash, an exceptions stanza
// is not, so the file a test plants is a list for --list and a refusal before
// it is read for the other.
func plant(t *testing.T, flag string) string {
	t.Helper()
	if flag == "--list" {
		return listFile(t, wordOne)
	}
	p := filepath.Join(t.TempDir(), "ex")
	write(t, filepath.Dir(p), "ex", "file: a.txt\nwhat: x\nwhy: a test\n")
	return p
}

func TestAListOverTheLimitIsRefusedWhole(t *testing.T) {
	eachFlag(t, func(t *testing.T, flag string) {
		p := filepath.Join(t.TempDir(), "big")
		write(t, filepath.Dir(p), "big", strings.Repeat("# filler\n", maxFile/9+1))
		if fi, err := os.Stat(p); err != nil || fi.Size() <= maxFile {
			t.Fatalf("the plant is no longer than the limit: %v", err)
		}
		refusal(t, flag, p)
	})
}

func TestAMissingListIsRefused(t *testing.T) {
	eachFlag(t, func(t *testing.T, flag string) {
		refusal(t, flag, filepath.Join(t.TempDir(), "none"))
	})
}

func TestAListTheAccountOwnsIsRead(t *testing.T) {
	eachFlag(t, func(t *testing.T, flag string) {
		p := plant(t, flag)
		args := []string{"--list", listFile(t, wordOne), "--stdin"}
		if flag == "--list" {
			args = []string{"--list", p, "--stdin"}
		} else {
			args = append(args, flag, p)
		}
		if code, out := runNames(t, "clean text\n", args...); code != 0 {
			t.Errorf("exit %d, want 0\n%s", code, out)
		}
	})
}

// The owner the list must have is the running account's: a file that account
// owns, judged for another account, is refused as not owned, with no root.
//
// What it costs when this fails: the owner check is never run on a system
// where the tests do not run as root, as the row that needs chown is skipped
// there, and a list another account owns could be read.
func TestAListOwnedByTheRunningAccountIsRefusedForAnotherOwner(t *testing.T) {
	me, err := trust.Account()
	if err != nil || me == "" {
		t.Skip("the running account cannot be told")
	}
	other := "S-1-5-21-1111111111-2222222222-3333333333-1001"
	if id, ok := strings.CutPrefix(me, "uid:"); ok {
		n, err := strconv.Atoi(id)
		if err != nil {
			t.Fatalf("the account %q is not a uid: %v", me, err)
		}
		if n == 0 {
			t.Skip("root's files are the system's, which is read as a second owner: run as another account")
		}
		other = "uid:" + strconv.Itoa(n+1)
	}
	was := account
	account = func() (string, error) { return other, nil }
	t.Cleanup(func() { account = was })
	// The swap is the whole process's, so the list is refused first and the
	// exceptions file is never reached: the row is the list's.
	p := plant(t, "--list")
	refusal(t, "--list", p)
	code, out := runNames(t, "clean text\n", "--list", p, "--stdin")
	if code != 2 || !strings.Contains(out, "is not owned by the running account or by the system") {
		t.Errorf("exit %d, want 2 with the not-owned reason:\n%s", code, out)
	}
}

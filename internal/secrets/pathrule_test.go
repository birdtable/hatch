package secrets

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/birdtable/hatch/internal/gitrun"
)

// A rule that reads a file's path alone fires staged as it does in a range, a
// file git calls binary included, and the staged scan moves no ref, no index
// and no work tree: it reads a temporary commit object of the index.
//
// What it costs when this fails: a private key container (a .p12) staged for
// a commit passes a pre-commit hook and is red only after the commit, or a
// pre-commit scan changes what the next commit holds.
func TestAPathRuleFiresStagedAsInARange(t *testing.T) {
	t.Parallel()
	bin := realGitleaks(t)
	blob := make([]byte, 300)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	text := append([]byte{}, blob...)
	for i, c := range text {
		if c == 0 {
			text[i] = 1
		}
	}
	// One NUL byte makes git call the container binary.
	binary := append([]byte{0}, text...)
	run := func(dir string, args ...string) (int, string) {
		code, out, errb := runSecrets(t, append([]string{"--gitleaks", bin, "--dir", dir}, args...)...)
		return code, out + errb
	}
	for name, body := range map[string]string{"a text container": string(text), "a container with a NUL byte": string(binary)} {
		committed := repo(t, map[string]string{"a.txt": "nothing here\n"})
		commit(t, committed, "container", map[string]string{"cert.p12": body})
		if code, got := run(committed, "--range", "HEAD~1..HEAD"); code != 1 {
			t.Errorf("%s in a range: exit %d, want 1:\n%s", name, code, got)
		}
		staged := repo(t, map[string]string{"a.txt": "nothing here\n"})
		if err := os.WriteFile(filepath.Join(staged, "cert.p12"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		gitT(t, staged, "add", "cert.p12")
		state := func() string {
			return gitOut(t, staged, "for-each-ref") + gitOut(t, staged, "status", "--porcelain") + gitOut(t, staged, "ls-files", "-s") + gitOut(t, staged, "rev-parse", "HEAD")
		}
		before := state()
		if code, got := run(staged, "--staged"); code != 1 {
			t.Errorf("%s staged: exit %d, want 1:\n%s", name, code, got)
		}
		if after := state(); after != before {
			t.Errorf("%s staged: the scan moved a ref, the index or the work tree:\n%s\n%s", name, before, after)
		}
	}
}

// gitOut is what git prints for args in dir.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	p, err := gitrun.Find()
	if err != nil {
		t.Skip("git is not on PATH")
	}
	g := gitrun.Git{Path: p, Timeout: time.Minute, MaxOutput: 1 << 20}
	res, err := g.Run(context.Background(), dir, args...)
	if err != nil || res.Code != 0 {
		t.Fatalf("git %s failed: %v\n%s", args[0], err, res.Stderr)
	}
	return string(res.Stdout)
}

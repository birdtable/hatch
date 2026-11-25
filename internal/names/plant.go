package names

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPlant is a proof that failed: the scan could not find a name planted for
// it, so a clean result would prove nothing. The error says which surface and
// never holds the planted word.
var ErrPlant = errors.New("the scan did not find a planted name")

// plantWord is a word made now from random letters, so no run of the scan
// can be tuned to a fixed one. It is lower case ASCII and 12 letters long.
func plantWord() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("no randomness for the plants: %w", err)
	}
	for i := range b {
		b[i] = 'a' + b[i]%26
	}
	return string(b), nil
}

// prove plants a name, in memory, on each surface judge reads, and fails
// when the scan misses one or flags the clean input beside it. It runs
// before every scan, so a matcher that went blind is red, not green. It
// builds its own list: nothing it holds comes from the caller's.
func prove() error {
	w, err := plantWord()
	if err != nil {
		return err
	}
	// The planted row carries a count so the glued rule is proved too.
	l, err := ReadList([]byte(hashOf(w) + " 12 plant\n"))
	if err != nil {
		return errors.New("the plant list was refused")
	}
	upper := strings.ToUpper(w[:1]) + w[1:]
	cases := []struct {
		surface string
		taken   taken
		want    kind
	}{
		{"tree", taken{tree: true, paths: []string{"a.txt"}, files: map[string][]byte{"a.txt": []byte("x\n" + w + "\n")}}, kindContent},
		{"tree, cased", taken{tree: true, paths: []string{"a.txt"}, files: map[string][]byte{"a.txt": []byte("see " + upper + " now\n")}}, kindContent},
		{"tree, glued", taken{tree: true, paths: []string{"a.txt"}, files: map[string][]byte{"a.txt": []byte("x" + w + "y\n")}}, kindContent},
		{"path", taken{tree: true, paths: []string{"dir/" + w + ".txt"}}, kindPath},
		{"branch", taken{branch: "feature-" + w}, kindBranch},
		{"message", taken{commits: []commit{{id: "0123456789abcdef", message: "subject\n\nbody " + w + "\n"}}}, kindMessage},
		{"added line", taken{commits: []commit{{id: "0123456789abcdef", patch: patchOf("b.txt", "+"+w)}}}, kindAdded},
		{"added path", taken{commits: []commit{{id: "0123456789abcdef", patch: patchOf(w+".txt", "+clean")}}}, kindAddedPath},
		{"added path of an empty file", taken{commits: []commit{{id: "0123456789abcdef", written: []string{w + "/.gitkeep"}}}}, kindAddedPath},
		{"stdin", taken{stdin: "title\n" + w + "\n"}, kindStdin},
	}
	for _, c := range cases {
		if !has(judge(l, nil, c.taken, nil), c.want, "") {
			return fmt.Errorf("%w: %s", ErrPlant, c.surface)
		}
	}
	// A planted name is found, and a clean text beside it is not.
	clean := taken{
		tree: true, paths: []string{"a.txt"}, files: map[string][]byte{"a.txt": []byte("nothing here\n")},
		branch: "main", stdin: "a title\n",
		commits: []commit{{id: "0123456789abcdef", author: "a@example.invalid", committer: "a@example.invalid", message: "a message\n", patch: patchOf("b.txt", "+clean")}},
	}
	if fs := judge(l, nil, clean, nil); len(fs) > 0 {
		return errors.New("the scan flagged a clean input it was planted beside")
	}
	// An address that does not match is found; one that does is not.
	re, err := regexp.Compile(`^(?:[a-z]+@example\.invalid)$`)
	if err != nil {
		return err
	}
	bad := taken{commits: []commit{{id: "0123456789abcdef", author: "someone@elsewhere.test", committer: "a@example.invalid"}}}
	if !has(judge(l, nil, bad, re), kindAddress, "") {
		return fmt.Errorf("%w: address", ErrPlant)
	}
	return nil
}

// patchOf is a one-file patch that adds line, in the shape git log -p
// prints with the arguments patchArgs gives.
func patchOf(path, line string) string {
	return "diff --git a/" + path + " b/" + path + "\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n" + line + "\n"
}

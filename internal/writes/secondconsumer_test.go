package writes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A second consumer: a project that runs hatch writes on a root, with caps and a deadline nothing like any one
// project's, and a command of its own. It gives hatch every value itself, and each row changes one value and
// reads the output that follows. A row that fails means hatch holds a value of its own where the caller should
// give it, so a project with other values cannot use it.
func TestSecondConsumer(t *testing.T) {
	t.Parallel()
	dir := tree(t, "site/index.html", "site/css/app.css", "notes.txt")
	site := filepath.Join(dir, "site")
	for _, c := range []struct {
		name  string
		root  string
		flags []string
		ops   []string
		code  int
		want  string
	}{
		{"a root of its own, a write below it", site, nil, []string{"write:new.html"}, 1, "added new.html\n"},
		{"a write above the root is not its to see", site, nil, []string{"write:../notes.txt"}, 0, "wrote nothing under " + site},
		{"caps that fit", site, []string{"--max-entries", "4", "--max-bytes", "4096"}, []string{"say:fits"}, 0, "fits\nok: "},
		{"caps one entry short", site, []string{"--max-entries", "2"}, []string{"say:short"}, 1, "--max-entries"},
		{"a deadline of its own", site, []string{"--timeout", "1h"}, []string{"exit:4"}, 4, ""},
	} {
		out, errs, code := hold(t, c.root, c.flags, c.ops...)
		if code != c.code || !strings.Contains(out+errs, c.want) {
			t.Errorf("%s: exit %d, want %d and %q\nstdout: %s\nstderr: %s", c.name, code, c.code, c.want, out, errs)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
}

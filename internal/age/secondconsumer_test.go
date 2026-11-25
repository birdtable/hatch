package age

import (
	"path/filepath"
	"strings"
	"testing"
)

// A second consumer: a project that runs hatch age with a window, a modfile and an exceptions file nothing like
// any one project's. It gives hatch every value itself, and each row changes one value and reads the output
// follow it. A row that fails means hatch holds a value of its own where the caller should give it, so a
// project with other values cannot use it.
func TestSecondConsumer(t *testing.T) {
	fixture(t, nil, []release{{"example.org/widget", "v0.3.1", 15 * day}, {"example.org/gadget", "v1.9.9", 40 * day}})
	pins := filepath.Join("tools", "tool.mod")
	write(t, "ours.cfg", "example.org/widget@v0.3.1 released with the fix our customer waits on\n")
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"a window of 21 days refuses a module 15 days old", []string{"--days", "21", "--modfile", pins}, 1, "example.org/widget@v0.3.1 is 15 days old, under the 21 days: released 2026-11-06T12:00:00Z, allowed from 2026-11-27"},
		{"a window of 14 days allows it", []string{"--days", "14", "--modfile", pins}, 0, "2 modules checked, none refused"},
		{"its own exceptions file, under the window of 21", []string{"--days", "21", "--modfile", pins, "--exceptions", "ours.cfg"}, 0, "excepted: released with the fix our customer waits on"},
		{"a window of 45 days refuses the older one too", []string{"--days", "45", "--modfile", pins}, 1, "example.org/gadget@v1.9.9 is 40 days old, under the 45 days"},
	} {
		code, out, errb := run(c.args...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, want %d and %q\nstdout: %s\nstderr: %s", c.name, code, c.code, c.want, out, errb)
		}
	}
}

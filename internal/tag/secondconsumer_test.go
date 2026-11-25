package tag

import (
	"strings"
	"testing"
)

// A second consumer: a project whose remote is called upstream, whose release branch is release, whose version
// is a pre-release and whose repository is not in the folder it runs from. It gives hatch every value itself,
// and each row changes one value and reads the output follow it. A row that fails means hatch holds a value of
// its own where the caller should give it, so a project with other values cannot use it.
func TestSecondConsumer(t *testing.T) {
	work, _ := repoFor(t, "release")
	gitT(t, work, "remote", "rename", "origin", "upstream")
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"its remote and its branch", []string{"--remote", "upstream", "--branch", "release", "v7.3.0-rc.2"}, 0, "ok: release at"},
		{"the remote it does not have", []string{"--remote", "origin", "--branch", "release", "v7.3.0-rc.2"}, 1, "origin has no release"},
		{"a branch it does not release on", []string{"--remote", "upstream", "--branch", "main", "v7.3.0-rc.2"}, 1, "HEAD is release, not main"},
		{"its snapshot", []string{"--remote", "upstream", "--snapshot"}, 0, "ok: release at"},
	} {
		out, errs, code := guardOn(t, work, c.args...)
		if code != c.code || !strings.Contains(out+errs, c.want) {
			t.Errorf("%s: exit %d, want %d and %q\nstdout: %s\nstderr: %s", c.name, code, c.code, c.want, out, errs)
		}
	}
}

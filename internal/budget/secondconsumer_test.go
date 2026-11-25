package budget

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A second consumer: a project that runs hatch budget with a tier, a limit, a
// machine and a load nothing like any one project's. It gives hatch every value
// itself, and each row changes one value and reads the output follow it. A row
// that fails means hatch holds a value of its own where the caller should give
// it, so a project with other values cannot use it.
func TestSecondConsumer(t *testing.T) {
	const started = 2_000_000_000
	const layout = `^(?P<id>[a-z-]+) = (?P<limit>[0-9]+)\r?$`
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gate"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Its own layout: one id = seconds per line, under a heading, with CRLF line ends.
	file := "[limits]\r\ntier-nightly = 7200\r\ntier-hourly = 90\r\n"
	if err := os.WriteFile(filepath.Join(dir, "gate", "limits.ini"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	type call struct {
		tier   string
		took   int
		before string
		load   float64
		limit  string
		when   string
		window string
	}
	base := call{tier: "nightly", took: 7000, before: "1.5", load: 2, limit: "64", when: "either", window: "5"}
	run := func(c call) (int, string, string) {
		args := []string{"--tier", c.tier, "--since", strconv.Itoa(started - c.took), "--budgets", filepath.Join(dir, "gate", "limits.ini"), "--prefix", "tier-", "--row", layout, "--window", c.window, "--limit", c.limit, "--loaded-when", c.when, "--noun", "the build", "--excused", "load, not the code", "--load-before", c.before}
		var out, errb bytes.Buffer
		code := Run(context.Background(), args, time.Unix(started, 0), fixed(c.load), &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, out, _ := run(base); code != 0 || !strings.Contains(out, "tier-nightly took 7000 s of 7200 s") {
		t.Fatalf("the base call: exit %d, %q", code, out)
	}
	rows := []struct {
		name  string
		edit  func(c *call)
		code  int
		stdio string // in stdout, or in stderr when the run is red
	}{
		{"the tier names its own limit", func(c *call) { c.tier = "hourly"; c.took = 100 }, 1, "tier-hourly is over its budget"},
		{"the time taken is judged against the limit", func(c *call) { c.took = 7300 }, 1, "tier-nightly is over its budget"},
		{"the limit decides what a loaded machine is", func(c *call) { c.took = 7300; c.load = 40; c.limit = "16" }, 0, "load 40 at the end of the build, above the limit of 16"},
		{"the same load under a higher limit is not loaded", func(c *call) { c.took = 7300; c.load = 40; c.limit = "64" }, 1, "tier-nightly is over its budget"},
		{"the load before the tier is the caller's", func(c *call) { c.took = 7300; c.before = "90" }, 0, "load 90 before the build, above the limit of 64"},
		{"only the end counts when the caller says so", func(c *call) { c.took = 7300; c.before = "90"; c.when = "end" }, 1, "tier-nightly is over its budget"},
		{"the forgiven words are the caller's", func(c *call) { c.took = 7300; c.load = 90 }, 0, "tier-nightly is over budget, load, not the code: load 90 at the end"},
		{"the window names the average that is read", func(c *call) { c.took = 7300; c.window = "7" }, 2, "--window"},
		{"a start after now is refused, naming the tier", func(c *call) { c.took = -30 }, 1, "tier-nightly: --since"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			c := base
			r.edit(&c)
			code, out, errb := run(c)
			if code != r.code || !strings.Contains(out+errb, r.stdio) {
				t.Fatalf("exit %d, want %d, and %q in\nstdout: %s\nstderr: %s", code, r.code, r.stdio, out, errb)
			}
		})
	}
}

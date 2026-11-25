package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The streams of testdata/streams are `go test -json` runs recorded with go 1.27.3, and each
// row's counts are the ones the reader of v0.4.0 gave for it, before testjson read the
// events. testjson's own totals differ on most of them: it counts a parent beside
// its subtests, a test once for each time it ran, and a test that never ended as a failure.
//
// What it costs when a row fails: every tier that ends on hatch tests is judged on other
// counts than before, and a gate moves with them.
func TestTheLeafCountsOfEachRecordedStream(t *testing.T) {
	rows := []struct {
		stream           string
		pass, fail, skip int
		skipped          []string
	}{
		{stream: "subtests", pass: 8},
		{stream: "parallel", pass: 6},
		{stream: "skip-in-a-leaf", pass: 2, skip: 2, skipped: []string{
			"example.com/fix/skipleaf.TestParent/skips", "example.com/fix/skipleaf.TestSkips",
		}},
		{stream: "skip-in-a-parent", pass: 2, skip: 4, skipped: []string{
			"example.com/fix/skipparent.TestAllSubtestsSkip/a", "example.com/fix/skipparent.TestAllSubtestsSkip/b",
			"example.com/fix/skipparent.TestSkipsAfter", "example.com/fix/skipparent.TestSkipsBefore",
		}},
		{stream: "every-test-skips", skip: 2, skipped: []string{"example.com/fix/allskip.TestA", "example.com/fix/allskip.TestB"}},
		{stream: "failure", pass: 2, fail: 2, skip: 1, skipped: []string{"example.com/fix/fail.TestSkips"}},
		// The parent of the subtest that panicked has no end event, and is no failure of its own.
		{stream: "panic", pass: 2, fail: 1},
		{stream: "build-failure"},
		{stream: "setup-failure", pass: 8},
		{stream: "no-test-files"},
		{stream: "no-tests-to-run"},
		// A TestMain that exits 1 fails its package and no test: go test's exit code says so.
		{stream: "testmain-exits-1", pass: 1},
		// go test's own -timeout, and the stream of a run ended at hatch's --timeout: the tests
		// still running have no end event and are not counted.
		{stream: "go-test-timeout", pass: 2},
		{stream: "killed-at-the-timeout", pass: 2},
		// -count=2: a test name counts once, by its last outcome.
		{stream: "count-2", pass: 10, skip: 2, skipped: []string{
			"example.com/fix/skipleaf.TestParent/skips", "example.com/fix/skipleaf.TestSkips",
		}},
		{stream: "packages-together", pass: 20, fail: 2, skip: 7, skipped: []string{
			"example.com/fix/fail.TestSkips",
			"example.com/fix/skipleaf.TestParent/skips", "example.com/fix/skipleaf.TestSkips",
			"example.com/fix/skipparent.TestAllSubtestsSkip/a", "example.com/fix/skipparent.TestAllSubtestsSkip/b",
			"example.com/fix/skipparent.TestSkipsAfter", "example.com/fix/skipparent.TestSkipsBefore",
		}},
		// Lines of 70,000 bytes in the tests: go test cuts each into events of about 1 KiB.
		{stream: "long-lines", pass: 2},
	}
	files, err := filepath.Glob(filepath.Join("testdata", "streams", "*.json"))
	if err != nil || len(files) != len(rows) {
		t.Fatalf("%d streams in testdata and %d rows (%v): each stream has one row", len(files), len(rows), err)
	}
	for _, row := range rows {
		b, err := os.ReadFile(filepath.Join("testdata", "streams", row.stream+".json"))
		if err != nil {
			t.Fatal(err)
		}
		s, err := Parse(bytes.NewReader(b))
		if err != nil {
			t.Errorf("%s: %v", row.stream, err)
			continue
		}
		if s.Pass != row.pass || s.Fail != row.fail || s.Skip != row.skip || !slices.Equal(s.Skipped, row.skipped) {
			t.Errorf("%s: got %d passed, %d failed, %d skipped %q, want %d, %d, %d %q",
				row.stream, s.Pass, s.Fail, s.Skip, s.Skipped, row.pass, row.fail, row.skip, row.skipped)
		}
	}
}

// Streams go test does not write, read as the reader of v0.4.0 read them.
func TestTheLeafCountsOfAStreamGoTestDoesNotWrite(t *testing.T) {
	for _, row := range []struct {
		name, stream     string
		pass, fail, skip int
	}{
		{name: "a line that is not JSON", stream: "ok and done\n" + ev("pass", "p", "TestA"), pass: 1},
		{name: "end events with no run event", stream: ev("skip", "p", "TestA/x") + ev("pass", "p", "TestA") + ev("fail", "p", "TestB"), fail: 1, skip: 1},
		{name: "a run event and no end", stream: ev("run", "p", "TestA") + ev("pass", "p", "TestB"), pass: 1},
		{name: "the same name ending twice", stream: ev("fail", "p", "TestA") + ev("pass", "p", "TestA"), pass: 1},
		{name: "no last newline", stream: ev("pass", "p", "TestA") + strings.TrimSuffix(ev("skip", "p", "TestB"), "\n"), pass: 1, skip: 1},
		{name: "an empty stream"},
	} {
		if s := parse(t, row.stream); s.Pass != row.pass || s.Fail != row.fail || s.Skip != row.skip {
			t.Errorf("%s: got %+v, want %d passed, %d failed, %d skipped", row.name, s, row.pass, row.fail, row.skip)
		}
	}
}

// A line that opens a JSON object and is no event is the error it was, word for word.
func TestParseBadEventKeepsItsWords(t *testing.T) {
	_, err := Parse(strings.NewReader(ev("pass", "p", "TestA") + "{not json}\n"))
	if want := "bad test event: invalid character 'n' looking for beginning of object key string"; err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// One line of the stream may be 64 KiB, the most testjson reads; the reader of v0.4.0 read
// 16 MiB. A longer line is an error, never a count of the part before it. go test writes no
// such line: the long-lines stream shows it cutting a test's output into short events.
func TestALineOverTestjsonsLimitIsAnErrorNeverACount(t *testing.T) {
	long := `{"Action":"output","Package":"p","Test":"TestA","Output":"` + strings.Repeat("x", 70_000) + `"}` + "\n"
	s, err := Parse(strings.NewReader(ev("pass", "p", "TestA") + long + ev("pass", "p", "TestB")))
	if err == nil || !strings.Contains(err.Error(), "token too long") || s.Pass+s.Fail+s.Skip != 0 {
		t.Fatalf("got %+v, %v, want an error that says the line is too long and no count", s, err)
	}
}

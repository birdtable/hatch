package tests

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// The live scan prints a line for each package event and nothing for a test event, another action or a
// line that is not JSON, and a line cut across two writes is read whole.
//
// What it costs when this fails: --stream prints a line a package never earned, drops one it did, or breaks a
// line in two.
func TestTheLiveScanPrintsOneLineForEachPackageEvent(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"example.test/a"}`,
		`{"Action":"run","Package":"example.test/a","Test":"TestA"}`,
		`{"Action":"pass","Package":"example.test/a","Test":"TestA"}`,
		`not json at all`,
		`{"Action":"output","Package":"example.test/a","Output":"ok\n"}`,
		`{"Action":"pass","Package":"example.test/a"}`,
		`{"Action":"fail","Package":"example.test/b"}`,
		`{"Action":"skip","Package":"example.test/c"}`,
		``,
	}, "\n")
	for _, piece := range []int{len(stream), 7, 1} {
		var out bytes.Buffer
		w, end := live(&out)
		for p := []byte(stream); len(p) > 0; {
			n := min(piece, len(p))
			if got, err := w.Write(p[:n]); got != n || err != nil {
				t.Fatalf("piece %d: Write = %d, %v", piece, got, err)
			}
			p = p[n:]
		}
		end()
		if want := "pass example.test/a\nfail example.test/b\nskip example.test/c\n"; out.String() != want {
			t.Errorf("pieces of %d: got %q, want %q", piece, out.String(), want)
		}
	}
}

// A final event with no newline after it is printed when the read is ended, and not before.
func TestTheLiveScanReadsALastLineWithNoNewlineAtItsEnd(t *testing.T) {
	out := &lockedBuffer{wrote: make(chan struct{}, 8)}
	w, end := live(out)
	_, _ = w.Write([]byte(`{"Action":"pass","Package":"example.test/a"}` + "\n" + `{"Action":"fail","Package":"example.test/b"}`))
	// The first line is printed while the stream is still open: the row waits for that write.
	// The second has no end yet, and a scan gives a line with no end only when its read ends.
	<-out.wrote
	if got := out.String(); got != "pass example.test/a\n" {
		t.Fatalf("before the end: %q", got)
	}
	end()
	if got := out.String(); got != "pass example.test/a\nfail example.test/b\n" {
		t.Errorf("got %q", got)
	}
}

// A scan that stops early never holds the run: the writer takes the rest of the stream, and
// the lines before the stop were printed.
//
// What it costs when this fails: a run under --stream hangs on a line testjson cannot read.
func TestTheLiveScanThatStopsStillTakesTheStream(t *testing.T) {
	var out bytes.Buffer
	w, end := live(&out)
	stream := `{"Action":"pass","Package":"example.test/a"}` + "\n" + strings.Repeat("x", 1<<17) + "\n" + `{"Action":"pass","Package":"example.test/b"}` + "\n"
	if n, err := w.Write([]byte(stream)); n != len(stream) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	end()
	if out.String() != "pass example.test/a\n" {
		t.Errorf("got %q", out.String())
	}
}

// lockedBuffer is a buffer the scan writes and the test reads while the scan still runs.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
	// wrote is sent to after each write.
	wrote chan struct{}
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer func() { l.wrote <- struct{}{} }()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// --stream prints a line for the package before the verdict line, and the verdict line and the exit
// code are what they are without it; a red run still prints the stream.
//
// What it costs when this fails: opting in changes a verdict, or prints no line while the run goes.
func TestRunStreamPrintsThePackageLineBeforeTheVerdict(t *testing.T) {
	module(t, "func TestA(t *testing.T) {}\n")
	code, out, errs := run("--stream", "--timeout", "2m", "./...")
	want := "pass example.test/m\nhatch tests: 1 passed, 0 failed, 0 skipped: green\n"
	if code != 0 || out != want {
		t.Fatalf("code %d, out %q, err %q, want %q", code, out, errs, want)
	}
	module(t, "func TestA(t *testing.T) { t.Fatal(\"boom\") }\n")
	code, out, errs = run("--stream", "--timeout", "2m", "./...")
	if code != 1 || !strings.HasPrefix(out, "fail example.test/m\n") || !strings.Contains(out, `"Action":"fail"`) ||
		!strings.HasSuffix(out, "red, tests failed\n") {
		t.Fatalf("red: code %d, out %q, err %q", code, out, errs)
	}
}

// Without --stream no package line is printed, and --stream takes no value.
func TestRunStreamIsOptInAndTakesNoValue(t *testing.T) {
	module(t, "func TestA(t *testing.T) {}\n")
	if _, out, _ := run("--timeout", "2m", "./..."); strings.Contains(out, "pass example.test/m") {
		t.Errorf("a package line without --stream: %q", out)
	}
	if code, out, errs := run("--stream=yes", "--timeout", "2m", "./..."); code != 2 || out != "" || !strings.Contains(errs, "--stream takes no value") {
		t.Errorf("--stream=yes: code %d, out %q, err %q", code, out, errs)
	}
}

package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/birdtable/hatch/internal/tests"
)

// The usage text carries the exit code of a run with no go on PATH.
func TestUsageWritesTheNoGoExitCode(t *testing.T) {
	want := strconv.Itoa(tests.ExitNoGo) + "  go not found on PATH"
	if !strings.Contains(usage, want) {
		t.Fatalf("usage lacks %q:\n%s", want, usage)
	}
}

func TestATestsRunWithNoGoExitsWithItsOwnCode(t *testing.T) {
	t.Setenv("PATH", "")
	if code := run([]string{"tests", "--timeout", "1m", "--red-when", "failed", "--green-word", "g", "--red-word", "r", "./..."}); code != tests.ExitNoGo {
		t.Fatalf("exit %d, want %d", code, tests.ExitNoGo)
	}
}

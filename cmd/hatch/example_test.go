package main

import (
	"os"
	"strings"
	"testing"
)

// The README's first quick start command, run as written: the packages of
// this module, without the ones under internal.
func Example() {
	run([]string{"tests", "packages", "github.com/birdtable/hatch/internal/..."})
	// Output:
	// github.com/birdtable/hatch/cmd/hatch
}

// The README shows the command the example runs.
//
// What it costs when this fails: the quick start in the README is a command
// that nothing runs.
func TestTheReadmeQuickStartIsTheExample(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "hatch tests packages 'github.com/birdtable/hatch/internal/...'"
	if !strings.Contains(string(b), want) {
		t.Errorf("README.md lacks the example's command:\n%s", want)
	}
	if !strings.Contains(string(b), "github.com/birdtable/hatch/cmd/hatch\n```") {
		t.Errorf("README.md lacks the example's output")
	}
}

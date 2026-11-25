package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ev(action, pkg, test string) string {
	s := `{"Action":"` + action + `","Package":"` + pkg + `"`
	if test != "" {
		s += `,"Test":"` + test + `"`
	}
	return s + "}\n"
}

func parse(t *testing.T, stream string) Summary {
	t.Helper()
	s, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	houseRule  = Rule{Failed: true, NoneRan: true, AllSkipped: true}
	houseWords = Words{Green: "green", Red: "red"}
)

func TestJudge(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		failed bool
		green  bool
		want   string
	}{
		{"passes", ev("pass", "p", "TestA") + ev("pass", "p", ""), false, true, "green"},
		{"some skip", ev("pass", "p", "TestA") + ev("skip", "p", "TestB"), false, true, "green"},
		{"all skip", ev("skip", "p", "TestA") + ev("skip", "p", "TestB"), false, false, "every test skipped"},
		{"zero tests", ev("pass", "p", ""), false, false, "no tests ran"},
		{"empty", "", false, false, "no tests ran"},
		{"failure", ev("pass", "p", "TestA") + ev("fail", "p", "TestB"), true, false, "tests failed"},
		{"exit nonzero", ev("pass", "p", "TestA"), true, false, "tests failed"},
		{
			"parent passes over skipped subtests",
			ev("skip", "p", "TestA/x") + ev("skip", "p", "TestA/y") + ev("pass", "p", "TestA"),
			false, false, "every test skipped",
		},
		{
			"parent with one running subtest",
			ev("pass", "p", "TestA/x") + ev("skip", "p", "TestA/y") + ev("pass", "p", "TestA"),
			false, true, "green",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, ok := Judge(parse(t, c.stream), c.failed, houseRule, houseWords)
			if ok != c.green || !strings.HasSuffix(line, c.want) {
				t.Fatalf("got %q green=%v, want %q green=%v", line, ok, c.want, c.green)
			}
		})
	}
}

// A sibling that sorts between a parent and its child must not hide the child.
func TestParentFoundByAncestry(t *testing.T) {
	for _, sib := range []string{"TestA#01", "TestA-1", "TestA.v2", "TestA "} {
		t.Run(sib, func(t *testing.T) {
			stream := ev("skip", "p", "TestA/x") + ev("skip", "p", "TestA/y") + ev("pass", "p", "TestA") + ev("skip", "p", sib)
			s := parse(t, stream)
			if s.Pass != 0 || s.Skip != 3 {
				t.Fatalf("got %+v", s)
			}
			if line, ok := Judge(s, false, houseRule, houseWords); ok {
				t.Fatalf("every test skipped is red, got %q", line)
			}
		})
	}
}

func TestParseIgnoresNonJSON(t *testing.T) {
	s := parse(t, "FAIL\tbuild failed\n"+ev("pass", "p", "TestA"))
	if s.Pass != 1 {
		t.Fatalf("got %+v", s)
	}
}

func TestParseBadEvent(t *testing.T) {
	if _, err := Parse(strings.NewReader("{not json}\n")); err == nil {
		t.Fatal("want an error")
	}
}

func TestSamePackageNamesDoNotCollide(t *testing.T) {
	s := parse(t, ev("pass", "p1", "TestA")+ev("skip", "p2", "TestA"))
	if s.Pass != 1 || s.Skip != 1 {
		t.Fatalf("got %+v", s)
	}
}

// With no go on PATH the run says so and exits with its own code, not the
// parse error's 2.
func TestNoGoOnPath(t *testing.T) {
	t.Setenv("PATH", "")
	var out, errb strings.Builder
	code := Run(context.Background(), []string{"--timeout", "1m", "--red-when", "failed", "--green-word", "g", "--red-word", "r", "./..."}, &out, &errb)
	if code != ExitNoGo || ExitNoGo == 1 || ExitNoGo == 2 {
		t.Fatalf("exit %d, want %d, a code of its own", code, ExitNoGo)
	}
	if got := errb.String(); got != "hatch tests: go not found on PATH\n" {
		t.Fatalf("stderr %q", got)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout %q", out.String())
	}
}

// The same counts are green or red by the rule alone: a count the rule does not name never makes a run red.
//
// What it costs when this fails: a program is held to a count it did not choose.
func TestJudgeTheRuleAloneDecides(t *testing.T) {
	failing := parse(t, ev("pass", "p", "TestA")+ev("fail", "p", "TestB"))
	skipped := parse(t, ev("skip", "p", "TestA"))
	none := parse(t, "")
	for _, c := range []struct {
		name   string
		s      Summary
		failed bool
		rule   Rule
		green  bool
	}{
		{"a failure, rule names failed", failing, true, Rule{Failed: true}, false},
		{"a failure, rule leaves it out", failing, true, Rule{NoneRan: true, AllSkipped: true}, true},
		{"go test exits non-zero, rule leaves failed out", failing, true, Rule{AllSkipped: true}, true},
		{"all skipped, rule names it", skipped, false, Rule{AllSkipped: true}, false},
		{"all skipped, rule leaves it out", skipped, false, Rule{Failed: true, NoneRan: true}, true},
		{"none ran, rule names it", none, false, Rule{NoneRan: true}, false},
		{"none ran, rule leaves it out", none, false, Rule{Failed: true, AllSkipped: true}, true},
	} {
		if _, ok := Judge(c.s, c.failed, c.rule, houseWords); ok != c.green {
			t.Errorf("%s: green %v, want %v", c.name, ok, c.green)
		}
	}
}

func TestParseRule(t *testing.T) {
	r, err := ParseRule("failed, all-skipped")
	if err != nil || r != (Rule{Failed: true, AllSkipped: true}) {
		t.Fatalf("got %+v, %v", r, err)
	}
	for _, bad := range []string{"", "failed,", "nope", "failed,failed"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A red line for any-skipped names the first ten skipped tests and counts the rest.
//
// What it costs when this fails: a reader of a red line cannot tell which tests skipped, or a run with hundreds of
// skips prints a line of hundreds of names.
func TestJudgeAnySkippedNamesTen(t *testing.T) {
	var names []string
	for i := range 12 {
		names = append(names, fmt.Sprintf("p.T%02d", i))
	}
	s := Summary{Pass: 3, Skip: 12, Skipped: names}
	line, ok := Judge(s, false, Rule{AnySkipped: true}, houseWords)
	if ok || !strings.Contains(line, "12 skipped: p.T00, p.T01") || !strings.Contains(line, "p.T09, and 2 more") || strings.Contains(line, "p.T10") {
		t.Errorf("line %q, green %v", line, ok)
	}
	if _, ok := Judge(Summary{Pass: 1}, false, Rule{AnySkipped: true}, houseWords); !ok {
		t.Error("a run with no skip was red under any-skipped")
	}
	if _, ok := Judge(s, false, Rule{Failed: true}, houseWords); !ok {
		t.Error("a skip was red though the rule leaves any-skipped out")
	}
	if r, err := ParseRule("failed,any-skipped"); err != nil || r != (Rule{Failed: true, AnySkipped: true}) {
		t.Errorf("ParseRule: %v, %v", r, err)
	}
}

// A parent that calls t.Skip after its subtests passed is a skip of its own:
// --red-when any-skipped names it.
func TestParentOwnSkipCounts(t *testing.T) {
	stream := ev("pass", "p", "TestA/x") + ev("pass", "p", "TestA/y") + ev("skip", "p", "TestA") + ev("pass", "p", "TestB")
	s := parse(t, stream)
	if s.Pass != 3 || s.Skip != 1 || len(s.Skipped) != 1 || s.Skipped[0] != "p.TestA" {
		t.Fatalf("got %+v", s)
	}
	rule := Rule{Failed: true, NoneRan: true, AnySkipped: true}
	line, ok := Judge(s, false, rule, houseWords)
	if ok || !strings.HasSuffix(line, "1 skipped: p.TestA") {
		t.Fatalf("a parent's own skip is red under any-skipped, got %q green=%v", line, ok)
	}
	// A parent that passes over passing subtests is still not counted.
	if s := parse(t, ev("pass", "p", "TestA/x")+ev("pass", "p", "TestA")); s.Pass != 1 || s.Skip != 0 {
		t.Fatalf("got %+v", s)
	}
}

// go-proc v0.5.0 refuses a Grace above zero on Windows, so hatch gives none there and keeps its 10 s elsewhere.
func TestGraceFor(t *testing.T) {
	for _, c := range []struct {
		goos string
		want time.Duration
	}{
		{"linux", 10 * time.Second},
		{"darwin", 10 * time.Second},
		{"freebsd", 10 * time.Second},
		{"windows", 0},
	} {
		t.Run(c.goos, func(t *testing.T) {
			if got := graceFor(c.goos); got != c.want {
				t.Errorf("graceFor(%q) = %v, want %v", c.goos, got, c.want)
			}
		})
	}
}

// go-proc v0.5.0 takes Drain as given and refuses zero on Windows.
func TestDrainIsSet(t *testing.T) {
	if drain <= 0 {
		t.Errorf("drain = %v, want above zero", drain)
	}
}

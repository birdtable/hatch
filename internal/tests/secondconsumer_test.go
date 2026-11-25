package tests

import (
	"strings"
	"testing"
)

// A second consumer: a project whose packages and test names are nothing like
// any one project's. hatch judges the stream it is given, so each row changes
// what the project ran, or the rule and the two words it gives, and the verdict
// follows it. A row that fails means hatch holds a name, a count, a rule or a
// word of its own.
var (
	shopRule  = Rule{Failed: true, NoneRan: true, AllSkipped: true}
	shopWords = Words{Green: "ship", Red: "hold"}
)

func TestSecondConsumer(t *testing.T) {
	rows := []struct {
		name   string
		stream string
		failed bool
		rule   Rule
		words  Words
		want   string
	}{
		{"one pass in a package of its own", ev("pass", "shop/cart", "TestTotals") + ev("pass", "shop/cart", ""), false, shopRule, shopWords, "1 passed, 0 failed, 0 skipped: ship"},
		{"two packages, one skip", ev("pass", "shop/cart", "TestTotals") + ev("skip", "shop/pay", "TestRefund"), false, shopRule, shopWords, "1 passed, 0 failed, 1 skipped: ship"},
		{"every test of the project skips", ev("skip", "shop/cart", "TestTotals") + ev("skip", "shop/pay", "TestRefund"), false, shopRule, shopWords, "hold, every test skipped"},
		{"the project ran none", ev("pass", "shop/cart", ""), false, shopRule, shopWords, "hold, no tests ran"},
		{"a failing test of the project", ev("pass", "shop/cart", "TestTotals") + ev("fail", "shop/pay", "TestRefund"), true, shopRule, shopWords, "1 passed, 1 failed, 0 skipped: hold, tests failed"},
		// The same counts, judged by another rule: the project reds only a failure, so a run of skips ships.
		{"the same skips, a rule that names only failed", ev("skip", "shop/cart", "TestTotals") + ev("skip", "shop/pay", "TestRefund"), false, Rule{Failed: true}, shopWords, "0 passed, 0 failed, 2 skipped: ship"},
		{"the same failure, a rule that does not name it", ev("pass", "shop/cart", "TestTotals") + ev("fail", "shop/pay", "TestRefund"), true, Rule{NoneRan: true}, shopWords, "1 passed, 1 failed, 0 skipped: ship"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			line, _ := Judge(parse(t, r.stream), r.failed, r.rule, r.words)
			if !strings.Contains(line, r.want) {
				t.Fatalf("verdict %q lacks %q", line, r.want)
			}
		})
	}
}

package runs

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Unseen is the program word of the one row that names the packages that may
// start a program chosen where the check cannot see.
const Unseen = "unseen"

// Rules is the caller's rows: for each program, the packages that may start
// it. Unseen holds the packages that may start a program chosen where the
// check cannot see. A package is the folder, with / between the parts, "." for
// the module's root.
type Rules struct {
	Programs map[string][]string
	Unseen   []string
}

// ReadRules parses a rules file: one row a line, `<program> <package>...`,
// blank lines and lines that start with # passed over. A program with two
// rows, a row with no package and a package twice on a row are errors, so a
// file never reads two ways.
func ReadRules(data []byte) (*Rules, error) {
	r := &Rules{Programs: map[string][]string{}}
	seen := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return nil, fmt.Errorf("rules line %d: a row is a program and at least one package", i+1)
		}
		prog, pkgs := f[0], f[1:]
		if seen[prog] {
			return nil, fmt.Errorf("rules line %d: a second row for %q", i+1, prog)
		}
		seen[prog] = true
		dup := map[string]bool{}
		for _, p := range pkgs {
			if dup[p] {
				return nil, fmt.Errorf("rules line %d: package %q twice on a row", i+1, p)
			}
			dup[p] = true
		}
		if prog == Unseen {
			r.Unseen = pkgs
			continue
		}
		r.Programs[prog] = pkgs
	}
	if len(seen) == 0 {
		return nil, errors.New("the rules file holds no row")
	}
	return r, nil
}

// Judge is every finding of the rules against what the tree runs: runs is
// each package's programs and unseen the sites whose program the check
// cannot see, as Runs gives them. The findings are in a fixed order.
func (r *Rules) Judge(runs map[string][]string, unseen []Finding) []Finding {
	var out []Finding
	used := map[string]map[string]bool{} // program -> package -> started
	for _, pkg := range sortedKeys(runs) {
		for _, prog := range runs[pkg] {
			if used[prog] == nil {
				used[prog] = map[string]bool{}
			}
			used[prog][pkg] = true
			allowed, ok := r.Programs[prog]
			switch {
			case !ok:
				out = append(out, Finding{What: fmt.Sprintf("package %s starts %s, which has no row", pkg, prog)})
			case !has(allowed, pkg):
				out = append(out, Finding{What: fmt.Sprintf("package %s starts %s, which only %s may start", pkg, prog, strings.Join(allowed, ", "))})
			}
		}
	}
	unseenUsed := map[string]bool{}
	for _, f := range unseen {
		pkg := packageOf(f.File)
		unseenUsed[pkg] = true
		if !has(r.Unseen, pkg) {
			out = append(out, Finding{File: f.File, Line: f.Line,
				What: "a program chosen where the check cannot see it, in package " + pkg + ", which is not on the unseen row"})
		}
	}
	for _, prog := range sortedKeys(r.Programs) {
		for _, pkg := range r.Programs[prog] {
			if !used[prog][pkg] {
				out = append(out, Finding{What: fmt.Sprintf("the row for %s names package %s, which does not start it", prog, pkg)})
			}
		}
	}
	for _, pkg := range r.Unseen {
		if !unseenUsed[pkg] {
			out = append(out, Finding{What: "the unseen row names package " + pkg + ", which has no such site"})
		}
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Sorted is the findings in a fixed order, for output: by file, line, text.
func Sorted(fs []Finding) []Finding {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.What < b.What
	})
	return fs
}
